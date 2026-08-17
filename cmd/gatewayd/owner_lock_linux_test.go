//go:build linux

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDatabaseOwnerLockCanonicalAliases(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		paths func(*testing.T) (string, string, string)
	}{
		{
			name: "relative and absolute",
			paths: func(t *testing.T) (string, string, string) {
				t.Helper()
				absolute := filepath.Join(t.TempDir(), "data", "gateway.db")
				relative, err := filepath.Rel(workingDirectory, absolute)
				if err != nil {
					t.Fatal(err)
				}
				return relative, absolute, absolute
			},
		},
		{
			name: "symlinked parent and canonical parent",
			paths: func(t *testing.T) (string, string, string) {
				t.Helper()
				root := t.TempDir()
				canonicalParent := filepath.Join(root, "canonical")
				if err := os.Mkdir(canonicalParent, 0o700); err != nil {
					t.Fatal(err)
				}
				aliasParent := filepath.Join(root, "alias")
				if err := os.Symlink(canonicalParent, aliasParent); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(aliasParent, "gateway.db"), filepath.Join(canonicalParent, "gateway.db"), filepath.Join(canonicalParent, "gateway.db")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			winnerPath, contenderPath, canonicalPath := tt.paths(t)
			winner, err := acquireDatabaseOwnerLock(winnerPath)
			if err != nil {
				t.Fatalf("acquire winner: %v", err)
			}
			defer winner.Close()
			if got := winner.DatabasePath(); got != canonicalPath {
				t.Fatalf("DatabasePath() = %q, want %q", got, canonicalPath)
			}
			contender, err := acquireDatabaseOwnerLock(contenderPath)
			if contender != nil {
				_ = contender.Close()
				t.Fatal("canonical alias acquired an independent owner lock")
			}
			if !errors.Is(err, errGatewayAlreadyRunning) {
				t.Fatalf("alias acquisition error = %v, want %v", err, errGatewayAlreadyRunning)
			}
		})
	}
}

func TestDatabaseOwnerLockPersistsAndReacquiresAfterClose(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "data", "gateway.db")
	lockPath := databasePath + ".lock"
	first, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	before := databaseOwnerLockFileInfo(t, lockPath)
	if got := before.Mode().Perm(); got != 0o600 {
		t.Fatalf("lock mode = %o, want 600", got)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	afterClose := databaseOwnerLockFileInfo(t, lockPath)
	if !os.SameFile(before, afterClose) {
		t.Fatal("lock inode changed or disappeared on close")
	}

	second, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatalf("reacquire after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close reacquired lock: %v", err)
	}
	afterReacquire := databaseOwnerLockFileInfo(t, lockPath)
	if !os.SameFile(before, afterReacquire) {
		t.Fatal("reacquisition replaced the persistent lock inode")
	}
}

