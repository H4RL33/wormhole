package projectstate

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestBuildRestorePlanCleanChangedBase(t *testing.T) {
	fixture := newRestorePlanFixture(t)
	current := composeCloneSnapshot(t, fixture.source)
	current.Tasks[composeTaskID].Value.Description = "changed on Git base"
	current.Tasks[composeTaskID].Value.UpdatedAt = current.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	current = stashPlanRefreshSnapshot(t, current)
	fixture.retry.Workspace.Snapshot = current
	fixture.retry.Workspace.Binding.AcceptedTreeDigest = string(current.Digest)

	wantMerge, err := ThreeWayRebase(fixture.source, current, fixture.composed)
	if err != nil {
		t.Fatal(err)
	}
	before := cloneRestoreRetryState(t, fixture.retry)
	got, err := buildRestorePlan(fixture.retry)
	if err != nil {
		t.Fatal(err)
	}
	if got.MergedSnapshot == nil || !equalRestorePlanSnapshots(t, *got.MergedSnapshot, wantMerge.Snapshot) {
		t.Fatal("clean plan does not retain the canonical semantic merge")
	}
	if got.Result.RestoredDigest != wantMerge.Snapshot.Digest || got.Result.RebasedThroughGeneration != 0 ||
		got.Result.Conflicts == nil || len(got.Result.Conflicts) != 0 || got.Result.StashRetained {
		t.Fatalf("clean result=%+v", got.Result)
	}
	if got.ConflictEvidence == nil || len(got.ConflictEvidence) != 0 {
		t.Fatalf("clean conflict evidence=%#v, want non-nil empty", got.ConflictEvidence)
	}
	if got.Current.ActiveRows == nil || len(got.Current.ActiveRows) != 0 ||
		!equalRestorePlanSnapshots(t, got.Current.Snapshot, current) {
		t.Fatalf("current proof=%+v", got.Current)
	}
	if !reflect.DeepEqual(fixture.retry, before) {
		t.Fatal("clean planner mutated its complete retry-state input")
	}
}

func TestBuildRestorePlanConflictUsesStashAsOursAndCurrentAsTheirs(t *testing.T) {
	fixture := newRestorePlanFixture(t)
	current := composeCloneSnapshot(t, fixture.source)
	current.Tasks[composeTaskID].Value.Title = "current title"
	current.Tasks[composeTaskID].Value.UpdatedAt = current.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	current = stashPlanRefreshSnapshot(t, current)
	fixture.retry.Workspace.Snapshot = current
	fixture.retry.Workspace.Binding.AcceptedTreeDigest = string(current.Digest)

	first, err := buildRestorePlan(fixture.retry)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildRestorePlan(fixture.retry)
	if err != nil {
		t.Fatal(err)
	}
	if first.MergedSnapshot != nil {
		t.Fatal("conflicted plan exposed ThreeWayRebase's provisional stash snapshot")
	}
	if first.Result.RestoredDigest != current.Digest || first.Result.RebasedThroughGeneration != 0 ||
		len(first.Result.Conflicts) != 1 || !first.Result.StashRetained {
		t.Fatalf("conflicted result=%+v", first.Result)
	}
	conflict := first.Result.Conflicts[0]
	if !bytes.Contains(conflict.Ours.Value, []byte("stashed title")) ||
		!bytes.Contains(conflict.Theirs.Value, []byte("current title")) ||
		bytes.Contains(conflict.Ours.Value, []byte("current title")) {
		t.Fatalf("conflict orientation Base/Ours/Theirs=%s/%s/%s", conflict.Base.Value, conflict.Ours.Value, conflict.Theirs.Value)
	}
	wantEvidence, err := encodeWorkspaceConflictEvidence(first.Result.Conflicts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.ConflictEvidence, wantEvidence) || !reflect.DeepEqual(first, second) {
		t.Fatal("conflict evidence or complete plan is not deterministic")
	}
}

