package projectstate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const stashReplayGolden = `{"schema_version":1,"selected_start_tree":[{"Path":"config.toml","Data":"c25hcHNob3RfdmVyc2lvbiA9IDEKcHJvamVjdF9pZCA9ICIwMDAwMDAwMC0wMDAwLTQwMDAtODAwMC0wMDAwMDAwMDAwMDEiCgpbaGFuZGxlXQpuYW1lc3BhY2UgPSAiYWNtZSIKbmFtZSA9ICJ3b3JtaG9sZSIKCltyZXBvc2l0b3J5XQpwcm92aWRlciA9ICIiCmltbXV0YWJsZV9pZCA9ICIiCmNhbm9uaWNhbF9yZW1vdGUgPSAiIgo="},{"Path":"state/v1/project.json","Data":"eyJzY2hlbWFfdmVyc2lvbiI6MSwia2luZCI6InByb2plY3QiLCJpZCI6IjAwMDAwMDAwLTAwMDAtNDAwMC04MDAwLTAwMDAwMDAwMDAwMSIsIm5hbWUiOiJXb3JtaG9sZSIsImFsaWFzZXMiOltdLCJjcmVhdGVkX2F0IjoiMjAyNi0wNy0yOFQxMjowMDowMFoiLCJ1cGRhdGVkX2F0IjoiMjAyNi0wNy0yOFQxMjowMDowMFoiLCJleHRlbnNpb25zIjp7fX0K"}],"selected_start_digest":"sha256:90c0f7581a9aad345632ced40349057333b7fd4813409095d6091c5b83de4e32","initial_through_generation":0,"absorbed_operations":[],"operations":[]}
`

func TestStashReplayCanonicalGoldenAndRoundTrip(t *testing.T) {
	want, binding := stashReplayGoldenFixture(t)
	raw, err := encodeStashReplay(want, binding, 0)
	if err != nil {
		t.Fatal(err)
	}
	if raw != stashReplayGolden {
		t.Fatalf("stash replay canonical bytes = %q", raw)
	}
	got, err := decodeStashReplay(raw, binding, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeStashReplay()=%+v, want %+v", got, want)
	}
}

func TestStashReplayAcceptsBoundaryShapesAndGenerationGaps(t *testing.T) {
	fixture := newStashReplayFixture(t)
	tests := []struct {
		name    string
		value   StashReplayV1
		through int64
	}{
		{name: "absorbed prefix and sparse later suffix", value: fixture.value, through: fixture.through},
		{name: "post-rebase empty later suffix", value: func() StashReplayV1 {
			value := cloneStashReplay(t, fixture.value)
			value.Operations = []StoredOperation{}
			return value
		}(), through: fixture.value.InitialThroughGeneration},
	}
	empty, emptyBinding := stashReplayGoldenFixture(t)
	tests = append(tests, struct {
		name    string
		value   StashReplayV1
		through int64
	}{name: "zero boundary and empty arrays", value: empty, through: 0})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := fixture.binding
			if test.name == "zero boundary and empty arrays" {
				binding = emptyBinding
			}
			raw, err := encodeStashReplay(test.value, binding, test.through)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeStashReplay(raw, binding, test.through)
			if err != nil || !reflect.DeepEqual(got, test.value) {
				t.Fatalf("round trip=(%+v,%v), want %+v", got, err, test.value)
			}
			if got.AbsorbedOperations == nil || got.Operations == nil {
				t.Fatal("non-nil operation arrays became nil")
			}
		})
	}
}

