package sync

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func validBootstrapWire() bootstrapResultWire {
	now := time.Date(2026, 7, 25, 12, 0, 0, 123, time.UTC)
	parent := types.BootstrapTaskV1{ID: "task-parent", ProjectID: "ns-1", Title: "parent", Description: "parent task", Status: "todo", CreatedAt: now, UpdatedAt: now}
	parentID := parent.ID
	child := types.BootstrapTaskV1{ID: "task-child", ProjectID: "ns-1", ParentTaskID: &parentID, Title: "child", Description: "child task", Status: "wip", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	article := types.BootstrapArticleV1{ID: "kb-1", ProjectID: "ns-1", Title: "article", Body: "body", Frontmatter: json.RawMessage(`{}`), AuthorAgentID: "agent-1", CreatedAt: now, UpdatedAt: now}
	return bootstrapResultWire{
		OrgConfig: types.BootstrapOrgConfigV1{
			SchemaVersion: types.BootstrapSchemaVersionV1,
			Project:       types.BootstrapProjectV1{ID: "ns-1", Name: "project", Owner: "owner", CreatedAt: now},
			Identity: types.BootstrapIdentityV1{
				Agent:       types.BootstrapAgentV1{ID: "agent-1", Owner: "owner", Model: "model", Capabilities: []string{}, CreatedAt: now},
				Passport:    types.BootstrapPassportV1{ID: "passport-1", AgentID: "agent-1", ProjectID: "ns-1", Repositories: []string{}, Roles: []string{}, IssuedAt: now},
				Permissions: []string{},
			},
			Channels:                    []types.BootstrapChannelV1{{ID: "channel-1", ProjectID: "ns-1", Name: "general", CreatedAt: now}},
			Events:                      []types.BootstrapEventV1{{ID: "event-1", ProjectID: "ns-1", ChannelID: "channel-1", AgentID: "agent-1", EventType: "message.posted", Payload: json.RawMessage(`{"body":"hi"}`), CreatedAt: now}},
			Tasks:                       []types.BootstrapTaskV1{parent, child},
			KB:                          types.BootstrapKBV1{Articles: []types.BootstrapArticleV1{article}},
			IntegrationManifestMetadata: json.RawMessage(`null`),
		},
		ProjectList: []string{},
		TaskList:    []types.BootstrapTaskV1{parent, child},
		KBList:      []types.BootstrapArticleV1{article},
		Timestamp:   now.Format(time.RFC3339Nano),
		Version:     SyncProtocolVersion,
	}
}

func TestDecodeBootstrapResultRejectsUnknownFields(t *testing.T) {
	want := validBootstrapWire()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpected"] = true
	if _, err := decodeBootstrapResult(object); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decode error = %v, want unknown field", err)
	}

	nested := object["org_config"].(map[string]any)
	delete(object, "unexpected")
	nested["unexpected"] = true
	if _, err := decodeBootstrapResult(object); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("nested decode error = %v, want unknown field", err)
	}
}

