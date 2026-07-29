package projectstate

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestBuildStashPlanNoCandidate(t *testing.T) {
	source := composeFixtureSnapshot(t)
	binding := types.WorkspaceBinding{
		Scope: types.WorkspaceScope{
			ProjectID:   source.Config.ProjectID,
			WorkspaceID: "77777777-7777-4777-8777-777777777777",
		},
		Checkout:           types.CheckoutIdentity{CanonicalPath: "/checkout", Device: 1, Inode: 2},
		Repository:         source.Config.Repository,
		AcceptedRef:        "refs/heads/main",
		AcceptedCommitSHA:  strings.Repeat("a", 40),
		AcceptedTreeDigest: string(source.Digest),
	}

	got, err := buildStashPlan(
		binding,
		source,
		nil,
		[]localstore.WorkspaceOperation{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceDigest != source.Digest || got.CandidateDigest != source.Digest {
		t.Fatalf("plan digests = source %q candidate %q, want %q", got.SourceDigest, got.CandidateDigest, source.Digest)
	}
	if got.ThroughGeneration != 0 || got.OperationCount != 0 {
		t.Fatalf("plan replay metadata = generation %d count %d, want zero", got.ThroughGeneration, got.OperationCount)
	}
}

func TestBuildStashPlanDirectCandidate(t *testing.T) {
	source := composeFixtureSnapshot(t)
	binding := stashPlanBinding(source)
	direct := composeCloneSnapshot(t, source)
	direct.Project.Name = "Imported Wormhole"
	direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
	direct = stashPlanRefreshSnapshot(t, direct)
	candidate := &localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest: source.Digest,
		WorkingTreeDigest:  direct.Digest,
		DirectSnapshot:     direct,
		ImportedBy:         "88888888-8888-4888-8888-888888888888",
		ImportedAt:         time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC),
	}

	got, err := buildStashPlan(
		binding,
		source,
		candidate,
		[]localstore.WorkspaceOperation{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.SourceTree, mustEncodeStashPlanTree(t, source)) {
		t.Fatal("plan source tree does not preserve the accepted snapshot")
	}
	if !reflect.DeepEqual(got.ComposedTree, mustEncodeStashPlanTree(t, direct)) {
		t.Fatal("plan composed tree does not select the direct candidate")
	}
	if got.SourceDigest != source.Digest || got.CandidateDigest != direct.Digest {
		t.Fatalf("plan digests = source %q candidate %q, want %q and %q", got.SourceDigest, got.CandidateDigest, source.Digest, direct.Digest)
	}
	if got.ThroughGeneration != 0 || got.OperationCount != 0 {
		t.Fatalf("plan replay metadata = generation %d count %d, want zero", got.ThroughGeneration, got.OperationCount)
	}

	replay, err := decodeStashReplay(got.OperationsJSON, binding, got.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay.SelectedStartTree, mustEncodeStashPlanTree(t, direct)) || replay.SelectedStartDigest != direct.Digest {
		t.Fatal("replay does not select the direct candidate")
	}
	if replay.InitialThroughGeneration != 0 || replay.AbsorbedOperations == nil || replay.Operations == nil {
		t.Fatalf("replay direct-candidate metadata = boundary %d absorbed %#v later %#v", replay.InitialThroughGeneration, replay.AbsorbedOperations, replay.Operations)
	}
	if len(replay.AbsorbedOperations) != 0 || len(replay.Operations) != 0 {
		t.Fatalf("replay operations = absorbed %d later %d, want zero", len(replay.AbsorbedOperations), len(replay.Operations))
	}
}

func TestBuildStashPlanAcceptedSourceWithActiveSuffix(t *testing.T) {
	source := composeFixtureSnapshot(t)
	binding := stashPlanBinding(source)
	operation := composeTaskOperation(source, "99999999-9999-4999-8999-999999999981", func(task *state.TaskV1) {
		task.Description = "active from accepted source"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	row := stashPlanOperationRow(t, 3, operation, "active")
	want, err := Compose(source, 0, []StoredOperation{{Generation: 3, Operation: operation}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := buildStashPlan(binding, source, nil, []localstore.WorkspaceOperation{row})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.SourceTree, mustEncodeStashPlanTree(t, source)) || got.SourceDigest != source.Digest {
		t.Fatal("plan source evidence does not preserve the accepted snapshot")
	}
	if !reflect.DeepEqual(got.ComposedTree, mustEncodeStashPlanTree(t, want.Snapshot)) || got.CandidateDigest != want.Snapshot.Digest {
		t.Fatal("plan candidate evidence does not compose the accepted-source suffix")
	}
	if got.ThroughGeneration != 3 || got.OperationCount != 1 {
		t.Fatalf("plan replay metadata = generation %d count %d, want 3 and 1", got.ThroughGeneration, got.OperationCount)
	}
	if len(got.AbsorbedRows) != 0 || !reflect.DeepEqual(got.LaterRows, []localstore.WorkspaceOperation{row}) {
		t.Fatalf("plan row membership = absorbed %#v later %#v", got.AbsorbedRows, got.LaterRows)
	}

	replay, err := decodeStashReplay(got.OperationsJSON, binding, got.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	wantLater := []StoredOperation{{Generation: 3, Operation: operation}}
	if !reflect.DeepEqual(replay.SelectedStartTree, mustEncodeStashPlanTree(t, source)) || replay.SelectedStartDigest != source.Digest {
		t.Fatal("replay does not select the accepted source")
	}
	if replay.InitialThroughGeneration != 0 || replay.AbsorbedOperations == nil || len(replay.AbsorbedOperations) != 0 || !reflect.DeepEqual(replay.Operations, wantLater) {
		t.Fatalf("replay = boundary %d absorbed %#v later %#v", replay.InitialThroughGeneration, replay.AbsorbedOperations, replay.Operations)
	}
}

func TestBuildStashPlanDirectCandidateWithActiveSuffix(t *testing.T) {
	source := composeFixtureSnapshot(t)
	binding := stashPlanBinding(source)
	direct := composeCloneSnapshot(t, source)
	direct.Project.Name = "Imported Wormhole"
	direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
	direct = stashPlanRefreshSnapshot(t, direct)
	candidate := &localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest: source.Digest,
		WorkingTreeDigest:  direct.Digest,
		DirectSnapshot:     direct,
		ImportedBy:         "88888888-8888-4888-8888-888888888888",
		ImportedAt:         time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC),
	}
	operation := composeTaskOperation(direct, "99999999-9999-4999-8999-999999999982", func(task *state.TaskV1) {
		task.Description = "active from direct candidate"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	stored := []StoredOperation{{Generation: 5, Operation: operation}}
	if _, err := Compose(source, 0, stored); err == nil {
		t.Fatal("direct-candidate operation unexpectedly composed from the accepted source")
	}
	want, err := Compose(direct, 0, stored)
	if err != nil {
		t.Fatal(err)
	}
	row := stashPlanOperationRow(t, 5, operation, "active")

	got, err := buildStashPlan(binding, source, candidate, []localstore.WorkspaceOperation{row})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.SourceTree, mustEncodeStashPlanTree(t, source)) || got.SourceDigest != source.Digest {
		t.Fatal("plan source evidence does not preserve the accepted snapshot")
	}
	if !reflect.DeepEqual(got.ComposedTree, mustEncodeStashPlanTree(t, want.Snapshot)) || got.CandidateDigest != want.Snapshot.Digest {
		t.Fatal("plan candidate evidence does not compose from the direct selected start")
	}
	if got.ThroughGeneration != 5 || got.OperationCount != 1 {
		t.Fatalf("plan replay metadata = generation %d count %d, want 5 and 1", got.ThroughGeneration, got.OperationCount)
	}
	if len(got.AbsorbedRows) != 0 || !reflect.DeepEqual(got.LaterRows, []localstore.WorkspaceOperation{row}) {
		t.Fatalf("plan row membership = absorbed %#v later %#v", got.AbsorbedRows, got.LaterRows)
	}

	replay, err := decodeStashReplay(got.OperationsJSON, binding, got.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replay.SelectedStartTree, mustEncodeStashPlanTree(t, direct)) || replay.SelectedStartDigest != direct.Digest {
		t.Fatal("replay does not select the direct candidate")
	}
	if replay.InitialThroughGeneration != 0 || replay.AbsorbedOperations == nil || len(replay.AbsorbedOperations) != 0 || !reflect.DeepEqual(replay.Operations, stored) {
		t.Fatalf("replay = boundary %d absorbed %#v later %#v", replay.InitialThroughGeneration, replay.AbsorbedOperations, replay.Operations)
	}
}

func TestBuildStashPlanRebasedCandidateWithEmptySuffix(t *testing.T) {
	source, binding, candidate, absorbed := stashPlanRebasedFixture(t, 4)

	got, err := buildStashPlan(
		binding,
		source,
		candidate,
		[]localstore.WorkspaceOperation{stashPlanOperationRow(t, 4, absorbed, "rebased")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.SourceTree, mustEncodeStashPlanTree(t, source)) {
		t.Fatal("plan source tree does not preserve the accepted snapshot")
	}
	if !reflect.DeepEqual(got.ComposedTree, mustEncodeStashPlanTree(t, *candidate.RebasedSnapshot)) {
		t.Fatal("plan composed tree does not select the rebased candidate")
	}
	if got.SourceDigest != source.Digest || got.CandidateDigest != candidate.RebasedSnapshot.Digest {
		t.Fatalf("plan digests = source %q candidate %q, want %q and %q", got.SourceDigest, got.CandidateDigest, source.Digest, candidate.RebasedSnapshot.Digest)
	}
	if got.ThroughGeneration != 4 || got.OperationCount != 1 {
		t.Fatalf("plan replay metadata = generation %d count %d, want 4 and 1", got.ThroughGeneration, got.OperationCount)
	}

	replay, err := decodeStashReplay(got.OperationsJSON, binding, got.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	wantAbsorbed := []StoredOperation{{Generation: 4, Operation: absorbed}}
	if !reflect.DeepEqual(replay.SelectedStartTree, mustEncodeStashPlanTree(t, *candidate.RebasedSnapshot)) || replay.SelectedStartDigest != candidate.RebasedSnapshot.Digest {
		t.Fatal("replay does not select the rebased candidate")
	}
	if replay.InitialThroughGeneration != 4 || !reflect.DeepEqual(replay.AbsorbedOperations, wantAbsorbed) {
		t.Fatalf("replay prefix = boundary %d absorbed %#v, want boundary 4 and %#v", replay.InitialThroughGeneration, replay.AbsorbedOperations, wantAbsorbed)
	}
	if replay.Operations == nil || len(replay.Operations) != 0 {
		t.Fatalf("replay suffix = %#v, want non-nil empty", replay.Operations)
	}
}

func TestBuildStashPlanRebasedCandidateWithSparseSuffix(t *testing.T) {
	source, binding, candidate, absorbed := stashPlanRebasedFixture(t, 4)
	first := composeTaskOperation(*candidate.RebasedSnapshot, "99999999-9999-4999-8999-999999999992", func(task *state.TaskV1) {
		task.Description = "later one"
		task.UpdatedAt = task.UpdatedAt.Add(2 * time.Minute)
	})
	firstView, err := Compose(*candidate.RebasedSnapshot, 4, []StoredOperation{{Generation: 7, Operation: first}})
	if err != nil {
		t.Fatal(err)
	}
	second := composeTaskOperation(firstView.Snapshot, "99999999-9999-4999-8999-999999999993", func(task *state.TaskV1) {
		task.Description = "later two"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	wantView, err := Compose(firstView.Snapshot, 7, []StoredOperation{{Generation: 10, Operation: second}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := buildStashPlan(
		binding,
		source,
		candidate,
		[]localstore.WorkspaceOperation{
			stashPlanOperationRow(t, 4, absorbed, "rebased"),
			stashPlanOperationRow(t, 7, first, "active"),
			stashPlanOperationRow(t, 10, second, "active"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.SourceTree, mustEncodeStashPlanTree(t, source)) || got.SourceDigest != source.Digest {
		t.Fatal("plan source evidence does not preserve the accepted snapshot")
	}
	if !reflect.DeepEqual(got.ComposedTree, mustEncodeStashPlanTree(t, wantView.Snapshot)) || got.CandidateDigest != wantView.Snapshot.Digest {
		t.Fatal("plan candidate evidence does not compose the sparse suffix")
	}
	if got.ThroughGeneration != 10 || got.OperationCount != 3 {
		t.Fatalf("plan replay metadata = generation %d count %d, want 10 and 3", got.ThroughGeneration, got.OperationCount)
	}

	replay, err := decodeStashReplay(got.OperationsJSON, binding, got.ThroughGeneration)
	if err != nil {
		t.Fatal(err)
	}
	wantAbsorbed := []StoredOperation{{Generation: 4, Operation: absorbed}}
	wantLater := []StoredOperation{{Generation: 7, Operation: first}, {Generation: 10, Operation: second}}
	if replay.InitialThroughGeneration != 4 || !reflect.DeepEqual(replay.AbsorbedOperations, wantAbsorbed) || !reflect.DeepEqual(replay.Operations, wantLater) {
		t.Fatalf("replay = boundary %d absorbed %#v later %#v", replay.InitialThroughGeneration, replay.AbsorbedOperations, replay.Operations)
	}
}

func TestBuildStashPlanValidatesAndIgnoresTerminalRows(t *testing.T) {
	fixture := newStashPlanFixture(t)
	want, err := buildStashPlan(fixture.binding, fixture.source, fixture.candidate, fixture.inventory)
	if err != nil {
		t.Fatal(err)
	}
	terminal := func(id, title string) state.OperationV1 {
		return composeTaskOperation(fixture.source, id, func(task *state.TaskV1) {
			task.Title = title
			task.UpdatedAt = task.UpdatedAt.Add(3 * time.Minute)
		})
	}
	ownedStash := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	inventory := []localstore.WorkspaceOperation{
		stashPlanOperationRow(t, 1, terminal("99999999-9999-4999-8999-999999999994", "materialized"), "materialized"),
		stashPlanOperationRow(t, 2, terminal("99999999-9999-4999-8999-999999999995", "stashed"), "stashed"),
		stashPlanOperationRow(t, 3, terminal("99999999-9999-4999-8999-999999999996", "discarded"), "discarded"),
		fixture.inventory[0],
		fixture.inventory[1],
		stashPlanOperationRow(t, 8, terminal("99999999-9999-4999-8999-999999999997", "legacy stashed"), "stashed"),
		fixture.inventory[2],
	}
	inventory[1].StashedByStashID = &ownedStash
	before := cloneStashPlanRows(inventory)

	got, err := buildStashPlan(fixture.binding, fixture.source, fixture.candidate, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal audit rows changed stash membership or replay:\ngot  %+v\nwant %+v", got, want)
	}
	if !reflect.DeepEqual(inventory, before) {
		t.Fatal("buildStashPlan mutated terminal audit rows")
	}
}

func TestBuildStashPlanRejectsRowShapeAndBoundaryCorruption(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, *stashPlanFixture)
	}{
		{name: "nil operation inventory", edit: func(_ *testing.T, fixture *stashPlanFixture) { fixture.inventory = nil }},
		{name: "active row at boundary", edit: func(_ *testing.T, fixture *stashPlanFixture) { fixture.inventory[0].State = "active" }},
		{name: "rebased row above boundary", edit: func(_ *testing.T, fixture *stashPlanFixture) { fixture.inventory[1].State = "rebased" }},
		{name: "absorbed row already owned", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			owner := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			fixture.inventory[0].StashedByStashID = &owner
		}},
		{name: "later row already owned", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			owner := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			fixture.inventory[1].StashedByStashID = &owner
		}},
		{name: "direct candidate has boundary", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.RebasedSnapshot = nil
			fixture.candidate.RebasedThroughGeneration = 4
		}},
		{name: "negative rebase boundary", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.RebasedThroughGeneration = -1
		}},
		{name: "absorbed row exceeds boundary", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[0].Generation = 5
		}},
		{name: "later row does not exceed boundary", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[1].Generation = 4
		}},
		{name: "unordered global inventory", edit: func(t *testing.T, fixture *stashPlanFixture) {
			operation := composeTaskOperation(fixture.source, "99999999-9999-4999-8999-999999999994", func(task *state.TaskV1) {
				task.Title = "earlier absorbed"
				task.UpdatedAt = task.UpdatedAt.Add(2 * time.Minute)
			})
			fixture.inventory = append(fixture.inventory, stashPlanOperationRow(t, 3, operation, "rebased"))
		}},
		{name: "unordered later rows", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[1], fixture.inventory[2] = fixture.inventory[2], fixture.inventory[1]
		}},
		{name: "hidden active row below boundary", edit: func(t *testing.T, fixture *stashPlanFixture) {
			operation := composeTaskOperation(fixture.source, "99999999-9999-4999-8999-999999999994", func(task *state.TaskV1) {
				task.Title = "hidden active"
				task.UpdatedAt = task.UpdatedAt.Add(2 * time.Minute)
			})
			fixture.inventory = append(
				[]localstore.WorkspaceOperation{stashPlanOperationRow(t, 3, operation, "active")},
				fixture.inventory...,
			)
		}},
		{name: "hidden rebased row above boundary", edit: func(t *testing.T, fixture *stashPlanFixture) {
			operation := composeTaskOperation(fixture.source, "99999999-9999-4999-8999-999999999994", func(task *state.TaskV1) {
				task.Title = "hidden rebased"
				task.UpdatedAt = task.UpdatedAt.Add(2 * time.Minute)
			})
			fixture.inventory = append(fixture.inventory, localstore.WorkspaceOperation{})
			copy(fixture.inventory[3:], fixture.inventory[2:])
			fixture.inventory[2] = stashPlanOperationRow(t, 8, operation, "rebased")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStashPlanFixture(t)
			test.edit(t, &fixture)
			got, err := buildStashPlan(
				fixture.binding,
				fixture.source,
				fixture.candidate,
				fixture.inventory,
			)
			if err == nil || !reflect.DeepEqual(got, stashPlan{}) {
				t.Fatalf("buildStashPlan()=(%+v,%v), want zero plan and error", got, err)
			}
		})
	}
}

func TestBuildStashPlanRejectsEvidenceAndOperationCorruption(t *testing.T) {
	otherDigest := state.Digest("sha256:" + strings.Repeat("b", 64))
	tests := []struct {
		name string
		edit func(*testing.T, *stashPlanFixture)
	}{
		{name: "invalid binding", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.binding.Checkout.CanonicalPath = "relative"
		}},
		{name: "binding accepted digest differs", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.binding.AcceptedTreeDigest = string(otherDigest)
		}},
		{name: "binding project differs", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.binding.Scope.ProjectID = "00000000-0000-4000-8000-000000000099"
		}},
		{name: "binding repository differs", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.binding.Repository = types.RepositoryIdentity{
				Provider: "github", ImmutableID: "R_other", CanonicalRemote: "https://github.com/acme/other",
			}
		}},
		{name: "stale source digest", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.source.Digest = otherDigest
		}},
		{name: "candidate accepted base differs", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.AcceptedBaseDigest = otherDigest
		}},
		{name: "stale direct candidate digest", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.DirectSnapshot.Digest = otherDigest
		}},
		{name: "candidate working digest differs", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.WorkingTreeDigest = otherDigest
		}},
		{name: "direct candidate project differs", edit: func(t *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.DirectSnapshot.Config.ProjectID = "00000000-0000-4000-8000-000000000099"
			fixture.candidate.DirectSnapshot.Project.ID = "00000000-0000-4000-8000-000000000099"
			fixture.candidate.DirectSnapshot = stashPlanRefreshSnapshot(t, fixture.candidate.DirectSnapshot)
			fixture.candidate.WorkingTreeDigest = fixture.candidate.DirectSnapshot.Digest
		}},
		{name: "stale rebased candidate digest", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.RebasedSnapshot.Digest = otherDigest
		}},
		{name: "rebased candidate repository differs", edit: func(t *testing.T, fixture *stashPlanFixture) {
			fixture.candidate.RebasedSnapshot.Config.Repository = types.RepositoryIdentity{
				Provider: "github", ImmutableID: "R_other", CanonicalRemote: "https://github.com/acme/other",
			}
			refreshed := stashPlanRefreshSnapshot(t, *fixture.candidate.RebasedSnapshot)
			fixture.candidate.RebasedSnapshot = &refreshed
		}},
		{name: "malformed operation bytes", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[1].OperationJSON = []byte("{")
		}},
		{name: "noncanonical operation bytes", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[1].OperationJSON = append(fixture.inventory[1].OperationJSON, ' ')
		}},
		{name: "operation row identity differs", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[1].OperationID = "99999999-9999-4999-8999-999999999994"
		}},
		{name: "invalid operation row identity", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[1].OperationID = "invalid"
		}},
		{name: "duplicate operation identity across prefix and suffix", edit: func(t *testing.T, fixture *stashPlanFixture) {
			operation, err := state.DecodeOperation(fixture.inventory[1].OperationJSON)
			if err != nil {
				t.Fatal(err)
			}
			operation.ID = fixture.inventory[0].OperationID
			fixture.inventory[1] = stashPlanOperationRow(t, fixture.inventory[1].Generation, operation, "active")
		}},
		{name: "stale suffix precondition", edit: func(t *testing.T, fixture *stashPlanFixture) {
			operation, err := state.DecodeOperation(fixture.inventory[1].OperationJSON)
			if err != nil {
				t.Fatal(err)
			}
			operation.ExpectedViewDigest = otherDigest
			fixture.inventory[1] = stashPlanOperationRow(t, fixture.inventory[1].Generation, operation, "active")
		}},
		{name: "duplicate global generation", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[1].Generation = fixture.inventory[0].Generation
		}},
		{name: "unknown terminal state", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[0].State = "unknown"
		}},
		{name: "invalid stashed owner", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[0].State = "stashed"
			owner := "aaaaaaaa-aaaa-1aaa-8aaa-aaaaaaaaaaaa"
			fixture.inventory[0].StashedByStashID = &owner
		}},
		{name: "owner on materialized row", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[0].State = "materialized"
			owner := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			fixture.inventory[0].StashedByStashID = &owner
		}},
		{name: "malformed terminal operation bytes", edit: func(_ *testing.T, fixture *stashPlanFixture) {
			fixture.inventory[0].State = "discarded"
			fixture.inventory[0].OperationJSON = []byte("{")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStashPlanFixture(t)
			test.edit(t, &fixture)
			got, err := buildStashPlan(
				fixture.binding,
				fixture.source,
				fixture.candidate,
				fixture.inventory,
			)
			if err == nil || !reflect.DeepEqual(got, stashPlan{}) {
				t.Fatalf("buildStashPlan()=(%+v,%v), want zero plan and error", got, err)
			}
		})
	}
}

