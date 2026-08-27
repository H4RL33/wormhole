package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	PrivateSetupRegisterWorkspaceRPCMethod = "wormhole.private.setup.register_workspace"
	PrivateSetupEnsureIdentityRPCMethod    = "wormhole.private.setup.ensure_identity"
	PrivateSetupPublicationRPCMethod       = "wormhole.private.setup.publication"
	PrivateSetupImportRPCMethod            = "wormhole.private.setup.import"
	PrivateSetupVerifyRPCMethod            = "wormhole.private.setup.verify"
)

var (
	ErrPrivateRuntimeAlreadyConfigured = errors.New("localapi: private runtime already configured")
	ErrIdentityRootInsideRepository    = errors.New("localapi: identity root must be outside every repository")
	ErrPrivateSetupRequest             = errors.New("localapi: private setup request failed")
)

type SetupWorkspaceRequest struct {
	WorkingDirectory    string                   `json:"working_directory"`
	ExpectedProjectID   string                   `json:"expected_project_id"`
	ExpectedRepository  types.RepositoryIdentity `json:"expected_repository"`
	ExpectedCommit      string                   `json:"expected_commit"`
	ExpectedPriorDigest config.StateDigest       `json:"expected_prior_digest"`
}

type SetupIdentityRequest struct {
	WorkingDirectory    string                           `json:"working_directory"`
	JournalID           string                           `json:"journal_id"`
	Selection           types.ConfirmedIdentitySelection `json:"selection"`
	ExpectedPriorDigest config.StateDigest               `json:"expected_prior_digest"`
}

func DigestSetupWorkspaceAbsent() config.StateDigest {
	return config.SHA256StateDigest([]byte("setup-workspace:v1:absent"))
}

func DigestSetupIdentityUnselected() config.StateDigest {
	return config.SHA256StateDigest([]byte("setup-identity:v1:unselected"))
}

type SetupPublicationRequest struct {
	WorkingDirectory      string                          `json:"working_directory"`
	Classification        types.PublicationClassification `json:"classification"`
	ExpectedBindingDigest config.StateDigest              `json:"expected_binding_digest"`
	ExpectedPriorDigest   config.StateDigest              `json:"expected_prior_digest"`
}

type SetupPublicationPredicate struct {
	Classification       types.PublicationClassification `json:"classification"`
	PolicyRevision       int64                           `json:"policy_revision"`
	ObservedOriginDigest state.Digest                    `json:"observed_origin_digest"`
	ConfiguredOrigin     state.Digest                    `json:"configured_origin_digest,omitempty"`
	TransitionKind       string                          `json:"transition_kind"`
	ChangedByHumanID     string                          `json:"changed_by_human_id,omitempty"`
	ChangedAt            string                          `json:"changed_at,omitempty"`
}

func DigestSetupPublicationPredicate(predicate SetupPublicationPredicate) config.StateDigest {
	data, err := json.Marshal(predicate)
	if err != nil {
		return ""
	}
	return config.SHA256StateDigest(data)
}

type SetupWorkingDirectoryRequest struct {
	WorkingDirectory string                           `json:"working_directory"`
	Identity         types.ConfirmedIdentitySelection `json:"identity"`
	ExpectedTree     state.Digest                     `json:"expected_tree"`
}

type SetupImportRequest struct {
	WorkingDirectory    string             `json:"working_directory"`
	ExpectedCommitSHA   string             `json:"expected_commit_sha"`
	ExpectedTreeDigest  state.Digest       `json:"expected_tree_digest"`
	ExpectedPriorDigest config.StateDigest `json:"expected_prior_digest"`
	DesiredDigest       config.StateDigest `json:"desired_digest"`
}

type SetupBasePredicate struct {
	CandidatePresent  bool         `json:"candidate_present"`
	CandidateDigest   state.Digest `json:"candidate_digest"`
	OverlayGeneration int64        `json:"overlay_generation"`
	WorkspaceState    string       `json:"workspace_state"`
}

func DigestSetupBasePredicate(predicate SetupBasePredicate) config.StateDigest {
	data, err := json.Marshal(predicate)
	if err != nil {
		return ""
	}
	return config.SHA256StateDigest(data)
}

