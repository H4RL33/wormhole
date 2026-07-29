# Git-Native Portable State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Implement Slice A so canonical Git-tracked Wormhole state can be validated by both Gateway and later Fabric code, while each local checkout has an isolated durable overlay, semantic import/rebase/stash flow, and recoverable checkpoint.

**Architecture:** internal/types/projectstate owns the canonical v1 schemas, TOML/JSON codecs, validation, and tree digest. Plain cross-layer identity and workspace values live in internal/types. internal/runtime/projectstate owns operation composition, a rebuildable snapshot-backed workspace projection, semantic diff/merge, Git observation, workspace service, checkpoint publication, and recovery over explicitly scoped localstore repositories. Existing local task/KB/event/Git tables become migration/read-only inputs; all current local pillar reads come from the composed snapshot and all writes append typed overlay operations.

**Tech Stack:** Go 1.26.5; standard library; existing github.com/BurntSushi/toml v1.6.0 for canonical config/remotes TOML; existing modernc SQLite; existing golang.org/x/sys for safe platform filesystem calls; read-only git subprocesses with hooks and optional locks disabled.

## Global Constraints

- Authority order is RFC-0001, RFC-0003 amendments, the approved 2026-07-28 architecture, implementation rules, then non-conflicting code.
- internal/types remains stdlib-only. internal/types/projectstate may import internal/types, stdlib, and existing BurntSushi/toml. It never imports runtime, Core, MCP, or commands.
- internal/runtime/projectstate may import internal/types, internal/types/projectstate, internal/runtime/localstore, stdlib, and existing x/sys. It never imports Core or MCP.
- Every localstore operation takes both project_id and workspace_id. Tests cover mismatched project/workspace pairs and independent valid workspaces.
- Git is sole accepted truth. Import and checkpoint do not advance the accepted base. ObserveGitBase advances it only after independently reading and validating the exact checked-out Git commit.
- Checkpoint never commits, stages, untracks, or pushes. Migration never modifies the Git index.
- V1 rejects unknown fields except the explicit typed extensions envelope. Unknown versions and kinds fail closed.
- New local writes require assurance=local through ValidateLocalAction. Legacy/unknown envelopes are accepted only through ValidateHistorical while decoding or migrating existing history; public-key-continuity and private-authenticated remain valid structurally but are issued and authorized by later Fabric/identity work.
- Provider repository-ID discovery is deferred. A supplied immutable ID is validated against config; no provider network call occurs.
- Linux uses atomic directory exchange. Every supported non-exchange filesystem uses the durable fallback journal. ErrCheckpointUnsupported is returned before mutation only when required durability primitives are proven unavailable.
- Existing replica tables are read models only. A strict-decoded snapshot start, its
  explicit initial through-generation, and strict-decoded active overlay operations
  are the only Compose inputs.
- A stash persists the semantic pre-stash base separately from the selected Compose
  start. Its existing `operations_json` column holds the selected start tree/digest,
  explicit initial through-generation, and only active stored operations above that
  boundary; it never reclassifies already-absorbed rows at or below the boundary.
- Events and Git links are live-only immutable add-only records. Exact canonical
  same-ID replay is idempotent; unequal same-ID content uses one generic immutable-
  record error/conflict/direct-delta contract. Neither kind has tombstone or
  resurrection semantics.
- Snapshot version, project ID, and repository identity are immutable binding fields.
  Config.Handle and Remotes are Git-base-owned and never overlay-owned.
- Focused RED precedes implementation, GREEN precedes each commit, and final merged statement coverage remains at least 80%.
- No new dependency, ORM, singleton, init registration, panic control flow, project hook execution, repository-provided executable, or implicit network request.

---

## Frozen package ownership and wire contracts

### Cross-layer plain types

Create internal/types/identity.go:

~~~go
package types

type ActorKind string
const (
    ActorHuman ActorKind = "human"
    ActorAgent ActorKind = "agent"
)
type Assurance string
const (
    AssuranceLocal Assurance = "local"
    AssuranceLegacy Assurance = "legacy"
    AssuranceUnknown Assurance = "unknown"
    AssurancePublicKeyContinuity Assurance = "public-key-continuity"
    AssurancePrivateAuthenticated Assurance = "private-authenticated"
)
type ActorEnvelope struct {
    ActorKind ActorKind `json:"actor_kind"`
    HumanPrincipalID string `json:"human_principal_id,omitempty"`
    AgentID string `json:"agent_id,omitempty"`
    AccountableHumanID string `json:"accountable_human_id,omitempty"`
    SessionID string `json:"session_id,omitempty"`
    HarnessName string `json:"harness_name,omitempty"`
    HarnessVersion string `json:"harness_version,omitempty"`
    ModelName string `json:"model_name,omitempty"`
    ModelVersion string `json:"model_version,omitempty"`
    Assurance Assurance `json:"assurance"`
    OccurredAt time.Time `json:"occurred_at"`
}
func (e ActorEnvelope) PrincipalID() string
func (e ActorEnvelope) Validate() error
func (e ActorEnvelope) ValidateLocalAction() error
func (e ActorEnvelope) ValidateHistorical() error
~~~

This is the final ActorEnvelope name and field set consumed by every later plan. PrincipalID returns HumanPrincipalID for ActorHuman and AgentID for ActorAgent. Validate is structural only: it accepts all five known assurances and rejects unknown assurance values. Human envelopes require HumanPrincipalID and empty AgentID/AccountableHumanID. Agent envelopes require AgentID and empty HumanPrincipalID. Every agent envelope with local, public-key-continuity, or private-authenticated assurance additionally requires AccountableHumanID, SessionID, HarnessName, and HarnessVersion; ModelName and ModelVersion are either both empty or both non-empty. ValidateLocalAction calls Validate, requires assurance exactly local, and therefore requires the complete accountable-human/session/harness provenance for agents; it rejects legacy, unknown, public, and private assurances. ValidateHistorical calls Validate and is the only API allowed to accept legacy/unknown envelopes, exclusively while decoding or migrating already-existing history. No normalization or migration upgrades assurance. OccurredAt is non-zero UTC for every envelope.

Create internal/types/workspace.go:

~~~go
package types

type WorkspaceID string
type WorkspaceScope struct {
    ProjectID string `json:"project_id"`
    WorkspaceID WorkspaceID `json:"workspace_id"`
}
type ProjectHandle struct { Namespace, Name string }
type RepositoryIdentity struct {
    Provider string `json:"provider" toml:"provider"`
    ImmutableID string `json:"immutable_id" toml:"immutable_id"`
    CanonicalRemote string `json:"canonical_remote" toml:"canonical_remote"`
}
type CheckoutIdentity struct {
    CanonicalPath string
    Device uint64
    Inode uint64
}
type WorkspaceContext struct {
    WorkingDirectory string `json:"working_directory"`
}
type WorkspaceBinding struct {
    Scope WorkspaceScope
    Checkout CheckoutIdentity
    Repository RepositoryIdentity
    AcceptedRef string
    AcceptedCommitSHA string
    AcceptedTreeDigest string
}
func (c WorkspaceContext) Validate() error
func (b WorkspaceBinding) Validate() error
~~~

WorkspaceContext is the final private observed-context type: it contains only WorkingDirectory, which must be absolute, clean, non-empty, and NUL-free. It is never part of a public CLI/MCP schema. WorkspaceBinding is the final shared name and exact field shape used by every later plan; no alternative WorkspaceBindingContext is permitted. Registration and the private resolver both return WorkspaceBinding. AcceptedTreeDigest is a string specifically to avoid an internal/types to internal/types/projectstate import cycle; Validate requires it to match ^sha256:[0-9a-f]{64}$ before runtime converts it to projectstate.Digest. The binding contains no actor, root alias, cwd, credential, profile, stream, or Fabric field. RepositoryIdentity requires a normalized remote without credentials, fragments, dot segments, or trailing .git/slash. Provider is empty only for local-only; otherwise ImmutableID is required and matches ^[A-Za-z0-9._:-]{1,255}$.

All project, actor, task, task-link, article, channel, event, and Git-link IDs are canonical lower-case UUID strings; operation, stash, journal, and workspace IDs are newly generated canonical lower-case UUIDs. Conflict IDs are deterministic `sha256:<lowerhex>` values over the frozen canonical preimage, never UUIDs. Handle namespace/name components match ^[a-z0-9][a-z0-9_-]{0,62}$ and are display aliases, never keys. Commit IDs are lower-case 40- or 64-hex object IDs, and observed branch refs are empty for detached HEAD or match refs/heads/<non-empty-refname>.

### Canonical v1 schemas

Create internal/types/projectstate. Every JSON struct uses integer schema_version=1, exact kind, exact id, and extensions. Extensions is:

~~~go
type ExtensionV1 struct {
    SchemaVersion int `json:"schema_version"`
    Data json.RawMessage `json:"data"`
}
type ExtensionsV1 map[string]ExtensionV1
~~~

Keys match reverse-DNS ^[a-z0-9]+(?:[.-][a-z0-9]+)+$; Data is one canonical JSON object. Recursive keys named token, access_token, refresh_token, password, secret, private_key, passport, credential, session_cookie, absolute_path, checkout_path, or workspace_id reject the tree.

Exact record schemas:

