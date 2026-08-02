package projectstate

import (
	"errors"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrCheckpointCAS         = errors.New("projectstate: checkpoint working tree changed")
	ErrCheckpointUnsupported = errors.New("projectstate: checkpoint filesystem unsupported")
)

type checkpointArtifactInput struct {
	Checkout        types.CheckoutIdentity
	PriorTree       state.Tree
	PriorTreeDigest state.Digest
	CandidateTree   state.Tree
	CandidateDigest state.Digest
}

type checkpointArtifactEvidence struct {
	JournalID  string
	StagePath  string
	BackupPath string
}
