package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	liveCreatedAt = "2026-01-01T00:00:00Z"
	liveBaseTime  = "2026-02-01T00:00:00Z"
	liveOursTime  = "2026-03-01T00:00:00Z"
	liveTheirTime = "2026-04-01T00:00:00Z"
)

func TestMergeExistingLiveRecordDisjointAndSameFields(t *testing.T) {
	t.Run("disjoint fields", func(t *testing.T) {
		base := liveSurface(liveDisjointRoot(0, 0, liveBaseTime), "")
		ours := liveSurface(liveDisjointRoot(1, 0, liveOursTime), "")
		theirs := liveSurface(liveDisjointRoot(0, 2, liveTheirTime), "")
		got, err := mergeExistingLiveRecord(liveKey("project"), base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		want := liveDisjointRoot(1, 2, liveTheirTime)
		if !reflect.DeepEqual(got.Surface.Root, want) || got.Surface.Body.Present || got.Conflicts == nil || len(got.Conflicts) != 0 {
			t.Fatalf("mergeExistingLiveRecord = %+v, want root %s", got, want.Value)
		}
	})

	t.Run("same field", func(t *testing.T) {
		base := liveSurface(liveTimedRoot("base", liveBaseTime), "")
		ours := liveSurface(liveTimedRoot("ours", liveOursTime), "")
		theirs := liveSurface(liveTimedRoot("theirs", liveTheirTime), "")
		got, err := mergeExistingLiveRecord(liveKey("task"), base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != ConflictSameField || got.Conflicts[0].FieldPath != "/title" {
			t.Fatalf("conflicts = %+v", got.Conflicts)
		}
		wantRoot := liveTimedRoot("ours", liveBaseTime)
		if !reflect.DeepEqual(got.Surface.Root, wantRoot) {
			t.Fatalf("conflicted root = %s, want ours semantics with base metadata %s", got.Surface.Root.Value, wantRoot.Value)
		}
		conflict := got.Conflicts[0]
		if string(conflict.Base.Value) != `"base"` || string(conflict.Ours.Value) != `"ours"` || string(conflict.Theirs.Value) != `"theirs"` {
			t.Fatalf("conflict orientation = %+v", conflict)
		}
	})
}

func TestMergeExistingLiveRecordRejectsCreatedAtChangesByKindAndSide(t *testing.T) {
	for _, kind := range []string{"project", "task", "kb_article", "channel"} {
		for _, side := range []string{"ours", "theirs"} {
			t.Run(kind+"_"+side, func(t *testing.T) {
				base := liveKindSurface(kind, liveCreatedAt, "base", liveBaseTime)
				ours := liveKindSurface(kind, liveCreatedAt, "base", liveBaseTime)
				theirs := liveKindSurface(kind, liveCreatedAt, "base", liveBaseTime)
				if side == "ours" {
					ours = liveKindSurface(kind, "2025-12-31T00:00:00Z", "base", liveBaseTime)
				} else {
					theirs = liveKindSurface(kind, "2025-12-31T00:00:00Z", "base", liveBaseTime)
				}
				got, err := mergeExistingLiveRecord(liveKey(kind), base, ours, theirs)
				if !errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, liveRecordMergeResult{}) {
					t.Fatalf("mergeExistingLiveRecord = (%+v, %v), want zero ErrOperationPrecondition", got, err)
				}
			})
		}
	}
}

func TestMergeExistingLiveRecordActorAndTaskLinkHaveNoTimestamps(t *testing.T) {
	for _, kind := range []string{"actor", "task_link"} {
		t.Run(kind, func(t *testing.T) {
			base := liveSurface(mergeJSONValue(`{"title":"base"}`), "")
			ours := liveSurface(mergeJSONValue(`{"title":"ours"}`), "")
			theirs := liveSurface(mergeJSONValue(`{"title":"base"}`), "")
			got, err := mergeExistingLiveRecord(liveKey(kind), base, ours, theirs)
			if err != nil || len(got.Conflicts) != 0 || !reflect.DeepEqual(got.Surface.Root, mergeJSONValue(`{"title":"ours"}`)) {
				t.Fatalf("mergeExistingLiveRecord = (%+v, %v)", got, err)
			}
		})
	}
}

func TestMergeExistingLiveRecordUpdatedAtSelection(t *testing.T) {
	tests := []struct {
		name                              string
		baseTitle, oursTitle, theirsTitle string
		oursTime, theirsTime              string
		wantTitle, wantTime               string
	}{
		{
			name: "ours semantic editor", baseTitle: "base", oursTitle: "ours", theirsTitle: "base",
			oursTime: liveOursTime, theirsTime: liveTheirTime, wantTitle: "ours", wantTime: liveOursTime,
		},
		{
			name: "theirs semantic editor", baseTitle: "base", oursTitle: "base", theirsTitle: "theirs",
			oursTime: liveTheirTime, theirsTime: liveOursTime, wantTitle: "theirs", wantTime: liveOursTime,
		},
		{
			name: "two clean editors use later UTC", baseTitle: "base", oursTitle: "shared", theirsTitle: "shared",
			oursTime: liveTheirTime, theirsTime: liveOursTime, wantTitle: "shared", wantTime: liveTheirTime,
		},
		{
			name: "neither editor retains base", baseTitle: "base", oursTitle: "base", theirsTitle: "base",
			oursTime: liveOursTime, theirsTime: liveTheirTime, wantTitle: "base", wantTime: liveBaseTime,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := liveSurface(liveTimedRoot(test.baseTitle, liveBaseTime), "")
			ours := liveSurface(liveTimedRoot(test.oursTitle, test.oursTime), "")
			theirs := liveSurface(liveTimedRoot(test.theirsTitle, test.theirsTime), "")
			got, err := mergeExistingLiveRecord(liveKey("task"), base, ours, theirs)
			if err != nil {
				t.Fatal(err)
			}
			want := liveTimedRoot(test.wantTitle, test.wantTime)
			if !reflect.DeepEqual(got.Surface.Root, want) || len(got.Conflicts) != 0 {
				t.Fatalf("merge = (%s, %+v), want (%s, [])", got.Surface.Root.Value, got.Conflicts, want.Value)
			}
		})
	}
}

