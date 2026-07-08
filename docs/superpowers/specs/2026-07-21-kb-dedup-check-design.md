# Knowledge Base Semantic Deduplication Design

This specification defines the semantic deduplication compliance check (dedup check) for Knowledge Base article contributions, as mandated by RFC-0001 §8.3 and architecture.md §6.

---

## 1. Goal

Prevent duplicate knowledge storage by rejecting or warning agents when they attempt to write an article that is semantically similar to an existing article in the same project. If needed, the check can be bypassed via a `force` flag.

## 2. Architecture & Components

- **Configuration:** `KBDedupThreshold` (float64, default 0.85) is loaded from the environment variable `WORMHOLE_KB_DEDUP_THRESHOLD`.
- **Database Query:** Uses pgvector's cosine distance operator `<=>` to find the nearest article in the same project:
  ```sql
  SELECT id, title, (1 - (embedding <=> $1::vector)) AS similarity
  FROM kb_articles
  WHERE project_id = $2 AND embedding IS NOT NULL
  ORDER BY embedding <=> $1::vector
  LIMIT 1
  ```
- **Error Model:** On violation (where `similarity >= threshold`), the transaction rolls back and returns a custom `ErrDedupViolation` error.
- **MCP Response:** The `wormhole.kb.write` tool returns a structured JSON error string inside the `CallResponse.Error` field to provide a machine-readable rejection and suggestions.

## 3. Data Flow

1. Client calls `wormhole.kb.write` with `title`, `body`, `links`, and an optional `force` boolean.
2. The server runs the authentication middleware and starts a database transaction.
3. The server sets `wormhole.project_id` and checks passports.
4. The server embeds the new article's `body` via `Embedder.Embed`.
5. If `force` is `false`:
   - Query the closest existing article in the same project.
   - If the similarity of the closest article is `>= KBDedupThreshold`:
     - Roll back the transaction.
     - Return an `ErrDedupViolation` error containing the details of the closest article.
6. If `force` is `true` or no duplicate is found:
   - Insert the new article and links.
   - Commit the transaction.

## 4. MCP Schema

### Input: `WriteArticleInput`
- `title` (string)
- `body` (string)
- `frontmatter` (RawMessage, optional)
- `links` ([]string, optional)
- `force` (boolean, optional)

### Rejection Output: `CallResponse`
```json
{
  "error": "{\"error\":\"kb: write article: semantic duplicate found\",\"code\":\"DEDUP_VIOLATION\",\"closest_article\":{\"id\":\"uuid-of-existing\",\"title\":\"Title of existing article\",\"similarity\":0.895},\"suggestion\":\"The article is too similar to 'Title of existing article' (similarity 0.895000 >= threshold 0.850000). Use the existing article, update it, or set the 'force' parameter to true to write it anyway.\"}"
}
```

## 5. Security & Isolation

- Checked within the same transaction using `set_config('wormhole.project_id', ...)` to satisfy RLS policies.
- Ensures duplicate check is strictly project-scoped.

## 6. Testing Strategy

- **TestWriteArticle_DedupViolation:** Happy path rejection.
- **TestWriteArticle_DedupBypass:** Force option successfully inserts.
- **TestWriteArticle_DedupCrossProject:** Rejects duplicates only within the same project.
- **TestMcp_WriteArticle_DedupViolation:** Verifies the MCP tool returns the structured JSON error.
