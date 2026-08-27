package localapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

// EnrolmentProtocolVersion is the version of Gateway's local enrolment
// request/result contract. Gateway accepts exactly this version until a later
// contract explicitly defines negotiation.
const EnrolmentProtocolVersion = 1

// EnrolmentToolName is the pre-credential local Gateway endpoint. It is
// exposed to same-user local MCP clients; Gateway owns all remote follow-on
// work and harnesses never contact Fabric directly.
const EnrolmentToolName = "wormhole.agent.enrol"

var (
	ErrUnsupportedEnrolmentVersion        = errors.New("localapi: unsupported enrolment version")
	ErrInvalidEnrolmentProjectBinding     = errors.New("localapi: invalid enrolment project binding")
	ErrMissingEnrolmentFabricAddress      = errors.New("localapi: missing enrolment Fabric address")
	ErrInvalidEnrolmentCredentialProfile  = errors.New("localapi: invalid enrolment credential profile")
	ErrDuplicateEnrolmentRepository       = errors.New("localapi: duplicate enrolment repository")
	ErrEnrolmentRoleOutsideEnvelope       = errors.New("localapi: enrolment role outside local envelope")
	ErrEnrolmentPermissionOutsideEnvelope = errors.New("localapi: enrolment permission outside local envelope")
	ErrMalformedEnrolmentIdempotencyKey   = errors.New("localapi: malformed enrolment idempotency key")
	ErrInvalidEnrolmentResult             = errors.New("localapi: invalid enrolment result")
	ErrEnrolmentPolicyUnavailable         = errors.New("localapi: enrolment policy unavailable")
	ErrEnrolmentExecutorUnavailable       = errors.New("localapi: Gateway enrolment execution is not configured")
)

// EnrolmentRequest is the version-1 request accepted over Gateway's local MCP
// socket before a Passport credential exists. ProjectID is the explicit local
// project binding; FabricAddress is data for Gateway's Fabric client, not an
// instruction for the CLI to contact Fabric directly.
type EnrolmentRequest struct {
	Version              int      `json:"version"`
	ProjectID            string   `json:"project_id"`
	Owner                string   `json:"owner"`
	Model                string   `json:"model"`
	Capabilities         []string `json:"capabilities"`
	Repositories         []string `json:"repositories"`
	Roles                []string `json:"roles"`
	RequestedPermissions []string `json:"requested_permissions"`
	FabricAddress        string   `json:"fabric_address"`
	IdempotencyKey       string   `json:"idempotency_key"`
	CredentialProfile    string   `json:"credential_profile"`
}

// fabricEnrolAgentInput is the private Gateway/Fabric registration shape.
// It is intentionally separate from EnrolmentRequest: the Fabric receives a
// canonical request digest and never receives the credential profile name.
type fabricEnrolAgentInput struct {
	ProjectID      string   `json:"project_id"`
	IdempotencyKey string   `json:"idempotency_key"`
	RequestHash    string   `json:"request_hash"`
	Permissions    []string `json:"permissions"`
	Owner          string   `json:"owner"`
	Model          string   `json:"model"`
	Capabilities   []string `json:"capabilities"`
	Repositories   []string `json:"repositories"`
	Roles          []string `json:"roles"`
	Reissue        bool     `json:"reissue,omitempty"`
}

type fabricEnrolAgentOutput struct {
	AgentID    string    `json:"agent_id"`
	PassportID string    `json:"passport_id"`
	Token      string    `json:"token,omitempty"`
	IssuedAt   time.Time `json:"issued_at"`
	Replay     bool      `json:"replay"`
	Reissued   bool      `json:"reissued"`
}

// EnrolmentPermissionEnvelope is local policy supplied by Gateway's caller.
// It is deliberately not serialized into EnrolmentRequest: clients cannot
// expand the set against which their requested roles and permissions are
// checked.
type EnrolmentPermissionEnvelope struct {
	Roles       []string
	Permissions []string
}

