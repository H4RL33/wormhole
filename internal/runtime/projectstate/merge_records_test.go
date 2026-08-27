package projectstate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestSnapshotRecordSurfacesExtractsEveryKindAndLegalEndpointState(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := recordSurfaceKeys(surfaces)
	if err != nil {
		t.Fatal(err)
	}
	wantKeys := []state.RecordKey{
		{Kind: "project", ID: snapshot.Project.ID},
		{Kind: "actor", ID: composeActorID},
		{Kind: "task", ID: composeTaskID},
		{Kind: "task_link", ID: diffTaskLinkID},
		{Kind: "kb_article", ID: diffArticleID},
		{Kind: "channel", ID: diffChannelID},
		{Kind: "event", ID: diffEventID},
		{Kind: "git_link", ID: diffGitLinkID},
	}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("surface keys = %+v, want %+v", keys, wantKeys)
	}
	for _, key := range keys {
		surface := surfaces[key]
		if surface.State != recordEndpointLive || !surface.Root.Present || len(surface.Root.Value) == 0 {
			t.Fatalf("surface %v = %+v", key, surface)
		}
		class, err := recordSurfaceClassForKind(key.Kind)
		if err != nil {
			t.Fatal(err)
		}
		switch key.Kind {
		case "project":
			if class != recordSurfaceProject {
				t.Fatalf("project class = %v", class)
			}
		case "event", "git_link":
			if class != recordSurfaceImmutable {
				t.Fatalf("%s class = %v", key.Kind, class)
			}
		default:
			if class != recordSurfaceMutable {
				t.Fatalf("%s class = %v", key.Kind, class)
			}
		}
		if key.Kind == "kb_article" {
			if !surface.Body.Present || string(surface.Body.Value) != `"body\n"` {
				t.Fatalf("KB body = %+v", surface.Body)
			}
		} else if surface.Body.Present || surface.Body.Value != nil {
			t.Fatalf("unexpected %s body = %+v", key.Kind, surface.Body)
		}
	}

	taskTombstones, err := snapshotRecordSurfaces(diffTombstoneTask(t, composeFixtureSnapshot(t)))
	if err != nil {
		t.Fatal(err)
	}
	task := taskTombstones[state.RecordKey{Kind: "task", ID: composeTaskID}]
	if task.State != recordEndpointTombstone || !task.Root.Present || task.Body.Present {
		t.Fatalf("task tombstone surface = %+v", task)
	}
	kbTombstones, err := snapshotRecordSurfaces(recordTombstonedKB(t))
	if err != nil {
		t.Fatal(err)
	}
	article := kbTombstones[state.RecordKey{Kind: "kb_article", ID: diffArticleID}]
	if article.State != recordEndpointTombstone || !article.Root.Present || article.Body.Present || article.Body.Value != nil {
		t.Fatalf("KB tombstone surface = %+v", article)
	}
}

func TestSnapshotRecordSurfacesIsStrictAndDoesNotAlias(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	articleKey := state.RecordKey{Kind: "kb_article", ID: diffArticleID}
	article := surfaces[articleKey]
	article.Root.Value[0] ^= 1
	article.Body.Value[0] ^= 1
	if snapshot.Articles[diffArticleID].Value.Title != "Article" || string(snapshot.Articles[diffArticleID].Body) != "body\n" {
		t.Fatal("record surfaces alias input snapshot")
	}

	stale := diffCloneSnapshot(t, snapshot)
	stale.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if got, err := snapshotRecordSurfaces(stale); err == nil || got != nil {
		t.Fatalf("stale extraction = (%+v, %v)", got, err)
	}
}

