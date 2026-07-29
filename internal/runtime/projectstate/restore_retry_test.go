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

func TestRestoreRetryPrivateAPIExists(t *testing.T) {
	var persisted localstore.WorkspaceRestoreRetryState
	var requestDigest state.Digest
	if _, err := buildRestoreStashRetryPreimage(RestoreStashRequest{}, requestDigest, persisted); err == nil {
		t.Fatal("zero retry state unexpectedly built a preimage")
	}
	if _, err := restoreStashRetryDigest(RestoreStashRequest{}, requestDigest, persisted); err == nil {
		t.Fatal("zero retry state unexpectedly produced a digest")
	}
	if err := validateConflictedRestoreTransition(persisted, persisted, nil); err == nil {
		t.Fatal("zero retry state unexpectedly passed transition validation")
	}
	var _ restoreStashRetryPreimageV1
}

func TestBuildRestoreStashRetryPreimageProjectsCompletePersistedState(t *testing.T) {
	req, requestDigest, persisted := restoreRetryFixture(t)
	preimage, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}
	if preimage.SchemaVersion != 1 || preimage.Action != "restore" || preimage.Outcome != "conflicted" ||
		preimage.Scope != req.Scope || preimage.RequestID != req.RequestID || preimage.RequestDigest != requestDigest || preimage.StashID != req.StashID {
		t.Fatalf("retry envelope=%+v", preimage)
	}
	if preimage.Binding.Binding.Scope != persisted.Workspace.Binding.Scope ||
		preimage.Binding.Binding.Checkout.CanonicalPath != persisted.Workspace.Binding.Checkout.CanonicalPath ||
		preimage.Binding.Status != "conflicted" || preimage.Binding.AcceptedSnapshotBlobDigest != persisted.AcceptedSnapshotBlobDigest ||
		!preimage.Binding.CreatedAt.Equal(persisted.BindingCreatedAt) || !preimage.Binding.UpdatedAt.Equal(persisted.BindingUpdatedAt) {
		t.Fatalf("retry binding=%+v", preimage.Binding)
	}
	if preimage.Candidate == nil || preimage.Candidate.DirectTreeBlobDigest != *persisted.CandidateDirectTreeBlobDigest ||
		preimage.Candidate.RebasedTreeBlobDigest == nil || *preimage.Candidate.RebasedTreeBlobDigest != *persisted.CandidateRebasedTreeBlobDigest {
		t.Fatalf("retry candidate=%+v", preimage.Candidate)
	}
	if preimage.Operations == nil || len(preimage.Operations) != len(persisted.Operations) ||
		preimage.Operations[0].OperationJSON != string(persisted.Operations[0].OperationJSON) ||
		preimage.Operations[0].StashedByStashID == nil || *preimage.Operations[0].StashedByStashID != *persisted.Operations[0].StashedByStashID {
		t.Fatalf("retry operations=%+v", preimage.Operations)
	}
	if preimage.Stash.ActorJSON != persisted.Stash.ActorJSON || preimage.Stash.OperationsJSON != persisted.Stash.OperationsJSON ||
		preimage.Stash.SourceTreeBlobDigest != persisted.StashSourceTreeBlobDigest || preimage.Stash.ComposedTreeBlobDigest != persisted.StashComposedTreeBlobDigest {
		t.Fatalf("retry stash=%+v", preimage.Stash)
	}
	if preimage.OpenConflicts == nil || len(preimage.OpenConflicts) != 1 ||
		preimage.OpenConflicts[0].OccurrenceID != persisted.OpenConflicts[0].OccurrenceID ||
		preimage.OpenConflicts[0].BaseJSON != persisted.OpenConflicts[0].BaseJSON {
		t.Fatalf("retry conflicts=%+v", preimage.OpenConflicts)
	}

	wantCandidateDigest := preimage.Candidate.DirectTreeBlobDigest
	wantOwner := *preimage.Operations[0].StashedByStashID
	wantOperationJSON := preimage.Operations[0].OperationJSON
	*persisted.CandidateDirectTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("f", 64))
	*persisted.Operations[0].StashedByStashID = "00000000-0000-4000-8000-000000000099"
	persisted.Operations[0].OperationJSON[0] ^= 0xff
	persisted.OpenConflicts[0].BaseJSON = "mutated"
	if preimage.Candidate.DirectTreeBlobDigest != wantCandidateDigest || *preimage.Operations[0].StashedByStashID != wantOwner || preimage.Operations[0].OperationJSON != wantOperationJSON ||
		preimage.OpenConflicts[0].BaseJSON == "mutated" {
		t.Fatal("retry preimage aliases caller-owned state")
	}
}

func TestRestoreStashRetryPreimageRestartEquivalentAndBidirectionallyOwned(t *testing.T) {
	req, requestDigest, persisted := restoreRetryTwoConflictFixture(t)
	persisted.Operations = append(persisted.Operations, restoreAuditRow(t, 10,
		servicePutTaskOperation(persisted.Workspace.Snapshot,
			"90000000-0000-4000-8000-000000000010",
			"80000000-0000-4000-8000-000000000010", "discarded history"),
		"discarded", ""))
	beforeRestart, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := state.CanonicalJSON(beforeRestart)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest, err := restoreStashRetryDigest(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}

	// The localstore package separately proves strict SQLite close/reopen reads.
	// This exact clone represents the strict returned state at this private codec seam.
	reopened := cloneRestoreRetryFixture(t, persisted)
	afterRestart, err := buildRestoreStashRetryPreimage(req, requestDigest, reopened)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := state.CanonicalJSON(afterRestart)
	if err != nil {
		t.Fatal(err)
	}
	afterDigest, err := restoreStashRetryDigest(req, requestDigest, reopened)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened, persisted) || string(afterBytes) != string(beforeBytes) || afterDigest != beforeDigest {
		t.Fatalf("restart-equivalent retry changed state/bytes/digest: bytes_equal=%v digests=%q/%q", string(afterBytes) == string(beforeBytes), beforeDigest, afterDigest)
	}

	inputMutated := cloneRestoreRetryFixture(t, persisted)
	projected, err := buildRestoreStashRetryPreimage(req, requestDigest, inputMutated)
	if err != nil {
		t.Fatal(err)
	}
	wantProjected, err := state.CanonicalJSON(projected)
	if err != nil {
		t.Fatal(err)
	}
	*inputMutated.CandidateRebasedTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("9", 64))
	*inputMutated.Operations[0].StashedByStashID = "00000000-0000-4000-8000-000000000099"
	inputMutated.Operations[0].OperationJSON[0] ^= 1
	inputMutated.Operations = inputMutated.Operations[:1]
	inputMutated.OpenConflicts[0].BaseJSON = "mutated"
	inputMutated.OpenConflicts = inputMutated.OpenConflicts[:1]
	stillProjected, err := state.CanonicalJSON(projected)
	if err != nil || string(stillProjected) != string(wantProjected) {
		t.Fatalf("input mutation changed projected preimage: equal=%v err=%v", string(stillProjected) == string(wantProjected), err)
	}

	ownedInput := cloneRestoreRetryFixture(t, persisted)
	wantInput := cloneRestoreRetryFixture(t, ownedInput)
	ownedProjection, err := buildRestoreStashRetryPreimage(req, requestDigest, ownedInput)
	if err != nil {
		t.Fatal(err)
	}
	*ownedProjection.Candidate.RebasedTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("9", 64))
	*ownedProjection.Operations[0].StashedByStashID = "00000000-0000-4000-8000-000000000099"
	ownedProjection.Operations[0].OperationJSON = "mutated"
	ownedProjection.Operations = ownedProjection.Operations[:1]
	ownedProjection.OpenConflicts[0].BaseJSON = "mutated"
	ownedProjection.OpenConflicts = ownedProjection.OpenConflicts[:1]
	if !reflect.DeepEqual(ownedInput, wantInput) {
		t.Fatal("projected preimage mutation changed caller-owned state")
	}
}

