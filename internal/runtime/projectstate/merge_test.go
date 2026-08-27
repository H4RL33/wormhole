package projectstate

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestConflictKindValues(t *testing.T) {
	got := []ConflictKind{
		ConflictSameField,
		ConflictMarkdown,
		ConflictImmutableRecord,
		ConflictTombstoneEdit,
		ConflictTombstoneBody,
		ConflictInvalidResurrection,
	}
	want := []ConflictKind{
		"same_field",
		"markdown",
		"immutable_record",
		"tombstone_edit",
		"tombstone_body",
		"invalid_resurrection",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConflictKind values = %v, want %v", got, want)
	}
}

func TestConflictIDGolden(t *testing.T) {
	conflict := Conflict{
		Key: state.RecordKey{Kind: "task", ID: composeTaskID}, FieldPath: "/title", Kind: ConflictSameField,
		Base:   FieldValue{Present: true, Value: json.RawMessage(`"base"`)},
		Ours:   FieldValue{Present: true, Value: json.RawMessage(`"ours"`)},
		Theirs: FieldValue{Present: true, Value: json.RawMessage(`"theirs"`)},
	}
	got, err := conflictID(conflict)
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:1c7adaf28e9f811f3039b88a685ed8f88510c5d3445d66c213d96bbc1ef3ca92"
	if got != want {
		t.Fatalf("conflictID = %q, want %q", got, want)
	}
	preimage, err := state.CanonicalJSON(conflictIDPreimageV1{
		SchemaVersion: 1, Key: conflict.Key, FieldPath: conflict.FieldPath, Kind: conflict.Kind,
		Base: conflict.Base, Ours: conflict.Ours, Theirs: conflict.Theirs,
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantPreimage = "{\"schema_version\":1,\"key\":{\"Kind\":\"task\",\"ID\":\"22222222-2222-4222-8222-222222222222\"},\"field_path\":\"/title\",\"kind\":\"same_field\",\"base\":{\"present\":true,\"value\":\"base\"},\"ours\":{\"present\":true,\"value\":\"ours\"},\"theirs\":{\"present\":true,\"value\":\"theirs\"}}\n"
	if string(preimage) != wantPreimage {
		t.Fatalf("conflict preimage = %q, want %q", preimage, wantPreimage)
	}
}

func TestThreeWayRebaseExactEquality(t *testing.T) {
	snapshot := composeFixtureSnapshot(t)
	wantTree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ThreeWayRebase(snapshot, snapshot, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	gotTree, err := state.EncodeTree(got.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotTree, wantTree) || got.Snapshot.Digest != snapshot.Digest || got.Conflicts == nil || len(got.Conflicts) != 0 {
		t.Fatalf("ThreeWayRebase exact result = %+v", got)
	}
	got.Snapshot.Tasks[composeTaskID].Value.Title = "mutated result"
	if snapshot.Tasks[composeTaskID].Value.Title == "mutated result" {
		t.Fatal("ThreeWayRebase result aliases its input")
	}
}

func TestConflictSortingCanonicalOrder(t *testing.T) {
	conflicts := []Conflict{
		{ID: "z", Key: state.RecordKey{Kind: "git_link", ID: "11111111-1111-4111-8111-111111111111"}},
		{ID: "z", Key: state.RecordKey{Kind: "event", ID: "11111111-1111-4111-8111-111111111111"}},
		{ID: "z", Key: state.RecordKey{Kind: "channel", ID: "11111111-1111-4111-8111-111111111111"}},
		{ID: "z", Key: state.RecordKey{Kind: "kb_article", ID: "11111111-1111-4111-8111-111111111111"}},
		{ID: "z", Key: state.RecordKey{Kind: "task_link", ID: "11111111-1111-4111-8111-111111111111"}},
		{ID: "z", Key: state.RecordKey{Kind: "task", ID: "22222222-2222-4222-8222-222222222222"}, FieldPath: "/b", Kind: ConflictSameField},
		{ID: "a", Key: state.RecordKey{Kind: "task", ID: "22222222-2222-4222-8222-222222222222"}, FieldPath: "/b", Kind: ConflictSameField},
		{ID: "z", Key: state.RecordKey{Kind: "task", ID: "22222222-2222-4222-8222-222222222222"}, FieldPath: "/b", Kind: ConflictMarkdown},
		{ID: "z", Key: state.RecordKey{Kind: "task", ID: "22222222-2222-4222-8222-222222222222"}, FieldPath: "/a", Kind: ConflictSameField},
		{ID: "z", Key: state.RecordKey{Kind: "task", ID: "11111111-1111-4111-8111-111111111111"}, FieldPath: "/z", Kind: ConflictSameField},
		{ID: "z", Key: state.RecordKey{Kind: "actor", ID: "11111111-1111-4111-8111-111111111111"}},
		{ID: "z", Key: state.RecordKey{Kind: "project", ID: "11111111-1111-4111-8111-111111111111"}},
	}
	sortConflicts(conflicts)
	want := []string{
		"project:11111111-1111-4111-8111-111111111111:::z",
		"actor:11111111-1111-4111-8111-111111111111:::z",
		"task:11111111-1111-4111-8111-111111111111:/z:same_field:z",
		"task:22222222-2222-4222-8222-222222222222:/a:same_field:z",
		"task:22222222-2222-4222-8222-222222222222:/b:markdown:z",
		"task:22222222-2222-4222-8222-222222222222:/b:same_field:a",
		"task:22222222-2222-4222-8222-222222222222:/b:same_field:z",
		"task_link:11111111-1111-4111-8111-111111111111:::z",
		"kb_article:11111111-1111-4111-8111-111111111111:::z",
		"channel:11111111-1111-4111-8111-111111111111:::z",
		"event:11111111-1111-4111-8111-111111111111:::z",
		"git_link:11111111-1111-4111-8111-111111111111:::z",
	}
	got := make([]string, len(conflicts))
	for index, conflict := range conflicts {
		got[index] = conflict.Key.Kind + ":" + conflict.Key.ID + ":" + conflict.FieldPath + ":" + string(conflict.Kind) + ":" + conflict.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conflict order = %v, want %v", got, want)
	}
	for left, right := 0, len(conflicts)-1; left < right; left, right = left+1, right-1 {
		conflicts[left], conflicts[right] = conflicts[right], conflicts[left]
	}
	sortConflicts(conflicts)
	for index, conflict := range conflicts {
		got[index] = conflict.Key.Kind + ":" + conflict.Key.ID + ":" + conflict.FieldPath + ":" + string(conflict.Kind) + ":" + conflict.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reversed conflict order = %v, want %v", got, want)
	}
}

func TestThreeWayRebaseRejectsInvalidAndStaleInputs(t *testing.T) {
	valid := composeFixtureSnapshot(t)
	tests := []struct {
		name   string
		mutate func(oldBase, newBase, candidate *state.Snapshot)
	}{
		{name: "invalid old base", mutate: func(oldBase, _, _ *state.Snapshot) { oldBase.Project.Name = "" }},
		{name: "invalid new base version", mutate: func(_, newBase, _ *state.Snapshot) { newBase.Config.SnapshotVersion = 2 }},
		{name: "invalid candidate", mutate: func(_, _, candidate *state.Snapshot) { candidate.Project.Name = "" }},
		{name: "stale old base digest", mutate: func(oldBase, _, _ *state.Snapshot) {
			oldBase.Digest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		}},
		{name: "stale new base digest", mutate: func(_, newBase, _ *state.Snapshot) {
			newBase.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}},
		{name: "stale candidate digest", mutate: func(_, _, candidate *state.Snapshot) {
			candidate.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldBase := diffCloneSnapshot(t, valid)
			newBase := diffCloneSnapshot(t, valid)
			candidate := diffCloneSnapshot(t, valid)
			test.mutate(&oldBase, &newBase, &candidate)
			got, err := ThreeWayRebase(oldBase, newBase, candidate)
			if err == nil || !reflect.DeepEqual(got, MergeResult{}) {
				t.Fatalf("ThreeWayRebase invalid result = %+v, %v", got, err)
			}
		})
	}
}

func TestThreeWayRebaseRejectsBindingMismatch(t *testing.T) {
	valid := composeFixtureSnapshot(t)
	tests := []struct {
		name      string
		candidate bool
		mutate    func(*state.Snapshot)
	}{
		{name: "new base project", mutate: func(snapshot *state.Snapshot) {
			snapshot.Config.ProjectID = "99999999-9999-4999-8999-999999999999"
			snapshot.Project.ID = snapshot.Config.ProjectID
		}},
		{name: "candidate project", candidate: true, mutate: func(snapshot *state.Snapshot) {
			snapshot.Config.ProjectID = "99999999-9999-4999-8999-999999999999"
			snapshot.Project.ID = snapshot.Config.ProjectID
		}},
		{name: "new base repository", mutate: func(snapshot *state.Snapshot) {
			snapshot.Config.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"}
		}},
		{name: "candidate repository", candidate: true, mutate: func(snapshot *state.Snapshot) {
			snapshot.Config.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			newBase := diffCloneSnapshot(t, valid)
			candidate := diffCloneSnapshot(t, valid)
			if test.candidate {
				test.mutate(&candidate)
				candidate = diffCanonicalSnapshot(t, candidate)
			} else {
				test.mutate(&newBase)
				newBase = diffCanonicalSnapshot(t, newBase)
			}
			got, err := ThreeWayRebase(valid, newBase, candidate)
			if err == nil || !reflect.DeepEqual(got, MergeResult{}) {
				t.Fatalf("ThreeWayRebase binding mismatch = %+v, %v", got, err)
			}
		})
	}
}

func TestThreeWayRebaseRejectsCandidateGitOwnedMutation(t *testing.T) {
	valid := composeFixtureSnapshot(t)
	tests := []struct {
		name   string
		mutate func(*state.Snapshot)
	}{
		{name: "handle", mutate: func(candidate *state.Snapshot) {
			candidate.Config.Handle = types.ProjectHandle{Namespace: "other", Name: "handle"}
		}},
		{name: "remotes", mutate: func(candidate *state.Snapshot) {
			candidate.Remotes = mergeTestRemotes()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := diffCloneSnapshot(t, valid)
			test.mutate(&candidate)
			candidate = diffCanonicalSnapshot(t, candidate)
			got, err := ThreeWayRebase(valid, valid, candidate)
			if err == nil || !reflect.DeepEqual(got, MergeResult{}) {
				t.Fatalf("ThreeWayRebase candidate mutation = %+v, %v", got, err)
			}
		})
	}
}

func TestThreeWayRebaseAdoptsNewBaseGitOwnedFieldsWithoutAliases(t *testing.T) {
	oldBase := composeFixtureSnapshot(t)
	candidate := diffCloneSnapshot(t, oldBase)
	newBase := diffCloneSnapshot(t, oldBase)
	newBase.Config.Handle = types.ProjectHandle{Namespace: "renamed", Name: "project"}
	newBase.Remotes = mergeTestRemotes()
	newBase = diffCanonicalSnapshot(t, newBase)
	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if err != nil {
		t.Fatal(err)
	}
	gotTree, err := state.EncodeTree(got.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	newTree, err := state.EncodeTree(newBase)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotTree, newTree) || got.Snapshot.Digest != newBase.Digest || len(got.Conflicts) != 0 {
		t.Fatalf("Git-owned adoption = %+v", got)
	}
	got.Snapshot.Tasks[composeTaskID].Value.Title = "mutated result"
	got.Snapshot.Remotes.Fabrics[0].Alias = "mutated"
	if !reflect.DeepEqual(oldBefore, diffTreeBytes(t, oldBase)) || !reflect.DeepEqual(newBefore, diffTreeBytes(t, newBase)) || !reflect.DeepEqual(candidateBefore, diffTreeBytes(t, candidate)) {
		t.Fatal("ThreeWayRebase mutated or aliased an input")
	}
}

func mergeTestRemotes() *state.RemotesV1 {
	return &state.RemotesV1{Version: 1, Fabrics: []state.FabricHintV1{{
		Alias: "public", URL: "https://fabric.example.test", InstanceID: "fabric-one",
		RemoteProjectID: "remote-one", Mode: "public",
	}}}
}
