package localapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localidentity"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
)

const PrivateSetupEnsureIdentityRPCMethod = "wormhole.private.setup.ensure_identity"

var (
	ErrPrivateRuntimeAlreadyConfigured = errors.New("localapi: private runtime already configured")
	ErrIdentityRootInsideRepository    = errors.New("localapi: identity root must be outside every repository")
)

type SetupIdentityRequest struct {
	JournalID string                           `json:"journal_id"`
	Selection types.ConfirmedIdentitySelection `json:"selection"`
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

func (s *Server) PrivateSetupEnsureIdentityRPC(ctx context.Context, req SetupIdentityRequest) (localidentity.PublicHumanProfile, error) {
	if s == nil || s.identityStore == nil {
		return localidentity.PublicHumanProfile{}, fmt.Errorf("localapi: private identity store is unavailable")
	}
	return s.identityStore.EnsureSelectedForSetup(ctx, req.JournalID, req.Selection)
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
