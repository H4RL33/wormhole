package projectstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestCheckpointUnavailableReturnsExactZero(t *testing.T) {
	var service *Service

	got, err := service.Checkpoint(context.Background(), CheckpointRequest{})
	if !errors.Is(err, localstore.ErrNotFound) {
		t.Fatalf("Checkpoint error = %v, want localstore.ErrNotFound", err)
	}
	if !reflect.DeepEqual(got, CheckpointResult{}) {
		t.Fatalf("Checkpoint result = %+v, want exact zero", got)
	}
}

func TestCheckpointAcknowledgementMatrixAndActorParity(t *testing.T) {
	human := diffActorEnvelope()
	agent := types.ActorEnvelope{
		ActorKind: types.ActorAgent, AgentID: "33333333-3333-4333-8333-333333333333",
		AccountableHumanID: human.HumanPrincipalID, SessionID: "session", HarnessName: "codex",
		HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5", Assurance: types.AssuranceLocal,
		OccurredAt: human.OccurredAt,
	}
	tests := []struct {
		name           string
		classification types.PublicationClassification
		actor          types.ActorEnvelope
		ack            string
		want           error
	}{
		{name: "public exact human", classification: types.PublicationPublicGit, actor: human, ack: "exact"},
		{name: "public exact agent", classification: types.PublicationPublicGit, actor: agent, ack: "exact"},
		{name: "public missing", classification: types.PublicationPublicGit, actor: human, want: ErrPublicationReviewRequired},
		{name: "public stale", classification: types.PublicationPublicGit, actor: human, ack: "stale", want: ErrPublicationReviewStale},
		{name: "local missing", classification: types.PublicationLocalOnly, actor: human},
		{name: "local exact", classification: types.PublicationLocalOnly, actor: human, ack: "exact"},
		{name: "local stale", classification: types.PublicationLocalOnly, actor: human, ack: "stale", want: ErrPublicationReviewStale},
		{name: "private missing", classification: types.PublicationPrivateGit, actor: human},
		{name: "private exact", classification: types.PublicationPrivateGit, actor: human, ack: "exact"},
		{name: "private stale", classification: types.PublicationPrivateGit, actor: human, ack: "stale", want: ErrPublicationReviewStale},
		{name: "already unclassified", classification: types.PublicationUnclassified, actor: human, want: ErrPublicationUnclassified},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, review := newCheckpointCoordinatorFixture(t, test.classification, test.actor)
			switch test.ack {
			case "exact":
				req.PublicationReviewDigest = &review
			case "stale":
				stale := state.Digest("sha256:" + strings.Repeat("f", 64))
				if stale == review {
					t.Fatal("stale digest fixture equals current review")
				}
				req.PublicationReviewDigest = &stale
			}
			got, err := fixture.service.Checkpoint(context.Background(), req)
			if test.want != nil {
				if !errors.Is(err, test.want) || got != (CheckpointResult{}) {
					t.Fatalf("Checkpoint = (%+v, %v), want zero and %v", got, err, test.want)
				}
				if fixture.publishCalls != 0 || fixture.closeCalls != 0 {
					t.Fatalf("rejected acknowledgement touched artifact: publish=%d close=%d", fixture.publishCalls, fixture.closeCalls)
				}
				return
			}
			if err != nil || got.JournalID == "" || fixture.publishCalls != 1 || fixture.closeCalls != 1 {
				t.Fatalf("Checkpoint = (%+v, %v), artifact publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
			}
		})
	}
}

func TestCheckpointRejectsReviewDigestFromAnotherWorkspace(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationPublicGit, diffActorEnvelope())
	_, _, foreignReview := newCheckpointCoordinatorFixture(t, types.PublicationPublicGit, diffActorEnvelope())
	req.PublicationReviewDigest = &foreignReview

	got, err := fixture.service.Checkpoint(context.Background(), req)
	if !errors.Is(err, ErrPublicationReviewStale) || got != (CheckpointResult{}) ||
		fixture.publishCalls != 0 || fixture.closeCalls != 0 {
		t.Fatalf("foreign-review Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
	}
}

func TestCheckpointMalformedArtifactHandlesFailClosed(t *testing.T) {
	tests := []struct {
		name  string
		build func(checkpointArtifactHandle) checkpointArtifactHandle
		close int
	}{
		{name: "nil publish", build: func(handle checkpointArtifactHandle) checkpointArtifactHandle { handle.publish = nil; return handle }, close: 1},
		{name: "nil close", build: func(handle checkpointArtifactHandle) checkpointArtifactHandle { handle.close = nil; return handle }},
		{name: "incomplete evidence", build: func(handle checkpointArtifactHandle) checkpointArtifactHandle {
			handle.evidence.StagePath = ""
			return handle
		}, close: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			baseFactory := fixture.service.prepareCheckpointArtifact
			fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
				handle, err := baseFactory(ctx, input)
				return test.build(handle), err
			}
			got, err := fixture.service.Checkpoint(context.Background(), req)
			if err == nil || got != (CheckpointResult{}) || fixture.publishCalls != 0 || fixture.closeCalls != test.close {
				t.Fatalf("Checkpoint = (%+v, %v), artifact publish=%d close=%d, want close=%d", got, err, fixture.publishCalls, fixture.closeCalls, test.close)
			}
			if disposition := readCheckpointDisposition(t, fixture.service, req.Scope); len(disposition.Journals) != 0 {
				t.Fatalf("malformed handle persisted journal: %+v", disposition)
			}
		})
	}
}

