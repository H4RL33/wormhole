DROP TABLE IF EXISTS kb_links;
DROP TABLE IF EXISTS kb_articles;
-- Deliberately not dropping the vector extension: extensions are
-- shared/system-level, not per-feature, matching how pgcrypto is never
-- dropped by any existing down migration.
