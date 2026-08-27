package projectstate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestMergeCanonicalJSONFastPathsAndNoAliases(t *testing.T) {
	tests := []struct {
		name               string
		base, ours, theirs FieldValue
		want               FieldValue
	}{
		{
			name: "equal sides", base: mergeJSONValue(`{"value":"base"}`),
			ours: mergeJSONValue(`{"value":"shared"}`), theirs: mergeJSONValue(`{"value":"shared"}`),
			want: mergeJSONValue(`{"value":"shared"}`),
		},
		{
			name: "ours unchanged", base: mergeJSONValue(`{"value":"base"}`),
			ours: mergeJSONValue(`{"value":"base"}`), theirs: mergeJSONValue(`{"value":"theirs"}`),
			want: mergeJSONValue(`{"value":"theirs"}`),
		},
		{
			name: "theirs unchanged", base: mergeJSONValue(`{"value":"base"}`),
			ours: mergeJSONValue(`{"value":"ours"}`), theirs: mergeJSONValue(`{"value":"base"}`),
			want: mergeJSONValue(`{"value":"ours"}`),
		},
		{
			name: "one-sided presence", base: FieldValue{}, ours: FieldValue{},
			theirs: mergeJSONValue(`null`), want: mergeJSONValue(`null`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, ours, theirs := cloneMergeFieldValue(test.base), cloneMergeFieldValue(test.ours), cloneMergeFieldValue(test.theirs)
			got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, base, ours, theirs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) || conflicts == nil || len(conflicts) != 0 {
				t.Fatalf("mergeCanonicalJSON() = (%+v, %+v), want (%+v, [])", got, conflicts, test.want)
			}
			if got.Present && len(got.Value) > 0 {
				got.Value[0] ^= 1
			}
			if !reflect.DeepEqual(base, test.base) || !reflect.DeepEqual(ours, test.ours) || !reflect.DeepEqual(theirs, test.theirs) {
				t.Fatal("merge result aliases an input")
			}
		})
	}
}

func TestMergeCanonicalJSONRecursesThroughSortedDisjointObjects(t *testing.T) {
	base := mergeJSONValue(`{"alpha":{"left":1,"right":2},"zeta":0}`)
	ours := mergeJSONValue(`{"alpha":{"left":10,"right":2},"zeta":0}`)
	theirs := mergeJSONValue(`{"alpha":{"left":1,"right":20},"zeta":3}`)
	want := mergeJSONValue(`{"alpha":{"left":10,"right":20},"zeta":3}`)

	got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || len(conflicts) != 0 {
		t.Fatalf("mergeCanonicalJSON() = (%s, %+v), want (%s, [])", got.Value, conflicts, want.Value)
	}
}

func TestMergeCanonicalJSONSameFieldsConflictAndContinue(t *testing.T) {
	base := mergeJSONValue(`{"alpha":0,"title":"base","zeta":0}`)
	ours := mergeJSONValue(`{"alpha":1,"title":"ours","zeta":2}`)
	theirs := mergeJSONValue(`{"alpha":3,"title":"theirs","zeta":4}`)

	got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, mergeJSONValue(`{"alpha":1,"title":"ours","zeta":2}`)) {
		t.Fatalf("provisional merged JSON = %s, want ours at conflicting leaves", got.Value)
	}
	if len(conflicts) != 3 {
		t.Fatalf("conflicts = %+v, want three", conflicts)
	}
	if gotPaths := []string{conflicts[0].FieldPath, conflicts[1].FieldPath, conflicts[2].FieldPath}; !reflect.DeepEqual(gotPaths, []string{"/alpha", "/title", "/zeta"}) {
		t.Fatalf("conflict paths = %v", gotPaths)
	}
	for _, conflict := range conflicts {
		if conflict.Kind != ConflictSameField || conflict.Key != mergeJSONKey() || conflict.ID == "" {
			t.Fatalf("conflict = %+v", conflict)
		}
	}
	title := conflicts[1]
	if title.Base.Present != true || string(title.Base.Value) != `"base"` ||
		string(title.Ours.Value) != `"ours"` || string(title.Theirs.Value) != `"theirs"` {
		t.Fatalf("title evidence = %+v", title)
	}
	const wantID = "sha256:1c7adaf28e9f811f3039b88a685ed8f88510c5d3445d66c213d96bbc1ef3ca92"
	if title.ID != wantID {
		t.Fatalf("title conflict ID = %q, want %q", title.ID, wantID)
	}
}

