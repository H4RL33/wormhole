package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type RestoreStashRequest struct {
	Scope     types.WorkspaceScope
	RequestID string
	StashID   string
	Actor     types.ActorEnvelope
}

type RestoreStashResult struct {
	RestoredDigest           state.Digest
	RebasedThroughGeneration int64
	Conflicts                []Conflict
	StashRetained            bool
}

type decodedRestoreReceipt struct {
	Result              RestoreStashResult
	Outcome             string
	ConflictRetryDigest *state.Digest
	SchemaVersion       int
	WorkspaceRevision   int64
}

type restoreRequestDigestV1 struct {
	SchemaVersion int                  `json:"schema_version"`
	Action        string               `json:"action"`
	Scope         types.WorkspaceScope `json:"scope"`
	Actor         types.ActorEnvelope  `json:"actor"`
	StashID       string               `json:"stash_id"`
}

type transitionRecordKeyV1 struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type transitionConflictV1 struct {
	ID        string                `json:"id"`
	Key       transitionRecordKeyV1 `json:"key"`
	FieldPath string                `json:"field_path"`
	Kind      ConflictKind          `json:"kind"`
	Base      FieldValue            `json:"base"`
	Ours      FieldValue            `json:"ours"`
	Theirs    FieldValue            `json:"theirs"`
}

type restoreStashResultV1 struct {
	RestoredDigest           state.Digest           `json:"restored_digest"`
	RebasedThroughGeneration int64                  `json:"rebased_through_generation"`
	Conflicts                []transitionConflictV1 `json:"conflicts"`
	StashRetained            bool                   `json:"stash_retained"`
}

type restoreStashReceiptV1 struct {
	SchemaVersion       int                  `json:"schema_version"`
	Action              string               `json:"action"`
	Outcome             string               `json:"outcome"`
	Result              restoreStashResultV1 `json:"result"`
	ConflictRetryDigest *state.Digest        `json:"conflict_retry_digest"`
}

type restoreStashReceiptV2 struct {
	SchemaVersion       int                  `json:"schema_version"`
	Action              string               `json:"action"`
	Outcome             string               `json:"outcome"`
	WorkspaceRevision   int64                `json:"workspace_revision"`
	Result              restoreStashResultV1 `json:"result"`
	ConflictRetryDigest *state.Digest        `json:"conflict_retry_digest"`
}

func restoreRequestDigest(req RestoreStashRequest) (state.Digest, error) {
	if !types.CanonicalUUID(req.Scope.ProjectID) || !types.CanonicalUUID(string(req.Scope.WorkspaceID)) {
		return "", fmt.Errorf("projectstate: invalid restore scope")
	}
	if !types.CanonicalUUID(req.RequestID) {
		return "", fmt.Errorf("projectstate: invalid restore request ID")
	}
	if !canonicalUUIDv4(req.StashID) {
		return "", fmt.Errorf("projectstate: invalid restore stash ID")
	}
	if err := req.Actor.ValidateLocalAction(); err != nil {
		return "", err
	}
	digest, err := state.DigestCanonicalJSON(restoreRequestDigestV1{
		SchemaVersion: 1, Action: "restore", Scope: req.Scope, Actor: req.Actor, StashID: req.StashID,
	})
	if err != nil {
		return "", fmt.Errorf("projectstate: digest restore request: %w", err)
	}
	return digest, nil
}

func encodeCleanRestoreReceipt(result RestoreStashResult) (json.RawMessage, error) {
	private, err := privateRestoreResult(result)
	if err != nil {
		return nil, err
	}
	if err := validateRestoreReceiptOutcome(private, "clean", nil); err != nil {
		return nil, err
	}
	return encodeRestoreReceipt(restoreStashReceiptV1{
		SchemaVersion: 1, Action: "restore", Outcome: "clean", Result: private,
	})
}

func encodeConflictedRestoreReceipt(result RestoreStashResult, retryDigest state.Digest) (json.RawMessage, error) {
	private, err := privateRestoreResult(result)
	if err != nil {
		return nil, err
	}
	if err := validateRestoreReceiptOutcome(private, "conflicted", &retryDigest); err != nil {
		return nil, err
	}
	return encodeRestoreReceipt(restoreStashReceiptV1{
		SchemaVersion: 1, Action: "restore", Outcome: "conflicted", Result: private,
		ConflictRetryDigest: &retryDigest,
	})
}