func TestCheckpointPublisherFailureRetainsPreparedAuthority(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "publisher rollback",
	)
	if _, err := fixture.service.Apply(context.Background(), req.Scope, operation); err != nil {
		t.Fatal(err)
	}
	setServiceWorkspaceState(t, fixture.store, req.Scope, "clean")
	before, err := fixture.service.repo.Workspace(context.Background(), req.Scope)
	if err != nil {
		t.Fatal(err)
	}
	publishErr := errors.New("publisher failed")
	baseFactory := fixture.service.prepareCheckpointArtifact
	fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
		handle, err := baseFactory(ctx, input)
		handle.publish = func(context.Context) (checkpointPublicationDisposition, error) {
			fixture.publishCalls++
			return 0, publishErr
		}
		return handle, err
	}
	got, err := fixture.service.Checkpoint(context.Background(), req)
	if !errors.Is(err, publishErr) || got != (CheckpointResult{}) || fixture.publishCalls != 1 || fixture.closeCalls != 1 {
		t.Fatalf("Checkpoint = (%+v, %v), artifact publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
	}
	disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
	if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" ||
		len(disposition.Operations) != 1 || disposition.Operations[0].State != "active" ||
		disposition.Operations[0].OperationID != operation.ID {
		t.Fatalf("publisher failure disposition = %+v", disposition)
	}
	workspace, err := fixture.service.repo.Workspace(context.Background(), req.Scope)
	if err != nil || workspace.State != "clean" || workspace.Binding != before.Binding ||
		!checkpointSnapshotsEqual(workspace.Snapshot, before.Snapshot) {
		t.Fatalf("publisher failure workspace = (%+v, %v)", workspace, err)
	}
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate != nil {
			return fmt.Errorf("publisher failure retained tentative candidate: %+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRejectsTemporaryUnpublishedPublicationDispositions(t *testing.T) {
	for _, test := range []struct {
		name        string
		disposition checkpointPublicationDisposition
		want        error
	}{
		{name: "preserved concurrent old", disposition: checkpointPublicationPreservedConcurrentOld, want: ErrCheckpointCAS},
		{name: "zero", want: ErrCheckpointRecoveryBlocked},
		{name: "unknown", disposition: checkpointPublicationDisposition(99), want: ErrCheckpointRecoveryBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			baseFactory := fixture.service.prepareCheckpointArtifact
			fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
				handle, err := baseFactory(ctx, input)
				handle.publish = func(context.Context) (checkpointPublicationDisposition, error) {
					fixture.publishCalls++
					return test.disposition, nil
				}
				return handle, err
			}

			got, err := fixture.service.Checkpoint(context.Background(), req)
			if !errors.Is(err, test.want) || got != (CheckpointResult{}) || fixture.publishCalls != 1 || fixture.closeCalls != 1 {
				t.Fatalf("Checkpoint = (%+v, %v), artifact publish=%d close=%d, want zero and %v", got, err, fixture.publishCalls, fixture.closeCalls, test.want)
			}
			disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
			if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" {
				t.Fatalf("temporary disposition changed database outcome: %+v", disposition)
			}
			if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
				candidate, err := tx.Candidate(context.Background())
				if err != nil {
					return err
				}
				if candidate != nil {
					return fmt.Errorf("temporary disposition retained tentative candidate: %+v", candidate)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckpointFinalizationFailureRollsBackAllTentativeDatabaseWrites(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "finalization rollback",
	)
	if _, err := fixture.service.Apply(context.Background(), req.Scope, operation); err != nil {
		t.Fatal(err)
	}
	setServiceWorkspaceState(t, fixture.store, req.Scope, "clean")
	before, err := fixture.service.repo.Workspace(context.Background(), req.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`
		CREATE TRIGGER fail_checkpoint_final_status
		BEFORE UPDATE OF status ON workspace_bindings
		BEGIN SELECT RAISE(ABORT,'injected checkpoint finalization failure'); END
	`); err != nil {
		t.Fatal(err)
	}

	got, err := fixture.service.Checkpoint(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "injected checkpoint finalization failure") ||
		got != (CheckpointResult{}) || fixture.prepareCalls != 1 || fixture.publishCalls != 0 || fixture.closeCalls != 1 {
		t.Fatalf("finalization failure = (%+v, %v), prepare=%d publish=%d close=%d", got, err, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
	}
	disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
	if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" ||
		len(disposition.Operations) != 1 || disposition.Operations[0].State != "active" ||
		disposition.Operations[0].OperationID != operation.ID {
		t.Fatalf("finalization rollback disposition = %+v", disposition)
	}
	after, err := fixture.service.repo.Workspace(context.Background(), req.Scope)
	if err != nil || after.State != "clean" || after.Binding != before.Binding ||
		!checkpointSnapshotsEqual(after.Snapshot, before.Snapshot) {
		t.Fatalf("finalization rollback workspace = (%+v, %v), before=%+v", after, err, before)
	}
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate != nil {
			return fmt.Errorf("finalization failure retained tentative candidate: %+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointPendingPrecedesOwnedRequestValidationAndMutation(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	publishErr := errors.New("retain prepared")
	baseFactory := fixture.service.prepareCheckpointArtifact
	fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
		handle, err := baseFactory(ctx, input)
		handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, publishErr }
		return handle, err
	}
	if _, err := fixture.service.Checkpoint(context.Background(), req); !errors.Is(err, publishErr) {
		t.Fatal(err)
	}
	before := readCheckpointDisposition(t, fixture.service, req.Scope)
	realObserver := fixture.service.publicationTrustObserver()
	observerCalls := 0
	fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
		observerCalls++
		return realObserver(ctx, binding)
	}
	fixture.service.readWorkingTree = func(string) (state.Tree, error) { panic("pending checkpoint read live tree") }
	fixture.service.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
		panic("pending checkpoint allocated artifact")
	}
	req.Root = filepath.Join(req.Root, "wrong")
	req.ExpectedWorkingTreeDigest = "bad"
	req.Actor = types.ActorEnvelope{}
	got, err := fixture.service.Checkpoint(context.Background(), req)
	if !errors.Is(err, ErrCheckpointPendingAcceptance) || got != (CheckpointResult{}) || observerCalls != 1 {
		t.Fatalf("pending Checkpoint = (%+v, %v), outside observer calls=%d", got, err, observerCalls)
	}
	after := readCheckpointDisposition(t, fixture.service, req.Scope)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("pending checkpoint changed disposition\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestCheckpointGateSerializesCancelsSeparatesAndPrunes(t *testing.T) {
	var service Service
	scope := types.WorkspaceScope{
		ProjectID:   "00000000-0000-4000-8000-000000000001",
		WorkspaceID: "00000000-0000-4000-8000-000000000002",
	}
	other := scope
	other.WorkspaceID = "00000000-0000-4000-8000-000000000003"
	firstRelease, err := service.checkpointGates.acquire(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, err := service.checkpointGates.acquire(canceled, scope)
		waiter <- err
	}()
	waitCheckpointGateRefs(t, &service.checkpointGates, scope, 2)
	select {
	case err := <-waiter:
		t.Fatalf("same-scope waiter did not serialize: %v", err)
	default:
	}
	otherRelease, err := service.checkpointGates.acquire(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	otherRelease()
	cancel()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	firstRelease()

	const workers = 12
	start := make(chan struct{})
	var owners int32
	var maximum int32
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			<-start
			release, err := service.checkpointGates.acquire(context.Background(), scope)
			if err != nil {
				t.Errorf("simultaneous gate acquire: %v", err)
				return
			}
			current := atomic.AddInt32(&owners, 1)
			for {
				prior := atomic.LoadInt32(&maximum)
				if current <= prior || atomic.CompareAndSwapInt32(&maximum, prior, current) {
					break
				}
			}
			atomic.AddInt32(&owners, -1)
			release()
		}()
	}
	close(start)
	group.Wait()
	if maximum != 1 {
		t.Fatalf("same-scope maximum concurrent owners = %d, want 1", maximum)
	}
	service.checkpointGates.mu.Lock()
	defer service.checkpointGates.mu.Unlock()
	if len(service.checkpointGates.byScope) != 0 {
		t.Fatalf("checkpoint gate map retained entries: %+v", service.checkpointGates.byScope)
	}
}

func TestCheckpointServiceGateSerializesSameScopeBeforeOutsideObservation(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	realObserver := fixture.service.publicationTrustObserver()
	var observerCalls atomic.Int32
	fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
		observerCalls.Add(1)
		return realObserver(ctx, binding)
	}
	entered := make(chan struct{})
	releaseArtifact := make(chan struct{})
	var prepareCalls, publishCalls, closeCalls atomic.Int32
	const journalID = "30000000-0000-4000-8000-000000000001"
	privateRoot := t.TempDir()
	fixture.service.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
		if prepareCalls.Add(1) == 1 {
			close(entered)
		}
		<-releaseArtifact
		return checkpointArtifactHandle{
			evidence: checkpointArtifactEvidence{
				JournalID: journalID, StagePath: filepath.Join(privateRoot, journalID+".stage"),
				BackupPath: filepath.Join(privateRoot, journalID+".backup"),
			},
			publish: func(context.Context) (checkpointPublicationDisposition, error) {
				publishCalls.Add(1)
				return checkpointPublicationPublished, nil
			},
			close: func() { closeCalls.Add(1) },
		}, nil
	}
	type checkpointOutcome struct {
		result CheckpointResult
		err    error
	}
	firstDone := make(chan checkpointOutcome, 1)
	go func() {
		result, err := fixture.service.Checkpoint(context.Background(), req)
		firstDone <- checkpointOutcome{result: result, err: err}
	}()
	<-entered
	if got := observerCalls.Load(); got != 2 {
		t.Fatalf("first checkpoint observer calls before prepare = %d, want 2", got)
	}
	secondDone := make(chan checkpointOutcome, 1)
	go func() {
		result, err := fixture.service.Checkpoint(context.Background(), req)
		secondDone <- checkpointOutcome{result: result, err: err}
	}()
	waitCheckpointGateRefs(t, &fixture.service.checkpointGates, req.Scope, 2)
	if got := observerCalls.Load(); got != 2 {
		t.Fatalf("queued checkpoint observed outside gate: observer calls=%d", got)
	}
	close(releaseArtifact)
	first := <-firstDone
	second := <-secondDone
	if first.err != nil || first.result.JournalID != journalID {
		t.Fatalf("first serialized Checkpoint = (%+v, %v)", first.result, first.err)
	}
	if !errors.Is(second.err, ErrCheckpointPendingAcceptance) || second.result != (CheckpointResult{}) {
		t.Fatalf("second serialized Checkpoint = (%+v, %v)", second.result, second.err)
	}
	if observerCalls.Load() != 5 || prepareCalls.Load() != 1 || publishCalls.Load() != 1 || closeCalls.Load() != 1 {
		t.Fatalf("serialized calls: observer=%d prepare=%d publish=%d close=%d",
			observerCalls.Load(), prepareCalls.Load(), publishCalls.Load(), closeCalls.Load())
	}
}

func TestCheckpointQueuedRequestOwnsReviewDigestImmediately(t *testing.T) {
	fixture, req, review := newCheckpointCoordinatorFixture(t, types.PublicationPublicGit, diffActorEnvelope())
	req.PublicationReviewDigest = &review
	release, err := fixture.service.checkpointGates.acquire(context.Background(), req.Scope)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result CheckpointResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fixture.service.Checkpoint(context.Background(), req)
		done <- outcome{result: result, err: err}
	}()
	waitCheckpointGateRefs(t, &fixture.service.checkpointGates, req.Scope, 2)
	review = state.Digest("sha256:" + strings.Repeat("f", 64))
	release()
	got := <-done
	if got.err != nil || got.result.JournalID == "" {
		t.Fatalf("queued owned-digest Checkpoint = (%+v, %v)", got.result, got.err)
	}
}

func TestCheckpointPendingStatusPreservesTimestampAndMaterializesExactOperations(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "checkpoint",
	)
	applied, err := fixture.service.Apply(context.Background(), req.Scope, operation)
	if err != nil {
		t.Fatal(err)
	}
	var beforeTimestamp string
	if err := fixture.store.DB().QueryRow(`
		SELECT CAST(updated_at AS TEXT) FROM workspace_bindings WHERE project_id=? AND workspace_id=?
	`, req.Scope.ProjectID, req.Scope.WorkspaceID).Scan(&beforeTimestamp); err != nil {
		t.Fatal(err)
	}
	got, err := fixture.service.Checkpoint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateDigest != applied.CandidateDigest || got.MaterializedThroughGeneration != 1 {
		t.Fatalf("active-operation Checkpoint = %+v, applied = %+v", got, applied)
	}
	var afterTimestamp string
	if err := fixture.store.DB().QueryRow(`
		SELECT CAST(updated_at AS TEXT) FROM workspace_bindings WHERE project_id=? AND workspace_id=?
	`, req.Scope.ProjectID, req.Scope.WorkspaceID).Scan(&afterTimestamp); err != nil {
		t.Fatal(err)
	}
	if afterTimestamp != beforeTimestamp {
		t.Fatalf("pending status timestamp changed from %q to %q", beforeTimestamp, afterTimestamp)
	}
	disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
	if len(disposition.Operations) != 1 || disposition.Operations[0].State != "materialized" ||
		disposition.Operations[0].OperationID != operation.ID {
		t.Fatalf("materialized operation inventory = %+v", disposition.Operations)
	}
}

