# Day 9: Event Log Schema + Channel Model

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the database migration and the Go core models/Store for the Communication pillar's event log and channels (RFC-0001 §7.1, §8.1). This includes the database schema migration with full RLS treatment, the `channels` and `events` core storage models, and integration tests verifying transactional safety, isolation, and scoping.

**Architecture:**
- Create migration `000007_event_channels` containing `channels` and `events` tables, foreign keys, indexes, and RLS policies.
- Create `internal/core/events` package containing `Store`, `Channel`, and `Event` models.
- All database queries for events and channels run within transactions with local `wormhole.project_id` set via `set_config` to enforce RLS.
- Implement methods:
  - `Store.CreateChannel(ctx, projectID, name)`
  - `Store.ListChannels(ctx, projectID)`
  - `Store.GetChannel(ctx, projectID, channelID)`
  - `Store.PublishEvent(ctx, projectID, channelID, agentID, eventType, payload, note)`
  - `Store.ListEvents(ctx, projectID, channelID, limit, offset)`
- Integration tests in `internal/core/events/events_test.go` cover CRUD operations, RLS isolation, and invalid event types.

**Tech Stack:** Go, PostgreSQL (`database/sql` + `lib/pq`), `golang-migrate`

## Global Constraints

- Run all database operations within transactions.
- Set RLS context at the start of every transaction.
- Do not use em-dashes (commas, colons, semicolons, parentheses instead).
- Validate that the `event_type` is one of the five allowed: `task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`, or `message.posted`.

---

### Task 1: Database Migration for Event and Channels

**Files:**
- Create: `migrations/000007_event_channels.up.sql`
- Create: `migrations/000007_event_channels.down.sql`

**Interfaces:**
- Consumes: Existing migrations
- Produces: `channels` and `events` tables in the database with RLS enabled

- [ ] **Step 1: Write UP migration**
  Create `migrations/000007_event_channels.up.sql`:
  - `channels` table: `id` (uuid PK default `gen_random_uuid()`), `project_id` (uuid REFERENCES projects(id) ON DELETE CASCADE), `name` (text NOT NULL), `created_at` (timestamptz NOT NULL DEFAULT now()).
  - `events` table: `id` (uuid PK default `gen_random_uuid()`), `project_id` (uuid REFERENCES projects(id) ON DELETE CASCADE), `channel_id` (uuid REFERENCES channels(id) ON DELETE CASCADE), `agent_id` (uuid REFERENCES agents(id) ON DELETE CASCADE), `event_type` (text NOT NULL), `payload` (jsonb NOT NULL DEFAULT '{}'), `note` (text), `created_at` (timestamptz NOT NULL DEFAULT now()).
  - Check constraint on `event_type` to allow only `task.status_changed`, `review.requested`, `build.failed`, `discovery.logged`, or `message.posted`.
  - Indexes on `channels(project_id)` and `events(project_id, channel_id, created_at)`.
  - Enable RLS on both tables.
  - Create isolation policies named `channels_project_isolation` and `events_project_isolation` matching:
    `USING (project_id = current_setting('wormhole.project_id', true)::uuid)`

- [ ] **Step 2: Write DOWN migration**
  Create `migrations/000007_event_channels.down.sql`:
  - Drop tables `events` and `channels`.

- [ ] **Step 3: Run migration UP and DOWN**
  Verify the migrations apply cleanly against local Postgres.
  Run: `migrate -path migrations -database "$DATABASE_URL" up` and `down` (then `up` again).

- [ ] **Step 4: Commit migration files**
  Commit the migration files.

---

### Task 2: Core events.Store and Models

**Files:**
- Create: `internal/core/events/events.go`
- Create: `internal/core/events/events_test.go`

**Interfaces:**
- Consumes: Database schema from Task 1
- Produces:
  - `type Channel struct { ID, ProjectID, Name string; CreatedAt time.Time }`
  - `type Event struct { ID, ProjectID, ChannelID, AgentID, EventType string; Payload json.RawMessage; Note *string; CreatedAt time.Time }`
  - `func NewStore(db *sql.DB) *Store`
  - `func (s *Store) CreateChannel(ctx context.Context, projectID, name string) (Channel, error)`
  - `func (s *Store) ListChannels(ctx context.Context, projectID string) ([]Channel, error)`
  - `func (s *Store) GetChannel(ctx context.Context, projectID, channelID string) (Channel, error)`
  - `func (s *Store) PublishEvent(ctx context.Context, projectID, channelID, agentID, eventType string, payload json.RawMessage, note *string) (Event, error)`
  - `func (s *Store) ListEvents(ctx context.Context, projectID, channelID string, limit, offset int) ([]Event, error)`

- [ ] **Step 1: Write the failing tests**
  Create `internal/core/events/events_test.go` with integration tests using `testStore` helper pattern:
  - `TestCreateChannel_Success`: creates a channel and verifies fields.
  - `TestListChannels_Scoping`: lists channels filtered by project.
  - `TestPublishEvent_Success`: publishes event and verifies payload / type constraints.
  - `TestPublishEvent_InvalidTypeRejected`: verifies publishing an invalid event type is rejected.
  - `TestListEvents_Scoping`: verifies event scoping.
  - `TestRLSIsolation`: verifies RLS under restricted role.

- [ ] **Step 2: Run tests to verify failure**
  Run: `go test ./internal/core/events/ -v`
  Expected: Compile errors.

- [ ] **Step 3: Implement Store and Models**
  Create `internal/core/events/events.go`. Implement methods ensuring:
  - Every DB operation runs inside a transaction.
  - `set_config('wormhole.project_id', projectID, true)` is set at the start of every transaction.
  - `event_type` is validated in Go code as well to return `ErrInvalidEventType`.

- [ ] **Step 4: Run and pass tests**
  Run: `go test ./internal/core/events/ -v`
  Expected: PASS.

- [ ] **Step 5: Run full test suite**
  Run: `go test ./...`
  Expected: PASS.

- [ ] **Step 6: Commit core events package**
  Commit `internal/core/events/events.go` and `internal/core/events/events_test.go`.
