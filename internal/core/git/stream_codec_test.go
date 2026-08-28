package git

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestStoredTreeRoundTripPreservesCanonicalBytes(t *testing.T) {
	tree := streamTestTree(t, "00000000-0000-4000-8000-000000000001", streamTestRepository())
	wantDigest, err := projectstate.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := EncodeStoredTree(reverseStreamTree(tree))
	if err != nil {
		t.Fatalf("EncodeStoredTree: %v", err)
	}
	decoded, err := DecodeStoredTree(raw)
	if err != nil {
		t.Fatalf("DecodeStoredTree: %v", err)
	}
	gotDigest, err := projectstate.DigestTree(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("decoded digest = %q, want %q", gotDigest, wantDigest)
	}
	assertStreamTreesEqual(t, tree, decoded)

	if got := binary.BigEndian.Uint32(raw[:4]); got != uint32(len(tree)) {
		t.Fatalf("stored file count = %d, want %d", got, len(tree))
	}
	firstPathLength := binary.BigEndian.Uint32(raw[4:8])
	firstPath := string(raw[8 : 8+firstPathLength])
	if firstPath != "config.toml" {
		t.Fatalf("first stored path = %q, want config.toml", firstPath)
	}
}

func TestStoredTreeRejectsInvalidContainersAndBounds(t *testing.T) {
	valid := streamTestTree(t, "00000000-0000-4000-8000-000000000001", streamTestRepository())
	validRaw, err := EncodeStoredTree(valid)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		raw  func() []byte
	}{
		{"duplicate path", func() []byte {
			return rawStoredFiles([]projectstate.File{{Path: "a", Data: nil}, {Path: "a", Data: nil}})
		}},
		{"out of order", func() []byte {
			return rawStoredFiles([]projectstate.File{{Path: "b", Data: nil}, {Path: "a", Data: nil}})
		}},
		{"empty path", func() []byte { return rawStoredFiles([]projectstate.File{{Path: "", Data: nil}}) }},
		{"absolute path", func() []byte { return rawStoredFiles([]projectstate.File{{Path: "/state", Data: nil}}) }},
		{"backslash path", func() []byte { return rawStoredFiles([]projectstate.File{{Path: `state\v1`, Data: nil}}) }},
		{"trailing bytes", func() []byte { return append(bytes.Clone(validRaw), 0) }},
		{"too many files", func() []byte {
			var raw bytes.Buffer
			_ = binary.Write(&raw, binary.BigEndian, uint32(10_001))
			return raw.Bytes()
		}},
		{"file too large", func() []byte {
			return rawStoredFiles([]projectstate.File{{Path: "config.toml", Data: make([]byte, 1<<20+1)}})
		}},
		{"aggregate too large", func() []byte {
			files := make([]projectstate.File, 17)
			for index := range files {
				files[index] = projectstate.File{Path: string(rune('a' + index)), Data: make([]byte, 1<<20)}
			}
			return rawStoredFiles(files)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeStoredTree(test.raw()); err == nil {
				t.Fatal("DecodeStoredTree unexpectedly accepted invalid container")
			}
		})
	}

	for _, tree := range []projectstate.Tree{
		append(projectstate.Tree(nil), valid[:len(valid)-1]...),
		{{Path: "config.toml", Data: make([]byte, 1<<20+1)}},
	} {
		if _, err := EncodeStoredTree(tree); err == nil {
			t.Fatal("EncodeStoredTree unexpectedly accepted invalid tree")
		}
	}
}

func rawStoredFiles(files []projectstate.File) []byte {
	var raw bytes.Buffer
	_ = binary.Write(&raw, binary.BigEndian, uint32(len(files)))
	for _, file := range files {
		_ = binary.Write(&raw, binary.BigEndian, uint32(len(file.Path)))
		raw.WriteString(file.Path)
		_ = binary.Write(&raw, binary.BigEndian, uint64(len(file.Data)))
		raw.Write(file.Data)
	}
	return raw.Bytes()
}

func reverseStreamTree(tree projectstate.Tree) projectstate.Tree {
	reversed := make(projectstate.Tree, len(tree))
	for index := range tree {
		file := tree[len(tree)-1-index]
		reversed[index] = projectstate.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return reversed
}

func assertStreamTreesEqual(t *testing.T, want, got projectstate.Tree) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("tree length = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].Path != want[index].Path || !bytes.Equal(got[index].Data, want[index].Data) {
			t.Fatalf("tree[%d] = %q %q, want %q %q", index, got[index].Path, got[index].Data, want[index].Path, want[index].Data)
		}
	}
}

func streamTestDigest(seed string) projectstate.Digest {
	return projectstate.Digest("sha256:" + strings.Repeat(seed, 64))
}