func TestRestoreStashRetryPreimageGolden(t *testing.T) {
	direct := state.Digest("sha256:" + strings.Repeat("d", 64))
	owner := "22222222-2222-4222-8222-222222222222"
	preimage := restoreStashRetryPreimageV1{
		SchemaVersion: 1, Action: "restore", Outcome: "conflicted",
		Scope:     types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "77777777-7777-4777-8777-777777777777"},
		RequestID: "99999999-9999-4999-8999-999999999991", RequestDigest: state.Digest("sha256:" + strings.Repeat("a", 64)),
		StashID: "20000000-0000-4000-8000-000000000001",
		Binding: restoreRetryBindingV1{
			Binding: workspaceBindingDigestV1{
				Scope:      types.WorkspaceScope{ProjectID: "00000000-0000-4000-8000-000000000001", WorkspaceID: "77777777-7777-4777-8777-777777777777"},
				Checkout:   checkoutIdentityDigestV1{CanonicalPath: "/checkout", Device: 1, Inode: 2},
				Repository: types.RepositoryIdentity{}, AcceptedRef: "refs/heads/main",
				AcceptedCommitSHA: strings.Repeat("b", 40), AcceptedTreeDigest: "sha256:" + strings.Repeat("c", 64),
			},
			Status: "conflicted", CreatedAt: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC), AcceptedSnapshotBlobDigest: state.Digest("sha256:" + strings.Repeat("1", 64)),
		},
		Candidate: &restoreRetryCandidateV1{
			AcceptedBaseDigest: state.Digest("sha256:" + strings.Repeat("c", 64)),
			WorkingTreeDigest:  state.Digest("sha256:" + strings.Repeat("e", 64)), DirectTreeBlobDigest: state.Digest("sha256:" + strings.Repeat("2", 64)),
			RebasedTreeBlobDigest: &direct, RebasedThroughGeneration: 4,
			ImportedBy: "00000000-0000-4000-8000-000000000061", ImportedAt: time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC),
		},
		Operations: []restoreRetryOperationV1{{
			Generation: 4, OperationID: "99999999-9999-4999-8999-999999999991", OperationJSON: "{}\n",
			State: "stashed", StashedByStashID: &owner, CreatedAt: time.Date(2026, 7, 29, 10, 30, 0, 0, time.UTC),
		}},
		Stash: restoreRetryStashV1{
			StashID:          "20000000-0000-4000-8000-000000000001",
			SourceBaseDigest: state.Digest("sha256:" + strings.Repeat("c", 64)), CandidateDigest: state.Digest("sha256:" + strings.Repeat("f", 64)),
			SourceTreeBlobDigest: state.Digest("sha256:" + strings.Repeat("4", 64)), ComposedTreeBlobDigest: state.Digest("sha256:" + strings.Repeat("5", 64)),
			OperationsJSON: "{}\n", ThroughGeneration: 4, ActorJSON: "{}\n", Label: "branch work",
			CreatedAt: time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC),
		},
		OpenConflicts: []restoreRetryConflictOccurrenceV1{{
			OccurrenceID: "00000000-0000-4000-8000-000000000071", ConflictID: "sha256:" + strings.Repeat("6", 64),
			RecordKind: "task", RecordID: "22222222-2222-4222-8222-222222222222", FieldPath: "/title", ConflictKind: "same_field",
			BaseJSON: "{\"present\":true}\n", OursJSON: "{\"present\":true}\n", TheirsJSON: "{\"present\":true}\n",
			CreatedAt: time.Date(2026, 7, 29, 11, 30, 0, 0, time.UTC),
		}},
	}
	canonical, err := state.CanonicalJSON(preimage)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestCanonicalJSON(preimage)
	if err != nil {
		t.Fatal(err)
	}
	const wantCanonical = `{"schema_version":1,"action":"restore","outcome":"conflicted","scope":{"project_id":"00000000-0000-4000-8000-000000000001","workspace_id":"77777777-7777-4777-8777-777777777777"},"request_id":"99999999-9999-4999-8999-999999999991","request_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","stash_id":"20000000-0000-4000-8000-000000000001","binding":{"binding":{"scope":{"project_id":"00000000-0000-4000-8000-000000000001","workspace_id":"77777777-7777-4777-8777-777777777777"},"checkout":{"canonical_path":"/checkout","device":1,"inode":2},"repository":{"provider":"","immutable_id":"","canonical_remote":""},"accepted_ref":"refs/heads/main","accepted_commit_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","accepted_tree_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"status":"conflicted","created_at":"2026-07-29T09:00:00Z","updated_at":"2026-07-29T12:00:00Z","accepted_snapshot_blob_digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"},"candidate":{"accepted_base_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","working_tree_digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","direct_tree_blob_digest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","rebased_tree_blob_digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","rebased_through_generation":4,"imported_by":"00000000-0000-4000-8000-000000000061","imported_at":"2026-07-29T10:00:00Z"},"operations":[{"generation":4,"operation_id":"99999999-9999-4999-8999-999999999991","operation_json":"{}\n","state":"stashed","stashed_by_stash_id":"22222222-2222-4222-8222-222222222222","created_at":"2026-07-29T10:30:00Z"}],"stash":{"stash_id":"20000000-0000-4000-8000-000000000001","source_base_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","candidate_digest":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","source_tree_blob_digest":"sha256:4444444444444444444444444444444444444444444444444444444444444444","composed_tree_blob_digest":"sha256:5555555555555555555555555555555555555555555555555555555555555555","operations_json":"{}\n","through_generation":4,"actor_json":"{}\n","label":"branch work","created_at":"2026-07-29T11:00:00Z"},"open_conflicts":[{"occurrence_id":"00000000-0000-4000-8000-000000000071","conflict_id":"sha256:6666666666666666666666666666666666666666666666666666666666666666","record_kind":"task","record_id":"22222222-2222-4222-8222-222222222222","field_path":"/title","conflict_kind":"same_field","base_json":"{\"present\":true}\n","ours_json":"{\"present\":true}\n","theirs_json":"{\"present\":true}\n","created_at":"2026-07-29T11:30:00Z"}]}` + "\n"
	if string(canonical) != wantCanonical {
		t.Fatalf("canonical retry preimage=%q, want %q", canonical, wantCanonical)
	}
	const wantDigest = state.Digest("sha256:470fa615abd8fe11bc45777f8ab0074d6a5336962a50160d05fa9e7bb36fbd25")
	if digest != wantDigest {
		t.Fatalf("retry preimage digest=%q, want %q", digest, wantDigest)
	}
	req, requestDigest, persisted := restoreRetryFixture(t)
	got, err := restoreStashRetryDigest(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}
	const wantBuiltDigest = state.Digest("sha256:3f4ae0060a5bd184a3917a8ed89b0dbdc4efd2e34475567282e481be0b8fff14")
	if got != wantBuiltDigest {
		t.Fatalf("built retry digest=%q, want hard-coded %q", got, wantBuiltDigest)
	}
}

