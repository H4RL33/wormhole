package projectstate

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestThreeWayRebaseRawMutableDeletionPrecedesSideValidation(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	for _, side := range []string{"new base", "candidate"} {
		t.Run(side, func(t *testing.T) {
			newBase := diffCloneSnapshot(t, oldBase)
			candidate := diffCloneSnapshot(t, oldBase)
			invalid := &newBase
			if side == "candidate" {
				invalid = &candidate
			}
			delete(invalid.Tasks, composeTaskID)
			invalid.Project.Name = ""

			got, err := ThreeWayRebase(oldBase, newBase, candidate)
			wantError := "projectstate: merge " + side + ": " + ErrRawRecordDeletion.Error() + ": task " + composeTaskID
			if !errors.Is(err, ErrRawRecordDeletion) || err.Error() != wantError || !reflect.DeepEqual(got, MergeResult{}) {
				t.Fatalf("raw deletion precedence = (%+v, %v), want zero and %q", got, err, wantError)
			}
		})
	}
}

func TestThreeWayRebaseChecksNewBaseRawDeletionBeforeCandidate(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	newBase := diffCloneSnapshot(t, oldBase)
	candidate := diffCloneSnapshot(t, oldBase)
	delete(newBase.Tasks, composeTaskID)
	delete(candidate.Actors, composeActorID)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	wantError := "projectstate: merge new base: " + ErrRawRecordDeletion.Error() + ": task " + composeTaskID
	if !errors.Is(err, ErrRawRecordDeletion) || err.Error() != wantError || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("raw deletion side order = (%+v, %v), want zero and %q", got, err, wantError)
	}
}

func TestThreeWayRebaseRawDeletionUsesCanonicalKindAndUUIDOrderWithinNewBase(t *testing.T) {
	const smallestActorID = "00000000-0000-4000-8000-000000000009"
	oldBase := recordAllKindsSnapshot(t)
	secondActor := *oldBase.Actors[composeActorID].Value
	secondActor.ID = smallestActorID
	secondActor.DisplayName = "First sorted actor"
	oldBase.Actors[smallestActorID] = state.Record[state.ActorV1]{Value: &secondActor}
	oldBase = diffCanonicalSnapshot(t, oldBase)
	newBase := diffCloneSnapshot(t, oldBase)
	delete(newBase.Actors, composeActorID)
	delete(newBase.Actors, smallestActorID)
	delete(newBase.Tasks, composeTaskID)
	candidate := diffCloneSnapshot(t, oldBase)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	wantError := "projectstate: merge new base: " + ErrRawRecordDeletion.Error() + ": actor " + smallestActorID
	if !errors.Is(err, ErrRawRecordDeletion) || err.Error() != wantError || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("canonical raw deletion order = (%+v, %v), want zero and %q", got, err, wantError)
	}
}

func TestThreeWayRebaseCleanDisjointTaskMergeOwnsResultAndAdoptsGitFields(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	candidate := diffCloneSnapshot(t, oldBase)
	newBase := diffCloneSnapshot(t, oldBase)
	candidate.Tasks[composeTaskID].Value.Title = "candidate title"
	candidate.Tasks[composeTaskID].Value.UpdatedAt = candidate.Tasks[composeTaskID].Value.UpdatedAt.Add(time.Minute)
	candidate = diffCanonicalSnapshot(t, candidate)
	newBase.Tasks[composeTaskID].Value.Description = "new base description"
	newBase.Tasks[composeTaskID].Value.UpdatedAt = newBase.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	newBase.Config.Handle.Namespace = "renamed"
	newBase.Config.Handle.Name = "project"
	newBase.Remotes = mergeTestRemotes()
	newBase = diffCanonicalSnapshot(t, newBase)

	want := diffCloneSnapshot(t, oldBase)
	want.Tasks[composeTaskID].Value.Title = candidate.Tasks[composeTaskID].Value.Title
	want.Tasks[composeTaskID].Value.Description = newBase.Tasks[composeTaskID].Value.Description
	want.Tasks[composeTaskID].Value.UpdatedAt = newBase.Tasks[composeTaskID].Value.UpdatedAt
	want.Config = newBase.Config
	want.Remotes = newBase.Remotes
	want = diffCanonicalSnapshot(t, want)
	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diffTreeBytes(t, got.Snapshot), diffTreeBytes(t, want)) || got.Snapshot.Digest != want.Digest ||
		got.Conflicts == nil || len(got.Conflicts) != 0 {
		t.Fatalf("clean orchestration = %+v, want %+v", got, want)
	}
	tree, err := state.EncodeTree(got.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot.Digest != digest {
		t.Fatalf("clean digest = %q, want canonical %q", got.Snapshot.Digest, digest)
	}

	got.Snapshot.Tasks[composeTaskID].Value.Title = "mutated result"
	got.Snapshot.Remotes.Fabrics[0].Alias = "mutated"
	if !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("clean orchestration result aliases or mutates an input")
	}
}

