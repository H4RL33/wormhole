package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const PrivateWorkspaceRPCMethod = "wormhole.private.workspace"

type WorkspaceOperation string

const (
	WorkspaceOperationStatus     WorkspaceOperation = "status"
	WorkspaceOperationDiff       WorkspaceOperation = "diff"
	WorkspaceOperationImport     WorkspaceOperation = "import"
	WorkspaceOperationCheckpoint WorkspaceOperation = "checkpoint"
	WorkspaceOperationStash      WorkspaceOperation = "stash"
)

var ErrWorkspaceCommand = errors.New("localapi: invalid workspace command")

type WorkspaceCommandRequest struct {
	Operation               WorkspaceOperation `json:"operation"`
	PublicationReviewDigest string             `json:"publication_review_digest,omitempty"`
	RequestID               string             `json:"request_id,omitempty"`
	Label                   string             `json:"label,omitempty"`
}

type PrivateWorkspaceCommandRequest struct {
	WorkingDirectory string                  `json:"working_directory"`
	Command          WorkspaceCommandRequest `json:"command"`
}

type WorkspaceStatusReadback struct {
	ProjectID                 string                          `json:"project_id"`
	WorkspaceID               types.WorkspaceID               `json:"workspace_id"`
	State                     string                          `json:"state"`
	AcceptedCommitSHA         string                          `json:"accepted_commit_sha"`
	AcceptedTreeDigest        state.Digest                    `json:"accepted_tree_digest"`
	CandidatePresent          bool                            `json:"candidate_present"`
	CandidateDigest           state.Digest                    `json:"candidate_digest"`
	OverlayGeneration         int64                           `json:"overlay_generation"`
	PublicationClassification types.PublicationClassification `json:"publication_classification"`
	PublicationReviewDigest   state.Digest                    `json:"publication_review_digest"`
}

type WorkspaceDiffReadback struct {
	BaseDigest                state.Digest                    `json:"base_digest"`
	ViewDigest                state.Digest                    `json:"view_digest"`
	Changes                   []projectstate.Change           `json:"changes"`
	CandidateDigest           state.Digest                    `json:"candidate_digest"`
	OverlayGeneration         int64                           `json:"overlay_generation"`
	PublicationClassification types.PublicationClassification `json:"publication_classification"`
	PublicationReviewDigest   state.Digest                    `json:"publication_review_digest"`
}

type WorkspaceImportReadback struct {
	PreviousCandidateDigest  *state.Digest           `json:"previous_candidate_digest"`
	ImportedCandidateDigest  state.Digest            `json:"imported_candidate_digest"`
	ComposedViewDigest       state.Digest            `json:"composed_view_digest"`
	ImportedChangeCount      int                     `json:"imported_change_count"`
	RebasedThroughGeneration int64                   `json:"rebased_through_generation"`
	Conflicts                []projectstate.Conflict `json:"conflicts"`
}

type WorkspaceCheckpointReadback struct {
	CandidateDigest               state.Digest `json:"candidate_digest"`
	MaterializedThroughGeneration int64        `json:"materialized_through_generation"`
	JournalID                     string       `json:"journal_id"`
}

type WorkspaceStashReadback struct {
	StashID         string       `json:"stash_id"`
	SourceDigest    state.Digest `json:"source_digest"`
	CandidateDigest state.Digest `json:"candidate_digest"`
	OperationCount  int          `json:"operation_count"`
}

type WorkspaceCommandResult struct {
	Operation  WorkspaceOperation           `json:"operation"`
	Status     *WorkspaceStatusReadback     `json:"status,omitempty"`
	Diff       *WorkspaceDiffReadback       `json:"diff,omitempty"`
	Import     *WorkspaceImportReadback     `json:"import,omitempty"`
	Checkpoint *WorkspaceCheckpointReadback `json:"checkpoint,omitempty"`
	Stash      *WorkspaceStashReadback      `json:"stash,omitempty"`
}

