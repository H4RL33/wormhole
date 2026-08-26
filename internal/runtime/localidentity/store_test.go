package localidentity

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const testJournalID = "11111111-1111-4111-8111-111111111111"

func testSelection() types.ConfirmedIdentitySelection {
	return types.ConfirmedIdentitySelection{DisplayName: "Alice Example", Email: "alice@example.test"}
}

func TestEnsureSelectedPersistsEd25519ProfileAndSelection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if err != nil {
		t.Fatalf("EnsureSelectedForSetup: %v", err)
	}
	if !types.CanonicalUUID(first.HumanPrincipalID) || first.DisplayName != testSelection().DisplayName || len(first.PublicKey) != ed25519.PublicKeySize || first.CreatedAt.IsZero() || first.CreatedAt.Location() != time.UTC {
		t.Fatalf("first public profile = %#v", first)
	}
	if strings.Contains(string(first.PublicKey), testSelection().Email) {
		t.Fatalf("public key contains selection email")
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	second, err := reopened.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if err != nil {
		t.Fatalf("repeat EnsureSelectedForSetup: %v", err)
	}
	if first.HumanPrincipalID != second.HumanPrincipalID || string(first.PublicKey) != string(second.PublicKey) || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("idempotent profile = %#v, want %#v", second, first)
	}
	selected, err := reopened.Selected(context.Background())
	if err != nil {
		t.Fatalf("Selected: %v", err)
	}
	if selected.HumanPrincipalID != first.HumanPrincipalID || selected.DisplayName != first.DisplayName || string(selected.PublicKey) != string(first.PublicKey) {
		t.Fatalf("Selected = %#v, want %#v", selected, first)
	}
}

func TestEnsureSelectedIsJournalIdempotentAndRejectsDrift(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err != nil {
		t.Fatal(err)
	}
	_, err = store.EnsureSelectedForSetup(context.Background(), testJournalID, types.ConfirmedIdentitySelection{DisplayName: "Bob Example"})
	if !errors.Is(err, ErrSetupIdentityDrift) {
		t.Fatalf("selection drift error = %v, want ErrSetupIdentityDrift", err)
	}
}

func TestEnsureSelectedReusesOneMatchingPendingAuthorityAcrossJournals(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	realAtomicWrite := store.atomicWrite
	store.atomicWrite = func(fd int, name string, data []byte, mode os.FileMode, replace bool) error {
		if err := realAtomicWrite(fd, name, data, mode, replace); err != nil {
			return err
		}
		if name == "setup-"+testJournalID+".json" {
			return errors.New("interrupt after durable receipt")
		}
		return nil
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err == nil {
		t.Fatal("first journal unexpectedly completed")
	}
	firstReceipt, exists, err := readSetupRecordFromRoot(root, testJournalID)
	if err != nil || !exists {
		t.Fatalf("first receipt = %#v, %v, %v", firstReceipt, exists, err)
	}
	secondJournal := "22222222-2222-4222-8222-222222222222"
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.EnsureSelectedForSetup(context.Background(), secondJournal, testSelection())
	if err != nil {
		t.Fatalf("second matching journal: %v", err)
	}
	if second.HumanPrincipalID != firstReceipt.HumanPrincipalID {
		t.Fatalf("second journal human = %q, want reserved %q", second.HumanPrincipalID, firstReceipt.HumanPrincipalID)
	}
	secondReceipt, exists, err := readSetupRecordFromRoot(root, secondJournal)
	if err != nil || !exists || secondReceipt.HumanPrincipalID != firstReceipt.HumanPrincipalID {
		t.Fatalf("second receipt = %#v, %v, %v", secondReceipt, exists, err)
	}
	first, err := reopened.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if err != nil {
		t.Fatalf("recover first journal: %v", err)
	}
	if first.HumanPrincipalID != second.HumanPrincipalID {
		t.Fatalf("recovered first human = %q, want %q", first.HumanPrincipalID, second.HumanPrincipalID)
	}
	assertOneIdentityAuthority(t, root, first.HumanPrincipalID)
}

func TestEnsureSelectedBlocksDifferentJournalAgainstPendingAuthorityWithoutWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	realAtomicWrite := store.atomicWrite
	store.atomicWrite = func(fd int, name string, data []byte, mode os.FileMode, replace bool) error {
		if err := realAtomicWrite(fd, name, data, mode, replace); err != nil {
			return err
		}
		if name == "setup-"+testJournalID+".json" {
			return errors.New("interrupt after durable receipt")
		}
		return nil
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err == nil {
		t.Fatal("first journal unexpectedly completed")
	}
	before := snapshotIdentityTree(t, root)
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopened.EnsureSelectedForSetup(context.Background(), "22222222-2222-4222-8222-222222222222", types.ConfirmedIdentitySelection{DisplayName: "Bob Example"})
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("different pending journal error = %v, want ErrIdentityConflict", err)
	}
	if after := snapshotIdentityTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("different pending journal mutated tree: got %#v, want %#v", after, before)
	}
}

