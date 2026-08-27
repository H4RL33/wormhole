package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/config/connector"
)

func TestConnectorListOutputIsBoundedPublicState(t *testing.T) {
	var stdout bytes.Buffer
	deps := fakeConnectorCommandDependencies()
	code := runConnector(context.Background(), []string{"list", "codex"}, strings.NewReader(""), &stdout, io.Discard, deps)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	output := stdout.String()
	for _, want := range []string{"codex", "available", "present", "user", "stdio"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output %q missing %q", output, want)
		}
	}
	for _, forbidden := range []string{"TOKEN=", "/private/", "connector-backup", "secret-value"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("output %q contains %q", output, forbidden)
		}
	}
}

func TestConnectorInstallAndRemoveUseTransactionalBoundaryAndOneConfirmation(t *testing.T) {
	for _, action := range []string{"install", "remove"} {
		t.Run(action, func(t *testing.T) {
			deps := fakeConnectorCommandDependencies()
			var stdout bytes.Buffer
			code := runConnector(context.Background(), []string{action, "codex"}, strings.NewReader("yes\n"), &stdout, io.Discard, deps)
			if code != 0 {
				t.Fatalf("code = %d", code)
			}
			if strings.Count(stdout.String(), "Apply connector change? [y/N]") != 1 {
				t.Fatalf("confirmation output = %q", stdout.String())
			}
			for _, field := range []string{"prior sha256:", "desired sha256:", "plan sha256:"} {
				if !strings.Contains(stdout.String(), field) {
					t.Fatalf("confirmed output %q missing %q", stdout.String(), field)
				}
			}
			if deps.transactionCalls != 1 || deps.lastAction != action {
				t.Fatalf("transaction calls = %d action = %q", deps.transactionCalls, deps.lastAction)
			}
		})
	}
}

func TestConnectorYesStillRendersCompletePlanWithoutPrompt(t *testing.T) {
	deps := fakeConnectorCommandDependencies()
	var stdout bytes.Buffer
	if code := runConnector(t.Context(), []string{"install", "codex", "--yes"}, strings.NewReader(""), &stdout, io.Discard, deps); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), "codex connector plan: install wormhole") || strings.Contains(stdout.String(), "[y/N]") {
		t.Fatalf("--yes output = %q", stdout.String())
	}
}

func TestProductionConnectorWiringPropagatesClaudeSecurityBoundary(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprintf("installed=%v", installed), func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if installed {
				claude := filepath.Join(bin, "claude")
				if err := os.WriteFile(claude, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "override"))
			commands, err := newProductionConnectorCommands()
			if !installed {
				if err != nil || commands == nil {
					t.Fatalf("uninstalled Claude constructor: commands=%T err=%v", commands, err)
				}
				return
			}
			if !errors.Is(err, connector.ErrUnsupportedConnectorEntry) {
				t.Fatalf("Claude constructor error = %v", err)
			}
		})
	}
}

func TestProductionConnectorWiringPropagatesNativeExecutableErrors(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	priorDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(priorDirectory) })
	t.Setenv("PATH", "bin")
	priorConfig, hadConfig := os.LookupEnv("CLAUDE_CONFIG_DIR")
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadConfig {
			_ = os.Setenv("CLAUDE_CONFIG_DIR", priorConfig)
		} else {
			_ = os.Unsetenv("CLAUDE_CONFIG_DIR")
		}
	})
	if commands, err := newProductionConnectorCommands(); err == nil || commands != nil {
		t.Fatalf("unsafe native executable was treated as unavailable: commands=%T err=%v", commands, err)
	}
}

