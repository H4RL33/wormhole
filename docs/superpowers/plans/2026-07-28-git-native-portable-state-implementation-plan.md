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
- Existing replica tables are read models only. Snapshot, imported candidate, and overlay operations are the only Compose inputs.
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

All project, actor, task, task-link, article, channel, event, and Git-link IDs are canonical lower-case UUID strings; operation, conflict, stash, journal, and workspace IDs are newly generated canonical lower-case UUIDs. Handle namespace/name components match ^[a-z0-9][a-z0-9_-]{0,62}$ and are display aliases, never keys. Commit IDs are lower-case 40- or 64-hex object IDs, and observed branch refs are empty for detached HEAD or match refs/heads/<non-empty-refname>.

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
- Events are immutable/add-only. Tombstones are allowed for actor, task, task_link, kb_article, channel, and git_link; never project or event.
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
~~~

Paths are sorted slash-relative to .wormhole. Digest input per file is uint64 big-endian path length, path bytes, uint64 big-endian data length, canonical data bytes; SHA-256 result is sha256:<lowerhex>. Encoding uses struct field order and recursively sorted map keys, LF, one trailing LF. Config/remotes TOML uses fixed field ordering and sorted fabric hints.

All operation content digests use the same `sha256:<lowerhex>` representation over one canonical byte sequence: record content hashes `CanonicalJSON` of the live typed record, KB body content hashes `CanonicalMarkdown` of the body independently, and a resurrection's expected tombstone hashes `CanonicalJSON` of the complete prior `TombstoneV1`. The tree-digest length-prefix framing is not used for these single-value digests. `ExpectedContentDigest`, `ExpectedBodyDigest`, and `ExpectedTombstoneDigest` must equal those exact values before mutation; tests assert fixed golden digests rather than recomputing expectations through the production helper.

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

Exactly one payload matches Kind. PutRecord contains exactly one typed pointer. ApplyOperation is stateless, clones its input, requires operation.ExpectedViewDigest to equal the input digest, calls operation.Actor.Validate, rejects legacy/unknown assurance for every new operation, rejects replacement of an existing unequal event, prevents project-ID change, computes TombstoneV1 from current canonical record/body bytes and the operation actor, requires resurrection to name the exact tombstone digest, validates the result, recomputes its digest, and returns it. Local Service.Apply/ApplyBatch and every local public domain mutation additionally call operation.Actor.ValidateLocalAction before persistence; later Fabric may accept public/private actors after its own authorization. Any error returns the original snapshot unchanged. CanonicalOperation uses CanonicalJSON. Runtime and Fabric must call this reducer rather than duplicate event/tombstone/resurrection logic.

Runtime composition:

~~~go
type ComposedView struct {
    Snapshot projectstate.Snapshot
    AppliedOperationIDs []string
    ThroughGeneration int64
}
func Compose(base projectstate.Snapshot, imported *projectstate.Snapshot, operations []StoredOperation) (ComposedView, error)
~~~

Compose starts from imported candidate when present, otherwise accepted base; operations apply in strictly increasing generation by calling projectstate.ApplyOperation for each row, and it never reads legacy replica tables. Fabric later persists/replays the exact same OperationV1 and encoded Snapshot values through ApplyOperation without importing internal/runtime/projectstate.

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
- Produces: all frozen cross-layer types, canonical schema types, codec functions, OperationV1, ApplyOperation, and errors ErrInvalidSnapshot, ErrUnknownVersion, ErrUnknownKind, ErrBrokenReference, ErrTrackedSecret, ErrInvalidActorEnvelope, ErrOperationPrecondition, ErrImmutableEvent, ErrTombstoneDigest, and ErrResurrectionDigest.

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

