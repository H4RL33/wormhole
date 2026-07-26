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

	cgconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	cggo "github.com/H4RL33/wormhole/internal/runtime/codegraph/golang"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

func TestFailBuildUsesDetachedBoundedCleanupContext(t *testing.T) {
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
	if err := graphStore.CreateCandidate(context.Background(), store.Revision{
		ProjectID: "project-a", ID: "revision-canceled", IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	original := errors.New("analysis interrupted")
	err = New(graphStore).failBuild(canceled, BuildRequest{ProjectID: "project-a", RevisionID: "revision-canceled"}, []cggo.Diagnostic{{
		ID: "diagnostic", Severity: cggo.DiagnosticError, Code: "go_package_error", Message: "bounded diagnostic",
	}}, "analysis_failed", original)
	if !errors.Is(err, original) {
		t.Fatalf("failBuild() error = %v, want original error", err)
	}
	revision, err := graphStore.Revision(context.Background(), "revision-canceled")
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != store.RevisionFailed {
		t.Fatalf("revision state = %q, want failed", revision.State)
	}
	diagnostics, err := graphStore.Diagnostics(context.Background(), "revision-canceled")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, want caller and system failure", diagnostics)
	}
}

func TestPublishBuildFailsCanceledCandidateWithDetachedCleanup(t *testing.T) {
	graphStore := newInternalBuildStore(t)
	approved := internalApprovedConfig(t, graphStore)
	seedInternalRepositoryCandidate(t, graphStore, "revision-publish-canceled")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	err := New(graphStore).publishBuild(canceled, BuildRequest{
		ProjectID: "project-a", RevisionID: "revision-publish-canceled",
	}, approved, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publishBuild() error = %v, want context.Canceled", err)
	}
	revision, err := graphStore.Revision(context.Background(), "revision-publish-canceled")
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != store.RevisionFailed {
		t.Fatalf("revision state = %q, want failed", revision.State)
	}
}

func TestPublishBuildValidatesApprovedConfigInsidePublication(t *testing.T) {
	graphStore := newInternalBuildStore(t)
	approved := internalApprovedConfig(t, graphStore)
	seedInternalRepositoryCandidate(t, graphStore, "revision-config-changed")
	changed := approved
	changed.Enabled = false
	if err := graphStore.PutProjectConfig(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	err := New(graphStore).publishBuild(context.Background(), BuildRequest{
		ProjectID: "project-a", RevisionID: "revision-config-changed",
	}, approved, nil)
	if !errors.Is(err, ErrApprovedCheckoutMismatch) {
		t.Fatalf("publishBuild() error = %v, want ErrApprovedCheckoutMismatch", err)
	}
	revision, err := graphStore.Revision(context.Background(), "revision-config-changed")
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != store.RevisionFailed {
		t.Fatalf("revision state = %q, want failed", revision.State)
	}
	if _, err := graphStore.ActiveRevision(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ActiveRevision() error = %v, want ErrNotFound", err)
	}
}

func newInternalBuildStore(t *testing.T) *store.Store {
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
	return graphStore
}

func internalApprovedConfig(t *testing.T, graphStore *store.Store) cgconfig.Project {
	t.Helper()
	approved := cgconfig.Project{
		ProjectID: "project-a", Enabled: true, CanonicalRemote: "https://example.com/approved.git",
		ActiveCheckout: "/approved", ProjectSourceByteCeiling: cgconfig.DefaultProjectSourceByteCeiling,
	}
	if err := graphStore.PutProjectConfig(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	return approved
}

func seedInternalRepositoryCandidate(t *testing.T, graphStore *store.Store, revisionID string) {
	t.Helper()
	if err := graphStore.CreateCandidate(context.Background(), store.Revision{
		ProjectID: "project-a", ID: revisionID, IndexedCommit: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutNode(context.Background(), store.Node{
		ProjectID: "project-a", RevisionID: revisionID, ID: "repository", Kind: store.NodeRepository, Name: "repository",
	}); err != nil {
		t.Fatal(err)
	}
}