func TestThreeWayRebaseCleanKBAndTaskMergeOwnsCanonicalResult(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	oldBase.Articles[diffArticleID] = state.KBRecord{
		Value: oldBase.Articles[diffArticleID].Value,
		Body:  []byte("a\nb\nc\nd\n"),
	}
	oldBase = diffCanonicalSnapshot(t, oldBase)
	candidate := diffCloneSnapshot(t, oldBase)
	candidate.Tasks[composeTaskID].Value.Title = "candidate title"
	candidate.Tasks[composeTaskID].Value.UpdatedAt = candidate.Tasks[composeTaskID].Value.UpdatedAt.Add(time.Minute)
	candidateArticle := candidate.Articles[diffArticleID]
	candidateArticle.Body = []byte("a\nB\nc\nd\n")
	candidate.Articles[diffArticleID] = candidateArticle
	candidate.Articles[diffArticleID].Value.UpdatedAt = candidate.Articles[diffArticleID].Value.UpdatedAt.Add(time.Minute)
	candidate = diffCanonicalSnapshot(t, candidate)
	newBase := diffCloneSnapshot(t, oldBase)
	newBase.Tasks[composeTaskID].Value.Description = "new base description"
	newBase.Tasks[composeTaskID].Value.UpdatedAt = newBase.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	newArticle := newBase.Articles[diffArticleID]
	newArticle.Body = []byte("a\nb\nc\nD\n")
	newBase.Articles[diffArticleID] = newArticle
	newBase.Articles[diffArticleID].Value.UpdatedAt = newBase.Articles[diffArticleID].Value.UpdatedAt.Add(2 * time.Minute)
	newBase.Config.Handle.Namespace = "renamed"
	newBase.Config.Handle.Name = "project"
	newBase.Remotes = mergeTestRemotes()
	newBase = diffCanonicalSnapshot(t, newBase)

	want := diffCloneSnapshot(t, oldBase)
	want.Tasks[composeTaskID].Value.Title = candidate.Tasks[composeTaskID].Value.Title
	want.Tasks[composeTaskID].Value.Description = newBase.Tasks[composeTaskID].Value.Description
	want.Tasks[composeTaskID].Value.UpdatedAt = newBase.Tasks[composeTaskID].Value.UpdatedAt
	wantArticle := want.Articles[diffArticleID]
	wantArticle.Body = []byte("a\nB\nc\nD\n")
	want.Articles[diffArticleID] = wantArticle
	want.Articles[diffArticleID].Value.UpdatedAt = newBase.Articles[diffArticleID].Value.UpdatedAt
	want.Config = newBase.Config
	want.Remotes = newBase.Remotes
	want = diffCanonicalSnapshot(t, want)
	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diffTreeBytes(t, got.Snapshot), diffTreeBytes(t, want)) || got.Snapshot.Digest != want.Digest ||
		got.Conflicts == nil || len(got.Conflicts) != 0 {
		t.Fatalf("clean KB and Task merge = %+v, want %+v", got, want)
	}
	if string(got.Snapshot.Articles[diffArticleID].Body) != "a\nB\nc\nD\n" ||
		!got.Snapshot.Tasks[composeTaskID].Value.UpdatedAt.Equal(newBase.Tasks[composeTaskID].Value.UpdatedAt) ||
		!got.Snapshot.Articles[diffArticleID].Value.UpdatedAt.Equal(newBase.Articles[diffArticleID].Value.UpdatedAt) {
		t.Fatalf("clean combined values = Task %+v, KB %+v", got.Snapshot.Tasks[composeTaskID], got.Snapshot.Articles[diffArticleID])
	}
	got.Snapshot.Tasks[composeTaskID].Value.Title = "mutated result"
	got.Snapshot.Articles[diffArticleID].Body[0] = 'X'
	got.Snapshot.Remotes.Fabrics[0].Alias = "mutated"
	if !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("clean KB and Task result aliases or mutates an input")
	}
}

