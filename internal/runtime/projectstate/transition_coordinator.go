package projectstate

import (
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

// transitionCoordinator owns working-tree import and the durable stash
// transitions.  Keeping these hooks together preserves the validation and
// receipt/commit confirmation order while removing them from Service.
type transitionCoordinator struct {
	repo                   *localstore.WorkspaceRepo
	readWorkingTree        func(string) (state.Tree, error)
	withImmediateWorkspace withImmediateWorkspaceFunc
	now                    func() time.Time
	newStashID             func() (string, error)
}
