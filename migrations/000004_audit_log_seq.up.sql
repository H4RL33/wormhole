-- Same-transaction audit_log inserts (e.g. Store.Register writing
-- agent.registered/passport.issued/token.issued in one tx) share an
-- identical created_at (Postgres now() is transaction-constant), so
-- created_at alone cannot order same-transaction rows. A monotonic
-- sequence column gives ListAuditTrail a real total order.
ALTER TABLE audit_log ADD COLUMN seq bigserial;