func TestCheckpointStableOriginMismatchInvalidatesInEachTransaction(t *testing.T) {
	t.Run("first transaction", func(t *testing.T) {
		fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationPublicGit, diffActorEnvelope())
		runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/changed.git")
		got, err := fixture.service.Checkpoint(context.Background(), req)
		if !errors.Is(err, ErrPublicationUnclassified) || got != (CheckpointResult{}) ||
			fixture.publishCalls != 0 || fixture.closeCalls != 0 {
			t.Fatalf("first invalidation Checkpoint = (%+v, %v), artifact publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
		}
		policy, history := readPublicationPolicyState(t, fixture.service, req.Scope)
		if policy.Classification != types.PublicationUnclassified || policy.TransitionKind != "origin_invalidated" || len(history) != 3 {
			t.Fatalf("first invalidation policy=%+v history=%+v", policy, history)
		}
	})
	t.Run("second transaction", func(t *testing.T) {
		fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
		realObserver := fixture.service.publicationTrustObserver()
		calls := 0
		fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
			calls++
			if calls == 3 {
				runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/changed.git")
			}
			return realObserver(ctx, binding)
		}
		got, err := fixture.service.Checkpoint(context.Background(), req)
		if !errors.Is(err, ErrPublicationUnclassified) || got != (CheckpointResult{}) ||
			fixture.publishCalls != 0 || fixture.closeCalls != 1 {
			t.Fatalf("second invalidation Checkpoint = (%+v, %v), calls=%d artifact publish=%d close=%d", got, err, calls, fixture.publishCalls, fixture.closeCalls)
		}
		disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
		if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" {
			t.Fatalf("second invalidation disposition = %+v", disposition)
		}
	})
}

func TestCheckpointFirstMaterialPreflightPrecedesOriginInvalidation(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "invalid boundary",
	)
	if _, err := fixture.service.Apply(context.Background(), req.Scope, operation); err != nil {
		t.Fatal(err)
	}
	candidate := checkpointPlanCandidate(fixture.binding, accepted, &accepted, 1)
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		return tx.UpsertCandidate(context.Background(), *candidate)
	}); err != nil {
		t.Fatal(err)
	}
	beforeDisposition := readCheckpointDisposition(t, fixture.service, req.Scope)
	beforePolicy, beforeHistory := readPublicationPolicyState(t, fixture.service, req.Scope)
	runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/changed.git")

	got, err := fixture.service.Checkpoint(context.Background(), req)
	if err == nil || errors.Is(err, ErrPublicationUnclassified) || got != (CheckpointResult{}) ||
		fixture.prepareCalls != 0 || fixture.publishCalls != 0 || fixture.closeCalls != 0 {
		t.Fatalf("invalid-material Checkpoint = (%+v, %v), prepare=%d publish=%d close=%d", got, err, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
	}
	afterPolicy, afterHistory := readPublicationPolicyState(t, fixture.service, req.Scope)
	if !reflect.DeepEqual(afterPolicy, beforePolicy) || !reflect.DeepEqual(afterHistory, beforeHistory) {
		t.Fatalf("invalid material committed invalidation\nbefore=(%+v, %+v)\nafter=(%+v, %+v)", beforePolicy, beforeHistory, afterPolicy, afterHistory)
	}
	if afterDisposition := readCheckpointDisposition(t, fixture.service, req.Scope); !reflect.DeepEqual(afterDisposition, beforeDisposition) {
		t.Fatalf("invalid-material disposition changed\nbefore=%+v\nafter=%+v", beforeDisposition, afterDisposition)
	}
}

func TestCheckpointStickyInvalidationUnknownCommitConfirmation(t *testing.T) {
	tests := []struct {
		name        string
		transaction int
		match       localstore.WorkspaceCheckpointCommitMatch
		confirmErr  error
		want        error
	}{
		{name: "first next", transaction: 1, match: localstore.WorkspaceCheckpointCommitNext, want: ErrPublicationUnclassified},
		{name: "first prior", transaction: 1, match: localstore.WorkspaceCheckpointCommitPrior, want: localstore.ErrCommitOutcomeUnknown},
		{name: "first third", transaction: 1, match: localstore.WorkspaceCheckpointCommitThird, want: localstore.ErrCommitOutcomeUnknown},
		{name: "first invalid", transaction: 1, match: localstore.WorkspaceCheckpointCommitMatch(99), want: localstore.ErrCommitOutcomeUnknown},
		{name: "first read error", transaction: 1, confirmErr: errors.New("invalidation confirmation failed"), want: localstore.ErrCommitOutcomeUnknown},
		{name: "second next", transaction: 2, match: localstore.WorkspaceCheckpointCommitNext, want: ErrPublicationUnclassified},
		{name: "second prior", transaction: 2, match: localstore.WorkspaceCheckpointCommitPrior, want: localstore.ErrCommitOutcomeUnknown},
		{name: "second third", transaction: 2, match: localstore.WorkspaceCheckpointCommitThird, want: localstore.ErrCommitOutcomeUnknown},
		{name: "second invalid", transaction: 2, match: localstore.WorkspaceCheckpointCommitMatch(99), want: localstore.ErrCommitOutcomeUnknown},
		{name: "second read error", transaction: 2, confirmErr: errors.New("invalidation confirmation failed"), want: localstore.ErrCommitOutcomeUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			if test.transaction == 1 {
				runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/changed.git")
			} else {
				realObserver := fixture.service.publicationTrustObserver()
				observerCalls := 0
				fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
					observerCalls++
					if observerCalls == 3 {
						runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/changed.git")
					}
					return realObserver(ctx, binding)
				}
			}
			realWithImmediate := fixture.service.repo.WithImmediateWorkspace
			transactionCalls := 0
			unknown := fmt.Errorf("synthetic invalidation: %w", localstore.ErrCommitOutcomeUnknown)
			fixture.service.withImmediateWorkspace = func(
				ctx context.Context,
				scope types.WorkspaceScope,
				fn func(*localstore.WorkspaceMutationTx) error,
			) error {
				transactionCalls++
				err := realWithImmediate(ctx, scope, fn)
				if err == nil && transactionCalls == test.transaction {
					return unknown
				}
				return err
			}
			confirmCalls := 0
			fixture.service.confirmCheckpointCommit = func(
				context.Context,
				localstore.WorkspaceCheckpointCommitState,
				localstore.WorkspaceCheckpointCommitState,
			) (localstore.WorkspaceCheckpointCommitMatch, error) {
				confirmCalls++
				return test.match, test.confirmErr
			}

			got, err := fixture.service.Checkpoint(context.Background(), req)
			if !errors.Is(err, test.want) || got != (CheckpointResult{}) || confirmCalls != 1 ||
				transactionCalls != test.transaction || fixture.prepareCalls != test.transaction-1 ||
				fixture.publishCalls != 0 || fixture.closeCalls != test.transaction-1 {
				t.Fatalf("unknown invalidation = (%+v, %v), transactions=%d confirm=%d prepare=%d publish=%d close=%d",
					got, err, transactionCalls, confirmCalls, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
			}
		})
	}
}

func TestCheckpointSecondPlanDriftPrecedesOriginInvalidation(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	realObserver := fixture.service.publicationTrustObserver()
	calls := 0
	fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
		calls++
		if calls == 3 {
			accepted := fixture.mustAcceptedSnapshot(t)
			operation := servicePutTaskOperation(
				accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "late with origin drift",
			)
			if _, err := fixture.service.Apply(ctx, req.Scope, operation); err != nil {
				t.Fatal(err)
			}
			runGit(t, fixture.repository.root, "remote", "set-url", "origin", "https://github.com/acme/changed.git")
		}
		return realObserver(ctx, binding)
	}
	got, err := fixture.service.Checkpoint(context.Background(), req)
	if err == nil || errors.Is(err, ErrPublicationUnclassified) || got != (CheckpointResult{}) ||
		fixture.publishCalls != 0 || fixture.closeCalls != 1 {
		t.Fatalf("combined drift Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
	}
	policy, history := readPublicationPolicyState(t, fixture.service, req.Scope)
	if policy.Classification != types.PublicationLocalOnly || policy.PolicyRevision != 2 || len(history) != 2 {
		t.Fatalf("combined drift changed policy=%+v history=%+v", policy, history)
	}
	disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
	if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" ||
		len(disposition.Operations) != 1 || disposition.Operations[0].State != "active" {
		t.Fatalf("combined drift disposition = %+v", disposition)
	}
}

