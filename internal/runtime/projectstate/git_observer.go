package projectstate

import (
	"errors"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var (
	ErrBranchSwitchPending            = errors.New("projectstate: branch switch has pending workspace state")
	ErrGitObservationChanged          = errors.New("projectstate: git observation changed")
	ErrGitMaterializationPrecondition = errors.New("projectstate: git materialization precondition failed")
)

type BranchSwitchAction string

const (
	BranchSwitchReject  BranchSwitchAction = ""
	BranchSwitchDiscard BranchSwitchAction = "discard"
)

type ObserveGitBaseRequest struct {
	Scope           types.WorkspaceScope
	ExpectedBinding types.WorkspaceBinding
	Root            string
	ExpectedCommit  string
	BranchAction    BranchSwitchAction
	RequestID       string
	Actor           types.ActorEnvelope
}

type ObserveGitBaseResult struct {
	PreviousCommit, ObservedCommit         string
	PreviousRef, ObservedRef               string
	PreviousBaseDigest, ObservedBaseDigest state.Digest
	CandidateAccepted                      bool
	AcceptedJournalID                      *string
	Rebased                                bool
	Conflicts                              []Conflict
}