func TestRestoreStashRetryPreimageDigestCoversEveryField(t *testing.T) {
	req, requestDigest, persisted := restoreRetryFixture(t)
	base, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := state.DigestCanonicalJSON(base)
	if err != nil {
		t.Fatal(err)
	}
	digest := func(char string) state.Digest { return state.Digest("sha256:" + strings.Repeat(char, 64)) }
	otherTime := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name string
		edit func(*restoreStashRetryPreimageV1)
	}{
		{"schema version", func(v *restoreStashRetryPreimageV1) { v.SchemaVersion++ }},
		{"action", func(v *restoreStashRetryPreimageV1) { v.Action = "changed" }},
		{"outcome", func(v *restoreStashRetryPreimageV1) { v.Outcome = "changed" }},
		{"scope project", func(v *restoreStashRetryPreimageV1) { v.Scope.ProjectID = "changed" }},
		{"scope workspace", func(v *restoreStashRetryPreimageV1) { v.Scope.WorkspaceID = "changed" }},
		{"request ID", func(v *restoreStashRetryPreimageV1) { v.RequestID = "changed" }},
		{"request digest", func(v *restoreStashRetryPreimageV1) { v.RequestDigest = digest("9") }},
		{"stash ID", func(v *restoreStashRetryPreimageV1) { v.StashID = "changed" }},
		{"binding scope project", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Scope.ProjectID = "changed" }},
		{"binding scope workspace", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Scope.WorkspaceID = "changed" }},
		{"checkout path", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Checkout.CanonicalPath = "/changed" }},
		{"checkout device", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Checkout.Device++ }},
		{"checkout inode", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Checkout.Inode++ }},
		{"repository provider", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Repository.Provider = "changed" }},
		{"repository immutable ID", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Repository.ImmutableID = "changed" }},
		{"repository remote", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.Repository.CanonicalRemote = "changed" }},
		{"accepted ref", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.AcceptedRef = "changed" }},
		{"accepted commit", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.AcceptedCommitSHA = "changed" }},
		{"accepted tree digest", func(v *restoreStashRetryPreimageV1) { v.Binding.Binding.AcceptedTreeDigest = "changed" }},
		{"binding status", func(v *restoreStashRetryPreimageV1) { v.Binding.Status = "changed" }},
		{"binding created", func(v *restoreStashRetryPreimageV1) { v.Binding.CreatedAt = otherTime }},
		{"binding updated", func(v *restoreStashRetryPreimageV1) { v.Binding.UpdatedAt = otherTime }},
		{"accepted blob digest", func(v *restoreStashRetryPreimageV1) { v.Binding.AcceptedSnapshotBlobDigest = digest("9") }},
		{"candidate accepted digest", func(v *restoreStashRetryPreimageV1) { v.Candidate.AcceptedBaseDigest = digest("9") }},
		{"candidate working digest", func(v *restoreStashRetryPreimageV1) { v.Candidate.WorkingTreeDigest = digest("9") }},
		{"candidate direct blob", func(v *restoreStashRetryPreimageV1) { v.Candidate.DirectTreeBlobDigest = digest("9") }},
		{"candidate rebased blob", func(v *restoreStashRetryPreimageV1) { value := digest("9"); v.Candidate.RebasedTreeBlobDigest = &value }},
		{"candidate boundary", func(v *restoreStashRetryPreimageV1) { v.Candidate.RebasedThroughGeneration++ }},
		{"candidate importer", func(v *restoreStashRetryPreimageV1) { v.Candidate.ImportedBy = "changed" }},
		{"candidate imported", func(v *restoreStashRetryPreimageV1) { v.Candidate.ImportedAt = otherTime }},
		{"operation generation", func(v *restoreStashRetryPreimageV1) { v.Operations[0].Generation++ }},
		{"operation ID", func(v *restoreStashRetryPreimageV1) { v.Operations[0].OperationID = "changed" }},
		{"operation JSON", func(v *restoreStashRetryPreimageV1) { v.Operations[0].OperationJSON = "changed" }},
		{"operation state", func(v *restoreStashRetryPreimageV1) { v.Operations[0].State = "changed" }},
		{"operation owner", func(v *restoreStashRetryPreimageV1) { owner := "changed"; v.Operations[0].StashedByStashID = &owner }},
		{"operation created", func(v *restoreStashRetryPreimageV1) { v.Operations[0].CreatedAt = otherTime }},
		{"stash row ID", func(v *restoreStashRetryPreimageV1) { v.Stash.StashID = "changed" }},
		{"stash source digest", func(v *restoreStashRetryPreimageV1) { v.Stash.SourceBaseDigest = digest("9") }},
		{"stash candidate digest", func(v *restoreStashRetryPreimageV1) { v.Stash.CandidateDigest = digest("9") }},
		{"stash source blob", func(v *restoreStashRetryPreimageV1) { v.Stash.SourceTreeBlobDigest = digest("9") }},
		{"stash composed blob", func(v *restoreStashRetryPreimageV1) { v.Stash.ComposedTreeBlobDigest = digest("9") }},
		{"stash operations", func(v *restoreStashRetryPreimageV1) { v.Stash.OperationsJSON = "changed" }},
		{"stash boundary", func(v *restoreStashRetryPreimageV1) { v.Stash.ThroughGeneration++ }},
		{"stash actor", func(v *restoreStashRetryPreimageV1) { v.Stash.ActorJSON = "changed" }},
		{"stash label", func(v *restoreStashRetryPreimageV1) { v.Stash.Label = "changed" }},
		{"stash created", func(v *restoreStashRetryPreimageV1) { v.Stash.CreatedAt = otherTime }},
		{"occurrence ID", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].OccurrenceID = "changed" }},
		{"conflict ID", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].ConflictID = "changed" }},
		{"record kind", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].RecordKind = "changed" }},
		{"record ID", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].RecordID = "changed" }},
		{"field path", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].FieldPath = "changed" }},
		{"conflict kind", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].ConflictKind = "changed" }},
		{"base JSON", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].BaseJSON = "changed" }},
		{"ours JSON", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].OursJSON = "changed" }},
		{"theirs JSON", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].TheirsJSON = "changed" }},
		{"conflict created", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts[0].CreatedAt = otherTime }},
		{"candidate null", func(v *restoreStashRetryPreimageV1) { v.Candidate = nil }},
		{"operations null", func(v *restoreStashRetryPreimageV1) { v.Operations = nil }},
		{"operations empty", func(v *restoreStashRetryPreimageV1) { v.Operations = []restoreRetryOperationV1{} }},
		{"conflicts null", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts = nil }},
		{"conflicts empty", func(v *restoreStashRetryPreimageV1) { v.OpenConflicts = []restoreRetryConflictOccurrenceV1{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fresh, err := buildRestoreStashRetryPreimage(req, requestDigest, cloneRestoreRetryFixture(t, persisted))
			if err != nil {
				t.Fatal(err)
			}
			test.edit(&fresh)
			got, err := state.DigestCanonicalJSON(fresh)
			if err != nil {
				t.Fatal(err)
			}
			if got == baseDigest {
				t.Fatalf("field drift did not change digest: %+v", fresh)
			}
		})
	}
}

