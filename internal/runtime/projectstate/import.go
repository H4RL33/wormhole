package projectstate

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrDirectImmutableRecordMutation = fmt.Errorf("projectstate: direct immutable record mutation: %w", state.ErrImmutableRecord)
	ErrDirectPathDeletion            = errors.New("projectstate: direct path deletion")
	ErrDirectEditTombstone           = errors.New("projectstate: direct tombstone edit")
	ErrDirectResurrection            = errors.New("projectstate: direct resurrection")
	ErrDirectImmutableFieldMutation  = errors.New("projectstate: direct immutable field mutation")
)

// ValidateDirectDelta verifies that next is a permitted direct successor to
// prior. It is pure: neither input is modified or retained.
func ValidateDirectDelta(prior, next state.Snapshot) error {
	prior, err := validatedDiffSnapshot(prior)
	if err != nil {
		return fmt.Errorf("projectstate: direct delta prior: %w", err)
	}
	if key, missing := directRawDeletion(prior, next); missing {
		if key.Kind == "event" || key.Kind == "git_link" {
			return fmt.Errorf("%w: %s %s", ErrDirectImmutableRecordMutation, key.Kind, key.ID)
		}
		return directPathDeletion(key)
	}
	if err := directRawTombstoneDigestPreflight(prior, next); err != nil {
		return err
	}
	next, err = validatedDiffSnapshot(next)
	if err != nil {
		return fmt.Errorf("projectstate: direct delta next: %w", err)
	}
	if err := directBindingEqual(prior, next); err != nil {
		return err
	}
	if err := directImmutableRecordsEqual(prior, next); err != nil {
		return err
	}
	return directMutableRecordsAllowed(prior, next)
}

// directRawTombstoneDigestPreflight preserves the tombstone-digest contract
// for the one digest field that snapshot validation otherwise rejects before
// direct lifecycle validation can classify it.
func directRawTombstoneDigestPreflight(prior, next state.Snapshot) error {
	ids := make([]string, 0, len(prior.Articles))
	for id := range prior.Articles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		priorRecord := prior.Articles[id]
		nextRecord, ok := next.Articles[id]
		if !ok || priorRecord.Value == nil || nextRecord.Value != nil || nextRecord.Tombstone == nil ||
			nextRecord.Tombstone.ID != id || nextRecord.Tombstone.EntityKind != "kb_article" {
			continue
		}
		if nextRecord.Tombstone.DeletedBodyDigest == nil && directKBBodyDigestIsSoleDefect(priorRecord, next, id) {
			return fmt.Errorf("%w: kb_article %s body digest", state.ErrTombstoneDigest, id)
		}
	}
	return nil
}

func directKBBodyDigestIsSoleDefect(prior state.KBRecord, snapshot state.Snapshot, id string) bool {
	digest, err := state.DigestCanonicalMarkdown(prior.Body)
	if err != nil {
		return false
	}
	probe := snapshot
	probe.Articles = make(map[string]state.KBRecord, len(snapshot.Articles))
	for articleID, record := range snapshot.Articles {
		probe.Articles[articleID] = record
	}
	record := probe.Articles[id]
	tombstone := *record.Tombstone
	tombstone.DeletedBodyDigest = &digest
	record.Tombstone = &tombstone
	probe.Articles[id] = record
	_, err = validatedDiffSnapshot(probe)
	return err == nil
}

func directRawDeletion(prior, next state.Snapshot) (state.RecordKey, bool) {
	for _, records := range []struct {
		kind    string
		missing func() (string, bool)
	}{
		{kind: "actor", missing: func() (string, bool) { return firstMissingRecordKey(prior.Actors, next.Actors) }},
		{kind: "task", missing: func() (string, bool) { return firstMissingRecordKey(prior.Tasks, next.Tasks) }},
		{kind: "task_link", missing: func() (string, bool) { return firstMissingRecordKey(prior.TaskLinks, next.TaskLinks) }},
		{kind: "kb_article", missing: func() (string, bool) { return firstMissingRecordKey(prior.Articles, next.Articles) }},
		{kind: "channel", missing: func() (string, bool) { return firstMissingRecordKey(prior.Channels, next.Channels) }},
		{kind: "event", missing: func() (string, bool) { return firstMissingRecordKey(prior.Events, next.Events) }},
		{kind: "git_link", missing: func() (string, bool) { return firstMissingRecordKey(prior.GitLinks, next.GitLinks) }},
	} {
		if id, missing := records.missing(); missing {
			return state.RecordKey{Kind: records.kind, ID: id}, true
		}
	}
	return state.RecordKey{}, false
}

