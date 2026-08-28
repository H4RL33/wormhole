package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

func validEnrolmentRequest() EnrolmentRequest {
	return EnrolmentRequest{
		Version:              EnrolmentProtocolVersion,
		ProjectID:            "project-1",
		Owner:                "harley",
		Model:                "gpt-5",
		Capabilities:         []string{"code", "review"},
		Repositories:         []string{"https://github.com/H4RL33/wormhole.git"},
		Roles:                []string{"contributor"},
		RequestedPermissions: []string{"task.create", "kb.write"},
		FabricAddress:        "https://fabric.example.test",
		IdempotencyKey:       "018f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1",
		CredentialProfile:    "project-1__default",
	}
}

func fabricEnrolmentResponse(t *testing.T, w http.ResponseWriter, id json.RawMessage, out fabricEnrolAgentOutput) {
	t.Helper()
	outRaw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal Fabric enrolment output: %v", err)
	}
	resultRaw, err := json.Marshal(toolCallResult{Content: []toolCallResultContent{{Type: "text", Text: string(outRaw)}}})
	if err != nil {
		t.Fatalf("marshal Fabric tool result: %v", err)
	}
	if err := json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: resultRaw}); err != nil {
		t.Fatalf("encode Fabric RPC response: %v", err)
	}
}

func TestRetainedEnrolmentBoundaryExecutesFabricAndPersistsTokenFreeResult(t *testing.T) {
	const rawToken = "fabric-one-time-secret-token"
	var calls int
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var rpcReq rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("decode Fabric request: %v", err)
		}
		var params toolsCallParams
		if err := json.Unmarshal(rpcReq.Params, &params); err != nil {
			t.Fatalf("decode Fabric params: %v", err)
		}
		if params.Name != EnrolmentToolName {
			t.Fatalf("Fabric tool = %q, want %q", params.Name, EnrolmentToolName)
		}
		var in fabricEnrolAgentInput
		if err := json.Unmarshal(params.Arguments, &in); err != nil {
			t.Fatalf("decode Fabric enrolment input: %v", err)
		}
		if in.ProjectID != "project-1" || in.RequestHash == "" || in.IdempotencyKey != validEnrolmentRequest().IdempotencyKey {
			t.Fatalf("Fabric enrolment input = %+v", in)
		}
		fabricEnrolmentResponse(t, w, rpcReq.ID, fabricEnrolAgentOutput{
			AgentID: "agent-1", PassportID: "passport-1", Token: rawToken,
			IssuedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		})
	}))
	defer fabric.Close()

	srv, _ := newMCPTestServer(t)
	credentialsDir := filepath.Join(t.TempDir(), "credentials")
	srv.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, credentialsDir)
	req := validEnrolmentRequest()
	req.FabricAddress = fabric.URL
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	result, err := srv.handleEnrolmentContract(context.Background(), raw)
	if err != nil {
		t.Fatalf("enrolment response error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Fabric calls = %d, want 1", calls)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), rawToken) {
		t.Fatalf("local response exposed raw token: result=%s", encoded)
	}
	var got EnrolmentResult
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != EnrolmentCredentialsPersistedResult || got.State != EnrolmentCredentialsPersisted || got.AgentID != "agent-1" || got.PassportID != "passport-1" {
		t.Fatalf("local enrolment result = %+v", got)
	}
	credentialPath := filepath.Join(credentialsDir, req.CredentialProfile+".json")
	data, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatalf("read persisted credential: %v", err)
	}
	var credentials runtimeconfig.Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		t.Fatalf("decode persisted credential: %v", err)
	}
	if credentials.Token != rawToken || credentials.AgentID != "agent-1" || credentials.PassportID != "passport-1" || credentials.ProjectID != req.ProjectID || credentials.Server != fabric.URL {
		t.Fatalf("persisted credential fields: token_match=%v agent=%q passport=%q project=%q server=%q",
			credentials.Token == rawToken, credentials.AgentID, credentials.PassportID, credentials.ProjectID, credentials.Server)
	}
	info, err := os.Stat(credentialPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v, %v; want 0600", info, err)
	}
}

