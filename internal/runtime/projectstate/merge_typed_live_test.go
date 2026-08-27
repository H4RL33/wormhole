package projectstate

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestSnapshotRecordSurfacesRetainsTypedCanonicalRootOrderForEveryKind(t *testing.T) {
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
		canonical, err := state.CanonicalJSON(recordConcreteLiveValue(t, snapshot, key))
		if err != nil {
			t.Fatal(err)
		}
		want := canonical[:len(canonical)-1]
		if !bytes.Equal(surfaces[key].Root.Value, want) {
			t.Fatalf("%s root = %s, want typed canonical %s", key.Kind, surfaces[key].Root.Value, want)
		}
	}
}

func TestDecodeMergeJSONSlotRejectsTypedCanonicalTaskRoot(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	key := state.RecordKey{Kind: "task", ID: composeTaskID}
	surface := recordSurfaceFromSnapshot(t, snapshot, key)
	if got, err := decodeMergeJSONSlot(surface.Root); err == nil || !reflect.DeepEqual(got, mergeJSONSlot{}) {
		t.Fatalf("decodeMergeJSONSlot typed root = (%+v, %v), want zero result and error", got, err)
	}
}

func TestTypedLiveRootGenericRoundTripIsByteExactForSupportedKinds(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	for _, key := range []state.RecordKey{
		{Kind: "project", ID: snapshot.Project.ID},
		{Kind: "actor", ID: composeActorID},
		{Kind: "task", ID: composeTaskID},
		{Kind: "task_link", ID: diffTaskLinkID},
		{Kind: "kb_article", ID: diffArticleID},
		{Kind: "channel", ID: diffChannelID},
	} {
		t.Run(key.Kind, func(t *testing.T) {
			surface := recordSurfaceFromSnapshot(t, snapshot, key)
			generic, err := normalizeTypedLiveRoot(key, surface.Root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeMergeJSONSlot(generic); err != nil {
				t.Fatalf("normalized root is not generic canonical JSON: %v", err)
			}
			if bytes.Equal(generic.Value, surface.Root.Value) {
				t.Fatalf("%s typed and generic root unexpectedly have identical ordering", key.Kind)
			}
			typed, err := rehydrateTypedLiveRoot(key, generic)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(typed, surface.Root) {
				t.Fatalf("round trip = %s, want %s", typed.Value, surface.Root.Value)
			}
			generic.Value[0] ^= 1
			typed.Value[0] ^= 1
			if !bytes.Equal(surface.Root.Value, recordSurfaceFromSnapshot(t, snapshot, key).Root.Value) {
				t.Fatal("normalized or rehydrated root aliases typed source")
			}
		})
	}
	for _, key := range []state.RecordKey{{Kind: "event", ID: diffEventID}, {Kind: "git_link", ID: diffGitLinkID}} {
		surface := recordSurfaceFromSnapshot(t, snapshot, key)
		if got, err := normalizeTypedLiveRoot(key, surface.Root); err == nil || !reflect.DeepEqual(got, FieldValue{}) {
			t.Fatalf("normalize unsupported %s = (%+v, %v)", key.Kind, got, err)
		}
	}
}

func TestMergeExistingTypedLiveTaskMergesRealDisjointRootsIntoCanonicalSnapshot(t *testing.T) {
	base := recordAllKindsSnapshot(t)
	ours := diffCloneSnapshot(t, base)
	theirs := diffCloneSnapshot(t, base)
	oursTask := ours.Tasks[composeTaskID].Value
	theirsTask := theirs.Tasks[composeTaskID].Value
	oursTask.Title = "ours title"
	oursTask.UpdatedAt = oursTask.UpdatedAt.Add(time.Minute)
	theirsTask.Description = "theirs description"
	theirsTask.UpdatedAt = theirsTask.UpdatedAt.Add(2 * time.Minute)
	ours = diffCanonicalSnapshot(t, ours)
	theirs = diffCanonicalSnapshot(t, theirs)
	key := state.RecordKey{Kind: "task", ID: composeTaskID}

	got, err := mergeExistingTypedLiveRecord(key,
		recordSurfaceFromSnapshot(t, base, key),
		recordSurfaceFromSnapshot(t, ours, key),
		recordSurfaceFromSnapshot(t, theirs, key))
	if err != nil {
		t.Fatal(err)
	}
	if got.Surface.State != recordEndpointLive || got.Conflicts == nil || len(got.Conflicts) != 0 || got.Surface.Body.Present {
		t.Fatalf("typed task merge metadata = %+v", got)
	}
	want := *base.Tasks[composeTaskID].Value
	want.Title = "ours title"
	want.Description = "theirs description"
	want.UpdatedAt = theirs.Tasks[composeTaskID].Value.UpdatedAt
	wantRoot, err := canonicalFieldJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Surface.Root.Value, wantRoot) {
		t.Fatalf("typed task root = %s, want %s", got.Surface.Root.Value, wantRoot)
	}
	merged := diffCloneSnapshot(t, base)
	if err := setRecordSurface(&merged, key, got.Surface); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalMergeSnapshot(merged)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*canonical.Tasks[composeTaskID].Value, want) {
		t.Fatalf("canonical task = %+v, want %+v", *canonical.Tasks[composeTaskID].Value, want)
	}
}

