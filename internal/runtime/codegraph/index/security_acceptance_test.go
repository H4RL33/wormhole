package index_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	cggo "github.com/H4RL33/wormhole/internal/runtime/codegraph/golang"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/index"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/query"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/source"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

func TestGateBCrossProjectSameDatabaseQueryIsolation(t *testing.T) {
	db, _ := gateBDatabase(t)
	checkoutA := gateBCrossProjectCheckout(t, "project-a")
	checkoutB := gateBCrossProjectCheckout(t, "project-b")
	storeA := gateBBuild(t, db, "project-a", checkoutA, "revision-a")
	storeB := gateBBuild(t, db, "project-b", checkoutB, "revision-b")

	resultA, err := query.New(storeA, codegraphconfig.DefaultProjectSourceByteCeiling).Execute(context.Background(), query.Request{
		EntrySymbols: []string{"TargetB"}, MaxNodes: 20, RequestedSourceBytes: 1024, SourceAuthorized: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resultA.Matches) != 0 || len(resultA.Nodes) != 0 || len(resultA.Edges) != 0 || len(resultA.Sources) != 0 {
		t.Fatalf("project A query leaked project B graph: %#v", resultA)
	}
	resultB, err := query.New(storeB, codegraphconfig.DefaultProjectSourceByteCeiling).Execute(context.Background(), query.Request{
		EntrySymbols: []string{"TargetB"}, MaxNodes: 20,
	})
	if err != nil || len(resultB.Matches) == 0 || !strings.HasSuffix(resultB.Matches[0].QualifiedName, ".TargetB") {
		t.Fatalf("project B own query result=%#v error=%v", resultB, err)
	}
}

func TestGateBCorruptActiveSourceRowFailsClosed(t *testing.T) {
	db, _ := gateBDatabase(t)
	checkout := gateBCrossProjectCheckout(t, "project-a")
	graphStore := gateBBuild(t, db, "project-a", checkout, "revision-a")
	result, err := db.Exec(`
		UPDATE codegraph_symbols SET start_byte = -1
		WHERE project_id = ? AND revision_id = ? AND qualified_name LIKE '%.TargetA'
	`, "project-a", "revision-a")
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("corrupt active source row rows=%d error=%v", rows, err)
	}
	_, err = query.New(graphStore, codegraphconfig.DefaultProjectSourceByteCeiling).Execute(context.Background(), query.Request{
		EntrySymbols: []string{"TargetA"}, MaxNodes: 20,
		RequestedSourceBytes: 1024, SourceAuthorized: true,
	})
	if !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatalf("query corrupt active row error = %v, want source.ErrInvalidRequest", err)
	}
}

func TestGateBDatabaseAndWALNeverPersistSourceOrReturnedContext(t *testing.T) {
	const sentinel = "TASK12_SOURCE_BODY_MUST_NEVER_ENTER_SQLITE_7f49cbb6"
	db, databasePath := gateBDatabase(t)
	checkout := newInventoryRepository(t)
	writeInventoryFile(t, checkout, "go.mod", "module example.invalid/persistence\n\ngo 1.26\n")
	writeInventoryFile(t, checkout, "persistence.go", "package persistence\nfunc PersistedProbe() string { return \""+sentinel+"\" }\n")
	runGit(t, checkout, "add", "go.mod", "persistence.go")
	runGit(t, checkout, "commit", "-m", "persistence fixture")
	graphStore := gateBBuild(t, db, "project-persistence", checkout, "revision-persistence")

	result, err := query.New(graphStore, codegraphconfig.DefaultProjectSourceByteCeiling).Execute(context.Background(), query.Request{
		EntrySymbols: []string{"PersistedProbe"}, MaxNodes: 20,
		RequestedSourceBytes: codegraphconfig.DefaultProjectSourceByteCeiling, SourceAuthorized: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	returned := false
	for _, outcome := range result.Sources {
		returned = returned || strings.Contains(outcome.Source, sentinel)
	}
	if !returned {
		t.Fatalf("authorized query did not return sentinel source: %#v", result.Sources)
	}
	gateBAssertLogicalValuesExclude(t, db, sentinel)
	for index, path := range []string{databasePath, databasePath + "-wal"} {
		raw, err := os.ReadFile(path)
		if index == 1 && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), sentinel) {
			t.Fatalf("source sentinel persisted in raw SQLite file %s", filepath.Base(path))
		}
	}
}