// EnrolmentPolicySource supplies the trusted local role/permission ceiling
// for a fresh daemon. Implementations come from operator-approved Gateway
// configuration, never from EnrolmentRequest or a credential that does not
// yet exist.
type EnrolmentPolicySource interface {
	EnrolmentPermissionEnvelope(ctx context.Context, projectID string) (EnrolmentPermissionEnvelope, error)
}

type EnrolmentState string

const (
	EnrolmentRequested              EnrolmentState = "requested"
	EnrolmentRegistrationInProgress EnrolmentState = "registration_in_progress"
	EnrolmentRegistered             EnrolmentState = "registered"
	EnrolmentCredentialsPersisted   EnrolmentState = "credentials_persisted"
	EnrolmentBootstrapInProgress    EnrolmentState = "bootstrap_in_progress"
	EnrolmentReady                  EnrolmentState = "ready"
	EnrolmentRecoveryRequired       EnrolmentState = "recovery_required"
	EnrolmentAttentionRequired      EnrolmentState = "attention_required"
	EnrolmentFailed                 EnrolmentState = "failed"
)

func EnrolmentStates() []EnrolmentState {
	return []EnrolmentState{
		EnrolmentRequested,
		EnrolmentRegistrationInProgress,
		EnrolmentRegistered,
		EnrolmentCredentialsPersisted,
		EnrolmentBootstrapInProgress,
		EnrolmentReady,
		EnrolmentRecoveryRequired,
		EnrolmentAttentionRequired,
		EnrolmentFailed,
	}
}

type EnrolmentResultCode string

const (
	EnrolmentFabricUnreachable             EnrolmentResultCode = "fabric_unreachable"
	EnrolmentInvalidProject                EnrolmentResultCode = "invalid_project"
	EnrolmentPermissionsRejected           EnrolmentResultCode = "permissions_rejected"
	EnrolmentDuplicateIdentity             EnrolmentResultCode = "duplicate_identity"
	EnrolmentRepositoryMismatch            EnrolmentResultCode = "repository_mismatch"
	EnrolmentCredentialPersistenceFailed   EnrolmentResultCode = "credential_persistence_failed"
	EnrolmentBootstrapFailedAfterEnrolment EnrolmentResultCode = "bootstrap_failed_after_enrolment"
	EnrolmentCheckpointPersistenceFailed   EnrolmentResultCode = "checkpoint_persistence_failed"
	EnrolmentCredentialsPersistedResult    EnrolmentResultCode = "credentials_persisted"
	EnrolmentSuccess                       EnrolmentResultCode = "success"
)

func EnrolmentResultCodes() []EnrolmentResultCode {
	return []EnrolmentResultCode{
		EnrolmentFabricUnreachable,
		EnrolmentInvalidProject,
		EnrolmentPermissionsRejected,
		EnrolmentDuplicateIdentity,
		EnrolmentRepositoryMismatch,
		EnrolmentCredentialPersistenceFailed,
		EnrolmentBootstrapFailedAfterEnrolment,
		EnrolmentCheckpointPersistenceFailed,
		EnrolmentCredentialsPersistedResult,
		EnrolmentSuccess,
	}
}

func EnrolmentResultContract(code EnrolmentResultCode) (EnrolmentState, bool, bool) {
	switch code {
	case EnrolmentFabricUnreachable:
		return EnrolmentFailed, true, true
	case EnrolmentInvalidProject, EnrolmentPermissionsRejected, EnrolmentDuplicateIdentity, EnrolmentRepositoryMismatch:
		return EnrolmentFailed, false, true
	case EnrolmentCredentialPersistenceFailed, EnrolmentBootstrapFailedAfterEnrolment:
		return EnrolmentRecoveryRequired, true, true
	case EnrolmentCheckpointPersistenceFailed:
		return EnrolmentAttentionRequired, false, true
	case EnrolmentCredentialsPersistedResult:
		return EnrolmentCredentialsPersisted, true, true
	case EnrolmentSuccess:
		return EnrolmentReady, false, true
	default:
		return "", false, false
	}
}

