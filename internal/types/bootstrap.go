package types

import (
	"encoding/json"
	"time"
)

// BootstrapSchemaVersionV1 is the strict nested org_config contract carried
// by the frozen version-1 wormhole.sync.bootstrap response.
const BootstrapSchemaVersionV1 = 1

type BootstrapProjectV1 struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Owner     string    `json:"owner"`
	CreatedAt time.Time `json:"created_at"`
}

type BootstrapAgentV1 struct {
	ID           string    `json:"id"`
	Owner        string    `json:"owner"`
	Model        string    `json:"model"`
	Capabilities []string  `json:"capabilities"`
	CreatedAt    time.Time `json:"created_at"`
}

type BootstrapPassportV1 struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	ProjectID    string    `json:"project_id"`
	Repositories []string  `json:"repositories"`
	Roles        []string  `json:"roles"`
	IssuedAt     time.Time `json:"issued_at"`
}

type BootstrapIdentityV1 struct {
	Agent       BootstrapAgentV1    `json:"agent"`
	Passport    BootstrapPassportV1 `json:"passport"`
	Permissions []string            `json:"permissions"`
}

type BootstrapChannelV1 struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type BootstrapEventV1 struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	ChannelID string          `json:"channel_id"`
	AgentID   string          `json:"agent_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
	CreatedAt time.Time       `json:"created_at"`
}

type BootstrapTaskV1 struct {
	ID           string     `json:"id"`
	ProjectID    string     `json:"project_id"`
	ParentTaskID *string    `json:"parent_task_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	OwnerAgentID *string    `json:"owner_agent_id"`
	Status       string     `json:"status"`
	Priority     int        `json:"priority"`
	DueBy        *time.Time `json:"due_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type BootstrapArticleV1 struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Frontmatter   json.RawMessage `json:"frontmatter"`
	AuthorAgentID string          `json:"author_agent_id"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type BootstrapKBV1 struct {
	Articles []BootstrapArticleV1 `json:"articles"`
}

// BootstrapOrgConfigV1 is one complete, project-scoped bootstrap snapshot.
// IntegrationManifestMetadata must be exactly JSON null in version 1 until
// issue #54 defines manifest storage and distribution.
type BootstrapOrgConfigV1 struct {
	SchemaVersion               int                  `json:"schema_version"`
	Project                     BootstrapProjectV1   `json:"project"`
	Identity                    BootstrapIdentityV1  `json:"identity"`
	Channels                    []BootstrapChannelV1 `json:"channels"`
	Events                      []BootstrapEventV1   `json:"events"`
	Tasks                       []BootstrapTaskV1    `json:"tasks"`
	KB                          BootstrapKBV1        `json:"kb"`
	IntegrationManifestMetadata json.RawMessage      `json:"integration_manifest_metadata"`
}