func TestDatabaseOwnerLockCloseIsIdempotentAndConcurrent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "data", "gateway.db")
	lock, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	const closers = 16
	errors := make(chan error, closers)
	var group sync.WaitGroup
	for range closers {
		group.Add(1)
		go func() {
			defer group.Done()
			errors <- lock.Close()
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
	reacquired, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatalf("reacquire after concurrent Close: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseOwnerLockContentionIsNonblockingAndLoserDoesNotMutate(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "data", "gateway.db")
	winner, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer winner.Close()

	paths := []string{databasePath + ".lock", databasePath, databasePath + "-wal", databasePath + "-shm"}
	for index, path := range paths {
		if index != 0 {
			if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	before := databaseOwnerLockSnapshots(t, paths)

	started := time.Now()
	contender, err := acquireDatabaseOwnerLock(databasePath)
	elapsed := time.Since(started)
	if contender != nil {
		_ = contender.Close()
		t.Fatal("contender acquired held owner lock")
	}
	if !errors.Is(err, errGatewayAlreadyRunning) {
		t.Fatalf("contention error = %v, want %v", err, errGatewayAlreadyRunning)
	}
	if elapsed > time.Second {
		t.Fatalf("contention took %v, want nonblocking failure", elapsed)
	}
	after := databaseOwnerLockSnapshots(t, paths)
	for index := range paths {
		if before[index] != after[index] {
			t.Fatalf("loser mutated %s: before=%+v after=%+v", paths[index], before[index], after[index])
		}
	}
}

func TestDatabaseOwnerLockWinnerNormalizesOwnerFileModes(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "data", "gateway.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	paths := []string{databasePath + ".lock", databasePath, databasePath + "-wal", databasePath + "-shm"}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("owner file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
	}

	lock, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, path := range paths {
		if got := databaseOwnerLockFileInfo(t, path).Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, got)
		}
	}
}

func TestDatabaseOwnerLockRejectsUnsafeParent(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) string
	}{
		{
			name: "regular file",
			setup: func(t *testing.T) string {
				t.Helper()
				parent := filepath.Join(t.TempDir(), "data")
				if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(parent, "gateway.db")
			},
		},
		{
			name: "non-owner-only mode",
			setup: func(t *testing.T) string {
				t.Helper()
				parent := filepath.Join(t.TempDir(), "data")
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(parent, 0o755); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(parent, "gateway.db")
			},
		},
		{
			name: "wrong owner",
			setup: func(t *testing.T) string {
				t.Helper()
				if os.Geteuid() == 0 {
					t.Skip("requires a parent owned by a different effective user")
				}
				return filepath.Join(string(filepath.Separator), "gateway-owner-lock-wrong-owner.db")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			databasePath := tt.setup(t)
			lock, err := acquireDatabaseOwnerLock(databasePath)
			if lock != nil {
				_ = lock.Close()
				t.Fatal("unsafe parent acquired owner lock")
			}
			if err == nil {
				t.Fatal("unsafe parent was accepted")
			}
		})
	}
}