func TestMergeExistingLiveRecordKBSemanticEditorsAndCleanMarkdown(t *testing.T) {
	t.Run("body-only editor supplies timestamp", func(t *testing.T) {
		base := liveSurface(liveTimedRoot("base", liveBaseTime), "base\n")
		ours := liveSurface(liveTimedRoot("base", liveOursTime), "ours\n")
		theirs := liveSurface(liveTimedRoot("base", liveTheirTime), "base\n")
		got, err := mergeExistingLiveRecord(liveKey("kb_article"), base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Surface.Root, liveTimedRoot("base", liveOursTime)) ||
			string(got.Surface.Body.Value) != `"ours\n"` || len(got.Conflicts) != 0 {
			t.Fatalf("merge = %+v", got)
		}
	})

	t.Run("non-overlapping body editors merge and use later timestamp", func(t *testing.T) {
		base := liveSurface(liveTimedRoot("base", liveBaseTime), "a\nb\nc\nd\n")
		ours := liveSurface(liveTimedRoot("base", liveOursTime), "a\nB\nc\nd\n")
		theirs := liveSurface(liveTimedRoot("base", liveTheirTime), "a\nb\nc\nD\n")
		got, err := mergeExistingLiveRecord(liveKey("kb_article"), base, ours, theirs)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Surface.Root, liveTimedRoot("base", liveTheirTime)) ||
			string(got.Surface.Body.Value) != `"a\nB\nc\nD\n"` || len(got.Conflicts) != 0 {
			t.Fatalf("merge = %+v", got)
		}
	})
}

