package projectstate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrPublicationConfigurationCAS         = localstore.ErrPublicationConfigurationCAS
	ErrPublicationConfigurationInvalidated = errors.New("projectstate: publication configuration invalidated")
)

type PublicationConfiguration struct {
	Classification         types.PublicationClassification
	PolicyRevision         int64
	ObservedOriginDigest   state.Digest
	ConfiguredOriginDigest *state.Digest
	TransitionKind         string
	ChangedBy              *types.ActorEnvelope
	ChangedAt              *time.Time
}

type ReconfigurePublicationRequest struct {
	Scope                            types.WorkspaceScope
	ExpectedBinding                  types.WorkspaceBinding
	ExpectedPublicationBindingDigest state.Digest
	Expected                         PublicationConfiguration
	Classification                   types.PublicationClassification
	Actor                            types.ActorEnvelope
}

// PublicationConfiguration returns one coherent effective publication policy
// resolved against an independently observed checkout origin.
func (s *Service) PublicationConfiguration(ctx context.Context, scope types.WorkspaceScope) (PublicationConfiguration, error) {
	if !validPublicationScope(scope) || s == nil || s.repo == nil {
		return PublicationConfiguration{}, localstore.ErrNotFound
	}
	workspace, err := s.repo.Workspace(ctx, scope)
	if err != nil {
		return PublicationConfiguration{}, err
	}
	observer := s.observePublicationOrigin
	if observer == nil {
		observer = observePublicationOrigin
	}
	observed, err := observer(ctx, workspace.Binding.Checkout.CanonicalPath)
	if err != nil {
		return PublicationConfiguration{}, err
	}
	if observed.root != workspace.Binding.Checkout.CanonicalPath || observed.checkout != workspace.Binding.Checkout {
		return PublicationConfiguration{}, fmt.Errorf("%w: observed checkout differs from persisted binding", ErrGitOriginChanged)
	}

	withWorkspace := s.withImmediateWorkspace
	if withWorkspace == nil {
		withWorkspace = s.repo.WithImmediateWorkspace
	}
	var result PublicationConfiguration
	var attempt publicationTransitionAttempt
	err = withWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		currentWorkspace, err := tx.Workspace(ctx)
		if err != nil {
			return err
		}
		policy, err := tx.PublicationPolicy(ctx)
		if err != nil {
			return err
		}
		if currentWorkspace.Binding.Checkout.CanonicalPath != observed.root || currentWorkspace.Binding.Checkout != observed.checkout {
			return fmt.Errorf("%w: current checkout differs from outside observation", ErrGitOriginChanged)
		}
		if err := reobservePublicationOriginWithReader(ctx, observed, observer); err != nil {
			return err
		}
		kind := publicationInvalidationKind(currentWorkspace.Binding, policy, observed.digest)
		if kind != "" {
			next, err := s.newPublicationInvalidation(currentWorkspace.Binding, policy, observed.digest, kind)
			if err != nil {
				return err
			}
			resolved, err := applyPublicationTransition(ctx, tx, policy, next, observed.digest, true, &attempt)
			if err != nil {
				return err
			}
			result = publicationConfigurationFromRecord(observed.digest, resolved)
			return nil
		}
		result = publicationConfigurationFromRecord(observed.digest, policy)
		return nil
	})
	if err != nil {
		if errors.Is(err, localstore.ErrCommitOutcomeUnknown) && attempt.completed {
			return confirmPublicationCommit(ctx, s.repo, scope, attempt, err)
		}
		return PublicationConfiguration{}, err
	}
	return clonePublicationConfiguration(result), nil
}

