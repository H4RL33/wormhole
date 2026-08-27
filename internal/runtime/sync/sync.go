package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
)

// SyncProtocolVersion is the Gateway-side version sent on every Fabric sync
// request. It intentionally duplicates internal/mcp.SyncProtocolVersion
// because runtime packages cannot import internal/mcp.
const SyncProtocolVersion = 1

// IntegrationManifestReceiver is the narrow boundary between transport sync
// and Gateway's authoritative manifest verifier/cache. The project, Passport,
// and roles are derived from the authenticated sync identity, never from the
// manifest body.
type IntegrationManifestReceiver interface {
	ReceiveIntegrationManifest(ctx context.Context, projectID, passportID string, roles []string, raw json.RawMessage) error
}

type bootstrapIntegrationManifestRollback interface {
	RollbackBootstrapIntegrationManifest(ctx context.Context, projectID, passportID string, roles []string, raw json.RawMessage) error
}

// CredentialSource resolves only profile-owned credential references. The
// returned secret is used for one request and is never retained by Engine.
type CredentialSource interface {
	Read(context.Context, string) (string, error)
}

type FabricRouteSource interface {
	GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
}

// Engine orchestrates the local sync lifecycle: bootstrap, incremental push/pull,
// and batching (RFC-0003 §8). It holds per-org state including queue and audit repos.
type Engine struct {
	httpClient            *http.Client
	coordServer           string
	token                 string
	namespaceID           string
	workspaceScope        types.WorkspaceScope
	remoteKey             types.RemoteBindingKey
	profileID             string
	routeRepo             FabricRouteSource
	credentialSource      CredentialSource
	conflictGate          localstore.WorkspaceConflictGate
	queueRepo             *QueueRepo
	auditRepo             *AuditRepo
	taskRepo              *localstore.TaskRepo
	kbRepo                *localstore.KBRepo
	eventRepo             *localstore.EventRepo
	gitRepo               *localstore.GitRepo
	bootstrapStore        *localstore.Store
	expectedAgentID       string
	expectedPassportID    string
	bootstrapAttempt      *localstore.EnrolmentAttemptRecord
	integrationReceiver   IntegrationManifestReceiver
	authenticatedRoles    []string
	mu                    sync.Mutex
	stateMu               sync.RWMutex
	connectionState       ConnectionState
	lastSyncCursor        string
	batchInterval         time.Duration
	batchSize             int
	latencyCheckInterval  time.Duration
	pullInterval          time.Duration
	highPriorityThreshold int
	startOnce             sync.Once
	stopOnce              sync.Once
	lifecycleMu           sync.Mutex
	cancel                context.CancelFunc
	stopped               bool
	wg                    sync.WaitGroup
	syncErrorReporter     func(error)
	// testCallSyncToolWithResultFn is for testing only: if set, overrides callSyncToolWithResult.
	testCallSyncToolWithResultFn func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
}

// ConfigureEventAndGitReplicas wires the remaining durable incremental
// pull targets without widening New's long-standing constructor signature.
func (e *Engine) ConfigureEventAndGitReplicas(events *localstore.EventRepo, gitLinks *localstore.GitRepo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.eventRepo = events
	e.gitRepo = gitLinks
}

// ConfigureIntegrationManifestReceiver routes Fabric bootstrap and
// incremental manifest records through the same Gateway verifier/cache.
func (e *Engine) ConfigureIntegrationManifestReceiver(receiver IntegrationManifestReceiver) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.integrationReceiver = receiver
}

// ConfigureBootstrap supplies the durable snapshot target and the identity
// references authenticated by the credential used for this engine.
func (e *Engine) ConfigureBootstrap(store *localstore.Store, agentID, passportID string, attempt *localstore.EnrolmentAttemptRecord) error {
	if store == nil || e.namespaceID == "" || agentID == "" || passportID == "" {
		return errors.New("sync: invalid bootstrap configuration")
	}
	e.bootstrapStore = store
	e.expectedAgentID = agentID
	e.expectedPassportID = passportID
	e.bootstrapAttempt = attempt
	return nil
}

// Config holds tunable sync batching parameters (RFC-0003 §8.2).
type Config struct {
	BatchInterval         time.Duration // time-based batching threshold
	BatchSize             int           // queue-size batching threshold
	LatencyCheckInterval  time.Duration // how often to check for high-priority entries needing an immediate push
	PullInterval          time.Duration // how often to pull server-side changes
	HighPriorityThreshold int           // queue entries with Priority >= this bypass BatchInterval
}

// DefaultConfig returns conservative batching defaults: 5 sec interval, 50
// item batch, 10 sec pull interval, and high-priority entries (priority >= 2)
// checked every 500ms instead of waiting the full 5 sec.
func DefaultConfig() Config {
	return Config{
		BatchInterval:         5 * time.Second,
		BatchSize:             50,
		LatencyCheckInterval:  500 * time.Millisecond,
		PullInterval:          10 * time.Second,
		HighPriorityThreshold: 2,
	}
}

// New creates a new sync engine for one (org, project) binding. taskRepo/
// kbRepo are the local-apply targets for Bootstrap/PullIncremental (RFC-0003
// §8.1/§8.2); either may be nil for callers that only exercise push (e.g.
// existing unit tests here), in which case a pull response with a non-empty
// task_list/kb_list is an error rather than a silent no-op.
func New(coordServerURL, token, namespaceID string, queueRepo *QueueRepo, auditRepo *AuditRepo, taskRepo *localstore.TaskRepo, kbRepo *localstore.KBRepo, cfg Config) (*Engine, error) {
	if cfg.BatchInterval <= 0 {
		return nil, errors.New("sync: invalid config: BatchInterval must be greater than zero")
	}
	if cfg.BatchSize <= 0 {
		return nil, errors.New("sync: invalid config: BatchSize must be greater than zero")
	}
	if cfg.LatencyCheckInterval <= 0 {
		return nil, errors.New("sync: invalid config: LatencyCheckInterval must be greater than zero")
	}
	if cfg.PullInterval <= 0 {
		return nil, errors.New("sync: invalid config: PullInterval must be greater than zero")
	}

	return &Engine{
		httpClient:            &http.Client{Timeout: 30 * time.Second},
		coordServer:           coordServerURL,
		token:                 token,
		namespaceID:           namespaceID,
		queueRepo:             queueRepo,
		auditRepo:             auditRepo,
		taskRepo:              taskRepo,
		kbRepo:                kbRepo,
		batchInterval:         cfg.BatchInterval,
		batchSize:             cfg.BatchSize,
		latencyCheckInterval:  cfg.LatencyCheckInterval,
		pullInterval:          cfg.PullInterval,
		highPriorityThreshold: cfg.HighPriorityThreshold,
		connectionState:       StateOffline,
		syncErrorReporter: func(err error) {
			log.Printf("wormhole sync namespace=%q: %v", namespaceID, err)
		},
	}, nil
}

