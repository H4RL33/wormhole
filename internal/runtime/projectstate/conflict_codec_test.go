package projectstate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestConflictCodecCanonicalCleanCase(t *testing.T) {
	evidence, err := encodeWorkspaceConflictEvidence(nil)
	if err != nil || evidence == nil || len(evidence) != 0 {
		t.Fatalf("encode nil conflicts = (%v, %v), want non-nil empty evidence", evidence, err)
	}
	conflicts, err := decodeWorkspaceConflictOccurrences(nil)
	if err != nil || conflicts == nil || len(conflicts) != 0 {
		t.Fatalf("decode nil occurrences = (%v, %v), want non-nil empty conflicts", conflicts, err)
	}
}

func TestConflictCodecRoundTripsTask3ConflictsAndOwnsBytes(t *testing.T) {
	conflicts := conflictCodecTask3Conflicts(t)
	evidence, err := encodeWorkspaceConflictEvidence(conflicts)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 6 {
		t.Fatalf("encoded evidence count = %d, want 6", len(evidence))
	}
	for index, row := range evidence {
		if row.ConflictKind != string(conflicts[index].Kind) || row.ConflictID != conflicts[index].ID ||
			!strings.HasSuffix(row.BaseJSON, "\n") || !strings.HasSuffix(row.OursJSON, "\n") || !strings.HasSuffix(row.TheirsJSON, "\n") {
			t.Fatalf("encoded evidence %d = %+v", index, row)
		}
	}
	const fixedID = "sha256:1c7adaf28e9f811f3039b88a685ed8f88510c5d3445d66c213d96bbc1ef3ca92"
	fixed, err := newConflict(state.RecordKey{Kind: "task", ID: composeTaskID}, "/title", ConflictSameField,
		codecField(`"base"`), codecField(`"ours"`), codecField(`"theirs"`))
	if err != nil || fixed.ID != fixedID {
		t.Fatalf("fixed conflict ID = %q, %v; want %q", fixed.ID, err, fixedID)
	}

	rows := conflictCodecOccurrences(evidence)
	decoded, err := decodeWorkspaceConflictOccurrences(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, conflicts) {
		t.Fatalf("decoded conflicts = %+v, want %+v", decoded, conflicts)
	}

	beforeEncoded := evidence[0]
	conflicts[0].Base.Value[0] ^= 1
	if evidence[0] != beforeEncoded {
		t.Fatal("encoded evidence aliases input conflicts")
	}
	beforeRow := rows[0]
	decoded[0].Base.Value[0] ^= 1
	if rows[0] != beforeRow {
		t.Fatal("decoded conflicts alias occurrence rows")
	}
}

func TestConflictCodecRoundTripsAbsentNullEscapedPointerAndEveryRoot(t *testing.T) {
	conflict, err := newConflict(state.RecordKey{Kind: "kb_article", ID: diffArticleID}, "/frontmatter/a~0b~1c", ConflictSameField,
		FieldValue{}, codecField(`null`), codecField(`{"nested":1}`))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := encodeWorkspaceConflictEvidence([]Conflict{conflict})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWorkspaceConflictOccurrences(conflictCodecOccurrences(evidence))
	if err != nil || !reflect.DeepEqual(decoded, []Conflict{conflict}) {
		t.Fatalf("escaped/absent/null round trip = (%+v, %v)", decoded, err)
	}

	snapshot := recordAllKindsSnapshot(t)
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := recordSurfaceKeys(surfaces)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		t.Run("live_"+key.Kind, func(t *testing.T) {
			generic := conflictCodecEnvelopeValue(t, surfaces[key].Root)
			got, err := rehydrateConflictRoot(key, generic)
			if err != nil || !reflect.DeepEqual(got, surfaces[key].Root) {
				t.Fatalf("rehydrate %s = (%s, %v), want %s", key.Kind, got.Value, err, surfaces[key].Root.Value)
			}
			generic.Value[0] ^= 1
			if !reflect.DeepEqual(got, surfaces[key].Root) {
				t.Fatal("rehydrated root aliases envelope bytes")
			}
		})
	}
	for _, kind := range []string{"actor", "task", "task_link", "kb_article", "channel"} {
		key := lifecycleKey(kind)
		t.Run("tombstone_"+kind, func(t *testing.T) {
			root := recordTombstoneSurface(t, key).Root
			got, err := rehydrateConflictRoot(key, conflictCodecEnvelopeValue(t, root))
			if err != nil || !reflect.DeepEqual(got, root) {
				t.Fatalf("rehydrate tombstone %s = (%s, %v), want %s", kind, got.Value, err, root.Value)
			}
		})
	}
}

