//go:build !linux && !darwin

package projectstate

import (
	"fmt"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func readWorkingTreeNoFollowPlatform(root string, _ workingTreeLimits, _ workingTreeReadHook) (state.Tree, error) {
	return nil, fmt.Errorf("%w: %s", ErrWorkingTreeFilesystemUnsupported, root)
}
