//go:build linux || darwin

package localidentity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"golang.org/x/sys/unix"
)

func TestOpenRejectsInsecureOwnerOnlyRootsAndRecords(t *testing.T) {
	t.Run("relative root", func(t *testing.T) {
		if _, err := Open("relative"); !errors.Is(err, ErrUnsafeStore) {
			t.Fatalf("Open(relative) error = %v, want ErrUnsafeStore", err)
		}
	})
	t.Run("symlink root", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(parent, "identity")
		if err := os.Symlink(target, root); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(root); !errors.Is(err, ErrUnsafeStore) {
			t.Fatalf("Open(symlink) error = %v, want ErrUnsafeStore", err)
		}
	})
	t.Run("symlink ancestor creates nothing through link", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(parent, "linked")
		if err := os.Symlink(target, linked); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(filepath.Join(linked, "identity")); !errors.Is(err, ErrUnsafeStore) {
			t.Fatalf("Open(symlink ancestor) error = %v, want ErrUnsafeStore", err)
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("symlink target mutated: %v", entries)
		}
	})
	t.Run("permissive root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "identity")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(root); !errors.Is(err, ErrUnsafeStore) {
			t.Fatalf("Open(permissive) error = %v, want ErrUnsafeStore", err)
		}
	})
	t.Run("root missing owner execute", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "identity")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(root); !errors.Is(err, ErrUnsafeStore) {
			t.Fatalf("Open(non-executable) error = %v, want ErrUnsafeStore", err)
		}
	})
	t.Run("permissive selected record", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "identity")
		store, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, selectedRecordName), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Selected(context.Background()); !errors.Is(err, ErrUnsafeStore) {
			t.Fatalf("Selected(permissive) error = %v, want ErrUnsafeStore", err)
		}
	})
	t.Run("executable selected record", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "identity")
		store, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, selectedRecordName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Selected(context.Background()); !errors.Is(err, ErrUnsafeStore) {
			t.Fatalf("Selected(executable) error = %v, want ErrUnsafeStore", err)
		}
	})
}

func TestEnsureSelectedRecoversEveryDurableWriteFailureBoundary(t *testing.T) {
	for boundary := 1; boundary <= 4; boundary++ {
		t.Run("boundary", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "identity")
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			realAtomicWrite := store.atomicWrite
			writes := 0
			injected := false
			store.atomicWrite = func(fd int, name string, data []byte, mode os.FileMode, replace bool) error {
				writes++
				if err := realAtomicWrite(fd, name, data, mode, replace); err != nil {
					return err
				}
				if writes == boundary {
					injected = true
					return errors.New("injected durable write failure")
				}
				return nil
			}
			_, _ = store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
			if !injected {
				t.Fatalf("boundary %d was not reached; writes=%d", boundary, writes)
			}
			prefix := snapshotIdentityTree(t, root)
			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := reopened.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
			if err != nil {
				t.Fatalf("recover boundary %d: %v", boundary, err)
			}
			assertRecoveredIdentityPrefix(t, root, testJournalID, testSelection(), prefix, profile)
			again, err := reopened.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
			if err != nil {
				t.Fatal(err)
			}
			if profile.HumanPrincipalID != again.HumanPrincipalID || string(profile.PublicKey) != string(again.PublicKey) {
				t.Fatalf("boundary %d replay profile changed", boundary)
			}
		})
	}
}

func TestEnsureSelectedRecoversPreWriteFailureBoundaries(t *testing.T) {
	for boundary := 1; boundary <= 4; boundary++ {
		t.Run("boundary", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "identity")
			store, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			realAtomicWrite := store.atomicWrite
			writes := 0
			store.atomicWrite = func(fd int, name string, data []byte, mode os.FileMode, replace bool) error {
				writes++
				if writes == boundary {
					return errors.New("injected pre-write failure")
				}
				return realAtomicWrite(fd, name, data, mode, replace)
			}
			if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); err == nil {
				t.Fatalf("boundary %d returned nil after %d writes", boundary, writes)
			}
			prefix := snapshotIdentityTree(t, root)
			reopened, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := reopened.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection())
			if err != nil {
				t.Fatalf("recover boundary %d: %v", boundary, err)
			}
			assertRecoveredIdentityPrefix(t, root, testJournalID, testSelection(), prefix, profile)
		})
	}
}

func assertRecoveredIdentityPrefix(t *testing.T, root, journalID string, selection types.ConfirmedIdentitySelection, prefix map[string][]byte, profile PublicHumanProfile) {
	t.Helper()
	after := snapshotIdentityTree(t, root)
	for name, beforeBytes := range prefix {
		if afterBytes, exists := after[name]; !exists || !reflect.DeepEqual(afterBytes, beforeBytes) {
			t.Fatalf("durable prefix %q changed (got %d bytes, want %d bytes)", name, len(afterBytes), len(beforeBytes))
		}
	}
	receipt, exists, err := readSetupRecordFromRoot(root, journalID)
	if err != nil || !exists {
		t.Fatalf("recovered receipt missing or invalid: exists=%v err=%v", exists, err)
	}
	if receipt.HumanPrincipalID != profile.HumanPrincipalID || receipt.Selection != selection {
		t.Fatal("recovered receipt does not match the public profile")
	}
	selected, exists, err := readSelectedRecordFromRoot(root)
	if err != nil || !exists || selected.HumanPrincipalID != profile.HumanPrincipalID {
		t.Fatalf("recovered selected pointer missing or invalid: exists=%v err=%v", exists, err)
	}
	assertOneIdentityAuthority(t, root, profile.HumanPrincipalID)
}