func TestMergeCanonicalJSONPresenceNullAndNoSyntheticObjectBase(t *testing.T) {
	t.Run("absent differs from null", func(t *testing.T) {
		base := mergeJSONValue(`{}`)
		ours := mergeJSONValue(`{"value":null}`)
		theirs := mergeJSONValue(`{"value":"theirs"}`)
		_, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		if len(conflicts) != 1 || conflicts[0].FieldPath != "/value" || conflicts[0].Base.Present ||
			!conflicts[0].Ours.Present || string(conflicts[0].Ours.Value) != "null" {
			t.Fatalf("conflicts = %+v", conflicts)
		}
	})

	t.Run("absent object is atomic", func(t *testing.T) {
		base := mergeJSONValue(`{}`)
		ours := mergeJSONValue(`{"new":{"ours":1}}`)
		theirs := mergeJSONValue(`{"new":{"theirs":2}}`)
		_, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		if len(conflicts) != 1 || conflicts[0].FieldPath != "/new" || conflicts[0].Base.Present {
			t.Fatalf("conflicts = %+v, want one conflict at absent object", conflicts)
		}
	})
}

func TestMergeCanonicalJSONMemberAddRemoveAndRemoveVersusEdit(t *testing.T) {
	tests := []struct {
		name               string
		base, ours, theirs FieldValue
		want               FieldValue
		wantConflictPath   string
	}{
		{
			name: "one-sided add", base: mergeJSONValue(`{"keep":1}`), ours: mergeJSONValue(`{"keep":1,"new":2}`),
			theirs: mergeJSONValue(`{"keep":1}`), want: mergeJSONValue(`{"keep":1,"new":2}`),
		},
		{
			name: "one-sided remove", base: mergeJSONValue(`{"keep":1,"remove":2}`), ours: mergeJSONValue(`{"keep":1}`),
			theirs: mergeJSONValue(`{"keep":1,"remove":2}`), want: mergeJSONValue(`{"keep":1}`),
		},
		{
			name: "equal dual add", base: mergeJSONValue(`{"keep":1}`), ours: mergeJSONValue(`{"keep":1,"new":2}`),
			theirs: mergeJSONValue(`{"keep":1,"new":2}`), want: mergeJSONValue(`{"keep":1,"new":2}`),
		},
		{
			name: "remove versus edit", base: mergeJSONValue(`{"keep":1,"value":{"nested":2}}`), ours: mergeJSONValue(`{"keep":1}`),
			theirs: mergeJSONValue(`{"keep":1,"value":{"nested":3}}`), want: mergeJSONValue(`{"keep":1}`), wantConflictPath: "/value",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, test.base, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("merged = %s, want %s", got.Value, test.want.Value)
			}
			if test.wantConflictPath == "" {
				if len(conflicts) != 0 {
					t.Fatalf("conflicts = %+v, want none", conflicts)
				}
			} else if len(conflicts) != 1 || conflicts[0].FieldPath != test.wantConflictPath || conflicts[0].Ours.Present {
				t.Fatalf("conflicts = %+v", conflicts)
			}
		})
	}
}

