# Trusted Publication Classification and Review-CAS Amendment

> **Task-5 V1 execution amendment (2026-08-11):** the publication-policy,
> origin-observation, review-CAS, migration, and public/private classification contracts in
> this document remain binding. The private Task-5 publisher/recovery mechanism and platform
> detail is superseded by
> `2026-08-11-task5-fallback-checkpoint-recovery-simplification-design.md`: one Linux/WSL
> no-replace fallback path, no exchange/Darwin runtime path or private receipt, one recovery
> writer transaction, known-state convergence, and byte-preserving block on ambiguous
> topology. Conflicting mechanism-level text below is historical and non-executable.

**Date:** 2026-08-01
**Status:** Approved architecture amendment for the Git-native portable-state branch
**Amends:**

- `2026-07-28-git-native-wormhole-architecture-design.md` §§8.3, 11.1, and 17
- `2026-07-28-git-native-portable-state-implementation-plan.md` hard gate before Task 5
- `2026-07-28-git-native-wormhole-programme-plan.md` migration ownership
- `2026-07-28-multifabric-identity-trial-implementation-plan.md` migration ownership
- `2026-07-28-gateway-setup-codegraph-implementation-plan.md` setup order and migration ownership
- `docs/implementation-rules.md` portable-state publication and migration rules

## 1. Decision and scope

Task 5 may not implement checkpoint publication until this amendment's machine-private
publication policy, origin observation, canonical semantic-diff digest, and publication
review compare-and-swap are implemented and reviewed.

This amendment resolves four gaps in the parent design:

1. a classification round trip such as `public_git -> private_git -> public_git` needs a
   monotonic policy revision or an old review digest can become valid again;
2. the tracked canonical repository identity is shared by forks and therefore cannot
   represent the checkout's observed `origin`;
3. the in-memory `Diff` type is not a stable canonical wire codec and must not be hashed
   directly; and
4. Gateway migration `000003` was already reserved for later activity storage even
   though publication policy is a prerequisite for Task 5.

The chosen sequence is:

1. `000003_workspace_publication.sql` — trusted classification and origin binding;
2. `000004_checkpoint_publication_review.sql` — Task-5 durable checkpoint-review proof;
3. `000005_workspace_activity.sql` — mandatory Task 6A activity/retention/promotion;
4. `000006` and `000007` — the later multi-Fabric routing/synchronisation migrations;
5. `000008` — the later, separately gated model-free Code Graph migration.

Existing migration files remain immutable. Publication policy and review state are local
Gateway data and never enter tracked `.wormhole/` state.

## 2. Authority boundaries

The service resolves one of exactly:

```go
type PublicationClassification string

const (
    PublicationUnclassified PublicationClassification = "unclassified"
    PublicationLocalOnly    PublicationClassification = "local_only"
    PublicationPublicGit    PublicationClassification = "public_git"
    PublicationPrivateGit   PublicationClassification = "private_git"
)
```

`PublicationClassification.Validate` accepts only those four byte-exact values.

Classification is explicit local user policy. It is independent of:

- whether the checkout is canonical or a fork;
- any Fabric mode, connection, hint, membership, or credential;
- actor kind or assurance;
- checkpoint, CLI, MCP, or transport arguments; and
- repository content copied from another checkout.

A public fork is configured as `public_git`. Canonical/fork relation is routing evidence,
not publication authority. No caller-facing status, diff, or checkpoint request contains a
classification override. Setup/reconfiguration is the only API that supplies a desired
classification, and the service persists that explicit policy before later operations can
resolve it.

`unclassified` permits status and diff but blocks checkpoint. `public_git` requires an
exact current review-digest acknowledgement. `local_only` and `private_git` do not require
an acknowledgement, but if a caller supplies one it must match rather than being ignored.
All checkpoint callers supply an `ActorEnvelope` that passes `ValidateLocalAction`;
human and accountable-agent actors have identical checkpoint capability. The local
Gateway socket, database, and policy files remain owner-only. The checkout/Git trust
boundary separately includes every principal the filesystem permits to write them.

## 3. Canonical repository versus observed origin

`WorkspaceBinding.Repository` remains the immutable, tracked canonical project repository
identity. A fork retains that identity in `.wormhole/config.toml`; it is never overwritten
with the fork's remote.

Each operation independently observes the exact registered checkout's effective `origin`
without network access. The private observation preimage is the strict union:

```go
type observedOriginV1 struct {
    SchemaVersion int      `json:"schema_version"` // exactly 1
    Kind          string   `json:"kind"`           // missing, network, or filesystem
    Host          string   `json:"host,omitempty"`
    Port          string   `json:"port,omitempty"`
    Path          string   `json:"path,omitempty"`
    AbsolutePath  string   `json:"absolute_path,omitempty"`
}
```

The variants are exact:

- `missing` has no other fields;
- `network` has a non-empty canonical host and repository path, an optional canonical
  numeric non-default port, and no filesystem path; and
- `filesystem` has one clean absolute path and no network fields.

Observation uses the existing hardened read-only Git environment at the exact canonical
root and verified checkout device/inode. It invokes bounded `git remote get-url --all
origin`, performs no fetch, credential-helper request, provider API request, DNS lookup, or
other network operation, and ignores all non-`origin` remotes. The runner accepts at most
16 KiB of stdout including separators, at most eight URL entries, and at most 4,096 bytes
per entry before its terminal LF; CR, NUL, every other ASCII control byte, an empty entry,
or any overflow fails closed. Its separately bounded stderr is diagnostic-only and is
never returned. Zero URLs is `missing`; exactly one canonical result is accepted; multiple
distinct, malformed, control-bearing, or over-limit results are an error. `upstream` and
copied Fabric hints never qualify a fork for an upstream service. Push URLs are outside
this V1 observation because checkpoint does not push and Wormhole cannot constrain a later
direct Git push.

The fetch-origin digest is publication-policy binding and diagnostic evidence only. A
canonical-looking fetch URL is locally spoofable and may coexist with a fork push URL; it
is never sufficient Fabric eligibility, identification, authentication, authorization, or
upstream-contact evidence. The later Fabric slice must independently prove the repository
and permitted remote project/ref, and may add an explicit push-destination/provider proof.

HTTP, HTTPS, SSH, Git, and SCP-like network locators normalize to a transport-neutral
`host[:port]/path` identity. After removal of one terminal dot, a DNS host is at most 253
lower-case ASCII bytes and consists of dot-separated labels of 1..63 bytes matching
`[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?`; empty labels, underscores, leading/trailing
hyphens, and any other byte reject. Punycode is accepted only as those ASCII labels; raw
Unicode/IDN input is rejected. Canonical IPv4 is accepted. Bracketed IPv6 is accepted and
rendered in `net.IP.String` form; unbracketed ambiguous IPv6 is rejected.
Numeric ports have leading zeroes removed, remain in range 1..65535, and are omitted when
they equal the source scheme's default (`http=80`, `https=443`, `ssh=22`, `git=9418`).
SCP has no explicit default-port field. An absent or exact `git` SSH/SCP username is
accepted and omitted from identity; every other username is rejected in V1 because generic
SSH servers may route usernames to different repositories. Path processing first removes
all trailing slashes, then one case-sensitive terminal `.git`; the remaining
ASCII path must be non-empty and clean, and its case is preserved. All percent escapes,
query, fragment, HTTP(S) userinfo, credentials, controls, dot segments, backslashes, empty
components, and unsupported schemes fail without echoing the raw locator. Consequently
`ssh://git@github.com/Acme/Repo.git`, `git@github.com:Acme/Repo.git`, and
`https://github.com/Acme/Repo` identify the same repository.

