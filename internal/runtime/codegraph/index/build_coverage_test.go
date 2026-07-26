package index

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cgconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	cggo "github.com/H4RL33/wormhole/internal/runtime/codegraph/golang"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
)

func TestBuildAPIsRejectInvalidLifecycleInputsBeforeIndexing(t *testing.T) {
	ctx := context.Background()
	var nilIndex *Index
	if err := nilIndex.Build(ctx, BuildRequest{}); err == nil {
		t.Fatal("nil Index.Build succeeded")
	}
	if err := nilIndex.BuildForLifecycle(ctx, BuildRequest{}, cgconfig.Project{}); err == nil {
		t.Fatal("nil Index.BuildForLifecycle succeeded")
	}
	if err := New(nil).Build(ctx, BuildRequest{}); err == nil {
		t.Fatal("Index with nil store succeeded")
	}

	graphStore := newInternalBuildStore(t)
	idx := New(graphStore)
	if err := idx.Build(ctx, BuildRequest{}); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("empty Build error = %v, want ErrInvalidCandidate", err)
	}
	if err := idx.BuildForLifecycle(ctx, BuildRequest{ProjectID: "project-a", RevisionID: "revision"}, cgconfig.Project{ProjectID: "other"}); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("mismatched lifecycle error = %v, want ErrInvalidCandidate", err)
	}
	if err := idx.BuildForLifecycle(ctx, BuildRequest{ProjectID: "project-a", RevisionID: "revision-invalid-config"}, cgconfig.Project{ProjectID: "project-a", Enabled: true}); err == nil {
		t.Fatal("lifecycle accepted invalid next config")
	}
	if err := idx.BuildForLifecycle(ctx, BuildRequest{ProjectID: "project-a", RevisionID: "revision-disabled"}, cgconfig.DefaultProject("project-a")); !errors.Is(err, ErrProjectDisabled) {
		t.Fatalf("disabled lifecycle error = %v, want ErrProjectDisabled", err)
	}

	approved := internalApprovedConfig(t, graphStore)
	if err := idx.Build(ctx, BuildRequest{ProjectID: "other", RevisionID: "revision-other"}); !errors.Is(err, ErrApprovedCheckoutMismatch) {
		t.Fatalf("cross-project Build error = %v, want ErrApprovedCheckoutMismatch", err)
	}
	approved.ActiveCheckout = filepath.Join(t.TempDir(), "missing")
	if err := graphStore.PutProjectConfig(ctx, approved); err != nil {
		t.Fatal(err)
	}
	if err := idx.Build(ctx, BuildRequest{ProjectID: "project-a", RevisionID: "revision-missing-checkout"}); !errors.Is(err, ErrApprovedCheckoutMismatch) {
		t.Fatalf("missing approved checkout error = %v, want ErrApprovedCheckoutMismatch", err)
	}
}

func TestBuildHelpersRejectUnsafeModulesAndUnsupportedSemanticValues(t *testing.T) {
	root := t.TempDir()
	if _, err := snapshotModuleFile(root); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("missing go.mod error = %v, want ErrInvalidCandidate", err)
	}
	if err := os.Mkdir(filepath.Join(root, "go.mod"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotModuleFile(root); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("directory go.mod error = %v, want ErrInvalidCandidate", err)
	}
	if err := os.Remove(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(outside, []byte("module outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotModuleFile(root); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("symlink go.mod error = %v, want ErrInvalidCandidate", err)
	}

	for _, relationship := range []cggo.Relationship{
		cggo.RelationshipContains, cggo.RelationshipDefines, cggo.RelationshipImports,
		cggo.RelationshipCalls, cggo.RelationshipReferences, cggo.RelationshipUsesType,
	} {
		if _, err := storeRelationship(relationship); err != nil {
			t.Fatalf("supported relationship %q: %v", relationship, err)
		}
	}
	if _, err := storeRelationship("unsupported"); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("unsupported relationship error = %v", err)
	}
	for _, provenance := range []cggo.Provenance{
		cggo.ProvenanceGoPackages, cggo.ProvenanceGoTypes, cggo.ProvenanceGoAST,
		cggo.ProvenanceParser, cggo.ProvenanceHeuristic,
	} {
		if _, err := storeProvenance(provenance); err != nil {
			t.Fatalf("supported provenance %q: %v", provenance, err)
		}
	}
	if _, err := storeProvenance("unsupported"); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("unsupported provenance error = %v", err)
	}
	if got := diagnosticSeverity(cggo.DiagnosticWarning); got != store.DiagnosticWarning {
		t.Fatalf("warning severity = %q", got)
	}
	if got := diagnosticSeverity(cggo.DiagnosticError); got != store.DiagnosticError {
		t.Fatalf("error severity = %q", got)
	}
	unsafe := " line\nwith\tcontrols\x00 " + strings.Repeat("x", maxStoredDiagnosticBytes+100)
	if got := boundedDiagnostic(unsafe); strings.ContainsAny(got, "\n\t\x00") || len(got) != maxStoredDiagnosticBytes {
		t.Fatalf("bounded diagnostic length=%d contains_controls=%t", len(got), strings.ContainsAny(got, "\n\t\x00"))
	}
}

