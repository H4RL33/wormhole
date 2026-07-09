package main

import (
	"log"
	"net/http"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
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
	eventsStore := events.NewStore(db)
	tasksStore := tasks.NewStore(db, eventsStore)
	gitStore := git.NewStore(db)
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, cfg.KBDedupThreshold, cfg.KBMaxBodyLength, cfg.KBMinLinksDecision, cfg.KBMinLinksPolicy, cfg.KBMinLinksProcedure)

	registry := mcp.NewRegistry()
	registry.Register(mcp.RegisterAgentTool(identityStore, eventsStore))
	registry.Register(mcp.WhoAmITool())
	registry.Register(mcp.CreateTaskTool(tasksStore))
	registry.Register(mcp.AssignTaskTool(tasksStore))
	registry.Register(mcp.ListTasksTool(tasksStore))
	registry.Register(mcp.UpdateTaskStatusTool(tasksStore))
	registry.Register(mcp.CreateChannelTool(eventsStore))
	registry.Register(mcp.PostEventTool(eventsStore))
	registry.Register(mcp.SubscribeChannelTool(eventsStore))
	registry.Register(mcp.ListChannelsTool(eventsStore))
	registry.Register(mcp.LinkCommitTool(gitStore))
	registry.Register(mcp.RequestReviewTool(gitStore))
	registry.Register(mcp.WriteArticleTool(kbStore))
	registry.Register(mcp.SearchArticlesTool(kbStore))
	registry.Register(mcp.GetArticleTool(kbStore))
	registry.Register(mcp.GetArticleLinksTool(kbStore))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/mcp", mcp.NewMCPHandler(registry, identityStore))

	log.Printf("wormhole-server listening on %s", cfg.ListenAddr)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           loggingMiddleware(mux),
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