// ReconfigurePublication compare-and-swaps one complete machine-private
// publication configuration using only independently observed origin evidence.
func (s *Service) ReconfigurePublication(ctx context.Context, req ReconfigurePublicationRequest) (PublicationConfiguration, error) {
	if err := validateReconfigurePublicationRequest(req); err != nil {
		return PublicationConfiguration{}, err
	}
	if s == nil || s.repo == nil || s.now == nil {
		return PublicationConfiguration{}, localstore.ErrNotFound
	}
	workspace, err := s.repo.Workspace(ctx, req.Scope)
	if err != nil {
		return PublicationConfiguration{}, err
	}
	observer := s.observePublicationOrigin
	if observer == nil {
		observer = observePublicationOrigin
	}
	observed, err := observer(ctx, workspace.Binding.Checkout.CanonicalPath)
	if err != nil {
		return PublicationConfiguration{}, err
	}
	if observed.root != workspace.Binding.Checkout.CanonicalPath || observed.checkout != workspace.Binding.Checkout {
		return PublicationConfiguration{}, fmt.Errorf("%w: observed checkout differs from persisted binding", ErrGitOriginChanged)
	}

	withWorkspace := s.withImmediateWorkspace
	if withWorkspace == nil {
		withWorkspace = s.repo.WithImmediateWorkspace
	}
	var result PublicationConfiguration
	var attempt publicationTransitionAttempt
	err = withWorkspace(ctx, req.Scope, func(tx *localstore.WorkspaceMutationTx) error {
		currentWorkspace, err := tx.Workspace(ctx)
		if err != nil {
			return err
		}
		policy, err := tx.PublicationPolicy(ctx)
		if err != nil {
			return err
		}
		if currentWorkspace.Binding.Checkout.CanonicalPath != observed.root || currentWorkspace.Binding.Checkout != observed.checkout {
			return fmt.Errorf("%w: current checkout differs from outside observation", ErrGitOriginChanged)
		}
		if err := reobservePublicationOriginWithReader(ctx, observed, observer); err != nil {
			return err
		}
		kind := publicationInvalidationKind(currentWorkspace.Binding, policy, observed.digest)
		if kind != "" {
			next, err := s.newPublicationInvalidation(currentWorkspace.Binding, policy, observed.digest, kind)
			if err != nil {
				return err
			}
			resolved, err := applyPublicationTransition(ctx, tx, policy, next, observed.digest, true, &attempt)
			if err != nil {
				return err
			}
			result = publicationConfigurationFromRecord(observed.digest, resolved)
			return nil
		}
		current := publicationConfigurationFromRecord(observed.digest, policy)
		if currentWorkspace.Binding != req.ExpectedBinding || !equalPublicationConfigurations(current, req.Expected) {
			return ErrPublicationConfigurationCAS
		}
		constraint, err := DigestPublicationBindingConstraint(currentWorkspace.Binding.Repository, observed.digest)
		if err != nil {
			return err
		}
		if constraint != req.ExpectedPublicationBindingDigest {
			return ErrPublicationConfigurationCAS
		}
		if policy.TransitionKind == "configured" && policy.Classification == req.Classification &&
			policy.ChangedBy != nil && policy.ChangedBy.HumanPrincipalID == req.Actor.HumanPrincipalID {
			result = current
			return nil
		}
		if policy.PolicyRevision == math.MaxInt64 {
			return fmt.Errorf("projectstate: publication policy revision overflow")
		}
		changedAt := s.now().UTC()
		if changedAt.IsZero() {
			return fmt.Errorf("projectstate: invalid publication configuration transaction time")
		}
		origin := observed.digest
		actor := req.Actor
		next := localstore.WorkspacePublicationPolicyRecord{
			Repository: currentWorkspace.Binding.Repository, OriginDigest: &origin,
			Classification: req.Classification, PolicyRevision: policy.PolicyRevision + 1,
			TransitionKind: "configured", ChangedBy: &actor, ChangedAt: &changedAt,
		}
		configured, err := applyPublicationTransition(ctx, tx, policy, next, observed.digest, false, &attempt)
		if err != nil {
			return err
		}
		result = publicationConfigurationFromRecord(observed.digest, configured)
		return nil
	})
	if err != nil {
		if errors.Is(err, localstore.ErrCommitOutcomeUnknown) && attempt.completed {
			confirmed, confirmErr := confirmPublicationCommit(ctx, s.repo, req.Scope, attempt, err)
			if confirmErr != nil {
				return PublicationConfiguration{}, confirmErr
			}
			if attempt.invalidated {
				return confirmed, ErrPublicationConfigurationInvalidated
			}
			return confirmed, nil
		}
		return PublicationConfiguration{}, err
	}
	if attempt.invalidated {
		return clonePublicationConfiguration(result), ErrPublicationConfigurationInvalidated
	}
	return clonePublicationConfiguration(result), nil
}

