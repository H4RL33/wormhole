package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/mcp"
	"github.com/H4RL33/wormhole/internal/storage"
	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/webui"
)

var version = "dev"

var runServerMain = runServer

func main() {
	if err := runServerMain(types.LoadConfig(), func(server *http.Server) error {
		return server.ListenAndServe()
	}); err != nil {
		log.Fatalf("fabric: %v", err)
	}
}

// runServer assembles the HTTP server and delegates its lifetime to serve.
// Keeping the process-global log.Fatal boundary in main makes the wiring
// observable under tests without changing the production listener contract.
func runServer(cfg types.Config, serve func(*http.Server) error) error {
	return runServerWithOpen(cfg, storage.Open, serve)
}

func fabricMCPHandler(registry *mcp.Registry, identityStore *identity.Store) http.HandlerFunc {
	return mcp.NewMCPHandlerWithVersion(registry, identityStore, version)
}

// runServerWithOpen separates database acquisition from HTTP composition so
// startup failures are observable without needing a live Postgres instance.
func runServerWithOpen(cfg types.Config, openDB func(types.Config) (*sql.DB, error), serve func(*http.Server) error) error {
	db, err := openDB(cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	identityStore := identity.NewStore(db)
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	gitStore := git.NewStore(db)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, cfg.KBDedupThreshold, cfg.KBMaxBodyLength, cfg.KBMinLinksDecision, cfg.KBMinLinksPolicy, cfg.KBMinLinksProcedure)
	rolesStore := roles.NewStore(db)

	registry := mcp.NewFabricRegistry(mcp.FabricRegistryDependencies{
		Identity: identityStore,
		Events:   eventsStore,
		Tasks:    tasksStore,
		Git:      gitStore,
		KB:       kbStore,
		Roles:    rolesStore,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/mcp", fabricMCPHandler(registry, identityStore))

	webuiHandler := &webui.Handler{
		Identity: identityStore,
		Tasks:    tasksStore,
		Events:   eventsStore,
		KB:       kbStore,
		AdminKey: cfg.AdminKey,
	}
	mux.Handle("/dashboard/", webuiHandler.NewMux())

	log.Printf("fabric listening on %s", cfg.ListenAddr)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           loggingMiddleware(mux),
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serve(server)
}