Plain filesystem paths and `file:` URLs normalize to a lexical clean absolute path.
Relative paths resolve against the canonical checkout root; only an empty or `localhost`
file authority is accepted. Percent escapes, controls, NUL, and noncanonical separators
are rejected. The target need not exist and target symlinks are not followed, so
observation remains offline and deterministic. Multiple `--all` results are accepted only
when every raw entry normalizes to the same canonical preimage; otherwise the origin is
ambiguous. Git's specific status-2 no-such-remote exit with empty bounded output maps to `missing`;
every other non-zero exit is an observation error and raw stderr is not returned.

The stored origin value is only this digest:

```text
sha256("dev.wormhole.workspace-origin.v1\x00" || canonical_json(observedOriginV1))
```

Raw/effective remote URLs, usernames, and credentials are never persisted, logged, or
returned. The canonical/fork relation may compare the normalized tracked
`RepositoryIdentity.CanonicalRemote` locator with the normalized observed origin, but that
derived relation never selects classification. V1 never invents a provider immutable ID
from a URL and performs no provider discovery.

The runtime package exposes only the narrow machine-private setup inspection:

```go
func InspectPublicationOrigin(
    context.Context, string, // exact canonical checkout root
) (projectstate.Digest, error)
func DigestPublicationBindingConstraint(
    types.RepositoryIdentity, projectstate.Digest,
) (projectstate.Digest, error)
```

It performs the same before/after root and checkout-identity validation and bounded offline
observation, and returns only the domain-separated digest—never the preimage or URL. The
second function strict-validates repository/origin and hashes the exact
`dev.wormhole.setup-publication-binding.v1` preimage frozen by the setup plan. Both setup and
Gateway call that one codec. The service independently repeats the observation after
registration; setup inspection is an expected-state confirmation constraint, not
publication authority.

Origin is resolved as current exact state, not as continuous monitoring. A bootstrap row's
nil configured origin is a valid never-configured state: it resolves `unclassified`, its
review binds the current observed origin, and it is not treated as a change. The first
explicit configuration binds that origin and increments the revision. After configuration,
a stored policy whose repository or origin digest differs from the current exact binding is
atomically and stickily invalidated to `unclassified`, bound to the newly observed
repository/origin, and advanced by one policy revision before status, diff, checkpoint, or
recovery proceeds. Returning the origin to its earlier value does not reactivate the
earlier class or digest; explicit reconfiguration is required. Wormhole makes no claim
that it detects an unobserved transient edit changed and reverted entirely between
operations.

## 4. Gateway migration v3 and strict policy record

`000003_workspace_publication.sql` creates exactly one row for every workspace and
backfills every existing workspace as `unclassified`, revision 1, with no configured
origin or actor:

```sql
CREATE TABLE workspace_publication_policies (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  repository_identity_json TEXT NOT NULL,
  origin_digest TEXT,
  classification TEXT NOT NULL CHECK(classification IN (
    'unclassified','local_only','public_git','private_git'
  )),
  policy_revision INTEGER NOT NULL CHECK(policy_revision > 0),
  transition_kind TEXT NOT NULL CHECK(transition_kind IN (
    'bootstrap','configured','origin_invalidated','repository_invalidated'
  )),
  changed_actor_json TEXT,
  changed_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id),
  CHECK(
    (transition_kind='bootstrap' AND classification='unclassified' AND
      origin_digest IS NULL AND changed_actor_json IS NULL AND changed_at IS NULL) OR
    (transition_kind='configured' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NOT NULL AND changed_at IS NOT NULL) OR
    (transition_kind IN ('origin_invalidated','repository_invalidated') AND
      classification='unclassified' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NULL AND changed_at IS NOT NULL)
  ),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

CREATE TABLE workspace_publication_policy_history (
  project_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  policy_revision INTEGER NOT NULL CHECK(policy_revision > 0),
  repository_identity_json TEXT NOT NULL,
  origin_digest TEXT,
  classification TEXT NOT NULL CHECK(classification IN (
    'unclassified','local_only','public_git','private_git'
  )),
  transition_kind TEXT NOT NULL CHECK(transition_kind IN (
    'bootstrap','configured','origin_invalidated','repository_invalidated'
  )),
  changed_actor_json TEXT,
  changed_at TIMESTAMP,
  recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(project_id,workspace_id,policy_revision),
  CHECK(
    (transition_kind='bootstrap' AND classification='unclassified' AND
      origin_digest IS NULL AND changed_actor_json IS NULL AND changed_at IS NULL) OR
    (transition_kind='configured' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NOT NULL AND changed_at IS NOT NULL) OR
    (transition_kind IN ('origin_invalidated','repository_invalidated') AND
      classification='unclassified' AND origin_digest IS NOT NULL AND
      changed_actor_json IS NULL AND changed_at IS NOT NULL)
  ),
  FOREIGN KEY(project_id,workspace_id)
    REFERENCES workspace_bindings(project_id,workspace_id) ON DELETE CASCADE
);

INSERT INTO workspace_publication_policies
  (project_id,workspace_id,repository_identity_json,origin_digest,classification,
   policy_revision,transition_kind,changed_actor_json,changed_at)
SELECT project_id,workspace_id,repository_identity_json,NULL,'unclassified',1,
       'bootstrap',NULL,NULL
FROM workspace_bindings;

INSERT INTO workspace_publication_policy_history
  (project_id,workspace_id,policy_revision,repository_identity_json,origin_digest,
   classification,transition_kind,changed_actor_json,changed_at)
SELECT project_id,workspace_id,1,repository_identity_json,NULL,'unclassified',
       'bootstrap',NULL,NULL
FROM workspace_bindings;
```

After v3, a missing or duplicate current row, missing current-revision history row, history
gap/duplicate, or disagreement between current and its final history row is corruption,
not a synthetic default. New workspace registration inserts its unclassified policy and
revision-1 history row in the same immediate transaction as the binding. Strict readers
validate SQLite storage classes, exact scope, stable revision ordering, canonical
repository/actor JSON, classification, positive revision, transition kind, optional
digest, actor/timestamp pairing, UTC timestamps, and the complete transition-kind
invariants. Every `configured` row's actor must be `ActorHuman` and pass
`ValidateLocalAction`; an otherwise valid locally assured agent actor is corruption. They
do not silently repair, default, or reinterpret malformed state.