func TestProveRestoreStashAcceptedDirectAndRebasedReplayShapes(t *testing.T) {
	for _, shape := range []string{"accepted-empty", "direct-empty", "direct-sparse", "rebased-empty", "rebased-sparse"} {
		t.Run(shape, func(t *testing.T) {
			fixture := newRestoreShapeFixture(t, shape)
			got, err := proveRestoreStash(fixture.retry)
			if err != nil {
				t.Fatal(err)
			}
			if !equalRestorePlanSnapshots(t, got.SourceBase, fixture.source) ||
				!equalRestorePlanSnapshots(t, got.Composed, fixture.composed) {
				t.Fatal("stash proof changed source or composed semantics")
			}
			if got.AbsorbedRows == nil || got.LaterRows == nil ||
				len(got.AbsorbedRows) != len(fixture.absorbed) || len(got.LaterRows) != len(fixture.later) {
				t.Fatalf("proof memberships=%d/%d, want %d/%d", len(got.AbsorbedRows), len(got.LaterRows), len(fixture.absorbed), len(fixture.later))
			}
			if got.Replay.AbsorbedOperations == nil || got.Replay.Operations == nil {
				t.Fatal("proof replay arrays became nil")
			}
		})
	}
}

func TestProveRestoreStashClassifiesCorruptionAndOperationMembership(t *testing.T) {
	corruption := []struct {
		name string
		edit func(*restorePlanFixture)
	}{
		{"source tree bytes", func(f *restorePlanFixture) { f.retry.Stash.SourceTree[0].Data[0] ^= 1 }},
		{"source digest", func(f *restorePlanFixture) {
			f.retry.Stash.SourceBaseDigest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{"composed tree bytes", func(f *restorePlanFixture) { f.retry.Stash.ComposedTree[0].Data[0] ^= 1 }},
		{"candidate digest", func(f *restorePlanFixture) {
			f.retry.Stash.CandidateDigest = state.Digest("sha256:" + strings.Repeat("b", 64))
		}},
		{"replay envelope", func(f *restorePlanFixture) {
			f.retry.Stash.OperationsJSON = strings.TrimSuffix(f.retry.Stash.OperationsJSON, "\n")
		}},
		{"replay composition", func(f *restorePlanFixture) {
			f.retry.Stash.ComposedTree = cloneCheckpointTree(f.retry.Stash.SourceTree)
			f.retry.Stash.CandidateDigest = f.retry.Stash.SourceBaseDigest
		}},
	}
	for _, test := range corruption {
		t.Run("corrupt "+test.name, func(t *testing.T) {
			fixture := newRestorePlanFixture(t)
			test.edit(&fixture)
			got, err := proveRestoreStash(fixture.retry)
			if !errors.Is(err, ErrStashCorrupt) || !reflect.DeepEqual(got, restoreStashProof{}) {
				t.Fatalf("proveRestoreStash()=(%+v,%v), want zero ErrStashCorrupt", got, err)
			}
		})
	}

	membership := []struct {
		name string
		edit func(*restorePlanFixture)
	}{
		{"nil complete audit", func(f *restorePlanFixture) { f.retry.Operations = nil }},
		{"missing member", func(f *restorePlanFixture) { f.retry.Operations = []localstore.WorkspaceOperationAuditRecord{} }},
		{"wrong state", func(f *restorePlanFixture) { f.retry.Operations[0].State = "active" }},
		{"wrong owner", func(f *restorePlanFixture) {
			owner := "30000000-0000-4000-8000-000000000099"
			f.retry.Operations[0].StashedByStashID = &owner
		}},
		{"ownerless legacy stashed", func(f *restorePlanFixture) { f.retry.Operations[0].StashedByStashID = nil }},
		{"altered row bytes", func(f *restorePlanFixture) {
			f.retry.Operations[0].OperationJSON = append([]byte(" "), f.retry.Operations[0].OperationJSON...)
		}},
		{"altered generation", func(f *restorePlanFixture) { f.retry.Operations[0].Generation++ }},
		{"altered operation ID", func(f *restorePlanFixture) {
			f.retry.Operations[0].OperationID = "90000000-0000-4000-8000-000000000099"
		}},
		{"extra owned row", func(f *restorePlanFixture) {
			extra := cloneRestoreAuditRecord(f.retry.Operations[0])
			extra.Generation = 12
			extra.OperationID = "90000000-0000-4000-8000-000000000012"
			extra.OperationJSON = canonicalRestoreOperation(t, servicePutTaskOperation(f.source, extra.OperationID, "80000000-0000-4000-8000-000000000012", "extra"))
			f.retry.Operations = append(f.retry.Operations, extra)
		}},
	}
	for _, test := range membership {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestorePlanFixture(t)
			test.edit(&fixture)
			got, err := proveRestoreStash(fixture.retry)
			if !errors.Is(err, ErrStashOperationMismatch) || !reflect.DeepEqual(got, restoreStashProof{}) {
				t.Fatalf("proveRestoreStash()=(%+v,%v), want zero ErrStashOperationMismatch", got, err)
			}
		})
	}

	t.Run("two-member swapped owner", func(t *testing.T) {
		fixture := newRestoreShapeFixture(t, "rebased-sparse")
		const otherOwner = "30000000-0000-4000-8000-000000000002"
		other := restoreAuditRow(t, 2,
			servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000002", "80000000-0000-4000-8000-000000000002", "other stash"),
			"stashed", fixture.retry.Stash.StashID)
		fixture.retry.Operations[0].StashedByStashID = restoreStringPointer(otherOwner)
		fixture.retry.Operations = append(fixture.retry.Operations, other)
		sortRestoreAudit(fixture.retry.Operations)

		got, err := proveRestoreStash(fixture.retry)
		if !errors.Is(err, ErrStashOperationMismatch) || !strings.Contains(err.Error(), "owned row 0 differs from replay") ||
			!reflect.DeepEqual(got, restoreStashProof{}) {
			t.Fatalf("swapped-owner proveRestoreStash()=(%+v,%v)", got, err)
		}
	})
}