func TestGateBMalformedRebuildKeepsOldRevisionQueryable(t *testing.T) {
	db, _ := gateBDatabase(t)
	checkout := newInventoryRepository(t)
	writeInventoryFile(t, checkout, "go.mod", "module example.invalid/cow\n\ngo 1.26\n")
	writeInventoryFile(t, checkout, "cow.go", "package cow\nfunc Stable() {}\n")
	runGit(t, checkout, "add", "go.mod", "cow.go")
	runGit(t, checkout, "commit", "-m", "stable")
	graphStore := gateBBuild(t, db, "project-cow", checkout, "revision-stable")
	writeInventoryFile(t, checkout, "cow.go", "package cow\nfunc Broken( {\n")
	if err := index.New(graphStore).Build(context.Background(), index.BuildRequest{ProjectID: "project-cow", RevisionID: "revision-malformed"}); !errors.Is(err, cggo.ErrPackageLoad) {
		t.Fatalf("malformed rebuild error = %v, want cggo.ErrPackageLoad", err)
	}
	active, err := graphStore.ActiveRevision(context.Background())
	if err != nil || active.ID != "revision-stable" {
		t.Fatalf("active revision after malformed rebuild=%#v error=%v", active, err)
	}
	result, err := query.New(graphStore, codegraphconfig.DefaultProjectSourceByteCeiling).Execute(context.Background(), query.Request{
		EntrySymbols: []string{"Stable"}, MaxNodes: 20,
	})
	if err != nil || result.RevisionID != "revision-stable" || len(result.Matches) == 0 {
		t.Fatalf("old active query after malformed rebuild=%#v error=%v", result, err)
	}
}

func gateBDatabase(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate-b.db")
	databaseURL := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	return db, path
}

func gateBBuild(t *testing.T, db *sql.DB, projectID, checkout, revisionID string) *store.Store {
	t.Helper()
	graphStore, err := store.Open(context.Background(), db, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutProjectConfig(context.Background(), codegraphconfig.Project{
		ProjectID: projectID, Enabled: true, CanonicalRemote: testCanonicalRemote,
		ActiveCheckout: checkout, ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling,
	}); err != nil {
		t.Fatal(err)
	}
	if err := index.New(graphStore).Build(context.Background(), index.BuildRequest{ProjectID: projectID, RevisionID: revisionID}); err != nil {
		t.Fatalf("build %s: %v", projectID, err)
	}
	return graphStore
}

func gateBCrossProjectCheckout(t *testing.T, project string) string {
	t.Helper()
	checkout := newInventoryRepository(t)
	fixtureRoot := filepath.Join("..", "..", "..", "..", "testdata", "codegraph", "cross-project", project)
	for _, name := range []string{"go.mod", strings.ReplaceAll(project, "-", "_") + ".go"} {
		content, err := os.ReadFile(filepath.Join(fixtureRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		writeInventoryFile(t, checkout, name, string(content))
	}
	runGit(t, checkout, "add", ".")
	runGit(t, checkout, "commit", "-m", project)
	return checkout
}

func gateBAssertLogicalValuesExclude(t *testing.T, db *sql.DB, sentinel string) {
	t.Helper()
	for _, table := range []string{
		"codegraph_config", "codegraph_revisions", "codegraph_nodes", "codegraph_files",
		"codegraph_symbols", "codegraph_edges", "codegraph_diagnostics", "codegraph_lifecycle",
	} {
		columns, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for columns.Next() {
			var name string
			if err := columns.Scan(&name); err != nil {
				columns.Close()
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := columns.Close(); err != nil {
			t.Fatal(err)
		}
		for _, column := range names {
			query := fmt.Sprintf(`SELECT CAST("%s" AS TEXT) FROM "%s" WHERE instr(COALESCE(CAST("%s" AS TEXT), ''), ?) > 0 LIMIT 1`, column, table, column)
			var value string
			err := db.QueryRow(query, sentinel).Scan(&value)
			if err == nil {
				t.Fatalf("source sentinel persisted in logical SQLite value %s.%s", table, column)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
		}
	}
}