func TestMergeCanonicalJSONEscapesPathsAndTreatsNonObjectsAtomically(t *testing.T) {
	tests := []struct {
		name               string
		base, ours, theirs FieldValue
		wantPath           string
	}{
		{
			name: "escaped member", base: mergeJSONValue(`{"a/b~c":0}`),
			ours: mergeJSONValue(`{"a/b~c":1}`), theirs: mergeJSONValue(`{"a/b~c":2}`), wantPath: "/a~1b~0c",
		},
		{
			name: "array", base: mergeJSONValue(`{"items":[0,0]}`),
			ours: mergeJSONValue(`{"items":[1,0]}`), theirs: mergeJSONValue(`{"items":[0,2]}`), wantPath: "/items",
		},
		{
			name: "type change at root", base: mergeJSONValue(`0`),
			ours: mergeJSONValue(`{"ours":1}`), theirs: mergeJSONValue(`{"theirs":2}`), wantPath: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, test.base, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			if len(conflicts) != 1 || conflicts[0].FieldPath != test.wantPath {
				t.Fatalf("conflicts = %+v, want path %q", conflicts, test.wantPath)
			}
		})
	}
}

func TestMergeCanonicalJSONSkipsOnlyRootUpdatedAt(t *testing.T) {
	base := mergeJSONValue(`{"nested":{"updated_at":"base-nested"},"title":"base","updated_at":"2026-01-01T00:00:00Z"}`)
	ours := mergeJSONValue(`{"nested":{"updated_at":"ours-nested"},"title":"ours","updated_at":"2026-02-01T00:00:00Z"}`)
	theirs := mergeJSONValue(`{"nested":{"updated_at":"theirs-nested"},"title":"base","updated_at":"2026-03-01T00:00:00Z"}`)

	got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey(), SkipRootUpdatedAt: true}, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	want := mergeJSONValue(`{"nested":{"updated_at":"ours-nested"},"title":"ours","updated_at":"2026-01-01T00:00:00Z"}`)
	if !reflect.DeepEqual(got, want) || len(conflicts) != 1 || conflicts[0].FieldPath != "/nested/updated_at" {
		t.Fatalf("merge = (%s, %+v), want base root metadata and nested conflict", got.Value, conflicts)
	}

	installed, err := installRootUpdatedAt(got, mergeJSONValue(`"2026-04-01T00:00:00Z"`))
	if err != nil {
		t.Fatal(err)
	}
	wantInstalled := mergeJSONValue(`{"nested":{"updated_at":"ours-nested"},"title":"ours","updated_at":"2026-04-01T00:00:00Z"}`)
	if !reflect.DeepEqual(installed, wantInstalled) {
		t.Fatalf("installRootUpdatedAt = %s, want %s", installed.Value, wantInstalled.Value)
	}
}