func TestInventoryEqualityDetectsEveryPublicationRelevantChange(t *testing.T) {
	base := GitInventory{
		Root: "/repo", CanonicalRemote: "https://example.invalid/repo.git", Commit: strings.Repeat("a", 40), TotalBytes: 3,
		Files: []TrackedFile{{Path: "a.go", Mode: "100644", SHA256: "sha256:" + strings.Repeat("b", 64), Bytes: []byte("abc")}},
	}
	if !equalInventory(base, base) {
		t.Fatal("identical inventory did not compare equal")
	}
	mutations := []func(*GitInventory){
		func(value *GitInventory) { value.Root = "/other" },
		func(value *GitInventory) { value.CanonicalRemote = "https://example.invalid/other.git" },
		func(value *GitInventory) { value.Commit = strings.Repeat("c", 40) },
		func(value *GitInventory) { value.TotalBytes++ },
		func(value *GitInventory) { value.Files = nil },
		func(value *GitInventory) { value.Files[0].Path = "b.go" },
		func(value *GitInventory) { value.Files[0].Mode = "100755" },
		func(value *GitInventory) { value.Files[0].SHA256 = "sha256:" + strings.Repeat("d", 64) },
		func(value *GitInventory) { value.Files[0].Bytes = []byte("abd") },
	}
	for i, mutate := range mutations {
		changed := base
		changed.Files = append([]TrackedFile(nil), base.Files...)
		mutate(&changed)
		if equalInventory(base, changed) {
			t.Fatalf("mutation %d compared equal", i)
		}
	}
}

func TestWriteCandidatePropagatesEveryDurableWriteBoundary(t *testing.T) {
	tracked := TrackedFile{Path: "a.go", SHA256: "sha256:" + strings.Repeat("a", 64), Bytes: []byte("package a")}
	semantic := cggo.File{ID: "file-a", Path: "a.go"}
	tests := []struct {
		name      string
		dropTable string
		inventory GitInventory
		analysis  cggo.Result
	}{
		{name: "package node", dropTable: "codegraph_nodes", analysis: cggo.Result{Packages: []cggo.Package{{ID: "package-a", ImportPath: "example/a"}}}},
		{name: "package edge", dropTable: "codegraph_edges", analysis: cggo.Result{Packages: []cggo.Package{{ID: "package-a", ImportPath: "example/a"}}}},
		{name: "semantic file omitted", inventory: GitInventory{Files: []TrackedFile{tracked}}},
		{name: "file node", dropTable: "codegraph_nodes", inventory: GitInventory{Files: []TrackedFile{tracked}}, analysis: cggo.Result{Files: []cggo.File{semantic}}},
		{name: "file record", dropTable: "codegraph_files", inventory: GitInventory{Files: []TrackedFile{tracked}}, analysis: cggo.Result{Files: []cggo.File{semantic}}},
		{name: "repository file edge", dropTable: "codegraph_edges", inventory: GitInventory{Files: []TrackedFile{tracked}}, analysis: cggo.Result{Files: []cggo.File{semantic}}},
		{name: "symbol node", dropTable: "codegraph_nodes", analysis: cggo.Result{Symbols: []cggo.Symbol{{ID: "symbol-a", Name: "A", FilePath: "a.go"}}}},
		{name: "symbol record", dropTable: "codegraph_symbols", analysis: cggo.Result{Symbols: []cggo.Symbol{{ID: "symbol-a", Name: "A", FileID: "file-a", FilePath: "a.go", StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1}}}},
		{name: "unsupported relationship", analysis: cggo.Result{Edges: []cggo.Edge{{ID: "edge-a", Relationship: "unsupported", Provenance: cggo.ProvenanceGoAST}}}},
		{name: "unsupported provenance", analysis: cggo.Result{Edges: []cggo.Edge{{ID: "edge-a", Relationship: cggo.RelationshipContains, Provenance: "unsupported"}}}},
		{name: "edge record", dropTable: "codegraph_edges", analysis: cggo.Result{Edges: []cggo.Edge{{ID: "edge-a", SourceID: "source", TargetID: "target", Relationship: cggo.RelationshipContains, Provenance: cggo.ProvenanceGoAST, Confidence: 1}}}},
		{name: "diagnostic record", dropTable: "codegraph_diagnostics", analysis: cggo.Result{Diagnostics: []cggo.Diagnostic{{ID: "diagnostic-a", Severity: cggo.DiagnosticError, Code: "broken", Message: "broken"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			graphStore, db := newCorruptibleBuildStore(t)
			request := BuildRequest{ProjectID: "project-a", RevisionID: "candidate"}
			if err := graphStore.CreateCandidate(context.Background(), store.Revision{
				ProjectID: request.ProjectID, ID: request.RevisionID, IndexedCommit: strings.Repeat("b", 40), CreatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			if test.dropTable != "" {
				if _, err := db.Exec(`DROP TABLE ` + test.dropTable); err != nil {
					t.Fatal(err)
				}
			}
			err := New(graphStore).writeCandidate(context.Background(), request, test.inventory, test.analysis)
			if err == nil {
				t.Fatal("writeCandidate unexpectedly succeeded")
			}
		})
	}
}

func newCorruptibleBuildStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "gateway.db"), OmitHost: true}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	graphStore, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	return graphStore, db
}