func TestBuildStashPlanDoesNotMutateOrAliasInputsAndResults(t *testing.T) {
	fixture := newStashPlanFixture(t)
	control := newStashPlanFixture(t)
	first, err := buildStashPlan(
		fixture.binding,
		fixture.source,
		fixture.candidate,
		fixture.inventory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture, control) {
		t.Fatal("buildStashPlan mutated its caller inputs")
	}
	want, err := buildStashPlan(
		control.binding,
		control.source,
		control.candidate,
		control.inventory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatal("equivalent stash planner inputs produced different plans")
	}

	first.SourceTree[0].Data[0] ^= 0xff
	first.ComposedTree[0].Data[0] ^= 0xff
	first.AbsorbedRows[0].OperationJSON[0] ^= 0xff
	first.AbsorbedRows[0].State = "mutated"
	first.LaterRows[0].OperationJSON[0] ^= 0xff
	first.LaterRows[0].Generation = 99
	afterOutputMutation, err := buildStashPlan(
		fixture.binding,
		fixture.source,
		fixture.candidate,
		fixture.inventory,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterOutputMutation, want) || !reflect.DeepEqual(fixture, control) {
		t.Fatal("stash planner result aliases caller inputs or another result")
	}

	fixture.source.Project.Name = "mutated source"
	fixture.candidate.DirectSnapshot.Project.Name = "mutated direct"
	fixture.candidate.RebasedSnapshot.Project.Name = "mutated rebased"
	fixture.inventory[0].OperationJSON[0] ^= 0xff
	fixture.inventory[1].OperationJSON[0] ^= 0xff
	if !reflect.DeepEqual(afterOutputMutation, want) {
		t.Fatal("stash planner result aliases caller-owned snapshot or operation memory")
	}
}

