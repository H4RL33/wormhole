package mcp

import (
	"context"
	"encoding/json"
	"strings"
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
	gatewayStatusEventID := uuid.NewString()
	taskPayload := mustMarshal(t, map[string]any{
		"task_id": task.ID, "from_status": "todo", "new_status": "wip", "channel_id": channel.ID, "event_id": gatewayStatusEventID,
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
	if statusEventID != gatewayStatusEventID {
		t.Fatalf("central status event ID = %q, want Gateway stable ID %q", statusEventID, gatewayStatusEventID)
	}
	statusEvent, err := eventsStore.GetEvent(ctx, projectID, gatewayStatusEventID)
	if err != nil {
		t.Fatal(err)
	}
	if statusEvent.AgentID != agentID {
		t.Fatalf("central status event agent = %q, want authenticated caller %q", statusEvent.AgentID, agentID)
	}
	pushTaskOnly := func(t *testing.T, actor *identity.AuthenticatedScope, payload map[string]any) string {
		t.Helper()
		input := IncrementalPushInput{NamespaceID: projectID, Version: SyncProtocolVersion}
		input.Items = append(input.Items, struct {
			EntityType string          `json:"entity_type"`
			EntityID   string          `json:"entity_id"`
			Operation  string          `json:"operation"`
			Payload    json.RawMessage `json:"payload"`
		}{"task", task.ID, "update", mustMarshal(t, payload)})
		result, err := push.Handler(ctx, actor, projectID, mustMarshal(t, input))
		if err != nil {
			t.Fatal(err)
		}
		applied := result.(IncrementalPushOutput).Applied
		if len(applied) != 1 {
			t.Fatalf("single replay applied = %+v", applied)
		}
		return applied[0].Error
	}
	doneStatusEventID := uuid.NewString()
	donePayload := map[string]any{"task_id": task.ID, "from_status": "wip", "new_status": "done", "channel_id": channel.ID, "event_id": doneStatusEventID}
	if got := pushTaskOnly(t, scope, donePayload); got != "" {
		t.Fatalf("later task transition error = %q", got)
	}
	originalPayload := map[string]any{"task_id": task.ID, "from_status": "todo", "new_status": "wip", "channel_id": channel.ID, "event_id": gatewayStatusEventID}
	if got := pushTaskOnly(t, scope, originalPayload); got != "" {
		t.Fatalf("exact historical replay after later transition error = %q", got)
	}
	otherAgentID, _ := mustRegisterAgent(t, projectID)
	otherScope := &identity.AuthenticatedScope{
		Agent: identity.Agent{ID: otherAgentID}, ProjectID: projectID,
		Permissions: []string{"task.update_status"},
	}
	for _, replay := range []struct {
		name      string
		actor     *identity.AuthenticatedScope
		payload   map[string]any
		wantError string
	}{
		{
			name: "same ID changed transition content", actor: scope,
			payload:   map[string]any{"task_id": task.ID, "from_status": "blocked", "new_status": "wip", "channel_id": channel.ID, "event_id": gatewayStatusEventID},
			wantError: "stable id conflict",
		},
		{
			name: "same ID changed authenticated actor", actor: otherScope,
			payload:   map[string]any{"task_id": task.ID, "from_status": "todo", "new_status": "wip", "channel_id": channel.ID, "event_id": gatewayStatusEventID},
			wantError: "stable id conflict",
		},
		{
			name: "different ID after applied transition", actor: scope,
			payload:   map[string]any{"task_id": task.ID, "from_status": "todo", "new_status": "wip", "channel_id": channel.ID, "event_id": uuid.NewString()},
			wantError: "stable id conflict",
		},
		{
			name: "malformed event ID", actor: scope,
			payload:   map[string]any{"task_id": task.ID, "from_status": "todo", "new_status": "wip", "channel_id": channel.ID, "event_id": "NOT-A-CANONICAL-UUID"},
			wantError: "canonical UUID event_id",
		},
	} {
		t.Run(replay.name, func(t *testing.T) {
			if got := pushTaskOnly(t, replay.actor, replay.payload); !strings.Contains(got, replay.wantError) {
				t.Fatalf("replay error = %q, want substring %q", got, replay.wantError)
			}
		})
	}
	var statusRows int
	if err := testDB(t).QueryRowContext(ctx, `SELECT count(*) FROM events WHERE project_id = $1 AND event_type = 'task.status_changed'`, projectID).Scan(&statusRows); err != nil || statusRows != 2 {
		t.Fatalf("historical/rejected replays left %d central status events, err %v; want 2 distinct transitions", statusRows, err)
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