func TestRetainedEnrolmentBoundaryCredentialFailureIsRecoverableAndRedacted(t *testing.T) {
	const rawToken = "credential-write-secret"
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("decode Fabric request: %v", err)
		}
		fabricEnrolmentResponse(t, w, rpcReq.ID, fabricEnrolAgentOutput{
			AgentID: "agent-failed", PassportID: "passport-failed", Token: rawToken, IssuedAt: time.Now().UTC(),
		})
	}))
	defer fabric.Close()

	srv, _ := newMCPTestServer(t)
	srv.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, filepath.Join(t.TempDir(), "credentials"))
	srv.writeCredentialProfile = func(string, string, runtimeconfig.Credentials) (string, error) {
		return "", errors.New("forced credential failure mentioning " + rawToken)
	}
	req := validEnrolmentRequest()
	req.FabricAddress = fabric.URL
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	result, err := srv.handleEnrolmentContract(context.Background(), raw)
	if err != nil {
		t.Fatalf("credential failure became boundary error: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), rawToken) {
		t.Fatalf("credential failure exposed raw token: result=%s", encoded)
	}
	var got EnrolmentResult
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != EnrolmentCredentialPersistenceFailed || got.State != EnrolmentRecoveryRequired || !got.Retryable || got.AgentID != "agent-failed" || got.PassportID != "passport-failed" {
		t.Fatalf("credential failure result = %+v", got)
	}
}

func TestCanonicalEnrolmentDigestDoesNotChangeWithClientRetryKey(t *testing.T) {
	first := validEnrolmentRequest()
	second := first
	second.IdempotencyKey = "118f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	firstHash, err := canonicalEnrolmentRequestHash(first)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	secondHash, err := canonicalEnrolmentRequestHash(second)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("client retry key changed canonical digest: first=%q second=%q", firstHash, secondHash)
	}
}

func TestFabricInProgressFailureIsRetryableNotDuplicateIdentity(t *testing.T) {
	result := classifyFabricEnrolmentFailure(validEnrolmentRequest(), errors.New("identity: enrolment already in progress"))
	if result.Code != EnrolmentFabricUnreachable || !result.Retryable {
		t.Fatalf("in-progress classification: code=%q retryable=%v", result.Code, result.Retryable)
	}
}