func directPathDeletion(key state.RecordKey) error {
	return fmt.Errorf("%w: %s %s", ErrDirectPathDeletion, key.Kind, key.ID)
}

func directBindingEqual(prior, next state.Snapshot) error {
	if prior.Config.SnapshotVersion != next.Config.SnapshotVersion ||
		prior.Config.ProjectID != next.Config.ProjectID ||
		prior.Config.Repository != next.Config.Repository {
		return fmt.Errorf("projectstate: direct delta binding mismatch")
	}
	return nil
}

func directImmutableRecordsEqual(prior, next state.Snapshot) error {
	for _, records := range []struct {
		kind  string
		prior map[string]any
		next  map[string]any
	}{
		{kind: "event", prior: directEventValues(prior.Events), next: directEventValues(next.Events)},
		{kind: "git_link", prior: directGitLinkValues(prior.GitLinks), next: directGitLinkValues(next.GitLinks)},
	} {
		for _, id := range directSharedIDs(records.prior, records.next) {
			equal, err := directCanonicalEqual(records.prior[id], records.next[id])
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("%w: %s %s", ErrDirectImmutableRecordMutation, records.kind, id)
			}
		}
	}
	return nil
}

func directEventValues(records map[string]state.EventV1) map[string]any {
	values := make(map[string]any, len(records))
	for id, value := range records {
		values[id] = value
	}
	return values
}

func directGitLinkValues(records map[string]state.Record[state.GitLinkV1]) map[string]any {
	values := make(map[string]any, len(records))
	for id, record := range records {
		if record.Value != nil {
			values[id] = *record.Value
		}
	}
	return values
}

