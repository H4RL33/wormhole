# Code Graph Alpha Storage and Revision Contract

**Status:** retained internal component contract; not a live Stage 2 CLI, MCP,
setup, connector, help, generated-guidance, or release-trial surface.

## Status and scope

This contract freezes Tasks 7 through 11 of the Code Graph alpha slice. It defines
Gateway-local configuration and SQLite storage, canonical Git inventory, the
Go semantic adapter, candidate construction and validation, revision
publication, lifecycle ownership/recovery, structural querying, bounded
source assembly, and the human CLI lifecycle. It does not define Fabric
synchronisation, KB integration, authenticated permission derivation, or
Warpspeed.

Code Graph remains experimental, Go-only for alpha, disabled by default, and
local to one Gateway machine. Git and the approved working tree remain source
authority. Fabric does not create, migrate, read, or synchronise these tables.

## Project binding and configuration

`store.Open` consumes one stable Gate A project identifier. The returned store
is permanently bound to that project. Every data read and write includes that
identifier in SQL; a record carrying another project identifier is rejected.
Separate project-bound stores may share the Gateway SQLite handle without
sharing configuration, revisions, or payloads.

There is at most one `codegraph_config` row per project and therefore one
approved active checkout. An absent row reads as disabled. The version-one
configuration persists only:

```text
project_id
enabled
canonical_remote
active_checkout
project_source_byte_ceiling
last_successful_build
active_revision_id
```

`active_revision_id` is storage-owned publication metadata, not a second
operator-selected checkout. Enabled configuration requires one canonical
remote and one active checkout. Canonicalisation, remote verification, human
confirmation, and destructive disablement belong to the Task 11 lifecycle. A build is
permitted only while this persisted configuration is enabled, and its checkout
and remote must equal the approved values through the publication transaction.

## Component-local SQLite schema

Code Graph owns only tables prefixed `codegraph_`:

```text
codegraph_schema_migrations
codegraph_config
codegraph_revisions
codegraph_nodes
codegraph_files
codegraph_symbols
codegraph_edges
codegraph_diagnostics
codegraph_lifecycle
```

The component migration ledger is currently version `2`. Version one owns graph
configuration/payload; version two adds the cross-process build/disable lease
with distinct builder and disabler PID/process-start identities. Migration runs
under a SQLite `BEGIN IMMEDIATE` critical section, so concurrent
project Stores serialize migration discovery and application. It fails closed
with `ErrSchemaTooNew` when the database records a later version. Code Graph
migrations never use or extend the Fabric `migrations/` sequence.

Every graph payload row is both project- and revision-scoped. The schema stores
node metadata, file paths and indexed SHA-256 hashes, symbol signatures and
source ranges, provenance-bearing edges, and metadata-only diagnostics. It has
no column for a complete source file, function body, source body, returned
slice, or context package. Diagnostic callers must not place those values in a
message.

## Revision lifecycle

Revision states are:

```text
candidate -> active -> retired
candidate -> failed
```

Candidate payload is copy-on-write and cannot be returned by an active reader.
Each payload insert selects the matching revision only while its state is
`candidate`; the state check and insert are one SQLite statement. Publication
and insertion therefore cannot commit in an order that exposes a row added
after validation, including through another Store or process connection.
Publication validates the candidate and then, in the same SQLite transaction:

1. retires the previous active revision for the bound project;
2. marks the candidate active and complete;
3. changes the project's active pointer and successful-build timestamp.

A partial pointer/state change is never committed. SQLite also enforces at most
one active revision per project.

Validation rejects non-canonical commit or file hashes, duplicate deterministic
identifiers, file or symbol metadata without matching candidate nodes, symbols
whose file or source range is invalid, dangling edges, and every reference that
does not resolve inside the same project and revision.

When validation fails, a second failure transaction removes candidate nodes,
files, symbols, and edges; marks the candidate failed; retains existing build
diagnostics; adds a validation diagnostic; and leaves the old active pointer and
payload unchanged. Failed payload is never queryable as active.

System failure diagnostics use the reserved `@wormhole/system/` identifier
namespace. Candidate writers cannot use that namespace. System inserts never
upsert over an existing diagnostic, so caller diagnostics and distinct failure
records remain intact.

## Canonical inventory and Go analysis

Inventory starts from the canonical approved Git root and verifies that the
normalized `remote.origin.url` repository identity equals the normalized
persisted canonical remote. URL scheme/host case, a trailing slash, and an
optional `.git` suffix are equivalent; credential-bearing HTTPS remotes fail
closed without echoing the raw origin. Git process
arguments are passed without shell interpretation, inherited `GIT_*` controls
are removed, and system/global Git configuration is disabled. The inventory
command is equivalent to:

```text
git -C <checkout> ls-files -z --cached --stage -- '*.go'
```

Only unique, local UTF-8 `.go` paths with stage zero and regular `100644` or
`100755` modes are accepted. Conflicts, symlinks, traversal, collisions,
missing files, and files that change during a root-scoped read fail closed.
Reads use a checkout-root handle and pre/post file identity checks. Limits are
10,000 files, 4 MiB per file, and 128 MiB total. Each accepted file carries its
exact current working-tree bytes and canonical SHA-256; tracked non-Go files and
all untracked or ignored source bytes are excluded.

