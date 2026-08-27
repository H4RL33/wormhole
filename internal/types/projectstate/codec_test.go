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

func TestDigestCanonicalGoldenValues(t *testing.T) {
	snapshot := operationSnapshot(t)
	articleBodyDigest := Digest("sha256:d036295b58150d384216c6757df413e01ea54f0e3f04f15e69eab0630586c71e")
	taskTombstone := TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: taskID, EntityKind: "task",
		DeletedContentDigest: "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0",
		DeletedBy:            operationActor(), DeletedAt: operationActor().OccurredAt, Extensions: ExtensionsV1{},
	}
	articleTombstone := TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: articleID, EntityKind: "kb_article",
		DeletedContentDigest: "sha256:d4c95b4bf2332b5f815d0ee45f3bd0bfe1811530e61ad1e47e5f40c709cab08b",
		DeletedBodyDigest:    &articleBodyDigest, DeletedBy: operationActor(), DeletedAt: operationActor().OccurredAt, Extensions: ExtensionsV1{},
	}
	tests := []struct {
		name  string
		value any
		want  Digest
	}{
		{"task", *snapshot.Tasks[taskID].Value, "sha256:87f7972dc4c0a198ece460bc094d35a981dc03c352ccfc2e0fb0280a60f5f3b0"},
		{"KB article", *snapshot.Articles[articleID].Value, "sha256:d4c95b4bf2332b5f815d0ee45f3bd0bfe1811530e61ad1e47e5f40c709cab08b"},
		{"GitLink", *snapshot.GitLinks[gitLinkID].Value, "sha256:46922078bd9d327fb4179236b47a8c77f05ddca8bd701b09b8e446a07c9590a3"},
		{"task tombstone", taskTombstone, "sha256:523f076ef867ea483ab7bffa532bffdca9ad3b0dfcda66ec73d91b130f1304a9"},
		{"KB tombstone", articleTombstone, "sha256:d43bc01d07c43897c6905eb5c974e45c955c5ce4ff9741c6dd10dfb8a06a13e5"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := DigestCanonicalJSON(test.value)
			if err != nil || got != test.want {
				t.Fatalf("DigestCanonicalJSON = %q, %v; want %q", got, err, test.want)
			}
		})
	}

	for _, body := range [][]byte{snapshot.Articles[articleID].Body, []byte(strings.ReplaceAll(string(snapshot.Articles[articleID].Body), "\n", "\r\n"))} {
		got, err := DigestCanonicalMarkdown(body)
		if err != nil || got != articleBodyDigest {
			t.Fatalf("DigestCanonicalMarkdown = %q, %v; want %q", got, err, articleBodyDigest)
		}
	}
}

func TestGitLinkTombstoneRejectedByDecodeTree(t *testing.T) {
	tree := readFixtureTree(t, "testdata/v1/valid/.wormhole")
	tombstone := TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: gitLinkID, EntityKind: "git_link",
		DeletedContentDigest: "sha256:46922078bd9d327fb4179236b47a8c77f05ddca8bd701b09b8e446a07c9590a3",
		DeletedBy:            operationActor(), DeletedAt: operationActor().OccurredAt, Extensions: ExtensionsV1{},
	}
	data, err := CanonicalJSON(tombstone)
	if err != nil {
		t.Fatal(err)
	}
	for index := range tree {
		if tree[index].Path == "state/v1/git-links/"+gitLinkID+".json" {
			tree[index].Data = data
		}
	}
	if _, err := DecodeTree(tree); err == nil {
		t.Fatal("DecodeTree accepted canonical GitLink tombstone")
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
		{{Path: "..", Data: []byte("x")}},
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

func TestRemotesRejectsControlCharactersBeforeEncoding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FabricHintV1)
	}{
		{"instance id BEL", func(fabric *FabricHintV1) { fabric.InstanceID = "bad\a" }},
		{"remote project id vertical tab", func(fabric *FabricHintV1) { fabric.RemoteProjectID = "bad\v" }},
		{"remote project id DEL", func(fabric *FabricHintV1) { fabric.RemoteProjectID = "bad\x7f" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot, err := DecodeTree(readFixtureTree(t, "testdata/v1/valid/.wormhole"))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&snapshot.Remotes.Fabrics[0])
			if _, err := EncodeTree(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("EncodeTree error = %v, want ErrInvalidSnapshot", err)
			}
		})
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
