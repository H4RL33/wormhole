package projectstate

import (
	"bytes"
	"errors"
	"fmt"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var errRecordLifecycleUnsupported = errors.New("projectstate: record lifecycle merge unsupported")

type recordSurfaceConflictMask struct {
	Root bool
	Body bool
}

func mergeRecordSurface(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	for _, endpoint := range []struct {
		name    string
		surface recordSurface
	}{
		{name: "base", surface: base},
		{name: "ours", surface: ours},
		{name: "theirs", surface: theirs},
	} {
		if err := validateRecordSurface(key, endpoint.surface); err != nil {
			return recordSurfaceMergeResult{}, fmt.Errorf("projectstate: record lifecycle %s: %w", endpoint.name, err)
		}
	}
	class, err := recordSurfaceClassForKind(key.Kind)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	switch class {
	case recordSurfaceImmutable:
		return mergeImmutableRecordSurface(key, base, ours, theirs)
	case recordSurfaceMutable:
		switch base.State {
		case recordEndpointAbsent:
			return mergeNewMutableRecordSurface(key, base, ours, theirs)
		case recordEndpointLive:
			return mergeOldLiveMutableRecordSurface(key, base, ours, theirs)
		case recordEndpointTombstone:
			return mergeOldTombstoneMutableRecordSurface(key, base, ours, theirs)
		default:
			return recordSurfaceMergeResult{}, fmt.Errorf("%w: mutable %s base state %d", errRecordLifecycleUnsupported, key.Kind, base.State)
		}
	case recordSurfaceProject:
		return mergeProjectRecordSurface(key, base, ours, theirs)
	default:
		return recordSurfaceMergeResult{}, fmt.Errorf("%w: class %d", errRecordLifecycleUnsupported, class)
	}
}

func mergeProjectRecordSurface(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	merged, err := mergeExistingTypedLiveRecord(key, base, ours, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if len(merged.Conflicts) == 0 {
		return merged, nil
	}
	return conflictRecordSurfaceResult(ours, merged.Conflicts)
}

func mergeOldTombstoneMutableRecordSurface(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	if ours.State == recordEndpointAbsent || theirs.State == recordEndpointAbsent {
		return recordSurfaceMergeResult{}, fmt.Errorf("%w: lifecycle merge %s %s", ErrRawRecordDeletion, key.Kind, key.ID)
	}
	equal, err := recordSurfacesEqual(key, ours, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if equal {
		return cleanRecordSurfaceResult(ours)
	}
	oursIsBase, err := recordSurfacesEqual(key, ours, base)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if oursIsBase {
		return cleanRecordSurfaceResult(theirs)
	}
	theirsIsBase, err := recordSurfacesEqual(key, theirs, base)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if theirsIsBase {
		return cleanRecordSurfaceResult(ours)
	}
	if ours.State == recordEndpointTombstone && theirs.State == recordEndpointTombstone {
		return conflictingRecordSurfaceResult(key, ConflictSameField, recordSurfaceConflictMask{Root: true}, base, ours, theirs)
	}
	mask, err := recordSurfaceDifferenceMask(key, ours, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	return conflictingRecordSurfaceResult(key, ConflictInvalidResurrection, mask, base, ours, theirs)
}

func mergeOldLiveMutableRecordSurface(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	if ours.State == recordEndpointAbsent || theirs.State == recordEndpointAbsent {
		return recordSurfaceMergeResult{}, fmt.Errorf("%w: lifecycle merge %s %s", ErrRawRecordDeletion, key.Kind, key.ID)
	}
	switch {
	case ours.State == recordEndpointLive && theirs.State == recordEndpointLive:
		return mergeLifecycleExistingLiveRecord(key, base, ours, theirs)
	case ours.State == recordEndpointTombstone && theirs.State == recordEndpointTombstone:
		equal, err := recordSurfacesEqual(key, ours, theirs)
		if err != nil {
			return recordSurfaceMergeResult{}, err
		}
		if equal {
			return cleanRecordSurfaceResult(ours)
		}
		return conflictingRecordSurfaceResult(key, ConflictSameField, recordSurfaceConflictMask{Root: true}, base, ours, theirs)
	}

	live := ours
	tombstone := theirs
	if ours.State == recordEndpointTombstone {
		live, tombstone = theirs, ours
	}
	mask, err := existingTypedLiveSemanticMask(key, base, live)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if !mask.Root && !mask.Body {
		return cleanRecordSurfaceResult(tombstone)
	}
	return conflictingRecordSurfaceResultKinds(key, ConflictTombstoneEdit, ConflictTombstoneBody, mask, base, ours, theirs)
}

func mergeLifecycleExistingLiveRecord(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	merged, err := mergeExistingTypedLiveRecord(key, base, ours, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if len(merged.Conflicts) == 0 {
		return merged, nil
	}
	provisional, err := cloneRecordSurface(ours)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	merged.Surface = provisional
	return merged, nil
}

func existingTypedLiveSemanticMask(key state.RecordKey, base, side recordSurface) (recordSurfaceConflictMask, error) {
	spec, err := existingLiveRecordSpec(key.Kind)
	if err != nil {
		return recordSurfaceConflictMask{}, err
	}
	baseNormalized, err := normalizeTypedLiveSurface("base", key, base)
	if err != nil {
		return recordSurfaceConflictMask{}, err
	}
	sideNormalized, err := normalizeTypedLiveSurface("side", key, side)
	if err != nil {
		return recordSurfaceConflictMask{}, err
	}
	baseValidated, err := validateLiveRecordSurface("base", spec, baseNormalized)
	if err != nil {
		return recordSurfaceConflictMask{}, err
	}
	sideValidated, err := validateLiveRecordSurface("side", spec, sideNormalized)
	if err != nil {
		return recordSurfaceConflictMask{}, err
	}
	if spec.createdAt && !bytes.Equal(baseValidated.createdAt.Value, sideValidated.createdAt.Value) {
		return recordSurfaceConflictMask{}, fmt.Errorf("%w: %s %s changed created_at", state.ErrOperationPrecondition, key.Kind, key.ID)
	}
	rootEqual, err := semanticJSONEqual(baseValidated.surface.Root, sideValidated.surface.Root)
	if err != nil {
		return recordSurfaceConflictMask{}, err
	}
	return recordSurfaceConflictMask{
		Root: !rootEqual,
		Body: spec.body && !bytes.Equal(baseValidated.bodyBytes, sideValidated.bodyBytes),
	}, nil
}

func mergeImmutableRecordSurface(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	if base.State == recordEndpointAbsent {
		switch {
		case ours.State == recordEndpointAbsent && theirs.State == recordEndpointAbsent:
			return cleanRecordSurfaceResult(ours)
		case ours.State == recordEndpointAbsent:
			return cleanRecordSurfaceResult(theirs)
		case theirs.State == recordEndpointAbsent:
			return cleanRecordSurfaceResult(ours)
		}
		equal, err := recordSurfacesEqual(key, ours, theirs)
		if err != nil {
			return recordSurfaceMergeResult{}, err
		}
		if equal {
			return cleanRecordSurfaceResult(ours)
		}
		return conflictingRecordSurfaceResult(key, ConflictImmutableRecord, recordSurfaceConflictMask{Root: true}, base, ours, theirs)
	}

	oursUnchanged, err := recordSurfacesEqual(key, base, ours)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	theirsUnchanged, err := recordSurfacesEqual(key, base, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if oursUnchanged && theirsUnchanged {
		return cleanRecordSurfaceResult(ours)
	}
	return conflictingRecordSurfaceResult(key, ConflictImmutableRecord, recordSurfaceConflictMask{Root: true}, base, ours, theirs)
}

func mergeNewMutableRecordSurface(key state.RecordKey, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	switch {
	case ours.State == recordEndpointAbsent && theirs.State == recordEndpointAbsent:
		return cleanRecordSurfaceResult(ours)
	case ours.State == recordEndpointAbsent:
		return cleanRecordSurfaceResult(theirs)
	case theirs.State == recordEndpointAbsent:
		return cleanRecordSurfaceResult(ours)
	}
	equal, err := recordSurfacesEqual(key, ours, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	if equal {
		return cleanRecordSurfaceResult(ours)
	}
	mask, err := recordSurfaceDifferenceMask(key, ours, theirs)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	switch {
	case ours.State == recordEndpointLive && theirs.State == recordEndpointLive:
		return conflictingRecordSurfaceResult(key, ConflictSameField, mask, base, ours, theirs)
	case ours.State == recordEndpointTombstone && theirs.State == recordEndpointTombstone:
		return conflictingRecordSurfaceResult(key, ConflictSameField, recordSurfaceConflictMask{Root: true}, base, ours, theirs)
	default:
		return conflictingRecordSurfaceResultKinds(key, ConflictTombstoneEdit, ConflictTombstoneBody, mask, base, ours, theirs)
	}
}

func recordSurfaceDifferenceMask(key state.RecordKey, left, right recordSurface) (recordSurfaceConflictMask, error) {
	if err := validateRecordSurface(key, left); err != nil {
		return recordSurfaceConflictMask{}, fmt.Errorf("projectstate: surface mask left: %w", err)
	}
	if err := validateRecordSurface(key, right); err != nil {
		return recordSurfaceConflictMask{}, fmt.Errorf("projectstate: surface mask right: %w", err)
	}
	return recordSurfaceConflictMask{
		Root: !fieldValuesEqual(left.Root, right.Root),
		Body: key.Kind == "kb_article" && !fieldValuesEqual(left.Body, right.Body),
	}, nil
}

func conflictingRecordSurfaceResult(key state.RecordKey, kind ConflictKind, mask recordSurfaceConflictMask, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	return conflictingRecordSurfaceResultKinds(key, kind, kind, mask, base, ours, theirs)
}

func conflictingRecordSurfaceResultKinds(key state.RecordKey, rootKind, bodyKind ConflictKind, mask recordSurfaceConflictMask, base, ours, theirs recordSurface) (recordSurfaceMergeResult, error) {
	if mask.Body && key.Kind != "kb_article" {
		return recordSurfaceMergeResult{}, fmt.Errorf("projectstate: body conflict mask requires kb_article, got %s", key.Kind)
	}
	conflicts := make([]Conflict, 0, 2)
	if mask.Root {
		conflict, err := newRecordRootConflict(key, rootKind, base, ours, theirs)
		if err != nil {
			return recordSurfaceMergeResult{}, err
		}
		conflicts = append(conflicts, conflict)
	}
	if mask.Body {
		conflict, err := newConflict(key, "/body", bodyKind, base.Body, ours.Body, theirs.Body)
		if err != nil {
			return recordSurfaceMergeResult{}, err
		}
		conflicts = append(conflicts, conflict)
	}
	return conflictRecordSurfaceResult(ours, conflicts)
}

func newRecordRootConflict(key state.RecordKey, kind ConflictKind, base, ours, theirs recordSurface) (Conflict, error) {
	for _, endpoint := range []struct {
		name    string
		surface recordSurface
	}{
		{name: "base", surface: base},
		{name: "ours", surface: ours},
		{name: "theirs", surface: theirs},
	} {
		if err := validateRecordSurface(key, endpoint.surface); err != nil {
			return Conflict{}, fmt.Errorf("projectstate: root conflict %s: %w", endpoint.name, err)
		}
	}
	conflict := Conflict{
		Key:       key,
		FieldPath: "",
		Kind:      kind,
		Base:      cloneRecordRoot(base.Root),
		Ours:      cloneRecordRoot(ours.Root),
		Theirs:    cloneRecordRoot(theirs.Root),
	}
	var err error
	conflict.ID, err = conflictID(conflict)
	if err != nil {
		return Conflict{}, err
	}
	return conflict, nil
}

func cleanRecordSurfaceResult(surface recordSurface) (recordSurfaceMergeResult, error) {
	cloned, err := cloneRecordSurface(surface)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	return recordSurfaceMergeResult{Surface: cloned, Conflicts: make([]Conflict, 0)}, nil
}

func conflictRecordSurfaceResult(ours recordSurface, conflicts []Conflict) (recordSurfaceMergeResult, error) {
	if len(conflicts) == 0 {
		return recordSurfaceMergeResult{}, fmt.Errorf("projectstate: conflict result requires conflict evidence")
	}
	provisional, err := cloneRecordSurface(ours)
	if err != nil {
		return recordSurfaceMergeResult{}, err
	}
	ownedConflicts := append(make([]Conflict, 0, len(conflicts)), conflicts...)
	sortConflicts(ownedConflicts)
	return recordSurfaceMergeResult{Surface: provisional, Conflicts: ownedConflicts}, nil
}

func cloneRecordRoot(root FieldValue) FieldValue {
	return FieldValue{Present: root.Present, Value: bytes.Clone(root.Value)}
}