func TestRestoreStashRetryDigestBindsEveryPersistedAndRequestField(t *testing.T) {
	req, requestDigest, persisted := restoreRetryFixtureWithTerminal(t)
	baseDigest, err := restoreStashRetryDigest(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}
	otherDigest := func(char string) state.Digest { return state.Digest("sha256:" + strings.Repeat(char, 64)) }
	otherTime := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	validID := "00000000-0000-4000-8000-000000000099"
	tests := []struct {
		name string
		edit func(*RestoreStashRequest, *state.Digest, *localstore.WorkspaceRestoreRetryState)
	}{
		{"supplied request digest", func(_ *RestoreStashRequest, digest *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			*digest = otherDigest("9")
		}},
		{"request scope project", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Scope.ProjectID = validID
		}},
		{"request scope workspace", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Scope.WorkspaceID = types.WorkspaceID(validID)
		}},
		{"request ID", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.RequestID = validID
		}},
		{"request stash ID", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.StashID = validID
		}},
		{"request actor kind", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.ActorKind = types.ActorAgent
		}},
		{"request human principal", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.HumanPrincipalID = validID
		}},
		{"request agent ID", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.AgentID = validID
		}},
		{"request accountable human", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.AccountableHumanID = validID
		}},
		{"request session", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.SessionID = "changed"
		}},
		{"request harness name", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.HarnessName = "changed"
		}},
		{"request harness version", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.HarnessVersion = "changed"
		}},
		{"request model name", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.ModelName = "changed"
		}},
		{"request model version", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.ModelVersion = "changed"
		}},
		{"request assurance", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.Assurance = types.AssuranceLegacy
		}},
		{"request occurred at", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Actor.OccurredAt = otherTime
		}},
		{"binding scope project", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Scope.ProjectID = validID
		}},
		{"binding scope workspace", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Scope.WorkspaceID = types.WorkspaceID(validID)
		}},
		{"checkout path", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Checkout.CanonicalPath = "/changed"
		}},
		{"checkout device", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Checkout.Device++
		}},
		{"checkout inode", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Checkout.Inode++
		}},
		{"repository provider", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Repository.Provider = "gitlab"
		}},
		{"repository immutable ID", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Repository.ImmutableID = "changed"
		}},
		{"repository remote", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Repository.CanonicalRemote += "/changed"
		}},
		{"accepted ref", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.AcceptedRef = "refs/heads/changed"
		}},
		{"accepted commit", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.AcceptedCommitSHA = strings.Repeat("b", 40)
		}},
		{"accepted tree digest", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.AcceptedTreeDigest = string(otherDigest("9"))
		}},
		{"accepted snapshot", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Snapshot.Project.Name = "changed"
		}},
		{"binding status", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.State = "pending"
		}},
		{"binding created at", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.BindingCreatedAt = value.BindingCreatedAt.Add(time.Second)
		}},
		{"binding updated at", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.BindingUpdatedAt = value.BindingUpdatedAt.Add(time.Second)
		}},
		{"accepted snapshot raw digest", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.AcceptedSnapshotBlobDigest = otherDigest("9")
		}},
		{"candidate accepted base", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.AcceptedBaseDigest = otherDigest("9")
		}},
		{"candidate working tree", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.WorkingTreeDigest = otherDigest("9")
		}},
		{"candidate direct snapshot", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.DirectSnapshot.Project.Name = "changed"
		}},
		{"candidate rebased snapshot", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.RebasedSnapshot.Project.Name = "changed"
		}},
		{"candidate rebased pointer", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.RebasedSnapshot = nil
		}},
		{"candidate boundary", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.RebasedThroughGeneration++
		}},
		{"candidate importer", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.ImportedBy = validID
		}},
		{"candidate imported at", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.ImportedAt = otherTime
		}},
		{"candidate pointer", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate = nil
		}},
		{"candidate direct raw pointer", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.CandidateDirectTreeBlobDigest = nil
		}},
		{"candidate direct raw digest", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			digest := otherDigest("9")
			value.CandidateDirectTreeBlobDigest = &digest
		}},
		{"candidate rebased raw pointer", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.CandidateRebasedTreeBlobDigest = nil
		}},
		{"candidate rebased raw digest", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			digest := otherDigest("9")
			value.CandidateRebasedTreeBlobDigest = &digest
		}},
		{"operation generation", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].Generation++
		}},
		{"operation ID", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].OperationID = validID
		}},
		{"operation raw JSON", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].OperationJSON = append(value.Operations[0].OperationJSON, ' ')
		}},
		{"operation state", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].State = "discarded"
		}},
		{"operation owner pointer", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].StashedByStashID = nil
		}},
		{"operation created at", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].CreatedAt = otherTime
		}},
		{"unrelated terminal operation created at", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[len(value.Operations)-1].CreatedAt = otherTime
		}},
		{"unrelated terminal count-preserving substitution", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[len(value.Operations)-1] = restoreAuditRow(t, 10,
				servicePutTaskOperation(value.Workspace.Snapshot, validID, validID, "replacement history"), "materialized", "")
		}},
		{"unrelated terminal removal", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Operations = value.Operations[:len(value.Operations)-1]
		}},
		{"stash ID", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.StashID = validID
		}},
		{"stash source base", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.SourceBaseDigest = otherDigest("9")
		}},
		{"stash candidate digest", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.CandidateDigest = otherDigest("9")
		}},
		{"stash source tree", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.SourceTree[0].Data[0] ^= 1
		}},
		{"stash composed tree", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.ComposedTree[0].Data[0] ^= 1
		}},
		{"stash replay raw JSON", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.OperationsJSON += " "
		}},
		{"stash boundary", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.ThroughGeneration++
		}},
		{"stash actor and canonical JSON", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.Actor.HumanPrincipalID = validID
			raw, encodeErr := state.CanonicalJSON(value.Stash.Actor)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			value.Stash.ActorJSON = string(raw)
		}},
		{"stash actor raw JSON", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.ActorJSON += " "
		}},
		{"stash label", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.Label = "changed"
		}},
		{"stash created at", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.CreatedAt = otherTime
		}},
		{"stash source raw digest", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.StashSourceTreeBlobDigest = otherDigest("9")
		}},
		{"stash composed raw digest", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.StashComposedTreeBlobDigest = otherDigest("9")
		}},
		{"conflict occurrence ID", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].OccurrenceID = validID
		}},
		{"conflict semantic ID", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].ConflictID = string(otherDigest("9"))
		}},
		{"conflict record kind", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].Key.Kind = "event"
		}},
		{"conflict record ID", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].Key.ID = validID
		}},
		{"conflict field path", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].FieldPath = "/description"
		}},
		{"conflict kind", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].ConflictKind = "markdown"
		}},
		{"conflict base raw JSON", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].BaseJSON += " "
		}},
		{"conflict ours raw JSON", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].OursJSON += " "
		}},
		{"conflict theirs raw JSON", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].TheirsJSON += " "
		}},
		{"conflict created at", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0].CreatedAt = otherTime
		}},
		{"conflict valid semantic evidence", func(_ *RestoreStashRequest, _ *state.Digest, value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0] = semanticallyDifferentRestoreConflict(t, value.OpenConflicts[0])
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changedReq := req
			changedRequestDigest := requestDigest
			changedState := cloneRestoreRetryFixture(t, persisted)
			test.edit(&changedReq, &changedRequestDigest, &changedState)
			preimage, buildErr := buildRestoreStashRetryPreimage(changedReq, changedRequestDigest, changedState)
			gotDigest, digestErr := restoreStashRetryDigest(changedReq, changedRequestDigest, changedState)
			if buildErr != nil {
				if !reflect.DeepEqual(preimage, restoreStashRetryPreimageV1{}) || digestErr == nil || gotDigest != "" {
					t.Fatalf("failed-closed drift=(%+v,%v,%q,%v)", preimage, buildErr, gotDigest, digestErr)
				}
				return
			}
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			projectedDigest, err := state.DigestCanonicalJSON(preimage)
			if err != nil {
				t.Fatal(err)
			}
			if gotDigest != projectedDigest || gotDigest == baseDigest {
				t.Fatalf("accepted drift digest=%q projected=%q base=%q", gotDigest, projectedDigest, baseDigest)
			}
		})
	}
}

