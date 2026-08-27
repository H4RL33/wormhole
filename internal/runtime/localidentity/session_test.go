package localidentity

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func TestMCPConnectionsReuseDurableAgentAndPersistAccountabilityAcrossRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identities")
	store := selectedIdentityStore(t, root)
	firstTime := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return firstTime }

	first, err := store.OpenMCP(t.Context(), MCPClientInfo{
		Name: " Codex ", Version: "0.150.0", ModelName: "gpt", ModelVersion: "5.6",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstActor, err := store.ResolveLocalActor(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.Selected(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if firstActor.ActorKind != types.ActorAgent || !types.CanonicalUUID(firstActor.AgentID) || firstActor.AccountableHumanID != selected.HumanPrincipalID || firstActor.SessionID != first.SessionID || firstActor.HarnessName != "codex" || firstActor.HarnessVersion != "0.150.0" || firstActor.ModelName != "gpt" || firstActor.ModelVersion != "5.6" || firstActor.Assurance != types.AssuranceLocal || !firstActor.OccurredAt.Equal(firstTime) {
		t.Fatalf("first actor = %+v", firstActor)
	}

	if err := store.CloseConnection(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	secondTime := firstTime.Add(time.Hour)
	reopened.clock = func() time.Time { return secondTime }
	second, err := reopened.OpenMCP(t.Context(), MCPClientInfo{Name: "codex", Version: "0.151.0"})
	if err != nil {
		t.Fatal(err)
	}
	secondActor, err := reopened.ResolveLocalActor(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if secondActor.AgentID != firstActor.AgentID || secondActor.SessionID == firstActor.SessionID || secondActor.HarnessVersion != "0.151.0" || secondActor.ModelName != "" || secondActor.ModelVersion != "" || secondActor.AccountableHumanID != selected.HumanPrincipalID {
		t.Fatalf("restarted actor = %+v, first = %+v", secondActor, firstActor)
	}

	other, err := reopened.OpenMCP(t.Context(), MCPClientInfo{Name: "claude-code", Version: "2.1"})
	if err != nil {
		t.Fatal(err)
	}
	otherActor, err := reopened.ResolveLocalActor(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if otherActor.AgentID == firstActor.AgentID || otherActor.AccountableHumanID != selected.HumanPrincipalID {
		t.Fatalf("distinct harness actor = %+v, codex = %+v", otherActor, firstActor)
	}
}

func TestOpenMCPIsConcurrentAndExactlyIdempotentForDurableAgent(t *testing.T) {
	store := selectedIdentityStore(t, filepath.Join(t.TempDir(), "identities"))
	store.clock = func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }
	const count = 16
	actors := make(chan types.ActorEnvelope, count)
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			connection, err := store.OpenMCP(context.Background(), MCPClientInfo{Name: "CODEX", Version: "1"})
			if err != nil {
				errs <- err
				return
			}
			actor, err := store.ResolveLocalActor(context.Background(), connection)
			if err != nil {
				errs <- err
				return
			}
			actors <- actor
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	close(actors)
	agentID := ""
	sessions := map[string]struct{}{}
	for actor := range actors {
		if agentID == "" {
			agentID = actor.AgentID
		}
		if actor.AgentID != agentID {
			t.Fatalf("durable agent IDs differ: %q != %q", actor.AgentID, agentID)
		}
		if _, duplicate := sessions[actor.SessionID]; duplicate {
			t.Fatalf("duplicate session ID %q", actor.SessionID)
		}
		sessions[actor.SessionID] = struct{}{}
	}
	if len(sessions) != count {
		t.Fatalf("sessions = %d, want %d", len(sessions), count)
	}
}

func TestHumanConnectionCarriesServerOwnedCLISessionProvenance(t *testing.T) {
	store := selectedIdentityStore(t, filepath.Join(t.TempDir(), "identities"))
	now := time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return now }
	connection, err := store.OpenHuman(t.Context(), MCPClientInfo{Name: "wormhole-cli", Version: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolveLocalActor(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := store.Selected(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: selected.HumanPrincipalID, SessionID: connection.SessionID, HarnessName: "wormhole-cli", HarnessVersion: "dev", Assurance: types.AssuranceLocal, OccurredAt: now}
	if actor != want {
		t.Fatalf("human connection actor = %+v, want %+v", actor, want)
	}
	direct, err := store.ResolveHumanActor(t.Context(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if direct.ActorKind != types.ActorHuman || direct.HumanPrincipalID != selected.HumanPrincipalID || direct.AgentID != "" || direct.SessionID != "" || direct.HarnessName != "" || direct.ModelName != "" {
		t.Fatalf("direct human actor = %+v", direct)
	}
}

func TestCLICapabilityIsCanonicalOwnerPrivateAndStable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identities")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := store.EnsureCLICapability(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(capability) != 64 || strings.Trim(capability, "0123456789abcdef") != "" {
		t.Fatalf("capability is not 32 canonical random bytes: length=%d", len(capability))
	}
	info, err := os.Lstat(filepath.Join(root, cliCapabilityRecordName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("capability mode = %v, want regular 0600", info.Mode())
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.CLICapability(t.Context())
	if err != nil || got != capability {
		t.Fatalf("reopened capability = %q, %v; want stable value", got, err)
	}
}

func TestConnectionPublicationRequiresSelectedPrivateKeyIntegrity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{name: "missing", mutate: func(t *testing.T, root, humanID string) {
			t.Helper()
			if err := os.Remove(filepath.Join(root, privateKeyRecordName(humanID))); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mismatched", mutate: func(t *testing.T, root, humanID string) {
			t.Helper()
			_, other, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, privateKeyRecordName(humanID)), other, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replaced", mutate: func(t *testing.T, root, humanID string) {
			t.Helper()
			replaceSelectedPrivateKey(t, root, humanID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "identities")
			store := selectedIdentityStore(t, root)
			seed, err := store.OpenMCP(t.Context(), MCPClientInfo{Name: "seed", Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CloseConnection(t.Context(), seed); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, connectionRecordsName))
			if err != nil {
				t.Fatal(err)
			}
			selected, err := store.Selected(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, root, selected.HumanPrincipalID)
			for _, open := range []func(context.Context, MCPClientInfo) (ConnectionIdentity, error){store.OpenMCP, store.OpenHuman} {
				if _, err := open(t.Context(), MCPClientInfo{Name: "wormhole-cli", Version: "test"}); !errors.Is(err, ErrInvalidStoreRecord) {
					t.Fatalf("open error = %v, want ErrInvalidStoreRecord", err)
				}
			}
			after, err := os.ReadFile(filepath.Join(root, connectionRecordsName))
			if err != nil || string(after) != string(before) {
				t.Fatalf("failed publication mutated connection records: error %v", err)
			}
		})
	}
}

func TestConnectionPublicationRevalidatesKeyImmediatelyBeforeCommit(t *testing.T) {
	for name, openConnection := range map[string]func(*Store, context.Context) (ConnectionIdentity, error){
		"agent": func(store *Store, ctx context.Context) (ConnectionIdentity, error) {
			return store.OpenMCP(ctx, MCPClientInfo{Name: "codex", Version: "test"})
		},
		"human": func(store *Store, ctx context.Context) (ConnectionIdentity, error) {
			return store.OpenHuman(ctx, MCPClientInfo{Name: "wormhole-cli", Version: "test"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "identities")
			store := selectedIdentityStore(t, root)
			seed, err := store.OpenMCP(t.Context(), MCPClientInfo{Name: "seed", Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CloseConnection(t.Context(), seed); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, connectionRecordsName))
			if err != nil {
				t.Fatal(err)
			}
			selected, err := store.Selected(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			store.beforeConnectionPublication = func() {
				store.beforeConnectionPublication = nil
				replaceSelectedPrivateKey(t, root, selected.HumanPrincipalID)
			}
			if _, err := openConnection(store, t.Context()); !errors.Is(err, ErrInvalidStoreRecord) {
				t.Fatalf("open connection error = %v, want ErrInvalidStoreRecord", err)
			}
			after, err := os.ReadFile(filepath.Join(root, connectionRecordsName))
			if err != nil || string(after) != string(before) {
				t.Fatalf("raced publication mutated connection records: error %v", err)
			}
		})
	}
}

func replaceSelectedPrivateKey(t *testing.T, root, humanID string) {
	t.Helper()
	_, other, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement-key")
	if err := os.WriteFile(replacement, other, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(root, privateKeyRecordName(humanID))); err != nil {
		t.Fatal(err)
	}
}

func TestMCPClientInfoUsesExplicitUnknownAndRejectsIncompleteModel(t *testing.T) {
	store := selectedIdentityStore(t, filepath.Join(t.TempDir(), "identities"))
	store.clock = func() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) }
	connection, err := store.OpenMCP(t.Context(), MCPClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := store.ResolveLocalActor(t.Context(), connection)
	if err != nil {
		t.Fatal(err)
	}
	if actor.HarnessName != UnknownClientMetadata || actor.HarnessVersion != UnknownClientMetadata || actor.ModelName != "" || actor.ModelVersion != "" {
		t.Fatalf("unknown client actor = %+v", actor)
	}
	for _, info := range []MCPClientInfo{
		{Name: "codex", Version: "1", ModelName: "gpt"},
		{Name: "codex", Version: "1", ModelVersion: "5"},
		{Name: " codex\nforge ", Version: "1"},
	} {
		if _, err := store.OpenMCP(t.Context(), info); !errors.Is(err, ErrInvalidClientInfo) {
			t.Fatalf("OpenMCP(%+v) error = %v, want ErrInvalidClientInfo", info, err)
		}
	}
}

func TestSessionRecoveryIsExplicitTerminalAndBounded(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identities")
	store := selectedIdentityStore(t, root)
	started := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return started }
	active, err := store.OpenMCP(t.Context(), MCPClientInfo{Name: "codex", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}

	// Opening another store is an inspection/restart precursor, not authority to
	// terminate a concurrently live connection.
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	activeRecord, err := reopened.Session(t.Context(), active.SessionID)
	if err != nil || activeRecord.EndedAt != nil {
		t.Fatalf("ordinary Open changed active session: record=%+v err=%v", activeRecord, err)
	}

	recoveredAt := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	reopened.clock = func() time.Time { return recoveredAt }
	if err := reopened.RecoverConnectionSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Session(t.Context(), active.SessionID)
	if err != nil || recovered.EndedAt == nil || !recovered.EndedAt.Equal(recoveredAt) {
		t.Fatalf("recovered session = %+v, err %v", recovered, err)
	}
	if err := reopened.CloseConnection(t.Context(), active); err != nil {
		t.Fatalf("idempotent close after recovery: %v", err)
	}

	// A terminal session becomes age-eligible and is pruned only by the same
	// explicit authoritative recovery path.
	pruneAt := recoveredAt.Add(ConnectionSessionRetention + time.Second)
	reopened.clock = func() time.Time { return pruneAt }
	if err := reopened.RecoverConnectionSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Session(t.Context(), active.SessionID); !errors.Is(err, ErrConnectionSessionNotFound) {
		t.Fatalf("aged terminal Session error = %v, want ErrConnectionSessionNotFound", err)
	}
}

func TestTerminalSessionRetentionKeepsNewestTenThousand(t *testing.T) {
	store := selectedIdentityStore(t, filepath.Join(t.TempDir(), "identities"))
	now := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return now }
	seed, err := store.OpenMCP(t.Context(), MCPClientInfo{Name: "codex", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	seedActor, err := store.ResolveLocalActor(t.Context(), seed)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := store.openRoot()
	if err != nil {
		t.Fatal(err)
	}
	records, exists, err := readConnectionRecords(fd)
	if err != nil || !exists {
		closeLocalIdentityFD(fd)
		t.Fatalf("read seed records = (%+v, %t, %v)", records, exists, err)
	}
	records.Sessions = make([]ConnectionSession, maxTerminalConnectionSessions)
	for index := range records.Sessions {
		started := now.Add(-2 * time.Hour).Add(time.Duration(index) * time.Millisecond)
		ended := started.Add(time.Minute)
		records.Sessions[index] = ConnectionSession{
			SchemaVersion: connectionRecordVersion,
			SessionID:     fmt.Sprintf("00000000-0000-4000-8000-%012x", index+1),
			AgentID:       seedActor.AgentID, AccountableHumanID: seedActor.AccountableHumanID,
			HarnessName: "codex", HarnessVersion: "1", StartedAt: started, EndedAt: &ended,
		}
	}
	if err := writeConnectionRecords(store, fd, records); err != nil {
		closeLocalIdentityFD(fd)
		t.Fatal(err)
	}
	if err := closeLocalIdentityFD(fd); err != nil {
		t.Fatal(err)
	}

	newest, err := store.OpenMCP(t.Context(), MCPClientInfo{Name: "codex", Version: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CloseConnection(t.Context(), newest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session(t.Context(), "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrConnectionSessionNotFound) {
		t.Fatalf("oldest overflow Session error = %v, want ErrConnectionSessionNotFound", err)
	}
	if session, err := store.Session(t.Context(), newest.SessionID); err != nil || session.EndedAt == nil {
		t.Fatalf("newest retained session = %+v, %v", session, err)
	}
}

func TestConnectionRecordsRejectUnknownAndDuplicateAuthorityFields(t *testing.T) {
	store := selectedIdentityStore(t, filepath.Join(t.TempDir(), "identities"))
	fd, err := store.openRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer closeLocalIdentityFD(fd)
	for index, raw := range []string{
		`{"schema_version":1,"agents":[],"sessions":[],"token":"secret"}`,
		`{"schema_version":1,"schema_version":1,"agents":[],"sessions":[]}`,
	} {
		name := fmt.Sprintf("bad-%d", index)
		if err := store.atomicWrite(fd, connectionRecordsName, []byte(raw), 0o600, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readConnectionRecords(fd); !errors.Is(err, ErrInvalidStoreRecord) {
			t.Fatalf("%s read error = %v, want ErrInvalidStoreRecord", name, err)
		}
	}
}

func selectedIdentityStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureSelectedForSetup(t.Context(), "00000000-0000-4000-8000-000000000031", types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"}); err != nil {
		t.Fatal(err)
	}
	return store
}
