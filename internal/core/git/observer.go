package git

import (
	"context"
	"errors"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

// ErrGitObservation collapses invalid provider responses, unsafe transport
// behavior, and unavailable server credentials into one observer boundary.
var ErrGitObservation = errors.New("git: canonical observation failed")

// CanonicalGitObserver independently observes one canonical repository ref.
// The credential reference is Fabric-owned; callers never supply a raw secret.
type CanonicalGitObserver interface {
	ObserveRef(context.Context, types.RepositoryIdentity, string, string) (RefObservation, projectstate.Tree, error)
}

// GitCredentialSource resolves only Fabric-server Git credential references.
type GitCredentialSource interface {
	ReadServerCredential(context.Context, string) (string, error)
}