func TestComposeRestoreCurrentUsesSparseActiveSuffixAndIgnoresDisjointTerminalOwners(t *testing.T) {
	fixture := newRestorePlanFixture(t)
	currentOperation := servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000020", "80000000-0000-4000-8000-000000000020", "current active")
	currentRow := restoreAuditRow(t, 20, currentOperation, "active", "")
	otherOwner := restoreAuditRow(t, 15,
		servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000015", "80000000-0000-4000-8000-000000000015", "other stash"),
		"stashed", "30000000-0000-4000-8000-000000000015")
	legacyOwnerless := restoreAuditRow(t, 16,
		servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000016", "80000000-0000-4000-8000-000000000016", "legacy stash"),
		"stashed", "")
	materialized := restoreAuditRow(t, 2,
		servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000002", "80000000-0000-4000-8000-000000000002", "history"),
		"materialized", "")
	fixture.retry.Operations = []localstore.WorkspaceOperationAuditRecord{materialized, fixture.retry.Operations[0], otherOwner, legacyOwnerless, currentRow}
	want, err := Compose(fixture.source, 0, []StoredOperation{{Generation: 20, Operation: currentOperation}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := composeRestoreCurrent(fixture.retry)
	if err != nil {
		t.Fatal(err)
	}
	if got.ThroughGeneration != 20 || len(got.ActiveRows) != 1 || got.ActiveRows[0].Generation != 20 ||
		!equalRestorePlanSnapshots(t, got.Snapshot, want.Snapshot) {
		t.Fatalf("current proof=%+v", got)
	}
	got.ActiveRows[0].OperationJSON[0] ^= 1
	if bytes.Equal(got.ActiveRows[0].OperationJSON, fixture.retry.Operations[4].OperationJSON) {
		t.Fatal("current proof aliases audit operation bytes")
	}
}

func TestProveRestoreStashKeepsSequentialStashOwnersAndTerminalHistoryDisjoint(t *testing.T) {
	fixture := newRestoreShapeFixture(t, "rebased-sparse")
	terminal := []localstore.WorkspaceOperationAuditRecord{
		restoreAuditRow(t, 1,
			servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000001", "80000000-0000-4000-8000-000000000001", "materialized history"),
			"materialized", ""),
		restoreAuditRow(t, 2,
			servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000002", "80000000-0000-4000-8000-000000000002", "prior stash"),
			"stashed", "30000000-0000-4000-8000-000000000002"),
		restoreAuditRow(t, 5,
			servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000005", "80000000-0000-4000-8000-000000000005", "discarded history"),
			"discarded", ""),
		restoreAuditRow(t, 12,
			servicePutTaskOperation(fixture.source, "90000000-0000-4000-8000-000000000012", "80000000-0000-4000-8000-000000000012", "later stash"),
			"stashed", "30000000-0000-4000-8000-000000000012"),
	}
	fixture.retry.Operations = append(fixture.retry.Operations, terminal...)
	sortRestoreAudit(fixture.retry.Operations)
	before := cloneRestoreRetryState(t, fixture.retry)

	got, err := proveRestoreStash(fixture.retry)
	if err != nil {
		t.Fatal(err)
	}
	wantAbsorbed := []localstore.WorkspaceOperation{cloneImportOperation(fixture.absorbed[0].WorkspaceOperation)}
	wantLater := []localstore.WorkspaceOperation{cloneImportOperation(fixture.later[0].WorkspaceOperation)}
	if !reflect.DeepEqual(got.AbsorbedRows, wantAbsorbed) || !reflect.DeepEqual(got.LaterRows, wantLater) {
		t.Fatalf("target memberships=%+v/%+v, want %+v/%+v", got.AbsorbedRows, got.LaterRows, wantAbsorbed, wantLater)
	}
	if !reflect.DeepEqual(fixture.retry, before) {
		t.Fatal("proving one stash changed disjoint stash owners or terminal history")
	}
}

func TestValidateRestoreAuditRejectsEveryMetadataAndTerminalOwnerCorruption(t *testing.T) {
	for _, test := range []struct {
		name      string
		wantError string
		edit      func(*testing.T, *[]localstore.WorkspaceOperationAuditRecord)
	}{
		{name: "active owner", wantError: "non-stashed operation has a stash owner", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) { (*rows)[0].State = "active" }},
		{name: "rebased owner", wantError: "non-stashed operation has a stash owner", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) { (*rows)[0].State = "rebased" }},
		{name: "materialized owner", wantError: "non-stashed operation has a stash owner", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) {
			(*rows)[0].State = "materialized"
		}},
		{name: "discarded owner", wantError: "non-stashed operation has a stash owner", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) { (*rows)[0].State = "discarded" }},
		{name: "noncanonical stash owner", wantError: "stashed operation has a noncanonical owner", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) {
			(*rows)[0].StashedByStashID = restoreStringPointer("BAD")
		}},
		{name: "unknown state", wantError: "invalid operation audit state", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) { (*rows)[0].State = "unknown" }},
		{name: "duplicate generation", wantError: "invalid operation audit metadata", edit: func(t *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) {
			*rows = append(*rows, restoreAuditRow(t, (*rows)[0].Generation,
				servicePutTaskOperation(composeFixtureSnapshot(t), "90000000-0000-4000-8000-000000000008", "80000000-0000-4000-8000-000000000008", "duplicate generation"), "discarded", ""))
		}},
		{name: "decreasing generation", wantError: "invalid operation audit metadata", edit: func(t *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) {
			(*rows)[0].Generation = 8
			*rows = append(*rows, restoreAuditRow(t, 7,
				servicePutTaskOperation(composeFixtureSnapshot(t), "90000000-0000-4000-8000-000000000006", "80000000-0000-4000-8000-000000000006", "decreasing generation"), "discarded", ""))
		}},
		{name: "duplicate operation ID", wantError: "duplicate operation audit identity", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) {
			duplicate := cloneRestoreAuditRecord((*rows)[0])
			duplicate.Generation++
			*rows = append(*rows, duplicate)
		}},
		{name: "noncanonical operation ID", wantError: "invalid operation audit metadata", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) { (*rows)[0].OperationID = "BAD" }},
		{name: "zero CreatedAt", wantError: "invalid operation audit metadata", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) {
			(*rows)[0].CreatedAt = time.Time{}
		}},
		{name: "non-UTC CreatedAt", wantError: "invalid operation audit metadata", edit: func(_ *testing.T, rows *[]localstore.WorkspaceOperationAuditRecord) {
			(*rows)[0].CreatedAt = (*rows)[0].CreatedAt.In(time.FixedZone("offset", 3600))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestorePlanFixture(t)
			rows := []localstore.WorkspaceOperationAuditRecord{cloneRestoreAuditRecord(fixture.retry.Operations[0])}
			test.edit(t, &rows)
			got, err := validateRestoreAudit(rows)
			if !errors.Is(err, ErrStashOperationMismatch) || !strings.Contains(err.Error(), test.wantError) || got != nil {
				t.Fatalf("validateRestoreAudit()=(%+v,%v), want nil matching %q", got, err, test.wantError)
			}
		})
	}
}