type SetupWorkspaceReadback struct {
	ProjectID          string            `json:"project_id"`
	WorkspaceID        types.WorkspaceID `json:"workspace_id"`
	AcceptedCommitSHA  string            `json:"accepted_commit_sha"`
	AcceptedTreeDigest string            `json:"accepted_tree_digest"`
	State              string            `json:"state"`
}

type SetupIdentityReadback struct {
	HumanPrincipalID string `json:"human_principal_id"`
	DisplayName      string `json:"display_name"`
	PublicKey        []byte `json:"public_key"`
}

type SetupPublicationReadback struct {
	Classification   types.PublicationClassification `json:"classification"`
	BindingDigest    config.StateDigest              `json:"binding_digest"`
	PolicyRevision   int64                           `json:"policy_revision"`
	TransitionKind   string                          `json:"transition_kind"`
	ChangedByHumanID string                          `json:"changed_by_human_id"`
}

type SetupImportReadback struct {
	AcceptedCommitSHA       string       `json:"accepted_commit_sha"`
	AcceptedTreeDigest      string       `json:"accepted_tree_digest"`
	ImportedCandidateDigest state.Digest `json:"imported_candidate_digest"`
	ImportedChangeCount     int          `json:"imported_change_count"`
	Conflicted              bool         `json:"conflicted"`
}

type SetupVerifyReadback struct {
	Workspace        SetupWorkspaceReadback   `json:"workspace"`
	Identity         SetupIdentityReadback    `json:"identity"`
	Publication      SetupPublicationReadback `json:"publication"`
	CandidatePresent bool                     `json:"candidate_present"`
	CandidateDigest  state.Digest             `json:"candidate_digest"`
}

// ConfigurePrivateRuntime installs the only binding and actor authorities used
// by the Stage-2 supervisor. It is a one-time pre-Serve operation.
func (s *Server) ConfigurePrivateRuntime(projectState *projectstate.Service, identity *localidentity.Store) error {
	if s == nil || projectState == nil || identity == nil {
		return fmt.Errorf("localapi: complete private runtime is required")
	}
	if s.projectState != nil || s.actorResolver != nil || s.identityStore != nil {
		return ErrPrivateRuntimeAlreadyConfigured
	}
	s.projectState = projectState
	s.actorResolver = identity
	s.identityStore = identity
	return nil
}

func (s *Server) PrivateSetupRegisterWorkspaceRPC(ctx context.Context, req SetupWorkspaceRequest) (SetupWorkspaceReadback, error) {
	if s == nil || s.projectState == nil || (types.WorkspaceContext{WorkingDirectory: req.WorkingDirectory}).Validate() != nil ||
		!types.CanonicalUUID(req.ExpectedProjectID) || req.ExpectedRepository.Validate() != nil {
		return SetupWorkspaceReadback{}, ErrPrivateSetupRequest
	}
	binding, err := s.projectState.ResolveWorkingDirectory(ctx, types.WorkspaceContext{WorkingDirectory: req.WorkingDirectory})
	if err != nil {
		if !errors.Is(err, localstore.ErrNotFound) || req.ExpectedPriorDigest != DigestSetupWorkspaceAbsent() {
			return SetupWorkspaceReadback{}, config.ErrConfirmedPlanDrift
		}
		registered, registerErr := s.projectState.RegisterWorkspace(ctx, projectstate.RegisterWorkspaceRequest{
			Root: req.WorkingDirectory, ExpectedProjectID: req.ExpectedProjectID,
			ExpectedRepository: req.ExpectedRepository, ExpectedCommit: req.ExpectedCommit,
		})
		if registerErr != nil {
			if errors.Is(registerErr, projectstate.ErrInvalidRegistration) || errors.Is(registerErr, projectstate.ErrWorkingTreeChanged) {
				return SetupWorkspaceReadback{}, config.ErrConfirmedPlanDrift
			}
			return SetupWorkspaceReadback{}, ErrPrivateSetupRequest
		}
		binding = registered.Binding
	}
	if binding.Validate() != nil || binding.Checkout.CanonicalPath != req.WorkingDirectory || binding.Scope.ProjectID != req.ExpectedProjectID ||
		binding.Repository != req.ExpectedRepository || binding.AcceptedCommitSHA != req.ExpectedCommit {
		return SetupWorkspaceReadback{}, config.ErrConfirmedPlanDrift
	}
	return setupWorkspaceReadback(binding, "registered"), nil
}