Shared RED cases are TestApplyOperationPutRecord, TestApplyOperationRejectsStaleDigest, TestApplyOperationRejectsUnequalEvent, TestApplyOperationCreatesExactTombstoneDigests, TestApplyOperationRejectsWrongTombstoneDigest, TestApplyOperationResurrectsMatchingTombstone, TestApplyOperationRejectsWrongResurrectionDigest, and TestApplyOperationErrorLeavesInputUnchanged.

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
- Produces: Store workspace methods and Service RegisterWorkspace, Status.

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
func (r *WorkspaceRepo) ResolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error)
func (r *WorkspaceRepo) RegisteredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error)
func (s *Service) RegisterWorkspace(ctx context.Context, req RegisterWorkspaceRequest) (RegisterWorkspaceResult, error)
func (s *Service) ResolveWorkingDirectory(ctx context.Context, observed types.WorkspaceContext) (types.WorkspaceBinding, error)
func (s *Service) RegisteredWorkspaces(ctx context.Context) ([]types.WorkspaceBinding, error)
func (s *Service) Status(ctx context.Context, scope types.WorkspaceScope) (WorkspaceStatus, error)
~~~

Registration resolves a non-symlink canonical root, captures device/inode, independently verifies HEAD equals ExpectedCommit, reads the committed .wormhole tree, validates project/repository equality, and creates a UUID. It returns types.WorkspaceBinding with AcceptedCommitSHA=req.ExpectedCommit and AcceptedTreeDigest=string(decoded.Digest). Repeating the same project, checkout identity, canonical path, repository, commit, and digest returns the identical WorkspaceBinding with Created=false and performs no write. Same checkout identity with another project/repository returns ErrCheckoutCollision. Same project in another worktree or clone gets a distinct ID.

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
  outcome TEXT NOT NULL,
  detail TEXT NOT NULL,
  migrated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,source_digest),
  FOREIGN KEY(project_id,workspace_id) REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);
CREATE INDEX workspace_overlay_generation ON workspace_overlay_operations(project_id,workspace_id,generation);
CREATE INDEX workspace_open_conflicts ON workspace_conflicts(project_id,workspace_id,state);
CREATE INDEX workspace_recovery ON workspace_materializations(state,project_id,workspace_id);
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

Run: go test ./internal/runtime/localstore ./internal/runtime/projectstate -run 'Test(GatewayMigrationLedger|GatewayMigrationRollback|GatewayMigrationRejectsFutureVersion|WorkspaceScopeMismatch|ValidWorkspacesRemainIsolated|RegisterWorkspaceIdempotent|RegisterWorkspaceCheckoutCollision|TwoWorktreesDistinct|ResolveWorkingDirectory|RegisteredWorkspaces)' -count=1
Expected: FAIL because migration, repositories, and service are absent.

- [ ] **Step 2: Implement migration, repositories, safe tree reader, and registration**