func TestThreeWayRebaseMarkdownLimitReturnsExactZero(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	baseArticle := oldBase.Articles[diffArticleID]
	baseArticle.Body = []byte(strings.Repeat("base\n", 10001))
	oldBase.Articles[diffArticleID] = baseArticle
	oldBase = diffCanonicalSnapshot(t, oldBase)
	candidate := diffCloneSnapshot(t, oldBase)
	candidateArticle := candidate.Articles[diffArticleID]
	candidateArticle.Body = []byte(strings.Repeat("ours\n", 10000))
	candidateArticle.Value.UpdatedAt = candidateArticle.Value.UpdatedAt.Add(time.Minute)
	candidate.Articles[diffArticleID] = candidateArticle
	candidate = diffCanonicalSnapshot(t, candidate)
	newBase := diffCloneSnapshot(t, oldBase)
	newArticle := newBase.Articles[diffArticleID]
	newArticle.Body = []byte(strings.Repeat("theirs\n", 10000))
	newArticle.Value.UpdatedAt = newArticle.Value.UpdatedAt.Add(2 * time.Minute)
	newBase.Articles[diffArticleID] = newArticle
	newBase = diffCanonicalSnapshot(t, newBase)
	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if !errors.Is(err, ErrMarkdownMergeLimit) || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("Markdown limit = (%+v, %v), want exact zero ErrMarkdownMergeLimit", got, err)
	}
	if !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("Markdown limit error mutated an input")
	}
}

func TestThreeWayRebaseOldBaseValidationPrecedesNewBaseRawDeletion(t *testing.T) {
	valid := recordAllKindsSnapshot(t)
	oldBase := diffCloneSnapshot(t, valid)
	oldBase.Project.Name = ""
	newBase := diffCloneSnapshot(t, valid)
	delete(newBase.Tasks, composeTaskID)
	candidate := diffCloneSnapshot(t, valid)
	oldWant := diffCloneSnapshot(t, valid)
	oldWant.Project.Name = ""
	newWant := diffCloneSnapshot(t, valid)
	delete(newWant.Tasks, composeTaskID)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if !errors.Is(err, state.ErrInvalidSnapshot) || errors.Is(err, ErrRawRecordDeletion) || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("old-base precedence = (%+v, %v), want exact zero ErrInvalidSnapshot only", got, err)
	}
	if !reflect.DeepEqual(oldBase, oldWant) || !reflect.DeepEqual(newBase, newWant) || !reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("old-base validation error mutated an input")
	}
}

func TestThreeWayRebaseEqualIllegalProjectCreatedAtMutationReturnsExactZero(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	candidate := diffCloneSnapshot(t, oldBase)
	newBase := diffCloneSnapshot(t, oldBase)
	candidate.Project.CreatedAt = candidate.Project.CreatedAt.Add(-time.Hour)
	newBase.Project.CreatedAt = newBase.Project.CreatedAt.Add(-time.Hour)
	candidate = diffCanonicalSnapshot(t, candidate)
	newBase = diffCanonicalSnapshot(t, newBase)
	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if !errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("equal illegal Project created_at = (%+v, %v), want exact zero ErrOperationPrecondition", got, err)
	}
	if !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("equal Project created_at error mutated an input")
	}
}

func TestThreeWayRebaseLaterCreatedAtErrorDiscardsEarlierProjectConflict(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	candidate := diffCloneSnapshot(t, oldBase)
	newBase := diffCloneSnapshot(t, oldBase)
	candidate.Project.Name = "Candidate project"
	candidate.Project.UpdatedAt = candidate.Project.UpdatedAt.Add(time.Minute)
	newBase.Project.Name = "New base project"
	newBase.Project.UpdatedAt = newBase.Project.UpdatedAt.Add(2 * time.Minute)
	candidate.Tasks[composeTaskID].Value.CreatedAt = candidate.Tasks[composeTaskID].Value.CreatedAt.Add(-time.Hour)
	candidate = diffCanonicalSnapshot(t, candidate)
	newBase = diffCanonicalSnapshot(t, newBase)
	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if !errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("later created_at failure = (%+v, %v), want exact zero ErrOperationPrecondition and no partial conflicts", got, err)
	}
	if !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("later created_at error mutated an input")
	}
}

