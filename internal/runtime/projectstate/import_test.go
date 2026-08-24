package projectstate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestImportPersistsCleanDirectCandidate(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	fixedNow := time.Date(2026, 7, 29, 18, 30, 0, 123, time.FixedZone("review-offset", -7*60*60))
	service.now = func() time.Time { return fixedNow }
	registered := registerGitRepository(t, service, repository)
	neighborRepository := createGitRepository(t, "00000000-0000-4000-8000-000000000002")
	neighbor := registerGitRepository(t, service, neighborRepository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	operation := servicePutTaskOperation(
		accepted,
		"99999999-9999-4999-8999-999999999991",
		"22222222-2222-4222-8222-222222222222",
		"direct task",
	)
	direct, err := state.ApplyOperation(accepted, operation)
	if err != nil {
		t.Fatal(err)
	}
	direct = diffCanonicalSnapshot(t, direct)
	writeImportSnapshot(t, repository.root, direct)

	got, err := service.Import(context.Background(), ImportRequest{
		Scope: registered.Binding.Scope, Root: repository.root, Actor: operation.Actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.PreviousCandidateDigest != nil || got.ImportedCandidateDigest != direct.Digest ||
		got.ComposedViewDigest != direct.Digest || got.ImportedChangeCount != 1 ||
		got.RebasedThroughGeneration != 0 || len(got.Conflicts) != 0 {
		t.Fatalf("Import() = %+v", got)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.State != "pending" || status.CandidateDigest != direct.Digest || status.OverlayGeneration != 0 {
		t.Fatalf("Status() = %+v", status)
	}
	var persisted *localstore.WorkspaceCandidateRecord
	if err := service.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		persisted, err = tx.Candidate(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.ImportedBy != operation.Actor.PrincipalID() || persisted.ImportedAt != fixedNow.UTC() ||
		!reflect.DeepEqual(persisted.DirectSnapshot, direct) || persisted.RebasedSnapshot == nil ||
		!reflect.DeepEqual(*persisted.RebasedSnapshot, direct) || persisted.RebasedThroughGeneration != 0 {
		t.Fatalf("persisted candidate=%+v", persisted)
	}
	neighborStatus := mustServiceStatus(t, service, neighbor.Binding.Scope)
	if neighborStatus.State != "clean" || neighborStatus.CandidateDigest != neighborStatus.AcceptedSnapshot.Digest {
		t.Fatalf("neighbor Status() = %+v", neighborStatus)
	}
}

func TestImportValidatesRequestAndOptionalDigestBeforeMutation(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	direct, err := state.ApplyOperation(accepted, servicePutTaskOperation(
		accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "direct task",
	))
	if err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, repository.root, direct)
	valid := ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: diffActorEnvelope()}
	for name, unavailable := range map[string]*Service{
		"nil service":   nil,
		"empty service": {},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := unavailable.Import(context.Background(), valid)
			if err == nil || !reflect.DeepEqual(got, ImportResult{}) {
				t.Fatalf("Import()=(%+v,%v)", got, err)
			}
		})
	}

	reads := 0
	realReader := service.readWorkingTree
	service.readWorkingTree = func(root string) (state.Tree, error) {
		reads++
		return realReader(root)
	}
	badDigest := state.Digest("not-a-digest")
	combinedInvalid := ImportRequest{
		Scope: types.WorkspaceScope{ProjectID: "not-a-project", WorkspaceID: "not-a-workspace"},
		Root:  ".", ExpectedWorkingTreeDigest: &badDigest, Actor: types.ActorEnvelope{},
	}
	got, err := service.Import(context.Background(), combinedInvalid)
	if !errors.Is(err, types.ErrInvalidActorEnvelope) || !reflect.DeepEqual(got, ImportResult{}) || reads != 0 {
		t.Fatalf("combined-invalid Import()=(%+v,%v), reads=%d; want actor precedence", got, err, reads)
	}
	bad := valid
	bad.ExpectedWorkingTreeDigest = &badDigest
	got, err = service.Import(context.Background(), bad)
	if err == nil || !reflect.DeepEqual(got, ImportResult{}) || reads != 0 {
		t.Fatalf("invalid digest Import()=(%+v,%v), reads=%d", got, err, reads)
	}
	bad = valid
	bad.Actor = types.ActorEnvelope{}
	got, err = service.Import(context.Background(), bad)
	if err == nil || !reflect.DeepEqual(got, ImportResult{}) || reads != 0 {
		t.Fatalf("invalid actor Import()=(%+v,%v), reads=%d", got, err, reads)
	}
	for name, scope := range map[string]types.WorkspaceScope{
		"project":   {ProjectID: "not-a-project", WorkspaceID: registered.Binding.Scope.WorkspaceID},
		"workspace": {ProjectID: registered.Binding.Scope.ProjectID, WorkspaceID: "not-a-workspace"},
	} {
		t.Run("invalid scope "+name, func(t *testing.T) {
			bad := valid
			bad.Scope = scope
			got, err := service.Import(context.Background(), bad)
			if !errors.Is(err, localstore.ErrNotFound) || !reflect.DeepEqual(got, ImportResult{}) || reads != 0 {
				t.Fatalf("Import()=(%+v,%v), reads=%d", got, err, reads)
			}
		})
	}

	mismatch := directOtherDigest
	bad = valid
	bad.ExpectedWorkingTreeDigest = &mismatch
	got, err = service.Import(context.Background(), bad)
	if !errors.Is(err, ErrWorkingTreeChanged) || !reflect.DeepEqual(got, ImportResult{}) || reads != 1 {
		t.Fatalf("mismatch Import()=(%+v,%v), reads=%d", got, err, reads)
	}
	if got := serviceWorkspaceState(t, store, registered.Binding.Scope); got != "clean" {
		t.Fatalf("workspace state=%q, want clean", got)
	}

	bad = valid
	bad.Root = "."
	got, err = service.Import(context.Background(), bad)
	if err == nil || !reflect.DeepEqual(got, ImportResult{}) || reads != 1 {
		t.Fatalf("relative-root Import()=(%+v,%v), reads=%d", got, err, reads)
	}

	wantDigest := direct.Digest
	valid.ExpectedWorkingTreeDigest = &wantDigest
	got, err = service.Import(context.Background(), valid)
	if err != nil || got.ImportedCandidateDigest != direct.Digest || reads != 3 {
		t.Fatalf("valid digest Import()=(%+v,%v), reads=%d", got, err, reads)
	}
}

func TestImportRawDeletionPrecedesMalformedPresentValue(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	_, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	operation := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "direct task")
	direct, err := state.ApplyOperation(accepted, operation)
	if err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, repository.root, direct)
	if _, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: operation.Actor}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository.root, ".wormhole", "state", "v1", "tasks", "22222222-2222-4222-8222-222222222222.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository.root, ".wormhole", "state", "v1", "project.json"), []byte("malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: operation.Actor})
	if !errors.Is(err, ErrDirectPathDeletion) || !strings.Contains(err.Error(), "task 22222222-2222-4222-8222-222222222222") || !reflect.DeepEqual(got, ImportResult{}) {
		t.Fatalf("Import()=(%+v,%v), want task path deletion", got, err)
	}
}

