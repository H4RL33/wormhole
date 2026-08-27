//go:build !linux

package projectstate

import "context"

func recoverCheckpointFilesystem(
	context.Context,
	checkpointRecoveryProof,
) (checkpointRecoveryFilesystemOutcome, error) {
	return 0, ErrCheckpointUnsupported
}
