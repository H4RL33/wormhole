-- RFC-0001/RFC-0002 do not specify a token TTL; 30 days is an inferred
-- alpha default (docs/superpowers/plans/2026-07-11-day5-mcp-wiring.md),
-- not an RFC value. Existing rows get an expiry 30 days from now so the
-- backfill doesn't retroactively invalidate live tokens.
ALTER TABLE agent_tokens ADD COLUMN expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 days');
ALTER TABLE agent_tokens ALTER COLUMN expires_at DROP DEFAULT;
