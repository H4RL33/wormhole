package index

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

func TestCandidateValidationRejectsEachStructuralInvariant(t *testing.T) {
	tests := []struct {
		name   string
		seed   func(*testing.T, *store.Store, string)
		mutate func(*testing.T, *store.Store, string)
	}{
		{name: "invalid commit", seed: seedValidationGraph, mutate: nil},
		{name: "missing repository", seed: func(*testing.T, *store.Store, string) {}, mutate: nil},
		{name: "multiple repositories", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			putValidationNode(t, s, revision, store.Node{ID: "repository-two", Kind: store.NodeRepository})
		}},
		{name: "file node kind", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			putValidationNode(t, s, revision, store.Node{ID: "bad-file", Kind: store.NodeSymbol, Path: "bad.go"})
			putValidationFile(t, s, revision, store.File{ID: "bad-file", Path: "bad.go", IndexedHash: validationHash(), ByteSize: 4})
		}},
		{name: "file path mismatch", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			putValidationNode(t, s, revision, store.Node{ID: "bad-file", Kind: store.NodeFile, Path: "node.go"})
			putValidationFile(t, s, revision, store.File{ID: "bad-file", Path: "record.go", IndexedHash: validationHash(), ByteSize: 4})
		}},
		{name: "symbol node missing", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			putValidationSymbol(t, s, revision, store.Symbol{ID: "missing-symbol", FileID: "file", StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1})
		}},
		{name: "symbol file missing", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			putValidationNode(t, s, revision, store.Node{ID: "orphan-symbol", Kind: store.NodeSymbol, Path: "file.go"})
			putValidationSymbol(t, s, revision, store.Symbol{ID: "orphan-symbol", FileID: "missing-file", StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1})
		}},
		{name: "symbol path mismatch", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			putValidationNode(t, s, revision, store.Node{ID: "path-symbol", Kind: store.NodeSymbol, Path: "other.go"})
			putValidationSymbol(t, s, revision, store.Symbol{ID: "path-symbol", FileID: "file", StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1})
		}},
		{name: "symbol line range", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			putValidationNode(t, s, revision, store.Node{ID: "line-symbol", Kind: store.NodeSymbol, Path: "file.go"})
			putValidationSymbol(t, s, revision, store.Symbol{ID: "line-symbol", FileID: "file", StartByte: 0, EndByte: 1, StartLine: 0, EndLine: 1})
		}},
		{name: "edge target missing", seed: seedValidationGraph, mutate: func(t *testing.T, s *store.Store, revision string) {
			if err := s.PutEdge(context.Background(), store.Edge{ProjectID: "project-a", RevisionID: revision, ID: "dangling", SourceNodeID: "repository", TargetNodeID: "missing", Relationship: store.RelationshipContains, Confidence: 1, Provenance: store.ProvenanceGoAST}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newInternalBuildStore(t)
			revision := "candidate"
			commit := strings.Repeat("a", 40)
			if tt.name == "invalid commit" {
				commit = "not-a-hash"
			}
			if err := s.CreateCandidate(context.Background(), store.Revision{ProjectID: "project-a", ID: revision, IndexedCommit: commit, CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			tt.seed(t, s, revision)
			if tt.mutate != nil {
				tt.mutate(t, s, revision)
			}
			err := s.ReadRevision(context.Background(), revision, func(snapshot *store.Snapshot) error {
				return validateCandidate(context.Background(), snapshot)
			})
			if !errors.Is(err, ErrInvalidCandidate) {
				t.Fatalf("validateCandidate error = %v, want ErrInvalidCandidate", err)
			}
		})
	}
}

func TestCandidateValidationFailsClosedOnInvalidStateAndUnreadablePayload(t *testing.T) {
	ctx := context.Background()

	t.Run("non-candidate revision", func(t *testing.T) {
		db := openValidationSQLite(t)
		s, err := store.Open(ctx, db, "project-a")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateCandidate(ctx, store.Revision{ProjectID: "project-a", ID: "failed", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE codegraph_revisions SET state='failed' WHERE project_id='project-a' AND revision_id='failed'`); err != nil {
			t.Fatal(err)
		}
		err = s.ReadRevision(ctx, "failed", func(snapshot *store.Snapshot) error { return validateCandidate(ctx, snapshot) })
		if !errors.Is(err, ErrInvalidCandidate) {
			t.Fatalf("validateCandidate failed revision error = %v", err)
		}
	})

	t.Run("empty node id", func(t *testing.T) {
		db := openValidationSQLite(t)
		s, err := store.Open(ctx, db, "project-a")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateCandidate(ctx, store.Revision{ProjectID: "project-a", ID: "candidate", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO codegraph_nodes(project_id, revision_id, node_id, kind, name, path) VALUES ('project-a', 'candidate', '', 'repository', '', '')`); err != nil {
			t.Fatal(err)
		}
		err = s.ReadRevision(ctx, "candidate", func(snapshot *store.Snapshot) error { return validateCandidate(ctx, snapshot) })
		if !errors.Is(err, ErrInvalidCandidate) {
			t.Fatalf("validateCandidate empty node error = %v", err)
		}
	})

	for _, table := range []string{"codegraph_nodes", "codegraph_files", "codegraph_symbols", "codegraph_edges"} {
		t.Run("missing "+table, func(t *testing.T) {
			db := openValidationSQLite(t)
			s, err := store.Open(ctx, db, "project-a")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateCandidate(ctx, store.Revision{ProjectID: "project-a", ID: "candidate", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `DROP TABLE `+table); err != nil {
				t.Fatal(err)
			}
			err = s.ReadRevision(ctx, "candidate", func(snapshot *store.Snapshot) error { return validateCandidate(ctx, snapshot) })
			if err == nil {
				t.Fatalf("validateCandidate succeeded without %s", table)
			}
		})
	}
}

func openValidationSQLite(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "gateway.db"), OmitHost: true}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedValidationGraph(t *testing.T, s *store.Store, revision string) {
	t.Helper()
	putValidationNode(t, s, revision, store.Node{ID: "repository", Kind: store.NodeRepository})
	putValidationNode(t, s, revision, store.Node{ID: "file", Kind: store.NodeFile, Path: "file.go"})
	putValidationFile(t, s, revision, store.File{ID: "file", Path: "file.go", IndexedHash: validationHash(), ByteSize: 4})
}

func putValidationNode(t *testing.T, s *store.Store, revision string, node store.Node) {
	t.Helper()
	node.ProjectID, node.RevisionID = "project-a", revision
	if err := s.PutNode(context.Background(), node); err != nil {
		t.Fatal(err)
	}
}

func putValidationFile(t *testing.T, s *store.Store, revision string, file store.File) {
	t.Helper()
	file.ProjectID, file.RevisionID = "project-a", revision
	if err := s.PutFile(context.Background(), file); err != nil {
		t.Fatal(err)
	}
}

func putValidationSymbol(t *testing.T, s *store.Store, revision string, symbol store.Symbol) {
	t.Helper()
	symbol.ProjectID, symbol.RevisionID = "project-a", revision
	if err := s.PutSymbol(context.Background(), symbol); err != nil {
		t.Fatal(err)
	}
}

func validationHash() string { return "sha256:" + strings.Repeat("b", 64) }