func (s *Server) PrivateSetupEnsureIdentityRPC(ctx context.Context, req SetupIdentityRequest) (SetupIdentityReadback, error) {
	if s == nil || s.identityStore == nil || s.projectState == nil || s.actorResolver == nil {
		return SetupIdentityReadback{}, ErrPrivateSetupRequest
	}
	if _, _, err := s.resolveSetupBinding(ctx, req.WorkingDirectory, false); err != nil {
		return SetupIdentityReadback{}, err
	}
	selected, selectedErr := s.identityStore.Selected(ctx)
	if selectedErr != nil && !errors.Is(selectedErr, localidentity.ErrNoSelectedIdentity) {
		return SetupIdentityReadback{}, ErrPrivateSetupRequest
	}
	if errors.Is(selectedErr, localidentity.ErrNoSelectedIdentity) && req.ExpectedPriorDigest != DigestSetupIdentityUnselected() {
		return SetupIdentityReadback{}, config.ErrConfirmedPlanDrift
	}
	if selectedErr == nil && selected.DisplayName != req.Selection.DisplayName {
		return SetupIdentityReadback{}, config.ErrConfirmedPlanDrift
	}
	actor, actorErr := s.actorResolver.ResolveLocalActor(ctx, ConnectionIdentity{OccurredAt: s.setupNow()})
	if actorErr != nil && !errors.Is(actorErr, localidentity.ErrNoSelectedIdentity) {
		return SetupIdentityReadback{}, ErrPrivateSetupRequest
	}
	if actorErr == nil && actor.ValidateLocalAction() != nil {
		return SetupIdentityReadback{}, ErrPrivateSetupRequest
	}
	if selectedErr == nil && (actorErr != nil || actor.ActorKind != types.ActorHuman || actor.HumanPrincipalID != selected.HumanPrincipalID) {
		return SetupIdentityReadback{}, ErrPrivateSetupRequest
	}
	if errors.Is(selectedErr, localidentity.ErrNoSelectedIdentity) && actorErr == nil {
		return SetupIdentityReadback{}, ErrPrivateSetupRequest
	}
	profile, err := s.identityStore.EnsureSelectedForSetup(ctx, req.JournalID, req.Selection)
	if err != nil {
		if errors.Is(err, localidentity.ErrSetupIdentityDrift) {
			return SetupIdentityReadback{}, config.ErrConfirmedPlanDrift
		}
		return SetupIdentityReadback{}, err
	}
	if actorErr != nil {
		_, actor, err = s.resolveSetupBinding(ctx, req.WorkingDirectory, true)
		if err != nil {
			return SetupIdentityReadback{}, err
		}
	}
	if actor.ActorKind != types.ActorHuman || actor.HumanPrincipalID != profile.HumanPrincipalID {
		return SetupIdentityReadback{}, ErrPrivateSetupRequest
	}
	return setupIdentityReadback(profile), nil
}

