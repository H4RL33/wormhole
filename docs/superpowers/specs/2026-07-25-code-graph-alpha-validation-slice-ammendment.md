# Historical: Code Graph Alpha Slice Roadmap Amendment

**Status:** Historical source document; superseded by the canonical
[`docs/superpowers/specs/2026-07-25-alpha-validation-unified-spec.md`](2026-07-25-alpha-validation-unified-spec.md).

The canonical root roadmap pointer is
[`ROADMAP-ALPHA-VALIDATION.md`](../../../ROADMAP-ALPHA-VALIDATION.md).

---

## Decision

Code Graph will be included in Alpha Validation as an **opt-in, local, Go-first experimental capability**.

This does not constitute acceptance of the complete Code Graph V1 design. The full polyglot and operational feature set remains post-alpha.

The alpha slice exists to test one proposition:

> Can a coding agent use a local structural graph to identify the relevant code path and retrieve a bounded source package before resorting to broad repository searches and whole-file reads?

## Architectural boundaries

The alpha slice must preserve the approved Code Graph boundaries:

* Code Graph is hosted by Gateway.
* It is project-scoped and local to one machine.
* It is not a shared KB.
* It is not synchronized through Fabric.
* It is not a fifth Core pillar.
* It never replaces Git or the working tree as code authority.
* It never stores complete source files or function bodies.
* Exact source slices are read from the working tree only after graph ranking.
* It is disabled by default.
* Enablement, disablement, checkout selection, and destructive actions remain human-controlled.
* Agents cannot activate Warpspeed.
* Governance has no dependency on or authority over Code Graph.

## Revised critical path

```text
Repository rebaseline
        ↓
Gateway-owned enrolment lifecycle
        ↓
Offline Gateway restart
        ↓
Integration-manifest design
        ↓
Code Graph Alpha Slice
        ↓
Generated AGENTS and SKILLS guidance
        ↓
Meaning-bearing shared KB search
        ↓
Fabric-distributed integration manifests
        ↓
Two-agent alpha acceptance loop
        ↓
Closed external validation
```

The Code Graph implementation may proceed in parallel with shared KB semantic-search work once Gateway lifecycle and local project binding are stable.

---

# New Milestone 4: Code Graph Alpha Slice

## Goal

Implement the smallest coherent Code Graph capability that can be dogfooded on the Wormhole repository and used by real agents during Alpha Validation.

## 4.1 Scope

The alpha slice includes:

* one active checkout per Wormhole project;
* canonical Git remote validation;
* Git-tracked files only;
* Go repositories only;
* repository, package, file, and symbol nodes;
* containment, definition, import, call, reference, and type-use relationships;
* deterministic symbol identities;
* one active completed graph revision;
* copy-on-write rebuild publication;
* lexical and structural entry-point discovery;
* bounded structural traversal;
* bounded source-slice assembly;
* source-hash validation before slices are returned;
* explicit truncation and omission metadata;
* project-scoped permissions;
* Gateway MCP exposure;
* CLI enable, disable, status, and rebuild commands;
* generated model guidance;
* benchmark and dogfood evaluation.

The alpha slice excludes:

* TypeScript and JavaScript semantic adapters;
* Python semantic adapters;
* Rust semantic adapters;
* generic polyglot tree-sitter support;
* local embedding discovery;
* remote embedding providers;
* filesystem watchers;
* fine-grained incremental indexing;
* stale last-known-good region preservation;
* promoted dependency source;
* Warpspeed;
* pause and resume;
* shared audit events;
* durable Code Graph attachments on tasks, events, and KB articles;
* destructive in-place rebuild;
* automatic model or parser downloads;
* historical graphs for commits;
* compiler-complete control-flow or data-flow analysis.

## 4.2 Local package boundary

Add a distinct Gateway-owned package tree:

```text
internal/runtime/codegraph/
  config
  store
  index
  golang
  query
  source
```

Responsibilities:

### `config`

* project enablement;
* canonical remote;
* active checkout;
* source-byte ceilings;
* enabled state.

### `store`

* graph metadata;
* completed and candidate revisions;
* files;
* symbols;
* edges;
* build diagnostics.

The store must not persist complete source files, function bodies, or returned source packages.

### `index`

