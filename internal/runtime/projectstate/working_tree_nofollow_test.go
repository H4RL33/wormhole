package projectstate

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestReadWorkingTreeNoFollowReturnsSortedExactBytes(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{
		"z-last.json":                 []byte("not canonical\r\n"),
		"state/v1/tasks/b.json":       []byte("{ \"raw\": true }"),
		"config.toml":                 []byte("raw config without newline"),
		"state/v1/tasks/links/a.json": []byte{0, 1, 2, 3},
	})

	got, err := ReadWorkingTreeNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	want := state.Tree{
		{Path: "config.toml", Data: []byte("raw config without newline")},
		{Path: "state/v1/tasks/b.json", Data: []byte("{ \"raw\": true }")},
		{Path: "state/v1/tasks/links/a.json", Data: []byte{0, 1, 2, 3}},
		{Path: "z-last.json", Data: []byte("not canonical\r\n")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadWorkingTreeNoFollow() = %#v, want %#v", got, want)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Path < got[j].Path }) {
		t.Fatalf("working-tree paths are not sorted: %#v", got)
	}
}

func TestReadWorkingTreeNoFollowMissingWormholeReturnsNonNilEmptyTree(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	got, err := ReadWorkingTreeNoFollow(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ReadWorkingTreeNoFollow() = %#v, want non-nil empty tree", got)
	}
}

func TestReadWorkingTreeNoFollowAbsentWormholeBecomesSymlink(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	outside := t.TempDir()
	created := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeBeforeAbsentRecheck || relativePath != ".wormhole" || created {
			return nil
		}
		created = true
		return os.Symlink(outside, filepath.Join(root, ".wormhole"))
	}

	_, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook)
	if !errors.Is(err, ErrWorkingTreeChanged) || !errors.Is(err, ErrUnsafeWorkingTree) {
		t.Fatalf("absent-to-symlink race error = %v, want changed and unsafe", err)
	}
}

func TestReadWorkingTreeNoFollowAbsentWormholeRevalidatesCheckoutAfterHook(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "checkout")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	replaced := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeBeforeAbsentRecheck || relativePath != ".wormhole" || replaced {
			return nil
		}
		replaced = true
		if err := os.Rename(root, filepath.Join(parent, "checkout-old")); err != nil {
			return err
		}
		return os.Mkdir(root, 0o700)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("checkout replacement error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowCapturesUnknownRegularPath(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	want := []byte("unknown bytes\n")
	writeWorkingTreeFixture(t, root, map[string][]byte{"unknown/path.data": want})

	got, err := ReadWorkingTreeNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "unknown/path.data" || !bytes.Equal(got[0].Data, want) {
		t.Fatalf("ReadWorkingTreeNoFollow() = %#v", got)
	}
}

func TestReadWorkingTreeNoFollowDoesNotAliasReturnedBytes(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	want := []byte("original bytes")
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": want})

	first, err := ReadWorkingTreeNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Data[0] = 'X'
	second, err := ReadWorkingTreeNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second[0].Data, want) {
		t.Fatalf("second read = %q, want %q", second[0].Data, want)
	}
}

func TestReadWorkingTreeNoFollowFrozenLimits(t *testing.T) {
	want := workingTreeLimits{
		maxFiles: 10_000, maxDirectories: 10_016, maxPathBytes: 4 << 10,
		maxPathDepth: 5, maxFileBytes: 16 << 20, maxTotalBytes: 64 << 20,
	}
	if got := defaultWorkingTreeLimits(); got != want {
		t.Fatalf("defaultWorkingTreeLimits() = %+v, want %+v", got, want)
	}
}