func TestComposeRestoreCurrentAcceptedDirectAndRebasedStartsWithEmptyOrSparseSuffix(t *testing.T) {
	for _, shape := range []string{"accepted-empty", "accepted-sparse", "direct-empty", "direct-sparse", "rebased-empty", "rebased-sparse"} {
		t.Run(shape, func(t *testing.T) {
			fixture := newRestorePlanFixture(t)
			want := configureRestoreCurrent(t, &fixture, shape)
			got, err := composeRestoreCurrent(fixture.retry)
			if err != nil {
				t.Fatal(err)
			}
			if !equalRestorePlanSnapshots(t, got.DirectSnapshot, want.direct) ||
				!equalRestorePlanSnapshots(t, got.Snapshot, want.composed) ||
				got.ThroughGeneration != want.through || got.ActiveRows == nil || len(got.ActiveRows) != want.activeCount {
				t.Fatalf("current %s proof=%+v, want through=%d active=%d", shape, got, want.through, want.activeCount)
			}
		})
	}
}

func TestComposeRestoreCurrentRejectsWrongSideRowsAndNilAudit(t *testing.T) {
	for _, test := range []struct {
		name      string
		wantError string
		edit      func(*testing.T, *restorePlanFixture)
	}{
		{name: "active at boundary", wantError: "active row at or below current boundary", edit: func(_ *testing.T, f *restorePlanFixture) {
			for index := range f.retry.Operations {
				if f.retry.Operations[index].Generation == 4 {
					f.retry.Operations[index].State = "active"
					return
				}
			}
		}},
		{name: "rebased above boundary", wantError: "rebased row above current boundary", edit: func(t *testing.T, f *restorePlanFixture) {
			op := servicePutTaskOperation(*f.retry.Candidate.RebasedSnapshot, "90000000-0000-4000-8000-000000000008", "80000000-0000-4000-8000-000000000008", "wrong side")
			f.retry.Operations = append(f.retry.Operations, restoreAuditRow(t, 8, op, "rebased", ""))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestorePlanFixture(t)
			configureRestoreCurrent(t, &fixture, "rebased-empty")
			test.edit(t, &fixture)
			sortRestoreAudit(fixture.retry.Operations)
			got, err := composeRestoreCurrent(fixture.retry)
			if !errors.Is(err, ErrStashOperationMismatch) || !strings.Contains(err.Error(), test.wantError) || !reflect.DeepEqual(got, currentProof{}) {
				t.Fatalf("composeRestoreCurrent()=(%+v,%v)", got, err)
			}
		})
	}
	fixture := newRestorePlanFixture(t)
	fixture.retry.Operations = nil
	if got, err := composeRestoreCurrent(fixture.retry); !errors.Is(err, ErrStashOperationMismatch) || !reflect.DeepEqual(got, currentProof{}) {
		t.Fatalf("nil audit composeRestoreCurrent()=(%+v,%v)", got, err)
	}
}