func TestThreeWayRebaseImmutableDisappearanceReturnsCanonicalCandidateAndOrientedEvidence(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	baseSurfaces, err := snapshotRecordSurfaces(oldBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		key       state.RecordKey
		candidate bool
		wantID    string
	}{
		{name: "event candidate", key: state.RecordKey{Kind: "event", ID: diffEventID}, candidate: true, wantID: "sha256:e7b38bb6dec6115525ada603a285b2221ec2fd2ba432c145d1fb586c39af9ffb"},
		{name: "event new base", key: state.RecordKey{Kind: "event", ID: diffEventID}, wantID: "sha256:eae2bd309fb3080d5e8ce465f3d9d8e8ad1d4924eb4ce6ae3b043fab6f5ad0bc"},
		{name: "git link candidate", key: state.RecordKey{Kind: "git_link", ID: diffGitLinkID}, candidate: true, wantID: "sha256:f748d75bc189f570a36a4525b8a17db0ed4c32f04c177ab7f7d0882bfb1acaef"},
		{name: "git link new base", key: state.RecordKey{Kind: "git_link", ID: diffGitLinkID}, wantID: "sha256:e81311f7902b8abb9be55a80a294511b45de18d802c2a87f0d38a0f7740d223d"},
	} {
		t.Run(test.name, func(t *testing.T) {
			newBase := diffCloneSnapshot(t, oldBase)
			candidate := diffCloneSnapshot(t, oldBase)
			if test.candidate {
				candidate = orchestrationDeleteImmutable(t, candidate, test.key)
			} else {
				newBase = orchestrationDeleteImmutable(t, newBase, test.key)
			}
			oldBefore := diffTreeBytes(t, oldBase)
			newBefore := diffTreeBytes(t, newBase)
			candidateBefore := diffTreeBytes(t, candidate)

			want, err := ThreeWayRebase(oldBase, newBase, candidate)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(diffTreeBytes(t, want.Snapshot), candidateBefore) || want.Snapshot.Digest != candidate.Digest || len(want.Conflicts) != 1 {
				t.Fatalf("immutable disappearance = %+v, want canonical candidate plus one conflict", want)
			}
			conflict := want.Conflicts[0]
			wantOurs, wantTheirs := baseSurfaces[test.key].Root, baseSurfaces[test.key].Root
			if test.candidate {
				wantOurs = FieldValue{}
			} else {
				wantTheirs = FieldValue{}
			}
			if conflict.ID != test.wantID || conflict.Key != test.key || conflict.FieldPath != "" || conflict.Kind != ConflictImmutableRecord ||
				!reflect.DeepEqual(conflict.Base, baseSurfaces[test.key].Root) || !reflect.DeepEqual(conflict.Ours, wantOurs) || !reflect.DeepEqual(conflict.Theirs, wantTheirs) {
				t.Fatalf("immutable conflict = %+v, want ID %q", conflict, test.wantID)
			}
			for run := 0; run < 25; run++ {
				got, err := ThreeWayRebase(oldBase, newBase, candidate)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, want)
				}
			}
			want.Snapshot.Project.Name = "mutated result"
			want.Conflicts[0].Base.Value[0] ^= 1
			if !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
				!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
				t.Fatal("immutable conflict result aliases or mutates an input")
			}
		})
	}
}

func TestThreeWayRebaseInvalidReferencedImmutableDisappearanceIsValidationError(t *testing.T) {
	for _, key := range []state.RecordKey{{Kind: "event", ID: diffEventID}, {Kind: "git_link", ID: diffGitLinkID}} {
		for _, side := range []string{"candidate", "new base"} {
			t.Run(key.Kind+"_"+side, func(t *testing.T) {
				oldBase := recordAllKindsSnapshot(t)
				oldBase.TaskLinks[diffTaskLinkID].Value.LinkType = key.Kind
				oldBase.TaskLinks[diffTaskLinkID].Value.TargetID = key.ID
				oldBase = diffCanonicalSnapshot(t, oldBase)
				newBase := diffCloneSnapshot(t, oldBase)
				candidate := diffCloneSnapshot(t, oldBase)
				if side == "candidate" {
					orchestrationRawDeleteImmutable(&candidate, key)
				} else {
					orchestrationRawDeleteImmutable(&newBase, key)
				}

				got, err := ThreeWayRebase(oldBase, newBase, candidate)
				if !errors.Is(err, state.ErrBrokenReference) || errors.Is(err, ErrRawRecordDeletion) || !reflect.DeepEqual(got, MergeResult{}) {
					t.Fatalf("referenced immutable disappearance = (%+v, %v), want zero ErrBrokenReference only", got, err)
				}
			})
		}
	}
}

