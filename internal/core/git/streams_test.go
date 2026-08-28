package git

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	streamTestFabricID    = "11111111-1111-4111-8111-111111111141"
	streamTestStreamID    = "22222222-2222-4222-8222-222222222241"
	streamTestWorkspaceID = "33333333-3333-4333-8333-333333333341"
	streamTestActorID     = "44444444-4444-4444-8444-444444444441"
	streamTestArticleID   = "55555555-5555-4555-8555-555555555541"
	streamTestOperationA  = "66666666-6666-4666-8666-666666666661"
	streamTestOperationB  = "66666666-6666-4666-8666-666666666662"
	streamTestRef         = "refs/heads/main"
	streamTestCommitA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	streamTestCommitB     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type streamFixture struct {
	t           *testing.T
	db          *sql.DB
	store       *StreamStore
	key         StreamKey
	workspaceID string
	repository  types.RepositoryIdentity
	ref         RefObservation
	tree        projectstate.Tree
	scope       types.ActorScope
}

func TestAttachPersistsVersionZeroLiveAndAcceptedTrees(t *testing.T) {
	fixture := newStreamFixture(t, "stream-attach")
	transition := fixture.attach()
	if transition.Version != 0 || transition.Live.Digest != transition.Accepted.Digest || transition.AcceptedCommitSHA != streamTestCommitA {
		t.Fatalf("initial transition = %+v", transition)
	}

	stored := readStoredStreamVersion(t, fixture.db, fixture.key, 0)
	if stored.kind != "initial" || stored.operationID.Valid || len(stored.operationJSON) != 0 || len(stored.actorJSON) != 0 {
		t.Fatalf("stored initial operation evidence = %+v", stored)
	}
	live := decodeAndValidateStoredTree(t, stored.liveTree, stored.liveDigest)
	accepted := decodeAndValidateStoredTree(t, stored.acceptedTree, stored.acceptedDigest)
	assertStreamTreesEqual(t, live, accepted)
	assertStreamTreesEqual(t, fixture.tree, live)

	var provider, immutableID, remote, refName string
	var writable bool
	err := fixture.db.QueryRow(`SELECT repository_provider,repository_immutable_id,canonical_ref,writable
		FROM fabric_workspace_stream_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND workspace_id=$4`,
		fixture.key.ProjectID, fixture.key.FabricInstanceID, fixture.key.StreamID, fixture.workspaceID).
		Scan(&provider, &immutableID, &refName, &writable)
	if err != nil {
		t.Fatalf("read workspace binding: %v", err)
	}
	err = fixture.db.QueryRow(`SELECT canonical_remote FROM project_repository_bindings
		WHERE project_id=$1 AND fabric_instance_id=$2`, fixture.key.ProjectID, fixture.key.FabricInstanceID).Scan(&remote)
	if err != nil {
		t.Fatalf("read repository binding: %v", err)
	}
	if provider != fixture.repository.Provider || immutableID != fixture.repository.ImmutableID || remote != fixture.repository.CanonicalRemote || refName != streamTestRef || !writable {
		t.Fatalf("workspace binding = %q %q %q %q %v", provider, immutableID, remote, refName, writable)
	}

	t.Run("existing stream accepts exact second workspace", func(t *testing.T) {
		input := fixture.attachInput()
		input.WorkspaceID = "33333333-3333-4333-8333-333333333342"
		tx, err := fixture.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		second, err := fixture.store.AttachInTx(context.Background(), tx, fixture.scope, input)
		if err != nil {
			t.Fatalf("AttachInTx second workspace: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if second.Version != transition.Version || second.Live.Digest != transition.Live.Digest || countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key) != 1 {
			t.Fatalf("second workspace attach = %+v", second)
		}
	})

	t.Run("caller owns rollback", func(t *testing.T) {
		rolledBack := newStreamFixture(t, "stream-attach-rollback")
		tx, err := rolledBack.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rolledBack.store.AttachInTx(context.Background(), tx, rolledBack.scope, rolledBack.attachInput()); err != nil {
			t.Fatalf("AttachInTx: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if got := countStreamRows(t, rolledBack.db, "fabric_streams", rolledBack.key); got != 0 {
			t.Fatalf("rolled-back stream rows = %d", got)
		}
	})
}

func TestAttachRejectsRepositoryRefScopeAndTreeMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*streamFixture, *types.ActorScope, *AttachStreamInput)
	}{
		{"scope project", func(f *streamFixture, scope *types.ActorScope, _ *AttachStreamInput) {
			scope.ProjectID = "00000000-0000-4000-8000-000000000002"
		}},
		{"repository", func(_ *streamFixture, _ *types.ActorScope, input *AttachStreamInput) {
			input.Repository.ImmutableID = "999"
		}},
		{"observation repository", func(_ *streamFixture, _ *types.ActorScope, input *AttachStreamInput) {
			input.Ref.Repository.CanonicalRemote = "https://github.com/wormhole/other"
		}},
		{"default ref", func(_ *streamFixture, _ *types.ActorScope, input *AttachStreamInput) {
			input.Ref.RefName = "refs/heads/topic"
		}},
		{"tree project", func(tested *streamFixture, _ *types.ActorScope, input *AttachStreamInput) {
			input.Tree = streamTestTree(tested.t, "00000000-0000-4000-8000-000000000009", tested.repository)
		}},
		{"tree repository", func(tested *streamFixture, _ *types.ActorScope, input *AttachStreamInput) {
			other := tested.repository
			other.CanonicalRemote = "https://github.com/wormhole/other"
			input.Tree = streamTestTree(tested.t, tested.key.ProjectID, other)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStreamFixture(t, "stream-attach-mismatch-"+strings.ReplaceAll(test.name, " ", "-"))
			scope, input := fixture.scope, fixture.attachInput()
			test.mutate(fixture, &scope, &input)
			tx, err := fixture.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := fixture.store.AttachInTx(context.Background(), tx, scope, input); err == nil {
				t.Fatal("AttachInTx unexpectedly accepted mismatched authority")
			}
			if got := countStreamRows(t, fixture.db, "fabric_streams", fixture.key); got != 0 {
				t.Fatalf("mismatched attach persisted %d streams", got)
			}
		})
	}
}