func TestImportComposesExactActiveDispositionAndChangeCount(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	overlay := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "overlay task")
	before, err := service.Apply(context.Background(), registered.Binding.Scope, overlay)
	if err != nil {
		t.Fatal(err)
	}
	direct := diffCloneSnapshot(t, accepted)
	direct.Project.Name = "Direct project"
	direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
	directActor := state.ActorV1{
		SchemaVersion: 1, Kind: "actor", ID: "33333333-3333-4333-8333-333333333333",
		ActorKind: types.ActorHuman, DisplayName: "Direct actor", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
	}
	direct.Actors[directActor.ID] = state.Record[state.ActorV1]{Value: &directActor}
	direct = diffCanonicalSnapshot(t, direct)
	directDiff, err := SemanticDiff(accepted, direct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(directDiff.Changes) < 2 {
		t.Fatalf("direct diff changes=%d, want multiple", len(directDiff.Changes))
	}
	writeImportSnapshot(t, repository.root, direct)

	got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: overlay.Actor})
	if err != nil {
		t.Fatal(err)
	}
	if got.PreviousCandidateDigest == nil || *got.PreviousCandidateDigest != before.CandidateDigest ||
		got.ImportedChangeCount != len(directDiff.Changes) || got.RebasedThroughGeneration != 1 {
		t.Fatalf("Import()=%+v", got)
	}
	status := mustServiceStatus(t, service, registered.Binding.Scope)
	if status.CandidateDigest != got.ComposedViewDigest || status.OverlayGeneration != 1 || status.State != "pending" {
		t.Fatalf("Status()=%+v", status)
	}
	var rowState string
	if err := store.DB().QueryRow(`SELECT state FROM workspace_overlay_operations WHERE project_id=? AND workspace_id=? AND generation=1`, registered.Binding.Scope.ProjectID, registered.Binding.Scope.WorkspaceID).Scan(&rowState); err != nil {
		t.Fatal(err)
	}
	if rowState != "rebased" {
		t.Fatalf("operation state=%q, want rebased", rowState)
	}
	secondOverlay := servicePutTaskOperation(
		status.AcceptedSnapshot,
		"99999999-9999-4999-8999-999999999992",
		"22222222-2222-4222-8222-222222222223",
		"second overlay task",
	)
	secondOverlay.ExpectedViewDigest = status.CandidateDigest
	if _, err := service.Apply(context.Background(), registered.Binding.Scope, secondOverlay); err != nil {
		t.Fatal(err)
	}
	second, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: overlay.Actor})
	if err != nil {
		t.Fatal(err)
	}
	if second.RebasedThroughGeneration != 2 || second.ImportedChangeCount != 0 || second.PreviousCandidateDigest == nil {
		t.Fatalf("second Import()=%+v", second)
	}
}

func TestImportMaterializationProofAloneAuthorizesResurrection(t *testing.T) {
	for _, test := range []struct {
		name           string
		removeEnvelope bool
		journalState   string
		wantErr        bool
	}{
		{name: "proved match", journalState: "published"},
		{name: "missing proof envelope", journalState: "published", removeEnvelope: true, wantErr: true},
		{name: "prepared current authority blocks import", journalState: "prepared", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			store, service := openProjectStateService(t, "")
			registered := registerGitRepository(t, service, repository)
			accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
			add := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "live task")
			live := diffCloneSnapshot(t, accepted)
			actor := state.ActorV1{
				SchemaVersion: 1, Kind: "actor", ID: composeActorID, ActorKind: types.ActorHuman,
				DisplayName: "Import actor", PublicKeys: []state.PublicKeyV1{}, Extensions: state.ExtensionsV1{},
			}
			live.Actors[composeActorID] = state.Record[state.ActorV1]{Value: &actor}
			task := *add.PutRecord.Record.Task
			live.Tasks[task.ID] = state.Record[state.TaskV1]{Value: &task}
			live = diffCanonicalSnapshot(t, live)
			writeImportSnapshot(t, repository.root, live)
			request := ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: add.Actor}
			if _, err := service.Import(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			tombstoned := diffTombstoneTask(t, live)
			writeImportSnapshot(t, repository.root, tombstoned)
			if _, err := service.Import(context.Background(), request); err != nil {
				t.Fatal(err)
			}

			envelope, err := encodeCheckpointOperations(CheckpointOperationsV1{SchemaVersion: 1, InitialThroughGeneration: 0, Operations: []CheckpointOperationV1{}})
			if err != nil {
				t.Fatal(err)
			}
			var included any = envelope
			if test.removeEnvelope {
				included = nil
			}
			if _, err := store.DB().Exec(`
				INSERT INTO workspace_materializations
				(project_id,workspace_id,journal_id,expected_live_digest,accepted_base_digest,
				 checkout_path,checkout_device,checkout_inode,prior_tree_digest,candidate_digest,
				 through_generation,prior_tree,candidate_tree,stage_path,backup_path,state,included_operations_json,publication_review_json,prior_candidate_json,publication_review_proof_version)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			`, registered.Binding.Scope.ProjectID, registered.Binding.Scope.WorkspaceID, "import-materialization",
				tombstoned.Digest, state.Digest(registered.Binding.AcceptedTreeDigest), registered.Binding.Checkout.CanonicalPath,
				registered.Binding.Checkout.Device, registered.Binding.Checkout.Inode, tombstoned.Digest, live.Digest, 0,
				encodeServiceSnapshot(t, tombstoned), encodeServiceSnapshot(t, live),
				filepath.Join(repository.root, ".wormhole-stage"), filepath.Join(repository.root, ".wormhole-backup"), test.journalState, included,
				" review\n", " prior-candidate\n", 1,
			); err != nil {
				t.Fatal(err)
			}
			writeImportSnapshot(t, repository.root, live)
			resurrectionDiff, err := SemanticDiff(tombstoned, live, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(resurrectionDiff.Changes) == 0 {
				t.Fatal("matching materialization diff unexpectedly has no changes")
			}
			got, err := service.Import(context.Background(), request)
			if test.wantErr {
				if err == nil || !reflect.DeepEqual(got, ImportResult{}) ||
					(test.journalState == "prepared" && !strings.Contains(err.Error(), "prepared")) {
					t.Fatalf("Import()=(%+v,%v), want proof failure", got, err)
				}
				return
			}
			if err != nil || got.ImportedCandidateDigest != live.Digest ||
				got.ImportedChangeCount != len(resurrectionDiff.Changes) || len(got.Conflicts) != 0 {
				t.Fatalf("Import()=(%+v,%v)", got, err)
			}
		})
	}
}