func TestReadWorkingTreeNoFollowEnforcesEachLimitAtBoundary(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{
		"a/one": []byte("1"),
		"b/two": []byte("22"),
	})

	base := defaultWorkingTreeLimits()
	tests := []struct {
		name   string
		change func(*workingTreeLimits)
	}{
		{name: "files", change: func(limits *workingTreeLimits) { limits.maxFiles = 1 }},
		{name: "directories", change: func(limits *workingTreeLimits) { limits.maxDirectories = 1 }},
		{name: "path bytes", change: func(limits *workingTreeLimits) { limits.maxPathBytes = len("a/one") - 1 }},
		{name: "path depth", change: func(limits *workingTreeLimits) { limits.maxPathDepth = 1 }},
		{name: "file bytes", change: func(limits *workingTreeLimits) { limits.maxFileBytes = 1 }},
		{name: "total bytes", change: func(limits *workingTreeLimits) { limits.maxTotalBytes = int64(len("a/one") + 1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := base
			test.change(&limits)
			if _, err := readWorkingTreeNoFollow(root, limits, nil); !errors.Is(err, ErrWorkingTreeLimit) {
				t.Fatalf("readWorkingTreeNoFollow() error = %v, want ErrWorkingTreeLimit", err)
			}
		})
	}

	boundary := base
	boundary.maxFiles = 2
	boundary.maxDirectories = 2
	boundary.maxPathBytes = len("a/one")
	boundary.maxPathDepth = 2
	boundary.maxFileBytes = 2
	boundary.maxTotalBytes = int64(len("a/one") + 1 + len("b/two") + 2)
	if _, err := readWorkingTreeNoFollow(root, boundary, nil); err != nil {
		t.Fatalf("readWorkingTreeNoFollow() rejected exact limits: %v", err)
	}
}

func TestReadWorkingTreeNoFollowBoundsDirectoryEnumerationBeforeTraversal(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{
		"one": []byte("1"), "two": []byte("2"), "three": []byte("3"),
	})
	limits := defaultWorkingTreeLimits()
	limits.maxFiles = 1
	limits.maxDirectories = 1
	visited := 0
	hook := func(stage workingTreeReadStage, _ string) error {
		if stage == workingTreeAfterEntryStat {
			visited++
		}
		return nil
	}

	_, err := readWorkingTreeNoFollow(root, limits, hook)
	if !errors.Is(err, ErrWorkingTreeLimit) {
		t.Fatalf("overfull directory error = %v, want ErrWorkingTreeLimit", err)
	}
	if visited != 0 {
		t.Fatalf("overfull directory visited %d entries before enforcing its bound", visited)
	}
}

func TestReadWorkingTreeNoFollowRejectsRootAncestorAndEntrySymlinks(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	t.Run("root", func(t *testing.T) {
		realRoot := t.TempDir()
		linkedRoot := filepath.Join(t.TempDir(), "linked")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadWorkingTreeNoFollow(linkedRoot); !errors.Is(err, ErrUnsafeWorkingTree) {
			t.Fatalf("root symlink error = %v, want ErrUnsafeWorkingTree", err)
		}
	})

	t.Run("wormhole", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, ".wormhole")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadWorkingTreeNoFollow(root); !errors.Is(err, ErrUnsafeWorkingTree) {
			t.Fatalf(".wormhole symlink error = %v, want ErrUnsafeWorkingTree", err)
		}
	})

	t.Run("ancestor", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".wormhole"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, ".wormhole", "state")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadWorkingTreeNoFollow(root); !errors.Is(err, ErrUnsafeWorkingTree) {
			t.Fatalf("ancestor symlink error = %v, want ErrUnsafeWorkingTree", err)
		}
	})

	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, ".wormhole"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, ".wormhole", "config.toml")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadWorkingTreeNoFollow(root); !errors.Is(err, ErrUnsafeWorkingTree) {
			t.Fatalf("file symlink error = %v, want ErrUnsafeWorkingTree", err)
		}
	})
}