func TestBuildRestoreStashRetryPreimageNullabilityAndValidation(t *testing.T) {
	t.Run("non-nil empty operation audit", func(t *testing.T) {
		req, requestDigest, persisted := restoreRetryEmptyOperationsFixture(t)
		preimage, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted)
		if err != nil {
			t.Fatal(err)
		}
		if preimage.Operations == nil || len(preimage.Operations) != 0 {
			t.Fatalf("empty operation projection=%#v", preimage.Operations)
		}
		persisted.Operations = nil
		if got, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted); err == nil || !reflect.DeepEqual(got, restoreStashRetryPreimageV1{}) {
			t.Fatalf("nil operation projection=(%+v,%v), want zero,error", got, err)
		}
	})

	t.Run("direct-only candidate", func(t *testing.T) {
		req, requestDigest, persisted := restoreRetryFixture(t)
		persisted.Candidate.RebasedSnapshot = nil
		persisted.Candidate.RebasedThroughGeneration = 0
		persisted.CandidateRebasedTreeBlobDigest = nil
		preimage, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted)
		if err != nil {
			t.Fatal(err)
		}
		if preimage.Candidate == nil || preimage.Candidate.RebasedTreeBlobDigest != nil || preimage.Candidate.RebasedThroughGeneration != 0 {
			t.Fatalf("direct-only candidate=%+v", preimage.Candidate)
		}
	})

	t.Run("absent candidate", func(t *testing.T) {
		req, requestDigest, persisted := restoreRetryFixture(t)
		persisted.Workspace.Snapshot = composeCloneSnapshot(t, persisted.Candidate.DirectSnapshot)
		persisted.Workspace.Binding.AcceptedTreeDigest = string(persisted.Workspace.Snapshot.Digest)
		persisted.Candidate = nil
		persisted.CandidateDirectTreeBlobDigest = nil
		persisted.CandidateRebasedTreeBlobDigest = nil
		replaceRestoreRetryEvidence(t, &persisted)
		preimage, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted)
		if err != nil {
			t.Fatal(err)
		}
		if preimage.Candidate != nil {
			t.Fatalf("absent candidate projected as %+v", preimage.Candidate)
		}
	})

	tests := []struct {
		name string
		edit func(*RestoreStashRequest, *state.Digest, *localstore.WorkspaceRestoreRetryState)
	}{
		{"request digest", func(_ *RestoreStashRequest, digest *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			*digest = state.Digest("sha256:" + strings.Repeat("9", 64))
		}},
		{"request scope", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.Scope.WorkspaceID = "00000000-0000-4000-8000-000000000099"
		}},
		{"request stash", func(req *RestoreStashRequest, _ *state.Digest, _ *localstore.WorkspaceRestoreRetryState) {
			req.StashID = "00000000-0000-4000-8000-000000000099"
		}},
		{"binding status", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.Workspace.State = "pending"
		}},
		{"binding timestamp", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.BindingUpdatedAt = time.Time{}
		}},
		{"accepted blob digest", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.AcceptedSnapshotBlobDigest = "BAD"
		}},
		{"nil operation audit", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.Operations = nil
		}},
		{"unordered operations", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.Operations[0], persisted.Operations[1] = persisted.Operations[1], persisted.Operations[0]
		}},
		{"nil conflicts", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.OpenConflicts = nil
		}},
		{"empty conflicts", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.OpenConflicts = []localstore.WorkspaceConflictOccurrence{}
		}},
		{"duplicate conflicts", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.OpenConflicts = append(persisted.OpenConflicts, persisted.OpenConflicts[0])
		}},
		{"semantic evidence", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.OpenConflicts[0].BaseJSON = persisted.OpenConflicts[0].OursJSON
		}},
		{"candidate missing direct blob", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.CandidateDirectTreeBlobDigest = nil
		}},
		{"candidate missing rebased blob", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.CandidateRebasedTreeBlobDigest = nil
		}},
		{"candidate unexpected rebased blob", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.Candidate.RebasedSnapshot = nil
			persisted.Candidate.RebasedThroughGeneration = 0
		}},
		{"candidate attribution", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.Candidate.ImportedBy = "BAD"
		}},
		{"stash actor bytes", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.Stash.ActorJSON = " " + persisted.Stash.ActorJSON
		}},
		{"stash replay bytes", func(_ *RestoreStashRequest, _ *state.Digest, persisted *localstore.WorkspaceRestoreRetryState) {
			persisted.Stash.OperationsJSON = strings.TrimSuffix(persisted.Stash.OperationsJSON, "\n")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, requestDigest, persisted := restoreRetryFixture(t)
			test.edit(&req, &requestDigest, &persisted)
			if got, err := buildRestoreStashRetryPreimage(req, requestDigest, persisted); err == nil || !reflect.DeepEqual(got, restoreStashRetryPreimageV1{}) {
				t.Fatalf("build invalid=(%+v,%v), want zero,error", got, err)
			}
		})
	}
}

func restoreRetryEmptyOperationsFixture(t *testing.T) (RestoreStashRequest, state.Digest, localstore.WorkspaceRestoreRetryState) {
	t.Helper()
	fixture := newRestoreShapeFixture(t, "direct-empty")
	persisted := cloneRestoreRetryState(t, fixture.retry)
	current := composeCloneSnapshot(t, persisted.Workspace.Snapshot)
	current.Project.Name = "current project"
	current.Project.UpdatedAt = current.Project.UpdatedAt.Add(2 * time.Minute)
	current = stashPlanRefreshSnapshot(t, current)
	persisted.Workspace.Snapshot = current
	persisted.Workspace.Binding.AcceptedTreeDigest = string(current.Digest)
	persisted.Workspace.State = "conflicted"
	persisted.BindingCreatedAt = time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	persisted.BindingUpdatedAt = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	persisted.AcceptedSnapshotBlobDigest = state.Digest("sha256:" + strings.Repeat("1", 64))
	persisted.Candidate = nil
	persisted.CandidateDirectTreeBlobDigest = nil
	persisted.CandidateRebasedTreeBlobDigest = nil
	persisted.Operations = []localstore.WorkspaceOperationAuditRecord{}
	persisted.StashSourceTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("4", 64))
	persisted.StashComposedTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("5", 64))
	replaceRestoreRetryEvidence(t, &persisted)
	req := RestoreStashRequest{
		Scope: persisted.Workspace.Binding.Scope, RequestID: "99999999-9999-4999-8999-999999999991",
		StashID: persisted.Stash.StashID,
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
		},
	}
	requestDigest, err := restoreRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	return req, requestDigest, persisted
}

func replaceRestoreRetryEvidence(t *testing.T, persisted *localstore.WorkspaceRestoreRetryState) {
	t.Helper()
	plan, err := buildRestorePlan(*persisted)
	if err != nil || len(plan.ConflictEvidence) == 0 {
		t.Fatalf("restore retry evidence plan=%+v err=%v", plan, err)
	}
	occurrences := make([]localstore.WorkspaceConflictOccurrence, len(plan.ConflictEvidence))
	occurrenceIDs := []string{
		"00000000-0000-4000-8000-000000000071",
		"00000000-0000-4000-8000-000000000072",
		"00000000-0000-4000-8000-000000000073",
	}
	if len(plan.ConflictEvidence) > len(occurrenceIDs) {
		t.Fatalf("restore retry fixture has %d conflicts, only %d deterministic occurrence IDs", len(plan.ConflictEvidence), len(occurrenceIDs))
	}
	for index, evidence := range plan.ConflictEvidence {
		occurrences[index] = localstore.WorkspaceConflictOccurrence{
			WorkspaceConflictEvidence: evidence,
			OccurrenceID:              occurrenceIDs[index],
			CreatedAt:                 time.Date(2026, 7, 29, 11, index, 0, 0, time.UTC),
		}
	}
	persisted.OpenConflicts = occurrences
}

func TestValidateConflictedRestoreTransitionAllowsOnlyExpectedDelta(t *testing.T) {
	_, _, after := restoreRetryFixture(t)
	before := cloneRestoreRetryFixture(t, after)
	before.Workspace.State = "pending"
	before.BindingUpdatedAt = time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	before.OpenConflicts = []localstore.WorkspaceConflictOccurrence{}
	persistedConflicts := append([]localstore.WorkspaceConflictOccurrence{}, after.OpenConflicts...)
	wantBefore := cloneRestoreRetryFixture(t, before)
	wantAfter := cloneRestoreRetryFixture(t, after)
	if err := validateConflictedRestoreTransition(before, after, persistedConflicts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, wantBefore) || !reflect.DeepEqual(after, wantAfter) {
		t.Fatal("transition validator mutated input state")
	}

	tests := []struct {
		name string
		edit func(*localstore.WorkspaceRestoreRetryState, *localstore.WorkspaceRestoreRetryState, *[]localstore.WorkspaceConflictOccurrence)
	}{
		{"before status", func(before, _ *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			before.Workspace.State = "invalid"
		}},
		{"binding", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.Workspace.Binding.AcceptedRef = "refs/heads/changed"
		}},
		{"accepted snapshot blob", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.AcceptedSnapshotBlobDigest = state.Digest("sha256:" + strings.Repeat("9", 64))
		}},
		{"binding created", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.BindingCreatedAt = after.BindingCreatedAt.Add(-time.Minute)
		}},
		{"candidate", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.Candidate.ImportedAt = after.Candidate.ImportedAt.Add(time.Second)
		}},
		{"candidate direct blob", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			value := state.Digest("sha256:" + strings.Repeat("9", 64))
			after.CandidateDirectTreeBlobDigest = &value
		}},
		{"candidate rebased blob", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			value := state.Digest("sha256:" + strings.Repeat("9", 64))
			after.CandidateRebasedTreeBlobDigest = &value
		}},
		{"operation", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.Operations[0].CreatedAt = after.Operations[0].CreatedAt.Add(time.Second)
		}},
		{"stash", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.Stash.Label = "changed label"
		}},
		{"stash source blob", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.StashSourceTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("9", 64))
		}},
		{"stash composed blob", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.StashComposedTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("9", 64))
		}},
		{"after status", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.Workspace.State = "pending"
		}},
		{"updated backwards", func(before, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.BindingUpdatedAt = before.BindingUpdatedAt.Add(-time.Second)
		}},
		{"after conflicts", func(_, after *localstore.WorkspaceRestoreRetryState, _ *[]localstore.WorkspaceConflictOccurrence) {
			after.OpenConflicts[0].CreatedAt = after.OpenConflicts[0].CreatedAt.Add(time.Second)
		}},
		{"supplied conflicts nil", func(_, _ *localstore.WorkspaceRestoreRetryState, conflicts *[]localstore.WorkspaceConflictOccurrence) {
			*conflicts = nil
		}},
		{"supplied conflicts empty", func(_, _ *localstore.WorkspaceRestoreRetryState, conflicts *[]localstore.WorkspaceConflictOccurrence) {
			*conflicts = []localstore.WorkspaceConflictOccurrence{}
		}},
		{"supplied conflicts differ", func(_, _ *localstore.WorkspaceRestoreRetryState, conflicts *[]localstore.WorkspaceConflictOccurrence) {
			(*conflicts)[0].CreatedAt = (*conflicts)[0].CreatedAt.Add(time.Second)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := cloneRestoreRetryFixture(t, wantBefore)
			after := cloneRestoreRetryFixture(t, wantAfter)
			conflicts := append([]localstore.WorkspaceConflictOccurrence{}, persistedConflicts...)
			test.edit(&before, &after, &conflicts)
			if err := validateConflictedRestoreTransition(before, after, conflicts); err == nil {
				t.Fatal("invalid conflicted transition passed validation")
			}
		})
	}
}