// NewRouted creates a v2 engine pinned to one immutable workspace/Fabric route.
// Profile URL and credential-reference rotations are resolved for every request.
func NewRouted(ctx context.Context, scope types.WorkspaceScope, routes FabricRouteSource,
	credentials CredentialSource, conflicts localstore.WorkspaceConflictGate,
	queueRepo *QueueRepo, auditRepo *AuditRepo, taskRepo *localstore.TaskRepo,
	kbRepo *localstore.KBRepo, cfg Config) (*Engine, error) {
	if routes == nil || credentials == nil || conflicts == nil || queueRepo == nil || auditRepo == nil {
		return nil, errors.New("sync: routed engine requires routes, credentials, conflict gate, queue, and audit")
	}
	binding, profile, err := routes.GetRoute(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("sync: resolve initial Fabric route: %w", err)
	}
	engine, err := New(profile.BaseURL, "", scope.ProjectID, queueRepo, auditRepo, taskRepo, kbRepo, cfg)
	if err != nil {
		return nil, err
	}
	engine.workspaceScope = scope
	engine.remoteKey = binding.RemoteKey()
	engine.profileID = profile.ProfileID
	engine.routeRepo = routes
	engine.credentialSource = credentials
	engine.conflictGate = conflicts
	return engine, nil
}

// Start begins the background sync loop. Callers must call Stop to cleanly shut down.
func (e *Engine) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		e.lifecycleMu.Lock()
		defer e.lifecycleMu.Unlock()
		if e.stopped {
			return
		}

		loopCtx, cancel := context.WithCancel(ctx)
		e.cancel = cancel
		e.setConnectionState(StateSynchronizing)
		e.wg.Add(1)
		go e.syncLoop(loopCtx)
	})
}

// Stop stops the background sync loop and waits for it to finish.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		e.lifecycleMu.Lock()
		e.stopped = true
		cancel := e.cancel
		e.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		e.wg.Wait()
	})
}

// syncLoop periodically evaluates pending work and pushes batches to the server.
// Runs until ctx is cancelled or Stop() is called.
func (e *Engine) syncLoop(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(e.batchInterval)
	defer ticker.Stop()

	latencyTicker := time.NewTicker(e.latencyCheckInterval)
	defer latencyTicker.Stop()
	pullTicker := time.NewTicker(e.pullInterval)
	defer pullTicker.Stop()

	// Reconcile immediately on startup. An enrolled Gateway serves its local
	// socket independently, while this goroutine drains durable writes before
	// accepting any server-side pull into the replica.
	if err := e.syncOnce(ctx); err != nil {
		e.reportSyncError(ctx, err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.syncBatch(ctx)
		case <-latencyTicker.C:
			if err := e.checkLatencySensitive(ctx); err != nil {
				e.reportSyncError(ctx, err)
			}
		case <-pullTicker.C:
			if err := e.syncOnce(ctx); err != nil {
				e.reportSyncError(ctx, err)
			}
		}
	}
}

func (e *Engine) reportSyncError(ctx context.Context, err error) {
	if err == nil || ctx.Err() != nil || e.syncErrorReporter == nil {
		return
	}
	e.syncErrorReporter(err)
}

// syncBatch drains durable outbound writes for a batch tick. It deliberately
// does not pull: scheduled pulls and startup reconciliation use syncOnce.
// An empty batch preserves the last state. A pending batch moves ordinary
// states to synchronizing but preserves attention_required until a successful
// scheduled pull; failures become visible before observers receive the error.
func (e *Engine) syncBatch(ctx context.Context) {
	pending, err := e.queueRepo.PendingCount(ctx, e.remoteKey)
	if err != nil {
		e.setConnectionState(StateAttentionRequired)
		e.reportSyncError(ctx, fmt.Errorf("sync: inspect pending writes before batch: %w", err))
		return
	}
	if pending == 0 {
		return
	}
	e.stateMu.RLock()
	priorState := e.connectionState
	e.stateMu.RUnlock()
	if priorState != StateAttentionRequired {
		e.setConnectionState(StateSynchronizing)
	}
	if err := e.pushBatch(ctx); err != nil {
		if ctx.Err() == nil {
			e.setConnectionState(stateForSyncError(err))
		}
		e.reportSyncError(ctx, err)
	}
}

func (e *Engine) syncOnce(ctx context.Context) error {
	e.setConnectionState(StateSynchronizing)
	if err := e.pushBatch(ctx); err != nil {
		if ctx.Err() == nil {
			e.setConnectionState(stateForSyncError(err))
		}
		return err
	}
	pending, err := e.queueRepo.PendingCount(ctx, e.remoteKey)
	if err != nil {
		e.setConnectionState(StateAttentionRequired)
		return fmt.Errorf("sync: inspect pending writes: %w", err)
	}
	if pending > 0 {
		return nil
	}
	if err := e.PullIncremental(ctx); err != nil {
		if ctx.Err() == nil {
			e.setConnectionState(stateForSyncError(err))
		}
		return err
	}
	e.setConnectionState(StateOnline)
	return nil
}

// pushBatch retrieves pending entries up to batchSize and pushes to the server.
func (e *Engine) pushBatch(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	entries, err := e.queueRepo.ListPending(ctx, e.remoteKey, e.batchSize)
	if err != nil {
		return fmt.Errorf("sync: push batch: list pending: %w", err)
	}

	if len(entries) == 0 {
		return nil // nothing to push
	}

	// Construct incremental push payload: array of {entity_type, entity_id, operation, payload} objects.
	pushItems := make([]map[string]interface{}, len(entries))
	for i, entry := range entries {
		var payload interface{}
		if err := json.Unmarshal(entry.OperationJSON, &payload); err != nil {
			payload = string(entry.OperationJSON)
		}
		pushItems[i] = map[string]interface{}{
			"entity_type": string(entry.Operation.Kind),
			"entity_id":   entry.Operation.ID,
			"operation":   "apply",
			"payload":     payload,
		}
	}

	// Call wormhole.sync.incremental_push on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	// Use callSyncToolWithResult to get the response body for per-item error checking.
	result, err := e.callSyncToolWithResult(ctx, "wormhole.sync.incremental_push", map[string]interface{}{
		"namespace_id": e.namespaceID,
		"version":      SyncProtocolVersion,
		"items":        pushItems,
	})
	if err != nil {
		return fmt.Errorf("sync: push batch: call server: %w", err)
	}

	// Decode the response to extract per-item results (issue #15).
	// If decoding fails, treat conservatively: mark no entries delivered, let the
	// batch retry on the next cycle.
	pushResult, err := decodeIncrementalPushResult(result)
	if err != nil {
		return fmt.Errorf("sync: push batch: decode result: %w", err)
	}
	if pushResult.Version != SyncProtocolVersion {
		return fmt.Errorf("sync: push batch: %w: response version = %d, want %d", ErrAttentionRequired, pushResult.Version, SyncProtocolVersion)
	}

	acknowledgements, err := validatePushAcknowledgements(entries, pushResult)
	if err != nil {
		return fmt.Errorf("sync: push batch: invalid acknowledgement: %w", err)
	}

	// Mark only successful entries as delivered. Failed entries remain in the queue for retry.
	var rejected []string
	for _, entry := range entries {
		key := acknowledgementKey{entityType: string(entry.Operation.Kind), entityID: entry.Operation.ID}
		if acknowledgement := acknowledgements[key]; acknowledgement.Error == "" {
			if err := e.queueRepo.MarkDelivered(ctx, e.remoteKey, entry.ID); err != nil {
				// Earlier rows remain delivered; this row and all later rows remain
				// pending so the next cycle can retry without hiding local data loss.
				return fmt.Errorf("sync: push batch: mark queue entry %q delivered: %w", entry.ID, err)
			}
		} else {
			rejected = append(rejected, fmt.Sprintf("%s/%s: %s", entry.Operation.Kind, entry.Operation.ID, acknowledgement.Error))
		}
	}
	if len(rejected) > 0 {
		return fmt.Errorf("sync: push batch: %w: %s", ErrAttentionRequired, strings.Join(rejected, "; "))
	}

	return nil
}