func TestImportRejectsDispositionRowsAcrossSelectedBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		generation int64
		rowState   string
	}{
		{name: "active at boundary", generation: 1, rowState: "active"},
		{name: "rebased above boundary", generation: 2, rowState: "rebased"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			store, service := openProjectStateService(t, "")
			registered := registerGitRepository(t, service, repository)
			accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
			overlay := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "overlay task")
			if _, err := service.Apply(context.Background(), registered.Binding.Scope, overlay); err != nil {
				t.Fatal(err)
			}
			writeImportSnapshot(t, repository.root, accepted)
			if _, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: overlay.Actor}); err != nil {
				t.Fatal(err)
			}
			if test.generation == 1 {
				if _, err := store.DB().Exec(`UPDATE workspace_overlay_operations SET state='active' WHERE project_id=? AND workspace_id=? AND generation=1`, registered.Binding.Scope.ProjectID, registered.Binding.Scope.WorkspaceID); err != nil {
					t.Fatal(err)
				}
			} else {
				later := servicePutTaskOperation(mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot, "99999999-9999-4999-8999-999999999992", "22222222-2222-4222-8222-222222222223", "later")
				insertServiceOperation(t, store, registered.Binding.Scope, test.generation, later, test.rowState)
			}
			got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: overlay.Actor})
			if err == nil || !reflect.DeepEqual(got, ImportResult{}) {
				t.Fatalf("Import()=(%+v,%v), want boundary corruption", got, err)
			}
		})
	}
}

func TestImportConflictIsSuccessfulAndPersistsOursTheirs(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	add := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "base task")
	base, err := state.ApplyOperation(accepted, add)
	if err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, repository.root, base)
	if _, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: add.Actor}); err != nil {
		t.Fatal(err)
	}

	overlayTask := *base.Tasks["22222222-2222-4222-8222-222222222222"].Value
	overlayTask.Title = "overlay title"
	overlayTask.UpdatedAt = overlayTask.UpdatedAt.Add(time.Minute)
	overlay := state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999992", Kind: state.OperationPutRecord,
		ExpectedViewDigest: base.Digest, Actor: add.Actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &overlayTask}},
	}
	oldComposed, err := service.Apply(context.Background(), registered.Binding.Scope, overlay)
	if err != nil {
		t.Fatal(err)
	}
	direct := diffCloneSnapshot(t, base)
	direct.Tasks["22222222-2222-4222-8222-222222222222"].Value.Title = "direct title"
	direct.Tasks["22222222-2222-4222-8222-222222222222"].Value.UpdatedAt = direct.Tasks["22222222-2222-4222-8222-222222222222"].Value.UpdatedAt.Add(2 * time.Minute)
	direct = diffCanonicalSnapshot(t, direct)
	ours, err := state.ApplyOperation(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	expectedMerge, err := ThreeWayRebase(base, direct, ours)
	if err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, repository.root, direct)

	got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: add.Actor})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Conflicts, expectedMerge.Conflicts) || len(got.Conflicts) == 0 ||
		got.PreviousCandidateDigest == nil || *got.PreviousCandidateDigest != oldComposed.CandidateDigest ||
		got.ImportedCandidateDigest != direct.Digest || got.ComposedViewDigest != oldComposed.CandidateDigest || got.ImportedChangeCount != 1 {
		t.Fatalf("Import()=%+v", got)
	}
	if got := serviceWorkspaceState(t, store, registered.Binding.Scope); got != "conflicted" {
		t.Fatalf("workspace state=%q, want conflicted", got)
	}
	setServiceWorkspaceState(t, store, registered.Binding.Scope, "pending")
	mismatchBefore := captureImportRawState(t, store)
	zero, mismatchErr := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: add.Actor})
	if mismatchErr == nil || !reflect.DeepEqual(zero, ImportResult{}) || !reflect.DeepEqual(captureImportRawState(t, store), mismatchBefore) {
		t.Fatalf("status/evidence mismatch Import()=(%+v,%v)", zero, mismatchErr)
	}
	setServiceWorkspaceState(t, store, registered.Binding.Scope, "conflicted")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopened := openProjectStateServiceAt(t, databasePath)
	status := mustServiceStatus(t, reopened, registered.Binding.Scope)
	if status.State != "conflicted" || status.CandidateDigest != oldComposed.CandidateDigest || status.OverlayGeneration != 1 {
		t.Fatalf("reopened Status()=%+v", status)
	}
	var persistedCandidate *localstore.WorkspaceCandidateRecord
	var persistedConflicts []Conflict
	if err := reopened.repo.WithImmediateWorkspace(context.Background(), registered.Binding.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		candidate, err := tx.Candidate(context.Background())
		if err != nil {
			return err
		}
		occurrences, err := tx.OpenConflictOccurrences(context.Background())
		if err != nil {
			return err
		}
		decoded, err := decodeWorkspaceConflictOccurrences(occurrences)
		if err != nil {
			return err
		}
		persistedCandidate, persistedConflicts = candidate, decoded
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if persistedCandidate == nil || !reflect.DeepEqual(persistedCandidate.DirectSnapshot, direct) ||
		persistedCandidate.RebasedSnapshot == nil || !reflect.DeepEqual(*persistedCandidate.RebasedSnapshot, expectedMerge.Snapshot) {
		t.Fatalf("reopened candidate=%+v", persistedCandidate)
	}
	if !reflect.DeepEqual(persistedConflicts, expectedMerge.Conflicts) || !reflect.DeepEqual(persistedConflicts, got.Conflicts) {
		t.Fatalf("reopened conflicts=%+v\nwant=%+v", persistedConflicts, expectedMerge.Conflicts)
	}
}

