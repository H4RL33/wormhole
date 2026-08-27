package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestNewRecordRootConflictPreservesTypedBytesAbsentEvidenceAndStableID(t *testing.T) {
	key := lifecycleKey("task")
	base := lifecycleAbsent()
	ours := lifecycleLive(t, key, "root1")
	theirs := lifecycleTombstone(t, key, "t1")
	got, err := newRecordRootConflict(key, ConflictTombstoneEdit, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != key || got.FieldPath != "" || got.Kind != ConflictTombstoneEdit ||
		!reflect.DeepEqual(got.Base, FieldValue{}) || !reflect.DeepEqual(got.Ours, ours.Root) || !reflect.DeepEqual(got.Theirs, theirs.Root) {
		t.Fatalf("root conflict = %+v", got)
	}
	const wantID = "sha256:f1a17c42a8e4d4bac67bdfa9b3144ebe1dfe6f5656a40ec519f1e245eba141fe"
	if got.ID != wantID {
		t.Fatalf("root conflict ID = %q, want %q", got.ID, wantID)
	}
	oursBefore := cloneMergeFieldValue(ours.Root)
	theirsBefore := cloneMergeFieldValue(theirs.Root)
	got.Ours.Value[0] ^= 1
	got.Theirs.Value[0] ^= 1
	if !reflect.DeepEqual(ours.Root, oursBefore) || !reflect.DeepEqual(theirs.Root, theirsBefore) {
		t.Fatal("root conflict evidence aliases an input")
	}
}

func TestMergeRecordSurfaceMutableAbsentBaseTaskMatrix(t *testing.T) {
	key := lifecycleKey("task")
	a := lifecycleAbsent()
	l0 := lifecycleLive(t, key, "base")
	l1 := lifecycleLive(t, key, "root1")
	l2 := lifecycleLive(t, key, "root2")
	t1 := lifecycleTombstone(t, key, "t1")
	t2 := lifecycleTombstone(t, key, "t2")
	tests := []struct {
		name         string
		ours, theirs recordSurface
		want         recordSurface
		conflicts    []lifecycleConflictWant
	}{
		{name: "absent absent", ours: a, theirs: a, want: a},
		{name: "absent live", ours: a, theirs: l0, want: l0},
		{name: "live absent", ours: l0, theirs: a, want: l0},
		{name: "absent tombstone", ours: a, theirs: t1, want: t1},
		{name: "tombstone absent", ours: t1, theirs: a, want: t1},
		{name: "exact dual live", ours: l1, theirs: l1, want: l1},
		{name: "exact dual tombstone", ours: t1, theirs: t1, want: t1},
		{name: "unequal dual live", ours: l1, theirs: l2, want: l1, conflicts: []lifecycleConflictWant{{"", ConflictSameField}}},
		{name: "live tombstone", ours: l1, theirs: t1, want: l1, conflicts: []lifecycleConflictWant{{"", ConflictTombstoneEdit}}},
		{name: "tombstone live", ours: t1, theirs: l1, want: t1, conflicts: []lifecycleConflictWant{{"", ConflictTombstoneEdit}}},
		{name: "unequal dual tombstone", ours: t1, theirs: t2, want: t1, conflicts: []lifecycleConflictWant{{"", ConflictSameField}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertLifecycleMerge(t, key, a, test.ours, test.theirs, test.want, test.conflicts)
		})
	}
}

func TestMergeRecordSurfaceMutableAbsentBaseKBIndependentRootBodyMask(t *testing.T) {
	key := lifecycleKey("kb_article")
	a := lifecycleAbsent()
	l0 := lifecycleLive(t, key, "base")
	root1 := lifecycleLive(t, key, "root1")
	body1 := lifecycleLive(t, key, "body1")
	body2 := lifecycleLive(t, key, "body2")
	t1 := lifecycleTombstone(t, key, "t1")
	tests := []struct {
		name         string
		ours, theirs recordSurface
		conflicts    []lifecycleConflictWant
	}{
		{name: "only root competes", ours: root1, theirs: l0, conflicts: []lifecycleConflictWant{{"", ConflictSameField}}},
		{name: "only body competes", ours: body1, theirs: body2, conflicts: []lifecycleConflictWant{{"/body", ConflictSameField}}},
		{name: "root and body compete", ours: root1, theirs: body1, conflicts: []lifecycleConflictWant{{"", ConflictSameField}, {"/body", ConflictSameField}}},
		{name: "live tombstone", ours: body1, theirs: t1, conflicts: []lifecycleConflictWant{{"", ConflictTombstoneEdit}, {"/body", ConflictTombstoneBody}}},
		{name: "tombstone live", ours: t1, theirs: body1, conflicts: []lifecycleConflictWant{{"", ConflictTombstoneEdit}, {"/body", ConflictTombstoneBody}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertLifecycleMerge(t, key, a, test.ours, test.theirs, test.ours, test.conflicts)
		})
	}

	got, err := mergeRecordSurface(key, a, body1, body2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != 1 {
		t.Fatalf("body conflicts = %+v", got.Conflicts)
	}
	conflict := got.Conflicts[0]
	if !reflect.DeepEqual(conflict.Base, FieldValue{}) || !reflect.DeepEqual(conflict.Ours, body1.Body) || !reflect.DeepEqual(conflict.Theirs, body2.Body) {
		t.Fatalf("body evidence = %+v", conflict)
	}
	const wantBodyID = "sha256:48dac5c0254289c121f578a2f3a9e8527004865d39c42100f33bd7deaa40eeb3"
	if conflict.ID != wantBodyID {
		t.Fatalf("body conflict ID = %q, want %q", conflict.ID, wantBodyID)
	}
}

func TestMergeRecordSurfaceImmutableAbsentAndOldLiveMatrix(t *testing.T) {
	for _, kind := range []string{"event", "git_link"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			a := lifecycleAbsent()
			l0 := lifecycleLive(t, key, "base")
			l1 := lifecycleLive(t, key, "root1")
			l2 := lifecycleLive(t, key, "root2")
			immutable := []lifecycleConflictWant{{"", ConflictImmutableRecord}}
			for _, test := range []struct {
				name               string
				base, ours, theirs recordSurface
				want               recordSurface
				conflicts          []lifecycleConflictWant
			}{
				{name: "new absent absent", base: a, ours: a, theirs: a, want: a},
				{name: "new ours live", base: a, ours: l1, theirs: a, want: l1},
				{name: "new theirs live", base: a, ours: a, theirs: l1, want: l1},
				{name: "new exact dual live", base: a, ours: l1, theirs: l1, want: l1},
				{name: "new unequal dual live", base: a, ours: l1, theirs: l2, want: l1, conflicts: immutable},
				{name: "old unchanged", base: l0, ours: l0, theirs: l0, want: l0},
				{name: "old ours mutation", base: l0, ours: l1, theirs: l0, want: l1, conflicts: immutable},
				{name: "old theirs mutation", base: l0, ours: l0, theirs: l1, want: l0, conflicts: immutable},
				{name: "old equal mutation", base: l0, ours: l1, theirs: l1, want: l1, conflicts: immutable},
				{name: "old unequal mutation", base: l0, ours: l1, theirs: l2, want: l1, conflicts: immutable},
				{name: "old absent absent", base: l0, ours: a, theirs: a, want: a, conflicts: immutable},
				{name: "old ours absent", base: l0, ours: a, theirs: l0, want: a, conflicts: immutable},
				{name: "old theirs absent", base: l0, ours: l0, theirs: a, want: l0, conflicts: immutable},
			} {
				t.Run(test.name, func(t *testing.T) {
					assertLifecycleMerge(t, key, test.base, test.ours, test.theirs, test.want, test.conflicts)
				})
			}
		})
	}
}

