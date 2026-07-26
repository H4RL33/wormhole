package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAssembleRootHandleSurvivesCheckoutRenameAndReplacement(t *testing.T) {
	parent := t.TempDir()
	checkout := filepath.Join(parent, "checkout")
	if err := os.Mkdir(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("original")
	if err := os.WriteFile(filepath.Join(checkout, "source.go"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	request := Request{
		Checkout: checkout, Authorized: true, RequestedBytes: int64(len(original)), ProjectCeiling: 100, GlobalCeiling: 100,
		Candidates: []Candidate{{SymbolID: "source", FilePath: "source.go", IndexedHash: "sha256:" + hex.EncodeToString(digest[:]), IndexedByteSize: int64(len(original)), StartByte: 0, EndByte: int64(len(original)), StartLine: 1, EndLine: 1}},
	}
	result, err := assemble(context.Background(), request, func() {
		if err := os.Rename(checkout, filepath.Join(parent, "original-root")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(checkout, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(checkout, "source.go"), []byte("attacker"), 0o644); err != nil {
			t.Fatal(err)
		}
	}, nil)
	if err != nil || len(result.Outcomes) != 1 || result.Outcomes[0].Source != "original" {
		t.Fatalf("root replacement result=%#v error=%v", result, err)
	}
}

func TestAssembleRejectsSymlinkSwappedBeforeRootScopedOpen(t *testing.T) {
	checkout := t.TempDir()
	original := []byte("inside")
	path := filepath.Join(checkout, "source.go")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	request := Request{
		Checkout: checkout, Authorized: true, RequestedBytes: 6, ProjectCeiling: 6, GlobalCeiling: 6,
		Candidates: []Candidate{{SymbolID: "source", FilePath: "source.go", IndexedHash: "sha256:" + hex.EncodeToString(digest[:]), IndexedByteSize: 6, StartByte: 0, EndByte: 6, StartLine: 1, EndLine: 1}},
	}
	_, err := assemble(context.Background(), request, nil, func(string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrContainment) {
		t.Fatalf("symlink swap error = %v, want ErrContainment", err)
	}
}