An explicitly configured `unclassified` row is valid: it retains the observed-origin
binding, local human actor, timestamp, and `configured` transition kind. Repeating it is a
current-exact no-op; any stale expected revision still fails CAS. Bootstrap and trusted
invalidation rows remain actorless under the SQL invariants above.

The localstore boundary is:

```go
type WorkspacePublicationPolicyRecord struct {
    Repository     types.RepositoryIdentity
    OriginDigest   *projectstate.Digest
    Classification types.PublicationClassification
    PolicyRevision int64
    TransitionKind string
    ChangedBy      *types.ActorEnvelope
    ChangedAt      *time.Time
}

type WorkspacePublicationPolicyTransition struct {
    Expected WorkspacePublicationPolicyRecord
    Next     WorkspacePublicationPolicyRecord
}

func (tx *WorkspaceMutationTx) PublicationPolicy(context.Context) (
    WorkspacePublicationPolicyRecord, error,
)
func (tx *WorkspaceMutationTx) PublicationPolicyHistory(context.Context) (
    []WorkspacePublicationPolicyRecord, error,
)
func (tx *WorkspaceMutationTx) ReconfigurePublication(
    context.Context,
    WorkspacePublicationPolicyTransition,
) (WorkspacePublicationPolicyRecord, error)
```

`ReconfigurePublication` uses complete-record CAS including exact canonical bytes and raw
timestamp/storage-class evidence, then strict-rereads the result. Trigger drift, stale
revision, overflow, scope mismatch, or binding drift rolls back. `policy_revision` is a
positive signed 64-bit integer, increments by exactly one for every classification,
semantic-origin, or future explicit repository-rebind transition, never wraps or resets,
and rejects a transition at `MaxInt64` within one intact Gateway-database lineage.
Restoring an older valid database snapshot can restore its earlier history and review
digests; such administrator rollback, like canonical local-database tampering, is outside
V1's threat model because there is no external monotonic anchor. V1 has no repository-rebind API; a row/binding
repository mismatch takes only the sticky `repository_invalidated` transition. An exact
same-repository/origin/classification no-op is allowed only when the current
`transition_kind` is already `configured`, the request's complete expected configuration
equals the current row, and its stored human principal equals the request actor's human
principal. A different human is an accountable reconfiguration and increments revision.
Bootstrap/invalidation -> explicit configuration always increments and records the human
actor, even when the effective class remains `unclassified`. A stale expected configuration always
returns `ErrPublicationConfigurationCAS`, even when its desired class now matches. The
caller makes setup idempotent by rereading and accepting a current exact desired state.
Public -> private -> public increments twice and cannot recreate the old review digest.
Every successful configured or trusted invalidation transition atomically inserts its
complete immutable next record into `workspace_publication_policy_history` and updates the
current row. It never rewrites/deletes history independently. The transition actor is
present only for an explicit human configuration; bootstrap and trusted service
invalidation retain exact typed reason/time without fabricating an actor.

The localstore transition boundary independently requires every configured
`Next.ChangedBy` to be `ActorHuman` and pass `ValidateLocalAction`; it never relies on the
service caller to enforce that invariant. Expected/current configured records pass the
same strict rule before CAS. An agent-actor configured row or transition returns a
corruption/validation error with no current/history write.

The service control-plane API is:

```go
type PublicationConfiguration struct {
    Classification         types.PublicationClassification // effective
    PolicyRevision         int64
    ObservedOriginDigest   projectstate.Digest
    ConfiguredOriginDigest *projectstate.Digest
    TransitionKind         string
    ChangedBy              *types.ActorEnvelope
    ChangedAt              *time.Time
}

type ReconfigurePublicationRequest struct {
    Scope                            types.WorkspaceScope
    ExpectedBinding                  types.WorkspaceBinding
    ExpectedPublicationBindingDigest projectstate.Digest
    Expected                         PublicationConfiguration
    Classification                   types.PublicationClassification
    Actor                            types.ActorEnvelope
}

func (s *Service) PublicationConfiguration(
    context.Context, types.WorkspaceScope,
) (PublicationConfiguration, error)
func (s *Service) ReconfigurePublication(
    context.Context, ReconfigurePublicationRequest,
) (PublicationConfiguration, error)

var ErrPublicationConfigurationInvalidated = errors.New(
    "projectstate: publication configuration invalidated",
)
```

The service observes origin once outside SQLite, enters the exact-workspace immediate
transaction, strict-loads the complete binding and policy, re-observes root/checkout/origin
before the first write, and requires the two canonical preimages and digests to match. A
race returns `ErrGitOriginChanged` with no policy mutation. Status, diff, checkpoint,
recovery, and reconfiguration use the same double-observation discipline. A stable
stored/current mismatch performs the sticky invalidation transition only after the
operation's complete Git-base/materialisation preconditions have succeeded; status/diff
then calculate from that new unclassified row, while checkpoint returns
`ErrPublicationUnclassified` without staging or journal mutation. The request's complete
binding and expected effective/configured values must match, and its actor must be
`ActorHuman` and pass `ValidateLocalAction`; a caller cannot substitute either origin digest. The service creates
`Next` from trusted binding/observation plus the single desired classification.
Reconfiguration is setup/control-plane state and is not exposed as an MCP project
operation. Fresh setup selects/creates its local human identity before it asks for and
persists classification, so there is no unattributed bootstrap configuration.

The service itself recomputes `ExpectedPublicationBindingDigest` with
`DigestPublicationBindingConstraint` from the strict-loaded binding and independently
observed origin. The field is CAS input, never authority. If it mismatches while the stored
configured policy is also stale against that observation, the sticky invalidation outcome
takes precedence and commits as below. If the policy is bootstrap/current and only the
caller's expected constraint is stale, reconfiguration returns
`ErrPublicationConfigurationCAS` with zero policy/history mutation. This ordering lets an
origin race fail closed without bypassing required invalidation.

The configuration DTO is a complete semantic CAS/readback projection: `ChangedBy` is
non-nil only for `configured`; `ChangedAt` follows the stored transition invariant; and
callers cannot omit any field from `Expected`. A current-exact configured no-op additionally
requires its stored human principal ID to equal `Request.Actor.HumanPrincipalID`; the
original actor envelope/time remain unchanged. If a different local human configures the
same class against the same binding/origin, that attribution change advances revision by
one and appends history with the new actor. Setup therefore converges to the selected human
without rewriting old accountability evidence.

`ObserveGitBase` and `RefreshWorkspace` retain Task 4's existing transaction and error
semantics. They validate and reconcile every Git-base, materialisation, branch-action,
discard-not-applicable, and observation-race condition before publication policy is
considered. `ErrBranchSwitchPending`, `ErrGitObservationChanged`, and every other failed
reconciliation roll back with no policy/history mutation. V1 defers origin-policy
invalidation from these two operations: after a successful reconciliation, the next
status, diff, checkpoint, recovery, or reconfiguration performs its own double observation
and commits the sticky invalidation if required. Therefore an origin mismatch can never
convert a Task-4 zero-mutation error into a policy-only write.