* Git-tracked file inventory;
* candidate revision creation;
* graph invariant validation;
* atomic active-revision publication;
* failed-candidate cleanup.

### `golang`

* package discovery through Go tooling;
* declarations;
* qualified names;
* signatures;
* imports;
* calls;
* references;
* type relationships;
* deterministic symbol fingerprints.

### `query`

* intent tokenization;
* optional exact entry-symbol lookup;
* lexical symbol and package ranking;
* structural graph traversal;
* confidence and provenance ranking;
* source-budget allocation;
* omission reporting.

### `source`

* filesystem containment;
* path canonicalization;
* working-tree file reads;
* indexed-hash comparison;
* exact line and byte slicing;
* source omission on mismatch.

## 4.3 Graph model

Alpha node categories:

```text
Repository
PackageOrModule
File
Symbol
```

Alpha relationship types:

```text
contains
defines
imports
calls
references
uses_type
```

Every edge records:

```text
source_node_id
target_node_id
relationship_type
confidence
provenance
graph_revision
```

Permitted provenance values:

```text
go_packages
go_types
go_ast
parser
heuristic
```

Heuristic edges must never be presented as authoritative.

## 4.4 Revision model

The first alpha implementation does not require fine-grained incremental indexing.

Initial build and rebuild use coarse copy-on-write publication:

```text
retain active revision
→ enumerate tracked Go files
→ construct candidate graph
→ validate candidate
→ atomically publish candidate
→ retire previous revision
```

If rebuilding fails:

* preserve the active graph;
* discard the candidate;
* expose diagnostics;
* report `degraded` or `error`;
* never expose a partial graph.

A dirty working tree does not automatically trigger indexing. Status and query responses report that the checkout has changed since the active revision.

A caller requesting current results may receive:

```text
graph_not_current
rebuild_recommended
```

The agent may request a normal balanced rebuild only when it holds `code_graph.rebuild`.

## 4.5 MCP surface

The alpha MCP surface is limited to:

```text
wormhole.code_graph.query
wormhole.code_graph.status
wormhole.code_graph.rebuild
```

### `wormhole.code_graph.query`

Accepts:

```json
{
  "intent": "Trace agent registration authentication",
  "entry_symbols": ["RegisterAgent"],
  "include_edges": [
    "calls",
    "references",
    "uses_type"
  ],
  "max_depth": 4,
  "minimum_confidence": 0.8,
  "requested_source_bytes": 12000
}
```

Returns:

* matched symbols;
* structural paths;
* source locations;
* edge confidence;
* edge provenance;
* exact source slices when authorized;
* current Git commit;
* working-tree status;
* graph revision;
* completeness;
* omitted-node count;
* omission reason;
* suggested follow-up symbols.

### `wormhole.code_graph.status`

Returns:

```text
disabled
initializing
ready
degraded
stale
error
```

And:

* active checkout;
* canonical remote;
* active revision;
* indexed commit;
* tracked Go file count;
* symbol count;
* edge count;
* dirty tracked file count;
* last successful build;
* database size;
* latest diagnostics.

### `wormhole.code_graph.rebuild`

Requests a normal copy-on-write rebuild.

It cannot:

* enable or disable Code Graph;
* change checkout;
* invoke Warpspeed;
* perform destructive in-place rebuilding;
* alter source-byte ceilings;
* change project configuration.

## 4.6 Permissions

Add project-scoped permissions:

```text
code_graph.query
code_graph.source.read
code_graph.status
code_graph.rebuild
```

Behaviour:

* `code_graph.query` permits graph metadata and source locations.
* Raw source slices additionally require `code_graph.source.read`.
* `code_graph.status` permits health inspection.
* `code_graph.rebuild` permits only a balanced copy-on-write rebuild.
* Enablement, disablement, and checkout configuration remain CLI-only.

A source-denied query returns useful metadata rather than failing completely:

```json
{
  "source_included": false,
  "source_omission_reason": "missing_permission",
  "required_permission": "code_graph.source.read"
}
```

## 4.7 CLI lifecycle

Required commands:

```bash
wormhole config code-graph enable
wormhole config code-graph disable
wormhole config code-graph status
wormhole config code-graph rebuild
wormhole config code-graph checkout set /path/to/repository
wormhole config code-graph checkout show
```