func TestDatabaseOwnerLockRejectsUnsafeLockDatabaseAndSidecars(t *testing.T) {
	targets := []struct {
		name   string
		suffix string
	}{
		{name: "lock", suffix: ".lock"},
		{name: "database", suffix: ""},
		{name: "wal", suffix: "-wal"},
		{name: "shm", suffix: "-shm"},
	}
	shapes := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "symlink-target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fifo",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hard link",
			setup: func(t *testing.T, path string) {
				t.Helper()
				source := filepath.Join(filepath.Dir(path), "hard-link-source")
				if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(source, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, target := range targets {
		for _, shape := range shapes {
			t.Run(target.name+"/"+shape.name, func(t *testing.T) {
				root := t.TempDir()
				parent := filepath.Join(root, "data")
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(parent, "gateway.db")
				shape.setup(t, path+target.suffix)
				lock, err := acquireDatabaseOwnerLock(path)
				if lock != nil {
					_ = lock.Close()
					t.Fatal("unsafe owner file acquired database lock")
				}
				if err == nil {
					t.Fatal("unsafe owner file was accepted")
				}
			})
		}
	}
}

func TestDatabaseOwnerLockRejectsWrongOwnerMetadata(t *testing.T) {
	wrongOwner := uint32(os.Geteuid() + 1)
	for _, name := range []string{"gateway.db.lock", "gateway.db", "gateway.db-wal", "gateway.db-shm"} {
		t.Run(name, func(t *testing.T) {
			stat := unix.Stat_t{Mode: unix.S_IFREG | 0o600, Nlink: 1, Uid: wrongOwner}
			if err := validateDatabaseOwnerFileStat(name, &stat, true); err == nil {
				t.Fatal("wrong-owner metadata was accepted")
			}
		})
	}
}

func TestDatabaseOwnerLockRunLosesBeforeDatabaseOrSocketMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runtimeDir := filepath.Join(home, "run")
	dataHome := filepath.Join(home, "data")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("XDG_DATA_HOME", dataHome)
	databasePath := filepath.Join(dataHome, "wormhole", "wormholed.db")
	winner, err := acquireDatabaseOwnerLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer winner.Close()

	socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const sentinel = "preserve socket path"
	if err := os.WriteFile(socketPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runWithSyncEngineFactory(t.Context(), "default", nil)
	if !errors.Is(err, errGatewayAlreadyRunning) {
		t.Fatalf("run error = %v, want %v", err, errGatewayAlreadyRunning)
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("losing daemon mutated %s: %v", path, statErr)
		}
	}
	contents, err := os.ReadFile(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != sentinel {
		t.Fatalf("socket sentinel = %q, want %q", contents, sentinel)
	}
}

func TestDatabaseOwnerLockRunRejectsUnsafeOwnerFilesBeforeSocketMutation(t *testing.T) {
	tests := []struct {
		name         string
		suffix       string
		safeSuffixes []string
		setup        func(*testing.T, string)
	}{
		{
			name:   "database symlink",
			suffix: "",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:         "wal hard link",
			suffix:       "-wal",
			safeSuffixes: []string{""},
			setup: func(t *testing.T, path string) {
				t.Helper()
				source := filepath.Join(filepath.Dir(path), "source")
				if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(source, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:         "shm directory",
			suffix:       "-shm",
			safeSuffixes: []string{"", "-wal"},
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, err := os.MkdirTemp("", "gateway-lock-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			t.Setenv("HOME", home)
			runtimeDir := filepath.Join(home, "run")
			dataHome := filepath.Join(home, "data")
			t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
			t.Setenv("XDG_DATA_HOME", dataHome)
			databasePath := filepath.Join(dataHome, "wormhole", "wormholed.db")
			if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
				t.Fatal(err)
			}
			preservedPaths := []string{databasePath + ".lock"}
			for _, suffix := range append([]string{".lock"}, tt.safeSuffixes...) {
				path := databasePath + suffix
				if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
				if suffix != ".lock" {
					preservedPaths = append(preservedPaths, path)
				}
			}
			unsafePath := databasePath + tt.suffix
			tt.setup(t, unsafePath)
			preservedPaths = append(preservedPaths, unsafePath)
			ownerFilesBefore := databaseOwnerLockSnapshots(t, preservedPaths)

			socketPath := filepath.Join(runtimeDir, "wormhole", "wormholed.sock")
			if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
				t.Fatal(err)
			}
			address, err := net.ResolveUnixAddr("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			stale, err := net.ListenUnix("unix", address)
			if err != nil {
				t.Fatal(err)
			}
			stale.SetUnlinkOnClose(false)
			if err := stale.Close(); err != nil {
				t.Fatal(err)
			}
			socketBefore := databaseOwnerLockFileInfo(t, socketPath)

			if err := runWithSyncEngineFactory(t.Context(), "default", nil); err == nil {
				t.Fatal("run accepted unsafe database owner file")
			}
			ownerFilesAfter := databaseOwnerLockSnapshots(t, preservedPaths)
			for index := range preservedPaths {
				if ownerFilesBefore[index] != ownerFilesAfter[index] {
					t.Fatalf("unsafe acquisition mutated %s: before=%+v after=%+v", preservedPaths[index], ownerFilesBefore[index], ownerFilesAfter[index])
				}
			}
			if socketAfter := databaseOwnerLockFileInfo(t, socketPath); !os.SameFile(socketBefore, socketAfter) {
				t.Fatal("unsafe database owner entry allowed stale-socket mutation")
			}
		})
	}
}

type databaseOwnerLockSnapshot struct {
	mode  os.FileMode
	size  int64
	inode uint64
}

func databaseOwnerLockSnapshots(t *testing.T, paths []string) []databaseOwnerLockSnapshot {
	t.Helper()
	snapshots := make([]databaseOwnerLockSnapshot, 0, len(paths))
	for _, path := range paths {
		info := databaseOwnerLockFileInfo(t, path)
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("%s has unexpected stat type %T", path, info.Sys())
		}
		snapshots = append(snapshots, databaseOwnerLockSnapshot{mode: info.Mode(), size: info.Size(), inode: stat.Ino})
	}
	return snapshots
}

func databaseOwnerLockFileInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
