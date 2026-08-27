package projectstate

import (
	"fmt"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type StoredOperation struct {
	Generation int64             `json:"generation"`
	Operation  state.OperationV1 `json:"operation"`
}

type ComposedView struct {
	Snapshot            state.Snapshot
	AppliedOperationIDs []string
	ThroughGeneration   int64
}

func Compose(start state.Snapshot, initialThroughGeneration int64, operations []StoredOperation) (ComposedView, error) {
	if initialThroughGeneration < 0 {
		return ComposedView{}, fmt.Errorf("projectstate: compose: negative initial generation")
	}
	tree, err := state.EncodeTree(start)
	if err != nil {
		return ComposedView{}, fmt.Errorf("projectstate: compose: validate start: %w", err)
	}
	current, err := state.DecodeTree(tree)
	if err != nil {
		return ComposedView{}, fmt.Errorf("projectstate: compose: clone start: %w", err)
	}
	if current.Digest != start.Digest {
		return ComposedView{}, fmt.Errorf("projectstate: compose: start digest mismatch")
	}

	result := ComposedView{
		Snapshot:            current,
		AppliedOperationIDs: make([]string, 0, len(operations)),
		ThroughGeneration:   initialThroughGeneration,
	}
	for _, stored := range operations {
		if stored.Generation <= 0 || stored.Generation <= result.ThroughGeneration {
			return ComposedView{}, fmt.Errorf("projectstate: compose: generation %d does not follow %d", stored.Generation, result.ThroughGeneration)
		}
		next, applyErr := state.ApplyOperation(result.Snapshot, stored.Operation)
		if applyErr != nil {
			return ComposedView{}, fmt.Errorf("projectstate: compose generation %d: %w", stored.Generation, applyErr)
		}
		result.Snapshot = next
		result.AppliedOperationIDs = append(result.AppliedOperationIDs, stored.Operation.ID)
		result.ThroughGeneration = stored.Generation
	}
	return result, nil
}
