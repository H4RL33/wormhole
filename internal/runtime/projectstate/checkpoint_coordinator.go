package projectstate

import (
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

// checkpointCoordinator is the sole owner of checkpoint and recovery
// lifecycle state.  Both operations share this gate set so a recovery can
// never race a checkpoint for the same workspace.
type checkpointCoordinator struct {
	repo                      *localstore.WorkspaceRepo
	publication               *publicationCoordinator
	withImmediateWorkspace    withImmediateWorkspaceFunc
	readWorkingTree           func(string) (state.Tree, error)
	gates                     *checkpointGateSet
	prepareCheckpointArtifact prepareCheckpointArtifactFunc
	confirmWorkspaceCommit    confirmWorkspaceCommitFunc
	observeRecoveryGit        checkpointRecoveryGitObserver
	recoverFilesystem         checkpointRecoveryFilesystemFunc
}

func newCheckpointCoordinator(repo *localstore.WorkspaceRepo, publication *publicationCoordinator, withImmediateWorkspace withImmediateWorkspaceFunc) *checkpointCoordinator {
	return &checkpointCoordinator{
		repo:                      repo,
		publication:               publication,
		withImmediateWorkspace:    withImmediateWorkspace,
		readWorkingTree:           ReadWorkingTreeNoFollow,
		gates:                     &checkpointGateSet{},
		prepareCheckpointArtifact: defaultPrepareCheckpointArtifact,
		confirmWorkspaceCommit:    repo.ConfirmWorkspaceCommit,
		observeRecoveryGit:        observeCheckpointRecoveryGit,
		recoverFilesystem:         recoverCheckpointFilesystem,
	}
}