Sticky invalidation is a committed outcome, not an error returned from inside the
transaction callback. The callback writes only the next policy/history pair, strict-rereads
it, returns nil so the immediate transaction commits, and reports an internal
`policyInvalidated` outcome. The outer Status/Diff path continues from that row; the outer
Checkpoint path returns `ErrPublicationUnclassified`. The same rule applies if the second
checkpoint transaction discovers the stable mismatch after a prepared journal exists: it
first requires the complete Git base to remain the stored accepted base; a base,
materialisation, branch-action, or observation mismatch leaves policy and the prepared
journal untouched and returns the existing checkpoint precondition error. Only then may an
origin mismatch commit the policy/history transition while leaving every
prepared-journal/project/filesystem byte unchanged, after which checkpoint returns
unclassified. If either policy commit has an unknown outcome,
a fresh connection confirms the exact expected revision+1 current row and immutable history
row; exact match is success, exact absence retains the original unknown-commit error, and
any third state fails closed without retrying the write.

For reconfiguration, a stable stored/current repository or origin mismatch commits that
same invalidation transaction and the outer call returns the new unclassified complete
configuration plus `ErrPublicationConfigurationInvalidated`; it never attempts the desired
configured transition in the same call. A caller must display/reconfirm or reread and issue
a fresh expected-state request. Unknown-COMMIT confirmation that proves the invalidation
returns the same updated value/sentinel; absence or any third state returns the unknown/
corruption error. Tests cover this path independently from configured-transition unknown
commit.

## 5. Canonical semantic-diff digest

The public `Diff` Go struct is not a codec. The review calculation uses a private,
versioned projection with fixed field order and explicit empty/null behavior:

```go
type publicationSemanticDiffV1 struct {
    SchemaVersion       int                   `json:"schema_version"`
    Kind                string                `json:"kind"` // semantic_diff
    AcceptedTreeDigest  projectstate.Digest   `json:"accepted_tree_digest"`
    CandidateTreeDigest projectstate.Digest   `json:"candidate_tree_digest"`
    Changes             []publicationChangeV1 `json:"changes"`
}

type publicationRecordKeyV1 struct {
    Kind string `json:"kind"`
    ID   string `json:"id"`
}

type publicationChangeV1 struct {
    Key              publicationRecordKeyV1      `json:"key"`
    Kind             ChangeKind                  `json:"kind"`
    BeforeDigest     *projectstate.Digest         `json:"before_digest"`
    AfterDigest      *projectstate.Digest         `json:"after_digest"`
    BeforeBodyDigest *projectstate.Digest         `json:"before_body_digest"`
    AfterBodyDigest  *projectstate.Digest         `json:"after_body_digest"`
    Fields           []publicationFieldChangeV1   `json:"fields"`
    Actor            *types.ActorEnvelope         `json:"actor"`
}

type publicationFieldChangeV1 struct {
    Path   string                  `json:"path"`
    Before publicationFieldValueV1 `json:"before"`
    After  publicationFieldValueV1 `json:"after"`
    Actor  *types.ActorEnvelope    `json:"actor"`
}

type publicationFieldValueV1 struct {
    Present bool            `json:"present"`
    Value   json.RawMessage `json:"value,omitempty"`
}
```

Each change contains, in order, its `{kind,id}` key, change kind, nullable before/after
record digests, nullable before/after body digests, non-nil ordered field array, and an
explicit actor envelope or JSON `null`. Each field contains `path`, `before`, `after`, and
an explicit actor envelope or JSON `null`.
An absent value requires a nil/empty `Value` and encodes as `{"present":false}`; a
present value requires one strict canonical JSON value, and present JSON null encodes as
`{"present":true,"value":null}`. Unknown/trailing/noncanonical raw JSON is rejected
before hashing. Empty changes and fields encode as `[]`, never `null`. Changes retain the
existing stable kind/ID order and fields retain JSON-pointer order.

Only active portable operations after the selected candidate boundary contribute actor
attribution. Starting from the selected candidate snapshot, replay them in increasing
generation order and compute the semantic fields changed by each step; the greatest
generation that changed an eventual final field supplies that field's actor. Direct
working-tree changes and effects already absorbed into a rebased candidate begin with
actor `null`: the import caller is an observer, not necessarily the author, and Wormhole
must not fabricate authorship. For a lifecycle/root field, an actor is present only when
the change began after the selected start and every contributing active operation has the
same actor; mixed or direct contribution is `null`. The enclosing change actor is present
only when every final field has the same non-null actor; otherwise it is `null`. Thus a
direct priority edit plus an agent title edit attributes only `/title`, while two agents
editing different fields retain their distinct field actors. The candidate's existing
`ImportedBy`/`ImportedAt` remain machine-private import audit. This resolves the current
lossy direct-import and mixed-writer cases without false attribution or a new migration.

The semantic-diff digest is `DigestCanonicalJSON(publicationSemanticDiffV1)`. Golden tests
hard-code both exact canonical JSON and SHA-256; production code never generates the
expected test digest.

The empty-diff golden preimage, including its final LF, is exactly:

```json
{"schema_version":1,"kind":"semantic_diff","accepted_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","candidate_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","changes":[]}
```

Its digest is
`sha256:69591135301299c7ad70bfece52d1bcd9a0c630998dbb4a930428b7fb1d1256f`.

The mixed-attribution golden proves that a direct priority change remains unattributed
while a later active human title operation retains its actor. Its exact preimage, including
the final LF, is:

```json
{"schema_version":1,"kind":"semantic_diff","accepted_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","candidate_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","changes":[{"key":{"kind":"task","id":"11111111-1111-4111-8111-111111111111"},"kind":"modify","before_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","after_digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","before_body_digest":null,"after_body_digest":null,"fields":[{"path":"/priority","before":{"present":true,"value":"low"},"after":{"present":true,"value":"high"},"actor":null},{"path":"/title","before":{"present":true,"value":"old"},"after":{"present":true,"value":"new"},"actor":{"actor_kind":"human","human_principal_id":"22222222-2222-4222-8222-222222222222","assurance":"local","occurred_at":"2026-08-01T12:00:00Z"}}],"actor":null}]}
```

Its digest is
`sha256:754848a8addbc94f263fbdc4dd5afe9b4db31d8ca466d445f215fff6c552f4bb`.

## 6. Publication-review envelope and service outputs

The private exact envelope is:

```go
type publicationReviewEnvelopeV1 struct {
    SchemaVersion       int                             `json:"schema_version"`
    Kind                string                          `json:"kind"` // publication_review
    Scope               types.WorkspaceScope            `json:"scope"`
    Repository          types.RepositoryIdentity        `json:"repository_identity"`
    OriginDigest        projectstate.Digest             `json:"origin_digest"`
    Classification      types.PublicationClassification `json:"classification"`
    PolicyRevision      int64                           `json:"policy_revision"`
    AcceptedRef         string                          `json:"accepted_ref"`
    AcceptedCommitSHA   string                          `json:"accepted_commit_sha"`
    AcceptedTreeDigest  projectstate.Digest             `json:"accepted_tree_digest"`
    CandidateTreeDigest projectstate.Digest             `json:"candidate_tree_digest"`
    SemanticDiffDigest  projectstate.Digest             `json:"semantic_diff_digest"`
    OverlayGeneration   int64                           `json:"overlay_generation"`
}
```

The review digest is `DigestCanonicalJSON(publicationReviewEnvelopeV1)`. Including exact
workspace scope prevents cross-workspace acknowledgement reuse. Including origin and the
monotonic policy revision prevents cross-origin and classification-cycle reuse. Detached
HEAD is the byte-exact empty accepted ref. Every field is required and has no `omitempty`.

One trust-observation bundle contains both the canonical observed origin and a pure
hardened Git-base observation of exact root, checkout device/inode, symbolic ref (or
detached empty ref), HEAD commit, committed `.wormhole` tree, tree digest, project ID, and
canonical repository identity. It is read once outside SQLite and again under the exact
workspace writer barrier. The two bundles must be byte-equivalent, and the Git-base fields
must exactly equal the complete stored accepted binding before a review digest is served.
Status/diff return `ErrGitObservationChanged` without a digest when the checked-out base
needs `RefreshWorkspace`; they never advance Git themselves. Task-8 adapters perform the
already-required refresh orchestration and retry, but checkpoint owns this trust check and
never relies on an adapter having refreshed first.

One transaction-local helper receives that exact outside bundle, re-observes it under the
writer barrier, resolves/stickily invalidates policy, and computes composition, semantic
diff, trusted effective classification, semantic-diff digest, review envelope, and review
digest from one SQLite snapshot. `Status`, `Diff`, and both checkpoint transactions
consume the same helper; no surface independently reconstructs or trusts these values. A
Git mutation after the in-transaction observation is outside the SQLite lock, so that
observation is the operation's Git/base-config linearization point. This matches the
existing HEAD/working-tree CAS and the explicit local-writer OS trust boundary: the owner
and every principal granted write access through UID, group, ACL, administrator, or the
filesystem are trusted equally. Wormhole verifies checkout identity but does not claim to
lock `.git/config` or HEAD against any such local writer.

The exact internal service additions are:

```go
type WorkspaceStatus struct {
    Binding                  types.WorkspaceBinding
    State                    string
    AcceptedSnapshot         projectstate.Snapshot
    CandidateDigest          projectstate.Digest
    OverlayGeneration        int64
    PublicationClassification types.PublicationClassification
    PublicationReviewDigest  projectstate.Digest
}

type WorkspaceDiff struct {
    SemanticDiff              Diff
    CandidateDigest           projectstate.Digest
    OverlayGeneration         int64
    PublicationClassification types.PublicationClassification
    PublicationReviewDigest   projectstate.Digest
}

func (s *Service) Diff(
    context.Context, types.WorkspaceScope,
) (WorkspaceDiff, error)
```

`FieldChange` gains the exact per-field `Actor *types.ActorEnvelope` described above;
`Change.Actor` remains the conservative all-fields-same projection. The Task-8 CLI/MCP
layer maps status and diff to explicit public DTOs and must not serialize internal
`WorkspaceBinding`, checkout path/device/inode, or the complete accepted snapshot.

Valid `unclassified` policy still produces a review digest for inspection; checkpoint
rejects the class. Corrupt policy/origin/diff data returns an error and no usable digest.

The publication-envelope golden preimage, including its final LF, is exactly:

```json
{"schema_version":1,"kind":"publication_review","scope":{"project_id":"00000000-0000-4000-8000-000000000001","workspace_id":"00000000-0000-4000-8000-000000000002"},"repository_identity":{"provider":"github","immutable_id":"repository-1","canonical_remote":"https://github.com/acme/wormhole"},"origin_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","classification":"public_git","policy_revision":2,"accepted_ref":"refs/heads/main","accepted_commit_sha":"dddddddddddddddddddddddddddddddddddddddd","accepted_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","candidate_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","semantic_diff_digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","overlay_generation":0}
```

Its digest is
`sha256:0c84b1ee405ef05d66f4f8c8fc096420fc148fa32608c9fb3133c8a833c5105a`.

## 7. Checkpoint acknowledgement and durable proof

Task 5 retains:

```go
type CheckpointRequest struct {
    Scope                     types.WorkspaceScope
    Root                      string
    ExpectedWorkingTreeDigest projectstate.Digest
    PublicationReviewDigest   *projectstate.Digest
    Actor                     types.ActorEnvelope
}
```

There is no pre-acknowledgement endpoint or acknowledgement table. The checkpoint call is
the acknowledgement. Exact sentinels are:

- `ErrPublicationUnclassified` — valid effective policy is unclassified;
- `ErrPublicationReviewRequired` — `public_git` omitted the digest; and
- `ErrPublicationReviewStale` — a supplied digest is not the exact recomputed digest.

All return `CheckpointResult{}`. Classification/origin/review resolution precedes stage
creation, journal insertion, and filesystem publication. When the trusted policy binding
is already current, a missing or stale review acknowledgement changes no durable database
domain row and creates/modifies no project, stage, or backup path/content; SQLite lock/WAL
bookkeeping is not a domain mutation. A stable repository/origin mismatch is processed first as the distinct
sticky invalidation transaction required by §4; that policy-row transition is the sole
permitted database change, after which checkpoint returns unclassified with no stage,
journal, candidate, overlay, conflict, materialization, or filesystem mutation.

Migration `000004_checkpoint_publication_review.sql`, owned by Task 5, adds:

```sql
ALTER TABLE workspace_materializations
  ADD COLUMN publication_review_json TEXT;

ALTER TABLE workspace_materializations
  ADD COLUMN prior_candidate_json TEXT;

ALTER TABLE workspace_materializations
  ADD COLUMN publication_review_proof_version INTEGER NOT NULL DEFAULT 0
  CHECK(
    (publication_review_proof_version=0 AND
      publication_review_json IS NULL AND prior_candidate_json IS NULL) OR
    (publication_review_proof_version=1 AND
      publication_review_json IS NOT NULL AND prior_candidate_json IS NOT NULL)
  );
```

Version 0/null/null is explicit pre-v4 missing proof, not trusted as "legacy": it blocks every
`prepared`, `published`, or `recovered_new` recovery/publication/acceptance path and retains
all evidence. It is tolerated only on terminal `accepted` or `recovered_old` history after
the existing complete no-residual-ownership proof succeeds. Version 1 requires both exact
canonical JSON envelopes below. SQL `NULL` in `prior_candidate_json` means only a blocked
version-0 row; an actually absent pre-checkpoint candidate is represented by the non-null
canonical `checkpointPriorCandidateV1` envelope whose `candidate` member is JSON `null`.
Every new prepared-journal writer requires version 1 and both envelopes:

```go
type checkpointPublicationReviewV1 struct {
    SchemaVersion int                         `json:"schema_version"`
    Kind          string                      `json:"kind"` // checkpoint_publication_review
    Review        publicationReviewEnvelopeV1 `json:"review"`
    ReviewDigest  projectstate.Digest         `json:"review_digest"`
    CheckpointedBy types.ActorEnvelope         `json:"checkpointed_by"`
}

type checkpointPriorCandidateV1 struct {
    SchemaVersion int                              `json:"schema_version"`
    Kind          string                           `json:"kind"` // checkpoint_prior_candidate
    Candidate     *checkpointPriorCandidateStateV1 `json:"candidate"`
}

type checkpointPriorCandidateStateV1 struct {
    AcceptedBaseDigest       projectstate.Digest    `json:"accepted_base_digest"`
    WorkingTreeDigest        projectstate.Digest    `json:"working_tree_digest"`
    DirectTree               checkpointPriorTreeV1  `json:"direct_tree"`
    RebasedTree              *checkpointPriorTreeV1 `json:"rebased_tree"`
    RebasedThroughGeneration int64                  `json:"rebased_through_generation"`
    ImportedBy               string                 `json:"imported_by"`
    ImportedAt               time.Time              `json:"imported_at"`
}

type checkpointPriorTreeV1 struct {
    Digest projectstate.Digest     `json:"digest"`
    Files  []checkpointPriorFileV1 `json:"files"`
}

type checkpointPriorFileV1 struct {
    Path string `json:"path"`
    Data []byte `json:"data"`
}
```

The exact absent-candidate preimage is
`{"schema_version":1,"kind":"checkpoint_prior_candidate","candidate":null}\n`.
All other preimages use the member order above, the shared canonical JSON encoder's final
LF, and standard JSON base64 for `Data`. The strict decoder disallows unknown fields and
trailing JSON, requires exactly one final canonical LF, re-encodes byte-identically, and
validates every digest, path, tree, boundary, importer, and UTC timestamp. `Files` is a
non-nil strictly path-sorted unique complete file list. Its decoded tree must round-trip to
the exact bytes and digest. `ImportedBy` is exactly a canonical UUID or
`system:git-observation-rebase-v1`; `ImportedAt` is non-zero, UTC, and zero-offset. A nil
`RebasedTree` requires boundary zero; a present rebased tree may validly have boundary zero.

Both `DirectTree` and `RebasedTree` are complete inline trees. The prior candidate's
direct snapshot is not the materialization journal's `prior_tree`: the former is a
database preimage while the latter is the independently captured live-tree CAS surface.
The journal and prior-candidate proof satisfy all of these cross-proof invariants before
prepare, publication, recovery, or acceptance:

- `prior_tree_digest == expected_live_digest == DigestTree(prior_tree)`;
- an absent prior candidate requires initial through-generation zero, but imposes no
  equality between `prior_tree` and the accepted snapshot because the live CAS surface may
  contain valid direct edits not yet represented by a candidate row;
- a present prior candidate requires its accepted-base digest to equal the journal and
  binding accepted digest, its working-tree digest to equal its complete inline direct-tree
  digest, and both inline trees to strict-decode as complete canonical snapshots for the
  same project and repository;
- `RebasedThroughGeneration` equals
  `CheckpointOperationsV1.InitialThroughGeneration`; and
- the independently built checkpoint plan uses its exact selected start and the same
  initial boundary, then applies all and only envelope-listed `active` rows above that
  boundary to reproduce the journal candidate tree, digest, and through-generation;
  listed `rebased` rows at or below the boundary remain exact ownership/prestate evidence.

Each direct or rebased inline prior-candidate tree is independently limited to at most
`10_000` files, at most `4 << 10` UTF-8 bytes per path, at most `16 << 20` bytes per file
body, and at most `64 << 20` total bytes, counted as the sum of every path byte plus every
file-data byte. There is no combined direct-plus-rebased aggregate limit and no raw-JSON
byte limit in v1. Filesystem-only directory-count and depth limits do not apply to the
serialized proof; canonical `DecodeTree` still rejects unknown or unsafe project-state
paths.

`publication_review_json` is non-null for every new checkpoint. For `public_git`, the
request digest must equal both `ReviewDigest` and the freshly recomputed digest. For
local/private, the same exact review and actor are durable checkpoint provenance even
though no explicit acknowledgement is required. The decoder rejects unknown fields,
trailing JSON,
noncanonical bytes, a checkpoint actor that does not pass `ValidateLocalAction`, malformed digests/envelopes, and any
recomputed mismatch. `WorkspaceMaterializationRecord`, every clone/scan/equality/CAS
helper, `MaterializationDisposition`, journal insert/update, `AcceptMaterialization`, the
complete ownership proof, and every restart-recovery branch include the proof version and
both JSON fields. A writer API can never create version 0 or either null proof.

The first checkpoint transaction strict-proves the complete disposition before doing
anything else. One existing `prepared`, `published`, or `recovered_new` journal returns
`ErrCheckpointPendingAcceptance` before stage allocation or mutation; multiple or mixed
pending rows fail as corruption. A prepared row is never silently superseded and must be
converged through `Recover`. The transaction then requires `Actor.ValidateLocalAction`, captures and
clones the exact pre-checkpoint candidate before any candidate mutation, re-observes the
complete Git-base/origin trust bundle, byte-matches it to the stored accepted binding, and
computes the exact review before staging or journal mutation. After the prepared journal
commits, the second immediate transaction repeats that complete observation and recomputes
the review and raw prior-candidate preimage from the still-current candidate; it requires
equality with the request where supplied and with both durable proofs before any
rename/exchange. Publication replaces the candidate with the prior live direct tree plus
the published candidate as its rebased tree and the exact through-generation. It preserves
the old candidate's importer/time when one existed; otherwise it uses the checkpoint
actor principal and occurrence time. The accepted binding remains unchanged, and every
successful result contains only the candidate digest, materialized through-generation,
and journal ID. `Checkpoint` and `Recover` never advance the accepted binding. A
checkpoint materialization is accepted only by same-symbolic-ref Reject/Refresh; Task 4
may otherwise advance the base through its proposal-free ref-switch or applicable-Discard
transitions.
Any accepted ref/commit/tree, candidate, semantic diff, actor attribution, overlay
generation, policy revision/class, repository, origin, included operation membership,
open conflict, checkout, or working-tree race prevents publication.