func TestReadWorkingTreeNoFollowRejectsNonRegularAndHardLinkedFiles(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	t.Run("socket", func(t *testing.T) {
		root := t.TempDir()
		wormhole := filepath.Join(root, ".wormhole")
		if err := os.Mkdir(wormhole, 0o700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", filepath.Join(wormhole, "socket"))
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if _, err := ReadWorkingTreeNoFollow(root); !errors.Is(err, ErrUnsafeWorkingTree) {
			t.Fatalf("socket error = %v, want ErrUnsafeWorkingTree", err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		root := t.TempDir()
		writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("bytes")})
		if err := os.Link(filepath.Join(root, ".wormhole", "config.toml"), filepath.Join(root, "alias")); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadWorkingTreeNoFollow(root); !errors.Is(err, ErrUnsafeWorkingTree) {
			t.Fatalf("hard-link error = %v, want ErrUnsafeWorkingTree", err)
		}
	})
}

func TestReadWorkingTreeNoFollowRejectsUnsafePathNames(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	tests := []string{"back\\slash", "line\nbreak", string([]byte{0xff})}
	for _, name := range tests {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			root := t.TempDir()
			writeWorkingTreeFixture(t, root, map[string][]byte{name: []byte("bytes")})
			if _, err := ReadWorkingTreeNoFollow(root); !errors.Is(err, ErrUnsafeWorkingTree) {
				t.Fatalf("unsafe name %q error = %v, want ErrUnsafeWorkingTree", name, err)
			}
		})
	}
}

func TestReadWorkingTreeNoFollowDetectsFileReplacementRace(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("inside")})
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterEntryStat || relativePath != "config.toml" || replaced {
			return nil
		}
		replaced = true
		path := filepath.Join(root, ".wormhole", "config.toml")
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Symlink(outside, path)
	}

	_, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook)
	if !errors.Is(err, ErrWorkingTreeChanged) || !errors.Is(err, ErrUnsafeWorkingTree) {
		t.Fatalf("replacement race error = %v, want changed and unsafe", err)
	}
}

func TestReadWorkingTreeNoFollowDetectsMutationDuringRead(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("before")})
	changed := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterFileRead || relativePath != "config.toml" || changed {
			return nil
		}
		changed = true
		return os.WriteFile(filepath.Join(root, ".wormhole", "config.toml"), []byte("after and longer"), 0o600)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("mutation race error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowDetectsHardLinkRace(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	filePath := filepath.Join(root, ".wormhole", "config.toml")
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("before")})
	linked := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterFileRead || relativePath != "config.toml" || linked {
			return nil
		}
		linked = true
		return os.Link(filePath, filepath.Join(root, "outside-alias"))
	}

	_, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook)
	if !errors.Is(err, ErrWorkingTreeChanged) || !errors.Is(err, ErrUnsafeWorkingTree) {
		t.Fatalf("hard-link race error = %v, want changed and unsafe", err)
	}
}

func TestReadWorkingTreeNoFollowFinalVerificationDetectsEarlierFileMutationFromLaterEntry(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	aPath := filepath.Join(root, ".wormhole", "a")
	writeWorkingTreeFixture(t, root, map[string][]byte{"a": []byte("before"), "z": []byte("later")})
	changed := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterFileRead || relativePath != "z" || changed {
			return nil
		}
		changed = true
		return os.WriteFile(aPath, []byte("second"), 0o600)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("later-entry mutation error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowFinalVerificationDetectsEarlierHardLinkFromLaterEntry(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	aPath := filepath.Join(root, ".wormhole", "a")
	writeWorkingTreeFixture(t, root, map[string][]byte{"a": []byte("before"), "z": []byte("later")})
	linked := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterFileRead || relativePath != "z" || linked {
			return nil
		}
		linked = true
		return os.Link(aPath, filepath.Join(root, "outside-alias"))
	}

	_, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook)
	if !errors.Is(err, ErrWorkingTreeChanged) || !errors.Is(err, ErrUnsafeWorkingTree) {
		t.Fatalf("later-entry hard-link error = %v, want changed and unsafe", err)
	}
}

