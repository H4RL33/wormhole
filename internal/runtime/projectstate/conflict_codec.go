package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var conflictExtensionKeyPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)+$`)

func encodeWorkspaceConflictEvidence(conflicts []Conflict) ([]localstore.WorkspaceConflictEvidence, error) {
	evidence := make([]localstore.WorkspaceConflictEvidence, 0, len(conflicts))
	var previous *Conflict
	for index := range conflicts {
		conflict, err := canonicalConflictForStorage(conflicts[index], true)
		if err != nil {
			return nil, fmt.Errorf("projectstate: encode conflict evidence %d: %w", index, err)
		}
		if previous != nil && compareConflicts(*previous, conflict) >= 0 {
			return nil, fmt.Errorf("projectstate: encode conflict evidence: conflicts are unordered or duplicated")
		}
		base, err := state.CanonicalJSON(conflict.Base)
		if err != nil {
			return nil, fmt.Errorf("projectstate: encode conflict base: %w", err)
		}
		ours, err := state.CanonicalJSON(conflict.Ours)
		if err != nil {
			return nil, fmt.Errorf("projectstate: encode conflict ours: %w", err)
		}
		theirs, err := state.CanonicalJSON(conflict.Theirs)
		if err != nil {
			return nil, fmt.Errorf("projectstate: encode conflict theirs: %w", err)
		}
		evidence = append(evidence, localstore.WorkspaceConflictEvidence{
			ConflictID: conflict.ID, Key: conflict.Key, FieldPath: conflict.FieldPath,
			ConflictKind: string(conflict.Kind), BaseJSON: string(base), OursJSON: string(ours), TheirsJSON: string(theirs),
		})
		previous = &conflict
	}
	return evidence, nil
}

func decodeWorkspaceConflictOccurrences(rows []localstore.WorkspaceConflictOccurrence) ([]Conflict, error) {
	conflicts := make([]Conflict, 0, len(rows))
	var previous *Conflict
	for index, row := range rows {
		if err := validateConflictOccurrenceMetadata(row); err != nil {
			return nil, fmt.Errorf("projectstate: decode conflict occurrence %d metadata: %w", index, err)
		}
		kind, err := decodeConflictKind(row.ConflictKind)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode conflict occurrence %d: %w", index, err)
		}
		if err := validateConflictShape(row.Key, row.FieldPath, kind); err != nil {
			return nil, fmt.Errorf("projectstate: decode conflict occurrence %d: %w", index, err)
		}
		base, err := decodeConflictFieldValue(row.Key, row.FieldPath, row.BaseJSON)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode conflict occurrence %d base: %w", index, err)
		}
		ours, err := decodeConflictFieldValue(row.Key, row.FieldPath, row.OursJSON)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode conflict occurrence %d ours: %w", index, err)
		}
		theirs, err := decodeConflictFieldValue(row.Key, row.FieldPath, row.TheirsJSON)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode conflict occurrence %d theirs: %w", index, err)
		}
		conflict, err := canonicalConflictForStorage(Conflict{
			ID: row.ConflictID, Key: row.Key, FieldPath: row.FieldPath, Kind: kind,
			Base: base, Ours: ours, Theirs: theirs,
		}, true)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode conflict occurrence %d: %w", index, err)
		}
		if previous != nil && compareConflicts(*previous, conflict) >= 0 {
			return nil, fmt.Errorf("projectstate: decode conflict occurrences: rows are unordered or duplicated")
		}
		conflicts = append(conflicts, conflict)
		previous = &conflicts[len(conflicts)-1]
	}
	return conflicts, nil
}

func canonicalConflictForStorage(conflict Conflict, requireTypedRootIdentity bool) (Conflict, error) {
	if err := validateConflictShape(conflict.Key, conflict.FieldPath, conflict.Kind); err != nil {
		return Conflict{}, err
	}
	values := []*FieldValue{&conflict.Base, &conflict.Ours, &conflict.Theirs}
	for _, value := range values {
		var canonical FieldValue
		var err error
		if conflict.FieldPath == "" {
			canonical, err = rehydrateConflictRoot(conflict.Key, *value)
			if err == nil && requireTypedRootIdentity && value.Present && !bytes.Equal(canonical.Value, value.Value) {
				err = fmt.Errorf("root is not canonical concrete JSON")
			}
		} else {
			canonical, err = cloneCanonicalFieldValue(*value)
		}
		if err != nil {
			return Conflict{}, err
		}
		*value = canonical
	}
	if conflict.Key.Kind == "project" && (!conflict.Base.Present || !conflict.Ours.Present || !conflict.Theirs.Present) {
		return Conflict{}, fmt.Errorf("project conflict evidence cannot be absent")
	}
	wantID, err := conflictID(conflict)
	if err != nil {
		return Conflict{}, err
	}
	if conflict.ID != wantID {
		return Conflict{}, fmt.Errorf("semantic conflict ID mismatch")
	}
	return conflict, nil
}

func decodeConflictFieldValue(key state.RecordKey, path, encoded string) (FieldValue, error) {
	if encoded == "" || !utf8.ValidString(encoded) {
		return FieldValue{}, fmt.Errorf("invalid conflict evidence bytes")
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value FieldValue
	if err := decoder.Decode(&value); err != nil {
		return FieldValue{}, fmt.Errorf("strict field envelope: %w", err)
	}
	if err := requireConflictJSONEOF(decoder); err != nil {
		return FieldValue{}, err
	}
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return FieldValue{}, err
	}
	if !bytes.Equal(canonical, []byte(encoded)) {
		return FieldValue{}, fmt.Errorf("field envelope is not canonical JSON")
	}
	if path == "" {
		rehydrated, err := rehydrateConflictRoot(key, value)
		if err != nil {
			return FieldValue{}, err
		}
		typedEnvelope, err := state.CanonicalJSON(rehydrated)
		if err != nil {
			return FieldValue{}, err
		}
		if !bytes.Equal(typedEnvelope, []byte(encoded)) {
			return FieldValue{}, fmt.Errorf("conflict root is not exact canonical concrete JSON")
		}
		return rehydrated, nil
	}
	return cloneCanonicalFieldValue(value)
}

func rehydrateConflictRoot(key state.RecordKey, root FieldValue) (FieldValue, error) {
	if !root.Present {
		if root.Value != nil {
			return FieldValue{}, fmt.Errorf("absent conflict root has a value")
		}
		return FieldValue{}, nil
	}
	if len(root.Value) == 0 {
		return FieldValue{}, fmt.Errorf("present conflict root has no value")
	}
	decoder := json.NewDecoder(bytes.NewReader(root.Value))
	decoder.UseNumber()
	var object any
	if err := decoder.Decode(&object); err != nil {
		return FieldValue{}, fmt.Errorf("decode conflict root: %w", err)
	}
	if err := requireConflictJSONEOF(decoder); err != nil {
		return FieldValue{}, err
	}
	fields, ok := object.(map[string]any)
	if !ok {
		return FieldValue{}, fmt.Errorf("present conflict root must be an object")
	}
	kind, ok := fields["kind"].(string)
	if !ok {
		return FieldValue{}, fmt.Errorf("conflict root has no string kind")
	}
	if kind == "tombstone" {
		value, err := decodeStrictMergeValue[state.TombstoneV1](root)
		if err != nil {
			return FieldValue{}, err
		}
		if err := validateConflictTombstone(key, value); err != nil {
			return FieldValue{}, err
		}
		raw, err := canonicalFieldJSON(value)
		return FieldValue{Present: true, Value: raw}, err
	}
	switch key.Kind {
	case "project":
		value, err := decodeStrictMergeValue[state.ProjectV1](root)
		return canonicalTypedLiveField(key, value, err)
	case "actor":
		value, err := decodeStrictMergeValue[state.ActorV1](root)
		return canonicalTypedLiveField(key, value, err)
	case "task":
		value, err := decodeStrictMergeValue[state.TaskV1](root)
		return canonicalTypedLiveField(key, value, err)
	case "task_link":
		value, err := decodeStrictMergeValue[state.TaskLinkV1](root)
		return canonicalTypedLiveField(key, value, err)
	case "kb_article":
		value, err := decodeStrictMergeValue[state.KBArticleV1](root)
		return canonicalTypedLiveField(key, value, err)
	case "channel":
		value, err := decodeStrictMergeValue[state.ChannelV1](root)
		return canonicalTypedLiveField(key, value, err)
	case "event":
		value, err := decodeStrictMergeValue[state.EventV1](root)
		return canonicalImmutableConflictRoot(key, value.SchemaVersion, value.Kind, value.ID, value, err)
	case "git_link":
		value, err := decodeStrictMergeValue[state.GitLinkV1](root)
		return canonicalImmutableConflictRoot(key, value.SchemaVersion, value.Kind, value.ID, value, err)
	default:
		return FieldValue{}, fmt.Errorf("unsupported conflict root kind %q", key.Kind)
	}
}

func canonicalImmutableConflictRoot(key state.RecordKey, version int, kind, id string, value any, decodeErr error) (FieldValue, error) {
	if err := validateMergeRecordHeader(key, version, kind, id, decodeErr); err != nil {
		return FieldValue{}, err
	}
	raw, err := canonicalFieldJSON(value)
	if err != nil {
		return FieldValue{}, err
	}
	return FieldValue{Present: true, Value: raw}, nil
}

func validateConflictTombstone(key state.RecordKey, tombstone state.TombstoneV1) error {
	switch key.Kind {
	case "actor", "task", "task_link", "kb_article", "channel":
	default:
		return fmt.Errorf("tombstone is not permitted for %s", key.Kind)
	}
	if tombstone.SchemaVersion != 1 || tombstone.Kind != "tombstone" || tombstone.ID != key.ID || tombstone.EntityKind != key.Kind {
		return fmt.Errorf("tombstone header does not match %s %s", key.Kind, key.ID)
	}
	if !validConflictDigest(string(tombstone.DeletedContentDigest)) ||
		(tombstone.DeletedBodyDigest != nil && !validConflictDigest(string(*tombstone.DeletedBodyDigest))) {
		return fmt.Errorf("invalid tombstone digest")
	}
	if (key.Kind == "kb_article") != (tombstone.DeletedBodyDigest != nil) {
		return fmt.Errorf("invalid tombstone body digest for %s", key.Kind)
	}
	if tombstone.DeletedAt.IsZero() || !zeroOffsetTime(tombstone.DeletedAt) {
		return fmt.Errorf("invalid tombstone deletion timestamp")
	}
	if err := tombstone.DeletedBy.ValidateHistorical(); err != nil {
		return fmt.Errorf("invalid tombstone actor: %w", err)
	}
	if tombstone.Extensions == nil {
		return fmt.Errorf("tombstone extensions must be an object")
	}
	return nil
}

func validateConflictShape(key state.RecordKey, path string, kind ConflictKind) error {
	if !validConflictRecordKey(key) {
		return fmt.Errorf("invalid conflict record key")
	}
	if !validConflictPointer(path) {
		return fmt.Errorf("invalid conflict field path")
	}
	switch kind {
	case ConflictMarkdown:
		if key.Kind != "kb_article" || path != "/body" {
			return fmt.Errorf("markdown conflict requires kb_article /body")
		}
	case ConflictImmutableRecord:
		if (key.Kind != "event" && key.Kind != "git_link") || path != "" {
			return fmt.Errorf("immutable record conflict requires an immutable root")
		}
	case ConflictTombstoneEdit:
		if !mutableConflictKind(key.Kind) || path != "" {
			return fmt.Errorf("tombstone edit conflict requires a mutable root")
		}
	case ConflictTombstoneBody:
		if key.Kind != "kb_article" || path != "/body" {
			return fmt.Errorf("tombstone body conflict requires kb_article /body")
		}
	case ConflictInvalidResurrection:
		if !mutableConflictKind(key.Kind) || (path != "" && !(key.Kind == "kb_article" && path == "/body")) {
			return fmt.Errorf("invalid resurrection conflict requires a mutable root or kb_article /body")
		}
	case ConflictSameField:
		if !validSameFieldConflictPath(key.Kind, path) {
			return fmt.Errorf("same-field conflict path cannot be produced for %s %s", key.Kind, path)
		}
	default:
		return fmt.Errorf("unknown conflict kind %q", kind)
	}
	return nil
}

func validConflictRecordKey(key state.RecordKey) bool {
	switch key.Kind {
	case "project", "actor", "task", "task_link", "kb_article", "channel", "event", "git_link":
		return types.CanonicalUUID(key.ID)
	default:
		return false
	}
}

func mutableConflictKind(kind string) bool {
	switch kind {
	case "actor", "task", "task_link", "kb_article", "channel":
		return true
	default:
		return false
	}
}

func validSameFieldConflictPath(kind, path string) bool {
	if path == "" {
		return mutableConflictKind(kind)
	}
	exact := map[string]map[string]struct{}{
		"project":    {"/name": {}, "/aliases": {}},
		"actor":      {"/actor_kind": {}, "/display_name": {}, "/public_keys": {}},
		"task":       {"/parent_task_id": {}, "/title": {}, "/description": {}, "/owner_actor_id": {}, "/status": {}, "/priority": {}, "/due_by": {}},
		"task_link":  {"/task_id": {}, "/link_type": {}, "/target_id": {}},
		"kb_article": {"/title": {}, "/author_actor_id": {}, "/related_article_ids": {}, "/body": {}},
		"channel":    {"/name": {}},
	}
	if _, ok := exact[kind][path]; ok {
		return true
	}
	if strings.HasPrefix(path, "/extensions/") {
		if kind != "project" && !mutableConflictKind(kind) {
			return false
		}
		remainder := strings.TrimPrefix(path, "/extensions/")
		slash := strings.IndexByte(remainder, '/')
		if slash < 0 {
			return conflictExtensionKeyPattern.MatchString(remainder)
		}
		return conflictExtensionKeyPattern.MatchString(remainder[:slash]) && strings.HasPrefix(remainder[slash:], "/data/")
	}
	if kind != "kb_article" || !strings.HasPrefix(path, "/frontmatter/") {
		return false
	}
	remainder := strings.TrimPrefix(path, "/frontmatter/")
	if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
		remainder = remainder[:slash]
	}
	token := strings.ReplaceAll(strings.ReplaceAll(remainder, "~1", "/"), "~0", "~")
	return token != "" && strings.TrimSpace(token) == token && !strings.ContainsRune(token, 0)
}

func validConflictPointer(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) || value[0] != '/' {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] == '~' {
			if index+1 >= len(value) || (value[index+1] != '0' && value[index+1] != '1') {
				return false
			}
			index++
		}
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func decodeConflictKind(value string) (ConflictKind, error) {
	kind := ConflictKind(value)
	switch kind {
	case ConflictSameField, ConflictMarkdown, ConflictImmutableRecord, ConflictTombstoneEdit, ConflictTombstoneBody, ConflictInvalidResurrection:
		return kind, nil
	default:
		return "", fmt.Errorf("unknown conflict kind %q", value)
	}
}

func compareConflicts(left, right Conflict) int {
	if leftRank, rightRank := diffKindRank(left.Key.Kind), diffKindRank(right.Key.Kind); leftRank != rightRank {
		return leftRank - rightRank
	}
	for _, pair := range [][2]string{{left.Key.ID, right.Key.ID}, {left.FieldPath, right.FieldPath}, {string(left.Kind), string(right.Kind)}, {left.ID, right.ID}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func validateConflictOccurrenceMetadata(row localstore.WorkspaceConflictOccurrence) error {
	if row.OccurrenceID != row.ConflictID && !canonicalUUIDv4(row.OccurrenceID) {
		return fmt.Errorf("invalid conflict occurrence ID")
	}
	if row.CreatedAt.IsZero() || !zeroOffsetTime(row.CreatedAt) {
		return fmt.Errorf("invalid conflict creation timestamp")
	}
	return nil
}

func canonicalUUIDv4(value string) bool {
	if !types.CanonicalUUID(value) || value[14] != '4' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

func zeroOffsetTime(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}

func validConflictDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func requireConflictJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple conflict JSON values")
		}
		return fmt.Errorf("trailing conflict JSON: %w", err)
	}
	return nil
}
