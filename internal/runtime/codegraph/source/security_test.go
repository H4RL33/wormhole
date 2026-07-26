package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecuritySourceMutationDuringAssemblyReturnsNoBytes(t *testing.T) {
	checkout := t.TempDir()
	original := []byte("package fixture\nfunc Safe() string { return \"inside\" }\n")
	changed := []byte("package fixture\nfunc Safe() string { return \"evil!!\" }\n")
	if len(original) != len(changed) {
		t.Fatal("race fixture must preserve size so the content hash is decisive")
	}
	path := filepath.Join(checkout, "safe.go")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	result, err := assemble(context.Background(), Request{
		Checkout: checkout, Authorized: true, RequestedBytes: int64(len(original)),
		ProjectCeiling: int64(len(original)), GlobalCeiling: int64(len(original)),
		Candidates: []Candidate{{
			SymbolID: "safe", QualifiedName: "fixture.Safe", FilePath: "safe.go",
			IndexedHash: "sha256:" + hex.EncodeToString(digest[:]), IndexedByteSize: int64(len(original)),
			StartByte: 0, EndByte: int64(len(original)), StartLine: 1, EndLine: 2,
		}},
	}, nil, func(string) {
		if err := os.WriteFile(path, changed, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatalf("assemble changed source: %v", err)
	}
	if result.ReturnedBytes != 0 || len(result.Outcomes) != 1 || result.Outcomes[0].SourceIncluded ||
		result.Outcomes[0].OmissionReason != OmissionWorkingTreeChanged || !result.Outcomes[0].RefreshRecommended {
		t.Fatalf("changed source result = %#v", result)
	}
}

func TestSecurityDeeplyNestedContainedSourceIsReturned(t *testing.T) {
	checkout := t.TempDir()
	components := make([]string, 80)
	for index := range components {
		components[index] = "d"
	}
	relative := strings.Join(components, "/") + "/deep.go"
	full := filepath.Join(checkout, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("package deep\nfunc Located() {}\n")
	if err := os.WriteFile(full, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	result, err := Assemble(context.Background(), Request{
		Checkout: checkout, Authorized: true, RequestedBytes: int64(len(content)),
		ProjectCeiling: int64(len(content)), GlobalCeiling: int64(len(content)),
		Candidates: []Candidate{{
			SymbolID: "deep", QualifiedName: "deep.Located", FilePath: relative,
			IndexedHash: "sha256:" + hex.EncodeToString(digest[:]), IndexedByteSize: int64(len(content)),
			StartByte: 0, EndByte: int64(len(content)), StartLine: 1, EndLine: 2,
		}},
	})
	if err != nil || result.ReturnedBytes != int64(len(content)) || len(result.Outcomes) != 1 || result.Outcomes[0].Source != string(content) {
		t.Fatalf("deep contained source result=%#v error=%v", result, err)
	}
}

func TestSecurityActualOversizedSourceIsRejectedBeforeRead(t *testing.T) {
	checkout := t.TempDir()
	content := make([]byte, MaxIndexedFileBytes+1)
	copy(content, "package oversized\n")
	path := filepath.Join(checkout, "oversized.go")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	_, err := Assemble(context.Background(), Request{
		Checkout: checkout, Authorized: true, RequestedBytes: int64(len(content)),
		ProjectCeiling: int64(len(content)), GlobalCeiling: int64(len(content)),
		Candidates: []Candidate{{
			SymbolID: "oversized", QualifiedName: "oversized.Value", FilePath: "oversized.go",
			IndexedHash: "sha256:" + hex.EncodeToString(digest[:]), IndexedByteSize: int64(len(content)),
			StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1,
		}},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("actual oversized source error = %v, want ErrInvalidRequest", err)
	}
}
