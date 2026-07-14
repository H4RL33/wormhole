-- Task 3 (2026-07-14-local-runtime-functional-alpha, Day 32 Chapter 8):
-- wormhole.sync.conflict_report needs to log every last-write-wins
-- resolution to the append-only audit trail. Per Global Constraints, this
-- reuses internal/core/events as the audit trail (no new core primitive):
-- publish a "sync.conflict_resolved" event carrying both the losing
-- (client) and winning (server) values. That event_type must be added to
-- the events table's CHECK constraint alongside events.AllowedEventTypes.

ALTER TABLE events DROP CONSTRAINT events_event_type_check;
ALTER TABLE events ADD CONSTRAINT events_event_type_check
    CHECK (event_type IN ('task.status_changed', 'review.requested', 'build.failed', 'discovery.logged', 'message.posted', 'sync.conflict_resolved'));