func TestMergeExistingLiveRecordMarkdownConflictHasCompleteEvidenceAndNoMarkers(t *testing.T) {
	base := liveSurface(liveTimedRoot("base", liveBaseTime), "a\nb\n")
	ours := liveSurface(liveTimedRoot("base", liveOursTime), "a\nx\nb\n")
	theirs := liveSurface(liveTimedRoot("base", liveTheirTime), "a\ny\nb\n")
	got, err := mergeExistingLiveRecord(liveKey("kb_article"), base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != ConflictMarkdown || got.Conflicts[0].FieldPath != "/body" {
		t.Fatalf("conflicts = %+v", got.Conflicts)
	}
	conflict := got.Conflicts[0]
	if string(conflict.Base.Value) != `"a\nb\n"` || string(conflict.Ours.Value) != `"a\nx\nb\n"` || string(conflict.Theirs.Value) != `"a\ny\nb\n"` || conflict.ID == "" {
		t.Fatalf("Markdown evidence = %+v", conflict)
	}
	if !reflect.DeepEqual(got.Surface.Root, liveTimedRoot("base", liveBaseTime)) || string(got.Surface.Body.Value) != `"a\nx\nb\n"` {
		t.Fatalf("conflicted surface = %+v", got.Surface)
	}
	if bytes.Contains(got.Surface.Body.Value, []byte("<<<<<<<")) || bytes.Contains(got.Surface.Body.Value, []byte(">>>>>>>")) {
		t.Fatalf("conflict markers entered body: %s", got.Surface.Body.Value)
	}
}

func TestMergeExistingLiveRecordContinuesRootAndBodyConflicts(t *testing.T) {
	base := liveSurface(liveTimedRoot("base", liveBaseTime), "a\nb\n")
	ours := liveSurface(liveTimedRoot("ours", liveOursTime), "a\nx\nb\n")
	theirs := liveSurface(liveTimedRoot("theirs", liveTheirTime), "a\ny\nb\n")
	got, err := mergeExistingLiveRecord(liveKey("kb_article"), base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Conflicts) != 2 || got.Conflicts[0].FieldPath != "/body" || got.Conflicts[0].Kind != ConflictMarkdown ||
		got.Conflicts[1].FieldPath != "/title" || got.Conflicts[1].Kind != ConflictSameField {
		t.Fatalf("conflicts = %+v", got.Conflicts)
	}
	if !reflect.DeepEqual(got.Surface.Root, liveTimedRoot("ours", liveBaseTime)) {
		t.Fatalf("timestamp selected through conflict: %s", got.Surface.Root.Value)
	}
}

func TestMergeExistingLiveRecordMarkdownLimitReturnsZeroResult(t *testing.T) {
	baseBody := strings.Repeat("base\n", 10_001)
	oursBody := strings.Repeat("ours\n", 10_000)
	theirsBody := strings.Repeat("theirs\n", 10_000)
	base := liveSurface(liveTimedRoot("base", liveBaseTime), baseBody)
	ours := liveSurface(liveTimedRoot("base", liveOursTime), oursBody)
	theirs := liveSurface(liveTimedRoot("base", liveTheirTime), theirsBody)
	got, err := mergeExistingLiveRecord(liveKey("kb_article"), base, ours, theirs)
	if !errors.Is(err, ErrMarkdownMergeLimit) || !reflect.DeepEqual(got, liveRecordMergeResult{}) {
		t.Fatalf("mergeExistingLiveRecord = (%+v, %v), want zero ErrMarkdownMergeLimit", got, err)
	}
}

func TestMergeExistingLiveRecordRejectsInvalidSurfaceShapesWithZeroResult(t *testing.T) {
	validTask := liveSurface(liveTimedRoot("base", liveBaseTime), "")
	validKB := liveSurface(liveTimedRoot("base", liveBaseTime), "body\n")
	tests := []struct {
		name               string
		key                state.RecordKey
		base, ours, theirs liveRecordSurface
	}{
		{name: "unsupported kind", key: liveKey("event"), base: validTask, ours: validTask, theirs: validTask},
		{name: "absent root", key: liveKey("task"), base: liveRecordSurface{}, ours: validTask, theirs: validTask},
		{name: "scalar root", key: liveKey("task"), base: liveSurface(mergeJSONValue(`1`), ""), ours: validTask, theirs: validTask},
		{name: "noncanonical root", key: liveKey("task"), base: liveSurface(FieldValue{Present: true, Value: json.RawMessage(` {}`)}, ""), ours: validTask, theirs: validTask},
		{name: "task body", key: liveKey("task"), base: liveSurface(liveTimedRoot("base", liveBaseTime), "body\n"), ours: validTask, theirs: validTask},
		{name: "kb absent body", key: liveKey("kb_article"), base: liveRecordSurface{Root: validKB.Root}, ours: validKB, theirs: validKB},
		{name: "kb non-string body", key: liveKey("kb_article"), base: liveRecordSurface{Root: validKB.Root, Body: mergeJSONValue(`1`)}, ours: validKB, theirs: validKB},
		{name: "kb noncanonical Markdown", key: liveKey("kb_article"), base: liveRecordSurface{Root: validKB.Root, Body: liveBodyValue("body\r\n")}, ours: validKB, theirs: validKB},
		{name: "missing created_at", key: liveKey("task"), base: liveSurface(mergeJSONValue(`{"title":"base","updated_at":"2026-02-01T00:00:00Z"}`), ""), ours: validTask, theirs: validTask},
		{name: "channel updated_at", key: liveKey("channel"), base: liveSurface(liveTimedRoot("base", liveBaseTime), ""), ours: validTask, theirs: validTask},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mergeExistingLiveRecord(test.key, test.base, test.ours, test.theirs)
			if err == nil || !reflect.DeepEqual(got, liveRecordMergeResult{}) {
				t.Fatalf("mergeExistingLiveRecord invalid = (%+v, %v)", got, err)
			}
		})
	}
}