type restoreCurrentWant struct {
	direct, composed state.Snapshot
	through          int64
	activeCount      int
}

func configureRestoreCurrent(t *testing.T, fixture *restorePlanFixture, shape string) restoreCurrentWant {
	t.Helper()
	accepted := fixture.source
	direct := accepted
	start := accepted
	boundary := int64(0)
	var candidate *localstore.WorkspaceCandidateRecord
	if strings.HasPrefix(shape, "direct") || strings.HasPrefix(shape, "rebased") {
		direct = composeCloneSnapshot(t, accepted)
		direct.Project.Name = "current direct"
		direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
		direct = stashPlanRefreshSnapshot(t, direct)
		candidate = &localstore.WorkspaceCandidateRecord{
			AcceptedBaseDigest: accepted.Digest, WorkingTreeDigest: direct.Digest, DirectSnapshot: direct,
			ImportedBy: "88888888-8888-4888-8888-888888888888", ImportedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
		}
		start = direct
	}
	if strings.HasPrefix(shape, "rebased") {
		rebasedOperation := servicePutTaskOperation(direct, "90000000-0000-4000-8000-000000000004", "80000000-0000-4000-8000-000000000004", "current rebased")
		rebased, err := state.ApplyOperation(direct, rebasedOperation)
		if err != nil {
			t.Fatal(err)
		}
		candidate.RebasedSnapshot = &rebased
		candidate.RebasedThroughGeneration = 4
		fixture.retry.Operations = append(fixture.retry.Operations, restoreAuditRow(t, 4, rebasedOperation, "rebased", ""))
		start, boundary = rebased, 4
	}
	activeCount := 0
	through := boundary
	composed := start
	if strings.HasSuffix(shape, "sparse") {
		operation := servicePutTaskOperation(start, "90000000-0000-4000-8000-000000000020", "80000000-0000-4000-8000-000000000020", "current suffix")
		fixture.retry.Operations = append(fixture.retry.Operations, restoreAuditRow(t, 20, operation, "active", ""))
		view, err := Compose(start, boundary, []StoredOperation{{Generation: 20, Operation: operation}})
		if err != nil {
			t.Fatal(err)
		}
		composed, through, activeCount = view.Snapshot, 20, 1
	}
	fixture.retry.Candidate = candidate
	sortRestoreAudit(fixture.retry.Operations)
	return restoreCurrentWant{direct: direct, composed: composed, through: through, activeCount: activeCount}
}