func TestEnsureSelectedFailsClosedWhenIdentityRootEntryLimitIsExceeded(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxLocalIdentityStoreEntries; index++ {
		name := fmt.Sprintf(".tmp-%032x", index)
		if err := os.WriteFile(filepath.Join(root, name), []byte("interrupted publication"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotIdentityTree(t, root)
	if _, err := store.EnsureSelectedForSetup(context.Background(), testJournalID, testSelection()); !errors.Is(err, ErrInvalidStoreRecord) {
		t.Fatalf("entry-limit error = %v, want ErrInvalidStoreRecord", err)
	}
	assertIdentityTreeEqual(t, root, before)
}

func readSelectedRecordFromRoot(root string) (selectedRecord, bool, error) {
	_, fd, err := openLocalIdentityRoot(root)
	if err != nil {
		return selectedRecord{}, false, err
	}
	defer closeLocalIdentityFD(fd)
	return readSelectedRecord(fd)
}

func TestOwnerOnlyFilesystemPrimitivesRejectUnsafeObjectsAndSerialize(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	_, fd, err := openLocalIdentityRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLocalIdentityFD(fd)
	constantRandom := func(data []byte) (int, error) {
		for index := range data {
			data[index] = byte(index + 1)
		}
		return len(data), nil
	}
	if err := atomicLocalIdentityWrite(fd, "record.json", []byte("first"), 0o600, false, constantRandom); err != nil {
		t.Fatalf("create record: %v", err)
	}
	if err := atomicLocalIdentityWrite(fd, "record.json", []byte("second"), 0o600, false, constantRandom); !errors.Is(err, ErrSetupIdentityDrift) {
		t.Fatalf("create collision error = %v, want ErrSetupIdentityDrift", err)
	}
	if err := atomicLocalIdentityWrite(fd, "record.json", []byte("second"), 0o600, true, constantRandom); err != nil {
		t.Fatalf("replace record: %v", err)
	}
	data, exists, err := readLocalIdentityFile(fd, "record.json")
	if err != nil || !exists || string(data) != "second" {
		t.Fatalf("read record missing, invalid, or unexpected size: exists=%v bytes=%d err=%v", exists, len(data), err)
	}
	if _, _, err := readLocalIdentityFile(fd, "../escape"); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("unsafe record name error = %v, want ErrUnsafeStore", err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLocalIdentityFile(fd, "directory"); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("directory record error = %v, want ErrUnsafeStore", err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.json"), make([]byte, maxLocalIdentityRecordBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readLocalIdentityFile(fd, "large.json"); !errors.Is(err, ErrInvalidStoreRecord) {
		t.Fatalf("oversized record error = %v, want ErrInvalidStoreRecord", err)
	}
	if err := atomicLocalIdentityWrite(fd, "bad/name", []byte("x"), 0o600, false, constantRandom); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("unsafe write name error = %v, want ErrUnsafeStore", err)
	}
	if err := atomicLocalIdentityWrite(fd, "bad.json", []byte("x"), 0o644, false, constantRandom); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("unsafe write mode error = %v, want ErrUnsafeStore", err)
	}

	unlock, err := lockLocalIdentityStore(context.Background(), fd)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-time.After(20 * time.Millisecond)
		cancel()
	}()
	if _, err := lockLocalIdentityStore(ctx, fd); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled second lock error = %v, want context.Canceled", err)
	}
}

func TestUnsafeLockIsRejectedWithoutModeMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	_, fd, err := openLocalIdentityRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLocalIdentityFD(fd)
	lockPath := filepath.Join(root, lockRecordName)
	if err := os.WriteFile(lockPath, []byte("unsafe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockLocalIdentityStore(context.Background(), fd); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("unsafe lock error = %v, want ErrUnsafeStore", err)
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unsafe lock mode mutated to %o", info.Mode().Perm())
	}
}

func TestStoreLockRevalidationRejectsReplacedEntry(t *testing.T) {
	root := filepath.Join(t.TempDir(), "identity")
	_, rootFD, err := openLocalIdentityRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLocalIdentityFD(rootFD)
	lockFD, err := unix.Openat(rootFD, lockRecordName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(lockFD)
	if err := unix.Unlinkat(rootFD, lockRecordName, 0); err != nil {
		t.Fatal(err)
	}
	replacementFD, err := unix.Openat(rootFD, lockRecordName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(replacementFD); err != nil {
		t.Fatal(err)
	}
	if err := revalidateLocalIdentityLock(rootFD, lockFD); !errors.Is(err, ErrUnsafeStore) {
		t.Fatalf("revalidate replaced lock error = %v, want ErrUnsafeStore", err)
	}
}
