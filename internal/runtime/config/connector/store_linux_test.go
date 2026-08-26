//go:build linux

package connector

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
)

func TestConnectorStoreCanonicalBackupJournalAndRedaction(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connector-store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	prior := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/private/path", Args: []string{"secret-token"}, Env: []EnvironmentVariable{{Name: "TOKEN", Value: "top-secret"}}}
	desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	plan, _ := BuildChangePlan(AdapterCodex, "wormhole", OperationInstall, prior, desired)
	backup := ConnectorBackup{SchemaVersion: 1, Adapter: AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest}
	reference, err := store.Put(t.Context(), backup)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(t.Context(), reference)
	if err != nil || !EqualConnectorEntry(loaded.Prior, prior) {
		t.Fatalf("Get=%#v, %v", loaded, err)
	}
	priorDigest, _ := DigestConnectorEntry(prior)
	desiredDigest, _ := DigestConnectorEntry(desired)
	record, err := store.Prepare(t.Context(), PrepareOperation{Change: ConfirmedConnectorChange{Adapter: AdapterCodex, Name: "wormhole", Action: OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}, BackupReference: reference})
	if err != nil {
		t.Fatal(err)
	}
	journalBytes, err := os.ReadFile(filepath.Join(root, "operation-"+record.OperationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"top-secret", "secret-token", "/private/path", "TOKEN"} {
		if strings.Contains(string(journalBytes), secret) {
			t.Fatalf("journal leaked %q: %s", secret, journalBytes)
		}
	}
	for _, stage := range []OperationStage{StageApplied, StageVerified, StageComplete} {
		if err := store.Advance(t.Context(), record.OperationID, stage); err != nil {
			t.Fatalf("Advance(%s): %v", stage, err)
		}
	}
	if _, ok, err := store.Active(t.Context(), AdapterCodex, "wormhole"); err != nil || ok {
		t.Fatalf("Active=%v, %v", ok, err)
	}
	for _, path := range []string{root, filepath.Join(root, "operation-"+record.OperationID+".json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if !info.IsDir() {
			want = 0o600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%o want=%o", path, info.Mode().Perm(), want)
		}
	}
}

func TestConnectorStorePrepareBindsReferencedBackup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connector-store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	prior := ConnectorEntry{State: EntryAbsent}
	desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	plan, _ := BuildChangePlan(AdapterCodex, "wormhole", OperationInstall, prior, desired)
	reference, err := store.Put(t.Context(), ConnectorBackup{SchemaVersion: 1, Adapter: AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest})
	if err != nil {
		t.Fatal(err)
	}
	priorDigest, _ := DigestConnectorEntry(prior)
	desiredDigest, _ := DigestConnectorEntry(desired)
	badDigest := config.SHA256StateDigest([]byte("different plan"))
	_, err = store.Prepare(t.Context(), PrepareOperation{Change: ConfirmedConnectorChange{Adapter: AdapterCodex, Name: "wormhole", Action: OperationInstall, PlanDigest: badDigest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}, BackupReference: reference})
	if !errors.Is(err, config.ErrConfirmedPlanDrift) {
		t.Fatalf("Prepare mismatched backup error=%v", err)
	}
	if _, active, activeErr := store.Active(t.Context(), AdapterCodex, "wormhole"); activeErr != nil || active {
		t.Fatalf("active=%v err=%v", active, activeErr)
	}
}

func TestConnectorStoreLoadRejectsActionDesiredMismatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "connector-store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	prior := ConnectorEntry{State: EntryAbsent}
	desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	plan, _ := BuildChangePlan(AdapterCodex, "wormhole", OperationInstall, prior, desired)
	reference, _ := store.Put(t.Context(), ConnectorBackup{SchemaVersion: 1, Adapter: AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest})
	priorDigest, _ := DigestConnectorEntry(prior)
	desiredDigest, _ := DigestConnectorEntry(desired)
	record, err := store.Prepare(t.Context(), PrepareOperation{Change: ConfirmedConnectorChange{Adapter: AdapterCodex, Name: "wormhole", Action: OperationInstall, PlanDigest: plan.Digest, ExpectedPriorDigest: priorDigest, DesiredDigest: desiredDigest}, BackupReference: reference})
	if err != nil {
		t.Fatal(err)
	}
	record.Action = OperationRemove
	corrupt, _ := marshalCanonicalConnectorJSON(record)
	if err := os.WriteFile(filepath.Join(root, "operation-"+record.OperationID+".json"), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Active(t.Context(), AdapterCodex, "wormhole"); !errors.Is(err, ErrInvalidConnectorStore) {
		t.Fatalf("Active mismatch error=%v", err)
	}
}