func encodeCleanRestoreReceiptV2(result RestoreStashResult, workspaceRevision int64) (json.RawMessage, error) {
	private, err := privateRestoreResult(result)
	if err != nil {
		return nil, err
	}
	if workspaceRevision < 1 {
		return nil, fmt.Errorf("projectstate: invalid restore receipt workspace revision")
	}
	if err := validateRestoreReceiptOutcome(private, "clean", nil); err != nil {
		return nil, err
	}
	return encodeRestoreReceiptV2(restoreStashReceiptV2{
		SchemaVersion: 2, Action: "restore", Outcome: "clean",
		WorkspaceRevision: workspaceRevision, Result: private,
	})
}

func encodeConflictedRestoreReceiptV2(result RestoreStashResult, workspaceRevision int64, retryDigest state.Digest) (json.RawMessage, error) {
	private, err := privateRestoreResult(result)
	if err != nil {
		return nil, err
	}
	if workspaceRevision < 1 {
		return nil, fmt.Errorf("projectstate: invalid restore receipt workspace revision")
	}
	if err := validateRestoreReceiptOutcome(private, "conflicted", &retryDigest); err != nil {
		return nil, err
	}
	return encodeRestoreReceiptV2(restoreStashReceiptV2{
		SchemaVersion: 2, Action: "restore", Outcome: "conflicted",
		WorkspaceRevision: workspaceRevision, Result: private, ConflictRetryDigest: &retryDigest,
	})
}