type acknowledgementKey struct {
	entityType string
	entityID   string
}

func validatePushAcknowledgements(entries []QueueEntry, result incrementalPushResultWire) (map[acknowledgementKey]appliedItemWire, error) {
	if result.ItemsReceived != len(entries) {
		return nil, fmt.Errorf("items_received = %d, want %d", result.ItemsReceived, len(entries))
	}

	expected := make(map[acknowledgementKey]int, len(entries))
	for _, entry := range entries {
		key := acknowledgementKey{entityType: string(entry.Operation.Kind), entityID: entry.Operation.ID}
		expected[key]++
		if expected[key] != 1 {
			return nil, fmt.Errorf("sent pair (%q, %q) is not unique", key.entityType, key.entityID)
		}
	}

	acknowledgements := make(map[acknowledgementKey]appliedItemWire, len(result.Applied))
	for _, applied := range result.Applied {
		key := acknowledgementKey{entityType: applied.Type, entityID: applied.ID}
		if expected[key] != 1 {
			return nil, fmt.Errorf("unknown pair (%q, %q)", key.entityType, key.entityID)
		}
		if _, duplicate := acknowledgements[key]; duplicate {
			return nil, fmt.Errorf("duplicate pair (%q, %q)", key.entityType, key.entityID)
		}
		acknowledgements[key] = applied
	}

	for key := range expected {
		if _, ok := acknowledgements[key]; !ok {
			return nil, fmt.Errorf("missing pair (%q, %q)", key.entityType, key.entityID)
		}
	}
	return acknowledgements, nil
}

// checkLatencySensitive peeks the highest-priority pending entry and, if it
// meets highPriorityThreshold, pushes immediately rather than waiting for
// the next batchInterval tick (RFC-0003 §8.2 latency-sensitive bypass).
// ListPending already orders priority DESC, so the first row is the one
// that matters.
func (e *Engine) checkLatencySensitive(ctx context.Context) error {
	e.mu.Lock()
	entries, err := e.queueRepo.ListPending(ctx, e.remoteKey, 1)
	e.mu.Unlock()
	if err != nil {
		return fmt.Errorf("sync: check latency-sensitive: list pending: %w", err)
	}
	if len(entries) == 0 || entries[0].Priority < e.highPriorityThreshold {
		return nil
	}
	return e.pushBatch(ctx)
}

// PullIncremental fetches the latest state from the server for all entities,
// applying last-write-wins conflict resolution (RFC-0003 §8.3).
// Used during normal operation to stay in sync with server state.
func (e *Engine) PullIncremental(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending, err := e.queueRepo.ListPending(ctx, e.remoteKey, 1)
	if err != nil {
		return fmt.Errorf("sync: pull incremental: inspect pending writes: %w", err)
	}
	if len(pending) > 0 {
		return nil
	}

	// Call wormhole.sync.incremental_pull on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	args := map[string]interface{}{
		"namespace_id": e.namespaceID,
		"version":      SyncProtocolVersion,
	}
	if e.lastSyncCursor != "" {
		args["last_sync"] = e.lastSyncCursor
	}
	result, err := e.callSyncToolWithResult(ctx, "wormhole.sync.incremental_pull", args)
	if err != nil {
		return fmt.Errorf("sync: pull incremental: call server: %w", err)
	}

	pullResult, err := decodeIncrementalPullResult(result)
	if err != nil {
		return fmt.Errorf("sync: pull incremental: decode result: %w", err)
	}
	if pullResult.Version != SyncProtocolVersion {
		return fmt.Errorf("sync: pull incremental: %w: response version = %d, want %d", ErrAttentionRequired, pullResult.Version, SyncProtocolVersion)
	}
	_, err = time.Parse(time.RFC3339, pullResult.Timestamp)
	if err != nil {
		return fmt.Errorf("sync: pull incremental: decode timestamp %q: %w", pullResult.Timestamp, err)
	}
	for _, u := range pullResult.Updates {
		switch u.Type {
		case "task":
			var task taskSummaryWire
			if err := json.Unmarshal(u.Data, &task); err != nil {
				return fmt.Errorf("sync: pull incremental: decode task update: %w", err)
			}
			if err := e.applyTask(ctx, task); err != nil {
				return fmt.Errorf("sync: pull incremental: apply task: %w", err)
			}
		case "kb":
			var article articleSummaryWire
			if err := json.Unmarshal(u.Data, &article); err != nil {
				return fmt.Errorf("sync: pull incremental: decode kb update: %w", err)
			}
			if err := e.applyArticle(ctx, article); err != nil {
				return fmt.Errorf("sync: pull incremental: apply kb article: %w", err)
			}
		case "channel":
			var channel channelWire
			if err := json.Unmarshal(u.Data, &channel); err != nil {
				return fmt.Errorf("sync: pull incremental: decode channel update: %w", err)
			}
			if err := e.applyChannel(ctx, channel); err != nil {
				return fmt.Errorf("sync: pull incremental: apply channel: %w", err)
			}
		case "event":
			var event eventWire
			if err := json.Unmarshal(u.Data, &event); err != nil {
				return fmt.Errorf("sync: pull incremental: decode event update: %w", err)
			}
			if err := e.applyEvent(ctx, event); err != nil {
				return fmt.Errorf("sync: pull incremental: apply event: %w", err)
			}
		case "git_link":
			var link gitLinkWire
			if err := json.Unmarshal(u.Data, &link); err != nil {
				return fmt.Errorf("sync: pull incremental: decode Git link update: %w", err)
			}
			if err := e.applyGitLink(ctx, link); err != nil {
				return fmt.Errorf("sync: pull incremental: apply Git link: %w", err)
			}
		case "integration_manifest":
			if e.integrationReceiver == nil {
				return errors.New("sync: pull incremental: no integration manifest receiver")
			}
			roles, err := e.integrationManifestRoles(ctx)
			if err != nil {
				return fmt.Errorf("sync: pull incremental: load authenticated manifest binding: %w", err)
			}
			if err := e.integrationReceiver.ReceiveIntegrationManifest(ctx, e.namespaceID, e.expectedPassportID, roles, u.Data); err != nil {
				return fmt.Errorf("sync: pull incremental: receive integration manifest: %w", err)
			}
		default:
			return fmt.Errorf("sync: pull incremental: unknown update type %q", u.Type)
		}
	}

	e.lastSyncCursor = pullResult.Timestamp
	return nil
}

