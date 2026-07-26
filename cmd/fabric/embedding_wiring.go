//go:build !wormhole_test_embedder

package main

import (
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/types"
)

func newFabricEmbedder(cfg types.EmbeddingConfig) (kb.Embedder, error) {
	return kb.NewCohereEmbedder(cfg.APIKey)
}