func TestMergeRecordSurfaceConflictIsDeterministicProvisionalOursAndDoesNotAlias(t *testing.T) {
	key := lifecycleKey("kb_article")
	base := lifecycleAbsent()
	ours := lifecycleLive(t, key, "root_body1")
	theirs := lifecycleTombstone(t, key, "t1")
	oursBefore, err := cloneRecordSurface(ours)
	if err != nil {
		t.Fatal(err)
	}
	theirsBefore, err := cloneRecordSurface(theirs)
	if err != nil {
		t.Fatal(err)
	}
	want, err := mergeRecordSurface(key, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want.Surface, ours) || len(want.Conflicts) != 2 || want.Conflicts[0].FieldPath != "" || want.Conflicts[1].FieldPath != "/body" {
		t.Fatalf("conflict result = %+v", want)
	}
	for run := 0; run < 50; run++ {
		got, err := mergeRecordSurface(key, base, ours, theirs)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, want)
		}
	}
	want.Surface.Root.Value[0] ^= 1
	want.Surface.Body.Value[0] ^= 1
	want.Conflicts[0].Ours.Value[0] ^= 1
	want.Conflicts[1].Ours.Value[0] ^= 1
	if !reflect.DeepEqual(ours, oursBefore) || !reflect.DeepEqual(theirs, theirsBefore) {
		t.Fatal("conflict result aliases an input")
	}

	selected := lifecycleLive(t, lifecycleKey("task"), "root1")
	selectedBefore, err := cloneRecordSurface(selected)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := mergeRecordSurface(lifecycleKey("task"), lifecycleAbsent(), lifecycleAbsent(), selected)
	if err != nil {
		t.Fatal(err)
	}
	clean.Surface.Root.Value[0] ^= 1
	if !reflect.DeepEqual(selected, selectedBefore) {
		t.Fatal("clean selected result aliases its input")
	}
}

func TestMergeRecordSurfaceProjectCleanMatrix(t *testing.T) {
	key := lifecycleKey("project")
	base := projectLifecycleSurface(t, "base")
	name := projectLifecycleSurface(t, "name1")
	aliases := projectLifecycleSurface(t, "aliases2")
	merged := projectLifecycleSurface(t, "name1_aliases2")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
		want         recordSurface
	}{
		{name: "exact", ours: base, theirs: base, want: base},
		{name: "ours semantic edit", ours: name, theirs: base, want: name},
		{name: "theirs semantic edit", ours: base, theirs: aliases, want: aliases},
		{name: "disjoint name and aliases", ours: name, theirs: aliases, want: merged},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseBefore, err := cloneRecordSurface(base)
			if err != nil {
				t.Fatal(err)
			}
			oursBefore, err := cloneRecordSurface(test.ours)
			if err != nil {
				t.Fatal(err)
			}
			theirsBefore, err := cloneRecordSurface(test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mergeRecordSurface(key, base, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			assertLifecycleResult(t, key, got, test.want, nil)
			got.Surface.Root.Value[0] ^= 1
			if !reflect.DeepEqual(base, baseBefore) || !reflect.DeepEqual(test.ours, oursBefore) || !reflect.DeepEqual(test.theirs, theirsBefore) {
				t.Fatal("clean Project result aliases an input")
			}
		})
	}

	value, err := decodeCanonicalMergeValue[state.ProjectV1](merged.Root)
	if err != nil {
		t.Fatal(err)
	}
	aliasesValue, err := decodeCanonicalMergeValue[state.ProjectV1](aliases.Root)
	if err != nil {
		t.Fatal(err)
	}
	if value.Name != "Project one" || !reflect.DeepEqual(value.Aliases, []string{"project-two"}) || !value.UpdatedAt.Equal(aliasesValue.UpdatedAt) {
		t.Fatalf("disjoint Project value = %+v", value)
	}
}

func TestMergeRecordSurfaceProjectConflictUsesExactOursAndSortedTypedEvidence(t *testing.T) {
	key := lifecycleKey("project")
	base := projectLifecycleSurface(t, "base")
	ours := projectLifecycleSurface(t, "name_aliases1")
	theirs := projectLifecycleSurface(t, "name_aliases2")
	baseBefore, err := cloneRecordSurface(base)
	if err != nil {
		t.Fatal(err)
	}
	oursBefore, err := cloneRecordSurface(ours)
	if err != nil {
		t.Fatal(err)
	}
	theirsBefore, err := cloneRecordSurface(theirs)
	if err != nil {
		t.Fatal(err)
	}
	want, err := mergeRecordSurface(key, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want.Surface, ours) || len(want.Conflicts) != 2 {
		t.Fatalf("conflicted Project = %+v, want exact Ours provisional and two conflicts", want)
	}
	for index, expected := range []struct {
		path               string
		base, ours, theirs string
	}{
		{path: "/aliases", base: `[]`, ours: `["project-one"]`, theirs: `["project-two"]`},
		{path: "/name", base: `"Wormhole"`, ours: `"Project one"`, theirs: `"Project two"`},
	} {
		conflict := want.Conflicts[index]
		if conflict.Key != key || conflict.FieldPath != expected.path || conflict.Kind != ConflictSameField || conflict.ID == "" ||
			string(conflict.Base.Value) != expected.base || string(conflict.Ours.Value) != expected.ours || string(conflict.Theirs.Value) != expected.theirs {
			t.Fatalf("Project conflict %d = %+v", index, conflict)
		}
	}
	oursValue, err := decodeCanonicalMergeValue[state.ProjectV1](ours.Root)
	if err != nil {
		t.Fatal(err)
	}
	provisionalValue, err := decodeCanonicalMergeValue[state.ProjectV1](want.Surface.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !provisionalValue.UpdatedAt.Equal(oursValue.UpdatedAt) {
		t.Fatalf("Project provisional updated_at = %s, want exact Ours %s", provisionalValue.UpdatedAt, oursValue.UpdatedAt)
	}
	for run := 0; run < 50; run++ {
		got, err := mergeRecordSurface(key, base, ours, theirs)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, want)
		}
	}
	secondConflictBefore := cloneMergeFieldValue(want.Conflicts[1].Ours)
	want.Surface.Root.Value[0] ^= 1
	want.Conflicts[0].Ours.Value[0] ^= 1
	if !reflect.DeepEqual(want.Conflicts[1].Ours, secondConflictBefore) || !reflect.DeepEqual(base, baseBefore) ||
		!reflect.DeepEqual(ours, oursBefore) || !reflect.DeepEqual(theirs, theirsBefore) {
		t.Fatal("Project conflict result aliases another conflict or an input")
	}
}

func TestMergeRecordSurfaceProjectTimestampOnlyUsesBaseSemanticsAndTimestamp(t *testing.T) {
	key := lifecycleKey("project")
	base := projectLifecycleSurface(t, "base")
	updated1 := projectLifecycleSurface(t, "updated1")
	updated2 := projectLifecycleSurface(t, "updated2")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
	}{
		{name: "ours only", ours: updated1, theirs: base},
		{name: "theirs only", ours: base, theirs: updated1},
		{name: "both", ours: updated1, theirs: updated2},
		{name: "equal sides", ours: updated1, theirs: updated1},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertLifecycleMerge(t, key, base, test.ours, test.theirs, base, nil)
		})
	}
}

func TestMergeRecordSurfaceProjectAlwaysChecksCreatedAt(t *testing.T) {
	key := lifecycleKey("project")
	base := projectLifecycleSurface(t, "base")
	changed := projectLifecycleSurface(t, "created1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
	}{
		{name: "ours changed", ours: changed, theirs: base},
		{name: "theirs changed", ours: base, theirs: changed},
		{name: "equal changed sides", ours: changed, theirs: changed},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeRecordSurface(key, base, test.ours, test.theirs)
			if !errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
				t.Fatalf("Project created_at merge = (%+v, %v), want exact zero ErrOperationPrecondition", got, err)
			}
		})
	}
}

func TestMergeRecordSurfaceProjectRejectsAbsentAndTombstoneAtEveryEndpoint(t *testing.T) {
	key := lifecycleKey("project")
	live := projectLifecycleSurface(t, "base")
	for _, state := range []recordEndpointState{recordEndpointAbsent, recordEndpointTombstone} {
		for _, endpoint := range []string{"base", "ours", "theirs"} {
			t.Run(endpoint+"_state_"+string(rune('0'+state)), func(t *testing.T) {
				invalid := recordSurface{State: state}
				base, ours, theirs := live, live, live
				switch endpoint {
				case "base":
					base = invalid
				case "ours":
					ours = invalid
				case "theirs":
					theirs = invalid
				}
				got, err := mergeRecordSurface(key, base, ours, theirs)
				if err == nil || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
					t.Fatalf("invalid Project endpoint = (%+v, %v), want exact zero validation error", got, err)
				}
			})
		}
	}
}

