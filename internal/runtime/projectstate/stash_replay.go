package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type StashReplayV1 struct {
	SchemaVersion            int               `json:"schema_version"`
	SelectedStartTree        state.Tree        `json:"selected_start_tree"`
	SelectedStartDigest      state.Digest      `json:"selected_start_digest"`
	InitialThroughGeneration int64             `json:"initial_through_generation"`
	AbsorbedOperations       []StoredOperation `json:"absorbed_operations"`
	Operations               []StoredOperation `json:"operations"`
}

func encodeStashReplay(value StashReplayV1, binding types.WorkspaceBinding, throughGeneration int64) (string, error) {
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("projectstate: encode stash replay: %w", err)
	}
	if _, err := decodeStashReplay(string(canonical), binding, throughGeneration); err != nil {
		return "", err
	}
	return string(canonical), nil
}

func decodeStashReplay(raw string, binding types.WorkspaceBinding, throughGeneration int64) (StashReplayV1, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var value StashReplayV1
	if err := decoder.Decode(&value); err != nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: decode stash replay: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return StashReplayV1{}, fmt.Errorf("projectstate: decode stash replay: trailing JSON")
		}
		return StashReplayV1{}, fmt.Errorf("projectstate: decode stash replay: %w", err)
	}
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: canonicalize stash replay: %w", err)
	}
	if !bytes.Equal(canonical, []byte(raw)) {
		return StashReplayV1{}, fmt.Errorf("projectstate: stash replay is not canonical")
	}
	if value.SchemaVersion != 1 {
		return StashReplayV1{}, fmt.Errorf("projectstate: unsupported stash replay schema version %d", value.SchemaVersion)
	}
	if err := binding.Validate(); err != nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: invalid stash replay binding: %w", err)
	}
	if err := validateMatchingTree(value.SelectedStartTree, value.SelectedStartDigest, binding); err != nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: invalid stash replay selected start tree: %w", err)
	}
	if value.AbsorbedOperations == nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: absorbed operations must be non-nil")
	}
	if value.Operations == nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: operations must be non-nil")
	}
	if value.InitialThroughGeneration < 0 {
		return StashReplayV1{}, fmt.Errorf("projectstate: initial through generation must be non-negative")
	}
	if throughGeneration < 0 {
		return StashReplayV1{}, fmt.Errorf("projectstate: through generation must be non-negative")
	}
	if value.InitialThroughGeneration > throughGeneration {
		return StashReplayV1{}, fmt.Errorf("projectstate: initial through generation %d exceeds through generation %d", value.InitialThroughGeneration, throughGeneration)
	}
	if err := validateReplayGenerations(value.AbsorbedOperations, value.InitialThroughGeneration, true); err != nil {
		return StashReplayV1{}, err
	}
	if err := validateReplayGenerations(value.Operations, value.InitialThroughGeneration, false); err != nil {
		return StashReplayV1{}, err
	}
	seenOperationIDs := make(map[string]struct{}, len(value.AbsorbedOperations)+len(value.Operations))
	if err := validateReplayOperations(value.AbsorbedOperations, seenOperationIDs); err != nil {
		return StashReplayV1{}, err
	}
	if err := validateReplayOperations(value.Operations, seenOperationIDs); err != nil {
		return StashReplayV1{}, err
	}
	if len(value.Operations) == 0 {
		if throughGeneration != value.InitialThroughGeneration {
			return StashReplayV1{}, fmt.Errorf("projectstate: empty operation suffix ends at generation %d, not %d", value.InitialThroughGeneration, throughGeneration)
		}
	} else if last := value.Operations[len(value.Operations)-1].Generation; last != throughGeneration {
		return StashReplayV1{}, fmt.Errorf("projectstate: operation suffix ends at generation %d, not %d", last, throughGeneration)
	}
	selectedStart, err := state.DecodeTree(value.SelectedStartTree)
	if err != nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: decode stash replay selected start tree: %w", err)
	}
	if _, err := Compose(selectedStart, value.InitialThroughGeneration, value.Operations); err != nil {
		return StashReplayV1{}, fmt.Errorf("projectstate: compose stash replay operation suffix: %w", err)
	}
	return value, nil
}

func validateReplayGenerations(operations []StoredOperation, boundary int64, absorbed bool) error {
	for index, operation := range operations {
		if operation.Generation <= 0 || (index > 0 && operation.Generation <= operations[index-1].Generation) {
			return fmt.Errorf("projectstate: stash replay generations are not strictly increasing positive values")
		}
		if absorbed && operation.Generation > boundary {
			return fmt.Errorf("projectstate: absorbed stash replay operation exceeds initial boundary")
		}
		if !absorbed && operation.Generation <= boundary {
			return fmt.Errorf("projectstate: stash replay operation does not exceed initial boundary")
		}
	}
	return nil
}

func validateReplayOperations(operations []StoredOperation, seenIDs map[string]struct{}) error {
	for _, stored := range operations {
		if !types.CanonicalUUID(stored.Operation.ID) {
			return fmt.Errorf("projectstate: invalid stash replay operation ID")
		}
		if _, duplicate := seenIDs[stored.Operation.ID]; duplicate {
			return fmt.Errorf("projectstate: duplicate stash replay operation ID")
		}
		seenIDs[stored.Operation.ID] = struct{}{}
		canonical, err := state.CanonicalOperation(stored.Operation)
		if err != nil {
			return fmt.Errorf("projectstate: canonicalize stash replay operation: %w", err)
		}
		decoded, err := state.DecodeOperation(canonical)
		if err != nil {
			return fmt.Errorf("projectstate: decode canonical stash replay operation: %w", err)
		}
		if decoded.ID != stored.Operation.ID {
			return fmt.Errorf("projectstate: canonical stash replay operation identity differs")
		}
	}
	return nil
}