func TestValidateConflictedRestoreTransitionRejectsIncoherentPreState(t *testing.T) {
	_, _, after := restoreRetryFixture(t)
	persistedConflicts := append([]localstore.WorkspaceConflictOccurrence{}, after.OpenConflicts...)

	for _, status := range []string{"clean", "pending", "blocked"} {
		t.Run(status+" with open conflicts", func(t *testing.T) {
			before := cloneRestoreRetryFixture(t, after)
			before.Workspace.State = status
			before.BindingUpdatedAt = before.BindingUpdatedAt.Add(-time.Second)
			if err := validateConflictedRestoreTransition(before, after, persistedConflicts); err == nil {
				t.Fatal("non-conflicted pre-state with open conflicts passed validation")
			}
		})
	}

	t.Run("conflicted without open conflicts", func(t *testing.T) {
		before := cloneRestoreRetryFixture(t, after)
		before.OpenConflicts = []localstore.WorkspaceConflictOccurrence{}
		before.BindingUpdatedAt = before.BindingUpdatedAt.Add(-time.Second)
		if err := validateConflictedRestoreTransition(before, after, persistedConflicts); err == nil {
			t.Fatal("conflicted pre-state without open conflicts passed validation")
		}
	})
}

func TestValidateConflictedRestoreTransitionAllowsSameSecondStatusTimestamp(t *testing.T) {
	_, _, after := restoreRetryFixture(t)
	before := cloneRestoreRetryFixture(t, after)
	before.Workspace.State = "pending"
	before.OpenConflicts = []localstore.WorkspaceConflictOccurrence{}
	before.BindingUpdatedAt = after.BindingUpdatedAt
	persistedConflicts := append([]localstore.WorkspaceConflictOccurrence{}, after.OpenConflicts...)
	if err := validateConflictedRestoreTransition(before, after, persistedConflicts); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConflictedRestoreTransitionRejectsEveryProtectedFieldDrift(t *testing.T) {
	_, _, afterFixture := restoreRetryFixtureWithTerminal(t)
	beforeFixture := cloneRestoreRetryFixture(t, afterFixture)
	beforeFixture.Workspace.State = "pending"
	beforeFixture.OpenConflicts = []localstore.WorkspaceConflictOccurrence{}
	beforeFixture.BindingUpdatedAt = beforeFixture.BindingUpdatedAt.Add(-time.Second)
	persistedConflicts := append([]localstore.WorkspaceConflictOccurrence{}, afterFixture.OpenConflicts...)
	otherDigest := state.Digest("sha256:" + strings.Repeat("9", 64))
	validID := "00000000-0000-4000-8000-000000000099"
	tests := []struct {
		name string
		edit func(*localstore.WorkspaceRestoreRetryState)
	}{
		{"binding scope project", func(value *localstore.WorkspaceRestoreRetryState) { value.Workspace.Binding.Scope.ProjectID = validID }},
		{"binding scope workspace", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Scope.WorkspaceID = types.WorkspaceID(validID)
		}},
		{"checkout path", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Checkout.CanonicalPath = "/changed"
		}},
		{"checkout device", func(value *localstore.WorkspaceRestoreRetryState) { value.Workspace.Binding.Checkout.Device++ }},
		{"checkout inode", func(value *localstore.WorkspaceRestoreRetryState) { value.Workspace.Binding.Checkout.Inode++ }},
		{"repository provider", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Repository.Provider = "gitlab"
		}},
		{"repository immutable ID", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Repository.ImmutableID = "changed"
		}},
		{"repository remote", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.Repository.CanonicalRemote += "/changed"
		}},
		{"accepted ref", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.AcceptedRef = "refs/heads/changed"
		}},
		{"accepted commit", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.AcceptedCommitSHA = strings.Repeat("b", 40)
		}},
		{"accepted tree digest", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Workspace.Binding.AcceptedTreeDigest = string(otherDigest)
		}},
		{"accepted snapshot", func(value *localstore.WorkspaceRestoreRetryState) { value.Workspace.Snapshot.Project.Name = "changed" }},
		{"binding created at", func(value *localstore.WorkspaceRestoreRetryState) {
			value.BindingCreatedAt = value.BindingCreatedAt.Add(time.Second)
		}},
		{"accepted snapshot raw digest", func(value *localstore.WorkspaceRestoreRetryState) { value.AcceptedSnapshotBlobDigest = otherDigest }},
		{"candidate pointer", func(value *localstore.WorkspaceRestoreRetryState) { value.Candidate = nil }},
		{"candidate accepted base", func(value *localstore.WorkspaceRestoreRetryState) { value.Candidate.AcceptedBaseDigest = otherDigest }},
		{"candidate working tree", func(value *localstore.WorkspaceRestoreRetryState) { value.Candidate.WorkingTreeDigest = otherDigest }},
		{"candidate direct snapshot", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.DirectSnapshot.Project.Name = "changed"
		}},
		{"candidate rebased pointer", func(value *localstore.WorkspaceRestoreRetryState) { value.Candidate.RebasedSnapshot = nil }},
		{"candidate rebased snapshot", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.RebasedSnapshot.Project.Name = "changed"
		}},
		{"candidate boundary", func(value *localstore.WorkspaceRestoreRetryState) { value.Candidate.RebasedThroughGeneration++ }},
		{"candidate importer", func(value *localstore.WorkspaceRestoreRetryState) { value.Candidate.ImportedBy = validID }},
		{"candidate imported at", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Candidate.ImportedAt = value.Candidate.ImportedAt.Add(time.Second)
		}},
		{"candidate direct raw pointer", func(value *localstore.WorkspaceRestoreRetryState) { value.CandidateDirectTreeBlobDigest = nil }},
		{"candidate direct raw digest", func(value *localstore.WorkspaceRestoreRetryState) {
			digest := otherDigest
			value.CandidateDirectTreeBlobDigest = &digest
		}},
		{"candidate rebased raw pointer", func(value *localstore.WorkspaceRestoreRetryState) { value.CandidateRebasedTreeBlobDigest = nil }},
		{"candidate rebased raw digest", func(value *localstore.WorkspaceRestoreRetryState) {
			digest := otherDigest
			value.CandidateRebasedTreeBlobDigest = &digest
		}},
		{"operations nil", func(value *localstore.WorkspaceRestoreRetryState) { value.Operations = nil }},
		{"operation count", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Operations = value.Operations[:len(value.Operations)-1]
		}},
		{"operation order", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0], value.Operations[1] = value.Operations[1], value.Operations[0]
		}},
		{"operation generation", func(value *localstore.WorkspaceRestoreRetryState) { value.Operations[0].Generation++ }},
		{"operation ID", func(value *localstore.WorkspaceRestoreRetryState) { value.Operations[0].OperationID = validID }},
		{"operation raw JSON", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].OperationJSON = append(value.Operations[0].OperationJSON, ' ')
		}},
		{"operation state", func(value *localstore.WorkspaceRestoreRetryState) { value.Operations[0].State = "discarded" }},
		{"operation owner pointer", func(value *localstore.WorkspaceRestoreRetryState) { value.Operations[0].StashedByStashID = nil }},
		{"operation owner value", func(value *localstore.WorkspaceRestoreRetryState) {
			owner := validID
			value.Operations[0].StashedByStashID = &owner
		}},
		{"operation created at", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[0].CreatedAt = value.Operations[0].CreatedAt.Add(time.Second)
		}},
		{"terminal count-preserving substitution", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[len(value.Operations)-1] = restoreAuditRow(t, 10,
				servicePutTaskOperation(value.Workspace.Snapshot, validID, validID, "replacement history"), "materialized", "")
		}},
		{"terminal created at", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Operations[len(value.Operations)-1].CreatedAt = value.Operations[len(value.Operations)-1].CreatedAt.Add(time.Second)
		}},
		{"stash ID", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.StashID = validID }},
		{"stash source base", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.SourceBaseDigest = otherDigest }},
		{"stash candidate digest", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.CandidateDigest = otherDigest }},
		{"stash source tree", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.SourceTree[0].Data[0] ^= 1 }},
		{"stash composed tree", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.ComposedTree[0].Data[0] ^= 1 }},
		{"stash replay raw JSON", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.OperationsJSON += " " }},
		{"stash boundary", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.ThroughGeneration++ }},
		{"stash actor", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.Actor.HumanPrincipalID = validID
			raw, err := state.CanonicalJSON(value.Stash.Actor)
			if err != nil {
				t.Fatal(err)
			}
			value.Stash.ActorJSON = string(raw)
		}},
		{"stash actor raw JSON", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.ActorJSON += " " }},
		{"stash label", func(value *localstore.WorkspaceRestoreRetryState) { value.Stash.Label = "changed" }},
		{"stash created at", func(value *localstore.WorkspaceRestoreRetryState) {
			value.Stash.CreatedAt = value.Stash.CreatedAt.Add(time.Second)
		}},
		{"stash source raw digest", func(value *localstore.WorkspaceRestoreRetryState) { value.StashSourceTreeBlobDigest = otherDigest }},
		{"stash composed raw digest", func(value *localstore.WorkspaceRestoreRetryState) { value.StashComposedTreeBlobDigest = otherDigest }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := cloneRestoreRetryFixture(t, beforeFixture)
			after := cloneRestoreRetryFixture(t, afterFixture)
			test.edit(&after)
			if err := validateConflictedRestoreTransition(before, after, persistedConflicts); err == nil {
				t.Fatal("protected field drift passed transition validation")
			}
		})
	}
}