func TestMergeRecordSurfaceInvalidAndDeferredPathsReturnExactZero(t *testing.T) {
	taskKey := lifecycleKey("task")
	taskLive := lifecycleLive(t, taskKey, "base")
	taskTomb := lifecycleTombstone(t, taskKey, "t1")
	eventKey := lifecycleKey("event")
	eventLive := lifecycleLive(t, eventKey, "base")
	tests := []struct {
		name               string
		key                state.RecordKey
		base, ours, theirs recordSurface
	}{
		{name: "invalid endpoint", key: taskKey, base: lifecycleAbsent(), ours: recordSurface{State: recordEndpointState(99)}, theirs: lifecycleAbsent()},
		{name: "immutable tombstone", key: eventKey, base: lifecycleAbsent(), ours: eventLive, theirs: taskTomb},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeRecordSurface(test.key, test.base, test.ours, test.theirs)
			if err == nil || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
				t.Fatalf("invalid/deferred merge = (%+v, %v)", got, err)
			}
		})
	}
	if got, err := newRecordRootConflict(taskKey, ConflictSameField, lifecycleAbsent(), recordSurface{State: recordEndpointState(99)}, taskLive); err == nil || !reflect.DeepEqual(got, Conflict{}) {
		t.Fatalf("invalid root conflict = (%+v, %v)", got, err)
	}
}

func TestMergeRecordSurfaceConflictEvidenceOrientationAndStableIDs(t *testing.T) {
	tests := []struct {
		name               string
		key                state.RecordKey
		base, ours, theirs recordSurface
		wants              []lifecycleConflictEvidenceWant
	}{
		{
			name: "reversed KB tombstone to live", key: lifecycleKey("kb_article"),
			base: lifecycleAbsent(), ours: lifecycleTombstone(t, lifecycleKey("kb_article"), "t1"), theirs: lifecycleLive(t, lifecycleKey("kb_article"), "root_body1"),
			wants: []lifecycleConflictEvidenceWant{
				{path: "", kind: ConflictTombstoneEdit, id: "sha256:6fbd5fc3453576c67d24a7181e155d5059326f9987fdec854fd93f10302106f0"},
				{path: "/body", kind: ConflictTombstoneBody, id: "sha256:18fdd079f283f39c2f0c554febe13a129b5e6e9161a3d24c6ec58716a40eef0c"},
			},
		},
		{
			name: "old immutable mutation", key: lifecycleKey("event"),
			base: lifecycleLive(t, lifecycleKey("event"), "base"), ours: lifecycleLive(t, lifecycleKey("event"), "root1"), theirs: lifecycleLive(t, lifecycleKey("event"), "base"),
			wants: []lifecycleConflictEvidenceWant{{path: "", kind: ConflictImmutableRecord, id: "sha256:673bc86a55d76cbaea334a9b5906d46e1baf95b966e690ee74ede8efab0d986b"}},
		},
		{
			name: "old immutable disappearance", key: lifecycleKey("event"),
			base: lifecycleLive(t, lifecycleKey("event"), "base"), ours: lifecycleAbsent(), theirs: lifecycleLive(t, lifecycleKey("event"), "base"),
			wants: []lifecycleConflictEvidenceWant{{path: "", kind: ConflictImmutableRecord, id: "sha256:e7b38bb6dec6115525ada603a285b2221ec2fd2ba432c145d1fb586c39af9ffb"}},
		},
		{
			name: "unequal tombstones", key: lifecycleKey("task"),
			base: lifecycleAbsent(), ours: lifecycleTombstone(t, lifecycleKey("task"), "t1"), theirs: lifecycleTombstone(t, lifecycleKey("task"), "t2"),
			wants: []lifecycleConflictEvidenceWant{{path: "", kind: ConflictSameField, id: "sha256:26172652d33a21e22837a7ee97479672f4e606d732bb1df27fa5bfc2e716437c"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want, err := mergeRecordSurface(test.key, test.base, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want.Surface, test.ours) || len(want.Conflicts) != len(test.wants) {
				t.Fatalf("oriented result = %+v", want)
			}
			for index, expected := range test.wants {
				conflict := want.Conflicts[index]
				var baseEvidence, oursEvidence, theirsEvidence FieldValue
				if expected.path == "" {
					baseEvidence, oursEvidence, theirsEvidence = test.base.Root, test.ours.Root, test.theirs.Root
				} else {
					baseEvidence, oursEvidence, theirsEvidence = test.base.Body, test.ours.Body, test.theirs.Body
				}
				if conflict.Key != test.key || conflict.FieldPath != expected.path || conflict.Kind != expected.kind || conflict.ID != expected.id ||
					!reflect.DeepEqual(conflict.Base, baseEvidence) || !reflect.DeepEqual(conflict.Ours, oursEvidence) || !reflect.DeepEqual(conflict.Theirs, theirsEvidence) {
					t.Fatalf("conflict %d = %+v, want %+v", index, conflict, expected)
				}
			}
			for run := 0; run < 25; run++ {
				got, err := mergeRecordSurface(test.key, test.base, test.ours, test.theirs)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, want)
				}
			}
		})
	}
}

func TestMergeRecordSurfaceAbsentBaseDualLiveUpdatedAtIsRootConflict(t *testing.T) {
	for _, kind := range []string{"task", "kb_article"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			base := lifecycleAbsent()
			ours := lifecycleLive(t, key, "updated1")
			theirs := lifecycleLive(t, key, "updated2")
			got, err := mergeRecordSurface(key, base, ours, theirs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Surface, ours) || len(got.Conflicts) != 1 || got.Conflicts[0].FieldPath != "" || got.Conflicts[0].Kind != ConflictSameField ||
				!reflect.DeepEqual(got.Conflicts[0].Base, base.Root) || !reflect.DeepEqual(got.Conflicts[0].Ours, ours.Root) || !reflect.DeepEqual(got.Conflicts[0].Theirs, theirs.Root) {
				t.Fatalf("updated_at-only merge = %+v", got)
			}
			if kind == "kb_article" && (!reflect.DeepEqual(ours.Body, theirs.Body) || got.Conflicts[0].FieldPath == "/body") {
				t.Fatalf("updated_at-only KB body competed: %+v", got)
			}
		})
	}
}

