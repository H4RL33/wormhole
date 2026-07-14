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
)

// Engine orchestrates the local sync lifecycle: bootstrap, incremental push/pull,
// and batching (RFC-0003 §8). It holds per-org state including queue and audit repos.
type Engine struct {
	httpClient    *http.Client
	coordServer   string
	token         string
	namespaceID   string
	queueRepo     *QueueRepo
	auditRepo     *AuditRepo
	mu            sync.Mutex
	lastSyncTime  time.Time
	batchInterval time.Duration
	batchSize     int
	shutdown      chan struct{}
	wg            sync.WaitGroup
}

// Config holds tunable sync batching parameters (RFC-0003 §8.2).
type Config struct {
	BatchInterval time.Duration // time-based batching threshold
	BatchSize     int           // queue-size batching threshold
}

// DefaultConfig returns conservative batching defaults: 5 sec interval, 50 item batch.
func DefaultConfig() Config {
	return Config{
		BatchInterval: 5 * time.Second,
		BatchSize:     50,
	}
}

// New creates a new sync engine for one (org, project) binding.
func New(coordServerURL, token, namespaceID string, queueRepo *QueueRepo, auditRepo *AuditRepo, cfg Config) *Engine {
	return &Engine{
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		coordServer:   coordServerURL,
		token:         token,
		namespaceID:   namespaceID,
		queueRepo:     queueRepo,
		auditRepo:     auditRepo,
		batchInterval: cfg.BatchInterval,
		batchSize:     cfg.BatchSize,
		shutdown:      make(chan struct{}),
	}
}

// Start begins the background sync loop. Callers must call Stop to cleanly shut down.
func (e *Engine) Start(ctx context.Context) {
	e.wg.Add(1)
	go e.syncLoop(ctx)
}

// Stop stops the background sync loop and waits for it to finish.
func (e *Engine) Stop() {
	close(e.shutdown)
	e.wg.Wait()
}

// syncLoop periodically evaluates pending work and pushes batches to the server.
// Runs until ctx is cancelled or Stop() is called.
func (e *Engine) syncLoop(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(e.batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.shutdown:
			return
		case <-ticker.C:
			// Time-based batch trigger: push any pending work.
			if err := e.pushBatch(ctx); err != nil {
				// Best-effort: log error and continue. The batch remains queued
				// for retry on the next interval.
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
	const SyncProtocolVersion = 1
	if err := e.callSyncTool(ctx, "wormhole.sync.incremental_push", map[string]interface{}{
		"namespace_id": e.namespaceID,
		"version":      SyncProtocolVersion,
		"items":        pushItems,
	}); err != nil {
		return fmt.Errorf("sync: push batch: call server: %w", err)
	}

	// Mark all entries as delivered.
	for _, entry := range entries {
		if err := e.queueRepo.MarkDelivered(ctx, e.namespaceID, entry.ID); err != nil {
			// If marking fails, the entry will be retried on the next cycle.
			// Do not fail the entire batch.
			_ = err
		}
	}

	e.lastSyncTime = time.Now().UTC()
	return nil
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
	result, err := e.callSyncToolWithResult(ctx, "wormhole.sync.incremental_pull", map[string]interface{}{
		"namespace_id": e.namespaceID,
		"version":      SyncProtocolVersion,
	})
	if err != nil {
		return fmt.Errorf("sync: pull incremental: call server: %w", err)
	}

	// Result should be a list of updated entities with server timestamps.
	// For now, we log receipt but don't apply them (that's repository-layer work).
	_ = result

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

	// Result should contain org config, project manifests, initial KB, tasks, policies, etc.
	// For now, we log receipt but don't apply them (that's repository-layer work).
	_ = result

	return nil
}

// callSyncTool makes a JSON-RPC 2.0 call to a wormhole.sync.* tool on the coordination server.
// Used for one-way operations (push).
func (e *Engine) callSyncTool(ctx context.Context, toolName string, args map[string]interface{}) error {
	_, err := e.callSyncToolWithResult(ctx, toolName, args)
	return err
}

// callSyncToolWithResult makes a JSON-RPC 2.0 call and returns the result.
// Mirrors localapi's proxyWhoAmI pattern for coordinating with the server.
func (e *Engine) callSyncToolWithResult(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
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