Enablement must:

1. verify the directory is a Git working tree;
2. canonicalize the path;
3. resolve the canonical remote;
4. verify it matches the Wormhole project binding;
5. explain that the capability is local and experimental;
6. explain resource and disk implications;
7. require explicit human confirmation;
8. build the initial graph;
9. leave the feature disabled if the initial build fails.

Disablement must:

1. require explicit confirmation;
2. reject active new queries;
3. allow or cancel current readers safely;
4. delete completed and candidate revisions;
5. delete graph nodes, edges, diagnostics, and configuration;
6. leave Git and the working tree untouched.

## 4.8 Source integrity and containment

Before returning any source slice:

1. resolve the file beneath the approved checkout;
2. reject path traversal;
3. reject symlinks escaping the approved checkout;
4. hash the current file;
5. compare it with the indexed file hash;
6. return the slice only when the hashes match.

On mismatch:

```json
{
  "source_included": false,
  "source_omission_reason": "working_tree_changed",
  "refresh_recommended": true
}
```

The alpha indexer may read only tracked files within the configured checkout and the Go metadata required to analyse them.

## 4.9 Gateway-managed skills integration

The generated integration manifest gains:

```text
.agents/skills/wormhole-code-graph/SKILL.md
```

The skill should teach the agent:

### Use Code Graph when

* beginning a code task in an unfamiliar area;
* tracing an implementation path;
* locating callers or references;
* finding where a type or interface is used;
* identifying the code responsible for an error;
* narrowing the files required for review;
* another Wormhole object references a symbol or source location.

### Do not use Code Graph when

* reading an already-known file;
* inspecting arbitrary prose or assets;
* querying untracked or ignored files;
* the graph reports `disabled` or `error`;
* strict current source is required but the graph is stale;
* filesystem inspection is inherently simpler;
* the task concerns code outside the approved checkout.

### Default agent sequence

```text
inspect code_graph.status
→ query Code Graph with a bounded source budget
→ inspect returned path and source slices
→ request targeted follow-up symbols if needed
→ use ordinary filesystem tools only for unresolved context
```

The skill must state:

> Code Graph narrows source discovery. It does not replace Git, the working tree, direct verification, builds, or tests.

The general `wormhole-operating-loop` skill should be amended to say:

> For code tasks, use Code Graph before broad repository search when it is enabled and sufficiently current.

## 4.10 Canonical tool guidance

The Gateway tool-guidance inventory must include, for every Code Graph tool:

* purpose;
* use when;
* do not use when;
* permissions;
* freshness implications;
* source-access implications;
* minimal request example;
* expected follow-up;
* misuse warning.

CI must fail when a Code Graph tool exists without matching model guidance.

## 4.11 Benchmark corpus

The first checked-in corpus should exercise the Wormhole repository itself:

```text
Where is agent registration authenticated?
Trace a task status update into its emitted event.
Where are Gateway sync response versions validated?
Which code writes Passport audit records?
Trace a local task write into the durable outbound queue.
Where is project isolation enforced for local SQLite queries?
```

For each query record:

* expected entry symbols;
* expected files;
* expected relationship path;
* authoritative versus heuristic edges;
* source bytes returned;
* irrelevant source bytes;
* omitted results;
* query duration;
* whether the returned context was sufficient to begin work.

No performance threshold should be invented before measurements exist.

## 4.12 Automated acceptance

Tests must prove:

* Code Graph is disabled by default.
* Enablement rejects a mismatched Git remote.
* Only tracked Go files are indexed.
* Complete source bodies are not stored.
* Deterministic symbol IDs survive body-only edits.
* Signature changes may produce a new identity.
* Every edge references existing nodes.
* Candidate revisions remain invisible.
* Failed rebuilds preserve the active revision.
* Queries return one coherent revision.
* Source budgets are enforced.
* Truncation is explicit.
* Source slices require separate permission.
* Source hashes are revalidated.
* Cross-project graph access is rejected.
* Path traversal and symlink escape are rejected.
* Agent-triggered rebuild cannot activate Warpspeed.
* Disablement removes all local graph state.
* Two MCP clients observe the same local contract.

## Exit criteria