func TestMergeExistingTypedLiveConflictReturnsTypedProvisionalRootAndLeafEvidence(t *testing.T) {
	base := recordAllKindsSnapshot(t)
	ours := diffCloneSnapshot(t, base)
	theirs := diffCloneSnapshot(t, base)
	ours.Tasks[composeTaskID].Value.Title = "ours title"
	ours.Tasks[composeTaskID].Value.UpdatedAt = ours.Tasks[composeTaskID].Value.UpdatedAt.Add(time.Minute)
	theirs.Tasks[composeTaskID].Value.Title = "theirs title"
	theirs.Tasks[composeTaskID].Value.UpdatedAt = theirs.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	ours = diffCanonicalSnapshot(t, ours)
	theirs = diffCanonicalSnapshot(t, theirs)
	key := state.RecordKey{Kind: "task", ID: composeTaskID}
	baseSurface := recordSurfaceFromSnapshot(t, base, key)
	oursSurface := recordSurfaceFromSnapshot(t, ours, key)
	theirsSurface := recordSurfaceFromSnapshot(t, theirs, key)
	baseBefore := cloneMergeFieldValue(baseSurface.Root)
	oursBefore := cloneMergeFieldValue(oursSurface.Root)
	theirsBefore := cloneMergeFieldValue(theirsSurface.Root)

	got, err := mergeExistingTypedLiveRecord(key, baseSurface, oursSurface, theirsSurface)
	if err != nil {
		t.Fatal(err)
	}
	if got.Surface.State != recordEndpointLive || len(got.Conflicts) != 1 || got.Conflicts[0].FieldPath != "/title" || got.Conflicts[0].Kind != ConflictSameField {
		t.Fatalf("typed conflicts = %+v", got.Conflicts)
	}
	want := *ours.Tasks[composeTaskID].Value
	want.UpdatedAt = base.Tasks[composeTaskID].Value.UpdatedAt
	wantRoot, err := canonicalFieldJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Surface.Root.Value, wantRoot) {
		t.Fatalf("typed provisional root = %s, want %s", got.Surface.Root.Value, wantRoot)
	}
	conflict := got.Conflicts[0]
	if string(conflict.Base.Value) != `"Compose task"` || string(conflict.Ours.Value) != `"ours title"` || string(conflict.Theirs.Value) != `"theirs title"` {
		t.Fatalf("typed conflict evidence = %+v", conflict)
	}
	got.Surface.Root.Value[0] ^= 1
	got.Conflicts[0].Base.Value[0] ^= 1
	if !reflect.DeepEqual(baseSurface.Root, baseBefore) || !reflect.DeepEqual(oursSurface.Root, oursBefore) || !reflect.DeepEqual(theirsSurface.Root, theirsBefore) {
		t.Fatal("typed merge result aliases an input")
	}
}

func TestMergeExistingTypedLiveCreatedAtPreconditionReturnsZero(t *testing.T) {
	base := recordAllKindsSnapshot(t)
	ours := diffCloneSnapshot(t, base)
	ours.Tasks[composeTaskID].Value.CreatedAt = ours.Tasks[composeTaskID].Value.CreatedAt.Add(-time.Hour)
	ours = diffCanonicalSnapshot(t, ours)
	key := state.RecordKey{Kind: "task", ID: composeTaskID}
	baseSurface := recordSurfaceFromSnapshot(t, base, key)
	got, err := mergeExistingTypedLiveRecord(key, baseSurface, recordSurfaceFromSnapshot(t, ours, key), baseSurface)
	if !errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
		t.Fatalf("created_at typed merge = (%+v, %v), want zero ErrOperationPrecondition", got, err)
	}
}