func TestEnrolmentResumesDurableAttemptAfterRestartAndReissuesOnce(t *testing.T) {
	const firstToken = "first-write-lost-token"
	const reissuedToken = "controlled-reissue-token"
	var mu sync.Mutex
	var inputs []fabricEnrolAgentInput
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Errorf("decode Fabric request: %v", err)
			return
		}
		var params toolsCallParams
		if err := json.Unmarshal(rpcReq.Params, &params); err != nil {
			t.Errorf("decode Fabric params: %v", err)
			return
		}
		var in fabricEnrolAgentInput
		if err := json.Unmarshal(params.Arguments, &in); err != nil {
			t.Errorf("decode Fabric input: %v", err)
			return
		}
		mu.Lock()
		inputs = append(inputs, in)
		callNumber := len(inputs)
		mu.Unlock()
		token := ""
		if callNumber == 1 {
			token = firstToken
		}
		if in.Reissue {
			token = reissuedToken
		}
		fabricEnrolmentResponse(t, w, rpcReq.ID, fabricEnrolAgentOutput{
			AgentID: "agent-restart", PassportID: "passport-restart", Token: token, IssuedAt: time.Now().UTC(),
			Replay: in.Reissue, Reissued: in.Reissue,
		})
	}))
	defer fabric.Close()

	databasePath := filepath.Join(t.TempDir(), "wormholed.db")
	credentialsDir := filepath.Join(t.TempDir(), "credentials")
	store, err := localstore.Open(databasePath)
	if err != nil {
		t.Fatalf("open first local store: %v", err)
	}
	firstServer := &Server{store: store, httpClient: fabric.Client()}
	firstServer.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, credentialsDir)
	firstServer.writeCredentialProfile = func(string, string, runtimeconfig.Credentials) (string, error) {
		return "", errors.New("injected write failure containing " + firstToken)
	}
	firstRequest := validEnrolmentRequest()
	firstRequest.FabricAddress = fabric.URL
	firstResult := firstServer.executeEnrolment(context.Background(), firstRequest)
	if firstResult.Code != EnrolmentCredentialPersistenceFailed || firstResult.IdempotencyKey != firstRequest.IdempotencyKey {
		t.Fatalf("first result: code=%q key=%q state=%q", firstResult.Code, firstResult.IdempotencyKey, firstResult.State)
	}
	var durableState string
	if err := store.DB().QueryRowContext(context.Background(), `SELECT state FROM enrolment_attempts WHERE project_id = ? AND idempotency_key = ?`, firstRequest.ProjectID, firstRequest.IdempotencyKey).Scan(&durableState); err != nil {
		t.Fatalf("read durable recovery state: %v", err)
	}
	if durableState != string(EnrolmentRecoveryRequired) {
		t.Fatalf("durable state = %q, want %q", durableState, EnrolmentRecoveryRequired)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first local store: %v", err)
	}

	store, err = localstore.Open(databasePath)
	if err != nil {
		t.Fatalf("open restarted local store: %v", err)
	}
	defer store.Close()
	restartedServer := &Server{store: store, httpClient: fabric.Client()}
	restartedServer.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, credentialsDir)
	retryRequest := firstRequest
	retryRequest.IdempotencyKey = "118f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	retryResult := restartedServer.executeEnrolment(context.Background(), retryRequest)
	if retryResult.Code != EnrolmentCredentialsPersistedResult || retryResult.IdempotencyKey != firstRequest.IdempotencyKey {
		t.Fatalf("retry result: code=%q key=%q state=%q", retryResult.Code, retryResult.IdempotencyKey, retryResult.State)
	}
	credentials, err := runtimeconfig.ReadCredentialProfile(credentialsDir, firstRequest.CredentialProfile)
	if err != nil {
		t.Fatalf("read recovered profile: %v", err)
	}
	if credentials.Token != reissuedToken || credentials.AgentID != "agent-restart" || credentials.PassportID != "passport-restart" {
		t.Fatalf("recovered profile: agent=%q passport=%q token_is_reissue=%v", credentials.AgentID, credentials.PassportID, credentials.Token == reissuedToken)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(inputs) != 3 {
		t.Fatalf("Fabric calls = %d, want initial, replay, and controlled reissue", len(inputs))
	}
	if inputs[0].IdempotencyKey != firstRequest.IdempotencyKey || inputs[1].IdempotencyKey != firstRequest.IdempotencyKey || inputs[2].IdempotencyKey != firstRequest.IdempotencyKey || inputs[0].Reissue || inputs[1].Reissue || !inputs[2].Reissue {
		t.Fatalf("Fabric attempt sequence: keys_match=%v/%v/%v reissue=%v/%v/%v",
			inputs[0].IdempotencyKey == firstRequest.IdempotencyKey, inputs[1].IdempotencyKey == firstRequest.IdempotencyKey,
			inputs[2].IdempotencyKey == firstRequest.IdempotencyKey, inputs[0].Reissue, inputs[1].Reissue, inputs[2].Reissue)
	}
}