func TestEnsureSelectedRejectsThirdSelectedStateWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	realAtomicWrite := store.atomicWrite
	store.atomicWrite = func(fd int, name string, data []byte, mode os.FileMode, replace bool) error {
		if err := realAtomicWrite(fd, name, data, mode, replace); err != nil {
			return err
		}
		if name == "setup-"+testJournalID+".json" {
			return errors.New("interrupt after durable receipt")
		}
		return nil
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err == nil {
		t.Fatal("first journal unexpectedly completed")
	}
	third := selectedRecord{HumanPrincipalID: "22222222-2222-4222-8222-222222222222"}
	bytes, err := marshalCanonical(third)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, selectedRecordName), bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotIdentityTree(t, root)
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reopened.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("third selected state error = %v, want ErrIdentityConflict", err)
	}
	if after := snapshotIdentityTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("third selected state mutated tree: got %#v, want %#v", after, before)
	}
}

func TestEnsureSelectedTreatsDesiredSelectedPointerAsNoOp(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotIdentityTree(t, root)
	realAtomicWrite := store.atomicWrite
	store.atomicWrite = func(fd int, name string, data []byte, mode os.FileMode, replace bool) error {
		if name == selectedRecordName {
			return errors.New("selected pointer must not be rewritten")
		}
		return realAtomicWrite(fd, name, data, mode, replace)
	}
	again, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if again.HumanPrincipalID != profile.HumanPrincipalID {
		t.Fatalf("replay human = %q, want %q", again.HumanPrincipalID, profile.HumanPrincipalID)
	}
	if after := snapshotIdentityTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("desired selected replay mutated tree: got %#v, want %#v", after, before)
	}
}

func TestEnsureSelectedRecoversPastNonAuthoritativeTemporaryFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	temporary := ".tmp-00000000000000000000000000000000"
	if err := os.WriteFile(filepath.Join(root, temporary), []byte("interrupted publication"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err != nil {
		t.Fatalf("EnsureSelectedForSetup with temporary file: %v", err)
	}
}

func TestEnsureSelectedRejectsInvalidInputsBeforeWriting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		context   context.Context
		journalID string
		selection types.ConfirmedIdentitySelection
	}{
		{name: "invalid journal", context: context.Background(), journalID: "not-a-uuid", selection: testSelection()},
		{name: "invalid selection", context: context.Background(), journalID: testJournalID, selection: types.ConfirmedIdentitySelection{DisplayName: "secret token"}},
		{name: "cancelled", context: cancelledContext(), journalID: testJournalID, selection: testSelection()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.EnsureSelectedForSetup(test.context, test.journalID, test.selection); err == nil {
				t.Fatal("EnsureSelectedForSetup unexpectedly succeeded")
			}
		})
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != lockRecordName {
			t.Fatalf("invalid input wrote %q", entry.Name())
		}
	}
}

