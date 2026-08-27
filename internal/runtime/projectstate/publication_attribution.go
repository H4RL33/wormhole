package projectstate

import (
	"fmt"
	"strings"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type publicationFieldTouch struct {
	key   state.RecordKey
	paths []string
	actor *types.ActorEnvelope
}

type publicationLifecycleActor struct {
	direct         bool
	started        bool
	mixed          bool
	actor          *types.ActorEnvelope
	canonicalActor string
}

// publicationAttributedDiff composes the selected candidate start and derives
// publication-only per-field attribution. Callers must supply only active
// operations after boundary, in increasing generation order.
func publicationAttributedDiff(
	accepted state.Snapshot,
	selectedStart state.Snapshot,
	boundary int64,
	operations []StoredOperation,
) (ComposedView, Diff, error) {
	view, err := Compose(selectedStart, boundary, nil)
	if err != nil {
		return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication attribution start: %w", err)
	}
	directDiff, err := SemanticDiff(accepted, view.Snapshot, nil)
	if err != nil {
		return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication attribution direct prefix: %w", err)
	}

	touches := make([]publicationFieldTouch, 0, len(directDiff.Changes)+len(operations))
	lifecycles := make(map[state.RecordKey]publicationLifecycleActor)
	for _, change := range directDiff.Changes {
		paths := publicationChangePaths(change)
		touches = append(touches, publicationFieldTouch{key: change.Key, paths: paths})
		lifecycle := lifecycles[change.Key]
		lifecycle.direct = true
		lifecycles[change.Key] = lifecycle
	}

	view.AppliedOperationIDs = make([]string, 0, len(operations))
	seenOperationIDs := make(map[string]struct{}, len(operations))
	for _, stored := range operations {
		if stored.Generation <= 0 || stored.Generation <= view.ThroughGeneration {
			return ComposedView{}, Diff{}, fmt.Errorf(
				"projectstate: publication attribution generation %d does not follow %d",
				stored.Generation, view.ThroughGeneration,
			)
		}
		if _, duplicate := seenOperationIDs[stored.Operation.ID]; duplicate {
			return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication attribution duplicate operation ID %q", stored.Operation.ID)
		}
		seenOperationIDs[stored.Operation.ID] = struct{}{}

		next, applyErr := state.ApplyOperation(view.Snapshot, stored.Operation)
		if applyErr != nil {
			return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication attribution generation %d: %w", stored.Generation, applyErr)
		}
		stepDiff, diffErr := SemanticDiff(view.Snapshot, next, nil)
		if diffErr != nil {
			return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication attribution generation %d diff: %w", stored.Generation, diffErr)
		}
		for _, change := range stepDiff.Changes {
			paths := publicationChangePaths(change)
			actor := stored.Operation.Actor
			touches = append(touches, publicationFieldTouch{
				key: change.Key, paths: paths, actor: publicationActorCopy(actor),
			})
			lifecycle := lifecycles[change.Key]
			startsLifecycle := publicationContainsRootPath(paths)
			if startsLifecycle && !lifecycle.started {
				lifecycle.started = true
			}
			if err := lifecycle.add(actor); err != nil {
				return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication lifecycle actor: %w", err)
			}
			lifecycles[change.Key] = lifecycle
		}
		view.Snapshot = next
		view.AppliedOperationIDs = append(view.AppliedOperationIDs, stored.Operation.ID)
		view.ThroughGeneration = stored.Generation
	}

	result, err := SemanticDiff(accepted, view.Snapshot, nil)
	if err != nil {
		return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication attribution final diff: %w", err)
	}
	if result.BaseDigest != accepted.Digest || result.ViewDigest != view.Snapshot.Digest {
		return ComposedView{}, Diff{}, fmt.Errorf("projectstate: publication attribution final composition digest mismatch")
	}
	for changeIndex := range result.Changes {
		change := &result.Changes[changeIndex]
		for fieldIndex := range change.Fields {
			field := &change.Fields[fieldIndex]
			if field.Path == "" {
				lifecycle := lifecycles[change.Key]
				if !lifecycle.direct && lifecycle.started && !lifecycle.mixed && lifecycle.actor != nil {
					field.Actor = publicationActorCopy(*lifecycle.actor)
				}
				continue
			}
			for _, touch := range touches {
				if touch.key != change.Key || !publicationPathsOverlapAny(field.Path, touch.paths) {
					continue
				}
				if touch.actor == nil {
					field.Actor = nil
				} else {
					field.Actor = publicationActorCopy(*touch.actor)
				}
			}
		}
		changeActor, actorErr := publicationCommonFieldActor(change.Fields)
		if actorErr != nil {
			return ComposedView{}, Diff{}, actorErr
		}
		change.Actor = changeActor
	}
	return view, result, nil
}

func (lifecycle *publicationLifecycleActor) add(actor types.ActorEnvelope) error {
	canonical, err := publicationCanonicalActor(actor)
	if err != nil {
		return err
	}
	if lifecycle.actor == nil {
		lifecycle.actor = publicationActorCopy(actor)
		lifecycle.canonicalActor = canonical
		return nil
	}
	if lifecycle.canonicalActor != canonical {
		lifecycle.mixed = true
	}
	return nil
}

func publicationCommonFieldActor(fields []FieldChange) (*types.ActorEnvelope, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	var common *types.ActorEnvelope
	var canonical string
	for _, field := range fields {
		if field.Actor == nil {
			return nil, nil
		}
		fieldCanonical, err := publicationCanonicalActor(*field.Actor)
		if err != nil {
			return nil, fmt.Errorf("projectstate: publication field actor: %w", err)
		}
		if common == nil {
			common = publicationActorCopy(*field.Actor)
			canonical = fieldCanonical
			continue
		}
		if canonical != fieldCanonical {
			return nil, nil
		}
	}
	return common, nil
}

func publicationCanonicalActor(actor types.ActorEnvelope) (string, error) {
	if err := actor.Validate(); err != nil {
		return "", err
	}
	canonical, err := state.CanonicalJSON(actor)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func publicationActorCopy(actor types.ActorEnvelope) *types.ActorEnvelope {
	copy := actor
	return &copy
}

func publicationChangePaths(change Change) []string {
	paths := make([]string, len(change.Fields))
	for index, field := range change.Fields {
		paths[index] = field.Path
	}
	return paths
}

func publicationContainsRootPath(paths []string) bool {
	for _, path := range paths {
		if path == "" {
			return true
		}
	}
	return false
}

func publicationPathsOverlapAny(path string, candidates []string) bool {
	for _, candidate := range candidates {
		if publicationPathsOverlap(path, candidate) {
			return true
		}
	}
	return false
}

func publicationPathsOverlap(left, right string) bool {
	if left == "" || right == "" || left == right {
		return true
	}
	return strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}
