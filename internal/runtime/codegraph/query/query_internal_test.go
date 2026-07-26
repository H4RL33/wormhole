package query

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

func TestTokenizeInitialismsCamelSnakeAndUnicode(t *testing.T) {
	want := []string{"http", "server", "jwt", "validator", "snake", "case", "café", "über"}
	if got := tokenize("HTTPServer JWTValidator snake_case caféÜber"); !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenize() = %v, want %v", got, want)
	}
}

func TestSortRankedUsesStructuralTieBreakChain(t *testing.T) {
	evidence := func(distance, relationship int, confidence float64, provenance int) structuralEvidence {
		return structuralEvidence{found: true, distance: distance, relationship: relationship, confidence: confidence, provenance: provenance}
	}
	tests := []struct {
		name        string
		left, right structuralEvidence
	}{
		{name: "distance", left: evidence(1, 4, .5, 4), right: evidence(2, 0, 1, 0)},
		{name: "relationship", left: evidence(1, 0, .5, 4), right: evidence(1, 1, 1, 0)},
		{name: "confidence", left: evidence(1, 0, .9, 4), right: evidence(1, 0, .8, 0)},
		{name: "provenance", left: evidence(1, 0, .9, 0), right: evidence(1, 0, .9, 4)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := []rankedMatch{
				{category: 2, overlap: 1, evidence: test.right, match: Match{QualifiedName: "a.Right", SymbolID: "right"}},
				{category: 2, overlap: 1, evidence: test.left, match: Match{QualifiedName: "z.Left", SymbolID: "left"}},
			}
			sortRanked(values)
			if values[0].match.SymbolID != "left" {
				t.Fatalf("rank order = %#v", values)
			}
		})
	}
}

func TestRankMatchesHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rankMatches(ctx, Request{Intent: "worker"}, []store.SymbolRecord{{Name: "Worker"}})
	if err != context.Canceled {
		t.Fatalf("rankMatches() error = %v, want context.Canceled", err)
	}
}

func TestExecuteKeepsOneRevisionDuringConcurrentPublication(t *testing.T) {
	checkout := t.TempDir()
	content := []byte("package p\nfunc OldSymbol() {}\nfunc NewSymbol() {}\n")
	if err := os.WriteFile(filepath.Join(checkout, "source.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "graph.db"), OmitHost: true}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	graphStore, err := store.Open(context.Background(), db, "project-coherent")
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutProjectConfig(context.Background(), config.Project{
		ProjectID: "project-coherent", Enabled: true, CanonicalRemote: "https://example.com/repo.git",
		ActiveCheckout: checkout, ProjectSourceByteCeiling: 128,
	}); err != nil {
		t.Fatal(err)
	}
	seedCoherenceRevision(t, graphStore, content, "revision-old", "OldSymbol", 10, 29, strings.Repeat("a", 40))
	if err := index.New(graphStore).Publish(context.Background(), "revision-old"); err != nil {
		t.Fatal(err)
	}
	seedCoherenceRevision(t, graphStore, content, "revision-new", "NewSymbol", 30, 49, strings.Repeat("b", 40))

	service := New(graphStore, 128)
	snapshotStarted := make(chan struct{})
	release := make(chan struct{})
	type response struct {
		result Result
		err    error
	}
	responseCh := make(chan response, 1)
	go func() {
		result, err := service.execute(context.Background(), Request{EntrySymbols: []string{"OldSymbol"}, MaxNodes: 10}, func(store.Revision) {
			close(snapshotStarted)
			<-release
		})
		responseCh <- response{result: result, err: err}
	}()
	<-snapshotStarted
	if err := index.New(graphStore).Publish(context.Background(), "revision-new"); err != nil {
		t.Fatal(err)
	}
	close(release)
	old := <-responseCh
	if old.err != nil {
		t.Fatal(old.err)
	}
	if old.result.RevisionID != "revision-old" || len(old.result.Matches) != 1 || old.result.Matches[0].QualifiedName != "example.com/p.OldSymbol" {
		t.Fatalf("mixed snapshot response = %#v", old.result)
	}
	current, err := service.Execute(context.Background(), Request{EntrySymbols: []string{"NewSymbol"}, MaxNodes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if current.RevisionID != "revision-new" || len(current.Matches) != 1 {
		t.Fatalf("new active response = %#v", current)
	}
}

func seedCoherenceRevision(t *testing.T, graphStore *store.Store, content []byte, revisionID, symbolName string, start, end int64, commit string) {
	t.Helper()
	ctx := context.Background()
	if err := graphStore.CreateCandidate(ctx, store.Revision{ProjectID: "project-coherent", ID: revisionID, IndexedCommit: commit, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, node := range []store.Node{
		{ProjectID: "project-coherent", RevisionID: revisionID, ID: "repository", Kind: store.NodeRepository, Name: "repo"},
		{ProjectID: "project-coherent", RevisionID: revisionID, ID: "file", Kind: store.NodeFile, Name: "source.go", Path: "source.go"},
		{ProjectID: "project-coherent", RevisionID: revisionID, ID: "symbol", Kind: store.NodeSymbol, Name: symbolName, Path: "source.go"},
	} {
		if err := graphStore.PutNode(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256(content)
	if err := graphStore.PutFile(ctx, store.File{ProjectID: "project-coherent", RevisionID: revisionID, ID: "file", Path: "source.go", IndexedHash: "sha256:" + hex.EncodeToString(digest[:]), ByteSize: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutSymbol(ctx, store.Symbol{ProjectID: "project-coherent", RevisionID: revisionID, ID: "symbol", FileID: "file", QualifiedName: "example.com/p." + symbolName, Signature: "func " + symbolName + "()", StartByte: start, EndByte: end, StartLine: 2, EndLine: 2}); err != nil {
		t.Fatal(err)
	}
	for _, edge := range []store.Edge{
		{ProjectID: "project-coherent", RevisionID: revisionID, ID: "contains", SourceNodeID: "repository", TargetNodeID: "file", Relationship: store.RelationshipContains, Confidence: 1, Provenance: store.ProvenanceGoPackages},
		{ProjectID: "project-coherent", RevisionID: revisionID, ID: "defines", SourceNodeID: "file", TargetNodeID: "symbol", Relationship: store.RelationshipDefines, Confidence: 1, Provenance: store.ProvenanceGoAST},
	} {
		if err := graphStore.PutEdge(ctx, edge); err != nil {
			t.Fatal(err)
		}
	}
}
