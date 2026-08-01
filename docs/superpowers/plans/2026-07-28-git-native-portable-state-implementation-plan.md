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
  explicit initial through-generation, the current rebased prefix, and the later active
  suffix in separate canonical arrays. Both arrays become terminal stashed rows owned by
  that stash ID; clean restore never reclassifies them.
- Portable `EventV1` records and Git links are live-only immutable add-only records. Exact canonical
  same-ID replay is idempotent; unequal same-ID content uses one generic immutable-
  record error/conflict/direct-delta contract. Neither kind has tombstone or
  resurrection semantics.
- Snapshot version, project ID, and repository identity are immutable binding fields.
  Config.Handle and Remotes are Git-base-owned and never overlay-owned.
- Focused RED precedes implementation, GREEN precedes each commit, and final merged statement coverage remains at least 80%.
- V1 is agent-first and project/repository-lineage scoped. CLI/MCP parity means equivalent
  authorised operations, not organisation-wide state or equal interface weighting.
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

Embed internal/runtime/localstore/migrations/*.sql and expose const GatewaySchemaVersion = 1 plus func applyGatewayMigrations(ctx context.Context, db *sql.DB) error. The function acquires one dedicated connection, executes BEGIN IMMEDIATE, creates and shape-checks gateway_schema_migrations, rejects a recorded version greater than GatewaySchemaVersion, applies each missing numbered file once, inserts its version row, and commits. Any DDL or ledger-write failure rolls back the entire version. This ledger name and API are the only Gateway SQLite migration mechanism. Task 4 advances it with `000002_portable_transitions.sql`. The approved 2026-08-01 publication amendment owns `000003_workspace_publication.sql`; Task 5 owns `000004_checkpoint_publication_review.sql`; mandatory Task 6A owns `000005_workspace_activity.sql` after its focused activity/retention/promotion artifact is reviewed and approved. Task 7 and every migration-6 consumer wait for the reviewed activity commit. Multi-Fabric routing/sync own `000006`/`000007` on the issue-56 path, and the later Code Graph branch owns `000008`. No slice edits committed `000001` or `000002`, creates another ledger, or reuses a number.

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
    ImportedBy string
    ImportedAt time.Time
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
func (tx *WorkspaceMutationTx) OperationAudit(ctx context.Context) ([]WorkspaceOperationAuditRecord, error)
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
non-negative boundary and `direct_tree`; a direct start has boundary zero. `ImportedBy`
is exactly either a canonical UUID or `system:git-observation-rebase-v1`, and
`ImportedAt` is a valid UTC timestamp. Candidate, stash, and retry readers apply this
same union and never widen it. Active rows
are selected with `state='active' AND generation>? ORDER BY generation`. Runtime converts
each row to `StoredOperation` only after `projectstate.DecodeOperation`, canonical-byte,
row-ID, canonical UUID, positive generation, and exact state checks. The existing
standalone `AppendWorkspaceOperation` is removed; it may not remain as a mutation bypass.
`OperationAudit` instead returns a non-nil complete slice of
`WorkspaceOperationAuditRecord` across active, rebased, materialized, stashed, and
discarded states in stable increasing-generation order. Each record embeds the exact
`WorkspaceOperation` and retains its `CreatedAt`. Before returning anything the reader
validates every row's positive and globally increasing generation, globally unique
canonical operation ID, canonical operation bytes, known state, state-appropriate
stash-owner metadata, and timestamp; any corrupt row fails the complete read. Mutations
that require no-loss classification, including Stash, must project every embedded
operation in returned order without filtering or omission rather than composing
separately filtered reads. `CreatedAt` remains retry/audit evidence and is not a planner
input.
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

- [x] **Step 1: Write RED shared immutable-record, strict-codec, digest, and created-at tests**

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

- [x] **Step 2: Implement the shared Task-1 corrections**

Add `ErrImmutableRecord`; retain `ErrImmutableEvent` and, only if a caller needs it, a
Git-link name as aliases. Implement the three exported APIs exactly as frozen above.
Use the exported digest helpers inside ApplyOperation. Reject a Git-link tombstone in
both strict decode and Validate, remove Git-link from tombstone lookup/clear/apply paths,
and make existing Event/Git-link Put an exact-replay-only path. Compare `created_at`
before replacement of an existing live mutable record, but do not compare against a
tombstone during explicit resurrection.

- [x] **Step 3: Write RED composition, status, transaction, corruption, and isolation tests**

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

- [x] **Step 4: Implement strict composition and the exact Apply transaction**

Apply executes this transaction:

~~~text
BEGIN IMMEDIATE
binding = RequireWorkspace(project_id, workspace_id)
candidate = StrictReadCandidate(binding)
(start, initialThrough) = SelectExplicitStart(binding, candidate)
rows = ListActiveOperationsAfter(initialThrough ORDER BY generation)
operations = StrictDecodeStoredOperations(rows)
view = Compose(start, initialThrough, operations)
openConflicts = HasOpenConflicts()
if openConflicts and HasOpenConflictForKeys(operation targets): reject ErrWorkspaceConflicted
for operation in caller order:
  require operation.Actor.ValidateLocalAction()
  require operation.ExpectedViewDigest == current.Digest
  current = projectstate.ApplyOperation(current, operation)
  bytes = projectstate.CanonicalOperation(operation)
INSERT every workspace_overlay_operation with consecutive generations
UPDATE workspace_bindings SET status=(openConflicts ? 'conflicted' : 'pending')
COMMIT
~~~

OperationV1 has no issuer method; Apply requires operation.Actor.ValidateLocalAction before calling the shared reducer. Any scope, binding, candidate, stored-row, validation, canonicalization, duplicate operation ID, generation, precondition, reducer, insert, or status error rolls back. Status and Diff read binding/candidate/active rows from one SQLite snapshot and use the same strict start-selection/DecodeOperation/Compose path. The shared projectstate reducer owns exact-one variant, immutable-record replay, created-at update guards, tombstone/resurrection digests, and final validation invariants.

- [x] **Step 5: Write RED structural diff, deterministic merge, Markdown, and no-loss tests**

Add golden tests for add/modify/tombstone/resurrect and KB record/body digests; absent
versus present JSON `null`; `~0`/`~1` RFC 6901 escaping; `""` root and `/body`; recursive
sorted objects; atomic arrays; deep-copy/alias safety; stable entity/UUID/path ordering;
and actor attribution from the last active operation affecting a key. Add fixed conflict
preimage/ID/order tests across reversed map insertion and repeated runs. Cover disjoint
typed fields, same field, frozen existing-live Event/Git-link endpoint mutation and
disappearance, absent immutable additions, tombstone record and KB-body edits, differing
tombstones, and resurrection cases. Raw disappearance of an
old Event or Git link must produce sorted `immutable_record` evidence with an absent
FieldValue; raw disappearance of a mutable record must return ErrRawRecordDeletion, and
the corresponding direct-import acceptance test in Task 4 must require a tombstone; that
test owns direct-import provenance and is intentionally deferred. Add binding/version/project/
repository mismatch and candidate Config.Handle/Remotes mutation rejection, successful
new-base handle/remotes adoption, and SemanticDiff exclusion for those Git-owned fields.
Add `updated_at` cases for one semantic editor, two clean semantic editors, no semantic
editor, and a same-field semantic conflict; add immutable `created_at` input rejection.
`TestThreeWayRebaseMultiRecordConflictsReturnLosslessOwnedCandidate` must compare
EncodeTree bytes and digest with the prior candidate, mutate the returned result/evidence
to prove no aliases, and prove no partially clean changes entered the conflict result.

Markdown cases cover LF/final newline canonicalisation, deterministic equal-cost LCS
ties with repeated anchors, non-overlapping hunks, identical and unequal shared-anchor
insertions, overlapping replacements/deletions, stable IDs, and absence of conflict
markers.

Run: go test ./internal/runtime/projectstate -run 'Test(SemanticDiff|ThreeWayRebase|Markdown)' -count=1
Expected: FAIL because diff/rebase and their deterministic representation are absent.

- [x] **Step 6: Implement the exact diff and merge algorithms**

`FieldValue{Present:false}` has nil Value. Present values contain exactly one canonical
JSON value without CanonicalJSON's trailing LF; present JSON null is exactly `null`.
A complete root value uses the concrete typed record's canonical schema field order.
Generic sorted-map JSON is only a transient recursive merge representation; rehydrate a
complete root through its strict typed record before assignment or evidence emission.
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
record conflict. When both the live KB record and body changed against a tombstone,
emit root `tombstone_edit` and `/body` `tombstone_body` evidence; each conflict exists
only for its competing semantic surface. Event/Git-link disagreement emits
`immutable_record`.

ThreeWayRebase deep-copies all inputs and first validates `oldBase`. It then preflights
raw mutable disappearance in `newBase` and `candidate`, in that side order and canonical
kind/UUID order, before validating either side. After both sides validate, it requires
equal immutable snapshot version/project/repository binding fields and requires candidate
Config.Handle/Remotes to equal oldBase before taking newBase's values. Equal and one-sided
changes are accepted only where lifecycle, immutable-record, and existing-record
`created_at` invariants permit them. Removing an old Event or Git link yields
`ConflictImmutableRecord` with explicit present-to-absent evidence only after that side
passes typed/reference validation; removing an old mutable record without a valid
tombstone returns ErrRawRecordDeletion.
It coalesces exact immutable records and recursively merges only the frozen compatible
typed fields. SemanticDiff likewise rejects a raw mutable disappearance rather than
representing path removal as deletion. For an existing mutable record, changed
`created_at` is an invalid input. An explicit one-sided resurrection may have a fresh
valid value because prior bytes are not in the tombstone. `/updated_at` never selects semantics: take one semantic editor's
value, the later UTC value only after two-sided semantics merge cleanly, or oldBase when
neither side changed semantics.

For an old tombstone, coalesce exact endpoints and accept whichever endpoint differs when
the other remains byte-equal to oldBase, whether that endpoint is a resurrection or a
changed tombstone. When both endpoints diverge, divergent resurrection or resurrection
opposed by a changed tombstone emits `invalid_resurrection`: root evidence for unequal
record/tombstone surfaces and `/body` evidence for unequal KB body presence or content,
with both conflicts when both surfaces differ. Only divergent non-base tombstones emit
root `same_field`. From an old-live mutable record, exact dual tombstones coalesce and
unequal dual tombstones emit root `same_field`. For an ID absent from oldBase, accept a
one-sided live or tombstone
addition and coalesce exact dual additions. Unequal dual mutable live additions emit
root `same_field`, plus `/body` `same_field` for an unequal KB body. A concurrent live
addition versus tombstone emits root `tombstone_edit` and, for KB, `/body`
`tombstone_body`. Unequal immutable Event/Git-link additions remain
`immutable_record`. No absent object is invented as a field-merge base.

Lifecycle `exact` means byte-exact canonical endpoint surfaces, including `updated_at`.
The metadata timestamp-selection rule applies only to old-live/live-live semantic
merging. In an old-live tombstone race, a timestamp-only live difference is not a
semantic edit, so the tombstone wins without selecting a timestamp. Concurrent KB
additions and resurrection conflicts are surface-aware: emit root evidence only when
record JSON differs and `/body` evidence only when body presence/content differs. For an
old-live immutable Event or Git link, only endpoints equal to the old record are clean;
equal side mutations still emit `immutable_record`.
An immutable disappearance that leaves its side snapshot referentially invalid returns
the validation error instead of conflict evidence. Mutable raw disappearance retains
`ErrRawRecordDeletion` precedence.

Reducer replay and direct-import/materialisation validation own tombstone and
resurrection provenance. ThreeWayRebase does not compare a new tombstone's deleted
digests with oldBase because the endpoint may represent an authorised edit-then-delete;
it reconciles validated endpoints only. Existing-live `created_at` mutation returns an
error wrapping `projectstate.ErrOperationPrecondition`. If independently valid side
changes combine into an invalid typed/reference graph, final canonical validation
returns that typed error and no conflict kind is invented.

Markdown canonicalises both inputs first and computes base-relative minimum-edit LCS
hunks without a synthetic terminal line. Equal-cost script choices advance the base/
deletion side before insertion; hunks sort by base start, base end, inserted bytes.
Non-overlapping hunks merge; identical same-anchor insertions coalesce; unequal shared-
anchor insertions and overlapping replacement/deletion hunks conflict. No conflict
marker is emitted. Ranges are half-open: insertion strictly inside a replacement
conflicts, while insertion at its start/end boundary is emitted before/after it.
Equality and one-sided fast paths run before hunk construction. Each remaining
base-versus-side DP grid is limited to 100,000,000 cells with checked line-count and
allocation arithmetic; excess returns `ErrMarkdownMergeLimit` before allocation and
never falls back to an approximate merge. ThreeWayRebase returns `MergeResult{}` with
that error; retaining the prior candidate means the caller's input and persistent state
remain untouched, not that a non-zero result accompanies an error.

Conflict evidence is always oriented `Base=oldBase`, `Ours=candidate`, and
`Theirs=newBase`. If any conflict exists, MergeResult.Snapshot is a deep copy whose
EncodeTree bytes and digest are byte-identical to the complete prior composed `candidate`;
clean partial merges are discarded. Sorted conflict triples retain all new-base/direct
evidence.
Only with no conflict is the assembled shell fully typed/reference validated, canonically
encoded, and digested.

- [x] **Step 7: Run GREEN and commit**

Run: go test ./internal/types/projectstate ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(DecodeOperation|DigestCanonical|ApplyOperation|GitLink|CreatedAt|Apply|Compose|SemanticDiff|ThreeWayRebase|Markdown|Operation)' -count=1
Expected: PASS.

~~~bash
git add internal/types/projectstate internal/runtime/projectstate internal/runtime/localstore
git commit -m "feat: compose and merge portable state"
~~~

### Task 4: Direct import, stash restore, Git-base observation, and branch guard

**Files:**
- Create: internal/runtime/projectstate/stash.go
- Create: internal/runtime/projectstate/stash_test.go
- Create: internal/runtime/projectstate/git_observer.go
- Create: internal/runtime/projectstate/git_observer_test.go
- Create: internal/runtime/localstore/workspace_materialization_repo.go
- Create: internal/runtime/localstore/workspace_materialization_repo_test.go
- Modify: internal/runtime/projectstate/import.go
- Modify: internal/runtime/projectstate/import_test.go
- Modify: internal/runtime/projectstate/conflict_codec.go
- Modify: internal/runtime/projectstate/conflict_codec_test.go
- Modify: internal/runtime/projectstate/working_tree.go
- Modify: internal/runtime/projectstate/working_tree_test.go
- Modify: internal/runtime/projectstate/service.go
- Modify: internal/runtime/projectstate/service_test.go
- Modify: internal/runtime/projectstate/restore_plan.go
- Modify: internal/runtime/projectstate/restore_plan_test.go
- Modify: internal/runtime/projectstate/restore_retry.go
- Modify: internal/runtime/projectstate/restore_retry_test.go
- Modify: internal/runtime/projectstate/stash_plan.go
- Modify: internal/runtime/projectstate/stash_plan_test.go
- Modify: internal/runtime/localstore/workspace_repo.go
- Modify: internal/runtime/localstore/workspace_repo_test.go
- Modify: internal/runtime/localstore/workspace_restore_retry_repo.go
- Modify: internal/runtime/localstore/workspace_restore_retry_repo_test.go
- Modify: internal/runtime/localstore/workspace_transition_repo.go
- Modify: internal/runtime/localstore/workspace_transition_repo_test.go

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
type WorkspaceMaterializationRecord struct {
    JournalID string
    ExpectedLiveDigest projectstate.Digest
    AcceptedBaseDigest projectstate.Digest
    Checkout types.CheckoutIdentity
    PriorTreeDigest projectstate.Digest
    CandidateDigest projectstate.Digest
    ThroughGeneration int64
    PriorTree projectstate.Tree
    CandidateTree projectstate.Tree
    IncludedOperationsJSON *string // raw nullable included_operations_json TEXT
    State string
}
type WorkspaceMaterializationDisposition struct {
    Journals []WorkspaceMaterializationRecord
    Operations []WorkspaceOperation
}
type CheckpointOperationV1 struct {
    Generation int64 `json:"generation"`
    OperationID string `json:"operation_id"`
    OperationJSON string `json:"operation_json"`
    PrepublicationState string `json:"prepublication_state"`
}
type CheckpointOperationsV1 struct {
    SchemaVersion int `json:"schema_version"`
    InitialThroughGeneration int64 `json:"initial_through_generation"`
    Operations []CheckpointOperationV1 `json:"operations"`
}
func (tx *WorkspaceMutationTx) AcceptanceEligibleMaterialization(
    ctx context.Context,
) (*WorkspaceMaterializationRecord, error)
func (tx *WorkspaceMutationTx) AcceptanceEligibleMaterializationByCandidateDigest(
    ctx context.Context,
    digest projectstate.Digest,
) (*WorkspaceMaterializationRecord, error)
func (tx *WorkspaceMutationTx) MaterializationDisposition(
    ctx context.Context,
) (WorkspaceMaterializationDisposition, error)
func (r *WorkspaceRepo) TransitionReceiptByKey(
    ctx context.Context,
    scope types.WorkspaceScope,
    requestID string,
) (*WorkspaceTransitionReceiptRecord, error)
func (r *WorkspaceRepo) WithImmediateWorkspaceTransition(
    ctx context.Context,
    scope types.WorkspaceScope,
    requestID string,
    fn func(*WorkspaceMutationTx, *WorkspaceTransitionReceiptRecord) error,
) error
func ValidateDirectDelta(prior, next projectstate.Snapshot) error
func (s *Service) Import(ctx context.Context, req ImportRequest) (ImportResult, error)

type StashRequest struct {
    Scope types.WorkspaceScope
    RequestID string
    Actor types.ActorEnvelope
    Label string
}
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
    AbsorbedOperations []StoredOperation `json:"absorbed_operations"`
    Operations []StoredOperation `json:"operations"`
}
type RestoreStashRequest struct {
    Scope types.WorkspaceScope
    RequestID string
    StashID string
    Actor types.ActorEnvelope
}
type RestoreStashResult struct {
    RestoredDigest projectstate.Digest
    RebasedThroughGeneration int64
    Conflicts []Conflict
    StashRetained bool
}
type WorkspaceOperationAuditRecord struct {
    WorkspaceOperation
    CreatedAt time.Time
}
type WorkspaceStashRecord struct {
    StashID string
    SourceBaseDigest projectstate.Digest
    CandidateDigest projectstate.Digest
    SourceTree projectstate.Tree
    ComposedTree projectstate.Tree
    OperationsJSON string
    ThroughGeneration int64
    Actor types.ActorEnvelope
    ActorJSON string
    Label string
    CreatedAt time.Time
}
type WorkspaceRestoreRetryState struct {
    Workspace WorkspaceRecord
    BindingCreatedAt time.Time
    BindingUpdatedAt time.Time
    AcceptedSnapshotBlobDigest projectstate.Digest
    Candidate *WorkspaceCandidateRecord
    CandidateDirectTreeBlobDigest *projectstate.Digest
    CandidateRebasedTreeBlobDigest *projectstate.Digest
    Operations []WorkspaceOperationAuditRecord
    Stash WorkspaceStashRecord
    StashSourceTreeBlobDigest projectstate.Digest
    StashComposedTreeBlobDigest projectstate.Digest
    OpenConflicts []WorkspaceConflictOccurrence
}
func (tx *WorkspaceMutationTx) RestoreRetryState(
    ctx context.Context,
    stashID string,
) (WorkspaceRestoreRetryState, error)
func digestWorkspaceBlobBytesV1(raw []byte) projectstate.Digest
func (s *Service) Stash(ctx context.Context, req StashRequest) (StashResult, error)
func (s *Service) RestoreStash(ctx context.Context, req RestoreStashRequest) (RestoreStashResult, error)

type BranchSwitchAction string
const (
    BranchSwitchReject BranchSwitchAction = ""
    BranchSwitchDiscard BranchSwitchAction = "discard"
)
var ErrBranchSwitchDiscardNotApplicable = errors.New(
    "projectstate: branch switch discard not applicable",
)
type ObserveGitBaseRequest struct {
    Scope types.WorkspaceScope
    ExpectedBinding types.WorkspaceBinding
    Root string
    ExpectedCommit string
    BranchAction BranchSwitchAction
    RequestID string
    Actor types.ActorEnvelope
}
type ObserveGitBaseResult struct {
    PreviousCommit, ObservedCommit string
    PreviousRef, ObservedRef string
    PreviousBaseDigest, ObservedBaseDigest projectstate.Digest
    CandidateAccepted bool
    AcceptedJournalID *string
    Rebased bool
    Conflicts []Conflict
}
func (s *Service) ObserveGitBase(ctx context.Context, req ObserveGitBaseRequest) (ObserveGitBaseResult, error)
func (s *Service) RefreshWorkspace(ctx context.Context, binding types.WorkspaceBinding) (types.WorkspaceBinding, error)
~~~

The importer-origin union update surface is exact:

- `internal/runtime/localstore/workspace_repo.go`: candidate read validation near current
  line 744 and `UpsertCandidate` write validation near current line 799;
- `internal/runtime/localstore/workspace_restore_retry_repo.go`: retry candidate metadata
  validation near current line 212;
- `internal/runtime/projectstate/restore_plan.go`: current-proof candidate attribution
  validation near current line 182;
- `internal/runtime/projectstate/restore_retry.go`: retry-preimage candidate projection
  validation near current line 267; and
- `internal/runtime/projectstate/stash_plan.go`: `validateStashCandidate` near current
  line 98.

Every surface accepts exactly canonical UUID or `system:git-observation-rebase-v1` and
rejects every other value on read and write. The existing UUID retry golden remains, and
`restore_retry_test.go` adds a separate literal system-token retry preimage with a
hard-coded digest; neither expected digest is produced by the codec under test.

`RestoreRetryState` is an all-or-error localstore boundary on the caller's
exact-workspace transaction. It returns stable-ordered non-nil operation and open-conflict
slices, strictly validates every selected row, and retains exact canonical operation,
stash replay, and actor JSON. It also returns strict binding `created_at`/`updated_at` and
explicit exact-byte digests computed by localstore while it owns the raw accepted-snapshot,
candidate direct/rebased, and stash source/composed file-list BLOBs. A candidate direct
digest is non-nil exactly when Candidate is non-nil; its rebased digest is non-nil exactly
when that candidate's persisted `rebased_tree` is non-null. Persisted BLOBs must
strict-decode and byte-equal their canonical re-encoding before localstore computes the
digest. Runtime maps only these returned digests into the private retry projection; it
never attempts to call localstore's private file-list encoder or substitutes a semantic
tree digest.

The BLOB digest domain is exact and separate from `DigestTree`:
`digestWorkspaceBlobBytesV1(raw)` is
`"sha256:" + lowerhex(SHA256(raw))` over the complete raw bytes of one strictly validated
persisted canonical file-list BLOB, with no JSON canonicalization, length framing, prefix,
or separator added to the hash input. Fixed golden tests use literal canonical BLOB bytes
and hard-coded expected digests for accepted snapshot, candidate direct/rebased, and stash
source/composed columns; tests never calculate the expected value through the production
helper. This adds no column, schema version, or migration.

The runtime copies `AcceptedSnapshotBlobDigest`, candidate direct/rebased blob-digest
fields, and stash source/composed blob-digest fields verbatim from
`WorkspaceRestoreRetryState` into their namesake private v1 projections. Pointer
nullability must match the reader invariants above. Recomputing any of these values from
the decoded `Snapshot` or `Tree` is forbidden.

`WorkspaceMaterializationRecord` is a localstore boundary. Localstore preserves a
non-null `IncludedOperationsJSON` string byte-for-byte and rejects only empty, non-UTF-8,
or NUL-containing TEXT; it never parses, canonicalizes, or defaults that runtime-owned
envelope. Nil represents a migrated v1 SQL NULL. A nil prepared, published, or
recovered-new envelope blocks recovery/checkpoint/acceptance as missing proof; a nil
historical accepted envelope contributes no ownership and is tolerated only under the
complete no-residual rule below.
Runtime/projectstate alone strict-decodes `CheckpointOperationsV1`, rejects unknown fields,
requires a non-nil ordered operation array and exact persisted-row membership, and
requires every `OperationJSON` string to equal the canonical operation bytes including
their final LF. `MaterializationDisposition` returns non-nil, stable-ordered, cloned
complete journal and operation slices or an error from the caller's exact-workspace
immediate transaction; it requires no schema or migration change.

One private runtime proof consumes that complete disposition before Import,
ObserveGitBase, or Recover may classify an acceptance-eligible journal. ObserveGitBase
acceptance is Reject/Refresh-only; Discard uses an exact match solely to return
`ErrBranchSwitchDiscardNotApplicable`. `accepted`, `published`, and `recovered_new`
journals own current materialized rows. A `prepared`
journal blocks a stable proof and must drive Recover; `recovered_old` is excluded from
ownership. A nil legacy `accepted` envelope contributes no claims and is permitted only
when no residual materialized row depends on it; nil `published` or `recovered_new`
envelopes fail closed. Across all owning journals, claimed generations and operation IDs
are globally unique. Every claim must byte-match exactly one ownerless persisted row in
`materialized` state, and every materialized row must have exactly one claim. For each
owning journal, any persisted `active` or `rebased` row at or below its
`ThroughGeneration` must be claimed, in which case the claim-to-row rule requires the
current row to be materialized. Stashed/discarded gaps and active rows later than the
journal boundary are allowed. Historical prepublication state is established by the
checkpoint transaction's exact state transition plus its durable envelope; a later proof
validates that envelope, its boundary, and current row identity rather than pretending to
reconstruct an independent historical column.

Both materialization lookups select only exact-workspace `published` or `recovered_new`
rows. They share one strict full-set scan which validates every selected row and proves
that at most one eligible row exists before the digest-filtered method decides whether
to return it. The unfiltered method is the checkpoint pending-acceptance gate. Every
strict read validates the complete current workspace binding, canonical tree bytes and
digests, checkout identity, timestamps, paths, and `ExpectedLiveDigest ==
PriorTreeDigest`; both fields name the same complete prepublication live tree.

RefreshWorkspace requires `binding.Validate()`, injects both `Scope: binding.Scope` and
`ExpectedBinding: binding`, revalidates the binding checkout identity, and invokes
ObserveGitBase with BranchSwitchReject, an empty RequestID, and the zero ActorEnvelope
against the independently observed HEAD/tree. ObserveGitBase requires
`req.ExpectedBinding.Validate()`, `req.Scope == req.ExpectedBinding.Scope`, and the
canonical Root to match that expected checkout. Inside its immediate transaction it
requires complete equality between the loaded binding and `ExpectedBinding` before Git
reobservation or mutation. The binding is private resolved runtime context: CLI/MCP
clients never supply it, and adapters copy it only from workspace resolution.
RefreshWorkspace then resolves and returns the updated exact WorkspaceBinding. It is the
single refresh seam consumed by Slice B. Gateway startup first calls
Recover(binding.Scope), then RefreshWorkspace(binding), for every RegisteredWorkspaces
result. Request orchestration calls RefreshWorkspace before every subsequent scoped
status, diff, pillar/workspace write, import, checkpoint, and graph operation. The sole
exception is Stash after RefreshWorkspace returns ErrBranchSwitchPending: Stash runs
against the still-validated pre-refresh binding with one stable RequestID, then the caller
must immediately call RefreshWorkspace(binding) and Recover(refreshed.Scope). Its
committed receipt lets a retry resume those follow-up calls, and the stash call is not
reported successful unless both succeed. ObserveGitBase never invokes Stash or nests a
stash transaction. Any invalid branch action or committed tree fails closed before the
requested operation; no caller-supplied ref/tree can bypass ObserveGitBase.
If its Reject observation has an indeterminate COMMIT, RefreshWorkspace returns no
binding (`types.WorkspaceBinding{}`) and the error wraps `ErrCommitOutcomeUnknown`; it
never resolves or infers the possibly committed binding.

Migration `000002_portable_transitions.sql`, `GatewaySchemaVersion=2`, candidate
persistence, transition receipts, and conflict-occurrence history have already landed.
The remaining Task 4 work consumes those committed boundaries and must not recreate or
edit migration 000002. Its new localstore work is the strict materialization reader and
accepted-base transition seam. Migration 000002 has this exact logical schema and
preserves every existing row:

~~~sql
-- Rebuild workspace_conflicts with the same columns plus occurrence_id.
CREATE TABLE workspace_conflicts_v2 (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  occurrence_id TEXT NOT NULL,
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
  PRIMARY KEY(project_id,workspace_id,occurrence_id),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
-- Copy every v1 row with occurrence_id=conflict_id, then replace the v1 table.
CREATE UNIQUE INDEX workspace_one_open_semantic_conflict
  ON workspace_conflicts(project_id,workspace_id,conflict_id)
  WHERE state='open';
CREATE INDEX workspace_open_conflicts
  ON workspace_conflicts(project_id,workspace_id,state);

-- Rebuild workspace_overlay_operations with all existing keys/columns plus nullable
-- stashed_by_stash_id and these checks:
-- CHECK(state IN ('active','rebased','stashed','materialized','discarded'));
-- CHECK(state='stashed' OR stashed_by_stash_id IS NULL).
-- Copy every v1 row, replace the v1 table, and recreate workspace_overlay_generation.
-- Do not add a foreign key from stashed_by_stash_id: owned audit rows outlive deletion
-- of their workspace_stashes row. New stashes require a non-null owner in application
-- code; migrated legacy stashed rows may remain NULL and are corruption if used.

-- Add a nullable legacy-compatible column to workspace_materializations. Every new
-- checkpoint row requires a strict canonical CheckpointOperationsV1 value.
ALTER TABLE workspace_materializations
  ADD COLUMN included_operations_json TEXT;

CREATE TABLE workspace_transition_receipts (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  action TEXT NOT NULL CHECK(action IN ('stash','restore','discard')),
  request_digest TEXT NOT NULL,
  actor_json TEXT NOT NULL,
  result_json TEXT NOT NULL,
  outcome TEXT NOT NULL CHECK(outcome IN ('clean','conflicted')),
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,request_id),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX workspace_one_acceptance_eligible_candidate
  ON workspace_materializations(project_id,workspace_id)
  WHERE state IN ('published','recovered_new');
~~~

The migration runner performs both table rebuilds and copies inside migration 000002's
single immediate transaction, verifies affected counts, and leaves all v1 constraints,
foreign keys, history, and indexes represented above intact. Existing conflict rows use
their semantic `conflict_id` as `occurrence_id`. Every new occurrence ID is a canonical
UUIDv4; the only accepted legacy non-UUID occurrence is a migrated row whose
`occurrence_id == conflict_id` and whose semantic ID recomputes exactly. Exact repeated
open evidence retains its occurrence; evidence absent from a later
replacement is resolved with `resolved_at`, and reopening a resolved semantic conflict
creates a fresh occurrence. `conflict_id` remains the recomputed deterministic semantic
ID and is never used as the history primary key. Migration runs `PRAGMA foreign_key_check`
before ledger advancement. More than one pre-v2 acceptance-eligible row in a workspace
makes the partial unique-index creation fail and roll back 000002; migration never
chooses or deletes one.

Runtime owns these exact private versioned digest and receipt projections; they are not
CLI/MCP request schemas:

~~~go
type checkoutIdentityDigestV1 struct {
    CanonicalPath string `json:"canonical_path"`
    Device uint64 `json:"device"`
    Inode uint64 `json:"inode"`
}
type workspaceBindingDigestV1 struct {
    Scope types.WorkspaceScope `json:"scope"`
    Checkout checkoutIdentityDigestV1 `json:"checkout"`
    Repository types.RepositoryIdentity `json:"repository"`
    AcceptedRef string `json:"accepted_ref"`
    AcceptedCommitSHA string `json:"accepted_commit_sha"`
    AcceptedTreeDigest string `json:"accepted_tree_digest"`
}
type stashRequestDigestV1 struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    Scope types.WorkspaceScope `json:"scope"`
    Actor types.ActorEnvelope `json:"actor"`
    Label string `json:"label"`
}
type restoreRequestDigestV1 struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    Scope types.WorkspaceScope `json:"scope"`
    Actor types.ActorEnvelope `json:"actor"`
    StashID string `json:"stash_id"`
}
type discardRequestDigestV1 struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    Scope types.WorkspaceScope `json:"scope"`
    Actor types.ActorEnvelope `json:"actor"`
    ExpectedBinding workspaceBindingDigestV1 `json:"expected_binding"`
    CanonicalRoot string `json:"canonical_root"`
    ExpectedCommit string `json:"expected_commit"`
}
type transitionRecordKeyV1 struct {
    Kind string `json:"kind"`
    ID string `json:"id"`
}
type transitionConflictV1 struct {
    ID string `json:"id"`
    Key transitionRecordKeyV1 `json:"key"`
    FieldPath string `json:"field_path"`
    Kind ConflictKind `json:"kind"`
    Base FieldValue `json:"base"`
    Ours FieldValue `json:"ours"`
    Theirs FieldValue `json:"theirs"`
}
type restoreStashResultV1 struct {
    RestoredDigest projectstate.Digest `json:"restored_digest"`
    RebasedThroughGeneration int64 `json:"rebased_through_generation"`
    Conflicts []transitionConflictV1 `json:"conflicts"`
    StashRetained bool `json:"stash_retained"`
}
type restoreStashReceiptV1 struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    Outcome string `json:"outcome"`
    Result restoreStashResultV1 `json:"result"`
    ConflictRetryDigest *projectstate.Digest `json:"conflict_retry_digest"`
}
type stashResultV1 struct {
    StashID string `json:"stash_id"`
    SourceDigest projectstate.Digest `json:"source_digest"`
    CandidateDigest projectstate.Digest `json:"candidate_digest"`
    OperationCount int `json:"operation_count"`
}
type stashReceiptV1 struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    Outcome string `json:"outcome"`
    Result stashResultV1 `json:"result"`
}
type discardResultV1 struct {
    PreviousCommit string `json:"previous_commit"`
    ObservedCommit string `json:"observed_commit"`
    PreviousRef string `json:"previous_ref"`
    ObservedRef string `json:"observed_ref"`
    PreviousBaseDigest projectstate.Digest `json:"previous_base_digest"`
    ObservedBaseDigest projectstate.Digest `json:"observed_base_digest"`
    CandidateAccepted bool `json:"candidate_accepted"`
    AcceptedJournalID *string `json:"accepted_journal_id"`
    Rebased bool `json:"rebased"`
    Conflicts []transitionConflictV1 `json:"conflicts"`
}
type discardReceiptV1 struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    Outcome string `json:"outcome"`
    Result discardResultV1 `json:"result"`
}
type restoreRetryBindingV1 struct {
    Binding workspaceBindingDigestV1 `json:"binding"`
    Status string `json:"status"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    AcceptedSnapshotBlobDigest projectstate.Digest `json:"accepted_snapshot_blob_digest"`
}
type restoreRetryCandidateV1 struct {
    AcceptedBaseDigest projectstate.Digest `json:"accepted_base_digest"`
    WorkingTreeDigest projectstate.Digest `json:"working_tree_digest"`
    DirectTreeBlobDigest projectstate.Digest `json:"direct_tree_blob_digest"`
    RebasedTreeBlobDigest *projectstate.Digest `json:"rebased_tree_blob_digest"`
    RebasedThroughGeneration int64 `json:"rebased_through_generation"`
    ImportedBy string `json:"imported_by"`
    ImportedAt time.Time `json:"imported_at"`
}
type restoreRetryOperationV1 struct {
    Generation int64 `json:"generation"`
    OperationID string `json:"operation_id"`
    OperationJSON string `json:"operation_json"`
    State string `json:"state"`
    StashedByStashID *string `json:"stashed_by_stash_id"`
    CreatedAt time.Time `json:"created_at"`
}
type restoreRetryStashV1 struct {
    StashID string `json:"stash_id"`
    SourceBaseDigest projectstate.Digest `json:"source_base_digest"`
    CandidateDigest projectstate.Digest `json:"candidate_digest"`
    SourceTreeBlobDigest projectstate.Digest `json:"source_tree_blob_digest"`
    ComposedTreeBlobDigest projectstate.Digest `json:"composed_tree_blob_digest"`
    OperationsJSON string `json:"operations_json"`
    ThroughGeneration int64 `json:"through_generation"`
    ActorJSON string `json:"actor_json"`
    Label string `json:"label"`
    CreatedAt time.Time `json:"created_at"`
}
type restoreRetryConflictOccurrenceV1 struct {
    OccurrenceID string `json:"occurrence_id"`
    ConflictID string `json:"conflict_id"`
    RecordKind string `json:"record_kind"`
    RecordID string `json:"record_id"`
    FieldPath string `json:"field_path"`
    ConflictKind string `json:"conflict_kind"`
    BaseJSON string `json:"base_json"`
    OursJSON string `json:"ours_json"`
    TheirsJSON string `json:"theirs_json"`
    CreatedAt time.Time `json:"created_at"`
}
type restoreStashRetryPreimageV1 struct {
    SchemaVersion int `json:"schema_version"`
    Action string `json:"action"`
    Outcome string `json:"outcome"`
    Scope types.WorkspaceScope `json:"scope"`
    RequestID string `json:"request_id"`
    RequestDigest projectstate.Digest `json:"request_digest"`
    StashID string `json:"stash_id"`
    Binding restoreRetryBindingV1 `json:"binding"`
    Candidate *restoreRetryCandidateV1 `json:"candidate"`
    Operations []restoreRetryOperationV1 `json:"operations"`
    Stash restoreRetryStashV1 `json:"stash"`
    OpenConflicts []restoreRetryConflictOccurrenceV1 `json:"open_conflicts"`
}
~~~

`request_id` is a canonical UUID. Each request digest is
`DigestCanonicalJSON` of its dedicated v1 projection above, so it binds schema version,
action, exact scope, the complete strict canonical actor envelope, and the action payload.
Discard additionally binds the complete private adapter-supplied resolved expected binding, canonical root,
and ExpectedCommit; serializing `types.WorkspaceBinding` directly is forbidden because
the dedicated tagged projection is the frozen digest contract. Request-digest golden
tests freeze the exact canonical bytes and digest for stash, restore, and discard.
Reusing an ID with the same digest is a retry; reuse with another digest returns
`ErrIdempotencyConflict` without mutation. Clean stash/restore returns the receipt result
read-only on retry, and the receipt survives clean restore's stash deletion. A conflicted
restore always reloads and strict-recomputes the current view, stash replay, merge, open
evidence, and retry digest before it may match and return the receipt. Receipt
`actor_json` is immutable canonical JSON; every read strict-decodes it, requires
byte-identical re-encoding, calls `ValidateHistorical`, and matches it to the actor
envelope bound into the request digest. A transition receipt's logical key is the exact
textual project/workspace/request triple. For detection only, localstore readers
CAST-match all three persisted key columns to expose storage-class aliases. A present key
requires exactly one match, exact raw key values, and TEXT storage for every selected
column; zero matches must satisfy the strict all-null absence shape, and multiple matches
fail as ambiguity. `InsertTransitionReceipt` strict-preflights that same logical key in
the caller-owned `BEGIN IMMEDIATE`, so no hidden logical duplicate can be inserted. This
requires no schema or migration change.

`TransitionReceiptByKey` syntactically validates the scope and canonical request ID, then
queries only `workspace_transition_receipts`; it never queries `workspace_bindings`.
It CAST-matches the exact textual project/workspace/request key, counts every logical
match, and requires exact raw-key equality plus TEXT storage class for every selected
column. Its table-only SELECT projects `COUNT(rowid) OVER()` plus the exact receipt
columns and `typeof` values over the CAST-matched rows. `sql.ErrNoRows` returns nil;
count exactly one returns the existing fully strict-decoded
`WorkspaceTransitionReceiptRecord`; any other returned count is corruption/ambiguity.
`WithImmediateWorkspaceTransition` performs the same syntactic validation, opens a
dedicated connection and `BEGIN IMMEDIATE`, and makes that same table-only lookup its
first SQL read. It passes the bound-scope `WorkspaceMutationTx` and receipt to the
callback. A present receipt permits no prior or implicit workspace read; only nil permits
the callback to call `tx.Workspace` or another state method. Existing
`WithImmediateWorkspace` and `TransitionReceipt` semantics remain unchanged for Stash and
RestoreStash.

Localstore treats receipt `result_json` as
action-opaque: it requires exactly one valid compact JSON value followed by exactly one
LF, preserves the bytes and object-member order, and never schema-decodes or generic-map
re-encodes it. The action-specific ProjectState codec owns schema, tags, member order,
exact re-encoding, and semantic canonicality and rejects noncanonical bytes. Restore
`result_json` therefore contains exactly one strict canonical private
`restoreStashReceiptV1`. Its tagged private `Result` maps exactly to the public
`RestoreStashResult`, its explicit `Action` and `Outcome` must equal the receipt row
(`Action == "restore"`, `Outcome == "clean" || Outcome == "conflicted"`), and
`ConflictRetryDigest` is nil exactly for clean restore and a canonical non-nil digest
exactly for conflicted restore. Unknown fields, trailing JSON, a noncanonical re-encoding,
an invalid or unequal action/outcome in any preimage or envelope, or disagreement with
the receipt row outcome fails closed. The private result requires a non-nil canonically
sorted conflict array, strict conflict-value rehydration, and exact mapping to the returned
public result and persisted open evidence. Clean restore requires `Conflicts == []` and
`StashRetained == false`; conflicted restore requires a non-empty conflict array and
`StashRetained == true`.

Stash `result_json` is exactly canonical `stashReceiptV1`, with Action `"stash"`, Outcome
`"clean"`, and a tagged Result that maps exactly to public `StashResult`. Discard
`result_json` is exactly canonical `discardReceiptV1`, with Action `"discard"`, Outcome
`"clean"`, and a tagged Result that maps exactly to the public ObserveGitBase result for
the actual discard. Discard requires `CandidateAccepted == false`,
`AcceptedJournalID == nil`, `Rebased == false`, and a non-nil empty `Conflicts` array.
Every conflict array in these private codecs is non-nil; empty is encoded as `[]`, never
`null`. Optional pointers have no `omitempty` and nil is encoded explicitly as `null`.
Each action's strict decoder rejects unknown fields, trailing values, noncanonical bytes,
wrong action/outcome, invalid result invariants, and any noncanonical/unsorted conflict.
Fixed golden tests freeze literal canonical result and receipt bytes plus their
hard-coded `DigestCanonicalJSON` digests for stash, clean/conflicted restore, and discard;
expected bytes/digests are not generated through the production codec under test.
These envelopes use the existing `result_json` TEXT and do not change
`GatewaySchemaVersion` or any migration. Each mutation
first reads any receipt and compares its action/digest, inserts the receipt in the same
transaction as the state transition, and requires exactly one row. If COMMIT returns an
error, the service reads back that exact scope/request ID on a fresh connection; readback
never proves failure. For Stash, only an available, strictly decoded clean stash receipt
whose action, request digest, and complete result exactly match the attempted transition
may promote the indeterminate outcome to success. An absent, unavailable, malformed,
corrupt, or mismatched Stash readback returns `StashResult{}` and preserves an error
wrapping `ErrCommitOutcomeUnknown`. A post-COMMIT readback mismatch is not
`ErrIdempotencyConflict`; that sentinel is reserved for the ordinary pre-mutation lookup
of a reused request ID with a different request digest. The caller retries Stash with the
same request ID. A conflicted restore receipt must pass the complete conflicted-retry
state and semantic verification before proving success; unavailable, malformed,
mismatched, or unverifiable readback wraps `ErrCommitOutcomeUnknown`. Other actions use
their separately frozen action-specific proof and retry rules.

Direct delta rules are exact. Relative to the previously imported candidate, or accepted
base when no candidate exists, Import first compares the prior canonical path inventory
with the captured raw tree, in canonical entity-kind and UUID order, before attempting to
DecodeTree the next surface. A missing mutable record path returns
`ErrDirectPathDeletion`; a missing existing Event or Git-link path returns
`ErrDirectImmutableRecordMutation`. A valid mutable tombstone retains the stable record
path, and a KB tombstone removes only its body. Malformed values at present paths then
receive the strict typed DecodeTree/validation error. After decode,
`ValidateDirectDelta(prior,next)`:

- returns ErrDirectImmutableRecordMutation for any changed or removed existing Event
  or Git-link ID; an exact canonical replay is unchanged;
- returns ErrTombstoneDigest when DeletedContentDigest is not the digest of the prior canonical record or DeletedBodyDigest is absent/wrong for KB;
- returns ErrDirectEditTombstone when a prior tombstone is changed directly rather than
  replayed exactly; it does not inspect or classify any active overlay edit;
- returns ErrDirectResurrection when a tombstone becomes live;
- returns ErrDirectImmutableFieldMutation when an existing live mutable record changes
  `created_at`.

The exported validator receives no materialization proof and never bypasses these rules.
Inside the repository-owned Import transaction only, runtime first obtains and proves one
complete `MaterializationDisposition`; a private helper may bypass the direct-edit errors
only when `AcceptanceEligibleMaterializationByCandidateDigest` returns exactly one row
that byte-matches the proved disposition, whose state is `published` or `recovered_new`, accepted-base digest and complete
checkout identity equal the current binding, candidate bytes strict-decode and canonicalize
byte-identically, and recorded candidate digest equals both those bytes and the captured
live-tree digest. `ExpectedLiveDigest` must equal `PriorTreeDigest`, which must match the
complete canonical prior tree and its recomputed digest. A nil legacy
`IncludedOperationsJSON` is never proof. The strict canonical checkpoint-operation envelope must enumerate the
exact operation rows, bytes, generations, prepublication states, and selected initial
boundary; missing legacy `included_operations_json`, extra or missing rows, or any mismatch
fails closed.
That exception makes a typed resurrection importable after checkpoint; callers cannot
construct or claim it.

Git-link tombstones/resurrections fail strict DecodeTree before delta comparison. Snapshot
version, project ID, and repository identity must match the binding. Direct Config.Handle
and Remotes changes are Git-base input, not overlay mutations; ThreeWayRebase owns them by
requiring the composed candidate to retain the prior surface and taking the direct values.
`ValidateDirectDelta(prior,next)` owns only the decoded direct-versus-prior surface. A
correct new direct tombstone is valid at this stage. `ThreeWayRebase(oldBase, newBase, candidate)` alone compares that
tombstone with the overlay candidate and emits `ConflictTombstoneEdit` when the overlay
edited the live record; the direct validator never returns an overlay conflict.
Import returns the scope/checkout/project/repository errors already frozen plus these
direct-delta sentinels. A nil `ExpectedWorkingTreeDigest` supplies no caller precondition;
a non-nil value must be canonical and equal the initially captured `DigestTree`, or
Import returns `ErrWorkingTreeChanged` with zero writes. No error replaces
workspace_candidates or conflicts. Import immediately deep-clones every filesystem or
repository reader result that it retains across another call. Its active replay set comes
only from the already-proved complete disposition: any active row at/below the selected
boundary or rebased row above it is corruption, every remaining active row is decoded and
composed, and `TransitionOperations` changes exactly those preloaded rows. It never uses
`ActiveOperationsAfter` or a generation-range UPDATE. After the second cloned no-follow
capture matches bytes and digest, Import repeats canonical-root and checkout-identity
validation immediately before `ReplaceOpenConflictOccurrences`, its first database write;
this rejects a byte-identical checkout replacement whose device/inode changed. Before any
replacement it also strict-reads open conflict occurrences and requires exact consistency
between their presence and the pre-existing workspace `conflicted` status. Mismatch fails
closed with zero writes. Import has no RequestID or transition receipt. An indeterminate
COMMIT therefore always returns `ImportResult{}` plus an error wrapping
`ErrCommitOutcomeUnknown`; retry performs the complete capture/transaction/recomputation
again and never guesses the original result from readback state.

Import executes:

~~~text
require req.Actor.ValidateLocalAction()
require req.ExpectedWorkingTreeDigest == nil || canonical(req.ExpectedWorkingTreeDigest)
capturedTree = owned deep clone of ReadWorkingTreeNoFollow(req.Root) result // bounded, sorted canonical paths/bytes
capturedDigest = projectstate.DigestTree(capturedTree)
require req.ExpectedWorkingTreeDigest == nil || *req.ExpectedWorkingTreeDigest == capturedDigest
BEGIN IMMEDIATE
workspace = owned deep clone of tx.Workspace(ctx) result // localstore.WorkspaceRecord for req.Scope
require exact complete old binding and RevalidateCheckout(workspace.Binding, req.Root)
binding = workspace.Binding // types.WorkspaceBinding
acceptedSnapshot = workspace.Snapshot // projectstate.Snapshot
openConflicts = owned deep clone of tx.OpenConflictOccurrences(ctx) result
require (workspace.State == "conflicted") == (len(openConflicts) != 0) before replacement
candidate = owned deep clone of tx.Candidate(ctx) result // *WorkspaceCandidateRecord
priorSurface = candidate.DirectSnapshot if candidate != nil else acceptedSnapshot
priorTree = projectstate.EncodeTree(priorSurface)
materializationDisposition = owned deep clone of tx.MaterializationDisposition(ctx) result
materializationProof = StrictProveMaterializationDisposition(materializationDisposition)
RawDirectDeletionPreflight(priorTree, capturedTree)
liveSnapshot = projectstate.DecodeTree(capturedTree)
canonicalLiveTree = projectstate.EncodeTree(liveSnapshot)
require canonicalLiveTree byte-equals capturedTree and liveSnapshot.Digest == capturedDigest
directDiff = SemanticDiff(priorSurface, liveSnapshot, nil)
importedChangeCount = len(directDiff.Changes)
liveTreeBytes = EncodeFileList(canonicalLiveTree)
journalRow = tx.AcceptanceEligibleMaterializationByCandidateDigest(ctx, capturedDigest)
if journalRow != nil: journalRow = owned deep clone of journalRow
if journalRow != nil: matchingProof = requireMatchingMaterialization(materializationProof, journalRow, binding, priorTree, capturedTree, capturedDigest)
if journalRow == nil: ValidateDirectDelta(priorSurface, liveSnapshot)
(start, initialThroughGeneration) = SelectExplicitStart(acceptedSnapshot, candidate)
require materializationDisposition.Operations contains no active row at/below initialThroughGeneration
require materializationDisposition.Operations contains no rebased row above initialThroughGeneration
activeRows = every materializationDisposition.Operations row whose state is active // stable generation order, all are above boundary
storedOperations = StrictDecodeStoredOperations(activeRows)
oldComposed = Compose(start, initialThroughGeneration, storedOperations)
merged = ThreeWayRebase(priorSurface, liveSnapshot, oldComposed.Snapshot)
rebasedTree = projectstate.EncodeTree(merged.Snapshot)
rebasedTreeBytes = EncodeFileList(rebasedTree)
liveTree = owned deep clone of ReadWorkingTreeNoFollow(req.Root) result // exact second read immediately before first DB mutation
require liveTree byte-equals capturedTree and projectstate.DigestTree(liveTree) == capturedDigest
require CanonicalRoot(req.Root) == binding.Checkout.CanonicalPath
require RevalidateCheckout(binding, req.Root) // exact final root/identity check before first write
StrictReplaceOpenConflicts(merged.Conflicts) // reuse exact occurrence, resolve absent, UUIDv4 on reopen/new
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
tx.TransitionOperations(ctx, activeRows, "rebased", nil) // exact preloaded membership and affected count
UPDATE workspace_bindings SET status = 'conflicted' when conflicts exist else 'pending'
  WHERE project_id=req.Scope.ProjectID AND workspace_id=req.Scope.WorkspaceID
COMMIT
if COMMIT result is indeterminate: return ImportResult{} plus error wrapping ErrCommitOutcomeUnknown
~~~

Conflict persistence accepts only Task 3's globally sorted conflicts. Before insert and
after every read it strict-decodes each `FieldValue` envelope, requires absent values to
have no bytes and present values to contain exactly one canonical JSON value, rehydrates
every root value through its concrete typed record codec, leaves `/body` and recursive
non-root values generic, recomputes `conflict_id`, and rejects row/key/path/kind/ID or
sort-order mismatch as corruption. Canonical JSON encoding of a `json.RawMessage` may
normalize object member order, so restart tests compare the rehydrated triples
byte-for-byte with the in-memory Task 3 result. Failure-injection and corruption tests
cover occurrence reuse, resolve, reopen-with-new-UUID, duplicate-open rejection, semantic
ID mismatch, unsorted rows, malformed envelopes, and noncanonical or wrong typed roots.

The candidate upsert writes all Task-2 non-null fields explicitly; it does not rely on a
default to supply actor, time, binding, or digest provenance. Conflict replacement
resolves or reuses only exact-workspace open occurrences and leaves resolved history and
every other workspace untouched. `ImportResult.PreviousCandidateDigest` is non-nil iff a
candidate row or active overlay existed before Import and equals that pre-import composed
digest. `ImportedCandidateDigest` is `liveSnapshot.Digest`,
`ComposedViewDigest` is `merged.Snapshot.Digest`, `ImportedChangeCount` is exactly
`len(SemanticDiff(priorSurface, liveSnapshot, nil).Changes)`, and
`RebasedThroughGeneration` is
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
require req.Scope valid, req.RequestID canonical UUID, req.Actor.ValidateLocalAction()
require req.Label is valid UTF-8, 1..256 bytes, and contains no NUL/CR/LF
requestDigest = DigestCanonicalJSON(stash request preimage v1)
stashID = NewCanonicalUUIDv4() // generated before any mutation
BEGIN IMMEDIATE for req.Scope
if receipt for RequestID exists:
  require action='stash' and request_digest=requestDigest else ErrIdempotencyConflict
  return strict-decoded receipt result read-only
workspace = tx.Workspace(ctx)
if tx.HasOpenConflicts(ctx):
  ROLLBACK and return StashResult{}, localstore.ErrWorkspaceConflicted
candidate = tx.Candidate(ctx)
(sourceBase, sourceBaseTree) = (workspace.Snapshot, canonical accepted tree)
auditRecords = tx.OperationAudit(ctx) // non-nil complete all-state records, stable order, strict rows
operationInventory = make non-nil []WorkspaceOperation with capacity len(auditRecords)
for auditRecord in auditRecords in returned order:
  operationInventory.append(owned clone of auditRecord.WorkspaceOperation) // no filtering or omission
require len(operationInventory) == len(auditRecords)
// auditRecord.CreatedAt remains retained retry/audit evidence; it is not planner input
plan = buildStashPlan(workspace.Binding, sourceBase, candidate, operationInventory)
require plan.SourceTree == sourceBaseTree // canonical accepted semantic source evidence
result = StashResult{StashID: stashID, SourceDigest: plan.SourceDigest,
  CandidateDigest: plan.CandidateDigest, OperationCount: plan.OperationCount}
INSERT workspace_stashes(stash_id=stashID, source_tree=EncodeFileList(plan.SourceTree),
  composed_tree=EncodeFileList(plan.ComposedTree),
  operations_json=plan.OperationsJSON,
  through_generation=plan.ThroughGeneration,
  source_base_digest=plan.SourceDigest,
  candidate_digest=plan.CandidateDigest,
  actor_json=CanonicalJSON(req.Actor), label=req.Label)
DELETE workspace_candidates
tx.TransitionOperations(ctx, plan.AbsorbedRows, "stashed", &stashID) // exact preloaded membership
tx.TransitionOperations(ctx, plan.LaterRows, "stashed", &stashID) // exact preloaded membership
UPDATE workspace_bindings SET status='clean'
INSERT canonical clean stash transition receipt with actor_json=CanonicalJSON(req.Actor)
  and result=result
COMMIT
if COMMIT succeeds: return result
if COMMIT result is indeterminate:
  readback = read exact scope/request ID on a fresh connection
  if readback is available and strictly decodes as action='stash', outcome='clean',
     with request_digest=requestDigest and complete result exactly equal to result:
    return decoded StashResult success
  // absent/unavailable/malformed/corrupt/mismatched is not ErrIdempotencyConflict
  // caller retries with the same RequestID
  return StashResult{} plus original error wrapping ErrCommitOutcomeUnknown
~~~

`source_tree` and `source_base_digest` are the semantic pre-stash rebase base, normally
the accepted snapshot, and are deliberately independent of replay selection. The exact
selected Compose start is embedded as `SelectedStartTree` plus
`SelectedStartDigest` in the canonical `StashReplayV1` stored in the existing
`operations_json` column. Strict decode requires schema version 1, non-nil absorbed and
later operation arrays, an embedded tree that passes the file-list-equivalent path/order limits,
`DecodeTree`, canonical re-encoding, digest, and exact project/repository binding checks.

`OperationAudit` is the sole Stash operation source. It returns every exact-workspace row
as a `WorkspaceOperationAuditRecord` in stable increasing-generation order and
strict-validates global generation and operation-ID uniqueness, canonical operation
bytes, state, owner metadata, and `CreatedAt` before the planner sees anything. Stash
maps every record's embedded `WorkspaceOperation`, in order and with equal input/output
cardinality, into a non-nil `[]WorkspaceOperation`; it performs no filtering or omission.
`CreatedAt` remains retained in the audit record for `RestoreRetryState` and is not a
`buildStashPlan` input. The planner derives all ownerless `rebased` rows at or below the
selected boundary and all ownerless `active` rows above it, rejects any active row
at/below the boundary or rebased row above it, and validates but ignores terminal
`materialized`, `stashed`, and `discarded` rows. Stash must not use
`RebasedOperationsAtOrBefore`, `ActiveOperationsAfter`, a pair of other filtered reads,
or generation-range updates. Only the two exact cloned memberships returned by the
planner may transition.

The replay envelope's non-nil, strictly generation-sorted `AbsorbedOperations` array
records every current candidate row already `state='rebased'` at or below the explicit
boundary; their effect is present in `SelectedStartTree`. Its separate non-nil
`Operations` array records only active rows above that boundary. An immediate
post-rebase stash is valid with an empty `Operations`
array and `InitialThroughGeneration == through_generation == G`. A later stash contains
exactly `G < generation <= through_generation`. `Operations` is always a non-nil JSON
array (`[]` when empty), never `null`; `AbsorbedOperations` follows the same rule.
Successful stash atomically deletes the candidate, transitions exactly both recorded
arrays to terminal stashed rows owned by `stashed_by_stash_id=stashID`, and sets the
binding to `clean`. A later stash can therefore never claim rows owned by an earlier
stash. The owner has no foreign key because the terminal operation audit survives clean
restore deletion of the stash row.
Any exact-scope open conflict returns `StashResult{}` plus
`localstore.ErrWorkspaceConflicted` and changes no candidate, operation, stash, conflict,
receipt, or binding row. `StashResult.SourceDigest` is `sourceBase.Digest`, CandidateDigest is
`plan.CandidateDigest` (the composed snapshot digest), and OperationCount is exactly
`len(plan.AbsorbedRows)+len(plan.LaterRows)` (the planner's `OperationCount`).

RestoreStash first requires a valid scope, canonical request/stash UUIDs, and
`Actor.ValidateLocalAction`, computes the canonical restore request digest, and opens one
immediate transaction. A same-ID different-digest receipt returns
`ErrIdempotencyConflict`. A matching clean receipt returns its strict canonical result
read-only even though the stash has been deleted. A matching conflicted receipt is not a
cached answer: restore continues through the complete strict recomputation below and
only returns read-only when the recomputed tagged result, persisted semantic evidence,
and complete retry-state digest match it.

Within that same transaction RestoreStash strict-decodes and digest-checks the semantic `source_tree`,
`composed_tree`, and `StashReplayV1`. It rejects unknown fields, trailing or
noncanonical JSON, a noncanonical embedded selected-start tree, selected-start digest
mismatch, a negative boundary, and unordered, overlapping, or duplicate rows. The final
later-row generation must equal `stash.through_generation`; an empty later list instead
requires the initial boundary to equal it. `AbsorbedOperations` must contain exactly the
rebased prefix captured through the initial boundary, and `Operations` exactly the active
suffix above it. Restore selects all and only persisted rows whose
`stashed_by_stash_id=stash.stash_id`; their count, generation, operation ID, canonical
operation JSON, ordering, and membership must byte-for-byte equal the concatenated two
arrays, and every selected row must be `state='stashed'`. An ownerless migrated legacy
stashed row or any swapped, extra, or missing owner/member is `ErrStashOperationMismatch`.
Restore calls
`Compose(selectedStart, replay.InitialThroughGeneration, replay.Operations)` and requires
the result's encoded tree bytes, digest, and ThroughGeneration to equal `composed_tree`,
`candidate_digest`, and `stash.through_generation` before considering current state.
Every query, recomputation, evidence comparison, affected-count check, and mutation in a
restore uses this one caller-owned transaction.

Restore then strict-selects and composes the current start/boundary/later active rows and
calls `ThreeWayRebase(sourceBase, current.Snapshot, stashComposed.Snapshot)`. On a clean
merge only, one immediate transaction preserves the current direct surface, persists the
merged rebased candidate with the greatest absorbed generation, transitions exactly the
current active rows absorbed by that candidate to `state='rebased'`, leaves every
original stash row terminal `state='stashed'` with its immutable owner,
resolves every exact-workspace open conflict occurrence, sets binding status
to `pending`, deletes the stash, and records a canonical clean receipt. When a candidate
row existed, its `imported_by` and `imported_at` are preserved; otherwise the new
candidate uses the restore actor principal and transaction UTC time. It never reinserts
an operation UUID or rewrites an original stash row. Every transition/delete/update
requires its exact expected affected count; a mismatch rolls back.

On conflict, candidate bytes/columns and every operation row remain byte-identical. Before
the allowed writes, `RestoreRetryState` strict-loads the complete binding/status and
accepted snapshot, every candidate field and canonical blob, every operation logical
field plus `created_at`, every stash field/blob/replay envelope/actor plus `created_at`,
and every sorted open conflict occurrence/evidence plus `created_at`. The transaction
writes only the deterministic sorted open conflict evidence for
`ThreeWayRebase(sourceBase, current.Snapshot, stashComposed.Snapshot)` and binding status
`conflicted`, then rereads that complete state. It requires the protected
accepted-snapshot blob digest, every non-status binding field, binding `created_at`,
candidate, operation, and stash projection to equal its prewrite value. Only the exact
status/`updated_at` mutation produced by this `SetStatus("conflicted")` call and the exact
open-conflict replacement may differ; the post-write status/evidence and `updated_at`
must equal that computed mutation. Both binding timestamps enter the post-state digest.
It computes `DigestCanonicalJSON` over a
`restoreStashRetryPreimageV1` whose Action is `"restore"` and Outcome is `"conflicted"`,
using the complete post-write state, and
records that canonical digest in the non-nil `ConflictRetryDigest` of the conflicted
receipt in the same transaction. It retains the complete stash unchanged. The returned
`RestoredDigest` and `RebasedThroughGeneration` describe the unchanged current composed
view and `StashRetained` is true; the stash-composed surface remains in the retained stash
as resolution/audit evidence, not as a silently installed candidate.

An exact repeated conflicted RestoreStash strict-decodes and composes the stash replay
and the current persisted rows again, recomputes the same three-way rebase and semantic
evidence, rereads the complete `WorkspaceRestoreRetryState`, and requires both the public
result and recomputed retry digest to match the receipt. It returns the same result with
zero writes only when the candidate, every operation row, binding/status, retained stash,
accepted-snapshot blob digest, both binding timestamps, and sorted open conflicts equal
the first conflicted post-state at this retry
transaction's linearization point.
Changed or corrupt current rows (including a row the failed merge would have absorbed),
changed candidate/stash/conflict evidence, resolved conflicts, or a non-conflicted
binding fail closed without mutation. `ErrStashCorrupt` covers tree/envelope/replay/digest
corruption; `ErrStashOperationMismatch` covers missing, altered, extra, noncanonical, or
wrongly stated persisted stash-operation rows. The digest cryptographically commits to
canonical persisted state at those two linearization points; it does not prove that no
intermediate mutation occurred and was later reversed. An indeterminate commit outcome
wraps `ErrCommitOutcomeUnknown`; retry uses the same RequestID, and a conflicted readback
must pass this same semantic and retry-digest verification before success may be returned.

Git observation remains independent:

1. Validate the request without reading the filesystem, Git, or current binding. Require
   `ExpectedBinding.Validate()`, exact `Scope == ExpectedBinding.Scope`, and a structurally
   canonical Root matching `ExpectedBinding.Checkout`. ExpectedCommit is exactly 40 or 64
   lowercase hexadecimal characters. `BranchSwitchReject` requires an empty RequestID
   and the zero ActorEnvelope. `BranchSwitchDiscard` requires a canonical UUID RequestID
   and `Actor.ValidateLocalAction()`. No other action is valid, and ObserveGitBase never
   calls Stash. Compute the discard digest from only this validated request, including the
   tagged complete expected-binding projection, Root, and ExpectedCommit.
2. Before any filesystem, Git, or current-binding check, a Discard request reads the exact
   receipt key through `TransitionReceiptByKey(ctx, Scope, RequestID)`. An exact strict
   discard receipt with the same action and digest returns its complete result read-only
   with zero Git calls and zero writes. The same ID with another action or digest returns
   `ErrIdempotencyConflict`; corrupt, hidden-alias, duplicate, or otherwise ambiguous
   receipt state fails closed. Only exact absence continues.
3. The no-receipt path performs one full outside observation. Run read-only Git with
   `GIT_OPTIONAL_LOCKS=0` and hooks disabled: `rev-parse HEAD^{commit}`,
   `symbolic-ref -q HEAD` (empty means detached), `ls-tree -rz --full-tree HEAD` for the
   canonical `.wormhole` paths, then `cat-file --batch`. Require HEAD equals
   ExpectedCommit, strict-decode the observed bytes, and validate project, repository,
   and digest. Re-read HEAD and checkout identity; a race returns
   `ErrGitObservationChanged` without DB writes.
4. Reject uses the existing `WithImmediateWorkspace`. Discard uses
   `WithImmediateWorkspaceTransition(ctx, Scope, RequestID, callback)`, whose enforced
   first SQL read re-reads only the receipt table and passes that receipt to the callback.
   A concurrent exact first commit returns its strict result read-only; another
   action/digest conflicts, and corrupt or ambiguous state fails closed. Only exact
   absence lets the callback call `tx.Workspace`, require complete equality with
   `ExpectedBinding`, and then preload the complete candidate, operation audit,
   open-conflict evidence, accepted state, and `MaterializationDisposition`.
5. Reobserve checkout identity, symbolic ref, and HEAD after those preloads and before the
   first write. Any difference from the outside observation returns
   `ErrGitObservationChanged`. This is the linearization boundary; a same-SHA ref change
   counts, and later external changes are caught by the next mandatory RefreshWorkspace.
   Capture one UTC transaction observation time after this reobservation and before the
   first write.
6. Strict-prove and classify the complete preloaded `MaterializationDisposition` before
   Discard applicability or any eligible match, rebase, or mutation. A `prepared` row,
   orphan/unclaimed/nonterminal materialized row, duplicate ownership, corrupt or
   ambiguous terminal ownership, invalid envelope/boundary, or any other incomplete proof
   fails first as its existing recovery/corruption blocker. Historical `accepted` and
   `recovered_old` rows are nonblocking only after their complete ownership proof passes.
   Only a proved-safe disposition reaches applicability.
7. Discard applies only when the symbolic ref actually changed and the proved preloaded
   state has at least one candidate, exact `active`/`rebased` proposal operation, or open
   conflict occurrence. Otherwise return `ObserveGitBaseResult{}` plus
   `ErrBranchSwitchDiscardNotApplicable` with zero mutation and no receipt. Exact receipt
   paths in steps 2 and 4 remain the only earlier outcome. Reject advances a ref change
   with no proposal normally. Under Reject, any actual symbolic-ref change with proposal
   always returns `ErrBranchSwitchPending`, even for an exact eligible materialization
   match. Discard instead proceeds through the proved applicability and match rules in
   steps 7 and 8. Only a same-symbolic-ref Reject/Refresh observation may accept or rebase.
8. Classify a proved acceptance-eligible row. An exact match requires the byte-identical
   row to be `published` or `recovered_new`; accepted-base digest and checkout to equal
   the current binding; observed canonical bytes/digest to equal `CandidateTree` and
   `CandidateDigest`; `ExpectedLiveDigest == PriorTreeDigest`; and `PriorTree`,
   `ThroughGeneration`, and strict included-operation membership to match.

   Discard never accepts that match. It returns `ObserveGitBaseResult{}` plus
   `ErrBranchSwitchDiscardNotApplicable`, with no receipt or mutation and byte-identical
   journal, candidate, materialized rows, conflicts, operations, and binding. A
   nonmatching proved acceptance-eligible row blocks applicable Discard with
   `ErrGitMaterializationPrecondition` and the same retention.

   Only same-symbolic-ref Reject/Refresh may accept an exact match: mark the journal
   `accepted`, delete the accepted candidate, retain materialized history, preserve exact
   later active generations, and set status `pending` iff later active rows remain,
   otherwise `clean`; no candidate or open conflict remains. A nonmatching eligible row
   blocks same-ref Reject rebase with `ErrGitMaterializationPrecondition`. No new journal
   state or migration is introduced.
9. A nonmatching same-ref base under Reject uses `ThreeWayRebase`. It enforces immutable
   version/project/repository fields, requires an existing candidate to retain old-base
   Handle/Remotes, adopts new-base Handle/Remotes, and atomically persists complete
   evidence, row transitions, binding, and exact affected counts. Preserve an existing
   candidate's `ImportedBy` and `ImportedAt` exactly. If no candidate exists, set
   `ImportedBy` to `system:git-observation-rebase-v1` and `ImportedAt` to the single
   transaction observation time from step 5. Never select an operation actor or fabricate
   an ActorEnvelope or UUID. Every candidate/stash/retry validation accepts importer
   provenance only as canonical UUID or that fixed token.
10. Applicable Discard keeps the existing write order: transition the exact preloaded
   active/rebased rows to `discarded`, leave stashed and accepted-journal materialized
   rows unchanged, delete the candidate, resolve the exact open conflicts, insert the
   clean receipt, and advance accepted ref/commit/digest/snapshot. Exact affected-count
   mismatch rolls back everything.
11. If the Discard COMMIT result is unknown, read the fresh exact receipt without any Git
    call. Return success only when its strict action, digest, and complete result equal the
    attempted transition; otherwise return `ObserveGitBaseResult{}` plus the original
    error wrapping `ErrCommitOutcomeUnknown`. A mismatch is not an idempotency conflict.
    Every non-discard ObserveGitBase unknown COMMIT returns the same zero result and
    sentinel without receipt or state inference; RefreshWorkspace consequently returns
    `types.WorkspaceBinding{}` on that error.

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
TestImportRawDeletionPrecedesMalformedPresentValueInCanonicalOrder,
TestImportRejectsIncorrectTombstoneContentDigest,
TestImportRejectsIncorrectTombstoneBodyDigest,
TestImportRejectsChangedCreatedAt,
TestValidateDirectDeltaAcceptsCorrectTombstoneWithoutOverlay,
TestImportPersistsOverlayTombstoneConflict,
TestImportRejectsDirectResurrection, and
TestImportAcceptsMatchingMaterializedResurrection. Add
TestValidateDirectDeltaHasNoMaterializationBypass,
TestImportExpectedWorkingTreeDigestOptionalAndCanonical,
TestImportSecondNoFollowReadDetectsSameDigestRace, and
TestImportMatchingMaterializationRequiresAcceptanceEligibleBoundCanonicalBytes. The
matching test passes the proved eligible row, complete binding, canonical prior tree,
captured candidate tree, and captured digest independently and rejects mutation of each.
Add `TestImportChangeCountIsDirectSemanticDiffOnly`: use multiple direct changes plus
independent overlay changes and require
`ImportedChangeCount == len(SemanticDiff(priorSurface, liveSnapshot, nil).Changes)`, with
overlay changes excluded.
Repeat that assertion for the matching-materialization exception and require the nil actor
map passed to `SemanticDiff`; neither path may substitute merged changes or operation count.
Add reader-alias fixtures proving Import clones retained filesystem/localstore values
before the next read. Add exact operation-disposition cases rejecting active rows at or
below the selected boundary and rebased rows above it, while proving every valid active
row is composed and only the exact preloaded active membership transitions to rebased.
Inject checkout replacement after the matching second no-follow capture but before the
first write and require the final canonical-root/identity revalidation to roll back with
zero candidate, operation, conflict, or binding mutation even when tracked bytes/digest
are unchanged. Add both pre-existing status/evidence mismatch directions (`conflicted`
without open evidence and open evidence without `conflicted`) and require zero writes.
Inject indeterminate Import COMMIT, require `ImportResult{}` plus
`ErrCommitOutcomeUnknown`, prove no receipt/readback inference occurs, and verify retry
recomputes from fresh state.
Add localstore reader cases for the unfiltered pending-acceptance lookup, digest
match/mismatch, migrated nil versus byte-exact non-nil operation TEXT, complete-set
duplicate detection, restart/non-aliasing, and corruption of every bound field. In
particular, unequal `ExpectedLiveDigest` and `PriorTreeDigest` must fail the reader and
the Import acceptance exception without mutation. Add complete-disposition ownership
cases for multiple disjoint owning journals, prepared blocking, recovered-old exclusion,
nil legacy accepted history, duplicate generation/operation-ID claims, claimed-row
byte/state/owner mismatch, an unclaimed materialized row, and an omitted active or
rebased row at/below an owning journal boundary. Stashed/discarded gaps and later active
rows must remain valid.
`TestImportPersistsOverlayTombstoneConflict` must first prove direct/prior validation
succeeds, then prove only ThreeWayRebase emits `ConflictTombstoneEdit`; no direct-delta
sentinel may substitute for the persisted merge evidence. Add
TestImportConflictPersistsOursTheirsAtomicallyAcrossRestart: inject a failure at each
candidate/conflict/row-state/status write, prove the prior state remains byte-identical,
then prove a successful conflict retains the old complete composed candidate, new direct
tree, canonical FieldValue triples, absorbed generation, and exact `conflicted` status
after reopen.
Add migration tests that open v1 fixtures through 000002, prove conflict occurrence
history/open uniqueness, discarded operation state plus nullable stash ownership,
receipt actor constraints, acceptance-eligible candidate uniqueness, nullable legacy
checkpoint envelopes, rollback on each rebuild/copy/index failure, and
`GatewaySchemaVersion == 2` without modifying 000001. Add strict conflict codec tests for
wrong semantic IDs, duplicate/unsorted rows, malformed envelopes, and typed-root
rehydration after restart.

Run: go test ./internal/runtime/projectstate -run 'Test(Import|ValidateDirectDelta)' -count=1
Expected: FAIL because Import is absent.

- [ ] **Step 2: Write RED complete-stash and restore tests**

Test restart byte equality for source_tree, composed_tree, and the canonical versioned
operations_json envelope, including the semantic source-base/selected-start split; clean
restore onto a changed base; conflicting restore retains the complete stash/evidence
while preserving candidate and every operation row; and restored operations do not fail
stale whole-view preconditions. Planner tests use real reducer operations and cover an
accepted source plus active suffix, a direct candidate plus active suffix that cannot
compose from the accepted source, a rebased candidate with an empty suffix, and a rebased
candidate with sparse later generations. They pass one complete projected operation
inventory, reject
otherwise-hidden active rows at/below the boundary and rebased rows above it, and prove
valid materialized/stashed/discarded terminal rows are strictly validated, preserved,
and absent from replay and transition memberships. Add
`TestStashAfterRebaseWithNoLaterOperationsPersistsBoundaryAcrossRestart`, proving an empty
row list, `initial_through_generation == through_generation == G`, a selected-start tree
that contains the absorbed prefix, a distinct accepted `source_tree`, and unchanged
operation bytes transitioned from rebased to terminal stashed state at/below G. Add
`TestStashAfterRebasePersistsOnlyLaterActiveOperationsAcrossRestart`, proving the absorbed
prefix and later active suffix occupy their distinct non-nil envelope arrays, receive the
same stash owner, and survive clean restore as terminal owned audit rows. Add
`TestRestoreStashConflictPreservesAbsorbedPrefixAndLaterOperationsAcrossRestart`, proving
the same two layers remain in the retained stash while current candidate/rows stay
byte-identical. Reject unknown envelope versions/fields, trailing/noncanonical bytes,
selected-start or source-base digest mismatch, boundary/through mismatch, and missing,
extra, altered, or wrongly stated persisted rows without mutation. Add two sequential
stashes and prove their row-owner sets are disjoint; swapped ownership, an extra owned
row, a missing owned row, and an ownerless legacy row must each fail closed without
mutation.

Add request/receipt cases for invalid UUIDs, actor assurance, empty/oversize/control-byte
labels, nil versus empty operations arrays, cross-binding trees, same-ID same-digest clean
retry, same-ID different-digest rejection, receipt survival after clean restore deletion,
strict historical actor-envelope decoding, audit/restart reads after clean restore stash
deletion and discard, conflicted retry strict recomputation, exact affected-count rollback, and an injected
unknown Stash commit outcome. Only an exact strictly decoded clean receipt with matching
action, request digest, and complete result may turn that outcome into success; absent,
unavailable, malformed, corrupt, or mismatched readback must return `StashResult{}` plus
an error wrapping `ErrCommitOutcomeUnknown`, and mismatch must not become
`ErrIdempotencyConflict`. Prove a
subsequent retry uses the same request ID. Freeze the exact canonical
stash/restore/discard request preimages and digests with golden tests. Also freeze literal
canonical bytes and hard-coded digests for `stashResultV1`/`stashReceiptV1`, clean and
conflicted `restoreStashResultV1`/`restoreStashReceiptV1`, and
`discardResultV1`/`discardReceiptV1`; reject null conflict arrays, invalid pointer/slice
combinations, action/outcome mismatch, unknown/trailing/noncanonical fields, and unsorted
conflicts. Add localstore fixed goldens for `digestWorkspaceBlobBytesV1` over literal
accepted-snapshot, candidate direct/rebased, and stash source/composed canonical BLOBs,
then prove `RestoreRetryState` returns those exact digests without runtime re-encoding.
Corrupt or drift
each retry-preimage component independently, including an unrelated operation and its
`created_at`, candidate/stash canonical blob bytes, every binding field/status/accepted
snapshot, binding `created_at` and `updated_at`,
stash actor/envelope/timestamp, and conflict occurrence/evidence/timestamp. Every change
must fail closed with zero writes; an exact conflicted restart retry must match the
receipt digest and public result. Unknown conflicted COMMIT readback must run that same
verification rather than trust the receipt alone. First-conflict fault tests require the
accepted snapshot, all non-status binding fields, and binding `created_at` to remain exact;
only the expected status/`updated_at` mutation from SetStatus and open-conflict replacement
may differ. Zero-write retry must reject drift in either persisted binding timestamp,
including a later same-status rewrite that changes only `updated_at`.

Before committing `Service.Stash`, add a hidden-BLOB receipt-key retry that returns zero
and leaves every row byte-identical, extend sibling isolation to the same project ID with
different workspace IDs, and make the idempotent read-only retry's write-blocking triggers
unrestricted `BEFORE UPDATE` triggers so owner, timestamp, and other-column rewrites
cannot evade them.

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
conflict no-loss restart, branch reject/discard, same-SHA ref change, detached HEAD,
complete-binding CAS, inside-transaction reobservation, post-linearization next-refresh
detection, discarded-versus-stashed/historical-materialized row disposition,
accepted-journal retention, exact included-operation membership, later active generations,
zero-actor Reject versus actor-attributed Discard, acceptance status, unequal
ExpectedLiveDigest/PriorTreeDigest rejection, and two-workspace isolation.

Add `TestBranchSwitchDiscardExactRetryPrecedesGitAndBinding`: install an exact strict
receipt, make every Git/filesystem/current-binding dependency fail if called, and require
the complete prior result with zero writes. Its action/digest-conflict cases return
`ErrIdempotencyConflict` before Git; hidden-alias, duplicate, malformed, corrupt, and
noncanonical receipts fail closed before Git. Add
`TestTransitionReceiptByKeyReadsOnlyReceiptTable`: a rejecting/query-recording
`database/sql` driver proxy proves present and absent lookups never touch
`workspace_bindings`; it rejects any unexpected table or SELECT. Real SQLite exact-TEXT,
hidden-alias, zero/one/multiple-match, and strict-record cases freeze query semantics. Add
`TestWithImmediateWorkspaceTransitionReceiptIsFirstRead`: invalid syntax issues no SQL;
the recording driver/proxy shows an exact retry's first and only SELECT is the receipt
table and an absent trace reads it before the callback's first `tx.Workspace`/binding
SELECT. Triggers are not accepted as SELECT-order evidence. Existing
`TransitionReceipt` and `WithImmediateWorkspace` stash/restore tests remain unchanged.
Add
`TestBranchSwitchDiscardConcurrentReceiptRecheckWins`: return absent outside the
transaction, commit the same request concurrently, and require the transaction's first
read to return the exact result before binding/state/Git reads or writes. A concurrent
different action/digest conflicts, and corruption fails closed.

Add a table-driven `TestBranchSwitchDiscardApplicability`. No ref change with any one or
combination of candidate, active row, rebased row, or open conflict is not applicable;
an actual ref change with none is also not applicable. Each returns
`ObserveGitBaseResult{}`, `ErrBranchSwitchDiscardNotApplicable`, no receipt, and
byte-identical state after restart. An actual ref change with each proposal kind applies;
same-SHA branch-to-branch, branch-to-detached, and detached-to-branch changes count.
Changed-ref cases with only stashed rows, only discarded rows, only accepted-journal
historical materialized rows, or only resolved conflict history are also not applicable;
each leaves no receipt or base change and is byte-identical after restart. An exact prior
receipt wins for every now-not-applicable row and every blocker fixture. Separate
changed-ref cases with orphan materialized rows, nonterminal materialized rows, unclaimed
rows, or corrupt/ambiguous terminal ownership must fail with their recovery/corruption
error before applicability; they produce no receipt/base change and remain byte-identical
after restart. Add Reject cases proving a ref switch without proposal advances normally
and every ref switch with proposal returns `ErrBranchSwitchPending` even for an exact
eligible match. Prove separately that applicable Discard follows its discard path and only
a same-ref Reject/Refresh commit change takes acceptance/rebase.

Add `TestObserveGitBaseRejectSameRefAcceptsMatchingPublished` and
`TestObserveGitBaseRejectSameRefAcceptsRecoveredNewAfterPublishRestart`. Both keep the
symbolic ref unchanged. The latter restarts after filesystem publication, runs Recover to
`recovered_new`, accepts the exact Git tree under Reject, deletes the candidate, retains
materialized history, and preserves only exact later active rows. Add a
published/recovered-new table proving applicable Discard never accepts an exact match: it
returns `ObserveGitBaseResult{}` plus
`ErrBranchSwitchDiscardNotApplicable`, writes no receipt, does not advance the base, and
preserves journal, candidate, materialized rows, conflicts, operations, and binding
byte-identically after restart. Negative tests prove a nonmatching `published` row blocks
same-ref Reject rebase and a nonmatching `recovered_new` row blocks applicable Discard
with `ErrGitMaterializationPrecondition`, retaining the same bytes. Prepared requires
recovery. Historical `accepted` and `recovered_old` rows do not block either path, and no
case creates a journal state or migration.

Add `TestObserveGitBaseSameRefRebasePreservesCandidateProvenance`, comparing
`ImportedBy` and `ImportedAt` bytes before/after success, conflict, rollback, and restart.
Add `TestObserveGitBaseSameRefRebaseUsesSystemProvenanceWithoutCandidate`: require exact
`system:git-observation-rebase-v1`, require `ImportedAt` to equal the single UTC time
captured after in-transaction Git reobservation, and prove no operation actor,
ActorEnvelope, or generated UUID is used. Candidate, stash, and retry readers accept only
a canonical UUID or the fixed token and reject every other string without mutation.
Keep the UUID retry golden and add a literal system-token retry preimage plus hard-coded
digest, exercising every exact read/write/restore/stash validator listed above.

Add fault injection after each discard and same-ref-rebase write. Every failure rolls back
operation, candidate, conflict, receipt, journal, and binding state byte-identically; a
successful restart reproduces the exact result and provenance. Add
`TestBranchSwitchDiscardUnknownCommitConfirmsExactReceiptWithoutGit`: only a fresh strict
receipt whose action, digest, and result equal the attempted transition returns success,
without Git. Absent, unavailable, malformed, corrupt, ambiguous, or mismatched readback
returns `ObserveGitBaseResult{}` plus the original error wrapping
`ErrCommitOutcomeUnknown`; mismatch is not `ErrIdempotencyConflict`. Add
`TestObserveGitBaseRejectUnknownCommitReturnsZeroWithoutInference` and
`TestRefreshWorkspaceUnknownCommitReturnsNoBinding`, requiring
`types.WorkspaceBinding{}` and using readback traps to prove neither path reads a receipt,
binding, or Git state to infer success.

Reject forbids a RequestID, Discard requires one, and ObserveGitBase has no stash path.
Reject invalid `ExpectedBinding`, unequal Scope, a Root differing from its checkout, or
any inside-transaction complete-binding mismatch. Prove RefreshWorkspace injects its
resolved binding and the discard golden digest changes for every expected-binding field.

Run:

~~~bash
go test ./internal/runtime/projectstate ./internal/runtime/localstore \
  -run 'Test(ObserveGitBase|BranchSwitch|RefreshWorkspace|'\
'TransitionReceiptByKey|WithImmediateWorkspaceTransition)' -count=1
~~~

Expected: FAIL because observer is absent.

- [ ] **Step 4: Implement the exact transactions and algorithms above**

Import performs its initial filesystem read before BEGIN IMMEDIATE and its exact second
no-follow read under the writer barrier before mutation, clones retained reader results,
revalidates canonical root/checkout identity immediately before its first write, derives
all operation disposition from the complete proof, and uses an exact
`TransitionOperations` call rather than a range UPDATE. Reject and a Discard no-receipt
path perform the initial full observation before BEGIN IMMEDIATE; Discard performs both
receipt reads through `TransitionReceiptByKey` and
`WithImmediateWorkspaceTransition` at the exact earlier boundaries above. The new seams
query only the receipt table before absence, add no schema, and do not change
`TransitionReceipt` or `WithImmediateWorkspace`. ObserveGitBase reobserves
checkout/ref/HEAD at its transaction linearization boundary, strictly proves the complete
materialization disposition before applicability, and never lets Discard execute matching
acceptance writes. Only same-ref Reject/Refresh may accept. Every mutation revalidates the
required binding state/digests and reuses Task 3's caller-owned transaction helpers.
Import, Stash, RestoreStash, and `BranchSwitchDiscard` call ValidateLocalAction before
mutation, so legacy/unknown can never create new state. Reject/Refresh are trusted Git
observations: they require the zero actor and create no actor attribution.
Trusted zero-actor same-ref rebase uses only the fixed system importer provenance or
preserves the existing candidate provenance; it never borrows an operation actor.
Every stored operation uses DecodeOperation; every stored tree uses strict canonical
decode. Encoding, merge, receipt/conflict serialization, affected-count verification,
row transition, or status failure rolls back. Stash rejects an existing open conflict
without mutation. A newly conflicted
restore persists only exact conflict evidence plus `conflicted` status and leaves its
candidate, every operation row, and retained stash unchanged; only a clean restore may
transition rows, persist a candidate, delete the stash, and set `pending`. Import never
deletes conflict history; its exact atomic replacement only reuses, resolves, or opens
occurrences. ObserveGitBase
never accepts a caller-provided tree/ref. TestRefreshWorkspaceCallsObserveGitBase,
TestRefreshBeforeStatus, TestRefreshBeforeWrite, TestRefreshBeforeCheckpoint,
TestStartupRecoversThenRefreshesEveryRegisteredWorkspace, and
TestStashAfterBranchPendingRefreshesThenRecovers freeze the orchestration seam; the last
five are completed by Slice B when it wires startup and requests.

- [ ] **Step 5: Run GREEN and commit**

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(GatewayMigration2|Import|ValidateDirectDelta|ConflictCodec|Stash|RestoreStash|TransitionReceipt|ObserveGitBase|BranchSwitch|Discard)' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/projectstate internal/runtime/localstore/workspace_repo.go internal/runtime/localstore/workspace_repo_test.go internal/runtime/localstore/workspace_materialization_repo.go internal/runtime/localstore/workspace_materialization_repo_test.go internal/runtime/localstore/workspace_transition_repo.go internal/runtime/localstore/workspace_transition_repo_test.go
git commit -m "feat: import and observe portable workspace state"
~~~

### Hard gate before Task 5: trusted publication classification and review CAS

The required focused amendment is approved at
`docs/superpowers/specs/2026-08-01-publication-classification-review-cas-amendment.md`.
Its contracts override this plan wherever publication policy, origin identity, canonical
diff/review encoding, service status/diff types, checkpoint/recovery review proof, or
successor migration numbering is more precise. Task 5 remains implementation-blocked until
the amendment's causal slices 1-4—origin observation, migration-v3 policy/history, trusted
reconfiguration/resolution, and canonical status/diff review—are implemented, independently
reviewed, and committed.

In particular, the service—not the caller—resolves exactly `unclassified`, `local_only`,
`public_git`, or `private_git`; a public fork may be explicitly `public_git`; classification
is independent of fork/Fabric/actor hints; configured origin/repository changes stickily
invalidate at revision+1; and review digests bind exact workspace, semantic origin, policy
revision, independently observed Git base, candidate, canonical per-field-attributed diff,
and overlay generation. Public checkpoint requires the exact digest from either human or
accountable agent. With a current policy binding, missing/stale/mismatched acknowledgement
returns a zero result with no durable domain-row or project/stage/backup mutation. A stable
binding mismatch first commits only the amendment's exact policy/history invalidation pair,
then blocks checkpoint without any other mutation. The amendment's recovery three-way
matrix preserves later Git acceptance and never advances the accepted base itself.

### Task 5: Checkpoint CAS, Linux exchange, durable fallback, and recovery

**Files:**
- Create: internal/runtime/localstore/migrations/000004_checkpoint_publication_review.sql
- Create: internal/runtime/projectstate/checkpoint.go
- Create: internal/runtime/projectstate/checkpoint_linux.go
- Create: internal/runtime/projectstate/checkpoint_darwin.go
- Create: internal/runtime/projectstate/checkpoint_fallback.go
- Create: internal/runtime/projectstate/checkpoint_test.go
- Modify: internal/runtime/projectstate/service.go
- Modify: internal/runtime/localstore/workspace_repo.go
- Modify: internal/runtime/localstore/workspace_repo_test.go
- Modify: internal/runtime/localstore/workspace_materialization_repo.go
- Modify: internal/runtime/localstore/workspace_materialization_repo_test.go
- Modify: internal/runtime/localstore/migrations.go
- Modify: internal/runtime/localstore/migrations_test.go

**Interfaces:**
- Consumes: Task 3 Compose and Task 4 conflicts.
- Produces: Checkpoint and Recover.

~~~go
type CheckpointRequest struct {
    Scope types.WorkspaceScope
    Root string
    ExpectedWorkingTreeDigest projectstate.Digest
    PublicationReviewDigest *projectstate.Digest
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
declares no alias or alternate sentinel. It declares
`ErrCheckpointPendingAcceptance` for the exact zero-mutation case where this workspace
already has one `published` or `recovered_new` journal awaiting Git acceptance.

Checkpoint algorithm:

1. Acquire the per-workspace in-process mutex and begin the first `BEGIN IMMEDIATE`
   journal-preparation transaction. Strict-load the workspace's acceptance-eligible
   journal set; if one `published` or `recovered_new` row exists, return
   `CheckpointResult{}` plus `ErrCheckpointPendingAcceptance` before filesystem staging
   or database mutation. More than one is corruption. A checkpoint never supersedes an
   unaccepted journal.
2. Validate scope/root/actor with `Actor.ValidateLocalAction`; perform the amendment's
   outside/inside complete Git-base plus semantic-origin observation; require it stable and
   byte-exactly equal to the accepted binding; privately resolve the trusted publication
   classification; process only the amendment's exact sticky policy/history invalidation
   outcome; reject unclassified; read the allowed live tree; require digest equals
   ExpectedWorkingTreeDigest; compute the exact semantic diff/publication-review digest;
   and, for `public_git`, require exact equality with PublicationReviewDigest before
   staging or mutation. Caller data never selects classification.
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
6. Apply migration `000004_checkpoint_publication_review.sql` and persist the prepared
   journal containing complete canonical prior/candidate trees,
   accepted_base_digest, bound checkout_path/device/inode, exact candidate digest, and
   through-generation. Its required canonical `CheckpointOperationsV1` envelope records
   the selected initial boundary and, for every included row, exact generation, operation
   ID, canonical operation bytes including the final LF encoded as a JSON string, and
   prepublication `active` or `rebased` state. Strict decoding reconstructs those bytes
   and requires exact equality with both `CanonicalOperation(decoded)` and the database
   row; store the envelope in `included_operations_json`. For `public_git` the
   same prepared record/receipt also stores the amendment's strict canonical
   `publication_review_proof_version=1` and non-null `publication_review_json`, including
   the exact acknowledging actor and matching publication-review envelope/digest. Every
   new local/private journal stores the same version-1 review proof and checkpoint actor
   without requiring a caller acknowledgement. Version 0/null exists only as the
   amendment's explicitly blocked pre-v4 proof state. Both
   `expected_live_digest` and
   `prior_tree_digest` must equal the recomputed digest of the complete canonical prior
   tree. Commit the first transaction before filesystem publication on the
   Task-2 WAL connection whose mandatory `synchronous=FULL` policy durably syncs the
   journal/WAL. Do not hand-fsync the main database file.
7. After that commit and before any live-tree rename/exchange, acquire a second dedicated
   connection and `BEGIN IMMEDIATE`. Reload the prepared journal and exact workspace,
   then recheck the live working-tree digest, binding accepted digest and checkout
   identity, `expected_live_digest == prior_tree_digest`, selected candidate bytes/digest, composed through-generation, the strict
   canonical included-operation envelope and every exact listed row/state, absence of
   omitted or unexpected included rows or changed later generations, and
   `tx.HasOpenConflicts(ctx)`. Any mismatch rolls back with no publication; an open
   conflict returns `CheckpointResult{}` plus `localstore.ErrWorkspaceConflicted`. It
   repeats the complete Git-base/origin observation, privately re-resolves classification,
   and recomputes/rechecks the exact publication review envelope and persisted proof before
   any publication. Git-base/materialisation/branch-action mismatch rolls back with policy
   and prepared evidence untouched. Only after those preconditions succeed may a stable
   origin-policy mismatch commit the policy/history invalidation while retaining the
   prepared journal and every project/filesystem byte, then return unclassified; unknown
   policy-commit outcome uses the amendment's exact confirmation.
8. Hold that second immediate transaction across filesystem publication and the complete
   database finalization. Linux exchanges live `.wormhole` and stage with `renameat2`
   `RENAME_EXCHANGE`, renames the old tree to backup, and fsyncs the parent. Darwin uses
   `renameatx_np` `RENAME_SWAP` when supported. Fallback renames live to backup, fsyncs
   parent, renames stage to live, and fsyncs parent. Before committing, mark the journal
   published, persist `candidate_tree` as the workspace candidate, transition exactly
   the envelope-listed operations to `materialized`, and leave verified later operations active.
   A failure rolls back database finalization while the durable prepared row remains the
   recovery authority.
9. Recover reads one complete `MaterializationDisposition` in its immediate transaction
   and follows the amendment's three-way Git-base recovery matrix.
   A prepared journal intentionally blocks a stable ownership proof and drives recovery;
   after either recovery transition, Recover rereads and proves the complete disposition
   before returning or matching a recovered-new acceptance-eligible no-op. It handles a
   prepared or published journal with either the old or new live tree.
   Before examining or renaming a path, it double-observes exact root, checkout,
   semantic-origin, and committed Git base. Exact stored base permits normal recovery; an
   exact later committed journal candidate permits only recovered-new finalisation while
   leaving accepted-base advance to the following Refresh/Observe call; any other Git-base
   mismatch returns ErrCheckpointRecoveryPrecondition with policy and evidence untouched.
   In either proven base case, a stable origin mismatch may commit only the sticky
   policy/history invalidation and cannot strand bytes already proven live/committed; the
   stored version-1 review proof remains historical evidence, not current authorization. With matching
   preconditions it requires `expected_live_digest == prior_tree_digest`, compares
   live/stage/backup against both recorded digests, and deterministically restores the old
   complete tree or finishes the already-published new complete tree and matching database
   state. Unknown digest returns ErrCheckpointRecoveryBlocked without deleting evidence.
   Both recovery directions strict-decode the envelope and validate all and only its exact
   persisted operation membership rather than inferring a generation range. Recover-old
   restores each listed row to its recorded prepublication `active`/`rebased` state before
   marking the journal terminal `recovered_old`; no orphan materialized row remains.
   Recover-new retains exactly the listed materialized rows as historical and marks the
   journal terminal `recovered_new`, which remains Git-acceptance-eligible after restart.
   Later same-symbolic-ref Reject/Refresh acceptance leaves materialized rows historical,
   deletes the now-accepted candidate, preserves later active rows, and marks the
   `published` or `recovered_new` journal `accepted`. It sets binding status to `pending`
   iff at least one later active row remains, otherwise to `clean`; stashed and
   materialized history do not make it pending. Publication alone never advances the
   accepted base.
10. ErrCheckpointUnsupported occurs before step 4 only when regular files, directory
    fsync, same-filesystem rename, or owner-only staging cannot be guaranteed. Accepted
    base stays unchanged until Task 4 performs a same-symbolic-ref Reject/Refresh
    observation that accepts the matching Git commit.

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
`TestCheckpointRejectsUnequalExpectedAndPriorDigest` corrupts that invariant before the
second publication transaction and in each recovery direction; every case fails closed
with byte-identical filesystem, journal, candidate, and operation evidence.
`TestCheckpointRejectsOpenConflictPreservesEvidence` creates the Task-4 conflict surface,
asserts `CheckpointResult{}` and
`errors.Is(err, localstore.ErrWorkspaceConflicted)` before stage/journal/publication
mutation, compares complete direct/ours/conflict/row bytes across reopen, then calls
Status separately and proves the same CandidateDigest and OverlayGeneration. Add
exact-scope subtests proving resolved-only and another project/workspace do not block.
`TestCheckpointOperationEnvelopePreservesCanonicalBytes` requires each canonical
operation payload to end in LF, proves the outer envelope represents that LF as `\n`,
strict-decodes back to byte-identical operation JSON, and rejects missing-LF or otherwise
noncanonical operation strings before operation-membership validation or recovery.

Add `TestCheckpointConflictAfterPreparedCommitPublishesNothing`: a deterministic hook
runs after the prepared journal commits but before the publication connection begins,
opens an exact-workspace conflict through the normal immediate mutation seam, and resumes
checkpoint. The second immediate recheck must return `CheckpointResult{}` plus
`localstore.ErrWorkspaceConflicted`; live-tree bytes/digest, candidate, operation states,
and publication state remain unchanged, and rename/exchange/fsync publisher counters are
zero. Reopen and Recover must recognize the old live tree and prepared evidence without
publishing the staged candidate. The downstream multi-Fabric Task 2/6 tests consume the
same gate and sentinel.

Add `TestCheckpointRejectsSecondPendingAcceptanceBeforeStaging`: publish checkpoint A,
append a later active generation, attempt checkpoint B, require
`ErrCheckpointPendingAcceptance`, zero publisher calls, and byte-identical journal,
candidate, operation, and working-tree state across reopen. After Git accepts A, prove
the later active generation remains and a new checkpoint can proceed.

Run: go test ./internal/runtime/projectstate -run 'TestCheckpoint(CAS|LinuxExchange|Fallback|Recover|RecoverRejectsChangedCheckout|RecoverRejectsChangedAcceptedBase|DoesNotAdvanceBase|PreservesLaterOverlay|RejectsOpenConflictPreservesEvidence|ConflictAfterPreparedCommitPublishesNothing|RejectsSecondPendingAcceptanceBeforeStaging|OperationEnvelopePreservesCanonicalBytes)' -count=1
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

Every method verifies the exact binding and scopes every query by project and workspace. Prepare owns one immediate transaction: structurally eligible tracked/untracked preparations require non-empty validated compatible StateJSON and atomically upsert `integration_manifest_project_state` plus the migration row; `ignored_unsafe` records no state JSON. Transition is a compare-and-swap on source digest and prior outcome. Pending lookup uses the unique partial index. Latest terminal lookup orders `updated_at DESC, source_digest DESC`, so historical rows are never selected nondeterministically.

`ErrLegacyStateRetained` is the general checkpoint blocker for any exact legacy file still present. `ErrTrackedLegacyState` wraps it for the tracked-source result, so `errors.Is` matches both. The owner-only XDG backup root is an injected trusted Service dependency; no public request supplies or overrides it.

Exact behavior:

- Read exact .wormhole/integration-state.json with descriptor-relative no-follow, single-link regular-file checks; reject credential-shaped keys and project mismatch.
- Query tracked status read-only with git ls-files --error-unmatch -- .wormhole/integration-state.json using GIT_OPTIONAL_LOCKS=0. Never run git add, rm, update-index, checkout, commit, or clean.
- A structurally eligible tracked source atomically writes the compatible private integration state and `migrated_tracked_source_retained` row before returning ErrTrackedLegacyState. A restart test reopens the Store and proves both records committed together while source bytes and Git index remain unchanged.
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

### Task 6A: Implement workspace activity, finite retention, and explicit promotion

This is a mandatory sequential implementation task after Task 6 and before Task 7. It
owns Gateway migration `000005_workspace_activity.sql` and the local operational seam;
multi-Fabric migrations `000006`/`000007` may consume version 5 only after this task's
reviewed commit lands. The optional Code Graph branch remains later at `000008`.

**Required approved artifact:**
`docs/superpowers/plans/2026-08-01-workspace-activity-retention-promotion-implementation-plan.md`.
That focused plan must be written, independently reviewed, and explicitly approved before
this task's RED step. It must declare the exact files and APIs, including ownership of:

- Create: `internal/runtime/localstore/migrations/000005_workspace_activity.sql`
- Modify: `internal/runtime/localstore/migrations.go`
- Modify: `internal/runtime/localstore/migrations_test.go`
- Create/modify: the strict stdlib-only `ActivityV1`/effective-policy types selected by
  the approved artifact
- Create/modify: localstore activity, retention, terminal/pruning, and promotion-receipt
  repositories selected by the approved artifact
- Create/modify: the ProjectState-owned promotion service and causal tests selected by
  the approved artifact

**Produces:** one reviewed commit that advances the sole
`GatewaySchemaVersion` from 4 to 5, installs the strict workspace-scoped ActivityV1 store
and finite policy, implements atomic terminal/pruning transactions, and implements the
single explicit activity-to-EventV1 promotion boundary. The commit must include causal
RED evidence, focused GREEN, restart/isolation/race/fault tests, migration rollback and
fresh/v4→v5 upgrade proof, `git diff --check`, and `make check` at the approved coverage
floor. The SDD ledger records the artifact approval, base/head SHAs, test evidence, and
fresh review approval. No placeholder schema or partial v5 ledger advance may land.

#### Design approval gate

Before this task's RED step, the focused approved artifact must freeze
`000005_workspace_activity.sql`, `ActivityV1`, exact repository/service interfaces, the
effective-policy handshake, terminal/pruning transactions, and public schemas. The gate
must prove all of the following before Task 7 becomes executable:

- task definition, owner, and portable task status use the version-one Snapshot and
  `OperationV1`; transition notifications/history, progress, generic channel activity,
  presence, runtime attribution, subscriptions, telemetry, and uncurated discoveries do
  not;
- presence is restart-discardable; ordinary activity is eligible when older than 30 days
  OR outside the newest 10,000 unprotected workspace rows and is pruned deterministically
  by `(created_at, activity_id)` ascending; pending queues/conflicts/recovery/receipts are
  excluded until terminal, then default to exactly 30 days with only finite configured
  longer durations, so protected rows may make the cap exceed;
- Gateway/Fabric expose an effective finite policy before live activity is accepted;
- generic task/channel activity cannot construct `EventV1`; promotion accepts only a
  complete promotable-event projection and uses one ProjectState service-owned immediate
  transaction to strict-read the canonical source activity/digest. The stable event ID is
  promotion-owned; channel, source actor, type, payload, note, and creation time are exact
  deep-owned copies; `OperationV1.Actor` is the distinct promoter; caller-selected
  semantics, attribution, or extensions reject. The event's sole extension is
  `dev.wormhole.promotion`, with schema-version-1 data containing only
  `source_activity_id` and `source_activity_digest`; the transaction atomically records
  event/operation/promoter/source-digest proof without nesting `ApplyBatch`; and
- pruning/expiry cannot mutate the accepted base, candidate, overlay, checkpoint, or any
  portable operation.

The approved artifact and implementation must include causal RED/GREEN, restart,
isolation, cap/age, protected-row,
promotion replay/collision, actor attribution, rollback-at-every-write, and CLI/MCP parity
tests. Neither the current codec nor a legacy event table satisfies this gate. Task 7
must fail closed at its dependency check unless migration version 5 and the exact approved
ActivityV1/promotion interfaces are present.

- [ ] **Step 1: Write, review, and obtain explicit approval for the required focused artifact.**

Record its review and approval in the SDD ledger. Do not create migration `000005` before
that record exists.

- [ ] **Step 2: Run the artifact's causal RED suite.**

The RED must fail because migration version 5 and/or one named ActivityV1, retention,
terminal/pruning, or promotion boundary is absent—not because of an unrelated compile or
fixture defect.

- [ ] **Step 3: Implement only the approved v5 schema and seams, then run focused GREEN,
race, rollback, restart, isolation, and fault tests.**

- [ ] **Step 4: Run `make check`, obtain fresh implementation and security reviews, and
commit exactly the artifact-declared files.**

The commit message is `feat(localstore): add workspace activity`. Verify the exact commit
SHA and update the SDD ledger before Task 7 or any migration-6 consumer begins.

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
// Activity/promotion interfaces are supplied by the required gate amendment; they may
// not be inferred from these portable projection interfaces.
func (d *WorkspaceDomain) ListArticles(context.Context, types.WorkspaceBinding, kbListArgs) (localArticleListResult, error)
func (d *WorkspaceDomain) GetArticle(context.Context, types.WorkspaceBinding, kbGetArgs) (localArticleResult, error)
func (d *WorkspaceDomain) WriteArticle(context.Context, types.WorkspaceBinding, types.ActorEnvelope, kbWriteArgs) (localArticleWriteResult, error)
func (d *WorkspaceDomain) LinkCommit(context.Context, types.WorkspaceBinding, types.ActorEnvelope, gitLinkCommitArgs) (localGitLinkResult, error)
~~~

~~~go
type localTaskStatusResult struct {
    TaskID string `json:"task_id"`
    Status string `json:"status"`
    ActivityID string `json:"activity_id"`
}
~~~

Projection calls Compose and never reads the legacy tasks, kb_articles, channels,
durable_events, git_links, or replica repository APIs. It is not separately persisted,
so it is rebuildable exactly from accepted_snapshot, candidate, and operations after
restart and cannot drift transactionally. Reads return sorted live portable records from
the composed snapshot; tombstones are omitted. Every portable mutation validates binding
and actor.ValidateLocalAction, builds only canonical projectstate.OperationV1 values, and
persists through Apply/ApplyBatch. UUIDv4 IDs and UTC timestamps are generated inside the
domain. UpdateTaskStatus writes the updated `TaskV1` portable task state and, through the
gate-frozen atomic seam, appends operational transition activity; it never constructs
`EventV1`. Generic channel post/list use the gate-frozen activity seam and create zero
portable operations. Explicit promotion is the sole activity-to-`EventV1` path. Route
writes the final assigned `TaskV1` after scheduler selection. Supplied agent_id fields
never override the resolved actor. Existing legacy tables/repositories are migration/
read-only and no handler may dual-write or fall back to them.

The portable conversion inventory is task list/get/create/update_status/route, channel
list/create, KB list/get/write, and Git link_commit. The gate amendment owns channel
events/post, transition activity, subscription wake-up, retention, and explicit
promotion; those paths must use its ActivityV1 repositories/services rather than a
portable or legacy EventV1 write. `internal/runtime/localapi/localapi.go` removes direct
legacy TaskRepo, KBRepo, EventRepo, GitRepo, and sync-queue write authority. KB search
remains a binding-scoped Fabric call and is not a local durable bypass.

- [ ] **Step 1: Write RED projection, restart, mutation-visibility, and isolation tests**

For each portable mutation tool above, use canonical UUID literals and assert its typed
OperationV1 is present after restart; Diff reports it; Checkpoint contains it; Projection
returns it; another scope cannot see it; and legacy repositories remain untouched. The
gate amendment owns atomic task-status/activity and promotion tests. Also add
TestProjectionRebuildsAfterRestart, TestEveryLocalPillarHandlerUsesWorkspaceDomain, and
TestNoLegacyReplicaWriteBypass.

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
type WorkspaceCheckpointArgs struct {
    ExpectedWorkingTreeDigest projectstate.Digest `json:"expected_working_tree_digest"`
    PublicationReviewDigest *projectstate.Digest `json:"publication_review_digest,omitempty"`
}
type WorkspaceStashArgs struct { Label string `json:"label"` }
type WorkspaceStatusResult struct {
    State string `json:"state"`
    AcceptedRef string `json:"accepted_ref"`
    AcceptedCommitSHA string `json:"accepted_commit_sha"`
    AcceptedTreeDigest projectstate.Digest `json:"accepted_tree_digest"`
    CandidateTreeDigest projectstate.Digest `json:"candidate_tree_digest"`
    OverlayGeneration int64 `json:"overlay_generation"`
    PublicationClassification types.PublicationClassification `json:"publication_classification"`
    PublicationReviewDigest projectstate.Digest `json:"publication_review_digest"`
}
type WorkspaceFieldValueResult struct {
    Present bool `json:"present"`
    Value json.RawMessage `json:"value,omitempty"`
}
type WorkspaceFieldChangeResult struct {
    Path string `json:"path"`
    Before WorkspaceFieldValueResult `json:"before"`
    After WorkspaceFieldValueResult `json:"after"`
    Actor *types.ActorEnvelope `json:"actor"`
}
type WorkspaceChangeResult struct {
    Kind string `json:"record_kind"`
    ID string `json:"record_id"`
    ChangeKind runtimeprojectstate.ChangeKind `json:"change_kind"`
    BeforeDigest *projectstate.Digest `json:"before_digest"`
    AfterDigest *projectstate.Digest `json:"after_digest"`
    BeforeBodyDigest *projectstate.Digest `json:"before_body_digest"`
    AfterBodyDigest *projectstate.Digest `json:"after_body_digest"`
    Fields []WorkspaceFieldChangeResult `json:"fields"`
    Actor *types.ActorEnvelope `json:"actor"`
}
type WorkspaceDiffResult struct {
    AcceptedTreeDigest projectstate.Digest `json:"accepted_tree_digest"`
    CandidateTreeDigest projectstate.Digest `json:"candidate_tree_digest"`
    Changes []WorkspaceChangeResult `json:"changes"`
    OverlayGeneration int64 `json:"overlay_generation"`
    PublicationClassification types.PublicationClassification `json:"publication_classification"`
    PublicationReviewDigest projectstate.Digest `json:"publication_review_digest"`
}
func (d *WorkspaceDomain) Status(context.Context, types.WorkspaceBinding, WorkspaceStatusArgs) (WorkspaceStatusResult, error)
func (d *WorkspaceDomain) Diff(context.Context, types.WorkspaceBinding, WorkspaceDiffArgs) (WorkspaceDiffResult, error)
func (d *WorkspaceDomain) Import(context.Context, types.WorkspaceBinding, types.ActorEnvelope, WorkspaceImportArgs) (runtimeprojectstate.ImportResult, error)
func (d *WorkspaceDomain) Checkpoint(context.Context, types.WorkspaceBinding, types.ActorEnvelope, WorkspaceCheckpointArgs) (runtimeprojectstate.CheckpointResult, error)
func (d *WorkspaceDomain) Stash(context.Context, types.WorkspaceBinding, types.ActorEnvelope, WorkspaceStashArgs) (runtimeprojectstate.StashResult, error)
type workspaceGatewayCaller func(context.Context, string, any) (json.RawMessage, error)
func runWorkspaceCommand(context.Context, string, []string, io.Writer, io.Writer, workspaceGatewayCaller) int
~~~

Mutations call ValidateLocalAction. Every adapter validates the binding, requires the registered checkout path/device/inode to match, and copies only binding-derived scope/root plus operation-specific args. Runtime startup calls Recover before RefreshWorkspace for every RegisteredWorkspaces result. Runtime calls RefreshWorkspace before every later scoped operation except that Stash may proceed only after ErrBranchSwitchPending and must be followed immediately by successful RefreshWorkspace plus Recover on the refreshed scope. RestoreStash stays private.

Status and diff are explicit public projections. Their mappers deep-copy canonical raw
field values and actor envelopes, require non-nil ordered change/field slices, and expose
only the fields above. They never serialize the internal `WorkspaceStatus`,
`WorkspaceDiff`, `WorkspaceBinding`, checkout path/device/inode, accepted snapshot,
publication-policy actor/history, or origin preimage. Adapter tests marshal the results and
reject every machine-private field name and sentinel value.

Public MCP names remain wormhole.workspace.status, wormhole.workspace.diff,
wormhole.workspace.import, wormhole.workspace.checkpoint, and wormhole.workspace.stash
with routing-free schemas. Status/diff return the exact candidate and publication-review
digests. Approved CLI names are top-level only: wormhole status, wormhole diff, wormhole
import [--expected-working-tree-digest sha256:...], wormhole checkpoint
--expected-working-tree-digest sha256:... [--publication-review-digest sha256:...], and
wormhole stash --label <non-empty-label>. `public_git` callers must supply the exact
digest, including public forks; `local_only`/`private_git` callers do not select their
classification through this flag.
There is no wormhole workspace subcommand. No public argument accepts project_id,
workspace_id, cwd, root, actor, Fabric profile, classification, or credential; cwd appears
only in the private WorkspaceContext bridge.

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
7. Mandatory Task 6A lands the reviewed migration-v5 ActivityV1, finite-retention, and
   explicit-promotion implementation commit after its focused artifact is independently
   reviewed and explicitly approved.
8. Task 7 consumes those exact reviewed v5 interfaces and replaces local pillar replica
   reads/writes with a rebuildable composed projection plus portable-operation and
   operational-activity domain adapters.
9. Task 8 adds binding-aware workspace operations and the five top-level CLI parsers; the downstream runtime/setup seam registers routes and updates every public inventory surface atomically.

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

The Slice-A acceptance fixture also proves status, diff, and import do not advance Git;
checkpoint does not stage, commit, push, or advance the accepted base; unclassified and
`public_git`-without-matching-digest checkpoints publish nothing; generic task/
channel operational activity is absent from the candidate; checkpoint performs no
implicit promotion; and only an explicitly promoted, source-bound `EventV1` appears in a
second clean reconstruction after later Git acceptance. The later programme portable-loop
gate adds real clone/setup/native-connector/Git-review/second-clone coverage before the
branch can stop.

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
- Git observation: binding-free `TransitionReceiptByKey` and first-read
  `WithImmediateWorkspaceTransition` make exact Discard receipt retry precede every
  Git/current-binding check at both boundaries without changing Stash/Restore seams;
  strict disposition ownership/blocker proof follows the in-transaction Git recheck and
  precedes applicability; applicability then requires ref change plus exact nonterminal
  proposal state; only same-ref Reject/Refresh accepts matching materialization, while
  Discard never does and a nonmatch blocks same-ref rebase/discard without new state;
  trusted rebase preserves provenance or uses only the fixed system token/time; and
  unknown non-discard COMMIT never infers a result or binding.
- Projection/routing seam: portable pillar reads/writes use Projection/OperationV1;
  generic activity uses the gated ActivityV1 seam and only explicit promotion produces
  EventV1. Public args expose no project/workspace/cwd/root/actor/classification data;
  Slice A owns both workspace.go files, and runtime/setup later modifies and stages them
  with private binding resolution, startup/request refresh, live routes, and atomic alpha
  inventory changes. CLI names are the top-level status/diff/import/checkpoint/stash forms.
- Existing requirements remain covered: strict schemas/remotes/references/operation
  rows, explicit Compose start/generation, CandidateDigest/OverlayGeneration status,
  idempotent registration, correct isolation, conflict checkpoint/Fabric gates, durable
  fallback, tracked legacy index preservation, concrete UUID fixtures, exact SQL/API/
  error algorithms, and the 80% gate.
- No task grants merge authority to legacy replica tables, discovers provider IDs over network, stages/commits/pushes Git, upgrades actor assurance, or leaves a public contract outside alpha inventory tests.
