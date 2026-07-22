package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

// Engine orchestrates the local sync lifecycle: bootstrap, incremental push/pull,
// and batching (RFC-0003 §8). It holds per-org state including queue and audit repos.
type Engine struct {
	httpClient            *http.Client
	coordServer           string
	token                 string
	namespaceID           string
	queueRepo             *QueueRepo
	auditRepo             *AuditRepo
	taskRepo              *localstore.TaskRepo
	kbRepo                *localstore.KBRepo
	mu                    sync.Mutex
	lastSyncTime          time.Time
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
	// testCallSyncToolWithResultFn is for testing only: if set, overrides callSyncToolWithResult.
	testCallSyncToolWithResultFn func(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error)
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
// item batch, 5 sec pull interval, and high-priority entries (priority >= 2)
// checked every 500ms instead of waiting the full 5 sec.
func DefaultConfig() Config {
	return Config{
		BatchInterval:         5 * time.Second,
		BatchSize:             50,
		LatencyCheckInterval:  500 * time.Millisecond,
		PullInterval:          5 * time.Second,
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
	}, nil
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Time-based batch trigger: push any pending work.
			if err := e.pushBatch(ctx); err != nil {
				// Best-effort: log error and continue. The batch remains queued
				// for retry on the next interval.
				_ = err
			}
		case <-latencyTicker.C:
			if err := e.checkLatencySensitive(ctx); err != nil {
				_ = err
			}
		case <-pullTicker.C:
			if err := e.PullIncremental(ctx); err != nil {
				_ = err
			}
		}
	}
}

// pushBatch retrieves pending entries up to batchSize and pushes to the server.
func (e *Engine) pushBatch(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	entries, err := e.queueRepo.ListPending(ctx, e.namespaceID, e.batchSize)
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
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			payload = string(entry.Payload)
		}
		pushItems[i] = map[string]interface{}{
			"entity_type": entry.EntityType,
			"entity_id":   entry.EntityID,
			"operation":   entry.Operation,
			"payload":     payload,
		}
	}

	// Call wormhole.sync.incremental_push on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	// Use callSyncToolWithResult to get the response body for per-item error checking.
	const SyncProtocolVersion = 1
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

	acknowledgements, err := validatePushAcknowledgements(entries, pushResult)
	if err != nil {
		return fmt.Errorf("sync: push batch: invalid acknowledgement: %w", err)
	}

	// Mark only successful entries as delivered. Failed entries remain in the queue for retry.
	for _, entry := range entries {
		key := acknowledgementKey{entityType: entry.EntityType, entityID: entry.EntityID}
		if acknowledgements[key].Error == "" {
			if err := e.queueRepo.MarkDelivered(ctx, e.namespaceID, entry.ID); err != nil {
				// If marking fails, the entry will be retried on the next cycle.
				// Do not fail the entire batch.
				_ = err
			}
		}
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
		key := acknowledgementKey{entityType: entry.EntityType, entityID: entry.EntityID}
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
	entries, err := e.queueRepo.ListPending(ctx, e.namespaceID, 1)
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

	// Call wormhole.sync.incremental_pull on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	const SyncProtocolVersion = 1
	args := map[string]interface{}{
		"namespace_id": e.namespaceID,
		"version":      SyncProtocolVersion,
	}
	if !e.lastSyncTime.IsZero() {
		args["last_sync"] = e.lastSyncTime.UTC().Format(time.RFC3339)
	}
	result, err := e.callSyncToolWithResult(ctx, "wormhole.sync.incremental_pull", args)
	if err != nil {
		return fmt.Errorf("sync: pull incremental: call server: %w", err)
	}

	pullResult, err := decodeIncrementalPullResult(result)
	if err != nil {
		return fmt.Errorf("sync: pull incremental: decode result: %w", err)
	}
	cursor, err := time.Parse(time.RFC3339, pullResult.Timestamp)
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
		default:
			return fmt.Errorf("sync: pull incremental: unknown update type %q", u.Type)
		}
	}

	e.lastSyncTime = cursor
	return nil
}

// Bootstrap performs a one-time bulk pull of the complete working environment
// (RFC-0003 §8.1). Used during org enrolment.
func (e *Engine) Bootstrap(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Call wormhole.sync.bootstrap on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	const SyncProtocolVersion = 1
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
	for _, task := range out.TaskList {
		if err := e.applyTask(ctx, task); err != nil {
			return fmt.Errorf("sync: bootstrap: apply task: %w", err)
		}
	}
	for _, article := range out.KBList {
		if err := e.applyArticle(ctx, article); err != nil {
			return fmt.Errorf("sync: bootstrap: apply kb article: %w", err)
		}
	}

	return nil
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

// bootstrapResultWire mirrors internal/mcp.BootstrapOutput's JSON shape.
type bootstrapResultWire struct {
	TaskList []taskSummaryWire    `json:"task_list"`
	KBList   []articleSummaryWire `json:"kb_list"`
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
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("unmarshal: %w", err)
	}
	return out, nil
}

// decodeIncrementalPullResult is decodeBootstrapResult's counterpart for
// wormhole.sync.incremental_pull's result shape.
func decodeIncrementalPullResult(result interface{}) (incrementalPullResultWire, error) {
	var out incrementalPullResultWire
	raw, err := json.Marshal(result)
	if err != nil {
		return out, fmt.Errorf("marshal: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("unmarshal: %w", err)
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
	_, err := e.taskRepo.UpsertTask(ctx, e.namespaceID, task.TaskID, task.Title, task.Description,
		task.ParentTaskID, task.OwnerAgentID, task.Status, task.Priority, task.DueBy)
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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.coordServer, "/")+"/mcp", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("sync: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.token)

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sync: call coordination server: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("sync: read response: %w", err)
	}

	var rpcResp map[string]interface{}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("sync: decode coordination server response: %w", err)
	}

	// Check for RPC error.
	if errVal, ok := rpcResp["error"]; ok && errVal != nil {
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

// ReportConflict reports a conflict that occurred during push to the server.
// The server's last-write-wins resolution becomes authoritative (RFC-0003 §8.3).
func (e *Engine) ReportConflict(ctx context.Context, entityType, entityID, conflictType, serverValue, localValue string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Call wormhole.sync.conflict_report on the coordination server.
	// Include protocol version per RFC-0003 §9 OQ5 (P6 hardening).
	const SyncProtocolVersion = 1
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
	_, err = e.auditRepo.LogConflict(ctx, e.namespaceID, entityType, entityID, conflictType, serverValue, localValue, resolved.ResolvedValue, "last_write_wins")
	if err != nil {
		// Audit log failure is not a blocking error; continue.
		_ = err
	}

	return nil
}