Before artifact creation, checkpoint resolves a Git-private checkpoint directory outside
the portable worktree through the hardened equivalent of
`git rev-parse --git-path wormhole/checkpoints`, proves it owner-only and on the same device
as live `.wormhole`, proves the required no-replace rename and directory-fsync primitives,
or returns `ErrCheckpointUnsupported` before artifact creation. Checkpoint generates a
canonical lowercase-UUID journal ID and allocates its
exact direct-child `<journal_id>.stage` and `<journal_id>.backup` absolute paths no-replace,
but creates only the owner-only stage; the backup must not exist before journal-backed
publication. An orphan stage is never an untracked worktree sibling exposed
to a broad `git add`. Checkpoint does not create a journal before the post-stage CAS
succeeds. On any pre-journal failure, including CAS failure, the staged tree is retained as
unowned diagnostic evidence: no row names or owns it, `Recover` never enumerates, opens,
validates, publishes, restores, or deletes it, and a later checkpoint uses another fresh
no-replace pathname. Safe cleanup is explicitly deferred beyond Task 5.

Task-5 V1 uses the fallback sequence only. It no-replace renames live to the absent backup,
fsyncs the private destination parent before the checkout source parent, reclassifies the
three paths, then no-replace renames stage to absent live and fsyncs the checkout destination
parent before the private source parent. The second rename is the publication linearization
point. Database postimage mutation follows durable publication in the still-open second
writer transaction. Each rename is attempted at most once; an error is classified as exact
prior, exact next, or a byte-preserving blocked third state.

`Recover` opens one dedicated `BEGIN IMMEDIATE`. In that one writer-excluding
snapshot it strict-loads and recovery-proves the binding, candidate, complete
materialization disposition, exact operation ownership, and every field required to compose
the return status before any Git or path I/O. No journal plus any materialized row is
corruption. Empty history, or only fully proved `accepted`/`recovered_old` history, is an
idempotent no-recovery-work disposition. Exactly one proved `recovered_new` alongside only
such terminal history is also no-recovery-work and remains acceptance-eligible. Exactly one
`prepared` or `published` journal alongside only terminal history drives recovery; any
mixed or multiple prepared/published/recovered-new rows, cross-journal claim, or
orphan/duplicate/partial ownership fails before Git or path I/O. Version 0 remains blocked.
A prepared proof requires the candidate row to equal the exact decoded prior
candidate preimage, every envelope-listed operation to remain in its recorded
prepublication `active`/`rebased` state, and zero owned materialized rows. A published or
recovered-new proof requires the candidate to equal the exact publication postimage and
every envelope claim to be materialized. Accepted history must pass its complete historical
ownership proof; a version-0 accepted row is tolerated only with no residual materialized
row. Recovered-old history owns none.

Every no-recovery-work disposition composes the exact `WorkspaceStatus` with both
publication-review fields zero inside that same snapshot, commits the transaction,
and returns it with no Git, origin, live/stage/backup path, clock, policy, or filesystem
I/O. It never calls `Status`, because `Status` intentionally performs a fresh publication
review. A prepared/published driver retains the proved writer transaction across its one
stable local Git observation, filesystem classification or mutation, database outcome
write and reread, and commit.

Only the one proved `prepared` or `published` journal invokes recovery observation. The
proved disposition is the owned transaction snapshot. Recovery uses a current-HEAD
observer, not the publication-review observer that requires the stored accepted commit.
The one Git observation has exact order: capture current symbolic ref and HEAD
position; read the complete committed `.wormhole` tree/digest, project, and repository at
that observed SHA; observe semantic origin; then capture the final symbolic-ref/HEAD
position and require it byte-equal the initial position. It then repeats the
recovery-specific disposition/ownership proof before any live/stage/backup access or write.
A malformed bundle, observation race, disposition drift, or root/checkout mismatch
is normalized to the Task-5 recovery precondition error with policy, filesystem, journal,
candidate, operations, and evidence untouched. Git-base case selection precedes origin
invalidation; case 3 leaves policy untouched. In cases 1 and 2, a stable configured-origin
mismatch may stickily invalidate policy without changing the already selected recovery
disposition.

The observed committed Git base then selects exactly one recovery case:

1. **Stored base exact.** Ref, commit, complete committed tree/digest, project, and
   repository equal the binding recorded before checkpoint. Normal old-live/new-live
   recovery applies. A proven old live tree is retained/restored, every listed operation
   returns to its prepublication state, and the journal becomes `recovered_old`. A proven
   new live tree is finalised as `recovered_new` because publication already occurred.
2. **Same-ref different-commit Git acceptance exact.** The observed symbolic ref exactly
   equals the stored accepted ref, HEAD commit differs from the stored accepted commit
   without any ancestry requirement, and its committed `.wormhole` tree is byte-exactly
   the journal candidate. All journal, review, prior-candidate,
   included-operation, checkout, project, repository, and materialisation-ownership proofs
   must succeed. Recovery finalises the candidate as `recovered_new` but does not advance
   the accepted binding. The following
   `RefreshWorkspace`/`ObserveGitBase` call remains the sole authority that accepts the new
   Git ref/commit/tree and consumes the acceptance-eligible materialisation. Recovery never
   restores the old tree over this committed candidate.
3. **Any other Git base.** This includes every changed symbolic ref, even when its tree
   happens to equal the journal candidate. Return `ErrCheckpointRecoveryPrecondition` with
   policy, filesystem, journal, candidate, operations, and evidence untouched.

After case 1 or 2 and before opening either stored stage/backup entry, recovery re-runs the
hardened Git-private-root resolver, canonicalizes and no-follow opens that owner-only root,
and proves it remains outside the portable worktree and on live `.wormhole`'s device. Every
new version-1 journal ID is a canonical lowercase UUID; its stored absolute paths must be
distinct and byte-equal to the direct root children `<journal_id>.stage` and
`<journal_id>.backup`. Raw stored paths are never traversed or opened. Only after those
string/containment/name checks may recovery inspect a child descriptor-relatively with
no-follow semantics. Any present child must be a same-device directory in the exact
state-dependent existence/digest matrix; recovery holds its descriptor, captures identity,
and revalidates the pathname-to-descriptor identity immediately before mutation.

An unsafe/rebound root, non-absolute or noncanonical stored path, escape/nested child,
wrong journal-bound basename, equal stage/backup paths, or cross-device root returns
`ErrCheckpointRecoveryPrecondition`. A canonical contained child that is a symlink, has an
unexpected type/existence/digest, or changes identity returns
`ErrCheckpointRecoveryBlocked`. Neither class follows the entry, mutates any path, or
deletes evidence.

Unknown live/stage/backup evidence always remains blocked and untouched. A sticky policy
invalidation may commit in cases 1 or 2, but it never changes which exact tree is already
proven live/committed; the now-unclassified policy blocks further checkpoint until explicit
reconfiguration. "Proof succeeds" for an already-published or later-Git-accepted tree means
the stored version-1 durable proof is internally canonical and matches the journal and
published bytes; it does not require the current policy to equal the historical review.
A later policy or origin change cannot authorize new publication, but also cannot prevent
finalization of bytes proven already live or committed. Durable proof fidelity survives
restart.

