package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrBindingAwareProviderUnavailable = errors.New("localapi: binding-aware provider unavailable")

// bindResolvedProjectArguments adapts legacy handler inputs to the Gateway-
// owned project selected by the exact resolved workspace. Public callers never
// supply this field.
func bindResolvedProjectArguments(ctx context.Context, public json.RawMessage) (json.RawMessage, error) {
	binding, err := ResolvedBinding(ctx)
	if err != nil {
		return nil, err
	}
	var arguments map[string]json.RawMessage
	if len(public) == 0 {
		arguments = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(public, &arguments); err != nil || arguments == nil {
		return nil, fmt.Errorf("localapi: public tool arguments must be an object")
	}
	projectID, err := json.Marshal(binding.Scope.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("localapi: encode resolved project: %w", err)
	}
	arguments["project_id"] = projectID
	bound, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("localapi: encode resolved arguments: %w", err)
	}
	return bound, nil
}

func (s *Server) privateRuntimeConfigured() bool {
	return s != nil && (s.projectState != nil || s.actorResolver != nil || s.identityStore != nil)
}

// authorizePrivateToolProvider makes the configured Stage-2 path an explicit
// allowlist. Existing project-only providers cannot silently consume a
// workspace-bound request.
func authorizePrivateToolProvider(toolName string, public json.RawMessage) error {
	switch toolName {
	case "wormhole.agent.presence", "wormhole.agent.list",
		"wormhole.channel.list", "wormhole.channel.create", "wormhole.channel.events", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.kb.list", "wormhole.kb.get", "wormhole.kb.write":
		return nil
	case "wormhole.agent.register":
		if !isJoinRegisterArgs(public) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrBindingAwareProviderUnavailable, toolName)
}

// validatePrivateAgentSemantics permits agent_id only when it identifies the
// target of an operation or subscription filter. Action attribution is never
// caller-owned on the configured path.
func validatePrivateAgentSemantics(toolName string, public json.RawMessage) error {
	var arguments map[string]json.RawMessage
	if len(public) == 0 {
		return nil
	}
	if err := json.Unmarshal(public, &arguments); err != nil || arguments == nil {
		return fmt.Errorf("localapi: public tool arguments must be an object")
	}
	if _, supplied := arguments["agent_id"]; !supplied {
		return nil
	}
	switch toolName {
	case "wormhole.agent.register", "wormhole.agent.presence", "wormhole.channel.subscribe":
		return nil
	default:
		return fmt.Errorf("%w: agent_id", ErrPrivateAuthorityClaim)
	}
}

// resolvedLocalNamespace preserves the exact workspace boundary for the
// existing local SQLite/scheduler/eventbus primitives. A configured runtime
// has no fallback when the binding is absent or mismatched.
func (s *Server) resolvedLocalNamespace(ctx context.Context, projectID string) (string, error) {
	if !s.privateRuntimeConfigured() {
		return projectID, nil
	}
	binding, err := ResolvedBinding(ctx)
	if err != nil {
		return "", err
	}
	if binding.Scope.ProjectID != projectID {
		return "", fmt.Errorf("localapi: resolved binding project mismatch")
	}
	return string(binding.Scope.WorkspaceID), nil
}

func (s *Server) resolvedActionPrincipal(ctx context.Context, legacy string) (string, error) {
	if !s.privateRuntimeConfigured() {
		return legacy, nil
	}
	actor, err := ServerOwnedActor(ctx)
	if err != nil {
		return "", err
	}
	principal := actor.PrincipalID()
	if principal == "" {
		return "", ErrServerOwnedActorMissing
	}
	return principal, nil
}