func stashPlanBinding(source state.Snapshot) types.WorkspaceBinding {
	return types.WorkspaceBinding{
		Scope: types.WorkspaceScope{
			ProjectID:   source.Config.ProjectID,
			WorkspaceID: "77777777-7777-4777-8777-777777777777",
		},
		Checkout:           types.CheckoutIdentity{CanonicalPath: "/checkout", Device: 1, Inode: 2},
		Repository:         source.Config.Repository,
		AcceptedRef:        "refs/heads/main",
		AcceptedCommitSHA:  strings.Repeat("a", 40),
		AcceptedTreeDigest: string(source.Digest),
	}
}

func stashPlanRefreshSnapshot(t *testing.T, snapshot state.Snapshot) state.Snapshot {
	t.Helper()
	tree := mustEncodeStashPlanTree(t, snapshot)
	refreshed, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return refreshed
}

func stashPlanRebasedFixture(t *testing.T, boundary int64) (state.Snapshot, types.WorkspaceBinding, *localstore.WorkspaceCandidateRecord, state.OperationV1) {
	t.Helper()
	source := composeFixtureSnapshot(t)
	direct := composeCloneSnapshot(t, source)
	direct.Project.Name = "Imported Wormhole"
	direct.Project.UpdatedAt = direct.Project.UpdatedAt.Add(time.Minute)
	direct = stashPlanRefreshSnapshot(t, direct)
	absorbed := composeTaskOperation(source, "99999999-9999-4999-8999-999999999991", func(task *state.TaskV1) {
		task.Description = "absorbed before import"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	rebased := composeCloneSnapshot(t, direct)
	rebased.Tasks[composeTaskID] = state.Record[state.TaskV1]{Value: absorbed.PutRecord.Record.Task}
	rebased = stashPlanRefreshSnapshot(t, rebased)
	candidate := &localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest:       source.Digest,
		WorkingTreeDigest:        direct.Digest,
		DirectSnapshot:           direct,
		RebasedSnapshot:          &rebased,
		RebasedThroughGeneration: boundary,
		ImportedBy:               "88888888-8888-4888-8888-888888888888",
		ImportedAt:               time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC),
	}
	return source, stashPlanBinding(source), candidate, absorbed
}