func TestRecordSurfaceAtDistinguishesAbsentAndRejectsImpossibleProjectAbsence(t *testing.T) {
	surfaces, err := snapshotRecordSurfaces(composeFixtureSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	missing := state.RecordKey{Kind: "event", ID: diffEventID}
	got, err := recordSurfaceAt(surfaces, missing)
	if err != nil || !reflect.DeepEqual(got, recordSurface{State: recordEndpointAbsent}) {
		t.Fatalf("recordSurfaceAt absent = (%+v, %v)", got, err)
	}
	delete(surfaces, state.RecordKey{Kind: "project", ID: "00000000-0000-4000-8000-000000000001"})
	if got, err := recordSurfaceAt(surfaces, state.RecordKey{Kind: "project", ID: "00000000-0000-4000-8000-000000000001"}); err == nil || !reflect.DeepEqual(got, recordSurface{}) {
		t.Fatalf("recordSurfaceAt project absence = (%+v, %v)", got, err)
	}
	if got, err := recordSurfaceAt(surfaces, state.RecordKey{Kind: "event", ID: "not-a-uuid"}); err == nil || !reflect.DeepEqual(got, recordSurface{}) {
		t.Fatalf("recordSurfaceAt invalid absent key = (%+v, %v)", got, err)
	}
}

func TestRecordSurfaceKeysUnionIsCanonicalAndDeterministic(t *testing.T) {
	left := map[state.RecordKey]recordSurface{
		{Kind: "git_link", ID: diffGitLinkID}: {State: recordEndpointLive},
		{Kind: "task", ID: diffSecondTaskID}:  {State: recordEndpointLive},
		{Kind: "actor", ID: composeActorID}:   {State: recordEndpointLive},
	}
	right := map[state.RecordKey]recordSurface{
		{Kind: "project", ID: "00000000-0000-4000-8000-000000000001"}: {State: recordEndpointLive},
		{Kind: "task", ID: composeTaskID}:                             {State: recordEndpointLive},
		{Kind: "event", ID: diffEventID}:                              {State: recordEndpointLive},
		{Kind: "actor", ID: composeActorID}:                           {State: recordEndpointLive},
	}
	want := []state.RecordKey{
		{Kind: "project", ID: "00000000-0000-4000-8000-000000000001"},
		{Kind: "actor", ID: composeActorID},
		{Kind: "task", ID: composeTaskID},
		{Kind: "task", ID: diffSecondTaskID},
		{Kind: "event", ID: diffEventID},
		{Kind: "git_link", ID: diffGitLinkID},
	}
	for run := 0; run < 100; run++ {
		got, err := recordSurfaceKeys(left, right)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d surface keys = (%+v, %v), want %+v", run, got, err, want)
		}
	}
	invalid := map[state.RecordKey]recordSurface{{Kind: "unknown", ID: composeTaskID}: {State: recordEndpointLive}}
	if got, err := recordSurfaceKeys(invalid); err == nil || got != nil {
		t.Fatalf("invalid surface keys = (%+v, %v)", got, err)
	}
}

func TestRecordSurfacesEqualIncludesPresenceRootUpdatedAtAndBody(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	taskKey := state.RecordKey{Kind: "task", ID: composeTaskID}
	task := surfaces[taskKey]
	equal, err := recordSurfacesEqual(taskKey, task, recordSurface{State: task.State, Root: cloneMergeFieldValue(task.Root)})
	if err != nil || !equal {
		t.Fatalf("equal task surfaces = %v, %v", equal, err)
	}
	changed := diffCloneSnapshot(t, snapshot)
	changed.Tasks[composeTaskID].Value.UpdatedAt = changed.Tasks[composeTaskID].Value.UpdatedAt.Add(time.Hour)
	changed = diffCanonicalSnapshot(t, changed)
	changedSurfaces, err := snapshotRecordSurfaces(changed)
	if err != nil {
		t.Fatal(err)
	}
	if equal, err := recordSurfacesEqual(taskKey, task, changedSurfaces[taskKey]); err != nil || equal {
		t.Fatalf("updated_at equality = %v, %v", equal, err)
	}
	if equal, err := recordSurfacesEqual(taskKey, task, recordSurface{State: recordEndpointAbsent}); err != nil || equal {
		t.Fatalf("presence equality = %v, %v", equal, err)
	}
	articleKey := state.RecordKey{Kind: "kb_article", ID: diffArticleID}
	article := surfaces[articleKey]
	changedArticle := recordSurface{State: article.State, Root: cloneMergeFieldValue(article.Root), Body: liveBodyValue("changed\n")}
	if equal, err := recordSurfacesEqual(articleKey, article, changedArticle); err != nil || equal {
		t.Fatalf("body equality = %v, %v", equal, err)
	}
	invalid := recordSurface{State: recordEndpointLive, Root: FieldValue{Present: true, Value: json.RawMessage(` {}`)}}
	if equal, err := recordSurfacesEqual(taskKey, task, invalid); err == nil || equal {
		t.Fatalf("invalid equality = %v, %v", equal, err)
	}
}