func TestThreeWayRebaseCleanRecordMergesRejectBrokenCombinedReferences(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	candidate := diffCloneSnapshot(t, oldBase)
	candidate.Tasks[composeTaskID].Value.OwnerActorID = nil
	if err := setRecordSurface(&candidate, state.RecordKey{Kind: "actor", ID: composeActorID},
		recordTombstoneSurface(t, state.RecordKey{Kind: "actor", ID: composeActorID})); err != nil {
		t.Fatal(err)
	}
	candidate = diffCanonicalSnapshot(t, candidate)
	newBase := diffCloneSnapshot(t, oldBase)
	added := *newBase.Tasks[composeTaskID].Value
	added.ID = diffSecondTaskID
	added.Title = "New dependent task"
	newBase.Tasks[added.ID] = state.Record[state.TaskV1]{Value: &added}
	newBase = diffCanonicalSnapshot(t, newBase)
	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)

	got, err := ThreeWayRebase(oldBase, newBase, candidate)
	if !errors.Is(err, state.ErrBrokenReference) || !reflect.DeepEqual(got, MergeResult{}) {
		t.Fatalf("broken combined references = (%+v, %v), want exact zero ErrBrokenReference", got, err)
	}
	if !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("failed combined-reference merge mutated an input")
	}
}

func TestThreeWayRebaseExistingLiveCreatedAtMutationReturnsExactZero(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	for _, side := range []string{"candidate", "new base"} {
		t.Run(side, func(t *testing.T) {
			newBase := diffCloneSnapshot(t, oldBase)
			candidate := diffCloneSnapshot(t, oldBase)
			changed := &candidate
			if side == "new base" {
				changed = &newBase
			}
			changed.Tasks[composeTaskID].Value.CreatedAt = changed.Tasks[composeTaskID].Value.CreatedAt.Add(-time.Hour)
			*changed = diffCanonicalSnapshot(t, *changed)

			got, err := ThreeWayRebase(oldBase, newBase, candidate)
			if !errors.Is(err, state.ErrOperationPrecondition) || !reflect.DeepEqual(got, MergeResult{}) {
				t.Fatalf("created_at mutation = (%+v, %v), want exact zero ErrOperationPrecondition", got, err)
			}
		})
	}
}