// EnrolmentResult is the tagged result union for a local enrolment attempt.
// It contains identity references but never a raw Passport token: credential
// persistence belongs to Gateway and precedes a success result.
type EnrolmentResult struct {
	Version           int                 `json:"version"`
	Code              EnrolmentResultCode `json:"code" enum:"fabric_unreachable,invalid_project,permissions_rejected,duplicate_identity,repository_mismatch,credential_persistence_failed,bootstrap_failed_after_enrolment,checkpoint_persistence_failed,credentials_persisted,success"`
	State             EnrolmentState      `json:"state" enum:"requested,registration_in_progress,registered,credentials_persisted,bootstrap_in_progress,ready,recovery_required,attention_required,failed"`
	IdempotencyKey    string              `json:"idempotency_key"`
	Retryable         bool                `json:"retryable"`
	AgentID           string              `json:"agent_id,omitempty"`
	PassportID        string              `json:"passport_id,omitempty"`
	CredentialProfile string              `json:"credential_profile,omitempty"`
	Message           string              `json:"message,omitempty"`
}

// ValidateEnrolmentRequest validates the versioned wire shape and checks the
// requested role/permission scope against local policy. The envelope is a
// trusted Gateway input, never a client-controlled request field.
func ValidateEnrolmentRequest(req EnrolmentRequest, envelope EnrolmentPermissionEnvelope) error {
	if err := validateEnrolmentRequestShape(req); err != nil {
		return err
	}
	if outside := firstOutsideEnvelope(req.Roles, envelope.Roles); outside != "" {
		return fmt.Errorf("%w: %q", ErrEnrolmentRoleOutsideEnvelope, outside)
	}
	if outside := firstOutsideEnvelope(req.RequestedPermissions, envelope.Permissions); outside != "" {
		return fmt.Errorf("%w: %q", ErrEnrolmentPermissionOutsideEnvelope, outside)
	}
	return nil
}

// ValidateEnrolmentRequestWithPolicy resolves the permission envelope from a
// trusted, pre-credential local policy source and then performs full request
// validation. A missing or failed source denies enrolment.
func ValidateEnrolmentRequestWithPolicy(ctx context.Context, req EnrolmentRequest, source EnrolmentPolicySource) error {
	if source == nil {
		return ErrEnrolmentPolicyUnavailable
	}
	envelope, err := source.EnrolmentPermissionEnvelope(ctx, req.ProjectID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEnrolmentPolicyUnavailable, err)
	}
	return ValidateEnrolmentRequest(req, envelope)
}

func validateEnrolmentRequestShape(req EnrolmentRequest) error {
	if req.Version != EnrolmentProtocolVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedEnrolmentVersion, req.Version, EnrolmentProtocolVersion)
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		return ErrInvalidEnrolmentProjectBinding
	}
	if strings.TrimSpace(req.FabricAddress) == "" {
		return ErrMissingEnrolmentFabricAddress
	}
	if err := validateEnrolmentCredentialProfile(req.CredentialProfile); err != nil {
		return err
	}
	seenRepositories := make(map[string]struct{}, len(req.Repositories))
	for _, repository := range req.Repositories {
		canonical := canonicalEnrolmentRepository(repository)
		if _, exists := seenRepositories[canonical]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateEnrolmentRepository, repository)
		}
		seenRepositories[canonical] = struct{}{}
	}
	if !validEnrolmentIdempotencyKey(req.IdempotencyKey) {
		return fmt.Errorf("%w: must be a canonical lowercase UUID", ErrMalformedEnrolmentIdempotencyKey)
	}
	return nil
}