func TestCheckpointUnknownCommitConfirmationMatrix(t *testing.T) {
	tests := []struct {
		name        string
		transaction int
		match       localstore.WorkspaceCheckpointCommitMatch
		confirmErr  error
		wantOK      bool
	}{
		{name: "first next", transaction: 1, match: localstore.WorkspaceCheckpointCommitNext, wantOK: true},
		{name: "first prior", transaction: 1, match: localstore.WorkspaceCheckpointCommitPrior},
		{name: "first third", transaction: 1, match: localstore.WorkspaceCheckpointCommitThird},
		{name: "first invalid outcome", transaction: 1, match: localstore.WorkspaceCheckpointCommitMatch(99)},
		{name: "first read error", transaction: 1, confirmErr: errors.New("confirmation read failed")},
		{name: "final next", transaction: 2, match: localstore.WorkspaceCheckpointCommitNext, wantOK: true},
		{name: "final prior", transaction: 2, match: localstore.WorkspaceCheckpointCommitPrior},
		{name: "final third", transaction: 2, match: localstore.WorkspaceCheckpointCommitThird},
		{name: "final invalid outcome", transaction: 2, match: localstore.WorkspaceCheckpointCommitMatch(99)},
		{name: "final read error", transaction: 2, confirmErr: errors.New("confirmation read failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			realWithImmediate := fixture.service.repo.WithImmediateWorkspace
			transactionCalls := 0
			unknown := fmt.Errorf("synthetic: %w", localstore.ErrCommitOutcomeUnknown)
			fixture.service.withImmediateWorkspace = func(
				ctx context.Context,
				scope types.WorkspaceScope,
				fn func(*localstore.WorkspaceMutationTx) error,
			) error {
				transactionCalls++
				err := realWithImmediate(ctx, scope, fn)
				if err == nil && transactionCalls == test.transaction {
					return unknown
				}
				return err
			}
			confirmCalls := 0
			fixture.service.confirmCheckpointCommit = func(
				context.Context,
				localstore.WorkspaceCheckpointCommitState,
				localstore.WorkspaceCheckpointCommitState,
			) (localstore.WorkspaceCheckpointCommitMatch, error) {
				confirmCalls++
				return test.match, test.confirmErr
			}
			got, err := fixture.service.Checkpoint(context.Background(), req)
			wantTransactions := test.transaction
			wantPublish := 0
			if test.transaction == 2 {
				wantPublish = 1
			} else if test.match == localstore.WorkspaceCheckpointCommitNext && test.confirmErr == nil {
				wantTransactions = 2
				wantPublish = 1
			}
			if test.wantOK {
				if err != nil || got.JournalID == "" || confirmCalls != 1 || transactionCalls != wantTransactions ||
					fixture.prepareCalls != 1 || fixture.publishCalls != wantPublish || fixture.closeCalls != 1 {
					t.Fatalf("confirmed next = (%+v, %v), transactions=%d confirm=%d prepare=%d publish=%d close=%d",
						got, err, transactionCalls, confirmCalls, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
				}
				return
			}
			if !errors.Is(err, localstore.ErrCommitOutcomeUnknown) || got != (CheckpointResult{}) || confirmCalls != 1 ||
				transactionCalls != wantTransactions || fixture.prepareCalls != 1 ||
				fixture.publishCalls != wantPublish || fixture.closeCalls != 1 {
				t.Fatalf("unknown outcome = (%+v, %v), transactions=%d confirm=%d prepare=%d publish=%d close=%d",
					got, err, transactionCalls, confirmCalls, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
			}
		})
	}
}

func TestDefaultCheckpointArtifactFactoryIntegration(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real checkpoint artifacts are supported only on Linux")
	}
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	accepted := fixture.mustAcceptedSnapshot(t)
	operation := servicePutTaskOperation(
		accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "real artifact candidate",
	)
	if _, err := fixture.service.Apply(context.Background(), req.Scope, operation); err != nil {
		t.Fatal(err)
	}
	setServiceWorkspaceState(t, fixture.store, req.Scope, "clean")
	before, err := fixture.service.repo.Workspace(context.Background(), req.Scope)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.prepareCheckpointArtifact = nil
	got, err := fixture.service.Checkpoint(context.Background(), req)
	if err != nil || got.JournalID == "" {
		t.Fatalf("real artifact Checkpoint = (%+v, %v)", got, err)
	}
	disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
	if len(disposition.Journals) != 1 || len(disposition.Operations) != 1 {
		t.Fatalf("real artifact disposition = %+v", disposition)
	}
	journal := disposition.Journals[0]
	if journal.State != "published" || journal.JournalID != got.JournalID ||
		journal.ExpectedLiveDigest != req.ExpectedWorkingTreeDigest || journal.PriorTreeDigest != req.ExpectedWorkingTreeDigest ||
		journal.AcceptedBaseDigest != state.Digest(before.Binding.AcceptedTreeDigest) || journal.Checkout != before.Binding.Checkout ||
		journal.CandidateDigest != got.CandidateDigest || journal.ThroughGeneration != got.MaterializedThroughGeneration ||
		journal.ThroughGeneration != 1 || journal.PublicationReviewProofVersion != 1 ||
		journal.IncludedOperationsJSON == nil || journal.PublicationReviewJSON == nil || journal.PriorCandidateJSON == nil ||
		disposition.Operations[0].OperationID != operation.ID || disposition.Operations[0].State != "materialized" {
		t.Fatalf("real artifact durable state: journal=%+v operations=%+v", journal, disposition.Operations)
	}
	checkpointRoot := filepath.Join(req.Root, ".git", "wormhole", "checkpoints")
	if journal.StagePath != filepath.Join(checkpointRoot, got.JournalID+".stage") ||
		journal.BackupPath != filepath.Join(checkpointRoot, got.JournalID+".backup") {
		t.Fatalf("real artifact topology paths: stage=%q backup=%q", journal.StagePath, journal.BackupPath)
	}
	if _, err := os.Lstat(journal.StagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published stage remains: %v", err)
	}
	backup := readCheckpointTestPathTree(t, journal.BackupPath)
	if !equalCheckpointTree(backup, journal.PriorTree) {
		t.Fatalf("published backup differs\ngot=%v\nwant=%v", backup, journal.PriorTree)
	}
	live, err := ReadWorkingTreeNoFollow(req.Root)
	if err != nil || !equalCheckpointTree(live, journal.CandidateTree) {
		t.Fatalf("published live tree = (%v, %v), want %v", live, err, journal.CandidateTree)
	}
	after, err := fixture.service.repo.Workspace(context.Background(), req.Scope)
	if err != nil || after.State != "pending" || after.Binding != before.Binding ||
		!checkpointSnapshotsEqual(after.Snapshot, before.Snapshot) {
		t.Fatalf("real artifact workspace = (%+v, %v), before=%+v", after, err, before)
	}
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil || candidate.AcceptedBaseDigest != journal.AcceptedBaseDigest ||
			candidate.WorkingTreeDigest != journal.PriorTreeDigest || candidate.RebasedSnapshot == nil ||
			candidate.RebasedSnapshot.Digest != journal.CandidateDigest || candidate.RebasedThroughGeneration != 1 ||
			candidate.ImportedBy != req.Actor.PrincipalID() || !candidate.ImportedAt.Equal(req.Actor.OccurredAt) {
			return fmt.Errorf("real artifact candidate = %+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRequestAndLiveValidationPrecedence(t *testing.T) {
	readerErr := errors.New("live reader failed")
	tests := []struct {
		name   string
		mutate func(*checkpointCoordinatorFixture, *CheckpointRequest)
		want   error
	}{
		{name: "actor", mutate: func(_ *checkpointCoordinatorFixture, req *CheckpointRequest) {
			req.Actor = types.ActorEnvelope{}
			req.Root += "/wrong"
			req.ExpectedWorkingTreeDigest = "bad"
		}, want: types.ErrInvalidActorEnvelope},
		{name: "root", mutate: func(_ *checkpointCoordinatorFixture, req *CheckpointRequest) {
			req.Root += "/wrong"
			req.ExpectedWorkingTreeDigest = "bad"
		}},
		{name: "digest syntax", mutate: func(fixture *checkpointCoordinatorFixture, req *CheckpointRequest) {
			req.ExpectedWorkingTreeDigest = "bad"
			fixture.service.readWorkingTree = func(string) (state.Tree, error) { panic("invalid digest read live") }
		}},
		{name: "digest CAS", mutate: func(_ *checkpointCoordinatorFixture, req *CheckpointRequest) {
			req.ExpectedWorkingTreeDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
		}, want: ErrCheckpointCAS},
		{name: "live reader", mutate: func(fixture *checkpointCoordinatorFixture, _ *CheckpointRequest) {
			fixture.service.readWorkingTree = func(string) (state.Tree, error) { return nil, readerErr }
		}, want: readerErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			test.mutate(fixture, &req)
			got, err := fixture.service.Checkpoint(context.Background(), req)
			if err == nil || got != (CheckpointResult{}) || fixture.publishCalls != 0 || fixture.closeCalls != 0 {
				t.Fatalf("invalid Checkpoint = (%+v, %v), artifact publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("invalid Checkpoint error = %v, want %v", err, test.want)
			}
		})
	}

	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	fixture.service.observePublicationTrust = func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error) {
		panic("invalid scope observed workspace")
	}
	req.Scope.WorkspaceID = "bad"
	got, err := fixture.service.Checkpoint(context.Background(), req)
	if !errors.Is(err, localstore.ErrNotFound) || got != (CheckpointResult{}) {
		t.Fatalf("invalid scope Checkpoint = (%+v, %v)", got, err)
	}
}

func TestCheckpointArtifactPrepareErrorsCreateNoJournal(t *testing.T) {
	for _, prepareErr := range []error{ErrCheckpointUnsupported, ErrCheckpointCAS, context.Canceled, errors.New("prepare failed")} {
		t.Run(prepareErr.Error(), func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			fixture.service.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
				return checkpointArtifactHandle{}, prepareErr
			}
			got, err := fixture.service.Checkpoint(context.Background(), req)
			if !errors.Is(err, prepareErr) || got != (CheckpointResult{}) {
				t.Fatalf("prepare failure Checkpoint = (%+v, %v)", got, err)
			}
			if disposition := readCheckpointDisposition(t, fixture.service, req.Scope); len(disposition.Journals) != 0 {
				t.Fatalf("prepare failure persisted journal: %+v", disposition)
			}
		})
	}
}