func directSharedIDs(left, right map[string]any) []string {
	ids := make([]string, 0)
	for id := range left {
		if _, ok := right[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func directCanonicalEqual(left, right any) (bool, error) {
	leftJSON, err := state.CanonicalJSON(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := state.CanonicalJSON(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

func directMutableRecordsAllowed(prior, next state.Snapshot) error {
	pairs := []directMutablePair{{
		key:   state.RecordKey{Kind: "project", ID: prior.Project.ID},
		prior: directRecord{live: prior.Project, createdAt: prior.Project.CreatedAt},
		next:  directRecord{live: next.Project, createdAt: next.Project.CreatedAt},
	}}
	for _, records := range []directMutableRecordSet{
		directActorRecordSet(prior.Actors, next.Actors),
		directTaskRecordSet(prior.Tasks, next.Tasks),
		directTaskLinkRecordSet(prior.TaskLinks, next.TaskLinks),
		directArticleRecordSet(prior.Articles, next.Articles),
		directChannelRecordSet(prior.Channels, next.Channels),
	} {
		for _, id := range records.sharedIDs() {
			pairs = append(pairs, directMutablePair{
				key: state.RecordKey{Kind: records.kind, ID: id}, prior: records.prior(id), next: records.next(id),
			})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		leftRank, rightRank := diffKindRank(pairs[i].key.Kind), diffKindRank(pairs[j].key.Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if pairs[i].key.ID != pairs[j].key.ID {
			return pairs[i].key.ID < pairs[j].key.ID
		}
		return pairs[i].key.Kind < pairs[j].key.Kind
	})
	for _, pair := range pairs {
		if pair.prior.tombstone == nil && pair.next.tombstone != nil {
			if err := directValidateTombstone(pair.key.Kind, pair.key.ID, pair.prior, *pair.next.tombstone); err != nil {
				return err
			}
		}
	}
	for _, pair := range pairs {
		if pair.prior.tombstone != nil && pair.next.tombstone != nil {
			equal, err := directCanonicalEqual(*pair.prior.tombstone, *pair.next.tombstone)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("%w: %s %s", ErrDirectEditTombstone, pair.key.Kind, pair.key.ID)
			}
		}
	}
	for _, pair := range pairs {
		if pair.prior.tombstone != nil && pair.next.tombstone == nil {
			return fmt.Errorf("%w: %s %s", ErrDirectResurrection, pair.key.Kind, pair.key.ID)
		}
	}
	for _, pair := range pairs {
		if pair.prior.tombstone != nil || pair.next.tombstone != nil || pair.prior.createdAt == nil || pair.next.createdAt == nil {
			continue
		}
		equal, err := directCanonicalEqual(pair.prior.createdAt, pair.next.createdAt)
		if err != nil {
			return err
		}
		if !equal {
			return fmt.Errorf("%w: %s %s changed created_at", ErrDirectImmutableFieldMutation, pair.key.Kind, pair.key.ID)
		}
	}
	return nil
}

type directMutablePair struct {
	key         state.RecordKey
	prior, next directRecord
}

type directMutableRecordSet struct {
	kind     string
	priorIDs map[string]struct{}
	nextIDs  map[string]struct{}
	prior    func(string) directRecord
	next     func(string) directRecord
}

func (records directMutableRecordSet) sharedIDs() []string {
	ids := make([]string, 0)
	for id := range records.priorIDs {
		if _, ok := records.nextIDs[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

type directRecord struct {
	live      any
	tombstone *state.TombstoneV1
	body      []byte
	createdAt any
}

func directValidateTombstone(kind, id string, prior directRecord, tombstone state.TombstoneV1) error {
	contentDigest, err := state.DigestCanonicalJSON(prior.live)
	if err != nil {
		return err
	}
	if tombstone.DeletedContentDigest != contentDigest {
		return fmt.Errorf("%w: %s %s content digest", state.ErrTombstoneDigest, kind, id)
	}
	if kind != "kb_article" {
		return nil
	}
	bodyDigest, err := state.DigestCanonicalMarkdown(prior.body)
	if err != nil {
		return err
	}
	if tombstone.DeletedBodyDigest == nil || *tombstone.DeletedBodyDigest != bodyDigest {
		return fmt.Errorf("%w: %s %s body digest", state.ErrTombstoneDigest, kind, id)
	}
	return nil
}

func directActorRecordSet(prior, next map[string]state.Record[state.ActorV1]) directMutableRecordSet {
	return directTypedRecordSet("actor", prior, next, func(value state.ActorV1) any { return value }, func(state.ActorV1) any { return nil })
}

func directTaskRecordSet(prior, next map[string]state.Record[state.TaskV1]) directMutableRecordSet {
	return directTypedRecordSet("task", prior, next, func(value state.TaskV1) any { return value }, func(value state.TaskV1) any { return value.CreatedAt })
}

func directTaskLinkRecordSet(prior, next map[string]state.Record[state.TaskLinkV1]) directMutableRecordSet {
	return directTypedRecordSet("task_link", prior, next, func(value state.TaskLinkV1) any { return value }, func(state.TaskLinkV1) any { return nil })
}

func directChannelRecordSet(prior, next map[string]state.Record[state.ChannelV1]) directMutableRecordSet {
	return directTypedRecordSet("channel", prior, next, func(value state.ChannelV1) any { return value }, func(value state.ChannelV1) any { return value.CreatedAt })
}

func directTypedRecordSet[T any](kind string, prior, next map[string]state.Record[T], live func(T) any, createdAt func(T) any) directMutableRecordSet {
	return directMutableRecordSet{
		kind: kind, priorIDs: directRecordIDs(prior), nextIDs: directRecordIDs(next),
		prior: func(id string) directRecord { return directTypedRecord(prior[id], live, createdAt) },
		next:  func(id string) directRecord { return directTypedRecord(next[id], live, createdAt) },
	}
}

func directRecordIDs[T any](records map[string]T) map[string]struct{} {
	ids := make(map[string]struct{}, len(records))
	for id := range records {
		ids[id] = struct{}{}
	}
	return ids
}

func directTypedRecord[T any](record state.Record[T], live func(T) any, createdAt func(T) any) directRecord {
	if record.Tombstone != nil {
		return directRecord{tombstone: record.Tombstone}
	}
	return directRecord{live: live(*record.Value), createdAt: createdAt(*record.Value)}
}

func directArticleRecordSet(prior, next map[string]state.KBRecord) directMutableRecordSet {
	return directMutableRecordSet{
		kind: "kb_article", priorIDs: directRecordIDs(prior), nextIDs: directRecordIDs(next),
		prior: func(id string) directRecord { return directArticleRecord(prior[id]) },
		next:  func(id string) directRecord { return directArticleRecord(next[id]) },
	}
}

func directArticleRecord(record state.KBRecord) directRecord {
	if record.Tombstone != nil {
		return directRecord{tombstone: record.Tombstone}
	}
	return directRecord{live: *record.Value, body: bytes.Clone(record.Body), createdAt: record.Value.CreatedAt}
}