func TestMergeRecordSurfaceCleanKBAdditionsOwnSelectedBody(t *testing.T) {
	key := lifecycleKey("kb_article")
	absent := lifecycleAbsent()
	live := lifecycleLive(t, key, "body1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
	}{
		{name: "one sided ours", ours: live, theirs: absent},
		{name: "one sided theirs", ours: absent, theirs: live},
		{name: "exact dual", ours: live, theirs: live},
	} {
		t.Run(test.name, func(t *testing.T) {
			oursBefore, err := cloneRecordSurface(test.ours)
			if err != nil {
				t.Fatal(err)
			}
			theirsBefore, err := cloneRecordSurface(test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mergeRecordSurface(key, absent, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Conflicts) != 0 || got.Conflicts == nil || string(got.Surface.Body.Value) != `"body one\n"` {
				t.Fatalf("clean KB addition = %+v", got)
			}
			got.Surface.Body.Value[0] ^= 1
			if !reflect.DeepEqual(test.ours, oursBefore) || !reflect.DeepEqual(test.theirs, theirsBefore) {
				t.Fatal("clean KB body aliases an input")
			}
		})
	}
}

func TestMergeRecordSurfaceProvisionalAndEvidenceOwnAllByteSlices(t *testing.T) {
	t.Run("typed roots", func(t *testing.T) {
		key := lifecycleKey("event")
		base := lifecycleLive(t, key, "base")
		ours := lifecycleLive(t, key, "root1")
		theirs := base
		baseBefore := cloneMergeFieldValue(base.Root)
		oursBefore := cloneMergeFieldValue(ours.Root)
		theirsBefore := cloneMergeFieldValue(theirs.Root)
		got, err := mergeRecordSurface(key, base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		conflictOursBefore := cloneMergeFieldValue(got.Conflicts[0].Ours)
		conflictTheirsBefore := cloneMergeFieldValue(got.Conflicts[0].Theirs)
		got.Conflicts[0].Base.Value[0] ^= 1
		if !reflect.DeepEqual(got.Conflicts[0].Ours, conflictOursBefore) || !reflect.DeepEqual(got.Conflicts[0].Theirs, conflictTheirsBefore) ||
			!reflect.DeepEqual(base.Root, baseBefore) || !reflect.DeepEqual(ours.Root, oursBefore) || !reflect.DeepEqual(theirs.Root, theirsBefore) {
			t.Fatal("root Base evidence aliases another field or input")
		}
		provisionalBefore := cloneMergeFieldValue(got.Surface.Root)
		got.Conflicts[0].Ours.Value[0] ^= 1
		if !reflect.DeepEqual(got.Surface.Root, provisionalBefore) || !reflect.DeepEqual(got.Conflicts[0].Theirs, conflictTheirsBefore) || !reflect.DeepEqual(ours.Root, oursBefore) {
			t.Fatal("root Ours evidence aliases provisional, Theirs, or input")
		}
	})

	t.Run("KB bodies", func(t *testing.T) {
		key := lifecycleKey("kb_article")
		base := lifecycleAbsent()
		ours := lifecycleLive(t, key, "body1")
		theirs := lifecycleLive(t, key, "body2")
		oursBefore := cloneMergeFieldValue(ours.Body)
		theirsBefore := cloneMergeFieldValue(theirs.Body)
		got, err := mergeRecordSurface(key, base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		conflictOursBefore := cloneMergeFieldValue(got.Conflicts[0].Ours)
		conflictTheirsBefore := cloneMergeFieldValue(got.Conflicts[0].Theirs)
		got.Surface.Body.Value[0] ^= 1
		if !reflect.DeepEqual(got.Conflicts[0].Ours, conflictOursBefore) || !reflect.DeepEqual(got.Conflicts[0].Theirs, conflictTheirsBefore) ||
			!reflect.DeepEqual(ours.Body, oursBefore) || !reflect.DeepEqual(theirs.Body, theirsBefore) {
			t.Fatal("provisional KB body aliases evidence or input")
		}
		provisionalBefore := cloneMergeFieldValue(got.Surface.Body)
		got.Conflicts[0].Ours.Value[0] ^= 1
		if !reflect.DeepEqual(got.Surface.Body, provisionalBefore) || !reflect.DeepEqual(got.Conflicts[0].Theirs, conflictTheirsBefore) || !reflect.DeepEqual(ours.Body, oursBefore) {
			t.Fatal("KB Ours evidence aliases provisional, Theirs, or input")
		}
	})
}

func TestConflictingRecordSurfaceResultRejectsBodyMaskForNonKB(t *testing.T) {
	key := lifecycleKey("task")
	base := lifecycleAbsent()
	ours := lifecycleLive(t, key, "root1")
	theirs := lifecycleLive(t, key, "root2")
	got, err := conflictingRecordSurfaceResultKinds(key, ConflictSameField, ConflictSameField,
		recordSurfaceConflictMask{Body: true}, base, ours, theirs)
	if err == nil || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
		t.Fatalf("non-KB body mask = (%+v, %v), want exact zero and error", got, err)
	}
}

func TestMergeRecordSurfaceMutableAbsentBaseRepresentativeKinds(t *testing.T) {
	for _, kind := range []string{"actor", "task_link", "channel"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			absent := lifecycleAbsent()
			live1 := lifecycleLive(t, key, "root1")
			live2 := lifecycleLive(t, key, "root2")
			tombstone := lifecycleTombstone(t, key, "t1")
			for _, test := range []struct {
				name         string
				ours, theirs recordSurface
				want         recordSurface
				conflicts    []lifecycleConflictWant
			}{
				{name: "one-sided live", ours: live1, theirs: absent, want: live1},
				{name: "one-sided tombstone", ours: absent, theirs: tombstone, want: tombstone},
				{name: "unequal dual live", ours: live1, theirs: live2, want: live1, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictSameField}}},
			} {
				t.Run(test.name, func(t *testing.T) {
					assertLifecycleMerge(t, key, absent, test.ours, test.theirs, test.want, test.conflicts)
				})
			}
		})
	}
}

func TestExistingTypedLiveSemanticMask(t *testing.T) {
	taskKey := lifecycleKey("task")
	taskBase := lifecycleLive(t, taskKey, "base")
	kbKey := lifecycleKey("kb_article")
	kbBase := lifecycleLive(t, kbKey, "base")
	for _, test := range []struct {
		name       string
		key        state.RecordKey
		base, side recordSurface
		want       recordSurfaceConflictMask
	}{
		{name: "task exact", key: taskKey, base: taskBase, side: taskBase},
		{name: "task updated_at only", key: taskKey, base: taskBase, side: lifecycleLive(t, taskKey, "updated1")},
		{name: "task root", key: taskKey, base: taskBase, side: lifecycleLive(t, taskKey, "root1"), want: recordSurfaceConflictMask{Root: true}},
		{name: "actor root no body", key: lifecycleKey("actor"), base: lifecycleLive(t, lifecycleKey("actor"), "base"), side: lifecycleLive(t, lifecycleKey("actor"), "root1"), want: recordSurfaceConflictMask{Root: true}},
		{name: "KB body", key: kbKey, base: kbBase, side: lifecycleLive(t, kbKey, "body1"), want: recordSurfaceConflictMask{Body: true}},
		{name: "KB root and body", key: kbKey, base: kbBase, side: lifecycleLive(t, kbKey, "root_body1"), want: recordSurfaceConflictMask{Root: true, Body: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := existingTypedLiveSemanticMask(test.key, test.base, test.side)
			if err != nil || got != test.want {
				t.Fatalf("semantic mask = (%+v, %v), want %+v", got, err, test.want)
			}
		})
	}
	if got, err := existingTypedLiveSemanticMask(taskKey, taskBase, lifecycleLive(t, taskKey, "created1")); !errors.Is(err, state.ErrOperationPrecondition) || got != (recordSurfaceConflictMask{}) {
		t.Fatalf("created_at semantic mask = (%+v, %v)", got, err)
	}
	if got, err := existingTypedLiveSemanticMask(taskKey, taskBase, lifecycleTombstone(t, taskKey, "t1")); err == nil || got != (recordSurfaceConflictMask{}) {
		t.Fatalf("tombstone semantic mask = (%+v, %v)", got, err)
	}
}

func TestMergeRecordSurfaceOldLiveTaskLiveMatrix(t *testing.T) {
	key := lifecycleKey("task")
	base := lifecycleLive(t, key, "base")
	root1 := lifecycleLive(t, key, "root1")
	root2 := lifecycleLive(t, key, "root2")
	updated1 := lifecycleLive(t, key, "updated1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
		want         recordSurface
	}{
		{name: "exact", ours: base, theirs: base, want: base},
		{name: "one sided", ours: root1, theirs: base, want: root1},
		{name: "equal mutation", ours: root1, theirs: root1, want: root1},
		{name: "timestamp only", ours: updated1, theirs: base, want: base},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertLifecycleMerge(t, key, base, test.ours, test.theirs, test.want, nil)
		})
	}

	other := lifecycleLive(t, key, "other1")
	disjoint, err := mergeRecordSurface(key, base, root1, other)
	if err != nil {
		t.Fatal(err)
	}
	disjointValue, err := decodeCanonicalMergeValue[state.TaskV1](disjoint.Surface.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(disjoint.Conflicts) != 0 || disjointValue.Title != "task one" || disjointValue.Description != "description one" || !disjointValue.UpdatedAt.Equal(otherTaskUpdatedAt(t, other)) {
		t.Fatalf("disjoint Task merge = %+v, value=%+v", disjoint, disjointValue)
	}

	oursBefore, err := cloneRecordSurface(root1)
	if err != nil {
		t.Fatal(err)
	}
	competing, err := mergeRecordSurface(key, base, root1, root2)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(competing.Surface, root1) || len(competing.Conflicts) != 1 || competing.Conflicts[0].FieldPath != "/title" || competing.Conflicts[0].Kind != ConflictSameField ||
		string(competing.Conflicts[0].Base.Value) != `"Compose task"` || string(competing.Conflicts[0].Ours.Value) != `"task one"` || string(competing.Conflicts[0].Theirs.Value) != `"task two"` {
		t.Fatalf("competing Task merge = %+v", competing)
	}
	competing.Surface.Root.Value[0] ^= 1
	if !reflect.DeepEqual(root1, oursBefore) {
		t.Fatal("old-live conflict provisional aliases Ours")
	}
}