func TestEnrolmentRejectsOccupiedOrMalformedProfileBeforeFabric(t *testing.T) {
	for _, test := range []struct {
		name string
		seed func(*testing.T, string, string)
	}{
		{
			name: "malformed",
			seed: func(t *testing.T, dir, profile string) {
				t.Helper()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatalf("mkdir credentials: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, profile+".json"), []byte("{"), 0o600); err != nil {
					t.Fatalf("write malformed profile: %v", err)
				}
			},
		},
		{
			name: "mismatched",
			seed: func(t *testing.T, dir, profile string) {
				t.Helper()
				if _, err := runtimeconfig.WriteCredentialProfile(dir, profile, runtimeconfig.Credentials{
					Server: "https://other.example", ProjectID: "other-project", AgentID: "other-agent", PassportID: "other-passport", Token: "other-secret",
				}); err != nil {
					t.Fatalf("seed occupied profile: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			fabric := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer fabric.Close()
			store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
			if err != nil {
				t.Fatalf("open local store: %v", err)
			}
			defer store.Close()
			credentialsDir := filepath.Join(t.TempDir(), "credentials")
			req := validEnrolmentRequest()
			req.FabricAddress = fabric.URL
			test.seed(t, credentialsDir, req.CredentialProfile)
			srv := &Server{store: store, httpClient: fabric.Client()}
			srv.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, credentialsDir)
			result := srv.executeEnrolment(context.Background(), req)
			if calls.Load() != 0 {
				t.Fatalf("Fabric calls = %d, want 0", calls.Load())
			}
			if result.Code != EnrolmentCredentialPersistenceFailed {
				t.Fatalf("result code=%q state=%q", result.Code, result.State)
			}
		})
	}
}

func TestEnrolmentSerializesCredentialProfileBeforeFabric(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			entered <- struct{}{}
			<-release
		}
		var rpcReq rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Errorf("decode Fabric request: %v", err)
			return
		}
		fabricEnrolmentResponse(t, w, rpcReq.ID, fabricEnrolAgentOutput{
			AgentID: "agent-winner", PassportID: "passport-winner", Token: "winner-token", IssuedAt: time.Now().UTC(),
		})
	}))
	defer fabric.Close()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("open local store: %v", err)
	}
	defer store.Close()
	credentialsDir := filepath.Join(t.TempDir(), "credentials")
	srv := &Server{store: store, httpClient: fabric.Client()}
	srv.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, credentialsDir)
	first := validEnrolmentRequest()
	first.FabricAddress = fabric.URL
	second := first
	second.IdempotencyKey = "118f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	second.Owner = "different-owner"
	results := make(chan EnrolmentResult, 2)
	go func() { results <- srv.executeEnrolment(context.Background(), first) }()
	<-entered
	go func() { results <- srv.executeEnrolment(context.Background(), second) }()
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("concurrent profile reached Fabric %d times before winner committed, want 1", calls.Load())
	}
	close(release)
	firstResult, secondResult := <-results, <-results
	if calls.Load() != 1 {
		t.Fatalf("Fabric calls = %d after both attempts, want 1", calls.Load())
	}
	if !((firstResult.Code == EnrolmentCredentialsPersistedResult && secondResult.Code == EnrolmentCredentialPersistenceFailed) ||
		(secondResult.Code == EnrolmentCredentialsPersistedResult && firstResult.Code == EnrolmentCredentialPersistenceFailed)) {
		t.Fatalf("result codes = %q/%q, want one persisted and one rejected", firstResult.Code, secondResult.Code)
	}
}

func enrolmentArgumentsForRequest(t *testing.T, req EnrolmentRequest) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal enrolment request: %v", err)
	}
	var arguments map[string]interface{}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		t.Fatalf("decode enrolment arguments: %v", err)
	}
	return arguments
}

func validEnrolmentEnvelope() EnrolmentPermissionEnvelope {
	return EnrolmentPermissionEnvelope{
		Roles:       []string{"contributor", "reviewer"},
		Permissions: []string{"task.create", "kb.write", "task.assign"},
	}
}

