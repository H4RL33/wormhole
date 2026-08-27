package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const diffSecondTaskID = "33333333-3333-4333-8333-333333333333"

const (
	diffArticleID  = "44444444-4444-4444-8444-444444444444"
	diffChannelID  = "55555555-5555-4555-8555-555555555555"
	diffEventID    = "66666666-6666-4666-8666-666666666666"
	diffGitLinkID  = "77777777-7777-4777-8777-777777777777"
	diffTaskLinkID = "88888888-8888-4888-8888-888888888888"
)

func TestSemanticDiffEmpty(t *testing.T) {
	base := composeFixtureSnapshot(t)

	got, err := SemanticDiff(base, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseDigest != base.Digest || got.ViewDigest != base.Digest || got.Changes == nil || len(got.Changes) != 0 {
		t.Fatalf("SemanticDiff(base, base) = %+v", got)
	}
}

func TestSemanticDiffTaskFieldAndDigests(t *testing.T) {
	base := composeFixtureSnapshot(t)
	view := diffCloneSnapshot(t, base)
	view.Tasks[composeTaskID].Value.Title = "Changed title"
	view = diffCanonicalSnapshot(t, view)
	beforeDigest, err := state.DigestCanonicalJSON(*base.Tasks[composeTaskID].Value)
	if err != nil {
		t.Fatal(err)
	}
	afterDigest, err := state.DigestCanonicalJSON(*view.Tasks[composeTaskID].Value)
	if err != nil {
		t.Fatal(err)
	}

	got, err := SemanticDiff(base, view, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Diff{
		BaseDigest: base.Digest,
		ViewDigest: view.Digest,
		Changes: []Change{{
			Key:          state.RecordKey{Kind: "task", ID: composeTaskID},
			Kind:         ChangeModify,
			BeforeDigest: &beforeDigest,
			AfterDigest:  &afterDigest,
			Fields: []FieldChange{{
				Path:   "/title",
				Before: FieldValue{Present: true, Value: json.RawMessage(`"Compose task"`)},
				After:  FieldValue{Present: true, Value: json.RawMessage(`"Changed title"`)},
			}},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SemanticDiff task change = %#v, want %#v", got, want)
	}
}

func TestSemanticDiffKinds(t *testing.T) {
	base := composeFixtureSnapshot(t)
	const liveDigest state.Digest = "sha256:6d00a426011812cbb2c85c8b7355320bb234cfdb2d1a161ac8b26c541891a2eb"
	const addedDigest state.Digest = "sha256:e25f2d10c78cf9f4ca6bb8c1100d9a73dd603dc5a97b9266340fada301a92590"
	const tombstoneDigest state.Digest = "sha256:d78ccab91dd7c035f31bf8e3028bb3f201eabf9f9c2e09bc30784ef12c6e5f4b"
	liveRoot := json.RawMessage(`{"schema_version":1,"kind":"task","id":"22222222-2222-4222-8222-222222222222","parent_task_id":null,"title":"Compose task","description":"base","owner_actor_id":"11111111-1111-4111-8111-111111111111","status":"todo","priority":1,"due_by":null,"created_at":"2026-07-28T12:00:00Z","updated_at":"2026-07-28T12:00:00Z","extensions":{}}`)
	addedRoot := json.RawMessage(`{"schema_version":1,"kind":"task","id":"33333333-3333-4333-8333-333333333333","parent_task_id":null,"title":"Added task","description":"added","owner_actor_id":"11111111-1111-4111-8111-111111111111","status":"todo","priority":1,"due_by":null,"created_at":"2026-07-28T14:00:00Z","updated_at":"2026-07-28T14:00:00Z","extensions":{}}`)
	tombstoneRoot := json.RawMessage(`{"schema_version":1,"kind":"tombstone","id":"22222222-2222-4222-8222-222222222222","entity_kind":"task","deleted_content_digest":"sha256:6d00a426011812cbb2c85c8b7355320bb234cfdb2d1a161ac8b26c541891a2eb","deleted_body_digest":null,"deleted_by":{"actor_kind":"human","human_principal_id":"11111111-1111-4111-8111-111111111111","assurance":"local","occurred_at":"2026-07-28T13:00:00Z"},"deleted_at":"2026-07-28T13:00:00Z","extensions":{}}`)
	tests := []struct {
		name                        string
		base, view                  state.Snapshot
		key                         state.RecordKey
		kind                        ChangeKind
		beforeDigest, afterDigest   state.Digest
		beforePresent, afterPresent bool
		beforeRoot, afterRoot       json.RawMessage
	}{
		{
			name: "add", base: base, view: diffAddTask(t, base),
			key: state.RecordKey{Kind: "task", ID: diffSecondTaskID}, kind: ChangeAdd,
			afterDigest: addedDigest, afterPresent: true, afterRoot: addedRoot,
		},
		{
			name: "tombstone", base: base, view: diffTombstoneTask(t, base),
			key: state.RecordKey{Kind: "task", ID: composeTaskID}, kind: ChangeTombstone,
			beforeDigest: liveDigest, afterDigest: tombstoneDigest,
			beforePresent: true, afterPresent: true, beforeRoot: liveRoot, afterRoot: tombstoneRoot,
		},
	}
	tombstoned := diffTombstoneTask(t, base)
	tests = append(tests, struct {
		name                        string
		base, view                  state.Snapshot
		key                         state.RecordKey
		kind                        ChangeKind
		beforeDigest, afterDigest   state.Digest
		beforePresent, afterPresent bool
		beforeRoot, afterRoot       json.RawMessage
	}{
		name: "resurrect", base: tombstoned, view: diffResurrectTask(t, tombstoned, *base.Tasks[composeTaskID].Value),
		key: state.RecordKey{Kind: "task", ID: composeTaskID}, kind: ChangeResurrect,
		beforeDigest: tombstoneDigest, afterDigest: liveDigest,
		beforePresent: true, afterPresent: true, beforeRoot: tombstoneRoot, afterRoot: liveRoot,
	})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SemanticDiff(test.base, test.view, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Changes) != 1 {
				t.Fatalf("SemanticDiff changes = %+v", got.Changes)
			}
			want := Change{
				Key: test.key, Kind: test.kind,
				Fields: []FieldChange{{
					Path:   "",
					Before: FieldValue{Present: test.beforePresent, Value: bytes.Clone(test.beforeRoot)},
					After:  FieldValue{Present: test.afterPresent, Value: bytes.Clone(test.afterRoot)},
				}},
			}
			if test.beforeDigest != "" {
				want.BeforeDigest = diffDigestPointer(test.beforeDigest)
			}
			if test.afterDigest != "" {
				want.AfterDigest = diffDigestPointer(test.afterDigest)
			}
			if !reflect.DeepEqual(got.Changes[0], want) {
				t.Fatalf("lifecycle change = %#v, want %#v", got.Changes[0], want)
			}
		})
	}
}

func TestSemanticDiffEntityOrdering(t *testing.T) {
	base := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "body\n")
	base = diffSnapshotWithImmutableRecords(t, base)
	taskLink := state.TaskLinkV1{
		SchemaVersion: 1, Kind: "task_link", ID: diffTaskLinkID,
		TaskID: composeTaskID, LinkType: "kb_article", TargetID: diffArticleID,
		Extensions: state.ExtensionsV1{},
	}
	base.TaskLinks[diffTaskLinkID] = state.Record[state.TaskLinkV1]{Value: &taskLink}
	base = diffCanonicalSnapshot(t, base)
	view := diffCloneSnapshot(t, base)
	view.Project.Name = "Changed project"
	view.Actors[composeActorID].Value.DisplayName = "Changed actor"
	view.Tasks[composeTaskID].Value.Description = "Changed task"
	view.TaskLinks[diffTaskLinkID].Value.LinkType = "task"
	view.TaskLinks[diffTaskLinkID].Value.TargetID = composeTaskID
	view.Articles[diffArticleID].Value.Title = "Changed article"
	view.Channels[diffChannelID].Value.Name = "Changed channel"
	note := "Changed event"
	view.Events[diffEventID] = func(event state.EventV1) state.EventV1 { event.Note = &note; return event }(view.Events[diffEventID])
	view.GitLinks[diffGitLinkID].Value.Summary = "Changed Git link"
	view = diffCanonicalSnapshot(t, view)

	got, err := SemanticDiff(base, view, nil)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, change := range got.Changes {
		kinds = append(kinds, change.Key.Kind)
	}
	if want := []string{"project", "actor", "task", "task_link", "kb_article", "channel", "event", "git_link"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("change kinds = %v, want %v", kinds, want)
	}
}

