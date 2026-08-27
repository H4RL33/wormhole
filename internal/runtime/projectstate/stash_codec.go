package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

type StashRequest struct {
	Scope     types.WorkspaceScope
	RequestID string
	Actor     types.ActorEnvelope
	Label     string
}

type StashResult struct {
	StashID         string
	SourceDigest    state.Digest
	CandidateDigest state.Digest
	OperationCount  int
}

type stashRequestDigestV1 struct {
	SchemaVersion int                  `json:"schema_version"`
	Action        string               `json:"action"`
	Scope         types.WorkspaceScope `json:"scope"`
	Actor         types.ActorEnvelope  `json:"actor"`
	Label         string               `json:"label"`
}

type stashResultV1 struct {
	StashID         string       `json:"stash_id"`
	SourceDigest    state.Digest `json:"source_digest"`
	CandidateDigest state.Digest `json:"candidate_digest"`
	OperationCount  int          `json:"operation_count"`
}

type stashReceiptV1 struct {
	SchemaVersion int           `json:"schema_version"`
	Action        string        `json:"action"`
	Outcome       string        `json:"outcome"`
	Result        stashResultV1 `json:"result"`
}

func stashRequestDigest(req StashRequest) (state.Digest, error) {
	if !types.CanonicalUUID(req.Scope.ProjectID) || !types.CanonicalUUID(string(req.Scope.WorkspaceID)) {
		return "", fmt.Errorf("projectstate: invalid stash scope")
	}
	if !types.CanonicalUUID(req.RequestID) {
		return "", fmt.Errorf("projectstate: invalid stash request ID")
	}
	if err := req.Actor.ValidateLocalAction(); err != nil {
		return "", err
	}
	if !utf8.ValidString(req.Label) || len(req.Label) == 0 || len(req.Label) > 256 || strings.ContainsAny(req.Label, "\x00\r\n") {
		return "", fmt.Errorf("projectstate: invalid stash label")
	}
	digest, err := state.DigestCanonicalJSON(stashRequestDigestV1{
		SchemaVersion: 1, Action: "stash", Scope: req.Scope, Actor: req.Actor, Label: req.Label,
	})
	if err != nil {
		return "", fmt.Errorf("projectstate: digest stash request: %w", err)
	}
	return digest, nil
}

func encodeStashReceipt(result StashResult) (json.RawMessage, error) {
	private, err := privateStashResult(result)
	if err != nil {
		return nil, err
	}
	encoded, err := state.CanonicalJSON(stashReceiptV1{
		SchemaVersion: 1, Action: "stash", Outcome: "clean", Result: private,
	})
	if err != nil {
		return nil, fmt.Errorf("projectstate: encode stash receipt: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func decodeStashReceipt(receipt *localstore.WorkspaceTransitionReceiptRecord, req StashRequest) (StashResult, error) {
	expectedDigest, err := stashRequestDigest(req)
	if err != nil {
		return StashResult{}, err
	}
	if receipt == nil {
		return StashResult{}, fmt.Errorf("projectstate: stash receipt is absent")
	}
	if receipt.RequestID != req.RequestID || !types.CanonicalUUID(receipt.RequestID) {
		return StashResult{}, fmt.Errorf("projectstate: stash receipt request ID mismatch")
	}
	if receipt.Action != "stash" {
		return StashResult{}, fmt.Errorf("projectstate: stash receipt action mismatch")
	}
	if !validImportDigest(receipt.RequestDigest) || receipt.RequestDigest != expectedDigest {
		return StashResult{}, fmt.Errorf("projectstate: stash receipt request digest mismatch")
	}
	if err := receipt.Actor.ValidateHistorical(); err != nil {
		return StashResult{}, fmt.Errorf("projectstate: invalid stash receipt actor: %w", err)
	}
	wantActor, err := state.CanonicalJSON(req.Actor)
	if err != nil {
		return StashResult{}, fmt.Errorf("projectstate: encode stash request actor: %w", err)
	}
	gotActor, err := state.CanonicalJSON(receipt.Actor)
	if err != nil || !bytes.Equal(gotActor, wantActor) {
		return StashResult{}, fmt.Errorf("projectstate: stash receipt actor mismatch")
	}
	if receipt.Outcome != "clean" {
		return StashResult{}, fmt.Errorf("projectstate: stash receipt outcome mismatch")
	}
	if receipt.CreatedAt.IsZero() || !zeroOffsetTime(receipt.CreatedAt) {
		return StashResult{}, fmt.Errorf("projectstate: invalid stash receipt timestamp")
	}
	return decodeStashReceiptJSON(receipt.ResultJSON)
}

func decodeStashReceiptJSON(raw json.RawMessage) (StashResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt stashReceiptV1
	if err := decoder.Decode(&receipt); err != nil {
		return StashResult{}, fmt.Errorf("projectstate: decode stash receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return StashResult{}, fmt.Errorf("projectstate: decode stash receipt: trailing JSON")
		}
		return StashResult{}, fmt.Errorf("projectstate: decode stash receipt: %w", err)
	}
	canonical, err := state.CanonicalJSON(receipt)
	if err != nil {
		return StashResult{}, fmt.Errorf("projectstate: canonicalize stash receipt: %w", err)
	}
	if !bytes.Equal(canonical, raw) {
		return StashResult{}, fmt.Errorf("projectstate: stash receipt is not canonical JSON")
	}
	if receipt.SchemaVersion != 1 || receipt.Action != "stash" || receipt.Outcome != "clean" {
		return StashResult{}, fmt.Errorf("projectstate: invalid stash receipt envelope")
	}
	return publicStashResult(receipt.Result)
}

func privateStashResult(result StashResult) (stashResultV1, error) {
	private := stashResultV1{
		StashID: result.StashID, SourceDigest: result.SourceDigest,
		CandidateDigest: result.CandidateDigest, OperationCount: result.OperationCount,
	}
	if err := validatePrivateStashResult(private); err != nil {
		return stashResultV1{}, err
	}
	return private, nil
}

func publicStashResult(result stashResultV1) (StashResult, error) {
	if err := validatePrivateStashResult(result); err != nil {
		return StashResult{}, err
	}
	return StashResult{
		StashID: result.StashID, SourceDigest: result.SourceDigest,
		CandidateDigest: result.CandidateDigest, OperationCount: result.OperationCount,
	}, nil
}

func validatePrivateStashResult(result stashResultV1) error {
	if !canonicalUUIDv4(result.StashID) {
		return fmt.Errorf("projectstate: invalid stash ID")
	}
	if !validImportDigest(result.SourceDigest) || !validImportDigest(result.CandidateDigest) {
		return fmt.Errorf("projectstate: invalid stash result digest")
	}
	if result.OperationCount < 0 {
		return fmt.Errorf("projectstate: invalid stash operation count")
	}
	return nil
}
