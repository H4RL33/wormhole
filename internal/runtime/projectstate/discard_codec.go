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

type discardRequestDigestV1 struct {
	SchemaVersion   int                      `json:"schema_version"`
	Action          string                   `json:"action"`
	Scope           types.WorkspaceScope     `json:"scope"`
	Actor           types.ActorEnvelope      `json:"actor"`
	ExpectedBinding workspaceBindingDigestV1 `json:"expected_binding"`
	CanonicalRoot   string                   `json:"canonical_root"`
	ExpectedCommit  string                   `json:"expected_commit"`
}

type discardResultV1 struct {
	PreviousCommit     string                 `json:"previous_commit"`
	ObservedCommit     string                 `json:"observed_commit"`
	PreviousRef        string                 `json:"previous_ref"`
	ObservedRef        string                 `json:"observed_ref"`
	PreviousBaseDigest state.Digest           `json:"previous_base_digest"`
	ObservedBaseDigest state.Digest           `json:"observed_base_digest"`
	CandidateAccepted  bool                   `json:"candidate_accepted"`
	AcceptedJournalID  *string                `json:"accepted_journal_id"`
	Rebased            bool                   `json:"rebased"`
	Conflicts          []transitionConflictV1 `json:"conflicts"`
}

type discardReceiptV1 struct {
	SchemaVersion int             `json:"schema_version"`
	Action        string          `json:"action"`
	Outcome       string          `json:"outcome"`
	Result        discardResultV1 `json:"result"`
}

func discardRequestDigestProjection(req ObserveGitBaseRequest) (discardRequestDigestV1, error) {
	if req.BranchAction != BranchSwitchDiscard {
		return discardRequestDigestV1{}, fmt.Errorf("projectstate: invalid discard branch action")
	}
	if !types.CanonicalUUID(req.Scope.ProjectID) || !types.CanonicalUUID(string(req.Scope.WorkspaceID)) || !types.CanonicalUUID(req.RequestID) {
		return discardRequestDigestV1{}, fmt.Errorf("projectstate: invalid discard scope or request ID")
	}
	if err := validateDiscardRequestUTF8(req); err != nil {
		return discardRequestDigestV1{}, err
	}
	if err := req.Actor.ValidateLocalAction(); err != nil {
		return discardRequestDigestV1{}, err
	}
	if err := req.ExpectedBinding.Validate(); err != nil {
		return discardRequestDigestV1{}, fmt.Errorf("projectstate: invalid discard expected binding: %w", err)
	}
	if req.Scope != req.ExpectedBinding.Scope {
		return discardRequestDigestV1{}, fmt.Errorf("projectstate: discard scope differs from expected binding")
	}
	if !validDiscardRef(req.ExpectedBinding.AcceptedRef) {
		return discardRequestDigestV1{}, fmt.Errorf("projectstate: invalid discard expected branch ref")
	}
	if err := (types.WorkspaceContext{WorkingDirectory: req.Root}).Validate(); err != nil || req.Root != req.ExpectedBinding.Checkout.CanonicalPath {
		return discardRequestDigestV1{}, fmt.Errorf("projectstate: discard root differs from expected binding")
	}
	if !validCommit(req.ExpectedCommit) {
		return discardRequestDigestV1{}, fmt.Errorf("projectstate: invalid discard expected commit")
	}
	binding, err := workspaceBindingDigest(req.ExpectedBinding)
	if err != nil {
		return discardRequestDigestV1{}, err
	}
	return discardRequestDigestV1{
		SchemaVersion: 1, Action: "discard", Scope: req.Scope, Actor: req.Actor,
		ExpectedBinding: binding, CanonicalRoot: req.Root, ExpectedCommit: req.ExpectedCommit,
	}, nil
}

func discardRequestDigest(req ObserveGitBaseRequest) (state.Digest, error) {
	projection, err := discardRequestDigestProjection(req)
	if err != nil {
		return "", err
	}
	digest, err := state.DigestCanonicalJSON(projection)
	if err != nil {
		return "", fmt.Errorf("projectstate: digest discard request: %w", err)
	}
	return digest, nil
}