type publicationTransitionAttempt struct {
	prior         localstore.WorkspacePublicationPolicyRecord
	next          localstore.WorkspacePublicationPolicyRecord
	priorHistory  []localstore.WorkspacePublicationPolicyRecord
	configuration PublicationConfiguration
	invalidated   bool
	completed     bool
}

func applyPublicationTransition(
	ctx context.Context,
	tx *localstore.WorkspaceMutationTx,
	prior, next localstore.WorkspacePublicationPolicyRecord,
	observed state.Digest,
	invalidated bool,
	attempt *publicationTransitionAttempt,
) (localstore.WorkspacePublicationPolicyRecord, error) {
	history, err := tx.PublicationPolicyHistory(ctx)
	if err != nil {
		return localstore.WorkspacePublicationPolicyRecord{}, err
	}
	configured, err := tx.ReconfigurePublication(ctx, localstore.WorkspacePublicationPolicyTransition{
		Expected: prior, Next: next,
	})
	if err != nil {
		return localstore.WorkspacePublicationPolicyRecord{}, err
	}
	*attempt = publicationTransitionAttempt{
		prior: cloneServicePublicationPolicyRecord(prior), next: cloneServicePublicationPolicyRecord(configured),
		priorHistory:  cloneServicePublicationPolicyHistory(history),
		configuration: publicationConfigurationFromRecord(observed, configured),
		invalidated:   invalidated, completed: true,
	}
	return configured, nil
}

func confirmPublicationCommit(
	ctx context.Context,
	repo *localstore.WorkspaceRepo,
	scope types.WorkspaceScope,
	attempt publicationTransitionAttempt,
	commitErr error,
) (PublicationConfiguration, error) {
	var current localstore.WorkspacePublicationPolicyRecord
	var history []localstore.WorkspacePublicationPolicyRecord
	err := repo.WithImmediateWorkspace(ctx, scope, func(tx *localstore.WorkspaceMutationTx) error {
		var err error
		current, err = tx.PublicationPolicy(ctx)
		if err != nil {
			return err
		}
		history, err = tx.PublicationPolicyHistory(ctx)
		return err
	})
	if err != nil {
		return PublicationConfiguration{}, fmt.Errorf("%w: publication commit confirmation failed: %v", commitErr, err)
	}
	if equalServicePublicationPolicyRecords(current, attempt.next) &&
		equalConfirmedPublicationHistory(history, attempt.priorHistory, attempt.next) {
		confirmed := publicationConfigurationFromRecord(attempt.configuration.ObservedOriginDigest, current)
		if !equalPublicationConfigurations(confirmed, attempt.configuration) {
			return PublicationConfiguration{}, fmt.Errorf("%w: publication commit confirmation projection mismatch", commitErr)
		}
		return clonePublicationConfiguration(confirmed), nil
	}
	if equalServicePublicationPolicyRecords(current, attempt.prior) &&
		equalServicePublicationPolicyHistories(history, attempt.priorHistory) {
		return PublicationConfiguration{}, commitErr
	}
	return PublicationConfiguration{}, fmt.Errorf("%w: publication commit confirmation found unexpected current or history state", commitErr)
}

func equalConfirmedPublicationHistory(
	actual, prior []localstore.WorkspacePublicationPolicyRecord,
	next localstore.WorkspacePublicationPolicyRecord,
) bool {
	if len(actual) != len(prior)+1 || !equalServicePublicationPolicyHistories(actual[:len(prior)], prior) {
		return false
	}
	return equalServicePublicationPolicyRecords(actual[len(actual)-1], next)
}

func publicationInvalidationKind(
	binding types.WorkspaceBinding,
	policy localstore.WorkspacePublicationPolicyRecord,
	observed state.Digest,
) string {
	if policy.Repository != binding.Repository {
		return "repository_invalidated"
	}
	if policy.OriginDigest != nil && *policy.OriginDigest != observed {
		return "origin_invalidated"
	}
	return ""
}