func (s *Server) executeWorkspaceCommand(ctx context.Context, request WorkspaceCommandRequest) (WorkspaceCommandResult, error) {
	if s == nil || s.projectState == nil {
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
	if err := validateWorkspaceCommandRequest(request); err != nil {
		return WorkspaceCommandResult{}, err
	}
	binding, err := ResolvedBinding(ctx)
	if err != nil {
		return WorkspaceCommandResult{}, err
	}
	refreshed, err := s.projectState.RefreshWorkspace(ctx, binding)
	if err != nil {
		if request.Operation != WorkspaceOperationStash || !errors.Is(err, projectstate.ErrBranchSwitchPending) {
			return WorkspaceCommandResult{}, err
		}
		return s.executeBranchSwitchStash(ctx, binding, request)
	}
	binding = refreshed
	switch request.Operation {
	case WorkspaceOperationStatus:
		status, err := s.projectState.Status(ctx, binding.Scope)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		readback := workspaceStatusReadback(status)
		return WorkspaceCommandResult{Operation: request.Operation, Status: &readback}, nil
	case WorkspaceOperationDiff:
		diff, err := s.projectState.Diff(ctx, binding.Scope)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		readback := workspaceDiffReadback(diff)
		return WorkspaceCommandResult{Operation: request.Operation, Diff: &readback}, nil
	case WorkspaceOperationImport:
		actor, err := ServerOwnedActor(ctx)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		tree, err := projectstate.ReadWorkingTreeNoFollow(binding.Checkout.CanonicalPath)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		digest, err := state.DigestTree(tree)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		result, err := s.projectState.Import(ctx, projectstate.ImportRequest{
			Scope: binding.Scope, Root: binding.Checkout.CanonicalPath, ExpectedWorkingTreeDigest: &digest, Actor: actor,
		})
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		readback := workspaceImportReadback(result)
		return WorkspaceCommandResult{Operation: request.Operation, Import: &readback}, nil
	case WorkspaceOperationCheckpoint:
		actor, err := ServerOwnedActor(ctx)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		tree, err := projectstate.ReadWorkingTreeNoFollow(binding.Checkout.CanonicalPath)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		digest, err := state.DigestTree(tree)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		var acknowledgement *state.Digest
		if request.PublicationReviewDigest != "" {
			value := state.Digest(request.PublicationReviewDigest)
			acknowledgement = &value
		}
		result, err := s.projectState.Checkpoint(ctx, projectstate.CheckpointRequest{
			Scope: binding.Scope, Root: binding.Checkout.CanonicalPath, ExpectedWorkingTreeDigest: digest,
			PublicationReviewDigest: acknowledgement, Actor: actor,
		})
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		readback := WorkspaceCheckpointReadback{CandidateDigest: result.CandidateDigest, MaterializedThroughGeneration: result.MaterializedThroughGeneration, JournalID: result.JournalID}
		return WorkspaceCommandResult{Operation: request.Operation, Checkpoint: &readback}, nil
	case WorkspaceOperationStash:
		actor, err := ServerOwnedActor(ctx)
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		result, err := s.projectState.Stash(ctx, projectstate.StashRequest{Scope: binding.Scope, RequestID: request.RequestID, Actor: actor, Label: request.Label})
		if err != nil {
			return WorkspaceCommandResult{}, err
		}
		if _, err := s.projectState.Recover(ctx, binding.Scope); err != nil {
			return WorkspaceCommandResult{}, err
		}
		readback := WorkspaceStashReadback{StashID: result.StashID, SourceDigest: result.SourceDigest, CandidateDigest: result.CandidateDigest, OperationCount: result.OperationCount}
		return WorkspaceCommandResult{Operation: request.Operation, Stash: &readback}, nil
	default:
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
}

func validateWorkspaceCommandRequest(request WorkspaceCommandRequest) error {
	switch request.Operation {
	case WorkspaceOperationStatus, WorkspaceOperationDiff, WorkspaceOperationImport:
		if request.PublicationReviewDigest != "" || request.RequestID != "" || request.Label != "" {
			return ErrWorkspaceCommand
		}
	case WorkspaceOperationCheckpoint:
		if request.RequestID != "" || request.Label != "" {
			return ErrWorkspaceCommand
		}
	case WorkspaceOperationStash:
		if request.PublicationReviewDigest != "" {
			return ErrWorkspaceCommand
		}
	default:
		return ErrWorkspaceCommand
	}
	return nil
}

func (s *Server) executeBranchSwitchStash(ctx context.Context, binding types.WorkspaceBinding, request WorkspaceCommandRequest) (WorkspaceCommandResult, error) {
	actor, err := ServerOwnedActor(ctx)
	if err != nil {
		return WorkspaceCommandResult{}, err
	}
	result, err := s.projectState.Stash(ctx, projectstate.StashRequest{
		Scope: binding.Scope, RequestID: request.RequestID, Actor: actor, Label: request.Label,
	})
	if err != nil {
		return WorkspaceCommandResult{}, err
	}
	refreshed, err := s.projectState.RefreshWorkspace(ctx, binding)
	if err != nil {
		return WorkspaceCommandResult{}, err
	}
	if _, err := s.projectState.Recover(ctx, refreshed.Scope); err != nil {
		return WorkspaceCommandResult{}, err
	}
	readback := WorkspaceStashReadback{StashID: result.StashID, SourceDigest: result.SourceDigest, CandidateDigest: result.CandidateDigest, OperationCount: result.OperationCount}
	return WorkspaceCommandResult{Operation: request.Operation, Stash: &readback}, nil
}

func (s *Server) PrivateWorkspaceRPC(ctx context.Context, request PrivateWorkspaceCommandRequest) (WorkspaceCommandResult, error) {
	if err := validateWorkspaceCommandRequest(request.Command); err != nil {
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
	if s == nil || s.projectState == nil || s.identityStore == nil {
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
	observed := types.WorkspaceContext{WorkingDirectory: request.WorkingDirectory}
	if observed.Validate() != nil {
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
	binding, err := s.projectState.ResolveWorkingDirectory(ctx, observed)
	if err != nil || binding.Validate() != nil || binding.Checkout.CanonicalPath != request.WorkingDirectory {
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
	actor, err := s.identityStore.ResolveHumanActor(ctx, s.setupNow())
	if err != nil || actor.ValidateLocalAction() != nil {
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
	ctx = WithResolvedBinding(ctx, binding)
	ctx = withServerOwnedActor(ctx, actor)
	return s.executeWorkspaceCommand(ctx, request.Command)
}

func (s *Server) dispatchPrivateWorkspaceRPC(ctx context.Context, params json.RawMessage) (WorkspaceCommandResult, error) {
	var request PrivateWorkspaceCommandRequest
	if err := decodeClosedJSON(params, &request); err != nil {
		return WorkspaceCommandResult{}, ErrWorkspaceCommand
	}
	return s.PrivateWorkspaceRPC(ctx, request)
}

func workspaceStatusReadback(status projectstate.WorkspaceStatus) WorkspaceStatusReadback {
	return WorkspaceStatusReadback{
		ProjectID: status.Binding.Scope.ProjectID, WorkspaceID: status.Binding.Scope.WorkspaceID, State: status.State,
		AcceptedCommitSHA: status.Binding.AcceptedCommitSHA, AcceptedTreeDigest: status.AcceptedSnapshot.Digest,
		CandidatePresent: status.CandidatePresent, CandidateDigest: status.CandidateDigest, OverlayGeneration: status.OverlayGeneration,
		PublicationClassification: status.PublicationClassification, PublicationReviewDigest: status.PublicationReviewDigest,
	}
}

func workspaceDiffReadback(diff projectstate.WorkspaceDiff) WorkspaceDiffReadback {
	return WorkspaceDiffReadback{
		BaseDigest: diff.SemanticDiff.BaseDigest, ViewDigest: diff.SemanticDiff.ViewDigest,
		Changes: append([]projectstate.Change(nil), diff.SemanticDiff.Changes...), CandidateDigest: diff.CandidateDigest,
		OverlayGeneration: diff.OverlayGeneration, PublicationClassification: diff.PublicationClassification,
		PublicationReviewDigest: diff.PublicationReviewDigest,
	}
}

func workspaceImportReadback(result projectstate.ImportResult) WorkspaceImportReadback {
	return WorkspaceImportReadback{
		PreviousCandidateDigest: result.PreviousCandidateDigest, ImportedCandidateDigest: result.ImportedCandidateDigest,
		ComposedViewDigest: result.ComposedViewDigest, ImportedChangeCount: result.ImportedChangeCount,
		RebasedThroughGeneration: result.RebasedThroughGeneration, Conflicts: append([]projectstate.Conflict(nil), result.Conflicts...),
	}
}

func decodeWorkspaceToolArguments(raw json.RawMessage, destination any) error {
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arguments); err != nil || arguments == nil {
		return ErrWorkspaceCommand
	}
	delete(arguments, "project_id")
	public, err := json.Marshal(arguments)
	if err != nil || decodeClosedJSON(public, destination) != nil {
		return ErrWorkspaceCommand
	}
	return nil
}

func workspaceToolResult(result WorkspaceCommandResult) (any, error) {
	switch result.Operation {
	case WorkspaceOperationStatus:
		return result.Status, nil
	case WorkspaceOperationDiff:
		return result.Diff, nil
	case WorkspaceOperationImport:
		return result.Import, nil
	case WorkspaceOperationCheckpoint:
		return result.Checkpoint, nil
	case WorkspaceOperationStash:
		return result.Stash, nil
	default:
		return nil, fmt.Errorf("%w: result operation", ErrWorkspaceCommand)
	}
}