func TestMergeCanonicalJSONRootUpdatedAtSkipPrecedesWholeValueFastPaths(t *testing.T) {
	tests := []struct {
		name               string
		base, ours, theirs FieldValue
		want               FieldValue
	}{
		{
			name:   "equal sides changed only metadata",
			base:   mergeJSONValue(`{"title":"base","updated_at":"base"}`),
			ours:   mergeJSONValue(`{"title":"base","updated_at":"shared"}`),
			theirs: mergeJSONValue(`{"title":"base","updated_at":"shared"}`),
			want:   mergeJSONValue(`{"title":"base","updated_at":"base"}`),
		},
		{
			name:   "ours unchanged",
			base:   mergeJSONValue(`{"title":"base","updated_at":"base"}`),
			ours:   mergeJSONValue(`{"title":"base","updated_at":"base"}`),
			theirs: mergeJSONValue(`{"title":"theirs","updated_at":"theirs"}`),
			want:   mergeJSONValue(`{"title":"theirs","updated_at":"base"}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey(), SkipRootUpdatedAt: true}, test.base, test.ours, test.theirs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) || len(conflicts) != 0 {
				t.Fatalf("mergeCanonicalJSON = (%s, %+v), want (%s, [])", got.Value, conflicts, test.want.Value)
			}
		})
	}
}

func TestSemanticJSONEqualIgnoresOnlyRootUpdatedAt(t *testing.T) {
	equal, err := semanticJSONEqual(
		mergeJSONValue(`{"nested":{"updated_at":1},"updated_at":"old","value":2}`),
		mergeJSONValue(`{"nested":{"updated_at":1},"updated_at":"new","value":2}`),
	)
	if err != nil || !equal {
		t.Fatalf("semanticJSONEqual timestamp-only = %v, %v", equal, err)
	}
	equal, err = semanticJSONEqual(
		mergeJSONValue(`{"nested":{"updated_at":1},"updated_at":"old","value":2}`),
		mergeJSONValue(`{"nested":{"updated_at":2},"updated_at":"old","value":2}`),
	)
	if err != nil || equal {
		t.Fatalf("semanticJSONEqual nested change = %v, %v", equal, err)
	}
	equal, err = semanticJSONEqual(FieldValue{}, FieldValue{})
	if err != nil || !equal {
		t.Fatalf("semanticJSONEqual absent = %v, %v", equal, err)
	}
}

func TestMergeCanonicalJSONRejectsNoncanonicalFieldValuesWithZeroResult(t *testing.T) {
	tests := []struct {
		name  string
		value FieldValue
	}{
		{name: "absent with value", value: FieldValue{Value: json.RawMessage(`null`)}},
		{name: "present empty", value: FieldValue{Present: true}},
		{name: "invalid", value: FieldValue{Present: true, Value: json.RawMessage(`{`)}},
		{name: "trailing", value: FieldValue{Present: true, Value: json.RawMessage(`null null`)}},
		{name: "whitespace", value: FieldValue{Present: true, Value: json.RawMessage(` {}`)}},
		{name: "canonical newline", value: FieldValue{Present: true, Value: json.RawMessage("{}\n")}},
		{name: "unsorted object", value: FieldValue{Present: true, Value: json.RawMessage(`{"z":1,"a":2}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, test.value, mergeJSONValue(`null`), mergeJSONValue(`null`))
			if err == nil || !reflect.DeepEqual(got, FieldValue{}) || conflicts != nil {
				t.Fatalf("mergeCanonicalJSON invalid = (%+v, %+v, %v)", got, conflicts, err)
			}
		})
	}
}

