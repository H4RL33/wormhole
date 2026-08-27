package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	shared "github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type recordEndpointState uint8

const (
	recordEndpointAbsent recordEndpointState = iota
	recordEndpointLive
	recordEndpointTombstone
)

type recordSurfaceClass uint8

const (
	recordSurfaceProject recordSurfaceClass = iota + 1
	recordSurfaceMutable
	recordSurfaceImmutable
)

type recordSurface struct {
	State recordEndpointState
	Root  FieldValue
	Body  FieldValue
}

type recordSurfaceMergeResult struct {
	Surface   recordSurface
	Conflicts []Conflict
}

func snapshotRecordSurfaces(snapshot state.Snapshot) (map[state.RecordKey]recordSurface, error) {
	canonical, err := validatedDiffSnapshot(snapshot)
	if err != nil {
		return nil, fmt.Errorf("projectstate: record surfaces: %w", err)
	}
	return snapshotRecordSurfacesFromValidated(canonical)
}

func snapshotRecordSurfacesFromValidated(snapshot state.Snapshot) (map[state.RecordKey]recordSurface, error) {
	records, err := diffRecords(snapshot)
	if err != nil {
		return nil, err
	}
	surfaces := make(map[state.RecordKey]recordSurface, len(records))
	for key, record := range records {
		surface := recordSurface{State: recordEndpointLive, Root: recordFieldValue(record, true)}
		if record.tombstone {
			surface.State = recordEndpointTombstone
		}
		if record.bodyPresent {
			surface.Body = optionalFieldValue(record.bodyValue, true)
		}
		if err := validateRecordSurface(key, surface); err != nil {
			return nil, err
		}
		surfaces[key] = surface
	}
	return surfaces, nil
}

func recordSurfaceAt(surfaces map[state.RecordKey]recordSurface, key state.RecordKey) (recordSurface, error) {
	surface, present := surfaces[key]
	if !present {
		surface = recordSurface{State: recordEndpointAbsent}
	}
	if err := validateRecordSurface(key, surface); err != nil {
		return recordSurface{}, err
	}
	return cloneRecordSurface(surface)
}

