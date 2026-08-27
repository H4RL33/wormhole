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
- `embedding` (legacy nullable vector; compatibility-only after migration 19)
- `author_agent_id` -> agents
- `created_at`
- `updated_at`

Atomic articles per RFC §8.3: one article = one fact, decision, or procedure.

## kb_links

- `id`
- `from_article_id` -> kb_articles
- `to_article_id` -> kb_articles

Explicit `[[link]]`-style linking, graph not folder tree (RFC §8.3).

## kb_embedding_generations

- `id`
- `project_id` -> projects
- `provider`, `model`, `version`, `dimension`
- `state` (`building`, `active`, `failed`, `retired`)
- safe `failure_code` plus lifecycle timestamps

Only one generation can be active per project. A replacement is built beside
the current generation and can activate only when it contains exactly one
embedding for every current article. Activation and retirement of the previous
generation are atomic; failed or incomplete rebuilds leave the old active
generation and its vectors untouched.

## kb_article_embeddings

- `project_id` -> projects
- `article_id` -> kb_articles
- `generation_id` -> kb_embedding_generations
- `provider`, `model`, `version`, `dimension`
- `content_hash` (SHA-256 of `title + "\n\n" + body`)
- `embedding` (`vector(1024)`, pgvector)
- `created_at`

Composite foreign keys bind every vector's project and immutable model metadata
to its article and generation. Both embedding tables have project-scoped RLS.

## Ranking and generation pinning

Search selects one active generation inside its project-scoped transaction,
reports that generation and its provider/model/version/dimension in ranking
metadata, then runs cosine ranking with both `project_id` and `generation_id`
in the SQL predicate. An activation that commits between those two statements
does not mix generations: retired vectors remain available to that in-flight
search. PostgreSQL RLS independently hides both generation and vector rows from
other project scopes.

## Re-embedding and recovery

`Store.RebuildEmbeddingGeneration` is the operational core workflow. It is not
an agent MCP mutation or CLI command. Trusted Fabric process orchestration runs
it with a maintenance Store configured for the target Embedder while the
serving Store retains the active generation's Embedder until the swap:

1. A `building` candidate is created while the old `active` generation keeps
   serving searches and writes.
2. Current articles are embedded in batches of at most 64. Remote calls occur
   outside transactions; each resulting batch is stored in a short
   project-scoped transaction with its article content hash.
3. Finalization takes the same project advisory lock as article writes. It
   compares every current article and content hash to the candidate. Missing or
   changed articles are re-embedded after releasing the lock, and finalization
   retries.
4. Once the locked snapshot is exact and the descriptor/dimension match the
   configured Embedder, the old generation is retired and the candidate is
   activated in one transaction.
5. Provider or contract failure marks only the candidate `failed` with a safe
   machine code. The previous active generation and vectors are unchanged.

To recover a prior provider/model/version, restore that approved Embedder
configuration and run the same workflow. If the newest retired generation for
that descriptor still has exact hashes for every current article, it is
reactivated atomically without a paid provider call. Otherwise a fresh
candidate is rebuilt. Do not update generation states manually.

Migration 19 deliberately does not promote or copy the compatibility-only
`kb_articles.embedding` values produced by earlier test stubs. Legacy article
rows and their nullable, variable-dimension vectors survive upgrade and
rollback, but no semantic generation is synthesized for them and production
search never reads that column.

## Production embedding contract

Fabric uses Cohere `embed-v4.0`, version `4.0`, at the fixed
`https://api.cohere.com/v1/embed` endpoint with 1024-dimensional float vectors
and cosine distance. Fabric authenticates with
`WORMHOLE_COHERE_API_KEY`; it sends article input as `title + "\n\n" + body`
with `search_document`, and a search query with `search_query`. Requests set
`embedding_types=["float"]`, `output_dimension=1024`, `max_tokens=8192`, and
`truncate="NONE"`.

Interactive writes and searches use a five-second total budget, at most two
attempts, and a two-second request timeout. Re-embedding batches use a
30-second total budget and at most three attempts. Network failures and HTTP
408, 429, 500, 502, 503, and 504 are retried with full-jitter exponential
backoff; `Retry-After` is honored only when it fits the remaining deadline.
Authentication, input, response-contract, and dimension errors are not
retried. Provider calls occur outside Postgres transactions.

The provider is a paid external processor. Article/query input is permitted
only when classified non-sensitive unless a separately verified zero-data-
retention agreement applies. Credentials, input text, response bodies, and
vectors are never logged.

Provider or active-index failure is fail-closed for write and search. Search
returns a structured tool error with `semantic_ranking=false`,
`degraded=true`, `fallback="none"`, and retryability metadata; it never returns
lexical results labelled as semantic ranking.

The paid-provider acceptance test is disabled by default. Set
`WORMHOLE_COHERE_LIVE_TEST=1` together with `WORMHOLE_COHERE_API_KEY` to run
`TestCohereLiveLowOverlapRankingOptIn`; default CI and local test runs never
contact Cohere. The test emits neither the key nor article/query text.

`generated_guidance_requirements.fabric_semantic_search` in
`testdata/alpha/kb/semantic-low-overlap.json` is the canonical semantic-ranking
fixture contract. It describes an optional Fabric/Core capability and is
asserted by the real-Postgres ranking test. Stage 2 Gateway instead exposes
deterministic `kb.list` and `kb.get`; it neither advertises semantic KB search
nor Code Graph. Generated live Gateway guidance is validated separately from
the Stage 2 contract inventory.

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
