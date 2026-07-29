package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestValidateDirectDeltaRejectsImmutableEventMutation(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	event := next.Events[diffEventID]
	event.EventType = "message.edited"
	next.Events[diffEventID] = event
	next = diffCanonicalSnapshot(t, next)

	err := ValidateDirectDelta(prior, next)
	if !errors.Is(err, ErrDirectImmutableRecordMutation) {
		t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableRecordMutation", err)
	}
	if !errors.Is(err, state.ErrImmutableRecord) {
		t.Fatalf("ValidateDirectDelta() error = %v, want state.ErrImmutableRecord", err)
	}
}

func TestValidateDirectDeltaRejectsImmutableGitLinkMutation(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	next.GitLinks[diffGitLinkID].Value.Summary = "changed"
	next = diffCanonicalSnapshot(t, next)

	if err := ValidateDirectDelta(prior, next); !errors.Is(err, ErrDirectImmutableRecordMutation) {
		t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableRecordMutation", err)
	}
}

func TestValidateDirectDeltaRejectsRawPathDeletion(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	for _, test := range []struct {
		name   string
		remove func(*state.Snapshot)
		want   error
	}{
		{name: "event", remove: func(snapshot *state.Snapshot) { delete(snapshot.Events, diffEventID) }, want: ErrDirectImmutableRecordMutation},
		{name: "git link", remove: func(snapshot *state.Snapshot) { delete(snapshot.GitLinks, diffGitLinkID) }, want: ErrDirectImmutableRecordMutation},
		{name: "actor", remove: func(snapshot *state.Snapshot) { delete(snapshot.Actors, composeActorID) }},
		{name: "task", remove: func(snapshot *state.Snapshot) { delete(snapshot.Tasks, composeTaskID) }},
		{name: "task link", remove: func(snapshot *state.Snapshot) { delete(snapshot.TaskLinks, diffTaskLinkID) }},
		{name: "article", remove: func(snapshot *state.Snapshot) { delete(snapshot.Articles, diffArticleID) }},
		{name: "channel", remove: func(snapshot *state.Snapshot) { delete(snapshot.Channels, diffChannelID) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := diffCloneSnapshot(t, prior)
			test.remove(&next)
			want := test.want
			if want == nil {
				want = ErrDirectPathDeletion
			}
			if err := ValidateDirectDelta(prior, next); !errors.Is(err, want) {
				t.Fatalf("ValidateDirectDelta() error = %v, want %v", err, want)
			}
		})
	}
}

func TestValidateDirectDeltaRawDeletionUsesCanonicalKindAndIDOrder(t *testing.T) {
	prior := diffAddTask(t, recordAllKindsSnapshot(t))
	next := diffCloneSnapshot(t, prior)
	delete(next.Tasks, composeTaskID)
	delete(next.Tasks, diffSecondTaskID)
	delete(next.Events, diffEventID)

	err := ValidateDirectDelta(prior, next)
	if !errors.Is(err, ErrDirectPathDeletion) || !strings.Contains(err.Error(), "task "+composeTaskID) {
		t.Fatalf("ValidateDirectDelta() error = %v, want first task deletion", err)
	}
}

func TestValidateDirectDeltaAllowsImmutableReplayAdditionAndNewTombstone(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	const eventID = "99999999-9999-4999-8999-999999999991"
	const gitLinkID = "99999999-9999-4999-8999-999999999992"
	const tombstoneID = "99999999-9999-4999-8999-999999999993"
	now := next.Project.CreatedAt
	next.Events[eventID] = state.EventV1{
		SchemaVersion: 1, Kind: "event", ID: eventID, ChannelID: diffChannelID, ActorID: composeActorID,
		EventType: "message.posted", Payload: json.RawMessage(`{}`), CreatedAt: now, Extensions: state.ExtensionsV1{},
	}
	next.GitLinks[gitLinkID] = state.Record[state.GitLinkV1]{Value: &state.GitLinkV1{
		SchemaVersion: 1, Kind: "git_link", ID: gitLinkID, Repository: "acme/wormhole", Summary: "new link",
		ActorID: composeActorID, CreatedAt: now, Extensions: state.ExtensionsV1{},
	}}
	next.Actors[tombstoneID] = state.Record[state.ActorV1]{Tombstone: &state.TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: tombstoneID, EntityKind: "actor", DeletedContentDigest: directOtherDigest,
		DeletedBy: diffActorEnvelope(), DeletedAt: diffActorEnvelope().OccurredAt, Extensions: state.ExtensionsV1{},
	}}
	next = diffCanonicalSnapshot(t, next)

	if err := ValidateDirectDelta(prior, next); err != nil {
		t.Fatalf("ValidateDirectDelta() error = %v", err)
	}
}

