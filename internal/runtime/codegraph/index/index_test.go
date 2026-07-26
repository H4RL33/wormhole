package index_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	cgconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	cggo "github.com/H4RL33/wormhole/internal/runtime/codegraph/golang"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

const testProjectID = "project-a"

func TestBuildPublishesTrackedSemanticGraphDeterministically(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "go.mod", "module example.com/build\n\ngo 1.26\n")
	writeInventoryFile(t, repository, "main.go", `package build

type Thing struct{}
func NewThing() *Thing { helper(); return &Thing{} }
func helper() {}
`)
	writeInventoryFile(t, repository, "excluded.go", "//go:build excluded\n\npackage build\nfunc BuildExcluded() {}\n")
	runGit(t, repository, "add", "go.mod", "main.go", "excluded.go")
	runGit(t, repository, "commit", "-m", "fixture")
	writeInventoryFile(t, repository, "untracked.go", "package build\nfunc Untracked() {}\n")

	s := newTestStore(t, testProjectID)
	approveBuildConfig(t, s, repository, testCanonicalRemote)
	idx := index.New(s)
	request := index.BuildRequest{
		ProjectID: testProjectID, RevisionID: "revision-build-one", Checkout: repository,
		CanonicalRemote: testCanonicalRemote,
	}
	if err := idx.Build(context.Background(), request); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	first := activeGraphSummary(t, s)
	if !containsString(first.files, "excluded.go") {
		t.Fatalf("tracked build-excluded file absent: %#v", first)
	}
	if containsSubstring(first.symbols, "Untracked") || containsSubstring(first.symbols, "BuildExcluded") {
		t.Fatalf("untracked/build-excluded symbol entered graph: %#v", first.symbols)
	}
	for _, want := range []string{"example.com/build.NewThing", "example.com/build.Thing", "example.com/build.helper"} {
		if !containsString(first.symbols, want) {
			t.Errorf("symbols = %v, missing %q", first.symbols, want)
		}
	}

	request.RevisionID = "revision-build-two"
	if err := idx.Build(context.Background(), request); err != nil {
		t.Fatalf("second Build() error = %v", err)
	}
	second := activeGraphSummary(t, s)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same commit and working tree produced different graph:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestBuildFailurePreservesActiveAndFailsCandidate(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "go.mod", "module example.com/broken\n\ngo 1.26\n")
	writeInventoryFile(t, repository, "broken.go", "package broken\nfunc Broken( {\n")
	runGit(t, repository, "add", "go.mod", "broken.go")
	runGit(t, repository, "commit", "-m", "broken")
	s := newTestStore(t, testProjectID)
	approveBuildConfig(t, s, repository, testCanonicalRemote)
	idx := index.New(s)
	seedValidCandidate(t, s, testProjectID, "revision-old", "old")
	if err := idx.Publish(context.Background(), "revision-old"); err != nil {
		t.Fatal(err)
	}
	err := idx.Build(context.Background(), index.BuildRequest{
		ProjectID: testProjectID, RevisionID: "revision-broken", Checkout: repository,
		CanonicalRemote: testCanonicalRemote,
	})
	if !errors.Is(err, cggo.ErrPackageLoad) {
		t.Fatalf("Build() error = %v, want ErrPackageLoad", err)
	}
	if got := activeNodeNames(t, s); !reflect.DeepEqual(got, []string{"old-file", "old-repository", "old-symbol"}) {
		t.Fatalf("active graph changed after build failure: %v", got)
	}
	revision, err := s.Revision(context.Background(), "revision-broken")
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != store.RevisionFailed {
		t.Fatalf("broken revision state = %q, want failed", revision.State)
	}
	diagnostics, err := s.Diagnostics(context.Background(), "revision-broken")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) < 1 {
		t.Fatal("failed build retained no diagnostics")
	}
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, repository) || len(diagnostic.Message) > 1024 {
			t.Fatalf("diagnostic was not bounded and sanitized: %#v", diagnostic)
		}
	}
}