func TestStashReplayRejectsEnvelopeAndOperationInvariants(t *testing.T) {
	fixture := newStashReplayFixture(t)
	tests := []struct {
		name string
		edit func(*StashReplayV1, *types.WorkspaceBinding, *int64)
	}{
		{name: "schema version", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) { value.SchemaVersion = 2 }},
		{name: "nil absorbed operations", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) { value.AbsorbedOperations = nil }},
		{name: "nil later operations", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) { value.Operations = nil }},
		{name: "negative boundary", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) { value.InitialThroughGeneration = -1 }},
		{name: "negative through generation", edit: func(_ *StashReplayV1, _ *types.WorkspaceBinding, through *int64) { *through = -1 }},
		{name: "boundary exceeds through generation", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, through *int64) {
			value.InitialThroughGeneration = *through + 1
		}},
		{name: "empty later suffix through mismatch", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, through *int64) {
			value.Operations = []StoredOperation{}
			*through = value.InitialThroughGeneration + 1
		}},
		{name: "last later generation differs from through", edit: func(_ *StashReplayV1, _ *types.WorkspaceBinding, through *int64) { *through-- }},
		{name: "absorbed generation above boundary", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.AbsorbedOperations[0].Generation = value.InitialThroughGeneration + 1
		}},
		{name: "later generation at boundary", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.Operations[0].Generation = value.InitialThroughGeneration
		}},
		{name: "zero absorbed generation", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.AbsorbedOperations[0].Generation = 0
		}},
		{name: "unordered absorbed generations", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.AbsorbedOperations = []StoredOperation{
				{Generation: 3, Operation: value.AbsorbedOperations[0].Operation},
				{Generation: 2, Operation: value.Operations[0].Operation},
			}
		}},
		{name: "zero later generation", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) { value.Operations[0].Generation = 0 }},
		{name: "unordered later generations", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, through *int64) {
			value.Operations[0].Generation = 9
			value.Operations[1].Generation = 8
			*through = 8
		}},
		{name: "duplicate operation ID across arrays", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.Operations[0].Operation = value.AbsorbedOperations[0].Operation
		}},
		{name: "duplicate operation ID within later suffix", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.Operations[1].Operation = value.Operations[0].Operation
		}},
		{name: "invalid operation", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.AbsorbedOperations[0].Operation.ID = "BAD"
		}},
		{name: "later operation fails compose", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding, _ *int64) {
			value.Operations[0].Operation.ExpectedViewDigest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := cloneStashReplay(t, fixture.value)
			binding, through := fixture.binding, fixture.through
			test.edit(&value, &binding, &through)
			raw := canonicalStashReplay(t, value)
			if got, err := decodeStashReplay(raw, binding, through); err == nil || !reflect.DeepEqual(got, StashReplayV1{}) {
				t.Fatalf("decode invalid replay=(%+v,%v), want zero and error", got, err)
			}
			if got, err := encodeStashReplay(value, binding, through); err == nil || got != "" {
				t.Fatalf("encode invalid replay=(%q,%v), want empty and error", got, err)
			}
		})
	}
}

func TestStashReplayRejectsBindingTreeAndDigestFailures(t *testing.T) {
	fixture := newStashReplayFixture(t)
	tests := []struct {
		name string
		edit func(*StashReplayV1, *types.WorkspaceBinding)
	}{
		{name: "invalid binding", edit: func(_ *StashReplayV1, binding *types.WorkspaceBinding) { binding.Checkout.CanonicalPath = "relative" }},
		{name: "tree project differs from binding", edit: func(_ *StashReplayV1, binding *types.WorkspaceBinding) {
			binding.Scope.ProjectID = "00000000-0000-4000-8000-000000000099"
		}},
		{name: "tree repository differs from binding", edit: func(_ *StashReplayV1, binding *types.WorkspaceBinding) {
			binding.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "other", CanonicalRemote: "https://github.com/acme/other"}
		}},
		{name: "selected digest", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding) {
			value.SelectedStartDigest = state.Digest("sha256:" + strings.Repeat("a", 64))
		}},
		{name: "nil selected tree", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding) { value.SelectedStartTree = nil }},
		{name: "unsorted selected tree", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding) {
			value.SelectedStartTree[0], value.SelectedStartTree[1] = value.SelectedStartTree[1], value.SelectedStartTree[0]
		}},
		{name: "duplicate selected path", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding) {
			value.SelectedStartTree = append(value.SelectedStartTree, state.File{Path: value.SelectedStartTree[0].Path, Data: bytes.Clone(value.SelectedStartTree[0].Data)})
		}},
		{name: "unsafe selected path", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding) {
			value.SelectedStartTree = append(value.SelectedStartTree, state.File{Path: "../escape", Data: []byte("bad")})
		}},
		{name: "changed selected bytes", edit: func(value *StashReplayV1, _ *types.WorkspaceBinding) {
			value.SelectedStartTree[0].Data = append(value.SelectedStartTree[0].Data, ' ')
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, binding := cloneStashReplay(t, fixture.value), fixture.binding
			test.edit(&value, &binding)
			raw := canonicalStashReplay(t, value)
			if got, err := decodeStashReplay(raw, binding, fixture.through); err == nil || !reflect.DeepEqual(got, StashReplayV1{}) {
				t.Fatalf("decode invalid replay=(%+v,%v), want zero and error", got, err)
			}
			if got, err := encodeStashReplay(value, binding, fixture.through); err == nil || got != "" {
				t.Fatalf("encode invalid replay=(%q,%v), want empty and error", got, err)
			}
		})
	}
}

func TestStashReplayRejectsNoncanonicalUnknownAndTrailingJSON(t *testing.T) {
	fixture := newStashReplayFixture(t)
	canonical := canonicalStashReplay(t, fixture.value)
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "malformed", raw: "{"},
		{name: "unknown outer field", raw: strings.TrimSuffix(canonical, "}\n") + ",\"unknown\":true}\n"},
		{name: "unknown stored operation field", raw: strings.Replace(canonical, "\"generation\":4", "\"generation\":4,\"unknown\":true", 1)},
		{name: "unknown operation field", raw: strings.Replace(canonical, "\"operation\":{\"schema_version\":1", "\"operation\":{\"unknown\":true,\"schema_version\":1", 1)},
		{name: "trailing JSON", raw: canonical + "{}\n"},
		{name: "missing final LF", raw: strings.TrimSuffix(canonical, "\n")},
		{name: "leading whitespace", raw: " " + canonical},
		{name: "duplicate outer field", raw: strings.Replace(canonical, "{", "{\"schema_version\":1,", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := decodeStashReplay(test.raw, fixture.binding, fixture.through); err == nil || !reflect.DeepEqual(got, StashReplayV1{}) {
				t.Fatalf("decode invalid raw=(%+v,%v), want zero and error", got, err)
			}
		})
	}
}

