package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const privateRequestContextKey = "_wormhole_workspace"

var (
	ErrResolvedBindingMissing = errors.New("localapi: resolved workspace binding is missing")
	ErrPrivateRequestContext  = errors.New("localapi: invalid private request context")
	ErrPrivateAuthorityClaim  = errors.New("localapi: machine-private authority fields are forbidden")
)

// PrivateRequestContext is bridge-only routing input. Gateway removes it
// before a public tool's arguments are decoded or forwarded.
type PrivateRequestContext struct {
	WorkingDirectory string `json:"working_directory"`
}

type resolvedBindingContextKey struct{}

func WithResolvedBinding(ctx context.Context, binding types.WorkspaceBinding) context.Context {
	return context.WithValue(ctx, resolvedBindingContextKey{}, binding)
}

func ResolvedBinding(ctx context.Context) (types.WorkspaceBinding, error) {
	binding, ok := ctx.Value(resolvedBindingContextKey{}).(types.WorkspaceBinding)
	if !ok || binding.Validate() != nil {
		return types.WorkspaceBinding{}, ErrResolvedBindingMissing
	}
	return binding, nil
}

func (s *Server) resolvePrivateRequest(ctx context.Context, raw json.RawMessage) (context.Context, json.RawMessage, error) {
	if s == nil || s.projectState == nil || s.actorResolver == nil {
		return ctx, nil, ErrPrivateRequestContext
	}
	var arguments map[string]json.RawMessage
	if len(raw) == 0 {
		arguments = map[string]json.RawMessage{}
	} else {
		if err := rejectDuplicateJSONMembers(raw); err != nil {
			return ctx, nil, fmt.Errorf("%w: duplicate request member", ErrPrivateRequestContext)
		}
		if err := json.Unmarshal(raw, &arguments); err != nil || arguments == nil {
			return ctx, nil, fmt.Errorf("%w: arguments must be an object", ErrPrivateRequestContext)
		}
	}
	privateRaw, ok := arguments[privateRequestContextKey]
	if !ok {
		return ctx, nil, fmt.Errorf("%w: missing bridge workspace", ErrPrivateRequestContext)
	}
	var privateContext PrivateRequestContext
	if err := decodeClosedJSON(privateRaw, &privateContext); err != nil {
		return ctx, nil, fmt.Errorf("%w: invalid bridge workspace", ErrPrivateRequestContext)
	}
	observed := types.WorkspaceContext{WorkingDirectory: privateContext.WorkingDirectory}
	if err := observed.Validate(); err != nil {
		return ctx, nil, fmt.Errorf("%w: invalid working directory", ErrPrivateRequestContext)
	}
	delete(arguments, privateRequestContextKey)
	// project_id is untrusted comparison evidence and never selects a route.
	// Remove it before public handler decoding; bindResolvedProjectArguments
	// adds the server-resolved value later.
	delete(arguments, "project_id")
	if field := privateAuthorityClaim(arguments); field != "" {
		return ctx, nil, fmt.Errorf("%w: %s", ErrPrivateAuthorityClaim, field)
	}
	binding, err := s.projectState.ResolveWorkingDirectory(ctx, observed)
	if err != nil || binding.Validate() != nil {
		return ctx, nil, fmt.Errorf("%w: workspace resolution failed", ErrPrivateRequestContext)
	}
	now := time.Now().UTC()
	if s.clock != nil {
		now = s.clock().UTC()
	}
	actor, err := s.actorResolver.ResolveLocalActor(ctx, ConnectionIdentity{OccurredAt: now})
	if err != nil || actor.ValidateLocalAction() != nil {
		return ctx, nil, fmt.Errorf("%w: local actor resolution failed", ErrPrivateRequestContext)
	}
	public, err := json.Marshal(arguments)
	if err != nil {
		return ctx, nil, fmt.Errorf("%w: encode public arguments", ErrPrivateRequestContext)
	}
	ctx = WithResolvedBinding(ctx, binding)
	ctx = withServerOwnedActor(ctx, actor)
	return ctx, public, nil
}

// validatePrivateProjectClaim treats project_id only as untrusted comparison
// evidence for private provider routes. The resolved workspace binding remains
// the sole scope authority, and the claim is never returned for public decode.
func validatePrivateProjectClaim(ctx context.Context, raw json.RawMessage) error {
	binding, err := ResolvedBinding(ctx)
	if err != nil {
		return err
	}
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arguments); err != nil || arguments == nil {
		return fmt.Errorf("%w: arguments must be an object", ErrPrivateRequestContext)
	}
	projectRaw, supplied := arguments["project_id"]
	if !supplied {
		return nil
	}
	var projectID string
	if err := json.Unmarshal(projectRaw, &projectID); err != nil || projectID != binding.Scope.ProjectID {
		return errors.New("localapi: resolved project mismatch")
	}
	return nil
}

func privateAuthorityClaim(arguments map[string]json.RawMessage) string {
	for _, field := range []string{
		"workspace_id", "checkout_id", "namespace_id", "namespace", "binding",
		"working_directory", "actor", "actor_kind", "human_principal_id",
		"accountable_human_id", "assurance", "session_id", "fabric_instance_id",
		"remote_project_id", "stream_id", "credential_ref",
	} {
		if _, exists := arguments[field]; exists {
			return field
		}
	}
	return ""
}
