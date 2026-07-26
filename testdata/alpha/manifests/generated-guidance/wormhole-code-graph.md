---
name: wormhole-code-graph
description: Use when a code task may benefit from bounded local structural discovery through the Wormhole Code Graph.
---

# Wormhole Code Graph

Use Code Graph only for project-local source structure and bounded code discovery when it is enabled and current; it is separate from the shared KB and does not replace Git or verification.

## Use Code Graph when

- beginning a code task in an unfamiliar area;
- tracing an implementation path, callers, references, types, or an error;
- narrowing files required for review;
- a Wormhole object references a symbol or source location.

## Do not use Code Graph when

- reading an already-known file or inspecting non-code prose or assets;
- querying untracked or ignored files, or code outside the approved checkout;
- status is disabled or error;
- strict current source is required while the graph is stale;
- direct filesystem inspection is inherently simpler.

## Sequence

1. Inspect wormhole.code_graph.status first.
2. Verify Git HEAD and working tree state directly.
3. When enabled and sufficiently current, call wormhole.code_graph.query with a bounded source budget.
4. Source slices require the separate code_graph.source.read permission.
5. Inspect returned paths and slices, then request targeted follow-up symbols.
6. Use ordinary filesystem tools only for unresolved context.

Heuristic edges are discovery hypotheses, not proof of complete relationships.
Code Graph narrows source discovery. It does not replace Git, the working tree,
direct verification, builds, or tests.