func TestMergeRecordSurfaceExistingLiveKindsTopLevel(t *testing.T) {
	for _, kind := range []string{"project", "actor", "task", "task_link", "kb_article", "channel"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			base := lifecycleLive(t, key, "base")
			root1 := lifecycleLive(t, key, "root1")
			root2 := lifecycleLive(t, key, "root2")
			assertLifecycleMerge(t, key, base, base, base, base, nil)
			assertLifecycleMerge(t, key, base, root1, base, root1, nil)
			got, err := mergeRecordSurface(key, base, root1, root2)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Surface, root1) || len(got.Conflicts) == 0 {
				t.Fatalf("competing %s merge = %+v", kind, got)
			}
		})
	}
}

func TestMergeRecordSurfaceOldLiveDualTombstonesAllMutableKinds(t *testing.T) {
	for _, kind := range []string{"actor", "task", "task_link", "kb_article", "channel"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			base := lifecycleLive(t, key, "base")
			t1 := lifecycleTombstone(t, key, "t1")
			t2 := lifecycleTombstone(t, key, "t2")
			assertLifecycleMerge(t, key, base, t1, t1, t1, nil)
			assertLifecycleMerge(t, key, base, t1, t2, t1, []lifecycleConflictWant{{path: "", kind: ConflictSameField}})
		})
	}
}

func TestMergeRecordSurfaceOldLiveTombstoneVersusTaskLive(t *testing.T) {
	key := lifecycleKey("task")
	base := lifecycleLive(t, key, "base")
	tombstone := lifecycleTombstone(t, key, "t1")
	root := lifecycleLive(t, key, "root1")
	updated := lifecycleLive(t, key, "updated1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
		want         recordSurface
		conflicts    []lifecycleConflictWant
	}{
		{name: "ours deletion unchanged live", ours: tombstone, theirs: base, want: tombstone},
		{name: "theirs deletion unchanged live", ours: base, theirs: tombstone, want: tombstone},
		{name: "ours deletion timestamp live", ours: tombstone, theirs: updated, want: tombstone},
		{name: "theirs deletion timestamp live", ours: updated, theirs: tombstone, want: tombstone},
		{name: "ours deletion root edit", ours: tombstone, theirs: root, want: tombstone, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictTombstoneEdit}}},
		{name: "theirs deletion root edit", ours: root, theirs: tombstone, want: root, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictTombstoneEdit}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertLifecycleMerge(t, key, base, test.ours, test.theirs, test.want, test.conflicts)
		})
	}
}

func TestMergeRecordSurfaceOldLiveTombstoneVersusKBMasks(t *testing.T) {
	key := lifecycleKey("kb_article")
	base := lifecycleLive(t, key, "base")
	tombstone := lifecycleTombstone(t, key, "t1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
		want         recordSurface
		conflicts    []lifecycleConflictWant
	}{
		{name: "ours deletion root", ours: tombstone, theirs: lifecycleLive(t, key, "root1"), want: tombstone, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictTombstoneEdit}}},
		{name: "ours deletion body", ours: tombstone, theirs: lifecycleLive(t, key, "body1"), want: tombstone, conflicts: []lifecycleConflictWant{{path: "/body", kind: ConflictTombstoneBody}}},
		{name: "ours deletion root and body", ours: tombstone, theirs: lifecycleLive(t, key, "root_body1"), want: tombstone, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictTombstoneEdit}, {path: "/body", kind: ConflictTombstoneBody}}},
		{name: "theirs deletion root and body", ours: lifecycleLive(t, key, "root_body1"), theirs: tombstone, want: lifecycleLive(t, key, "root_body1"), conflicts: []lifecycleConflictWant{{path: "", kind: ConflictTombstoneEdit}, {path: "/body", kind: ConflictTombstoneBody}}},
		{name: "ours deletion timestamp", ours: tombstone, theirs: lifecycleLive(t, key, "updated1"), want: tombstone},
		{name: "theirs deletion timestamp", ours: lifecycleLive(t, key, "updated1"), theirs: tombstone, want: tombstone},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertLifecycleMerge(t, key, base, test.ours, test.theirs, test.want, test.conflicts)
		})
	}
}

func TestMergeRecordSurfaceOldLiveRejectsCreatedAtAndRawDeletion(t *testing.T) {
	key := lifecycleKey("task")
	base := lifecycleLive(t, key, "base")
	created := lifecycleLive(t, key, "created1")
	tombstone := lifecycleTombstone(t, key, "t1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
		want         error
	}{
		{name: "live live created_at", ours: created, theirs: base, want: state.ErrOperationPrecondition},
		{name: "equal live created_at", ours: created, theirs: created, want: state.ErrOperationPrecondition},
		{name: "tombstone live created_at", ours: tombstone, theirs: created, want: state.ErrOperationPrecondition},
		{name: "live tombstone created_at", ours: created, theirs: tombstone, want: state.ErrOperationPrecondition},
		{name: "ours raw deletion", ours: lifecycleAbsent(), theirs: base, want: ErrRawRecordDeletion},
		{name: "theirs raw deletion", ours: base, theirs: lifecycleAbsent(), want: ErrRawRecordDeletion},
		{name: "both raw deletion", ours: lifecycleAbsent(), theirs: lifecycleAbsent(), want: ErrRawRecordDeletion},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeRecordSurface(key, base, test.ours, test.theirs)
			if !errors.Is(err, test.want) || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
				t.Fatalf("old-live invalid = (%+v, %v), want zero %v", got, err, test.want)
			}
		})
	}
}

func TestMergeRecordSurfaceOldLiveConflictEvidenceIsDeterministicAndOwned(t *testing.T) {
	key := lifecycleKey("kb_article")
	base := lifecycleLive(t, key, "base")
	ours := lifecycleTombstone(t, key, "t1")
	theirs := lifecycleLive(t, key, "root_body1")
	baseBefore, err := cloneRecordSurface(base)
	if err != nil {
		t.Fatal(err)
	}
	oursBefore, err := cloneRecordSurface(ours)
	if err != nil {
		t.Fatal(err)
	}
	theirsBefore, err := cloneRecordSurface(theirs)
	if err != nil {
		t.Fatal(err)
	}
	want, err := mergeRecordSurface(key, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want.Surface, ours) || len(want.Conflicts) != 2 ||
		want.Conflicts[0].FieldPath != "" || want.Conflicts[0].Kind != ConflictTombstoneEdit ||
		!reflect.DeepEqual(want.Conflicts[0].Base, base.Root) || !reflect.DeepEqual(want.Conflicts[0].Ours, ours.Root) || !reflect.DeepEqual(want.Conflicts[0].Theirs, theirs.Root) ||
		want.Conflicts[1].FieldPath != "/body" || want.Conflicts[1].Kind != ConflictTombstoneBody ||
		!reflect.DeepEqual(want.Conflicts[1].Base, base.Body) || !reflect.DeepEqual(want.Conflicts[1].Ours, ours.Body) || !reflect.DeepEqual(want.Conflicts[1].Theirs, theirs.Body) {
		t.Fatalf("old-live KB evidence = %+v", want)
	}
	for run := 0; run < 50; run++ {
		got, err := mergeRecordSurface(key, base, ours, theirs)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, want)
		}
	}
	rootOursBefore := cloneMergeFieldValue(want.Conflicts[0].Ours)
	rootTheirsBefore := cloneMergeFieldValue(want.Conflicts[0].Theirs)
	bodyTheirsBefore := cloneMergeFieldValue(want.Conflicts[1].Theirs)
	want.Surface.Root.Value[0] ^= 1
	if !reflect.DeepEqual(want.Conflicts[0].Ours, rootOursBefore) {
		t.Fatal("old-live provisional aliases root evidence")
	}
	want.Conflicts[0].Theirs.Value[0] ^= 1
	want.Conflicts[1].Theirs.Value[0] ^= 1
	if !reflect.DeepEqual(base, baseBefore) || !reflect.DeepEqual(ours, oursBefore) || !reflect.DeepEqual(theirs, theirsBefore) {
		t.Fatal("old-live conflict result aliases an input")
	}
	if reflect.DeepEqual(want.Conflicts[0].Theirs, rootTheirsBefore) || reflect.DeepEqual(want.Conflicts[1].Theirs, bodyTheirsBefore) {
		t.Fatal("ownership mutation probes did not change evidence")
	}
}

