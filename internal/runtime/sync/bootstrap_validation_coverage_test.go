package sync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

// These cases complete the bootstrap trust-boundary contract: every identity,
// collection, timestamp, reference, and JSON field must be rejected before a
// snapshot can reach the local replica transaction.
func TestValidateBootstrapResultRejectsRemainingMalformedSnapshots(t *testing.T) {
	badTime := time.Time{}
	missing := "missing"
	tests := []struct {
		name   string
		mutate func(*bootstrapResultWire)
		want   string
	}{
		{"missing namespace", func(out *bootstrapResultWire) {}, "authenticated namespace"},
		{"nonempty project list", func(out *bootstrapResultWire) { out.ProjectList = []string{"ns-1"} }, "project_list"},
		{"nil capabilities", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Agent.Capabilities = nil }, "capabilities"},
		{"nil repositories", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Passport.Repositories = nil }, "repositories"},
		{"nil roles", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Passport.Roles = nil }, "roles"},
		{"nil permissions", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Permissions = nil }, "permissions"},
		{"nil events", func(out *bootstrapResultWire) { out.OrgConfig.Events = nil }, "events"},
		{"nil tasks", func(out *bootstrapResultWire) { out.OrgConfig.Tasks = nil }, "tasks"},
		{"nil articles", func(out *bootstrapResultWire) { out.OrgConfig.KB.Articles = nil }, "kb articles"},
		{"nil top level articles", func(out *bootstrapResultWire) { out.KBList = nil }, "task_list and kb_list"},
		{"passport agent reference", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Passport.AgentID = "other" }, "references"},
		{"zero identity timestamp", func(out *bootstrapResultWire) { out.OrgConfig.Identity.Agent.CreatedAt = badTime }, "identity timestamps"},
		{"invalid outer timestamp", func(out *bootstrapResultWire) { out.Timestamp = "not-a-time" }, "bootstrap timestamp"},
		{"invalid channel", func(out *bootstrapResultWire) { out.OrgConfig.Channels[0].CreatedAt = badTime }, "invalid project or timestamp"},
		{"duplicate channel", func(out *bootstrapResultWire) {
			out.OrgConfig.Channels = append(out.OrgConfig.Channels, out.OrgConfig.Channels[0])
		}, "duplicate channel"},
		{"invalid event", func(out *bootstrapResultWire) { out.OrgConfig.Events[0].CreatedAt = badTime }, "invalid project or timestamp"},
		{"duplicate event", func(out *bootstrapResultWire) {
			out.OrgConfig.Events = append(out.OrgConfig.Events, out.OrgConfig.Events[0])
		}, "duplicate event"},
		{"invalid task timestamp", func(out *bootstrapResultWire) {
			out.OrgConfig.Tasks[0].UpdatedAt = badTime
			out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
		}, "invalid project or timestamp"},
		{"invalid task status", func(out *bootstrapResultWire) {
			out.OrgConfig.Tasks[0].Status = "unknown"
			out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
		}, "invalid status"},
		{"duplicate task", func(out *bootstrapResultWire) {
			out.OrgConfig.Tasks = append(out.OrgConfig.Tasks, out.OrgConfig.Tasks[0])
			out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
		}, "duplicate task"},
		{"missing task parent", func(out *bootstrapResultWire) {
			out.OrgConfig.Tasks[1].ParentTaskID = &missing
			out.TaskList = append([]types.BootstrapTaskV1(nil), out.OrgConfig.Tasks...)
		}, "parent reference"},
		{"invalid article timestamp", func(out *bootstrapResultWire) {
			out.OrgConfig.KB.Articles[0].UpdatedAt = badTime
			out.KBList = append([]types.BootstrapArticleV1(nil), out.OrgConfig.KB.Articles...)
		}, "invalid project or timestamp"},
		{"duplicate article", func(out *bootstrapResultWire) {
			out.OrgConfig.KB.Articles = append(out.OrgConfig.KB.Articles, out.OrgConfig.KB.Articles[0])
			out.KBList = append([]types.BootstrapArticleV1(nil), out.OrgConfig.KB.Articles...)
		}, "duplicate kb article"},
		{"invalid frontmatter", func(out *bootstrapResultWire) {
			out.OrgConfig.KB.Articles[0].Frontmatter = json.RawMessage(`{`)
			out.KBList = append([]types.BootstrapArticleV1(nil), out.OrgConfig.KB.Articles...)
		}, "frontmatter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := validBootstrapWire()
			tt.mutate(&out)
			namespace := "ns-1"
			if tt.name == "missing namespace" {
				namespace = ""
			}
			if err := validateBootstrapResult(out, namespace, "agent-1", "passport-1"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validation error = %v, want containing %q", err, tt.want)
			}
		})
	}
}
