package localapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codegraphconfig "github.com/H4RL33/wormhole/internal/runtime/codegraph/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func TestPrivateCodeGraphLifecycleSerializesWithPublicRebuild(t *testing.T) {
	ctx := context.Background()
	_, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/approved.git"})
	runtime, err := NewCodeGraphRuntime(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := runtime.Lifecycle
	checkout := lifecycleGitRepository(t, "https://example.invalid/approved.git", "package approved\nfunc Ready() {}\n")
	if _, err := lifecycle.Enable(ctx, checkout); err != nil {
		t.Fatal(err)
	}
	srv := &Server{projectID: "project-a"}
	srv.SetCodeGraphRuntime("project-a", runtime)

	privateEntered := make(chan struct{})
	releasePrivate := make(chan struct{})
	lifecycle.beforeBuild = func() {
		close(privateEntered)
		<-releasePrivate
	}
	privateDone := make(chan error, 1)
	go func() {
		_, err := lifecycle.Execute(ctx, CodeGraphLifecycleRequest{
			Operation: CodeGraphRebuild, ProjectID: "project-a",
			CredentialProfile: "profile", AgentID: "agent", PassportID: "passport",
		})
		privateDone <- err
	}()
	select {
	case <-privateEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("private lifecycle did not reach build barrier")
	}

	_, publicErr := srv.handleCodeGraphRebuild(ctx, json.RawMessage(`{"project_id":"project-a"}`))
	close(releasePrivate)
	if err := <-privateDone; err != nil {
		t.Fatalf("private rebuild: %v", err)
	}
	if publicErr == nil || !strings.Contains(publicErr.Error(), "already in progress") {
		t.Errorf("public rebuild during private lifecycle = %v, want already-in-progress rejection", publicErr)
	}

	lazyStore, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lazyStore.Close() })
	lazyServer := &Server{store: lazyStore}
	start := make(chan struct{})
	runtimes := make(chan CodeGraphRuntime, 2)
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			created, createErr := lazyServer.ensureCodeGraphRuntime(ctx, "project-lazy")
			if createErr != nil {
				errorsCh <- createErr
				return
			}
			runtimes <- created
		}()
	}
	close(start)
	var first, second CodeGraphRuntime
	for i := 0; i < 2; i++ {
		select {
		case err := <-errorsCh:
			t.Fatal(err)
		case got := <-runtimes:
			if i == 0 {
				first = got
			} else {
				second = got
			}
		}
	}
	if first.lifecycleMu != second.lifecycleMu || first.Lifecycle != second.Lifecycle {
		t.Errorf("concurrent first access published distinct project executors: %p/%p != %p/%p", first.lifecycleMu, first.Lifecycle, second.lifecycleMu, second.Lifecycle)
	}
}

func TestCodeGraphProjectExecutorsAreIndependent(t *testing.T) {
	ctx := context.Background()
	_, db := newLifecycleTestState(t, "project-a", []string{"https://example.invalid/a.git"})
	runtimeA, err := NewCodeGraphRuntime(ctx, db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	privateLifecycle := runtimeA.Lifecycle
	checkoutA := lifecycleGitRepository(t, "https://example.invalid/a.git", "package a\nfunc A() {}\n")
	if _, err := privateLifecycle.Enable(ctx, checkoutA); err != nil {
		t.Fatal(err)
	}

	checkoutB := lifecycleGitRepository(t, "https://example.invalid/b.git", "package b\nfunc B() {}\n")
	runtimeB, err := NewCodeGraphRuntime(ctx, db, "project-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeB.Store.PutProjectConfig(ctx, codegraphconfig.Project{
		ProjectID: "project-b", Enabled: true, CanonicalRemote: "https://example.invalid/b.git",
		ActiveCheckout: checkoutB, ProjectSourceByteCeiling: codegraphconfig.DefaultProjectSourceByteCeiling,
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{projectID: "project-b"}
	srv.SetCodeGraphRuntime("project-b", runtimeB)

	privateEntered := make(chan struct{})
	releasePrivate := make(chan struct{})
	privateLifecycle.beforeBuild = func() {
		close(privateEntered)
		<-releasePrivate
	}
	privateDone := make(chan error, 1)
	go func() {
		_, err := privateLifecycle.Execute(ctx, CodeGraphLifecycleRequest{
			Operation: CodeGraphRebuild, ProjectID: "project-a",
			CredentialProfile: "profile", AgentID: "agent", PassportID: "passport",
		})
		privateDone <- err
	}()
	select {
	case <-privateEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("project A lifecycle did not reach build barrier")
	}

	publicDone := make(chan error, 1)
	go func() {
		_, err := srv.handleCodeGraphRebuild(ctx, json.RawMessage(`{"project_id":"project-b"}`))
		publicDone <- err
	}()
	var publicErr error
	select {
	case publicErr = <-publicDone:
	case <-time.After(5 * time.Second):
		close(releasePrivate)
		<-privateDone
		t.Fatal("project B rebuild waited for project A executor")
	}
	close(releasePrivate)
	if err := <-privateDone; err != nil {
		t.Fatalf("project A private rebuild: %v", err)
	}
	if publicErr != nil {
		t.Fatalf("project B rebuild while project A blocked: %v", publicErr)
	}
}