func validateEnrolmentCredentialProfile(profile string) error {
	if profile == "" || strings.ContainsAny(profile, `/\`) || profile == "." || profile == ".." || strings.Contains(profile, "..") {
		return fmt.Errorf("%w: unsafe or empty profile name", ErrInvalidEnrolmentCredentialProfile)
	}
	return nil
}

// NewEnrolmentIdempotencyKey creates a UUIDv4-shaped attempt identifier. A
// caller generates it once when the user approves an attempt and reuses that
// exact value for every retry of the attempt.
func NewEnrolmentIdempotencyKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("localapi: generate enrolment idempotency key: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], raw[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], raw[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], raw[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], raw[10:16])
	return string(encoded), nil
}

func validEnrolmentIdempotencyKey(key string) bool {
	if len(key) != 36 {
		return false
	}
	for i := range key {
		switch i {
		case 8, 13, 18, 23:
			if key[i] != '-' {
				return false
			}
		default:
			if !((key[i] >= '0' && key[i] <= '9') || (key[i] >= 'a' && key[i] <= 'f')) {
				return false
			}
		}
	}
	return true
}

func firstOutsideEnvelope(requested, permitted []string) string {
	allowed := make(map[string]struct{}, len(permitted))
	for _, value := range permitted {
		allowed[strings.TrimSpace(value)] = struct{}{}
	}
	for _, value := range requested {
		value = strings.TrimSpace(value)
		if _, ok := allowed[value]; !ok {
			return value
		}
	}
	return ""
}

func canonicalEnrolmentRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	parsed, err := url.Parse(repository)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = cleanRepositoryPath(parsed.Path)
		return parsed.String()
	}
	if strings.Contains(repository, "://") {
		return repository
	}
	return cleanRepositoryPath(filepath.Clean(repository))
}

func cleanRepositoryPath(repositoryPath string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(repositoryPath))
	if strings.HasSuffix(strings.ToLower(cleaned), ".git") {
		cleaned = cleaned[:len(cleaned)-4]
	}
	return strings.TrimSuffix(cleaned, "/")
}

// CanTransitionEnrolment implements the documented lifecycle. Recovery may
// resume registration or bootstrap according to the last durable checkpoint;
// a retryable pre-registration failure restarts at requested with the same
// idempotency key.
type EnrolmentAttempt struct {
	State             EnrolmentState
	ResultCode        EnrolmentResultCode
	Retryable         bool
	DurableCheckpoint EnrolmentState
}

func CanTransitionEnrolment(attempt EnrolmentAttempt, to EnrolmentState) bool {
	allowed := map[EnrolmentState][]EnrolmentState{
		EnrolmentRequested:              {EnrolmentRegistrationInProgress, EnrolmentFailed},
		EnrolmentRegistrationInProgress: {EnrolmentRegistered, EnrolmentFailed},
		EnrolmentRegistered:             {EnrolmentCredentialsPersisted, EnrolmentRecoveryRequired},
		EnrolmentCredentialsPersisted:   {EnrolmentBootstrapInProgress, EnrolmentRecoveryRequired},
		EnrolmentBootstrapInProgress:    {EnrolmentReady, EnrolmentRecoveryRequired},
	}
	if attempt.State == EnrolmentFailed && attempt.Retryable && attempt.ResultCode == EnrolmentFabricUnreachable && attempt.DurableCheckpoint == EnrolmentRequested {
		allowed[EnrolmentFailed] = []EnrolmentState{EnrolmentRequested}
	}
	if attempt.State == EnrolmentRecoveryRequired && attempt.Retryable {
		switch {
		case attempt.ResultCode == EnrolmentCredentialPersistenceFailed && attempt.DurableCheckpoint == EnrolmentRegistered:
			allowed[EnrolmentRecoveryRequired] = []EnrolmentState{EnrolmentRegistrationInProgress}
		case attempt.ResultCode == EnrolmentBootstrapFailedAfterEnrolment && attempt.DurableCheckpoint == EnrolmentCredentialsPersisted:
			allowed[EnrolmentRecoveryRequired] = []EnrolmentState{EnrolmentBootstrapInProgress}
		}
	}
	for _, candidate := range allowed[attempt.State] {
		if candidate == to {
			return true
		}
	}
	return false
}

func (result EnrolmentResult) Validate() error {
	if result.Version != EnrolmentProtocolVersion || !validEnrolmentIdempotencyKey(result.IdempotencyKey) {
		return ErrInvalidEnrolmentResult
	}
	state, retryable, ok := EnrolmentResultContract(result.Code)
	if !ok || result.State != state || result.Retryable != retryable {
		return ErrInvalidEnrolmentResult
	}
	if result.Code == EnrolmentCredentialsPersistedResult {
		if err := validateEnrolmentCredentialProfile(result.CredentialProfile); err != nil {
			return ErrInvalidEnrolmentResult
		}
	}
	return nil
}

// handleEnrolmentContract validates and executes Gateway-owned registration
// and credential persistence. Task 3 truthfully stops at credentials_persisted;
// bootstrap and ready remain Task 4 concerns.
func (s *Server) handleEnrolmentContract(ctx context.Context, args json.RawMessage) (any, error) {
	var req EnrolmentRequest
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return nil, fmt.Errorf("localapi: decode enrolment request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("localapi: decode enrolment request: trailing data")
	}
	if err := ValidateEnrolmentRequestWithPolicy(ctx, req, s.enrolmentPolicy); err != nil {
		return nil, err
	}
	if s.credentialsDir == "" || s.writeCredentialProfile == nil || s.store == nil {
		return nil, ErrEnrolmentExecutorUnavailable
	}
	return s.executeEnrolment(ctx, req), nil
}

func (s *Server) executeEnrolment(ctx context.Context, req EnrolmentRequest) EnrolmentResult {
	requestHash, err := canonicalEnrolmentRequestHash(req)
	if err != nil {
		return enrolmentFailure(req, EnrolmentFabricUnreachable, "Gateway could not prepare the Fabric enrolment request.", "", "")
	}
	unlock := s.lockCredentialProfile(req.CredentialProfile)
	defer unlock()

	attempt, _, err := s.store.ResolveEnrolmentAttempt(ctx, localstore.EnrolmentAttemptRecord{
		ProjectID: req.ProjectID, IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash,
		State: string(EnrolmentRequested), CredentialProfile: req.CredentialProfile,
	})
	if err != nil {
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"The selected credential profile is already bound to another enrolment.", "", "")
	}
	// The durable local key is authoritative across CLI process restarts.
	req.IdempotencyKey = attempt.IdempotencyKey

	if credentials, readErr := runtimeconfig.ReadCredentialProfile(s.credentialsDir, req.CredentialProfile); readErr == nil {
		if attempt.AgentID != "" && attempt.PassportID != "" && credentials.Server == req.FabricAddress &&
			credentials.ProjectID == req.ProjectID && credentials.AgentID == attempt.AgentID &&
			credentials.PassportID == attempt.PassportID && credentials.Token != "" {
			if attempt.State == string(EnrolmentReady) {
				return enrolmentReady(req, attempt.AgentID, attempt.PassportID)
			}
			if attempt.State != string(EnrolmentRecoveryRequired) && attempt.State != string(EnrolmentBootstrapInProgress) && attempt.State != string(EnrolmentCredentialsPersisted) {
				_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentCredentialsPersisted), attempt.AgentID, attempt.PassportID, false)
			}
			return s.continueEnrolmentBootstrap(ctx, req, attempt, credentials)
		}
		_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentFailed), attempt.AgentID, attempt.PassportID, true)
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"The selected credential profile belongs to a different enrolment; choose another profile.", attempt.AgentID, attempt.PassportID)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentFailed), attempt.AgentID, attempt.PassportID, true)
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"The existing credential profile could not be read safely.", attempt.AgentID, attempt.PassportID)
	}

	if err := s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentRegistrationInProgress), attempt.AgentID, attempt.PassportID, false); err != nil {
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"Gateway could not durably checkpoint the enrolment attempt.", attempt.AgentID, attempt.PassportID)
	}
	registered, err := s.callFabricEnrolment(ctx, req, requestHash, false)
	if err != nil {
		result := classifyFabricEnrolmentFailure(req, err)
		_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(result.State), attempt.AgentID, attempt.PassportID, !result.Retryable)
		return result
	}
	if registered.AgentID == "" || registered.PassportID == "" ||
		(attempt.AgentID != "" && attempt.AgentID != registered.AgentID) ||
		(attempt.PassportID != "" && attempt.PassportID != registered.PassportID) {
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"Fabric returned identity references that do not match the durable enrolment attempt.", attempt.AgentID, attempt.PassportID)
	}
	attempt.AgentID = registered.AgentID
	attempt.PassportID = registered.PassportID
	if err := s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentRegistered), attempt.AgentID, attempt.PassportID, false); err != nil {
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"Gateway registered the identity but could not durably checkpoint its references.", attempt.AgentID, attempt.PassportID)
	}

	if registered.Token == "" {
		// Re-check after Fabric replay in case another process created the file.
		credentials, readErr := runtimeconfig.ReadCredentialProfile(s.credentialsDir, req.CredentialProfile)
		if readErr == nil {
			if credentials.Server == req.FabricAddress && credentials.ProjectID == req.ProjectID &&
				credentials.AgentID == attempt.AgentID && credentials.PassportID == attempt.PassportID && credentials.Token != "" {
				_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentCredentialsPersisted), attempt.AgentID, attempt.PassportID, false)
				return s.continueEnrolmentBootstrap(ctx, req, attempt, credentials)
			}
			return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
				"The selected credential profile belongs to a different enrolment; choose another profile.", attempt.AgentID, attempt.PassportID)
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
				"The existing credential profile could not be read safely.", attempt.AgentID, attempt.PassportID)
		}
		registered, err = s.callFabricEnrolment(ctx, req, requestHash, true)
		if err != nil {
			return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
				"Credential recovery requires operator intervention.", attempt.AgentID, attempt.PassportID)
		}
		if registered.AgentID != attempt.AgentID || registered.PassportID != attempt.PassportID {
			return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
				"Fabric reissue references do not match the durable enrolment attempt.", attempt.AgentID, attempt.PassportID)
		}
	}
	if registered.Token == "" {
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"Fabric did not provide credential material for this recoverable attempt.", attempt.AgentID, attempt.PassportID)
	}

	role := ""
	if len(req.Roles) == 1 {
		role = req.Roles[0]
	}
	_, err = s.writeCredentialProfile(s.credentialsDir, req.CredentialProfile, runtimeconfig.Credentials{
		Server: req.FabricAddress, ProjectID: req.ProjectID, AgentID: attempt.AgentID,
		PassportID: attempt.PassportID, Token: registered.Token, IssuedAt: registered.IssuedAt, Role: role,
	})
	if err != nil {
		// Never include the dependency error: a filesystem wrapper may have
		// echoed the credential value it was attempting to persist.
		_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentRecoveryRequired), attempt.AgentID, attempt.PassportID, false)
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"Gateway registered the identity but could not commit its credential profile.", attempt.AgentID, attempt.PassportID)
	}
	if err := s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentCredentialsPersisted), attempt.AgentID, attempt.PassportID, false); err != nil {
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"Gateway committed credentials but could not checkpoint the enrolment state.", attempt.AgentID, attempt.PassportID)
	}
	return s.continueEnrolmentBootstrap(ctx, req, attempt, runtimeconfig.Credentials{
		Server: req.FabricAddress, ProjectID: req.ProjectID, AgentID: attempt.AgentID,
		PassportID: attempt.PassportID, Token: registered.Token, IssuedAt: registered.IssuedAt, Role: role,
	})
}

func (s *Server) lockCredentialProfile(profile string) func() {
	value, _ := s.credentialProfileLocks.LoadOrStore(profile, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func (s *Server) callFabricEnrolment(ctx context.Context, req EnrolmentRequest, requestHash string, reissue bool) (fabricEnrolAgentOutput, error) {
	arguments, err := json.Marshal(fabricEnrolAgentInput{
		ProjectID: req.ProjectID, IdempotencyKey: req.IdempotencyKey, RequestHash: requestHash,
		Permissions: req.RequestedPermissions, Owner: req.Owner, Model: req.Model,
		Capabilities: req.Capabilities, Repositories: canonicalEnrolmentRepositories(req.Repositories),
		Roles: req.Roles, Reissue: reissue,
	})
	if err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: encode Fabric enrolment: %w", err)
	}
	params, err := json.Marshal(toolsCallParams{Name: EnrolmentToolName, Arguments: arguments})
	if err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: encode Fabric enrolment params: %w", err)
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call", Params: params})
	if err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: encode Fabric enrolment RPC: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(req.FabricAddress, "/")+"/mcp", bytes.NewReader(body))
	if err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: build Fabric enrolment request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := s.httpClient.Do(httpRequest)
	if err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: call Fabric enrolment: %w", err)
	}
	defer response.Body.Close()
	var rpcResponse rpcResponse
	if err := json.NewDecoder(response.Body).Decode(&rpcResponse); err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: decode Fabric enrolment response: %w", err)
	}
	if rpcResponse.Error != nil {
		return fabricEnrolAgentOutput{}, errors.New(rpcResponse.Error.Message)
	}
	var result toolCallResult
	if err := json.Unmarshal(rpcResponse.Result, &result); err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: decode Fabric enrolment tool result: %w", err)
	}
	if len(result.Content) == 0 {
		return fabricEnrolAgentOutput{}, errors.New("localapi: empty Fabric enrolment result")
	}
	if result.IsError {
		return fabricEnrolAgentOutput{}, errors.New(result.Content[0].Text)
	}
	var output fabricEnrolAgentOutput
	if err := json.Unmarshal([]byte(result.Content[0].Text), &output); err != nil {
		return fabricEnrolAgentOutput{}, fmt.Errorf("localapi: decode Fabric enrolment output: %w", err)
	}
	return output, nil
}

func canonicalEnrolmentRequestHash(req EnrolmentRequest) (string, error) {
	canonical := req
	canonical.IdempotencyKey = ""
	canonical.ProjectID = strings.TrimSpace(canonical.ProjectID)
	canonical.FabricAddress = strings.TrimRight(strings.TrimSpace(canonical.FabricAddress), "/")
	canonical.Repositories = canonicalEnrolmentRepositories(canonical.Repositories)
	canonical.Capabilities = sortedEnrolmentValues(canonical.Capabilities)
	canonical.Roles = sortedEnrolmentValues(canonical.Roles)
	canonical.RequestedPermissions = sortedEnrolmentValues(canonical.RequestedPermissions)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalEnrolmentRepositories(repositories []string) []string {
	canonical := make([]string, len(repositories))
	for i, repository := range repositories {
		canonical[i] = canonicalEnrolmentRepository(repository)
	}
	sort.Strings(canonical)
	return canonical
}

func sortedEnrolmentValues(values []string) []string {
	out := append([]string(nil), values...)
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	sort.Strings(out)
	return out
}

func enrolmentPersisted(req EnrolmentRequest, agentID, passportID string) EnrolmentResult {
	return EnrolmentResult{
		Version: EnrolmentProtocolVersion, Code: EnrolmentCredentialsPersistedResult,
		State: EnrolmentCredentialsPersisted, IdempotencyKey: req.IdempotencyKey, Retryable: true,
		AgentID: agentID, PassportID: passportID, CredentialProfile: req.CredentialProfile,
	}
}

func enrolmentFailure(req EnrolmentRequest, code EnrolmentResultCode, message, agentID, passportID string) EnrolmentResult {
	state, retryable, _ := EnrolmentResultContract(code)
	return EnrolmentResult{
		Version: EnrolmentProtocolVersion, Code: code, State: state,
		IdempotencyKey: req.IdempotencyKey, Retryable: retryable,
		AgentID: agentID, PassportID: passportID, CredentialProfile: req.CredentialProfile, Message: message,
	}
}

func classifyFabricEnrolmentFailure(req EnrolmentRequest, err error) EnrolmentResult {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "idempotency conflict"):
		return enrolmentFailure(req, EnrolmentDuplicateIdentity, "Fabric rejected the enrolment attempt key.", "", "")
	case strings.Contains(message, "already in progress"):
		return enrolmentFailure(req, EnrolmentFabricUnreachable, "Fabric is still completing the enrolment attempt.", "", "")
	case strings.Contains(message, "invalid scope"), strings.Contains(message, "violates foreign key"):
		return enrolmentFailure(req, EnrolmentInvalidProject, "Fabric rejected the project binding.", "", "")
	default:
		return enrolmentFailure(req, EnrolmentFabricUnreachable, "Fabric registration could not be confirmed.", "", "")
	}
}