func TestEnrolmentContractVocabulary(t *testing.T) {
	if EnrolmentProtocolVersion != 1 {
		t.Fatalf("protocol version = %d, want 1", EnrolmentProtocolVersion)
	}
	if EnrolmentToolName != "wormhole.agent.enrol" {
		t.Fatalf("tool name = %q", EnrolmentToolName)
	}

	wantStates := []EnrolmentState{
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
	if !reflect.DeepEqual(EnrolmentStates(), wantStates) {
		t.Fatalf("states = %v, want %v", EnrolmentStates(), wantStates)
	}

	wantResults := []EnrolmentResultCode{
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
	if !reflect.DeepEqual(EnrolmentResultCodes(), wantResults) {
		t.Fatalf("result codes = %v, want %v", EnrolmentResultCodes(), wantResults)
	}
}

func TestValidateEnrolmentRequestAcceptsCompleteRequest(t *testing.T) {
	if err := ValidateEnrolmentRequest(validEnrolmentRequest(), validEnrolmentEnvelope()); err != nil {
		t.Fatalf("ValidateEnrolmentRequest: %v", err)
	}
}

func TestValidateEnrolmentRequestRejectsInvalidContractValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*EnrolmentRequest, *EnrolmentPermissionEnvelope)
		want   error
	}{
		{
			name: "unsafe credential profile",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.CredentialProfile = "../escape"
			},
			want: ErrInvalidEnrolmentCredentialProfile,
		},
		{
			name: "absolute credential path",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.CredentialProfile = "/tmp/credential.json"
			},
			want: ErrInvalidEnrolmentCredentialProfile,
		},
		{
			name: "symlink or out-of-root path cannot be expressed",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.CredentialProfile = "link/../outside"
			},
			want: ErrInvalidEnrolmentCredentialProfile,
		},
		{
			name: "unsupported version",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.Version++
			},
			want: ErrUnsupportedEnrolmentVersion,
		},
		{
			name: "empty project binding",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.ProjectID = "  "
			},
			want: ErrInvalidEnrolmentProjectBinding,
		},
		{
			name: "missing Fabric address",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.FabricAddress = "\t"
			},
			want: ErrMissingEnrolmentFabricAddress,
		},
		{
			name: "duplicate repositories after URL canonicalisation",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.Repositories = []string{
					"https://GitHub.com/H4RL33/wormhole.git",
					"https://github.com/H4RL33/wormhole/",
				}
			},
			want: ErrDuplicateEnrolmentRepository,
		},
		{
			name: "role outside local envelope",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.Roles = append(req.Roles, "maintainer")
			},
			want: ErrEnrolmentRoleOutsideEnvelope,
		},
		{
			name: "permission outside local envelope",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.RequestedPermissions = append(req.RequestedPermissions, "task.delete")
			},
			want: ErrEnrolmentPermissionOutsideEnvelope,
		},
		{
			name: "malformed idempotency key",
			mutate: func(req *EnrolmentRequest, _ *EnrolmentPermissionEnvelope) {
				req.IdempotencyKey = "retry-me"
			},
			want: ErrMalformedEnrolmentIdempotencyKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validEnrolmentRequest()
			envelope := validEnrolmentEnvelope()
			tt.mutate(&req, &envelope)
			err := ValidateEnrolmentRequest(req, envelope)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}
}

type staticEnrolmentPolicySource struct {
	envelope EnrolmentPermissionEnvelope
	err      error
}

func (source staticEnrolmentPolicySource) EnrolmentPermissionEnvelope(context.Context, string) (EnrolmentPermissionEnvelope, error) {
	return source.envelope, source.err
}

func TestValidateEnrolmentRequestWithPolicyUsesTrustedSource(t *testing.T) {
	req := validEnrolmentRequest()
	if err := ValidateEnrolmentRequestWithPolicy(context.Background(), req, staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}); err != nil {
		t.Fatalf("ValidateEnrolmentRequestWithPolicy: %v", err)
	}
	if err := ValidateEnrolmentRequestWithPolicy(context.Background(), req, nil); !errors.Is(err, ErrEnrolmentPolicyUnavailable) {
		t.Fatalf("nil source error = %v, want %v", err, ErrEnrolmentPolicyUnavailable)
	}
	denied := staticEnrolmentPolicySource{envelope: EnrolmentPermissionEnvelope{}}
	if err := ValidateEnrolmentRequestWithPolicy(context.Background(), req, denied); !errors.Is(err, ErrEnrolmentRoleOutsideEnvelope) {
		t.Fatalf("deny-all source error = %v", err)
	}
}

func enrolmentArguments(t *testing.T) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(validEnrolmentRequest())
	if err != nil {
		t.Fatalf("marshal enrolment request: %v", err)
	}
	var arguments map[string]interface{}
	if err := json.Unmarshal(raw, &arguments); err != nil {
		t.Fatalf("decode enrolment arguments: %v", err)
	}
	return arguments
}