func TestSemanticDiffRecursiveFieldsNullEscapingAndAtomicArrays(t *testing.T) {
	base := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{
		"array":  json.RawMessage(`[1,2]`),
		"nested": json.RawMessage(`{"a":1,"b":2}`),
		"remove": json.RawMessage(`null`),
	}, "line one\n")
	view := diffCloneSnapshot(t, base)
	view.Articles[diffArticleID].Value.Frontmatter = map[string]json.RawMessage{
		"a/b~c":  json.RawMessage(`null`),
		"array":  json.RawMessage(`[1,3]`),
		"nested": json.RawMessage(`{"b":2,"a":3}`),
	}
	view = diffCanonicalSnapshot(t, view)

	got, err := SemanticDiff(base, view, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 {
		t.Fatalf("changes = %+v", got.Changes)
	}
	want := []FieldChange{
		{Path: "/frontmatter/array", Before: FieldValue{Present: true, Value: json.RawMessage(`[1,2]`)}, After: FieldValue{Present: true, Value: json.RawMessage(`[1,3]`)}},
		{Path: "/frontmatter/a~1b~0c", Before: FieldValue{}, After: FieldValue{Present: true, Value: json.RawMessage(`null`)}},
		{Path: "/frontmatter/nested/a", Before: FieldValue{Present: true, Value: json.RawMessage(`1`)}, After: FieldValue{Present: true, Value: json.RawMessage(`3`)}},
		{Path: "/frontmatter/remove", Before: FieldValue{Present: true, Value: json.RawMessage(`null`)}, After: FieldValue{}},
	}
	if !reflect.DeepEqual(got.Changes[0].Fields, want) {
		t.Fatalf("fields = %#v, want %#v", got.Changes[0].Fields, want)
	}
	for _, field := range got.Changes[0].Fields {
		if field.Path == "/frontmatter/array/1" {
			t.Fatal("array was diffed element-by-element rather than atomically")
		}
	}
}

