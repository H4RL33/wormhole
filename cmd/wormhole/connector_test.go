package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

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
func (d *fakeConnectorDeps) Transaction(_ context.Context, adapter connector.AdapterName, action string) error {
	d.transactionCalls++
	d.lastAction = action
	if action == "install" {
		d.entry = connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
	} else {
		d.entry = connector.ConnectorEntry{State: connector.EntryAbsent}
	}
	return nil
}