func TestConflictCodecRejectsMalformedAndNoncanonicalEnvelopes(t *testing.T) {
	row := conflictCodecValidOccurrence(t)
	canonicalAbsent, err := state.CanonicalJSON(FieldValue{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		value string
	}{
		{name: "malformed", value: "{"},
		{name: "unknown field", value: `{"present":false,"extra":1}` + "\n"},
		{name: "trailing value", value: string(canonicalAbsent) + `{}`},
		{name: "noncanonical whitespace", value: " " + string(canonicalAbsent)},
		{name: "present empty", value: `{"present":true}` + "\n"},
		{name: "absent with bytes", value: `{"present":false,"value":1}` + "\n"},
		{name: "multiple inner values", value: `{"present":true,"value":1 2}` + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := row
			corrupt.BaseJSON = test.value
			if got, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{corrupt}); err == nil || got != nil {
				t.Fatalf("decode corrupt envelope = (%+v, %v), want nil error result", got, err)
			}
		})
	}
}

func TestConflictCodecRejectsBadKeyPathKindCombinationAndProjectAbsence(t *testing.T) {
	valid, err := newConflict(state.RecordKey{Kind: "task", ID: composeTaskID}, "/title", ConflictSameField,
		codecField(`"base"`), codecField(`"ours"`), codecField(`"theirs"`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*Conflict)
	}{
		{name: "unknown key kind", edit: func(c *Conflict) { c.Key.Kind = "unknown" }},
		{name: "noncanonical UUID", edit: func(c *Conflict) {
			c.Key.ID = strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		}},
		{name: "relative path", edit: func(c *Conflict) { c.FieldPath = "title" }},
		{name: "malformed escape", edit: func(c *Conflict) { c.FieldPath = "/a~2b" }},
		{name: "control path", edit: func(c *Conflict) { c.FieldPath = "/a\nb" }},
		{name: "unknown conflict kind", edit: func(c *Conflict) { c.Kind = "unknown" }},
		{name: "markdown task", edit: func(c *Conflict) { c.Kind = ConflictMarkdown }},
		{name: "markdown KB root", edit: func(c *Conflict) { c.Key.Kind = "kb_article"; c.FieldPath = ""; c.Kind = ConflictMarkdown }},
		{name: "immutable task", edit: func(c *Conflict) { c.FieldPath = ""; c.Kind = ConflictImmutableRecord }},
		{name: "tombstone event", edit: func(c *Conflict) { c.Key.Kind = "event"; c.FieldPath = ""; c.Kind = ConflictTombstoneEdit }},
		{name: "tombstone body root", edit: func(c *Conflict) { c.Key.Kind = "kb_article"; c.FieldPath = ""; c.Kind = ConflictTombstoneBody }},
		{name: "resurrection body task", edit: func(c *Conflict) { c.FieldPath = "/body"; c.Kind = ConflictInvalidResurrection }},
		{name: "same field immutable", edit: func(c *Conflict) { c.Key.Kind = "event"; c.FieldPath = "/payload" }},
		{name: "same field nested scalar", edit: func(c *Conflict) { c.FieldPath = "/title/nested" }},
		{name: "same field unknown field", edit: func(c *Conflict) { c.FieldPath = "/unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := valid
			test.edit(&corrupt)
			corrupt.ID, _ = conflictID(corrupt)
			if got, err := encodeWorkspaceConflictEvidence([]Conflict{corrupt}); err == nil || got != nil {
				t.Fatalf("encode invalid conflict = (%+v, %v)", got, err)
			}
		})
	}

	project := valid
	project.Key = state.RecordKey{Kind: "project", ID: "00000000-0000-4000-8000-000000000001"}
	project.FieldPath = "/name"
	project.Base = FieldValue{}
	project.ID, _ = conflictID(project)
	if got, err := encodeWorkspaceConflictEvidence([]Conflict{project}); err == nil || got != nil {
		t.Fatalf("encode absent project evidence = (%+v, %v)", got, err)
	}
}

