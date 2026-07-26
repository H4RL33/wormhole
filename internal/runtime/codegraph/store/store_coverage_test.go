package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	_ "modernc.org/sqlite"
)

func TestStoreRejectsInvalidAndOutOfStateWrites(t *testing.T) {
	ctx := context.Background()
	graphStore, _ := openCoverageStore(t)
	if err := graphStore.PutProjectConfig(ctx, config.Project{ProjectID: "other"}); !errors.Is(err, ErrProjectScope) {
		t.Fatalf("cross-project config error = %v", err)
	}
	if err := graphStore.PutProjectConfig(ctx, config.Project{ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://user:secret@example.com/repo.git", ActiveCheckout: "/repo", ProjectSourceByteCeiling: 1}); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("credential remote error = %v", err)
	}
	if err := graphStore.PutProjectConfig(ctx, config.Project{ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.com/repo.git", ActiveCheckout: "/repo"}); err == nil {
		t.Fatal("zero source ceiling accepted")
	}
	if err := graphStore.BeginBuild(ctx, ""); err == nil {
		t.Fatal("empty build token accepted")
	}
	if err := graphStore.EndBuild(ctx, "absent"); err != nil {
		t.Fatalf("ending absent lease = %v", err)
	}
	if err := graphStore.CreateCandidate(ctx, Revision{ProjectID: "other"}); !errors.Is(err, ErrProjectScope) {
		t.Fatalf("cross-project candidate error = %v", err)
	}
	if err := graphStore.CreateCandidate(ctx, Revision{ProjectID: "project-a"}); err == nil {
		t.Fatal("incomplete candidate accepted")
	}
	createCoverageCandidate(t, graphStore, "candidate")
	if err := graphStore.CreateCandidate(ctx, Revision{ProjectID: "project-a", ID: "candidate", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now()}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate candidate error = %v", err)
	}

	crossProjectWrites := []func() error{
		func() error { return graphStore.PutNode(ctx, Node{ProjectID: "other"}) },
		func() error { return graphStore.PutFile(ctx, File{ProjectID: "other"}) },
		func() error { return graphStore.PutSymbol(ctx, Symbol{ProjectID: "other"}) },
		func() error { return graphStore.PutEdge(ctx, Edge{ProjectID: "other"}) },
		func() error { return graphStore.PutDiagnostic(ctx, Diagnostic{ProjectID: "other"}) },
	}
	for index, write := range crossProjectWrites {
		if err := write(); !errors.Is(err, ErrProjectScope) {
			t.Errorf("cross-project write %d error = %v", index, err)
		}
	}
	if err := graphStore.PutDiagnostic(ctx, Diagnostic{ProjectID: "project-a", RevisionID: "candidate", ID: "missing-time"}); err == nil {
		t.Fatal("diagnostic without creation time accepted")
	}

	node := Node{ProjectID: "project-a", RevisionID: "candidate", ID: "repository", Kind: NodeRepository, Name: "repo"}
	if err := graphStore.PutNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutNode(ctx, node); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate node error = %v", err)
	}
	if err := graphStore.PutNode(ctx, Node{ProjectID: "project-a", RevisionID: "missing", ID: "node", Kind: NodeFile}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing candidate write error = %v", err)
	}
	if err := graphStore.PutFile(ctx, File{ProjectID: "project-a", RevisionID: "candidate", ID: "bad", Path: "bad.go", ByteSize: -1}); err == nil {
		t.Fatal("negative file size accepted")
	}
	if err := graphStore.PutEdge(ctx, Edge{ProjectID: "project-a", RevisionID: "candidate", ID: "bad", Relationship: RelationshipCalls, Confidence: 2, Provenance: ProvenanceGoAST}); err == nil {
		t.Fatal("out-of-range edge confidence accepted")
	}
	if err := graphStore.FailCandidate(ctx, "", "code", "message"); err == nil {
		t.Fatal("empty failed candidate accepted")
	}
	if err := graphStore.FailCandidate(ctx, "candidate", "", "message"); err == nil {
		t.Fatal("empty failure code accepted")
	}
	if err := graphStore.FailCandidate(ctx, "missing", "code", "message"); !errors.Is(err, ErrNotCandidate) {
		t.Fatalf("missing failed candidate error = %v", err)
	}
}

func TestStorePublicationGuardsPreserveAtomicState(t *testing.T) {
	ctx := context.Background()
	graphStore, _ := openCoverageStore(t)
	validationErr := errors.New("candidate rejected")
	createCoverageCandidate(t, graphStore, "invalid")
	if err := graphStore.PublishCandidate(ctx, "invalid", func(context.Context, *Snapshot) error { return validationErr }); !errors.Is(err, validationErr) {
		t.Fatalf("validator error = %v", err)
	}
	assertCoverageRevisionState(t, graphStore, "invalid", RevisionFailed)

	authorizationErr := errors.New("binding rotated")
	createCoverageCandidate(t, graphStore, "unauthorized")
	if err := graphStore.PublishCandidateGuarded(ctx, "unauthorized", func(context.Context, PublicationReader) error { return authorizationErr }, func(context.Context, *Snapshot) error { return nil }); !errors.Is(err, authorizationErr) {
		t.Fatalf("guard error = %v", err)
	}
	assertCoverageRevisionState(t, graphStore, "unauthorized", RevisionFailed)

	if err := graphStore.PublishCandidate(ctx, "missing", func(context.Context, *Snapshot) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing publication error = %v", err)
	}
	if err := graphStore.PublishCandidate(ctx, "invalid", func(context.Context, *Snapshot) error { return nil }); !errors.Is(err, ErrNotCandidate) {
		t.Fatalf("failed revision publication error = %v", err)
	}

	createCoverageCandidate(t, graphStore, "cas")
	expected, err := graphStore.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := config.Project{ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.com/repo.git", ActiveCheckout: "/repo", ProjectSourceByteCeiling: config.DefaultProjectSourceByteCeiling}
	changed := expected
	changed.ProjectSourceByteCeiling++
	if err := graphStore.PutProjectConfig(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PublishCandidateWithConfig(ctx, "cas", expected, "", next, func(context.Context, *Snapshot) error { return nil }); err == nil {
		t.Fatal("configuration CAS mismatch published")
	}
	assertCoverageRevisionState(t, graphStore, "cas", RevisionCandidate)

	invalidConfigs := []struct {
		name     string
		expected config.Project
		next     config.Project
	}{
		{name: "expected project", expected: config.Project{ProjectID: "other"}, next: next},
		{name: "next project", expected: expected, next: config.Project{ProjectID: "other", Enabled: true}},
		{name: "disabled", expected: expected, next: config.Project{ProjectID: "project-a"}},
		{name: "credential remote", expected: expected, next: config.Project{ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://u:p@example.com/repo.git", ActiveCheckout: "/repo", ProjectSourceByteCeiling: 1}},
	}
	for _, test := range invalidConfigs {
		t.Run(test.name, func(t *testing.T) {
			if err := graphStore.PublishCandidateWithConfig(ctx, "cas", test.expected, "", test.next, func(context.Context, *Snapshot) error { return nil }); err == nil {
				t.Fatal("invalid lifecycle publication accepted")
			}
		})
	}

	createCoverageCandidate(t, graphStore, "active")
	current, err := graphStore.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PublishCandidateWithConfig(ctx, "active", current, "", next, func(context.Context, *Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	active, err := graphStore.ActiveRevision(ctx)
	if err != nil || active.ID != "active" {
		t.Fatalf("active revision = %+v, %v", active, err)
	}
}

func TestStorePublicationRollsBackDatabaseFailures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(*testing.T, *Store, *sql.DB)
		want    string
	}{
		{
			name: "retire active revision",
			prepare: func(t *testing.T, s *Store, db *sql.DB) {
				createCoverageCandidate(t, s, "old")
				if err := s.PublishCandidate(ctx, "old", func(context.Context, *Snapshot) error { return nil }); err != nil {
					t.Fatal(err)
				}
				createCoverageCandidate(t, s, "candidate")
				if _, err := db.Exec(`CREATE TRIGGER reject_retirement BEFORE UPDATE OF state ON codegraph_revisions WHEN OLD.state='active' AND NEW.state='retired' BEGIN SELECT RAISE(ABORT, 'retirement rejected'); END`); err != nil {
					t.Fatal(err)
				}
			},
			want: "retire active revision",
		},
		{
			name: "activate candidate",
			prepare: func(t *testing.T, s *Store, db *sql.DB) {
				createCoverageCandidate(t, s, "candidate")
				if _, err := db.Exec(`CREATE TRIGGER reject_activation BEFORE UPDATE OF state ON codegraph_revisions WHEN NEW.state='active' BEGIN SELECT RAISE(ABORT, 'activation rejected'); END`); err != nil {
					t.Fatal(err)
				}
			},
			want: "activate revision",
		},
		{
			name: "activation ignored",
			prepare: func(t *testing.T, s *Store, db *sql.DB) {
				createCoverageCandidate(t, s, "candidate")
				if _, err := db.Exec(`CREATE TRIGGER ignore_activation BEFORE UPDATE OF state ON codegraph_revisions WHEN NEW.state='active' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
			},
			want: "revision is not a candidate",
		},
		{
			name: "active pointer update",
			prepare: func(t *testing.T, s *Store, db *sql.DB) {
				createCoverageCandidate(t, s, "candidate")
				if _, err := db.Exec(`CREATE TRIGGER reject_pointer BEFORE UPDATE OF active_revision_id ON codegraph_config BEGIN SELECT RAISE(ABORT, 'pointer rejected'); END`); err != nil {
					t.Fatal(err)
				}
			},
			want: "update active pointer",
		},
		{
			name: "missing active pointer row",
			prepare: func(t *testing.T, s *Store, db *sql.DB) {
				createCoverageCandidate(t, s, "candidate")
				if _, err := db.Exec(`DELETE FROM codegraph_config WHERE project_id='project-a'`); err != nil {
					t.Fatal(err)
				}
			},
			want: `project config "project-a" missing`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, db := openCoverageStore(t)
			test.prepare(t, s, db)
			err := s.PublishCandidate(ctx, "candidate", func(context.Context, *Snapshot) error { return nil })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PublishCandidate error = %v, want %q", err, test.want)
			}
			assertCoverageRevisionState(t, s, "candidate", RevisionCandidate)
		})
	}
}

func TestStoreLifecyclePublicationFailsClosedWithoutConfig(t *testing.T) {
	ctx := context.Background()
	s, db := openCoverageStore(t)
	createCoverageCandidate(t, s, "candidate")
	expected, err := s.ProjectConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next := config.Project{ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.com/repo.git", ActiveCheckout: "/repo", ProjectSourceByteCeiling: config.DefaultProjectSourceByteCeiling}
	if _, err := db.ExecContext(ctx, `DELETE FROM codegraph_config WHERE project_id='project-a'`); err != nil {
		t.Fatal(err)
	}
	err = s.PublishCandidateWithConfig(ctx, "candidate", expected, "", next, func(context.Context, *Snapshot) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "read lifecycle configuration") {
		t.Fatalf("PublishCandidateWithConfig error = %v", err)
	}
	assertCoverageRevisionState(t, s, "candidate", RevisionCandidate)
}

func TestStorePropagatesUnavailableDatabaseAcrossOperations(t *testing.T) {
	ctx := context.Background()
	graphStore, db := openCoverageStore(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		call func() error
	}{
		{name: "put config", call: func() error { return graphStore.PutProjectConfig(ctx, config.DefaultProject("project-a")) }},
		{name: "project config", call: func() error { _, err := graphStore.ProjectConfig(ctx); return err }},
		{name: "database size", call: func() error { _, err := graphStore.DatabaseSize(ctx); return err }},
		{name: "begin build", call: func() error { return graphStore.BeginBuild(ctx, "build") }},
		{name: "end build", call: func() error { return graphStore.EndBuild(ctx, "build") }},
		{name: "candidate", call: func() error {
			return graphStore.CreateCandidate(ctx, Revision{ProjectID: "project-a", ID: "revision", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now()})
		}},
		{name: "node", call: func() error {
			return graphStore.PutNode(ctx, Node{ProjectID: "project-a", RevisionID: "revision", ID: "node", Kind: NodeFile})
		}},
		{name: "file", call: func() error {
			return graphStore.PutFile(ctx, File{ProjectID: "project-a", RevisionID: "revision", ID: "file"})
		}},
		{name: "symbol", call: func() error {
			return graphStore.PutSymbol(ctx, Symbol{ProjectID: "project-a", RevisionID: "revision", ID: "symbol"})
		}},
		{name: "edge", call: func() error {
			return graphStore.PutEdge(ctx, Edge{ProjectID: "project-a", RevisionID: "revision", ID: "edge"})
		}},
		{name: "diagnostic", call: func() error {
			return graphStore.PutDiagnostic(ctx, Diagnostic{ProjectID: "project-a", RevisionID: "revision", ID: "diagnostic", CreatedAt: time.Now()})
		}},
		{name: "revision", call: func() error { _, err := graphStore.Revision(ctx, "revision"); return err }},
		{name: "active", call: func() error { _, err := graphStore.ActiveRevision(ctx); return err }},
		{name: "diagnostics", call: func() error { _, err := graphStore.Diagnostics(ctx, "revision"); return err }},
		{name: "latest diagnostics", call: func() error { _, err := graphStore.LatestDiagnostics(ctx); return err }},
		{name: "read revision", call: func() error { return graphStore.ReadRevision(ctx, "revision", func(*Snapshot) error { return nil }) }},
		{name: "read active", call: func() error { return graphStore.ReadActive(ctx, func(*Snapshot) error { return nil }) }},
		{name: "publish", call: func() error {
			return graphStore.PublishCandidate(ctx, "revision", func(context.Context, *Snapshot) error { return nil })
		}},
		{name: "fail", call: func() error { return graphStore.FailCandidate(ctx, "revision", "failed", "failed") }},
		{name: "disable", call: func() error { return graphStore.Disable(ctx) }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.call(); err == nil {
				t.Fatal("operation succeeded with closed database")
			}
		})
	}
}

func TestSnapshotQueriesPropagateClosedTransactionAndCoverEmptyFilters(t *testing.T) {
	ctx := context.Background()
	graphStore, db := openCoverageStore(t)
	createCoverageCandidate(t, graphStore, "snapshot")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &Snapshot{tx: tx, revision: Revision{ProjectID: "project-a", ID: "snapshot", State: RevisionCandidate}}
	if _, _, err := snapshot.SearchSymbols(ctx, nil, nil, 0); err == nil {
		t.Fatal("zero symbol search limit accepted")
	}
	if records, err := snapshot.SymbolRecords(ctx, nil); err != nil || len(records) != 0 {
		t.Fatalf("empty symbol records = %v/%v", records, err)
	}
	if nodes, err := snapshot.NodesByIDs(ctx, nil); err != nil || len(nodes) != 0 {
		t.Fatalf("empty nodes = %v/%v", nodes, err)
	}
	if edges, err := snapshot.EdgesForNodes(ctx, EdgeSelection{}, nil, 0, 0); err != nil || len(edges) != 0 {
		t.Fatalf("empty edge selection = %v/%v", edges, err)
	}
	if count, err := snapshot.CountEdgesForNodes(ctx, EdgeSelection{}, nil, 0); err != nil || count != 0 {
		t.Fatalf("empty edge count = %d/%v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	queries := []func() error{
		func() error { _, err := snapshot.ProjectConfig(ctx); return err },
		func() error { _, err := snapshot.Nodes(ctx); return err },
		func() error { _, err := snapshot.Files(ctx); return err },
		func() error { _, err := snapshot.Symbols(ctx); return err },
		func() error { _, err := snapshot.Edges(ctx); return err },
		func() error {
			_, _, err := snapshot.SearchSymbols(ctx, []string{"Symbol"}, []string{"term"}, 1)
			return err
		},
		func() error { _, err := snapshot.SymbolRecords(ctx, []string{"symbol"}); return err },
		func() error { _, err := snapshot.NodesByIDs(ctx, []string{"node"}); return err },
		func() error {
			_, err := snapshot.EdgesForNodes(ctx, EdgeSelection{SourceNodeIDs: []string{"node"}}, []Relationship{RelationshipCalls}, .5, 1)
			return err
		},
		func() error {
			_, err := snapshot.CountEdgesForNodes(ctx, EdgeSelection{TargetNodeIDs: []string{"node"}}, []Relationship{RelationshipReferences}, .5)
			return err
		},
		func() error { _, err := snapshot.PayloadCounts(ctx); return err },
	}
	for index, query := range queries {
		if err := query(); err == nil {
			t.Errorf("closed snapshot query %d succeeded", index)
		}
	}
}

func TestOpenRejectsInvalidDatabaseScopeAndFutureSchema(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, nil, "project-a"); err == nil {
		t.Fatal("nil database unexpectedly accepted")
	}
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "future.db"), OmitHost: true}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := Open(ctx, db, ""); !errors.Is(err, ErrProjectScope) {
		t.Fatalf("empty project error = %v, want ErrProjectScope", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE codegraph_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO codegraph_schema_migrations (version, applied_at) VALUES (?, ?)`, CurrentSchemaVersion+1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, db, "project-a"); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("future schema error = %v, want ErrSchemaTooNew", err)
	}
	if _, err := OpenRecovering(ctx, nil, "project-a"); err == nil {
		t.Fatal("recovering open accepted nil database")
	}
}

func TestProcessLeaseLivenessRejectsIncompleteIdentity(t *testing.T) {
	if got := processLeaseLiveness(0, "proc:1"); got != leaseUnknown {
		t.Fatalf("zero PID status = %v", got)
	}
	if got := processLeaseLiveness(1, ""); got != leaseUnknown {
		t.Fatalf("empty identity status = %v", got)
	}
}

func TestMigrationRejectsMalformedLedgersAndVersionConflicts(t *testing.T) {
	openRaw := func(t *testing.T) *sql.DB {
		t.Helper()
		databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "migration.db"), OmitHost: true}
		db, err := sql.Open("sqlite", databaseURL.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			t.Fatal(err)
		}
		return db
	}
	t.Run("ledger read", func(t *testing.T) {
		db := openRaw(t)
		if _, err := db.Exec(`CREATE TABLE codegraph_schema_migrations (wrong INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if err := migrate(context.Background(), db); err == nil {
			t.Fatal("migration accepted a malformed ledger")
		}
	})
	t.Run("version one schema conflict", func(t *testing.T) {
		db := openRaw(t)
		if _, err := db.Exec(`CREATE TABLE codegraph_config (wrong INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if err := migrate(context.Background(), db); err == nil {
			t.Fatal("migration accepted a version-one table conflict")
		}
	})
	t.Run("version one ledger write", func(t *testing.T) {
		db := openRaw(t)
		for _, statement := range []string{
			`CREATE TABLE codegraph_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL)`,
			`CREATE TRIGGER reject_migration_record BEFORE INSERT ON codegraph_schema_migrations BEGIN SELECT RAISE(ABORT, 'record rejected'); END`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if err := migrate(context.Background(), db); err == nil {
			t.Fatal("migration ignored a rejected version-one ledger write")
		}
	})
	t.Run("version two schema conflict", func(t *testing.T) {
		db := openRaw(t)
		for _, statement := range []string{
			`CREATE TABLE codegraph_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL)`,
			`INSERT INTO codegraph_schema_migrations VALUES (1, CURRENT_TIMESTAMP)`,
			`CREATE TABLE codegraph_lifecycle (wrong INTEGER)`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if err := migrate(context.Background(), db); err == nil {
			t.Fatal("migration accepted a version-two table conflict")
		}
	})
	t.Run("version two ledger write", func(t *testing.T) {
		db := openRaw(t)
		for _, statement := range []string{
			`CREATE TABLE codegraph_schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL)`,
			`INSERT INTO codegraph_schema_migrations VALUES (1, CURRENT_TIMESTAMP)`,
			`CREATE TRIGGER reject_migration_record BEFORE INSERT ON codegraph_schema_migrations BEGIN SELECT RAISE(ABORT, 'record rejected'); END`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		if err := migrate(context.Background(), db); err == nil {
			t.Fatal("migration ignored a rejected version-two ledger write")
		}
	})
}

func TestBuildLeaseAndCandidateFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	graphStore, _ := openCoverageStore(t)
	if err := graphStore.BeginBuild(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.BeginBuild(ctx, "second"); err == nil {
		t.Fatal("second build acquired a live lease")
	}
	if err := graphStore.EndBuild(ctx, "wrong"); err == nil {
		t.Fatal("EndBuild accepted the wrong lease token")
	}
	if err := graphStore.EndBuild(ctx, "first"); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"codegraph_nodes", "codegraph_diagnostics"} {
		t.Run(table, func(t *testing.T) {
			graphStore, db := openCoverageStore(t)
			createCoverageCandidate(t, graphStore, "candidate")
			if _, err := db.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			if err := graphStore.FailCandidate(ctx, "candidate", "failed", "failed"); err == nil {
				t.Fatalf("FailCandidate ignored missing %s", table)
			}
		})
	}
}

func TestDisableAndFinishDisableCoverDrainAndCorruptSchemaBoundaries(t *testing.T) {
	ctx := context.Background()
	t.Run("live build cancellation", func(t *testing.T) {
		graphStore, _ := openCoverageStore(t)
		if err := graphStore.BeginBuild(ctx, "build"); err != nil {
			t.Fatal(err)
		}
		cancelCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		if err := graphStore.Disable(cancelCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Disable draining live build error = %v", err)
		}
	})

	t.Run("end abandoned disabling build", func(t *testing.T) {
		graphStore, db := openCoverageStore(t)
		createCoverageCandidate(t, graphStore, "active")
		if err := graphStore.PublishCandidate(ctx, "active", func(context.Context, *Snapshot) error { return nil }); err != nil {
			t.Fatalf("publish active revision: %v", err)
		}
		if err := graphStore.BeginBuild(ctx, "build"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE codegraph_lifecycle SET state='disabling' WHERE project_id='project-a'`); err != nil {
			t.Fatal(err)
		}
		previousProbe := processLeaseProbe
		processLeaseProbe = func(int, string) processLeaseStatus { return leaseDead }
		defer func() { processLeaseProbe = previousProbe }()
		if err := graphStore.EndBuild(ctx, "build"); err != nil {
			t.Fatalf("EndBuild abandoned disable: %v", err)
		}
		project, err := graphStore.ProjectConfig(ctx)
		if err != nil {
			t.Fatalf("ProjectConfig after abandoned disable: %v", err)
		}
		if project.Enabled || project.CanonicalRemote != "" || project.ActiveCheckout != "" || project.LastSuccessfulBuild != nil {
			t.Fatalf("ProjectConfig after abandoned disable = %+v, want disabled default", project)
		}
		if _, err := graphStore.ActiveRevision(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ActiveRevision after abandoned disable error = %v, want ErrNotFound", err)
		}
		if _, err := graphStore.Revision(ctx, "active"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Revision after abandoned disable error = %v, want ErrNotFound", err)
		}
		for _, table := range []string{"codegraph_lifecycle", "codegraph_config", "codegraph_revisions"} {
			var rows int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE project_id = ?`, "project-a").Scan(&rows); err != nil {
				t.Fatalf("count %s after abandoned disable: %v", table, err)
			}
			if rows != 0 {
				t.Fatalf("%s rows after abandoned disable = %d, want 0", table, rows)
			}
		}
	})

	t.Run("closed database", func(t *testing.T) {
		graphStore, db := openCoverageStore(t)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := graphStore.finishDisableLocked(ctx, true); err == nil {
			t.Fatal("finishDisableLocked succeeded on a closed database")
		}
	})

	for _, test := range []struct {
		name  string
		table string
	}{
		{name: "payload table", table: "codegraph_nodes"},
		{name: "lifecycle marker", table: "codegraph_lifecycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			graphStore, db := openCoverageStore(t)
			if _, err := db.Exec(`DROP TABLE ` + test.table); err != nil {
				t.Fatal(err)
			}
			if err := graphStore.finishDisableLocked(ctx, true); err == nil {
				t.Fatalf("finishDisableLocked ignored missing %s", test.table)
			}
		})
	}
}

func TestCodeGraphOperationsFailClosedWhenDurableTablesAreMissing(t *testing.T) {
	ctx := context.Background()

	t.Run("lifecycle", func(t *testing.T) {
		graphStore, db := openCoverageStore(t)
		createCoverageCandidate(t, graphStore, "candidate")
		if _, err := db.ExecContext(ctx, `DROP TABLE codegraph_lifecycle`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error { return graphStore.PutProjectConfig(ctx, config.DefaultProject("project-a")) },
			func() error { return graphStore.BeginBuild(ctx, "build") },
			func() error { return graphStore.EndBuild(ctx, "build") },
			func() error {
				return graphStore.PublishCandidate(ctx, "candidate", func(context.Context, *Snapshot) error { return nil })
			},
			func() error { return graphStore.Disable(ctx) },
		}
		assertCodeGraphCallsFail(t, calls)
	})

	t.Run("configuration", func(t *testing.T) {
		graphStore, db := openCoverageStore(t)
		if _, err := db.ExecContext(ctx, `DROP TABLE codegraph_config`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error { _, err := graphStore.ProjectConfig(ctx); return err },
			func() error { return graphStore.PutProjectConfig(ctx, config.DefaultProject("project-a")) },
			func() error {
				return graphStore.CreateCandidate(ctx, Revision{ProjectID: "project-a", ID: "candidate", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()})
			},
		}
		assertCodeGraphCallsFail(t, calls)
	})

	t.Run("revisions", func(t *testing.T) {
		graphStore, db := openCoverageStore(t)
		createCoverageCandidate(t, graphStore, "candidate")
		if _, err := db.ExecContext(ctx, `DROP TABLE codegraph_revisions`); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error {
				return graphStore.CreateCandidate(ctx, Revision{ProjectID: "project-a", ID: "other", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()})
			},
			func() error { _, err := graphStore.Revision(ctx, "candidate"); return err },
			func() error { _, err := graphStore.ActiveRevision(ctx); return err },
			func() error { return graphStore.ReadRevision(ctx, "candidate", func(*Snapshot) error { return nil }) },
			func() error {
				return graphStore.PublishCandidate(ctx, "candidate", func(context.Context, *Snapshot) error { return nil })
			},
			func() error { return graphStore.FailCandidate(ctx, "candidate", "failed", "failed") },
		}
		assertCodeGraphCallsFail(t, calls)
	})

	t.Run("payload", func(t *testing.T) {
		graphStore, db := openCoverageStore(t)
		createCoverageCandidate(t, graphStore, "candidate")
		for _, table := range []string{"codegraph_nodes", "codegraph_files", "codegraph_symbols", "codegraph_edges", "codegraph_diagnostics"} {
			if _, err := db.ExecContext(ctx, `DROP TABLE `+table); err != nil {
				t.Fatal(err)
			}
		}
		calls := []func() error{
			func() error {
				return graphStore.PutNode(ctx, Node{ProjectID: "project-a", RevisionID: "candidate", ID: "node", Kind: NodeFile})
			},
			func() error {
				return graphStore.PutFile(ctx, File{ProjectID: "project-a", RevisionID: "candidate", ID: "file"})
			},
			func() error {
				return graphStore.PutSymbol(ctx, Symbol{ProjectID: "project-a", RevisionID: "candidate", ID: "symbol"})
			},
			func() error {
				return graphStore.PutEdge(ctx, Edge{ProjectID: "project-a", RevisionID: "candidate", ID: "edge"})
			},
			func() error {
				return graphStore.PutDiagnostic(ctx, Diagnostic{ProjectID: "project-a", RevisionID: "candidate", ID: "diagnostic", CreatedAt: time.Now().UTC()})
			},
			func() error { _, err := graphStore.Diagnostics(ctx, "candidate"); return err },
			func() error { _, err := graphStore.LatestDiagnostics(ctx); return err },
		}
		assertCodeGraphCallsFail(t, calls)
	})
}

func TestOpenRecoveringPropagatesCorruptRecoverySchema(t *testing.T) {
	ctx := context.Background()
	graphStore, db := openCoverageStore(t)
	_ = graphStore
	if _, err := db.ExecContext(ctx, `DROP TABLE codegraph_lifecycle`); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRecovering(ctx, db, "project-a"); err == nil {
		t.Fatal("OpenRecovering succeeded with missing lifecycle table")
	}
}

func assertCodeGraphCallsFail(t *testing.T, calls []func() error) {
	t.Helper()
	for index, call := range calls {
		if err := call(); err == nil {
			t.Errorf("corrupt CodeGraph call %d unexpectedly succeeded", index)
		}
	}
}

func openCoverageStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	databaseURL := &url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "gateway.db"), OmitHost: true}
	query := databaseURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	graphStore, err := Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	return graphStore, db
}

func createCoverageCandidate(t *testing.T, graphStore *Store, revisionID string) {
	t.Helper()
	if err := graphStore.CreateCandidate(context.Background(), Revision{ProjectID: "project-a", ID: revisionID, IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func assertCoverageRevisionState(t *testing.T, graphStore *Store, revisionID string, want RevisionState) {
	t.Helper()
	revision, err := graphStore.Revision(context.Background(), revisionID)
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != want {
		t.Fatalf("revision %q state = %q, want %q", revisionID, revision.State, want)
	}
}