func TestMergeRecordSurfaceOldLiveTombstoneRacesRepresentativeMutableKinds(t *testing.T) {
	for _, kind := range []string{"actor", "task_link", "channel"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			base := lifecycleLive(t, key, "base")
			changed := lifecycleLive(t, key, "root1")
			tombstone := lifecycleTombstone(t, key, "t1")
			assertLifecycleMerge(t, key, base, tombstone, base, tombstone, nil)
			assertLifecycleMerge(t, key, base, tombstone, changed, tombstone, []lifecycleConflictWant{{path: "", kind: ConflictTombstoneEdit}})
			if kind == "channel" {
				assertLifecycleMerge(t, key, base, changed, tombstone, changed, []lifecycleConflictWant{{path: "", kind: ConflictTombstoneEdit}})
			}
		})
	}
}

func TestMergeRecordSurfaceOldLivePreconditionOrdering(t *testing.T) {
	taskKey := lifecycleKey("task")
	taskBase := lifecycleLive(t, taskKey, "base")
	taskCreated := lifecycleLive(t, taskKey, "created1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
	}{
		{name: "ours deletion before invalid theirs", ours: lifecycleAbsent(), theirs: taskCreated},
		{name: "invalid ours before theirs deletion", ours: taskCreated, theirs: lifecycleAbsent()},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeRecordSurface(taskKey, taskBase, test.ours, test.theirs)
			if !errors.Is(err, ErrRawRecordDeletion) || errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
				t.Fatalf("precondition ordering = (%+v, %v), want exact-zero ErrRawRecordDeletion", got, err)
			}
		})
	}

	channelKey := lifecycleKey("channel")
	channelBase := lifecycleLive(t, channelKey, "base")
	channelCreated := lifecycleLive(t, channelKey, "created1")
	channelTombstone := lifecycleTombstone(t, channelKey, "t1")
	if got, err := mergeRecordSurface(channelKey, channelBase, channelTombstone, channelCreated); !errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
		t.Fatalf("Channel created_at tombstone race = (%+v, %v)", got, err)
	}
}

func TestMergeRecordSurfaceOldLiveKBMarkdownConflictUsesExactOursProvisional(t *testing.T) {
	key := lifecycleKey("kb_article")
	base := lifecycleLive(t, key, "base")
	ours := lifecycleLive(t, key, "body1")
	theirs := lifecycleLive(t, key, "body2")
	oursBefore, err := cloneRecordSurface(ours)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mergeRecordSurface(key, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Surface, ours) || len(got.Conflicts) != 1 || got.Conflicts[0].FieldPath != "/body" || got.Conflicts[0].Kind != ConflictMarkdown ||
		!reflect.DeepEqual(got.Conflicts[0].Base, base.Body) || !reflect.DeepEqual(got.Conflicts[0].Ours, ours.Body) || !reflect.DeepEqual(got.Conflicts[0].Theirs, theirs.Body) {
		t.Fatalf("KB Markdown conflict = %+v", got)
	}
	conflictOursBefore := cloneMergeFieldValue(got.Conflicts[0].Ours)
	got.Surface.Body.Value[0] ^= 1
	if !reflect.DeepEqual(got.Conflicts[0].Ours, conflictOursBefore) || !reflect.DeepEqual(ours, oursBefore) {
		t.Fatal("KB Markdown provisional aliases evidence or Ours")
	}
}

func TestMergeRecordSurfaceOldTombstoneTaskMatrix(t *testing.T) {
	key := lifecycleKey("task")
	base := lifecycleTombstone(t, key, "t0")
	t1 := lifecycleTombstone(t, key, "t1")
	t2 := lifecycleTombstone(t, key, "t2")
	t3 := lifecycleTombstone(t, key, "t3")
	live1 := lifecycleLive(t, key, "root1")
	live2 := lifecycleLive(t, key, "root2")
	updated1 := lifecycleLive(t, key, "updated1")
	updated2 := lifecycleLive(t, key, "updated2")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
		want         recordSurface
		conflicts    []lifecycleConflictWant
		wantError    error
	}{
		{name: "exact changed tombstone", ours: t1, theirs: t1, want: t1},
		{name: "one-sided tombstone ours", ours: t1, theirs: base, want: t1},
		{name: "one-sided tombstone theirs", ours: base, theirs: t2, want: t2},
		{name: "divergent tombstones", ours: t2, theirs: t3, want: t2, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictSameField}}},
		{name: "one-sided resurrection ours", ours: live1, theirs: base, want: live1},
		{name: "one-sided resurrection theirs", ours: base, theirs: live1, want: live1},
		{name: "exact resurrection", ours: live1, theirs: live1, want: live1},
		{name: "divergent resurrection", ours: live1, theirs: live2, want: live1, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}}},
		{name: "updated_at-only resurrection divergence", ours: updated1, theirs: updated2, want: updated1, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}}},
		{name: "live versus changed tombstone", ours: live1, theirs: t1, want: live1, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}}},
		{name: "changed tombstone versus live", ours: t1, theirs: live1, want: t1, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}}},
		{name: "ours absent", ours: lifecycleAbsent(), theirs: base, wantError: ErrRawRecordDeletion},
		{name: "theirs absent", ours: base, theirs: lifecycleAbsent(), wantError: ErrRawRecordDeletion},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeRecordSurface(key, base, test.ours, test.theirs)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
					t.Fatalf("old-tombstone error = (%+v, %v), want zero %v", got, err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertLifecycleResult(t, key, got, test.want, test.conflicts)
		})
	}
}

func TestMergeRecordSurfaceOldTombstoneKBIndependentMasks(t *testing.T) {
	key := lifecycleKey("kb_article")
	base := lifecycleTombstone(t, key, "t0")
	liveBase := lifecycleLive(t, key, "base")
	root := lifecycleLive(t, key, "root1")
	body1 := lifecycleLive(t, key, "body1")
	body2 := lifecycleLive(t, key, "body2")
	t1 := lifecycleTombstone(t, key, "t1")
	for _, test := range []struct {
		name         string
		ours, theirs recordSurface
		want         recordSurface
		conflicts    []lifecycleConflictWant
	}{
		{name: "exact resurrection", ours: body1, theirs: body1, want: body1},
		{name: "root only", ours: root, theirs: liveBase, want: root, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}}},
		{name: "body only", ours: body1, theirs: body2, want: body1, conflicts: []lifecycleConflictWant{{path: "/body", kind: ConflictInvalidResurrection}}},
		{name: "root and body", ours: root, theirs: body1, want: root, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}, {path: "/body", kind: ConflictInvalidResurrection}}},
		{name: "live versus changed tombstone", ours: body1, theirs: t1, want: body1, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}, {path: "/body", kind: ConflictInvalidResurrection}}},
		{name: "changed tombstone versus live", ours: t1, theirs: body1, want: t1, conflicts: []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}, {path: "/body", kind: ConflictInvalidResurrection}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeRecordSurface(key, base, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			assertLifecycleResult(t, key, got, test.want, test.conflicts)
			for _, conflict := range got.Conflicts {
				if conflict.FieldPath == "/body" && !reflect.DeepEqual(conflict.Base, base.Body) {
					t.Fatalf("old-tombstone Base body evidence = %+v, want absent", conflict.Base)
				}
			}
		})
	}
}

