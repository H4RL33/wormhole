ALTER TABLE events DROP CONSTRAINT events_event_type_check;
ALTER TABLE events ADD CONSTRAINT events_event_type_check
    CHECK (event_type IN ('task.status_changed', 'review.requested', 'build.failed', 'discovery.logged', 'message.posted'));