func TestBuildRequiresEnabledApprovedCheckoutAndRemote(t *testing.T) {
	repository := newInventoryRepository(t)
	writeInventoryFile(t, repository, "go.mod", "module example.com/approved\n\ngo 1.26\n")
	writeInventoryFile(t, repository, "approved.go", "package approved\n")
	runGit(t, repository, "add", "go.mod", "approved.go")
	runGit(t, repository, "commit", "-m", "approved")
	other := newInventoryRepository(t)
	writeInventoryFile(t, other, "go.mod", "module example.com/other\n\ngo 1.26\n")
	writeInventoryFile(t, other, "other.go", "package other\n")
	runGit(t, other, "add", "go.mod", "other.go")
	runGit(t, other, "commit", "-m", "other")

	s := newTestStore(t, testProjectID)
	idx := index.New(s)
	request := index.BuildRequest{ProjectID: testProjectID, RevisionID: "revision-disabled", Checkout: repository, CanonicalRemote: testCanonicalRemote}
	if err := idx.Build(context.Background(), request); !errors.Is(err, index.ErrProjectDisabled) {
		t.Fatalf("disabled Build() error = %v, want ErrProjectDisabled", err)
	}
	approveBuildConfig(t, s, repository, testCanonicalRemote)
	request.RevisionID = "revision-other"
	request.Checkout = other
	if err := idx.Build(context.Background(), request); !errors.Is(err, index.ErrApprovedCheckoutMismatch) {
		t.Fatalf("other checkout Build() error = %v, want ErrApprovedCheckoutMismatch", err)
	}
	request.RevisionID = "revision-remote"
	request.Checkout = repository
	request.CanonicalRemote = "https://example.com/injected.git"
	if err := idx.Build(context.Background(), request); !errors.Is(err, index.ErrApprovedCheckoutMismatch) {
		t.Fatalf("other remote Build() error = %v, want ErrApprovedCheckoutMismatch", err)
	}
}

type graphSummary struct {
	nodes   []string
	files   []string
	symbols []string
	edges   []string
}