// Bootstrap performs a one-time bulk pull of the complete working environment
// (RFC-0003 §8.1). Used during org enrolment.
func (e *Engine) Bootstrap(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Call wormhole.sync.bootstrap on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	result, err := e.callSyncToolWithResult(ctx, "wormhole.sync.bootstrap", map[string]interface{}{
		"namespace_id": e.namespaceID,
		"version":      SyncProtocolVersion,
	})
	if err != nil {
		return fmt.Errorf("sync: bootstrap: call server: %w", err)
	}

	out, err := decodeBootstrapResult(result)
	if err != nil {
		return fmt.Errorf("sync: bootstrap: decode result: %w", err)
	}
	if err := validateBootstrapResult(out, e.namespaceID, e.expectedAgentID, e.expectedPassportID); err != nil {
		return fmt.Errorf("sync: bootstrap: validate result: %w", err)
	}
	if e.bootstrapStore == nil {
		return errors.New("sync: bootstrap: no local store configured")
	}
	metadata := bytes.TrimSpace(out.OrgConfig.IntegrationManifestMetadata)
	manifestReceived := false
	var manifestRoles []string
	if !bytes.Equal(metadata, []byte("null")) {
		if e.integrationReceiver == nil {
			return errors.New("sync: bootstrap: no integration manifest receiver")
		}
		manifestRoles = append([]string(nil), out.OrgConfig.Identity.Passport.Roles...)
		if err := e.integrationReceiver.ReceiveIntegrationManifest(ctx, e.namespaceID, e.expectedPassportID, manifestRoles, metadata); err != nil {
			return fmt.Errorf("sync: bootstrap: receive integration manifest: %w", err)
		}
		e.authenticatedRoles = manifestRoles
		manifestReceived = true
	}
	timestamp, _ := time.Parse(time.RFC3339Nano, out.Timestamp)
	if err := e.bootstrapStore.ApplyBootstrap(ctx, e.namespaceID, out.OrgConfig, timestamp, e.bootstrapAttempt); err != nil {
		if manifestReceived {
			rollback, ok := e.integrationReceiver.(bootstrapIntegrationManifestRollback)
			if !ok {
				return fmt.Errorf("sync: bootstrap: commit snapshot: %w; integration manifest receiver cannot roll back candidate", err)
			}
			if rollbackErr := rollback.RollbackBootstrapIntegrationManifest(ctx, e.namespaceID, e.expectedPassportID, manifestRoles, metadata); rollbackErr != nil {
				return fmt.Errorf("sync: bootstrap: commit snapshot: %w; roll back integration manifest: %v", err, rollbackErr)
			}
		}
		return fmt.Errorf("sync: bootstrap: commit snapshot: %w", err)
	}
	return nil
}