func TestRetainedEnrolmentBoundaryEnforcesPolicy(t *testing.T) {
	tests := []struct {
		name      string
		policy    EnrolmentPolicySource
		wantError string
	}{
		{"missing policy denies closed", nil, ErrEnrolmentPolicyUnavailable.Error()},
		{"denied scope", staticEnrolmentPolicySource{envelope: EnrolmentPermissionEnvelope{}}, ErrEnrolmentRoleOutsideEnvelope.Error()},
		{"allowed scope reaches executor", staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, ErrEnrolmentExecutorUnavailable.Error()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newMCPTestServer(t)
			srv.SetEnrolmentPolicySource(tt.policy)
			raw, err := json.Marshal(validEnrolmentRequest())
			if err != nil {
				t.Fatal(err)
			}
			_, err = srv.handleEnrolmentContract(context.Background(), raw)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
		})
	}
}

func TestRetainedEnrolmentBoundaryRejectsUnknownCredentialPathField(t *testing.T) {
	srv, _ := newMCPTestServer(t)
	srv.SetEnrolmentPolicySource(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()})
	arguments := enrolmentArguments(t)
	arguments["credential_path"] = "/tmp/outside.json"
	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	_, err = srv.handleEnrolmentContract(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want strict unknown-field rejection", err)
	}
}

func TestNewEnrolmentIdempotencyKeyIsCanonicalAndValid(t *testing.T) {
	key, err := NewEnrolmentIdempotencyKey()
	if err != nil {
		t.Fatalf("NewEnrolmentIdempotencyKey: %v", err)
	}
	req := validEnrolmentRequest()
	req.IdempotencyKey = key
	if err := ValidateEnrolmentRequest(req, validEnrolmentEnvelope()); err != nil {
		t.Fatalf("generated key rejected: %v", err)
	}
	if key2, err := NewEnrolmentIdempotencyKey(); err != nil {
		t.Fatalf("second NewEnrolmentIdempotencyKey: %v", err)
	} else if key2 == key {
		t.Fatalf("two generated attempt keys unexpectedly match: %q", key)
	}
}

func TestEnrolmentLifecycleTransitions(t *testing.T) {
	tests := []struct {
		name    string
		attempt EnrolmentAttempt
		allowed []EnrolmentState
	}{
		{"requested", EnrolmentAttempt{State: EnrolmentRequested}, []EnrolmentState{EnrolmentRegistrationInProgress, EnrolmentFailed}},
		{"registration", EnrolmentAttempt{State: EnrolmentRegistrationInProgress}, []EnrolmentState{EnrolmentRegistered, EnrolmentFailed}},
		{"registered", EnrolmentAttempt{State: EnrolmentRegistered}, []EnrolmentState{EnrolmentCredentialsPersisted, EnrolmentRecoveryRequired}},
		{"credentials persisted", EnrolmentAttempt{State: EnrolmentCredentialsPersisted}, []EnrolmentState{EnrolmentBootstrapInProgress, EnrolmentRecoveryRequired}},
		{"bootstrap", EnrolmentAttempt{State: EnrolmentBootstrapInProgress}, []EnrolmentState{EnrolmentReady, EnrolmentRecoveryRequired}},
		{"ready terminal", EnrolmentAttempt{State: EnrolmentReady}, nil},
		{"retryable unreachable", EnrolmentAttempt{State: EnrolmentFailed, ResultCode: EnrolmentFabricUnreachable, Retryable: true, DurableCheckpoint: EnrolmentRequested}, []EnrolmentState{EnrolmentRequested}},
		{"nonretryable failure", EnrolmentAttempt{State: EnrolmentFailed, ResultCode: EnrolmentInvalidProject, DurableCheckpoint: EnrolmentRequested}, nil},
		{"registration recovery", EnrolmentAttempt{State: EnrolmentRecoveryRequired, ResultCode: EnrolmentCredentialPersistenceFailed, Retryable: true, DurableCheckpoint: EnrolmentRegistered}, []EnrolmentState{EnrolmentRegistrationInProgress}},
		{"bootstrap recovery", EnrolmentAttempt{State: EnrolmentRecoveryRequired, ResultCode: EnrolmentBootstrapFailedAfterEnrolment, Retryable: true, DurableCheckpoint: EnrolmentCredentialsPersisted}, []EnrolmentState{EnrolmentBootstrapInProgress}},
		{"wrong recovery checkpoint", EnrolmentAttempt{State: EnrolmentRecoveryRequired, ResultCode: EnrolmentBootstrapFailedAfterEnrolment, Retryable: true, DurableCheckpoint: EnrolmentRegistered}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, to := range EnrolmentStates() {
				want := false
				for _, allowed := range tt.allowed {
					want = want || allowed == to
				}
				if got := CanTransitionEnrolment(tt.attempt, to); got != want {
					t.Errorf("transition %+v -> %s = %t, want %t", tt.attempt, to, got, want)
				}
			}
		})
	}
}