func TestInstallRootUpdatedAtRejectsInvalidInputsAndDoesNotAlias(t *testing.T) {
	root := mergeJSONValue(`{"updated_at":"old","value":1}`)
	metadata := mergeJSONValue(`"new"`)
	got, err := installRootUpdatedAt(root, metadata)
	if err != nil {
		t.Fatal(err)
	}
	got.Value[0] ^= 1
	if string(root.Value) != `{"updated_at":"old","value":1}` || string(metadata.Value) != `"new"` {
		t.Fatal("installed metadata aliases input")
	}

	tests := []struct {
		name           string
		root, metadata FieldValue
	}{
		{name: "absent root", root: FieldValue{}, metadata: mergeJSONValue(`"new"`)},
		{name: "scalar root", root: mergeJSONValue(`1`), metadata: mergeJSONValue(`"new"`)},
		{name: "absent metadata", root: root, metadata: FieldValue{}},
		{name: "noncanonical metadata", root: root, metadata: FieldValue{Present: true, Value: json.RawMessage(` "new"`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := installRootUpdatedAt(test.root, test.metadata)
			if err == nil || !reflect.DeepEqual(got, FieldValue{}) {
				t.Fatalf("installRootUpdatedAt invalid = (%+v, %v)", got, err)
			}
		})
	}
}

func TestNewConflictValidatesClonesAndOrientsEvidence(t *testing.T) {
	base := mergeJSONValue(`"base"`)
	ours := mergeJSONValue(`"ours"`)
	theirs := mergeJSONValue(`"theirs"`)
	conflict, err := newConflict(mergeJSONKey(), "/title", ConflictSameField, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	const wantID = "sha256:1c7adaf28e9f811f3039b88a685ed8f88510c5d3445d66c213d96bbc1ef3ca92"
	if conflict.ID != wantID || string(conflict.Base.Value) != `"base"` || string(conflict.Ours.Value) != `"ours"` || string(conflict.Theirs.Value) != `"theirs"` {
		t.Fatalf("newConflict = %+v", conflict)
	}
	base.Value[0], ours.Value[0], theirs.Value[0] = 'x', 'x', 'x'
	if string(conflict.Base.Value) != `"base"` || string(conflict.Ours.Value) != `"ours"` || string(conflict.Theirs.Value) != `"theirs"` {
		t.Fatal("conflict evidence aliases input")
	}

	got, err := newConflict(mergeJSONKey(), "/title", ConflictSameField,
		FieldValue{Present: true, Value: json.RawMessage(` "base"`)}, ours, theirs)
	if err == nil || !reflect.DeepEqual(got, Conflict{}) {
		t.Fatalf("newConflict invalid = (%+v, %v)", got, err)
	}
}

func TestMergeCanonicalJSONDeterministicAcrossMapOrderAndRepeatedRuns(t *testing.T) {
	base := mergeCanonicalMapValue(t, []mergeMapEntry{{"z", 0}, {"a", 0}, {"m", 0}})
	ours := mergeCanonicalMapValue(t, []mergeMapEntry{{"m", 1}, {"a", 2}, {"z", 0}})
	theirs := mergeCanonicalMapValue(t, []mergeMapEntry{{"a", 3}, {"z", 4}, {"m", 2}})
	want, wantConflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if gotPaths := []string{wantConflicts[0].FieldPath, wantConflicts[1].FieldPath}; !reflect.DeepEqual(gotPaths, []string{"/a", "/m"}) {
		t.Fatalf("conflict paths = %v", gotPaths)
	}
	for run := 0; run < 100; run++ {
		got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, base, ours, theirs)
		if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(conflicts, wantConflicts) {
			t.Fatalf("run %d = (%+v, %+v, %v), want (%+v, %+v, nil)", run, got, conflicts, err, want, wantConflicts)
		}
	}

	reversedBase := mergeCanonicalMapValue(t, []mergeMapEntry{{"m", 0}, {"a", 0}, {"z", 0}})
	reversedOurs := mergeCanonicalMapValue(t, []mergeMapEntry{{"z", 0}, {"a", 2}, {"m", 1}})
	reversedTheirs := mergeCanonicalMapValue(t, []mergeMapEntry{{"m", 2}, {"z", 4}, {"a", 3}})
	got, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: mergeJSONKey()}, reversedBase, reversedOurs, reversedTheirs)
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(conflicts, wantConflicts) {
		t.Fatalf("reversed maps = (%+v, %+v, %v)", got, conflicts, err)
	}

	got.Value[0] ^= 1
	conflicts[0].Base.Value[0] ^= 1
	if bytes.Equal(got.Value, want.Value) || bytes.Equal(conflicts[0].Base.Value, wantConflicts[0].Base.Value) {
		t.Fatal("determinism fixtures unexpectedly alias returned values")
	}
}

type mergeMapEntry struct {
	key   string
	value int
}

func mergeCanonicalMapValue(t *testing.T, entries []mergeMapEntry) FieldValue {
	t.Helper()
	value := make(map[string]int, len(entries))
	for _, entry := range entries {
		value[entry.key] = entry.value
	}
	raw, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return FieldValue{Present: true, Value: bytes.Clone(raw[:len(raw)-1])}
}

func mergeJSONValue(raw string) FieldValue {
	return FieldValue{Present: true, Value: json.RawMessage(raw)}
}

func cloneMergeFieldValue(value FieldValue) FieldValue {
	return FieldValue{Present: value.Present, Value: bytes.Clone(value.Value)}
}

func mergeJSONKey() state.RecordKey {
	return state.RecordKey{Kind: "task", ID: composeTaskID}
}
