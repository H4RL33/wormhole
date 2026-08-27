package localapi

import (
	"context"
	"errors"

	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/types"
)

type ConnectionIdentity = localidentity.ConnectionIdentity

type LocalActorResolver interface {
	ResolveLocalActor(context.Context, ConnectionIdentity) (types.ActorEnvelope, error)
}

var ErrServerOwnedActorMissing = errors.New("localapi: server-owned actor is missing")

type serverOwnedActorContextKey struct{}

func withServerOwnedActor(ctx context.Context, actor types.ActorEnvelope) context.Context {
	return context.WithValue(ctx, serverOwnedActorContextKey{}, actor)
}

func ServerOwnedActor(ctx context.Context) (types.ActorEnvelope, error) {
	actor, ok := ctx.Value(serverOwnedActorContextKey{}).(types.ActorEnvelope)
	if !ok || actor.ValidateLocalAction() != nil {
		return types.ActorEnvelope{}, ErrServerOwnedActorMissing
	}
	return actor, nil
}
