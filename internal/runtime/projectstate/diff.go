package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrRawRecordDeletion = errors.New("projectstate: raw record deletion")

type ChangeKind string

const (
	ChangeAdd       ChangeKind = "add"
	ChangeModify    ChangeKind = "modify"
	ChangeTombstone ChangeKind = "tombstone"
	ChangeResurrect ChangeKind = "resurrect"
)

type FieldValue struct {
	Present bool            `json:"present"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type FieldChange struct {
	Path          string
	Before, After FieldValue
}

type Change struct {
	Key                               state.RecordKey
	Kind                              ChangeKind
	BeforeDigest, AfterDigest         *state.Digest
	BeforeBodyDigest, AfterBodyDigest *state.Digest
	Fields                            []FieldChange
	Actor                             *types.ActorEnvelope
}

type Diff struct {
	BaseDigest state.Digest
	ViewDigest state.Digest
	Changes    []Change
}

type diffRecord struct {
	raw         json.RawMessage
	decoded     any
	digest      state.Digest
	tombstone   bool
	bodyPresent bool
	bodyValue   FieldValue
	bodyDigest  state.Digest
}

func SemanticDiff(base, view state.Snapshot, actors map[state.RecordKey]types.ActorEnvelope) (Diff, error) {
	base, err := validatedDiffSnapshot(base)
	if err != nil {
		return Diff{}, fmt.Errorf("projectstate: semantic diff base: %w", err)
	}
	if key, deleted := rawMutableDeletion(base, view); deleted {
		return Diff{}, fmt.Errorf("%w: %s %s", ErrRawRecordDeletion, key.Kind, key.ID)
	}
	view, err = validatedDiffSnapshot(view)
	if err != nil {
		return Diff{}, fmt.Errorf("projectstate: semantic diff view: %w", err)
	}
	if base.Config.SnapshotVersion != view.Config.SnapshotVersion ||
		base.Config.ProjectID != view.Config.ProjectID ||
		base.Config.Repository != view.Config.Repository {
		return Diff{}, fmt.Errorf("projectstate: semantic diff binding mismatch")
	}

	baseRecords, err := diffRecords(base)
	if err != nil {
		return Diff{}, err
	}
	viewRecords, err := diffRecords(view)
	if err != nil {
		return Diff{}, err
	}
	keys := diffRecordKeys(baseRecords, viewRecords)
	result := Diff{BaseDigest: base.Digest, ViewDigest: view.Digest, Changes: make([]Change, 0)}
	for _, key := range keys {
		before, beforePresent := baseRecords[key]
		after, afterPresent := viewRecords[key]
		if beforePresent && !afterPresent && key.Kind != "event" && key.Kind != "git_link" {
			return Diff{}, fmt.Errorf("%w: %s %s", ErrRawRecordDeletion, key.Kind, key.ID)
		}
		change, changed, err := diffOneRecord(key, before, beforePresent, after, afterPresent)
		if err != nil {
			return Diff{}, err
		}
		if !changed {
			continue
		}
		if actor, ok := actors[key]; ok {
			actorCopy := actor
			change.Actor = &actorCopy
		}
		result.Changes = append(result.Changes, change)
	}
	return result, nil
}

func rawMutableDeletion(base, rawView state.Snapshot) (state.RecordKey, bool) {
	for _, records := range []struct {
		kind    string
		missing func() (string, bool)
	}{
		{kind: "actor", missing: func() (string, bool) { return firstMissingRecordKey(base.Actors, rawView.Actors) }},
		{kind: "task", missing: func() (string, bool) { return firstMissingRecordKey(base.Tasks, rawView.Tasks) }},
		{kind: "task_link", missing: func() (string, bool) { return firstMissingRecordKey(base.TaskLinks, rawView.TaskLinks) }},
		{kind: "kb_article", missing: func() (string, bool) { return firstMissingRecordKey(base.Articles, rawView.Articles) }},
		{kind: "channel", missing: func() (string, bool) { return firstMissingRecordKey(base.Channels, rawView.Channels) }},
	} {
		if id, missing := records.missing(); missing {
			return state.RecordKey{Kind: records.kind, ID: id}, true
		}
	}
	return state.RecordKey{}, false
}

func firstMissingRecordKey[T any](base, view map[string]T) (string, bool) {
	missing := make([]string, 0)
	for id := range base {
		if _, ok := view[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return "", false
	}
	sort.Strings(missing)
	return missing[0], true
}

func validatedDiffSnapshot(snapshot state.Snapshot) (state.Snapshot, error) {
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		return state.Snapshot{}, err
	}
	canonical, err := state.DecodeTree(tree)
	if err != nil {
		return state.Snapshot{}, err
	}
	if canonical.Digest != snapshot.Digest {
		return state.Snapshot{}, fmt.Errorf("snapshot digest mismatch")
	}
	return canonical, nil
}

func diffRecords(snapshot state.Snapshot) (map[state.RecordKey]diffRecord, error) {
	records := make(map[state.RecordKey]diffRecord, 1+len(snapshot.Actors)+len(snapshot.Tasks)+len(snapshot.TaskLinks)+len(snapshot.Articles)+len(snapshot.Channels)+len(snapshot.Events)+len(snapshot.GitLinks))
	if err := addDiffValue(records, state.RecordKey{Kind: "project", ID: snapshot.Project.ID}, snapshot.Project, false, nil); err != nil {
		return nil, err
	}
	if err := addDiffRecordMap(records, "actor", snapshot.Actors); err != nil {
		return nil, err
	}
	if err := addDiffRecordMap(records, "task", snapshot.Tasks); err != nil {
		return nil, err
	}
	if err := addDiffRecordMap(records, "task_link", snapshot.TaskLinks); err != nil {
		return nil, err
	}
	for id, article := range snapshot.Articles {
		key := state.RecordKey{Kind: "kb_article", ID: id}
		switch {
		case article.Value != nil:
			if err := addDiffValue(records, key, *article.Value, false, article.Body); err != nil {
				return nil, err
			}
		case article.Tombstone != nil:
			if err := addDiffValue(records, key, *article.Tombstone, true, nil); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("projectstate: semantic diff invalid empty KB record %s", id)
		}
	}
	if err := addDiffRecordMap(records, "channel", snapshot.Channels); err != nil {
		return nil, err
	}
	for id, event := range snapshot.Events {
		if err := addDiffValue(records, state.RecordKey{Kind: "event", ID: id}, event, false, nil); err != nil {
			return nil, err
		}
	}
	if err := addDiffRecordMap(records, "git_link", snapshot.GitLinks); err != nil {
		return nil, err
	}
	return records, nil
}

func addDiffRecordMap[T any](destination map[state.RecordKey]diffRecord, kind string, records map[string]state.Record[T]) error {
	for id, record := range records {
		key := state.RecordKey{Kind: kind, ID: id}
		switch {
		case record.Value != nil:
			if err := addDiffValue(destination, key, *record.Value, false, nil); err != nil {
				return err
			}
		case record.Tombstone != nil:
			if err := addDiffValue(destination, key, *record.Tombstone, true, nil); err != nil {
				return err
			}
		default:
			return fmt.Errorf("projectstate: semantic diff invalid empty %s record %s", kind, id)
		}
	}
	return nil
}

func addDiffValue(destination map[state.RecordKey]diffRecord, key state.RecordKey, value any, tombstone bool, body []byte) error {
	raw, err := canonicalFieldJSON(value)
	if err != nil {
		return err
	}
	decoded, err := decodeDiffJSON(raw)
	if err != nil {
		return err
	}
	digest, err := state.DigestCanonicalJSON(value)
	if err != nil {
		return err
	}
	record := diffRecord{raw: raw, decoded: decoded, digest: digest, tombstone: tombstone}
	if body != nil {
		canonicalBody, err := state.CanonicalMarkdown(body)
		if err != nil {
			return err
		}
		record.bodyPresent = true
		record.bodyValue, err = presentFieldValue(string(canonicalBody))
		if err != nil {
			return err
		}
		record.bodyDigest, err = state.DigestCanonicalMarkdown(canonicalBody)
		if err != nil {
			return err
		}
	}
	destination[key] = record
	return nil
}

func diffRecordKeys(left, right map[state.RecordKey]diffRecord) []state.RecordKey {
	set := make(map[state.RecordKey]struct{}, len(left)+len(right))
	for key := range left {
		set[key] = struct{}{}
	}
	for key := range right {
		set[key] = struct{}{}
	}
	keys := make([]state.RecordKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		leftRank, rightRank := diffKindRank(keys[i].Kind), diffKindRank(keys[j].Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if keys[i].ID != keys[j].ID {
			return keys[i].ID < keys[j].ID
		}
		return keys[i].Kind < keys[j].Kind
	})
	return keys
}

func diffKindRank(kind string) int {
	switch kind {
	case "project":
		return 0
	case "actor":
		return 1
	case "task":
		return 2
	case "task_link":
		return 3
	case "kb_article":
		return 4
	case "channel":
		return 5
	case "event":
		return 6
	case "git_link":
		return 7
	default:
		return 8
	}
}

func diffOneRecord(key state.RecordKey, before diffRecord, beforePresent bool, after diffRecord, afterPresent bool) (Change, bool, error) {
	change := Change{Key: key, Fields: make([]FieldChange, 0)}
	switch {
	case !beforePresent && afterPresent && after.tombstone:
		change.Kind = ChangeTombstone
	case !beforePresent && afterPresent:
		change.Kind = ChangeAdd
	case beforePresent && !afterPresent:
		change.Kind = ChangeModify
	case before.tombstone && !after.tombstone:
		change.Kind = ChangeResurrect
	case !before.tombstone && after.tombstone:
		change.Kind = ChangeTombstone
	default:
		change.Kind = ChangeModify
	}
	if beforePresent {
		change.BeforeDigest = digestCopy(before.digest)
	}
	if afterPresent {
		change.AfterDigest = digestCopy(after.digest)
	}
	if beforePresent && before.bodyPresent {
		change.BeforeBodyDigest = digestCopy(before.bodyDigest)
	}
	if afterPresent && after.bodyPresent {
		change.AfterBodyDigest = digestCopy(after.bodyDigest)
	}

	lifecycle := !beforePresent || !afterPresent || before.tombstone != after.tombstone
	if lifecycle {
		change.Fields = append(change.Fields, FieldChange{
			Path: "", Before: recordFieldValue(before, beforePresent), After: recordFieldValue(after, afterPresent),
		})
	} else {
		var err error
		change.Fields, _, err = diffJSONFields("", before.decoded, after.decoded)
		if err != nil {
			return Change{}, false, err
		}
	}
	if before.bodyPresent != after.bodyPresent || (before.bodyPresent && !bytes.Equal(before.bodyValue.Value, after.bodyValue.Value)) {
		change.Fields = append(change.Fields, FieldChange{
			Path: "/body", Before: optionalFieldValue(before.bodyValue, before.bodyPresent), After: optionalFieldValue(after.bodyValue, after.bodyPresent),
		})
	}
	sort.Slice(change.Fields, func(i, j int) bool { return change.Fields[i].Path < change.Fields[j].Path })
	return change, len(change.Fields) > 0, nil
}

func diffJSONFields(path string, before, after any) ([]FieldChange, bool, error) {
	beforeObject, beforeIsObject := before.(map[string]any)
	afterObject, afterIsObject := after.(map[string]any)
	if beforeIsObject && afterIsObject {
		keys := make([]string, 0, len(beforeObject)+len(afterObject))
		seen := make(map[string]struct{}, len(beforeObject)+len(afterObject))
		for key := range beforeObject {
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
		for key := range afterObject {
			if _, ok := seen[key]; !ok {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		changes := make([]FieldChange, 0)
		for _, key := range keys {
			childPath := path + "/" + escapeJSONPointerToken(key)
			if path == "" && key == "updated_at" {
				continue
			}
			beforeChild, beforePresent := beforeObject[key]
			afterChild, afterPresent := afterObject[key]
			if beforePresent && afterPresent {
				children, changed, err := diffJSONFields(childPath, beforeChild, afterChild)
				if err != nil {
					return nil, false, err
				}
				if changed {
					changes = append(changes, children...)
				}
				continue
			}
			beforeValue, err := diffFieldValue(beforeChild, beforePresent)
			if err != nil {
				return nil, false, err
			}
			afterValue, err := diffFieldValue(afterChild, afterPresent)
			if err != nil {
				return nil, false, err
			}
			changes = append(changes, FieldChange{Path: childPath, Before: beforeValue, After: afterValue})
		}
		return changes, len(changes) > 0, nil
	}
	beforeValue, err := diffFieldValue(before, true)
	if err != nil {
		return nil, false, err
	}
	afterValue, err := diffFieldValue(after, true)
	if err != nil {
		return nil, false, err
	}
	if bytes.Equal(beforeValue.Value, afterValue.Value) {
		return nil, false, nil
	}
	return []FieldChange{{Path: path, Before: beforeValue, After: afterValue}}, true, nil
}

func recordFieldValue(record diffRecord, present bool) FieldValue {
	if !present {
		return FieldValue{}
	}
	return FieldValue{Present: true, Value: bytes.Clone(record.raw)}
}

func optionalFieldValue(value FieldValue, present bool) FieldValue {
	if !present {
		return FieldValue{}
	}
	return FieldValue{Present: true, Value: bytes.Clone(value.Value)}
}

func diffFieldValue(value any, present bool) (FieldValue, error) {
	if !present {
		return FieldValue{}, nil
	}
	raw, err := canonicalFieldJSON(value)
	if err != nil {
		return FieldValue{}, err
	}
	return FieldValue{Present: true, Value: raw}, nil
}

func presentFieldValue(value any) (FieldValue, error) {
	raw, err := canonicalFieldJSON(value)
	if err != nil {
		return FieldValue{}, err
	}
	return FieldValue{Present: true, Value: raw}, nil
}

func canonicalFieldJSON(value any) (json.RawMessage, error) {
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 || canonical[len(canonical)-1] != '\n' {
		return nil, fmt.Errorf("projectstate: canonical JSON missing final newline")
	}
	return bytes.Clone(canonical[:len(canonical)-1]), nil
}

func decodeDiffJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("projectstate: trailing diff JSON")
		}
		return nil, err
	}
	return value, nil
}

func escapeJSONPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func digestCopy(digest state.Digest) *state.Digest {
	copy := digest
	return &copy
}