func TestBuildRestorePlanOwnsAllNestedResults(t *testing.T) {
	fixture := newRestoreShapeFixture(t, "rebased-sparse")
	current := composeCloneSnapshot(t, fixture.source)
	current.Tasks[composeTaskID].Value.Title = "current title"
	current.Tasks[composeTaskID].Value.UpdatedAt = current.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	current = stashPlanRefreshSnapshot(t, current)
	fixture.retry.Workspace.Snapshot = current
	fixture.retry.Workspace.Binding.AcceptedTreeDigest = string(current.Digest)
	active := servicePutTaskOperation(current, "90000000-0000-4000-8000-000000000020", "80000000-0000-4000-8000-000000000020", "current active")
	fixture.retry.Operations = append(fixture.retry.Operations, restoreAuditRow(t, 20, active, "active", ""))
	sortRestoreAudit(fixture.retry.Operations)
	before := cloneRestoreRetryState(t, fixture.retry)
	got, err := buildRestorePlan(fixture.retry)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Stash.Replay.AbsorbedOperations) != 1 || len(got.Stash.Replay.Operations) != 1 ||
		len(got.Stash.AbsorbedRows) != 1 || len(got.Stash.LaterRows) != 1 || len(got.Current.ActiveRows) != 1 ||
		len(got.Result.Conflicts) == 0 || len(got.ConflictEvidence) == 0 {
		t.Fatalf("ownership fixture did not exercise every nested surface: %+v", got)
	}
	got.Stash.SourceBase.Project.Name = "mutated source proof"
	got.Stash.Composed.Project.Name = "mutated composed proof"
	got.Stash.Replay.SelectedStartTree[0].Data[0] ^= 1
	got.Stash.Replay.AbsorbedOperations[0].Operation.ID = "mutated absorbed replay"
	got.Stash.Replay.Operations[0].Operation.ID = "mutated"
	*got.Stash.AbsorbedRows[0].StashedByStashID = "mutated absorbed owner"
	got.Stash.AbsorbedRows[0].OperationJSON[0] ^= 1
	*got.Stash.LaterRows[0].StashedByStashID = "mutated later owner"
	got.Stash.LaterRows[0].OperationJSON[0] ^= 1
	got.Current.DirectSnapshot.Project.Name = "mutated current direct"
	got.Current.Snapshot.Project.Name = "mutated current composed"
	got.Current.ActiveRows[0].OperationJSON[0] ^= 1
	got.Result.Conflicts[0].Ours.Value[0] ^= 1
	got.ConflictEvidence[0].OursJSON = "mutated evidence"
	if !reflect.DeepEqual(fixture.retry, before) {
		t.Fatal("mutating restore plan changed input state")
	}

	cleanFixture := newRestorePlanFixture(t)
	cleanBefore := cloneRestoreRetryState(t, cleanFixture.retry)
	clean, err := buildRestorePlan(cleanFixture.retry)
	if err != nil || clean.MergedSnapshot == nil {
		t.Fatalf("clean buildRestorePlan()=(%+v,%v)", clean, err)
	}
	clean.MergedSnapshot.Project.Name = "mutated merged result"
	if !reflect.DeepEqual(cleanFixture.retry, cleanBefore) {
		t.Fatal("mutating the clean merged snapshot changed input state")
	}
}