func TestMergeExistingLiveRecordDeterministicAndDoesNotAlias(t *testing.T) {
	base := liveSurface(liveTimedRoot("base", liveBaseTime), "a\nb\n")
	ours := liveSurface(liveTimedRoot("ours", liveOursTime), "a\nx\nb\n")
	theirs := liveSurface(liveTimedRoot("theirs", liveTheirTime), "a\ny\nb\n")
	want, err := mergeExistingLiveRecord(liveKey("kb_article"), base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 50; run++ {
		got, err := mergeExistingLiveRecord(liveKey("kb_article"), base, ours, theirs)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, want)
		}
	}
	baseBefore, oursBefore, theirsBefore := cloneLiveSurface(base), cloneLiveSurface(ours), cloneLiveSurface(theirs)
	want.Surface.Root.Value[0] ^= 1
	want.Surface.Body.Value[0] ^= 1
	want.Conflicts[0].Base.Value[0] ^= 1
	if !reflect.DeepEqual(base, baseBefore) || !reflect.DeepEqual(ours, oursBefore) || !reflect.DeepEqual(theirs, theirsBefore) {
		t.Fatal("merged surface or evidence aliases an input")
	}
}

func liveKey(kind string) state.RecordKey {
	return state.RecordKey{Kind: kind, ID: composeTaskID}
}

func liveTimedRoot(title, updatedAt string) FieldValue {
	return mergeJSONValue(fmt.Sprintf(`{"created_at":"%s","title":"%s","updated_at":"%s"}`, liveCreatedAt, title, updatedAt))
}

func liveDisjointRoot(left, right int, updatedAt string) FieldValue {
	return mergeJSONValue(fmt.Sprintf(`{"created_at":"%s","left":%d,"right":%d,"updated_at":"%s"}`, liveCreatedAt, left, right, updatedAt))
}

func liveKindSurface(kind, createdAt, title, updatedAt string) liveRecordSurface {
	if kind == "channel" {
		return liveSurface(mergeJSONValue(fmt.Sprintf(`{"created_at":"%s","title":"%s"}`, createdAt, title)), "")
	}
	body := ""
	if kind == "kb_article" {
		body = "body\n"
	}
	return liveSurface(mergeJSONValue(fmt.Sprintf(`{"created_at":"%s","title":"%s","updated_at":"%s"}`, createdAt, title, updatedAt)), body)
}

func liveSurface(root FieldValue, body string) liveRecordSurface {
	surface := liveRecordSurface{Root: root}
	if body != "" {
		surface.Body = liveBodyValue(body)
	}
	return surface
}

func liveBodyValue(body string) FieldValue {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return FieldValue{Present: true, Value: raw}
}

func cloneLiveSurface(surface liveRecordSurface) liveRecordSurface {
	return liveRecordSurface{Root: cloneMergeFieldValue(surface.Root), Body: cloneMergeFieldValue(surface.Body)}
}