func TestNewMergeSnapshotShellOwnsNewBaseGitFieldsAndInitializesMaps(t *testing.T) {
	newBase := recordAllKindsSnapshot(t)
	newBase.Config.Handle.Namespace = "renamed"
	newBase.Remotes = mergeTestRemotes()
	newBase = diffCanonicalSnapshot(t, newBase)
	shell, err := newMergeSnapshotShell(newBase)
	if err != nil {
		t.Fatal(err)
	}
	if shell.Config != newBase.Config || !reflect.DeepEqual(shell.Remotes, newBase.Remotes) || shell.Digest != "" || shell.Project.ID != "" ||
		shell.Actors == nil || shell.Tasks == nil || shell.TaskLinks == nil || shell.Articles == nil || shell.Channels == nil || shell.Events == nil || shell.GitLinks == nil ||
		len(shell.Actors)+len(shell.Tasks)+len(shell.TaskLinks)+len(shell.Articles)+len(shell.Channels)+len(shell.Events)+len(shell.GitLinks) != 0 {
		t.Fatalf("merge shell = %+v", shell)
	}
	shell.Config.Handle.Namespace = "mutated"
	shell.Remotes.Fabrics[0].Alias = "mutated"
	if newBase.Config.Handle.Namespace != "renamed" || newBase.Remotes.Fabrics[0].Alias == "mutated" {
		t.Fatal("merge shell aliases new base")
	}
}

func TestSetRecordSurfaceReconstructsAllEightTypedKindsWithoutAliases(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := recordSurfaceKeys(surfaces)
	if err != nil {
		t.Fatal(err)
	}
	shell, err := newMergeSnapshotShell(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if err := setRecordSurface(&shell, key, surfaces[key]); err != nil {
			t.Fatalf("set %v: %v", key, err)
		}
	}
	rebuilt, err := canonicalMergeSnapshot(shell)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diffTreeBytes(t, rebuilt), diffTreeBytes(t, snapshot)) || rebuilt.Digest != snapshot.Digest {
		t.Fatalf("rebuilt snapshot differs: %+v", rebuilt)
	}
	shell.Project.Name = "mutated"
	shell.Actors[composeActorID].Value.DisplayName = "mutated"
	shell.Articles[diffArticleID].Body[0] = 'X'
	shell.Events[diffEventID] = state.EventV1{}
	if snapshot.Project.Name == "mutated" || snapshot.Actors[composeActorID].Value.DisplayName == "mutated" || string(snapshot.Articles[diffArticleID].Body) != "body\n" || snapshot.Events[diffEventID].ID == "" {
		t.Fatal("typed setter aliases source snapshot")
	}
	articleSurface := surfaces[state.RecordKey{Kind: "kb_article", ID: diffArticleID}]
	if string(articleSurface.Body.Value) != `"body\n"` {
		t.Fatal("typed setter aliases source surface")
	}
}