func TestMergeRecordSurfaceOldTombstoneAllMutableKindsFastPathsAndMixedConflict(t *testing.T) {
	for _, kind := range []string{"actor", "task", "task_link", "kb_article", "channel"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			base := lifecycleTombstone(t, key, "t0")
			t1 := lifecycleTombstone(t, key, "t1")
			live := lifecycleLive(t, key, "base")
			assertLifecycleMerge(t, key, base, t1, t1, t1, nil)
			assertLifecycleMerge(t, key, base, base, t1, t1, nil)
			assertLifecycleMerge(t, key, base, base, live, live, nil)
			conflicts := []lifecycleConflictWant{{path: "", kind: ConflictInvalidResurrection}}
			if kind == "kb_article" {
				conflicts = append(conflicts, lifecycleConflictWant{path: "/body", kind: ConflictInvalidResurrection})
			}
			assertLifecycleMerge(t, key, base, live, t1, live, conflicts)
		})
	}
}

func TestMergeRecordSurfaceOldTombstoneSelectsProvenanceByteExact(t *testing.T) {
	key := lifecycleKey("task")
	base := lifecycleTombstone(t, key, "t0")
	provenance := lifecycleTombstone(t, key, "provenance")
	got, err := mergeRecordSurface(key, base, base, provenance)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Surface, provenance) || got.Conflicts == nil || len(got.Conflicts) != 0 {
		t.Fatalf("selected provenance = %+v, want byte-exact %+v", got, provenance)
	}
	want, err := decodeCanonicalMergeValue[state.TombstoneV1](provenance.Root)
	if err != nil {
		t.Fatal(err)
	}
	gotValue, err := decodeCanonicalMergeValue[state.TombstoneV1](got.Surface.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue.DeletedBy, want.DeletedBy) || !gotValue.DeletedAt.Equal(want.DeletedAt) || !reflect.DeepEqual(gotValue.Extensions, want.Extensions) {
		t.Fatalf("provenance fields = %+v, want %+v", gotValue, want)
	}
}

func TestMergeRecordSurfaceOldTombstoneAllowsFreshValidCreatedAtResurrection(t *testing.T) {
	for _, kind := range []string{"task", "kb_article", "channel"} {
		t.Run(kind, func(t *testing.T) {
			key := lifecycleKey(kind)
			base := lifecycleTombstone(t, key, "t0")
			fresh := lifecycleLive(t, key, "created1")
			assertLifecycleMerge(t, key, base, base, fresh, fresh, nil)
		})
	}
}

func TestMergeRecordSurfaceOldTombstoneOrientationIDsAndOwnership(t *testing.T) {
	key := lifecycleKey("kb_article")
	base := lifecycleTombstone(t, key, "t0")
	root := lifecycleLive(t, key, "root1")
	body := lifecycleLive(t, key, "body1")
	live := lifecycleLive(t, key, "root_body1")
	t1 := lifecycleTombstone(t, key, "t1")
	for _, test := range []struct {
		name           string
		ours, theirs   recordSurface
		rootID, bodyID string
	}{
		{name: "dual resurrection", ours: root, theirs: body, rootID: "sha256:3c2b540d3175325c92e9560f5702d6e28112ae0ef2e61146c7e1ebb4b14f89e3", bodyID: "sha256:a4aca311cd8caf585e842fa3b37bca4b28d0cba54600dc4bfb775e57bcde5063"},
		{name: "live then tombstone", ours: live, theirs: t1, rootID: "sha256:da41339856c77190e09fe9ab2d72bd9aec6093456c1f39e31df69b875031f236", bodyID: "sha256:95175073c085effbb0491cbe3cba1342e2150ea83764a8d5af1cfc8ec6a44342"},
		{name: "tombstone then live", ours: t1, theirs: live, rootID: "sha256:8c86dc9fd1e541db5e75509d78a013786f52ba1eab8e603067e773db64a8f724", bodyID: "sha256:cfeb57a15c3d8049af06a7886f369ab211cc106ecd193f29c1d76bf21e4d10d4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			want, err := mergeRecordSurface(key, base, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want.Surface, test.ours) || len(want.Conflicts) != 2 {
				t.Fatalf("orientation result = %+v", want)
			}
			ids := []string{test.rootID, test.bodyID}
			for index, conflict := range want.Conflicts {
				var baseEvidence, oursEvidence, theirsEvidence FieldValue
				if conflict.FieldPath == "" {
					baseEvidence, oursEvidence, theirsEvidence = base.Root, test.ours.Root, test.theirs.Root
				} else {
					baseEvidence, oursEvidence, theirsEvidence = base.Body, test.ours.Body, test.theirs.Body
				}
				if conflict.Kind != ConflictInvalidResurrection || conflict.ID != ids[index] || !reflect.DeepEqual(conflict.Base, baseEvidence) ||
					!reflect.DeepEqual(conflict.Ours, oursEvidence) || !reflect.DeepEqual(conflict.Theirs, theirsEvidence) {
					t.Errorf("conflict %d = %+v, want ID %s", index, conflict, ids[index])
				}
			}
			for run := 0; run < 50; run++ {
				got, err := mergeRecordSurface(key, base, test.ours, test.theirs)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, want)
				}
			}
		})
	}

	oursBefore, err := cloneRecordSurface(live)
	if err != nil {
		t.Fatal(err)
	}
	theirsBefore, err := cloneRecordSurface(t1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mergeRecordSurface(key, base, live, t1)
	if err != nil {
		t.Fatal(err)
	}
	rootEvidenceBefore := cloneMergeFieldValue(got.Conflicts[0].Ours)
	bodyEvidenceBefore := cloneMergeFieldValue(got.Conflicts[1].Ours)
	got.Surface.Root.Value[0] ^= 1
	got.Surface.Body.Value[0] ^= 1
	if !reflect.DeepEqual(got.Conflicts[0].Ours, rootEvidenceBefore) || !reflect.DeepEqual(got.Conflicts[1].Ours, bodyEvidenceBefore) ||
		!reflect.DeepEqual(live, oursBefore) || !reflect.DeepEqual(t1, theirsBefore) {
		t.Fatal("old-tombstone provisional aliases evidence or inputs")
	}
}

type lifecycleConflictEvidenceWant struct {
	path string
	kind ConflictKind
	id   string
}

type lifecycleConflictWant struct {
	path string
	kind ConflictKind
}