The storage-independent Go adapter invokes `go/packages` with syntax, module,
and test metadata, `Tests=true`, checkout `Dir`, exact tracked-byte overlays,
and `-mod=readonly`. Every on-disk `.go` path absent from the tracked inventory
is overlaid with a never-enabled suppression file without reading its contents.
It disables external package drivers, workspaces, persistent Go environment,
ambient flags, toolchain switching, and CGO. Alpha analysis has one explicit
deterministic build context: Linux, amd64, GOAMD64 v1. This is an alpha analysis
policy, not a portability claim.

Production packages, internal tests, and external tests are distinct package
variants. Build-excluded tracked files retain file nodes and hashes but do not
produce declarations. Supported declaration nodes are functions, methods,
types, interfaces, constants, and variables, including generics and multiple
`init` declarations. Supported edges are `contains`, `defines`, `imports`,
`calls`, `references`, and `uses_type`. Semantic edges resolve only to local
nodes, use exact `go_packages`, `go_types`, or `go_ast` provenance, confidence
`1`, and never use heuristic or external placeholder nodes. Type conversions
are type uses, not calls.

Package, file, symbol, edge, repository, fingerprint, and diagnostic identities
use SHA-256 over length-prefixed identity fields. Symbol fields exclude bodies,
comments, whitespace, and source positions; body-only edits preserve identity,
while signature changes and renames change it. Adapter limits are 2,000
packages, 250,000 symbols, 2,000,000 edges, and 1,000 bounded sanitized
diagnostics.

A build hashes root `go.mod`, constructs a candidate from the exact inventory,
and rechecks the module file, Git root, remote, commit, paths, modes, bytes, and
approved project configuration before publication. The final enabled checkout
and remote check shares the SQLite transaction with the active-pointer swap.
Human lifecycle builds also re-read the exact credential-profile, agent,
Passport, ready-checkpoint, and canonical repository binding in that same
transaction. Bootstrap or Passport repository rotation during a build therefore
fails the candidate instead of publishing authority that is no longer active.
Checkout switches and re-enablement preserve the existing project source-byte
ceiling; the lifecycle CLI exposes no limit override.
Any analysis, limit, checkout-change, write, cancellation, or publication
failure uses bounded detached cleanup, marks an unfinished candidate failed,
retains diagnostics, and leaves the active revision intact.

## Reader and restart guarantees

`ReadActive` resolves the active pointer and executes the complete callback in
one read-only SQLite transaction. Every `Snapshot` method is bound to that same
project and revision. A publication concurrent with the callback may complete,
but the callback continues to observe its original SQLite snapshot. A later
reader observes the newly active revision.

Ordinary `Open` never recovers live candidates. Gateway startup uses explicit
`OpenRecovering`, verifies lifecycle PID plus process-start ownership, preserves
a live build candidate while failing unrelated stale candidates, and never
clears a live disable marker. Startup holds a SQLite `BEGIN IMMEDIATE` writer
barrier from lifecycle inspection through candidate/disable cleanup, preventing
new build or disable admission into the check-to-clean window. Ordinary build
admission conditionally reclaims only an exactly matched, positively dead owner
so a crashed CLI build does not require a Gateway restart; live or uncertain
ownership remains authoritative. Verified-stale recovery atomically cleans
payload, marks interrupted candidates failed, and
records an `interrupted_candidate` diagnostic. It neither changes the active
revision nor touches candidates belonging to another project; opening that
other project performs its own recovery.

## Query and source guarantees

`query.Service` resolves project configuration, candidate symbols, traversal
frontiers, and source metadata from one `ReadActive` callback. Candidate search
is SQL-capped at 512 rows; returned matches are capped at 64, traversal at 8
levels, 1,000 nodes, and 4,000 edges. Exact qualified and unqualified entry
symbols precede lexical symbol matches and package/file matches. Ties then use
bounded structural distance, relationship relevance, confidence, provenance,
qualified name, and deterministic identifier. Alpha has no embedding or
semantic-similarity path.

Traversal applies direction, relationship, and confidence predicates in SQL
before its edge row cap, then applies depth and node bounds before returning a
deterministic response. Previously returned edge identifiers are excluded from
later frontier reads. After traversal, one bounded SQL count over the union of
expanded frontier nodes excludes returned identifiers and reports the unique
filtered edge cardinality omitted by the response cap; DirectionBoth never
counts the same edge again from its opposite endpoint. Every returned edge has
both endpoints in the returned node set. Revision and indexed-commit identity,
completeness, omitted counts and reason, and bounded follow-up symbols are
explicit response fields.

Source assembly occurs only after graph ranking. Query passes the checkout and
project ceiling captured from its pinned snapshot to `source.Assemble`; a
global ceiling is supplied by runtime wiring. The effective budget is the
minimum of requested, project, and global bytes, and overlapping slices each
count their exact returned UTF-8 bytes. A source-denied request returns metadata
with `missing_permission` and `code_graph.source.read` without opening the
checkout.

Authorized reads validate canonical local paths, indexed SHA-256 and file size,
byte and line ranges, UTF-8 boundaries, and a 4 MiB indexed-file cap. One
root-scoped checkout handle prevents traversal, symlink escape, and root-swap
races. Files are capped-read and hashed before slicing. Missing, resized, or
hash-mismatched files omit source as `working_tree_changed` and recommend a
refresh; they never return stale bytes.