Embed internal/runtime/localstore/migrations/*.sql and expose const GatewaySchemaVersion = 1 plus func applyGatewayMigrations(ctx context.Context, db *sql.DB) error. The function acquires one dedicated connection, executes BEGIN IMMEDIATE, creates and shape-checks gateway_schema_migrations, rejects a recorded version greater than GatewaySchemaVersion, applies each missing numbered file once, inserts its version row, and commits. Any DDL or ledger-write failure rolls back the entire version. This ledger name and API are the only Gateway SQLite migration mechanism; Slice B and Slice D append 000002 and later files rather than creating another ledger.

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
- Consumes: Task 1 OperationV1 and Task 2 persistence.
- Produces: Compose, Apply, SemanticDiff, ThreeWayRebase, and exact conflict types used by Import, stash restore, Git observation, and later Fabric.

~~~go
type ChangeKind string
const (
    ChangeAdd ChangeKind = "add"
    ChangeModify ChangeKind = "modify"
    ChangeTombstone ChangeKind = "tombstone"
    ChangeResurrect ChangeKind = "resurrect"
)
type FieldChange struct { Path string; Before, After json.RawMessage }
type Change struct {
    Key projectstate.RecordKey
    Kind ChangeKind
    BeforeDigest, AfterDigest *projectstate.Digest
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
    ConflictImmutableEvent ConflictKind = "immutable_event"
    ConflictTombstoneEdit ConflictKind = "tombstone_edit"
    ConflictTombstoneBody ConflictKind = "tombstone_body"
    ConflictInvalidResurrection ConflictKind = "invalid_resurrection"
)
type Conflict struct {
    ID string
    Key projectstate.RecordKey
    FieldPath string
    Kind ConflictKind
    Base, Ours, Theirs json.RawMessage
}
type MergeResult struct { Snapshot projectstate.Snapshot; Conflicts []Conflict }
func ThreeWayRebase(oldBase, newBase, candidate projectstate.Snapshot) (MergeResult, error)
func (s *Service) Apply(ctx context.Context, scope types.WorkspaceScope, operation projectstate.OperationV1) (WorkspaceStatus, error)
func (s *Service) ApplyBatch(ctx context.Context, scope types.WorkspaceScope, operations []projectstate.OperationV1) (WorkspaceStatus, error)
func (s *Service) Diff(ctx context.Context, scope types.WorkspaceScope) (Diff, error)
~~~

Compose starts from workspace_candidates.rebased_tree and state='active' operations newer than rebased_through_generation when present; otherwise it starts from direct_tree or accepted_snapshot and state='active' operations. That rule lets Task 4 mark absorbed rows state='rebased' without leaving their old whole-view ExpectedViewDigest preconditions active.

Apply delegates to ApplyBatch with one element. ApplyBatch rejects an empty batch, duplicate operation IDs, and every actor that fails ValidateLocalAction; under one BEGIN IMMEDIATE it composes the current view, applies operations in caller order through projectstate.ApplyOperation (each ExpectedViewDigest must chain to the prior result), allocates consecutive generations, and appends every row or none. This is the atomic path for task-status-plus-event and every other multi-record local mutation.

- [ ] **Step 1: Write RED operation, diff, and merge tests**

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

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(Apply|Compose|SemanticDiff|ThreeWayRebase)' -count=1
Expected: FAIL because composer/diff/merge are absent.

- [ ] **Step 2: Implement exact operation validation and Apply transaction**

Apply executes this transaction:

~~~text
BEGIN IMMEDIATE
binding = RequireWorkspace(project_id, workspace_id)
candidate, rebasedThrough = ReadCandidate(binding)
operations = ListOperationsAfter(rebasedThrough)
view = Compose(binding.acceptedSnapshot, candidate, operations)
require operation.ExpectedViewDigest == view.Snapshot.Digest
require operation.Actor.ValidateLocalAction()
nextSnapshot = projectstate.ApplyOperation(view.Snapshot, operation)
INSERT workspace_overlay_operations(nextGeneration, CanonicalJSON(operation))
UPDATE workspace_bindings SET status='pending'
COMMIT
~~~

OperationV1 has no issuer method; Apply requires operation.Actor.ValidateLocalAction before calling the shared reducer. Any scope, validation, canonicalization, duplicate operation ID, generation, precondition, or reducer error rolls back. The shared projectstate reducer owns exactly-one variant, event immutability, tombstone digest, resurrection digest, and final validation invariants.

- [ ] **Step 3: Implement diff and merge algorithms**

SemanticDiff orders project, actor, task, task_link, kb_article, channel, event, git_link, then UUID and field path. ThreeWayRebase accepts one-sided/equal changes; recursively merges disjoint typed JSON paths; uses normalized-LF deterministic old-base anchors for Markdown; and returns explicit conflicts for overlapping Markdown, same field, unequal immutable event, tombstone/edit, tombstone/body, and invalid resurrection. It never inserts conflict markers, compares timestamps for precedence, or returns a partial unvalidated snapshot.

- [ ] **Step 4: Run GREEN and commit**

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(Apply|Compose|SemanticDiff|ThreeWayRebase|Operation)' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/projectstate internal/runtime/localstore
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
func ValidateDirectDelta(prior, next projectstate.Snapshot, matchingMaterialization *DirectImportJournalMatch) error
func (s *Service) Import(ctx context.Context, req ImportRequest) (ImportResult, error)

type StashResult struct {
    StashID string
    SourceDigest projectstate.Digest
    CandidateDigest projectstate.Digest
    OperationCount int
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

- returns ErrDirectEventMutation for an existing event ID with unequal bytes;
- returns ErrDirectPathDeletion when any prior record path disappears instead of becoming a valid TombstoneV1;
- returns ErrTombstoneDigest when DeletedContentDigest is not the digest of the prior canonical record or DeletedBodyDigest is absent/wrong for KB;
- returns ErrDirectEditTombstone when an existing tombstone changes or a correct direct tombstone conflicts with an active overlay edit;
- returns ErrDirectResurrection when a tombstone becomes live;
- may bypass these direct-edit errors only when its digest and bytes equal the candidate_tree of a published materialization journal bound to the same accepted base and checkout. That exception is how a typed Resurrect operation becomes importable after checkpoint; callers cannot claim the exception.

Import returns the scope/checkout/working-tree/project/repository errors already frozen plus these direct-delta sentinels. No error replaces workspace_candidates or conflicts.

Import executes:

~~~text
liveTree = ReadWorkingTree(root)
liveSnapshot = DecodeTree(liveTree)
BEGIN IMMEDIATE
binding = RequireAndRevalidateCheckout(scope, root)
priorSurface = candidate.direct_tree if present else binding.accepted_snapshot
journalMatch = FindPublishedJournalByCandidateDigest(scope, liveSnapshot.Digest)
require journalMatch is nil or journalMatch.accepted_base_digest == binding.accepted_digest
require journalMatch is nil or journalMatch.checkout path/device/inode == binding checkout
ValidateDirectDelta(priorSurface, liveSnapshot, journalMatch.candidate_tree)
oldComposed = Compose(binding.accepted_snapshot, candidate, activeOperations)
merged = ThreeWayRebase(priorSurface, liveSnapshot, oldComposed.Snapshot)
DELETE existing open conflicts for this import generation
INSERT merged.Conflicts
UPSERT workspace_candidates(
  direct_tree=EncodeTree(liveSnapshot),
  rebased_tree=EncodeTree(merged.Snapshot),
  rebased_through_generation=oldComposed.ThroughGeneration)
UPDATE workspace_overlay_operations SET state='rebased'
  WHERE generation <= oldComposed.ThroughGeneration AND state='active'
UPDATE workspace_bindings SET status = conflicted when conflicts exist else pending
COMMIT
~~~

Operations through RebasedThroughGeneration remain immutable audit rows but Compose skips them. Later operations precondition against the new rebased_tree digest. A correct direct tombstone versus an active overlay edit persists ConflictTombstoneEdit and returns a conflicted ImportResult; no silent overwrite occurs.

Stash persists complete canonical bytes, not only digests:

~~~text
sourceSnapshot = imported direct snapshot if present else accepted snapshot
sourceTree = EncodeTree(sourceSnapshot)
composed = Compose(...)
INSERT workspace_stashes(source_tree=sourceTree,
  composed_tree=EncodeTree(composed.Snapshot),
  operations_json=CanonicalJSON(all operations through composed.ThroughGeneration),
  through_generation=composed.ThroughGeneration,
  source_base_digest=sourceTree digest,
  candidate_digest=composed.Snapshot.Digest,
  actor_json=CanonicalJSON(actor), label)
DELETE workspace_candidates
UPDATE workspace_overlay_operations SET state='stashed'
  WHERE generation <= composed.ThroughGeneration AND state IN ('active','rebased')
COMMIT
~~~

RestoreStash decodes and digest-checks source_tree/composed_tree, verifies operations_json exactly matches the existing state='stashed' rows through stash.through_generation and replays from source_tree to composed_tree, computes current = Compose(current base/candidate/active overlay), then ThreeWayRebase(stash source, current snapshot, stash composed). It updates those existing stashed rows and current active rows absorbed by the merge to state='rebased', persists the merge snapshot as rebased_tree with rebased_through_generation equal to the greatest absorbed generation, and stores conflicts atomically; it never reinserts operation UUIDs. On a clean merge it deletes the stash and returns StashRetained=false. On conflicts it retains the stash and returns StashRetained=true so resolution/retry cannot lose the source. A tree/replay/digest mismatch returns ErrStashCorrupt before mutation; missing or altered stashed operation rows return ErrStashOperationMismatch before mutation.

Git observation remains independent:

1. Validate binding/root/actor with ActorEnvelope.ValidateLocalAction.
2. Run read-only git with GIT_OPTIONAL_LOCKS=0 and hooks disabled: rev-parse HEAD^{commit}, symbolic-ref -q HEAD, ls-tree -rz --full-tree HEAD for canonical .wormhole paths, then cat-file --batch.
3. Require HEAD equals ExpectedCommit; DecodeTree; validate project/repository.
4. Re-read HEAD and checkout identity. Race returns ErrGitObservationChanged without DB writes.
5. BEGIN IMMEDIATE and CAS accepted_commit. Ref change with pending state and no matching materialized candidate returns ErrBranchSwitchPending unless explicit stash/discard.
6. Matching published materialization advances base only when its accepted_base_digest and checkout path/device/inode still equal the binding, marks journal accepted, and preserves operations newer than its through_generation. A bound-precondition mismatch returns ErrGitMaterializationPrecondition and retains the journal.
7. Nonmatching same-ref base uses ThreeWayRebase and persists conflicts. Candidate mismatch never retires a journal.

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

Add named cases TestImportRejectsUnequalImmutableEvent, TestImportRejectsPathDeletionWithoutTombstone, TestImportRejectsIncorrectTombstoneContentDigest, TestImportRejectsIncorrectTombstoneBodyDigest, TestImportPersistsOverlayTombstoneConflict, TestImportRejectsDirectResurrection, and TestImportAcceptsMatchingMaterializedResurrection.

Run: go test ./internal/runtime/projectstate -run 'Test(Import|ValidateDirectDelta)' -count=1
Expected: FAIL because Import is absent.

- [ ] **Step 2: Write RED complete-stash and restore tests**

Test restart byte equality for source_tree, composed_tree, and operations_json; clean restore onto changed base; conflicting restore retains stash/evidence; and restored operations do not fail stale whole-view preconditions.

Run: go test ./internal/runtime/projectstate ./internal/runtime/localstore -run 'Test(Stash|RestoreStash)' -count=1
Expected: FAIL because complete stash persistence/restore are absent.

- [ ] **Step 3: Write RED Git observation and branch tests**

Cover HEAD race, invalid committed tree, materialized candidate match/mismatch, base advance atomicity, same-ref semantic rebase, branch reject/stash/discard, and two-workspace isolation.

Run: go test ./internal/runtime/projectstate -run 'TestObserveGitBase|TestBranchSwitch|TestRefreshWorkspace' -count=1
Expected: FAIL because observer is absent.

- [ ] **Step 4: Implement the exact transactions and algorithms above**

Every filesystem read precedes BEGIN IMMEDIATE; every transaction revalidates binding state/digests before mutation. Import, Stash, RestoreStash, and any actor-attributed branch action call ValidateLocalAction before mutation, so legacy/unknown can never create new state. Encoding or merge failure rolls back. Stash/restore and import never delete evidence on a conflict. ObserveGitBase never accepts a caller-provided tree/ref. TestRefreshWorkspaceCallsObserveGitBase, TestRefreshBeforeStatus, TestRefreshBeforeWrite, TestRefreshBeforeCheckpoint, TestStartupRecoversThenRefreshesEveryRegisteredWorkspace, and TestStashAfterBranchPendingRefreshesThenRecovers freeze the orchestration seam; the last five are completed by Slice B when it wires startup and requests.

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

Checkpoint algorithm:

1. Acquire per-workspace in-process mutex and BEGIN IMMEDIATE journal preparation transaction.
2. Validate scope/root/actor; read allowed live tree; require digest equals ExpectedWorkingTreeDigest.
3. Import valid direct tree, Compose with active operations, reject open conflicts, EncodeTree complete candidate, validate DecodeTree round-trip.
4. Create owner-only sibling stage and backup paths on the same filesystem; write every file with fsync, fsync every created directory, and fsync parent.
5. Re-read live tree and compare digest. Mismatch returns ErrCheckpointCAS, preserves direct input/stage evidence, and performs no publication.
6. Persist prepared journal containing complete canonical prior/candidate trees, accepted_base_digest, bound checkout_path/device/inode, and generation; commit and fsync SQLite before filesystem mutation.
7. Linux exchanges live .wormhole and stage with renameat2 RENAME_EXCHANGE, renames old tree to backup, and fsyncs parent. Darwin uses renameatx_np RENAME_SWAP when supported.
8. Fallback never returns success after a partial sequence: rename live to backup, fsync parent, rename stage to live, fsync parent, then mark published. Each step is journal-state-derived and idempotent. Before examining or renaming any path, Recover requires the current binding accepted_digest to equal journal.accepted_base_digest and the canonical root/device/inode to equal the journal checkout preconditions. A mismatch returns ErrCheckpointRecoveryPrecondition and leaves the live tree, stage, backup, journal, candidate, and operations untouched. With matching preconditions, Recover examines live/stage/backup digests and deterministically restores old complete tree or finishes new complete tree; unknown digest returns ErrCheckpointRecoveryBlocked without deleting evidence.
9. ErrCheckpointUnsupported occurs before step 4 only when regular files, directory fsync, same-filesystem rename, or owner-only staging cannot be guaranteed.
10. Mark published after publication, persist candidate_tree as workspace_candidates direct/rebased tree, mark included operations state='materialized', and leave newer operations active. Accepted base stays unchanged until Task 4 observes matching Git commit.

- [ ] **Step 1: Write RED fault-injection matrix**

Create table tests injecting failure after stage fsync, prepared journal, live-to-backup rename, stage-to-live rename, directory fsync, exchange, and published-row update. Each restart Recover must yield byte-exact old or candidate tree, preserve later overlay generations, and retain evidence on unknown digest. TestCheckpointRecoverRejectsChangedCheckout replaces the bound directory inode; TestCheckpointRecoverRejectsChangedAcceptedBase advances the binding base. Both assert ErrCheckpointRecoveryPrecondition and byte-identical journal/stage/backup evidence.

Run: go test ./internal/runtime/projectstate -run 'TestCheckpoint(CAS|LinuxExchange|Fallback|Recover|RecoverRejectsChangedCheckout|RecoverRejectsChangedAcceptedBase|DoesNotAdvanceBase|PreservesLaterOverlay)' -count=1
Expected: FAIL because checkpoint implementation is absent.

- [ ] **Step 2: Implement platform publishers and journal state machine**

Reuse the descriptor-relative/no-follow discipline from localapi materialization but define projectstate-local primitives for a complete .wormhole directory. Do not import localapi. Use exact prepared/published/accepted/recovered_old/recovered_new states from Task 2 DDL.

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
- Modify: .gitignore

**Interfaces:**
- Consumes: Task 2 migration audit and existing private integration_manifest tables.
- Produces: MigrateLegacyIntegrationState and ErrTrackedLegacyState.

~~~go
type LegacyMigrationOutcome string
const (
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

Exact behavior:

- Read exact .wormhole/integration-state.json with descriptor-relative no-follow, single-link regular-file checks; reject credential-shaped keys and project mismatch.
- Query tracked status read-only with git ls-files --error-unmatch -- .wormhole/integration-state.json using GIT_OPTIONAL_LOCKS=0. Never run git add, rm, update-index, checkout, commit, or clean.
- Persist safe durable fields into existing private integration_manifest_project_state plus source digest/outcome audit in one transaction.
- If untracked, move the file to owner-only XDG workspace backup after durable import and fsync both directories; .gitignore prevents recurrence.
- If tracked, leave bytes and index untouched, record migrated_tracked_source_retained, and return ErrTrackedLegacyState from Checkpoint until the human removes the tracked path through normal Git workflow. Canonical snapshot digest excludes this exact legacy path, but checkpoint never silently deletes or carries it into a staged canonical tree.
- Unsafe/malformed input remains untouched, records ignored_unsafe without sensitive detail, and returns a path-specific error.
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

Run: go test ./internal/runtime/projectstate ./internal/runtime/localapi -run 'Test(TrackedLegacy|UntrackedLegacy|UnsafeLegacy|IntegrationMaterializationDoesNotWriteLegacyState)' -count=1
Expected: FAIL because migration and writer removal are absent.

- [ ] **Step 2: Implement exact migration and writer removal**

Add only .wormhole/integration-state.json to .gitignore. Do not introduce .wormhole/local. Preserve existing private integration tables and current compatible IDs.

- [ ] **Step 3: Run GREEN and commit**

Run: go test ./internal/runtime/projectstate ./internal/runtime/localapi -run 'Test(TrackedLegacy|UntrackedLegacy|UnsafeLegacy|Integration)' -count=1
Expected: PASS.

~~~bash
git add internal/runtime/projectstate internal/runtime/localapi .gitignore
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
3. Task 3 introduces typed local mutations plus the semantic diff/merge primitives required before import.
4. Task 4 validates direct deltas, rebases overlays onto imported candidates, persists/restores complete stashes, makes Git the only base-advance authority, and guards branch switches.
5. Task 5 materializes a candidate with crash recovery while leaving base unchanged.
6. Task 6 removes the tracked/private legacy leak without touching the Git index.
7. Task 7 replaces local pillar replica reads/writes with a rebuildable composed projection and typed-operation domain adapters.
8. Task 8 adds binding-aware workspace operations and the five top-level CLI parsers; the downstream runtime/setup seam registers routes and updates every public inventory surface atomically.

The runtime plan consumes WorkspaceContext, WorkspaceScope, WorkspaceBinding, RegisterWorkspaceResult, ResolveWorkingDirectory, RegisteredWorkspaces, RefreshWorkspace, registration, Status, Recover, observation results, Projection, and the single workspace.go domain; its private resolver returns the exact shared WorkspaceBinding and it exclusively owns bridge transport, Gateway wiring, and MCP registration. The setup/runtime plan modifies and stages the Slice-A-owned cmd/wormhole/workspace.go parser when it makes routes live. Slice D/Fabric consumes the exact shared internal/types ActorEnvelope, WorkspaceBinding, and RepositoryIdentity plus internal/types/projectstate Digest, DecodeTree(Tree), EncodeTree(Snapshot), Validate(Snapshot), DigestTree(Tree), OperationV1, and ApplyOperation(Snapshot, OperationV1); it does not import runtime types or redefine canonical records/reducer logic. Slice E validates/creates public/private ActorEnvelope values through Validate; local issuance/writes use ValidateLocalAction, historical imports use ValidateHistorical, and neither rewrites legacy/unknown assurance.

## Self-review

- Type reconciliation: one final internal/types ActorEnvelope has PrincipalID, Validate, ValidateLocalAction, and ValidateHistorical; new writes reject legacy/unknown, all non-legacy agent assurances require accountability and session/harness provenance, and Fabric consumes the same type without importing runtime.
- Import/rebase: strict direct-delta errors cover immutable events, raw deletion, tombstone digests, tombstone/edit, and direct resurrection; matching journals are the only exception; active overlays are semantically rebased and absorbed before later operation preconditions are evaluated.
- Durability: stashes retain canonical source/composed Tree bytes and operations with exact restore semantics; journals bind accepted-base digest and checkout identity; recovery precondition failures retain all evidence; gateway_schema_migrations is the one extensible local ledger.
- Projection/routing seam: all current local pillar reads/writes use Projection/OperationV1 with no legacy-table bypass; public args expose no project/workspace/cwd/root/actor data; Slice A owns both workspace.go files, and runtime/setup later modifies and stages them with private binding resolution, startup/request refresh, live routes, and atomic alpha inventory changes. CLI names are the top-level status/diff/import/checkpoint/stash forms only.
- Existing requirements remain covered: strict schemas/remotes/references, idempotent registration, correct isolation, durable fallback, tracked legacy index preservation, concrete UUID fixtures, exact SQL/API/error algorithms, and the 80% gate.
- No task grants merge authority to legacy replica tables, discovers provider IDs over network, stages/commits/pushes Git, upgrades actor assurance, or leaves a public contract outside alpha inventory tests.
