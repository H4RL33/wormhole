package projectstate

import (
	"context"
	"errors"
	"testing"
)

func TestCheckpointArtifactRejectsNilPublication(t *testing.T) {
	disposition, err := publishPreparedCheckpointArtifact(context.Background(), nil)
	if disposition != 0 || !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("publish nil artifact = (%d, %v), want zero and ErrCheckpointUnsupported", disposition, err)
	}
}