func TestThreeWayRebaseMultiRecordConflictsReturnLosslessOwnedCandidate(t *testing.T) {
	oldBase := recordAllKindsSnapshot(t)
	oldBase.Remotes = mergeTestRemotes()
	oldBase = diffCanonicalSnapshot(t, oldBase)
	candidate := diffCloneSnapshot(t, oldBase)
	candidate.Project.Name = "Candidate project"
	candidate.Project.UpdatedAt = candidate.Project.UpdatedAt.Add(time.Minute)
	candidate.Actors[composeActorID].Value.DisplayName = "Candidate actor"
	candidate.Tasks[composeTaskID].Value.Title = "Candidate task"
	candidate.Tasks[composeTaskID].Value.UpdatedAt = candidate.Tasks[composeTaskID].Value.UpdatedAt.Add(time.Minute)
	candidate.Articles[diffArticleID].Value.Title = "Candidate article"
	candidate.Articles[diffArticleID].Value.UpdatedAt = candidate.Articles[diffArticleID].Value.UpdatedAt.Add(time.Minute)
	candidateArticle := candidate.Articles[diffArticleID]
	candidateArticle.Body = []byte("candidate body\n")
	candidate.Articles[diffArticleID] = candidateArticle
	candidate.Channels[diffChannelID].Value.Name = "candidate-channel"
	candidateEvent := candidate.Events[diffEventID]
	candidateNote := "candidate event"
	candidateEvent.Note = &candidateNote
	candidate.Events[diffEventID] = candidateEvent
	candidate = diffCanonicalSnapshot(t, candidate)

	newBase := diffCloneSnapshot(t, oldBase)
	newBase.Project.Name = "New base project"
	newBase.Project.Aliases = []string{"new-base-alias"}
	newBase.Project.UpdatedAt = newBase.Project.UpdatedAt.Add(2 * time.Minute)
	newBase.Actors[composeActorID].Value.DisplayName = "New base actor"
	newBase.Tasks[composeTaskID].Value.Title = "New base task"
	newBase.Tasks[composeTaskID].Value.Description = "new base description"
	newBase.Tasks[composeTaskID].Value.UpdatedAt = newBase.Tasks[composeTaskID].Value.UpdatedAt.Add(2 * time.Minute)
	newBase.Articles[diffArticleID].Value.Title = "New base article"
	newBase.Articles[diffArticleID].Value.UpdatedAt = newBase.Articles[diffArticleID].Value.UpdatedAt.Add(2 * time.Minute)
	newArticle := newBase.Articles[diffArticleID]
	newArticle.Body = []byte("new base body\n")
	newBase.Articles[diffArticleID] = newArticle
	newEvent := newBase.Events[diffEventID]
	newNote := "new base event"
	newEvent.Note = &newNote
	newBase.Events[diffEventID] = newEvent
	added := *newBase.Tasks[composeTaskID].Value
	added.ID = diffSecondTaskID
	added.Title = "New base added task"
	newBase.Tasks[added.ID] = state.Record[state.TaskV1]{Value: &added}
	newBase.Config.Handle.Namespace = "renamed"
	newBase.Config.Handle.Name = "project"
	newBase.Remotes = mergeTestRemotes()
	newBase.Remotes.Fabrics[0].Alias = "new-base"
	newBase.Remotes.Fabrics[0].InstanceID = "fabric-two"
	newBase.Remotes.Fabrics[0].RemoteProjectID = "remote-two"
	newBase = diffCanonicalSnapshot(t, newBase)
	if candidate.Config != oldBase.Config || !reflect.DeepEqual(candidate.Remotes, oldBase.Remotes) {
		t.Fatalf("candidate fixture changed Git-owned fields: candidate=%+v old=%+v", candidate.Config, oldBase.Config)
	}
	if reflect.DeepEqual(candidate.Project.Aliases, newBase.Project.Aliases) ||
		candidate.Tasks[composeTaskID].Value.Description == newBase.Tasks[composeTaskID].Value.Description ||
		candidate.Tasks[diffSecondTaskID].Value != nil || newBase.Tasks[diffSecondTaskID].Value == nil ||
		candidate.Config.Handle == newBase.Config.Handle || reflect.DeepEqual(candidate.Remotes, newBase.Remotes) {
		t.Fatalf("new-base-only fixture state is not distinct: candidate=%+v new=%+v", candidate, newBase)
	}

	oldBefore := diffTreeBytes(t, oldBase)
	newBefore := diffTreeBytes(t, newBase)
	candidateBefore := diffTreeBytes(t, candidate)
	oldSurfaces, err := snapshotRecordSurfaces(oldBase)
	if err != nil {
		t.Fatal(err)
	}
	candidateSurfaces, err := snapshotRecordSurfaces(candidate)
	if err != nil {
		t.Fatal(err)
	}
	newSurfaces, err := snapshotRecordSurfaces(newBase)
	if err != nil {
		t.Fatal(err)
	}

	first, err := ThreeWayRebase(oldBase, newBase, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(diffTreeBytes(t, first.Snapshot), candidateBefore) || first.Snapshot.Digest != candidate.Digest || first.Conflicts == nil || len(first.Conflicts) != 6 {
		t.Fatalf("multi-record conflict result = %+v, want exact canonical candidate and six conflicts", first)
	}
	if _, err := state.EncodeTree(first.Snapshot); err != nil {
		t.Fatalf("conflicted candidate does not encode: %v", err)
	}
	if !reflect.DeepEqual(first.Snapshot.Config, candidate.Config) || !reflect.DeepEqual(first.Snapshot.Remotes, candidate.Remotes) ||
		!reflect.DeepEqual(first.Snapshot.Project.Aliases, candidate.Project.Aliases) || first.Snapshot.Tasks[composeTaskID].Value.Description != candidate.Tasks[composeTaskID].Value.Description ||
		first.Snapshot.Tasks[diffSecondTaskID].Value != nil || first.Snapshot.Channels[diffChannelID].Value.Name != "candidate-channel" {
		t.Fatalf("conflicted result adopted non-candidate state: %+v", first.Snapshot)
	}

	type conflictWant struct {
		key                state.RecordKey
		path               string
		kind               ConflictKind
		base, ours, theirs FieldValue
		id                 string
	}
	wants := []conflictWant{
		{key: lifecycleKey("project"), path: "/name", kind: ConflictSameField,
			base: orchestrationField(t, "Wormhole"), ours: orchestrationField(t, "Candidate project"), theirs: orchestrationField(t, "New base project"), id: "sha256:c688c406c099805538404654f7edfa3b7f7941cb0af49e73829b18930a9094c8"},
		{key: lifecycleKey("actor"), path: "/display_name", kind: ConflictSameField,
			base: orchestrationField(t, "Compose Actor"), ours: orchestrationField(t, "Candidate actor"), theirs: orchestrationField(t, "New base actor"), id: "sha256:d1c2089f8bdc603a5a4f78d000bac15374654df8f45575bf8d9daa277e1bcc9d"},
		{key: lifecycleKey("task"), path: "/title", kind: ConflictSameField,
			base: orchestrationField(t, "Compose task"), ours: orchestrationField(t, "Candidate task"), theirs: orchestrationField(t, "New base task"), id: "sha256:50be68bfb577bfeac235b66b92cb40944e956b801da93f43565b769644f030f8"},
		{key: lifecycleKey("kb_article"), path: "/body", kind: ConflictMarkdown,
			base: oldSurfaces[lifecycleKey("kb_article")].Body, ours: candidateSurfaces[lifecycleKey("kb_article")].Body, theirs: newSurfaces[lifecycleKey("kb_article")].Body, id: "sha256:7e92469ff1ffc11d5dc68025f5fae0484a4b1ddbeaa19e031118979469132883"},
		{key: lifecycleKey("kb_article"), path: "/title", kind: ConflictSameField,
			base: orchestrationField(t, "Article"), ours: orchestrationField(t, "Candidate article"), theirs: orchestrationField(t, "New base article"), id: "sha256:04e2c9922dae21e770ed7e851fa4af379c22830daa8ed9daf5b355e9932516d1"},
		{key: lifecycleKey("event"), path: "", kind: ConflictImmutableRecord,
			base: oldSurfaces[lifecycleKey("event")].Root, ours: candidateSurfaces[lifecycleKey("event")].Root, theirs: newSurfaces[lifecycleKey("event")].Root, id: "sha256:498055fd28df4eb60a25ce49c436386fafdc8bcbec537caf446dca03fba7ad90"},
	}
	gotIDs := make([]string, len(first.Conflicts))
	wantIDs := make([]string, len(wants))
	for index, want := range wants {
		conflict := first.Conflicts[index]
		gotIDs[index], wantIDs[index] = conflict.ID, want.id
		if conflict.Key != want.key || conflict.FieldPath != want.path || conflict.Kind != want.kind ||
			!reflect.DeepEqual(conflict.Base, want.base) || !reflect.DeepEqual(conflict.Ours, want.ours) || !reflect.DeepEqual(conflict.Theirs, want.theirs) {
			t.Errorf("conflict %d = %+v, want %+v", index, conflict, want)
		}
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("conflict IDs = %+v, want %+v", gotIDs, wantIDs)
	}
	for run := 0; run < 25; run++ {
		got, err := ThreeWayRebase(oldBase, newBase, candidate)
		if err != nil || !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d = (%+v, %v), want %+v", run, got, err, first)
		}
	}

	second, err := ThreeWayRebase(oldBase, newBase, candidate)
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshotBeforeEvidenceMutation := diffTreeBytes(t, first.Snapshot)
	secondSnapshotBefore := diffTreeBytes(t, second.Snapshot)
	secondConflictsBefore := orchestrationCloneConflicts(second.Conflicts)
	firstExpectedEvidence := orchestrationCloneConflicts(first.Conflicts)
	for conflictIndex := range first.Conflicts {
		actualValues := []*FieldValue{&first.Conflicts[conflictIndex].Base, &first.Conflicts[conflictIndex].Ours, &first.Conflicts[conflictIndex].Theirs}
		expectedValues := []*FieldValue{&firstExpectedEvidence[conflictIndex].Base, &firstExpectedEvidence[conflictIndex].Ours, &firstExpectedEvidence[conflictIndex].Theirs}
		for valueIndex := range actualValues {
			if len(actualValues[valueIndex].Value) == 0 {
				continue
			}
			actualValues[valueIndex].Value[0] ^= 1
			expectedValues[valueIndex].Value[0] ^= 1
			if !reflect.DeepEqual(first.Conflicts, firstExpectedEvidence) {
				t.Fatalf("mutating conflict %d evidence %d changed sibling evidence", conflictIndex, valueIndex)
			}
			orchestrationAssertOwnedResultsAndInputs(t, first, firstSnapshotBeforeEvidenceMutation, second, secondSnapshotBefore, secondConflictsBefore,
				oldBase, oldBefore, newBase, newBefore, candidate, candidateBefore)
		}
	}

	firstEvidenceBeforeSnapshotMutation := orchestrationCloneConflicts(first.Conflicts)
	firstSnapshotExpected := diffCloneSnapshot(t, first.Snapshot)
	orchestrationMutateConflictSnapshot(&first.Snapshot)
	orchestrationMutateConflictSnapshot(&firstSnapshotExpected)
	if !reflect.DeepEqual(first.Conflicts, firstEvidenceBeforeSnapshotMutation) {
		t.Fatal("mutating conflicted Snapshot changed conflict evidence")
	}
	orchestrationAssertOwnedResultsAndInputs(t, first, diffTreeBytes(t, firstSnapshotExpected), second, secondSnapshotBefore, secondConflictsBefore,
		oldBase, oldBefore, newBase, newBefore, candidate, candidateBefore)
	firstSnapshotBeforeMetadataMutation := diffTreeBytes(t, first.Snapshot)
	first.Conflicts[0].ID = "mutated-id"
	first.Conflicts[0].FieldPath = "/mutated"
	if !reflect.DeepEqual(diffTreeBytes(t, first.Snapshot), firstSnapshotBeforeMetadataMutation) {
		t.Fatal("mutating conflict metadata changed conflicted Snapshot")
	}
	if !reflect.DeepEqual(diffTreeBytes(t, second.Snapshot), secondSnapshotBefore) || !reflect.DeepEqual(second.Conflicts, secondConflictsBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, oldBase), oldBefore) || !reflect.DeepEqual(diffTreeBytes(t, newBase), newBefore) ||
		!reflect.DeepEqual(diffTreeBytes(t, candidate), candidateBefore) {
		t.Fatal("mutating conflict metadata changed second result or an input")
	}
}

