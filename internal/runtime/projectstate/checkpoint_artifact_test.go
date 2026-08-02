package projectstate

import (
	"context"
	"errors"
	"testing"
)

func TestCheckpointArtifactRejectsNilPublication(t *testing.T) {
	if err := publishPreparedCheckpointArtifact(context.Background(), nil); !errors.Is(err, ErrCheckpointUnsupported) {
		t.Fatalf("publish nil artifact error = %v, want ErrCheckpointUnsupported", err)
	}
}
