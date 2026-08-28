package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestToolHandlersRejectMalformedArgumentsAtContractBoundary(t *testing.T) {
	tools := []Tool{
		EnrolAgentTool(nil, nil, nil),
		CreateTaskTool(nil), AssignTaskTool(nil), ListTasksTool(nil, nil), UpdateTaskStatusTool(nil),
		CreateChannelTool(nil), PostEventTool(nil), SubscribeChannelTool(nil),
		WriteArticleTool(nil), SearchArticlesTool(nil), GetArticleTool(nil), GetArticleLinksTool(nil),
		LinkCommitTool(nil), RequestReviewTool(nil),
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), nil, "project-a", json.RawMessage(`{"unterminated"`))
			if err == nil || !strings.Contains(err.Error(), "decode") {
				t.Fatalf("%s malformed arguments error = %v", tool.Name, err)
			}
		})
	}
}

func TestProjectScopedToolsRejectMismatchedArgumentProject(t *testing.T) {
	tools := []Tool{CreateTaskTool(nil), ListTasksTool(nil, nil), SearchArticlesTool(nil)}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), nil, "project-a", json.RawMessage(`{"project_id":"project-b"}`))
			if err == nil || !strings.Contains(err.Error(), "project_id mismatch") {
				t.Fatalf("%s project mismatch error = %v", tool.Name, err)
			}
		})
	}
}