func TestValidateDirectDeltaRejectsInvalidPriorAndNextAndBindingMismatch(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	invalidPrior := diffCloneSnapshot(t, prior)
	invalidPrior.Digest = ""
	if err := ValidateDirectDelta(invalidPrior, prior); err == nil {
		t.Fatal("ValidateDirectDelta accepted invalid prior")
	}

	invalidNext := diffCloneSnapshot(t, prior)
	invalidNext.Project.Name = ""
	if err := ValidateDirectDelta(prior, invalidNext); err == nil {
		t.Fatal("ValidateDirectDelta accepted invalid next")
	}

	mismatch := diffCloneSnapshot(t, prior)
	mismatch.Config.Repository = types.RepositoryIdentity{Provider: "github", ImmutableID: "repository-2", CanonicalRemote: "https://github.com/acme/other"}
	mismatch = diffCanonicalSnapshot(t, mismatch)
	if err := ValidateDirectDelta(prior, mismatch); err == nil {
		t.Fatal("ValidateDirectDelta accepted a binding mismatch")
	}
}

func TestValidateDirectDeltaTombstoneLifecycle(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	taskTombstone := diffTombstoneTask(t, prior)
	articleTombstone := directTombstoneArticle(t, prior)

	for _, test := range []struct {
		name string
		base state.Snapshot
		next state.Snapshot
		want error
	}{
		{name: "accept exact task tombstone", base: prior, next: taskTombstone},
		{name: "accept exact KB tombstone", base: prior, next: articleTombstone},
		{name: "reject incorrect content digest", base: prior, next: directChangedTaskTombstone(t, taskTombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedContentDigest = directOtherDigest }), want: state.ErrTombstoneDigest},
		{name: "reject incorrect KB body digest", base: prior, next: directChangedArticleTombstone(t, articleTombstone, func(tombstone *state.TombstoneV1) { digest := directOtherDigest; tombstone.DeletedBodyDigest = &digest }), want: state.ErrTombstoneDigest},
		{name: "reject missing KB body digest", base: prior, next: directChangedArticleTombstoneRaw(t, articleTombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedBodyDigest = nil }), want: state.ErrTombstoneDigest},
		{name: "reject edited tombstone", base: taskTombstone, next: directChangedTaskTombstone(t, taskTombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedAt = tombstone.DeletedAt.Add(time.Minute) }), want: ErrDirectEditTombstone},
		{name: "reject resurrection", base: taskTombstone, next: diffResurrectTask(t, taskTombstone, *prior.Tasks[composeTaskID].Value), want: ErrDirectResurrection},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDirectDelta(test.base, test.next)
			if test.want == nil {
				if err != nil {
					t.Fatalf("ValidateDirectDelta() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("ValidateDirectDelta() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestValidateDirectDeltaMissingKBBodyDigestPreflightDoesNotMaskMalformedRecord(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := directTombstoneArticle(t, prior)
	next = directChangedArticleTombstoneRaw(t, next, func(tombstone *state.TombstoneV1) { tombstone.DeletedBodyDigest = nil })
	live := *prior.Articles[diffArticleID].Value
	record := next.Articles[diffArticleID]
	record.Value = &live
	next.Articles[diffArticleID] = record

	err := ValidateDirectDelta(prior, next)
	if err == nil || errors.Is(err, state.ErrTombstoneDigest) {
		t.Fatalf("ValidateDirectDelta() error = %v, want generic invalid-next error", err)
	}
}

func TestValidateDirectDeltaMissingKBBodyDigestPreflightDoesNotMaskMalformedTombstone(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	tombstone := directTombstoneArticle(t, prior)
	for _, test := range []struct {
		name   string
		mutate func(*state.Snapshot)
	}{
		{name: "unexpected body", mutate: func(snapshot *state.Snapshot) {
			record := snapshot.Articles[diffArticleID]
			record.Body = []byte("unexpected\n")
			snapshot.Articles[diffArticleID] = record
		}},
		{name: "bad tombstone header", mutate: func(snapshot *state.Snapshot) {
			snapshot.Articles[diffArticleID].Tombstone.SchemaVersion = 2
		}},
		{name: "bad tombstone actor", mutate: func(snapshot *state.Snapshot) {
			snapshot.Articles[diffArticleID].Tombstone.DeletedBy = types.ActorEnvelope{}
		}},
		{name: "stale snapshot digest", mutate: func(snapshot *state.Snapshot) {
			snapshot.Digest = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := directChangedArticleTombstoneRaw(t, tombstone, func(tombstone *state.TombstoneV1) { tombstone.DeletedBodyDigest = nil })
			test.mutate(&next)
			err := ValidateDirectDelta(prior, next)
			if err == nil || errors.Is(err, state.ErrTombstoneDigest) {
				t.Fatalf("ValidateDirectDelta() error = %v, want generic invalid-next error", err)
			}
		})
	}
}

func TestValidateDirectDeltaRejectsChangedCreatedAt(t *testing.T) {
	for _, test := range []struct {
		name   string
		base   func(*testing.T) state.Snapshot
		change func(*state.Snapshot)
	}{
		{name: "project", base: composeFixtureSnapshot, change: func(snapshot *state.Snapshot) {
			snapshot.Project.CreatedAt = snapshot.Project.CreatedAt.Add(-time.Minute)
		}},
		{name: "task", base: composeFixtureSnapshot, change: func(snapshot *state.Snapshot) {
			snapshot.Tasks[composeTaskID].Value.CreatedAt = snapshot.Tasks[composeTaskID].Value.CreatedAt.Add(-time.Minute)
		}},
		{name: "KB article", base: func(t *testing.T) state.Snapshot {
			return diffSnapshotWithArticle(t, composeFixtureSnapshot(t), map[string]json.RawMessage{}, "body\n")
		}, change: func(snapshot *state.Snapshot) {
			snapshot.Articles[diffArticleID].Value.CreatedAt = snapshot.Articles[diffArticleID].Value.CreatedAt.Add(-time.Minute)
		}},
		{name: "channel", base: recordAllKindsSnapshot, change: func(snapshot *state.Snapshot) {
			snapshot.Channels[diffChannelID].Value.CreatedAt = snapshot.Channels[diffChannelID].Value.CreatedAt.Add(-time.Minute)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prior := test.base(t)
			next := diffCloneSnapshot(t, prior)
			test.change(&next)
			next = diffCanonicalSnapshot(t, next)
			if err := ValidateDirectDelta(prior, next); !errors.Is(err, ErrDirectImmutableFieldMutation) {
				t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableFieldMutation", err)
			}
		})
	}
}

func TestValidateDirectDeltaAllowsMutableAndGitOwnedChangesWithoutAliasing(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	next.Actors[composeActorID].Value.DisplayName = "Changed actor"
	next.Tasks[composeTaskID].Value.Title = "Changed task"
	next.TaskLinks[diffTaskLinkID].Value.LinkType = "task"
	next.TaskLinks[diffTaskLinkID].Value.TargetID = composeTaskID
	next.Config.Handle = types.ProjectHandle{Namespace: "other", Name: "handle"}
	next.Remotes = &state.RemotesV1{Version: 1, Fabrics: []state.FabricHintV1{{
		Alias: "public", URL: "https://fabric.example.test", InstanceID: "fabric-one", RemoteProjectID: "remote-one", Mode: "public",
	}}}
	next = diffCanonicalSnapshot(t, next)
	priorBefore, nextBefore := diffTreeBytes(t, prior), diffTreeBytes(t, next)

	if err := ValidateDirectDelta(prior, next); err != nil {
		t.Fatalf("ValidateDirectDelta() error = %v", err)
	}
	if !bytes.Equal(priorBefore, diffTreeBytes(t, prior)) || !bytes.Equal(nextBefore, diffTreeBytes(t, next)) {
		t.Fatal("ValidateDirectDelta mutated or aliased an input")
	}
}

func TestValidateDirectDeltaErrorDoesNotMutateInputs(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	event := next.Events[diffEventID]
	event.EventType = "message.edited"
	next.Events[diffEventID] = event
	next = diffCanonicalSnapshot(t, next)
	priorBefore, nextBefore := diffTreeBytes(t, prior), diffTreeBytes(t, next)

	err := ValidateDirectDelta(prior, next)
	if !errors.Is(err, ErrDirectImmutableRecordMutation) {
		t.Fatalf("ValidateDirectDelta() error = %v, want ErrDirectImmutableRecordMutation", err)
	}
	if !bytes.Equal(priorBefore, diffTreeBytes(t, prior)) || !bytes.Equal(nextBefore, diffTreeBytes(t, next)) {
		t.Fatal("ValidateDirectDelta mutated or aliased an input on error")
	}
}

func TestValidateDirectDeltaPrecedence(t *testing.T) {
	prior := recordAllKindsSnapshot(t)
	next := diffCloneSnapshot(t, prior)
	delete(next.Events, diffEventID)
	delete(next.Tasks, composeTaskID)
	next.Project.Name = ""
	if err := ValidateDirectDelta(prior, next); !errors.Is(err, ErrDirectPathDeletion) {
		t.Fatalf("ValidateDirectDelta() error = %v, want raw task deletion before other failures", err)
	}

	stale := diffCloneSnapshot(t, prior)
	stale.Digest = ""
	if err := ValidateDirectDelta(stale, next); err == nil || errors.Is(err, ErrDirectPathDeletion) {
		t.Fatalf("ValidateDirectDelta() error = %v, want invalid prior before raw deletion", err)
	}
}

const directOtherDigest state.Digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func directChangedTaskTombstone(t *testing.T, snapshot state.Snapshot, change func(*state.TombstoneV1)) state.Snapshot {
	t.Helper()
	next := diffCloneSnapshot(t, snapshot)
	change(next.Tasks[composeTaskID].Tombstone)
	return diffCanonicalSnapshot(t, next)
}

func directChangedArticleTombstone(t *testing.T, snapshot state.Snapshot, change func(*state.TombstoneV1)) state.Snapshot {
	t.Helper()
	next := diffCloneSnapshot(t, snapshot)
	change(next.Articles[diffArticleID].Tombstone)
	return diffCanonicalSnapshot(t, next)
}

func directChangedArticleTombstoneRaw(t *testing.T, snapshot state.Snapshot, change func(*state.TombstoneV1)) state.Snapshot {
	t.Helper()
	next := diffCloneSnapshot(t, snapshot)
	change(next.Articles[diffArticleID].Tombstone)
	return next
}

func directTombstoneArticle(t *testing.T, snapshot state.Snapshot) state.Snapshot {
	t.Helper()
	contentDigest, err := state.DigestCanonicalJSON(*snapshot.Articles[diffArticleID].Value)
	if err != nil {
		t.Fatal(err)
	}
	bodyDigest, err := state.DigestCanonicalMarkdown(snapshot.Articles[diffArticleID].Body)
	if err != nil {
		t.Fatal(err)
	}
	next, err := state.ApplyOperation(snapshot, state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999997", Kind: state.OperationTombstone,
		ExpectedViewDigest: snapshot.Digest, Actor: diffActorEnvelope(),
		Tombstone: &state.TombstoneOperationV1{
			Key: state.RecordKey{Kind: "kb_article", ID: diffArticleID}, ExpectedContentDigest: contentDigest, ExpectedBodyDigest: &bodyDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return next
}