func orchestrationMutateConflictSnapshot(snapshot *state.Snapshot) {
	snapshot.Project.Name = "Mutated result project"
	snapshot.Actors[composeActorID].Value.DisplayName = "Mutated result actor"
	snapshot.Tasks[composeTaskID].Value.Title = "Mutated result task"
	snapshot.Articles[diffArticleID].Value.Title = "Mutated result article"
	mutatedArticle := snapshot.Articles[diffArticleID]
	mutatedArticle.Body = []byte("mutated result body\n")
	snapshot.Articles[diffArticleID] = mutatedArticle
	snapshot.Channels[diffChannelID].Value.Name = "mutated-result-channel"
	mutatedEvent := snapshot.Events[diffEventID]
	mutatedNote := "mutated result event"
	mutatedEvent.Note = &mutatedNote
	snapshot.Events[diffEventID] = mutatedEvent
	snapshot.Remotes.Fabrics[0].Alias = "mutated-result"
}

func orchestrationField(t *testing.T, value any) FieldValue {
	t.Helper()
	field, err := presentFieldValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return field
}

func orchestrationCloneConflicts(conflicts []Conflict) []Conflict {
	cloned := append(make([]Conflict, 0, len(conflicts)), conflicts...)
	for index := range cloned {
		cloned[index].Base = cloneMergeFieldValue(cloned[index].Base)
		cloned[index].Ours = cloneMergeFieldValue(cloned[index].Ours)
		cloned[index].Theirs = cloneMergeFieldValue(cloned[index].Theirs)
	}
	return cloned
}