func TestSetRecordSurfaceSupportsMutableTombstonesAndLegalRemoval(t *testing.T) {
	for _, test := range []struct {
		kind string
		id   string
	}{
		{kind: "actor", id: composeActorID},
		{kind: "task", id: composeTaskID},
		{kind: "task_link", id: diffTaskLinkID},
		{kind: "kb_article", id: diffArticleID},
		{kind: "channel", id: diffChannelID},
	} {
		t.Run(test.kind, func(t *testing.T) {
			snapshot := recordAllKindsSnapshot(t)
			key := state.RecordKey{Kind: test.kind, ID: test.id}
			tombstone := recordTombstoneSurface(t, key)
			if err := setRecordSurface(&snapshot, key, tombstone); err != nil {
				t.Fatal(err)
			}
			if got := recordSnapshotTombstone(snapshot, key); got == nil || got.ID != key.ID || got.EntityKind != key.Kind {
				t.Fatalf("set tombstone = %+v", got)
			}
			if test.kind == "kb_article" && snapshot.Articles[test.id].Body != nil {
				t.Fatalf("KB tombstone retained body %q", snapshot.Articles[test.id].Body)
			}
			if err := setRecordSurface(&snapshot, key, recordSurface{State: recordEndpointAbsent}); err != nil {
				t.Fatal(err)
			}
			if recordSnapshotHasKey(snapshot, key) {
				t.Fatalf("record %v was not removed", key)
			}
		})
	}
	for _, key := range []state.RecordKey{{Kind: "event", ID: diffEventID}, {Kind: "git_link", ID: diffGitLinkID}} {
		snapshot := recordAllKindsSnapshot(t)
		if err := setRecordSurface(&snapshot, key, recordSurface{State: recordEndpointAbsent}); err != nil || recordSnapshotHasKey(snapshot, key) {
			t.Fatalf("immutable removal adapter %v = %v, present=%v", key, err, recordSnapshotHasKey(snapshot, key))
		}
	}
}

func TestSetRecordSurfaceRejectsInvalidKindPresenceAndShapeWithoutMutation(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	projectKey := state.RecordKey{Kind: "project", ID: snapshot.Project.ID}
	taskKey := state.RecordKey{Kind: "task", ID: composeTaskID}
	eventKey := state.RecordKey{Kind: "event", ID: diffEventID}
	tests := []struct {
		name    string
		key     state.RecordKey
		surface recordSurface
	}{
		{name: "unknown kind", key: state.RecordKey{Kind: "unknown", ID: composeTaskID}, surface: recordSurface{State: recordEndpointAbsent}},
		{name: "invalid state", key: taskKey, surface: recordSurface{State: recordEndpointState(99)}},
		{name: "absent with root", key: taskKey, surface: recordSurface{State: recordEndpointAbsent, Root: surfaces[taskKey].Root}},
		{name: "live without root", key: taskKey, surface: recordSurface{State: recordEndpointLive}},
		{name: "project absent", key: projectKey, surface: recordSurface{State: recordEndpointAbsent}},
		{name: "project tombstone", key: projectKey, surface: recordTombstoneSurface(t, state.RecordKey{Kind: "task", ID: projectKey.ID})},
		{name: "immutable tombstone", key: eventKey, surface: recordTombstoneSurface(t, state.RecordKey{Kind: "task", ID: eventKey.ID})},
		{name: "task body", key: taskKey, surface: recordSurface{State: recordEndpointLive, Root: surfaces[taskKey].Root, Body: liveBodyValue("body\n")}},
		{name: "KB missing body", key: state.RecordKey{Kind: "kb_article", ID: diffArticleID}, surface: recordSurface{State: recordEndpointLive, Root: surfaces[state.RecordKey{Kind: "kb_article", ID: diffArticleID}].Root}},
		{name: "wrong typed kind", key: state.RecordKey{Kind: "actor", ID: composeTaskID}, surface: surfaces[taskKey]},
		{name: "unknown field", key: taskKey, surface: recordSurface{State: recordEndpointLive, Root: mergeJSONValue(`{"extra":1}`)}},
		{name: "noncanonical root", key: taskKey, surface: recordSurface{State: recordEndpointLive, Root: FieldValue{Present: true, Value: json.RawMessage(` {}`)}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			working := diffCloneSnapshot(t, snapshot)
			before := diffTreeBytes(t, working)
			if err := setRecordSurface(&working, test.key, test.surface); err == nil {
				t.Fatalf("setRecordSurface accepted %+v", test)
			}
			if !bytes.Equal(before, diffTreeBytes(t, working)) {
				t.Fatal("invalid setter mutated snapshot")
			}
		})
	}
	if err := setRecordSurface(nil, taskKey, surfaces[taskKey]); err == nil {
		t.Fatal("nil snapshot accepted")
	}
}

