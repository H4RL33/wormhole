package main

import (
	"log"
	"net/http"
	"time"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/mcp"
	"github.com/H4RL33/wormhole/internal/storage"
	"github.com/H4RL33/wormhole/internal/types"
)

func main() {
	cfg := types.LoadConfig()

	db, err := storage.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	identityStore := identity.NewStore(db)

	registry := mcp.NewRegistry()
	registry.Register(mcp.RegisterAgentTool(identityStore))
	registry.Register(mcp.WhoAmITool())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp/tools", func(w http.ResponseWriter, r *http.Request) {
		tools := registry.List()
		w.Header().Set("Content-Type", "application/json")
		if len(tools) == 0 {
			w.Write([]byte("[]"))
			return
		}
	})
	mux.HandleFunc("/mcp/tools/call", mcp.NewCallHandler(registry, identityStore))

	log.Printf("wormhole-server listening on %s", cfg.ListenAddr)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