Recover-old is a complete preimage restoration, not a reconstruction. If
`checkpointPriorCandidateV1.Candidate` is null it deletes the publication-created
candidate. Otherwise it upserts the exact original direct snapshot, optional rebased
snapshot, boundary, accepted/working-tree digests, importer, and timestamp. In the same
transaction it restores every listed operation to its exact recorded prepublication
`active`/`rebased` state, preserves every later active row, and marks the journal
`recovered_old`. Recover-new keeps the published candidate and exact materialized rows and
marks the journal `recovered_new`.

Task-5 journal transitions have no request receipt and never retry an indeterminate write.
Before COMMIT each path retains an owned exact prior disposition and intended next
disposition. A fresh journal-backed confirmation transaction has only three outcomes:

1. exact complete next journal, candidate, operation, policy, and ownership state: treat
   the attempted prepare, publish, recover-old, or recover-new transition as committed;
2. exact complete prior state, including journal absence for attempted preparation: return
   the original error wrapping
   `localstore.ErrCommitOutcomeUnknown` and a zero public result; or
3. any state that is neither that transition's exact prior shape nor its exact next shape,
   including read failure, corruption, a partial mix, or a third valid state: fail closed
   with the original unknown-COMMIT error plus confirmation context.

Confirmation performs no Git/path I/O and never replays the write. A confirmed prepared
transition may continue to the second publication transaction; a confirmed published
transition returns its already-constructed result; confirmed recovery returns its
already-constructed zero-review status. For attempted preparation, exact prior absence,
read failure, or any third state retains the unjournaled stage byte-identically. If the
filesystem already changed but the database remains the exact prepared prior state, the
call stays unknown and later `Recover` is the sole convergence path. Sticky invalidation
confirmation continues to use §4's exact prior/next policy-and-history matrix and likewise
never retries.

## 8. Required causal implementation slices

Implementation follows RED -> minimal GREEN -> focused review in this order:

1. **Enum, origin codec, and origin observer**
   - exact enum and observed-origin canonical JSON/digest goldens;
   - HTTPS/SSH/SCP equivalence, ports/path case, missing/local/offline origin;
   - exact URL-count/entry/stdout bounds, DNS/IP grammar, credential redaction,
     malformed/multiple origins, hardened Git environment;
   - machine-private digest-only setup inspection and independent service re-observation;
   - canonical/fork relation is diagnostic only and performs zero network calls.
2. **Gateway migration v3 and strict localstore policy**
   - fresh v3 and v2->v3 backfill/preservation;
   - atomic registration row, FK/cascade/CHECK/rollback/ledger proofs;
   - strict storage-class/canonical-JSON/timestamp/corruption reads, including rejection of
     a locally assured agent as a configured-policy actor;
   - complete CAS, stale/overflow/trigger-drift/restart/isolation tests.
3. **Service reconfiguration and effective resolution**
   - outside/inside origin race returns zero mutation;
   - explicit unclassified/local/public/private configuration and readback, current-exact
     no-op, stale rejection, and caller readback idempotence;
   - same-human exact no-op, different-human attribution revision, and complete private
     configuration readback;
   - stale publication-binding constraint against bootstrap/current policy is zero-mutation
     CAS, while a configured-origin mismatch commits sticky invalidation first;
   - public -> private -> public and origin A -> B -> A produce distinct sticky revisions;
   - repository/origin mismatch resolves unclassified;
   - reconfiguration on a stable mismatch commits invalidation, returns the updated
     configuration plus the exact invalidated sentinel, and confirms unknown COMMIT;
   - a public fork explicitly configures `public_git`;
   - forged actor assurance, Fabric/canonical/fork hint, or caller mode has no authority;
   - non-`git` SSH username changes reject without leaking it or preserving a digest.
4. **Canonical diff/review codec and status/diff integration**
   - hard-coded empty, mixed-attribution, and review-envelope JSON/SHA-256 goldens;
   - mixed direct/active attribution and distinct multi-actor field attribution;
   - every bound-field mutation changes the review digest;
   - status/diff agree from one snapshot; unclassified inspection succeeds;
   - human/agent data changes do not alter classification; cross-workspace reuse fails.
5. **Task-5 migration v4, checkpoint, and recovery**
   - public human/agent acknowledgement parity;
   - with a current binding, missing/stale/mismatched digest changes no durable domain row
     or project/stage/backup path;
   - stable origin mismatch commits exactly one policy/history invalidation pair and no
     other domain/filesystem change;
   - branch-switch/discard/race failures preserve Task-4 zero-mutation semantics;
   - second-transaction races and conflicts publish nothing;
   - fresh/v3-to-v4 joint proof CHECKs and strict absent/direct/rebased complete inline
     prior-candidate codec/cross-proof goldens;
   - strict durable proof and crash/restart recovery in both filesystem directions,
     including exact candidate absence/snapshots/boundary/import provenance restoration;
   - Linux/WSL fallback ordering, destination-before-source parent fsyncs, exact
     prior/next/third rename classification, and blocked byte-preserving ambiguous evidence;
   - exact recovery-specific prepared/published/recovered-new cardinality, candidate, and
     operation-ownership proofs before Git/path I/O;
   - journal-backed exact unknown-COMMIT confirmation without write replay, including
     attempted-prepare journal absence and byte-identical unjournaled-stage retention;
   - no-journal and proved no-recovery-work calls return zero-review status with no
     Git/path I/O, while every pre-journal stage is ignored and later checkpoints allocate
     fresh paths;
   - one recovery `BEGIN IMMEDIATE` for same-snapshot proof/status composition, a single
     position/tree/origin/position observation, filesystem outcome, database reread, and
     commit, with in-transaction disposition drift rejection before path access;
   - hardened recovery-root re-resolution plus descriptor-relative/no-follow exact
     journal-ID child validation, with escape/symlink/type/identity/rebind negatives and
     zero path mutation;
   - same-ref different-commit exact Git acceptance without an ancestry requirement,
     changed-ref/unrelated-base rejection untouched, and Git-case precedence over origin
     invalidation; and
   - a policy change after publication cannot strand already-published bytes; checkpoint
     and recovery never advance the accepted binding.
6. **Task-8 public projections and parity**
   - CLI and MCP expose the same safe status/diff/checkpoint semantics;
   - no machine-private binding, root, checkout identity, policy actor, or snapshot leaks.

Each slice must include cross-project and cross-workspace negative tests, `git diff
--check`, focused race tests where concurrency exists, and the repository's >=80% merged
coverage gate. No Task-5 staging or filesystem publisher code begins before slices 1-4
are reviewed and committed.

## 9. Explicit non-goals

This amendment does not:

- authenticate a Git host, continuously detect host visibility, or provide DLP;
- read Git credentials or credential-helper output;
- authorize Fabric access or let forks use upstream Fabric;
- stage, commit, push, or advance the accepted Git base;
- make direct imported edits falsely attributable to the actor who happened to import;
- expose publication reconfiguration as an MCP project mutation; or
- implement checkpoint publication, activity retention, Fabric, setup, or Code Graph.