func recordAllKindsSnapshot(t *testing.T) state.Snapshot {
	t.Helper()
	snapshot := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "body\n")
	snapshot = diffSnapshotWithImmutableRecords(t, snapshot)
	link := state.TaskLinkV1{
		SchemaVersion: 1, Kind: "task_link", ID: diffTaskLinkID, TaskID: composeTaskID,
		LinkType: "kb_article", TargetID: diffArticleID, Extensions: state.ExtensionsV1{},
	}
	snapshot.TaskLinks[link.ID] = state.Record[state.TaskLinkV1]{Value: &link}
	return diffCanonicalSnapshot(t, snapshot)
}

func recordTombstonedKB(t *testing.T) state.Snapshot {
	t.Helper()
	snapshot := diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "body\n")
	contentDigest, err := state.DigestCanonicalJSON(*snapshot.Articles[diffArticleID].Value)
	if err != nil {
		t.Fatal(err)
	}
	bodyDigest, err := state.DigestCanonicalMarkdown(snapshot.Articles[diffArticleID].Body)
	if err != nil {
		t.Fatal(err)
	}
	operation := state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999992", Kind: state.OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: diffActorEnvelope(),
		Tombstone: &state.TombstoneOperationV1{
			Key:                   state.RecordKey{Kind: "kb_article", ID: diffArticleID},
			ExpectedContentDigest: contentDigest, ExpectedBodyDigest: &bodyDigest,
		},
	}
	result, err := state.ApplyOperation(snapshot, operation)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func recordTombstoneSurface(t *testing.T, key state.RecordKey) recordSurface {
	t.Helper()
	bodyDigest := state.Digest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	tombstone := state.TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: key.ID, EntityKind: key.Kind,
		DeletedContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		DeletedBy:            diffActorEnvelope(), DeletedAt: diffActorEnvelope().OccurredAt, Extensions: state.ExtensionsV1{},
	}
	if key.Kind == "kb_article" {
		tombstone.DeletedBodyDigest = &bodyDigest
	}
	raw, err := canonicalFieldJSON(tombstone)
	if err != nil {
		t.Fatal(err)
	}
	return recordSurface{State: recordEndpointTombstone, Root: FieldValue{Present: true, Value: raw}}
}

func recordSnapshotTombstone(snapshot state.Snapshot, key state.RecordKey) *state.TombstoneV1 {
	switch key.Kind {
	case "actor":
		return snapshot.Actors[key.ID].Tombstone
	case "task":
		return snapshot.Tasks[key.ID].Tombstone
	case "task_link":
		return snapshot.TaskLinks[key.ID].Tombstone
	case "kb_article":
		return snapshot.Articles[key.ID].Tombstone
	case "channel":
		return snapshot.Channels[key.ID].Tombstone
	default:
		return nil
	}
}

func recordSnapshotHasKey(snapshot state.Snapshot, key state.RecordKey) bool {
	switch key.Kind {
	case "actor":
		_, ok := snapshot.Actors[key.ID]
		return ok
	case "task":
		_, ok := snapshot.Tasks[key.ID]
		return ok
	case "task_link":
		_, ok := snapshot.TaskLinks[key.ID]
		return ok
	case "kb_article":
		_, ok := snapshot.Articles[key.ID]
		return ok
	case "channel":
		_, ok := snapshot.Channels[key.ID]
		return ok
	case "event":
		_, ok := snapshot.Events[key.ID]
		return ok
	case "git_link":
		_, ok := snapshot.GitLinks[key.ID]
		return ok
	default:
		return false
	}
}