func TestValidateBootstrapResultRejectsWholeSnapshotBeforeApply(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*bootstrapResultWire)
		want   string
	}{
		{"outer version", func(out *bootstrapResultWire) { out.Version = 2 }, "outer version"},
		{"nested version", func(out *bootstrapResultWire) { out.OrgConfig.SchemaVersion = 2 }, "org_config schema_version"},
		{"nil array", func(out *bootstrapResultWire) { out.OrgConfig.Channels = nil }, "channels must be an array"},
		{"project mismatch", func(out *bootstrapResultWire) { out.OrgConfig.Project.ID = "ns-2" }, "project id"},
		{"credential agent mismatch", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Agent.ID = "agent-2" }, "credential agent"},
		{"credential passport mismatch", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Passport.ID = "passport-2" }, "credential passport"},
		{"event channel", func(out *bootstrapResultWire) { out.OrgConfig.Events[0].ChannelID = "missing" }, "event channel"},
		{"task cycle", func(out *bootstrapResultWire) {
			parent := "task-child"
			out.OrgConfig.Tasks[0].ParentTaskID = &parent
			out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
		}, "cycle"},
		{"invalid payload", func(out *bootstrapResultWire) { out.OrgConfig.Events[0].Payload = json.RawMessage(`{`) }, "event payload"},
		{"invalid metadata", func(out *bootstrapResultWire) { out.OrgConfig.IntegrationManifestMetadata = json.RawMessage(`{`) }, "manifest metadata"},
		{"task mirror", func(out *bootstrapResultWire) { out.TaskList[0].Title = "different" }, "task_list mirror"},
		{"kb mirror", func(out *bootstrapResultWire) { out.KBList[0].Title = "different" }, "kb_list mirror"},
		{"zero timestamp", func(out *bootstrapResultWire) { out.OrgConfig.Project.CreatedAt = time.Time{} }, "project created_at"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := validBootstrapWire()
			tt.mutate(&out)
			if err := validateBootstrapResult(out, "ns-1", "agent-1", "passport-1"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateBootstrapResultAcceptsCompleteSnapshot(t *testing.T) {
	out := validBootstrapWire()
	if err := validateBootstrapResult(out, "ns-1", "agent-1", "passport-1"); err != nil {
		t.Fatalf("validateBootstrapResult: %v", err)
	}
	if !reflect.DeepEqual(out.TaskList, out.OrgConfig.Tasks) || !reflect.DeepEqual(out.KBList, out.OrgConfig.KB.Articles) {
		t.Fatal("fixture mirrors drifted")
	}
}

func TestValidateBootstrapResultAcceptsDomainValidEmptyTextValues(t *testing.T) {
	out := validBootstrapWire()
	out.OrgConfig.Project.Name = ""
	out.OrgConfig.Project.Owner = ""
	out.OrgConfig.Identity.Agent.Owner = ""
	out.OrgConfig.Identity.Agent.Model = ""
	out.OrgConfig.Identity.Agent.Capabilities = []string{""}
	out.OrgConfig.Identity.Passport.Repositories = []string{""}
	out.OrgConfig.Identity.Passport.Roles = []string{""}
	out.OrgConfig.Identity.Permissions = []string{""}
	out.OrgConfig.Channels[0].Name = ""
	out.OrgConfig.Tasks[0].Title = ""
	out.OrgConfig.Tasks[0].Description = ""
	out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
	out.OrgConfig.KB.Articles[0].Title = ""
	out.OrgConfig.KB.Articles[0].Body = ""
	out.KBList = append([]types.BootstrapArticleV1(nil), out.OrgConfig.KB.Articles...)

	if err := validateBootstrapResult(out, "ns-1", "agent-1", "passport-1"); err != nil {
		t.Fatalf("validate domain-valid empty text values: %v", err)
	}
}

func TestDecodeBootstrapResultRejectsMissingRequiredNestedFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"project name", func(object map[string]any) {
			delete(object["org_config"].(map[string]any)["project"].(map[string]any), "name")
		}, "org_config.project.name"},
		{"agent model", func(object map[string]any) {
			identity := object["org_config"].(map[string]any)["identity"].(map[string]any)
			delete(identity["agent"].(map[string]any), "model")
		}, "org_config.identity.agent.model"},
		{"channel name", func(object map[string]any) {
			channels := object["org_config"].(map[string]any)["channels"].([]any)
			delete(channels[0].(map[string]any), "name")
		}, "org_config.channels[0].name"},
		{"event nullable note", func(object map[string]any) {
			events := object["org_config"].(map[string]any)["events"].([]any)
			delete(events[0].(map[string]any), "note")
		}, "org_config.events[0].note"},
		{"nested task description", func(object map[string]any) {
			tasks := object["org_config"].(map[string]any)["tasks"].([]any)
			delete(tasks[0].(map[string]any), "description")
		}, "org_config.tasks[0].description"},
		{"top-level task description", func(object map[string]any) {
			tasks := object["task_list"].([]any)
			delete(tasks[0].(map[string]any), "description")
		}, "task_list[0].description"},
		{"task nullable due date", func(object map[string]any) {
			tasks := object["org_config"].(map[string]any)["tasks"].([]any)
			delete(tasks[0].(map[string]any), "due_by")
		}, "org_config.tasks[0].due_by"},
		{"nested KB body", func(object map[string]any) {
			kb := object["org_config"].(map[string]any)["kb"].(map[string]any)
			articles := kb["articles"].([]any)
			delete(articles[0].(map[string]any), "body")
		}, "org_config.kb.articles[0].body"},
		{"top-level KB body", func(object map[string]any) {
			articles := object["kb_list"].([]any)
			delete(articles[0].(map[string]any), "body")
		}, "kb_list[0].body"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(validBootstrapWire())
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			tt.mutate(object)
			if _, err := decodeBootstrapResult(object); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("decode error = %v, want missing-field path %q", err, tt.want)
			}
		})
	}
}

func TestValidateBootstrapResultRejectsNonNullManifestAndMissingRequiredScalars(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*bootstrapResultWire)
		want   string
	}{
		{"manifest object", func(out *bootstrapResultWire) { out.OrgConfig.IntegrationManifestMetadata = json.RawMessage(`{}`) }, "must be JSON null"},
		{"manifest string", func(out *bootstrapResultWire) { out.OrgConfig.IntegrationManifestMetadata = json.RawMessage(`"null"`) }, "must be JSON null"},
		{"event agent", func(out *bootstrapResultWire) { out.OrgConfig.Events[0].AgentID = "" }, "event agent_id"},
		{"event type", func(out *bootstrapResultWire) { out.OrgConfig.Events[0].EventType = " " }, "event event_type"},
		{"empty parent reference", func(out *bootstrapResultWire) {
			empty := ""
			out.OrgConfig.Tasks[1].ParentTaskID = &empty
			out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
		}, "parent_task_id"},
		{"empty owner reference", func(out *bootstrapResultWire) {
			empty := ""
			out.OrgConfig.Tasks[0].OwnerAgentID = &empty
			out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
		}, "owner_agent_id"},
		{"article author", func(out *bootstrapResultWire) {
			out.OrgConfig.KB.Articles[0].AuthorAgentID = ""
			out.KBList = append([]types.BootstrapArticleV1(nil), out.OrgConfig.KB.Articles...)
		}, "kb article author_agent_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := validBootstrapWire()
			tt.mutate(&out)
			if err := validateBootstrapResult(out, "ns-1", "agent-1", "passport-1"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want %q", err, tt.want)
			}
		})
	}
}