func TestStashReplayDoesNotMutateOrAliasInputsAndResults(t *testing.T) {
	fixture := newStashReplayFixture(t)
	want := cloneStashReplay(t, fixture.value)
	raw, err := encodeStashReplay(fixture.value, fixture.binding, fixture.through)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.value, want) {
		t.Fatal("encodeStashReplay mutated its input")
	}
	first, err := decodeStashReplay(raw, fixture.binding, fixture.through)
	if err != nil {
		t.Fatal(err)
	}
	second, err := decodeStashReplay(raw, fixture.binding, fixture.through)
	if err != nil {
		t.Fatal(err)
	}
	first.SelectedStartTree[0].Data[0] ^= 0xff
	first.AbsorbedOperations[0].Operation.PutRecord.Record.Task.Title = "mutated"
	first.Operations[0].Operation.PutRecord.Record.Task.Description = "mutated"
	if !reflect.DeepEqual(second, want) || !reflect.DeepEqual(fixture.value, want) {
		t.Fatal("decoded replay aliases another result or caller input")
	}
}

type stashReplayFixture struct {
	value   StashReplayV1
	binding types.WorkspaceBinding
	through int64
}

func newStashReplayFixture(t *testing.T) stashReplayFixture {
	t.Helper()
	base := composeFixtureSnapshot(t)
	absorbed := composeTaskOperation(base, "99999999-9999-4999-8999-999999999991", func(task *state.TaskV1) {
		task.Description = "absorbed"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	selected, err := state.ApplyOperation(base, absorbed)
	if err != nil {
		t.Fatal(err)
	}
	first := composeTaskOperation(selected, "99999999-9999-4999-8999-999999999992", func(task *state.TaskV1) {
		task.Title = "later one"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	afterFirst, err := state.ApplyOperation(selected, first)
	if err != nil {
		t.Fatal(err)
	}
	second := composeTaskOperation(afterFirst, "99999999-9999-4999-8999-999999999993", func(task *state.TaskV1) {
		task.Description = "later two"
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
	})
	tree, err := state.EncodeTree(selected)
	if err != nil {
		t.Fatal(err)
	}
	return stashReplayFixture{
		value: StashReplayV1{
			SchemaVersion: 1, SelectedStartTree: tree, SelectedStartDigest: selected.Digest,
			InitialThroughGeneration: 4,
			AbsorbedOperations:       []StoredOperation{{Generation: 4, Operation: absorbed}},
			Operations: []StoredOperation{
				{Generation: 7, Operation: first},
				{Generation: 10, Operation: second},
			},
		},
		binding: types.WorkspaceBinding{
			Scope:      types.WorkspaceScope{ProjectID: base.Config.ProjectID, WorkspaceID: "77777777-7777-4777-8777-777777777777"},
			Checkout:   types.CheckoutIdentity{CanonicalPath: "/checkout", Device: 1, Inode: 2},
			Repository: base.Config.Repository, AcceptedRef: "refs/heads/main",
			AcceptedCommitSHA: strings.Repeat("a", 40), AcceptedTreeDigest: string(base.Digest),
		},
		through: 10,
	}
}

func stashReplayGoldenFixture(t *testing.T) (StashReplayV1, types.WorkspaceBinding) {
	t.Helper()
	snapshot := composeFixtureSnapshot(t)
	snapshot.Actors = map[string]state.Record[state.ActorV1]{}
	snapshot.Tasks = map[string]state.Record[state.TaskV1]{}
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	tree, err = state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	binding := types.WorkspaceBinding{
		Scope:      types.WorkspaceScope{ProjectID: snapshot.Config.ProjectID, WorkspaceID: "77777777-7777-4777-8777-777777777777"},
		Checkout:   types.CheckoutIdentity{CanonicalPath: "/checkout", Device: 1, Inode: 2},
		Repository: snapshot.Config.Repository, AcceptedRef: "refs/heads/main",
		AcceptedCommitSHA: strings.Repeat("a", 40), AcceptedTreeDigest: string(snapshot.Digest),
	}
	return StashReplayV1{
		SchemaVersion: 1, SelectedStartTree: tree, SelectedStartDigest: snapshot.Digest,
		InitialThroughGeneration: 0, AbsorbedOperations: []StoredOperation{}, Operations: []StoredOperation{},
	}, binding
}

func cloneStashReplay(t *testing.T, value StashReplayV1) StashReplayV1 {
	t.Helper()
	raw, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	var cloned StashReplayV1
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func canonicalStashReplay(t *testing.T, value StashReplayV1) string {
	t.Helper()
	raw, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