func (s *Server) PrivateSetupPublicationRPC(ctx context.Context, req SetupPublicationRequest) (SetupPublicationReadback, error) {
	if req.Classification.Validate() != nil {
		return SetupPublicationReadback{}, ErrPrivateSetupRequest
	}
	binding, actor, err := s.resolveSetupBinding(ctx, req.WorkingDirectory, true)
	if err != nil {
		return SetupPublicationReadback{}, err
	}
	current, err := s.projectState.PublicationConfiguration(ctx, binding.Scope)
	if err != nil {
		return SetupPublicationReadback{}, ErrPrivateSetupRequest
	}
	constraint, err := projectstate.DigestPublicationBindingConstraint(binding.Repository, current.ObservedOriginDigest)
	if err != nil || config.StateDigest(constraint) != req.ExpectedBindingDigest {
		return SetupPublicationReadback{}, config.ErrConfirmedPlanDrift
	}
	desired := current.Classification == req.Classification && current.TransitionKind == "configured" && current.ChangedBy != nil && current.ChangedBy.ActorKind == types.ActorHuman && current.ChangedBy.HumanPrincipalID == actor.HumanPrincipalID
	if !desired {
		if digestSetupPublicationConfiguration(current) != req.ExpectedPriorDigest {
			return SetupPublicationReadback{}, config.ErrConfirmedPlanDrift
		}
		current, err = s.projectState.ReconfigurePublication(ctx, projectstate.ReconfigurePublicationRequest{
			Scope: binding.Scope, ExpectedBinding: binding, ExpectedPublicationBindingDigest: constraint,
			Expected: current, Classification: req.Classification, Actor: actor,
		})
		if err != nil {
			if errors.Is(err, projectstate.ErrPublicationConfigurationInvalidated) || errors.Is(err, projectstate.ErrPublicationConfigurationCAS) {
				return SetupPublicationReadback{}, config.ErrConfirmedPlanDrift
			}
			return SetupPublicationReadback{}, ErrPrivateSetupRequest
		}
	}
	return setupPublicationReadback(binding, current)
}

func digestSetupPublicationConfiguration(publication projectstate.PublicationConfiguration) config.StateDigest {
	predicate := SetupPublicationPredicate{
		Classification: publication.Classification, PolicyRevision: publication.PolicyRevision,
		ObservedOriginDigest: publication.ObservedOriginDigest, TransitionKind: publication.TransitionKind,
	}
	if publication.ConfiguredOriginDigest != nil {
		predicate.ConfiguredOrigin = *publication.ConfiguredOriginDigest
	}
	if publication.ChangedBy != nil && publication.ChangedBy.ActorKind == types.ActorHuman {
		predicate.ChangedByHumanID = publication.ChangedBy.HumanPrincipalID
	}
	if publication.ChangedAt != nil {
		predicate.ChangedAt = publication.ChangedAt.UTC().Format(time.RFC3339Nano)
	}
	return DigestSetupPublicationPredicate(predicate)
}

func (s *Server) PrivateSetupImportRPC(ctx context.Context, req SetupImportRequest) (SetupImportReadback, error) {
	binding, actor, err := s.resolveSetupBinding(ctx, req.WorkingDirectory, true)
	if err != nil {
		return SetupImportReadback{}, err
	}
	if binding.AcceptedCommitSHA != req.ExpectedCommitSHA || binding.AcceptedTreeDigest != string(req.ExpectedTreeDigest) {
		return SetupImportReadback{}, config.ErrConfirmedPlanDrift
	}
	exactPrior := DigestSetupBasePredicate(SetupBasePredicate{CandidatePresent: false, CandidateDigest: req.ExpectedTreeDigest, WorkspaceState: "clean"})
	exactDesired := DigestSetupBasePredicate(SetupBasePredicate{CandidatePresent: true, CandidateDigest: req.ExpectedTreeDigest, WorkspaceState: "pending"})
	if req.ExpectedPriorDigest != exactPrior || req.DesiredDigest != exactDesired {
		return SetupImportReadback{}, config.ErrConfirmedPlanDrift
	}
	if s.beforeSetupImportTransaction != nil {
		if err := s.beforeSetupImportTransaction(ctx); err != nil {
			return SetupImportReadback{}, ErrPrivateSetupRequest
		}
	}
	reconciled, err := s.projectState.ReconcileImport(ctx, projectstate.ReconcileImportRequest{
		Import:        projectstate.ImportRequest{Scope: binding.Scope, Root: binding.Checkout.CanonicalPath, ExpectedWorkingTreeDigest: &req.ExpectedTreeDigest, Actor: actor},
		ExpectedPrior: projectstate.ImportWorkspacePredicate{CandidatePresent: false, CandidateDigest: req.ExpectedTreeDigest, WorkspaceState: "clean"},
		Desired:       projectstate.ImportWorkspacePredicate{CandidatePresent: true, CandidateDigest: req.ExpectedTreeDigest, WorkspaceState: "pending"},
	})
	if err != nil {
		if errors.Is(err, projectstate.ErrWorkingTreeChanged) || errors.Is(err, projectstate.ErrImportStateDrift) {
			return SetupImportReadback{}, config.ErrConfirmedPlanDrift
		}
		return SetupImportReadback{}, ErrPrivateSetupRequest
	}
	if reconciled.Status.Binding != binding || DigestSetupBasePredicate(setupBasePredicate(reconciled.Status)) != req.DesiredDigest {
		return SetupImportReadback{}, config.ErrConfirmedPlanDrift
	}
	return SetupImportReadback{
		AcceptedCommitSHA: binding.AcceptedCommitSHA, AcceptedTreeDigest: binding.AcceptedTreeDigest,
		ImportedCandidateDigest: reconciled.Status.CandidateDigest, ImportedChangeCount: reconciled.Import.ImportedChangeCount,
		Conflicted: reconciled.Status.State == "conflicted",
	}, nil
}

