package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/config/connector"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var setupTestGitExecutable, _ = exec.LookPath("git")

func TestSetupProvesGatewayStateAbsentBeforeFreezingUnavailablePlan(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", data)
	if !gatewayStateKnownAbsent() {
		t.Fatal("empty private data root was not proven absent")
	}
	root := filepath.Join(data, "wormhole")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "wormholed.db"), []byte("existing authority"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gatewayStateKnownAbsent() {
		t.Fatal("existing Gateway authority was treated as absent")
	}
}

func TestProductionSetupReadyGatewayRejectsNonAuthoritativeVerify(t *testing.T) {
	if setupTestGitExecutable == "" {
		t.Fatal("git test executable is unavailable")
	}
	root := setupPlanRepository(t)
	t.Setenv("PATH", filepath.Dir(setupTestGitExecutable))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "absent-data-root"))
	gateway := &readySetupGateway{verifyErr: localapi.ErrPrivateSetupRequest}
	driver := &productionSetupDriver{
		root: root, runner: runtimeconfig.NewCommandRunner(), gateway: gateway,
		connectors: &productionConnectorCommands{
			adapters: map[connector.AdapterName]connector.Adapter{}, unavailable: map[connector.AdapterName]bool{connector.AdapterCodex: true, connector.AdapterClaude: true},
			desired: connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}},
		},
	}
	_, err := driver.Plan(t.Context(), setupOptions{publication: string(types.PublicationUnclassified), name: "Alice Example"}, nil)
	if !errors.Is(err, runtimeconfig.ErrConfirmedPlanDrift) {
		t.Fatalf("ready Gateway non-authoritative verify error = %v", err)
	}
	if gateway.verifyCalls != 1 {
		t.Fatalf("verify calls = %d", gateway.verifyCalls)
	}
}

type readySetupGateway struct {
	verifyErr   error
	verifyCalls int
}

func (*readySetupGateway) Ready(context.Context) error { return nil }
func (*readySetupGateway) Register(context.Context, localapi.SetupWorkspaceRequest) (localapi.SetupWorkspaceReadback, error) {
	return localapi.SetupWorkspaceReadback{}, errors.New("unexpected register")
}
func (*readySetupGateway) EnsureIdentity(context.Context, localapi.SetupIdentityRequest) (localapi.SetupIdentityReadback, error) {
	return localapi.SetupIdentityReadback{}, errors.New("unexpected identity")
}
func (*readySetupGateway) Classify(context.Context, localapi.SetupPublicationRequest) (localapi.SetupPublicationReadback, error) {
	return localapi.SetupPublicationReadback{}, errors.New("unexpected publication")
}
func (*readySetupGateway) Import(context.Context, localapi.SetupImportRequest) (localapi.SetupImportReadback, error) {
	return localapi.SetupImportReadback{}, errors.New("unexpected import")
}
func (g *readySetupGateway) Verify(context.Context, localapi.SetupWorkingDirectoryRequest) (localapi.SetupVerifyReadback, error) {
	g.verifyCalls++
	return localapi.SetupVerifyReadback{}, g.verifyErr
}

func setupPlanRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	projectID := "00000000-0000-4000-8000-000000000055"
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	snapshot := state.Snapshot{
		Config:  state.ConfigV1{SnapshotVersion: 1, ProjectID: projectID, Handle: types.ProjectHandle{Namespace: "acme", Name: "wormhole"}},
		Project: state.ProjectV1{SchemaVersion: 1, Kind: "project", ID: projectID, Name: "Wormhole", Aliases: []string{}, CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{}},
		Actors:  map[string]state.Record[state.ActorV1]{}, Tasks: map[string]state.Record[state.TaskV1]{}, TaskLinks: map[string]state.Record[state.TaskLinkV1]{},
		Articles: map[string]state.KBRecord{}, Channels: map[string]state.Record[state.ChannelV1]{}, Events: map[string]state.EventV1{}, GitLinks: map[string]state.Record[state.GitLinkV1]{},
	}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range tree {
		path := filepath.Join(root, ".wormhole", filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.test"}, {"add", ".wormhole"}, {"commit", "-q", "-m", "snapshot"}} {
		command := exec.Command(setupTestGitExecutable, append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func TestSetupRendersAndConfirmsOneCompletePlanBeforeEffects(t *testing.T) {
	events := []string{}
	journal := newFakeSetupJournal(&events)
	driver := newFakeSetupDriver(&events)
	var stdout bytes.Buffer
	code := runSetup(context.Background(), []string{"--publication=local_only"}, strings.NewReader("yes\n"), &stdout, io.Discard, setupDependencies{
		journal: journal,
		driver:  driver,
	})
	if code != 0 {
		t.Fatalf("runSetup code = %d", code)
	}
	if got := strings.Count(stdout.String(), "Wormhole setup plan\n"); got != 1 {
		t.Fatalf("plan render count = %d, output %q", got, stdout.String())
	}
	if got := strings.Count(stdout.String(), "Apply this complete plan? [y/N]"); got != 1 {
		t.Fatalf("confirmation count = %d, output %q", got, stdout.String())
	}
	for _, change := range testSetupSelection("local_only").Changes {
		want := fmt.Sprintf("  change %s %s %s: %s -> %s", change.Stage, change.Subject, change.Action, change.PriorDigest, change.DesiredDigest)
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("rendered plan omitted confirmed change %q: %s", want, stdout.String())
		}
	}
	selection := eventIndex(events, "selection")
	if selection < 0 {
		t.Fatalf("selection was not durably recorded: %v", events)
	}
	for _, stage := range orderedCLISetupStages {
		index := eventIndex(events, "effect:"+string(stage))
		if index <= selection {
			t.Fatalf("stage %s occurred before durable selection: %v", stage, events)
		}
	}
	if !reflect.DeepEqual(journal.value.CompletedStages, orderedCLISetupStages) || journal.value.State != runtimeconfig.SetupJournalCompleted {
		t.Fatalf("journal = %+v", journal.value)
	}
}

func TestSetupResumeAfterEveryEffectUsesFrozenSelectionWithoutReprompt(t *testing.T) {
	for _, crashStage := range orderedCLISetupStages {
		t.Run(string(crashStage), func(t *testing.T) {
			events := []string{}
			journal := newFakeSetupJournal(&events)
			driver := newFakeSetupDriver(&events)
			driver.crashAfter[crashStage] = true
			if code := runSetup(context.Background(), []string{"--publication=local_only"}, strings.NewReader("yes\n"), io.Discard, io.Discard, setupDependencies{journal: journal, driver: driver}); code == 0 {
				t.Fatal("injected crash unexpectedly succeeded")
			}
			if !driver.desired[crashStage] {
				t.Fatalf("stage %s did not reach its desired state", crashStage)
			}
			driver.crashAfter[crashStage] = false
			beforeSelections := journal.selectionWrites
			beforeRenders := driver.renderCount
			if code := runSetup(context.Background(), nil, strings.NewReader("no\n"), io.Discard, io.Discard, setupDependencies{journal: journal, driver: driver}); code != 0 {
				t.Fatalf("resume code = %d, events %v", code, events)
			}
			if journal.selectionWrites != beforeSelections || driver.renderCount != beforeRenders {
				t.Fatalf("resume replanned or rewrote selection: selection writes %d -> %d, renders %d -> %d", beforeSelections, journal.selectionWrites, beforeRenders, driver.renderCount)
			}
			if got := driver.effectCount[crashStage]; got != 1 {
				t.Fatalf("stage %s effect count = %d, want one", crashStage, got)
			}
			if journal.value.State != runtimeconfig.SetupJournalCompleted {
				t.Fatalf("journal state = %s", journal.value.State)
			}
		})
	}
}

func TestSetupDriftPreservesJournalAndPerformsNoWrite(t *testing.T) {
	events := []string{}
	journal := newFakeSetupJournal(&events)
	driver := newFakeSetupDriver(&events)
	driver.driftAt = runtimeconfig.StagePublicationClassified
	if code := runSetup(context.Background(), []string{"--publication=local_only", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, setupDependencies{journal: journal, driver: driver}); code == 0 {
		t.Fatal("drift unexpectedly succeeded")
	}
	before := journal.snapshot()
	writes := journal.writeCount
	effects := cloneStageCounts(driver.effectCount)
	if code := runSetup(context.Background(), nil, strings.NewReader("yes\n"), io.Discard, io.Discard, setupDependencies{journal: journal, driver: driver}); code == 0 {
		t.Fatal("resume drift unexpectedly succeeded")
	}
	if journal.writeCount != writes || !bytes.Equal(before, journal.snapshot()) {
		t.Fatalf("drift changed journal: writes %d -> %d", writes, journal.writeCount)
	}
	if !reflect.DeepEqual(effects, driver.effectCount) {
		t.Fatalf("drift performed an effect: before %v after %v", effects, driver.effectCount)
	}
}

func TestSetupConnectorFailurePreservesImportedWorkspaceAndRestoresPrior(t *testing.T) {
	events := []string{}
	journal := newFakeSetupJournal(&events)
	driver := newFakeSetupDriver(&events)
	driver.connectorFailure = true
	if code := runSetup(context.Background(), []string{"--publication=local_only", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, setupDependencies{journal: journal, driver: driver}); code == 0 {
		t.Fatal("connector failure unexpectedly succeeded")
	}
	if !driver.desired[runtimeconfig.StageBaseImported] {
		t.Fatal("imported workspace was not retained")
	}
	if !driver.connectorRestored {
		t.Fatal("connector prior was not restored")
	}
	if journal.value.CompletedStages[len(journal.value.CompletedStages)-1] != runtimeconfig.StageBaseImported {
		t.Fatalf("completed stages = %v", journal.value.CompletedStages)
	}
	if journal.value.LastError == nil || journal.value.LastError.Stage != runtimeconfig.StageConnectorsApplied {
		t.Fatalf("last error = %+v", journal.value.LastError)
	}
}

func TestProductionSetupConnectorFailureCompensatesFreshProcessCompletion(t *testing.T) {
	store, err := connector.OpenStoreAt(filepath.Join(t.TempDir(), "connectors"))
	if err != nil {
		t.Fatal(err)
	}
	desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
	absent := connector.ConnectorEntry{State: connector.EntryAbsent}
	codex := &setupStateAdapter{name: connector.AdapterCodex, current: absent, version: "0.149.0"}
	claude := &setupStateAdapter{name: connector.AdapterClaude, current: absent, version: "2.1.220", applyErr: errors.New("claude install failed")}
	selection := runtimeconfig.SetupSelection{ConnectorAdapters: []string{"codex", "claude"}, PlanDigest: runtimeconfig.SHA256StateDigest([]byte("setup-attempt"))}
	owner := setupConnectorOwner("00000000-0000-4000-8000-000000000099", selection.PlanDigest)
	for _, adapter := range []*setupStateAdapter{codex, claude} {
		plan, planErr := adapter.Plan(t.Context(), absent, desired)
		if planErr != nil {
			t.Fatal(planErr)
		}
		priorDigest, _ := connector.DigestConnectorEntry(absent)
		desiredDigest, _ := connector.DigestConnectorEntry(desired)
		selection.Changes = append(selection.Changes, runtimeconfig.ConfirmedChange{Stage: runtimeconfig.StageConnectorsApplied, Subject: "connector:" + string(adapter.name), Action: "install", PriorDigest: priorDigest, DesiredDigest: desiredDigest})
		if adapter == codex {
			change := connector.ConfirmedConnectorChange{Adapter: adapter.name, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
			if _, err := connector.ApplyTransactionalFor(t.Context(), adapter, desired, change, owner, store, store, store); err != nil {
				t.Fatal(err)
			}
		}
	}
	driver := &productionSetupDriver{connectors: &productionConnectorCommands{store: store, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: codex, connector.AdapterClaude: claude}}}
	if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err == nil {
		t.Fatal("connector failure unexpectedly succeeded")
	}
	if !connector.EqualConnectorEntry(codex.current, absent) || !connector.EqualConnectorEntry(claude.current, absent) {
		t.Fatalf("connectors not restored: codex=%+v claude=%+v", codex.current, claude.current)
	}
	if codex.applyCalls != 1 {
		t.Fatalf("fresh resume re-applied codex: calls=%d", codex.applyCalls)
	}
	if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err == nil {
		t.Fatal("retry connector failure unexpectedly succeeded")
	}
	if !connector.EqualConnectorEntry(codex.current, absent) || !connector.EqualConnectorEntry(claude.current, absent) {
		t.Fatalf("retry connectors not restored: codex=%+v claude=%+v", codex.current, claude.current)
	}
	if codex.applyCalls != 2 {
		t.Fatalf("retry did not apply exactly one successor: calls=%d", codex.applyCalls)
	}
}

func TestProductionSetupConnectorResumeRetiresCompensationCrashBeforeSuccessor(t *testing.T) {
	store, err := connector.OpenStoreAt(filepath.Join(t.TempDir(), "connectors"))
	if err != nil {
		t.Fatal(err)
	}
	desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
	absent := connector.ConnectorEntry{State: connector.EntryAbsent}
	codex := &setupStateAdapter{name: connector.AdapterCodex, current: absent, version: "0.149.0"}
	claude := &setupStateAdapter{name: connector.AdapterClaude, current: absent, version: "2.1.220", applyErr: errors.New("claude install failed")}
	selection := runtimeconfig.SetupSelection{ConnectorAdapters: []string{"codex", "claude"}, PlanDigest: runtimeconfig.SHA256StateDigest([]byte("setup-compensation-crash"))}
	owner := setupConnectorOwner("00000000-0000-4000-8000-000000000077", selection.PlanDigest)
	priorDigest, _ := connector.DigestConnectorEntry(absent)
	desiredDigest, _ := connector.DigestConnectorEntry(desired)
	for _, adapter := range []*setupStateAdapter{codex, claude} {
		selection.Changes = append(selection.Changes, runtimeconfig.ConfirmedChange{Stage: runtimeconfig.StageConnectorsApplied, Subject: "connector:" + string(adapter.name), Action: "install", PriorDigest: priorDigest, DesiredDigest: desiredDigest})
	}
	plan, err := codex.Plan(t.Context(), absent, desired)
	if err != nil {
		t.Fatal(err)
	}
	change := connector.ConfirmedConnectorChange{Adapter: connector.AdapterCodex, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
	if _, err := connector.ApplyTransactionalFor(t.Context(), codex, desired, change, owner, store, store, store); err != nil {
		t.Fatal(err)
	}
	crashing := &compensationAdvanceFailure{Store: store}
	if err := connector.RollbackCompletedTransactional(t.Context(), codex, change, owner, store, crashing, store); err == nil {
		t.Fatal("compensation journal crash unexpectedly succeeded")
	}
	if !connector.EqualConnectorEntry(codex.current, absent) {
		t.Fatalf("compensation did not reach exact prior: %+v", codex.current)
	}

	driver := &productionSetupDriver{connectors: &productionConnectorCommands{store: store, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: codex, connector.AdapterClaude: claude}}}
	if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err == nil {
		t.Fatal("connector failure unexpectedly succeeded")
	}
	if !connector.EqualConnectorEntry(codex.current, absent) || !connector.EqualConnectorEntry(claude.current, absent) {
		t.Fatalf("fresh resume did not restore exact priors: codex=%+v claude=%+v", codex.current, claude.current)
	}
	if codex.applyCalls != 2 {
		t.Fatalf("fresh resume did not apply exactly one successor: calls=%d", codex.applyCalls)
	}
	if _, found, err := store.CompletedTransition(t.Context(), connector.AdapterCodex, "wormhole", connector.OperationInstall, priorDigest, desiredDigest, owner); err != nil || found {
		t.Fatalf("completed operation survived compensation recovery: found=%v err=%v", found, err)
	}
}

func TestProductionSetupConnectorRecoveryOutcomeDoesNotCollideWithSuccessor(t *testing.T) {
	for _, crashedStage := range []connector.OperationStage{connector.StagePrepared, connector.StageApplied, connector.StageRolledBack} {
		t.Run(string(crashedStage), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "connectors")
			store, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
			absent := connector.ConnectorEntry{State: connector.EntryAbsent}
			codex := &setupStateAdapter{name: connector.AdapterCodex, current: absent, version: "0.149.0"}
			claude := &setupStateAdapter{name: connector.AdapterClaude, current: absent, version: "2.1.220", applyErr: errors.New("claude install failed")}
			selection := runtimeconfig.SetupSelection{ConnectorAdapters: []string{"codex", "claude"}, PlanDigest: runtimeconfig.SHA256StateDigest([]byte("setup-recovery-" + string(crashedStage)))}
			owner := setupConnectorOwner("00000000-0000-4000-8000-000000000066", selection.PlanDigest)
			priorDigest, _ := connector.DigestConnectorEntry(absent)
			desiredDigest, _ := connector.DigestConnectorEntry(desired)
			for _, adapter := range []*setupStateAdapter{codex, claude} {
				selection.Changes = append(selection.Changes, runtimeconfig.ConfirmedChange{Stage: runtimeconfig.StageConnectorsApplied, Subject: "connector:" + string(adapter.name), Action: "install", PriorDigest: priorDigest, DesiredDigest: desiredDigest})
			}
			plan, err := codex.Plan(t.Context(), absent, desired)
			if err != nil {
				t.Fatal(err)
			}
			change := connector.ConfirmedConnectorChange{Adapter: connector.AdapterCodex, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
			reference, err := store.Put(t.Context(), connector.ConnectorBackup{SchemaVersion: 1, Adapter: connector.AdapterCodex, Name: "wormhole", Prior: absent, Desired: desired, PlanDigest: plan.Digest})
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Prepare(t.Context(), connector.PrepareOperation{Change: change, BackupReference: reference, OwnerDigest: owner})
			if err != nil {
				t.Fatal(err)
			}
			if crashedStage != connector.StagePrepared {
				if err := store.Advance(t.Context(), record.OperationID, crashedStage); err != nil {
					t.Fatal(err)
				}
			}

			driver := &productionSetupDriver{connectors: &productionConnectorCommands{store: store, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: codex, connector.AdapterClaude: claude}}}
			if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err == nil {
				t.Fatal("later connector failure unexpectedly succeeded")
			}
			if !connector.EqualConnectorEntry(codex.current, absent) || !connector.EqualConnectorEntry(claude.current, absent) {
				t.Fatalf("recovery successor was not compensated: codex=%+v claude=%+v", codex.current, claude.current)
			}
			freshStore, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			freshDriver := &productionSetupDriver{connectors: &productionConnectorCommands{store: freshStore, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: codex, connector.AdapterClaude: claude}}}
			if _, err := freshDriver.reconcileConnectors(t.Context(), selection, owner); err == nil {
				t.Fatal("fresh-process later connector failure unexpectedly succeeded")
			}
			if !connector.EqualConnectorEntry(codex.current, absent) || !connector.EqualConnectorEntry(claude.current, absent) || codex.applyCalls != 2 {
				t.Fatalf("fresh recovery not resumable: codex=%+v claude=%+v applies=%d", codex.current, claude.current, codex.applyCalls)
			}
		})
	}
}

func TestProductionSetupRecoversActiveDesiredBeforeClassification(t *testing.T) {
	for _, test := range []struct {
		stage          connector.OperationStage
		wantRecovered  connector.OperationStage
		wantApplyCalls int
	}{
		{stage: connector.StagePrepared, wantRecovered: connector.StageRestored, wantApplyCalls: 1},
		{stage: connector.StageApplied, wantRecovered: connector.StageRestored, wantApplyCalls: 1},
		{stage: connector.StageVerified, wantRecovered: connector.StageComplete, wantApplyCalls: 0},
	} {
		t.Run(string(test.stage), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "connectors")
			store, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
			prior := connector.ConnectorEntry{State: connector.EntryAbsent}
			adapter := &setupStateAdapter{name: connector.AdapterCodex, current: desired, version: "0.149.0"}
			selection := runtimeconfig.SetupSelection{ConnectorAdapters: []string{"codex"}, PlanDigest: runtimeconfig.SHA256StateDigest([]byte("active-desired-" + string(test.stage)))}
			owner := setupConnectorOwner("00000000-0000-4000-8000-000000000067", selection.PlanDigest)
			priorDigest, _ := connector.DigestConnectorEntry(prior)
			desiredDigest, _ := connector.DigestConnectorEntry(desired)
			selection.Changes = []runtimeconfig.ConfirmedChange{{Stage: runtimeconfig.StageConnectorsApplied, Subject: "connector:codex", Action: "install", PriorDigest: priorDigest, DesiredDigest: desiredDigest}}
			plan, err := adapter.Plan(t.Context(), prior, desired)
			if err != nil {
				t.Fatal(err)
			}
			change := connector.ConfirmedConnectorChange{Adapter: connector.AdapterCodex, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
			reference, err := store.Put(t.Context(), connector.ConnectorBackup{SchemaVersion: 1, Adapter: connector.AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest})
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Prepare(t.Context(), connector.PrepareOperation{Change: change, BackupReference: reference, OwnerDigest: owner})
			if err != nil {
				t.Fatal(err)
			}
			if test.stage == connector.StageApplied || test.stage == connector.StageVerified {
				if err := store.Advance(t.Context(), record.OperationID, connector.StageApplied); err != nil {
					t.Fatal(err)
				}
			}
			if test.stage == connector.StageVerified {
				if err := store.Advance(t.Context(), record.OperationID, connector.StageVerified); err != nil {
					t.Fatal(err)
				}
			}

			freshStore, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			driver := &productionSetupDriver{connectors: &productionConnectorCommands{store: freshStore, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: adapter}}}
			if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err != nil {
				t.Fatalf("active desired resume: %v", err)
			}
			if adapter.applyCalls != test.wantApplyCalls || !connector.EqualConnectorEntry(adapter.current, desired) {
				t.Fatalf("resume current=%+v applies=%d want=%d", adapter.current, adapter.applyCalls, test.wantApplyCalls)
			}
			persisted := readSetupConnectorOperation(t, root, record.OperationID)
			if persisted.Stage != test.wantRecovered {
				t.Fatalf("recovered predecessor stage=%s want=%s", persisted.Stage, test.wantRecovered)
			}
			if _, active, err := freshStore.Active(t.Context(), connector.AdapterCodex, "wormhole"); err != nil || active {
				t.Fatalf("active after resume=%v err=%v", active, err)
			}
			if _, found, err := freshStore.CompletedTransition(t.Context(), connector.AdapterCodex, "wormhole", connector.OperationInstall, priorDigest, desiredDigest, owner); err != nil || !found {
				t.Fatalf("desired completion found=%v err=%v", found, err)
			}

			retryStore, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			driver.connectors.store = retryStore
			if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err != nil {
				t.Fatalf("fresh retry: %v", err)
			}
			if adapter.applyCalls != test.wantApplyCalls {
				t.Fatalf("fresh retry reapplied connector: calls=%d want=%d", adapter.applyCalls, test.wantApplyCalls)
			}
			if _, active, err := retryStore.Active(t.Context(), connector.AdapterCodex, "wormhole"); err != nil || active {
				t.Fatalf("active after fresh retry=%v err=%v", active, err)
			}
		})
	}
}

