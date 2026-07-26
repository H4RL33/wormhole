package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	coregit "github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestAlphaAcceptanceIncrementalTaskEventAndGitPropagationIsReplaySafe(t *testing.T) {
	ctx := context.Background()
	tasksStore := testTasksStore(t)
	kbStore := testKBStore(t)
	eventsStore := testEventsStore(t)
	gitStore := coregit.NewStore(testDB(t))
	projectID := mustCreateProject(t, "alpha-acceptance-sync")
	agentID, _ := mustRegisterAgent(t, projectID)
	channel, err := eventsStore.CreateChannel(ctx, projectID, "acceptance")
	if err != nil {
		t.Fatal(err)
	}
	task, err := tasksStore.Create(ctx, projectID, "handoff acceptance", "prove propagation", nil, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	gitLinkID := uuid.NewString()
	taskPayload := mustMarshal(t, map[string]any{
		"task_id": task.ID, "new_status": "wip", "channel_id": channel.ID,
	})
	gitPayload := mustMarshal(t, map[string]any{
		"git_link_id": gitLinkID, "project_id": projectID, "task_id": task.ID,
		"repo": "H4RL33/wormhole", "commit_sha": "abc123", "summary": "acceptance handoff",
	})
	in := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion}
	for _, item := range []struct {
		typ, id, operation string
		payload            json.RawMessage
	}{
		{"task", task.ID, "update", taskPayload},
		{"git_link", gitLinkID, "create", gitPayload},
	} {
		in.Items = append(in.Items, struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{item.typ, item.id, item.operation, item.payload})
	}
	scope := &identity.AuthenticatedScope{
		Agent: identity.Agent{ID: agentID}, ProjectID: projectID,
		Permissions: []string{"task.update_status", "git.link_commit"},
	}
	push := IncrementalPushToolWithGit(tasksStore, kbStore, eventsStore, gitStore, NewSyncRateLimiter(30, time.Minute))
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := push.Handler(ctx, scope, projectID, mustMarshal(t, in))
		if err != nil {
			t.Fatalf("push attempt %d: %v", attempt, err)
		}
		out := result.(IncrementalPushOutput)
		if len(out.Applied) != 2 || out.Applied[0].Error != "" || out.Applied[1].Error != "" {
			t.Fatalf("push attempt %d applied = %+v", attempt, out.Applied)
		}
	}
	rows, err := tasksStore.List(ctx, projectID, nil)
	if err != nil || len(rows) != 1 || rows[0].Status != "wip" {
		t.Fatalf("central task after replay = %+v, err %v", rows, err)
	}
	eventList, err := eventsStore.ListEvents(ctx, projectID, channel.ID, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	statusEvents := 0
	var statusEventID string
	for _, event := range eventList {
		if event.EventType == "task.status_changed" {
			statusEvents++
			statusEventID = event.ID
		}
	}
	if statusEvents != 1 {
		t.Fatalf("task status replay produced %d status events", statusEvents)
	}
	var gitRows int
	if err := testDB(t).QueryRowContext(ctx, `SELECT count(*) FROM git_links WHERE id = $1 AND project_id = $2`, gitLinkID, projectID).Scan(&gitRows); err != nil || gitRows != 1 {
		t.Fatalf("central Git replay rows = %d, err %v", gitRows, err)
	}

	pull := IncrementalPullToolWithGit(tasksStore, kbStore, eventsStore, gitStore, NewSyncRateLimiter(30, time.Minute))
	result, err := pull.Handler(ctx, scope, projectID, mustMarshal(t, IncrementalPullInput{NamespaceID: projectID, Version: SyncProtocolVersion}))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"channel": channel.ID, "event": statusEventID, "git_link": gitLinkID}
	for _, raw := range result.(IncrementalPullOutput).Updates {
		var envelope syncUpdateEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		if _, needed := want[envelope.Type]; !needed {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(envelope.Data, &data); err != nil {
			t.Fatal(err)
		}
		id, _ := data["id"].(string)
		if envelope.Type == "git_link" {
			id, _ = data["git_link_id"].(string)
		}
		if id == want[envelope.Type] {
			delete(want, envelope.Type)
		}
	}
	if len(want) != 0 {
		t.Fatalf("incremental pull missing stable envelopes: %+v", want)
	}
}