func setupBasePredicate(status projectstate.WorkspaceStatus) SetupBasePredicate {
	return SetupBasePredicate{CandidatePresent: status.CandidatePresent, CandidateDigest: status.CandidateDigest, OverlayGeneration: status.OverlayGeneration, WorkspaceState: status.State}
}

func (s *Server) PrivateSetupVerifyRPC(ctx context.Context, req SetupWorkingDirectoryRequest) (SetupVerifyReadback, error) {
	binding, actor, err := s.resolveSetupBinding(ctx, req.WorkingDirectory, true)
	if err != nil {
		return SetupVerifyReadback{}, err
	}
	status, err := s.projectState.Status(ctx, binding.Scope)
	if err != nil || status.Binding != binding {
		return SetupVerifyReadback{}, ErrPrivateSetupRequest
	}
	profile, matches, err := s.identityStore.SelectedMatchesSetup(ctx, req.Identity)
	if err != nil || !matches || profile.HumanPrincipalID != actor.HumanPrincipalID || !status.CandidatePresent || status.CandidateDigest != req.ExpectedTree {
		return SetupVerifyReadback{}, ErrPrivateSetupRequest
	}
	publication, err := s.projectState.PublicationConfiguration(ctx, binding.Scope)
	if err != nil {
		return SetupVerifyReadback{}, ErrPrivateSetupRequest
	}
	publicationReadback, err := setupPublicationReadback(binding, publication)
	if err != nil {
		return SetupVerifyReadback{}, err
	}
	return SetupVerifyReadback{
		Workspace: setupWorkspaceReadback(binding, status.State),
		Identity:  setupIdentityReadback(profile), Publication: publicationReadback, CandidatePresent: status.CandidatePresent, CandidateDigest: status.CandidateDigest,
	}, nil
}

func (s *Server) resolveSetupBinding(ctx context.Context, workingDirectory string, withActor bool) (types.WorkspaceBinding, types.ActorEnvelope, error) {
	if s == nil || s.projectState == nil || (types.WorkspaceContext{WorkingDirectory: workingDirectory}).Validate() != nil {
		return types.WorkspaceBinding{}, types.ActorEnvelope{}, ErrPrivateSetupRequest
	}
	binding, err := s.projectState.ResolveWorkingDirectory(ctx, types.WorkspaceContext{WorkingDirectory: workingDirectory})
	if err != nil || binding.Validate() != nil || binding.Checkout.CanonicalPath != workingDirectory {
		return types.WorkspaceBinding{}, types.ActorEnvelope{}, ErrPrivateSetupRequest
	}
	if !withActor {
		return binding, types.ActorEnvelope{}, nil
	}
	if s.actorResolver == nil {
		return types.WorkspaceBinding{}, types.ActorEnvelope{}, ErrPrivateSetupRequest
	}
	actor, err := s.actorResolver.ResolveLocalActor(ctx, ConnectionIdentity{OccurredAt: s.setupNow()})
	if err != nil || actor.ValidateLocalAction() != nil {
		return types.WorkspaceBinding{}, types.ActorEnvelope{}, ErrPrivateSetupRequest
	}
	return binding, actor, nil
}

func (s *Server) setupNow() time.Time {
	now := time.Now().UTC()
	if s != nil && s.clock != nil {
		now = s.clock().UTC()
	}
	return now
}

