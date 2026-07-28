package projectstate

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

var (
	ErrInvalidSnapshot       = errors.New("projectstate: invalid snapshot")
	ErrUnknownVersion        = errors.New("projectstate: unknown version")
	ErrUnknownKind           = errors.New("projectstate: unknown kind")
	ErrBrokenReference       = errors.New("projectstate: broken reference")
	ErrTrackedSecret         = errors.New("projectstate: tracked secret")
	ErrInvalidActorEnvelope  = types.ErrInvalidActorEnvelope
	ErrOperationPrecondition = errors.New("projectstate: operation precondition failed")
	ErrImmutableEvent        = errors.New("projectstate: immutable event")
	ErrTombstoneDigest       = errors.New("projectstate: tombstone digest mismatch")
	ErrResurrectionDigest    = errors.New("projectstate: resurrection digest mismatch")
)

type Digest string

type File struct {
	Path string
	Data []byte
}

type Tree []File

type RecordKey struct {
	Kind string
	ID   string
}

type ExtensionV1 struct {
	SchemaVersion int             `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
}

type ExtensionsV1 map[string]ExtensionV1

type ConfigV1 struct {
	SnapshotVersion int                      `toml:"snapshot_version"`
	ProjectID       string                   `toml:"project_id"`
	Handle          types.ProjectHandle      `toml:"handle"`
	Repository      types.RepositoryIdentity `toml:"repository"`
}

type ProjectV1 struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Aliases       []string     `json:"aliases"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	Extensions    ExtensionsV1 `json:"extensions"`
}

type PublicKeyV1 struct {
	KeyID           string `json:"key_id"`
	Algorithm       string `json:"algorithm"`
	PublicKeyBase64 string `json:"public_key_base64"`
}

type ActorV1 struct {
	SchemaVersion int             `json:"schema_version"`
	Kind          string          `json:"kind"`
	ID            string          `json:"id"`
	ActorKind     types.ActorKind `json:"actor_kind"`
	DisplayName   string          `json:"display_name"`
	PublicKeys    []PublicKeyV1   `json:"public_keys"`
	Extensions    ExtensionsV1    `json:"extensions"`
}

type TaskV1 struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	ID            string       `json:"id"`
	ParentTaskID  *string      `json:"parent_task_id"`
	Title         string       `json:"title"`
	Description   string       `json:"description"`
	OwnerActorID  *string      `json:"owner_actor_id"`
	Status        string       `json:"status"`
	Priority      int          `json:"priority"`
	DueBy         *time.Time   `json:"due_by"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	Extensions    ExtensionsV1 `json:"extensions"`
}

type TaskLinkV1 struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	ID            string       `json:"id"`
	TaskID        string       `json:"task_id"`
	LinkType      string       `json:"link_type"`
	TargetID      string       `json:"target_id"`
	Extensions    ExtensionsV1 `json:"extensions"`
}

type KBArticleV1 struct {
	SchemaVersion     int                        `json:"schema_version"`
	Kind              string                     `json:"kind"`
	ID                string                     `json:"id"`
	Title             string                     `json:"title"`
	Frontmatter       map[string]json.RawMessage `json:"frontmatter"`
	AuthorActorID     string                     `json:"author_actor_id"`
	RelatedArticleIDs []string                   `json:"related_article_ids"`
	CreatedAt         time.Time                  `json:"created_at"`
	UpdatedAt         time.Time                  `json:"updated_at"`
	Extensions        ExtensionsV1               `json:"extensions"`
}

type ChannelV1 struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	CreatedAt     time.Time    `json:"created_at"`
	Extensions    ExtensionsV1 `json:"extensions"`
}

type EventV1 struct {
	SchemaVersion int             `json:"schema_version"`
	Kind          string          `json:"kind"`
	ID            string          `json:"id"`
	ChannelID     string          `json:"channel_id"`
	ActorID       string          `json:"actor_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
	Note          *string         `json:"note"`
	CreatedAt     time.Time       `json:"created_at"`
	Extensions    ExtensionsV1    `json:"extensions"`
}

type GitLinkV1 struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	ID            string       `json:"id"`
	TaskID        *string      `json:"task_id"`
	Repository    string       `json:"repository"`
	CommitSHA     *string      `json:"commit_sha"`
	PRURL         *string      `json:"pr_url"`
	Summary       string       `json:"summary"`
	ActorID       string       `json:"actor_id"`
	CreatedAt     time.Time    `json:"created_at"`
	Extensions    ExtensionsV1 `json:"extensions"`
}

type TombstoneV1 struct {
	SchemaVersion        int                 `json:"schema_version"`
	Kind                 string              `json:"kind"`
	ID                   string              `json:"id"`
	EntityKind           string              `json:"entity_kind"`
	DeletedContentDigest Digest              `json:"deleted_content_digest"`
	DeletedBodyDigest    *Digest             `json:"deleted_body_digest"`
	DeletedBy            types.ActorEnvelope `json:"deleted_by"`
	DeletedAt            time.Time           `json:"deleted_at"`
	Extensions           ExtensionsV1        `json:"extensions"`
}

type RemotesV1 struct {
	Version int            `toml:"version"`
	Fabrics []FabricHintV1 `toml:"fabric"`
}

type FabricHintV1 struct {
	Alias              string                   `toml:"alias"`
	URL                string                   `toml:"url"`
	InstanceID         string                   `toml:"instance_id"`
	RemoteProjectID    string                   `toml:"remote_project_id"`
	ExpectedRepository types.RepositoryIdentity `toml:"expected_repository"`
	Mode               string                   `toml:"mode"`
}

type Snapshot struct {
	Config    ConfigV1
	Remotes   *RemotesV1
	Project   ProjectV1
	Actors    map[string]Record[ActorV1]
	Tasks     map[string]Record[TaskV1]
	TaskLinks map[string]Record[TaskLinkV1]
	Articles  map[string]KBRecord
	Channels  map[string]Record[ChannelV1]
	Events    map[string]EventV1
	GitLinks  map[string]Record[GitLinkV1]
	Digest    Digest
}

type Record[T any] struct {
	Value     *T
	Tombstone *TombstoneV1
}

type KBRecord struct {
	Value     *KBArticleV1
	Tombstone *TombstoneV1
	Body      []byte
}