~~~go
type ConfigV1 struct {
    SnapshotVersion int `toml:"snapshot_version"`
    ProjectID string `toml:"project_id"`
    Handle types.ProjectHandle `toml:"handle"`
    Repository types.RepositoryIdentity `toml:"repository"`
}
type ProjectV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // project
    ID string `json:"id"`
    Name string `json:"name"`
    Aliases []string `json:"aliases"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type PublicKeyV1 struct {
    KeyID string `json:"key_id"`
    Algorithm string `json:"algorithm"` // ed25519
    PublicKeyBase64 string `json:"public_key_base64"`
}
type ActorV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // actor
    ID string `json:"id"`
    ActorKind types.ActorKind `json:"actor_kind"`
    DisplayName string `json:"display_name"`
    PublicKeys []PublicKeyV1 `json:"public_keys"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type TaskV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // task
    ID string `json:"id"`
    ParentTaskID *string `json:"parent_task_id"`
    Title string `json:"title"`
    Description string `json:"description"`
    OwnerActorID *string `json:"owner_actor_id"`
    Status string `json:"status"` // todo,wip,blocked,done
    Priority int `json:"priority"`
    DueBy *time.Time `json:"due_by"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type TaskLinkV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // task_link
    ID string `json:"id"`
    TaskID string `json:"task_id"`
    LinkType string `json:"link_type"` // kb_article,task,event,git_link
    TargetID string `json:"target_id"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type KBArticleV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // kb_article
    ID string `json:"id"`
    Title string `json:"title"`
    Frontmatter map[string]json.RawMessage `json:"frontmatter"`
    AuthorActorID string `json:"author_actor_id"`
    RelatedArticleIDs []string `json:"related_article_ids"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type ChannelV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // channel
    ID string `json:"id"`
    Name string `json:"name"`
    CreatedAt time.Time `json:"created_at"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type EventV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // event
    ID string `json:"id"`
    ChannelID string `json:"channel_id"`
    ActorID string `json:"actor_id"`
    EventType string `json:"event_type"`
    Payload json.RawMessage `json:"payload"`
    Note *string `json:"note"`
    CreatedAt time.Time `json:"created_at"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type GitLinkV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // git_link
    ID string `json:"id"`
    TaskID *string `json:"task_id"`
    Repository string `json:"repository"`
    CommitSHA *string `json:"commit_sha"`
    PRURL *string `json:"pr_url"`
    Summary string `json:"summary"`
    ActorID string `json:"actor_id"`
    CreatedAt time.Time `json:"created_at"`
    Extensions ExtensionsV1 `json:"extensions"`
}
type TombstoneV1 struct {
    SchemaVersion int `json:"schema_version"`
    Kind string `json:"kind"` // tombstone
    ID string `json:"id"`
    EntityKind string `json:"entity_kind"`
    DeletedContentDigest Digest `json:"deleted_content_digest"`
    DeletedBodyDigest *Digest `json:"deleted_body_digest"`
    DeletedBy types.ActorEnvelope `json:"deleted_by"`
    DeletedAt time.Time `json:"deleted_at"`
    Extensions ExtensionsV1 `json:"extensions"`
}
~~~

Remotes TOML is optional and exactly:

~~~go
type RemotesV1 struct {
    Version int `toml:"version"` // 1
    Fabrics []FabricHintV1 `toml:"fabric"`
}
type FabricHintV1 struct {
    Alias string `toml:"alias"`
    URL string `toml:"url"`
    InstanceID string `toml:"instance_id"`
    RemoteProjectID string `toml:"remote_project_id"`
    ExpectedRepository types.RepositoryIdentity `toml:"expected_repository"`
    Mode string `toml:"mode"` // public or private
}
~~~

Hints sort by Alias, aliases are unique lowercase slugs, URL is absolute http/https with no userinfo/query/fragment, IDs are non-empty, repository identity passes the rule above, and credential-shaped TOML keys reject. Decode preserves a valid remotes document semantically; Encode deterministically renders it and never drops it.

Reference classes are frozen:

- Live-required: Task.ParentTaskID to task; Task.OwnerActorID to actor.
- Historical: TaskLink.TaskID and typed TargetID; KBArticle.AuthorActorID and RelatedArticleIDs; Event.ChannelID and ActorID; GitLink.TaskID and ActorID; Tombstone.DeletedBy.PrincipalID(). TaskLink LinkType resolves TargetID exactly as kb_article to KB article, task to task, event to event, and git_link to Git link.
- Missing targets always reject.
- A tombstone satisfies only historical references. A live-required reference to a tombstone rejects.
- Project ID must equal config project_id. Paths must equal entity IDs.
- Events and Git links are live-only immutable/add-only records. Their snapshot entries
  must contain a live value; exact canonical same-ID replays coalesce and unequal
  content conflicts. Tombstones are allowed only for actor, task, task_link,
  kb_article, and channel; never project, event, or git_link.
- KB tombstone requires DeletedBodyDigest and no body.md. Live KB requires one body.md.

### Canonical tree API

~~~go
package projectstate

type Digest string
type File struct { Path string; Data []byte }
type Tree []File
type RecordKey struct { Kind, ID string }
type Snapshot struct {
    Config ConfigV1
    Remotes *RemotesV1
    Project ProjectV1
    Actors map[string]Record[ActorV1]
    Tasks map[string]Record[TaskV1]
    TaskLinks map[string]Record[TaskLinkV1]
    Articles map[string]KBRecord
    Channels map[string]Record[ChannelV1]
    Events map[string]EventV1
    GitLinks map[string]Record[GitLinkV1]
    Digest Digest
}
type Record[T any] struct { Value *T; Tombstone *TombstoneV1 }
type KBRecord struct { Value *KBArticleV1; Tombstone *TombstoneV1; Body []byte }

func DecodeTree(tree Tree) (Snapshot, error)
func EncodeTree(snapshot Snapshot) (Tree, error)
func Validate(snapshot Snapshot) error
func DigestTree(tree Tree) (Digest, error)
func CanonicalJSON(value any) ([]byte, error)
func CanonicalMarkdown(body []byte) ([]byte, error)
func DecodeOperation(raw []byte) (OperationV1, error)
func CanonicalOperation(operation OperationV1) ([]byte, error)
func DigestCanonicalJSON(value any) (Digest, error)
func DigestCanonicalMarkdown(body []byte) (Digest, error)
~~~

Paths are sorted slash-relative to .wormhole. Digest input per file is uint64 big-endian path length, path bytes, uint64 big-endian data length, canonical data bytes; SHA-256 result is sha256:<lowerhex>. Encoding uses struct field order and recursively sorted map keys, LF, one trailing LF. Config/remotes TOML uses fixed field ordering and sorted fabric hints.

All operation content digests use the same `sha256:<lowerhex>` representation over one canonical byte sequence: `DigestCanonicalJSON` hashes `CanonicalJSON` of the live typed record or complete prior `TombstoneV1`, and `DigestCanonicalMarkdown` hashes `CanonicalMarkdown` of the KB body independently. The tree-digest length-prefix framing is not used for these single-value digests. `ExpectedContentDigest`, `ExpectedBodyDigest`, and `ExpectedTombstoneDigest` must equal those exact values before mutation; tests assert fixed golden digests rather than recomputing expectations through the production helper. `Digest` is declared in `internal/types/projectstate`; no `types.Digest` is introduced.

`DecodeOperation` uses `json.Decoder.DisallowUnknownFields`, rejects trailing JSON,
requires byte equality with a fresh canonical rendering, and validates schema version,
canonical operation ID, digest syntax, actor envelope, exact operation kind/payload,
and nested payload shape. It does not check a workspace's expected-view equality or
call `ValidateLocalAction`; the reducer and local service own those context checks.
`CanonicalOperation` validates the same envelope and nested payload semantics as
`DecodeOperation` before returning its one authoritative canonical byte rendering.
`DecodeOperation(CanonicalOperation(op))` must return the same operation, and strict
decoding requires the supplied bytes to byte-match that rendering; consumers never
extract an operation through a second permissive JSON path.
SQLite readers additionally require the row operation ID to equal the decoded ID.

### Typed operation and composition contract

~~~go
type OperationKind string
const (
    OperationPutRecord OperationKind = "put_record"
    OperationPutKBArticle OperationKind = "put_kb_article"
    OperationTombstone OperationKind = "tombstone"
    OperationResurrect OperationKind = "resurrect"
)
type RecordValueV1 struct {
    Project *ProjectV1 `json:"project,omitempty"`
    Actor *ActorV1 `json:"actor,omitempty"`
    Task *TaskV1 `json:"task,omitempty"`
    TaskLink *TaskLinkV1 `json:"task_link,omitempty"`
    Channel *ChannelV1 `json:"channel,omitempty"`
    Event *EventV1 `json:"event,omitempty"`
    GitLink *GitLinkV1 `json:"git_link,omitempty"`
}
type PutRecordV1 struct { Record RecordValueV1 `json:"record"` }
type PutKBArticleV1 struct { Record KBArticleV1 `json:"record"`; Body string `json:"body"` }
type TombstoneOperationV1 struct {
    Key RecordKey `json:"key"`
    ExpectedContentDigest Digest `json:"expected_content_digest"`
    ExpectedBodyDigest *Digest `json:"expected_body_digest"`
}
type ResurrectOperationV1 struct {
    Key RecordKey `json:"key"`
    ExpectedTombstoneDigest Digest `json:"expected_tombstone_digest"`
    Record RecordValueV1 `json:"record"`
    KBRecord *KBArticleV1 `json:"kb_record,omitempty"`
    KBBody *string `json:"kb_body,omitempty"`
}
type OperationV1 struct {
    SchemaVersion int `json:"schema_version"`
    ID string `json:"id"`
    Kind OperationKind `json:"kind"`
    ExpectedViewDigest Digest `json:"expected_view_digest"`
    Actor types.ActorEnvelope `json:"actor"`
    PutRecord *PutRecordV1 `json:"put_record,omitempty"`
    PutKBArticle *PutKBArticleV1 `json:"put_kb_article,omitempty"`
    Tombstone *TombstoneOperationV1 `json:"tombstone,omitempty"`
    Resurrect *ResurrectOperationV1 `json:"resurrect,omitempty"`
}

func ApplyOperation(snapshot Snapshot, operation OperationV1) (Snapshot, error)
~~~

Exactly one payload matches Kind. PutRecord contains exactly one typed pointer.
ApplyOperation is stateless, clones its input, requires
operation.ExpectedViewDigest to equal the input digest, calls
operation.Actor.Validate, and rejects legacy/unknown assurance for every new
operation. It coalesces a byte-identical existing Event or Git link and returns
`ErrImmutableRecord` for an unequal replacement of either kind. `ErrImmutableEvent`
and an optional Git-link-specific name may remain aliases to `ErrImmutableRecord`
while alpha callers migrate; they do not define separate behaviour. Event and Git-
link tombstone/resurrection operations reject. Existing mutable Project, Task, KB-
article, and Channel values retain their exact `created_at` on ordinary Put; an
explicit digest-proven resurrection may carry a fresh valid `created_at` because a
tombstone does not retain prior record bytes. ApplyOperation prevents project-ID
change, computes TombstoneV1 from current canonical record/body bytes and the
operation actor, requires resurrection to name the exact tombstone digest, validates
the result, recomputes its digest, and returns it. Local Service.Apply/ApplyBatch and
every local public domain mutation additionally call
operation.Actor.ValidateLocalAction before persistence; later Fabric may accept
public/private actors after its own authorization. Any error returns the original
snapshot unchanged. Runtime and Fabric must call this reducer and the exported
digest/operation decoders rather than duplicate immutable-record, digest, tombstone,
resurrection, or strict-decoding logic.

Runtime composition:

~~~go
type StoredOperation struct {
    Generation int64 `json:"generation"`
    Operation projectstate.OperationV1 `json:"operation"`
}
type ComposedView struct {
    Snapshot projectstate.Snapshot
    AppliedOperationIDs []string
    ThroughGeneration int64
}
func Compose(start projectstate.Snapshot, initialThroughGeneration int64, operations []StoredOperation) (ComposedView, error)
~~~

`start` is already strict-decoded, canonically re-encodable, digest-checked, and
binding-validated. `initialThroughGeneration` is zero for an accepted/direct start
and `rebased_through_generation` for a rebased start. Stored operations have already
passed `projectstate.DecodeOperation` plus row-ID/state checks; their generations
must be strictly increasing and greater than the initial boundary. Compose calls
`projectstate.ApplyOperation` in supplied order, rejects duplicate, unordered, or
pre-boundary generations, starts `ThroughGeneration` at the supplied boundary, and
advances it only to the last operation actually applied. It never chooses a
candidate implicitly, depends on SQL's accidental row order, or reads legacy replica
tables. Fabric later persists/replays the exact same OperationV1 and encoded Snapshot
values through ApplyOperation without importing internal/runtime/projectstate.

Task 2's status type is extended, not renamed or moved:

~~~go
type WorkspaceStatus struct {
    Binding types.WorkspaceBinding
    State string
    AcceptedSnapshot projectstate.Snapshot
    CandidateDigest projectstate.Digest
    OverlayGeneration int64
}
~~~

`CandidateDigest` is the exact composed snapshot digest. `OverlayGeneration` is
the final `ThroughGeneration`: zero for an accepted/direct start with no applied
overlay, and the retained non-zero replay boundary when a rebased candidate has
already absorbed operations. `State` remains `string`; this plan introduces no
`WorkspaceState` type.

---

### Task 1: Shared schemas, codec, validator, digest, and module map

**Files:**
- Create: internal/types/identity.go
- Create: internal/types/identity_test.go
- Create: internal/types/workspace.go
- Create: internal/types/workspace_test.go
- Create: internal/types/projectstate/types.go
- Create: internal/types/projectstate/codec.go
- Create: internal/types/projectstate/validate.go
- Create: internal/types/projectstate/operation.go
- Create: internal/types/projectstate/codec_test.go
- Create: internal/types/projectstate/validate_test.go
- Create: internal/types/projectstate/operation_test.go
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/config.toml
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/remotes.toml
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/project.json
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/actors/11111111-1111-4111-8111-111111111111.json
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/tasks/22222222-2222-4222-8222-222222222222.json
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/tasks/links/33333333-3333-4333-8333-333333333333.json
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/kb/44444444-4444-4444-8444-444444444444/record.json
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/kb/44444444-4444-4444-8444-444444444444/body.md
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/channels/55555555-5555-4555-8555-555555555555.json
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/events/66666666-6666-4666-8666-666666666666.json
- Create: internal/types/projectstate/testdata/v1/valid/.wormhole/state/v1/git-links/77777777-7777-4777-8777-777777777777.json
- Create: internal/types/projectstate/testdata/v1/duplicate-id/.wormhole
- Create: internal/types/projectstate/testdata/v1/path-id-mismatch/.wormhole
- Create: internal/types/projectstate/testdata/v1/secret-field/.wormhole
- Create: internal/types/projectstate/testdata/v1/unknown-version/.wormhole
- Create: internal/types/projectstate/testdata/v1/dangling-live-reference/.wormhole
- Create: internal/types/projectstate/testdata/v1/live-reference-tombstone/.wormhole
- Create: internal/types/projectstate/testdata/v1/historical-reference-tombstone/.wormhole
- Create: internal/types/projectstate/testdata/v1/kb-tombstone-body/.wormhole
- Create: internal/types/projectstate/testdata/v1/event-id-collision/.wormhole
- Create: internal/types/projectstate/testdata/v1/bad-remotes/.wormhole

The valid fixture uses project 00000000-0000-4000-8000-000000000001, actor 11111111-1111-4111-8111-111111111111, task 22222222-2222-4222-8222-222222222222, task link 33333333-3333-4333-8333-333333333333, article 44444444-4444-4444-8444-444444444444, channel 55555555-5555-4555-8555-555555555555, event 66666666-6666-4666-8666-666666666666, and Git link 77777777-7777-4777-8777-777777777777 in both paths and record IDs. Invalid fixtures derive from these exact bytes by one documented mutation each.
- Modify: docs/implementation-rules.md

**Interfaces:**
- Consumes: existing BurntSushi/toml dependency.
- Produces: all frozen cross-layer types, canonical schema types, codec functions,
  OperationV1, ApplyOperation, and errors ErrInvalidSnapshot, ErrUnknownVersion,
  ErrUnknownKind, ErrBrokenReference, ErrTrackedSecret, ErrInvalidActorEnvelope,
  ErrOperationPrecondition, ErrImmutableRecord (with ErrImmutableEvent and an optional
  Git-link compatibility alias), ErrTombstoneDigest, and ErrResurrectionDigest.

- [ ] **Step 1: Write RED golden, schema, reference, remotes, and digest tests**

~~~go
func TestCanonicalV1RoundTrip(t *testing.T) {
    tree := readFixtureTree(t, "testdata/v1/valid/.wormhole")
    snapshot, err := DecodeTree(tree)
    if err != nil { t.Fatal(err) }
    rendered, err := EncodeTree(snapshot)
    if err != nil { t.Fatal(err) }
    assertTreeEqual(t, tree, rendered)
    if snapshot.Remotes == nil || len(snapshot.Remotes.Fabrics) != 2 { t.Fatalf("%+v", snapshot.Remotes) }
}
func TestReferenceClasses(t *testing.T) {
    if _, err := DecodeTree(readFixtureTree(t, "testdata/v1/live-reference-tombstone/.wormhole")); !errors.Is(err, ErrBrokenReference) { t.Fatal(err) }
    if _, err := DecodeTree(readFixtureTree(t, "testdata/v1/historical-reference-tombstone/.wormhole")); err != nil { t.Fatal(err) }
}
~~~

Run: go test ./internal/types ./internal/types/projectstate -run 'Test(ActorEnvelope|RepositoryIdentity|CanonicalV1|ReferenceClasses|Digest|Remotes|ApplyOperation|Rejects)' -count=1
Expected: FAIL because shared types and package are absent.

- [ ] **Step 2: Implement the frozen contracts above**

Use json.Decoder.DisallowUnknownFields for every struct, then require all exact kind/version/path invariants. Canonicalize extension Data and frontmatter map values with the same JSON encoder. Reject duplicate semantic IDs even if paths differ. Parse TOML with toml.Decode and reject undecoded keys using MetaData.Undecoded. Encode ConfigV1 and RemotesV1 with explicit ordered writers so bytes are stable. Implement ApplyOperation as copy-on-write over typed maps and run Validate plus EncodeTree/DigestTree before success.

Shared RED cases are TestApplyOperationPutRecord, TestApplyOperationRejectsStaleDigest, TestApplyOperationRejectsUnequalImmutableRecord (covering Event and Git link), TestApplyOperationCreatesExactTombstoneDigests, TestApplyOperationRejectsWrongTombstoneDigest, TestApplyOperationResurrectsMatchingTombstone, TestApplyOperationRejectsWrongResurrectionDigest, and TestApplyOperationErrorLeavesInputUnchanged.

Actor RED cases are TestActorEnvelopeValidateHuman, TestActorEnvelopeValidateLocalAgentRequiresAccountabilitySessionHarness, TestActorEnvelopeValidatePublicAndPrivateAgents, TestActorEnvelopeValidateHistoricalLegacyAllowsMissingProvenance, TestActorEnvelopeValidateHistoricalUnknownAllowsMissingProvenance, TestActorEnvelopeValidateLocalActionRejectsLegacyUnknownPublicPrivate, TestActorEnvelopePrincipalID, and TestActorEnvelopeHistoricalNeverUpgrades. Canonical decode/migration calls ValidateHistorical; Service.Apply/ApplyBatch and local domain writes call ValidateLocalAction.

- [ ] **Step 3: Update the implementation-rules module map**

Add rows declaring internal/types stdlib-only plain shared types and internal/types/projectstate as canonical snapshot schema/codec/validator/digest allowed to import internal/types, stdlib, and BurntSushi/toml. Replace any statement claiming all internal/types subpackages are stdlib-only with this exact exception. State runtime and Fabric must consume this package rather than duplicate canonical schemas.

- [ ] **Step 4: Run GREEN and commit**

Run: go test ./internal/types ./internal/types/projectstate -count=1
Expected: PASS.

~~~bash
git add internal/types docs/implementation-rules.md
git commit -m "feat: freeze portable state v1 schemas"
~~~

### Task 2: Scoped SQLite schema and idempotent registration

**Files:**
- Modify: internal/runtime/localstore/localstore.go
- Create: internal/runtime/localstore/migrations.go
- Create: internal/runtime/localstore/migrations_test.go
- Create: internal/runtime/localstore/migrations/000001_portable_state.sql
- Create: internal/runtime/localstore/workspace_repo.go
- Create: internal/runtime/localstore/workspace_repo_test.go
- Modify: internal/runtime/localstore/corrupt_schema_coverage_test.go
- Create: internal/runtime/projectstate/service.go
- Create: internal/runtime/projectstate/service_test.go
- Create: internal/runtime/projectstate/working_tree.go
- Create: internal/runtime/projectstate/working_tree_test.go

**Interfaces:**
- Consumes: Task 1 shared types and DecodeTree.
- Produces: Store workspace methods, Service RegisterWorkspace and Status, and the sole Gateway SQLite durability policy used by later materialization journals.

Exact registration contract:

~~~go
type RegisterWorkspaceRequest struct {
    Root string
    ExpectedProjectID string
    ExpectedRepository types.RepositoryIdentity
    ExpectedCommit string
}
type RegisterWorkspaceResult struct {
    Binding types.WorkspaceBinding
    Created bool
}
type ServiceConfig struct {
    LegacyIntegrationBackupRoot string
}
func NewService(repo *localstore.WorkspaceRepo, config ServiceConfig) (*Service, error)
func (r *WorkspaceRepo) ResolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error)
func (r *WorkspaceRepo) RegisteredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error)
func (s *Service) RegisterWorkspace(ctx context.Context, req RegisterWorkspaceRequest) (RegisterWorkspaceResult, error)
func (s *Service) ResolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error)
func (s *Service) RegisteredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error)
func (s *Service) Status(ctx context.Context, scope types.WorkspaceScope) (WorkspaceStatus, error)
~~~

Registration resolves a non-symlink canonical root, captures device/inode, independently verifies HEAD equals ExpectedCommit, reads the committed .wormhole tree, validates project/repository equality, and creates a UUID. It returns types.WorkspaceBinding with AcceptedCommitSHA=req.ExpectedCommit and AcceptedTreeDigest=string(decoded.Digest). Repeating the same project, checkout identity, canonical path, repository, commit, and digest returns the identical WorkspaceBinding with Created=false and performs no write. Same checkout identity with another project/repository returns ErrCheckoutCollision. Same project in another worktree or clone gets a distinct ID.

ServiceConfig is process-private configuration. `LegacyIntegrationBackupRoot` is an absolute trusted XDG-derived path outside every repository; NewService canonicalizes it, creates it owner-only when non-empty, and rejects relative, symlinked, or repository-contained roots. Task 6 requires it before legacy migration; Tasks 2–5 may leave it empty in tests that never call that method. No public command, MCP argument, tracked file, or Fabric value may override it.

ResolveWorkingDirectory validates the private observed WorkspaceContext, canonicalizes/evaluates its WorkingDirectory, chooses the longest boundary-aware registered checkout ancestor, then re-stats and requires the stored device/inode before returning the exact binding; a sibling path prefix, replaced checkout, ambiguous match, or unregistered directory fails closed. The repository exposes the same exact signature for deterministic scoped lookup and returns stored values only; Service owns filesystem canonicalization and identity revalidation. RegisteredWorkspaces returns every binding sorted by project_id then workspace_id and validates each value, enabling startup recovery without a current-workspace default.

Exact local migration 1 DDL:

~~~sql
CREATE TABLE gateway_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE workspace_bindings (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  checkout_path TEXT NOT NULL,
  checkout_device INTEGER NOT NULL,
  checkout_inode INTEGER NOT NULL,
  repository_identity_json TEXT NOT NULL,
  accepted_ref TEXT NOT NULL,
  accepted_commit TEXT NOT NULL,
  accepted_digest TEXT NOT NULL,
  accepted_snapshot BLOB NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('clean','pending','conflicted','blocked')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  UNIQUE(checkout_device,checkout_inode)
);
CREATE TABLE workspace_candidates (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  accepted_base_digest TEXT NOT NULL,
  working_tree_digest TEXT NOT NULL,
  direct_tree BLOB NOT NULL,
  rebased_tree BLOB,
  rebased_through_generation INTEGER NOT NULL DEFAULT 0,
  imported_by TEXT NOT NULL,
  imported_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_overlay_operations (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  generation INTEGER NOT NULL CHECK(generation > 0),
  operation_id TEXT NOT NULL,
  operation_json TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('active','rebased','stashed','materialized')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,generation),
  UNIQUE(project_id,workspace_id,operation_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_materializations (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  journal_id TEXT NOT NULL,
  expected_live_digest TEXT NOT NULL,
  accepted_base_digest TEXT NOT NULL,
  checkout_path TEXT NOT NULL,
  checkout_device INTEGER NOT NULL,
  checkout_inode INTEGER NOT NULL,
  prior_tree_digest TEXT NOT NULL,
  candidate_digest TEXT NOT NULL,
  through_generation INTEGER NOT NULL,
  prior_tree BLOB NOT NULL,
  candidate_tree BLOB NOT NULL,
  stage_path TEXT NOT NULL,
  backup_path TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('prepared','published','accepted','recovered_old','recovered_new')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,journal_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_stashes (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  stash_id TEXT NOT NULL,
  source_base_digest TEXT NOT NULL,
  candidate_digest TEXT NOT NULL,
  source_tree BLOB NOT NULL,
  composed_tree BLOB NOT NULL,
  operations_json TEXT NOT NULL,
  through_generation INTEGER NOT NULL,
  actor_json TEXT NOT NULL,
  label TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,stash_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE workspace_conflicts (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  conflict_id TEXT NOT NULL,
  record_kind TEXT NOT NULL,
  record_id TEXT NOT NULL,
  field_path TEXT NOT NULL,
  conflict_kind TEXT NOT NULL,
  base_json TEXT NOT NULL,
  ours_json TEXT NOT NULL,
  theirs_json TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('open','resolved')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,conflict_id),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE TABLE legacy_integration_state_migrations (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_digest TEXT NOT NULL,
  outcome TEXT NOT NULL CHECK(outcome IN ('imported_move_pending','migrated_and_moved','migrated_tracked_source_retained','ignored_unsafe')),
  backup_path TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL,
  migrated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,source_digest),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE INDEX workspace_overlay_generation ON workspace_overlay_operations(project_id,workspace_id,generation);
CREATE INDEX workspace_open_conflicts ON workspace_conflicts(project_id,workspace_id,state);
CREATE INDEX workspace_recovery ON workspace_materializations(state,project_id,workspace_id);
CREATE UNIQUE INDEX legacy_integration_one_pending ON legacy_integration_state_migrations(project_id,workspace_id) WHERE outcome='imported_move_pending';
~~~

- [ ] **Step 1: Write correct RED registration/isolation tests**

~~~go
func TestWorkspaceScopeMismatchIsRejected(t *testing.T) {
    store := openWorkspaceStore(t)
    a := createBinding(t, store, "00000000-0000-4000-8000-000000000001", "/checkout-a")
    b := createBinding(t, store, "00000000-0000-4000-8000-000000000002", "/checkout-b")
    appendOperation(t, store, a, 1)
    if _, err := store.ListWorkspaceOperations(t.Context(), "00000000-0000-4000-8000-000000000001", b.WorkspaceID, 0); !errors.Is(err, localstore.ErrNotFound) { t.Fatal(err) }
}
func TestValidWorkspacesRemainIsolated(t *testing.T) {
    store := openWorkspaceStore(t)
    a := createBinding(t, store, "00000000-0000-4000-8000-000000000001", "/checkout-a")
    b := createBinding(t, store, "00000000-0000-4000-8000-000000000001", "/checkout-b")
    appendOperation(t, store, a, 1)
    if got := mustList(t, store, b); len(got) != 0 { t.Fatalf("%+v", got) }
}
~~~

Run: go test ./internal/runtime/localstore ./internal/runtime/projectstate -run 'Test(GatewayMigrationLedger|GatewayMigrationRollback|GatewayMigrationRejectsFutureVersion|GatewaySQLiteSynchronousFull|WorkspaceScopeMismatch|ValidWorkspacesRemainIsolated|RegisterWorkspaceIdempotent|RegisterWorkspaceCheckoutCollision|TwoWorktreesDistinct|ResolveWorkingDirectory|RegisteredWorkspaces)' -count=1
Expected: FAIL because migration, repositories, and service are absent.

- [ ] **Step 2: Implement migration, repositories, safe tree reader, and registration**

Embed internal/runtime/localstore/migrations/*.sql and expose const GatewaySchemaVersion = 1 plus func applyGatewayMigrations(ctx context.Context, db *sql.DB) error. The function acquires one dedicated connection, executes BEGIN IMMEDIATE, creates and shape-checks gateway_schema_migrations, rejects a recorded version greater than GatewaySchemaVersion, applies each missing numbered file once, inserts its version row, and commits. Any DDL or ledger-write failure rolls back the entire version. This ledger name and API are the only Gateway SQLite migration mechanism; Slice B/C owns `000002`, and Slice D appends `000003` and later files rather than creating another ledger or reusing a number.

Extend the existing SQLite DSN with `_pragma=synchronous(FULL)` while retaining WAL and the busy timeout, so every pooled and dedicated connection uses the same durability setting. `TestGatewaySQLiteSynchronousFull` opens and reopens a file-backed Store, checks `PRAGMA journal_mode` is `wal` and `PRAGMA synchronous` is `2` on both the ordinary database handle and a newly acquired dedicated connection, and proves a committed migration-ledger row survives reopen. Later journal preparation transactions rely on SQLite's WAL/FULL commit guarantee; they must not manually fsync the main database file while committed bytes may reside in the WAL.

All repository SELECT/UPDATE/DELETE statements include WHERE project_id=? AND workspace_id=?; first verify binding existence. Serialize snapshots/trees with canonical EncodeTree plus a length-prefixed file-list codec. Use BEGIN IMMEDIATE for next overlay generation and registration collision checks.

Add RED cases TestResolveWorkingDirectoryChild, TestResolveWorkingDirectoryRejectsSiblingPrefix, TestResolveWorkingDirectoryRejectsReplacedCheckout, TestResolveWorkingDirectoryLongestAncestor, and TestRegisteredWorkspacesStableAfterRestart. Every project/workspace/entity literal in executable tests is a canonical lower-case UUID; human-readable labels belong only in names and temporary paths.

- [ ] **Step 3: Run GREEN and commit**

Run: go test ./internal/runtime/localstore ./internal/runtime/projectstate -run 'Test(GatewayMigration|Workspace|Register|TwoWorktrees|WorkingTree)' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/localstore internal/runtime/projectstate
git commit -m "feat: register durable workspace bases"
~~~

### Task 3: Typed operations, Compose, semantic diff, and merge primitives

**Files:**
- Modify: internal/types/projectstate/types.go
- Modify: internal/types/projectstate/codec.go
- Modify: internal/types/projectstate/codec_test.go
- Modify: internal/types/projectstate/validate.go
- Modify: internal/types/projectstate/validate_test.go
- Modify: internal/types/projectstate/operation.go
- Modify: internal/types/projectstate/operation_test.go
- Create: internal/runtime/projectstate/compose.go
- Create: internal/runtime/projectstate/compose_test.go
- Create: internal/runtime/projectstate/diff.go
- Create: internal/runtime/projectstate/diff_test.go
- Create: internal/runtime/projectstate/merge.go
- Create: internal/runtime/projectstate/merge_test.go
- Modify: internal/runtime/projectstate/service.go
- Modify: internal/runtime/projectstate/service_test.go
- Modify: internal/runtime/localstore/workspace_repo.go
- Modify: internal/runtime/localstore/workspace_repo_test.go

**Interfaces:**
- Consumes: landed Task 1 OperationV1/codec/reducer and Task 2 persistence.
- Produces: the Task-1 immutable-record/strict-decoder correction,
  caller-owned immediate transaction seam, Compose, Apply, SemanticDiff,
  ThreeWayRebase, and exact conflict types used by Import, stash restore, Git
  observation, checkpoint, and later Fabric.

~~~go
type StoredOperation struct {
    Generation int64 `json:"generation"`
    Operation projectstate.OperationV1 `json:"operation"`
}
type ComposedView struct {
    Snapshot projectstate.Snapshot
    AppliedOperationIDs []string
    ThroughGeneration int64
}
func Compose(
    start projectstate.Snapshot,
    initialThroughGeneration int64,
    operations []StoredOperation,
) (ComposedView, error)

type WorkspaceStatus struct {
    Binding types.WorkspaceBinding
    State string
    AcceptedSnapshot projectstate.Snapshot
    CandidateDigest projectstate.Digest
    OverlayGeneration int64
}

type ChangeKind string
const (
    ChangeAdd ChangeKind = "add"
    ChangeModify ChangeKind = "modify"
    ChangeTombstone ChangeKind = "tombstone"
    ChangeResurrect ChangeKind = "resurrect"
)
type FieldValue struct {
    Present bool `json:"present"`
    Value json.RawMessage `json:"value,omitempty"`
}
type FieldChange struct { Path string; Before, After FieldValue }
type Change struct {
    Key projectstate.RecordKey
    Kind ChangeKind
    BeforeDigest, AfterDigest *projectstate.Digest
    BeforeBodyDigest, AfterBodyDigest *projectstate.Digest
    Fields []FieldChange
    Actor *types.ActorEnvelope
}
type Diff struct {
    BaseDigest projectstate.Digest
    ViewDigest projectstate.Digest
    Changes []Change
}
func SemanticDiff(base, view projectstate.Snapshot, actors map[projectstate.RecordKey]types.ActorEnvelope) (Diff, error)

type ConflictKind string
const (
    ConflictSameField ConflictKind = "same_field"
    ConflictMarkdown ConflictKind = "markdown"
    ConflictImmutableRecord ConflictKind = "immutable_record"
    ConflictTombstoneEdit ConflictKind = "tombstone_edit"
    ConflictTombstoneBody ConflictKind = "tombstone_body"
    ConflictInvalidResurrection ConflictKind = "invalid_resurrection"
)
type Conflict struct {
    ID string
    Key projectstate.RecordKey
    FieldPath string
    Kind ConflictKind
    Base, Ours, Theirs FieldValue
}
type MergeResult struct { Snapshot projectstate.Snapshot; Conflicts []Conflict }
func ThreeWayRebase(oldBase, newBase, candidate projectstate.Snapshot) (MergeResult, error)
func (s *Service) Apply(ctx context.Context, scope types.WorkspaceScope, operation projectstate.OperationV1) (WorkspaceStatus, error)
func (s *Service) ApplyBatch(ctx context.Context, scope types.WorkspaceScope, operations []projectstate.OperationV1) (WorkspaceStatus, error)
func (s *Service) Diff(ctx context.Context, scope types.WorkspaceScope) (Diff, error)
~~~

The localstore transaction seam is exact and reusable by Task 4:

~~~go
type WorkspaceCandidateRecord struct {
    AcceptedBaseDigest projectstate.Digest
    WorkingTreeDigest projectstate.Digest
    DirectSnapshot projectstate.Snapshot
    RebasedSnapshot *projectstate.Snapshot
    RebasedThroughGeneration int64
}
type WorkspaceOperationInsert struct {
    Generation int64
    OperationID string
    OperationJSON []byte
}
type WorkspaceMutationTx struct { /* repository-owned connection and scope */ }
type WorkspaceConflictGate interface {
    HasOpenConflicts(context.Context, types.WorkspaceScope) (bool, error)
}
func (r *WorkspaceRepo) WithImmediateWorkspace(
    ctx context.Context,
    scope types.WorkspaceScope,
    fn func(*WorkspaceMutationTx) error,
) error
func (r *WorkspaceRepo) HasOpenConflicts(ctx context.Context, scope types.WorkspaceScope) (bool, error)
func (tx *WorkspaceMutationTx) Workspace(ctx context.Context) (WorkspaceRecord, error)
func (tx *WorkspaceMutationTx) Candidate(ctx context.Context) (*WorkspaceCandidateRecord, error)
func (tx *WorkspaceMutationTx) ActiveOperationsAfter(ctx context.Context, generation int64) ([]WorkspaceOperation, error)
func (tx *WorkspaceMutationTx) NextGeneration(ctx context.Context) (int64, error)
func (tx *WorkspaceMutationTx) InsertActiveOperations(ctx context.Context, operations []WorkspaceOperationInsert) error
func (tx *WorkspaceMutationTx) HasOpenConflicts(ctx context.Context) (bool, error)
func (tx *WorkspaceMutationTx) SetStatus(ctx context.Context, state string) error
~~~

`WithImmediateWorkspace` owns one dedicated connection, `BEGIN IMMEDIATE`, and
commit/rollback. Every method uses the callback's exact project/workspace scope and
does not open a nested transaction. Candidate blobs pass the file-list decoder,
`projectstate.DecodeTree`, canonical re-encoding, recorded-digest checks, and immutable
snapshot-version/project/repository binding checks. `rebased_tree` requires a valid
non-negative boundary and `direct_tree`; a direct start has boundary zero. Active rows
are selected with `state='active' AND generation>? ORDER BY generation`. Runtime converts
each row to `StoredOperation` only after `projectstate.DecodeOperation`, canonical-byte,
row-ID, canonical UUID, positive generation, and exact state checks. The existing
standalone `AppendWorkspaceOperation` is removed; it may not remain as a mutation bypass.
`WorkspaceConflictGate` is declared in `internal/runtime/localstore`. The repository
method binds both supplied scope components; the transaction method has no scope
argument because it can query only the callback's already-bound exact workspace. Both
return true only for `state='open'`; resolved rows and conflicts in another project or
workspace do not block. `localstore.ErrWorkspaceConflicted` is the sole sentinel used by
checkpoint, stash, and writable-Fabric delivery. No runtime alias or alternate sentinel
declaration is added.

The Service selects `rebased_tree` plus its `rebased_through_generation` when present,
otherwise `direct_tree` with boundary zero, otherwise `accepted_snapshot` with boundary
zero. It passes that explicit start/boundary plus only later active rows to Compose. This
lets Task 4 absorb rows as `rebased` without replaying stale whole-view preconditions.

Apply delegates to ApplyBatch with one element. ApplyBatch rejects an empty batch, duplicate operation IDs, and every actor that fails ValidateLocalAction; under one BEGIN IMMEDIATE it composes the current view, applies operations in caller order through projectstate.ApplyOperation (each ExpectedViewDigest must chain to the prior result), allocates consecutive generations, and appends every row or none. This is the atomic path for task-status-plus-event and every other multi-record local mutation.

- [ ] **Step 1: Write RED shared immutable-record, strict-codec, digest, and created-at tests**

Add fixed-golden tests that `DecodeOperation` rejects unknown fields, trailing JSON,
non-canonical bytes, malformed/extra payloads, invalid actor/ID/digest values, and that
`DigestCanonicalJSON`/`DigestCanonicalMarkdown` produce the exact existing record/body/
tombstone digests. Add Event and Git-link cases proving exact replay coalesces, unequal
replay returns `ErrImmutableRecord`, and Git-link tombstone, resurrection, validation,
and tree decode reject. Add ordinary Project/Task/KB/Channel update cases proving a
changed `created_at` rejects and an explicit matching-digest resurrection can carry a
fresh valid timestamp.

Run: go test ./internal/types/projectstate -run 'Test(DecodeOperation|DigestCanonical|ApplyOperation.*Immutable|GitLink.*Tombstone|CreatedAt)' -count=1
Expected: FAIL because the generic immutable contract, strict decoder, exported digests,
Git-link live-only validation, and created-at guard are absent.

- [ ] **Step 2: Implement the shared Task-1 corrections**

Add `ErrImmutableRecord`; retain `ErrImmutableEvent` and, only if a caller needs it, a
Git-link name as aliases. Implement the three exported APIs exactly as frozen above.
Use the exported digest helpers inside ApplyOperation. Reject a Git-link tombstone in
both strict decode and Validate, remove Git-link from tombstone lookup/clear/apply paths,
and make existing Event/Git-link Put an exact-replay-only path. Compare `created_at`
before replacement of an existing live mutable record, but do not compare against a
tombstone during explicit resurrection.

- [ ] **Step 3: Write RED composition, status, transaction, corruption, and isolation tests**

~~~go
func TestApplyTransactionIsDurable(t *testing.T) {
    svc, scope := workspaceFixture(t)
    op := validPutTaskOperation(t, mustStatus(t, svc, scope).CandidateDigest)
    if _, err := svc.Apply(t.Context(), scope, op); err != nil { t.Fatal(err) }
    reopenService(t, svc)
    if got := mustStatus(t, svc, scope); got.OverlayGeneration != 1 { t.Fatalf("%+v", got) }
}
func TestThreeWayRebaseTombstoneEditConflicts(t *testing.T) {
    got, err := ThreeWayRebase(liveTask(t), tombstonedTask(t), editedTask(t))
    if err != nil { t.Fatal(err) }
    if len(got.Conflicts) != 1 || got.Conflicts[0].Kind != ConflictTombstoneEdit { t.Fatalf("%+v", got) }
}
~~~

Add `TestComposeRejectsUnorderedDuplicateAndPreBoundaryGenerations`,
`TestComposeCandidateSelectionAndRebasedGeneration`,
`TestComposeRejectsStalePersistedDigestChain`,
`TestStatusExposesCandidateDigestAndOverlayGeneration`,
`TestApplyBatchAppendsConsecutiveChainedOperationsAtomically`,
`TestApplyBatchSecondOperationFailureRollsBackEverything`,
`TestApplyBatchInsertOrStatusFailureRollsBackEverything`,
`TestApplyBatchRejectsEmptyDuplicateAndNonLocalActorsWithoutWrites`,
`TestApplyBatchExistingOperationIDCollisionRollsBackAll`, and
`TestApplyBatchScopeIsolation`, `TestWorkspaceRepoHasOpenConflictsExactScope`, and
`TestWorkspaceMutationTxHasOpenConflictsExactScope`. The conflict-gate tests cover open,
resolved-only, another project, another workspace in the same project, and query failure;
the transaction case proves the check observes writes in the same immediate transaction.
Reopen the database after every injected failure.
Corruption table tests inject malformed, unknown-field, trailing, non-canonical, row-ID-
mismatched, bad-state operation rows and corrupt/non-canonical candidate file lists,
wrong recorded digests, stale accepted-base digests, invalid rebased combinations, and
cross-project/repository snapshots. Every case fails closed before exposing or appending
a partial view. `TestNewOperationsPersistExactCanonicalBytes` compares fixed bytes.

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(Apply|Compose|StatusExposes|CandidateDecode|NewOperationsPersist|Workspace.*HasOpenConflicts)' -count=1
Expected: FAIL because the composer, strict repository reads, status fields, and atomic
batch transaction are absent.

- [ ] **Step 4: Implement strict composition and the exact Apply transaction**

Apply executes this transaction:

~~~text
BEGIN IMMEDIATE
binding = RequireWorkspace(project_id, workspace_id)
candidate = StrictReadCandidate(binding)
(start, initialThrough) = SelectExplicitStart(binding, candidate)
rows = ListActiveOperationsAfter(initialThrough ORDER BY generation)
operations = StrictDecodeStoredOperations(rows)
view = Compose(start, initialThrough, operations)
for operation in caller order:
  require operation.Actor.ValidateLocalAction()
  require operation.ExpectedViewDigest == current.Digest
  current = projectstate.ApplyOperation(current, operation)
  bytes = projectstate.CanonicalOperation(operation)
INSERT every workspace_overlay_operation with consecutive generations
UPDATE workspace_bindings SET status='pending'
COMMIT
~~~

OperationV1 has no issuer method; Apply requires operation.Actor.ValidateLocalAction before calling the shared reducer. Any scope, binding, candidate, stored-row, validation, canonicalization, duplicate operation ID, generation, precondition, reducer, insert, or status error rolls back. Status and Diff read binding/candidate/active rows from one SQLite snapshot and use the same strict start-selection/DecodeOperation/Compose path. The shared projectstate reducer owns exact-one variant, immutable-record replay, created-at update guards, tombstone/resurrection digests, and final validation invariants.

- [ ] **Step 5: Write RED structural diff, deterministic merge, Markdown, and no-loss tests**

Add golden tests for add/modify/tombstone/resurrect and KB record/body digests; absent
versus present JSON `null`; `~0`/`~1` RFC 6901 escaping; `""` root and `/body`; recursive
sorted objects; atomic arrays; deep-copy/alias safety; stable entity/UUID/path ordering;
and actor attribution from the last active operation affecting a key. Add fixed conflict
preimage/ID/order tests across reversed map insertion and repeated runs. Cover disjoint
typed fields, same field, Event and Git-link exact replay/divergence, tombstone record and
KB-body edits, differing tombstones, and resurrection cases. Raw disappearance of an
old Event or Git link must produce sorted `immutable_record` evidence with an absent
FieldValue; raw disappearance of a mutable record must return ErrRawRecordDeletion, and
the corresponding direct-import test must require a tombstone. Add binding/version/project/
repository mismatch and candidate Config.Handle/Remotes mutation rejection, successful
new-base handle/remotes adoption, and SemanticDiff exclusion for those Git-owned fields.
Add `updated_at` cases for one semantic editor, two clean semantic editors, no semantic
editor, and a same-field semantic conflict; add immutable `created_at` input rejection.
`TestThreeWayRebaseConflictResultIsValidatedAndLossless` must compare EncodeTree bytes and
digest with the prior candidate, mutate the returned result/evidence to prove no aliases,
and prove no partially clean changes entered the conflict result.

Markdown cases cover LF/final newline canonicalisation, deterministic equal-cost LCS
ties with repeated anchors, non-overlapping hunks, identical and unequal shared-anchor
insertions, overlapping replacements/deletions, stable IDs, and absence of conflict
markers.

Run: go test ./internal/runtime/projectstate -run 'Test(SemanticDiff|ThreeWayRebase|Markdown)' -count=1
Expected: FAIL because diff/rebase and their deterministic representation are absent.

- [ ] **Step 6: Implement the exact diff and merge algorithms**

`FieldValue{Present:false}` has nil Value. Present values contain exactly one canonical
JSON value without CanonicalJSON's trailing LF; present JSON null is exactly `null`.
Markdown field values are the canonical body represented as a JSON string. RFC 6901
uses `""` for a root, `/body` for Markdown, and `~0`/`~1` escaping. Objects recurse in
sorted-key order and arrays are atomic. `BeforeDigest`/`AfterDigest` are shared canonical
JSON digests of the complete record or tombstone; the body digest fields independently
use `DigestCanonicalMarkdown` for a live KB body. Missing surfaces have nil digests.
SemanticDiff excludes Config.Handle, Remotes, and `/updated_at`, and orders project,
actor, task, task_link, kb_article, channel, event, git_link, then UUID and field path.

Conflict IDs are `string(projectstate.DigestCanonicalJSON(conflictIDPreimageV1))` over:

~~~go
type conflictIDPreimageV1 struct {
    SchemaVersion int `json:"schema_version"`
    Key projectstate.RecordKey `json:"key"`
    FieldPath string `json:"field_path"`
    Kind ConflictKind `json:"kind"`
    Base FieldValue `json:"base"`
    Ours FieldValue `json:"ours"`
    Theirs FieldValue `json:"theirs"`
}
~~~

Conflicts sort by the canonical entity-kind order above, record ID, field path, kind,
then ID. A KB body-only tombstone collision emits `tombstone_body`, not a duplicate
record conflict. Event/Git-link disagreement emits `immutable_record`.

ThreeWayRebase validates and deep-copies all inputs, requires equal immutable snapshot
version/project/repository binding fields, and requires candidate Config.Handle/Remotes
to equal oldBase before taking newBase's values. It accepts equal and one-sided changes,
except that disappearance is never a valid one-sided change. Removing an old Event or
Git link yields `ConflictImmutableRecord` with explicit present-to-absent evidence;
removing an old mutable record without a valid tombstone returns ErrRawRecordDeletion.
It coalesces exact immutable records and recursively merges only the frozen compatible
typed fields. SemanticDiff likewise rejects a raw mutable disappearance rather than
representing path removal as deletion. For an existing mutable record, changed
`created_at` is an invalid input;
an explicit one-sided resurrection may have a fresh valid value because prior bytes are
not in the tombstone. `/updated_at` never selects semantics: take one semantic editor's
value, the later UTC value only after two-sided semantics merge cleanly, or oldBase when
neither side changed semantics.

Markdown canonicalises both inputs first and computes base-relative minimum-edit LCS
hunks without a synthetic terminal line. Equal-cost script choices advance the base/
deletion side before insertion; hunks sort by base start, base end, inserted bytes.
Non-overlapping hunks merge; identical same-anchor insertions coalesce; unequal shared-
anchor insertions and overlapping replacement/deletion hunks conflict. No conflict
marker is emitted.

If any conflict exists, MergeResult.Snapshot is a deep copy whose EncodeTree bytes and
digest are byte-identical to the complete prior composed `candidate`; clean partial
merges are discarded. Sorted conflict triples retain all new-base/direct evidence.
With no conflict, the result is fully validated, canonically encoded, and digested.

- [ ] **Step 7: Run GREEN and commit**

Run: go test ./internal/types/projectstate ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(DecodeOperation|DigestCanonical|ApplyOperation|GitLink|CreatedAt|Apply|Compose|SemanticDiff|ThreeWayRebase|Markdown|Operation)' -count=1
Expected: PASS.

~~~bash
git add internal/types/projectstate internal/runtime/projectstate internal/runtime/localstore
git commit -m "feat: compose and merge portable state"
~~~

### Task 4: Direct import, stash restore, Git-base observation, and branch guard

**Files:**
- Create: internal/runtime/projectstate/import.go
- Create: internal/runtime/projectstate/import_test.go
- Create: internal/runtime/projectstate/stash.go
- Create: internal/runtime/projectstate/stash_test.go
- Create: internal/runtime/projectstate/git_observer.go
- Create: internal/runtime/projectstate/git_observer_test.go
- Modify: internal/runtime/projectstate/service.go
- Modify: internal/runtime/projectstate/service_test.go
- Modify: internal/runtime/localstore/workspace_repo.go
- Modify: internal/runtime/localstore/workspace_repo_test.go

**Interfaces:**
- Consumes: Task 3 Compose and ThreeWayRebase.
- Produces: Import, Stash, RestoreStash, ObserveGitBase, direct-delta validation, and branch disposition.

~~~go
type ImportRequest struct {
    Scope types.WorkspaceScope
    Root string
    ExpectedWorkingTreeDigest *projectstate.Digest
    Actor types.ActorEnvelope
}
type ImportResult struct {
    PreviousCandidateDigest *projectstate.Digest
    ImportedCandidateDigest projectstate.Digest
    ComposedViewDigest projectstate.Digest
    ImportedChangeCount int
    RebasedThroughGeneration int64
    Conflicts []Conflict
}
type DirectImportJournalMatch struct {
    AcceptedBaseDigest projectstate.Digest
    Checkout types.CheckoutIdentity
    CandidateTree projectstate.Tree
}
type WorkspaceMaterializationRecord struct {
    JournalID string
    AcceptedBaseDigest projectstate.Digest
    Checkout types.CheckoutIdentity
    CandidateTree projectstate.Tree
    State string
}
func (tx *WorkspaceMutationTx) PublishedMaterializationByCandidateDigest(
    ctx context.Context,
    digest projectstate.Digest,
) (*WorkspaceMaterializationRecord, error)
func ValidateDirectDelta(prior, next projectstate.Snapshot, matchingMaterialization *DirectImportJournalMatch) error
func (s *Service) Import(ctx context.Context, req ImportRequest) (ImportResult, error)

type StashResult struct {
    StashID string
    SourceDigest projectstate.Digest
    CandidateDigest projectstate.Digest
    OperationCount int
}
type StashReplayV1 struct {
    SchemaVersion int `json:"schema_version"`
    SelectedStartTree projectstate.Tree `json:"selected_start_tree"`
    SelectedStartDigest projectstate.Digest `json:"selected_start_digest"`
    InitialThroughGeneration int64 `json:"initial_through_generation"`
    Operations []StoredOperation `json:"operations"`
}
type RestoreStashRequest struct {
    Scope types.WorkspaceScope
    StashID string
    Actor types.ActorEnvelope
}
type RestoreStashResult struct {
    RestoredDigest projectstate.Digest
    RebasedThroughGeneration int64
    Conflicts []Conflict
    StashRetained bool
}
func (s *Service) Stash(ctx context.Context, scope types.WorkspaceScope, actor types.ActorEnvelope, label string) (StashResult, error)
func (s *Service) RestoreStash(ctx context.Context, req RestoreStashRequest) (RestoreStashResult, error)

type BranchSwitchAction string
const (
    BranchSwitchReject BranchSwitchAction = ""
    BranchSwitchStash BranchSwitchAction = "stash"
    BranchSwitchDiscard BranchSwitchAction = "discard"
)
type ObserveGitBaseRequest struct {
    Scope types.WorkspaceScope
    Root string
    ExpectedCommit string
    BranchAction BranchSwitchAction
    Actor types.ActorEnvelope
}
type ObserveGitBaseResult struct {
    PreviousCommit, ObservedCommit string
    PreviousRef, ObservedRef string
    PreviousBaseDigest, ObservedBaseDigest projectstate.Digest
    CandidateAccepted bool
    RetiredJournalID *string
    Rebased bool
    Conflicts []Conflict
}
func (s *Service) ObserveGitBase(ctx context.Context, req ObserveGitBaseRequest) (ObserveGitBaseResult, error)
func (s *Service) RefreshWorkspace(ctx context.Context, binding types.WorkspaceBinding) (types.WorkspaceBinding, error)
~~~

RefreshWorkspace validates and revalidates binding checkout identity, invokes ObserveGitBase with BranchSwitchReject against the independently observed HEAD/tree, then resolves and returns the updated exact WorkspaceBinding. It is the single refresh seam consumed by Slice B. Gateway startup first calls Recover(binding.Scope), then RefreshWorkspace(binding), for every RegisteredWorkspaces result. Request orchestration calls RefreshWorkspace before every subsequent scoped status, diff, pillar/workspace write, import, checkpoint, and graph operation. The sole exception is Stash after RefreshWorkspace returns ErrBranchSwitchPending: Stash runs against the still-validated pre-refresh binding, then the caller must immediately call RefreshWorkspace(binding) and Recover(refreshed.Scope); the stash call is not reported successful unless both follow-up calls succeed. Any other branch action or invalid committed tree fails closed before the requested operation; no caller-supplied ref/tree can bypass ObserveGitBase.

Direct delta rules are exact. Relative to the previously imported candidate, or accepted base when no candidate exists, a direct tree:

- returns ErrDirectImmutableRecordMutation for any changed or removed existing Event
  or Git-link ID; an exact canonical replay is unchanged;
- returns ErrDirectPathDeletion when any prior record path disappears instead of becoming a valid TombstoneV1;
- returns ErrTombstoneDigest when DeletedContentDigest is not the digest of the prior canonical record or DeletedBodyDigest is absent/wrong for KB;
- returns ErrDirectEditTombstone when a prior tombstone is changed directly rather than
  replayed exactly; it does not inspect or classify any active overlay edit;
- returns ErrDirectResurrection when a tombstone becomes live;
- returns ErrDirectImmutableFieldMutation when an existing live mutable record changes
  `created_at`; explicit matching-materialization resurrection is exempt because a
  tombstone has no prior record bytes;
- may bypass these direct-edit errors only when its digest and bytes equal the candidate_tree of a published materialization journal bound to the same accepted base and checkout. That exception is how a typed Resurrect operation becomes importable after checkpoint; callers cannot claim the exception.

Git-link tombstones/resurrections fail strict DecodeTree before delta comparison. Snapshot
version, project ID, and repository identity must match the binding. Direct Config.Handle
and Remotes changes are Git-base input, not overlay mutations; ThreeWayRebase owns them by
requiring the composed candidate to retain the prior surface and taking the direct values.
`ValidateDirectDelta(prior, next, matchingMaterialization)` owns only the direct-versus-
prior surface and the bound materialization exception. A correct new direct tombstone is
valid at this stage. `ThreeWayRebase(oldBase, newBase, candidate)` alone compares that
tombstone with the overlay candidate and emits `ConflictTombstoneEdit` when the overlay
edited the live record; the direct validator never returns an overlay conflict.
Import returns the scope/checkout/working-tree/project/repository errors already frozen
plus these direct-delta sentinels. No error replaces workspace_candidates or conflicts.

Import executes:

~~~text
require req.Actor.ValidateLocalAction()
liveTree = ReadWorkingTree(req.Root)
liveSnapshot = projectstate.DecodeTree(liveTree)
canonicalLiveTree = projectstate.EncodeTree(liveSnapshot)
require canonicalLiveTree byte-equals liveTree
liveTreeBytes = EncodeFileList(canonicalLiveTree)
BEGIN IMMEDIATE
workspace = tx.Workspace(ctx) // localstore.WorkspaceRecord for req.Scope
require RevalidateCheckout(workspace.Binding, req.Root)
binding = workspace.Binding // types.WorkspaceBinding
acceptedSnapshot = workspace.Snapshot // projectstate.Snapshot
candidate = tx.Candidate(ctx) // *WorkspaceCandidateRecord
priorSurface = candidate.DirectSnapshot if candidate != nil else acceptedSnapshot
journalRow = tx.PublishedMaterializationByCandidateDigest(ctx, liveSnapshot.Digest)
journalMatch = nil if journalRow == nil else &DirectImportJournalMatch{
  AcceptedBaseDigest: journalRow.AcceptedBaseDigest,
  Checkout: journalRow.Checkout,
  CandidateTree: journalRow.CandidateTree,
}
require journalMatch == nil || journalMatch.AcceptedBaseDigest == projectstate.Digest(binding.AcceptedTreeDigest)
require journalMatch == nil || journalMatch.Checkout == binding.Checkout
ValidateDirectDelta(priorSurface, liveSnapshot, journalMatch)
(start, initialThroughGeneration) = SelectExplicitStart(acceptedSnapshot, candidate)
rows = tx.ActiveOperationsAfter(ctx, initialThroughGeneration) // state=active, ORDER BY generation
storedOperations = StrictDecodeStoredOperations(rows)
oldComposed = Compose(start, initialThroughGeneration, storedOperations)
merged = ThreeWayRebase(priorSurface, liveSnapshot, oldComposed.Snapshot)
rebasedTree = projectstate.EncodeTree(merged.Snapshot)
rebasedTreeBytes = EncodeFileList(rebasedTree)
DELETE FROM workspace_conflicts
  WHERE project_id=req.Scope.ProjectID AND workspace_id=req.Scope.WorkspaceID
    AND state='open'
INSERT each CanonicalJSON(merged.Conflicts FieldValue envelope) as state='open'
UPSERT workspace_candidates with every non-null column:
  project_id=req.Scope.ProjectID,
  workspace_id=req.Scope.WorkspaceID,
  accepted_base_digest=projectstate.Digest(binding.AcceptedTreeDigest),
  working_tree_digest=liveSnapshot.Digest,
  direct_tree=liveTreeBytes,
  rebased_tree=rebasedTreeBytes,
  rebased_through_generation=oldComposed.ThroughGeneration,
  imported_by=req.Actor.PrincipalID(),
  imported_at=clock.Now().UTC()
UPDATE workspace_overlay_operations SET state='rebased'
  WHERE project_id=req.Scope.ProjectID AND workspace_id=req.Scope.WorkspaceID
    AND generation <= oldComposed.ThroughGeneration AND state='active'
UPDATE workspace_bindings SET status = 'conflicted' when conflicts exist else 'pending'
  WHERE project_id=req.Scope.ProjectID AND workspace_id=req.Scope.WorkspaceID
COMMIT
~~~

The candidate upsert writes all Task-2 non-null fields explicitly; it does not rely on a
default to supply actor, time, binding, or digest provenance. Replacing conflicts means
only deleting prior `state='open'` rows for the exact `(project_id, workspace_id)` before
inserting the new sorted set in the same transaction. Resolved history and every other
workspace remain untouched. `ImportResult.PreviousCandidateDigest` is the prior composed
candidate digest when one existed, `ImportedCandidateDigest` is `liveSnapshot.Digest`,
`ComposedViewDigest` is `merged.Snapshot.Digest`, `ImportedChangeCount` is the stable
direct semantic-change count, and `RebasedThroughGeneration` is
`oldComposed.ThroughGeneration`.

On a clean merge, `merged.Snapshot` is the merged candidate. On a conflict it is the
complete byte-identical `oldComposed.Snapshot` (`ours`), while `direct_tree` retains the
complete `liveSnapshot` (`theirs`) and canonical conflict triples retain base/ours/theirs
field evidence. The direct tree, ours surface, open conflicts, absorbed row-state
transitions, rebased generation, and conflicted binding status commit atomically or not
at all. Operations through RebasedThroughGeneration remain immutable audit rows but
Compose skips them. Later operations precondition against the persisted rebased-tree
digest. A correct direct tombstone versus an active overlay edit persists
ConflictTombstoneEdit and returns a conflicted ImportResult; no silent overwrite occurs.
After restart Status and Compose must return the same candidate digest/generation and
conflict evidence. Checkpoint and writable Fabric are blocked while any conflict is open.

Stash persists complete canonical bytes, not only digests:

~~~text
BEGIN IMMEDIATE for scope
workspace = tx.Workspace(ctx)
if tx.HasOpenConflicts(ctx):
  ROLLBACK and return StashResult{}, localstore.ErrWorkspaceConflicted
candidate = tx.Candidate(ctx)
(sourceBase, sourceBaseTree) = (workspace.Snapshot, canonical accepted tree)
(selectedStart, initialThroughGeneration) = SelectExplicitStart(workspace.Snapshot, candidate)
rows = tx.ActiveOperationsAfter(ctx, initialThroughGeneration) // ORDER BY generation
storedOperations = StrictDecodeStoredOperations(rows)
composed = Compose(selectedStart, initialThroughGeneration, storedOperations)
sourceTree = EncodeFileList(sourceBaseTree)
selectedStartTree = projectstate.EncodeTree(selectedStart)
composedCanonicalTree = projectstate.EncodeTree(composed.Snapshot)
composedTree = EncodeFileList(composedCanonicalTree)
replayEnvelope = StashReplayV1{
  SchemaVersion: 1,
  SelectedStartTree: selectedStartTree,
  SelectedStartDigest: selectedStart.Digest,
  InitialThroughGeneration: initialThroughGeneration,
  Operations: storedOperations,
}
INSERT workspace_stashes(source_tree=sourceTree,
  composed_tree=composedTree,
  operations_json=projectstate.CanonicalJSON(replayEnvelope),
  through_generation=composed.ThroughGeneration,
  source_base_digest=sourceBase.Digest,
  candidate_digest=composed.Snapshot.Digest,
  actor_json=CanonicalJSON(actor), label)
DELETE workspace_candidates
UPDATE workspace_overlay_operations SET state='stashed'
  WHERE project_id=scope.ProjectID AND workspace_id=scope.WorkspaceID
    AND generation > initialThroughGeneration
    AND generation <= composed.ThroughGeneration
    AND state='active'
UPDATE workspace_bindings SET status='clean'
COMMIT
~~~

`source_tree` and `source_base_digest` are the semantic pre-stash rebase base, normally
the accepted snapshot, and are deliberately independent of replay selection. The exact
selected Compose start is embedded as `SelectedStartTree` plus
`SelectedStartDigest` in the canonical `StashReplayV1` stored in the existing
`operations_json` column. Do not add local migration `000002`; Slice B/C already owns
that number. Strict decode requires the embedded tree to pass the file-list-equivalent
path/order limits, `DecodeTree`, canonical re-encoding, digest, and binding checks.

The replay envelope stores only active `StoredOperation` rows above its explicit
boundary. Rows already `state='rebased'` at or below that boundary remain rebased because
their effect is present in `SelectedStartTree`; stash never expects, serializes, or
transitions them. An immediate post-rebase stash is valid with an empty `Operations`
array and `InitialThroughGeneration == through_generation == G`. A later stash contains
exactly `G < generation <= through_generation`. `Operations` is always a non-nil JSON
array (`[]` when empty), never `null`. Successful stash atomically deletes the candidate,
transitions only those later active rows to stashed, and sets the binding to `clean`.
Any exact-scope open conflict returns `StashResult{}` plus
`localstore.ErrWorkspaceConflicted` and changes no candidate, operation, stash, conflict,
or binding row. `StashResult.SourceDigest` is `sourceBase.Digest`, CandidateDigest is
`composed.Snapshot.Digest`, and OperationCount is exactly `len(storedOperations)`.

RestoreStash strict-decodes and digest-checks the semantic `source_tree`,
`composed_tree`, and `StashReplayV1`. It rejects unknown fields, trailing or
noncanonical JSON, a noncanonical embedded selected-start tree, selected-start digest
mismatch, a negative boundary, and unordered or duplicate rows. The final row generation
must equal `stash.through_generation`; an empty list instead requires the initial
boundary to equal it. Every envelope row must byte-for-byte match the exact persisted
`state='stashed'` row for this workspace, generation, operation ID, and canonical
operation JSON. Rows at or below the boundary are not queried or expected. Restore calls
`Compose(selectedStart, replay.InitialThroughGeneration, replay.Operations)` and requires
the result's encoded tree bytes, digest, and ThroughGeneration to equal `composed_tree`,
`candidate_digest`, and `stash.through_generation` before considering current state.

Restore then strict-selects and composes the current start/boundary/later active rows and
calls `ThreeWayRebase(sourceBase, current.Snapshot, stashComposed.Snapshot)`. On a clean
merge only, one immediate transaction preserves the current direct surface, persists the
merged rebased candidate with the greatest absorbed generation, transitions exactly the
stash rows and current active rows absorbed by that candidate to `state='rebased'`,
replaces exact-workspace open conflict evidence with the empty set, sets binding status
to `pending`, and deletes the stash. It never reinserts an operation UUID or rewrites a
pre-boundary rebased row.

On conflict, candidate bytes/columns and every operation row remain byte-identical. The
transaction writes only the deterministic sorted open conflict evidence for
`ThreeWayRebase(sourceBase, current.Snapshot, stashComposed.Snapshot)`, sets binding
status to `conflicted`, and retains the complete stash unchanged. The returned
`RestoredDigest` and `RebasedThroughGeneration` describe the unchanged current composed
view and `StashRetained` is true; the stash-composed surface remains in the retained stash
as resolution/audit evidence, not as a silently installed candidate.

An exact repeated conflicted RestoreStash strict-decodes and composes the stash replay
and the current persisted rows again, recomputes the same three-way rebase, and requires
the unchanged candidate, every operation row, binding state, retained stash bytes, and
sorted open conflicts to match the prior outcome. It returns the same result read-only.
Changed or corrupt current rows (including a row the failed merge would have absorbed),
changed candidate/stash/conflict evidence, resolved conflicts, or a non-conflicted
binding fail closed without mutation. `ErrStashCorrupt` covers tree/envelope/replay/digest
corruption; `ErrStashOperationMismatch` covers missing, altered, extra, noncanonical, or
wrongly stated persisted stash-operation rows.

Git observation remains independent:

1. Validate binding/root/actor with ActorEnvelope.ValidateLocalAction.
2. Run read-only git with GIT_OPTIONAL_LOCKS=0 and hooks disabled: rev-parse HEAD^{commit}, symbolic-ref -q HEAD, ls-tree -rz --full-tree HEAD for canonical .wormhole paths, then cat-file --batch.
3. Require HEAD equals ExpectedCommit; DecodeTree; validate project/repository.
4. Re-read HEAD and checkout identity. Race returns ErrGitObservationChanged without DB writes.
5. BEGIN IMMEDIATE and CAS accepted_commit. Ref change with pending state and no matching materialized candidate returns ErrBranchSwitchPending unless explicit stash/discard.
6. Matching published materialization advances base only when its accepted_base_digest and checkout path/device/inode still equal the binding, marks journal accepted, and preserves operations newer than its through_generation. A bound-precondition mismatch returns ErrGitMaterializationPrecondition and retains the journal.
7. Nonmatching same-ref base uses ThreeWayRebase. It enforces immutable version/project/
   repository binding fields, requires the candidate to retain old-base Handle/Remotes,
   takes new-base Handle/Remotes, and persists the complete ours/theirs conflict surfaces
   and row transitions atomically. Candidate mismatch never retires a journal.

- [ ] **Step 1: Write RED direct-delta and semantic overlay-import tests**

~~~go
func TestImportRejectsDirectResurrection(t *testing.T) {
    svc, req := tombstonedWorkspace(t)
    writeLiveReplacement(t, req.Root)
    _, err := svc.Import(t.Context(), req)
    if !errors.Is(err, ErrDirectResurrection) { t.Fatal(err) }
}
func TestImportRebasesOverlayOntoDirectCandidate(t *testing.T) {
    svc, req := workspaceWithOverlayTitleEdit(t)
    writeDirectPriorityEdit(t, req.Root)
    got, err := svc.Import(t.Context(), req)
    if err != nil || len(got.Conflicts) != 0 { t.Fatalf("%+v %v", got, err) }
    view := mustComposedView(t, svc, req.Scope)
    assertTaskTitleAndPriority(t, view, "overlay title", 9)
    assertOnlyOperationsAfter(t, svc, req.Scope, got.RebasedThroughGeneration)
}
~~~

Add named cases TestImportRejectsUnequalImmutableEventAndGitLink,
TestImportRejectsImmutableRecordDeletion,
TestImportRejectsPathDeletionWithoutTombstone,
TestImportRejectsIncorrectTombstoneContentDigest,
TestImportRejectsIncorrectTombstoneBodyDigest,
TestImportRejectsChangedCreatedAt,
TestValidateDirectDeltaAcceptsCorrectTombstoneWithoutOverlay,
TestImportPersistsOverlayTombstoneConflict,
TestImportRejectsDirectResurrection, and
TestImportAcceptsMatchingMaterializedResurrection. Add
`TestImportPersistsOverlayTombstoneConflict` must first prove direct/prior validation
succeeds, then prove only ThreeWayRebase emits `ConflictTombstoneEdit`; no direct-delta
sentinel may substitute for the persisted merge evidence. Add
TestImportConflictPersistsOursTheirsAtomicallyAcrossRestart: inject a failure at each
candidate/conflict/row-state/status write, prove the prior state remains byte-identical,
then prove a successful conflict retains the old complete composed candidate, new direct
tree, canonical FieldValue triples, absorbed generation, and blocked status after reopen.

Run: go test ./internal/runtime/projectstate -run 'Test(Import|ValidateDirectDelta)' -count=1
Expected: FAIL because Import is absent.

- [ ] **Step 2: Write RED complete-stash and restore tests**

Test restart byte equality for source_tree, composed_tree, and the canonical versioned
operations_json envelope, including the semantic source-base/selected-start split; clean
restore onto a changed base; conflicting restore retains the complete stash/evidence
while preserving candidate and every operation row; and restored operations do not fail
stale whole-view preconditions. Add
`TestStashAfterRebaseWithNoLaterOperationsPersistsBoundaryAcrossRestart`, proving an empty
row list, `initial_through_generation == through_generation == G`, a selected-start tree
that contains the absorbed prefix, a distinct accepted `source_tree`, and unchanged
rebased rows at/below G. Add
`TestStashAfterRebasePersistsOnlyLaterActiveOperationsAcrossRestart`, proving only
generations greater than G enter the envelope/transition to stashed and both the absorbed
prefix and later operations survive a clean restore. Add
`TestRestoreStashConflictPreservesAbsorbedPrefixAndLaterOperationsAcrossRestart`, proving
the same two layers remain in the retained stash while current candidate/rows stay
byte-identical. Reject unknown envelope versions/fields, trailing/noncanonical bytes,
selected-start or source-base digest mismatch, boundary/through mismatch, and missing,
extra, altered, or wrongly stated persisted rows without mutation.

Add `TestStashRejectsOpenConflictWithoutMutation`, requiring `StashResult{}`,
`errors.Is(err, localstore.ErrWorkspaceConflicted)`, and byte-identical candidate,
operation, stash, conflict, and binding rows after reopen. Assert successful stash status
is exactly `clean`, clean restore status is exactly `pending`, and conflicted restore
status is exactly `conflicted`. `TestRestoreStashConflictedRepeatIsIdempotent` makes the
first restore persist only exact conflict evidence/status while leaving candidate and all
operation rows unchanged; an exact repeat after restart strict-composes both replay and
current rows, returns the same result with zero writes, and rejects changed candidate,
stash, or conflict evidence. `TestRestoreStashConflictedRepeatRejectsCorruptAbsorbedCurrentRow`
corrupts a current active row the merge would have absorbed and proves repeat fails closed
instead of serving cached evidence. Add
`TestStashBranchThenRefreshWorkspaceEndToEnd`, which exercises the branch-pending stash
path, observes exact `clean` immediately after stash, then requires RefreshWorkspace and
Recover to succeed on the destination branch without resurrecting the stashed overlay.

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(Stash|RestoreStash)' -count=1
Expected: FAIL because complete stash persistence/restore are absent.

- [ ] **Step 3: Write RED Git observation and branch tests**

Cover HEAD race, invalid committed tree, immutable version/project/repository mismatch,
old-candidate Handle/Remotes mutation, valid new-base Handle/Remotes adoption,
materialized candidate match/mismatch, base advance atomicity, same-ref semantic rebase,
conflict no-loss restart, branch reject/stash/discard, and two-workspace isolation.

Run: go test ./internal/runtime/projectstate -run 'TestObserveGitBase|TestBranchSwitch|TestRefreshWorkspace' -count=1
Expected: FAIL because observer is absent.

- [ ] **Step 4: Implement the exact transactions and algorithms above**

Every filesystem read precedes BEGIN IMMEDIATE; every transaction revalidates binding
state/digests before mutation and reuses Task 3's caller-owned transaction helpers.
Import, Stash, RestoreStash, and any actor-attributed branch action call
ValidateLocalAction before mutation, so legacy/unknown can never create new state.
Every stored operation uses DecodeOperation; every stored tree uses strict canonical
decode. Encoding, merge, conflict serialization, row transition, or status failure rolls
back. Stash rejects an existing open conflict without mutation. A newly conflicted
restore persists only exact conflict evidence plus `conflicted` status and leaves its
candidate, every operation row, and retained stash unchanged; only a clean restore may
transition rows, persist a candidate, delete the stash, and set `pending`. Import never
deletes conflict evidence outside its exact atomic replacement contract. ObserveGitBase
never accepts a caller-provided tree/ref. TestRefreshWorkspaceCallsObserveGitBase,
TestRefreshBeforeStatus, TestRefreshBeforeWrite, TestRefreshBeforeCheckpoint,
TestStartupRecoversThenRefreshesEveryRegisteredWorkspace, and
TestStashAfterBranchPendingRefreshesThenRecovers freeze the orchestration seam; the last
five are completed by Slice B when it wires startup and requests.

- [ ] **Step 5: Run GREEN and commit**

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(Import|ValidateDirectDelta|Stash|RestoreStash|ObserveGitBase|BranchSwitch)' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/projectstate internal/runtime/localstore
git commit -m "feat: import and observe portable workspace state"
~~~

### Task 5: Checkpoint CAS, Linux exchange, durable fallback, and recovery

**Files:**
- Create: internal/runtime/projectstate/checkpoint.go
- Create: internal/runtime/projectstate/checkpoint_linux.go
- Create: internal/runtime/projectstate/checkpoint_darwin.go
- Create: internal/runtime/projectstate/checkpoint_fallback.go
- Create: internal/runtime/projectstate/checkpoint_test.go
- Modify: internal/runtime/projectstate/service.go
- Modify: internal/runtime/localstore/workspace_repo.go
- Modify: internal/runtime/localstore/workspace_repo_test.go

**Interfaces:**
- Consumes: Task 3 Compose and Task 4 conflicts.
- Produces: Checkpoint and Recover.

~~~go
type CheckpointRequest struct {
    Scope types.WorkspaceScope
    Root string
    ExpectedWorkingTreeDigest projectstate.Digest
    Actor types.ActorEnvelope
}
type CheckpointResult struct {
    CandidateDigest projectstate.Digest
    MaterializedThroughGeneration int64
    JournalID string
    BaseAdvanced bool
}
func (s *Service) Checkpoint(ctx context.Context, req CheckpointRequest) (CheckpointResult, error)
func (s *Service) Recover(ctx context.Context, scope types.WorkspaceScope) (WorkspaceStatus, error)
~~~

Checkpoint returns `localstore.ErrWorkspaceConflicted` directly. Runtime/projectstate
declares no alias or alternate sentinel.

Checkpoint algorithm:

1. Acquire the per-workspace in-process mutex and begin the first `BEGIN IMMEDIATE`
   journal-preparation transaction.
2. Validate scope/root/actor; read allowed live tree; require digest equals ExpectedWorkingTreeDigest.
3. Strict-decode/import the valid direct tree, select the explicit Compose start and
   initial generation, DecodeOperation every later active row, compose, and reject any
   open conflict before staging by calling `tx.HasOpenConflicts(ctx)` inside the same
   immediate transaction. Return exactly `CheckpointResult{}` and
   `localstore.ErrWorkspaceConflicted` on rejection. Checkpoint never changes its return type to
   WorkspaceStatus; a separate `Status(ctx, req.Scope)` call proves the unchanged exact
   CandidateDigest/OverlayGeneration. Otherwise EncodeTree the complete candidate and
   validate a DecodeTree round trip.
4. Create owner-only sibling stage and backup paths on the same filesystem; write every file with fsync, fsync every created directory, and fsync parent.
5. Re-read live tree and compare digest. Mismatch returns ErrCheckpointCAS, preserves direct input/stage evidence, and performs no publication.
6. Persist the prepared journal containing complete canonical prior/candidate trees,
   accepted_base_digest, bound checkout_path/device/inode, exact candidate digest, and
   through-generation; commit the first transaction before filesystem publication on the
   Task-2 WAL connection whose mandatory `synchronous=FULL` policy durably syncs the
   journal/WAL. Do not hand-fsync the main database file.
7. After that commit and before any live-tree rename/exchange, acquire a second dedicated
   connection and `BEGIN IMMEDIATE`. Reload the prepared journal and exact workspace,
   then recheck the live working-tree digest, binding accepted digest and checkout
   identity, selected candidate bytes/digest, composed through-generation, complete
   included operation IDs/bytes/states, absence of unexpected later generations, and
   `tx.HasOpenConflicts(ctx)`. Any mismatch rolls back with no publication; an open
   conflict returns `CheckpointResult{}` plus `localstore.ErrWorkspaceConflicted`.
8. Hold that second immediate transaction across filesystem publication and the complete
   database finalization. Linux exchanges live `.wormhole` and stage with `renameat2`
   `RENAME_EXCHANGE`, renames the old tree to backup, and fsyncs the parent. Darwin uses
   `renameatx_np` `RENAME_SWAP` when supported. Fallback renames live to backup, fsyncs
   parent, renames stage to live, and fsyncs parent. Before committing, mark the journal
   published, persist `candidate_tree` as the workspace candidate, transition exactly
   the included operations to `materialized`, and leave verified later operations active.
   A failure rolls back database finalization while the durable prepared row remains the
   recovery authority.
9. Recover handles a prepared or published journal with either the old or new live tree.
   Before examining or renaming a path, it requires the current binding accepted digest
   and checkout identity to match the journal. With matching preconditions it compares
   live/stage/backup against both recorded digests and deterministically restores the old
   complete tree or finishes the new complete tree and matching database state. Unknown
   digest returns ErrCheckpointRecoveryBlocked without deleting evidence; a binding
   mismatch returns ErrCheckpointRecoveryPrecondition and leaves all evidence untouched.
10. ErrCheckpointUnsupported occurs before step 4 only when regular files, directory
    fsync, same-filesystem rename, or owner-only staging cannot be guaranteed. Accepted
    base stays unchanged until Task 4 observes the matching Git commit.

The open-conflict predicate is exact `(project_id, workspace_id, state='open')`.
Resolved conflicts and an open conflict in any other project/workspace do not block this
checkpoint. The first check and journal preparation share one `BEGIN IMMEDIATE`. The
mandatory second `BEGIN IMMEDIATE` closes the post-commit race: it rechecks the exact
publication preconditions and conflict gate, then excludes every Gateway writer until
filesystem publication plus journal/candidate/operation finalization commit together.

- [ ] **Step 1: Write RED fault-injection matrix**

Create table tests injecting failure after stage fsync, prepared-journal commit, second
immediate begin, live-to-backup rename, stage-to-live rename, directory fsync, exchange,
published-row update, candidate update, and operation transition. Each restart Recover
must accept either recorded old or new live state, converge to one byte-exact complete
tree/database outcome, preserve later overlay generations, and retain evidence on unknown
digest. TestCheckpointRecoverRejectsChangedCheckout replaces the bound directory inode;
TestCheckpointRecoverRejectsChangedAcceptedBase advances the binding base. Both assert
ErrCheckpointRecoveryPrecondition and byte-identical journal/stage/backup evidence.
`TestCheckpointRejectsOpenConflictPreservesEvidence` creates the Task-4 conflict surface,
asserts `CheckpointResult{}` and
`errors.Is(err, localstore.ErrWorkspaceConflicted)` before stage/journal/publication
mutation, compares complete direct/ours/conflict/row bytes across reopen, then calls
Status separately and proves the same CandidateDigest and OverlayGeneration. Add
exact-scope subtests proving resolved-only and another project/workspace do not block.

Add `TestCheckpointConflictAfterPreparedCommitPublishesNothing`: a deterministic hook
runs after the prepared journal commits but before the publication connection begins,
opens an exact-workspace conflict through the normal immediate mutation seam, and resumes
checkpoint. The second immediate recheck must return `CheckpointResult{}` plus
`localstore.ErrWorkspaceConflicted`; live-tree bytes/digest, candidate, operation states,
and publication state remain unchanged, and rename/exchange/fsync publisher counters are
zero. Reopen and Recover must recognize the old live tree and prepared evidence without
publishing the staged candidate. The downstream multi-Fabric Task 2/6 tests consume the
same gate and sentinel.

Run: go test ./internal/runtime/projectstate -run 'TestCheckpoint(CAS|LinuxExchange|Fallback|Recover|RecoverRejectsChangedCheckout|RecoverRejectsChangedAcceptedBase|DoesNotAdvanceBase|PreservesLaterOverlay|RejectsOpenConflictPreservesEvidence|ConflictAfterPreparedCommitPublishesNothing)' -count=1
Expected: FAIL because checkpoint implementation is absent.

- [ ] **Step 2: Implement platform publishers and journal state machine**

Reuse the descriptor-relative/no-follow discipline from localapi materialization but
define projectstate-local primitives for a complete `.wormhole` directory. Do not import
localapi. Use exact prepared/published/accepted/recovered_old/recovered_new states from
Task 2 DDL. Open conflicts are a hard pre-publication gate shared with later writable
Fabric. Implement both immediate transactions above; the second begins only after the
prepared commit and remains open through publication and the complete final database
update so a conflict or overlay mutation cannot race the rename boundary.

- [ ] **Step 3: Run GREEN and commit**

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'TestCheckpoint' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/projectstate internal/runtime/localstore
git commit -m "feat: checkpoint portable state durably"
~~~

### Task 6: Legacy private-state migration without Git-index mutation

**Files:**
- Create: internal/runtime/projectstate/legacy_integration.go
- Create: internal/runtime/projectstate/legacy_integration_test.go
- Modify: internal/runtime/localapi/materialize.go
- Modify: internal/runtime/localapi/manifest.go
- Modify: internal/runtime/localapi/materialize_test.go
- Modify: internal/runtime/localapi/manifest_test.go
- Modify: internal/runtime/projectstate/checkpoint.go
- Modify: internal/runtime/projectstate/checkpoint_test.go
- Modify: internal/runtime/projectstate/service.go
- Modify: internal/runtime/projectstate/service_test.go
- Modify: internal/runtime/localstore/workspace_repo.go
- Modify: internal/runtime/localstore/workspace_repo_test.go
- Modify: .gitignore

**Interfaces:**
- Consumes: Task 2 migration audit and existing private integration_manifest tables.
- Produces: MigrateLegacyIntegrationState, LegacyImportedMovePending, ErrLegacyStateRetained, and ErrTrackedLegacyState.

~~~go
type LegacyMigrationOutcome string
const (
    LegacyImportedMovePending LegacyMigrationOutcome = "imported_move_pending"
    LegacyMigratedAndMoved LegacyMigrationOutcome = "migrated_and_moved"
    LegacyMigratedTrackedSourceRetained LegacyMigrationOutcome = "migrated_tracked_source_retained"
    LegacyIgnoredUnsafe LegacyMigrationOutcome = "ignored_unsafe"
)
type LegacyMigrationResult struct {
    SourceDigest projectstate.Digest
    Outcome LegacyMigrationOutcome
    BackupPath string
}
func (s *Service) MigrateLegacyIntegrationState(ctx context.Context, scope types.WorkspaceScope, root string) (LegacyMigrationResult, error)
~~~

The scoped localstore seam is exact:

~~~go
type LegacyIntegrationMigrationRow struct {
    ProjectID string
    WorkspaceID types.WorkspaceID
    SourceDigest string
    Outcome string
    BackupPath string
    Detail string
    MigratedAt time.Time
    UpdatedAt time.Time
}
type LegacyIntegrationPreparation struct {
    StateJSON []byte
    RepositoryRoot string
    Row LegacyIntegrationMigrationRow
}
func (r *WorkspaceRepo) PrepareLegacyIntegrationMigration(context.Context, types.WorkspaceScope, LegacyIntegrationPreparation) (LegacyIntegrationMigrationRow, error)
func (r *WorkspaceRepo) PendingLegacyIntegrationMigration(context.Context, types.WorkspaceScope) (LegacyIntegrationMigrationRow, error)
func (r *WorkspaceRepo) LatestLegacyIntegrationMigration(context.Context, types.WorkspaceScope) (LegacyIntegrationMigrationRow, error)
func (r *WorkspaceRepo) TransitionLegacyIntegrationMigration(context.Context, types.WorkspaceScope, sourceDigest, fromOutcome, toOutcome, detail string) (LegacyIntegrationMigrationRow, error)
~~~

Every method verifies the exact binding and scopes every query by project and workspace. Prepare owns one immediate transaction: safe tracked/untracked preparations require non-empty validated compatible StateJSON and atomically upsert `integration_manifest_project_state` plus the migration row; `ignored_unsafe` records no state JSON. Transition is a compare-and-swap on source digest and prior outcome. Pending lookup uses the unique partial index. Latest terminal lookup orders `updated_at DESC, source_digest DESC`, so historical rows are never selected nondeterministically.

`ErrLegacyStateRetained` is the general checkpoint blocker for any exact legacy file still present. `ErrTrackedLegacyState` wraps it for the tracked-source result, so `errors.Is` matches both. The owner-only XDG backup root is an injected trusted Service dependency; no public request supplies or overrides it.

Exact behavior:

- Read exact .wormhole/integration-state.json with descriptor-relative no-follow, single-link regular-file checks; reject credential-shaped keys and project mismatch.
- Query tracked status read-only with git ls-files --error-unmatch -- .wormhole/integration-state.json using GIT_OPTIONAL_LOCKS=0. Never run git add, rm, update-index, checkout, commit, or clean.
- A safe tracked source atomically writes the compatible private integration state and `migrated_tracked_source_retained` row before returning ErrTrackedLegacyState. A restart test reopens the Store and proves both records committed together while source bytes and Git index remain unchanged.
- For an untracked safe file, first persist the compatible private integration state plus an `imported_move_pending` migration row in one WAL/FULL transaction. The row contains the source digest and deterministic owner-only XDG workspace `backup_path`; `detail` is a non-sensitive reason code. Commit before copying, renaming, or unlinking source bytes.
- Complete the pending move by copying through an owner-only temporary backup file, fsyncing the file, no-replace renaming it to the deterministic backup path, and fsyncing the backup directory. A pre-existing exact-digest backup is reusable; another type or digest blocks recovery. Revalidate the held source identity/digest and read-only tracked status. If it became tracked, retain it and transition to `migrated_tracked_source_retained`. Otherwise descriptor-relatively unlink only that exact source, fsync its parent, then CAS-update the pending row to `migrated_and_moved` in a second transaction. Cross-filesystem rename is never assumed.
- Reconcile an existing pending row before requiring the source to exist: source exact/backup absent creates the backup; source exact/backup exact unlinks and finalizes; source absent/backup exact only finalizes. Unexpected type, identity, bytes, digest, or both paths absent fails closed and retains the row/evidence. An ambiguous final database commit is read back on a fresh connection. Failure after the first commit returns `LegacyImportedMovePending`, its BackupPath, and the error; no premature final outcome is exposed.
- A final row with source absent returns idempotently. A tracked-retained row may return to pending only after normal Git removes the path from the index. If identical source bytes reappear after completion, transition the row back to pending and retire them again rather than silently succeeding.
- If tracked, leave bytes and index untouched, record migrated_tracked_source_retained, and return ErrTrackedLegacyState from MigrateLegacyIntegrationState until the human removes the tracked path through normal Git workflow. Canonical snapshot digest excludes this exact legacy path, but checkpoint never silently deletes or carries it into a staged canonical tree.
- Unsafe/malformed input remains untouched, records ignored_unsafe without sensitive detail, and returns a path-specific error.
- When the source path is absent, first reconcile the unique pending row. If none exists, read the latest terminal row ordered by `updated_at DESC, source_digest DESC`; a final row returns its stored result idempotently, while no row and no source returns a zero LegacyMigrationResult and nil. This lookup order is mandatory even when multiple historical source digests exist.
- Checkpoint checks exact-path presence during initial validation and its final CAS recheck. Any retained unsafe or move-pending legacy file returns ErrLegacyStateRetained before directory publication. When the scoped latest/pending ledger identifies a tracked-retained source, Checkpoint returns ErrTrackedLegacyState, whose definition wraps ErrLegacyStateRetained so `errors.Is` matches both.
- Remove the current materializer write/rollback of the legacy file while retaining its returned IntegrationState and private SQLite journals.

- [ ] **Step 1: Write RED tracked/untracked/unsafe migration tests**

~~~go
func TestTrackedLegacyMigrationDoesNotChangeIndex(t *testing.T) {
    repo := trackedLegacyRepo(t)
    before := gitIndexDigest(t, repo)
    got, err := svc.MigrateLegacyIntegrationState(t.Context(), scope, repo)
    if !errors.Is(err, ErrTrackedLegacyState) || got.Outcome != LegacyMigratedTrackedSourceRetained { t.Fatalf("%+v %v", got, err) }
    if after := gitIndexDigest(t, repo); after != before { t.Fatalf("%s != %s", after, before) }
    if _, err := os.Stat(filepath.Join(repo, ".wormhole", "integration-state.json")); err != nil { t.Fatal(err) }
}
~~~

Add fault-injection cases after the pending database commit, backup-file fsync, backup-directory fsync, source unlink, source-directory fsync, and final database update/ambiguous commit. Every restart/rerun imports once, converges to `migrated_and_moved`, preserves divergent evidence, and never reports final early. Add checkpoint cases for tracked, unsafe, and move-pending retained sources.

Add focused `WorkspaceRepo` cases for prepare atomic commit and rollback across reopen, cross-project/workspace rejection, unique pending lookup, deterministic latest-terminal ordering, and transition compare-and-swap failure/success.

Run: go test ./internal/runtime/projectstate ./internal/runtime/localapi ./internal/runtime/localstore -run 'Test(TrackedLegacy|UntrackedLegacy|UnsafeLegacy|LegacyIntegrationMigrationRepo|LegacyMoveRecovery|CheckpointRejectsRetainedLegacy|IntegrationMaterializationDoesNotWriteLegacyState)' -count=1
Expected: FAIL because migration and writer removal are absent.

- [ ] **Step 2: Implement exact migration and writer removal**

Add only .wormhole/integration-state.json to .gitignore. Do not introduce .wormhole/local. Preserve existing private integration tables and current compatible IDs. Implement the exact pending/copy/fsync/unlink/finalize recovery state machine above; never claim `migrated_and_moved` until its final CAS transition commits.

- [ ] **Step 3: Run GREEN and commit**

Run: go test ./internal/runtime/projectstate ./internal/runtime/localapi ./internal/runtime/localstore -run 'Test(TrackedLegacy|UntrackedLegacy|UnsafeLegacy|LegacyIntegrationMigrationRepo|LegacyMoveRecovery|CheckpointRejectsRetainedLegacy|Integration)' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/projectstate internal/runtime/localapi internal/runtime/localstore .gitignore
git commit -m "fix: retire tracked integration state projection"
~~~

### Task 7: Snapshot-backed pillar projection and bound local domain adapters

**Files:**
- Create: internal/runtime/projectstate/projection.go
- Create: internal/runtime/projectstate/projection_test.go
- Create: internal/runtime/localapi/workspace.go
- Create: internal/runtime/localapi/workspace_test.go
- Modify: internal/runtime/localapi/localapi.go
- Modify: internal/runtime/localapi/localapi_qa_test.go
- Modify: internal/runtime/localapi/localapi_p3_test.go
- Modify: internal/runtime/localapi/alpha_acceptance_gap_test.go

**Interfaces:**
- Consumes: Compose and atomic ApplyBatch.
- Produces: one rebuildable workspace projection and one binding/actor-explicit domain used by every current local task/KB/channel/event/Git MCP handler.

~~~go
type Projection struct {
    Snapshot projectstate.Snapshot
    ThroughGeneration int64
}
func (s *Service) Projection(ctx context.Context, scope types.WorkspaceScope) (Projection, error)

type WorkspaceDomain struct { service *runtimeprojectstate.Service }
func NewWorkspaceDomain(service *runtimeprojectstate.Service) (*WorkspaceDomain, error)
func (d *WorkspaceDomain) ListTasks(context.Context, types.WorkspaceBinding, listTasksArgs) (localTaskListResult, error)
func (d *WorkspaceDomain) GetTask(context.Context, types.WorkspaceBinding, getTaskArgs) (localTaskResult, error)
func (d *WorkspaceDomain) CreateTask(context.Context, types.WorkspaceBinding, types.ActorEnvelope, createTaskArgs) (localTaskWriteResult, error)
func (d *WorkspaceDomain) UpdateTaskStatus(context.Context, types.WorkspaceBinding, types.ActorEnvelope, taskUpdateStatusArgs) (localTaskStatusResult, error)
func (d *WorkspaceDomain) RouteTask(context.Context, types.WorkspaceBinding, types.ActorEnvelope, taskRouteArgs, string, string) (localTaskRouteResult, error)
func (d *WorkspaceDomain) ListChannels(context.Context, types.WorkspaceBinding, channelListArgs) (localChannelListResult, error)
func (d *WorkspaceDomain) CreateChannel(context.Context, types.WorkspaceBinding, types.ActorEnvelope, channelCreateArgs) (localChannelWriteResult, error)
func (d *WorkspaceDomain) ListChannelEvents(context.Context, types.WorkspaceBinding, channelEventsArgs) (localEventListResult, error)
func (d *WorkspaceDomain) PostChannelEvent(context.Context, types.WorkspaceBinding, types.ActorEnvelope, channelPostArgs) (localEventWriteResult, error)
func (d *WorkspaceDomain) ListArticles(context.Context, types.WorkspaceBinding, kbListArgs) (localArticleListResult, error)
func (d *WorkspaceDomain) GetArticle(context.Context, types.WorkspaceBinding, kbGetArgs) (localArticleResult, error)
func (d *WorkspaceDomain) WriteArticle(context.Context, types.WorkspaceBinding, types.ActorEnvelope, kbWriteArgs) (localArticleWriteResult, error)
func (d *WorkspaceDomain) LinkCommit(context.Context, types.WorkspaceBinding, types.ActorEnvelope, gitLinkCommitArgs) (localGitLinkResult, error)
~~~

~~~go
type localTaskStatusResult struct {
    TaskID string `json:"task_id"`
    Status string `json:"status"`
    EventID string `json:"event_id"`
}
~~~

Projection calls Compose and never reads the legacy tasks, kb_articles, channels, durable_events, git_links, or replica repository APIs. It is not separately persisted, so it is rebuildable exactly from accepted_snapshot, candidate, and operations after restart and cannot drift transactionally. Reads return sorted live records from the composed snapshot; tombstones are omitted. Every mutation validates binding and actor.ValidateLocalAction, builds only canonical projectstate.OperationV1 values, and persists through Apply/ApplyBatch. UUIDv4 IDs and UTC timestamps are generated inside the domain. UpdateTaskStatus generates one EventID, builds an updated TaskV1 operation followed by an immutable EventV1 operation whose ExpectedViewDigest is the digest after the task operation, calls Service.ApplyBatch once, and returns that same EventID in localTaskStatusResult; neither record may persist alone. Thus the seven public mutating pillar tools produce eight durable operations. Route writes the final assigned TaskV1 after scheduler selection; post publishes ephemeral notification only after the event operation is durable. Supplied agent_id fields never override the resolved actor. Existing legacy tables/repositories are migration/read-only and no handler may dual-write or fall back to them.

The conversion inventory is exact: wormhole.task.list/get/create/update_status/route; wormhole.channel.list/create/events/post; wormhole.kb.list/get/write; and wormhole.git.link_commit. internal/runtime/localapi/localapi.go delegates those handler bodies to WorkspaceDomain and removes direct TaskRepo, KBRepo, EventRepo, GitRepo, and sync-queue writes. wormhole.channel.subscribe remains ephemeral delivery sourced from post-commit eventbus publication, and wormhole.kb.search remains an authenticated Fabric proxy; neither is a local durable read/write bypass.

- [ ] **Step 1: Write RED projection, restart, mutation-visibility, and isolation tests**

For each mutation tool above, use canonical UUID literals and assert: its typed OperationV1 is present after restart; Diff reports the mutation; Checkpoint output contains it; the same read tool returns it from Projection; another canonical project/workspace UUID cannot see it; and panic-on-use legacy repositories remain untouched. TestTaskUpdateStatusAppendsTaskAndEventAtomically asserts ApplyBatch receives exactly two chained operations, the EventV1.ID equals the returned localTaskStatusResult.EventID, and injected failure before either batch insert leaves both absent. Also add TestProjectionRebuildsAfterRestart, TestEveryLocalPillarHandlerUsesWorkspaceDomain, and TestNoLegacyReplicaWriteBypass.

Run: go test ./internal/runtime/projectstate ./internal/runtime/localapi -run 'Test(Projection|EveryLocalPillar|NoLegacyReplica|TaskUpdateStatus|Task|Channel|KB|Git).*Workspace' -count=1
Expected: FAIL because Projection and bound adapters are absent.

- [ ] **Step 2: Implement projection and every exact mapping**

Map create/update records to OperationPutRecord, KB writes to OperationPutKBArticle, deletes when later exposed to OperationTombstone, and explicit restoration when later exposed to OperationResurrect. Generate canonical UUIDs with crypto/rand; tests use fixed literals such as 00000000-0000-4000-8000-000000000001, 11111111-1111-4111-8111-111111111111, 22222222-2222-4222-8222-222222222222, 44444444-4444-4444-8444-444444444444, 55555555-5555-4555-8555-555555555555, 66666666-6666-4666-8666-666666666666, and 77777777-7777-4777-8777-777777777777; no executable test uses labels as IDs.

- [ ] **Step 3: Run GREEN and commit**

Run: go test ./internal/runtime/projectstate ./internal/runtime/localapi -run 'Test(Projection|EveryLocalPillar|NoLegacyReplica|TaskUpdateStatus|Task|Channel|KB|Git).*Workspace' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/projectstate/projection.go internal/runtime/projectstate/projection_test.go internal/runtime/localapi/workspace.go internal/runtime/localapi/workspace_test.go internal/runtime/localapi/localapi.go internal/runtime/localapi/localapi_qa_test.go internal/runtime/localapi/localapi_p3_test.go internal/runtime/localapi/alpha_acceptance_gap_test.go
git commit -m "feat: project local tools from portable state"
~~~

### Task 8: Workspace operations, top-level CLI parser, and public seam

**Files owned by this task:**
- Modify: internal/runtime/localapi/workspace.go
- Modify: internal/runtime/localapi/workspace_test.go
- Create: cmd/wormhole/workspace.go
- Create: cmd/wormhole/workspace_test.go

**Files modified and staged later by the coordinated runtime/setup task:**
- internal/runtime/localapi/workspace.go, internal/runtime/localapi/mcp.go, internal/runtime/localapi/localapi.go, internal/runtime/localapi/contract_manifest_test.go, cmd/wormhole/workspace.go, cmd/wormhole/main.go, cmd/wormhole/contract_manifest_test.go, and the private bridge binding resolver/envelope.
- docs/contracts/alpha-contract.json, docs/contracts/README.md, docs/compatibility.md, and README.md in the same commit that makes the routes live.

**Interfaces:**

~~~go
type WorkspaceStatusArgs struct{}
type WorkspaceDiffArgs struct{}
type WorkspaceImportArgs struct { ExpectedWorkingTreeDigest *projectstate.Digest `json:"expected_working_tree_digest,omitempty"` }
type WorkspaceCheckpointArgs struct { ExpectedWorkingTreeDigest projectstate.Digest `json:"expected_working_tree_digest"` }
type WorkspaceStashArgs struct { Label string `json:"label"` }
func (d *WorkspaceDomain) Status(context.Context, types.WorkspaceBinding, WorkspaceStatusArgs) (runtimeprojectstate.WorkspaceStatus, error)
func (d *WorkspaceDomain) Diff(context.Context, types.WorkspaceBinding, WorkspaceDiffArgs) (runtimeprojectstate.Diff, error)
func (d *WorkspaceDomain) Import(context.Context, types.WorkspaceBinding, types.ActorEnvelope, WorkspaceImportArgs) (runtimeprojectstate.ImportResult, error)
func (d *WorkspaceDomain) Checkpoint(context.Context, types.WorkspaceBinding, types.ActorEnvelope, WorkspaceCheckpointArgs) (runtimeprojectstate.CheckpointResult, error)
func (d *WorkspaceDomain) Stash(context.Context, types.WorkspaceBinding, types.ActorEnvelope, WorkspaceStashArgs) (runtimeprojectstate.StashResult, error)
type workspaceGatewayCaller func(context.Context, string, any) (json.RawMessage, error)
func runWorkspaceCommand(context.Context, string, []string, io.Writer, io.Writer, workspaceGatewayCaller) int
~~~

Mutations call ValidateLocalAction. Every adapter validates the binding, requires the registered checkout path/device/inode to match, and copies only binding-derived scope/root plus operation-specific args. Runtime startup calls Recover before RefreshWorkspace for every RegisteredWorkspaces result. Runtime calls RefreshWorkspace before every later scoped operation except that Stash may proceed only after ErrBranchSwitchPending and must be followed immediately by successful RefreshWorkspace plus Recover on the refreshed scope. RestoreStash stays private.

Public MCP names remain wormhole.workspace.status, wormhole.workspace.diff, wormhole.workspace.import, wormhole.workspace.checkpoint, and wormhole.workspace.stash with the already frozen routing-free schemas. Approved CLI names are top-level only: wormhole status, wormhole diff, wormhole import [--expected-working-tree-digest sha256:...], wormhole checkpoint --expected-working-tree-digest sha256:..., and wormhole stash --label <non-empty-label>. There is no wormhole workspace subcommand. No public argument accepts project_id, workspace_id, cwd, root, actor, Fabric profile, or credential; cwd appears only in the private WorkspaceContext bridge.

- [ ] **Step 1: Write RED adapter and top-level parser tests**

Assert public schemas have additionalProperties=false and no routing fields, mutators reject legacy/unknown actors, the five top-level command strings call the matching unchanged MCP names, and `wormhole workspace ...` is rejected. Use only canonical UUID literals in executable test data.

Run: go test ./internal/runtime/localapi ./cmd/wormhole -run 'TestWorkspace(PublicArgs|Domain|Binding|TopLevelCLI|RejectsLegacy|Refresh)' -count=1
Expected: FAIL because workspace operation adapters and parser are absent.

- [ ] **Step 2: Implement adapters and parser, then run GREEN**

Run: go test ./internal/runtime/localapi ./cmd/wormhole -run 'TestWorkspace(PublicArgs|Domain|Binding|TopLevelCLI|RejectsLegacy|Refresh)' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/localapi/workspace.go internal/runtime/localapi/workspace_test.go cmd/wormhole/workspace.go cmd/wormhole/workspace_test.go
git commit -m "feat: add portable workspace command domain"
~~~

### Atomic downstream inventory acceptance seam

Slice B injects/strips types.WorkspaceContext, calls Service.ResolveWorkingDirectory, resolves ActorEnvelope separately, calls RefreshWorkspace at startup and before status/write/checkpoint, and then stages its modifications to the two Slice-A-owned workspace.go files with live routing. Its coordinated commit registers the five unchanged MCP names, switches main.go to the five top-level CLI names (replacing the legacy sync-status meaning of wormhole status), removes any wormhole workspace form, and updates every alpha inventory/document surface atomically. Registry-wide tests prove every current local pillar tool and workspace tool receives the resolved binding and cannot reach a legacy table path.

## Dependency order and handoff

1. Task 1 freezes shared bytes and package ownership.
2. Task 2 persists an independently observed accepted base and stable workspace identity.
3. Task 3 first corrects the landed shared immutable-record/created-at/strict-codec
   contracts, then introduces atomic typed local mutations plus the deterministic
   semantic diff/merge primitives required before import.
4. Task 4 validates direct deltas, rebases overlays onto imported candidates,
   atomically retains complete ours/theirs conflict evidence, persists/restores
   complete stashes, makes Git the only base-advance authority, and guards branch
   switches.
5. Task 5 materializes a candidate with crash recovery while leaving base unchanged
   and blocks publication while Task-4 conflicts remain open.
6. Task 6 removes the tracked/private legacy leak without touching the Git index.
7. Task 7 replaces local pillar replica reads/writes with a rebuildable composed projection and typed-operation domain adapters.
8. Task 8 adds binding-aware workspace operations and the five top-level CLI parsers; the downstream runtime/setup seam registers routes and updates every public inventory surface atomically.

The runtime plan consumes WorkspaceContext, WorkspaceScope, WorkspaceBinding, RegisterWorkspaceResult, ResolveWorkingDirectory, RegisteredWorkspaces, RefreshWorkspace, registration, Status, Recover, observation results, Projection, and the single workspace.go domain; its private resolver returns the exact shared WorkspaceBinding and it exclusively owns bridge transport, Gateway wiring, and MCP registration. The setup/runtime plan modifies and stages the Slice-A-owned cmd/wormhole/workspace.go parser when it makes routes live. Slice D/Fabric consumes the exact shared internal/types ActorEnvelope, WorkspaceBinding, and RepositoryIdentity plus internal/types/projectstate Digest, DecodeTree(Tree), EncodeTree(Snapshot), Validate(Snapshot), DigestTree(Tree), DecodeOperation([]byte), CanonicalOperation(OperationV1), DigestCanonicalJSON, DigestCanonicalMarkdown, OperationV1, and ApplyOperation(Snapshot, OperationV1); it does not import runtime types or redefine canonical records/reducer/digest/strict-decoding logic. Slice E validates/creates public/private ActorEnvelope values through Validate; local issuance/writes use ValidateLocalAction, historical imports use ValidateHistorical, and neither rewrites legacy/unknown assurance.

## Final Slice-A verification gate

After every task-specific GREEN command and before declaring Slice A complete, run:

~~~bash
make check
~~~

Expected: PASS. The `make check` target includes the repository's merged atomic coverage
check; its reported merged statement coverage must be at least 80%. A focused package
command, a cached earlier run, or a passing build without this coverage result does not
satisfy the final gate.

## Self-review

- Type reconciliation: one final internal/types ActorEnvelope has PrincipalID, Validate, ValidateLocalAction, and ValidateHistorical; new writes reject legacy/unknown, all non-legacy agent assurances require accountability and session/harness provenance, and Fabric consumes the same type without importing runtime.
- Immutable/content contracts: Events and Git links are live-only exact-replay records
  with generic immutable errors/conflicts/direct-delta handling; mutable-record
  `created_at` is update-immutable but explicit resurrection may supply a fresh valid
  value; one shared strict `DecodeOperation`/`CanonicalOperation` byte authority and
  canonical JSON/Markdown digest API is used by runtime and later Fabric.
- Import/rebase: strict direct-delta errors cover immutable records, raw deletion,
  immutable `created_at`, tombstone digests, a changed prior tombstone, and direct
  resurrection; matching journals are the only exception. Direct validation never
  inspects an overlay; `ThreeWayRebase` alone owns overlay-versus-direct tombstone/edit
  evidence. RFC 6901 FieldValue changes, atomic arrays,
  sorted objects/conflicts, canonical SHA-256 IDs, deterministic Markdown LCS hunks,
  Git-base-owned Handle/Remotes, and post-semantic `updated_at` metadata are frozen.
  Conflicts retain the byte-identical complete prior candidate plus direct/both-side
  evidence atomically before absorbed operations stop replaying.
- Durability: stashes separate the semantic source base from a canonical selected-start
  replay envelope, retain complete composed bytes, and preserve candidate/operation rows
  on conflicted restore; checkpoint journals bind accepted-base digest and checkout
  identity, and a second immediate transaction rechecks/holds publication through final
  database state; recovery precondition failures retain all evidence;
  gateway_schema_migrations is the one extensible local ledger.
- Projection/routing seam: all current local pillar reads/writes use Projection/OperationV1 with no legacy-table bypass; public args expose no project/workspace/cwd/root/actor data; Slice A owns both workspace.go files, and runtime/setup later modifies and stages them with private binding resolution, startup/request refresh, live routes, and atomic alpha inventory changes. CLI names are the top-level status/diff/import/checkpoint/stash forms only.
- Existing requirements remain covered: strict schemas/remotes/references/operation
  rows, explicit Compose start/generation, CandidateDigest/OverlayGeneration status,
  idempotent registration, correct isolation, conflict checkpoint/Fabric gates, durable
  fallback, tracked legacy index preservation, concrete UUID fixtures, exact SQL/API/
  error algorithms, and the 80% gate.
- No task grants merge authority to legacy replica tables, discovers provider IDs over network, stages/commits/pushes Git, upgrades actor assurance, or leaves a public contract outside alpha inventory tests.