func encodeRestoreReceiptV2(receipt restoreStashReceiptV2) (json.RawMessage, error) {
	encoded, err := state.CanonicalJSON(receipt)
	if err != nil {
		return nil, fmt.Errorf("projectstate: encode restore receipt v2: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func encodeRestoreReceipt(receipt restoreStashReceiptV1) (json.RawMessage, error) {
	encoded, err := state.CanonicalJSON(receipt)
	if err != nil {
		return nil, fmt.Errorf("projectstate: encode restore receipt: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func decodeRestoreReceipt(receipt *localstore.WorkspaceTransitionReceiptRecord, req RestoreStashRequest, requestDigest state.Digest) (decodedRestoreReceipt, error) {
	expectedDigest, err := restoreRequestDigest(req)
	if err != nil {
		return decodedRestoreReceipt{}, err
	}
	if !validImportDigest(requestDigest) || requestDigest != expectedDigest {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: restore request digest mismatch")
	}
	if receipt == nil {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: restore receipt is absent")
	}
	if receipt.RequestID != req.RequestID || !types.CanonicalUUID(receipt.RequestID) {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: restore receipt request ID mismatch")
	}
	if receipt.Action != "restore" {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: restore receipt action mismatch")
	}
	if !validImportDigest(receipt.RequestDigest) || receipt.RequestDigest != requestDigest {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: restore receipt request digest mismatch")
	}
	if err := receipt.Actor.ValidateHistorical(); err != nil {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: invalid restore receipt actor: %w", err)
	}
	wantActor, err := state.CanonicalJSON(req.Actor)
	if err != nil {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: encode restore request actor: %w", err)
	}
	gotActor, err := state.CanonicalJSON(receipt.Actor)
	if err != nil || !bytes.Equal(gotActor, wantActor) {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: restore receipt actor mismatch")
	}
	if receipt.Outcome != "clean" && receipt.Outcome != "conflicted" {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: invalid restore receipt outcome")
	}
	if receipt.CreatedAt.IsZero() || !zeroOffsetTime(receipt.CreatedAt) {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: invalid restore receipt timestamp")
	}

	private, schemaVersion, workspaceRevision, err := decodeRestoreReceiptJSONAny(receipt.ResultJSON)
	if err != nil {
		return decodedRestoreReceipt{}, err
	}
	if private.Action != "restore" || private.Outcome != receipt.Outcome {
		return decodedRestoreReceipt{}, fmt.Errorf("projectstate: invalid restore receipt envelope")
	}
	result, err := publicRestoreResult(private.Result)
	if err != nil {
		return decodedRestoreReceipt{}, err
	}
	if err := validateRestoreReceiptOutcome(private.Result, private.Outcome, private.ConflictRetryDigest); err != nil {
		return decodedRestoreReceipt{}, err
	}

	var retryDigest *state.Digest
	if private.ConflictRetryDigest != nil {
		owned := *private.ConflictRetryDigest
		retryDigest = &owned
	}
	return decodedRestoreReceipt{
		Result: result, Outcome: private.Outcome, ConflictRetryDigest: retryDigest,
		SchemaVersion: schemaVersion, WorkspaceRevision: workspaceRevision,
	}, nil
}

func decodeRestoreReceiptJSONAny(raw json.RawMessage) (restoreStashReceiptV1, int, int64, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return restoreStashReceiptV1{}, 0, 0, fmt.Errorf("projectstate: decode restore receipt header: %w", err)
	}
	switch header.SchemaVersion {
	case 1:
		private, err := decodeRestoreReceiptJSON(raw)
		if err != nil {
			return restoreStashReceiptV1{}, 0, 0, err
		}
		revision := int64(0)
		if private.Outcome == "conflicted" {
			revision = 1
		}
		return private, 1, revision, nil
	case 2:
		private, err := decodeRestoreReceiptJSONV2(raw)
		if err != nil {
			return restoreStashReceiptV1{}, 0, 0, err
		}
		if private.WorkspaceRevision < 1 {
			return restoreStashReceiptV1{}, 0, 0, fmt.Errorf("projectstate: invalid restore receipt workspace revision")
		}
		return restoreStashReceiptV1{
			SchemaVersion: 2, Action: private.Action, Outcome: private.Outcome,
			Result: private.Result, ConflictRetryDigest: private.ConflictRetryDigest,
		}, 2, private.WorkspaceRevision, nil
	default:
		return restoreStashReceiptV1{}, 0, 0, fmt.Errorf("projectstate: unsupported restore receipt schema version")
	}
}

func decodeRestoreReceiptJSONV2(raw json.RawMessage) (restoreStashReceiptV2, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt restoreStashReceiptV2
	if err := decoder.Decode(&receipt); err != nil {
		return restoreStashReceiptV2{}, fmt.Errorf("projectstate: decode restore receipt v2: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return restoreStashReceiptV2{}, fmt.Errorf("projectstate: decode restore receipt v2: trailing JSON")
		}
		return restoreStashReceiptV2{}, fmt.Errorf("projectstate: decode restore receipt v2: %w", err)
	}
	canonical, err := state.CanonicalJSON(receipt)
	if err != nil {
		return restoreStashReceiptV2{}, fmt.Errorf("projectstate: canonicalize restore receipt v2: %w", err)
	}
	if !bytes.Equal(canonical, raw) {
		return restoreStashReceiptV2{}, fmt.Errorf("projectstate: restore receipt v2 is not canonical JSON")
	}
	return receipt, nil
}

func decodeRestoreReceiptJSON(raw json.RawMessage) (restoreStashReceiptV1, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt restoreStashReceiptV1
	if err := decoder.Decode(&receipt); err != nil {
		return restoreStashReceiptV1{}, fmt.Errorf("projectstate: decode restore receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return restoreStashReceiptV1{}, fmt.Errorf("projectstate: decode restore receipt: trailing JSON")
		}
		return restoreStashReceiptV1{}, fmt.Errorf("projectstate: decode restore receipt: %w", err)
	}
	canonical, err := state.CanonicalJSON(receipt)
	if err != nil {
		return restoreStashReceiptV1{}, fmt.Errorf("projectstate: canonicalize restore receipt: %w", err)
	}
	if !bytes.Equal(canonical, raw) {
		return restoreStashReceiptV1{}, fmt.Errorf("projectstate: restore receipt is not canonical JSON")
	}
	return receipt, nil
}

func privateRestoreResult(result RestoreStashResult) (restoreStashResultV1, error) {
	if !validImportDigest(result.RestoredDigest) {
		return restoreStashResultV1{}, fmt.Errorf("projectstate: invalid restored digest")
	}
	if result.RebasedThroughGeneration < 0 {
		return restoreStashResultV1{}, fmt.Errorf("projectstate: invalid restore generation")
	}
	conflicts, err := privateTransitionConflicts(result.Conflicts)
	if err != nil {
		return restoreStashResultV1{}, err
	}
	return restoreStashResultV1{
		RestoredDigest: result.RestoredDigest, RebasedThroughGeneration: result.RebasedThroughGeneration,
		Conflicts: conflicts, StashRetained: result.StashRetained,
	}, nil
}

func publicRestoreResult(result restoreStashResultV1) (RestoreStashResult, error) {
	if !validImportDigest(result.RestoredDigest) {
		return RestoreStashResult{}, fmt.Errorf("projectstate: invalid restored digest")
	}
	if result.RebasedThroughGeneration < 0 {
		return RestoreStashResult{}, fmt.Errorf("projectstate: invalid restore generation")
	}
	conflicts, err := publicTransitionConflicts(result.Conflicts)
	if err != nil {
		return RestoreStashResult{}, err
	}
	return RestoreStashResult{
		RestoredDigest: result.RestoredDigest, RebasedThroughGeneration: result.RebasedThroughGeneration,
		Conflicts: conflicts, StashRetained: result.StashRetained,
	}, nil
}

func privateTransitionConflicts(conflicts []Conflict) ([]transitionConflictV1, error) {
	if conflicts == nil {
		return nil, fmt.Errorf("projectstate: restore conflicts must be an array")
	}
	private := make([]transitionConflictV1, len(conflicts))
	var previous Conflict
	for index, value := range conflicts {
		conflict, err := canonicalConflictForStorage(value, true)
		if err != nil {
			return nil, fmt.Errorf("projectstate: encode restore conflict %d: %w", index, err)
		}
		if index > 0 && compareConflicts(previous, conflict) >= 0 {
			return nil, fmt.Errorf("projectstate: restore conflicts are unordered or duplicated")
		}
		private[index] = transitionConflictV1{
			ID: conflict.ID, Key: transitionRecordKeyV1{Kind: conflict.Key.Kind, ID: conflict.Key.ID},
			FieldPath: conflict.FieldPath, Kind: conflict.Kind,
			Base: cloneRestoreFieldValue(conflict.Base), Ours: cloneRestoreFieldValue(conflict.Ours),
			Theirs: cloneRestoreFieldValue(conflict.Theirs),
		}
		previous = conflict
	}
	return private, nil
}

func publicTransitionConflicts(conflicts []transitionConflictV1) ([]Conflict, error) {
	if conflicts == nil {
		return nil, fmt.Errorf("projectstate: restore conflicts must be an array")
	}
	public := make([]Conflict, len(conflicts))
	var previous Conflict
	for index, value := range conflicts {
		conflict, err := canonicalConflictForStorage(Conflict{
			ID: value.ID, Key: state.RecordKey{Kind: value.Key.Kind, ID: value.Key.ID},
			FieldPath: value.FieldPath, Kind: value.Kind,
			Base: cloneRestoreFieldValue(value.Base), Ours: cloneRestoreFieldValue(value.Ours),
			Theirs: cloneRestoreFieldValue(value.Theirs),
		}, true)
		if err != nil {
			return nil, fmt.Errorf("projectstate: decode restore conflict %d: %w", index, err)
		}
		if index > 0 && compareConflicts(previous, conflict) >= 0 {
			return nil, fmt.Errorf("projectstate: restore conflicts are unordered or duplicated")
		}
		public[index] = conflict
		previous = conflict
	}
	return public, nil
}

func validateRestoreReceiptOutcome(result restoreStashResultV1, outcome string, retryDigest *state.Digest) error {
	if result.Conflicts == nil {
		return fmt.Errorf("projectstate: restore conflicts must be an array")
	}
	switch outcome {
	case "clean":
		if len(result.Conflicts) != 0 || result.StashRetained || retryDigest != nil {
			return fmt.Errorf("projectstate: invalid clean restore result")
		}
	case "conflicted":
		if len(result.Conflicts) == 0 || !result.StashRetained || retryDigest == nil || !validImportDigest(*retryDigest) {
			return fmt.Errorf("projectstate: invalid conflicted restore result")
		}
	default:
		return fmt.Errorf("projectstate: invalid restore outcome")
	}
	return nil
}

func cloneRestoreFieldValue(value FieldValue) FieldValue {
	return FieldValue{Present: value.Present, Value: bytes.Clone(value.Value)}
}