func TestFieldValueJSONDistinguishesAbsentAndNull(t *testing.T) {
	absent, err := json.Marshal(FieldValue{})
	if err != nil {
		t.Fatal(err)
	}
	presentNull, err := json.Marshal(FieldValue{Present: true, Value: json.RawMessage(`null`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(absent) != `{"present":false}` || string(presentNull) != `{"present":true,"value":null}` {
		t.Fatalf("absent=%s present-null=%s", absent, presentNull)
	}
}

func TestSemanticDiffKBBodyAndGoldenDigests(t *testing.T) {
	base := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "line one\n")
	view := diffCloneSnapshot(t, base)
	view.Articles[diffArticleID] = state.KBRecord{Value: view.Articles[diffArticleID].Value, Body: []byte("line two\n")}
	view = diffCanonicalSnapshot(t, view)

	got, err := SemanticDiff(base, view, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 {
		t.Fatalf("changes = %+v", got.Changes)
	}
	change := got.Changes[0]
	const recordDigest state.Digest = "sha256:23d08d743ba93795b70d8282cdbc246bc398c4ec896b7681b128ca695b853974"
	const beforeBody state.Digest = "sha256:31f21b1dae81d3f32f40e38134bc688e6f7df4f08dde1d7d2cda3c4b59104e1c"
	const afterBody state.Digest = "sha256:6c49a5c084a239ab9911b14f378d793a8eb3942ee7582354f4ef4d527dc0d528"
	if change.BeforeDigest == nil || *change.BeforeDigest != recordDigest || change.AfterDigest == nil || *change.AfterDigest != recordDigest {
		t.Fatalf("record digests = %v -> %v", change.BeforeDigest, change.AfterDigest)
	}
	if change.BeforeBodyDigest == nil || *change.BeforeBodyDigest != beforeBody || change.AfterBodyDigest == nil || *change.AfterBodyDigest != afterBody {
		t.Fatalf("body digests = %v -> %v", change.BeforeBodyDigest, change.AfterBodyDigest)
	}
	wantFields := []FieldChange{{
		Path:   "/body",
		Before: FieldValue{Present: true, Value: json.RawMessage(`"line one\n"`)},
		After:  FieldValue{Present: true, Value: json.RawMessage(`"line two\n"`)},
	}}
	if !reflect.DeepEqual(change.Fields, wantFields) {
		t.Fatalf("body fields = %#v, want %#v", change.Fields, wantFields)
	}
}

func TestSemanticDiffKBTombstoneGoldenEvidence(t *testing.T) {
	base := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "line one\n")
	const recordDigest state.Digest = "sha256:23d08d743ba93795b70d8282cdbc246bc398c4ec896b7681b128ca695b853974"
	const bodyDigest state.Digest = "sha256:31f21b1dae81d3f32f40e38134bc688e6f7df4f08dde1d7d2cda3c4b59104e1c"
	const tombstoneDigest state.Digest = "sha256:76b1b79ee96c469bda809cf37ce1d951c3e583f17bff59efc71f40d0e4263c23"
	operation := state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999995", Kind: state.OperationTombstone,
		ExpectedViewDigest: base.Digest, Actor: diffActorEnvelope(),
		Tombstone: &state.TombstoneOperationV1{
			Key:                   state.RecordKey{Kind: "kb_article", ID: diffArticleID},
			ExpectedContentDigest: recordDigest, ExpectedBodyDigest: diffDigestPointer(bodyDigest),
		},
	}
	view, err := state.ApplyOperation(base, operation)
	if err != nil {
		t.Fatal(err)
	}

	got, err := SemanticDiff(base, view, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeRoot := json.RawMessage(`{"schema_version":1,"kind":"kb_article","id":"44444444-4444-4444-8444-444444444444","title":"Article","frontmatter":{},"author_actor_id":"11111111-1111-4111-8111-111111111111","related_article_ids":[],"created_at":"2026-07-28T12:00:00Z","updated_at":"2026-07-28T12:00:00Z","extensions":{}}`)
	afterRoot := json.RawMessage(`{"schema_version":1,"kind":"tombstone","id":"44444444-4444-4444-8444-444444444444","entity_kind":"kb_article","deleted_content_digest":"sha256:23d08d743ba93795b70d8282cdbc246bc398c4ec896b7681b128ca695b853974","deleted_body_digest":"sha256:31f21b1dae81d3f32f40e38134bc688e6f7df4f08dde1d7d2cda3c4b59104e1c","deleted_by":{"actor_kind":"human","human_principal_id":"11111111-1111-4111-8111-111111111111","assurance":"local","occurred_at":"2026-07-28T13:00:00Z"},"deleted_at":"2026-07-28T13:00:00Z","extensions":{}}`)
	want := Diff{
		BaseDigest: base.Digest, ViewDigest: view.Digest,
		Changes: []Change{{
			Key: state.RecordKey{Kind: "kb_article", ID: diffArticleID}, Kind: ChangeTombstone,
			BeforeDigest: diffDigestPointer(recordDigest), AfterDigest: diffDigestPointer(tombstoneDigest),
			BeforeBodyDigest: diffDigestPointer(bodyDigest),
			Fields: []FieldChange{
				{Path: "", Before: FieldValue{Present: true, Value: beforeRoot}, After: FieldValue{Present: true, Value: afterRoot}},
				{Path: "/body", Before: FieldValue{Present: true, Value: json.RawMessage(`"line one\n"`)}, After: FieldValue{}},
			},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KB tombstone Diff = %#v, want %#v", got, want)
	}
}

func TestSemanticDiffRejectsRawMutableDeletion(t *testing.T) {
	base := composeFixtureSnapshot(t)
	view := diffCloneSnapshot(t, base)
	delete(view.Tasks, composeTaskID)
	view = diffCanonicalSnapshot(t, view)

	got, err := SemanticDiff(base, view, nil)
	if !errors.Is(err, ErrRawRecordDeletion) {
		t.Fatalf("SemanticDiff error = %v, want ErrRawRecordDeletion", err)
	}
	if !reflect.DeepEqual(got, Diff{}) {
		t.Fatalf("SemanticDiff returned partial result: %+v", got)
	}
}

func TestSemanticDiffRawMutableDeletionPrecedesViewValidation(t *testing.T) {
	base := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "body\n")
	base = diffSnapshotWithImmutableRecords(t, base)
	taskLink := state.TaskLinkV1{
		SchemaVersion: 1, Kind: "task_link", ID: diffTaskLinkID,
		TaskID: composeTaskID, LinkType: "kb_article", TargetID: diffArticleID,
		Extensions: state.ExtensionsV1{},
	}
	base.TaskLinks[diffTaskLinkID] = state.Record[state.TaskLinkV1]{Value: &taskLink}
	base = diffCanonicalSnapshot(t, base)

	tests := []struct {
		name   string
		remove func(state.Snapshot)
	}{
		{name: "referenced actor", remove: func(view state.Snapshot) { delete(view.Actors, composeActorID) }},
		{name: "referenced task", remove: func(view state.Snapshot) { delete(view.Tasks, composeTaskID) }},
		{name: "task link", remove: func(view state.Snapshot) { delete(view.TaskLinks, diffTaskLinkID) }},
		{name: "referenced KB article", remove: func(view state.Snapshot) { delete(view.Articles, diffArticleID) }},
		{name: "referenced channel", remove: func(view state.Snapshot) { delete(view.Channels, diffChannelID) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := diffCloneSnapshot(t, base)
			test.remove(view)
			got, err := SemanticDiff(base, view, nil)
			if !errors.Is(err, ErrRawRecordDeletion) {
				t.Fatalf("SemanticDiff error = %v, want ErrRawRecordDeletion", err)
			}
			if !reflect.DeepEqual(got, Diff{}) {
				t.Fatalf("SemanticDiff returned partial result: %+v", got)
			}
		})
	}
}

func TestSemanticDiffRepresentsImmutableDisappearance(t *testing.T) {
	base := diffSnapshotWithImmutableRecords(t, composeFixtureSnapshot(t))
	view := diffCloneSnapshot(t, base)
	delete(view.Events, diffEventID)
	delete(view.GitLinks, diffGitLinkID)
	view = diffCanonicalSnapshot(t, view)

	got, err := SemanticDiff(base, view, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 2 || got.Changes[0].Key.Kind != "event" || got.Changes[1].Key.Kind != "git_link" {
		t.Fatalf("immutable disappearance changes = %+v", got.Changes)
	}
	for _, change := range got.Changes {
		if change.Kind != ChangeModify || len(change.Fields) != 1 || change.Fields[0].Path != "" || !change.Fields[0].Before.Present || change.Fields[0].After.Present || change.Fields[0].After.Value != nil || change.AfterDigest != nil {
			t.Fatalf("immutable disappearance = %+v", change)
		}
	}
}

func TestSemanticDiffValidatesDigestsBindingsAndGitOwnedFields(t *testing.T) {
	base := composeFixtureSnapshot(t)

	t.Run("stale digest", func(t *testing.T) {
		stale := diffCloneSnapshot(t, base)
		stale.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if _, err := SemanticDiff(stale, base, nil); err == nil {
			t.Fatal("SemanticDiff accepted a stale base digest")
		}
	})

	t.Run("invalid snapshot", func(t *testing.T) {
		invalid := diffCloneSnapshot(t, base)
		invalid.Project.Name = ""
		if _, err := SemanticDiff(base, invalid, nil); err == nil {
			t.Fatal("SemanticDiff accepted an invalid view")
		}
	})

	t.Run("binding mismatch", func(t *testing.T) {
		mismatch := diffCloneSnapshot(t, base)
		mismatch.Config.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"}
		mismatch = diffCanonicalSnapshot(t, mismatch)
		if _, err := SemanticDiff(base, mismatch, nil); err == nil {
			t.Fatal("SemanticDiff accepted a repository binding mismatch")
		}
	})

	t.Run("handle and remotes omitted", func(t *testing.T) {
		view := diffCloneSnapshot(t, base)
		view.Config.Handle = types.ProjectHandle{Namespace: "other", Name: "handle"}
		view.Remotes = &state.RemotesV1{Version: 1, Fabrics: []state.FabricHintV1{{
			Alias: "public", URL: "https://fabric.example.test", InstanceID: "fabric-one",
			RemoteProjectID: "remote-one", Mode: "public",
		}}}
		view = diffCanonicalSnapshot(t, view)
		got, err := SemanticDiff(base, view, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Changes) != 0 {
			t.Fatalf("Git-owned fields appeared in semantic diff: %+v", got.Changes)
		}
	})
}

func TestSemanticDiffExcludesUpdatedAtMetadata(t *testing.T) {
	base := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "body\n")
	view := diffCloneSnapshot(t, base)
	view.Project.UpdatedAt = view.Project.UpdatedAt.Add(time.Hour)
	view.Articles[diffArticleID].Value.UpdatedAt = view.Articles[diffArticleID].Value.UpdatedAt.Add(time.Hour)
	view = diffCanonicalSnapshot(t, view)

	got, err := SemanticDiff(base, view, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 0 {
		t.Fatalf("updated_at-only changes = %+v", got.Changes)
	}
}

func TestSemanticDiffActorAttributionAndAliasSafety(t *testing.T) {
	base := composeFixtureSnapshot(t)
	view := diffCloneSnapshot(t, base)
	view.Tasks[composeTaskID].Value.Title = "Attributed change"
	view = diffCanonicalSnapshot(t, view)
	key := state.RecordKey{Kind: "task", ID: composeTaskID}
	actor := diffActorEnvelope()
	actors := map[state.RecordKey]types.ActorEnvelope{
		key: actor,
		{Kind: "task", ID: diffSecondTaskID}: {
			ActorKind: types.ActorHuman, HumanPrincipalID: diffSecondTaskID,
			Assurance: types.AssuranceLocal, OccurredAt: actor.OccurredAt,
		},
	}
	baseTree := diffTreeBytes(t, base)
	viewTree := diffTreeBytes(t, view)

	got, err := SemanticDiff(base, view, actors)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 || got.Changes[0].Actor == nil || *got.Changes[0].Actor != actor {
		t.Fatalf("actor attribution = %+v", got.Changes)
	}
	actors[key] = types.ActorEnvelope{}
	got.Changes[0].Actor.HumanPrincipalID = diffSecondTaskID
	got.Changes[0].Fields[0].After.Value[0] = 'X'

	again, err := SemanticDiff(base, view, map[state.RecordKey]types.ActorEnvelope{key: actor})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changes[0].Actor == nil || *again.Changes[0].Actor != actor || string(again.Changes[0].Fields[0].After.Value) != `"Attributed change"` {
		t.Fatalf("diff output aliases caller or prior result: %+v", again.Changes[0])
	}
	if !bytes.Equal(baseTree, diffTreeBytes(t, base)) || !bytes.Equal(viewTree, diffTreeBytes(t, view)) {
		t.Fatal("SemanticDiff mutated an input snapshot")
	}
	withoutActor, err := SemanticDiff(base, view, nil)
	if err != nil || withoutActor.Changes[0].Actor != nil {
		t.Fatalf("missing attribution = %+v, %v", withoutActor.Changes, err)
	}
}

func TestSemanticDiffUUIDAndMapInsertionDeterminism(t *testing.T) {
	base := composeFixtureSnapshot(t)
	ids := []string{"99999999-9999-4999-8999-999999999999", diffSecondTaskID}
	forward := diffSnapshotWithTaskInsertionOrder(t, base, ids)
	reverse := diffSnapshotWithTaskInsertionOrder(t, base, []string{ids[1], ids[0]})

	first, err := SemanticDiff(base, forward, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changes) != 2 || first.Changes[0].Key.ID != diffSecondTaskID || first.Changes[1].Key.ID != ids[0] {
		t.Fatalf("UUID order = %+v", first.Changes)
	}
	got, err := SemanticDiff(base, reverse, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("reversed insertion differs:\n%#v\n%#v", got, first)
	}
}

func diffAddTask(t *testing.T, base state.Snapshot) state.Snapshot {
	t.Helper()
	view := diffCloneSnapshot(t, base)
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	owner := composeActorID
	task := state.TaskV1{
		SchemaVersion: 1, Kind: "task", ID: diffSecondTaskID, Title: "Added task",
		Description: "added", OwnerActorID: &owner, Status: "todo", Priority: 1,
		CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{},
	}
	view.Tasks[task.ID] = state.Record[state.TaskV1]{Value: &task}
	return diffCanonicalSnapshot(t, view)
}

func diffSnapshotWithTaskInsertionOrder(t *testing.T, base state.Snapshot, ids []string) state.Snapshot {
	t.Helper()
	view := diffCloneSnapshot(t, base)
	tasks := make(map[string]state.Record[state.TaskV1], 1+len(ids))
	tasks[composeTaskID] = view.Tasks[composeTaskID]
	now := time.Date(2026, 7, 28, 14, 0, 0, 0, time.UTC)
	for _, id := range ids {
		owner := composeActorID
		task := state.TaskV1{
			SchemaVersion: 1, Kind: "task", ID: id, Title: id, OwnerActorID: &owner,
			Status: "todo", CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{},
		}
		tasks[id] = state.Record[state.TaskV1]{Value: &task}
	}
	view.Tasks = tasks
	tree, err := state.EncodeTree(view)
	if err != nil {
		t.Fatal(err)
	}
	view.Digest, err = state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func diffSnapshotWithArticle(t *testing.T, base state.Snapshot, frontmatter map[string]json.RawMessage, body string) state.Snapshot {
	t.Helper()
	view := diffCloneSnapshot(t, base)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	article := state.KBArticleV1{
		SchemaVersion: 1, Kind: "kb_article", ID: diffArticleID, Title: "Article",
		Frontmatter: frontmatter, AuthorActorID: composeActorID, RelatedArticleIDs: []string{},
		CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{},
	}
	view.Articles[diffArticleID] = state.KBRecord{Value: &article, Body: []byte(body)}
	return diffCanonicalSnapshot(t, view)
}

func diffSnapshotWithImmutableRecords(t *testing.T, base state.Snapshot) state.Snapshot {
	t.Helper()
	view := diffCloneSnapshot(t, base)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	channel := state.ChannelV1{
		SchemaVersion: 1, Kind: "channel", ID: diffChannelID, Name: "general",
		CreatedAt: now, Extensions: state.ExtensionsV1{},
	}
	event := state.EventV1{
		SchemaVersion: 1, Kind: "event", ID: diffEventID, ChannelID: diffChannelID,
		ActorID: composeActorID, EventType: "message.posted", Payload: json.RawMessage(`{}`),
		CreatedAt: now, Extensions: state.ExtensionsV1{},
	}
	gitLink := state.GitLinkV1{
		SchemaVersion: 1, Kind: "git_link", ID: diffGitLinkID, Repository: "acme/wormhole",
		Summary: "immutable link", ActorID: composeActorID, CreatedAt: now, Extensions: state.ExtensionsV1{},
	}
	view.Channels[diffChannelID] = state.Record[state.ChannelV1]{Value: &channel}
	view.Events[diffEventID] = event
	view.GitLinks[diffGitLinkID] = state.Record[state.GitLinkV1]{Value: &gitLink}
	return diffCanonicalSnapshot(t, view)
}

func diffTreeBytes(t *testing.T, snapshot state.Snapshot) []byte {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func diffDigestPointer(value state.Digest) *state.Digest {
	copy := value
	return &copy
}

func diffTombstoneTask(t *testing.T, base state.Snapshot) state.Snapshot {
	t.Helper()
	digest, err := state.DigestCanonicalJSON(*base.Tasks[composeTaskID].Value)
	if err != nil {
		t.Fatal(err)
	}
	operation := state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999993", Kind: state.OperationTombstone,
		ExpectedViewDigest: base.Digest, Actor: diffActorEnvelope(),
		Tombstone: &state.TombstoneOperationV1{
			Key: state.RecordKey{Kind: "task", ID: composeTaskID}, ExpectedContentDigest: digest,
		},
	}
	view, err := state.ApplyOperation(base, operation)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func diffResurrectTask(t *testing.T, base state.Snapshot, record state.TaskV1) state.Snapshot {
	t.Helper()
	tombstoneDigest, err := state.DigestCanonicalJSON(*base.Tasks[composeTaskID].Tombstone)
	if err != nil {
		t.Fatal(err)
	}
	operation := state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999994", Kind: state.OperationResurrect,
		ExpectedViewDigest: base.Digest, Actor: diffActorEnvelope(),
		Resurrect: &state.ResurrectOperationV1{
			Key: state.RecordKey{Kind: "task", ID: composeTaskID}, ExpectedTombstoneDigest: tombstoneDigest,
			Record: state.RecordValueV1{Task: &record},
		},
	}
	view, err := state.ApplyOperation(base, operation)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func diffActorEnvelope() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: composeActorID,
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 7, 28, 13, 0, 0, 0, time.UTC),
	}
}

func diffCloneSnapshot(t *testing.T, snapshot state.Snapshot) state.Snapshot {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return clone
}

func diffCanonicalSnapshot(t *testing.T, snapshot state.Snapshot) state.Snapshot {
	t.Helper()
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := state.DecodeTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