func encodeDiscardReceipt(result ObserveGitBaseResult) (json.RawMessage, error) {
	private, err := privateDiscardResult(result)
	if err != nil {
		return nil, err
	}
	receipt := discardReceiptV1{SchemaVersion: 1, Action: "discard", Outcome: "clean", Result: private}
	if err := validateDiscardReceipt(receipt); err != nil {
		return nil, err
	}
	encoded, err := state.CanonicalJSON(receipt)
	if err != nil {
		return nil, fmt.Errorf("projectstate: encode discard receipt: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func decodeDiscardReceipt(receipt *localstore.WorkspaceTransitionReceiptRecord, req ObserveGitBaseRequest, requestDigest state.Digest) (ObserveGitBaseResult, error) {
	expectedDigest, err := discardRequestDigest(req)
	if err != nil {
		return ObserveGitBaseResult{}, err
	}
	if !validImportDigest(requestDigest) || requestDigest != expectedDigest {
		return ObserveGitBaseResult{}, fmt.Errorf("projectstate: discard request digest mismatch")
	}
	if receipt == nil {
		return ObserveGitBaseResult{}, fmt.Errorf("projectstate: discard receipt is absent")
	}
	if receipt.RequestID != req.RequestID || !types.CanonicalUUID(receipt.RequestID) || receipt.Action != "discard" ||
		!validImportDigest(receipt.RequestDigest) || receipt.RequestDigest != requestDigest || receipt.Outcome != "clean" ||
		receipt.CreatedAt.IsZero() || !zeroOffsetTime(receipt.CreatedAt) {
		return ObserveGitBaseResult{}, fmt.Errorf("projectstate: discard receipt metadata mismatch")
	}
	if err := receipt.Actor.ValidateHistorical(); err != nil {
		return ObserveGitBaseResult{}, fmt.Errorf("projectstate: invalid discard receipt actor: %w", err)
	}
	wantActor, err := state.CanonicalJSON(req.Actor)
	if err != nil {
		return ObserveGitBaseResult{}, fmt.Errorf("projectstate: encode discard request actor: %w", err)
	}
	gotActor, err := state.CanonicalJSON(receipt.Actor)
	if err != nil || !bytes.Equal(gotActor, wantActor) {
		return ObserveGitBaseResult{}, fmt.Errorf("projectstate: discard receipt actor mismatch")
	}
	private, err := decodeDiscardReceiptJSON(receipt.ResultJSON)
	if err != nil {
		return ObserveGitBaseResult{}, err
	}
	if err := validateDiscardReceipt(private); err != nil {
		return ObserveGitBaseResult{}, err
	}
	result, err := publicDiscardResult(private.Result)
	if err != nil {
		return ObserveGitBaseResult{}, err
	}
	if result.PreviousCommit != req.ExpectedBinding.AcceptedCommitSHA ||
		result.PreviousRef != req.ExpectedBinding.AcceptedRef ||
		result.PreviousBaseDigest != state.Digest(req.ExpectedBinding.AcceptedTreeDigest) ||
		result.ObservedCommit != req.ExpectedCommit {
		return ObserveGitBaseResult{}, fmt.Errorf("projectstate: discard receipt result differs from request")
	}
	return result, nil
}

func decodeDiscardReceiptJSON(raw json.RawMessage) (discardReceiptV1, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt discardReceiptV1
	if err := decoder.Decode(&receipt); err != nil {
		return discardReceiptV1{}, fmt.Errorf("projectstate: decode discard receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return discardReceiptV1{}, fmt.Errorf("projectstate: decode discard receipt: trailing JSON")
		}
		return discardReceiptV1{}, fmt.Errorf("projectstate: decode discard receipt: %w", err)
	}
	canonical, err := state.CanonicalJSON(receipt)
	if err != nil || !bytes.Equal(canonical, raw) {
		return discardReceiptV1{}, fmt.Errorf("projectstate: discard receipt is not canonical JSON")
	}
	return receipt, nil
}

func privateDiscardResult(result ObserveGitBaseResult) (discardResultV1, error) {
	conflicts, err := privateTransitionConflicts(result.Conflicts)
	if err != nil {
		return discardResultV1{}, err
	}
	private := discardResultV1{
		PreviousCommit: result.PreviousCommit, ObservedCommit: result.ObservedCommit,
		PreviousRef: result.PreviousRef, ObservedRef: result.ObservedRef,
		PreviousBaseDigest: result.PreviousBaseDigest, ObservedBaseDigest: result.ObservedBaseDigest,
		CandidateAccepted: result.CandidateAccepted, Rebased: result.Rebased, Conflicts: conflicts,
	}
	if result.AcceptedJournalID != nil {
		value := *result.AcceptedJournalID
		private.AcceptedJournalID = &value
	}
	if err := validatePrivateDiscardResult(private); err != nil {
		return discardResultV1{}, err
	}
	return private, nil
}

func publicDiscardResult(result discardResultV1) (ObserveGitBaseResult, error) {
	if err := validatePrivateDiscardResult(result); err != nil {
		return ObserveGitBaseResult{}, err
	}
	conflicts, err := publicTransitionConflicts(result.Conflicts)
	if err != nil {
		return ObserveGitBaseResult{}, err
	}
	public := ObserveGitBaseResult{
		PreviousCommit: result.PreviousCommit, ObservedCommit: result.ObservedCommit,
		PreviousRef: result.PreviousRef, ObservedRef: result.ObservedRef,
		PreviousBaseDigest: result.PreviousBaseDigest, ObservedBaseDigest: result.ObservedBaseDigest,
		CandidateAccepted: result.CandidateAccepted, Rebased: result.Rebased, Conflicts: conflicts,
	}
	if result.AcceptedJournalID != nil {
		value := *result.AcceptedJournalID
		public.AcceptedJournalID = &value
	}
	return public, nil
}

func validateDiscardReceipt(receipt discardReceiptV1) error {
	if receipt.SchemaVersion != 1 || receipt.Action != "discard" || receipt.Outcome != "clean" {
		return fmt.Errorf("projectstate: invalid discard receipt envelope")
	}
	return validatePrivateDiscardResult(receipt.Result)
}

func validatePrivateDiscardResult(result discardResultV1) error {
	if !validCommit(result.PreviousCommit) || !validCommit(result.ObservedCommit) ||
		!validDiscardRef(result.PreviousRef) || !validDiscardRef(result.ObservedRef) ||
		!validImportDigest(result.PreviousBaseDigest) || !validImportDigest(result.ObservedBaseDigest) ||
		result.CandidateAccepted || result.AcceptedJournalID != nil || result.Rebased || result.Conflicts == nil || len(result.Conflicts) != 0 {
		return fmt.Errorf("projectstate: invalid discard result")
	}
	return nil
}

func validDiscardRef(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	if value == "" {
		return true
	}
	if !strings.HasPrefix(value, "refs/heads/") {
		return false
	}
	name := strings.TrimPrefix(value, "refs/heads/")
	if name == "" || strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validateDiscardRequestUTF8(req ObserveGitBaseRequest) error {
	values := []string{
		req.Root, req.ExpectedCommit,
		req.Scope.ProjectID, string(req.Scope.WorkspaceID), req.RequestID,
		req.ExpectedBinding.Scope.ProjectID, string(req.ExpectedBinding.Scope.WorkspaceID),
		req.ExpectedBinding.Checkout.CanonicalPath,
		req.ExpectedBinding.Repository.Provider, req.ExpectedBinding.Repository.ImmutableID,
		req.ExpectedBinding.Repository.CanonicalRemote, req.ExpectedBinding.AcceptedRef,
		req.ExpectedBinding.AcceptedCommitSHA, req.ExpectedBinding.AcceptedTreeDigest,
		string(req.Actor.ActorKind), req.Actor.HumanPrincipalID, req.Actor.AgentID,
		req.Actor.AccountableHumanID, req.Actor.SessionID, req.Actor.HarnessName,
		req.Actor.HarnessVersion, req.Actor.ModelName, req.Actor.ModelVersion,
		string(req.Actor.Assurance),
	}
	for _, value := range values {
		if !utf8.ValidString(value) {
			return fmt.Errorf("projectstate: discard request contains invalid UTF-8")
		}
	}
	return nil
}