func (e *Engine) integrationManifestRoles(ctx context.Context) ([]string, error) {
	if len(e.authenticatedRoles) > 0 {
		return append([]string(nil), e.authenticatedRoles...), nil
	}
	if e.bootstrapStore == nil || e.expectedPassportID == "" {
		return nil, errors.New("authenticated Passport binding unavailable")
	}
	var raw string
	err := e.bootstrapStore.DB().QueryRowContext(ctx, `SELECT roles FROM passports
		WHERE namespace_id = ? AND id = ? AND project_id = ?`, e.namespaceID, e.expectedPassportID, e.namespaceID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var roles []string
	if err := json.Unmarshal([]byte(raw), &roles); err != nil {
		return nil, err
	}
	e.authenticatedRoles = append([]string(nil), roles...)
	return roles, nil
}

// taskSummaryWire mirrors internal/mcp.TaskSummary's JSON shape. This
// package cannot import internal/mcp (RFC-0003 §6.3 keeps internal/runtime/*
// and internal/mcp separate trees), so the wire contract is duplicated
// here, same as internal/runtime/localapi already does for the same reason.
type taskSummaryWire struct {
	TaskID       string     `json:"task_id"`
	ParentTaskID *string    `json:"parent_task_id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	OwnerAgentID *string    `json:"owner_agent_id"`
	Status       string     `json:"status"`
	Priority     int        `json:"priority"`
	DueBy        *time.Time `json:"due_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// articleSummaryWire mirrors internal/mcp.ArticleSummary's JSON shape.
type articleSummaryWire struct {
	ArticleID     string          `json:"article_id"`
	ProjectID     string          `json:"project_id"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Frontmatter   json.RawMessage `json:"frontmatter,omitempty"`
	AuthorAgentID string          `json:"author_agent_id"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type channelWire struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type eventWire struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	ChannelID string          `json:"channel_id"`
	AgentID   string          `json:"agent_id"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      *string         `json:"note"`
	CreatedAt time.Time       `json:"created_at"`
}

type gitLinkWire struct {
	GitLinkID string    `json:"git_link_id"`
	ProjectID string    `json:"project_id"`
	TaskID    string    `json:"task_id"`
	Repo      string    `json:"repo"`
	CommitSHA string    `json:"commit_sha"`
	Summary   string    `json:"summary"`
	AgentID   string    `json:"agent_id"`
	CreatedAt time.Time `json:"created_at"`
}

// bootstrapResultWire mirrors internal/mcp.BootstrapOutput's JSON shape.
type bootstrapResultWire struct {
	OrgConfig   types.BootstrapOrgConfigV1 `json:"org_config"`
	ProjectList []string                   `json:"project_list"`
	TaskList    []types.BootstrapTaskV1    `json:"task_list"`
	KBList      []types.BootstrapArticleV1 `json:"kb_list"`
	Timestamp   string                     `json:"timestamp"`
	Version     int                        `json:"version"`
}

// bootstrapPresence*Wire mirrors the required version-1 JSON fields with
// pointers (or RawMessage for nullable/opaque values). It distinguishes a
// required field that is absent from one that is present with its domain-valid
// zero value; the normal bootstrap DTO remains the value used by validation
// and storage.
type bootstrapPresenceResultWire struct {
	OrgConfig   *bootstrapPresenceOrgConfigWire `json:"org_config"`
	ProjectList *[]string                       `json:"project_list"`
	TaskList    *[]bootstrapPresenceTaskWire    `json:"task_list"`
	KBList      *[]bootstrapPresenceArticleWire `json:"kb_list"`
	Timestamp   *string                         `json:"timestamp"`
	Version     *int                            `json:"version"`
}

type bootstrapPresenceOrgConfigWire struct {
	SchemaVersion               *int                            `json:"schema_version"`
	Project                     *bootstrapPresenceProjectWire   `json:"project"`
	Identity                    *bootstrapPresenceIdentityWire  `json:"identity"`
	Channels                    *[]bootstrapPresenceChannelWire `json:"channels"`
	Events                      *[]bootstrapPresenceEventWire   `json:"events"`
	Tasks                       *[]bootstrapPresenceTaskWire    `json:"tasks"`
	KB                          *bootstrapPresenceKBWire        `json:"kb"`
	IntegrationManifestMetadata json.RawMessage                 `json:"integration_manifest_metadata"`
}

type bootstrapPresenceProjectWire struct {
	ID        *string    `json:"id"`
	Name      *string    `json:"name"`
	Owner     *string    `json:"owner"`
	CreatedAt *time.Time `json:"created_at"`
}

type bootstrapPresenceIdentityWire struct {
	Agent       *bootstrapPresenceAgentWire    `json:"agent"`
	Passport    *bootstrapPresencePassportWire `json:"passport"`
	Permissions *[]string                      `json:"permissions"`
}

type bootstrapPresenceAgentWire struct {
	ID           *string    `json:"id"`
	Owner        *string    `json:"owner"`
	Model        *string    `json:"model"`
	Capabilities *[]string  `json:"capabilities"`
	CreatedAt    *time.Time `json:"created_at"`
}

type bootstrapPresencePassportWire struct {
	ID           *string    `json:"id"`
	AgentID      *string    `json:"agent_id"`
	ProjectID    *string    `json:"project_id"`
	Repositories *[]string  `json:"repositories"`
	Roles        *[]string  `json:"roles"`
	IssuedAt     *time.Time `json:"issued_at"`
}

type bootstrapPresenceChannelWire struct {
	ID        *string    `json:"id"`
	ProjectID *string    `json:"project_id"`
	Name      *string    `json:"name"`
	CreatedAt *time.Time `json:"created_at"`
}

type bootstrapPresenceEventWire struct {
	ID        *string         `json:"id"`
	ProjectID *string         `json:"project_id"`
	ChannelID *string         `json:"channel_id"`
	AgentID   *string         `json:"agent_id"`
	EventType *string         `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	Note      json.RawMessage `json:"note"`
	CreatedAt *time.Time      `json:"created_at"`
}

type bootstrapPresenceTaskWire struct {
	ID           *string         `json:"id"`
	ProjectID    *string         `json:"project_id"`
	ParentTaskID json.RawMessage `json:"parent_task_id"`
	Title        *string         `json:"title"`
	Description  *string         `json:"description"`
	OwnerAgentID json.RawMessage `json:"owner_agent_id"`
	Status       *string         `json:"status"`
	Priority     *int            `json:"priority"`
	DueBy        json.RawMessage `json:"due_by"`
	CreatedAt    *time.Time      `json:"created_at"`
	UpdatedAt    *time.Time      `json:"updated_at"`
}

type bootstrapPresenceKBWire struct {
	Articles *[]bootstrapPresenceArticleWire `json:"articles"`
}

type bootstrapPresenceArticleWire struct {
	ID            *string         `json:"id"`
	ProjectID     *string         `json:"project_id"`
	Title         *string         `json:"title"`
	Body          *string         `json:"body"`
	Frontmatter   json.RawMessage `json:"frontmatter"`
	AuthorAgentID *string         `json:"author_agent_id"`
	CreatedAt     *time.Time      `json:"created_at"`
	UpdatedAt     *time.Time      `json:"updated_at"`
}

// syncUpdateEnvelopeWire mirrors internal/mcp's syncUpdateEnvelope.
type syncUpdateEnvelopeWire struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// incrementalPullResultWire mirrors internal/mcp.IncrementalPullOutput's JSON shape.
type incrementalPullResultWire struct {
	Updates   []syncUpdateEnvelopeWire `json:"updates"`
	Timestamp string                   `json:"timestamp"`
	Version   int                      `json:"version"`
}

// appliedItemWire mirrors internal/mcp.AppliedItem's JSON shape for decoding
// wormhole.sync.incremental_push responses. ID matches the client's entity_id;
// Error is empty on success, set on per-item failure (partial-success semantics).
type appliedItemWire struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

// incrementalPushResultWire mirrors internal/mcp.IncrementalPushOutput's JSON shape
// for decoding push responses. Applied carries per-item outcome; one non-empty Error
// does not fail the entire batch.
type incrementalPushResultWire struct {
	ItemsReceived int               `json:"items_received"`
	Applied       []appliedItemWire `json:"applied"`
	Timestamp     string            `json:"timestamp"`
	Version       int               `json:"version"`
}

// decodeBootstrapResult re-marshals the generic interface{} that
// callSyncToolWithResult returns back into JSON and decodes it into the
// typed bootstrap wire shape. The round-trip is redundant work but keeps
// callSyncToolWithResult's signature generic for every wormhole.sync.* tool.
func decodeBootstrapResult(result interface{}) (bootstrapResultWire, error) {
	var out bootstrapResultWire
	raw, err := json.Marshal(result)
	if err != nil {
		return out, fmt.Errorf("marshal: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("unmarshal: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return out, fmt.Errorf("unmarshal: trailing data")
	}
	if err := validateBootstrapRequiredFieldPresence(raw); err != nil {
		return out, err
	}
	return out, nil
}

func validateBootstrapRequiredFieldPresence(raw []byte) error {
	var presence bootstrapPresenceResultWire
	if err := json.Unmarshal(raw, &presence); err != nil {
		return fmt.Errorf("unmarshal required-field presence: %w", err)
	}
	require := func(present bool, path string) error {
		if !present {
			return fmt.Errorf("missing required field %s", path)
		}
		return nil
	}
	for _, field := range []struct {
		present bool
		path    string
	}{
		{presence.OrgConfig != nil, "org_config"},
		{presence.ProjectList != nil, "project_list"},
		{presence.TaskList != nil, "task_list"},
		{presence.KBList != nil, "kb_list"},
		{presence.Timestamp != nil, "timestamp"},
		{presence.Version != nil, "version"},
	} {
		if err := require(field.present, field.path); err != nil {
			return err
		}
	}
	org := presence.OrgConfig
	for _, field := range []struct {
		present bool
		path    string
	}{
		{org.SchemaVersion != nil, "org_config.schema_version"},
		{org.Project != nil, "org_config.project"},
		{org.Identity != nil, "org_config.identity"},
		{org.Channels != nil, "org_config.channels"},
		{org.Events != nil, "org_config.events"},
		{org.Tasks != nil, "org_config.tasks"},
		{org.KB != nil, "org_config.kb"},
		{len(org.IntegrationManifestMetadata) != 0, "org_config.integration_manifest_metadata"},
	} {
		if err := require(field.present, field.path); err != nil {
			return err
		}
	}
	project := org.Project
	for _, field := range []struct {
		present bool
		path    string
	}{
		{project.ID != nil, "org_config.project.id"},
		{project.Name != nil, "org_config.project.name"},
		{project.Owner != nil, "org_config.project.owner"},
		{project.CreatedAt != nil, "org_config.project.created_at"},
	} {
		if err := require(field.present, field.path); err != nil {
			return err
		}
	}
	identity := org.Identity
	if err := require(identity.Agent != nil, "org_config.identity.agent"); err != nil {
		return err
	}
	if err := require(identity.Passport != nil, "org_config.identity.passport"); err != nil {
		return err
	}
	if err := require(identity.Permissions != nil, "org_config.identity.permissions"); err != nil {
		return err
	}
	agent := identity.Agent
	for _, field := range []struct {
		present bool
		path    string
	}{
		{agent.ID != nil, "org_config.identity.agent.id"},
		{agent.Owner != nil, "org_config.identity.agent.owner"},
		{agent.Model != nil, "org_config.identity.agent.model"},
		{agent.Capabilities != nil, "org_config.identity.agent.capabilities"},
		{agent.CreatedAt != nil, "org_config.identity.agent.created_at"},
	} {
		if err := require(field.present, field.path); err != nil {
			return err
		}
	}
	passport := identity.Passport
	for _, field := range []struct {
		present bool
		path    string
	}{
		{passport.ID != nil, "org_config.identity.passport.id"},
		{passport.AgentID != nil, "org_config.identity.passport.agent_id"},
		{passport.ProjectID != nil, "org_config.identity.passport.project_id"},
		{passport.Repositories != nil, "org_config.identity.passport.repositories"},
		{passport.Roles != nil, "org_config.identity.passport.roles"},
		{passport.IssuedAt != nil, "org_config.identity.passport.issued_at"},
	} {
		if err := require(field.present, field.path); err != nil {
			return err
		}
	}
	for i, channel := range *org.Channels {
		prefix := fmt.Sprintf("org_config.channels[%d].", i)
		for _, field := range []struct {
			present bool
			name    string
		}{
			{channel.ID != nil, "id"}, {channel.ProjectID != nil, "project_id"},
			{channel.Name != nil, "name"}, {channel.CreatedAt != nil, "created_at"},
		} {
			if err := require(field.present, prefix+field.name); err != nil {
				return err
			}
		}
	}
	for i, event := range *org.Events {
		prefix := fmt.Sprintf("org_config.events[%d].", i)
		for _, field := range []struct {
			present bool
			name    string
		}{
			{event.ID != nil, "id"}, {event.ProjectID != nil, "project_id"},
			{event.ChannelID != nil, "channel_id"}, {event.AgentID != nil, "agent_id"},
			{event.EventType != nil, "event_type"}, {len(event.Payload) != 0, "payload"},
			{len(event.Note) != 0, "note"}, {event.CreatedAt != nil, "created_at"},
		} {
			if err := require(field.present, prefix+field.name); err != nil {
				return err
			}
		}
	}
	if err := validateBootstrapTaskFieldPresence(*org.Tasks, "org_config.tasks", require); err != nil {
		return err
	}
	if err := require(org.KB.Articles != nil, "org_config.kb.articles"); err != nil {
		return err
	}
	if err := validateBootstrapArticleFieldPresence(*org.KB.Articles, "org_config.kb.articles", require); err != nil {
		return err
	}
	if err := validateBootstrapTaskFieldPresence(*presence.TaskList, "task_list", require); err != nil {
		return err
	}
	return validateBootstrapArticleFieldPresence(*presence.KBList, "kb_list", require)
}

func validateBootstrapTaskFieldPresence(tasks []bootstrapPresenceTaskWire, path string, require func(bool, string) error) error {
	for i, task := range tasks {
		prefix := fmt.Sprintf("%s[%d].", path, i)
		for _, field := range []struct {
			present bool
			name    string
		}{
			{task.ID != nil, "id"}, {task.ProjectID != nil, "project_id"},
			{len(task.ParentTaskID) != 0, "parent_task_id"}, {task.Title != nil, "title"},
			{task.Description != nil, "description"}, {len(task.OwnerAgentID) != 0, "owner_agent_id"},
			{task.Status != nil, "status"}, {task.Priority != nil, "priority"},
			{len(task.DueBy) != 0, "due_by"}, {task.CreatedAt != nil, "created_at"},
			{task.UpdatedAt != nil, "updated_at"},
		} {
			if err := require(field.present, prefix+field.name); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBootstrapArticleFieldPresence(articles []bootstrapPresenceArticleWire, path string, require func(bool, string) error) error {
	for i, article := range articles {
		prefix := fmt.Sprintf("%s[%d].", path, i)
		for _, field := range []struct {
			present bool
			name    string
		}{
			{article.ID != nil, "id"}, {article.ProjectID != nil, "project_id"},
			{article.Title != nil, "title"}, {article.Body != nil, "body"},
			{len(article.Frontmatter) != 0, "frontmatter"}, {article.AuthorAgentID != nil, "author_agent_id"},
			{article.CreatedAt != nil, "created_at"}, {article.UpdatedAt != nil, "updated_at"},
		} {
			if err := require(field.present, prefix+field.name); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateBootstrapResult(out bootstrapResultWire, namespaceID, expectedAgentID, expectedPassportID string) error {
	if out.Version != SyncProtocolVersion {
		return fmt.Errorf("outer version = %d, want %d", out.Version, SyncProtocolVersion)
	}
	if out.OrgConfig.SchemaVersion != types.BootstrapSchemaVersionV1 {
		return fmt.Errorf("org_config schema_version = %d, want %d", out.OrgConfig.SchemaVersion, types.BootstrapSchemaVersionV1)
	}
	if namespaceID == "" || expectedAgentID == "" || expectedPassportID == "" {
		return errors.New("missing authenticated namespace or credential identity")
	}
	if out.ProjectList == nil || len(out.ProjectList) != 0 {
		return errors.New("project_list must be a non-nil empty array")
	}
	identity := out.OrgConfig.Identity
	if identity.Agent.Capabilities == nil {
		return errors.New("agent capabilities must be an array")
	}
	if identity.Passport.Repositories == nil {
		return errors.New("passport repositories must be an array")
	}
	if identity.Passport.Roles == nil {
		return errors.New("passport roles must be an array")
	}
	if identity.Permissions == nil {
		return errors.New("identity permissions must be an array")
	}
	if out.OrgConfig.Channels == nil {
		return errors.New("channels must be an array")
	}
	if out.OrgConfig.Events == nil {
		return errors.New("events must be an array")
	}
	if out.OrgConfig.Tasks == nil {
		return errors.New("tasks must be an array")
	}
	if out.OrgConfig.KB.Articles == nil {
		return errors.New("kb articles must be an array")
	}
	if out.TaskList == nil || out.KBList == nil {
		return errors.New("task_list and kb_list must be arrays")
	}
	if out.OrgConfig.Project.ID != namespaceID {
		return fmt.Errorf("project id %q does not match namespace %q", out.OrgConfig.Project.ID, namespaceID)
	}
	if out.OrgConfig.Project.CreatedAt.IsZero() {
		return errors.New("project created_at is zero")
	}
	if identity.Agent.ID != expectedAgentID {
		return fmt.Errorf("credential agent %q does not match snapshot agent %q", expectedAgentID, identity.Agent.ID)
	}
	if identity.Passport.ID != expectedPassportID {
		return fmt.Errorf("credential passport %q does not match snapshot passport %q", expectedPassportID, identity.Passport.ID)
	}
	if identity.Passport.AgentID != identity.Agent.ID || identity.Passport.ProjectID != namespaceID {
		return errors.New("passport identity/project references do not match snapshot")
	}
	if identity.Agent.CreatedAt.IsZero() || identity.Passport.IssuedAt.IsZero() {
		return errors.New("identity timestamps must be nonzero")
	}
	if !reflect.DeepEqual(out.TaskList, out.OrgConfig.Tasks) {
		return errors.New("task_list mirror differs from org_config tasks")
	}
	if !reflect.DeepEqual(out.KBList, out.OrgConfig.KB.Articles) {
		return errors.New("kb_list mirror differs from org_config kb articles")
	}
	metadata := bytes.TrimSpace(out.OrgConfig.IntegrationManifestMetadata)
	if !json.Valid(metadata) || (!bytes.Equal(metadata, []byte("null")) && (len(metadata) == 0 || metadata[0] != '{')) {
		return errors.New("integration manifest metadata must be JSON null or an object")
	}
	outerTimestamp, err := time.Parse(time.RFC3339Nano, out.Timestamp)
	if err != nil || outerTimestamp.IsZero() {
		return fmt.Errorf("bootstrap timestamp is invalid: %w", err)
	}

	channels := make(map[string]struct{}, len(out.OrgConfig.Channels))
	for _, channel := range out.OrgConfig.Channels {
		if channel.ID == "" || channel.ProjectID != namespaceID || channel.CreatedAt.IsZero() {
			return fmt.Errorf("channel %q has invalid project or timestamp", channel.ID)
		}
		if _, duplicate := channels[channel.ID]; duplicate {
			return fmt.Errorf("duplicate channel id %q", channel.ID)
		}
		channels[channel.ID] = struct{}{}
	}
	events := make(map[string]struct{}, len(out.OrgConfig.Events))
	for _, event := range out.OrgConfig.Events {
		if event.ID == "" || event.ProjectID != namespaceID || event.CreatedAt.IsZero() {
			return fmt.Errorf("event %q has invalid project or timestamp", event.ID)
		}
		if _, duplicate := events[event.ID]; duplicate {
			return fmt.Errorf("duplicate event id %q", event.ID)
		}
		events[event.ID] = struct{}{}
		if strings.TrimSpace(event.AgentID) == "" {
			return fmt.Errorf("event agent_id for %q must be nonempty", event.ID)
		}
		if strings.TrimSpace(event.EventType) == "" {
			return fmt.Errorf("event event_type for %q must be nonempty", event.ID)
		}
		if _, ok := channels[event.ChannelID]; !ok {
			return fmt.Errorf("event channel reference %q is missing", event.ChannelID)
		}
		if len(event.Payload) == 0 || !json.Valid(event.Payload) {
			return fmt.Errorf("event payload %q is invalid JSON", event.ID)
		}
	}
	tasksByID := make(map[string]types.BootstrapTaskV1, len(out.OrgConfig.Tasks))
	for _, task := range out.OrgConfig.Tasks {
		if task.ID == "" || task.ProjectID != namespaceID || task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() || (task.DueBy != nil && task.DueBy.IsZero()) {
			return fmt.Errorf("task %q has invalid project or timestamp", task.ID)
		}
		if task.ParentTaskID != nil && strings.TrimSpace(*task.ParentTaskID) == "" {
			return fmt.Errorf("task %q parent_task_id must be nonempty when present", task.ID)
		}
		if task.OwnerAgentID != nil && strings.TrimSpace(*task.OwnerAgentID) == "" {
			return fmt.Errorf("task %q owner_agent_id must be nonempty when present", task.ID)
		}
		switch task.Status {
		case "todo", "wip", "blocked", "done":
		default:
			return fmt.Errorf("task %q has invalid status %q", task.ID, task.Status)
		}
		if _, duplicate := tasksByID[task.ID]; duplicate {
			return fmt.Errorf("duplicate task id %q", task.ID)
		}
		tasksByID[task.ID] = task
	}
	for _, task := range out.OrgConfig.Tasks {
		if task.ParentTaskID != nil {
			if _, ok := tasksByID[*task.ParentTaskID]; !ok {
				return fmt.Errorf("task %q parent reference %q is missing", task.ID, *task.ParentTaskID)
			}
		}
	}
	visiting := make(map[string]bool, len(tasksByID))
	visited := make(map[string]bool, len(tasksByID))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("task graph contains a cycle at %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		if parent := tasksByID[id].ParentTaskID; parent != nil {
			if err := visit(*parent); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range tasksByID {
		if err := visit(id); err != nil {
			return err
		}
	}
	articles := make(map[string]struct{}, len(out.OrgConfig.KB.Articles))
	for _, article := range out.OrgConfig.KB.Articles {
		if article.ID == "" || article.ProjectID != namespaceID || article.CreatedAt.IsZero() || article.UpdatedAt.IsZero() {
			return fmt.Errorf("kb article %q has invalid project or timestamp", article.ID)
		}
		if strings.TrimSpace(article.AuthorAgentID) == "" {
			return fmt.Errorf("kb article author_agent_id for %q must be nonempty", article.ID)
		}
		if _, duplicate := articles[article.ID]; duplicate {
			return fmt.Errorf("duplicate kb article id %q", article.ID)
		}
		articles[article.ID] = struct{}{}
		if len(article.Frontmatter) == 0 || !json.Valid(article.Frontmatter) {
			return fmt.Errorf("kb article %q frontmatter is invalid JSON", article.ID)
		}
	}
	return nil
}

// decodeIncrementalPullResult is decodeBootstrapResult's counterpart for
// wormhole.sync.incremental_pull's result shape.
func decodeIncrementalPullResult(result interface{}) (incrementalPullResultWire, error) {
	var out incrementalPullResultWire
	raw, err := json.Marshal(result)
	if err != nil {
		return out, fmt.Errorf("marshal: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("unmarshal: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return out, fmt.Errorf("unmarshal: trailing data")
	}
	return out, nil
}

// decodeIncrementalPushResult re-marshals the generic interface{} that
// callSyncToolWithResult returns back into JSON and decodes it into the
// typed push result wire shape (internal/mcp.IncrementalPushOutput).
func decodeIncrementalPushResult(result interface{}) (incrementalPushResultWire, error) {
	var out incrementalPushResultWire
	raw, err := json.Marshal(result)
	if err != nil {
		return out, fmt.Errorf("marshal: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("unmarshal: %w", err)
	}
	return out, nil
}

// applyTask upserts one server task into the local task replica
// (RFC-0003 §8.1/§8.2 local-apply). A nil taskRepo (callers that only
// exercise push) is a configuration error, not a silent no-op.
func (e *Engine) applyTask(ctx context.Context, task taskSummaryWire) error {
	if e.taskRepo == nil {
		return errors.New("sync: no taskRepo configured to apply server task")
	}
	_, err := e.taskRepo.UpsertTaskFromServer(ctx, e.namespaceID, task.TaskID, task.Title, task.Description,
		task.ParentTaskID, task.OwnerAgentID, task.Status, task.Priority, task.DueBy, task.CreatedAt, task.UpdatedAt)
	return err
}

// applyArticle upserts one server KB article into the local KB replica.
func (e *Engine) applyArticle(ctx context.Context, article articleSummaryWire) error {
	if e.kbRepo == nil {
		return errors.New("sync: no kbRepo configured to apply server kb article")
	}
	_, err := e.kbRepo.UpsertArticle(ctx, e.namespaceID, article.ArticleID, article.Title, article.Body,
		article.Frontmatter, article.AuthorAgentID, article.CreatedAt, article.UpdatedAt)
	return err
}

func (e *Engine) applyChannel(ctx context.Context, channel channelWire) error {
	if e.eventRepo == nil {
		return errors.New("sync: no eventRepo configured to apply server channel")
	}
	if channel.ID == "" || channel.ProjectID != e.namespaceID || channel.Name == "" || channel.CreatedAt.IsZero() {
		return errors.New("sync: malformed or cross-project channel update")
	}
	return e.eventRepo.UpsertChannelFromServer(ctx, e.namespaceID, channel.ID, channel.Name, channel.CreatedAt)
}

func (e *Engine) applyEvent(ctx context.Context, event eventWire) error {
	if e.eventRepo == nil {
		return errors.New("sync: no eventRepo configured to apply server event")
	}
	if event.ID == "" || event.ProjectID != e.namespaceID || event.ChannelID == "" || event.AgentID == "" || event.EventType == "" || !json.Valid(event.Payload) || event.CreatedAt.IsZero() {
		return errors.New("sync: malformed or cross-project event update")
	}
	return e.eventRepo.UpsertEventFromServer(ctx, localstore.DurableEvent{
		ID: event.ID, NamespaceID: e.namespaceID, ChannelID: event.ChannelID, AgentID: event.AgentID,
		EventType: event.EventType, Payload: event.Payload, Note: event.Note, CreatedAt: event.CreatedAt,
	})
}

func (e *Engine) applyGitLink(ctx context.Context, link gitLinkWire) error {
	if e.gitRepo == nil {
		return errors.New("sync: no gitRepo configured to apply server Git link")
	}
	if link.GitLinkID == "" || link.ProjectID != e.namespaceID || link.TaskID == "" || link.Repo == "" || link.CommitSHA == "" || link.Summary == "" || link.AgentID == "" || link.CreatedAt.IsZero() {
		return errors.New("sync: malformed or cross-project Git link update")
	}
	return e.gitRepo.UpsertFromServer(ctx, localstore.GitLink{
		ID: link.GitLinkID, ProjectID: e.namespaceID, TaskID: link.TaskID, Repo: link.Repo,
		CommitSHA: link.CommitSHA, Summary: link.Summary, AgentID: link.AgentID, CreatedAt: link.CreatedAt,
	})
}

// callSyncTool makes a JSON-RPC 2.0 call to a wormhole.sync.* tool on the coordination server.
// Used for one-way operations (push).
func (e *Engine) callSyncTool(ctx context.Context, toolName string, args map[string]interface{}) error {
	_, err := e.callSyncToolWithResult(ctx, toolName, args)
	return err
}

// callSyncToolWithResult makes a JSON-RPC 2.0 call and returns the result.
// Mirrors localapi's proxyWhoAmI pattern for coordinating with the server.
// If testCallSyncToolWithResultFn is set (testing only), it is used instead.
func (e *Engine) callSyncToolWithResult(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	coordServer, token, err := e.networkTarget(ctx)
	if err != nil {
		return nil, err
	}
	// Test hook for injection (testing only).
	if e.testCallSyncToolWithResultFn != nil {
		return e.testCallSyncToolWithResultFn(ctx, toolName, args)
	}

	argsJSON, _ := json.Marshal(args)
	paramsRaw, err := json.Marshal(map[string]interface{}{
		"name":      toolName,
		"arguments": json.RawMessage(argsJSON),
	})
	if err != nil {
		return nil, fmt.Errorf("sync: marshal params: %w", err)
	}

	reqBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  json.RawMessage(paramsRaw),
	})
	if err != nil {
		return nil, fmt.Errorf("sync: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(coordServer, "/")+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("sync: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sync: call coordination server: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sync: read response: %w", err)
	}
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: HTTP status %d", ErrFabricUnavailable, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: Fabric HTTP status %d", ErrAttentionRequired, resp.StatusCode)
	}

	var rpcResp map[string]interface{}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("sync: decode coordination server response: %w", err)
	}

	// Check for RPC error.
	if errVal, ok := rpcResp["error"]; ok && errVal != nil {
		if e.routeRepo != nil {
			return nil, fmt.Errorf("%w: Fabric RPC rejected request", ErrAttentionRequired)
		}
		return nil, fmt.Errorf("sync: server error: %v", errVal)
	}

	// Extract result from tools/call wrapper.
	resultRaw, ok := rpcResp["result"]
	if !ok {
		return nil, errors.New("sync: no result in coordination server response")
	}

	// Result wraps the actual tool result in a toolCallResult struct.
	var toolResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}

	resultBytes, _ := json.Marshal(resultRaw)
	if err := json.Unmarshal(resultBytes, &toolResult); err != nil {
		return nil, fmt.Errorf("sync: decode tools/call result: %w", err)
	}

	if toolResult.IsError && len(toolResult.Content) > 0 {
		if e.routeRepo != nil {
			return nil, fmt.Errorf("%w: Fabric tool rejected request", ErrAttentionRequired)
		}
		return nil, fmt.Errorf("sync: tool error: %s", toolResult.Content[0].Text)
	}

	if len(toolResult.Content) == 0 {
		return nil, errors.New("sync: empty result from coordination server")
	}

	var result interface{}
	if err := json.Unmarshal([]byte(toolResult.Content[0].Text), &result); err != nil {
		return nil, fmt.Errorf("sync: decode tool output: %w", err)
	}

	return result, nil
}

func (e *Engine) networkTarget(ctx context.Context) (string, string, error) {
	if e.routeRepo == nil {
		return e.coordServer, e.token, nil
	}
	conflicted, err := e.conflictGate.HasOpenConflicts(ctx, e.workspaceScope)
	if err != nil {
		return "", "", fmt.Errorf("sync: inspect workspace conflicts: %w", err)
	}
	if conflicted {
		return "", "", localstore.ErrWorkspaceConflicted
	}
	binding, profile, err := e.routeRepo.GetRoute(ctx, e.workspaceScope)
	if err != nil {
		return "", "", fmt.Errorf("sync: resolve Fabric route: %w", err)
	}
	if binding.RemoteKey() != e.remoteKey || profile.ProfileID != e.profileID || profile.FabricInstanceID != e.remoteKey.FabricInstanceID {
		return "", "", fmt.Errorf("sync: immutable Fabric route changed")
	}
	if profile.CredentialRef == "" {
		return profile.BaseURL, "", nil
	}
	token, err := e.credentialSource.Read(ctx, profile.CredentialRef)
	if err != nil {
		return "", "", errors.New("sync: resolve Fabric credential")
	}
	return profile.BaseURL, token, nil
}

// ReportConflict reports a conflict that occurred during push to the server.
// The server's last-write-wins resolution becomes authoritative (RFC-0003 §8.3).
func (e *Engine) ReportConflict(ctx context.Context, entityType, entityID, conflictType, serverValue, localValue string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Call wormhole.sync.conflict_report on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	result, err := e.callSyncToolWithResult(ctx, "wormhole.sync.conflict_report", map[string]interface{}{
		"namespace_id":  e.namespaceID,
		"version":       SyncProtocolVersion,
		"entity_type":   entityType,
		"entity_id":     entityID,
		"conflict_type": conflictType,
		"server_value":  serverValue,
		"local_value":   localValue,
	})
	if err != nil {
		return fmt.Errorf("sync: report conflict: %w", err)
	}

	// Extract resolved value from result (expected to be {resolved_value: "..."}).
	var resolved struct {
		ResolvedValue string `json:"resolved_value"`
	}
	resolvedBytes, _ := json.Marshal(result)
	if err := json.Unmarshal(resolvedBytes, &resolved); err != nil {
		resolved.ResolvedValue = ""
	}

	// Log the conflict in the audit trail (RFC-0003 §8.3).
	conflictJSON, _ := json.Marshal(map[string]string{
		"entity_type": entityType, "entity_id": entityID, "conflict_type": conflictType,
		"server_value": serverValue, "local_value": localValue, "resolved_value": resolved.ResolvedValue,
	})
	actorJSON := json.RawMessage(`{"resolved_by":"last_write_wins"}`)
	_, err = e.auditRepo.LogConflict(ctx, e.remoteKey, conflictJSON, actorJSON)
	if err != nil {
		// Audit log failure is not a blocking error; continue.
		_ = err
	}

	return nil
}
