package source_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/source"
)

func TestAssembleMissingPermissionShortCircuitsBeforeFilesystemAndPathChecks(t *testing.T) {
	result, err := source.Assemble(context.Background(), source.Request{
		Checkout: "/definitely/absent/checkout", Authorized: false,
		RequestedBytes: 100, ProjectCeiling: 100, GlobalCeiling: 100,
		Candidates: []source.Candidate{{SymbolID: "secret", QualifiedName: "pkg.Secret", FilePath: "../../secret", IndexedHash: "invalid", StartByte: -1, EndByte: -2}},
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if result.Completeness != source.CompletenessMetadataOnly || result.OmittedNodeCount != 1 || len(result.Outcomes) != 1 {
		t.Fatalf("metadata-only result = %#v", result)
	}
	outcome := result.Outcomes[0]
	if outcome.SourceIncluded || outcome.OmissionReason != source.OmissionMissingPermission || outcome.RequiredPermission != "code_graph.source.read" {
		t.Fatalf("permission outcome = %#v", outcome)
	}
}

func TestAssembleEnforcesEffectiveBudgetAndExactByteAccounting(t *testing.T) {
	root, indexedHash := sourceFixture(t, []byte("abcdefghijklmnop"))
	candidates := []source.Candidate{
		{SymbolID: "c", QualifiedName: "pkg.C", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 16, StartByte: 10, EndByte: 13, StartLine: 1, EndLine: 1, Rank: 3},
		{SymbolID: "a", QualifiedName: "pkg.A", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 16, StartByte: 0, EndByte: 4, StartLine: 1, EndLine: 1, Rank: 1},
		{SymbolID: "b", QualifiedName: "pkg.B", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 16, StartByte: 4, EndByte: 10, StartLine: 1, EndLine: 1, Rank: 2},
	}
	result, err := source.Assemble(context.Background(), source.Request{
		Checkout: root, Authorized: true, RequestedBytes: 10, ProjectCeiling: 8, GlobalCeiling: 20, Candidates: candidates,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveBudget != 8 || result.ReturnedBytes != 7 || result.OmittedNodeCount != 1 || result.OmissionReason != source.OmissionBudgetExhausted {
		t.Fatalf("budget result = %#v", result)
	}
	if got := includedSources(result.Outcomes); !reflect.DeepEqual(got, []string{"abcd", "klm"}) {
		t.Fatalf("included source = %q", got)
	}
	if !reflect.DeepEqual(result.SuggestedFollowUpSymbols, []string{"pkg.B"}) {
		t.Fatalf("follow-ups = %v", result.SuggestedFollowUpSymbols)
	}

	exact, err := source.Assemble(context.Background(), source.Request{
		Checkout: root, Authorized: true, RequestedBytes: 10, ProjectCeiling: 10, GlobalCeiling: 10, Candidates: candidates,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := includedSources(exact.Outcomes)
	if exact.ReturnedBytes != 10 || !reflect.DeepEqual(got, []string{"abcd", "efghij"}) {
		t.Fatalf("exact exhaustion = %#v sources=%q", exact, got)
	}

	overlapping := []source.Candidate{
		{SymbolID: "overlap-a", QualifiedName: "pkg.OverlapA", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 16, StartByte: 0, EndByte: 6, StartLine: 1, EndLine: 1, Rank: 1},
		{SymbolID: "overlap-b", QualifiedName: "pkg.OverlapB", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 16, StartByte: 3, EndByte: 9, StartLine: 1, EndLine: 1, Rank: 2},
	}
	overlapResult, err := source.Assemble(context.Background(), source.Request{
		Checkout: root, Authorized: true, RequestedBytes: 12, ProjectCeiling: 20, GlobalCeiling: 20, Candidates: overlapping,
	})
	if err != nil || overlapResult.ReturnedBytes != 12 {
		t.Fatalf("overlap accounting result=%#v error=%v", overlapResult, err)
	}
}

func TestAssembleZeroBudgetReturnsExplicitMetadataOnly(t *testing.T) {
	result, err := source.Assemble(context.Background(), source.Request{
		Checkout: "/not-read", Authorized: true, RequestedBytes: 0, ProjectCeiling: 10, GlobalCeiling: 10,
		Candidates: []source.Candidate{{SymbolID: "a", QualifiedName: "pkg.A", FilePath: "../bad"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completeness != source.CompletenessMetadataOnly || result.OmissionReason != source.OmissionBudgetZero || result.OmittedNodeCount != 1 {
		t.Fatalf("zero-budget result = %#v", result)
	}
}

func TestAssembleOmitsChangedWorkingTreeAndRecommendsRefresh(t *testing.T) {
	root, indexedHash := sourceFixture(t, []byte("original source"))
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("modified source"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := source.Assemble(context.Background(), source.Request{
		Checkout: root, Authorized: true, RequestedBytes: 100, ProjectCeiling: 100, GlobalCeiling: 100,
		Candidates: []source.Candidate{{SymbolID: "a", QualifiedName: "pkg.A", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 15, StartByte: 0, EndByte: 8, StartLine: 1, EndLine: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].SourceIncluded || result.Outcomes[0].OmissionReason != source.OmissionWorkingTreeChanged || !result.Outcomes[0].RefreshRecommended {
		t.Fatalf("stale outcome = %#v", result)
	}
}

func TestAssembleRejectsTraversalAbsoluteNULBackslashAndSymlinkEscape(t *testing.T) {
	root, indexedHash := sourceFixture(t, []byte("inside"))
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.go")); err != nil {
		t.Fatal(err)
	}
	for _, malicious := range []string{"../outside.go", outside, "nested\\escape.go", "bad\x00.go", "escape.go"} {
		_, err := source.Assemble(context.Background(), source.Request{
			Checkout: root, Authorized: true, RequestedBytes: 100, ProjectCeiling: 100, GlobalCeiling: 100,
			Candidates: []source.Candidate{{SymbolID: "bad", FilePath: malicious, IndexedHash: indexedHash, IndexedByteSize: 6, StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1}},
		})
		if !errors.Is(err, source.ErrContainment) {
			t.Errorf("path %q error = %v, want ErrContainment", malicious, err)
		}
	}
}

func TestAssembleRejectsMalformedPathFixture(t *testing.T) {
	fixture, err := os.Open(filepath.Join("..", "..", "..", "..", "testdata", "codegraph", "malformed", "source-paths.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	root, indexedHash := sourceFixture(t, []byte("inside"))
	scanner := bufio.NewScanner(fixture)
	for scanner.Scan() {
		malicious := strings.TrimSpace(scanner.Text())
		if malicious == "" {
			continue
		}
		_, err := source.Assemble(context.Background(), source.Request{
			Checkout: root, Authorized: true, RequestedBytes: 6, ProjectCeiling: 6, GlobalCeiling: 6,
			Candidates: []source.Candidate{{SymbolID: "malformed", FilePath: malicious, IndexedHash: indexedHash, IndexedByteSize: 6, StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1}},
		})
		if !errors.Is(err, source.ErrContainment) {
			t.Errorf("fixture path %q error = %v, want ErrContainment", malicious, err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestAssembleRejectsInvalidRangesAndDuplicateCandidates(t *testing.T) {
	root, indexedHash := sourceFixture(t, []byte("inside"))
	requests := []source.Request{
		{Checkout: root, Authorized: true, RequestedBytes: 10, ProjectCeiling: 10, GlobalCeiling: 10, Candidates: []source.Candidate{{SymbolID: "bad", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 6, StartByte: 5, EndByte: 2, StartLine: 1, EndLine: 1}}},
		{Checkout: root, Authorized: true, RequestedBytes: 10, ProjectCeiling: 10, GlobalCeiling: 10, Candidates: []source.Candidate{{SymbolID: "same", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 6, StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1}, {SymbolID: "same", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 6, StartByte: 1, EndByte: 2, StartLine: 1, EndLine: 1}}},
		{Checkout: root, Authorized: true, RequestedBytes: 10, ProjectCeiling: 10, GlobalCeiling: 10, Candidates: []source.Candidate{{SymbolID: "bad-hash", FilePath: "source.go", IndexedHash: "sha256:xyz", IndexedByteSize: 6, StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1}}},
	}
	for _, request := range requests {
		if _, err := source.Assemble(context.Background(), request); !errors.Is(err, source.ErrInvalidRequest) {
			t.Errorf("Assemble() error = %v, want ErrInvalidRequest", err)
		}
	}
}

func TestAssembleUsesExactUTF8CRLFByteAndLineRange(t *testing.T) {
	content := []byte("α\r\nbeta\r\n")
	root, indexedHash := sourceFixture(t, content)
	result, err := source.Assemble(context.Background(), source.Request{
		Checkout: root, Authorized: true, RequestedBytes: 4, ProjectCeiling: 10, GlobalCeiling: 10,
		Candidates: []source.Candidate{{
			SymbolID: "beta", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: int64(len(content)),
			StartByte: 4, EndByte: 8, StartLine: 2, EndLine: 2,
		}},
	})
	if err != nil || len(result.Outcomes) != 1 || result.Outcomes[0].Source != "beta" || result.ReturnedBytes != 4 {
		t.Fatalf("UTF-8/CRLF result=%#v error=%v", result, err)
	}
	request := source.Request{
		Checkout: root, Authorized: true, RequestedBytes: 4, ProjectCeiling: 10, GlobalCeiling: 10,
		Candidates: []source.Candidate{{SymbolID: "bad-line", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: int64(len(content)), StartByte: 4, EndByte: 8, StartLine: 1, EndLine: 1}},
	}
	if _, err := source.Assemble(context.Background(), request); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatalf("line mismatch error = %v, want ErrInvalidRequest", err)
	}
}

func TestAssembleBoundsMissingNonRegularAndGrownFiles(t *testing.T) {
	root, indexedHash := sourceFixture(t, []byte("small"))
	base := source.Request{Checkout: root, Authorized: true, RequestedBytes: 5, ProjectCeiling: 10, GlobalCeiling: 10}
	base.Candidates = []source.Candidate{{SymbolID: "missing", FilePath: "missing.go", IndexedHash: indexedHash, IndexedByteSize: 5, StartByte: 0, EndByte: 5, StartLine: 1, EndLine: 1}}
	missing, err := source.Assemble(context.Background(), base)
	if err != nil || missing.Outcomes[0].OmissionReason != source.OmissionWorkingTreeChanged {
		t.Fatalf("missing result=%#v error=%v", missing, err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	base.Candidates[0].SymbolID, base.Candidates[0].FilePath = "directory", "directory.go"
	if _, err := source.Assemble(context.Background(), base); !errors.Is(err, source.ErrContainment) {
		t.Fatalf("directory error = %v, want ErrContainment", err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.go"), make([]byte, source.MaxIndexedFileBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	base.Candidates[0] = source.Candidate{SymbolID: "grown", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 5, StartByte: 0, EndByte: 5, StartLine: 1, EndLine: 1}
	grown, err := source.Assemble(context.Background(), base)
	if err != nil || grown.Outcomes[0].OmissionReason != source.OmissionWorkingTreeChanged {
		t.Fatalf("grown result=%#v error=%v", grown, err)
	}
}

func TestAssembleHonorsCancellation(t *testing.T) {
	root, indexedHash := sourceFixture(t, []byte("safe"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := source.Assemble(ctx, source.Request{
		Checkout: root, Authorized: true, RequestedBytes: 4, ProjectCeiling: 4, GlobalCeiling: 4,
		Candidates: []source.Candidate{{SymbolID: "safe", FilePath: "source.go", IndexedHash: indexedHash, IndexedByteSize: 4, StartByte: 0, EndByte: 4, StartLine: 1, EndLine: 1}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func FuzzAssembleContainment(f *testing.F) {
	for _, seed := range []string{"../secret", "/etc/passwd", "safe.go", "a\\b.go", "", "bad\x00.go"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, candidatePath string) {
		root, indexedHash := sourceFixture(t, []byte("safe"))
		result, err := source.Assemble(context.Background(), source.Request{
			Checkout: root, Authorized: true, RequestedBytes: 4, ProjectCeiling: 4, GlobalCeiling: 4,
			Candidates: []source.Candidate{{SymbolID: "candidate", FilePath: candidatePath, IndexedHash: indexedHash, IndexedByteSize: 4, StartByte: 0, EndByte: 4, StartLine: 1, EndLine: 1}},
		})
		if err == nil {
			for _, outcome := range result.Outcomes {
				if outcome.SourceIncluded && outcome.Source != "safe" {
					t.Fatalf("escaped source read for %q: %q", candidatePath, outcome.Source)
				}
			}
		}
	})
}

func sourceFixture(t *testing.T, content []byte) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return root, "sha256:" + hex.EncodeToString(digest[:])
}

func includedSources(outcomes []source.Outcome) []string {
	var values []string
	for _, outcome := range outcomes {
		if outcome.SourceIncluded {
			values = append(values, outcome.Source)
		}
	}
	return values
}