func TestImportRejectsPreexistingConflictStatusMismatch(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateService(t, "")
	registered := registerGitRepository(t, service, repository)
	setServiceWorkspaceState(t, store, registered.Binding.Scope, "conflicted")
	got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: diffActorEnvelope()})
	if err == nil || !reflect.DeepEqual(got, ImportResult{}) || serviceWorkspaceState(t, store, registered.Binding.Scope) != "conflicted" {
		t.Fatalf("Import()=(%+v,%v)", got, err)
	}
}

func TestImportSecondCaptureAndFinalCheckoutRacesReturnZero(t *testing.T) {
	t.Run("reader alias is cloned", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		store, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		shared, err := ReadWorkingTreeNoFollow(repository.root)
		if err != nil {
			t.Fatal(err)
		}
		if len(shared) < 2 {
			t.Fatalf("working tree has %d files, want at least two", len(shared))
		}
		calls := 0
		service.readWorkingTree = func(string) (state.Tree, error) {
			calls++
			if calls == 2 {
				shared[0], shared[1] = shared[1], shared[0]
			}
			return shared, nil
		}
		got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: diffActorEnvelope()})
		if !errors.Is(err, ErrWorkingTreeChanged) || !reflect.DeepEqual(got, ImportResult{}) || serviceWorkspaceState(t, store, registered.Binding.Scope) != "clean" {
			t.Fatalf("Import()=(%+v,%v)", got, err)
		}
	})

	t.Run("second capture bytes", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		store, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		first, err := ReadWorkingTreeNoFollow(repository.root)
		if err != nil {
			t.Fatal(err)
		}
		second := checkpointCloneTree(first)
		second[0].Data = append(bytes.Clone(second[0].Data), ' ')
		calls := 0
		service.readWorkingTree = func(string) (state.Tree, error) {
			calls++
			if calls == 1 {
				return first, nil
			}
			return second, nil
		}
		got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: diffActorEnvelope()})
		if !errors.Is(err, ErrWorkingTreeChanged) || !reflect.DeepEqual(got, ImportResult{}) || serviceWorkspaceState(t, store, registered.Binding.Scope) != "clean" {
			t.Fatalf("Import()=(%+v,%v)", got, err)
		}
	})

	t.Run("checkout identity after second capture", func(t *testing.T) {
		repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
		store, service := openProjectStateService(t, "")
		registered := registerGitRepository(t, service, repository)
		realReader := service.readWorkingTree
		calls := 0
		service.readWorkingTree = func(root string) (state.Tree, error) {
			calls++
			tree, err := realReader(root)
			if err != nil {
				return nil, err
			}
			if calls == 2 {
				oldRoot := repository.root + "-old"
				if err := os.Rename(repository.root, oldRoot); err != nil {
					return nil, err
				}
				if err := os.Mkdir(repository.root, 0o700); err != nil {
					return nil, err
				}
				if err := writeImportTree(repository.root, tree); err != nil {
					return nil, err
				}
			}
			return tree, nil
		}
		got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: diffActorEnvelope()})
		if !errors.Is(err, ErrWorkingTreeChanged) || !reflect.DeepEqual(got, ImportResult{}) || serviceWorkspaceState(t, store, registered.Binding.Scope) != "clean" {
			t.Fatalf("Import()=(%+v,%v)", got, err)
		}
	})
}

func TestImportWriteFailureAndCommitUnknownReturnZero(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *localstore.Store)
		want  error
	}{
		{name: "candidate write", setup: func(t *testing.T, store *localstore.Store) {
			if _, err := store.DB().Exec(`CREATE TRIGGER import_reject_candidate BEFORE INSERT ON workspace_candidates BEGIN SELECT RAISE(ABORT, 'reject candidate'); END`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "commit unknown after final status write", want: localstore.ErrCommitOutcomeUnknown, setup: func(t *testing.T, store *localstore.Store) {
			if _, err := store.DB().Exec(`
				CREATE TABLE import_deferred_failure(
				  project_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
				  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) DEFERRABLE INITIALLY DEFERRED
				);
				CREATE TRIGGER import_fail_commit AFTER UPDATE OF status ON workspace_bindings BEGIN
				  INSERT INTO import_deferred_failure(project_id,workspace_id) VALUES ('00000000-0000-4000-8000-000000000099','00000000-0000-4000-8000-000000000098');
				END;
			`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
			databasePath := filepath.Join(t.TempDir(), "gateway.db")
			store, service := openProjectStateServiceAt(t, databasePath)
			registered := registerGitRepository(t, service, repository)
			test.setup(t, store)
			before := captureImportRawState(t, store)
			got, err := service.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: diffActorEnvelope()})
			if err == nil || !reflect.DeepEqual(got, ImportResult{}) || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("Import()=(%+v,%v), want %v", got, err, test.want)
			}
			if after := captureImportRawState(t, store); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed Import changed state\nbefore=%+v\nafter=%+v", before, after)
			}
			if test.want == nil {
				return
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopenedStore, reopenedService := openProjectStateServiceAt(t, databasePath)
			if after := captureImportRawState(t, reopenedStore); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed Import changed state after reopen\nbefore=%+v\nafter=%+v", before, after)
			}
			if _, err := reopenedStore.DB().Exec(`DROP TRIGGER import_fail_commit; DROP TABLE import_deferred_failure`); err != nil {
				t.Fatal(err)
			}
			accepted := mustServiceStatus(t, reopenedService, registered.Binding.Scope).AcceptedSnapshot
			operation := servicePutTaskOperation(accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "fresh retry")
			direct, err := state.ApplyOperation(accepted, operation)
			if err != nil {
				t.Fatal(err)
			}
			writeImportSnapshot(t, repository.root, direct)
			retry, err := reopenedService.Import(context.Background(), ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: operation.Actor})
			if err != nil || retry.ImportedCandidateDigest != direct.Digest || retry.ImportedChangeCount != 1 {
				t.Fatalf("retry Import()=(%+v,%v)", retry, err)
			}
		})
	}
}

