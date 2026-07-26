package store_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/codegraph/store"
	_ "modernc.org/sqlite"
)

func TestDisableRejectsNewReadsWhileExistingSnapshotCompletes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	readerDB := openLifecycleTestDBAt(t, path)
	disablerDB := openLifecycleTestDBAt(t, path)
	readerStore, err := store.Open(ctx, readerDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	disablerStore, err := store.Open(ctx, disablerDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := readerStore.PutProjectConfig(ctx, config.Project{ProjectID: "project-a", Enabled: true, CanonicalRemote: "remote", ActiveCheckout: "/checkout", ProjectSourceByteCeiling: 1}); err != nil {
		t.Fatal(err)
	}
	createLifecycleCandidate(t, readerStore, "active")
	if err := readerStore.PublishCandidate(ctx, "active", func(context.Context, *store.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- readerStore.ReadActive(ctx, func(snapshot *store.Snapshot) error {
			close(entered)
			<-release
			_, err := snapshot.Nodes(ctx)
			return err
		})
	}()
	<-entered
	disableDone := make(chan error, 1)
	go func() { disableDone <- disablerStore.Disable(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := readerStore.ReadActive(ctx, func(*store.Snapshot) error { return nil })
		if errors.Is(err, store.ErrDisabling) || errors.Is(err, store.ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("new read remained admitted during disable: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-readDone; err != nil {
		t.Fatalf("in-flight read error = %v", err)
	}
	if err := <-disableDone; err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
}

func TestDisableDrainsBuildLeaseAndPreventsPostCleanupWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	buildingDB := openLifecycleTestDBAt(t, path)
	disablingDB := openLifecycleTestDBAt(t, path)
	building, err := store.Open(ctx, buildingDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	disabling, err := store.Open(ctx, disablingDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := building.BeginBuild(ctx, "build-token"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- disabling.Disable(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := building.BeginBuild(ctx, "new-build-token")
		if errors.Is(err, store.ErrDisabling) {
			break
		}
		if err == nil {
			_ = building.EndBuild(ctx, "new-build-token")
			t.Fatal("new build lease succeeded after disable began")
		}
		if !errors.Is(err, store.ErrBuildInProgress) {
			t.Fatalf("new build lease error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("new build did not observe disablement: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := building.EndBuild(ctx, "build-token"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := buildingDB.QueryRow(`SELECT COUNT(*) FROM codegraph_revisions WHERE project_id = ?`, "project-a").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("revision rows after disable = %d, want 0", rows)
	}
}

func TestOpenRecoversInterruptedDisable(t *testing.T) {
	ctx := context.Background()
	db := openLifecycleTestDB(t)
	graphStore, err := store.Open(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	createLifecycleCandidate(t, graphStore, "candidate")
	if _, err := db.Exec(`INSERT INTO codegraph_lifecycle (project_id,state,build_token,owner_pid,owner_start) VALUES (?, 'disabling', NULL, 99999999, 'stale')`, "project-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenRecovering(ctx, db, "project-a"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"codegraph_config", "codegraph_revisions", "codegraph_nodes", "codegraph_lifecycle"} {
		var rows int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id = ?`, "project-a").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Errorf("%s rows after recovery = %d, want 0", table, rows)
		}
	}
}

func TestOrdinaryOpenDoesNotRecoverLiveCandidate(t *testing.T) {
	ctx := context.Background()
	db := openLifecycleTestDB(t)
	building, err := store.Open(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	createLifecycleCandidate(t, building, "live")
	if _, err := store.Open(ctx, db, "project-a"); err != nil {
		t.Fatal(err)
	}
	revision, err := building.Revision(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != store.RevisionCandidate {
		t.Fatalf("live revision state = %q, want candidate", revision.State)
	}
}

func TestRecoveringOpenDoesNotClearLiveBuildOwnedByThisProcess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	buildingDB := openLifecycleTestDBAt(t, path)
	recoveringDB := openLifecycleTestDBAt(t, path)
	building, err := store.Open(ctx, buildingDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := building.BeginBuild(ctx, "live-build"); err != nil {
		t.Fatal(err)
	}
	createLifecycleCandidate(t, building, "live-build")
	createLifecycleCandidate(t, building, "unrelated-stale")
	if _, err := store.OpenRecovering(ctx, recoveringDB, "project-a"); err != nil {
		t.Fatal(err)
	}
	revision, err := building.Revision(ctx, "live-build")
	if err != nil || revision.State != store.RevisionCandidate {
		t.Fatalf("live candidate after recovering open = %+v, %v", revision, err)
	}
	stale, err := building.Revision(ctx, "unrelated-stale")
	if err != nil || stale.State != store.RevisionFailed {
		t.Fatalf("unrelated stale candidate after recovering open = %+v, %v", stale, err)
	}
	if err := building.EndBuild(ctx, "live-build"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveringOpenDoesNotClearLiveDisable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gateway.db")
	buildingDB := openLifecycleTestDBAt(t, path)
	disablingDB := openLifecycleTestDBAt(t, path)
	recoveringDB := openLifecycleTestDBAt(t, path)
	building, err := store.Open(ctx, buildingDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	disabling, err := store.Open(ctx, disablingDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := building.BeginBuild(ctx, "draining-build"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- disabling.Disable(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := building.BeginBuild(ctx, "probe")
		if errors.Is(err, store.ErrDisabling) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disable marker not visible: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := store.OpenRecovering(ctx, recoveringDB, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := building.BeginBuild(ctx, "probe-after-recovery"); !errors.Is(err, store.ErrDisabling) {
		t.Fatalf("recovering open cleared live disable: %v", err)
	}
	if err := building.EndBuild(ctx, "draining-build"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func openLifecycleTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openLifecycleTestDBAt(t, filepath.Join(t.TempDir(), "gateway.db"))
}

func openLifecycleTestDBAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	databaseURL := &url.URL{Scheme: "file", Path: path, OmitHost: true}
	query := databaseURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "journal_mode(WAL)")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRecoveringOpenArbitratesActualBuildAndDisableProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	releaseBuild := filepath.Join(t.TempDir(), "release-build")
	buildReady := filepath.Join(t.TempDir(), "build-ready")
	disableReady := filepath.Join(t.TempDir(), "disable-ready")
	builder := startLifecycleHelper(t, path, "build", buildReady, releaseBuild)
	waitForLifecycleFile(t, buildReady)
	disabler := startLifecycleHelper(t, path, "disable", disableReady, "")
	waitForLifecycleFile(t, disableReady)

	db := openLifecycleTestDBAt(t, path)
	if _, err := store.OpenRecovering(context.Background(), db, "project-a"); err != nil {
		t.Fatal(err)
	}
	if err := disabler.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = disabler.Wait()
	if _, err := store.OpenRecovering(context.Background(), db, "project-a"); err != nil {
		t.Fatal(err)
	}
	probe, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.BeginBuild(context.Background(), "probe"); !errors.Is(err, store.ErrDisabling) {
		t.Fatalf("dead disabler caused live builder marker recovery: %v", err)
	}
	if err := os.WriteFile(releaseBuild, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := builder.Wait(); err != nil {
		t.Fatalf("builder helper: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var rows int
		err := db.QueryRow(`SELECT COUNT(*) FROM codegraph_lifecycle WHERE project_id = ?`, "project-a").Scan(&rows)
		if err == nil && rows == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("abandoned disable not completed after builder drain: rows=%d err=%v", rows, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBeginBuildReclaimsCrashedBuilderWithoutGatewayRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	ready := filepath.Join(t.TempDir(), "build-ready")
	release := filepath.Join(t.TempDir(), "never-release")
	builder := startLifecycleHelper(t, path, "build", ready, release)
	waitForLifecycleFile(t, ready)
	db := openLifecycleTestDBAt(t, path)
	probe, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.BeginBuild(context.Background(), "retry-build"); !errors.Is(err, store.ErrBuildInProgress) {
		t.Fatalf("retry while builder is live = %v, want ErrBuildInProgress", err)
	}
	if err := builder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = builder.Wait()
	if err := probe.BeginBuild(context.Background(), "retry-build"); err != nil {
		t.Fatalf("retry after crashed builder = %v", err)
	}
	t.Cleanup(func() { _ = probe.EndBuild(context.Background(), "retry-build") })
	stale, err := probe.Revision(context.Background(), "process-build")
	if err != nil || stale.State != store.RevisionFailed {
		t.Fatalf("crashed builder candidate = %+v, %v; want failed", stale, err)
	}
	if err := probe.EndBuild(context.Background(), "retry-build"); err != nil {
		t.Fatal(err)
	}
}

func TestDisableCompletesAfterBuilderProcessCrashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	ready := filepath.Join(t.TempDir(), "build-ready")
	release := filepath.Join(t.TempDir(), "never-release")
	builder := startLifecycleHelper(t, path, "build", ready, release)
	waitForLifecycleFile(t, ready)

	db := openLifecycleTestDBAt(t, path)
	disabler, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- disabler.Disable(context.Background()) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var state string
		err := db.QueryRow(`SELECT state FROM codegraph_lifecycle WHERE project_id = ?`, "project-a").Scan(&state)
		if err == nil && state == "disabling" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disable marker not visible: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := builder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = builder.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("disable did not finish after builder crash")
	}
}

func TestCodeGraphLifecycleSubprocessHelper(t *testing.T) {
	mode := os.Getenv("WORMHOLE_CODEGRAPH_HELPER")
	if mode == "" {
		return
	}
	path := os.Getenv("WORMHOLE_CODEGRAPH_DB")
	ready := os.Getenv("WORMHOLE_CODEGRAPH_READY")
	release := os.Getenv("WORMHOLE_CODEGRAPH_RELEASE")
	db := openLifecycleTestDBAt(t, path)
	graphStore, err := store.Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "build":
		if err := graphStore.BeginBuild(context.Background(), "process-build"); err != nil {
			t.Fatal(err)
		}
		createLifecycleCandidate(t, graphStore, "process-build")
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForLifecycleFile(t, release)
		if err := graphStore.EndBuild(context.Background(), "process-build"); err != nil {
			t.Fatal(err)
		}
	case "disable":
		done := make(chan error, 1)
		go func() { done <- graphStore.Disable(context.Background()) }()
		deadline := time.Now().Add(2 * time.Second)
		for {
			var state string
			err := db.QueryRow(`SELECT state FROM codegraph_lifecycle WHERE project_id = ?`, "project-a").Scan(&state)
			if err == nil && state == "disabling" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("disable helper marker: %v", err)
			}
			time.Sleep(time.Millisecond)
		}
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func startLifecycleHelper(t *testing.T, path, mode, ready, release string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCodeGraphLifecycleSubprocessHelper$")
	command.Env = append(os.Environ(),
		"WORMHOLE_CODEGRAPH_HELPER="+mode,
		"WORMHOLE_CODEGRAPH_DB="+path,
		"WORMHOLE_CODEGRAPH_READY="+ready,
		"WORMHOLE_CODEGRAPH_RELEASE="+release,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	return command
}

func waitForLifecycleFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for helper marker %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func createLifecycleCandidate(t *testing.T, graphStore *store.Store, revisionID string) {
	t.Helper()
	if err := graphStore.CreateCandidate(context.Background(), store.Revision{ProjectID: "project-a", ID: revisionID, IndexedCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.PutNode(context.Background(), store.Node{ProjectID: "project-a", RevisionID: revisionID, ID: "repository", Kind: store.NodeRepository, Name: "repository"}); err != nil {
		t.Fatal(err)
	}
}