func stashPlanOperationRow(t *testing.T, generation int64, operation state.OperationV1, rowState string) localstore.WorkspaceOperation {
	t.Helper()
	raw, err := state.CanonicalOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	return localstore.WorkspaceOperation{
		Generation:    generation,
		OperationID:   operation.ID,
		OperationJSON: raw,
		State:         rowState,
	}
}

func cloneStashPlanRows(rows []localstore.WorkspaceOperation) []localstore.WorkspaceOperation {
	cloned := make([]localstore.WorkspaceOperation, len(rows))
	for index, row := range rows {
		cloned[index] = row
		cloned[index].OperationJSON = append([]byte(nil), row.OperationJSON...)
		if row.StashedByStashID != nil {
			owner := *row.StashedByStashID
			cloned[index].StashedByStashID = &owner
		}
	}
	return cloned
}

type stashPlanFixture struct {
	source    state.Snapshot
	binding   types.WorkspaceBinding
	candidate *localstore.WorkspaceCandidateRecord
	inventory []localstore.WorkspaceOperation
}

func newStashPlanFixture(t *testing.T) stashPlanFixture {
	t.Helper()
	source, binding, candidate, absorbed := stashPlanRebasedFixture(t, 4)
	first := composeTaskOperation(*candidate.RebasedSnapshot, "99999999-9999-4999-8999-999999999992", func(task *state.TaskV1) {
		task.Description = "later one"
		task.UpdatedAt = task.UpdatedAt.Add(2 * time.Minute)
	})
	firstView, err := Compose(*candidate.RebasedSnapshot, 4, []StoredOperation{{Generation: 7, Operation: first}})
	if err != nil {
		t.Fatal(err)
	}
	second := composeTaskOperation(firstView.Snapshot, "99999999-9999-4999-8999-999999999993", func(task *state.TaskV1) {
		task.Description = "later two"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	return stashPlanFixture{
		source:    source,
		binding:   binding,
		candidate: candidate,
		inventory: []localstore.WorkspaceOperation{
			stashPlanOperationRow(t, 4, absorbed, "rebased"),
			stashPlanOperationRow(t, 7, first, "active"),
			stashPlanOperationRow(t, 10, second, "active"),
		},
	}
}

func mustEncodeStashPlanTree(t *testing.T, snapshot state.Snapshot) state.Tree {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