func TestApplyOperationPersistsCanonicalOperationAndResultTree(t *testing.T) {
	fixture := newStreamFixture(t, "stream-apply")
	initial := fixture.attach()
	operation := streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "canonical\r\nbody\r\n")
	input := fixture.applyInput(initial, operation)
	transition := fixture.apply(input)

	canonical, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := projectstate.DigestCanonicalJSON(operation)
	if err != nil {
		t.Fatal(err)
	}
	stored := readStoredStreamVersion(t, fixture.db, fixture.key, 1)
	if stored.kind != "operation" || stored.operationID.String != operation.ID || !bytes.Equal(stored.operationJSON, canonical) || stored.operationDigest.String != string(digest) {
		t.Fatalf("stored operation evidence differs: %+v", stored)
	}
	var requestJSON, actorJSON []byte
	var requestDigest, result string
	if err := fixture.db.QueryRow(`SELECT canonical_operation_json,operation_digest,result,actor_envelope_json
		FROM fabric_stream_requests WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND ref_name=$4 AND operation_id=$5`,
		fixture.key.ProjectID, fixture.key.FabricInstanceID, fixture.key.StreamID, streamTestRef, operation.ID).
		Scan(&requestJSON, &requestDigest, &result, &actorJSON); err != nil {
		t.Fatalf("read request: %v", err)
	}
	wantActor, err := projectstate.CanonicalJSON(fixture.scope.Actor)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requestJSON, canonical) || requestDigest != string(digest) || result != "applied" || !bytes.Equal(actorJSON, wantActor) || !bytes.Equal(stored.actorJSON, wantActor) {
		t.Fatalf("request/version canonical evidence differs")
	}
	resultTree := decodeAndValidateStoredTree(t, stored.liveTree, stored.liveDigest)
	if transition.Live.Digest != projectstate.Digest(stored.liveDigest) || transition.Live.Articles[streamTestArticleID].Value == nil || string(transition.Live.Articles[streamTestArticleID].Body) != "canonical\nbody\n" {
		t.Fatalf("result transition = %+v", transition)
	}
	encodedResult, err := projectstate.EncodeTree(transition.Live)
	if err != nil {
		t.Fatal(err)
	}
	assertStreamTreesEqual(t, encodedResult, resultTree)
}

func TestReadReconstructsEveryVersionAfterRestart(t *testing.T) {
	fixture := newStreamFixture(t, "stream-restart")
	initial := fixture.attach()
	applied := fixture.apply(fixture.applyInput(initial, streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "first\n")))
	newAcceptedSnapshot, err := projectstate.ApplyOperation(initial.Accepted, streamActorOperation(initial.Accepted, fixture.scope.Actor, streamTestOperationB))
	if err != nil {
		t.Fatal(err)
	}
	newAcceptedTree, err := projectstate.EncodeTree(newAcceptedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	advanced := fixture.advance(AdvanceAcceptedInput{
		Key:  fixture.key,
		Ref:  RefObservation{Repository: fixture.repository, RefName: streamTestRef, CommitSHA: streamTestCommitB, ObservedAt: fixture.ref.ObservedAt.Add(time.Minute)},
		Tree: newAcceptedTree,
	})
	if advanced.Version != 2 || advanced.Live.Digest != applied.Live.Digest || advanced.ConflictID == "" {
		t.Fatalf("advanced transition = %+v", advanced)
	}

	for version := int64(0); version <= 2; version++ {
		fixture.reopen()
		transition, err := fixture.store.Read(context.Background(), fixture.key, version)
		if err != nil {
			t.Fatalf("Read version %d after restart: %v", version, err)
		}
		stored := readStoredStreamVersion(t, fixture.db, fixture.key, version)
		liveTree := decodeAndValidateStoredTree(t, stored.liveTree, stored.liveDigest)
		acceptedTree := decodeAndValidateStoredTree(t, stored.acceptedTree, stored.acceptedDigest)
		if transition.Live.Digest != projectstate.Digest(stored.liveDigest) || transition.Accepted.Digest != projectstate.Digest(stored.acceptedDigest) {
			t.Fatalf("version %d transition digests = %q/%q, want %q/%q", version, transition.Live.Digest, transition.Accepted.Digest, stored.liveDigest, stored.acceptedDigest)
		}
		encodedLive, err := projectstate.EncodeTree(transition.Live)
		if err != nil {
			t.Fatal(err)
		}
		encodedAccepted, err := projectstate.EncodeTree(transition.Accepted)
		if err != nil {
			t.Fatal(err)
		}
		assertStreamTreesEqual(t, liveTree, encodedLive)
		assertStreamTreesEqual(t, acceptedTree, encodedAccepted)
	}
}