func TestConnectorClosedHelpAdvertisesOnlyApplicableFlags(t *testing.T) {
	for _, action := range []string{"install", "remove"} {
		var stdout, stderr bytes.Buffer
		if code := runConnector(t.Context(), []string{action, "--help"}, strings.NewReader(""), &stdout, &stderr, fakeConnectorCommandDependencies()); code != 0 {
			t.Fatalf("%s --help code=%d stderr=%q", action, code, stderr.String())
		}
		if !strings.Contains(stdout.String()+stderr.String(), "-yes") {
			t.Fatalf("%s help omitted --yes: %q %q", action, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runConnector(t.Context(), []string{"list", "--help"}, strings.NewReader(""), &stdout, &stderr, fakeConnectorCommandDependencies()); code != 0 || strings.Contains(stdout.String()+stderr.String(), "-yes") {
		t.Fatalf("list help code=%d output=%q", code, stdout.String()+stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runConnector(t.Context(), []string{"install", "--unknown", "codex"}, strings.NewReader(""), &stdout, &stderr, fakeConnectorCommandDependencies()); code != 2 {
		t.Fatalf("unknown flag code=%d", code)
	}
}

func TestStandaloneConnectorPassesPreconfirmedChangeToTransactionCAS(t *testing.T) {
	store, err := connector.OpenStoreAt(filepath.Join(t.TempDir(), "connectors"))
	if err != nil {
		t.Fatal(err)
	}
	prior := connector.ConnectorEntry{State: connector.EntryAbsent}
	desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
	adapter := &setupStateAdapter{name: connector.AdapterCodex, current: prior, version: "0.149.0"}
	commands := &productionConnectorCommands{store: store, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: adapter}}
	frozenDesired, change, err := commands.Plan(t.Context(), connector.AdapterCodex, "install", prior)
	if err != nil {
		t.Fatal(err)
	}
	adapter.current = connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/third/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
	if err := commands.Transaction(t.Context(), connector.AdapterCodex, frozenDesired, change); !errors.Is(err, runtimeconfig.ErrConfirmedPlanDrift) {
		t.Fatalf("raced transaction error = %v", err)
	}
	if adapter.applyCalls != 0 {
		t.Fatalf("raced transaction mutated adapter %d times", adapter.applyCalls)
	}
}

type fakeConnectorDeps struct {
	entry            connector.ConnectorEntry
	transactionCalls int
	lastAction       string
}

func fakeConnectorCommandDependencies() *fakeConnectorDeps {
	return &fakeConnectorDeps{entry: connector.ConnectorEntry{
		State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio,
		Command: "/private/wormhole", Args: []string{"mcp"},
		Env: []connector.EnvironmentVariable{{Name: "TOKEN", Value: "secret-value"}},
	}}
}

func (d *fakeConnectorDeps) Inspect(context.Context, connector.AdapterName) (connector.Availability, connector.ConnectorEntry, error) {
	return connector.Availability{Available: true, Version: "0.149.0"}, d.entry, nil
}
func (d *fakeConnectorDeps) Plan(_ context.Context, adapter connector.AdapterName, action string, prior connector.ConnectorEntry) (connector.ConnectorEntry, connector.ConfirmedConnectorChange, error) {
	desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
	operation := connector.OperationInstall
	if action == "remove" {
		desired = connector.ConnectorEntry{State: connector.EntryAbsent}
		operation = connector.OperationRemove
	}
	plan, err := connector.BuildChangePlan(adapter, "wormhole", operation, prior, desired)
	if err != nil {
		return connector.ConnectorEntry{}, connector.ConfirmedConnectorChange{}, err
	}
	priorDigest, _ := connector.DigestConnectorEntry(prior)
	desiredDigest, _ := connector.DigestConnectorEntry(desired)
	return desired, connector.ConfirmedConnectorChange{Adapter: adapter, Name: "wormhole", Action: operation, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}, nil
}
func (d *fakeConnectorDeps) Transaction(_ context.Context, adapter connector.AdapterName, desired connector.ConnectorEntry, change connector.ConfirmedConnectorChange) error {
	d.transactionCalls++
	d.lastAction = string(change.Action)
	d.entry = desired
	return nil
}