func TestSelectedAndActorNeverExposePrivateSelectionFields(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if err != nil {
		t.Fatal(err)
	}
	publicBytes, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicBytes), testSelection().Email) || strings.Contains(string(publicBytes), "private") {
		t.Fatalf("public profile leaked private data: %s", publicBytes)
	}
	actor, err := store.ResolveLocalActor(context.Background(), ConnectionIdentity{OccurredAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if actor.ActorKind != types.ActorHuman || actor.HumanPrincipalID != profile.HumanPrincipalID || actor.Assurance != types.AssuranceLocal || actor.OccurredAt.Location() != time.UTC {
		t.Fatalf("actor = %#v", actor)
	}
	if _, err := store.ResolveLocalActor(context.Background(), ConnectionIdentity{}); !errors.Is(err, ErrInvalidConnectionIdentity) {
		t.Fatalf("ResolveLocalActor(zero) error = %v, want ErrInvalidConnectionIdentity", err)
	}
	if _, err := store.ResolveLocalActor(context.Background(), ConnectionIdentity{OccurredAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.FixedZone("plus-one", 3600))}); !errors.Is(err, ErrInvalidConnectionIdentity) {
		t.Fatalf("ResolveLocalActor(non-UTC) error = %v, want ErrInvalidConnectionIdentity", err)
	}
}

func TestSelectedRequiresMatchingPrivateKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, privateKeyRecordName(profile.HumanPrincipalID))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Selected(context.Background()); !errors.Is(err, ErrInvalidStoreRecord) {
		t.Fatalf("Selected without key error = %v, want ErrInvalidStoreRecord", err)
	}
}

func TestStoreRejectsUnknownAndDuplicateRecordJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"unknown":          `{"human_principal_id":"11111111-1111-4111-8111-111111111111","unknown":true}`,
		"duplicate":        `{"human_principal_id":"11111111-1111-4111-8111-111111111111","human_principal_id":"22222222-2222-4222-8222-222222222222"}`,
		"nested duplicate": `{"human_principal_id":"11111111-1111-4111-8111-111111111111","selection":{"display_name":"Alice","display_name":"Bob"},"created_at":"2026-08-26T12:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, selectedRecordName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Selected(context.Background()); !errors.Is(err, ErrInvalidStoreRecord) {
				t.Fatalf("Selected error = %v, want ErrInvalidStoreRecord", err)
			}
		})
	}
}

func TestStrictJSONDecodeRejectsMalformedTrailingAndDuplicateValues(t *testing.T) {
	for _, input := range []string{
		`{`,
		`{"human_principal_id":"11111111-1111-4111-8111-111111111111"} null`,
		`{"human_principal_id":"11111111-1111-4111-8111-111111111111","human_principal_id":"22222222-2222-4222-8222-222222222222"}`,
		`[]`,
	} {
		var record selectedRecord
		if err := strictJSONDecode([]byte(input), &record); err == nil {
			t.Fatalf("strictJSONDecode(%q) unexpectedly succeeded", input)
		}
	}
}

func TestStoreRejectsCorruptReservedPrivateKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err != nil {
		t.Fatal(err)
	}
	setup, exists, err := readSetupRecordFromRoot(root, testJournalID)
	if err != nil || !exists {
		t.Fatalf("read setup = %#v, %v, %v", setup, exists, err)
	}
	if err := os.WriteFile(filepath.Join(root, privateKeyRecordName(setup.HumanPrincipalID)), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); !errors.Is(err, ErrInvalidStoreRecord) {
		t.Fatalf("EnsureSelectedForSetup corrupt key error = %v, want ErrInvalidStoreRecord", err)
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func readSetupRecordFromRoot(root, journalID string) (setupRecord, bool, error) {
	_, fd, err := openLocalIdentityRoot(root)
	if err != nil {
		return setupRecord{}, false, err
	}
	defer closeLocalIdentityFD(fd)
	return readSetupRecord(fd, "setup-"+journalID+".json")
}

func snapshotIdentityTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.Name() == lockRecordName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot[entry.Name()] = data
	}
	return snapshot
}

func assertOneIdentityAuthority(t *testing.T, root, humanID string) {
	t.Helper()
	snapshot := snapshotIdentityTree(t, root)
	if len(snapshot[humanRecordName(humanID)]) == 0 || len(snapshot[privateKeyRecordName(humanID)]) != ed25519.PrivateKeySize {
		t.Fatalf("missing expected human/key authority: %#v", snapshot)
	}
	for name := range snapshot {
		if strings.HasPrefix(name, "human-") && name != humanRecordName(humanID) {
			t.Fatalf("competing human record %q in %#v", name, snapshot)
		}
		if strings.HasPrefix(name, "key-") && name != privateKeyRecordName(humanID) {
			t.Fatalf("competing key record %q in %#v", name, snapshot)
		}
	}
}