func recordSurfaceKeys(surfaceMaps ...map[state.RecordKey]recordSurface) ([]state.RecordKey, error) {
	set := make(map[state.RecordKey]struct{})
	for _, surfaces := range surfaceMaps {
		for key := range surfaces {
			if _, err := recordSurfaceClassForKind(key.Kind); err != nil {
				return nil, err
			}
			if !shared.CanonicalUUID(key.ID) {
				return nil, fmt.Errorf("projectstate: invalid %s surface ID %q", key.Kind, key.ID)
			}
			set[key] = struct{}{}
		}
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
	return keys, nil
}

func recordSurfacesEqual(key state.RecordKey, left, right recordSurface) (bool, error) {
	if err := validateRecordSurface(key, left); err != nil {
		return false, fmt.Errorf("projectstate: left record surface: %w", err)
	}
	if err := validateRecordSurface(key, right); err != nil {
		return false, fmt.Errorf("projectstate: right record surface: %w", err)
	}
	return left.State == right.State && fieldValuesEqual(left.Root, right.Root) && fieldValuesEqual(left.Body, right.Body), nil
}

func mergeExistingTypedLiveRecord(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	if _, err := existingLiveRecordSpec(key.Kind); err != nil {
		return recordSurfaceMergeResult{}, err
	}
	baseLive, err := normalizeTypedLiveSurface("base", key, base)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	oursLive, err := normalizeTypedLiveSurface("ours", key, ours)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	theirsLive, err := normalizeTypedLiveSurface("theirs", key, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	merged, err := mergeExistingLiveRecord(key, baseLive, oursLive, theirsLive)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	typedRoot, err := rehydrateTypedLiveRoot(key, merged.Surface.Root)
	if err != nil {
		return recordSurfaceMergeResult{}, fmt.Errorf("projectstate: existing typed-live result root: %w", err)
	}
	body, err := cloneCanonicalFieldValue(merged.Surface.Body)
	if err != nil {
		return recordSurfaceMergeResult{}, fmt.Errorf("projectstate: existing typed-live result body: %w", err)
	}
	result := recordSurfaceMergeResult{
		Surface:   recordSurface{State: recordEndpointLive, Root: typedRoot, Body: body},
		Conflicts: merged.Conflicts,
	}
	if err := validateRecordSurface(key, result.Surface); err != nil {
		return recordSurfaceMergeResult{}, fmt.Errorf("projectstate: existing typed-live result: %w", err)
	}
	return result, nil
}

func normalizeTypedLiveSurface(name string, key state.RecordKey, surface recordSurface) (liveRecordSurface, error) {
	if err := validateRecordSurface(key, surface); err != nil {
		return liveRecordSurface{}, fmt.Errorf("projectstate: existing typed-live %s: %w", name, err)
	}
	if surface.State != recordEndpointLive {
		return liveRecordSurface{}, fmt.Errorf("projectstate: existing typed-live %s endpoint must be live", name)
	}
	root, err := normalizeTypedLiveRoot(key, surface.Root)
	if err != nil {
		return liveRecordSurface{}, fmt.Errorf("projectstate: existing typed-live %s root: %w", name, err)
	}
	cloned, err := cloneRecordSurface(surface)
	if err != nil {
		return liveRecordSurface{}, err
	}
	return liveRecordSurface{Root: root, Body: cloned.Body}, nil
}

func normalizeTypedLiveRoot(key state.RecordKey, root FieldValue) (FieldValue, error) {
	if _, err := existingLiveRecordSpec(key.Kind); err != nil {
		return FieldValue{}, err
	}
	if err := validateCanonicalTypedLiveRoot(key, root); err != nil {
		return FieldValue{}, err
	}
	decoded, err := decodeDiffJSON(root.Value)
	if err != nil {
		return FieldValue{}, fmt.Errorf("projectstate: normalize typed-live root: %w", err)
	}
	if _, ok := decoded.(map[string]any); !ok {
		return FieldValue{}, fmt.Errorf("projectstate: typed-live root must be an object")
	}
	raw, err := canonicalFieldJSON(decoded)
	if err != nil {
		return FieldValue{}, err
	}
	return FieldValue{Present: true, Value: raw}, nil
}

func rehydrateTypedLiveRoot(key state.RecordKey, root FieldValue) (FieldValue, error) {
	if _, err := existingLiveRecordSpec(key.Kind); err != nil {
		return FieldValue{}, err
	}
	slot, err := decodeMergeJSONSlot(root)
	if err != nil {
		return FieldValue{}, fmt.Errorf("projectstate: rehydrate typed-live root: %w", err)
	}
	if _, ok := mergeJSONObject(slot); !ok {
		return FieldValue{}, fmt.Errorf("projectstate: rehydrate typed-live root must be a present object")
	}
	switch key.Kind {
	case "project":
		value, decodeErr := decodeStrictMergeValue[state.ProjectV1](root)
		return canonicalTypedLiveField(key, value, decodeErr)
	case "actor":
		value, decodeErr := decodeStrictMergeValue[state.ActorV1](root)
		return canonicalTypedLiveField(key, value, decodeErr)
	case "task":
		value, decodeErr := decodeStrictMergeValue[state.TaskV1](root)
		return canonicalTypedLiveField(key, value, decodeErr)
	case "task_link":
		value, decodeErr := decodeStrictMergeValue[state.TaskLinkV1](root)
		return canonicalTypedLiveField(key, value, decodeErr)
	case "kb_article":
		value, decodeErr := decodeStrictMergeValue[state.KBArticleV1](root)
		return canonicalTypedLiveField(key, value, decodeErr)
	case "channel":
		value, decodeErr := decodeStrictMergeValue[state.ChannelV1](root)
		return canonicalTypedLiveField(key, value, decodeErr)
	default:
		return FieldValue{}, fmt.Errorf("projectstate: existing typed-live merge unsupported kind %q", key.Kind)
	}
}

func validateCanonicalTypedLiveRoot(key state.RecordKey, root FieldValue) error {
	switch key.Kind {
	case "project":
		value, err := decodeCanonicalMergeValue[state.ProjectV1](root)
		_, err = canonicalTypedLiveField(key, value, err)
		return err
	case "actor":
		value, err := decodeCanonicalMergeValue[state.ActorV1](root)
		_, err = canonicalTypedLiveField(key, value, err)
		return err
	case "task":
		value, err := decodeCanonicalMergeValue[state.TaskV1](root)
		_, err = canonicalTypedLiveField(key, value, err)
		return err
	case "task_link":
		value, err := decodeCanonicalMergeValue[state.TaskLinkV1](root)
		_, err = canonicalTypedLiveField(key, value, err)
		return err
	case "kb_article":
		value, err := decodeCanonicalMergeValue[state.KBArticleV1](root)
		_, err = canonicalTypedLiveField(key, value, err)
		return err
	case "channel":
		value, err := decodeCanonicalMergeValue[state.ChannelV1](root)
		_, err = canonicalTypedLiveField(key, value, err)
		return err
	default:
		return fmt.Errorf("projectstate: existing typed-live merge unsupported kind %q", key.Kind)
	}
}

func canonicalTypedLiveField(key state.RecordKey, value any, decodeErr error) (FieldValue, error) {
	if decodeErr != nil {
		return FieldValue{}, decodeErr
	}
	var version int
	var kind, id string
	switch typed := value.(type) {
	case state.ProjectV1:
		version, kind, id = typed.SchemaVersion, typed.Kind, typed.ID
	case state.ActorV1:
		version, kind, id = typed.SchemaVersion, typed.Kind, typed.ID
	case state.TaskV1:
		version, kind, id = typed.SchemaVersion, typed.Kind, typed.ID
	case state.TaskLinkV1:
		version, kind, id = typed.SchemaVersion, typed.Kind, typed.ID
	case state.KBArticleV1:
		version, kind, id = typed.SchemaVersion, typed.Kind, typed.ID
	case state.ChannelV1:
		version, kind, id = typed.SchemaVersion, typed.Kind, typed.ID
	default:
		return FieldValue{}, fmt.Errorf("projectstate: unsupported concrete typed-live root %T", value)
	}
	if err := validateMergeRecordHeader(key, version, kind, id, nil); err != nil {
		return FieldValue{}, err
	}
	raw, err := canonicalFieldJSON(value)
	if err != nil {
		return FieldValue{}, err
	}
	return FieldValue{Present: true, Value: raw}, nil
}

func decodeStrictMergeValue[T any](root FieldValue) (T, error) {
	var zero T
	if !root.Present {
		if root.Value != nil {
			return zero, fmt.Errorf("projectstate: absent strict merge record root has a value")
		}
		return zero, fmt.Errorf("projectstate: strict merge record root must be present")
	}
	if len(root.Value) == 0 {
		return zero, fmt.Errorf("projectstate: present strict merge record root has no value")
	}
	decoder := json.NewDecoder(bytes.NewReader(root.Value))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("projectstate: strict generic merge record decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return zero, fmt.Errorf("projectstate: multiple generic merge record JSON values")
		}
		return zero, fmt.Errorf("projectstate: trailing generic merge record JSON: %w", err)
	}
	return value, nil
}

func newMergeSnapshotShell(newBase state.Snapshot) (state.Snapshot, error) {
	canonical, err := validatedDiffSnapshot(newBase)
	if err != nil {
		return state.Snapshot{}, fmt.Errorf("projectstate: merge shell: %w", err)
	}
	return newMergeSnapshotShellFromValidated(canonical), nil
}

func newMergeSnapshotShellFromValidated(newBase state.Snapshot) state.Snapshot {
	return state.Snapshot{
		Config:    newBase.Config,
		Remotes:   cloneMergeRemotes(newBase.Remotes),
		Actors:    make(map[string]state.Record[state.ActorV1]),
		Tasks:     make(map[string]state.Record[state.TaskV1]),
		TaskLinks: make(map[string]state.Record[state.TaskLinkV1]),
		Articles:  make(map[string]state.KBRecord),
		Channels:  make(map[string]state.Record[state.ChannelV1]),
		Events:    make(map[string]state.EventV1),
		GitLinks:  make(map[string]state.Record[state.GitLinkV1]),
	}
}

func cloneMergeRemotes(remotes *state.RemotesV1) *state.RemotesV1 {
	if remotes == nil {
		return nil
	}
	cloned := *remotes
	cloned.Fabrics = append(make([]state.FabricHintV1, 0, len(remotes.Fabrics)), remotes.Fabrics...)
	return &cloned
}

func setRecordSurface(snapshot *state.Snapshot, key state.RecordKey, surface recordSurface) error {
	if snapshot == nil {
		return fmt.Errorf("projectstate: set record surface requires a snapshot")
	}
	if err := validateRecordSurface(key, surface); err != nil {
		return err
	}
	switch key.Kind {
	case "project":
		value, err := decodeCanonicalMergeValue[state.ProjectV1](surface.Root)
		if err != nil {
			return err
		}
		snapshot.Project = value
	case "actor":
		if snapshot.Actors == nil {
			return fmt.Errorf("projectstate: actor map is nil")
		}
		if surface.State == recordEndpointAbsent {
			delete(snapshot.Actors, key.ID)
			return nil
		}
		if surface.State == recordEndpointTombstone {
			value, err := decodeCanonicalMergeValue[state.TombstoneV1](surface.Root)
			if err != nil {
				return err
			}
			snapshot.Actors[key.ID] = state.Record[state.ActorV1]{Tombstone: &value}
			return nil
		}
		value, err := decodeCanonicalMergeValue[state.ActorV1](surface.Root)
		if err != nil {
			return err
		}
		snapshot.Actors[key.ID] = state.Record[state.ActorV1]{Value: &value}
	case "task":
		if snapshot.Tasks == nil {
			return fmt.Errorf("projectstate: task map is nil")
		}
		if surface.State == recordEndpointAbsent {
			delete(snapshot.Tasks, key.ID)
			return nil
		}
		if surface.State == recordEndpointTombstone {
			value, err := decodeCanonicalMergeValue[state.TombstoneV1](surface.Root)
			if err != nil {
				return err
			}
			snapshot.Tasks[key.ID] = state.Record[state.TaskV1]{Tombstone: &value}
			return nil
		}
		value, err := decodeCanonicalMergeValue[state.TaskV1](surface.Root)
		if err != nil {
			return err
		}
		snapshot.Tasks[key.ID] = state.Record[state.TaskV1]{Value: &value}
	case "task_link":
		if snapshot.TaskLinks == nil {
			return fmt.Errorf("projectstate: task_link map is nil")
		}
		if surface.State == recordEndpointAbsent {
			delete(snapshot.TaskLinks, key.ID)
			return nil
		}
		if surface.State == recordEndpointTombstone {
			value, err := decodeCanonicalMergeValue[state.TombstoneV1](surface.Root)
			if err != nil {
				return err
			}
			snapshot.TaskLinks[key.ID] = state.Record[state.TaskLinkV1]{Tombstone: &value}
			return nil
		}
		value, err := decodeCanonicalMergeValue[state.TaskLinkV1](surface.Root)
		if err != nil {
			return err
		}
		snapshot.TaskLinks[key.ID] = state.Record[state.TaskLinkV1]{Value: &value}
	case "kb_article":
		if snapshot.Articles == nil {
			return fmt.Errorf("projectstate: kb_article map is nil")
		}
		if surface.State == recordEndpointAbsent {
			delete(snapshot.Articles, key.ID)
			return nil
		}
		if surface.State == recordEndpointTombstone {
			value, err := decodeCanonicalMergeValue[state.TombstoneV1](surface.Root)
			if err != nil {
				return err
			}
			snapshot.Articles[key.ID] = state.KBRecord{Tombstone: &value}
			return nil
		}
		value, err := decodeCanonicalMergeValue[state.KBArticleV1](surface.Root)
		if err != nil {
			return err
		}
		body, err := decodeCanonicalMergeBody(surface.Body)
		if err != nil {
			return err
		}
		snapshot.Articles[key.ID] = state.KBRecord{Value: &value, Body: body}
	case "channel":
		if snapshot.Channels == nil {
			return fmt.Errorf("projectstate: channel map is nil")
		}
		if surface.State == recordEndpointAbsent {
			delete(snapshot.Channels, key.ID)
			return nil
		}
		if surface.State == recordEndpointTombstone {
			value, err := decodeCanonicalMergeValue[state.TombstoneV1](surface.Root)
			if err != nil {
				return err
			}
			snapshot.Channels[key.ID] = state.Record[state.ChannelV1]{Tombstone: &value}
			return nil
		}
		value, err := decodeCanonicalMergeValue[state.ChannelV1](surface.Root)
		if err != nil {
			return err
		}
		snapshot.Channels[key.ID] = state.Record[state.ChannelV1]{Value: &value}
	case "event":
		if snapshot.Events == nil {
			return fmt.Errorf("projectstate: event map is nil")
		}
		if surface.State == recordEndpointAbsent {
			delete(snapshot.Events, key.ID)
			return nil
		}
		value, err := decodeCanonicalMergeValue[state.EventV1](surface.Root)
		if err != nil {
			return err
		}
		snapshot.Events[key.ID] = value
	case "git_link":
		if snapshot.GitLinks == nil {
			return fmt.Errorf("projectstate: git_link map is nil")
		}
		if surface.State == recordEndpointAbsent {
			delete(snapshot.GitLinks, key.ID)
			return nil
		}
		value, err := decodeCanonicalMergeValue[state.GitLinkV1](surface.Root)
		if err != nil {
			return err
		}
		snapshot.GitLinks[key.ID] = state.Record[state.GitLinkV1]{Value: &value}
	default:
		return fmt.Errorf("projectstate: unsupported record surface kind %q", key.Kind)
	}
	return nil
}

func validateRecordSurface(key state.RecordKey, surface recordSurface) error {
	class, err := recordSurfaceClassForKind(key.Kind)
	if err != nil {
		return err
	}
	if !shared.CanonicalUUID(key.ID) {
		return fmt.Errorf("projectstate: invalid %s surface ID %q", key.Kind, key.ID)
	}
	if surface.State != recordEndpointAbsent && surface.State != recordEndpointLive && surface.State != recordEndpointTombstone {
		return fmt.Errorf("projectstate: invalid %s endpoint state %d", key.Kind, surface.State)
	}
	if surface.State == recordEndpointAbsent {
		if class == recordSurfaceProject {
			return fmt.Errorf("projectstate: project surface cannot be absent")
		}
		if _, err := requireAbsentMergeField("root", surface.Root); err != nil {
			return err
		}
		if _, err := requireAbsentMergeField("body", surface.Body); err != nil {
			return err
		}
		return nil
	}
	if surface.State == recordEndpointTombstone {
		if class != recordSurfaceMutable {
			return fmt.Errorf("projectstate: %s cannot have a tombstone surface", key.Kind)
		}
		if _, err := requireAbsentMergeField("body", surface.Body); err != nil {
			return err
		}
		value, err := decodeCanonicalMergeValue[state.TombstoneV1](surface.Root)
		if err != nil {
			return err
		}
		if value.SchemaVersion != 1 || value.Kind != "tombstone" || value.ID != key.ID || value.EntityKind != key.Kind {
			return fmt.Errorf("projectstate: tombstone surface does not match %s %s", key.Kind, key.ID)
		}
		if (key.Kind == "kb_article") != (value.DeletedBodyDigest != nil) {
			return fmt.Errorf("projectstate: invalid %s tombstone body digest", key.Kind)
		}
		return nil
	}

	if key.Kind == "kb_article" {
		if _, err := decodeCanonicalMergeBody(surface.Body); err != nil {
			return err
		}
	} else if _, err := requireAbsentMergeField("body", surface.Body); err != nil {
		return err
	}
	switch key.Kind {
	case "project":
		value, err := decodeCanonicalMergeValue[state.ProjectV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	case "actor":
		value, err := decodeCanonicalMergeValue[state.ActorV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	case "task":
		value, err := decodeCanonicalMergeValue[state.TaskV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	case "task_link":
		value, err := decodeCanonicalMergeValue[state.TaskLinkV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	case "kb_article":
		value, err := decodeCanonicalMergeValue[state.KBArticleV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	case "channel":
		value, err := decodeCanonicalMergeValue[state.ChannelV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	case "event":
		value, err := decodeCanonicalMergeValue[state.EventV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	case "git_link":
		value, err := decodeCanonicalMergeValue[state.GitLinkV1](surface.Root)
		return validateMergeRecordHeader(key, value.SchemaVersion, value.Kind, value.ID, err)
	default:
		return fmt.Errorf("projectstate: unsupported record surface kind %q", key.Kind)
	}
}

func recordSurfaceClassForKind(kind string) (recordSurfaceClass, error) {
	switch kind {
	case "project":
		return recordSurfaceProject, nil
	case "actor", "task", "task_link", "kb_article", "channel":
		return recordSurfaceMutable, nil
	case "event", "git_link":
		return recordSurfaceImmutable, nil
	default:
		return 0, fmt.Errorf("projectstate: unsupported record surface kind %q", kind)
	}
}

func decodeCanonicalMergeValue[T any](root FieldValue) (T, error) {
	var zero T
	if !root.Present {
		if root.Value != nil {
			return zero, fmt.Errorf("projectstate: absent merge record root has a value")
		}
		return zero, fmt.Errorf("projectstate: merge record root must be present")
	}
	if len(root.Value) == 0 {
		return zero, fmt.Errorf("projectstate: present merge record root has no value")
	}
	decoder := json.NewDecoder(bytes.NewReader(root.Value))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("projectstate: strict merge record decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return zero, fmt.Errorf("projectstate: multiple merge record JSON values")
		}
		return zero, fmt.Errorf("projectstate: trailing merge record JSON: %w", err)
	}
	typed, err := canonicalFieldJSON(value)
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(typed, root.Value) {
		return zero, fmt.Errorf("projectstate: merge record is not canonical typed JSON")
	}
	return value, nil
}

func decodeCanonicalMergeBody(body FieldValue) ([]byte, error) {
	canonical, err := cloneCanonicalFieldValue(body)
	if err != nil {
		return nil, err
	}
	if !canonical.Present {
		return nil, fmt.Errorf("projectstate: live KB body must be present")
	}
	slot, err := decodeMergeJSONSlot(canonical)
	if err != nil {
		return nil, err
	}
	text, ok := slot.value.(string)
	if !ok {
		return nil, fmt.Errorf("projectstate: live KB body must be a JSON string")
	}
	markdown, err := state.CanonicalMarkdown([]byte(text))
	if err != nil || !bytes.Equal(markdown, []byte(text)) {
		return nil, fmt.Errorf("projectstate: live KB body must be canonical Markdown")
	}
	return bytes.Clone(markdown), nil
}

func validateMergeRecordHeader(key state.RecordKey, version int, kind, id string, decodeErr error) error {
	if decodeErr != nil {
		return decodeErr
	}
	if version != 1 || kind != key.Kind || id != key.ID {
		return fmt.Errorf("projectstate: merge record does not match %s %s", key.Kind, key.ID)
	}
	return nil
}

func requireAbsentMergeField(name string, value FieldValue) (FieldValue, error) {
	if value.Present {
		return FieldValue{}, fmt.Errorf("projectstate: %s must be absent", name)
	}
	if value.Value != nil {
		return FieldValue{}, fmt.Errorf("projectstate: absent %s has a value", name)
	}
	return FieldValue{}, nil
}

func fieldValuesEqual(left, right FieldValue) bool {
	return left.Present == right.Present && bytes.Equal(left.Value, right.Value)
}

func cloneRecordSurface(surface recordSurface) (recordSurface, error) {
	return recordSurface{
		State: surface.State,
		Root:  FieldValue{Present: surface.Root.Present, Value: bytes.Clone(surface.Root.Value)},
		Body:  FieldValue{Present: surface.Body.Present, Value: bytes.Clone(surface.Body.Value)},
	}, nil
}