func TestReadWorkingTreeNoFollowFinalVerificationDetectsIdenticalByteInodeReplacement(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	aPath := filepath.Join(root, ".wormhole", "a", "file")
	writeWorkingTreeFixture(t, root, map[string][]byte{"a/file": []byte("same bytes"), "z": []byte("later")})
	replaced := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterFileRead || relativePath != "z" || replaced {
			return nil
		}
		replaced = true
		replacement := filepath.Join(root, ".wormhole", "a", "replacement")
		if err := os.WriteFile(replacement, []byte("same bytes"), 0o600); err != nil {
			return err
		}
		return os.Rename(replacement, aPath)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("identical-byte inode replacement error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowDetectsSameSizeMutationAfterEntryStat(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("before")})
	changed := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterEntryStat || relativePath != "config.toml" || changed {
			return nil
		}
		changed = true
		return os.WriteFile(filepath.Join(root, ".wormhole", "config.toml"), []byte("second"), 0o600)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("same-size mutation error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowDetectsDirectoryInventoryRace(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("before")})
	changed := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeBeforeDirectoryRecheck || relativePath != "." || changed {
			return nil
		}
		changed = true
		return os.WriteFile(filepath.Join(root, ".wormhole", "appeared"), []byte("new"), 0o600)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("inventory race error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowDetectsTransientDirectoryInventoryRace(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("before")})
	changed := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeBeforeDirectoryRecheck || relativePath != "." || changed {
			return nil
		}
		changed = true
		transient := filepath.Join(root, ".wormhole", "transient")
		if err := os.WriteFile(transient, []byte("brief"), 0o600); err != nil {
			return err
		}
		return os.Remove(transient)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("transient inventory race error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowDetectsWormholeDirectoryReplacement(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("before")})
	replaced := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeBeforeDirectoryRecheck || relativePath != "." || replaced {
			return nil
		}
		replaced = true
		oldPath := filepath.Join(root, ".wormhole-old")
		if err := os.Rename(filepath.Join(root, ".wormhole"), oldPath); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(root, ".wormhole"), 0o700)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); !errors.Is(err, ErrWorkingTreeChanged) {
		t.Fatalf("directory replacement error = %v, want ErrWorkingTreeChanged", err)
	}
}

func TestReadWorkingTreeNoFollowIgnoresUnrelatedAncestorEntryChanges(t *testing.T) {
	requireSecureWorkingTreePlatform(t)
	root := t.TempDir()
	writeWorkingTreeFixture(t, root, map[string][]byte{"config.toml": []byte("stable")})
	created := false
	hook := func(stage workingTreeReadStage, relativePath string) error {
		if stage != workingTreeAfterFileRead || relativePath != "config.toml" || created {
			return nil
		}
		created = true
		return os.Mkdir(filepath.Join(filepath.Dir(root), "unrelated-sibling"), 0o700)
	}

	if _, err := readWorkingTreeNoFollow(root, defaultWorkingTreeLimits(), hook); err != nil {
		t.Fatalf("unrelated ancestor entry change rejected: %v", err)
	}
}

func TestReadWorkingTreeNoFollowUnsupportedPlatformFailsClosed(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		t.Skip("secure descriptor-relative implementation is available")
	}
	if _, err := ReadWorkingTreeNoFollow(t.TempDir()); !errors.Is(err, ErrWorkingTreeFilesystemUnsupported) {
		t.Fatalf("ReadWorkingTreeNoFollow() error = %v, want ErrWorkingTreeFilesystemUnsupported", err)
	}
}

func TestWorkingTreeChangedIOErrorPreservesCauseAndUnsafeClassification(t *testing.T) {
	err := workingTreeChangedIOError("open", "config.toml", os.ErrPermission, false)
	if !errors.Is(err, ErrWorkingTreeChanged) || !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrUnsafeWorkingTree) {
		t.Fatalf("ordinary I/O error = %v, want changed+permission without unsafe", err)
	}

	cause := errors.New("symlink replacement")
	err = workingTreeChangedIOError("open", "config.toml", cause, true)
	if !errors.Is(err, ErrWorkingTreeChanged) || !errors.Is(err, ErrUnsafeWorkingTree) || !errors.Is(err, cause) {
		t.Fatalf("unsafe race error = %v, want changed+unsafe+cause", err)
	}
}

func requireSecureWorkingTreePlatform(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("secure descriptor-relative working-tree reader is unavailable")
	}
}

func writeWorkingTreeFixture(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for relativePath, data := range files {
		fullPath := filepath.Join(root, ".wormhole", filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
