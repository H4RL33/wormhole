package projectstate

import (
	"bytes"
	"fmt"
	"time"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type liveRecordSurface struct {
	Root FieldValue
	Body FieldValue
}

type liveRecordMergeResult struct {
	Surface   liveRecordSurface
	Conflicts []Conflict
}

type liveRecordSpec struct {
	createdAt bool
	updatedAt bool
	body      bool
}

type validatedLiveRecordSurface struct {
	surface       liveRecordSurface
	createdAt     FieldValue
	updatedAt     FieldValue
	updatedAtTime time.Time
	bodyBytes     []byte
}

func mergeExistingLiveRecord(key state.RecordKey, base, ours, theirs liveRecordSurface) (liveRecordMergeResult, error) {
	spec, err := existingLiveRecordSpec(key.Kind)
	if err != nil {
		return liveRecordMergeResult{}, err
	}
	baseValue, err := validateLiveRecordSurface("base", spec, base)
	if err != nil {
		return liveRecordMergeResult{}, err
	}
	oursValue, err := validateLiveRecordSurface("ours", spec, ours)
	if err != nil {
		return liveRecordMergeResult{}, err
	}
	theirsValue, err := validateLiveRecordSurface("theirs", spec, theirs)
	if err != nil {
		return liveRecordMergeResult{}, err
	}
	if spec.createdAt {
		if !bytes.Equal(baseValue.createdAt.Value, oursValue.createdAt.Value) {
			return liveRecordMergeResult{}, fmt.Errorf("%w: %s %s ours changed created_at", state.ErrOperationPrecondition, key.Kind, key.ID)
		}
		if !bytes.Equal(baseValue.createdAt.Value, theirsValue.createdAt.Value) {
			return liveRecordMergeResult{}, fmt.Errorf("%w: %s %s theirs changed created_at", state.ErrOperationPrecondition, key.Kind, key.ID)
		}
	}

	oursRootEqual, err := semanticJSONEqual(baseValue.surface.Root, oursValue.surface.Root)
	if err != nil {
		return liveRecordMergeResult{}, err
	}
	theirsRootEqual, err := semanticJSONEqual(baseValue.surface.Root, theirsValue.surface.Root)
	if err != nil {
		return liveRecordMergeResult{}, err
	}
	oursEdited := !oursRootEqual || (spec.body && !bytes.Equal(baseValue.bodyBytes, oursValue.bodyBytes))
	theirsEdited := !theirsRootEqual || (spec.body && !bytes.Equal(baseValue.bodyBytes, theirsValue.bodyBytes))

	mergedRoot, conflicts, err := mergeCanonicalJSON(mergeJSONOptions{Key: key, SkipRootUpdatedAt: spec.updatedAt},
		baseValue.surface.Root, oursValue.surface.Root, theirsValue.surface.Root)
	if err != nil {
		return liveRecordMergeResult{}, err
	}
	result := liveRecordMergeResult{
		Surface:   liveRecordSurface{Root: mergedRoot},
		Conflicts: conflicts,
	}
	if spec.body {
		mergedBody, conflict, mergeErr := mergeMarkdown(baseValue.bodyBytes, oursValue.bodyBytes, theirsValue.bodyBytes)
		if mergeErr != nil {
			return liveRecordMergeResult{}, mergeErr
		}
		if conflict {
			result.Surface.Body, err = cloneCanonicalFieldValue(oursValue.surface.Body)
			if err != nil {
				return liveRecordMergeResult{}, err
			}
			bodyConflict, conflictErr := newConflict(key, "/body", ConflictMarkdown,
				baseValue.surface.Body, oursValue.surface.Body, theirsValue.surface.Body)
			if conflictErr != nil {
				return liveRecordMergeResult{}, conflictErr
			}
			result.Conflicts = append(result.Conflicts, bodyConflict)
		} else {
			result.Surface.Body, err = presentFieldValue(string(mergedBody))
			if err != nil {
				return liveRecordMergeResult{}, err
			}
		}
	}

	if len(result.Conflicts) == 0 && spec.updatedAt {
		updatedAt := selectLiveUpdatedAt(baseValue, oursValue, theirsValue, oursEdited, theirsEdited)
		result.Surface.Root, err = installRootUpdatedAt(result.Surface.Root, updatedAt)
		if err != nil {
			return liveRecordMergeResult{}, err
		}
	}
	if result.Conflicts == nil {
		result.Conflicts = make([]Conflict, 0)
	}
	sortConflicts(result.Conflicts)
	return result, nil
}

func existingLiveRecordSpec(kind string) (liveRecordSpec, error) {
	switch kind {
	case "project", "task":
		return liveRecordSpec{createdAt: true, updatedAt: true}, nil
	case "actor", "task_link":
		return liveRecordSpec{}, nil
	case "kb_article":
		return liveRecordSpec{createdAt: true, updatedAt: true, body: true}, nil
	case "channel":
		return liveRecordSpec{createdAt: true}, nil
	default:
		return liveRecordSpec{}, fmt.Errorf("projectstate: existing-live merge unsupported kind %q", kind)
	}
}

func validateLiveRecordSurface(name string, spec liveRecordSpec, surface liveRecordSurface) (validatedLiveRecordSurface, error) {
	rootSlot, err := decodeMergeJSONSlot(surface.Root)
	if err != nil {
		return validatedLiveRecordSurface{}, fmt.Errorf("projectstate: existing-live %s root: %w", name, err)
	}
	rootObject, ok := mergeJSONObject(rootSlot)
	if !ok {
		return validatedLiveRecordSurface{}, fmt.Errorf("projectstate: existing-live %s root must be a present object", name)
	}
	root, err := mergeJSONSlotFieldValue(rootSlot)
	if err != nil {
		return validatedLiveRecordSurface{}, err
	}
	value := validatedLiveRecordSurface{surface: liveRecordSurface{Root: root}}
	value.createdAt, _, err = validateLiveTimestampField(name, rootObject, "created_at", spec.createdAt)
	if err != nil {
		return validatedLiveRecordSurface{}, err
	}
	value.updatedAt, value.updatedAtTime, err = validateLiveTimestampField(name, rootObject, "updated_at", spec.updatedAt)
	if err != nil {
		return validatedLiveRecordSurface{}, err
	}

	body, err := cloneCanonicalFieldValue(surface.Body)
	if err != nil {
		return validatedLiveRecordSurface{}, fmt.Errorf("projectstate: existing-live %s body: %w", name, err)
	}
	if !spec.body {
		if body.Present {
			return validatedLiveRecordSurface{}, fmt.Errorf("projectstate: existing-live %s kind does not have a body", name)
		}
		return value, nil
	}
	if !body.Present {
		return validatedLiveRecordSurface{}, fmt.Errorf("projectstate: existing-live %s KB body must be present", name)
	}
	bodySlot, err := decodeMergeJSONSlot(body)
	if err != nil {
		return validatedLiveRecordSurface{}, err
	}
	bodyText, ok := bodySlot.value.(string)
	if !ok {
		return validatedLiveRecordSurface{}, fmt.Errorf("projectstate: existing-live %s KB body must be a JSON string", name)
	}
	canonicalBody, err := state.CanonicalMarkdown([]byte(bodyText))
	if err != nil || !bytes.Equal(canonicalBody, []byte(bodyText)) {
		return validatedLiveRecordSurface{}, fmt.Errorf("projectstate: existing-live %s KB body must be canonical Markdown", name)
	}
	value.surface.Body = body
	value.bodyBytes = bytes.Clone(canonicalBody)
	return value, nil
}

func validateLiveTimestampField(name string, root map[string]any, field string, required bool) (FieldValue, time.Time, error) {
	slot := mergeJSONObjectSlot(root, field)
	if !required {
		if slot.present {
			return FieldValue{}, time.Time{}, fmt.Errorf("projectstate: existing-live %s root must not contain %s", name, field)
		}
		return FieldValue{}, time.Time{}, nil
	}
	if !slot.present {
		return FieldValue{}, time.Time{}, fmt.Errorf("projectstate: existing-live %s root requires %s", name, field)
	}
	text, ok := slot.value.(string)
	if !ok {
		return FieldValue{}, time.Time{}, fmt.Errorf("projectstate: existing-live %s %s must be a timestamp string", name, field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || parsed.IsZero() {
		return FieldValue{}, time.Time{}, fmt.Errorf("projectstate: existing-live %s invalid %s", name, field)
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return FieldValue{}, time.Time{}, fmt.Errorf("projectstate: existing-live %s %s must be UTC", name, field)
	}
	value, err := mergeJSONSlotFieldValue(slot)
	if err != nil {
		return FieldValue{}, time.Time{}, err
	}
	return value, parsed, nil
}

func selectLiveUpdatedAt(base, ours, theirs validatedLiveRecordSurface, oursEdited, theirsEdited bool) FieldValue {
	switch {
	case oursEdited && !theirsEdited:
		return ours.updatedAt
	case theirsEdited && !oursEdited:
		return theirs.updatedAt
	case oursEdited && theirsEdited && theirs.updatedAtTime.After(ours.updatedAtTime):
		return theirs.updatedAt
	case oursEdited && theirsEdited:
		return ours.updatedAt
	default:
		return base.updatedAt
	}
}