func setupWorkspaceReadback(binding types.WorkspaceBinding, workspaceState string) SetupWorkspaceReadback {
	return SetupWorkspaceReadback{ProjectID: binding.Scope.ProjectID, WorkspaceID: binding.Scope.WorkspaceID, AcceptedCommitSHA: binding.AcceptedCommitSHA, AcceptedTreeDigest: binding.AcceptedTreeDigest, State: workspaceState}
}

func setupIdentityReadback(profile localidentity.PublicHumanProfile) SetupIdentityReadback {
	return SetupIdentityReadback{HumanPrincipalID: profile.HumanPrincipalID, DisplayName: profile.DisplayName, PublicKey: append([]byte{}, profile.PublicKey...)}
}

func setupPublicationReadback(binding types.WorkspaceBinding, publication projectstate.PublicationConfiguration) (SetupPublicationReadback, error) {
	constraint, err := projectstate.DigestPublicationBindingConstraint(binding.Repository, publication.ObservedOriginDigest)
	if err != nil || publication.ChangedBy == nil || publication.ChangedBy.ActorKind != types.ActorHuman {
		return SetupPublicationReadback{}, ErrPrivateSetupRequest
	}
	return SetupPublicationReadback{
		Classification: publication.Classification, BindingDigest: config.StateDigest(constraint), PolicyRevision: publication.PolicyRevision,
		TransitionKind: publication.TransitionKind, ChangedByHumanID: publication.ChangedBy.HumanPrincipalID,
	}, nil
}

func (s *Server) dispatchPrivateSetupRPC(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case PrivateSetupRegisterWorkspaceRPCMethod:
		var request SetupWorkspaceRequest
		if decodeClosedJSON(params, &request) != nil {
			return nil, ErrPrivateSetupRequest
		}
		return s.PrivateSetupRegisterWorkspaceRPC(ctx, request)
	case PrivateSetupEnsureIdentityRPCMethod:
		var request SetupIdentityRequest
		if decodeClosedJSON(params, &request) != nil {
			return nil, ErrPrivateSetupRequest
		}
		return s.PrivateSetupEnsureIdentityRPC(ctx, request)
	case PrivateSetupPublicationRPCMethod:
		var request SetupPublicationRequest
		if decodeClosedJSON(params, &request) != nil {
			return nil, ErrPrivateSetupRequest
		}
		return s.PrivateSetupPublicationRPC(ctx, request)
	case PrivateSetupImportRPCMethod:
		var request SetupImportRequest
		if decodeClosedJSON(params, &request) != nil {
			return nil, ErrPrivateSetupRequest
		}
		return s.PrivateSetupImportRPC(ctx, request)
	case PrivateSetupVerifyRPCMethod:
		var request SetupWorkingDirectoryRequest
		if decodeClosedJSON(params, &request) != nil {
			return nil, ErrPrivateSetupRequest
		}
		return s.PrivateSetupVerifyRPC(ctx, request)
	default:
		return nil, ErrPrivateSetupRequest
	}
}

// OpenProductionIdentityStore derives identity authority only from Gateway's
// owner-private data-home configuration. It accepts no checkout or request
// path and rejects an XDG data root nested in a repository.
func OpenProductionIdentityStore() (*localidentity.Store, error) {
	paths, err := config.ResolveRuntimePaths()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(filepath.Dir(paths.DBPath), "identities")
	contained, err := pathInsideRepository(root)
	if err != nil {
		return nil, err
	}
	if contained {
		return nil, ErrIdentityRootInsideRepository
	}
	return localidentity.Open(root)
}

func pathInsideRepository(candidate string) (bool, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return false, fmt.Errorf("localapi: resolve identity root: %w", err)
	}
	for directory := filepath.Clean(absolute); ; directory = filepath.Dir(directory) {
		if info, statErr := os.Lstat(filepath.Join(directory, ".git")); statErr == nil {
			if info.IsDir() || info.Mode().IsRegular() {
				return true, nil
			}
			return false, fmt.Errorf("localapi: inspect repository marker")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return false, fmt.Errorf("localapi: inspect repository boundary: %w", statErr)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false, nil
		}
	}
}