func TestProductionSetupActiveDesiredSuccessorCompensatesLaterFailure(t *testing.T) {
	for _, stage := range []connector.OperationStage{connector.StagePrepared, connector.StageApplied} {
		t.Run(string(stage), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "connectors")
			store, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
			prior := connector.ConnectorEntry{State: connector.EntryAbsent}
			codex := &setupStateAdapter{name: connector.AdapterCodex, current: desired, version: "0.149.0"}
			claude := &setupStateAdapter{name: connector.AdapterClaude, current: prior, version: "2.1.220", applyErr: errors.New("later adapter failed")}
			selection := runtimeconfig.SetupSelection{ConnectorAdapters: []string{"codex", "claude"}, PlanDigest: runtimeconfig.SHA256StateDigest([]byte("active-desired-compensation-" + string(stage)))}
			owner := setupConnectorOwner("00000000-0000-4000-8000-000000000068", selection.PlanDigest)
			priorDigest, _ := connector.DigestConnectorEntry(prior)
			desiredDigest, _ := connector.DigestConnectorEntry(desired)
			for _, name := range []string{"codex", "claude"} {
				selection.Changes = append(selection.Changes, runtimeconfig.ConfirmedChange{Stage: runtimeconfig.StageConnectorsApplied, Subject: "connector:" + name, Action: "install", PriorDigest: priorDigest, DesiredDigest: desiredDigest})
			}
			plan, err := codex.Plan(t.Context(), prior, desired)
			if err != nil {
				t.Fatal(err)
			}
			change := connector.ConfirmedConnectorChange{Adapter: connector.AdapterCodex, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
			reference, err := store.Put(t.Context(), connector.ConnectorBackup{SchemaVersion: 1, Adapter: connector.AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest})
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Prepare(t.Context(), connector.PrepareOperation{Change: change, BackupReference: reference, OwnerDigest: owner})
			if err != nil {
				t.Fatal(err)
			}
			if stage == connector.StageApplied {
				if err := store.Advance(t.Context(), record.OperationID, connector.StageApplied); err != nil {
					t.Fatal(err)
				}
			}

			freshStore, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			driver := &productionSetupDriver{connectors: &productionConnectorCommands{store: freshStore, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: codex, connector.AdapterClaude: claude}}}
			if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err == nil {
				t.Fatal("later adapter failure unexpectedly succeeded")
			}
			if !connector.EqualConnectorEntry(codex.current, prior) || !connector.EqualConnectorEntry(claude.current, prior) || codex.applyCalls != 1 {
				t.Fatalf("compensation current codex=%+v claude=%+v applies=%d", codex.current, claude.current, codex.applyCalls)
			}
			if persisted := readSetupConnectorOperation(t, root, record.OperationID); persisted.Stage != connector.StageRestored {
				t.Fatalf("recovered predecessor stage=%s want=%s", persisted.Stage, connector.StageRestored)
			}
			for _, name := range []connector.AdapterName{connector.AdapterCodex, connector.AdapterClaude} {
				if _, active, err := freshStore.Active(t.Context(), name, "wormhole"); err != nil || active {
					t.Fatalf("%s active after compensation=%v err=%v", name, active, err)
				}
			}

			claude.applyErr = nil
			retryStore, err := connector.OpenStoreAt(root)
			if err != nil {
				t.Fatal(err)
			}
			driver.connectors.store = retryStore
			if _, err := driver.reconcileConnectors(t.Context(), selection, owner); err != nil {
				t.Fatalf("fresh retry: %v", err)
			}
			if !connector.EqualConnectorEntry(codex.current, desired) || !connector.EqualConnectorEntry(claude.current, desired) || codex.applyCalls != 2 {
				t.Fatalf("fresh retry current codex=%+v claude=%+v applies=%d", codex.current, claude.current, codex.applyCalls)
			}
			for _, name := range []connector.AdapterName{connector.AdapterCodex, connector.AdapterClaude} {
				if _, active, err := retryStore.Active(t.Context(), name, "wormhole"); err != nil || active {
					t.Fatalf("%s active after fresh retry=%v err=%v", name, active, err)
				}
			}
		})
	}
}