func TestConflictCodecSameFieldAllowlistMatchesTask3Surfaces(t *testing.T) {
	valid := []struct {
		kind string
		path string
	}{
		{kind: "project", path: "/name"},
		{kind: "actor", path: "/public_keys"},
		{kind: "task", path: ""},
		{kind: "task", path: "/title"},
		{kind: "task_link", path: "/target_id"},
		{kind: "kb_article", path: "/frontmatter/a~0b~1c/nested"},
		{kind: "kb_article", path: "/body"},
		{kind: "channel", path: "/name"},
		{kind: "project", path: "/extensions/acme.test"},
		{kind: "task", path: "/extensions/acme.test/data/nested"},
	}
	for _, test := range valid {
		if err := validateConflictShape(state.RecordKey{Kind: test.kind, ID: composeTaskID}, test.path, ConflictSameField); err != nil {
			t.Errorf("valid same_field %s %s rejected: %v", test.kind, test.path, err)
		}
	}
	invalid := []struct {
		kind string
		path string
	}{
		{kind: "project", path: ""},
		{kind: "event", path: "/payload"},
		{kind: "task", path: "/updated_at"},
		{kind: "task", path: "/created_at"},
		{kind: "task", path: "/extensions/"},
		{kind: "task", path: "/extensions/acme.test/schema_version"},
		{kind: "task", path: "/extensions/acme.test/data"},
		{kind: "task", path: "/extensions/not_an_extension"},
		{kind: "kb_article", path: "/frontmatter/"},
		{kind: "kb_article", path: "/frontmatter/ /nested"},
	}
	for _, test := range invalid {
		if err := validateConflictShape(state.RecordKey{Kind: test.kind, ID: composeTaskID}, test.path, ConflictSameField); err == nil {
			t.Errorf("impossible same_field %s %s accepted", test.kind, test.path)
		}
	}
}

func TestConflictCodecRejectsUnsortedDuplicateAndMismatchedIDs(t *testing.T) {
	conflicts := conflictCodecTask3Conflicts(t)
	unsorted := append([]Conflict(nil), conflicts...)
	unsorted[0], unsorted[1] = unsorted[1], unsorted[0]
	if got, err := encodeWorkspaceConflictEvidence(unsorted); err == nil || got != nil {
		t.Fatalf("encode unsorted = (%+v, %v)", got, err)
	}
	duplicate := []Conflict{conflicts[0], conflicts[0]}
	if got, err := encodeWorkspaceConflictEvidence(duplicate); err == nil || got != nil {
		t.Fatalf("encode duplicate = (%+v, %v)", got, err)
	}
	mismatch := conflicts[0]
	mismatch.ID = conflictCodecMutateDigest(mismatch.ID)
	if got, err := encodeWorkspaceConflictEvidence([]Conflict{mismatch}); err == nil || got != nil {
		t.Fatalf("encode mismatched ID = (%+v, %v)", got, err)
	}

	evidence, err := encodeWorkspaceConflictEvidence(conflicts)
	if err != nil {
		t.Fatal(err)
	}
	rows := conflictCodecOccurrences(evidence)
	rows[0], rows[1] = rows[1], rows[0]
	if got, err := decodeWorkspaceConflictOccurrences(rows); err == nil || got != nil {
		t.Fatalf("decode unsorted = (%+v, %v)", got, err)
	}
	rows = conflictCodecOccurrences(evidence)
	rows[1] = rows[0]
	if got, err := decodeWorkspaceConflictOccurrences(rows[:2]); err == nil || got != nil {
		t.Fatalf("decode duplicate = (%+v, %v)", got, err)
	}
	rows = conflictCodecOccurrences(evidence[:1])
	rows[0].ConflictID = conflictCodecMutateDigest(rows[0].ConflictID)
	if got, err := decodeWorkspaceConflictOccurrences(rows); err == nil || got != nil {
		t.Fatalf("decode mismatched ID = (%+v, %v)", got, err)
	}
}