func (s *Service) newPublicationInvalidation(
	binding types.WorkspaceBinding,
	policy localstore.WorkspacePublicationPolicyRecord,
	observed state.Digest,
	kind string,
) (localstore.WorkspacePublicationPolicyRecord, error) {
	if policy.PolicyRevision == math.MaxInt64 {
		return localstore.WorkspacePublicationPolicyRecord{}, fmt.Errorf("projectstate: publication policy revision overflow")
	}
	if s == nil || s.now == nil {
		return localstore.WorkspacePublicationPolicyRecord{}, localstore.ErrNotFound
	}
	changedAt := s.now().UTC()
	if changedAt.IsZero() {
		return localstore.WorkspacePublicationPolicyRecord{}, fmt.Errorf("projectstate: invalid publication configuration transaction time")
	}
	origin := observed
	return localstore.WorkspacePublicationPolicyRecord{
		Repository: binding.Repository, OriginDigest: &origin,
		Classification: types.PublicationUnclassified, PolicyRevision: policy.PolicyRevision + 1,
		TransitionKind: kind, ChangedAt: &changedAt,
	}, nil
}

func publicationConfigurationFromRecord(observed state.Digest, record localstore.WorkspacePublicationPolicyRecord) PublicationConfiguration {
	configuration := PublicationConfiguration{
		Classification: record.Classification, PolicyRevision: record.PolicyRevision,
		ObservedOriginDigest: observed, TransitionKind: record.TransitionKind,
	}
	if record.OriginDigest != nil {
		value := *record.OriginDigest
		configuration.ConfiguredOriginDigest = &value
	}
	if record.ChangedBy != nil {
		value := *record.ChangedBy
		configuration.ChangedBy = &value
	}
	if record.ChangedAt != nil {
		value := *record.ChangedAt
		configuration.ChangedAt = &value
	}
	return configuration
}

func clonePublicationConfiguration(configuration PublicationConfiguration) PublicationConfiguration {
	clone := configuration
	if configuration.ConfiguredOriginDigest != nil {
		value := *configuration.ConfiguredOriginDigest
		clone.ConfiguredOriginDigest = &value
	}
	if configuration.ChangedBy != nil {
		value := *configuration.ChangedBy
		clone.ChangedBy = &value
	}
	if configuration.ChangedAt != nil {
		value := *configuration.ChangedAt
		clone.ChangedAt = &value
	}
	return clone
}

func cloneServicePublicationPolicyRecord(record localstore.WorkspacePublicationPolicyRecord) localstore.WorkspacePublicationPolicyRecord {
	clone := record
	if record.OriginDigest != nil {
		value := *record.OriginDigest
		clone.OriginDigest = &value
	}
	if record.ChangedBy != nil {
		value := *record.ChangedBy
		clone.ChangedBy = &value
	}
	if record.ChangedAt != nil {
		value := *record.ChangedAt
		clone.ChangedAt = &value
	}
	return clone
}

func cloneServicePublicationPolicyHistory(history []localstore.WorkspacePublicationPolicyRecord) []localstore.WorkspacePublicationPolicyRecord {
	clone := make([]localstore.WorkspacePublicationPolicyRecord, len(history))
	for index := range history {
		clone[index] = cloneServicePublicationPolicyRecord(history[index])
	}
	return clone
}

func equalServicePublicationPolicyRecords(left, right localstore.WorkspacePublicationPolicyRecord) bool {
	if left.Repository != right.Repository || left.Classification != right.Classification ||
		left.PolicyRevision != right.PolicyRevision || left.TransitionKind != right.TransitionKind ||
		(left.OriginDigest == nil) != (right.OriginDigest == nil) ||
		(left.ChangedBy == nil) != (right.ChangedBy == nil) || (left.ChangedAt == nil) != (right.ChangedAt == nil) {
		return false
	}
	if left.OriginDigest != nil && *left.OriginDigest != *right.OriginDigest {
		return false
	}
	if left.ChangedBy != nil && !equalPublicationActors(*left.ChangedBy, *right.ChangedBy) {
		return false
	}
	return left.ChangedAt == nil || left.ChangedAt.Equal(*right.ChangedAt)
}

