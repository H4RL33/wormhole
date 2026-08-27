package projectstate

import (
	"fmt"
	"reflect"
	"sort"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type ConflictKind string

const (
	ConflictSameField           ConflictKind = "same_field"
	ConflictMarkdown            ConflictKind = "markdown"
	ConflictImmutableRecord     ConflictKind = "immutable_record"
	ConflictTombstoneEdit       ConflictKind = "tombstone_edit"
	ConflictTombstoneBody       ConflictKind = "tombstone_body"
	ConflictInvalidResurrection ConflictKind = "invalid_resurrection"
)

type Conflict struct {
	ID                 string
	Key                state.RecordKey
	FieldPath          string
	Kind               ConflictKind
	Base, Ours, Theirs FieldValue
}

type MergeResult struct {
	Snapshot  state.Snapshot
	Conflicts []Conflict
}

type conflictIDPreimageV1 struct {
	SchemaVersion int             `json:"schema_version"`
	Key           state.RecordKey `json:"key"`
	FieldPath     string          `json:"field_path"`
	Kind          ConflictKind    `json:"kind"`
	Base          FieldValue      `json:"base"`
	Ours          FieldValue      `json:"ours"`
	Theirs        FieldValue      `json:"theirs"`
}

func conflictID(conflict Conflict) (string, error) {
	digest, err := state.DigestCanonicalJSON(conflictIDPreimageV1{
		SchemaVersion: 1,
		Key:           conflict.Key,
		FieldPath:     conflict.FieldPath,
		Kind:          conflict.Kind,
		Base:          conflict.Base,
		Ours:          conflict.Ours,
		Theirs:        conflict.Theirs,
	})
	if err != nil {
		return "", err
	}
	return string(digest), nil
}

func sortConflicts(conflicts []Conflict) {
	sort.Slice(conflicts, func(i, j int) bool {
		left, right := conflicts[i], conflicts[j]
		leftRank, rightRank := diffKindRank(left.Key.Kind), diffKindRank(right.Key.Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if left.Key.ID != right.Key.ID {
			return left.Key.ID < right.Key.ID
		}
		if left.FieldPath != right.FieldPath {
			return left.FieldPath < right.FieldPath
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.ID < right.ID
	})
}

func ThreeWayRebase(oldBase, newBase, candidate state.Snapshot) (MergeResult, error) {
	oldBase, err := validatedMergeSnapshot("old base", oldBase)
	if err != nil {
		return MergeResult{}, err
	}
	if key, deleted := rawMutableDeletion(oldBase, newBase); deleted {
		return MergeResult{}, fmt.Errorf("projectstate: merge new base: %w: %s %s", ErrRawRecordDeletion, key.Kind, key.ID)
	}
	if key, deleted := rawMutableDeletion(oldBase, candidate); deleted {
		return MergeResult{}, fmt.Errorf("projectstate: merge candidate: %w: %s %s", ErrRawRecordDeletion, key.Kind, key.ID)
	}
	newBase, err = validatedMergeSnapshot("new base", newBase)
	if err != nil {
		return MergeResult{}, err
	}
	candidate, err = validatedMergeSnapshot("candidate", candidate)
	if err != nil {
		return MergeResult{}, err
	}
	if !sameMergeBinding(oldBase, newBase) || !sameMergeBinding(oldBase, candidate) {
		return MergeResult{}, fmt.Errorf("projectstate: merge binding mismatch")
	}
	if candidate.Config.Handle != oldBase.Config.Handle || !reflect.DeepEqual(candidate.Remotes, oldBase.Remotes) {
		return MergeResult{}, fmt.Errorf("projectstate: candidate changed Git-owned fields")
	}

	oldSurfaces, err := snapshotRecordSurfacesFromValidated(oldBase)
	if err != nil {
		return MergeResult{}, fmt.Errorf("projectstate: merge old base surfaces: %w", err)
	}
	newSurfaces, err := snapshotRecordSurfacesFromValidated(newBase)
	if err != nil {
		return MergeResult{}, fmt.Errorf("projectstate: merge new base surfaces: %w", err)
	}
	candidateSurfaces, err := snapshotRecordSurfacesFromValidated(candidate)
	if err != nil {
		return MergeResult{}, fmt.Errorf("projectstate: merge candidate surfaces: %w", err)
	}
	keys, err := recordSurfaceKeys(oldSurfaces, candidateSurfaces, newSurfaces)
	if err != nil {
		return MergeResult{}, fmt.Errorf("projectstate: merge record keys: %w", err)
	}
	shell := newMergeSnapshotShellFromValidated(newBase)
	conflicts := make([]Conflict, 0)
	for _, key := range keys {
		baseSurface, err := recordSurfaceAt(oldSurfaces, key)
		if err != nil {
			return MergeResult{}, fmt.Errorf("projectstate: merge %s %s base surface: %w", key.Kind, key.ID, err)
		}
		oursSurface, err := recordSurfaceAt(candidateSurfaces, key)
		if err != nil {
			return MergeResult{}, fmt.Errorf("projectstate: merge %s %s candidate surface: %w", key.Kind, key.ID, err)
		}
		theirsSurface, err := recordSurfaceAt(newSurfaces, key)
		if err != nil {
			return MergeResult{}, fmt.Errorf("projectstate: merge %s %s new base surface: %w", key.Kind, key.ID, err)
		}
		merged, err := mergeRecordSurface(key, baseSurface, oursSurface, theirsSurface)
		if err != nil {
			return MergeResult{}, fmt.Errorf("projectstate: merge %s %s: %w", key.Kind, key.ID, err)
		}
		if err := setRecordSurface(&shell, key, merged.Surface); err != nil {
			return MergeResult{}, fmt.Errorf("projectstate: merge %s %s result: %w", key.Kind, key.ID, err)
		}
		conflicts = append(conflicts, merged.Conflicts...)
	}
	sortConflicts(conflicts)
	if len(conflicts) != 0 {
		return MergeResult{Snapshot: candidate, Conflicts: conflicts}, nil
	}
	merged, err := canonicalMergeSnapshot(shell)
	if err != nil {
		return MergeResult{}, fmt.Errorf("projectstate: merge result: %w", err)
	}
	return MergeResult{Snapshot: merged, Conflicts: conflicts}, nil
}

func validatedMergeSnapshot(name string, snapshot state.Snapshot) (state.Snapshot, error) {
	validated, err := validatedDiffSnapshot(snapshot)
	if err != nil {
		return state.Snapshot{}, fmt.Errorf("projectstate: merge %s: %w", name, err)
	}
	return validated, nil
}

func sameMergeBinding(left, right state.Snapshot) bool {
	return left.Config.SnapshotVersion == right.Config.SnapshotVersion &&
		left.Config.ProjectID == right.Config.ProjectID &&
		left.Config.Repository == right.Config.Repository
}

func canonicalMergeSnapshot(snapshot state.Snapshot) (state.Snapshot, error) {
	tree, err := state.EncodeTree(snapshot)
	if err != nil {
		return state.Snapshot{}, err
	}
	canonical, err := state.DecodeTree(tree)
	if err != nil {
		return state.Snapshot{}, err
	}
	return canonical, nil
}