func TestProductionSetupConnectorThirdStateRecoveryIsNoWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connectors")
	store, err := connector.OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	desired := connector.ConnectorEntry{State: connector.EntryPresent, Scope: connector.ScopeUser, Transport: connector.TransportStdio, Command: "/usr/bin/wormhole", Args: []string{"mcp"}, Env: []connector.EnvironmentVariable{}}
	prior := connector.ConnectorEntry{State: connector.EntryAbsent}
	third := desired
	third.Command = "/usr/bin/competitor"
	adapter := &setupStateAdapter{name: connector.AdapterCodex, current: third, version: "0.149.0"}
	selection := runtimeconfig.SetupSelection{ConnectorAdapters: []string{"codex"}, PlanDigest: runtimeconfig.SHA256StateDigest([]byte("active-third"))}
	owner := setupConnectorOwner("00000000-0000-4000-8000-000000000069", selection.PlanDigest)
	priorDigest, _ := connector.DigestConnectorEntry(prior)
	desiredDigest, _ := connector.DigestConnectorEntry(desired)
	selection.Changes = []runtimeconfig.ConfirmedChange{{Stage: runtimeconfig.StageConnectorsApplied, Subject: "connector:codex", Action: "install", PriorDigest: priorDigest, DesiredDigest: desiredDigest}}
	plan, err := adapter.Plan(t.Context(), prior, desired)
	if err != nil {
		t.Fatal(err)
	}
	change := connector.ConfirmedConnectorChange{Adapter: connector.AdapterCodex, Name: "wormhole", Action: connector.OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}
	reference, err := store.Put(t.Context(), connector.ConnectorBackup{SchemaVersion: 1, Adapter: connector.AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Prepare(t.Context(), connector.PrepareOperation{Change: change, BackupReference: reference, OwnerDigest: owner})
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "operation-"+record.OperationID+".json")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	freshStore, err := connector.OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	driver := &productionSetupDriver{connectors: &productionConnectorCommands{store: freshStore, desired: desired, adapters: map[connector.AdapterName]connector.Adapter{connector.AdapterCodex: adapter}}}
	if _, err := driver.reconcileConnectors(t.Context(), selection, owner); !errors.Is(err, connector.ErrConnectorStateDrift) {
		t.Fatalf("third-state error=%v", err)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !connector.EqualConnectorEntry(adapter.current, third) || adapter.applyCalls != 0 {
		t.Fatalf("third state mutated: journal_equal=%v current=%+v applies=%d", bytes.Equal(before, after), adapter.current, adapter.applyCalls)
	}
}

func readSetupConnectorOperation(t *testing.T, root, operationID string) connector.OperationRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "operation-"+operationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record connector.OperationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

type compensationAdvanceFailure struct {
	*connector.Store
}

func (j *compensationAdvanceFailure) Advance(_ context.Context, _ string, stage connector.OperationStage) error {
	if stage == connector.StageCompensated {
		return errors.New("injected compensation journal crash")
	}
	return errors.New("unexpected compensation stage")
}

type setupStateAdapter struct {
	name       connector.AdapterName
	current    connector.ConnectorEntry
	version    string
	applyErr   error
	applyCalls int
}

func (a *setupStateAdapter) AdapterName() connector.AdapterName { return a.name }
func (a *setupStateAdapter) Discover(context.Context) (connector.Availability, error) {
	return connector.Availability{Available: true, Version: a.version}, nil
}
func (a *setupStateAdapter) Inspect(context.Context) (connector.ConnectorEntry, error) {
	return a.current, nil
}
func (a *setupStateAdapter) Plan(_ context.Context, prior, desired connector.ConnectorEntry) (connector.ChangePlan, error) {
	return connector.BuildChangePlan(a.name, "wormhole", connector.OperationInstall, prior, desired)
}
func (a *setupStateAdapter) Apply(_ context.Context, plan connector.ChangePlan) error {
	a.applyCalls++
	a.current = plan.Desired
	return a.applyErr
}
func (a *setupStateAdapter) Verify(_ context.Context, desired connector.ConnectorEntry) error {
	if !connector.EqualConnectorEntry(a.current, desired) {
		return connector.ErrConnectorStateDrift
	}
	return nil
}
func (a *setupStateAdapter) Rollback(_ context.Context, plan connector.ChangePlan) error {
	if !connector.EqualConnectorEntry(a.current, plan.Prior) && !connector.EqualConnectorEntry(a.current, plan.Desired) {
		return connector.ErrConnectorStateDrift
	}
	a.current = plan.Prior
	return nil
}
func (a *setupStateAdapter) Remove(context.Context, connector.ConnectorEntry) error {
	return errors.New("unexpected remove")
}

func TestSetupUsesExactlyEightLocalStages(t *testing.T) {
	want := []runtimeconfig.SetupStage{
		runtimeconfig.StageProjectValidated,
		runtimeconfig.StageGatewayReady,
		runtimeconfig.StageWorkspaceRegistered,
		runtimeconfig.StageIdentitySelected,
		runtimeconfig.StagePublicationClassified,
		runtimeconfig.StageBaseImported,
		runtimeconfig.StageConnectorsApplied,
		runtimeconfig.StageFinalVerified,
	}
	if !reflect.DeepEqual(orderedCLISetupStages, want) {
		t.Fatalf("stages = %v, want %v", orderedCLISetupStages, want)
	}
	for _, stage := range orderedCLISetupStages {
		if strings.Contains(string(stage), "fabric") || strings.Contains(string(stage), "graph") {
			t.Fatalf("remote/graph stage leaked into setup: %s", stage)
		}
	}
}

func TestSetupAndConnectorCommandsAreCanonicalCLIEntrypoints(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"setup", "--unknown"}, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("setup dispatch code = %d, stderr %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"connector", "unknown"}, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("connector dispatch code = %d, stderr %q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "wormhole setup") || !strings.Contains(stdout.String(), "wormhole connector") {
		t.Fatalf("help output = %q, code %d", stdout.String(), code)
	}
	for _, legacy := range []string{"join", "connect"} {
		stdout.Reset()
		stderr.Reset()
		if code := run([]string{legacy}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
			t.Fatalf("legacy %s dispatch code = %d, stderr %q", legacy, code, stderr.String())
		}
	}
}

