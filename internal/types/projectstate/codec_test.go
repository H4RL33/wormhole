package projectstate

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func readFixtureTree(t *testing.T, root string) Tree {
	t.Helper()
	baseRoot := root
	if !strings.Contains(filepath.ToSlash(root), "/valid/") {
		baseRoot = "testdata/v1/valid/.wormhole"
	}
	files := readTreeDirectory(t, baseRoot)
	if baseRoot != root {
		for _, override := range readTreeDirectory(t, root) {
			replaced := false
			for i := range files {
				if files[i].Path == override.Path {
					files[i] = override
					replaced = true
					break
				}
			}
			if !replaced {
				files = append(files, override)
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func readTreeDirectory(t *testing.T, root string) Tree {
	t.Helper()
	var tree Tree
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		tree = append(tree, File{Path: filepath.ToSlash(relative), Data: data})
		return nil
	})
	if err != nil {
		t.Fatalf("read fixture %s: %v", root, err)
	}
	return tree
}

func assertTreeEqual(t *testing.T, want, got Tree) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("tree length = %d, want %d\ngot=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Path != want[i].Path || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("tree file %d differs\ngot %q: %q\nwant %q: %q", i, got[i].Path, got[i].Data, want[i].Path, want[i].Data)
		}
	}
}

func TestCanonicalV1RoundTrip(t *testing.T) {
	tree := readFixtureTree(t, "testdata/v1/valid/.wormhole")
	snapshot, err := DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	assertTreeEqual(t, tree, rendered)
	if snapshot.Remotes == nil || len(snapshot.Remotes.Fabrics) != 2 {
		t.Fatalf("%+v", snapshot.Remotes)
	}
}

func TestCanonicalV1CanonicalizesDynamicJSON(t *testing.T) {
	canonical, err := CanonicalJSON(map[string]any{"z": 1, "a": map[string]any{"z": 2, "a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), "{\"a\":{\"a\":1,\"z\":2},\"z\":1}\n"; got != want {
		t.Fatalf("CanonicalJSON = %q, want %q", got, want)
	}
	markdown, err := CanonicalMarkdown([]byte("line one\r\nline two\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(markdown), "line one\nline two\n"; got != want {
		t.Fatalf("CanonicalMarkdown = %q, want %q", got, want)
	}
}

func TestDigestTreeGoldenAndOrderIndependent(t *testing.T) {
	tree := readFixtureTree(t, "testdata/v1/valid/.wormhole")
	digest, err := DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	const want Digest = "sha256:f82daf77d2b9100211c35d162067c982e8c8240e484a4e26fccdbc9eb9e591c4"
	if digest != want {
		t.Fatalf("DigestTree = %q, want %q", digest, want)
	}
	reversed := append(Tree(nil), tree...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reordered, err := DigestTree(reversed)
	if err != nil || reordered != digest {
		t.Fatalf("reordered DigestTree = %q, %v; want %q", reordered, err, digest)
	}
}

func TestDigestTreeRejectsUnsafeOrDuplicatePaths(t *testing.T) {
	for _, tree := range []Tree{
		{{Path: "../config.toml", Data: []byte("x")}},
		{{Path: "/config.toml", Data: []byte("x")}},
		{{Path: "state\\v1\\project.json", Data: []byte("x")}},
		{{Path: "config.toml", Data: []byte("x")}, {Path: "config.toml", Data: []byte("y")}},
	} {
		if _, err := DigestTree(tree); !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("DigestTree(%v) error = %v, want ErrInvalidSnapshot", tree, err)
		}
	}
}

func TestRemotesRejectsCredentialShapedKey(t *testing.T) {
	_, err := DecodeTree(readFixtureTree(t, "testdata/v1/bad-remotes/.wormhole"))
	if !errors.Is(err, ErrTrackedSecret) {
		t.Fatalf("DecodeTree error = %v, want ErrTrackedSecret", err)
	}
}

func TestRejectsUnknownJSONField(t *testing.T) {
	tree := readFixtureTree(t, "testdata/v1/valid/.wormhole")
	for i := range tree {
		if tree[i].Path == "state/v1/project.json" {
			tree[i].Data = bytes.Replace(tree[i].Data, []byte("\"name\":"), []byte("\"unexpected\":true,\"name\":"), 1)
		}
	}
	if _, err := DecodeTree(tree); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("DecodeTree error = %v, want ErrInvalidSnapshot", err)
	}
}

func TestRejectsInvalidCanonicalInputs(t *testing.T) {
	if _, err := CanonicalMarkdown([]byte{0xff}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("CanonicalMarkdown error = %v, want ErrInvalidSnapshot", err)
	}
	if _, err := CanonicalJSON(map[int]string{1: "value"}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("CanonicalJSON error = %v, want ErrInvalidSnapshot", err)
	}
	tree := readFixtureTree(t, "testdata/v1/valid/.wormhole")
	withUnknownPath := append(append(Tree(nil), tree...), File{Path: "state/v1/unknown.json", Data: []byte("{}\n")})
	if _, err := DecodeTree(withUnknownPath); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("unknown-path DecodeTree error = %v, want ErrInvalidSnapshot", err)
	}
	withoutConfig := make(Tree, 0, len(tree)-1)
	for _, file := range tree {
		if file.Path != "config.toml" {
			withoutConfig = append(withoutConfig, file)
		}
	}
	if _, err := DecodeTree(withoutConfig); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("missing-config DecodeTree error = %v, want ErrInvalidSnapshot", err)
	}
}
