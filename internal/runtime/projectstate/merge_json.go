package projectstate

import (
	"bytes"
	"fmt"
	"sort"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type mergeJSONOptions struct {
	Key               state.RecordKey
	SkipRootUpdatedAt bool
}

type mergeJSONSlot struct {
	present bool
	value   any
}

func mergeCanonicalJSON(options mergeJSONOptions, base, ours, theirs FieldValue) (FieldValue, []Conflict, error) {
	baseSlot, err := decodeMergeJSONSlot(base)
	if err != nil {
		return FieldValue{}, nil, fmt.Errorf("projectstate: merge JSON base: %w", err)
	}
	oursSlot, err := decodeMergeJSONSlot(ours)
	if err != nil {
		return FieldValue{}, nil, fmt.Errorf("projectstate: merge JSON ours: %w", err)
	}
	theirsSlot, err := decodeMergeJSONSlot(theirs)
	if err != nil {
		return FieldValue{}, nil, fmt.Errorf("projectstate: merge JSON theirs: %w", err)
	}
	merged, conflicts, err := mergeJSONSlots(options, "", baseSlot, oursSlot, theirsSlot)
	if err != nil {
		return FieldValue{}, nil, err
	}
	result, err := mergeJSONSlotFieldValue(merged)
	if err != nil {
		return FieldValue{}, nil, err
	}
	if conflicts == nil {
		conflicts = make([]Conflict, 0)
	}
	sortConflicts(conflicts)
	return result, conflicts, nil
}

func mergeJSONSlots(options mergeJSONOptions, path string, base, ours, theirs mergeJSONSlot) (mergeJSONSlot, []Conflict, error) {
	baseObject, baseIsObject := mergeJSONObject(base)
	oursObject, oursIsObject := mergeJSONObject(ours)
	theirsObject, theirsIsObject := mergeJSONObject(theirs)
	forceRootObjectMerge := options.SkipRootUpdatedAt && path == "" && baseIsObject && oursIsObject && theirsIsObject
	if !forceRootObjectMerge {
		equal, err := mergeJSONSlotsEqual(ours, theirs)
		if err != nil {
			return mergeJSONSlot{}, nil, err
		}
		if equal {
			return ours, make([]Conflict, 0), nil
		}
		equal, err = mergeJSONSlotsEqual(ours, base)
		if err != nil {
			return mergeJSONSlot{}, nil, err
		}
		if equal {
			return theirs, make([]Conflict, 0), nil
		}
		equal, err = mergeJSONSlotsEqual(theirs, base)
		if err != nil {
			return mergeJSONSlot{}, nil, err
		}
		if equal {
			return ours, make([]Conflict, 0), nil
		}
	}

	if baseIsObject && oursIsObject && theirsIsObject {
		keys := mergeJSONObjectKeys(baseObject, oursObject, theirsObject)
		merged := make(map[string]any, len(keys))
		conflicts := make([]Conflict, 0)
		for _, key := range keys {
			baseChild := mergeJSONObjectSlot(baseObject, key)
			if options.SkipRootUpdatedAt && path == "" && key == "updated_at" {
				if baseChild.present {
					merged[key] = baseChild.value
				}
				continue
			}
			oursChild := mergeJSONObjectSlot(oursObject, key)
			theirsChild := mergeJSONObjectSlot(theirsObject, key)
			childPath := path + "/" + escapeJSONPointerToken(key)
			child, childConflicts, childErr := mergeJSONSlots(options, childPath, baseChild, oursChild, theirsChild)
			if childErr != nil {
				return mergeJSONSlot{}, nil, childErr
			}
			if child.present {
				merged[key] = child.value
			}
			conflicts = append(conflicts, childConflicts...)
		}
		return mergeJSONSlot{present: true, value: merged}, conflicts, nil
	}

	baseValue, err := mergeJSONSlotFieldValue(base)
	if err != nil {
		return mergeJSONSlot{}, nil, err
	}
	oursValue, err := mergeJSONSlotFieldValue(ours)
	if err != nil {
		return mergeJSONSlot{}, nil, err
	}
	theirsValue, err := mergeJSONSlotFieldValue(theirs)
	if err != nil {
		return mergeJSONSlot{}, nil, err
	}
	conflict, err := newConflict(options.Key, path, ConflictSameField, baseValue, oursValue, theirsValue)
	if err != nil {
		return mergeJSONSlot{}, nil, err
	}
	return ours, []Conflict{conflict}, nil
}

func semanticJSONEqual(left, right FieldValue) (bool, error) {
	leftSlot, err := decodeMergeJSONSlot(left)
	if err != nil {
		return false, fmt.Errorf("projectstate: semantic JSON left: %w", err)
	}
	rightSlot, err := decodeMergeJSONSlot(right)
	if err != nil {
		return false, fmt.Errorf("projectstate: semantic JSON right: %w", err)
	}
	if leftObject, ok := mergeJSONObject(leftSlot); ok {
		leftCopy := cloneMergeJSONObject(leftObject)
		delete(leftCopy, "updated_at")
		leftSlot.value = leftCopy
	}
	if rightObject, ok := mergeJSONObject(rightSlot); ok {
		rightCopy := cloneMergeJSONObject(rightObject)
		delete(rightCopy, "updated_at")
		rightSlot.value = rightCopy
	}
	return mergeJSONSlotsEqual(leftSlot, rightSlot)
}

func installRootUpdatedAt(root, updatedAt FieldValue) (FieldValue, error) {
	rootSlot, err := decodeMergeJSONSlot(root)
	if err != nil {
		return FieldValue{}, fmt.Errorf("projectstate: install updated_at root: %w", err)
	}
	updatedAtSlot, err := decodeMergeJSONSlot(updatedAt)
	if err != nil {
		return FieldValue{}, fmt.Errorf("projectstate: install updated_at value: %w", err)
	}
	rootObject, ok := mergeJSONObject(rootSlot)
	if !ok {
		return FieldValue{}, fmt.Errorf("projectstate: install updated_at requires a present root object")
	}
	if !updatedAtSlot.present {
		return FieldValue{}, fmt.Errorf("projectstate: install updated_at requires a present value")
	}
	merged := cloneMergeJSONObject(rootObject)
	merged["updated_at"] = updatedAtSlot.value
	return mergeJSONSlotFieldValue(mergeJSONSlot{present: true, value: merged})
}

func newConflict(key state.RecordKey, path string, kind ConflictKind, base, ours, theirs FieldValue) (Conflict, error) {
	base, err := cloneCanonicalFieldValue(base)
	if err != nil {
		return Conflict{}, fmt.Errorf("projectstate: conflict base: %w", err)
	}
	ours, err = cloneCanonicalFieldValue(ours)
	if err != nil {
		return Conflict{}, fmt.Errorf("projectstate: conflict ours: %w", err)
	}
	theirs, err = cloneCanonicalFieldValue(theirs)
	if err != nil {
		return Conflict{}, fmt.Errorf("projectstate: conflict theirs: %w", err)
	}
	conflict := Conflict{Key: key, FieldPath: path, Kind: kind, Base: base, Ours: ours, Theirs: theirs}
	conflict.ID, err = conflictID(conflict)
	if err != nil {
		return Conflict{}, err
	}
	return conflict, nil
}

func cloneCanonicalFieldValue(value FieldValue) (FieldValue, error) {
	slot, err := decodeMergeJSONSlot(value)
	if err != nil {
		return FieldValue{}, err
	}
	return mergeJSONSlotFieldValue(slot)
}

func decodeMergeJSONSlot(value FieldValue) (mergeJSONSlot, error) {
	if !value.Present {
		if value.Value != nil {
			return mergeJSONSlot{}, fmt.Errorf("absent field has a value")
		}
		return mergeJSONSlot{}, nil
	}
	if len(value.Value) == 0 {
		return mergeJSONSlot{}, fmt.Errorf("present field has no value")
	}
	decoded, err := decodeDiffJSON(value.Value)
	if err != nil {
		return mergeJSONSlot{}, err
	}
	canonical, err := canonicalFieldJSON(decoded)
	if err != nil {
		return mergeJSONSlot{}, err
	}
	if !bytes.Equal(canonical, value.Value) {
		return mergeJSONSlot{}, fmt.Errorf("field value is not canonical JSON")
	}
	return mergeJSONSlot{present: true, value: decoded}, nil
}

func mergeJSONSlotFieldValue(slot mergeJSONSlot) (FieldValue, error) {
	if !slot.present {
		return FieldValue{}, nil
	}
	raw, err := canonicalFieldJSON(slot.value)
	if err != nil {
		return FieldValue{}, err
	}
	return FieldValue{Present: true, Value: raw}, nil
}

func mergeJSONSlotsEqual(left, right mergeJSONSlot) (bool, error) {
	if left.present != right.present {
		return false, nil
	}
	if !left.present {
		return true, nil
	}
	leftValue, err := canonicalFieldJSON(left.value)
	if err != nil {
		return false, err
	}
	rightValue, err := canonicalFieldJSON(right.value)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftValue, rightValue), nil
}

func mergeJSONObject(slot mergeJSONSlot) (map[string]any, bool) {
	if !slot.present {
		return nil, false
	}
	value, ok := slot.value.(map[string]any)
	return value, ok
}

func mergeJSONObjectKeys(objects ...map[string]any) []string {
	seen := make(map[string]struct{})
	for _, object := range objects {
		for key := range object {
			seen[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mergeJSONObjectSlot(object map[string]any, key string) mergeJSONSlot {
	value, present := object[key]
	return mergeJSONSlot{present: present, value: value}
}

func cloneMergeJSONObject(object map[string]any) map[string]any {
	clone := make(map[string]any, len(object))
	for key, value := range object {
		clone[key] = value
	}
	return clone
}