func TestSetupGatewayClientUsesPrivateHandshakeAndBoundedReadback(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "gateway.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			serverDone <- readErr
			return
		}
		var initialize rpcRequest
		if decodeErr := json.Unmarshal(bytes.TrimSpace(line), &initialize); decodeErr != nil || initialize.Method != "initialize" {
			serverDone <- errors.New("missing initialize")
			return
		}
		_, _ = connection.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","serverInfo":{"name":"gatewayd"}}}` + "\n"))
		line, _ = reader.ReadBytes('\n')
		var notification rpcRequest
		_ = json.Unmarshal(bytes.TrimSpace(line), &notification)
		line, _ = reader.ReadBytes('\n')
		var request rpcRequest
		_ = json.Unmarshal(bytes.TrimSpace(line), &request)
		if notification.Method != "notifications/initialized" || request.Method != localapi.PrivateSetupRegisterWorkspaceRPCMethod {
			serverDone <- errors.New("private setup handshake mismatch")
			return
		}
		_, _ = connection.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"project_id":"00000000-0000-4000-8000-000000000001","workspace_id":"00000000-0000-4000-8000-000000000002","accepted_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","accepted_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","state":"registered"}}` + "\n"))
		serverDone <- nil
	}()
	client := &unixSetupGateway{socketPath: socket}
	readback, err := client.Register(t.Context(), localapi.SetupWorkspaceRequest{WorkingDirectory: "/tmp/repository"})
	if err != nil || readback.WorkspaceID != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("readback = %+v, err %v", readback, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func eventIndex(events []string, value string) int {
	for index, event := range events {
		if event == value {
			return index
		}
	}
	return -1
}

type fakeSetupJournal struct {
	events          *[]string
	value           runtimeconfig.SetupJournal
	selectionWrites int
	writeCount      int
}

func newFakeSetupJournal(events *[]string) *fakeSetupJournal {
	return &fakeSetupJournal{events: events, value: runtimeconfig.SetupJournal{
		SchemaVersion:    1,
		JournalID:        "00000000-0000-4000-8000-000000000081",
		CanonicalRoot:    "/tmp/wormhole-setup-project",
		State:            runtimeconfig.SetupJournalActive,
		CompletedStages:  []runtimeconfig.SetupStage{},
		ConnectorBackups: []runtimeconfig.BackupReference{},
	}}
}

func (j *fakeSetupJournal) Begin(context.Context, string) (runtimeconfig.SetupJournal, error) {
	return cloneTestJournal(j.value), nil
}
func (j *fakeSetupJournal) Resumable(context.Context, string) (runtimeconfig.SetupJournal, bool, error) {
	return cloneTestJournal(j.value), j.value.State == runtimeconfig.SetupJournalActive, nil
}
func (j *fakeSetupJournal) SetSelection(_ context.Context, _ string, selection runtimeconfig.SetupSelection) error {
	if j.value.Selection != nil {
		if reflect.DeepEqual(*j.value.Selection, selection) {
			return nil
		}
		return runtimeconfig.ErrConfirmedPlanDrift
	}
	j.value.Selection = &selection
	j.selectionWrites++
	j.record("selection")
	return nil
}
func (j *fakeSetupJournal) BindWorkspace(_ context.Context, _ string, id types.WorkspaceID) error {
	if j.value.WorkspaceID != "" {
		if j.value.WorkspaceID == id {
			return nil
		}
		return runtimeconfig.ErrConfirmedPlanDrift
	}
	j.value.WorkspaceID = id
	j.record("bind-workspace")
	return nil
}
func (j *fakeSetupJournal) BindIdentity(_ context.Context, _, id string) error {
	if j.value.IdentityPrincipalID != "" {
		if j.value.IdentityPrincipalID == id {
			return nil
		}
		return runtimeconfig.ErrConfirmedPlanDrift
	}
	j.value.IdentityPrincipalID = id
	j.record("bind-identity")
	return nil
}
func (j *fakeSetupJournal) RecordConnectorBackup(_ context.Context, _ string, reference runtimeconfig.BackupReference) error {
	for _, existing := range j.value.ConnectorBackups {
		if existing == reference {
			return nil
		}
	}
	j.value.ConnectorBackups = append(j.value.ConnectorBackups, reference)
	j.record("connector-backup")
	return nil
}
func (j *fakeSetupJournal) MarkCompleted(_ context.Context, _ string, stage runtimeconfig.SetupStage) error {
	stageIndex := setupStageIndexCLI(stage)
	if stageIndex < len(j.value.CompletedStages) && j.value.CompletedStages[stageIndex] == stage {
		return nil
	}
	if stageIndex != len(j.value.CompletedStages) {
		return runtimeconfig.ErrConfirmedPlanDrift
	}
	j.value.CompletedStages = append(j.value.CompletedStages, stage)
	j.value.LastError = nil
	j.record("mark:" + string(stage))
	return nil
}
func (j *fakeSetupJournal) RecordLastError(_ context.Context, _ string, stage runtimeconfig.SetupStage, _ error) error {
	j.value.LastError = &runtimeconfig.SetupFailure{Stage: stage, Message: "redacted"}
	j.record("last-error:" + string(stage))
	return nil
}
func (j *fakeSetupJournal) Complete(context.Context, string) error {
	j.value.State = runtimeconfig.SetupJournalCompleted
	j.record("complete")
	return nil
}
func (j *fakeSetupJournal) record(event string) {
	*j.events = append(*j.events, event)
	j.writeCount++
}
func (j *fakeSetupJournal) snapshot() []byte {
	return []byte(strings.Join(append([]string{}, *j.events...), "\n"))
}

func cloneTestJournal(j runtimeconfig.SetupJournal) runtimeconfig.SetupJournal {
	j.CompletedStages = append([]runtimeconfig.SetupStage{}, j.CompletedStages...)
	j.ConnectorBackups = append([]runtimeconfig.BackupReference{}, j.ConnectorBackups...)
	if j.Selection != nil {
		selection := *j.Selection
		selection.ConnectorAdapters = append([]string{}, selection.ConnectorAdapters...)
		selection.Changes = append([]runtimeconfig.ConfirmedChange{}, selection.Changes...)
		j.Selection = &selection
	}
	return j
}

type fakeSetupDriver struct {
	events            *[]string
	desired           map[runtimeconfig.SetupStage]bool
	effectCount       map[runtimeconfig.SetupStage]int
	crashAfter        map[runtimeconfig.SetupStage]bool
	driftAt           runtimeconfig.SetupStage
	renderCount       int
	connectorFailure  bool
	connectorRestored bool
}

func newFakeSetupDriver(events *[]string) *fakeSetupDriver {
	return &fakeSetupDriver{events: events, desired: map[runtimeconfig.SetupStage]bool{}, effectCount: map[runtimeconfig.SetupStage]int{}, crashAfter: map[runtimeconfig.SetupStage]bool{}}
}

func (d *fakeSetupDriver) CanonicalRoot(context.Context) (string, error) {
	return "/tmp/wormhole-setup-project", nil
}
func (d *fakeSetupDriver) Plan(_ context.Context, options setupOptions, frozen *runtimeconfig.SetupSelection) (setupPlan, error) {
	if frozen != nil {
		return setupPlan{Selection: *frozen, ProjectID: "00000000-0000-4000-8000-000000000091", Commit: strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 71)}, nil
	}
	d.renderCount++
	selection := testSetupSelection(options.publication)
	return setupPlan{Selection: selection, ProjectID: "00000000-0000-4000-8000-000000000091", Commit: strings.Repeat("a", 40), TreeDigest: "sha256:" + strings.Repeat("b", 64)}, nil
}
func (d *fakeSetupDriver) ReconcileStage(_ context.Context, stage runtimeconfig.SetupStage, _ setupPlan, _ runtimeconfig.SetupJournal) (setupStageResult, error) {
	if stage == d.driftAt {
		return setupStageResult{}, runtimeconfig.ErrConfirmedPlanDrift
	}
	if !d.desired[stage] {
		d.effectCount[stage]++
		*d.events = append(*d.events, "effect:"+string(stage))
		d.desired[stage] = true
	}
	result := setupStageResult{}
	if stage == runtimeconfig.StageWorkspaceRegistered {
		result.WorkspaceID = types.WorkspaceID("00000000-0000-4000-8000-000000000092")
	}
	if stage == runtimeconfig.StageIdentitySelected {
		result.IdentityPrincipalID = "00000000-0000-4000-8000-000000000093"
	}
	if stage == runtimeconfig.StageConnectorsApplied {
		if d.connectorFailure {
			d.connectorRestored = true
			return setupStageResult{}, errors.New("connector verification failed")
		}
		result.ConnectorBackups = []runtimeconfig.BackupReference{"connector-backup:v1:codex:00000000-0000-4000-8000-000000000094"}
	}
	if d.crashAfter[stage] {
		return setupStageResult{}, errors.New("injected crash after effect")
	}
	return result, nil
}

func testSetupSelection(publication string) runtimeconfig.SetupSelection {
	if publication == "" {
		publication = "unclassified"
	}
	changes := make([]runtimeconfig.ConfirmedChange, 0, len(orderedCLISetupStages)+1)
	for index, stage := range orderedCLISetupStages {
		subject, action := testChangeVocabulary(stage)
		changes = append(changes, runtimeconfig.ConfirmedChange{
			Stage: stage, Subject: subject, Action: action,
			PriorDigest:   runtimeconfig.SHA256StateDigest([]byte("prior-" + string(stage))),
			DesiredDigest: runtimeconfig.SHA256StateDigest([]byte("desired-" + string(stage))),
		})
		_ = index
	}
	changes = append(changes[:6], append([]runtimeconfig.ConfirmedChange{{
		Stage: runtimeconfig.StageConnectorsApplied, Subject: "connector:codex", Action: "install",
		PriorDigest: runtimeconfig.SHA256StateDigest([]byte("connector-prior")), DesiredDigest: runtimeconfig.SHA256StateDigest([]byte("connector-desired")),
	}}, changes[6:]...)...)
	return runtimeconfig.SetupSelection{
		ConnectorAdapters: []string{"codex"}, PublicationVisibility: publication,
		PublicationBindingDigest: runtimeconfig.SHA256StateDigest([]byte("publication-binding")),
		Identity:                 types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"},
		PlanDigest:               runtimeconfig.SHA256StateDigest([]byte("complete-plan")), Changes: changes,
	}
}

func testChangeVocabulary(stage runtimeconfig.SetupStage) (string, string) {
	switch stage {
	case runtimeconfig.StageProjectValidated:
		return "project", "validate"
	case runtimeconfig.StageGatewayReady:
		return "gateway-service", "ensure"
	case runtimeconfig.StageWorkspaceRegistered:
		return "workspace", "register"
	case runtimeconfig.StageIdentitySelected:
		return "identity", "ensure-selected"
	case runtimeconfig.StagePublicationClassified:
		return "publication", "classify"
	case runtimeconfig.StageBaseImported:
		return "base", "import"
	case runtimeconfig.StageConnectorsApplied:
		return "setup", "verify" // replaced by the connector change above.
	case runtimeconfig.StageFinalVerified:
		return "setup", "verify"
	default:
		return "", ""
	}
}

func cloneStageCounts(source map[runtimeconfig.SetupStage]int) map[runtimeconfig.SetupStage]int {
	clone := make(map[runtimeconfig.SetupStage]int, len(source))
	for stage, count := range source {
		clone[stage] = count
	}
	return clone
}