func TestRestoreStashRetryDigestRejectsLaterSameStatusRewrite(t *testing.T) {
	req, requestDigest, persisted := restoreRetryFixture(t)
	storedDigest, err := restoreStashRetryDigest(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}
	later := cloneRestoreRetryFixture(t, persisted)
	later.BindingUpdatedAt = later.BindingUpdatedAt.Add(time.Second)
	laterDigest, err := restoreStashRetryDigest(req, requestDigest, later)
	if err != nil {
		t.Fatal(err)
	}
	if laterDigest == storedDigest {
		t.Fatalf("same-status updated_at rewrite retained stored retry digest %q", storedDigest)
	}
}

func TestRestoreRetryTwoConflictMembershipAndSemanticEvidence(t *testing.T) {
	req, requestDigest, persisted := restoreRetryTwoConflictFixture(t)
	if len(persisted.OpenConflicts) != 2 {
		t.Fatalf("two-conflict fixture has %d conflicts", len(persisted.OpenConflicts))
	}
	baseDigest, err := restoreStashRetryDigest(req, requestDigest, persisted)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		edit    func(*localstore.WorkspaceRestoreRetryState)
		wantErr string
	}{
		{"reordered", func(value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0], value.OpenConflicts[1] = value.OpenConflicts[1], value.OpenConflicts[0]
		}, "unordered"},
		{"missing", func(value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts = value.OpenConflicts[:1]
		}, "membership mismatch"},
		{"duplicate extra", func(value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts = append(value.OpenConflicts, value.OpenConflicts[1])
		}, "unordered or duplicated"},
		{"distinct ordered extra", func(value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts = append(value.OpenConflicts, additionalOrderedRestoreConflict(t, value.OpenConflicts[1]))
		}, "semantic conflict membership mismatch"},
		{"count-preserving substitution", func(value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0] = value.OpenConflicts[1]
		}, "unordered or duplicated"},
		{"valid re-ID semantic substitution", func(value *localstore.WorkspaceRestoreRetryState) {
			value.OpenConflicts[0] = semanticallyDifferentRestoreConflict(t, value.OpenConflicts[0])
		}, "semantic conflict evidence mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneRestoreRetryFixture(t, persisted)
			test.edit(&changed)
			if got, err := restoreStashRetryDigest(req, requestDigest, changed); err == nil || got != "" || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("changed retry digest=(%q,%v), want zero error containing %q (base %q)", got, err, test.wantErr, baseDigest)
			}
		})
	}

	before := cloneRestoreRetryFixture(t, persisted)
	before.Workspace.State = "pending"
	before.OpenConflicts = []localstore.WorkspaceConflictOccurrence{}
	before.BindingUpdatedAt = before.BindingUpdatedAt.Add(-time.Second)
	conflictTests := []struct {
		name    string
		edit    func(*[]localstore.WorkspaceConflictOccurrence)
		wantErr string
	}{
		{"reordered", func(rows *[]localstore.WorkspaceConflictOccurrence) { (*rows)[0], (*rows)[1] = (*rows)[1], (*rows)[0] }, ""},
		{"missing", func(rows *[]localstore.WorkspaceConflictOccurrence) { *rows = (*rows)[:1] }, ""},
		{"duplicate extra", func(rows *[]localstore.WorkspaceConflictOccurrence) { *rows = append(*rows, (*rows)[1]) }, ""},
		{"distinct ordered extra", func(rows *[]localstore.WorkspaceConflictOccurrence) {
			*rows = append(*rows, additionalOrderedRestoreConflict(t, (*rows)[1]))
		}, "post-state mismatch"},
		{"count-preserving substitution", func(rows *[]localstore.WorkspaceConflictOccurrence) { (*rows)[0] = (*rows)[1] }, ""},
		{"occurrence ID drift", func(rows *[]localstore.WorkspaceConflictOccurrence) {
			(*rows)[0].OccurrenceID = "00000000-0000-4000-8000-000000000079"
		}, ""},
		{"created timestamp drift", func(rows *[]localstore.WorkspaceConflictOccurrence) {
			(*rows)[0].CreatedAt = (*rows)[0].CreatedAt.Add(time.Second)
		}, ""},
		{"valid semantic substitution", func(rows *[]localstore.WorkspaceConflictOccurrence) {
			(*rows)[0] = semanticallyDifferentRestoreConflict(t, (*rows)[0])
		}, ""},
	}
	for _, test := range conflictTests {
		t.Run("persisted conflicts "+test.name, func(t *testing.T) {
			rows := append([]localstore.WorkspaceConflictOccurrence{}, persisted.OpenConflicts...)
			test.edit(&rows)
			if err := validateConflictedRestoreTransition(before, persisted, rows); err == nil || (test.wantErr != "" && !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("drifted persisted conflicts error=%v, want non-nil containing %q", err, test.wantErr)
			}
		})
	}
}