func activeGraphSummary(t *testing.T, s *store.Store) graphSummary {
	t.Helper()
	var summary graphSummary
	if err := s.ReadActive(context.Background(), func(snapshot *store.Snapshot) error {
		nodes, err := snapshot.Nodes(context.Background())
		if err != nil {
			return err
		}
		files, err := snapshot.Files(context.Background())
		if err != nil {
			return err
		}
		symbols, err := snapshot.Symbols(context.Background())
		if err != nil {
			return err
		}
		edges, err := snapshot.Edges(context.Background())
		if err != nil {
			return err
		}
		for _, node := range nodes {
			summary.nodes = append(summary.nodes, string(node.Kind)+"|"+node.Name+"|"+node.Path+"|"+node.ID)
		}
		for _, file := range files {
			summary.files = append(summary.files, file.Path)
		}
		for _, symbol := range symbols {
			summary.symbols = append(summary.symbols, symbol.QualifiedName)
		}
		for _, edge := range edges {
			summary.edges = append(summary.edges, edge.SourceNodeID+"|"+string(edge.Relationship)+"|"+edge.TargetNodeID+"|"+string(edge.Provenance))
		}
		for _, values := range [][]string{summary.nodes, summary.files, summary.symbols, summary.edges} {
			sort.Strings(values)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return summary
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func TestCandidateIsInvisibleUntilAtomicPublish(t *testing.T) {
	s := newTestStore(t, testProjectID)
	idx := index.New(s)
	ctx := context.Background()
	seedValidCandidate(t, s, testProjectID, "revision-old", "old")
	if err := idx.Publish(ctx, "revision-old"); err != nil {
		t.Fatalf("publish old: %v", err)
	}
	seedValidCandidate(t, s, testProjectID, "revision-new", "new")

	if got := activeNodeNames(t, s); !reflect.DeepEqual(got, []string{"old-file", "old-repository", "old-symbol"}) {
		t.Fatalf("active nodes with candidate present = %v, want old revision only", got)
	}
	if err := idx.Publish(ctx, "revision-new"); err != nil {
		t.Fatalf("publish new: %v", err)
	}
	if got := activeNodeNames(t, s); !reflect.DeepEqual(got, []string{"new-file", "new-repository", "new-symbol"}) {
		t.Fatalf("active nodes after publish = %v, want new revision only", got)
	}

	oldRevision, err := s.Revision(ctx, "revision-old")
	if err != nil {
		t.Fatal(err)
	}
	newRevision, err := s.ActiveRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if oldRevision.State != store.RevisionRetired || newRevision.ID != "revision-new" || newRevision.State != store.RevisionActive {
		t.Fatalf("atomic states: old=%#v new=%#v", oldRevision, newRevision)
	}
}

func TestFailedCandidateCleansPayloadAndPreservesActive(t *testing.T) {
	s := newTestStore(t, testProjectID)
	idx := index.New(s)
	ctx := context.Background()
	seedValidCandidate(t, s, testProjectID, "revision-old", "old")
	if err := idx.Publish(ctx, "revision-old"); err != nil {
		t.Fatal(err)
	}
	seedValidCandidate(t, s, testProjectID, "revision-bad", "bad")
	for _, diagnostic := range []store.Diagnostic{
		{ProjectID: testProjectID, RevisionID: "revision-bad", ID: "build-error", Severity: store.DiagnosticError, Code: "go_list_failed", Message: "package metadata unavailable", CreatedAt: time.Now().UTC()},
		{ProjectID: testProjectID, RevisionID: "revision-bad", ID: "validation_failed", Severity: store.DiagnosticWarning, Code: "caller_validation", Message: "caller validation diagnostic", CreatedAt: time.Now().UTC()},
		{ProjectID: testProjectID, RevisionID: "revision-bad", ID: "interrupted_candidate", Severity: store.DiagnosticWarning, Code: "caller_interrupted", Message: "caller interrupted diagnostic", CreatedAt: time.Now().UTC()},
	} {
		if err := s.PutDiagnostic(ctx, diagnostic); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutEdge(ctx, store.Edge{
		ProjectID: testProjectID, RevisionID: "revision-bad", ID: "bad-dangling",
		SourceNodeID: "bad-repository", TargetNodeID: "missing-node",
		Relationship: store.RelationshipCalls, Confidence: 1, Provenance: store.ProvenanceGoTypes,
	}); err != nil {
		t.Fatal(err)
	}

	err := idx.Publish(ctx, "revision-bad")
	if !errors.Is(err, index.ErrInvalidCandidate) {
		t.Fatalf("Publish() error = %v, want ErrInvalidCandidate", err)
	}
	if got := activeNodeNames(t, s); !reflect.DeepEqual(got, []string{"old-file", "old-repository", "old-symbol"}) {
		t.Fatalf("active nodes after failed candidate = %v", got)
	}
	failed, err := s.Revision(ctx, "revision-bad")
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != store.RevisionFailed {
		t.Fatalf("failed candidate state = %q, want failed", failed.State)
	}
	if err := s.ReadRevision(ctx, "revision-bad", func(snapshot *store.Snapshot) error {
		counts, err := snapshot.PayloadCounts(ctx)
		if err != nil {
			return err
		}
		if counts != (store.PayloadCounts{}) {
			t.Fatalf("failed candidate payload = %#v, want empty", counts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := s.Diagnostics(ctx, "revision-bad")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 4 {
		t.Fatalf("failed candidate diagnostics = %#v, want three caller diagnostics and one system failure", diagnostics)
	}
	byID := make(map[string]store.Diagnostic, len(diagnostics))
	for _, diagnostic := range diagnostics {
		byID[diagnostic.ID] = diagnostic
	}
	if byID["validation_failed"].Code != "caller_validation" || byID["validation_failed"].Message != "caller validation diagnostic" {
		t.Fatalf("caller validation diagnostic overwritten: %#v", byID["validation_failed"])
	}
	if byID["interrupted_candidate"].Code != "caller_interrupted" || byID["interrupted_candidate"].Message != "caller interrupted diagnostic" {
		t.Fatalf("caller interrupted diagnostic overwritten: %#v", byID["interrupted_candidate"])
	}
	var systemFailures int
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "validation_failed" {
			systemFailures++
		}
	}
	if systemFailures != 1 {
		t.Fatalf("system validation failures = %d, want 1: %#v", systemFailures, diagnostics)
	}
}

func TestCandidateValidationRejectsInvalidReferencesRangesAndHashes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *store.Store, string)
	}{
		{
			name: "cross revision edge",
			mutate: func(t *testing.T, s *store.Store, revisionID string) {
				t.Helper()
				putEdge(t, s, store.Edge{ProjectID: testProjectID, RevisionID: revisionID, ID: "cross-revision", SourceNodeID: "foreign-node", TargetNodeID: "graph-repository", Relationship: store.RelationshipReferences, Confidence: 1, Provenance: store.ProvenanceGoTypes})
			},
		},
		{
			name: "invalid source range",
			mutate: func(t *testing.T, s *store.Store, revisionID string) {
				t.Helper()
				if err := s.PutNode(context.Background(), store.Node{ProjectID: testProjectID, RevisionID: revisionID, ID: "range-symbol", Kind: store.NodeSymbol, Name: "Range", Path: "graph.go"}); err != nil {
					t.Fatal(err)
				}
				if err := s.PutSymbol(context.Background(), store.Symbol{ProjectID: testProjectID, RevisionID: revisionID, ID: "range-symbol", FileID: "graph-file", QualifiedName: "pkg.Range", Signature: "func Range()", StartByte: 1, EndByte: 101, StartLine: 1, EndLine: 2}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid indexed hash",
			mutate: func(t *testing.T, s *store.Store, revisionID string) {
				t.Helper()
				if err := s.PutNode(context.Background(), store.Node{ProjectID: testProjectID, RevisionID: revisionID, ID: "invalid-file", Kind: store.NodeFile, Name: "invalid.go", Path: "invalid.go"}); err != nil {
					t.Fatal(err)
				}
				if err := s.PutFile(context.Background(), store.File{ProjectID: testProjectID, RevisionID: revisionID, ID: "invalid-file", Path: "invalid.go", IndexedHash: "SHA256:not-canonical", ByteSize: 10}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t, testProjectID)
			idx := index.New(s)
			ctx := context.Background()
			if tt.name == "cross revision edge" {
				if err := s.CreateCandidate(ctx, store.Revision{ProjectID: testProjectID, ID: "foreign-revision", IndexedCommit: strings.Repeat("b", 40), CreatedAt: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				if err := s.PutNode(ctx, store.Node{ProjectID: testProjectID, RevisionID: "foreign-revision", ID: "foreign-node", Kind: store.NodeSymbol, Name: "foreign"}); err != nil {
					t.Fatal(err)
				}
			}
			seedValidCandidate(t, s, testProjectID, "revision-invalid", "graph")
			tt.mutate(t, s, "revision-invalid")
			if err := idx.Publish(ctx, "revision-invalid"); !errors.Is(err, index.ErrInvalidCandidate) {
				t.Fatalf("Publish() error = %v, want ErrInvalidCandidate", err)
			}
		})
	}
}

func TestCandidateValidationRejectsCrossProjectReferences(t *testing.T) {
	db := openIndexSQLite(t, filepath.Join(t.TempDir(), "gateway.db"))
	sA, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	sB, err := store.Open(context.Background(), db, "project-b")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := sB.CreateCandidate(ctx, store.Revision{ProjectID: "project-b", ID: "revision-b", IndexedCommit: strings.Repeat("b", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := sB.PutNode(ctx, store.Node{ProjectID: "project-b", RevisionID: "revision-b", ID: "foreign-node", Kind: store.NodeSymbol, Name: "foreign"}); err != nil {
		t.Fatal(err)
	}
	seedValidCandidate(t, sA, "project-a", "revision-a", "graph")
	putEdge(t, sA, store.Edge{ProjectID: "project-a", RevisionID: "revision-a", ID: "cross-project", SourceNodeID: "foreign-node", TargetNodeID: "graph-repository", Relationship: store.RelationshipReferences, Confidence: 1, Provenance: store.ProvenanceGoTypes})

	if err := index.New(sA).Publish(ctx, "revision-a"); !errors.Is(err, index.ErrInvalidCandidate) {
		t.Fatalf("Publish() error = %v, want ErrInvalidCandidate", err)
	}
	revisionB, err := sB.Revision(ctx, "revision-b")
	if err != nil {
		t.Fatal(err)
	}
	if revisionB.State != store.RevisionCandidate {
		t.Fatalf("other project candidate state = %q, want candidate", revisionB.State)
	}
}

func TestDuplicateDeterministicIDsAreRejected(t *testing.T) {
	s := newTestStore(t, testProjectID)
	ctx := context.Background()
	if err := s.CreateCandidate(ctx, store.Revision{ProjectID: testProjectID, ID: "revision-a", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	node := store.Node{ProjectID: testProjectID, RevisionID: "revision-a", ID: "deterministic-id", Kind: store.NodeSymbol, Name: "Symbol"}
	if err := s.PutNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := s.PutNode(ctx, node); !errors.Is(err, store.ErrDuplicateID) {
		t.Fatalf("duplicate PutNode() error = %v, want ErrDuplicateID", err)
	}
}

func TestReaderPinsOneSQLiteSnapshotAcrossPublish(t *testing.T) {
	s := newTestStore(t, testProjectID)
	idx := index.New(s)
	ctx := context.Background()
	seedValidCandidate(t, s, testProjectID, "revision-old", "old")
	if err := idx.Publish(ctx, "revision-old"); err != nil {
		t.Fatal(err)
	}
	seedValidCandidate(t, s, testProjectID, "revision-new", "new")

	started := make(chan []string, 1)
	release := make(chan struct{})
	secondRead := make(chan []string, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.ReadActive(ctx, func(snapshot *store.Snapshot) error {
			first, err := snapshot.Nodes(ctx)
			if err != nil {
				return err
			}
			started <- sortedNodeNames(first)
			<-release
			second, err := snapshot.Nodes(ctx)
			if err != nil {
				return err
			}
			secondRead <- sortedNodeNames(second)
			return nil
		})
	}()

	first := <-started
	if err := idx.Publish(ctx, "revision-new"); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	second := <-secondRead
	wantOld := []string{"old-file", "old-repository", "old-symbol"}
	if !reflect.DeepEqual(first, wantOld) || !reflect.DeepEqual(second, wantOld) {
		t.Fatalf("pinned reader changed revision: first=%v second=%v", first, second)
	}
	if got := activeNodeNames(t, s); !reflect.DeepEqual(got, []string{"new-file", "new-repository", "new-symbol"}) {
		t.Fatalf("new reader nodes = %v", got)
	}
}

func TestOpenRecoversOnlyBoundProjectsInterruptedCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db := openIndexSQLite(t, path)
	ctx := context.Background()
	sA, err := store.Open(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	seedValidCandidate(t, sA, "project-a", "revision-active", "active")
	if err := index.New(sA).Publish(ctx, "revision-active"); err != nil {
		t.Fatal(err)
	}
	seedValidCandidate(t, sA, "project-a", "revision-interrupted-a", "interrupted-a")
	if err := sA.PutDiagnostic(ctx, store.Diagnostic{
		ProjectID: "project-a", RevisionID: "revision-interrupted-a", ID: "interrupted_candidate",
		Severity: store.DiagnosticWarning, Code: "caller_interrupted", Message: "caller diagnostic", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	sB, err := store.Open(ctx, db, "project-b")
	if err != nil {
		t.Fatal(err)
	}
	seedValidCandidate(t, sB, "project-b", "revision-interrupted-b", "interrupted-b")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = openIndexSQLite(t, path)
	sA, err = store.OpenRecovering(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	active, err := sA.ActiveRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != "revision-active" {
		t.Fatalf("active revision after restart = %q", active.ID)
	}
	interruptedA, err := sA.Revision(ctx, "revision-interrupted-a")
	if err != nil {
		t.Fatal(err)
	}
	if interruptedA.State != store.RevisionFailed {
		t.Fatalf("project A interrupted state = %q, want failed", interruptedA.State)
	}
	diagnosticsA, err := sA.Diagnostics(ctx, "revision-interrupted-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnosticsA) != 2 {
		t.Fatalf("interrupted diagnostics = %#v, want caller and system failures", diagnosticsA)
	}
	var callerPreserved, systemRetained bool
	for _, diagnostic := range diagnosticsA {
		callerPreserved = callerPreserved || (diagnostic.ID == "interrupted_candidate" && diagnostic.Code == "caller_interrupted" && diagnostic.Message == "caller diagnostic")
		systemRetained = systemRetained || diagnostic.Code == "interrupted_candidate"
	}
	if !callerPreserved || !systemRetained {
		t.Fatalf("interrupted diagnostics lost or overwritten: %#v", diagnosticsA)
	}
	var stateB store.RevisionState
	if err := db.QueryRow("SELECT state FROM codegraph_revisions WHERE project_id = ? AND revision_id = ?", "project-b", "revision-interrupted-b").Scan(&stateB); err != nil {
		t.Fatal(err)
	}
	if stateB != store.RevisionCandidate {
		t.Fatalf("unopened project B state = %q, want candidate", stateB)
	}
	if _, err := store.OpenRecovering(ctx, db, "project-b"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT state FROM codegraph_revisions WHERE project_id = ? AND revision_id = ?", "project-b", "revision-interrupted-b").Scan(&stateB); err != nil {
		t.Fatal(err)
	}
	if stateB != store.RevisionFailed {
		t.Fatalf("opened project B state = %q, want failed", stateB)
	}
}

func seedValidCandidate(t *testing.T, s *store.Store, projectID, revisionID, prefix string) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateCandidate(ctx, store.Revision{ProjectID: projectID, ID: revisionID, IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, node := range []store.Node{
		{ProjectID: projectID, RevisionID: revisionID, ID: prefix + "-repository", Kind: store.NodeRepository, Name: prefix + "-repository"},
		{ProjectID: projectID, RevisionID: revisionID, ID: prefix + "-file", Kind: store.NodeFile, Name: prefix + "-file", Path: prefix + ".go"},
		{ProjectID: projectID, RevisionID: revisionID, ID: prefix + "-symbol", Kind: store.NodeSymbol, Name: prefix + "-symbol", Path: prefix + ".go"},
	} {
		if err := s.PutNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutFile(ctx, store.File{ProjectID: projectID, RevisionID: revisionID, ID: prefix + "-file", Path: prefix + ".go", IndexedHash: "sha256:" + strings.Repeat("a", 64), ByteSize: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSymbol(ctx, store.Symbol{ProjectID: projectID, RevisionID: revisionID, ID: prefix + "-symbol", FileID: prefix + "-file", QualifiedName: "pkg." + prefix, Signature: "func " + prefix + "()", StartByte: 0, EndByte: 10, StartLine: 1, EndLine: 1}); err != nil {
		t.Fatal(err)
	}
	putEdge(t, s, store.Edge{ProjectID: projectID, RevisionID: revisionID, ID: prefix + "-contains", SourceNodeID: prefix + "-repository", TargetNodeID: prefix + "-file", Relationship: store.RelationshipContains, Confidence: 1, Provenance: store.ProvenanceGoPackages})
	putEdge(t, s, store.Edge{ProjectID: projectID, RevisionID: revisionID, ID: prefix + "-defines", SourceNodeID: prefix + "-file", TargetNodeID: prefix + "-symbol", Relationship: store.RelationshipDefines, Confidence: 1, Provenance: store.ProvenanceGoAST})
}

func putEdge(t *testing.T, s *store.Store, edge store.Edge) {
	t.Helper()
	if err := s.PutEdge(context.Background(), edge); err != nil {
		t.Fatal(err)
	}
}

func activeNodeNames(t *testing.T, s *store.Store) []string {
	t.Helper()
	var names []string
	if err := s.ReadActive(context.Background(), func(snapshot *store.Snapshot) error {
		nodes, err := snapshot.Nodes(context.Background())
		if err != nil {
			return err
		}
		names = sortedNodeNames(nodes)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return names
}

func sortedNodeNames(nodes []store.Node) []string {
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.Name)
	}
	sort.Strings(names)
	return names
}

func newTestStore(t *testing.T, projectID string) *store.Store {
	t.Helper()
	db := openIndexSQLite(t, filepath.Join(t.TempDir(), "gateway.db"))
	s, err := store.Open(context.Background(), db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func approveBuildConfig(t *testing.T, graphStore *store.Store, checkout, remote string) {
	t.Helper()
	if err := graphStore.PutProjectConfig(context.Background(), cgconfig.Project{
		ProjectID: testProjectID, Enabled: true, CanonicalRemote: remote, ActiveCheckout: checkout,
		ProjectSourceByteCeiling: cgconfig.DefaultProjectSourceByteCeiling,
	}); err != nil {
		t.Fatal(err)
	}
}

func openIndexSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	u := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := u.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
