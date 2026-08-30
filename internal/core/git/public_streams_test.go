package git

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
	"github.com/google/uuid"
)

func beginPublicRuntimeTx(db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`SET LOCAL ROLE wormhole_fabric_runtime`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func TestAdvanceAcceptedObservedRefSupportsExactNonDefaultBranch(t *testing.T) {
	f := newStreamFixture(t, "public-observed-non-default")
	f.key.StreamID, f.workspaceID, f.ref.RefName = uuid.NewString(), uuid.NewString(), "refs/heads/topic"
	initial := f.attach()
	input := f.advanceInput(initial, streamTestCommitB, f.ref.ObservedAt.Add(time.Minute), f.tree)
	input.Ref.RefName = f.ref.RefName
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := f.store.AdvanceAcceptedObservedRefInTx(context.Background(), tx, f.scope, input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 {
		t.Fatalf("transition=%+v", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveConflictExactReplayChangedBytesAndRace(t *testing.T) {
	f := newStreamFixture(t, "public-conflict-resolution")
	_, claimed := publicAttach(t, f, "sha256:"+strings.Repeat("9", 64))
	initial := claimed.State
	local := f.apply(f.applyInput(initial, streamKBOperation(initial.Live, f.scope.Actor, streamTestOperationA, "local\n")))
	accepted, err := projectstate.ApplyOperation(initial.Accepted, streamActorOperation(initial.Accepted, f.scope.Actor, streamTestOperationB))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := projectstate.EncodeTree(accepted)
	if err != nil {
		t.Fatal(err)
	}
	advance := f.advanceInput(local, streamTestCommitB, f.ref.ObservedAt.Add(time.Minute), tree)
	advance.ExpectedAcceptedCommitSHA, advance.ExpectedAcceptedTreeDigest = initial.AcceptedCommitSHA, initial.Accepted.Digest
	diverged := f.advance(advance)
	attachment := claimed.Attachment
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := f.store.OpenConflictIDsInTx(context.Background(), tx, attachment)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{diverged.ConflictID}) {
		t.Fatalf("ids=%v", ids)
	}
	resolution := streamKBOperation(diverged.Live, f.scope.Actor, uuid.NewString(), "resolved\n")
	in := ResolveStreamConflictInput{Attachment: attachment, ConflictID: diverged.ConflictID, Precondition: SyncPrecondition{Repository: f.repository, CanonicalRef: f.ref.RefName, BaseCommitSHA: diverged.AcceptedCommitSHA, BaseTreeDigest: diverged.Accepted.Digest, ExpectedStreamVersion: diverged.Version, ExpectedLiveTreeDigest: diverged.Live.Digest}, Resolution: resolution}
	resolve := func(input ResolveStreamConflictInput) (StreamTransition, error) {
		tx, e := beginPublicRuntimeTx(f.db)
		if e != nil {
			return StreamTransition{}, e
		}
		defer tx.Rollback()
		got, e := f.store.ResolveConflictInTx(context.Background(), tx, f.scope, input)
		if e == nil {
			e = tx.Commit()
		}
		return got, e
	}
	first, err := resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := resolve(in)
	if err != nil || replay.Version != first.Version {
		t.Fatalf("replay=(%+v,%v)", replay, err)
	}
	changed := in
	changed.Resolution.PutKBArticle.Body = "changed\n"
	if _, err := resolve(changed); !errors.Is(err, ErrOperationReplay) {
		t.Fatalf("changed error=%v", err)
	}

	// A second conflict exercises the real two-transaction race. Both callers
	// must converge on one recorded operation/version rather than create a
	// second resolution transition.
	secondLocal := f.apply(f.applyInput(first, streamKBOperation(first.Live, f.scope.Actor, uuid.NewString(), "race local\n")))
	acceptedTree, err := projectstate.EncodeTree(first.Accepted)
	if err != nil {
		t.Fatal(err)
	}
	secondAdvance := f.advanceInput(secondLocal, streamTestCommitC, f.ref.ObservedAt.Add(2*time.Minute), acceptedTree)
	secondAdvance.ExpectedAcceptedCommitSHA = first.AcceptedCommitSHA
	secondAdvance.ExpectedAcceptedTreeDigest = first.Accepted.Digest
	secondConflict := f.advance(secondAdvance)
	raceInput := ResolveStreamConflictInput{
		Attachment: attachment,
		ConflictID: secondConflict.ConflictID,
		Precondition: SyncPrecondition{
			Repository: f.repository, CanonicalRef: f.ref.RefName,
			BaseCommitSHA: secondConflict.AcceptedCommitSHA, BaseTreeDigest: secondConflict.Accepted.Digest,
			ExpectedStreamVersion: secondConflict.Version, ExpectedLiveTreeDigest: secondConflict.Live.Digest,
		},
		Resolution: streamKBOperation(secondConflict.Live, f.scope.Actor, uuid.NewString(), "race resolved\n"),
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make([]StreamTransition, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			tx, txErr := beginPublicRuntimeTx(f.db)
			if txErr != nil {
				errs[index] = txErr
				return
			}
			defer tx.Rollback()
			started <- struct{}{}
			<-release
			results[index], errs[index] = f.store.ResolveConflictInTx(context.Background(), tx, f.scope, raceInput)
			if errs[index] == nil {
				errs[index] = tx.Commit()
			}
		}(i)
	}
	<-started
	<-started
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || results[0].Version != results[1].Version {
		t.Fatalf("race results=(%+v,%v) (%+v,%v)", results[0], errs[0], results[1], errs[1])
	}
	var resolutionCount int
	if err := f.db.QueryRow(`SELECT count(*) FROM fabric_stream_conflicts WHERE project_id=$1 AND conflict_id=$2 AND state='resolved' AND resolution_operation_id=$3 AND resolution_version=$4`, f.key.ProjectID, secondConflict.ConflictID, raceInput.Resolution.ID, results[0].Version).Scan(&resolutionCount); err != nil || resolutionCount != 1 {
		t.Fatalf("resolution evidence count=%d err=%v", resolutionCount, err)
	}
}

func TestResolveConflictRejectsHistoricalOperationFromAnotherConflict(t *testing.T) {
	f := newStreamFixture(t, "public-conflict-historical-replay")
	_, claimed := publicAttach(t, f, "sha256:"+strings.Repeat("8", 64))
	initial := claimed.State
	operationA := streamKBOperation(initial.Live, f.scope.Actor, uuid.NewString(), "historical\n")
	appliedA := f.apply(f.applyInput(initial, operationA))
	accepted, err := projectstate.ApplyOperation(appliedA.Accepted, streamActorOperation(appliedA.Accepted, f.scope.Actor, uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := projectstate.EncodeTree(accepted)
	if err != nil {
		t.Fatal(err)
	}
	advanceInput := f.advanceInput(appliedA, streamTestCommitB, f.ref.ObservedAt.Add(time.Minute), tree)
	advanceInput.ExpectedAcceptedCommitSHA, advanceInput.ExpectedAcceptedTreeDigest = appliedA.AcceptedCommitSHA, appliedA.Accepted.Digest
	conflict := f.advance(advanceInput)
	if conflict.ConflictID == "" {
		t.Fatal("expected open conflict")
	}
	input := ResolveStreamConflictInput{Attachment: claimed.Attachment, ConflictID: conflict.ConflictID,
		Precondition: publicPrecondition(claimed.Attachment, initial), Resolution: operationA}
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	before := publicRouteBytes(t, tx, claimed.Attachment.Key)
	_, err = f.store.ResolveConflictInTx(context.Background(), tx, f.scope, input)
	if !errors.Is(err, ErrOperationReplay) {
		t.Fatalf("historical resolution error=%v", err)
	}
	if after := publicRouteBytes(t, tx, claimed.Attachment.Key); after != before {
		t.Fatal("historical resolution changed route")
	}
	_ = tx.Rollback()
	var state string
	if err := f.db.QueryRow(`SELECT state FROM fabric_stream_conflicts WHERE project_id=$1 AND conflict_id=$2`, f.key.ProjectID, conflict.ConflictID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("conflict state=%s", state)
	}
	fresh := streamKBOperation(conflict.Live, f.scope.Actor, uuid.NewString(), "fresh resolution\n")
	freshInput := ResolveStreamConflictInput{Attachment: claimed.Attachment, ConflictID: conflict.ConflictID,
		Precondition: publicPrecondition(claimed.Attachment, conflict), Resolution: fresh}
	tx, err = beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.store.ResolveConflictInTx(context.Background(), tx, f.scope, freshInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.store.ResolveConflictInTx(context.Background(), tx, f.scope, freshInput)
	if err != nil || replayed.Version != resolved.Version || replayed.Live.Digest != resolved.Live.Digest {
		t.Fatalf("fresh resolution replay=(%+v,%v), first=%+v", replayed, err, resolved)
	}
	_ = tx.Rollback()
}

func TestResolveConflictInTxClassifiesMissingDurableConflict(t *testing.T) {
	f := newStreamFixture(t, "public-conflict-missing")
	_, attached := publicAttach(t, f, "sha256:"+strings.Repeat("c", 64))
	resolution := streamKBOperation(attached.State.Live, f.scope.Actor, uuid.NewString(), "missing conflict\n")
	input := ResolveStreamConflictInput{
		Attachment:   attached.Attachment,
		ConflictID:   uuid.NewString(),
		Precondition: publicPrecondition(attached.Attachment, attached.State),
		Resolution:   resolution,
	}
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before := publicRouteBytes(t, tx, attached.Attachment.Key)
	got, err := f.store.ResolveConflictInTx(context.Background(), tx, f.scope, input)
	if !errors.Is(err, ErrStreamConflict) || !reflect.DeepEqual(got, StreamTransition{}) {
		t.Fatalf("missing durable conflict = (%+v,%v), want ErrStreamConflict", got, err)
	}
	if after := publicRouteBytes(t, tx, attached.Attachment.Key); after != before {
		t.Fatalf("missing durable conflict changed route rows\nbefore=%s\nafter=%s", before, after)
	}
}

func TestResolveConflictInTxRejectsReachableTypedNestedConflict(t *testing.T) {
	f := newStreamFixture(t, "public-conflict-nested")
	_, attached := publicAttach(t, f, "sha256:"+strings.Repeat("d", 64))
	originalOperation := projectstate.OperationV1{
		SchemaVersion: 1, ID: uuid.NewString(), Kind: projectstate.OperationTombstone,
		ExpectedViewDigest: attached.State.Live.Digest, Actor: f.scope.Actor,
		Tombstone: &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "actor", ID: streamTestActorID},
			ExpectedContentDigest: projectstate.Digest("sha256:" + strings.Repeat("f", 64)),
		},
	}
	original := applyPublic(t, f, attached.Attachment, publicPrecondition(attached.Attachment, attached.State), originalOperation)
	if original.ConflictID == "" || original.Version != attached.State.Version {
		t.Fatalf("typed original conflict = %+v", original)
	}

	resolution := originalOperation
	resolution.ID = uuid.NewString()
	resolutionTombstone := *originalOperation.Tombstone
	resolutionTombstone.ExpectedContentDigest = projectstate.Digest("sha256:" + strings.Repeat("e", 64))
	resolution.Tombstone = &resolutionTombstone
	input := ResolveStreamConflictInput{
		Attachment:   attached.Attachment,
		ConflictID:   original.ConflictID,
		Precondition: publicPrecondition(attached.Attachment, attached.State),
		Resolution:   resolution,
	}
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	before := publicRouteBytes(t, tx, attached.Attachment.Key)
	got, err := f.store.ResolveConflictInTx(context.Background(), tx, f.scope, input)
	if !errors.Is(err, ErrStreamPrecondition) || !reflect.DeepEqual(got, StreamTransition{}) {
		_ = tx.Rollback()
		t.Fatalf("nested typed conflict = (%+v,%v), want ErrStreamPrecondition", got, err)
	}
	var conflictRows, requestRows, originalOpen, nestedRequest int
	if err := tx.QueryRow(`SELECT
		(SELECT count(*) FROM fabric_stream_conflicts WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		(SELECT count(*) FROM fabric_stream_requests WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		(SELECT count(*) FROM fabric_stream_conflicts WHERE project_id=$1 AND conflict_id=$4 AND state='open'),
		(SELECT count(*) FROM fabric_stream_requests WHERE project_id=$1 AND operation_id=$5)`,
		attached.Attachment.Key.ProjectID, attached.Attachment.Key.FabricInstanceID, attached.Attachment.Key.StreamID,
		original.ConflictID, resolution.ID).Scan(&conflictRows, &requestRows, &originalOpen, &nestedRequest); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if conflictRows != 2 || requestRows != 2 || originalOpen != 1 || nestedRequest != 1 {
		_ = tx.Rollback()
		t.Fatalf("in-transaction nested evidence conflicts=%d requests=%d original_open=%d nested_request=%d, want 2/2/1/1", conflictRows, requestRows, originalOpen, nestedRequest)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	verifyTx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyTx.Rollback()
	if after := publicRouteBytes(t, verifyTx, attached.Attachment.Key); after != before {
		t.Fatalf("nested conflict rollback changed externally visible rows\nbefore=%s\nafter=%s", before, after)
	}
	if err := verifyTx.QueryRow(`SELECT
		(SELECT count(*) FROM fabric_stream_conflicts WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		(SELECT count(*) FROM fabric_stream_requests WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		(SELECT count(*) FROM fabric_stream_requests WHERE project_id=$1 AND operation_id=$4)`,
		attached.Attachment.Key.ProjectID, attached.Attachment.Key.FabricInstanceID, attached.Attachment.Key.StreamID,
		resolution.ID).Scan(&conflictRows, &requestRows, &nestedRequest); err != nil {
		t.Fatal(err)
	}
	if conflictRows != 1 || requestRows != 1 || nestedRequest != 0 {
		t.Fatalf("post-rollback nested evidence conflicts=%d requests=%d nested_request=%d, want 1/1/0", conflictRows, requestRows, nestedRequest)
	}
}

func publicAttach(t *testing.T, f *streamFixture, issuer string) (PublicAttachDraft, PublicAttachResult) {
	t.Helper()
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := f.store.BeginPublicAttachInTx(context.Background(), tx, f.scope, PublicAttachInput{
		ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID,
		Repository: f.repository, Ref: f.ref, Tree: f.tree,
	})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fabric_public_actor_keys
		(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,source_version,activated_at,harness_name,harness_version,model_name,model_version)
		VALUES($1,$2,$3,$4,$5,$6,'human',$7,$8,now(),'','','','')`, f.key.ProjectID, f.key.FabricInstanceID, draft.Key.StreamID, draft.CanonicalRef, issuer, []byte(strings.Repeat("k", 32)), streamTestActorID, draft.SourceVersion); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	state, err := f.store.ClaimPublicAttachInTx(context.Background(), tx, draft, issuer)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.key = draft.Key
	f.workspaceID = draft.WorkspaceID
	return draft, state
}

func TestPublicAttachReturnsGeneratedCompleteAttachment(t *testing.T) {
	f := newStreamFixture(t, "public-attach-generated")
	draft, state := publicAttach(t, f, "sha256:"+strings.Repeat("a", 64))
	if draft.AttachmentRef == "" || draft.ActivitySourceRef == "" || draft.WorkspaceID == "" || draft.Key.StreamID == "" {
		t.Fatal("draft IDs were not generated")
	}
	if draft.SourceVersion != draft.State.Version || draft.State.Key != draft.Key || draft.State.AcceptedCommitSHA != f.ref.CommitSHA || draft.State.Live.Digest == "" {
		t.Fatalf("draft state/source evidence = %+v", draft)
	}
	if state.Attachment.IssuerKeyFingerprint != "sha256:"+strings.Repeat("a", 64) || !state.Attachment.Writable {
		t.Fatalf("claimed attachment = %+v", state.Attachment)
	}
	var sourceVersion interface{}
	if err := f.db.QueryRow(`SELECT source_version FROM fabric_workspace_stream_bindings WHERE attachment_ref=$1`, draft.AttachmentRef).Scan(&sourceVersion); err != nil {
		t.Fatal(err)
	}
	if sourceVersion == nil {
		t.Fatal("claim did not initialize source_version")
	}
}

func TestPublicAttachExactReplayUsesSourceVersionEvidence(t *testing.T) {
	f := newStreamFixture(t, "public-attach-replay")
	issuer := "sha256:" + strings.Repeat("b", 64)
	draft, first := publicAttach(t, f, issuer)
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ReplayPublicAttachInTx(context.Background(), tx, PublicAttachReplayInput{ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID, Repository: f.repository, Ref: f.ref, Tree: f.tree, IssuerKeyFingerprint: issuer})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	tx.Commit()
	if got.Attachment.AttachmentRef != draft.AttachmentRef || got.State.Version != first.State.Version {
		t.Fatalf("replay = %+v", got)
	}
	// Replay is bound to the attachment's source version even after the current
	// stream advances.
	advanced := f.advance(f.advanceInput(first.State, streamTestCommitB, f.ref.ObservedAt.Add(time.Minute), f.tree))
	tx, _ = beginPublicRuntimeTx(f.db)
	got, err = f.store.ReplayPublicAttachInTx(context.Background(), tx, PublicAttachReplayInput{ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID, Repository: f.repository, Ref: f.ref, Tree: f.tree, IssuerKeyFingerprint: issuer})
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	if err != nil || got.State.Version != advanced.Version || got.Attachment.SourceVersion != draft.SourceVersion {
		t.Fatalf("historical replay after advance=(%+v,%v), current=%+v", got, err, advanced)
	}
	changed := f.ref
	changed.CommitSHA = streamTestCommitB
	tx, _ = beginPublicRuntimeTx(f.db)
	_, err = f.store.ReplayPublicAttachInTx(context.Background(), tx, PublicAttachReplayInput{ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID, Repository: f.repository, Ref: changed, Tree: f.tree, IssuerKeyFingerprint: issuer})
	tx.Rollback()
	if !errors.Is(err, ErrPublicAttachReplay) {
		t.Fatalf("changed replay error = %v", err)
	}
	tx, _ = beginPublicRuntimeTx(f.db)
	_, err = f.store.ResolvePublicAttachmentByIssuerInTx(context.Background(), tx, PublicAttachIssuerLookup{ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID, CanonicalRef: f.ref.RefName, IssuerKeyFingerprint: "sha256:" + strings.Repeat("c", 64)})
	tx.Rollback()
	if !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("wrong issuer error = %v", err)
	}
}

func TestPublicAttachConcurrentClaimsHaveOneWinnerAndNoOrphanDraft(t *testing.T) {
	f := newStreamFixture(t, "public-attach-concurrent")
	tx, _ := beginPublicRuntimeTx(f.db)
	draft, err := f.store.BeginPublicAttachInTx(context.Background(), tx, f.scope, PublicAttachInput{ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID, Repository: f.repository, Ref: f.ref, Tree: f.tree})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	tx.Commit()
	issuer := "sha256:" + strings.Repeat("d", 64)
	if _, err := f.db.Exec(`INSERT INTO fabric_public_actor_keys
		(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,source_version,activated_at,harness_name,harness_version,model_name,model_version)
		VALUES($1,$2,$3,$4,$5,$6,'human',$7,0,now(),'','','','')`, f.key.ProjectID, f.key.FabricInstanceID, draft.Key.StreamID, draft.CanonicalRef, issuer, []byte(strings.Repeat("k", 32)), streamTestActorID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			tx, e := beginPublicRuntimeTx(f.db)
			if e == nil {
				started <- struct{}{}
				<-release
				_, e = f.store.ClaimPublicAttachInTx(context.Background(), tx, draft, issuer)
				if e == nil {
					e = tx.Commit()
				} else {
					tx.Rollback()
				}
			}
			errs[i] = e
		}(i)
	}
	<-started
	<-started
	close(release)
	wg.Wait()
	winners := 0
	for _, e := range errs {
		if e == nil {
			winners++
		} else if !errors.Is(e, ErrPublicAttachClaimConflict) {
			t.Fatalf("claim error = %v", e)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, errors = %v", winners, errs)
	}
	var bindings int
	if err := f.db.QueryRow(`SELECT count(*) FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND workspace_id=$4`, draft.Key.ProjectID, draft.Key.FabricInstanceID, draft.Key.StreamID, draft.WorkspaceID).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("binding count=%d err=%v", bindings, err)
	}
}

func TestPublicAttachDraftActivationClaimOrderSatisfiesImmediateForeignKeys(t *testing.T) {
	f := newStreamFixture(t, "public-attach-fk-order")
	draft, result := publicAttach(t, f, "sha256:"+strings.Repeat("1", 64))
	if result.Attachment.SourceVersion != draft.State.Version || result.State.Version != draft.State.Version {
		t.Fatalf("source/state versions draft=%+v result=%+v", draft, result)
	}
	var keyRows, bindingRows int
	if err := f.db.QueryRow(`SELECT
		(SELECT count(*) FROM fabric_public_actor_keys WHERE project_id=$1 AND stream_id=$2 AND source_version=$3),
		(SELECT count(*) FROM fabric_workspace_stream_bindings WHERE project_id=$1 AND attachment_ref=$4 AND source_version=$3 AND public_issuer_key_fingerprint=$5)`,
		draft.Key.ProjectID, draft.Key.StreamID, draft.SourceVersion, draft.AttachmentRef, result.Attachment.IssuerKeyFingerprint).Scan(&keyRows, &bindingRows); err != nil {
		t.Fatal(err)
	}
	if keyRows != 1 || bindingRows != 1 {
		t.Fatalf("activation/binding rows=%d/%d", keyRows, bindingRows)
	}
}

func TestPublicAttachSameHumanKeyReusesAndDifferentHumanGetsDistinctWorkspace(t *testing.T) {
	f := newStreamFixture(t, "public-attach-human-routing")
	firstIssuer := "sha256:" + strings.Repeat("2", 64)
	_, first := publicAttach(t, f, firstIssuer)
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := f.store.ResolvePublicAttachmentByIssuerInTx(context.Background(), tx, PublicAttachIssuerLookup{ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID, Repository: f.repository, CanonicalRef: f.ref.RefName, IssuerKeyFingerprint: firstIssuer})
	_ = tx.Rollback()
	if err != nil || reused.Attachment != first.Attachment {
		t.Fatalf("same key reuse=(%+v,%v), want %+v", reused, err, first.Attachment)
	}

	secondHuman := uuid.NewString()
	secondScope := f.scope
	secondScope.Actor.HumanPrincipalID = secondHuman
	secondIssuer := "sha256:" + strings.Repeat("3", 64)
	tx, err = beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := f.store.BeginPublicAttachInTx(context.Background(), tx, secondScope, PublicAttachInput{ProjectID: f.key.ProjectID, FabricInstanceID: f.key.FabricInstanceID, Repository: f.repository, Ref: f.ref, Tree: f.tree})
	if err == nil {
		_, err = tx.Exec(`INSERT INTO fabric_public_actor_keys
			(project_id,fabric_instance_id,stream_id,canonical_ref,key_fingerprint,public_key,actor_kind,human_principal_id,source_version,activated_at,harness_name,harness_version,model_name,model_version)
			VALUES($1,$2,$3,$4,$5,$6,'human',$7,$8,now(),'','','','')`, draft.Key.ProjectID, draft.Key.FabricInstanceID, draft.Key.StreamID, draft.CanonicalRef, secondIssuer, []byte(strings.Repeat("q", 32)), secondHuman, draft.SourceVersion)
	}
	var second PublicAttachResult
	if err == nil {
		second, err = f.store.ClaimPublicAttachInTx(context.Background(), tx, draft, secondIssuer)
	}
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	if second.Attachment.Key.StreamID != first.Attachment.Key.StreamID || second.Attachment.WorkspaceID == first.Attachment.WorkspaceID || second.Attachment.AttachmentRef == first.Attachment.AttachmentRef {
		t.Fatalf("distinct human routes first=%+v second=%+v", first.Attachment, second.Attachment)
	}
}

func TestAttachmentReadsRejectCrossProjectFabricRefAndDetachedRows(t *testing.T) {
	f := newStreamFixture(t, "public-attachment-isolation")
	_, result := publicAttach(t, f, "sha256:"+strings.Repeat("4", 64))
	read := func(lookup AttachmentLookup) error {
		tx, err := beginPublicRuntimeTx(f.db)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		got, err := f.store.ReadAttachmentInTx(context.Background(), tx, lookup)
		if err == nil && got.Attachment != result.Attachment {
			return errors.New("attachment bytes changed")
		}
		return err
	}
	lookup := AttachmentLookup{ProjectID: result.Attachment.Key.ProjectID, FabricInstanceID: result.Attachment.Key.FabricInstanceID, AttachmentRef: result.Attachment.AttachmentRef}
	if err := read(lookup); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []AttachmentLookup{
		{ProjectID: uuid.NewString(), FabricInstanceID: lookup.FabricInstanceID, AttachmentRef: lookup.AttachmentRef},
		{ProjectID: lookup.ProjectID, FabricInstanceID: uuid.NewString(), AttachmentRef: lookup.AttachmentRef},
		{ProjectID: lookup.ProjectID, FabricInstanceID: lookup.FabricInstanceID, AttachmentRef: uuid.NewString()},
	} {
		if err := read(changed); !errors.Is(err, ErrStreamNotFound) {
			t.Fatalf("cross-route lookup=%+v error=%v", changed, err)
		}
	}
	if _, err := f.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, lookup.ProjectID, lookup.AttachmentRef); err != nil {
		t.Fatal(err)
	}
	if err := read(lookup); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("detached read error=%v", err)
	}
}

func TestAttachmentReadsResolveOpaqueRefBeforeForcedRLS(t *testing.T) {
	f := newStreamFixture(t, "public-attachment-resolver")
	_, result := publicAttach(t, f, "sha256:"+strings.Repeat("6", 64))

	projectID, err := f.store.ResolveAttachmentProject(context.Background(), result.Attachment.Key.FabricInstanceID, result.Attachment.AttachmentRef)
	if err != nil || projectID != result.Attachment.Key.ProjectID {
		t.Fatalf("ResolveAttachmentProject = (%q, %v), want (%q, nil)", projectID, err, result.Attachment.Key.ProjectID)
	}
	for _, lookup := range []AttachmentLookup{
		{FabricInstanceID: uuid.NewString(), AttachmentRef: result.Attachment.AttachmentRef},
		{FabricInstanceID: result.Attachment.Key.FabricInstanceID, AttachmentRef: uuid.NewString()},
	} {
		if _, err := f.store.ResolveAttachmentProject(context.Background(), lookup.FabricInstanceID, lookup.AttachmentRef); !errors.Is(err, ErrStreamNotFound) {
			t.Fatalf("ResolveAttachmentProject(%+v) error = %v, want ErrStreamNotFound", lookup, err)
		}
	}
	if _, err := f.db.Exec(`UPDATE fabric_workspace_stream_bindings SET writable=false,detached_at=now() WHERE project_id=$1 AND attachment_ref=$2`, result.Attachment.Key.ProjectID, result.Attachment.AttachmentRef); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ResolveAttachmentProject(context.Background(), result.Attachment.Key.FabricInstanceID, result.Attachment.AttachmentRef); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("detached ResolveAttachmentProject error = %v, want ErrStreamNotFound", err)
	}
}

func TestStreamPreconditionChecksEverySignedFieldBeforeReducer(t *testing.T) {
	f := newStreamFixture(t, "public-precondition-fields")
	_, result := publicAttach(t, f, "sha256:"+strings.Repeat("5", 64))
	base := SyncPrecondition{Repository: f.repository, CanonicalRef: f.ref.RefName, BaseCommitSHA: result.State.AcceptedCommitSHA, BaseTreeDigest: result.State.Accepted.Digest, ExpectedStreamVersion: result.State.Version, ExpectedLiveTreeDigest: result.State.Live.Digest}
	otherDigest := projectstate.Digest("sha256:" + strings.Repeat("f", 64))
	cases := []struct {
		name   string
		mutate func(*SyncPrecondition)
	}{
		{"repository provider", func(p *SyncPrecondition) { p.Repository.Provider = "gitlab" }},
		{"repository immutable id", func(p *SyncPrecondition) { p.Repository.ImmutableID = "999" }},
		{"repository canonical remote", func(p *SyncPrecondition) { p.Repository.CanonicalRemote += "/changed" }},
		{"canonical ref", func(p *SyncPrecondition) { p.CanonicalRef = "refs/heads/other" }},
		{"base commit", func(p *SyncPrecondition) { p.BaseCommitSHA = streamTestCommitB }},
		{"base tree", func(p *SyncPrecondition) { p.BaseTreeDigest = otherDigest }},
		{"stream version", func(p *SyncPrecondition) { p.ExpectedStreamVersion++ }},
		{"live tree", func(p *SyncPrecondition) { p.ExpectedLiveTreeDigest = otherDigest }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			precondition := base
			test.mutate(&precondition)
			operation := streamKBOperation(result.State.Live, f.scope.Actor, uuid.NewString(), "precondition\n")
			tx, err := beginPublicRuntimeTx(f.db)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			before := publicRouteBytes(t, tx, result.Attachment.Key)
			_, err = f.store.ApplyPublicOperationInTx(context.Background(), tx, f.scope, ApplyPublicOperationInput{Attachment: result.Attachment, Precondition: precondition, Operation: operation})
			if !errors.Is(err, ErrStreamPrecondition) {
				t.Fatalf("error=%v, want ErrStreamPrecondition", err)
			}
			after := publicRouteBytes(t, tx, result.Attachment.Key)
			if before != after {
				t.Fatalf("route mutated\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
	attachmentCases := []struct {
		name   string
		mutate func(*StreamAttachment)
	}{
		{"attachment stream", func(a *StreamAttachment) { a.Key.StreamID = uuid.NewString() }},
		{"attachment workspace", func(a *StreamAttachment) { a.WorkspaceID = uuid.NewString() }},
		{"attachment ref", func(a *StreamAttachment) { a.AttachmentRef = uuid.NewString() }},
		{"activity source ref", func(a *StreamAttachment) { a.ActivitySourceRef = uuid.NewString() }},
		{"attachment canonical ref", func(a *StreamAttachment) { a.CanonicalRef = "refs/heads/other" }},
		{"attachment repository provider", func(a *StreamAttachment) { a.Repository.Provider = "gitlab" }},
		{"attachment repository immutable id", func(a *StreamAttachment) { a.Repository.ImmutableID = "998" }},
		{"attachment canonical remote", func(a *StreamAttachment) { a.Repository.CanonicalRemote += "/other" }},
		{"attachment issuer", func(a *StreamAttachment) { a.IssuerKeyFingerprint = "sha256:" + strings.Repeat("e", 64) }},
		{"attachment source version", func(a *StreamAttachment) { a.SourceVersion++ }},
		{"attachment writable", func(a *StreamAttachment) { a.Writable = false }},
	}
	for _, test := range attachmentCases {
		t.Run(test.name, func(t *testing.T) {
			attachment := result.Attachment
			test.mutate(&attachment)
			operation := streamKBOperation(result.State.Live, f.scope.Actor, uuid.NewString(), "attachment route\n")
			tx, err := beginPublicRuntimeTx(f.db)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			before := publicRouteBytes(t, tx, result.Attachment.Key)
			_, err = f.store.ApplyPublicOperationInTx(context.Background(), tx, f.scope, ApplyPublicOperationInput{Attachment: attachment, Precondition: base, Operation: operation})
			if !errors.Is(err, ErrStreamPrecondition) {
				t.Fatalf("error=%v, want ErrStreamPrecondition", err)
			}
			if after := publicRouteBytes(t, tx, result.Attachment.Key); before != after {
				t.Fatalf("route mutated\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestApplyPublicOperationExactReplayUsesHistoricalSignedPrecondition(t *testing.T) {
	f := newStreamFixture(t, "public-operation-replay")
	_, attached := publicAttach(t, f, "sha256:"+strings.Repeat("6", 64))
	precondition := publicPrecondition(attached.Attachment, attached.State)
	operation := streamKBOperation(attached.State.Live, f.scope.Actor, uuid.NewString(), "first\n")
	first := applyPublic(t, f, attached.Attachment, precondition, operation)
	secondOperation := streamKBOperation(first.Live, f.scope.Actor, uuid.NewString(), "second\n")
	second := applyPublic(t, f, attached.Attachment, publicPrecondition(attached.Attachment, first), secondOperation)
	replayed := applyPublic(t, f, attached.Attachment, precondition, operation)
	if replayed.Version != first.Version || replayed.Live.Digest != first.Live.Digest || second.Version <= replayed.Version {
		t.Fatalf("first=%+v second=%+v replay=%+v", first, second, replayed)
	}
	changed := precondition
	changed.BaseCommitSHA = streamTestCommitB
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before := publicRouteBytes(t, tx, attached.Attachment.Key)
	_, err = f.store.ApplyPublicOperationInTx(context.Background(), tx, f.scope, ApplyPublicOperationInput{Attachment: attached.Attachment, Precondition: changed, Operation: operation})
	if !errors.Is(err, ErrOperationReplay) || before != publicRouteBytes(t, tx, attached.Attachment.Key) {
		t.Fatalf("changed historical replay error=%v", err)
	}
}

func TestApplyPublicOperationTypedConflictExactReplayAndChangedBytes(t *testing.T) {
	f := newStreamFixture(t, "public-typed-conflict-replay")
	_, attached := publicAttach(t, f, "sha256:"+strings.Repeat("6", 64))
	precondition := publicPrecondition(attached.Attachment, attached.State)
	operation := projectstate.OperationV1{
		SchemaVersion: 1, ID: streamTestOperationA, Kind: projectstate.OperationTombstone,
		ExpectedViewDigest: attached.State.Live.Digest, Actor: f.scope.Actor,
		Tombstone: &projectstate.TombstoneOperationV1{
			Key:                   projectstate.RecordKey{Kind: "actor", ID: streamTestActorID},
			ExpectedContentDigest: projectstate.Digest("sha256:" + strings.Repeat("f", 64)),
		},
	}
	wantOperation, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	wantActor, err := projectstate.CanonicalJSON(operation.Actor)
	if err != nil {
		t.Fatal(err)
	}
	first := applyPublic(t, f, attached.Attachment, precondition, operation)
	if first.Version != attached.State.Version || first.Live.Digest != attached.State.Live.Digest || first.ConflictID == "" {
		t.Fatalf("typed conflict = %+v", first)
	}
	var requestOperation, requestActor []byte
	if err := f.db.QueryRow(`SELECT canonical_operation_json,actor_envelope_json FROM fabric_stream_requests
		WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3 AND operation_id=$4`,
		attached.Attachment.Key.ProjectID, attached.Attachment.Key.FabricInstanceID, attached.Attachment.Key.StreamID, operation.ID).
		Scan(&requestOperation, &requestActor); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(requestOperation, wantOperation) || !bytes.Equal(requestActor, wantActor) {
		t.Fatalf("stored typed-conflict operation or actor bytes changed")
	}
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := publicRouteBytes(t, tx, attached.Attachment.Key)
	_ = tx.Rollback()
	replayed := applyPublic(t, f, attached.Attachment, precondition, operation)
	if !samePublicAttachTransition(first, replayed) {
		t.Fatalf("exact typed-conflict replay = %+v, want %+v", replayed, first)
	}
	tx, err = beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay := publicRouteBytes(t, tx, attached.Attachment.Key); afterReplay != afterFirst {
		t.Fatalf("exact conflict replay changed rows\nbefore=%s\nafter=%s", afterFirst, afterReplay)
	}
	_ = tx.Rollback()

	changed := []struct {
		name   string
		mutate func(*projectstate.OperationV1)
	}{
		{"operation", func(candidate *projectstate.OperationV1) {
			candidate.Tombstone.ExpectedContentDigest = projectstate.Digest("sha256:" + strings.Repeat("e", 64))
		}},
		{"actor", func(candidate *projectstate.OperationV1) {
			candidate.Actor.OccurredAt = candidate.Actor.OccurredAt.Add(time.Minute)
		}},
	}
	for _, test := range changed {
		t.Run(test.name, func(t *testing.T) {
			candidate := operation
			tombstone := *operation.Tombstone
			candidate.Tombstone = &tombstone
			test.mutate(&candidate)
			tx, err := beginPublicRuntimeTx(f.db)
			if err != nil {
				t.Fatal(err)
			}
			before := publicRouteBytes(t, tx, attached.Attachment.Key)
			transition, err := f.store.ApplyPublicOperationInTx(context.Background(), tx, f.scope, ApplyPublicOperationInput{Attachment: attached.Attachment, Precondition: precondition, Operation: candidate})
			if !errors.Is(err, ErrOperationReplay) || !reflect.DeepEqual(transition, StreamTransition{}) {
				t.Fatalf("changed replay = (%+v,%v), want ErrOperationReplay", transition, err)
			}
			if after := publicRouteBytes(t, tx, attached.Attachment.Key); after != before {
				t.Fatalf("changed replay mutated rows\nbefore=%s\nafter=%s", before, after)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit changed replay rejection: %v", err)
			}
		})
	}
}

func TestStreamOperationStableActorMatchPreservesExactBytes(t *testing.T) {
	f := newStreamFixture(t, "public-stable-actor-bytes")
	_, attached := publicAttach(t, f, "sha256:"+strings.Repeat("7", 64))
	contentActor := f.scope.Actor
	contentActor.Assurance = types.AssuranceLocal
	operation := streamKBOperation(attached.State.Live, contentActor, uuid.NewString(), "stable actor\n")
	wantOperation, err := projectstate.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, _ := projectstate.DigestCanonicalJSON(operation)
	wantActor, _ := projectstate.CanonicalJSON(contentActor)
	result := applyPublic(t, f, attached.Attachment, publicPrecondition(attached.Attachment, attached.State), operation)
	var versionOperation, requestOperation, versionActor, requestActor []byte
	var versionDigest, requestDigest string
	if err := f.db.QueryRow(`SELECT v.canonical_operation_json,v.operation_digest,v.actor_envelope_json,r.canonical_operation_json,r.operation_digest,r.actor_envelope_json
		FROM fabric_stream_versions v JOIN fabric_stream_requests r ON r.project_id=v.project_id AND r.fabric_instance_id=v.fabric_instance_id AND r.stream_id=v.stream_id AND r.ref_name=v.canonical_ref AND r.operation_id=v.operation_id
		WHERE v.project_id=$1 AND v.stream_id=$2 AND v.version=$3`, attached.Attachment.Key.ProjectID, attached.Attachment.Key.StreamID, result.Version).Scan(&versionOperation, &versionDigest, &versionActor, &requestOperation, &requestDigest, &requestActor); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(versionOperation, wantOperation) || !bytes.Equal(requestOperation, wantOperation) || versionDigest != string(wantDigest) || requestDigest != string(wantDigest) || !bytes.Equal(versionActor, wantActor) || !bytes.Equal(requestActor, wantActor) {
		t.Fatalf("stored operation attribution changed")
	}
}

func TestStreamOperationStableActorRejectsHumanAgentAndOwnerMismatch(t *testing.T) {
	base := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: streamTestActorID, Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
	operation := projectstate.OperationV1{Actor: base}
	for _, changed := range []types.ActorEnvelope{
		{ActorKind: types.ActorAgent, AgentID: uuid.NewString(), AccountableHumanID: streamTestActorID, SessionID: uuid.NewString(), HarnessName: "codex", HarnessVersion: "1", Assurance: types.AssurancePublicKeyContinuity, OccurredAt: base.OccurredAt},
		{ActorKind: types.ActorHuman, HumanPrincipalID: uuid.NewString(), Assurance: types.AssurancePublicKeyContinuity, OccurredAt: base.OccurredAt},
	} {
		if _, _, _, _, err := reconcileStreamOperation(types.ActorScope{ProjectID: uuid.NewString(), Actor: changed}, operation); !errors.Is(err, ErrStreamActor) {
			t.Fatalf("mismatch actor=%+v error=%v", changed, err)
		}
	}
	agentContent := types.ActorEnvelope{ActorKind: types.ActorAgent, AgentID: uuid.NewString(), AccountableHumanID: streamTestActorID, SessionID: uuid.NewString(), HarnessName: "codex", HarnessVersion: "1", Assurance: types.AssuranceLocal, OccurredAt: base.OccurredAt}
	agentTransport := agentContent
	agentTransport.Assurance = types.AssurancePrivateAuthenticated
	agentTransport.AccountableHumanID = uuid.NewString()
	operation.Actor = agentContent
	if _, _, _, _, err := reconcileStreamOperation(types.ActorScope{ProjectID: uuid.NewString(), Actor: agentTransport}, operation); !errors.Is(err, ErrStreamActor) {
		t.Fatalf("agent owner mismatch error=%v", err)
	}
}

func TestStoredStreamOperationAcceptsLocalPublicAndPrivateContentAssuranceOnly(t *testing.T) {
	for _, assurance := range []types.Assurance{types.AssuranceLocal, types.AssurancePublicKeyContinuity, types.AssurancePrivateAuthenticated} {
		actor := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: streamTestActorID, Assurance: assurance, OccurredAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
		operation := projectstate.OperationV1{SchemaVersion: 1, ID: uuid.NewString(), Kind: projectstate.OperationPutRecord, ExpectedViewDigest: projectstate.Digest("sha256:" + strings.Repeat("a", 64)), Actor: actor, PutRecord: &projectstate.PutRecordV1{Record: projectstate.RecordValueV1{}}}
		canonical, err := projectstate.CanonicalOperation(operation)
		if err != nil {
			// The payload shape is irrelevant to actor decoding; use the canonical
			// bytes of a valid fixture operation instead.
			operation = streamKBOperation(streamTestSnapshotForActor(t, actor), actor, uuid.NewString(), "assurance\n")
			canonical, err = projectstate.CanonicalOperation(operation)
		}
		if err != nil {
			t.Fatal(err)
		}
		digest, _ := projectstate.DigestCanonicalJSON(operation)
		actorJSON, _ := projectstate.CanonicalJSON(actor)
		if _, err := validateStoredStreamOperation(operation.ID, canonical, digest, actorJSON); err != nil {
			t.Fatalf("assurance %q rejected: %v", assurance, err)
		}
	}
	for _, assurance := range []types.Assurance{types.AssuranceLegacy, types.AssuranceUnknown} {
		actor := types.ActorEnvelope{ActorKind: types.ActorHuman, HumanPrincipalID: streamTestActorID, Assurance: assurance, OccurredAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
		raw, _ := projectstate.CanonicalJSON(actor)
		if _, err := decodeStoredStreamActor(raw); err == nil {
			t.Fatalf("assurance %q accepted", assurance)
		}
	}
}

func TestAdvanceAcceptedDefaultRemainsCheckedAdapter(t *testing.T) {
	f := newStreamFixture(t, "public-default-adapter")
	f.ref.RefName = "refs/heads/topic"
	_, attached := publicAttach(t, f, "sha256:"+strings.Repeat("8", 64))
	input := f.advanceInput(attached.State, streamTestCommitB, f.ref.ObservedAt.Add(time.Minute), f.tree)
	input.Ref.RefName = f.ref.RefName
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := f.store.AdvanceAcceptedDefaultInTx(context.Background(), tx, f.scope, input); !errors.Is(err, ErrStreamConflict) {
		t.Fatalf("default adapter error=%v", err)
	}
}

func TestOpenConflictIDsAreCompleteRouteScopedAndDeterministic(t *testing.T) {
	f := newStreamFixture(t, "public-open-conflicts")
	_, attached := publicAttach(t, f, "sha256:"+strings.Repeat("a", 64))
	ids := []string{"10000000-0000-4000-8000-000000000002", "10000000-0000-4000-8000-000000000001"}
	for _, id := range ids {
		if _, err := f.db.Exec(`INSERT INTO fabric_stream_conflicts(project_id,fabric_instance_id,stream_id,canonical_ref,conflict_id,detected_at_version,conflict_kind,base_tree_digest,ours_tree_digest,theirs_tree_digest,detail_json,state)
			VALUES($1,$2,$3,$4,$5,$6,'git_base_diverged',$7,$7,$7,'{}','open')`, attached.Attachment.Key.ProjectID, attached.Attachment.Key.FabricInstanceID, attached.Attachment.Key.StreamID, attached.Attachment.CanonicalRef, id, attached.State.Version, attached.State.Live.Digest); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.store.OpenConflictIDsInTx(context.Background(), tx, attached.Attachment)
	_ = tx.Rollback()
	if err != nil || !reflect.DeepEqual(got, []string{ids[1], ids[0]}) {
		t.Fatalf("open conflicts=%v err=%v", got, err)
	}
	changed := attached.Attachment
	changed.CanonicalRef = "refs/heads/other"
	tx, _ = beginPublicRuntimeTx(f.db)
	_, err = f.store.OpenConflictIDsInTx(context.Background(), tx, changed)
	_ = tx.Rollback()
	if !errors.Is(err, ErrStreamPrecondition) {
		t.Fatalf("cross-route open conflicts error=%v", err)
	}
}

func publicPrecondition(attachment StreamAttachment, state StreamTransition) SyncPrecondition {
	return SyncPrecondition{Repository: attachment.Repository, CanonicalRef: attachment.CanonicalRef, BaseCommitSHA: state.AcceptedCommitSHA, BaseTreeDigest: state.Accepted.Digest, ExpectedStreamVersion: state.Version, ExpectedLiveTreeDigest: state.Live.Digest}
}

func applyPublic(t *testing.T, f *streamFixture, attachment StreamAttachment, precondition SyncPrecondition, operation projectstate.OperationV1) StreamTransition {
	t.Helper()
	tx, err := beginPublicRuntimeTx(f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := f.store.ApplyPublicOperationInTx(context.Background(), tx, f.scope, ApplyPublicOperationInput{Attachment: attachment, Precondition: precondition, Operation: operation})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func publicRouteBytes(t *testing.T, tx *sql.Tx, key StreamKey) string {
	t.Helper()
	if _, err := tx.Exec(`SELECT set_config('wormhole.project_id',$1,true)`, key.ProjectID); err != nil {
		t.Fatal(err)
	}
	var value string
	err := tx.QueryRow(`SELECT jsonb_build_object(
		'stream',(SELECT to_jsonb(s) FROM fabric_streams s WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		'versions',(SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY version),'[]'::jsonb) FROM fabric_stream_versions v WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		'requests',(SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY operation_id),'[]'::jsonb) FROM fabric_stream_requests r WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		'conflicts',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY conflict_id),'[]'::jsonb) FROM fabric_stream_conflicts c WHERE project_id=$1 AND fabric_instance_id=$2 AND stream_id=$3),
		'audit',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY seq),'[]'::jsonb) FROM audit_log a WHERE project_id=$1))::text`, key.ProjectID, key.FabricInstanceID, key.StreamID).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func streamTestSnapshotForActor(t *testing.T, actor types.ActorEnvelope) projectstate.Snapshot {
	t.Helper()
	repository := streamTestRepository()
	tree := streamTestTree(t, "00000000-0000-4000-8000-000000000123", repository)
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	_ = actor
	return snapshot
}