func TestMergeExistingTypedLiveKBMergesRootAndBodyIntoCanonicalSnapshot(t *testing.T) {
	base := recordAllKindsSnapshot(t)
	ours := diffCloneSnapshot(t, base)
	theirs := diffCloneSnapshot(t, base)
	ours.Articles[diffArticleID].Value.Title = "ours article"
	ours.Articles[diffArticleID].Value.UpdatedAt = ours.Articles[diffArticleID].Value.UpdatedAt.Add(time.Minute)
	theirsArticle := theirs.Articles[diffArticleID]
	theirsArticle.Body = []byte("theirs body\n")
	theirs.Articles[diffArticleID] = theirsArticle
	theirs.Articles[diffArticleID].Value.UpdatedAt = theirs.Articles[diffArticleID].Value.UpdatedAt.Add(2 * time.Minute)
	ours = diffCanonicalSnapshot(t, ours)
	theirs = diffCanonicalSnapshot(t, theirs)
	key := state.RecordKey{Kind: "kb_article", ID: diffArticleID}
	baseSurface := recordSurfaceFromSnapshot(t, base, key)
	oursSurface := recordSurfaceFromSnapshot(t, ours, key)
	theirsSurface := recordSurfaceFromSnapshot(t, theirs, key)

	got, err := mergeExistingTypedLiveRecord(key, baseSurface, oursSurface, theirsSurface)
	if err != nil {
		t.Fatal(err)
	}
	if got.Surface.State != recordEndpointLive || len(got.Conflicts) != 0 || string(got.Surface.Body.Value) != `"theirs body\n"` {
		t.Fatalf("typed KB merge = %+v", got)
	}
	want := *base.Articles[diffArticleID].Value
	want.Title = "ours article"
	want.UpdatedAt = theirs.Articles[diffArticleID].Value.UpdatedAt
	wantRoot, err := canonicalFieldJSON(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Surface.Root.Value, wantRoot) {
		t.Fatalf("typed KB root = %s, want %s", got.Surface.Root.Value, wantRoot)
	}
	merged := diffCloneSnapshot(t, base)
	if err := setRecordSurface(&merged, key, got.Surface); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalMergeSnapshot(merged)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*canonical.Articles[diffArticleID].Value, want) || string(canonical.Articles[diffArticleID].Body) != "theirs body\n" {
		t.Fatalf("canonical KB = %+v", canonical.Articles[diffArticleID])
	}
	got.Surface.Body.Value[0] ^= 1
	if !reflect.DeepEqual(baseSurface, recordSurfaceFromSnapshot(t, base, key)) ||
		!reflect.DeepEqual(oursSurface, recordSurfaceFromSnapshot(t, ours, key)) ||
		!reflect.DeepEqual(theirsSurface, recordSurfaceFromSnapshot(t, theirs, key)) {
		t.Fatal("typed KB merge aliases an input")
	}
}

func TestTypedLiveBridgeRejectsMalformedGenericAndInvalidEndpointShapesWithZeroResult(t *testing.T) {
	snapshot := recordAllKindsSnapshot(t)
	taskKey := state.RecordKey{Kind: "task", ID: composeTaskID}
	task := recordSurfaceFromSnapshot(t, snapshot, taskKey)
	generic, err := normalizeTypedLiveRoot(taskKey, task.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := setRecordSurface(&snapshot, taskKey, recordSurface{State: recordEndpointLive, Root: generic}); err == nil {
		t.Fatal("typed setter accepted a complete generic-map-ordered task root")
	}
	slot, err := decodeMergeJSONSlot(generic)
	if err != nil {
		t.Fatal(err)
	}
	object, ok := mergeJSONObject(slot)
	if !ok {
		t.Fatal("normalized task root is not an object")
	}
	malformedObject := cloneMergeJSONObject(object)
	malformedObject["unknown"] = "field"
	malformed, err := mergeJSONSlotFieldValue(mergeJSONSlot{present: true, value: malformedObject})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rehydrateTypedLiveRoot(taskKey, malformed); err == nil || !reflect.DeepEqual(got, FieldValue{}) {
		t.Fatalf("rehydrate malformed root = (%+v, %v)", got, err)
	}

	for _, test := range []struct {
		name string
		key  state.RecordKey
		base recordSurface
	}{
		{name: "absent", key: taskKey, base: recordSurface{State: recordEndpointAbsent}},
		{name: "tombstone", key: taskKey, base: recordTombstoneSurface(t, taskKey)},
		{name: "event", key: state.RecordKey{Kind: "event", ID: diffEventID}, base: recordSurfaceFromSnapshot(t, snapshot, state.RecordKey{Kind: "event", ID: diffEventID})},
		{name: "git_link", key: state.RecordKey{Kind: "git_link", ID: diffGitLinkID}, base: recordSurfaceFromSnapshot(t, snapshot, state.RecordKey{Kind: "git_link", ID: diffGitLinkID})},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeExistingTypedLiveRecord(test.key, test.base, test.base, test.base)
			if err == nil || !reflect.DeepEqual(got, recordSurfaceMergeResult{}) {
				t.Fatalf("invalid typed live merge = (%+v, %v)", got, err)
			}
		})
	}
}

func recordSurfaceFromSnapshot(t *testing.T, snapshot state.Snapshot, key state.RecordKey) recordSurface {
	t.Helper()
	surfaces, err := snapshotRecordSurfaces(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	surface, ok := surfaces[key]
	if !ok {
		t.Fatalf("snapshot has no %v surface", key)
	}
	return surface
}

func recordConcreteLiveValue(t *testing.T, snapshot state.Snapshot, key state.RecordKey) any {
	t.Helper()
	switch key.Kind {
	case "project":
		return snapshot.Project
	case "actor":
		return *snapshot.Actors[key.ID].Value
	case "task":
		return *snapshot.Tasks[key.ID].Value
	case "task_link":
		return *snapshot.TaskLinks[key.ID].Value
	case "kb_article":
		return *snapshot.Articles[key.ID].Value
	case "channel":
		return *snapshot.Channels[key.ID].Value
	case "event":
		return snapshot.Events[key.ID]
	case "git_link":
		return *snapshot.GitLinks[key.ID].Value
	default:
		t.Fatalf("unsupported concrete record kind %q", key.Kind)
		return nil
	}
}