func TestReadRejectsAmbiguousStreamKey(t *testing.T) {
	fixture := newStreamFixture(t, "stream-ambiguous-key")
	fixture.attach()
	storedTree, err := EncodeStoredTree(fixture.tree)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := projectstate.DigestTree(fixture.tree)
	if err != nil {
		t.Fatal(err)
	}
	const secondRef = "refs/heads/topic"
	if _, err := fixture.db.Exec(`INSERT INTO fabric_streams
		(project_id,fabric_instance_id,stream_id,canonical_ref,ref_name,current_version,live_tree_digest,accepted_tree_digest,accepted_commit_sha)
		VALUES($1,$2,$3,$4,$4,0,$5,$5,$6)`, fixture.key.ProjectID, fixture.key.FabricInstanceID,
		fixture.key.StreamID, secondRef, string(digest), streamTestCommitA); err != nil {
		t.Fatalf("insert ambiguous stream: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO fabric_stream_versions
		(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,
		 canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest)
		VALUES($1,$2,$3,$4,0,'initial',$5,$6,$7,$6,$7)`, fixture.key.ProjectID, fixture.key.FabricInstanceID,
		fixture.key.StreamID, secondRef, streamTestCommitA, storedTree, string(digest)); err != nil {
		t.Fatalf("insert ambiguous version: %v", err)
	}
	if transition, err := fixture.store.Read(context.Background(), fixture.key, 0); !errors.Is(err, ErrStreamConflict) || !reflect.DeepEqual(transition, StreamTransition{}) {
		t.Fatalf("ambiguous Read = (%+v,%v), want zero and ErrStreamConflict", transition, err)
	}
}

func TestApplyOperationReplayReturnsOriginalResult(t *testing.T) {
	fixture := newStreamFixture(t, "stream-replay")
	initial := fixture.attach()
	input := fixture.applyInput(initial, streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "same\n"))
	first := fixture.apply(input)
	second := fixture.apply(input)
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("replay = %+v, want %+v", second, first)
	}
	if got := countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key); got != 2 {
		t.Fatalf("version rows after replay = %d, want 2", got)
	}
	if got := countStreamRows(t, fixture.db, "fabric_stream_requests", fixture.key); got != 1 {
		t.Fatalf("request rows after replay = %d, want 1", got)
	}

	t.Run("conflict result", func(t *testing.T) {
		conflicted := newStreamFixture(t, "stream-replay-conflict")
		base := conflicted.attach()
		conflicted.apply(conflicted.applyInput(base, streamKBOperation(base.Live, conflicted.scope.Actor, streamTestOperationA, "first\n")))
		input := conflicted.applyInput(base, streamActorOperation(base.Live, conflicted.scope.Actor, streamTestOperationB))
		firstConflict := conflicted.apply(input)
		secondConflict := conflicted.apply(input)
		if firstConflict.ConflictID == "" || !reflect.DeepEqual(secondConflict, firstConflict) {
			t.Fatalf("conflict replay = %+v, want %+v", secondConflict, firstConflict)
		}
		if got := countStreamRows(t, conflicted.db, "fabric_stream_conflicts", conflicted.key); got != 1 {
			t.Fatalf("conflict rows after replay = %d, want 1", got)
		}
	})
}

func TestApplyOperationChangedBodyReplayRejects(t *testing.T) {
	fixture := newStreamFixture(t, "stream-changed-replay")
	initial := fixture.attach()
	fixture.apply(fixture.applyInput(initial, streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "first\n")))
	changed := streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "changed\n")
	tx, err := fixture.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := fixture.store.ApplyOperationInTx(context.Background(), tx, fixture.scope, fixture.applyInput(initial, changed)); !errors.Is(err, ErrOperationReplay) {
		t.Fatalf("changed replay error = %v, want ErrOperationReplay", err)
	}
}

func TestApplyOperationStaleVersionPersistsConflict(t *testing.T) {
	fixture := newStreamFixture(t, "stream-stale")
	initial := fixture.attach()
	fixture.apply(fixture.applyInput(initial, streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "first\n")))
	stale := fixture.apply(fixture.applyInput(initial, streamActorOperation(initial.Live, fixture.scope.Actor, streamTestOperationB)))
	if stale.Version != 1 || stale.ConflictID == "" {
		t.Fatalf("stale result = %+v", stale)
	}
	var kind, state, base, ours, theirs, result string
	var detected, requestVersion int64
	if err := fixture.db.QueryRow(`SELECT c.conflict_kind,c.state,c.base_tree_digest,c.ours_tree_digest,c.theirs_tree_digest,c.detected_at_version,r.result,r.result_stream_version
		FROM fabric_stream_conflicts c JOIN fabric_stream_requests r
		ON r.project_id=c.project_id AND r.fabric_instance_id=c.fabric_instance_id AND r.stream_id=c.stream_id AND r.ref_name=c.canonical_ref
		WHERE c.project_id=$1 AND c.fabric_instance_id=$2 AND c.stream_id=$3 AND c.conflict_id=$4 AND r.operation_id=$5`,
		fixture.key.ProjectID, fixture.key.FabricInstanceID, fixture.key.StreamID, stale.ConflictID, streamTestOperationB).
		Scan(&kind, &state, &base, &ours, &theirs, &detected, &result, &requestVersion); err != nil {
		t.Fatalf("read conflict: %v", err)
	}
	if kind != "operation_precondition" || state != "open" || base != string(initial.Live.Digest) || ours != string(stale.Live.Digest) || theirs != string(initial.Live.Digest) || detected != 1 || result != "conflict" || requestVersion != 1 {
		t.Fatalf("stored conflict = %q %q %q %q %q %d %q %d", kind, state, base, ours, theirs, detected, result, requestVersion)
	}
	if got := countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key); got != 2 {
		t.Fatalf("stale operation created version: rows=%d", got)
	}

	for _, test := range []struct {
		name   string
		mutate func(*ApplyStreamOperationInput)
	}{
		{"expected tree digest", func(input *ApplyStreamOperationInput) { input.ExpectedTreeDigest = streamTestDigest("c") }},
		{"operation view digest", func(input *ApplyStreamOperationInput) { input.Operation.ExpectedViewDigest = streamTestDigest("d") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			mismatched := newStreamFixture(t, "stream-precondition-"+strings.ReplaceAll(test.name, " ", "-"))
			base := mismatched.attach()
			input := mismatched.applyInput(base, streamActorOperation(base.Live, mismatched.scope.Actor, streamTestOperationA))
			test.mutate(&input)
			result := mismatched.apply(input)
			if result.Version != 0 || result.ConflictID == "" || countStreamRows(t, mismatched.db, "fabric_stream_versions", mismatched.key) != 1 {
				t.Fatalf("precondition mismatch result = %+v", result)
			}
		})
	}
}

func TestApplyOperationConcurrentExpectedVersionHasOneConflict(t *testing.T) {
	fixture := newStreamFixture(t, "stream-concurrent")
	initial := fixture.attach()
	inputs := []ApplyStreamOperationInput{
		fixture.applyInput(initial, streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "first\n")),
		fixture.applyInput(initial, streamActorOperation(initial.Live, fixture.scope.Actor, streamTestOperationB)),
	}
	results := make([]StreamTransition, 2)
	errorsSeen := make([]error, 2)
	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	workers.Add(2)
	for index := range inputs {
		go func(index int) {
			defer workers.Done()
			start.Wait()
			tx, err := fixture.db.BeginTx(context.Background(), nil)
			if err != nil {
				errorsSeen[index] = err
				return
			}
			defer tx.Rollback()
			results[index], err = fixture.store.ApplyOperationInTx(context.Background(), tx, fixture.scope, inputs[index])
			if err == nil {
				err = tx.Commit()
			}
			errorsSeen[index] = err
		}(index)
	}
	start.Done()
	workers.Wait()
	for index, err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent operation %d: %v", index, err)
		}
	}
	conflicts := 0
	for _, result := range results {
		if result.ConflictID != "" {
			conflicts++
		}
	}
	if conflicts != 1 || countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key) != 2 || countStreamRows(t, fixture.db, "fabric_stream_conflicts", fixture.key) != 1 {
		t.Fatalf("concurrent results = %+v, want one applied and one conflict", results)
	}
}

func TestApplyOperationRequiresCompleteWorkspaceAndActorScope(t *testing.T) {
	fixture := newStreamFixture(t, "stream-operation-scope")
	initial := fixture.attach()
	operation := streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "body\n")
	tests := []struct {
		name   string
		mutate func(*types.ActorScope, *ApplyStreamOperationInput)
	}{
		{"workspace", func(_ *types.ActorScope, input *ApplyStreamOperationInput) {
			input.WorkspaceID = "33333333-3333-4333-8333-333333333349"
		}},
		{"project", func(scope *types.ActorScope, _ *ApplyStreamOperationInput) {
			scope.ProjectID = "00000000-0000-4000-8000-000000000002"
		}},
		{"actor", func(_ *types.ActorScope, input *ApplyStreamOperationInput) {
			input.Operation.Actor.HumanPrincipalID = "44444444-4444-4444-8444-444444444449"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope, input := fixture.scope, fixture.applyInput(initial, operation)
			test.mutate(&scope, &input)
			tx, err := fixture.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := fixture.store.ApplyOperationInTx(context.Background(), tx, scope, input); err == nil {
				t.Fatal("ApplyOperationInTx unexpectedly accepted incomplete scope")
			}
		})
	}
	if got := countStreamRows(t, fixture.db, "fabric_stream_requests", fixture.key); got != 0 {
		t.Fatalf("scope failures persisted %d requests", got)
	}
}

func TestApplyOperationAndAdvanceLeaveTransactionOwnershipToCaller(t *testing.T) {
	fixture := newStreamFixture(t, "stream-transaction-owner")
	initial := fixture.attach()
	operation := streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "rolled back\n")
	tx, err := fixture.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ApplyOperationInTx(context.Background(), tx, fixture.scope, fixture.applyInput(initial, operation)); err != nil {
		t.Fatalf("ApplyOperationInTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if versions, requests := countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key), countStreamRows(t, fixture.db, "fabric_stream_requests", fixture.key); versions != 1 || requests != 0 {
		t.Fatalf("rolled-back apply left versions=%d requests=%d", versions, requests)
	}

	nextSnapshot, err := projectstate.ApplyOperation(initial.Accepted, streamActorOperation(initial.Accepted, fixture.scope.Actor, streamTestOperationB))
	if err != nil {
		t.Fatal(err)
	}
	nextTree, err := projectstate.EncodeTree(nextSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = fixture.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.AdvanceAcceptedDefaultInTx(context.Background(), tx, fixture.scope, AdvanceAcceptedInput{
		Key:  fixture.key,
		Ref:  RefObservation{Repository: fixture.repository, RefName: streamTestRef, CommitSHA: streamTestCommitB, ObservedAt: fixture.ref.ObservedAt.Add(time.Minute)},
		Tree: nextTree,
	})
	if err != nil {
		t.Fatalf("AdvanceAcceptedDefaultInTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if versions := countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key); versions != 1 {
		t.Fatalf("rolled-back advance left versions=%d", versions)
	}
}

func TestAdvanceAcceptedUsesExactObservedCommit(t *testing.T) {
	fixture := newStreamFixture(t, "stream-advance")
	initial := fixture.attach()
	nextSnapshot, err := projectstate.ApplyOperation(initial.Live, streamActorOperation(initial.Live, fixture.scope.Actor, streamTestOperationA))
	if err != nil {
		t.Fatal(err)
	}
	nextTree, err := projectstate.EncodeTree(nextSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	transition := fixture.advance(AdvanceAcceptedInput{
		Key:  fixture.key,
		Ref:  RefObservation{Repository: fixture.repository, RefName: streamTestRef, CommitSHA: streamTestCommitB, ObservedAt: fixture.ref.ObservedAt.Add(time.Minute)},
		Tree: nextTree,
	})
	if transition.Version != 1 || transition.AcceptedCommitSHA != streamTestCommitB || transition.Live.Digest != transition.Accepted.Digest || transition.ConflictID != "" {
		t.Fatalf("advance transition = %+v", transition)
	}
	stored := readStoredStreamVersion(t, fixture.db, fixture.key, 1)
	if stored.kind != "accepted_ref" || stored.commitSHA != streamTestCommitB || stored.operationID.Valid {
		t.Fatalf("stored accepted transition = %+v", stored)
	}

	for _, test := range []struct {
		name   string
		mutate func(*AdvanceAcceptedInput)
	}{
		{"commit", func(input *AdvanceAcceptedInput) { input.Ref.CommitSHA = "not-a-commit" }},
		{"ref", func(input *AdvanceAcceptedInput) { input.Ref.RefName = "refs/heads/topic" }},
		{"repository", func(input *AdvanceAcceptedInput) { input.Ref.Repository.ImmutableID = "999" }},
	} {
		t.Run("rejects "+test.name+" mismatch", func(t *testing.T) {
			mismatched := newStreamFixture(t, "stream-advance-mismatch-"+test.name)
			base := mismatched.attach()
			input := AdvanceAcceptedInput{
				Key:  mismatched.key,
				Ref:  RefObservation{Repository: mismatched.repository, RefName: streamTestRef, CommitSHA: streamTestCommitB, ObservedAt: mismatched.ref.ObservedAt.Add(time.Minute)},
				Tree: mismatched.tree,
			}
			test.mutate(&input)
			tx, err := mismatched.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := mismatched.store.AdvanceAcceptedDefaultInTx(context.Background(), tx, mismatched.scope, input); err == nil {
				t.Fatal("AdvanceAcceptedDefaultInTx unexpectedly accepted mismatched observation")
			}
			if got := countStreamRows(t, mismatched.db, "fabric_stream_versions", mismatched.key); got != int(base.Version)+1 {
				t.Fatalf("mismatched advance left version rows=%d", got)
			}
		})
	}
}

func TestAdvanceAcceptedDivergencePreservesLiveAndPersistsConflict(t *testing.T) {
	fixture := newStreamFixture(t, "stream-divergence")
	initial := fixture.attach()
	proposed := fixture.apply(fixture.applyInput(initial, streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "proposal\n")))
	priorLiveTree, err := projectstate.EncodeTree(proposed.Live)
	if err != nil {
		t.Fatal(err)
	}
	acceptedSnapshot, err := projectstate.ApplyOperation(initial.Accepted, streamActorOperation(initial.Accepted, fixture.scope.Actor, streamTestOperationB))
	if err != nil {
		t.Fatal(err)
	}
	acceptedTree, err := projectstate.EncodeTree(acceptedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	advanced := fixture.advance(AdvanceAcceptedInput{
		Key:  fixture.key,
		Ref:  RefObservation{Repository: fixture.repository, RefName: streamTestRef, CommitSHA: streamTestCommitB, ObservedAt: fixture.ref.ObservedAt.Add(time.Minute)},
		Tree: acceptedTree,
	})
	if advanced.Version != 2 || advanced.Live.Digest != proposed.Live.Digest || advanced.Accepted.Digest != acceptedSnapshot.Digest || advanced.ConflictID == "" {
		t.Fatalf("divergent advance = %+v", advanced)
	}
	stored := readStoredStreamVersion(t, fixture.db, fixture.key, 2)
	storedLive := decodeAndValidateStoredTree(t, stored.liveTree, stored.liveDigest)
	assertStreamTreesEqual(t, priorLiveTree, storedLive)
	var kind, state, base, ours, theirs string
	if err := fixture.db.QueryRow(`SELECT conflict_kind,state,base_tree_digest,ours_tree_digest,theirs_tree_digest
		FROM fabric_stream_conflicts WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND conflict_id=$4`,
		fixture.key.ProjectID, fixture.key.FabricInstanceID, fixture.key.StreamID, advanced.ConflictID).
		Scan(&kind, &state, &base, &ours, &theirs); err != nil {
		t.Fatalf("read divergence conflict: %v", err)
	}
	if kind != "git_base_diverged" || state != "open" || base != string(initial.Accepted.Digest) || ours != string(proposed.Live.Digest) || theirs != string(acceptedSnapshot.Digest) {
		t.Fatalf("divergence conflict = %q %q %q %q %q", kind, state, base, ours, theirs)
	}
}

func TestReadRejectsCorruptStoredOperation(t *testing.T) {
	for _, corruption := range streamOperationCorruptions() {
		t.Run(corruption.name, func(t *testing.T) {
			fixture := newStreamFixture(t, "stream-corrupt-version-"+strings.ReplaceAll(corruption.name, " ", "-"))
			initial := fixture.attach()
			operation := streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "body\n")
			applied := fixture.apply(fixture.applyInput(initial, operation))
			canonical, _ := projectstate.CanonicalOperation(operation)
			digest, _ := projectstate.DigestCanonicalJSON(operation)
			raw, storedDigest := corruption.mutate(operation, canonical, digest)
			replaceVersionOperationEvidence(t, fixture.db, fixture.key, 1, raw, storedDigest)
			fixture.reopen()

			if transition, err := fixture.store.Read(context.Background(), fixture.key, 1); !errors.Is(err, ErrStreamCorrupt) || !reflect.DeepEqual(transition, StreamTransition{}) {
				t.Fatalf("Read corrupt version = (%+v,%v), want zero and ErrStreamCorrupt", transition, err)
			}
			later := streamActorOperation(applied.Live, fixture.scope.Actor, streamTestOperationB)
			tx, err := fixture.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			input := ApplyStreamOperationInput{Key: fixture.key, WorkspaceID: fixture.workspaceID, ExpectedVersion: 1, ExpectedTreeDigest: applied.Live.Digest, Operation: later}
			if transition, err := fixture.store.ApplyOperationInTx(context.Background(), tx, fixture.scope, input); !errors.Is(err, ErrStreamCorrupt) || !reflect.DeepEqual(transition, StreamTransition{}) {
				t.Fatalf("later apply over corrupt version = (%+v,%v), want zero and ErrStreamCorrupt", transition, err)
			}
			if got := countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key); got != 2 {
				t.Fatalf("corrupt read/apply created version: rows=%d", got)
			}
		})
	}
}

func TestApplyOperationReplayRejectsCorruptStoredRequest(t *testing.T) {
	for _, corruption := range streamOperationCorruptions() {
		t.Run(corruption.name, func(t *testing.T) {
			fixture := newStreamFixture(t, "stream-corrupt-request-"+strings.ReplaceAll(corruption.name, " ", "-"))
			initial := fixture.attach()
			operation := streamKBOperation(initial.Live, fixture.scope.Actor, streamTestOperationA, "body\n")
			input := fixture.applyInput(initial, operation)
			fixture.apply(input)
			canonical, _ := projectstate.CanonicalOperation(operation)
			digest, _ := projectstate.DigestCanonicalJSON(operation)
			raw, storedDigest := corruption.mutate(operation, canonical, digest)
			replaceRequestOperationEvidence(t, fixture.db, fixture.key, fixture.workspaceID, operation.ID, raw, storedDigest)
			fixture.reopen()

			tx, err := fixture.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if transition, err := fixture.store.ApplyOperationInTx(context.Background(), tx, fixture.scope, input); !errors.Is(err, ErrStreamCorrupt) || !reflect.DeepEqual(transition, StreamTransition{}) {
				t.Fatalf("replay corrupt request = (%+v,%v), want zero and ErrStreamCorrupt", transition, err)
			}
			if got := countStreamRows(t, fixture.db, "fabric_stream_versions", fixture.key); got != 2 {
				t.Fatalf("corrupt replay created version: rows=%d", got)
			}
		})
	}
}

type operationCorruption struct {
	name   string
	mutate func(projectstate.OperationV1, []byte, projectstate.Digest) ([]byte, projectstate.Digest)
}

func streamOperationCorruptions() []operationCorruption {
	return []operationCorruption{
		{"malformed operation JSON", func(_ projectstate.OperationV1, _ []byte, digest projectstate.Digest) ([]byte, projectstate.Digest) {
			return []byte("{"), digest
		}},
		{"unknown field", func(_ projectstate.OperationV1, canonical []byte, digest projectstate.Digest) ([]byte, projectstate.Digest) {
			return append(bytes.Clone(canonical[:len(canonical)-2]), []byte(",\"unknown\":true}\n")...), digest
		}},
		{"trailing JSON", func(_ projectstate.OperationV1, canonical []byte, digest projectstate.Digest) ([]byte, projectstate.Digest) {
			return append(bytes.Clone(canonical), []byte("{}\n")...), digest
		}},
		{"noncanonical bytes", func(_ projectstate.OperationV1, canonical []byte, digest projectstate.Digest) ([]byte, projectstate.Digest) {
			return append([]byte(" "), canonical...), digest
		}},
		{"decoded operation ID differs from row", func(operation projectstate.OperationV1, _ []byte, _ projectstate.Digest) ([]byte, projectstate.Digest) {
			operation.ID = "66666666-6666-4666-8666-666666666669"
			canonical, _ := projectstate.CanonicalOperation(operation)
			digest, _ := projectstate.DigestCanonicalJSON(operation)
			return canonical, digest
		}},
		{"stored digest differs", func(_ projectstate.OperationV1, canonical []byte, _ projectstate.Digest) ([]byte, projectstate.Digest) {
			return canonical, streamTestDigest("b")
		}},
	}
}

func newStreamFixture(t *testing.T, name string) *streamFixture {
	t.Helper()
	db := migration21DB(t)
	requireMigration21(t, db)
	projectID := migration21CreateProject(t, db, name)
	t.Cleanup(func() {
		cleanupDB, err := sql.Open("postgres", types.LoadConfig().DatabaseURL)
		if err == nil {
			_, _ = cleanupDB.Exec(`DELETE FROM projects WHERE id=$1`, projectID)
			_ = cleanupDB.Close()
		}
	})
	repository := streamTestRepositoryFor(projectID)
	_, err := db.Exec(`INSERT INTO project_repository_bindings
		(project_id,fabric_instance_id,provider,provider_repository_id,canonical_remote,default_ref,visibility)
		VALUES($1,$2,$3,$4,$5,$6,'public')`, projectID, streamTestFabricID, repository.Provider,
		repository.ImmutableID, repository.CanonicalRemote, streamTestRef)
	if err != nil {
		t.Fatalf("seed repository binding: %v", err)
	}
	actor := types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: streamTestActorID,
		Assurance:  types.AssurancePublicKeyContinuity,
		OccurredAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
	}
	fixture := &streamFixture{
		t: t, db: db, store: &StreamStore{db: db},
		key:         StreamKey{ProjectID: projectID, FabricInstanceID: streamTestFabricID, StreamID: streamTestStreamID},
		workspaceID: streamTestWorkspaceID,
		repository:  repository,
		ref: RefObservation{Repository: repository, RefName: streamTestRef, CommitSHA: streamTestCommitA,
			ObservedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)},
		scope: types.ActorScope{Actor: actor, ProjectID: projectID},
	}
	fixture.tree = streamTestTree(t, projectID, repository)
	return fixture
}

func streamTestRepository() types.RepositoryIdentity {
	return types.RepositoryIdentity{Provider: "github", ImmutableID: "123456789", CanonicalRemote: "https://github.com/wormhole/test"}
}

func streamTestRepositoryFor(projectID string) types.RepositoryIdentity {
	return types.RepositoryIdentity{
		Provider: "github", ImmutableID: strings.ReplaceAll(streamTestFabricID, "-", ""),
		CanonicalRemote: "https://github.com/wormhole/" + projectID,
	}
}

func streamTestTree(t *testing.T, projectID string, repository types.RepositoryIdentity) projectstate.Tree {
	t.Helper()
	createdAt := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	actor := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: streamTestActorID, ActorKind: types.ActorHuman,
		DisplayName: "Harley", PublicKeys: []projectstate.PublicKeyV1{}, Extensions: projectstate.ExtensionsV1{},
	}
	snapshot := projectstate.Snapshot{
		Config:  projectstate.ConfigV1{SnapshotVersion: 1, ProjectID: projectID, Handle: types.ProjectHandle{Namespace: "wormhole", Name: "stream"}, Repository: repository},
		Project: projectstate.ProjectV1{SchemaVersion: 1, Kind: "project", ID: projectID, Name: "Stream Test", Aliases: []string{}, CreatedAt: createdAt, UpdatedAt: createdAt, Extensions: projectstate.ExtensionsV1{}},
		Actors:  map[string]projectstate.Record[projectstate.ActorV1]{streamTestActorID: {Value: &actor}},
		Tasks:   map[string]projectstate.Record[projectstate.TaskV1]{}, TaskLinks: map[string]projectstate.Record[projectstate.TaskLinkV1]{},
		Articles: map[string]projectstate.KBRecord{}, Channels: map[string]projectstate.Record[projectstate.ChannelV1]{},
		Events: map[string]projectstate.EventV1{}, GitLinks: map[string]projectstate.Record[projectstate.GitLinkV1]{},
	}
	tree, err := projectstate.EncodeTree(snapshot)
	if err != nil {
		t.Fatalf("encode stream test tree: %v", err)
	}
	return tree
}

func streamKBOperation(snapshot projectstate.Snapshot, actor types.ActorEnvelope, operationID, body string) projectstate.OperationV1 {
	createdAt := actor.OccurredAt
	return projectstate.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: projectstate.OperationPutKBArticle,
		ExpectedViewDigest: snapshot.Digest, Actor: actor,
		PutKBArticle: &projectstate.PutKBArticleV1{
			Record: projectstate.KBArticleV1{
				SchemaVersion: 1, Kind: "kb_article", ID: streamTestArticleID, Title: "Portable stream",
				Frontmatter: map[string]json.RawMessage{}, AuthorActorID: streamTestActorID, RelatedArticleIDs: []string{},
				CreatedAt: createdAt, UpdatedAt: createdAt, Extensions: projectstate.ExtensionsV1{},
			},
			Body: body,
		},
	}
}

func streamActorOperation(snapshot projectstate.Snapshot, actor types.ActorEnvelope, operationID string) projectstate.OperationV1 {
	record := projectstate.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: "77777777-7777-4777-8777-777777777771",
		ActorKind: types.ActorAgent, DisplayName: "Builder", PublicKeys: []projectstate.PublicKeyV1{}, Extensions: projectstate.ExtensionsV1{},
	}
	return projectstate.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: projectstate.OperationPutRecord,
		ExpectedViewDigest: snapshot.Digest, Actor: actor,
		PutRecord: &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{Actor: &record}},
	}
}

func (f *streamFixture) attachInput() AttachStreamInput {
	return AttachStreamInput{Key: f.key, WorkspaceID: f.workspaceID, Repository: f.repository, Ref: f.ref, Tree: f.tree, Writable: true}
}

func (f *streamFixture) attach() StreamTransition {
	f.t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	transition, err := f.store.AttachInTx(context.Background(), tx, f.scope, f.attachInput())
	if err != nil {
		f.t.Fatalf("AttachInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatalf("commit attach: %v", err)
	}
	return transition
}

func (f *streamFixture) applyInput(base StreamTransition, operation projectstate.OperationV1) ApplyStreamOperationInput {
	return ApplyStreamOperationInput{Key: f.key, WorkspaceID: f.workspaceID, ExpectedVersion: base.Version, ExpectedTreeDigest: base.Live.Digest, Operation: operation}
}

func (f *streamFixture) apply(input ApplyStreamOperationInput) StreamTransition {
	f.t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	transition, err := f.store.ApplyOperationInTx(context.Background(), tx, f.scope, input)
	if err != nil {
		f.t.Fatalf("ApplyOperationInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatalf("commit apply: %v", err)
	}
	return transition
}

func (f *streamFixture) advance(input AdvanceAcceptedInput) StreamTransition {
	f.t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	transition, err := f.store.AdvanceAcceptedDefaultInTx(context.Background(), tx, f.scope, input)
	if err != nil {
		f.t.Fatalf("AdvanceAcceptedDefaultInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatalf("commit advance: %v", err)
	}
	return transition
}

func (f *streamFixture) reopen() {
	f.t.Helper()
	_ = f.db.Close()
	db, err := sql.Open("postgres", types.LoadConfig().DatabaseURL)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = db.Close() })
	f.db = db
	f.store = &StreamStore{db: db}
}

type storedStreamVersion struct {
	kind                       string
	commitSHA                  string
	liveTree, acceptedTree     []byte
	liveDigest, acceptedDigest string
	operationID                sql.NullString
	operationJSON, actorJSON   []byte
	operationDigest            sql.NullString
}

func readStoredStreamVersion(t *testing.T, db *sql.DB, key StreamKey, version int64) storedStreamVersion {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, key.ProjectID); err != nil {
		t.Fatal(err)
	}
	var stored storedStreamVersion
	err = tx.QueryRow(`SELECT transition_kind,accepted_commit_sha,canonical_live_tree,live_tree_digest,
		canonical_accepted_tree,accepted_tree_digest,operation_id,canonical_operation_json,operation_digest,actor_envelope_json
		FROM fabric_stream_versions WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND version=$4`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, version).
		Scan(&stored.kind, &stored.commitSHA, &stored.liveTree, &stored.liveDigest, &stored.acceptedTree, &stored.acceptedDigest,
			&stored.operationID, &stored.operationJSON, &stored.operationDigest, &stored.actorJSON)
	if err != nil {
		t.Fatalf("read stored version %d: %v", version, err)
	}
	return stored
}

func decodeAndValidateStoredTree(t *testing.T, raw []byte, storedDigest string) projectstate.Tree {
	t.Helper()
	tree, err := DecodeStoredTree(raw)
	if err != nil {
		t.Fatalf("DecodeStoredTree: %v", err)
	}
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		t.Fatalf("DecodeTree: %v", err)
	}
	if err := projectstate.Validate(snapshot); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	digest, err := projectstate.DigestTree(tree)
	if err != nil {
		t.Fatalf("DigestTree: %v", err)
	}
	if digest != projectstate.Digest(storedDigest) || snapshot.Digest != digest {
		t.Fatalf("stored tree digest = %q snapshot=%q, want %q", digest, snapshot.Digest, storedDigest)
	}
	return tree
}

func countStreamRows(t *testing.T, db *sql.DB, table string, key StreamKey) int {
	t.Helper()
	allowed := map[string]bool{"fabric_streams": true, "fabric_stream_versions": true, "fabric_stream_requests": true, "fabric_stream_conflicts": true}
	if !allowed[table] {
		t.Fatalf("unsupported table %q", table)
	}
	var count int
	query := fmt.Sprintf(`SELECT count(*) FROM %s WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3`, table)
	if err := db.QueryRow(query, key.ProjectID, key.FabricInstanceID, key.StreamID).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func replaceVersionOperationEvidence(t *testing.T, db *sql.DB, key StreamKey, version int64, operationJSON []byte, digest projectstate.Digest) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, key.ProjectID); err != nil {
		t.Fatal(err)
	}
	stored := readStoredStreamVersion(t, db, key, version)
	if _, err := tx.Exec(`DELETE FROM fabric_stream_versions WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND version=$4`, key.ProjectID, key.FabricInstanceID, key.StreamID, version); err != nil {
		t.Fatalf("delete version for corruption: %v", err)
	}
	_, err = tx.Exec(`INSERT INTO fabric_stream_versions
		(project_id,fabric_instance_id,stream_id,canonical_ref,version,transition_kind,accepted_commit_sha,
		 canonical_live_tree,live_tree_digest,canonical_accepted_tree,accepted_tree_digest,
		 operation_id,canonical_operation_json,operation_digest,actor_envelope_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, streamTestRef, version, stored.kind, stored.commitSHA,
		stored.liveTree, stored.liveDigest, stored.acceptedTree, stored.acceptedDigest,
		stored.operationID, operationJSON, string(digest), stored.actorJSON)
	if err != nil {
		t.Fatalf("insert corrupt version: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func replaceRequestOperationEvidence(t *testing.T, db *sql.DB, key StreamKey, workspaceID, operationID string, operationJSON []byte, digest projectstate.Digest) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, key.ProjectID); err != nil {
		t.Fatal(err)
	}
	var expectedVersion, resultVersion int64
	var expectedDigest, result string
	var actorJSON, conflictJSON []byte
	err = tx.QueryRow(`DELETE FROM fabric_stream_requests
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND ref_name=$4 AND operation_id=$5
		RETURNING expected_stream_version,expected_tree_digest,result,result_stream_version,actor_envelope_json,conflict_json`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, streamTestRef, operationID).
		Scan(&expectedVersion, &expectedDigest, &result, &resultVersion, &actorJSON, &conflictJSON)
	if err != nil {
		t.Fatalf("delete request for corruption: %v", err)
	}
	_, err = tx.Exec(`INSERT INTO fabric_stream_requests
		(project_id,fabric_instance_id,stream_id,workspace_id,ref_name,operation_id,canonical_operation_json,
		 operation_digest,expected_stream_version,expected_tree_digest,result,result_stream_version,actor_envelope_json,conflict_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		key.ProjectID, key.FabricInstanceID, key.StreamID, workspaceID, streamTestRef, operationID, operationJSON,
		string(digest), expectedVersion, expectedDigest, result, resultVersion, actorJSON, nullableJSON(conflictJSON))
	if err != nil {
		t.Fatalf("insert corrupt request: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func nullableJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