func TestCheckpointPrepareErrorClosesReturnedArtifactOwnership(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	prepareErr := errors.New("prepare returned owned residue")
	fixture.service.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
		fixture.prepareCalls++
		return checkpointArtifactHandle{
			publish: func(context.Context) (checkpointPublicationDisposition, error) {
				panic("prepare-error artifact published")
			},
			close: func() { fixture.closeCalls++ },
		}, prepareErr
	}

	got, err := fixture.service.Checkpoint(context.Background(), req)
	if !errors.Is(err, prepareErr) || got != (CheckpointResult{}) || fixture.prepareCalls != 1 ||
		fixture.publishCalls != 0 || fixture.closeCalls != 1 {
		t.Fatalf("owned prepare failure = (%+v, %v), prepare=%d publish=%d close=%d", got, err, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
	}
	if disposition := readCheckpointDisposition(t, fixture.service, req.Scope); len(disposition.Journals) != 0 {
		t.Fatalf("owned prepare failure persisted journal: %+v", disposition)
	}
}

func TestCheckpointCurrentOpenConflictReturnsDirectSentinel(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	insertServiceConflict(t, fixture.store, req.Scope, "current checkpoint conflict", state.RecordKey{
		Kind: "task", ID: "22222222-2222-4222-8222-222222222222",
	}, "open")
	setServiceWorkspaceState(t, fixture.store, req.Scope, "conflicted")
	got, err := fixture.service.Checkpoint(context.Background(), req)
	if !errors.Is(err, localstore.ErrWorkspaceConflicted) || got != (CheckpointResult{}) ||
		fixture.publishCalls != 0 || fixture.closeCalls != 0 {
		t.Fatalf("conflicted Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
	}
}

func TestCheckpointResolvedAndOtherWorkspaceConflictsDoNotBlock(t *testing.T) {
	t.Run("resolved current", func(t *testing.T) {
		fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
		insertServiceConflict(t, fixture.store, req.Scope, "resolved checkpoint conflict", state.RecordKey{
			Kind: "task", ID: "22222222-2222-4222-8222-222222222222",
		}, "resolved")
		got, err := fixture.service.Checkpoint(context.Background(), req)
		if err != nil || got.JournalID == "" || fixture.publishCalls != 1 || fixture.closeCalls != 1 {
			t.Fatalf("resolved-conflict Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
		}
	})
	t.Run("other project", func(t *testing.T) {
		fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
		neighborRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
		neighbor := registerGitRepository(t, fixture.service, neighborRepository)
		insertServiceConflict(t, fixture.store, neighbor.Binding.Scope, "other project checkpoint conflict", state.RecordKey{
			Kind: "task", ID: "22222222-2222-4222-8222-222222222222",
		}, "open")
		setServiceWorkspaceState(t, fixture.store, neighbor.Binding.Scope, "conflicted")
		got, err := fixture.service.Checkpoint(context.Background(), req)
		if err != nil || got.JournalID == "" || fixture.publishCalls != 1 || fixture.closeCalls != 1 {
			t.Fatalf("other-project conflict Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
		}
	})
}

func TestCheckpointHelperFailuresRemainClosed(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	observerErr := errors.New("outside observer failed")
	fixture.service.observePublicationTrust = func(context.Context, types.WorkspaceBinding) (publicationTrustObservation, error) {
		return publicationTrustObservation{}, observerErr
	}
	if got, err := fixture.service.Checkpoint(context.Background(), req); !errors.Is(err, observerErr) || got != (CheckpointResult{}) {
		t.Fatalf("outside observer failure = (%+v, %v)", got, err)
	}
	if err := validateCheckpointAcknowledgement(types.PublicationClassification("future"), req.ExpectedWorkingTreeDigest, nil); err == nil {
		t.Fatal("unknown publication classification was accepted")
	}
	fixture.service.readWorkingTree = func(string) (state.Tree, error) { return state.Tree{}, nil }
	if tree, err := fixture.service.checkpointReadLive(req.Root, fixture.binding, req.ExpectedWorkingTreeDigest, nil); err == nil || tree != nil {
		t.Fatalf("invalid live tree = (%+v, %v)", tree, err)
	}
}

func TestCheckpointSecondTransactionLateOperationAndConflictPublishNothing(t *testing.T) {
	t.Run("late operation", func(t *testing.T) {
		fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
		realObserver := fixture.service.publicationTrustObserver()
		calls := 0
		fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
			calls++
			if calls == 3 {
				accepted := fixture.mustAcceptedSnapshot(t)
				operation := servicePutTaskOperation(
					accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "late",
				)
				if _, err := fixture.service.Apply(ctx, req.Scope, operation); err != nil {
					t.Fatal(err)
				}
			}
			return realObserver(ctx, binding)
		}
		got, err := fixture.service.Checkpoint(context.Background(), req)
		if err == nil || got != (CheckpointResult{}) || fixture.publishCalls != 0 || fixture.closeCalls != 1 {
			t.Fatalf("late-operation Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
		}
		disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
		if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" ||
			len(disposition.Operations) != 1 || disposition.Operations[0].State != "active" {
			t.Fatalf("late-operation disposition = %+v", disposition)
		}
	})
	t.Run("conflict", func(t *testing.T) {
		fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
		realObserver := fixture.service.publicationTrustObserver()
		calls := 0
		fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
			calls++
			if calls == 3 {
				insertServiceConflict(t, fixture.store, req.Scope, "checkpoint conflict", state.RecordKey{Kind: "task", ID: "22222222-2222-4222-8222-222222222222"}, "open")
				setServiceWorkspaceState(t, fixture.store, req.Scope, "conflicted")
			}
			return realObserver(ctx, binding)
		}
		got, err := fixture.service.Checkpoint(context.Background(), req)
		if !errors.Is(err, localstore.ErrWorkspaceConflicted) || got != (CheckpointResult{}) ||
			fixture.publishCalls != 0 || fixture.closeCalls != 1 {
			t.Fatalf("conflict Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
		}
	})
}

func TestCheckpointSecondTransactionPlanAndProofDriftPublishNothing(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*checkpointCoordinatorFixture, CheckpointRequest)
	}{
		{name: "live tree", configure: func(fixture *checkpointCoordinatorFixture, _ CheckpointRequest) {
			original := fixture.service.readWorkingTree
			if original == nil {
				original = ReadWorkingTreeNoFollow
			}
			changed := checkpointPlanMutatedSnapshot(t, fixture.mustAcceptedSnapshot(t), "late live tree")
			changedTree, err := state.EncodeTree(changed)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			fixture.service.readWorkingTree = func(root string) (state.Tree, error) {
				calls++
				if calls == 3 {
					return cloneCheckpointTree(changedTree), nil
				}
				return original(root)
			}
		}},
		{name: "binding", configure: func(fixture *checkpointCoordinatorFixture, req CheckpointRequest) {
			checkpointOnSecondOutsideObservation(t, fixture, func() {
				if _, err := fixture.store.DB().Exec(`
					UPDATE workspace_bindings SET accepted_ref='refs/heads/late'
					WHERE project_id=? AND workspace_id=?
				`, req.Scope.ProjectID, req.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			})
		}},
		{name: "checkout", configure: func(fixture *checkpointCoordinatorFixture, req CheckpointRequest) {
			checkpointOnSecondOutsideObservation(t, fixture, func() {
				if _, err := fixture.store.DB().Exec(`
					UPDATE workspace_bindings SET checkout_path=?
					WHERE project_id=? AND workspace_id=?
				`, filepath.Join(req.Root, "late"), req.Scope.ProjectID, req.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			})
		}},
		{name: "candidate", configure: func(fixture *checkpointCoordinatorFixture, req CheckpointRequest) {
			checkpointOnSecondOutsideObservation(t, fixture, func() {
				direct := checkpointPlanMutatedSnapshot(t, fixture.mustAcceptedSnapshot(t), "late candidate")
				candidate := checkpointPlanCandidate(fixture.binding, direct, nil, 0)
				if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
					return tx.UpsertCandidate(context.Background(), *candidate)
				}); err != nil {
					t.Fatal(err)
				}
			})
		}},
		{name: "proof bytes", configure: func(fixture *checkpointCoordinatorFixture, req CheckpointRequest) {
			checkpointOnSecondOutsideObservation(t, fixture, func() {
				if _, err := fixture.store.DB().Exec(`
					UPDATE workspace_materializations SET publication_review_json='{}'
					WHERE project_id=? AND workspace_id=? AND state='prepared'
				`, req.Scope.ProjectID, req.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			test.configure(fixture, req)
			got, err := fixture.service.Checkpoint(context.Background(), req)
			if err == nil || got != (CheckpointResult{}) || fixture.publishCalls != 0 || fixture.closeCalls != 1 {
				t.Fatalf("drifted Checkpoint = (%+v, %v), publish=%d close=%d", got, err, fixture.publishCalls, fixture.closeCalls)
			}
			var prepared int
			if err := fixture.store.DB().QueryRow(`
				SELECT COUNT(*) FROM workspace_materializations
				WHERE project_id=? AND workspace_id=? AND state='prepared'
			`, req.Scope.ProjectID, req.Scope.WorkspaceID).Scan(&prepared); err != nil || prepared != 1 {
				t.Fatalf("prepared evidence count = (%d, %v), want (1, nil)", prepared, err)
			}
		})
	}
}

func TestCheckpointSecondTransactionRejectsExactTerminalDispositionDrift(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	first, err := fixture.service.Checkpoint(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		disposition, err := tx.MaterializationDisposition(context.Background())
		if err != nil {
			return err
		}
		if len(disposition.Journals) != 1 || disposition.Journals[0].JournalID != first.JournalID {
			return fmt.Errorf("unexpected first checkpoint disposition: %+v", disposition)
		}
		_, err = tx.AcceptMaterialization(context.Background(), disposition.Journals[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls = 0, 0, 0
	const secondJournalID = "40000000-0000-4000-8000-000000000001"
	privateRoot := t.TempDir()
	fixture.service.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
		fixture.prepareCalls++
		return checkpointArtifactHandle{
			evidence: checkpointArtifactEvidence{
				JournalID:  secondJournalID,
				StagePath:  filepath.Join(privateRoot, secondJournalID+".stage"),
				BackupPath: filepath.Join(privateRoot, secondJournalID+".backup"),
			},
			publish: func(context.Context) (checkpointPublicationDisposition, error) {
				fixture.publishCalls++
				return checkpointPublicationPublished, nil
			},
			close: func() { fixture.closeCalls++ },
		}, nil
	}
	checkpointOnSecondOutsideObservation(t, fixture, func() {
		if _, err := fixture.store.DB().Exec(`
			UPDATE workspace_materializations SET stage_path=stage_path||'.drift'
			WHERE project_id=? AND workspace_id=? AND state='accepted'
		`, req.Scope.ProjectID, req.Scope.WorkspaceID); err != nil {
			t.Fatal(err)
		}
	})

	got, err := fixture.service.Checkpoint(context.Background(), req)
	if err == nil || got != (CheckpointResult{}) || fixture.prepareCalls != 1 ||
		fixture.publishCalls != 0 || fixture.closeCalls != 1 {
		t.Fatalf("terminal-drift Checkpoint = (%+v, %v), prepare=%d publish=%d close=%d", got, err, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
	}
	disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
	if len(disposition.Journals) != 2 || disposition.Journals[0].State != "accepted" ||
		disposition.Journals[1].JournalID != secondJournalID || disposition.Journals[1].State != "prepared" {
		t.Fatalf("terminal-drift disposition = %+v", disposition)
	}
}

func TestCheckpointSecondTransactionRejectsExactOperationDispositionDrift(t *testing.T) {
	tests := []struct {
		name       string
		before     func(*checkpointCoordinatorFixture, CheckpointRequest, state.OperationV1)
		secondStep func(*checkpointCoordinatorFixture, CheckpointRequest, state.OperationV1)
	}{
		{name: "late discarded membership", secondStep: func(fixture *checkpointCoordinatorFixture, req CheckpointRequest, operation state.OperationV1) {
			if _, err := fixture.service.Apply(context.Background(), req.Scope, operation); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.DB().Exec(`
				UPDATE workspace_overlay_operations SET state='discarded'
				WHERE project_id=? AND workspace_id=? AND operation_id=?
			`, req.Scope.ProjectID, req.Scope.WorkspaceID, operation.ID); err != nil {
				t.Fatal(err)
			}
			setServiceWorkspaceState(t, fixture.store, req.Scope, "clean")
		}},
		{name: "discarded operation bytes", before: func(fixture *checkpointCoordinatorFixture, req CheckpointRequest, operation state.OperationV1) {
			if _, err := fixture.service.Apply(context.Background(), req.Scope, operation); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.DB().Exec(`
				UPDATE workspace_overlay_operations SET state='discarded'
				WHERE project_id=? AND workspace_id=? AND operation_id=?
			`, req.Scope.ProjectID, req.Scope.WorkspaceID, operation.ID); err != nil {
				t.Fatal(err)
			}
			setServiceWorkspaceState(t, fixture.store, req.Scope, "clean")
		}, secondStep: func(fixture *checkpointCoordinatorFixture, req CheckpointRequest, operation state.OperationV1) {
			changed := operation
			changed.PutRecord.Record.Task.Title = "changed ignored bytes"
			raw, err := state.CanonicalOperation(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.DB().Exec(`
				UPDATE workspace_overlay_operations SET operation_json=?
				WHERE project_id=? AND workspace_id=? AND operation_id=?
			`, raw, req.Scope.ProjectID, req.Scope.WorkspaceID, operation.ID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
			operation := servicePutTaskOperation(
				fixture.mustAcceptedSnapshot(t), "99999999-9999-4999-8999-999999999991",
				"22222222-2222-4222-8222-222222222222", "ignored disposition row",
			)
			if test.before != nil {
				test.before(fixture, req, operation)
			}
			checkpointOnSecondOutsideObservation(t, fixture, func() { test.secondStep(fixture, req, operation) })

			got, err := fixture.service.Checkpoint(context.Background(), req)
			if err == nil || got != (CheckpointResult{}) || fixture.prepareCalls != 1 ||
				fixture.publishCalls != 0 || fixture.closeCalls != 1 {
				t.Fatalf("operation-disposition drift = (%+v, %v), prepare=%d publish=%d close=%d",
					got, err, fixture.prepareCalls, fixture.publishCalls, fixture.closeCalls)
			}
			disposition := readCheckpointDisposition(t, fixture.service, req.Scope)
			if len(disposition.Journals) != 1 || disposition.Journals[0].State != "prepared" ||
				len(disposition.Operations) != 1 || disposition.Operations[0].State != "discarded" {
				t.Fatalf("operation-disposition drift durable state = %+v", disposition)
			}
		})
	}
}

func TestCheckpointPendingStructuralAndCardinalityValidation(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	publishErr := errors.New("retain prepared")
	baseFactory := fixture.service.prepareCheckpointArtifact
	fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
		handle, err := baseFactory(ctx, input)
		handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, publishErr }
		return handle, err
	}
	if _, err := fixture.service.Checkpoint(context.Background(), req); !errors.Is(err, publishErr) {
		t.Fatal(err)
	}
	base := readCheckpointDisposition(t, fixture.service, req.Scope)
	binding := fixture.binding
	terminalOnly := cloneImportDisposition(base)
	terminalOnly.Journals[0].State = "accepted"
	if got, err := checkpointRequirePreparedDisposition(binding, terminalOnly, base.Journals[0]); err == nil || got.Journals != nil || got.Operations != nil {
		t.Fatalf("terminal-only prepared disposition = (%+v, %v)", got, err)
	}
	for _, journalState := range []string{"prepared", "published", "recovered_new"} {
		value := cloneImportDisposition(base)
		value.Journals[0].State = journalState
		pending, err := checkpointPendingJournal(binding, value)
		if err != nil || pending == nil || pending.State != journalState {
			t.Fatalf("valid pending %s = (%+v, %v)", journalState, pending, err)
		}
	}
	tests := []struct {
		name   string
		mutate func(*localstore.WorkspaceMaterializationDisposition)
	}{
		{name: "nil journals", mutate: func(value *localstore.WorkspaceMaterializationDisposition) { value.Journals = nil }},
		{name: "nil operations", mutate: func(value *localstore.WorkspaceMaterializationDisposition) { value.Operations = nil }},
		{name: "duplicate pending", mutate: func(value *localstore.WorkspaceMaterializationDisposition) {
			second := cloneMaterializationRecord(value.Journals[0])
			second.JournalID = "40000000-0000-4000-8000-000000000001"
			second.StagePath = filepath.Join(filepath.Dir(second.StagePath), second.JournalID+".stage")
			second.BackupPath = filepath.Join(filepath.Dir(second.BackupPath), second.JournalID+".backup")
			value.Journals = append(value.Journals, second)
		}},
		{name: "unknown state", mutate: func(value *localstore.WorkspaceMaterializationDisposition) { value.Journals[0].State = "future" }},
		{name: "bad journal ID", mutate: func(value *localstore.WorkspaceMaterializationDisposition) { value.Journals[0].JournalID = "bad" }},
		{name: "missing proof", mutate: func(value *localstore.WorkspaceMaterializationDisposition) {
			value.Journals[0].PublicationReviewJSON = nil
		}},
		{name: "wrong path", mutate: func(value *localstore.WorkspaceMaterializationDisposition) { value.Journals[0].StagePath += ".wrong" }},
		{name: "prior tree", mutate: func(value *localstore.WorkspaceMaterializationDisposition) {
			value.Journals[0].PriorTree[0].Data[0] ^= 1
		}},
		{name: "candidate tree", mutate: func(value *localstore.WorkspaceMaterializationDisposition) {
			value.Journals[0].CandidateTree[0].Data[0] ^= 1
		}},
		{name: "operation proof", mutate: func(value *localstore.WorkspaceMaterializationDisposition) {
			bad := "{}\n"
			value.Journals[0].IncludedOperationsJSON = &bad
		}},
		{name: "boundary", mutate: func(value *localstore.WorkspaceMaterializationDisposition) { value.Journals[0].ThroughGeneration++ }},
		{name: "publication proof", mutate: func(value *localstore.WorkspaceMaterializationDisposition) {
			bad := "{}\n"
			value.Journals[0].PublicationReviewJSON = &bad
		}},
		{name: "prior candidate proof", mutate: func(value *localstore.WorkspaceMaterializationDisposition) {
			bad := "{}\n"
			value.Journals[0].PriorCandidateJSON = &bad
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneImportDisposition(base)
			test.mutate(&value)
			if pending, err := checkpointPendingJournal(binding, value); err == nil || pending != nil {
				t.Fatalf("malformed pending = (%+v, %v)", pending, err)
			}
		})
	}
}

func TestCheckpointPendingFastPathValidatesAllTerminalProofs(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	publishErr := errors.New("retain prepared")
	baseFactory := fixture.service.prepareCheckpointArtifact
	fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
		handle, err := baseFactory(ctx, input)
		handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, publishErr }
		return handle, err
	}
	if _, err := fixture.service.Checkpoint(context.Background(), req); !errors.Is(err, publishErr) {
		t.Fatal(err)
	}
	base := readCheckpointDisposition(t, fixture.service, req.Scope)
	terminal := cloneMaterializationRecord(base.Journals[0])
	terminal.JournalID = "20000000-0000-4000-8000-000000000001"
	terminal.StagePath = filepath.Join(filepath.Dir(terminal.StagePath), terminal.JournalID+".stage")
	terminal.BackupPath = filepath.Join(filepath.Dir(terminal.BackupPath), terminal.JournalID+".backup")
	terminal.State = "accepted"
	mixed := cloneImportDisposition(base)
	mixed.Journals = append([]localstore.WorkspaceMaterializationRecord{terminal}, mixed.Journals...)
	if pending, err := checkpointPendingJournal(fixture.binding, mixed); err != nil || pending == nil || pending.State != "prepared" {
		t.Fatalf("valid mixed pending disposition = (%+v, %v)", pending, err)
	}

	tests := []struct {
		name   string
		mutate func(*localstore.WorkspaceMaterializationRecord)
	}{
		{name: "operation proof", mutate: func(journal *localstore.WorkspaceMaterializationRecord) {
			bad := "{}\n"
			journal.IncludedOperationsJSON = &bad
		}},
		{name: "publication proof", mutate: func(journal *localstore.WorkspaceMaterializationRecord) {
			bad := "{}\n"
			journal.PublicationReviewJSON = &bad
		}},
		{name: "prior candidate proof", mutate: func(journal *localstore.WorkspaceMaterializationRecord) {
			bad := "{}\n"
			journal.PriorCandidateJSON = &bad
		}},
		{name: "candidate tree", mutate: func(journal *localstore.WorkspaceMaterializationRecord) {
			journal.CandidateTree[0].Data[0] ^= 1
		}},
		{name: "proof version", mutate: func(journal *localstore.WorkspaceMaterializationRecord) {
			journal.PublicationReviewProofVersion = 2
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneImportDisposition(mixed)
			test.mutate(&value.Journals[0])
			if pending, err := checkpointPendingJournal(fixture.binding, value); err == nil || pending != nil {
				t.Fatalf("malformed mixed pending disposition = (%+v, %v)", pending, err)
			}
		})
	}
}

func TestCheckpointPendingFastPathIgnoresRecoveredOldLegacyOperationEnvelope(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	publishErr := errors.New("retain prepared")
	baseFactory := fixture.service.prepareCheckpointArtifact
	fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
		handle, err := baseFactory(ctx, input)
		handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, publishErr }
		return handle, err
	}
	if _, err := fixture.service.Checkpoint(context.Background(), req); !errors.Is(err, publishErr) {
		t.Fatal(err)
	}
	base := readCheckpointDisposition(t, fixture.service, req.Scope)
	legacy := cloneMaterializationRecord(base.Journals[0])
	legacy.JournalID = "20000000-0000-4000-8000-000000000001"
	legacy.StagePath = filepath.Join(filepath.Dir(legacy.StagePath), legacy.JournalID+".stage")
	legacy.BackupPath = filepath.Join(filepath.Dir(legacy.BackupPath), legacy.JournalID+".backup")
	legacy.State = "recovered_old"
	malformedLegacyEnvelope := "legacy operation evidence is intentionally noncanonical"
	legacy.IncludedOperationsJSON = &malformedLegacyEnvelope
	mixed := cloneImportDisposition(base)
	mixed.Journals = append([]localstore.WorkspaceMaterializationRecord{legacy}, mixed.Journals...)

	pending, err := checkpointPendingJournal(fixture.binding, mixed)
	if err != nil || pending == nil || pending.State != "prepared" || pending.JournalID != base.Journals[0].JournalID {
		t.Fatalf("recovered-old legacy envelope pending result = (%+v, %v)", pending, err)
	}
}

func TestCheckpointPendingReviewRequiresExactAcceptedGitPosition(t *testing.T) {
	fixture, req, _ := newCheckpointCoordinatorFixture(t, types.PublicationLocalOnly, diffActorEnvelope())
	publishErr := errors.New("retain prepared")
	baseFactory := fixture.service.prepareCheckpointArtifact
	fixture.service.prepareCheckpointArtifact = func(ctx context.Context, input checkpointArtifactInput) (checkpointArtifactHandle, error) {
		handle, err := baseFactory(ctx, input)
		handle.publish = func(context.Context) (checkpointPublicationDisposition, error) { return 0, publishErr }
		return handle, err
	}
	if _, err := fixture.service.Checkpoint(context.Background(), req); !errors.Is(err, publishErr) {
		t.Fatal(err)
	}
	base := readCheckpointDisposition(t, fixture.service, req.Scope)
	tests := []struct {
		name   string
		mutate func(*publicationReviewEnvelopeV1)
	}{
		{name: "accepted ref", mutate: func(review *publicationReviewEnvelopeV1) {
			review.AcceptedRef = "refs/heads/other"
		}},
		{name: "accepted commit", mutate: func(review *publicationReviewEnvelopeV1) {
			review.AcceptedCommitSHA = strings.Repeat("f", 40)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneImportDisposition(base)
			journal := &value.Journals[0]
			publication, err := decodeCheckpointPublicationReview(*journal.PublicationReviewJSON)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&publication.Review)
			_, digest, err := encodePublicationReviewEnvelope(publication.Review)
			if err != nil {
				t.Fatal(err)
			}
			publication.ReviewDigest = digest
			raw, err := encodeCheckpointPublicationReview(publication)
			if err != nil {
				t.Fatal(err)
			}
			journal.PublicationReviewJSON = &raw
			if pending, err := checkpointPendingJournal(fixture.binding, value); err == nil || pending != nil {
				t.Fatalf("mismatched accepted Git position = (%+v, %v)", pending, err)
			}
		})
	}
}

func TestCheckpointPublicationPostimageUsesOnlyDurableProofProvenance(t *testing.T) {
	for _, priorPresent := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_%t", priorPresent), func(t *testing.T) {
			input := checkpointPlanFixture(t)
			if priorPresent {
				input = checkpointPlanDirectInput(t, input)
			}
			journal := checkpointJournalFromPlan(t, input)
			got, err := checkpointPublicationPostimage(journal)
			if err != nil {
				t.Fatal(err)
			}
			wantBy := input.Actor.PrincipalID()
			wantAt := input.Actor.OccurredAt
			if input.Current != nil {
				wantBy, wantAt = input.Current.ImportedBy, input.Current.ImportedAt
			}
			if got.AcceptedBaseDigest != journal.AcceptedBaseDigest || got.WorkingTreeDigest != journal.PriorTreeDigest ||
				got.DirectSnapshot.Digest != journal.PriorTreeDigest || got.RebasedSnapshot == nil ||
				got.RebasedSnapshot.Digest != journal.CandidateDigest || got.RebasedThroughGeneration != journal.ThroughGeneration ||
				got.ImportedBy != wantBy || !got.ImportedAt.Equal(wantAt) {
				t.Fatalf("publication postimage = %+v", got)
			}
		})
	}
	base := checkpointJournalFromPlan(t, checkpointPlanFixture(t))
	tests := []struct {
		name   string
		mutate func(*localstore.WorkspaceMaterializationRecord)
	}{
		{name: "proof version", mutate: func(value *localstore.WorkspaceMaterializationRecord) { value.PublicationReviewProofVersion = 0 }},
		{name: "publication JSON", mutate: func(value *localstore.WorkspaceMaterializationRecord) {
			bad := "{}\n"
			value.PublicationReviewJSON = &bad
		}},
		{name: "prior JSON", mutate: func(value *localstore.WorkspaceMaterializationRecord) { bad := "{}\n"; value.PriorCandidateJSON = &bad }},
		{name: "operation JSON", mutate: func(value *localstore.WorkspaceMaterializationRecord) {
			bad := "{}\n"
			value.IncludedOperationsJSON = &bad
		}},
		{name: "accepted digest", mutate: func(value *localstore.WorkspaceMaterializationRecord) {
			value.AcceptedBaseDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
		}},
		{name: "prior tree", mutate: func(value *localstore.WorkspaceMaterializationRecord) { value.PriorTree[0].Data[0] ^= 1 }},
		{name: "candidate tree", mutate: func(value *localstore.WorkspaceMaterializationRecord) { value.CandidateTree[0].Data[0] ^= 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneMaterializationRecord(base)
			test.mutate(&value)
			if got, err := checkpointPublicationPostimage(value); err == nil || !reflect.DeepEqual(got, localstore.WorkspaceCandidateRecord{}) {
				t.Fatalf("invalid publication postimage = (%+v, %v)", got, err)
			}
		})
	}
}