func TestEnrolmentResultValidationKeepsRecoveryExplicit(t *testing.T) {
	tests := []struct {
		name   string
		result EnrolmentResult
	}{
		{
			name: "credentials persisted is a truthful Task 3 boundary",
			result: EnrolmentResult{Version: EnrolmentProtocolVersion, Code: EnrolmentCredentialsPersistedResult,
				State: EnrolmentCredentialsPersisted, IdempotencyKey: validEnrolmentRequest().IdempotencyKey, Retryable: true,
				CredentialProfile: "project-1__default"},
		},
		{
			name: "success is ready",
			result: EnrolmentResult{Version: EnrolmentProtocolVersion, Code: EnrolmentSuccess,
				State: EnrolmentReady, IdempotencyKey: validEnrolmentRequest().IdempotencyKey},
		},
		{
			name: "credential persistence failure is recoverable",
			result: EnrolmentResult{Version: EnrolmentProtocolVersion, Code: EnrolmentCredentialPersistenceFailed,
				State: EnrolmentRecoveryRequired, IdempotencyKey: validEnrolmentRequest().IdempotencyKey, Retryable: true},
		},
		{
			name: "bootstrap failure after enrolment is recoverable",
			result: EnrolmentResult{Version: EnrolmentProtocolVersion, Code: EnrolmentBootstrapFailedAfterEnrolment,
				State: EnrolmentRecoveryRequired, IdempotencyKey: validEnrolmentRequest().IdempotencyKey, Retryable: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.result.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}

	invalid := EnrolmentResult{
		Version: EnrolmentProtocolVersion, Code: EnrolmentBootstrapFailedAfterEnrolment,
		State: EnrolmentFailed, IdempotencyKey: validEnrolmentRequest().IdempotencyKey,
	}
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidEnrolmentResult) {
		t.Fatalf("invalid recovery result error = %v", err)
	}
}

func persistedEnrolmentReplayFixture(t *testing.T, state EnrolmentState) (*Server, EnrolmentRequest, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	fabric := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	t.Cleanup(fabric.Close)
	srv, _ := newMCPTestServer(t)
	srv.httpClient = fabric.Client()
	req := validEnrolmentRequest()
	req.FabricAddress = fabric.URL
	credentialsDir := filepath.Join(t.TempDir(), "credentials")
	srv.SetEnrolmentRuntime(staticEnrolmentPolicySource{envelope: validEnrolmentEnvelope()}, credentialsDir)
	requestHash, err := canonicalEnrolmentRequestHash(req)
	if err != nil {
		t.Fatal(err)
	}
	stored, _, err := srv.store.ResolveEnrolmentAttempt(context.Background(), localstore.EnrolmentAttemptRecord{
		ProjectID: req.ProjectID, IdempotencyKey: req.IdempotencyKey,
		RequestHash: requestHash, State: string(EnrolmentRequested),
		CredentialProfile: req.CredentialProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.UpdateEnrolmentAttempt(context.Background(), stored,
		string(state), "agent-1", "passport-1", state == EnrolmentReady); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeconfig.WriteCredentialProfile(credentialsDir, req.CredentialProfile, runtimeconfig.Credentials{
		Server: req.FabricAddress, ProjectID: req.ProjectID, AgentID: "agent-1",
		PassportID: "passport-1", Token: "never-return-this-secret",
	}); err != nil {
		t.Fatal(err)
	}
	return srv, req, &calls
}

func durableEnrolmentState(t *testing.T, srv *Server, req EnrolmentRequest) EnrolmentState {
	t.Helper()
	var state string
	if err := srv.store.DB().QueryRowContext(context.Background(),
		`SELECT state FROM enrolment_attempts WHERE project_id = ? AND idempotency_key = ?`,
		req.ProjectID, req.IdempotencyKey).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return EnrolmentState(state)
}

func TestRetainedEnrolmentNormalizesPostCredentialSurvivorsWithoutRemoteCall(t *testing.T) {
	for _, state := range []EnrolmentState{EnrolmentBootstrapInProgress, EnrolmentRecoveryRequired} {
		t.Run(string(state), func(t *testing.T) {
			srv, req, calls := persistedEnrolmentReplayFixture(t, state)
			got := srv.executeEnrolment(context.Background(), req)
			if got.Code != EnrolmentCredentialsPersistedResult ||
				got.State != EnrolmentCredentialsPersisted || !got.Retryable {
				t.Fatalf("result = %+v", got)
			}
			if state := durableEnrolmentState(t, srv, req); state != EnrolmentCredentialsPersisted {
				t.Fatalf("durable state = %q, want %q", state, EnrolmentCredentialsPersisted)
			}
			if calls.Load() != 0 {
				t.Fatalf("remote calls = %d, want 0", calls.Load())
			}
		})
	}
}

func TestRetainedEnrolmentPersistedReplayIsIdempotentWithoutRemoteCall(t *testing.T) {
	srv, req, calls := persistedEnrolmentReplayFixture(t, EnrolmentCredentialsPersisted)
	got := srv.executeEnrolment(context.Background(), req)
	if got.Code != EnrolmentCredentialsPersistedResult ||
		got.State != EnrolmentCredentialsPersisted || !got.Retryable {
		t.Fatalf("result = %+v", got)
	}
	if state := durableEnrolmentState(t, srv, req); state != EnrolmentCredentialsPersisted {
		t.Fatalf("durable state = %q", state)
	}
	if calls.Load() != 0 {
		t.Fatalf("remote calls = %d, want 0", calls.Load())
	}
}

func TestRetainedHistoricalReadyReplayRemainsTerminalWithoutRemoteCall(t *testing.T) {
	srv, req, calls := persistedEnrolmentReplayFixture(t, EnrolmentReady)
	got := srv.executeEnrolment(context.Background(), req)
	if got.Code != EnrolmentSuccess || got.State != EnrolmentReady || got.Retryable {
		t.Fatalf("result = %+v", got)
	}
	if state := durableEnrolmentState(t, srv, req); state != EnrolmentReady {
		t.Fatalf("durable state = %q", state)
	}
	if calls.Load() != 0 {
		t.Fatalf("remote calls = %d, want 0", calls.Load())
	}
}

func TestRetainedEnrolmentNormalizationPersistenceFailureIsSafe(t *testing.T) {
	srv, req, calls := persistedEnrolmentReplayFixture(t, EnrolmentBootstrapInProgress)
	if _, err := srv.store.DB().Exec(`
		CREATE TRIGGER fail_credentials_persisted
		BEFORE UPDATE OF state ON enrolment_attempts
		WHEN NEW.state = 'credentials_persisted'
		BEGIN SELECT RAISE(ABORT, 'injected persistence failure'); END
	`); err != nil {
		t.Fatal(err)
	}
	got := srv.executeEnrolment(context.Background(), req)
	if got.Code != EnrolmentCredentialPersistenceFailed ||
		got.State != EnrolmentRecoveryRequired || !got.Retryable {
		t.Fatalf("result = %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("never-return-this-secret")) {
		t.Fatalf("safe failure exposed credential: %s", raw)
	}
	if state := durableEnrolmentState(t, srv, req); state != EnrolmentBootstrapInProgress {
		t.Fatalf("failed normalization mutated durable state to %q", state)
	}
	if calls.Load() != 0 {
		t.Fatalf("remote calls = %d, want 0", calls.Load())
	}
}