func semanticallyDifferentRestoreConflict(t *testing.T, row localstore.WorkspaceConflictOccurrence) localstore.WorkspaceConflictOccurrence {
	t.Helper()
	conflicts, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{row})
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("decode conflict for semantic mutation=(%+v,%v)", conflicts, err)
	}
	conflict := conflicts[0]
	conflict.Ours.Value = append([]byte(nil), conflict.Theirs.Value...)
	conflict.ID, err = conflictID(conflict)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := encodeWorkspaceConflictEvidence([]Conflict{conflict})
	if err != nil || len(evidence) != 1 {
		t.Fatalf("encode semantic mutation=(%+v,%v)", evidence, err)
	}
	row.WorkspaceConflictEvidence = evidence[0]
	return row
}

func additionalOrderedRestoreConflict(t *testing.T, row localstore.WorkspaceConflictOccurrence) localstore.WorkspaceConflictOccurrence {
	t.Helper()
	conflicts, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{row})
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("decode conflict for ordered addition=(%+v,%v)", conflicts, err)
	}
	conflict := conflicts[0]
	conflict.Key.ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	conflict.ID, err = conflictID(conflict)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := encodeWorkspaceConflictEvidence([]Conflict{conflict})
	if err != nil || len(evidence) != 1 {
		t.Fatalf("encode ordered conflict addition=(%+v,%v)", evidence, err)
	}
	return localstore.WorkspaceConflictOccurrence{
		WorkspaceConflictEvidence: evidence[0],
		OccurrenceID:              "00000000-0000-4000-8000-000000000073",
		CreatedAt:                 row.CreatedAt.Add(time.Minute),
	}
}

func restoreRetryFixture(t *testing.T) (RestoreStashRequest, state.Digest, localstore.WorkspaceRestoreRetryState) {
	t.Helper()
	return restoreRetryFixtureFromPlan(t, newRestoreShapeFixture(t, "rebased-sparse"), false)
}

func restoreRetryFixtureWithTerminal(t *testing.T) (RestoreStashRequest, state.Digest, localstore.WorkspaceRestoreRetryState) {
	t.Helper()
	req, requestDigest, persisted := restoreRetryFixture(t)
	persisted.Operations = append(persisted.Operations, restoreAuditRow(t, 10,
		servicePutTaskOperation(persisted.Workspace.Snapshot,
			"90000000-0000-4000-8000-000000000010",
			"80000000-0000-4000-8000-000000000010", "discarded history"),
		"discarded", ""))
	return req, requestDigest, persisted
}

func restoreRetryTwoConflictFixture(t *testing.T) (RestoreStashRequest, state.Digest, localstore.WorkspaceRestoreRetryState) {
	t.Helper()
	source, binding, candidate, absorbed := stashPlanRebasedFixture(t, 4)
	stashed := composeTaskOperation(*candidate.RebasedSnapshot, "90000000-0000-4000-8000-000000000009", func(task *state.TaskV1) {
		task.Title = "stashed title"
		task.Priority = 8
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	plan, err := buildStashPlan(binding, source, candidate, []localstore.WorkspaceOperation{
		stashPlanOperationRow(t, 4, absorbed, "rebased"),
		stashPlanOperationRow(t, 9, stashed, "active"),
	})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := state.DecodeTree(plan.ComposedTree)
	if err != nil {
		t.Fatal(err)
	}
	const stashID = "20000000-0000-4000-8000-000000000001"
	actor := diffActorEnvelope()
	actorJSON, err := state.CanonicalJSON(actor)
	if err != nil {
		t.Fatal(err)
	}
	retryBinding := binding
	retryBinding.AcceptedTreeDigest = string(source.Digest)
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	fixture := restorePlanFixture{
		retry: localstore.WorkspaceRestoreRetryState{
			Workspace:        localstore.WorkspaceRecord{Binding: retryBinding, Snapshot: source, State: "clean"},
			BindingCreatedAt: now, BindingUpdatedAt: now, AcceptedSnapshotBlobDigest: source.Digest,
			Operations: append(restoreOwnedAuditRows(plan.AbsorbedRows, stashID), restoreOwnedAuditRows(plan.LaterRows, stashID)...),
			Stash: localstore.WorkspaceStashRecord{
				StashID: stashID, SourceBaseDigest: plan.SourceDigest, CandidateDigest: plan.CandidateDigest,
				SourceTree: cloneCheckpointTree(plan.SourceTree), ComposedTree: cloneCheckpointTree(plan.ComposedTree),
				OperationsJSON: plan.OperationsJSON, ThroughGeneration: plan.ThroughGeneration,
				Actor: actor, ActorJSON: string(actorJSON), Label: "restore plan", CreatedAt: now,
			},
			StashSourceTreeBlobDigest: source.Digest, StashComposedTreeBlobDigest: composed.Digest,
			OpenConflicts: []localstore.WorkspaceConflictOccurrence{},
		},
		source: source, composed: composed,
	}
	return restoreRetryFixtureFromPlan(t, fixture, true)
}

func restoreRetryFixtureFromPlan(t *testing.T, fixture restorePlanFixture, twoConflicts bool) (RestoreStashRequest, state.Digest, localstore.WorkspaceRestoreRetryState) {
	t.Helper()
	persisted := cloneRestoreRetryState(t, fixture.retry)
	persisted.Workspace.State = "conflicted"
	persisted.BindingCreatedAt = time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	persisted.BindingUpdatedAt = time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	persisted.AcceptedSnapshotBlobDigest = state.Digest("sha256:" + strings.Repeat("1", 64))
	direct := composeCloneSnapshot(t, persisted.Workspace.Snapshot)
	direct.Tasks[composeTaskID].Value.Title = "current title"
	if twoConflicts {
		direct.Tasks[composeTaskID].Value.Priority = 3
	}
	direct.Tasks[composeTaskID].Value.UpdatedAt = direct.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	direct = stashPlanRefreshSnapshot(t, direct)
	rebased := composeCloneSnapshot(t, direct)
	persisted.Candidate = &localstore.WorkspaceCandidateRecord{
		AcceptedBaseDigest: state.Digest(persisted.Workspace.Binding.AcceptedTreeDigest),
		WorkingTreeDigest:  direct.Digest, DirectSnapshot: direct, RebasedSnapshot: &rebased,
		RebasedThroughGeneration: 4,
	}
	directDigest := state.Digest("sha256:" + strings.Repeat("2", 64))
	rebasedDigest := state.Digest("sha256:" + strings.Repeat("3", 64))
	persisted.CandidateDirectTreeBlobDigest = &directDigest
	persisted.CandidateRebasedTreeBlobDigest = &rebasedDigest
	persisted.Candidate.ImportedBy = "00000000-0000-4000-8000-000000000061"
	persisted.Candidate.ImportedAt = time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	persisted.StashSourceTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("4", 64))
	persisted.StashComposedTreeBlobDigest = state.Digest("sha256:" + strings.Repeat("5", 64))
	replaceRestoreRetryEvidence(t, &persisted)
	req := RestoreStashRequest{
		Scope: persisted.Workspace.Binding.Scope, RequestID: "99999999-9999-4999-8999-999999999991",
		StashID: persisted.Stash.StashID,
		Actor: types.ActorEnvelope{
			ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
			Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
		},
	}
	requestDigest, err := restoreRequestDigest(req)
	if err != nil {
		t.Fatal(err)
	}
	return req, requestDigest, persisted
}

func cloneRestoreRetryFixture(t *testing.T, persisted localstore.WorkspaceRestoreRetryState) localstore.WorkspaceRestoreRetryState {
	t.Helper()
	cloned := cloneRestoreRetryState(t, persisted)
	if persisted.CandidateDirectTreeBlobDigest != nil {
		digest := *persisted.CandidateDirectTreeBlobDigest
		cloned.CandidateDirectTreeBlobDigest = &digest
	}
	if persisted.CandidateRebasedTreeBlobDigest != nil {
		digest := *persisted.CandidateRebasedTreeBlobDigest
		cloned.CandidateRebasedTreeBlobDigest = &digest
	}
	return cloned
}