func orchestrationAssertOwnedResultsAndInputs(t *testing.T, first MergeResult, firstSnapshotWant []byte, second MergeResult, secondSnapshotWant []byte, secondConflictsWant []Conflict,
	oldBase state.Snapshot, oldWant []byte, newBase state.Snapshot, newWant []byte, candidate state.Snapshot, candidateWant []byte,
) {
	t.Helper()
	if !reflect.DeepEqual(diffTreeBytes(t, first.Snapshot), firstSnapshotWant) || !reflect.DeepEqual(diffTreeBytes(t, second.Snapshot), secondSnapshotWant) ||
		!reflect.DeepEqual(second.Conflicts, secondConflictsWant) || !reflect.DeepEqual(diffTreeBytes(t, oldBase), oldWant) ||
		!reflect.DeepEqual(diffTreeBytes(t, newBase), newWant) || !reflect.DeepEqual(diffTreeBytes(t, candidate), candidateWant) {
		t.Fatal("mutating first conflict evidence changed a Snapshot, second result, or input")
	}
}

func orchestrationDeleteImmutable(t *testing.T, snapshot state.Snapshot, key state.RecordKey) state.Snapshot {
	t.Helper()
	orchestrationRawDeleteImmutable(&snapshot, key)
	return diffCanonicalSnapshot(t, snapshot)
}

func orchestrationRawDeleteImmutable(snapshot *state.Snapshot, key state.RecordKey) {
	switch key.Kind {
	case "event":
		delete(snapshot.Events, key.ID)
	case "git_link":
		delete(snapshot.GitLinks, key.ID)
	}
}