func TestCheckpointPublicationPostimageRejectsPriorCandidateFromAnotherWorkspace(t *testing.T) {
	input := checkpointPlanDirectInput(t, checkpointPlanFixture(t))
	journal := checkpointJournalFromPlan(t, input)
	prior, err := decodeCheckpointPriorCandidate(*journal.PriorCandidateJSON)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := validateCheckpointPriorTree(prior.Candidate.DirectTree, checkpointPriorTreeProductionLimits())
	if err != nil {
		t.Fatal(err)
	}
	direct.Config.ProjectID = "00000000-0000-4000-8000-000000000002"
	direct.Project.ID = direct.Config.ProjectID
	tree, err := state.EncodeTree(direct)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	prior.Candidate.DirectTree = checkpointPriorTree(tree, digest)
	prior.Candidate.WorkingTreeDigest = digest
	raw, err := encodeCheckpointPriorCandidate(prior)
	if err != nil {
		t.Fatal(err)
	}
	journal.PriorCandidateJSON = &raw

	if got, err := checkpointPublicationPostimage(journal); err == nil || !reflect.DeepEqual(got, localstore.WorkspaceCandidateRecord{}) {
		t.Fatalf("foreign prior candidate postimage = (%+v, %v)", got, err)
	}
}

func TestCheckpointLocalOnlyPublishesExactPlan(t *testing.T) {
	fixture := newPublicationServiceFixture(t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git")
	actor := diffActorEnvelope()
	configurePublicationForTest(t, fixture, types.PublicationLocalOnly, actor, time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	live, err := ReadWorkingTreeNoFollow(fixture.repository.root)
	if err != nil {
		t.Fatal(err)
	}
	liveDigest, err := state.DigestTree(live)
	if err != nil {
		t.Fatal(err)
	}
	const journalID = "30000000-0000-4000-8000-000000000001"
	privateRoot := t.TempDir()
	publishCalls, closeCalls := 0, 0
	fixture.service.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
		return checkpointArtifactHandle{
			evidence: checkpointArtifactEvidence{
				JournalID:  journalID,
				StagePath:  filepath.Join(privateRoot, journalID+".stage"),
				BackupPath: filepath.Join(privateRoot, journalID+".backup"),
			},
			publish: func(context.Context) (checkpointPublicationDisposition, error) {
				publishCalls++
				return checkpointPublicationPublished, nil
			},
			close: func() { closeCalls++ },
		}, nil
	}

	before, err := fixture.service.repo.Workspace(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fixture.service.Checkpoint(context.Background(), CheckpointRequest{
		Scope: fixture.binding.Scope, Root: fixture.repository.root,
		ExpectedWorkingTreeDigest: liveDigest, Actor: actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateDigest != liveDigest || got.MaterializedThroughGeneration != 0 || got.JournalID != journalID {
		t.Fatalf("Checkpoint result = %+v", got)
	}
	if publishCalls != 1 || closeCalls != 1 {
		t.Fatalf("artifact calls publish=%d close=%d, want one each", publishCalls, closeCalls)
	}

	after, err := fixture.service.repo.Workspace(context.Background(), fixture.binding.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if after.Binding != before.Binding || !checkpointSnapshotsEqual(after.Snapshot, before.Snapshot) || after.State != "pending" {
		t.Fatalf("workspace after checkpoint = %+v, before = %+v", after, before)
	}
	if err := fixture.service.repo.WithImmediateWorkspace(context.Background(), fixture.binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		disposition, err := tx.MaterializationDisposition(context.Background())
		if err != nil {
			return err
		}
		if len(disposition.Journals) != 1 || disposition.Journals[0].State != "published" ||
			disposition.Journals[0].JournalID != journalID || disposition.Journals[0].CandidateDigest != liveDigest ||
			disposition.Journals[0].AcceptedBaseDigest != state.Digest(before.Binding.AcceptedTreeDigest) ||
			len(disposition.Operations) != 0 {
			t.Fatalf("materialization disposition = %+v", disposition)
		}
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		if candidate == nil || candidate.AcceptedBaseDigest != state.Digest(before.Binding.AcceptedTreeDigest) ||
			candidate.WorkingTreeDigest != liveDigest || candidate.RebasedSnapshot == nil ||
			candidate.RebasedSnapshot.Digest != liveDigest || candidate.RebasedThroughGeneration != 0 ||
			candidate.ImportedBy != actor.PrincipalID() || !candidate.ImportedAt.Equal(actor.OccurredAt) {
			t.Fatalf("published candidate = %+v", candidate)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type checkpointCoordinatorFixture struct {
	publicationServiceFixture
	prepareCalls int
	publishCalls int
	closeCalls   int
}

func readCheckpointTestPathTree(t *testing.T, root string) state.Tree {
	t.Helper()
	tree := make(state.Tree, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink %q", path)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected non-regular file %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree = append(tree, state.File{Path: filepath.ToSlash(relative), Data: data})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return tree
}

func waitCheckpointGateRefs(t *testing.T, gates *checkpointGateSet, scope types.WorkspaceScope, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		gates.mu.Lock()
		refs := 0
		if entry := gates.byScope[scope]; entry != nil {
			refs = entry.refs
		}
		gates.mu.Unlock()
		if refs >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("checkpoint gate refs = %d, want at least %d", refs, want)
		}
		runtime.Gosched()
	}
}

func checkpointOnSecondOutsideObservation(t *testing.T, fixture *checkpointCoordinatorFixture, mutate func()) {
	t.Helper()
	realObserver := fixture.service.publicationTrustObserver()
	calls := 0
	fixture.service.observePublicationTrust = func(ctx context.Context, binding types.WorkspaceBinding) (publicationTrustObservation, error) {
		calls++
		if calls == 3 {
			mutate()
		}
		return realObserver(ctx, binding)
	}
}

func newCheckpointCoordinatorFixture(
	t *testing.T,
	classification types.PublicationClassification,
	actor types.ActorEnvelope,
) (*checkpointCoordinatorFixture, CheckpointRequest, state.Digest) {
	t.Helper()
	publication := newPublicationServiceFixture(
		t, "00000000-0000-4000-8000-000000000001", "https://github.com/acme/wormhole.git",
	)
	if classification != types.PublicationUnclassified {
		configurePublicationForTest(
			t, publication, classification, diffActorEnvelope(),
			time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC),
		)
	}
	status := mustServiceStatus(t, publication.service, publication.binding.Scope)
	live, err := ReadWorkingTreeNoFollow(publication.repository.root)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestTree(live)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &checkpointCoordinatorFixture{publicationServiceFixture: publication}
	const journalID = "30000000-0000-4000-8000-000000000001"
	privateRoot := t.TempDir()
	fixture.service.prepareCheckpointArtifact = func(context.Context, checkpointArtifactInput) (checkpointArtifactHandle, error) {
		fixture.prepareCalls++
		return checkpointArtifactHandle{
			evidence: checkpointArtifactEvidence{
				JournalID:  journalID,
				StagePath:  filepath.Join(privateRoot, journalID+".stage"),
				BackupPath: filepath.Join(privateRoot, journalID+".backup"),
			},
			publish: func(context.Context) (checkpointPublicationDisposition, error) {
				fixture.publishCalls++
				return checkpointPublicationPublished, nil
			},
			close: func() { fixture.closeCalls++ },
		}, nil
	}
	return fixture, CheckpointRequest{
		Scope: publication.binding.Scope, Root: publication.repository.root,
		ExpectedWorkingTreeDigest: digest, Actor: actor,
	}, status.PublicationReviewDigest
}

func readCheckpointDisposition(
	t *testing.T,
	service *Service,
	scope types.WorkspaceScope,
) localstore.WorkspaceMaterializationDisposition {
	t.Helper()
	var disposition localstore.WorkspaceMaterializationDisposition
	if err := service.repo.WithImmediateWorkspace(context.Background(), scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		disposition, err = tx.MaterializationDisposition(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return disposition
}

func checkpointJournalFromPlan(t *testing.T, input checkpointPlanInput) localstore.WorkspaceMaterializationRecord {
	t.Helper()
	plan, err := proveCheckpointPlan(input)
	if err != nil {
		t.Fatal(err)
	}
	const journalID = "30000000-0000-4000-8000-000000000001"
	included, publication, prior := plan.IncludedOperationsJSON, plan.PublicationReviewJSON, plan.PriorCandidateJSON
	return localstore.WorkspaceMaterializationRecord{
		JournalID: journalID, ExpectedLiveDigest: plan.PriorTreeDigest,
		AcceptedBaseDigest: state.Digest(input.Binding.AcceptedTreeDigest), Checkout: input.Binding.Checkout,
		PriorTreeDigest: plan.PriorTreeDigest, CandidateDigest: plan.CandidateDigest,
		ThroughGeneration: plan.ThroughGeneration, PriorTree: cloneCheckpointTree(plan.PriorTree),
		CandidateTree:          cloneCheckpointTree(plan.CandidateTree),
		StagePath:              filepath.Join(t.TempDir(), journalID+".stage"),
		BackupPath:             filepath.Join(t.TempDir(), journalID+".backup"),
		IncludedOperationsJSON: &included, PublicationReviewProofVersion: 1,
		PublicationReviewJSON: &publication, PriorCandidateJSON: &prior, State: "prepared",
	}
}