func assertLifecycleMerge(t *testing.T, key state.RecordKey, base, ours, theirs, want recordSurface, conflicts []lifecycleConflictWant) {
	t.Helper()
	got, err := mergeRecordSurface(key, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	assertLifecycleResult(t, key, got, want, conflicts)
}

func assertLifecycleResult(t *testing.T, key state.RecordKey, got recordSurfaceMergeResult, want recordSurface, conflicts []lifecycleConflictWant) {
	t.Helper()
	if !reflect.DeepEqual(got.Surface, want) {
		t.Fatalf("surface = %+v, want %+v", got.Surface, want)
	}
	if got.Conflicts == nil || len(got.Conflicts) != len(conflicts) {
		t.Fatalf("conflicts = %+v, want %+v", got.Conflicts, conflicts)
	}
	for index, wantConflict := range conflicts {
		if got.Conflicts[index].Key != key || got.Conflicts[index].FieldPath != wantConflict.path || got.Conflicts[index].Kind != wantConflict.kind || got.Conflicts[index].ID == "" {
			t.Fatalf("conflict %d = %+v, want %+v", index, got.Conflicts[index], wantConflict)
		}
	}
}

func lifecycleAbsent() recordSurface {
	return recordSurface{State: recordEndpointAbsent}
}

func lifecycleKey(kind string) state.RecordKey {
	switch kind {
	case "project":
		return state.RecordKey{Kind: kind, ID: "00000000-0000-4000-8000-000000000001"}
	case "actor":
		return state.RecordKey{Kind: kind, ID: composeActorID}
	case "task":
		return state.RecordKey{Kind: kind, ID: composeTaskID}
	case "task_link":
		return state.RecordKey{Kind: kind, ID: diffTaskLinkID}
	case "kb_article":
		return state.RecordKey{Kind: kind, ID: diffArticleID}
	case "channel":
		return state.RecordKey{Kind: kind, ID: diffChannelID}
	case "event":
		return state.RecordKey{Kind: kind, ID: diffEventID}
	case "git_link":
		return state.RecordKey{Kind: kind, ID: diffGitLinkID}
	default:
		return state.RecordKey{Kind: kind, ID: composeTaskID}
	}
}

func lifecycleLive(t *testing.T, key state.RecordKey, variant string) recordSurface {
	t.Helper()
	snapshot := recordAllKindsSnapshot(t)
	if variant == "base" {
		return recordSurfaceFromSnapshot(t, snapshot, key)
	}
	rank := 1
	if variant == "root2" || variant == "body2" || variant == "updated2" || variant == "other1" {
		rank = 2
	}
	token := "one"
	if rank == 2 {
		token = "two"
	}
	rootChange := variant == "root1" || variant == "root2" || variant == "root_body1"
	bodyChange := variant == "body1" || variant == "body2" || variant == "root_body1"
	updatedChange := variant == "updated1" || variant == "updated2"
	otherChange := variant == "other1"
	createdChange := variant == "created1"
	if rootChange {
		switch key.Kind {
		case "project":
			snapshot.Project.Name = "project " + token
			snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(time.Duration(rank) * time.Minute)
		case "actor":
			snapshot.Actors[key.ID].Value.DisplayName = "actor " + token
		case "task":
			snapshot.Tasks[key.ID].Value.Title = "task " + token
			snapshot.Tasks[key.ID].Value.UpdatedAt = snapshot.Tasks[key.ID].Value.UpdatedAt.Add(time.Duration(rank) * time.Minute)
		case "task_link":
			if rank == 1 {
				snapshot.TaskLinks[key.ID].Value.LinkType = "task"
				snapshot.TaskLinks[key.ID].Value.TargetID = composeTaskID
			} else {
				snapshot.TaskLinks[key.ID].Value.LinkType = "event"
				snapshot.TaskLinks[key.ID].Value.TargetID = diffEventID
			}
		case "kb_article":
			snapshot.Articles[key.ID].Value.Title = "article " + token
			snapshot.Articles[key.ID].Value.UpdatedAt = snapshot.Articles[key.ID].Value.UpdatedAt.Add(time.Duration(rank) * time.Minute)
		case "channel":
			snapshot.Channels[key.ID].Value.Name = "channel " + token
		case "event":
			note := "event " + token
			event := snapshot.Events[key.ID]
			event.Note = &note
			snapshot.Events[key.ID] = event
		case "git_link":
			snapshot.GitLinks[key.ID].Value.Summary = "link " + token
		default:
			t.Fatalf("root lifecycle variant unsupported for %s", key.Kind)
		}
	}
	if otherChange {
		switch key.Kind {
		case "task":
			snapshot.Tasks[key.ID].Value.Description = "description one"
			snapshot.Tasks[key.ID].Value.UpdatedAt = snapshot.Tasks[key.ID].Value.UpdatedAt.Add(2 * time.Minute)
		default:
			t.Fatalf("other-field lifecycle variant unsupported for %s", key.Kind)
		}
	}
	if updatedChange {
		switch key.Kind {
		case "task":
			snapshot.Tasks[key.ID].Value.UpdatedAt = snapshot.Tasks[key.ID].Value.UpdatedAt.Add(time.Duration(rank) * time.Minute)
		case "kb_article":
			snapshot.Articles[key.ID].Value.UpdatedAt = snapshot.Articles[key.ID].Value.UpdatedAt.Add(time.Duration(rank) * time.Minute)
		default:
			t.Fatalf("updated_at lifecycle variant unsupported for %s", key.Kind)
		}
	}
	if createdChange {
		switch key.Kind {
		case "task":
			snapshot.Tasks[key.ID].Value.CreatedAt = snapshot.Tasks[key.ID].Value.CreatedAt.Add(-time.Hour)
		case "kb_article":
			snapshot.Articles[key.ID].Value.CreatedAt = snapshot.Articles[key.ID].Value.CreatedAt.Add(-time.Hour)
		case "channel":
			snapshot.Channels[key.ID].Value.CreatedAt = snapshot.Channels[key.ID].Value.CreatedAt.Add(-time.Hour)
		default:
			t.Fatalf("created_at lifecycle variant unsupported for %s", key.Kind)
		}
	}
	if bodyChange {
		if key.Kind != "kb_article" {
			t.Fatalf("body lifecycle variant unsupported for %s", key.Kind)
		}
		article := snapshot.Articles[key.ID]
		article.Body = []byte("body " + token + "\n")
		snapshot.Articles[key.ID] = article
	}
	snapshot = diffCanonicalSnapshot(t, snapshot)
	return recordSurfaceFromSnapshot(t, snapshot, key)
}

func projectLifecycleSurface(t *testing.T, variant string) recordSurface {
	t.Helper()
	snapshot := recordAllKindsSnapshot(t)
	switch variant {
	case "base":
	case "name1":
		snapshot.Project.Name = "Project one"
		snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(time.Minute)
	case "aliases2":
		snapshot.Project.Aliases = []string{"project-two"}
		snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(2 * time.Minute)
	case "name1_aliases2":
		snapshot.Project.Name = "Project one"
		snapshot.Project.Aliases = []string{"project-two"}
		snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(2 * time.Minute)
	case "name_aliases1":
		snapshot.Project.Name = "Project one"
		snapshot.Project.Aliases = []string{"project-one"}
		snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(time.Minute)
	case "name_aliases2":
		snapshot.Project.Name = "Project two"
		snapshot.Project.Aliases = []string{"project-two"}
		snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(2 * time.Minute)
	case "updated1":
		snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(time.Minute)
	case "updated2":
		snapshot.Project.UpdatedAt = snapshot.Project.UpdatedAt.Add(2 * time.Minute)
	case "created1":
		snapshot.Project.CreatedAt = snapshot.Project.CreatedAt.Add(-time.Hour)
	default:
		t.Fatalf("unknown Project lifecycle variant %q", variant)
	}
	snapshot = diffCanonicalSnapshot(t, snapshot)
	return recordSurfaceFromSnapshot(t, snapshot, lifecycleKey("project"))
}

func lifecycleTombstone(t *testing.T, key state.RecordKey, variant string) recordSurface {
	t.Helper()
	surface := recordTombstoneSurface(t, key)
	value, err := decodeCanonicalMergeValue[state.TombstoneV1](surface.Root)
	if err != nil {
		t.Fatal(err)
	}
	switch variant {
	case "t0":
	case "t1":
		value.DeletedContentDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	case "t2":
		value.DeletedContentDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	case "t3":
		value.DeletedContentDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	case "provenance":
		value.DeletedContentDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		value.DeletedBy.HumanPrincipalID = "77777777-7777-4777-8777-777777777777"
		value.DeletedBy.OccurredAt = value.DeletedBy.OccurredAt.Add(time.Hour)
		value.DeletedAt = value.DeletedAt.Add(time.Hour)
		value.Extensions = state.ExtensionsV1{
			"com.example.audit": {SchemaVersion: 1, Data: json.RawMessage(`{"note":"selected"}`)},
		}
	default:
		t.Fatalf("unknown tombstone variant %q", variant)
	}
	raw, err := canonicalFieldJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return recordSurface{State: recordEndpointTombstone, Root: FieldValue{Present: true, Value: bytes.Clone(raw)}}
}

func otherTaskUpdatedAt(t *testing.T, surface recordSurface) time.Time {
	t.Helper()
	value, err := decodeCanonicalMergeValue[state.TaskV1](surface.Root)
	if err != nil {
		t.Fatal(err)
	}
	return value.UpdatedAt
}
