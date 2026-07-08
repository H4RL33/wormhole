package main

import (
	"encoding/json"
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
	kbStore := kb.NewStore(db, kb.StubEmbedder{})

	registry := mcp.NewRegistry()
	registry.Register(mcp.RegisterAgentTool(identityStore))
	registry.Register(mcp.WhoAmITool())
	registry.Register(mcp.CreateTaskTool(tasksStore))
	registry.Register(mcp.AssignTaskTool(tasksStore))
	registry.Register(mcp.ListTasksTool(tasksStore))
	registry.Register(mcp.UpdateTaskStatusTool(tasksStore))
	registry.Register(mcp.CreateChannelTool(eventsStore))
	registry.Register(mcp.PostEventTool(eventsStore))
	registry.Register(mcp.SubscribeChannelTool(eventsStore))
	registry.Register(mcp.LinkCommitTool(gitStore))
	registry.Register(mcp.RequestReviewTool(gitStore))
	registry.Register(mcp.WriteArticleTool(kbStore))

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
		json.NewEncoder(w).Encode(tools)
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
