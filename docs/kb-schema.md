# Knowledge Base Schema Draft

Atomic knowledge articles linked explicitly as a graph, per RFC-0001 §8.3. KB
reads and writes are strictly project-scoped per the RFC-0001 §15 Decision
Register; a multi-project runtime keeps separate namespaces rather than an
implicit merged KB. Compliance checks run server-side on write and soft-reject
with structured rewrite suggestions. Exact thresholds remain tunable
configuration rather than architecture.

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

All three checks follow the soft-reject-with-rewrite-suggestion model decided
in the RFC-0001 §15 Decision Register, not hard blocks: the agent receives a
structured rejection carrying the closest conflicting articles or link
recommendations and can revise and resubmit. Exact thresholds (similarity
ceiling, length ceiling, required link counts per article type) are tunable
config constants, not hardcoded architectural choices.

## RFC-0001 §8.3 Scope Note

RFC-0001 §8.3 specifies the design constraints (atomic articles, explicit linking, compliance checks, semantic search, model-agnostic format) but does not specify exact column names or types for `kb_articles` and `kb_links`. This sketch is a reasonable extension for the next implementer to start from, not an RFC-literal schema.