func TestConnectorPairLockSerializesTwoProcesses(t *testing.T) {
	if os.Getenv("WORMHOLE_CONNECTOR_LOCK_HELPER") == "1" {
		root := os.Getenv("WORMHOLE_CONNECTOR_LOCK_ROOT")
		store, err := OpenStoreAt(root)
		if err != nil {
			os.Exit(3)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		err = store.WithOperationLock(ctx, AdapterCodex, "wormhole", func(context.Context) error { return nil })
		if errors.Is(err, context.DeadlineExceeded) {
			os.Exit(0)
		}
		if err != nil {
			os.Exit(4)
		}
		os.Exit(5)
	}
	root := filepath.Join(t.TempDir(), "connector-store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	err = store.WithOperationLock(t.Context(), AdapterCodex, "wormhole", func(context.Context) error {
		command := exec.Command(os.Args[0], "-test.run=^TestConnectorPairLockSerializesTwoProcesses$")
		command.Env = append(os.Environ(), "WORMHOLE_CONNECTOR_LOCK_HELPER=1", "WORMHOLE_CONNECTOR_LOCK_ROOT="+root)
		output, runErr := command.CombinedOutput()
		if runErr != nil {
			return errors.New("lock helper failed: " + string(output))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConnectorStoreRejectsInsecureRootAndCorruptUnknownJournal(t *testing.T) {
	insecure := filepath.Join(t.TempDir(), "insecure")
	if err := os.Mkdir(insecure, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStoreAt(insecure); !errors.Is(err, ErrUnsafeConnectorStore) {
		t.Fatalf("insecure error=%v", err)
	}

	root := filepath.Join(t.TempDir(), "store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(root, "operation-22222222-2222-4222-8222-222222222222.json")
	if err := os.WriteFile(bad, []byte("{\"schema_version\":1,\"unknown\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Active(t.Context(), AdapterCodex, "wormhole"); !errors.Is(err, ErrInvalidConnectorStore) {
		t.Fatalf("corrupt error=%v", err)
	}
}

func TestConnectorStoreRejectsUnknownPairLockGrammar(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".pair-junk.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Active(t.Context(), AdapterCodex, "wormhole"); !errors.Is(err, ErrInvalidConnectorStore) {
		t.Fatalf("error=%v", err)
	}
}

func TestConnectorStorePreservesPrefixOnlyUnknownTemporaryFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(root, ".tmp-owner-note")
	if err := os.WriteFile(unknown, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Active(t.Context(), AdapterCodex, "wormhole"); !errors.Is(err, ErrInvalidConnectorStore) {
		t.Fatalf("error=%v", err)
	}
	content, err := os.ReadFile(unknown)
	if err != nil || string(content) != "retain" {
		t.Fatalf("unknown file was changed: %q, %v", content, err)
	}
}

func TestConnectorStoreEnforcesDurableRecordLimit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	store, err := OpenStoreAt(root)
	if err != nil {
		t.Fatal(err)
	}
	prior := ConnectorEntry{State: EntryAbsent}
	desired := ConnectorEntry{State: EntryPresent, Scope: ScopeUser, Transport: TransportStdio, Command: "/opt/wormhole", Args: []string{"mcp"}, Env: []EnvironmentVariable{}}
	plan, _ := BuildChangePlan(AdapterCodex, "wormhole", OperationInstall, prior, desired)
	backup := ConnectorBackup{SchemaVersion: 1, Adapter: AdapterCodex, Name: "wormhole", Prior: prior, Desired: desired, PlanDigest: plan.Digest}
	for index := 0; index < maxConnectorStoreRecords; index++ {
		if _, err := store.Put(t.Context(), backup); err != nil {
			t.Fatalf("Put(%d): %v", index, err)
		}
	}
	if _, err := store.Put(t.Context(), backup); !errors.Is(err, ErrInvalidConnectorStore) {
		t.Fatalf("over-cap error=%v", err)
	}
}

var _ BackupStore = (*Store)(nil)
var _ OperationJournal = (*Store)(nil)
var _ OperationCoordinator = (*Store)(nil)
var _ = config.ErrConfirmedPlanDrift