func TestConflictCodecRejectsBadOccurrenceMetadata(t *testing.T) {
	valid := conflictCodecValidOccurrence(t)
	tests := []struct {
		name string
		edit func(*localstore.WorkspaceConflictOccurrence)
	}{
		{name: "occurrence UUID", edit: func(row *localstore.WorkspaceConflictOccurrence) { row.OccurrenceID = "bad" }},
		{name: "occurrence non-v4", edit: func(row *localstore.WorkspaceConflictOccurrence) {
			row.OccurrenceID = "00000000-0000-1000-8000-000000000001"
		}},
		{name: "occurrence uppercase", edit: func(row *localstore.WorkspaceConflictOccurrence) {
			row.OccurrenceID = strings.ToUpper("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		}},
		{name: "created zero", edit: func(row *localstore.WorkspaceConflictOccurrence) { row.CreatedAt = time.Time{} }},
		{name: "created nonzero offset", edit: func(row *localstore.WorkspaceConflictOccurrence) {
			row.CreatedAt = time.Date(2026, 7, 29, 12, 0, 0, 0, time.FixedZone("offset", 3600))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := valid
			test.edit(&row)
			if got, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{row}); err == nil || got != nil {
				t.Fatalf("decode bad metadata = (%+v, %v)", got, err)
			}
		})
	}
	legacy := valid
	legacy.OccurrenceID = legacy.ConflictID
	if got, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{legacy}); err != nil || len(got) != 1 {
		t.Fatalf("decode legacy occurrence = (%+v, %v)", got, err)
	}
}

func TestConflictCodecRejectsWrongTypedRootAndTombstoneInvariants(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	taskKey := state.RecordKey{Kind: "task", ID: composeTaskID}
	actorKey := state.RecordKey{Kind: "actor", ID: composeActorID}
	tests := []struct {
		name string
		key  state.RecordKey
		root FieldValue
	}{
		{name: "wrong concrete type", key: actorKey, root: surfaces[taskKey].Root},
		{name: "wrong header kind", key: taskKey, root: conflictCodecMutateRoot(t, surfaces[taskKey].Root, "kind", "actor")},
		{name: "wrong root ID", key: taskKey, root: conflictCodecMutateRoot(t, surfaces[taskKey].Root, "id", diffSecondTaskID)},
		{name: "root scalar", key: taskKey, root: codecField(`1`)},
		{name: "tombstone entity kind", key: taskKey, root: conflictCodecMutateRoot(t, recordTombstoneSurface(t, taskKey).Root, "entity_kind", "actor")},
		{name: "task tombstone body digest", key: taskKey, root: conflictCodecMutateRoot(t, recordTombstoneSurface(t, taskKey).Root, "deleted_body_digest", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")},
		{name: "KB tombstone missing body digest", key: state.RecordKey{Kind: "kb_article", ID: diffArticleID}, root: conflictCodecMutateRoot(t, recordTombstoneSurface(t, state.RecordKey{Kind: "kb_article", ID: diffArticleID}).Root, "deleted_body_digest", nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conflict := Conflict{Key: test.key, Kind: ConflictSameField, Base: test.root, Ours: test.root, Theirs: test.root}
			var err error
			conflict.ID, err = conflictID(conflict)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := encodeWorkspaceConflictEvidence([]Conflict{conflict}); err == nil || got != nil {
				t.Fatalf("encode wrong root = (%+v, %v)", got, err)
			}
			if got, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{conflictCodecRawOccurrence(t, conflict)}); err == nil || got != nil {
				t.Fatalf("decode wrong persisted root = (%+v, %v)", got, err)
			}
		})
	}
}

func TestConflictCodecRejectsRootNormalizationAndInnerUnknownFields(t *testing.T) {
	surfaces, err := snapshotRecordSurfaces(recordAllKindsSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	key := state.RecordKey{Kind: "task", ID: composeTaskID}
	root := surfaces[key].Root
	omitted := conflictCodecMutateRootObject(t, root, func(object map[string]any) { delete(object, "due_by") })
	normalized, err := rehydrateConflictRoot(key, omitted)
	if err != nil {
		t.Fatal(err)
	}
	conflict := Conflict{Key: key, Kind: ConflictSameField, Base: omitted, Ours: omitted, Theirs: omitted}
	normalizedConflict := conflict
	normalizedConflict.Base, normalizedConflict.Ours, normalizedConflict.Theirs = normalized, normalized, normalized
	conflict.ID, err = conflictID(normalizedConflict)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{conflictCodecRawOccurrence(t, conflict)}); err == nil || got != nil {
		t.Fatalf("decode root with omitted zero field = (%+v, %v), want strict rejection", got, err)
	}

	unknown := conflictCodecMutateRootObject(t, root, func(object map[string]any) { object["unknown"] = 1 })
	conflict = Conflict{Key: key, Kind: ConflictSameField, Base: unknown, Ours: unknown, Theirs: unknown}
	conflict.ID, err = conflictID(conflict)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeWorkspaceConflictOccurrences([]localstore.WorkspaceConflictOccurrence{conflictCodecRawOccurrence(t, conflict)}); err == nil || got != nil {
		t.Fatalf("decode root with inner unknown field = (%+v, %v), want strict rejection", got, err)
	}
}

func conflictCodecTask3Conflicts(t *testing.T) []Conflict {
	t.Helper()
	var conflicts []Conflict
	appendConflicts := func(key state.RecordKey, base, ours, theirs recordSurface, wantKind ConflictKind) {
		got, err := mergeRecordSurface(key, base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		for _, conflict := range got.Conflicts {
			if conflict.Kind == wantKind {
				conflicts = append(conflicts, conflict)
				return
			}
		}
		t.Fatalf("Task-3 merge for %s did not emit %s: %+v", key.Kind, wantKind, got.Conflicts)
	}
	projectKey := lifecycleKey("project")
	appendConflicts(projectKey, projectLifecycleSurface(t, "base"), projectLifecycleSurface(t, "name_aliases1"), projectLifecycleSurface(t, "name_aliases2"), ConflictSameField)
	actorKey := lifecycleKey("actor")
	appendConflicts(actorKey, lifecycleLive(t, actorKey, "base"), lifecycleTombstone(t, actorKey, "t1"), lifecycleLive(t, actorKey, "root1"), ConflictTombstoneEdit)
	taskKey := lifecycleKey("task")
	appendConflicts(taskKey, lifecycleTombstone(t, taskKey, "t1"), lifecycleLive(t, taskKey, "root1"), lifecycleLive(t, taskKey, "root2"), ConflictInvalidResurrection)
	kbKey := lifecycleKey("kb_article")
	appendConflicts(kbKey, lifecycleLive(t, kbKey, "base"), lifecycleLive(t, kbKey, "body1"), lifecycleLive(t, kbKey, "body2"), ConflictMarkdown)
	appendConflicts(kbKey, lifecycleLive(t, kbKey, "base"), lifecycleTombstone(t, kbKey, "t1"), lifecycleLive(t, kbKey, "body1"), ConflictTombstoneBody)
	eventKey := lifecycleKey("event")
	appendConflicts(eventKey, lifecycleLive(t, eventKey, "base"), lifecycleAbsent(), lifecycleLive(t, eventKey, "base"), ConflictImmutableRecord)
	sortConflicts(conflicts)
	return conflicts
}

func conflictCodecOccurrences(evidence []localstore.WorkspaceConflictEvidence) []localstore.WorkspaceConflictOccurrence {
	rows := make([]localstore.WorkspaceConflictOccurrence, len(evidence))
	for index, item := range evidence {
		rows[index] = localstore.WorkspaceConflictOccurrence{
			WorkspaceConflictEvidence: item,
			OccurrenceID:              "00000000-0000-4000-8000-" + strings.Repeat("0", 11) + string(rune('1'+index)),
			CreatedAt:                 time.Date(2026, 7, 29, 12, index, 0, 0, time.UTC),
		}
	}
	return rows
}

func conflictCodecValidOccurrence(t *testing.T) localstore.WorkspaceConflictOccurrence {
	t.Helper()
	conflict, err := newConflict(state.RecordKey{Kind: "task", ID: composeTaskID}, "/title", ConflictSameField,
		codecField(`"base"`), codecField(`"ours"`), codecField(`"theirs"`))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := encodeWorkspaceConflictEvidence([]Conflict{conflict})
	if err != nil {
		t.Fatal(err)
	}
	return conflictCodecOccurrences(evidence)[0]
}

func conflictCodecRawOccurrence(t *testing.T, conflict Conflict) localstore.WorkspaceConflictOccurrence {
	t.Helper()
	base, err := state.CanonicalJSON(conflict.Base)
	if err != nil {
		t.Fatal(err)
	}
	ours, err := state.CanonicalJSON(conflict.Ours)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := state.CanonicalJSON(conflict.Theirs)
	if err != nil {
		t.Fatal(err)
	}
	return localstore.WorkspaceConflictOccurrence{
		WorkspaceConflictEvidence: localstore.WorkspaceConflictEvidence{
			ConflictID: conflict.ID, Key: conflict.Key, FieldPath: conflict.FieldPath, ConflictKind: string(conflict.Kind),
			BaseJSON: string(base), OursJSON: string(ours), TheirsJSON: string(theirs),
		},
		OccurrenceID: "00000000-0000-4000-8000-000000000001",
		CreatedAt:    time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
	}
}

func conflictCodecEnvelopeValue(t *testing.T, value FieldValue) FieldValue {
	t.Helper()
	encoded, err := state.CanonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded FieldValue
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func conflictCodecMutateRoot(t *testing.T, root FieldValue, key string, value any) FieldValue {
	t.Helper()
	return conflictCodecMutateRootObject(t, root, func(object map[string]any) { object[key] = value })
}

func conflictCodecMutateRootObject(t *testing.T, root FieldValue, mutate func(map[string]any)) FieldValue {
	t.Helper()
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(root.Value))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	mutate(object)
	mutated, err := canonicalFieldJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	return FieldValue{Present: true, Value: mutated}
}

func codecField(value string) FieldValue {
	return FieldValue{Present: true, Value: json.RawMessage(value)}
}

func conflictCodecMutateDigest(value string) string {
	if value[len(value)-1] == '0' {
		return value[:len(value)-1] + "1"
	}
	return value[:len(value)-1] + "0"
}