type restorePlanFixture struct {
	retry            localstore.WorkspaceRestoreRetryState
	source, composed state.Snapshot
	absorbed, later  []localstore.WorkspaceOperationAuditRecord
}

func newRestorePlanFixture(t *testing.T) restorePlanFixture {
	return newRestoreShapeFixture(t, "accepted-sparse")
}

func newRestoreShapeFixture(t *testing.T, shape string) restorePlanFixture {
	t.Helper()
	source := composeFixtureSnapshot(t)
	stashBinding := stashPlanBinding(source)
	var candidate *localstore.WorkspaceCandidateRecord
	rows := []localstore.WorkspaceOperation{}
	switch shape {
	case "direct-empty", "direct-sparse":
		direct := composeCloneSnapshot(t, source)
		direct.Project.Name = "direct project"
		direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
		direct = stashPlanRefreshSnapshot(t, direct)
		candidate = &localstore.WorkspaceCandidateRecord{AcceptedBaseDigest: source.Digest, WorkingTreeDigest: direct.Digest, DirectSnapshot: direct}
		if shape == "direct-sparse" {
			op := composeTaskOperation(direct, "90000000-0000-4000-8000-000000000007", func(task *state.TaskV1) {
				task.Title = "stashed title"
				task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
			})
			rows = append(rows, stashPlanOperationRow(t, 7, op, "active"))
		}
	case "rebased-empty", "rebased-sparse":
		var binding types.WorkspaceBinding
		candidateSource, candidateBinding, rebasedCandidate, absorbed := stashPlanRebasedFixture(t, 4)
		source, binding, candidate = candidateSource, candidateBinding, rebasedCandidate
		stashBinding = binding
		rows = append(rows, stashPlanOperationRow(t, 4, absorbed, "rebased"))
		if shape == "rebased-sparse" {
			op := composeTaskOperation(*candidate.RebasedSnapshot, "90000000-0000-4000-8000-000000000009", func(task *state.TaskV1) {
				task.Title = "stashed title"
				task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
			})
			rows = append(rows, stashPlanOperationRow(t, 9, op, "active"))
		}
	case "accepted-empty":
	case "accepted-sparse":
		op := composeTaskOperation(source, "90000000-0000-4000-8000-000000000007", func(task *state.TaskV1) {
			task.Title = "stashed title"
			task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
		})
		rows = append(rows, stashPlanOperationRow(t, 7, op, "active"))
	default:
		t.Fatalf("unknown restore shape %q", shape)
	}
	plan, err := buildStashPlan(stashBinding, source, candidate, rows)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := state.DecodeTree(plan.ComposedTree)
	if err != nil {
		t.Fatal(err)
	}
	const stashID = "20000000-0000-4000-8000-000000000001"
	absorbed := restoreOwnedAuditRows(plan.AbsorbedRows, stashID)
	later := restoreOwnedAuditRows(plan.LaterRows, stashID)
	operations := append([]localstore.WorkspaceOperationAuditRecord{}, absorbed...)
	operations = append(operations, later...)
	actor := diffActorEnvelope()
	actorJSON, err := state.CanonicalJSON(actor)
	if err != nil {
		t.Fatal(err)
	}
	retryBinding := stashBinding
	retryBinding.AcceptedTreeDigest = string(source.Digest)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	retry := localstore.WorkspaceRestoreRetryState{
		Workspace:        localstore.WorkspaceRecord{Binding: retryBinding, Snapshot: source, State: "clean"},
		BindingCreatedAt: now, BindingUpdatedAt: now,
		AcceptedSnapshotBlobDigest: source.Digest,
		Operations:                 operations,
		Stash: localstore.WorkspaceStashRecord{
			StashID: stashID, SourceBaseDigest: plan.SourceDigest, CandidateDigest: plan.CandidateDigest,
			SourceTree: cloneCheckpointTree(plan.SourceTree), ComposedTree: cloneCheckpointTree(plan.ComposedTree),
			OperationsJSON: plan.OperationsJSON, ThroughGeneration: plan.ThroughGeneration,
			Actor: actor, ActorJSON: string(actorJSON), Label: "restore plan", CreatedAt: now,
		},
		StashSourceTreeBlobDigest: source.Digest, StashComposedTreeBlobDigest: composed.Digest,
		OpenConflicts: []localstore.WorkspaceConflictOccurrence{},
	}
	return restorePlanFixture{retry: retry, source: source, composed: composed, absorbed: absorbed, later: later}
}