func equalServicePublicationPolicyHistories(left, right []localstore.WorkspacePublicationPolicyRecord) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !equalServicePublicationPolicyRecords(left[index], right[index]) {
			return false
		}
	}
	return true
}

func validPublicationScope(scope types.WorkspaceScope) bool {
	return types.CanonicalUUID(scope.ProjectID) && types.CanonicalUUID(string(scope.WorkspaceID))
}

func validateReconfigurePublicationRequest(req ReconfigurePublicationRequest) error {
	if !validPublicationScope(req.Scope) || req.ExpectedBinding.Validate() != nil || req.ExpectedBinding.Scope != req.Scope ||
		!validPublicationDigest(req.ExpectedPublicationBindingDigest) || req.Classification.Validate() != nil ||
		req.Actor.ActorKind != types.ActorHuman || req.Actor.ValidateLocalAction() != nil {
		return fmt.Errorf("projectstate: invalid publication reconfiguration request")
	}
	if err := validatePublicationConfiguration(req.Expected); err != nil {
		return err
	}
	return nil
}

func validatePublicationConfiguration(configuration PublicationConfiguration) error {
	if configuration.Classification.Validate() != nil || configuration.PolicyRevision <= 0 ||
		!validPublicationDigest(configuration.ObservedOriginDigest) ||
		(configuration.ConfiguredOriginDigest != nil && !validPublicationDigest(*configuration.ConfiguredOriginDigest)) {
		return fmt.Errorf("projectstate: invalid publication configuration")
	}
	if configuration.TransitionKind != "bootstrap" && configuration.PolicyRevision < 2 {
		return fmt.Errorf("projectstate: invalid publication configuration revision")
	}
	switch configuration.TransitionKind {
	case "bootstrap":
		if configuration.PolicyRevision != 1 || configuration.Classification != types.PublicationUnclassified ||
			configuration.ConfiguredOriginDigest != nil || configuration.ChangedBy != nil || configuration.ChangedAt != nil {
			return fmt.Errorf("projectstate: invalid bootstrap publication configuration")
		}
	case "configured":
		if configuration.ConfiguredOriginDigest == nil || configuration.ChangedBy == nil || configuration.ChangedAt == nil ||
			configuration.ChangedBy.ActorKind != types.ActorHuman || configuration.ChangedBy.ValidateLocalAction() != nil ||
			!validPublicationTime(*configuration.ChangedAt) {
			return fmt.Errorf("projectstate: invalid configured publication configuration")
		}
	case "origin_invalidated", "repository_invalidated":
		if configuration.Classification != types.PublicationUnclassified || configuration.ConfiguredOriginDigest == nil ||
			configuration.ChangedBy != nil || configuration.ChangedAt == nil || !validPublicationTime(*configuration.ChangedAt) {
			return fmt.Errorf("projectstate: invalid invalidated publication configuration")
		}
	default:
		return fmt.Errorf("projectstate: invalid publication configuration transition kind")
	}
	return nil
}

func equalPublicationConfigurations(left, right PublicationConfiguration) bool {
	if left.Classification != right.Classification || left.PolicyRevision != right.PolicyRevision ||
		left.ObservedOriginDigest != right.ObservedOriginDigest || left.TransitionKind != right.TransitionKind ||
		(left.ConfiguredOriginDigest == nil) != (right.ConfiguredOriginDigest == nil) ||
		(left.ChangedBy == nil) != (right.ChangedBy == nil) || (left.ChangedAt == nil) != (right.ChangedAt == nil) {
		return false
	}
	if left.ConfiguredOriginDigest != nil && *left.ConfiguredOriginDigest != *right.ConfiguredOriginDigest {
		return false
	}
	if left.ChangedBy != nil && !equalPublicationActors(*left.ChangedBy, *right.ChangedBy) {
		return false
	}
	return left.ChangedAt == nil || left.ChangedAt.Equal(*right.ChangedAt)
}

func equalPublicationActors(left, right types.ActorEnvelope) bool {
	leftTime, rightTime := left.OccurredAt, right.OccurredAt
	left.OccurredAt = time.Time{}
	right.OccurredAt = time.Time{}
	return left == right && leftTime.Equal(rightTime)
}

func validPublicationTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}