The Code Graph Alpha Slice is complete when:

1. It can be enabled for the Wormhole repository.
2. A coherent Go structural graph is constructed.
3. A fresh agent can retrieve a bounded path for each benchmark query.
4. Returned source slices match the indexed file hashes.
5. Query and source permissions are independently enforced.
6. Failed rebuilds leave the previous graph available.
7. Generated skills teach a fresh model to use the feature appropriately.
8. At least two different models or harnesses can call the same Gateway tools.
9. Dogfood evaluation records whether Code Graph reduces broad source reading.
10. The project makes no claim that full Code Graph V1 is complete.

---

# Revised generated-guidance milestone

The previous generated AGENTS and SKILLS milestone becomes Milestone 5.

Its tool inventory must now include:

* Core Gateway tools;
* approved integration-guidance tools;
* Code Graph alpha tools.

Generated instructions must be capability-aware:

```text
if Code Graph is ready:
    query it before broad code discovery
else:
    continue with normal filesystem and repository tools
```

The generated instructions must not assume Code Graph is installed, enabled, current, or authorized.

---

# Revised alpha acceptance loop

The real alpha acceptance scenario gains a code-discovery requirement.

## Agent A code-discovery path

Before broad repository inspection, Agent A should:

1. inspect `wormhole.code_graph.status`;
2. use `wormhole.code_graph.query` when the graph is ready;
3. request a bounded context package;
4. inspect direct source files only after reviewing the graph result;
5. complete normal Git and test verification independently.

The evaluation must compare against a baseline task performed without Code Graph.

Measure:

* files opened before the first correct edit;
* total source bytes exposed;
* broad search operations;
* time or turns before locating the relevant implementation path;
* correctness of selected files;
* task outcome;
* human corrections;
* Code Graph calls that produced no useful value.

Code Graph passes the alpha experiment only if the evidence shows useful narrowing without degrading correctness.

## Agent B review path

The reviewer should use Code Graph to:

* trace the changed implementation path;
* identify callers and affected types;
* narrow review context;
* verify findings against Git and the working tree.

The reviewer must not treat graph relationships as proof where confidence or provenance indicates heuristic resolution.

---

# Revised Definition of Alpha Validated

Wormhole reaches Alpha Validated when:

1. Gateway owns enrolment, bootstrap, synchronization, and normal operation.
2. An enrolled Gateway remains useful while Fabric is unavailable.
3. Shared KB retrieval is meaning-bearing.
4. Gateway manages safe, declarative AGENTS and SKILLS templates.
5. Every exposed agent-facing tool has current usage guidance.
6. Code Graph can locally index the Wormhole Go repository.
7. Code Graph returns bounded, coherent, source-validated code paths.
8. Fresh models use Code Graph appropriately through generated guidance.
9. Two different agents complete the shared-context handoff.
10. The alpha evaluation measures whether Code Graph reduces broad source reading.
11. At least three external users complete a controlled trial.
12. No beta compatibility or full Code Graph V1 claim has been made.

---

# Revised pull-request sequence

```text
PR 1   Rebaseline roadmap and issues
PR 2   Gateway enrolment request and lifecycle design
PR 3   Gateway-owned registration and credentials
PR 4   Gateway bootstrap ownership and lifecycle tests
PR 5   Offline Gateway restart
PR 6   Integration-manifest design
PR 7   Code Graph alpha storage and revision model
PR 8   Git-tracked Go inventory and semantic adapter
PR 9   Structural query and bounded source assembly
PR 10  Code Graph MCP tools and permissions
PR 11  Code Graph CLI lifecycle and destructive disablement
PR 12  Code Graph benchmark and security hardening
PR 13  Tool-guidance metadata and contract coverage
PR 14  Generated orientation, operating-loop, and Code Graph skills
PR 15  Safe AGENTS and SKILLS materialization
PR 16  Read-only model guidance through Gateway
PR 17  Shared KB semantic embedding implementation
PR 18  Shared KB semantic ranking and migration tests
PR 19  Fabric manifest storage and bootstrap distribution
PR 20  Gateway manifest verification and approval
PR 21  Full automated alpha acceptance loop
PR 22  Manual two-model and Code Graph validation
PR 23  Closed-trial instrumentation and operator guide
```
