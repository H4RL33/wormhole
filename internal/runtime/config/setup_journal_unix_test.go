//go:build linux

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSetupJournalOwnerOnlyStoreRejectsUnsafeObjects(t *testing.T) {
	t.Run("store mode", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "journals")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenSetupJournalStoreAt(root); !errors.Is(err, ErrUnsafeSetupJournalStore) {
			t.Fatalf("OpenSetupJournalStoreAt(permissive) error = %v", err)
		}
	})
	t.Run("store symlink", func(t *testing.T) {
		parent := t.TempDir()
		realRoot := filepath.Join(parent, "real")
		if err := os.Mkdir(realRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(parent, "alias")
		if err := os.Symlink(realRoot, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenSetupJournalStoreAt(alias); !errors.Is(err, ErrUnsafeSetupJournalStore) {
			t.Fatalf("OpenSetupJournalStoreAt(symlink) error = %v", err)
		}
	})
	t.Run("record mode and symlink", func(t *testing.T) {
		store, projectRoot := newSetupJournalTestStore(t)
		journal, err := store.Begin(context.Background(), projectRoot)
		if err != nil {
			t.Fatal(err)
		}
		name := setupJournalRecordName(journal.JournalID)
		path := filepath.Join(store.root, name)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrUnsafeSetupJournalStore) {
			t.Fatalf("Resumable(permissive record) error = %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("target", path); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrUnsafeSetupJournalStore) {
			t.Fatalf("Resumable(symlink record) error = %v", err)
		}
	})
	t.Run("interrupted temporary symlink", func(t *testing.T) {
		store, projectRoot := newSetupJournalTestStore(t)
		if _, err := store.Begin(context.Background(), projectRoot); err != nil {
			t.Fatal(err)
		}
		name := setupJournalTemporaryPrefix + strings.Repeat("a", 32)
		if err := os.Symlink("missing", filepath.Join(store.root, name)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrUnsafeSetupJournalStore) {
			t.Fatalf("Resumable(temporary symlink) error = %v, want ErrUnsafeSetupJournalStore", err)
		}
	})
}

func TestConfirmedPlanThirdStateAtPublishBoundaryIsPreserved(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	competitor := journal
	selection := testSetupSelection()
	selection.PublicationVisibility = "private_git"
	competitor.Selection = &selection
	competitor.UpdatedAt = competitor.UpdatedAt.Add(time.Second)
	competitorBytes, err := marshalCanonicalSetupJournal(competitor)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(store.root, setupJournalRecordName(journal.JournalID))
	fired := false
	store.fault = func(point string) error {
		if point == "write_before_publish" && !fired {
			fired = true
			return os.WriteFile(target, competitorBytes, 0o600)
		}
		return nil
	}
	if err := store.SetSelection(context.Background(), journal.JournalID, testSetupSelection()); !errors.Is(err, ErrConfirmedPlanDrift) {
		t.Fatalf("SetSelection(third state) error = %v, want ErrConfirmedPlanDrift", err)
	}
	if !fired {
		t.Fatal("publication boundary was not reached")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(competitorBytes) {
		t.Fatal("third-party canonical journal state was overwritten")
	}
}

func TestSetupJournalRejectsCorruptDuplicateUnknownAndAmbiguousRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"duplicate", func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `{"schema_version":1`, `{"schema_version":1,"schema_version":1`, 1))
		}},
		{"unknown", func(data []byte) []byte {
			return []byte(strings.Replace(string(data), `{"schema_version":1`, `{"future":true,"schema_version":1`, 1))
		}},
		{"trailing", func(data []byte) []byte { return append(data, []byte("{}\n")...) }},
		{"noncanonical", func(data []byte) []byte { return []byte(" " + string(data)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, projectRoot := newSetupJournalTestStore(t)
			journal, err := store.Begin(context.Background(), projectRoot)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.root, setupJournalRecordName(journal.JournalID))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, test.mutate(data), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotSetupJournalTree(t, store.root)
			if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrInvalidSetupJournal) {
				t.Fatalf("Resumable(corrupt) error = %v", err)
			}
			assertSetupJournalTreeEqual(t, store.root, before)
		})
	}

	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	copyJournal := journal
	copyJournal.JournalID = "50000000-0000-4000-8000-000000000001"
	copyJournal.CreatedAt = copyJournal.CreatedAt.Add(1)
	copyJournal.UpdatedAt = copyJournal.CreatedAt
	data, err := marshalCanonicalSetupJournal(copyJournal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, setupJournalRecordName(copyJournal.JournalID)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, rootFD, err := openSetupJournalRoot(store.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, readErr := readSetupJournalRecord(rootFD, setupJournalRecordName(copyJournal.JournalID)); readErr != nil || !exists {
		_ = closeSetupJournalFD(rootFD)
		t.Fatalf("ambiguous fixture is not a valid canonical journal: exists=%v err=%v", exists, readErr)
	}
	_ = closeSetupJournalFD(rootFD)
	before := snapshotSetupJournalTree(t, store.root)
	if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrAmbiguousSetupJournal) {
		t.Fatalf("Resumable(two active) error = %v", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
}

func TestSetupJournalRejectsLogicallyImpossibleReferencePrefix(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	journal, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	selection := testSetupSelection()
	journal.Selection = &selection
	journal.WorkspaceID = testSetupWorkspaceID
	journal.UpdatedAt = journal.UpdatedAt.Add(time.Second)
	data, err := marshalCanonicalSetupJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.root, setupJournalRecordName(journal.JournalID))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotSetupJournalTree(t, store.root)
	if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrInvalidSetupJournal) {
		t.Fatalf("Resumable(impossible binding prefix) error = %v, want ErrInvalidSetupJournal", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
}

func TestSetupJournalDoesNotRecoverUnconfirmedReplacementPair(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	old, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	selection := testSetupSelection()
	replacement := SetupJournal{
		SchemaVersion: setupJournalSchemaVersion,
		JournalID:     "50000000-0000-4000-8000-000000000001", CanonicalRoot: old.CanonicalRoot,
		State: SetupJournalActive, Selection: &selection, CompletedStages: []SetupStage{}, ConnectorBackups: []BackupReference{},
		ReplacesJournalID: old.JournalID, CreatedAt: old.CreatedAt.Add(time.Second), UpdatedAt: old.UpdatedAt.Add(time.Second),
	}
	data, err := marshalCanonicalSetupJournal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, setupJournalRecordName(replacement.JournalID)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotSetupJournalTree(t, store.root)
	if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrAmbiguousSetupJournal) {
		t.Fatalf("Resumable(unconfirmed replacement) error = %v, want ErrAmbiguousSetupJournal", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
}

func TestSetupJournalWholeStoreValidationPrecedesRecoveryWrites(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	old, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), old.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	old, _, err = store.Resumable(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}

	selection := testSetupSelection()
	replacement := SetupJournal{
		SchemaVersion:     setupJournalSchemaVersion,
		JournalID:         "50000000-0000-4000-8000-000000000001",
		CanonicalRoot:     old.CanonicalRoot,
		State:             SetupJournalActive,
		Selection:         &selection,
		CompletedStages:   []SetupStage{},
		ConnectorBackups:  []BackupReference{},
		ReplacesJournalID: old.JournalID,
		CreatedAt:         old.UpdatedAt,
		UpdatedAt:         old.UpdatedAt,
	}
	otherRoot := filepath.Join(t.TempDir(), "other-project")
	if err := os.Mkdir(otherRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	invalidTerminal := SetupJournal{
		SchemaVersion:       setupJournalSchemaVersion,
		JournalID:           "60000000-0000-4000-8000-000000000001",
		CanonicalRoot:       otherRoot,
		State:               SetupJournalReplaced,
		Selection:           &selection,
		CompletedStages:     []SetupStage{},
		ConnectorBackups:    []BackupReference{},
		ReplacedByJournalID: "70000000-0000-4000-8000-000000000001",
		CreatedAt:           old.CreatedAt,
		UpdatedAt:           old.UpdatedAt,
		CompletedAt:         timePointer(old.UpdatedAt),
	}
	writeSetupJournalFixture(t, store.root, replacement)
	writeSetupJournalFixture(t, store.root, invalidTerminal)
	temporaryName := setupJournalTemporaryPrefix + strings.Repeat("b", 32)
	temporaryPath := filepath.Join(store.root, temporaryName)
	if err := os.WriteFile(temporaryPath, []byte("recognized interrupted write"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotAllSetupJournalEntries(t, store.root)
	if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrInvalidSetupJournal) {
		t.Fatalf("Resumable(mixed invalid topology) error = %v, want ErrInvalidSetupJournal", err)
	}
	after := snapshotAllSetupJournalEntries(t, store.root)
	if !reflectSetupTreesEqual(before, after) {
		t.Fatalf("mixed invalid topology mutated store:\n got=%q\nwant=%q", after, before)
	}
}

func TestSetupJournalTopologyRecoveryActionsAreDeterministic(t *testing.T) {
	selection := testSetupSelection()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	pair := func(root, oldID, replacementID string) (SetupJournal, SetupJournal) {
		old := SetupJournal{
			SchemaVersion: setupJournalSchemaVersion, JournalID: oldID, CanonicalRoot: root,
			State: SetupJournalActive, Selection: &selection, CompletedStages: []SetupStage{}, ConnectorBackups: []BackupReference{},
			CreatedAt: now, UpdatedAt: now,
		}
		replacement := SetupJournal{
			SchemaVersion: setupJournalSchemaVersion, JournalID: replacementID, CanonicalRoot: root,
			State: SetupJournalActive, Selection: &selection, CompletedStages: []SetupStage{}, ConnectorBackups: []BackupReference{},
			ReplacesJournalID: oldID, CreatedAt: now, UpdatedAt: now,
		}
		return old, replacement
	}
	oldZ, replacementZ := pair("/z-project", "a1000000-0000-4000-8000-000000000001", "a2000000-0000-4000-8000-000000000001")
	oldA, replacementA := pair("/a-project", "b1000000-0000-4000-8000-000000000001", "b2000000-0000-4000-8000-000000000001")
	records := map[string]SetupJournal{
		oldZ.JournalID: oldZ, replacementA.JournalID: replacementA,
		replacementZ.JournalID: replacementZ, oldA.JournalID: oldA,
	}
	actions, err := classifySetupJournalTopology(records)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].predecessorID != oldA.JournalID || actions[0].successorID != replacementA.JournalID ||
		actions[1].predecessorID != oldZ.JournalID || actions[1].successorID != replacementZ.JournalID {
		t.Fatalf("recovery actions = %+v, want canonical-root order", actions)
	}
}

func TestSetupJournalMixedAmbiguousRootCannotPartiallyRecover(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	old, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), old.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	old, _, err = store.Resumable(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	selection := testSetupSelection()
	replacement := SetupJournal{
		SchemaVersion: setupJournalSchemaVersion, JournalID: "50000000-0000-4000-8000-000000000001", CanonicalRoot: old.CanonicalRoot,
		State: SetupJournalActive, Selection: &selection, CompletedStages: []SetupStage{}, ConnectorBackups: []BackupReference{},
		ReplacesJournalID: old.JournalID, CreatedAt: old.UpdatedAt, UpdatedAt: old.UpdatedAt,
	}
	otherRoot := filepath.Join(t.TempDir(), "ambiguous-project")
	if err := os.Mkdir(otherRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	firstOther := SetupJournal{
		SchemaVersion: setupJournalSchemaVersion, JournalID: "60000000-0000-4000-8000-000000000001", CanonicalRoot: otherRoot,
		State: SetupJournalActive, Selection: &selection, CompletedStages: []SetupStage{}, ConnectorBackups: []BackupReference{},
		CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt,
	}
	secondOther := cloneSetupJournal(firstOther)
	secondOther.JournalID = "70000000-0000-4000-8000-000000000001"
	for _, journal := range []SetupJournal{replacement, firstOther, secondOther} {
		writeSetupJournalFixture(t, store.root, journal)
	}
	before := snapshotAllSetupJournalEntries(t, store.root)
	if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrAmbiguousSetupJournal) {
		t.Fatalf("Resumable(mixed ambiguous roots) error = %v, want ErrAmbiguousSetupJournal", err)
	}
	if after := snapshotAllSetupJournalEntries(t, store.root); !reflectSetupTreesEqual(before, after) {
		t.Fatalf("mixed ambiguous roots mutated store:\n got=%q\nwant=%q", after, before)
	}
}

func TestSetupJournalRetiresRecognizedTemporaryFilesOnlyAfterValidation(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	if _, err := store.Begin(context.Background(), projectRoot); err != nil {
		t.Fatal(err)
	}
	for index, character := range []string{"a", "b", "c"} {
		name := setupJournalTemporaryPrefix + strings.Repeat(character, 32)
		if err := os.WriteFile(filepath.Join(store.root, name), []byte{byte(index + 1)}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Resumable(context.Background(), projectRoot); err != nil {
		t.Fatal(err)
	}
	for _, character := range []string{"a", "b", "c"} {
		name := setupJournalTemporaryPrefix + strings.Repeat(character, 32)
		if _, err := os.Lstat(filepath.Join(store.root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("recognized temporary %q was not retired: %v", name, err)
		}
	}
}

func TestSetupJournalTemporaryCleanupCanCrossRecordEntryCap(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	if _, err := store.Begin(context.Background(), projectRoot); err != nil {
		t.Fatal(err)
	}
	// Lock + record + these recognized temps exceeds the durable-record cap by
	// one. Cleanup must remain reachable under its own bounded temp allowance.
	for index := 0; index < maxSetupJournalStoreEntries-1; index++ {
		name := setupJournalTemporaryPrefix + fmt.Sprintf("%032x", index)
		if err := os.WriteFile(filepath.Join(store.root, name), []byte("interrupted"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.Resumable(context.Background(), projectRoot); err != nil {
		t.Fatalf("Resumable(over old total-entry cap with bounded temps): %v", err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries after temp cleanup = %d, want lock + record", len(entries))
	}
}

func TestSetupJournalRejectsReplacementAuthorityCycle(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	first, err := store.Begin(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSelection(context.Background(), first.JournalID, testSetupSelection()); err != nil {
		t.Fatal(err)
	}
	first, _, err = store.Resumable(context.Background(), projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	second := cloneSetupJournal(first)
	second.JournalID = "50000000-0000-4000-8000-000000000001"
	now := first.UpdatedAt.Add(time.Second)
	first.State = SetupJournalReplaced
	first.ReplacesJournalID = second.JournalID
	first.ReplacedByJournalID = second.JournalID
	first.UpdatedAt = now
	first.CompletedAt = timePointer(now)
	second.State = SetupJournalReplaced
	second.ReplacesJournalID = first.JournalID
	second.ReplacedByJournalID = first.JournalID
	second.CreatedAt = now
	second.UpdatedAt = now
	second.CompletedAt = timePointer(now)
	for _, journal := range []SetupJournal{first, second} {
		data, err := marshalCanonicalSetupJournal(journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(store.root, setupJournalRecordName(journal.JournalID)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotSetupJournalTree(t, store.root)
	if _, _, err := store.Resumable(context.Background(), projectRoot); !errors.Is(err, ErrInvalidSetupJournal) {
		t.Fatalf("Resumable(replacement cycle) error = %v, want ErrInvalidSetupJournal", err)
	}
	assertSetupJournalTreeEqual(t, store.root, before)
}

func TestSetupJournalFaultMatrixLeavesOldOrNewCanonicalRecord(t *testing.T) {
	for _, point := range []string{"write_before_publish", "write_after_publish", "write_after_directory_sync"} {
		t.Run(point, func(t *testing.T) {
			store, projectRoot := newSetupJournalTestStore(t)
			journal, err := store.Begin(context.Background(), projectRoot)
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotSetupJournalTree(t, store.root)
			fired := false
			store.fault = func(got string) error {
				if got == point && !fired {
					fired = true
					return errors.New("injected write fault")
				}
				return nil
			}
			_ = store.SetSelection(context.Background(), journal.JournalID, testSetupSelection())
			if !fired {
				t.Fatalf("fault point %q not reached", point)
			}
			after := snapshotSetupJournalTree(t, store.root)
			if !reflectSetupTreesEqual(before, after) {
				reopened, err := OpenSetupJournalStoreAt(store.root)
				if err != nil {
					t.Fatal(err)
				}
				got, ok, err := reopened.Resumable(context.Background(), projectRoot)
				if err != nil || !ok || got.Selection == nil {
					t.Fatalf("new fault state is not canonical/resumable: %+v %v %v", got, ok, err)
				}
			}
			store.fault = nil
			if err := store.SetSelection(context.Background(), journal.JournalID, testSetupSelection()); err != nil {
				t.Fatalf("retry after %s: %v", point, err)
			}
		})
	}
}

func TestSetupJournalConfirmedReplacementRecoversEveryWriteFault(t *testing.T) {
	for _, point := range []string{"replacement_after_new", "replacement_after_old"} {
		t.Run(point, func(t *testing.T) {
			store, projectRoot := newSetupJournalTestStore(t)
			old, err := store.Begin(context.Background(), projectRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetSelection(context.Background(), old.JournalID, testSetupSelection()); err != nil {
				t.Fatal(err)
			}
			fired := false
			store.fault = func(got string) error {
				if got == point && !fired {
					fired = true
					return errors.New("injected replacement fault")
				}
				return nil
			}
			_, _ = store.BeginConfirmedReplacement(context.Background(), projectRoot, old.JournalID, testSetupSelection())
			if !fired {
				t.Fatalf("fault point %q not reached", point)
			}
			reopened, err := OpenSetupJournalStoreAt(store.root)
			if err != nil {
				t.Fatal(err)
			}
			got, ok, err := reopened.Resumable(context.Background(), projectRoot)
			if err != nil || !ok || got.Selection == nil {
				t.Fatalf("Resumable after %s = %+v, %v, %v", point, got, ok, err)
			}
			if got.JournalID != old.JournalID && got.ReplacesJournalID != old.JournalID {
				t.Fatalf("unexpected recovery owner after %s: %+v", point, got)
			}
		})
	}
}

func TestSetupJournalOwnerLockSerializesConcurrentBegin(t *testing.T) {
	store, projectRoot := newSetupJournalTestStore(t)
	const workers = 12
	results := make(chan SetupJournal, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			journal, err := store.Begin(context.Background(), projectRoot)
			results <- journal
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	identifier := ""
	for journal := range results {
		if identifier == "" {
			identifier = journal.JournalID
		}
		if journal.JournalID != identifier {
			t.Fatalf("concurrent Begin returned journal %q, want %q", journal.JournalID, identifier)
		}
	}
}

func TestSetupJournalOwnerLockEntryReplacementIsRejected(t *testing.T) {
	store, _ := newSetupJournalTestStore(t)
	_, rootFD, err := openSetupJournalRoot(store.root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSetupJournalFD(rootFD)
	lockFD, err := unix.Openat(rootFD, setupJournalLockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(lockFD)
	if err := unix.Unlinkat(rootFD, setupJournalLockName, 0); err != nil {
		t.Fatal(err)
	}
	replacement, err := unix.Openat(rootFD, setupJournalLockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(replacement)
	if err := revalidateSetupJournalLock(rootFD, lockFD); !errors.Is(err, ErrUnsafeSetupJournalStore) {
		t.Fatalf("revalidate replaced lock error = %v", err)
	}
}

func reflectSetupTreesEqual(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for name, data := range left {
		if string(right[name]) != string(data) {
			return false
		}
	}
	return true
}

func writeSetupJournalFixture(t *testing.T, root string, journal SetupJournal) {
	t.Helper()
	data, err := marshalCanonicalSetupJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, setupJournalRecordName(journal.JournalID)), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func snapshotAllSetupJournalEntries(t *testing.T, root string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.Name() == setupJournalLockName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = data
	}
	return result
}