func TestImportRollsBackEveryWriteStage(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T) importServiceFixture
		trigger string
	}{
		{name: "candidate insert", prepare: prepareBasicImportFixture, trigger: `
			CREATE TRIGGER import_fault BEFORE INSERT ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate insert'); END`},
		{name: "candidate update", prepare: prepareExistingCandidateImportFixture, trigger: `
			CREATE TRIGGER import_fault BEFORE UPDATE ON workspace_candidates BEGIN SELECT RAISE(ABORT,'candidate update'); END`},
		{name: "second operation transition", prepare: prepareTwoOperationImportFixture, trigger: `
			CREATE TRIGGER import_fault BEFORE UPDATE OF state ON workspace_overlay_operations
			WHEN OLD.generation=2 AND NEW.state='rebased' BEGIN SELECT RAISE(ABORT,'second transition'); END`},
		{name: "conflict insert", prepare: prepareConflictingImportFixture, trigger: `
			CREATE TRIGGER import_fault BEFORE INSERT ON workspace_conflicts BEGIN SELECT RAISE(ABORT,'conflict insert'); END`},
		{name: "conflict resolve", prepare: prepareConflictResolutionImportFixture, trigger: `
			CREATE TRIGGER import_fault BEFORE UPDATE OF state ON workspace_conflicts
			WHEN OLD.state='open' AND NEW.state='resolved' BEGIN SELECT RAISE(ABORT,'conflict resolve'); END`},
		{name: "final status", prepare: prepareBasicImportFixture, trigger: `
			CREATE TRIGGER import_fault BEFORE UPDATE OF status ON workspace_bindings BEGIN SELECT RAISE(ABORT,'status'); END`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := test.prepare(t)
			if _, err := fixture.store.DB().Exec(test.trigger); err != nil {
				t.Fatal(err)
			}
			before := captureImportRawState(t, fixture.store)
			got, err := fixture.service.Import(context.Background(), fixture.request)
			if err == nil || !reflect.DeepEqual(got, ImportResult{}) {
				t.Fatalf("Import()=(%+v,%v), want zero rollback", got, err)
			}
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, _ := openProjectStateServiceAt(t, fixture.databasePath)
			if after := captureImportRawState(t, reopened); !reflect.DeepEqual(after, before) {
				t.Fatalf("rollback changed raw state\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

type importServiceFixture struct {
	databasePath string
	store        *localstore.Store
	service      *Service
	request      ImportRequest
	accepted     state.Snapshot
}

func prepareBasicImportFixture(t *testing.T) importServiceFixture {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "gateway.db")
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	store, service := openProjectStateServiceAt(t, databasePath)
	registered := registerGitRepository(t, service, repository)
	accepted := mustServiceStatus(t, service, registered.Binding.Scope).AcceptedSnapshot
	return importServiceFixture{
		databasePath: databasePath, store: store, service: service, accepted: accepted,
		request: ImportRequest{Scope: registered.Binding.Scope, Root: repository.root, Actor: diffActorEnvelope()},
	}
}

func prepareExistingCandidateImportFixture(t *testing.T) importServiceFixture {
	t.Helper()
	fixture := prepareBasicImportFixture(t)
	if _, err := fixture.service.Import(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func prepareTwoOperationImportFixture(t *testing.T) importServiceFixture {
	t.Helper()
	fixture := prepareBasicImportFixture(t)
	first := servicePutTaskOperation(fixture.accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "first")
	afterFirst, err := state.ApplyOperation(fixture.accepted, first)
	if err != nil {
		t.Fatal(err)
	}
	second := servicePutTaskOperation(afterFirst, "99999999-9999-4999-8999-999999999992", "22222222-2222-4222-8222-222222222223", "second")
	if _, err := fixture.service.ApplyBatch(context.Background(), fixture.request.Scope, []state.OperationV1{first, second}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func prepareConflictingImportFixture(t *testing.T) importServiceFixture {
	t.Helper()
	fixture := prepareBasicImportFixture(t)
	add := servicePutTaskOperation(fixture.accepted, "99999999-9999-4999-8999-999999999991", "22222222-2222-4222-8222-222222222222", "base")
	base, err := state.ApplyOperation(fixture.accepted, add)
	if err != nil {
		t.Fatal(err)
	}
	writeImportSnapshot(t, fixture.request.Root, base)
	fixture.request.Actor = add.Actor
	if _, err := fixture.service.Import(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	overlayTask := *base.Tasks["22222222-2222-4222-8222-222222222222"].Value
	overlayTask.Title = "overlay"
	overlayTask.UpdatedAt = overlayTask.UpdatedAt.Add(time.Minute)
	overlay := state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999992", Kind: state.OperationPutRecord,
		ExpectedViewDigest: base.Digest, Actor: add.Actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Task: &overlayTask}},
	}
	if _, err := fixture.service.Apply(context.Background(), fixture.request.Scope, overlay); err != nil {
		t.Fatal(err)
	}
	direct := diffCloneSnapshot(t, base)
	direct.Tasks["22222222-2222-4222-8222-222222222222"].Value.Title = "direct"
	direct.Tasks["22222222-2222-4222-8222-222222222222"].Value.UpdatedAt = direct.Tasks["22222222-2222-4222-8222-222222222222"].Value.UpdatedAt.Add(2 * time.Minute)
	direct = diffCanonicalSnapshot(t, direct)
	writeImportSnapshot(t, fixture.request.Root, direct)
	return fixture
}

func prepareConflictResolutionImportFixture(t *testing.T) importServiceFixture {
	t.Helper()
	fixture := prepareConflictingImportFixture(t)
	if _, err := fixture.service.Import(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func writeImportSnapshot(t *testing.T, root string, snapshot state.Snapshot) {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeImportTree(root, tree); err != nil {
		t.Fatal(err)
	}
}

func writeImportTree(root string, tree state.Tree) error {
	wormholeRoot := filepath.Join(root, ".wormhole")
	if err := os.RemoveAll(wormholeRoot); err != nil {
		return err
	}
	for _, file := range tree {
		path := filepath.Join(wormholeRoot, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.Data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

type importRawState struct {
	bindings   [][]string
	candidates [][]string
	operations [][]string
	conflicts  [][]string
}

func captureImportRawState(t *testing.T, store *localstore.Store) importRawState {
	t.Helper()
	return importRawState{
		bindings: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(checkout_path),quote(checkout_device),quote(checkout_inode),
			       quote(repository_identity_json),quote(accepted_ref),quote(accepted_commit),quote(accepted_digest),
			       quote(accepted_snapshot),quote(status),quote(created_at),quote(updated_at)
			FROM workspace_bindings ORDER BY project_id,workspace_id`),
		candidates: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(accepted_base_digest),quote(working_tree_digest),
			       quote(direct_tree),quote(rebased_tree),quote(rebased_through_generation),quote(imported_by),quote(imported_at)
			FROM workspace_candidates ORDER BY project_id,workspace_id`),
		operations: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(generation),quote(operation_id),quote(operation_json),
			       quote(state),quote(stashed_by_stash_id),quote(created_at)
			FROM workspace_overlay_operations ORDER BY project_id,workspace_id,generation`),
		conflicts: queryImportRawRows(t, store, `
			SELECT quote(project_id),quote(workspace_id),quote(occurrence_id),quote(conflict_id),quote(record_kind),quote(record_id),
			       quote(field_path),quote(conflict_kind),quote(base_json),quote(ours_json),quote(theirs_json),
			       quote(state),quote(created_at),quote(resolved_at)
			FROM workspace_conflicts ORDER BY project_id,workspace_id,occurrence_id`),
	}
}

func queryImportRawRows(t *testing.T, store *localstore.Store, query string) [][]string {
	t.Helper()
	rows, err := store.DB().Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := make([][]string, 0)
	for rows.Next() {
		values := make([]string, len(columns))
		targets := make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestValidateDirectDeltaRejectsImmutableEventMutation(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	event := next.Events[diffEventID]
	event.EventType = "message.edited"
	next.Events[diffEventID] = event
	next = diffCanonicalSnapshot(t, next)

	err := ValidateDirectDelta(prior, next)
	if !errors.Is(err, ErrDirectImmutableRecordMutation) {
		t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableRecordMutation", err)
	}
	if !errors.Is(err, state.ErrImmutableRecord) {
		t.Fatalf("ValidateDirectDelta() error = %v, want state.ErrImmutableRecord", err)
	}
}

func TestValidateDirectDeltaRejectsImmutableGitLinkMutation(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	next.GitLinks[diffGitLinkID].Value.Summary = "changed"
	next = diffCanonicalSnapshot(t, next)

	if err := ValidateDirectDelta(prior, next); !errors.Is(err, ErrDirectImmutableRecordMutation) {
		t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableRecordMutation", err)
	}
}

func TestValidateDirectDeltaRejectsRawPathDeletion(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	for _, test := range []struct {
		name   string
		remove func(*state.Snapshot)
		want   error
	}{
		{name: "event", remove: func(snapshot *state.Snapshot) { delete(snapshot.Events, diffEventID) }, want: ErrDirectImmutableRecordMutation},
		{name: "git link", remove: func(snapshot *state.Snapshot) { delete(snapshot.GitLinks, diffGitLinkID) }, want: ErrDirectImmutableRecordMutation},
		{name: "actor", remove: func(snapshot *state.Snapshot) { delete(snapshot.Actors, composeActorID) }},
		{name: "task", remove: func(snapshot *state.Snapshot) { delete(snapshot.Tasks, composeTaskID) }},
		{name: "task link", remove: func(snapshot *state.Snapshot) { delete(snapshot.TaskLinks, diffTaskLinkID) }},
		{name: "article", remove: func(snapshot *state.Snapshot) { delete(snapshot.Articles, diffArticleID) }},
		{name: "channel", remove: func(snapshot *state.Snapshot) { delete(snapshot.Channels, diffChannelID) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := diffCloneSnapshot(t, prior)
			test.remove(&next)
			want := test.want
			if want == nil {
				want = ErrDirectPathDeletion
			}
			if err := ValidateDirectDelta(prior, next); !errors.Is(err, want) {
				t.Fatalf("ValidateDirectDelta() error = %v, want %v", err, want)
			}
		})
	}
}

func TestValidateDirectDeltaRawDeletionUsesCanonicalKindAndIDOrder(t *testing.T) {
	prior := diffAddTask(t, recordAllKindsSnapshot(t))
	next := diffCloneSnapshot(t, prior)
	delete(next.Tasks, composeTaskID)
	delete(next.Tasks, diffSecondTaskID)
	delete(next.Events, diffEventID)

	err := ValidateDirectDelta(prior, next)
	if !errors.Is(err, ErrDirectPathDeletion) || !strings.Contains(err.Error(), "task "+composeTaskID) {
		t.Fatalf("ValidateDirectDelta() error = %v, want first task deletion", err)
	}
}

func TestValidateDirectDeltaAllowsImmutableReplayAdditionAndNewTombstone(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	const eventID = "99999999-9999-4999-8999-999999999991"
	const gitLinkID = "99999999-9999-4999-8999-999999999992"
	const tombstoneID = "99999999-9999-4999-8999-999999999993"
	now := next.Project.CreatedAt
	next.Events[eventID] = state.EventV1{
		SchemaVersion: 1, Kind: "event", ID: eventID, ChannelID: diffChannelID, ActorID: composeActorID,
		EventType: "message.posted", Payload: json.RawMessage(`{}`), CreatedAt: now, Extensions: state.ExtensionsV1{},
	}
	next.GitLinks[gitLinkID] = state.Record[state.GitLinkV1]{Value: &state.GitLinkV1{
		SchemaVersion: 1, Kind: "git_link", ID: gitLinkID, Repository: "acme/wormhole", Summary: "new link",
		ActorID: composeActorID, CreatedAt: now, Extensions: state.ExtensionsV1{},
	}}
	next.Actors[tombstoneID] = state.Record[state.ActorV1]{Tombstone: &state.TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: tombstoneID, EntityKind: "actor", DeletedContentDigest: directOtherDigest,
		DeletedBy: diffActorEnvelope(), DeletedAt: diffActorEnvelope().OccurredAt, Extensions: state.ExtensionsV1{},
	}}
	next = diffCanonicalSnapshot(t, next)

	if err := ValidateDirectDelta(prior, next); err != nil {
		t.Fatalf("ValidateDirectDelta() error = %v", err)
	}
}

func TestValidateDirectDeltaRejectsInvalidPriorAndNextAndBindingMismatch(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	invalidPrior := diffCloneSnapshot(t, prior)
	invalidPrior.Digest = ""
	if err := ValidateDirectDelta(invalidPrior, prior); err == nil {
		t.Fatal("ValidateDirectDelta accepted invalid prior")
	}

	invalidNext := diffCloneSnapshot(t, prior)
	invalidNext.Project.Name = ""
	if err := ValidateDirectDelta(prior, invalidNext); err == nil {
		t.Fatal("ValidateDirectDelta accepted invalid next")
	}

	mismatch := diffCloneSnapshot(t, prior)
	mismatch.Config.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"}
	mismatch = diffCanonicalSnapshot(t, mismatch)
	if err := ValidateDirectDelta(prior, mismatch); err == nil {
		t.Fatal("ValidateDirectDelta accepted a binding mismatch")
	}
}

func TestValidateDirectDeltaTombstoneLifecycle(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	taskTombstone := diffTombstoneTask(t, prior)
	articleTombstone := directTombstoneArticle(t, prior)

	for _, test := range []struct {
		name string
		base state.Snapshot
		next state.Snapshot
		want error
	}{
		{name: "accept exact task tombstone", base: prior, next: taskTombstone},
		{name: "accept exact KB tombstone", base: prior, next: articleTombstone},
		{name: "reject incorrect content digest", base: prior, next: directChangedTaskTombstone(t, taskTombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedContentDigest = directOtherDigest }), want: state.ErrTombstoneDigest},
		{name: "reject incorrect KB body digest", base: prior, next: directChangedArticleTombstone(t, articleTombstone, func(tombstone *state.TombstoneV1) { digest := directOtherDigest; tombstone.DeletedBodyDigest = &digest }), want: state.ErrTombstoneDigest},
		{name: "reject missing KB body digest", base: prior, next: directChangedArticleTombstoneRaw(t, articleTombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedBodyDigest = nil }), want: state.ErrTombstoneDigest},
		{name: "reject edited tombstone", base: taskTombstone, next: directChangedTaskTombstone(t, taskTombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedAt = tombstone.DeletedAt.Add(time.Minute) }), want: ErrDirectEditTombstone},
		{name: "reject resurrection", base: taskTombstone, next: diffResurrectTask(t, taskTombstone, *prior.Tasks[composeTaskID].Value), want: ErrDirectResurrection},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDirectDelta(test.base, test.next)
			if test.want == nil {
				if err != nil {
					t.Fatalf("ValidateDirectDelta() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("ValidateDirectDelta() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidateDirectDeltaMissingKBBodyDigestPreflightDoesNotMaskMalformedRecord(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := directTombstoneArticle(t, prior)
	next = directChangedArticleTombstoneRaw(t, next, func(tombstone *state.TombstoneV1) { tombstone.DeletedBodyDigest = nil })
	live := *prior.Articles[diffArticleID].Value
	record := next.Articles[diffArticleID]
	record.Value = &live
	next.Articles[diffArticleID] = record

	err := ValidateDirectDelta(prior, next)
	if err == nil || errors.Is(err, state.ErrTombstoneDigest) {
		t.Fatalf("ValidateDirectDelta() error = %v, want generic invalid-next error", err)
	}
}

func TestValidateDirectDeltaMissingKBBodyDigestPreflightDoesNotMaskMalformedTombstone(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	tombstone := directTombstoneArticle(t, prior)
	for _, test := range []struct {
		name   string
		mutate func(*state.Snapshot)
	}{
		{name: "unexpected body", mutate: func(snapshot *state.Snapshot) {
			record := snapshot.Articles[diffArticleID]
			record.Body = []byte("unexpected\n")
			snapshot.Articles[diffArticleID] = record
		}},
		{name: "bad tombstone header", mutate: func(snapshot *state.Snapshot) {
			snapshot.Articles[diffArticleID].Tombstone.SchemaVersion = 2
		}},
		{name: "bad tombstone actor", mutate: func(snapshot *state.Snapshot) {
			snapshot.Articles[diffArticleID].Tombstone.DeletedBy = types.ActorEnvelope{}
		}},
		{name: "stale snapshot digest", mutate: func(snapshot *state.Snapshot) {
			snapshot.Digest = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := directChangedArticleTombstoneRaw(t, tombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedBodyDigest = nil })
			test.mutate(&next)
			err := ValidateDirectDelta(prior, next)
			if err == nil || errors.Is(err, state.ErrTombstoneDigest) {
				t.Fatalf("ValidateDirectDelta() error = %v, want generic invalid-next error", err)
			}
		})
	}
}

func TestValidateDirectDeltaRejectsChangedCreatedAt(t *testing.T) {
	for _, test := range []struct {
		name   string
		base   func(*testing.T) state.Snapshot
		change func(*state.Snapshot)
	}{
		{name: "project", base: composeFixtureSnapshot, change: func(snapshot *state.Snapshot) {
			snapshot.Project.CreatedAt = snapshot.Project.CreatedAt.Add(-time.Minute)
		}},
		{name: "task", base: composeFixtureSnapshot, change: func(snapshot *state.Snapshot) {
			snapshot.Tasks[composeTaskID].Value.CreatedAt = snapshot.Tasks[composeTaskID].Value.CreatedAt.Add(-time.Minute)
		}},
		{name: "KB article", base: func(t *testing.T) state.Snapshot {
			return diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "body\n")
		}, change: func(snapshot *state.Snapshot) {
			snapshot.Articles[diffArticleID].Value.CreatedAt = snapshot.Articles[diffArticleID].Value.CreatedAt.Add(-time.Minute)
		}},
		{name: "channel", base: recordAllKindsSnapshot, change: func(snapshot *state.Snapshot) {
			snapshot.Channels[diffChannelID].Value.CreatedAt = snapshot.Channels[diffChannelID].Value.CreatedAt.Add(-time.Minute)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prior := test.base(t)
			next := diffCloneSnapshot(t, prior)
			test.change(&next)
			next = diffCanonicalSnapshot(t, next)
			if err := ValidateDirectDelta(prior, next); !errors.Is(err, ErrDirectImmutableFieldMutation) {
				t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableFieldMutation", err)
			}
		})
	}
}

func TestValidateDirectDeltaAllowsMutableAndGitOwnedChangesWithoutAliasing(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	next.Actors[composeActorID].Value.DisplayName = "Changed actor"
	next.Tasks[composeTaskID].Value.Title = "Changed task"
	next.TaskLinks[diffTaskLinkID].Value.LinkType = "task"
	next.TaskLinks[diffTaskLinkID].Value.TargetID = composeTaskID
	next.Config.Handle = types.ProjectHandle{Namespace: "other", Name: "handle"}
	next.Remotes = &state.RemotesV1{Version: 1, Fabrics: []state.FabricHintV1{{
		Alias: "public", URL: "https://fabric.example.test", InstanceID: "fabric-one", RemoteProjectID: "remote-one", Mode: "public",
	}}}
	next = diffCanonicalSnapshot(t, next)
	priorBefore, nextBefore := diffTreeBytes(t, prior), diffTreeBytes(t, next)

	if err := ValidateDirectDelta(prior, next); err != nil {
		t.Fatalf("ValidateDirectDelta() error = %v", err)
	}
	if !bytes.Equal(priorBefore, diffTreeBytes(t, prior)) || !bytes.Equal(nextBefore, diffTreeBytes(t, next)) {
		t.Fatal("ValidateDirectDelta mutated or aliased an input")
	}
}

func TestValidateDirectDeltaErrorDoesNotMutateInputs(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	event := next.Events[diffEventID]
	event.EventType = "message.edited"
	next.Events[diffEventID] = event
	next = diffCanonicalSnapshot(t, next)
	priorBefore, nextBefore := diffTreeBytes(t, prior), diffTreeBytes(t, next)

	err := ValidateDirectDelta(prior, next)
	if !errors.Is(err, ErrDirectImmutableRecordMutation) {
		t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableRecordMutation", err)
	}
	if !bytes.Equal(priorBefore, diffTreeBytes(t, prior)) || !bytes.Equal(nextBefore, diffTreeBytes(t, next)) {
		t.Fatal("ValidateDirectDelta mutated or aliased an input on error")
	}
}

func TestValidateDirectDeltaPrecedence(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	delete(next.Events, diffEventID)
	delete(next.Tasks, composeTaskID)
	next.Project.Name = ""
	if err := ValidateDirectDelta(prior, next); !errors.Is(err, ErrDirectPathDeletion) {
		t.Fatalf("ValidateDirectDelta() error = %v, want raw task deletion before other failures", err)
	}

	stale := diffCloneSnapshot(t, prior)
	stale.Digest = ""
	if err := ValidateDirectDelta(stale, next); err == nil || errors.Is(err, ErrDirectPathDeletion) {
		t.Fatalf("ValidateDirectDelta() error = %v, want invalid prior before raw deletion", err)
	}
}

const directOtherDigest state.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func directChangedTaskTombstone(t *testing.T, snapshot state.Snapshot, change func(*state.TombstoneV1)) state.Snapshot {
	t.Helper()
	next := diffCloneSnapshot(t, snapshot)
	change(next.Tasks[composeTaskID].Tombstone)
	return diffCanonicalSnapshot(t, next)
}

func directChangedArticleTombstone(t *testing.T, snapshot state.Snapshot, change func(*state.TombstoneV1)) state.Snapshot {
	t.Helper()
	next := diffCloneSnapshot(t, snapshot)
	change(next.Articles[diffArticleID].Tombstone)
	return diffCanonicalSnapshot(t, next)
}

func directChangedArticleTombstoneRaw(t *testing.T, snapshot state.Snapshot, change func(*state.TombstoneV1)) state.Snapshot {
	t.Helper()
	next := diffCloneSnapshot(t, snapshot)
	change(next.Articles[diffArticleID].Tombstone)
	return next
}

func directTombstoneArticle(t *testing.T, snapshot state.Snapshot) state.Snapshot {
	t.Helper()
	contentDigest, err := state.DigestCanonicalJSON(*snapshot.Articles[diffArticleID].Value)
	if err != nil {
		t.Fatal(err)
	}
	bodyDigest, err := state.DigestCanonicalMarkdown(snapshot.Articles[diffArticleID].Body)
	if err != nil {
		t.Fatal(err)
	}
	next, err := state.ApplyOperation(snapshot, state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999997", Kind: state.OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: diffActorEnvelope(),
		Tombstone: &state.TombstoneOperationV1{
			Key: state.RecordKey{Kind: "kb_article", ID: diffArticleID}, ExpectedContentDigest: contentDigest, ExpectedBodyDigest: &bodyDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return next
}