func restoreOwnedAuditRows(rows []localstore.WorkspaceOperation, owner string) []localstore.WorkspaceOperationAuditRecord {
	result := make([]localstore.WorkspaceOperationAuditRecord, len(rows))
	for index, row := range rows {
		row = cloneImportOperation(row)
		row.State = "stashed"
		owned := owner
		row.StashedByStashID = &owned
		result[index] = localstore.WorkspaceOperationAuditRecord{WorkspaceOperation: row, CreatedAt: time.Date(2026, 7, 29, 11, index, 0, 0, time.UTC)}
	}
	return result
}

func restoreAuditRow(t *testing.T, generation int64, operation state.OperationV1, rowState, owner string) localstore.WorkspaceOperationAuditRecord {
	t.Helper()
	row := stashPlanOperationRow(t, generation, operation, rowState)
	if owner != "" {
		row.StashedByStashID = &owner
	}
	return localstore.WorkspaceOperationAuditRecord{WorkspaceOperation: row, CreatedAt: time.Date(2026, 7, 29, 10, 0, int(generation), 0, time.UTC)}
}

func canonicalRestoreOperation(t *testing.T, operation state.OperationV1) []byte {
	t.Helper()
	raw, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func cloneRestoreAuditRecord(value localstore.WorkspaceOperationAuditRecord) localstore.WorkspaceOperationAuditRecord {
	value.WorkspaceOperation = cloneImportOperation(value.WorkspaceOperation)
	return value
}

func restoreStringPointer(value string) *string {
	return &value
}

func sortRestoreAudit(rows []localstore.WorkspaceOperationAuditRecord) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Generation < rows[j-1].Generation; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func cloneRestoreRetryState(t *testing.T, value localstore.WorkspaceRestoreRetryState) localstore.WorkspaceRestoreRetryState {
	t.Helper()
	cloned := value
	cloned.Workspace.Snapshot = composeCloneSnapshot(t, value.Workspace.Snapshot)
	cloned.Stash.SourceTree = cloneCheckpointTree(value.Stash.SourceTree)
	cloned.Stash.ComposedTree = cloneCheckpointTree(value.Stash.ComposedTree)
	cloned.Operations = make([]localstore.WorkspaceOperationAuditRecord, len(value.Operations))
	for index := range value.Operations {
		cloned.Operations[index] = cloneRestoreAuditRecord(value.Operations[index])
	}
	if value.OpenConflicts != nil {
		cloned.OpenConflicts = append([]localstore.WorkspaceConflictOccurrence{}, value.OpenConflicts...)
	}
	if value.Candidate != nil {
		candidate, err := cloneImportCandidate(value.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		cloned.Candidate = candidate
	}
	return cloned
}

func equalRestorePlanSnapshots(t *testing.T, left, right state.Snapshot) bool {
	t.Helper()
	leftTree, err := state.EncodeTree(left)
	if err != nil {
		t.Fatal(err)
	}
	rightTree, err := state.EncodeTree(right)
	if err != nil {
		t.Fatal(err)
	}
	return equalCheckpointTree(leftTree, rightTree)
}
