//go:build wormhole_test_embedder

package main

import (
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/types"
)

// newFabricEmbedder is compiled only into subprocess integration-test
// binaries. Normal Fabric builds cannot select this implementation.
func newFabricEmbedder(types.EmbeddingConfig) (kb.Embedder, error) {
	return kb.StubEmbedder{}, nil
}
