# Knowledge Base Schema Draft

Atomic knowledge articles linked explicitly as a graph, per RFC-0001 §8.3. Compliance checks run server-side on write; exact thresholds are config tunable, not hardcoded, and are deferred to the implementation task that wires `wormhole.kb.write`.

## kb_articles

- `id`
- `project_id` -> projects
- `title`
- `body`
- `frontmatter` (jsonb)
- `embedding` (vector, pgvector)
- `author_agent_id` -> agents
- `created_at`
- `updated_at`

Atomic articles per RFC §8.3: one article = one fact, decision, or procedure.

## kb_links

- `id`
- `from_article_id` -> kb_articles
- `to_article_id` -> kb_articles

Explicit `[[link]]`-style linking, graph not folder tree (RFC §8.3).

## Compliance Checks on Write

Every KB article contribution is subject to server-side checks per RFC-0001 §8.3 and architecture.md §6:

- **Semantic deduplication.** Incoming article is checked against existing articles by embedding similarity; articles above a threshold are candidates for merging or rejection with a rewrite suggestion.
- **Conciseness.** Articles exceeding a length ceiling are rejected with a rewrite prompt, not silently accepted.
- **Required links.** Depending on article type (declarative policy, architectural decision, procedure), outbound links to related articles may be required; missing links trigger a soft rejection with link suggestions.

All three checks follow a soft-reject-with-rewrite-suggestion model, not hard blocks: the agent receives a structured rejection carrying the closest conflicting articles or link recommendations and can revise and resubmit. Exact thresholds (similarity ceiling, length ceiling, required link counts per article type) are RFC-0001 §15 open question territory and are deferred to the implementation task that wires `wormhole.kb.write`, not decided in this draft. They will be tunable config constants, not hardcoded.

## RFC-0001 §8.3 Scope Note

RFC-0001 §8.3 specifies the design constraints (atomic articles, explicit linking, compliance checks, semantic search, model-agnostic format) but does not specify exact column names or types for `kb_articles` and `kb_links`. This sketch is a reasonable extension for the next implementer to start from, not an RFC-literal schema.
