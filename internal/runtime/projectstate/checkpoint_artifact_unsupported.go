//go:build !linux

package projectstate

import "context"

type checkpointArtifactDependencies struct{}
type checkpointArtifact struct{}

func prepareCheckpointArtifact(context.Context, checkpointArtifactInput) (*checkpointArtifact, error) {
	return nil, ErrCheckpointUnsupported
}

func prepareCheckpointArtifactWithDependencies(context.Context, checkpointArtifactInput, checkpointArtifactDependencies) (*checkpointArtifact, error) {
	return nil, ErrCheckpointUnsupported
}

func (*checkpointArtifact) evidence() checkpointArtifactEvidence { return checkpointArtifactEvidence{} }
func publishPreparedCheckpointArtifact(context.Context, *checkpointArtifact) (checkpointPublicationDisposition, error) {
	return 0, ErrCheckpointUnsupported
}
func (*checkpointArtifact) close() {}
