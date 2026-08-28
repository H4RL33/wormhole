# Sync v2 Slice 1 Contract Cut Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every live sync-v1 owner and freeze the strict, descriptor-only sync-v2 and Activity-v1 public contract without activating public handlers.

**Architecture:** Plain wire values and repository-scope proof bytes live once in `internal/types/projectstate`; MCP aliases those values, builds closed non-callable descriptors, and provides strict public-envelope and safe-error primitives for later handler slices. The retained Fabric registry continues to dispatch only its sixteen unrelated private tools, while one `V2Engine` shell preserves the exact local `wormhole.sync.status` API and enrolment ends truthfully at durable `credentials_persisted`.

**Tech Stack:** Go 1.25.1, JSON-RPC/MCP, reflection-based JSON Schema, SQLite-backed Gateway runtime, PostgreSQL-backed retained Fabric tools, Git, Markdown and JSON contract fixtures.

## Global Constraints

- Delivery base is the exact controller-recorded commit `72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0`; the controller-owned approved-spec clarification and this plan are documentation inputs, not implementation commits in the reviewed range.
- Slice 1 publishes descriptor data for exactly ten public tools; it does not register, dispatch, or assemble a public handler.
- Canonical safe `{code,operation}` failures apply only to those ten public tools. The sixteen retained private tools keep their current numeric/auth/domain error behavior until Task 14.
- The private Fabric registry contains exactly sixteen unrelated live tools and no live `wormhole.sync.*` or `wormhole.activity.*` tool after the cut.
- Enrolment stops at the existing durable `credentials_persisted` result until v2 bootstrap assembly; no temporary bootstrap adapter or remote engine is constructed.
- Preserve the exact local `wormhole.sync.status` signature `Status(context.Context) (Status, error)`, four state literals, `pending_writes` field, and wire bytes `{"state":"offline","pending_writes":0}`.
- Preserve every Activity-v1 shared value, Fabric store/policy/lifecycle/pruner, Gateway localstore transport/promotion behavior, queue, receipt, retention, and test.
- Declare `V2Engine`, `NewV2Engine`, and its status method once. Later slices extend this type without redeclaration.
- `internal/runtime/*` never imports `internal/mcp` or `internal/core/*`; `internal/types/projectstate` imports only the parent `internal/types`, standard library, and the already-approved TOML dependency used elsewhere in that package.
- No control-flow panic, new dependency, new top-level package, ORM, global singleton, `init` registration, source-body persistence, private credential in tracked state, or AdminKey MCP authority.
- Work test-first. Each commit runs the complete repository gate before it is created. Do not push from this plan.

## Controller Base and Review Boundary

Before Task 1, the controller runs this exact assertion and records the output outside the implementation commits:

```bash
test "$(git rev-parse --verify HEAD)" = "72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0"
git merge-base --is-ancestor 72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0 HEAD
```

After Task 3, the implementer records `IMPLEMENTATION_HEAD=$(git rev-parse --verify HEAD)`, hands that exact forty-character value to the controller, and stops. A distinct reviewer reviews exactly `72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0..$IMPLEMENTATION_HEAD`, records both exact hashes and finding counts, and the controller verifies `REVIEWED_HEAD == IMPLEMENTATION_HEAD`. Fixes create a new implementation head and require a fresh distinct re-review of the same base-to-head range. This plan contains no push step.

---

### Task 1: Freeze Shared Records, Proof Carriage, Safe Results, and Non-Callable Descriptors

**Files:**
- Create: `internal/types/public_proof.go`
- Create: `internal/types/public_proof_test.go`
- Create: `internal/types/projectstate/sync_protocol.go`
- Create: `internal/types/projectstate/sync_protocol_test.go`
- Create: `internal/mcp/sync_v2_contract.go`
- Create: `internal/mcp/sync_v2_contract_test.go`
- Create: `internal/mcp/public_proof_test.go`
- Create: `internal/mcp/safe_tool_error_test.go`
- Create: `docs/contracts/public-fabric-descriptors.json`
- Modify: `internal/mcp/jsonrpc.go:114-234,316-321`

**Interfaces:**
- Consumes: `types.RepositoryIdentity`, `types.Assurance`, and same-package `Digest`, `Tree`, `OperationV1`, `ActivityV1`, `EffectiveActivityPolicyV1`, and `ActivityReceiptV1`.
- Produces: `types.PublicRequestProof`, `types.PublicRequestSignature`, `projectstate.SyncProtocolVersionV2`, every shared protocol record below, `projectstate.RepositoryScopeProjection`, `projectstate.RepositoryScopeKey`, MCP type aliases, `ToolsCallParams`, strict public-envelope helpers, `ToolFailureV1`, and `PublicFabricToolDescriptors`.

- [ ] **Step 1: Write the failing shared-value and repository-scope tests**

Create `internal/types/public_proof_test.go`:

```go
package types

import (
	"encoding/json"
	"testing"
)

func TestPublicRequestProofWireOrderAndOptionalSession(t *testing.T) {
	proof := PublicRequestProof{
		KeyID: "sha256:key", PublicKey: "public", Timestamp: "2026-08-28T12:00:00Z",
		Nonce: "nonce", Signature: "signature",
	}
	got, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"key_id":"sha256:key","public_key":"public","timestamp":"2026-08-28T12:00:00Z","nonce":"nonce","signature":"signature"}`
	if string(got) != want {
		t.Fatalf("proof JSON = %s, want %s", got, want)
	}
	proof.SessionID = "session-1"
	got, err = json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"key_id":"sha256:key","public_key":"public","timestamp":"2026-08-28T12:00:00Z","nonce":"nonce","signature":"signature","session_id":"session-1"}`
	if string(got) != want {
		t.Fatalf("session proof JSON = %s, want %s", got, want)
	}
}
```

Create `internal/types/projectstate/sync_protocol_test.go`:

```go
package projectstate

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

func TestRepositoryScopeProjectionGolden(t *testing.T) {
	repository := types.RepositoryIdentity{
		Provider: "github", ImmutableID: "123456",
		CanonicalRemote: "https://github.com/H4RL33/wormhole",
	}
	projection, err := RepositoryScopeProjection(repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	wantProjection := "{\"provider\":\"github\",\"immutable_id\":\"123456\",\"canonical_ref\":\"refs/heads/main\"}\n"
	if string(projection) != wantProjection {
		t.Fatalf("projection = %q, want %q", projection, wantProjection)
	}
	scope, err := RepositoryScopeKey(repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	wantScope := "repository:sha256:6affdf263e9aac6b09e1196d401d97b4a17fcebf1df54551d99b39c465ccf349"
	if scope != wantScope {
		t.Fatalf("scope = %q, want %q", scope, wantScope)
	}
	changedRemote := repository
	changedRemote.CanonicalRemote = "https://mirror.example.test/H4RL33/wormhole"
	changedScope, err := RepositoryScopeKey(changedRemote, "refs/heads/main")
	if err != nil || changedScope != scope {
		t.Fatalf("canonical remote changed public projection: scope=%q err=%v", changedScope, err)
	}
}

func TestRepositoryScopeRejectsIncompleteInputs(t *testing.T) {
	valid := types.RepositoryIdentity{Provider: "github", ImmutableID: "123456", CanonicalRemote: "https://github.com/H4RL33/wormhole"}
	for name, input := range map[string]struct {
		repository types.RepositoryIdentity
		ref        string
	}{
		"local-only repository": {repository: types.RepositoryIdentity{}, ref: "refs/heads/main"},
		"invalid repository":    {repository: types.RepositoryIdentity{Provider: "github"}, ref: "refs/heads/main"},
		"empty ref":              {repository: valid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RepositoryScopeKey(input.repository, input.ref); err == nil {
				t.Fatal("RepositoryScopeKey accepted incomplete input")
			}
		})
	}
}

func TestSyncProtocolRecordsRoundTripWithExactEmptyLists(t *testing.T) {
	state := SyncPullV2Result{
		Version: SyncProtocolVersionV2,
		State: SyncStateV2{
			StreamVersion: 3, AcceptedCommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AcceptedTreeDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			LiveTreeDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			AcceptedTree: Tree{}, LiveTree: Tree{}, OpenConflictIDs: []string{},
		},
	}
	got, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"version":2,"changed":false,"state":{"stream_version":3,"accepted_commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","accepted_tree_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","live_tree_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","accepted_tree":[],"live_tree":[],"open_conflict_ids":[]}}` {
		t.Fatalf("state JSON = %s", got)
	}
	var decoded SyncPullV2Result
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, state) {
		t.Fatalf("round trip = %#v, want %#v", decoded, state)
	}
}

func TestPublicAgentSessionIssueCarriesSharedAssuranceAndTime(t *testing.T) {
	want := PublicAgentSessionIssueV2Result{
		Version: 2, SessionID: "session", AgentID: "agent", AccountableHumanID: "human",
		HarnessName: "codex", HarnessVersion: "1", ModelName: "gpt", ModelVersion: "5",
		Assurance: types.AssurancePublicKeyContinuity,
		ExpiresAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got PublicAgentSessionIssueV2Result
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("session round trip = %#v, %v; want %#v", got, err, want)
	}
}
```

- [ ] **Step 2: Run the shared-value tests to verify RED**

Run:

```bash
go test ./internal/types/... -run 'Test(PublicRequestProof|RepositoryScope|SyncProtocolRecords|PublicAgentSession)' -count=1
```

Expected: FAIL to compile because `PublicRequestProof`, `RepositoryScopeKey`, `SyncPullV2Result`, and the other v2 records do not exist.

- [ ] **Step 3: Add the complete shared proof and protocol owners**

Create `internal/types/public_proof.go` exactly:

```go
package types

type PublicRequestProof struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Timestamp string `json:"timestamp"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`
	SessionID string `json:"session_id,omitempty"`
}

type PublicRequestSignature struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}
```

Create `internal/types/projectstate/sync_protocol.go` exactly. Same-package values are deliberately unqualified:

```go
package projectstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const SyncProtocolVersionV2 = 2

var ErrInvalidRepositoryScope = errors.New("projectstate: invalid repository scope")

type repositoryScopeProjection struct {
	Provider     string `json:"provider"`
	ImmutableID  string `json:"immutable_id"`
	CanonicalRef string `json:"canonical_ref"`
}

func RepositoryScopeProjection(repository types.RepositoryIdentity, canonicalRef string) ([]byte, error) {
	if err := repository.Validate(); err != nil || repository.Provider == "" || canonicalRef == "" {
		return nil, ErrInvalidRepositoryScope
	}
	projection, err := CanonicalJSON(repositoryScopeProjection{
		Provider: repository.Provider, ImmutableID: repository.ImmutableID, CanonicalRef: canonicalRef,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: projection: %v", ErrInvalidRepositoryScope, err)
	}
	return projection, nil
}

func RepositoryScopeKey(repository types.RepositoryIdentity, canonicalRef string) (string, error) {
	projection, err := RepositoryScopeProjection(repository, canonicalRef)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(projection)
	return "repository:sha256:" + hex.EncodeToString(digest[:]), nil
}

type SyncV2Scope struct {
	Version                int                      `json:"version" const:"2"`
	AttachmentRef          string                   `json:"attachment_ref"`
	Repository             types.RepositoryIdentity `json:"repository"`
	CanonicalRef           string                   `json:"canonical_ref"`
	BaseCommitSHA          string                   `json:"base_commit_sha"`
	BaseTreeDigest         Digest                   `json:"base_tree_digest"`
	ExpectedStreamVersion  int64                    `json:"expected_stream_version"`
	ExpectedLiveTreeDigest Digest                   `json:"expected_live_tree_digest"`
}

type SyncStateV2 struct {
	StreamVersion      int64    `json:"stream_version"`
	AcceptedCommitSHA  string   `json:"accepted_commit_sha"`
	AcceptedTreeDigest Digest   `json:"accepted_tree_digest"`
	LiveTreeDigest     Digest   `json:"live_tree_digest"`
	AcceptedTree       Tree     `json:"accepted_tree"`
	LiveTree           Tree     `json:"live_tree"`
	OpenConflictIDs    []string `json:"open_conflict_ids"`
}

type SyncAttachV2Args struct {
	Version        int                      `json:"version" const:"2"`
	Repository     types.RepositoryIdentity `json:"repository"`
	CanonicalRef   string                   `json:"canonical_ref"`
	BaseCommitSHA  string                   `json:"base_commit_sha"`
	BaseTreeDigest Digest                   `json:"base_tree_digest"`
}

type SyncAttachV2Result struct {
	Version                 int                       `json:"version" const:"2"`
	AttachmentRef           string                    `json:"attachment_ref"`
	RemoteProjectID         string                    `json:"remote_project_id"`
	StreamID                string                    `json:"stream_id"`
	StreamVersion           int64                     `json:"stream_version"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
}

type PublicAgentSessionIssueV2Args struct {
	Version        int    `json:"version" const:"2"`
	AttachmentRef  string `json:"attachment_ref"`
	AgentID        string `json:"agent_id"`
	HarnessName    string `json:"harness_name"`
	HarnessVersion string `json:"harness_version"`
	ModelName      string `json:"model_name"`
	ModelVersion   string `json:"model_version"`
}

type PublicAgentSessionIssueV2Result struct {
	Version            int             `json:"version" const:"2"`
	SessionID          string          `json:"session_id"`
	AgentID            string          `json:"agent_id"`
	AccountableHumanID string          `json:"accountable_human_id"`
	HarnessName        string          `json:"harness_name"`
	HarnessVersion     string          `json:"harness_version"`
	ModelName          string          `json:"model_name"`
	ModelVersion       string          `json:"model_version"`
	Assurance          types.Assurance `json:"assurance" const:"public-key-continuity"`
	ExpiresAt          time.Time       `json:"expires_at"`
}

type SyncBootstrapV2Args struct {
	SyncV2Scope
	AfterVersion int64 `json:"after_version"`
}

type SyncBootstrapV2Result struct {
	Version                 int                       `json:"version" const:"2"`
	Changed                 bool                      `json:"changed"`
	State                   SyncStateV2               `json:"state"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
}

type SyncPullV2Args struct {
	SyncV2Scope
	AfterVersion int64 `json:"after_version"`
}

type SyncPullV2Result struct {
	Version int         `json:"version" const:"2"`
	Changed bool        `json:"changed"`
	State   SyncStateV2 `json:"state"`
}

type SyncPushV2Args struct {
	SyncV2Scope
	Operation OperationV1 `json:"operation"`
}

type SyncPushAppliedV2Result struct {
	Version        int    `json:"version" const:"2"`
	Status         string `json:"status" const:"applied"`
	OperationID    string `json:"operation_id"`
	StreamVersion  int64  `json:"stream_version"`
	LiveTreeDigest Digest `json:"live_tree_digest"`
}

type SyncPushConflictV2Result struct {
	Version        int    `json:"version" const:"2"`
	Status         string `json:"status" const:"conflict"`
	OperationID    string `json:"operation_id"`
	StreamVersion  int64  `json:"stream_version"`
	LiveTreeDigest Digest `json:"live_tree_digest"`
	ConflictID     string `json:"conflict_id"`
}

type SyncConflictV2Args struct {
	SyncV2Scope
	ConflictID string      `json:"conflict_id"`
	Resolution OperationV1 `json:"resolution"`
}

type SyncConflictResolvedV2Result struct {
	Version        int    `json:"version" const:"2"`
	Status         string `json:"status" const:"resolved"`
	ConflictID     string `json:"conflict_id"`
	OperationID    string `json:"operation_id"`
	StreamVersion  int64  `json:"stream_version"`
	LiveTreeDigest Digest `json:"live_tree_digest"`
}

type ActivityAcceptV1Args struct {
	Version        int        `json:"version" const:"1"`
	AttachmentRef  string     `json:"attachment_ref"`
	PolicyVersion  int64      `json:"policy_version"`
	PolicyDigest   Digest     `json:"policy_digest"`
	Activity       ActivityV1 `json:"activity"`
	ActivityDigest Digest     `json:"activity_digest"`
}

type ActivityAcceptedV1Result struct {
	Version                 int                       `json:"version" const:"1"`
	Status                  string                    `json:"status" const:"accepted"`
	Receipt                 ActivityReceiptV1         `json:"receipt"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
	PolicyDigest            Digest                    `json:"policy_digest"`
}

type ActivityPolicyChangedV1Result struct {
	Version                 int                       `json:"version" const:"1"`
	Status                  string                    `json:"status" const:"policy_changed"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
	PolicyDigest            Digest                    `json:"policy_digest"`
}

type ActivityPresenceV1Args = ActivityAcceptV1Args

type ActivityPresenceAcceptedV1Result struct {
	Version int    `json:"version" const:"1"`
	Status  string `json:"status" const:"accepted"`
}

type ActivityPullV1Args struct {
	Version       int    `json:"version" const:"1"`
	AttachmentRef string `json:"attachment_ref"`
	AfterSequence int64  `json:"after_sequence"`
	Limit         int    `json:"limit"`
}

type ActivityPolicyEvidenceV1 struct {
	Policy       EffectiveActivityPolicyV1 `json:"policy"`
	PolicyDigest Digest                    `json:"policy_digest"`
}

type ActivityDeliveryV1 struct {
	SourceRef      string            `json:"source_ref"`
	Activity       ActivityV1        `json:"activity"`
	ActivityDigest Digest            `json:"activity_digest"`
	Receipt        ActivityReceiptV1 `json:"receipt"`
}

type ActivityPullV1Result struct {
	Version            int                       `json:"version" const:"1"`
	EffectivePolicy    EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
	PolicyDigest       Digest                    `json:"policy_digest"`
	HistoricalPolicies []ActivityPolicyEvidenceV1 `json:"historical_policies"`
	Deliveries         []ActivityDeliveryV1       `json:"deliveries"`
	NextSequence       int64                      `json:"next_sequence"`
	HasMore            bool                       `json:"has_more"`
}

type ActivityLifecycleV1Args struct {
	Version       int    `json:"version" const:"1"`
	AttachmentRef string `json:"attachment_ref"`
	ActivityID    string `json:"activity_id"`
	Kind          string `json:"kind"`
	ReferenceID   string `json:"reference_id"`
	ExpectedState string `json:"expected_state"`
	NextState     string `json:"next_state"`
}

type ActivityLifecycleV1Result struct {
	Version int    `json:"version" const:"1"`
	State   string `json:"state"`
}
```

- [ ] **Step 4: Run shared-value GREEN**

Run:

```bash
gofmt -w internal/types/public_proof.go internal/types/public_proof_test.go internal/types/projectstate/sync_protocol.go internal/types/projectstate/sync_protocol_test.go
go test ./internal/types/... -run 'Test(PublicRequestProof|RepositoryScope|SyncProtocolRecords|PublicAgentSession)' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write the failing MCP alias, descriptor, proof-carriage, schema, and safe-error tests**

Create `internal/mcp/sync_v2_contract_test.go`:

```go
package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestSyncV2MCPAliasesAreTypeIdentical(t *testing.T) {
	pairs := [][2]any{
		{SyncV2Scope{}, projectstate.SyncV2Scope{}}, {SyncStateV2{}, projectstate.SyncStateV2{}},
		{SyncAttachV2Args{}, projectstate.SyncAttachV2Args{}}, {SyncAttachV2Result{}, projectstate.SyncAttachV2Result{}},
		{PublicAgentSessionIssueV2Args{}, projectstate.PublicAgentSessionIssueV2Args{}}, {PublicAgentSessionIssueV2Result{}, projectstate.PublicAgentSessionIssueV2Result{}},
		{SyncBootstrapV2Args{}, projectstate.SyncBootstrapV2Args{}}, {SyncBootstrapV2Result{}, projectstate.SyncBootstrapV2Result{}},
		{SyncPullV2Args{}, projectstate.SyncPullV2Args{}}, {SyncPullV2Result{}, projectstate.SyncPullV2Result{}},
		{SyncPushV2Args{}, projectstate.SyncPushV2Args{}}, {SyncPushAppliedV2Result{}, projectstate.SyncPushAppliedV2Result{}},
		{SyncPushConflictV2Result{}, projectstate.SyncPushConflictV2Result{}}, {SyncConflictV2Args{}, projectstate.SyncConflictV2Args{}},
		{SyncConflictResolvedV2Result{}, projectstate.SyncConflictResolvedV2Result{}},
		{ActivityAcceptV1Args{}, projectstate.ActivityAcceptV1Args{}}, {ActivityAcceptedV1Result{}, projectstate.ActivityAcceptedV1Result{}},
		{ActivityPolicyChangedV1Result{}, projectstate.ActivityPolicyChangedV1Result{}}, {ActivityPresenceV1Args{}, projectstate.ActivityPresenceV1Args{}},
		{ActivityPresenceAcceptedV1Result{}, projectstate.ActivityPresenceAcceptedV1Result{}}, {ActivityPullV1Args{}, projectstate.ActivityPullV1Args{}},
		{ActivityPolicyEvidenceV1{}, projectstate.ActivityPolicyEvidenceV1{}}, {ActivityDeliveryV1{}, projectstate.ActivityDeliveryV1{}},
		{ActivityPullV1Result{}, projectstate.ActivityPullV1Result{}}, {ActivityLifecycleV1Args{}, projectstate.ActivityLifecycleV1Args{}},
		{ActivityLifecycleV1Result{}, projectstate.ActivityLifecycleV1Result{}},
	}
	for i, pair := range pairs {
		if reflect.TypeOf(pair[0]) != reflect.TypeOf(pair[1]) {
			t.Fatalf("alias pair %d differs: %T != %T", i, pair[0], pair[1])
		}
	}
}

func TestPublicFabricToolDescriptorsAreExactSortedDescriptorValues(t *testing.T) {
	want := []string{
		"wormhole.activity.accept", "wormhole.activity.lifecycle", "wormhole.activity.presence", "wormhole.activity.pull",
		"wormhole.sync.attach", "wormhole.sync.bootstrap", "wormhole.sync.conflict", "wormhole.sync.issue_agent_session",
		"wormhole.sync.pull", "wormhole.sync.push",
	}
	descriptors := PublicFabricToolDescriptors()
	if len(descriptors) != len(want) {
		t.Fatalf("descriptor count = %d, want %d", len(descriptors), len(want))
	}
	got := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		got = append(got, descriptor.Name)
		if descriptor.Description == "" || descriptor.AuthFamily != PublicProofAuth {
			t.Fatalf("descriptor %q = %+v", descriptor.Name, descriptor)
		}
		if descriptor.InputSchema["additionalProperties"] != false {
			t.Fatalf("%s input is not closed: %#v", descriptor.Name, descriptor.InputSchema)
		}
		properties := descriptor.InputSchema["properties"].(map[string]any)
		version := properties["version"].(map[string]any)
		wantVersion := any(2)
		if descriptor.Name[:18] == "wormhole.activity." {
			wantVersion = 1
		}
		if version["const"] != wantVersion {
			t.Fatalf("%s version schema = %#v, want const %v", descriptor.Name, version, wantVersion)
		}
		if _, ok := descriptor.OutputSchema["oneOf"]; !ok {
			t.Fatalf("%s result lacks oneOf: %#v", descriptor.Name, descriptor.OutputSchema)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor names = %q, want %q", got, want)
	}
}

type publicDescriptorGolden struct {
	Definitions map[string]map[string]any
	Descriptors []ToolDescriptor
}

func readPublicDescriptorGolden(t *testing.T) publicDescriptorGolden {
	t.Helper()
	raw, err := os.ReadFile("../../docs/contracts/public-fabric-descriptors.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden publicDescriptorGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden.Definitions) == 0 || len(golden.Descriptors) != 10 {
		t.Fatalf("public descriptor golden = %d definitions, %d descriptors", len(golden.Definitions), len(golden.Descriptors))
	}
	return golden
}

func expandGoldenSchema(t *testing.T, definitions map[string]map[string]any, value any) any {
	t.Helper()
	switch value := value.(type) {
	case []any:
		expanded := make([]any, len(value))
		for index := range value {
			expanded[index] = expandGoldenSchema(t, definitions, value[index])
		}
		return expanded
	case map[string]any:
		if rawRef, ok := value["$ref"]; ok {
			if len(value) != 1 {
				t.Fatalf("$ref has siblings: %#v", value)
			}
			ref, ok := rawRef.(string)
			const prefix = "#/definitions/"
			if !ok || !strings.HasPrefix(ref, prefix) {
				t.Fatalf("invalid local $ref: %#v", rawRef)
			}
			definition, ok := definitions[strings.TrimPrefix(ref, prefix)]
			if !ok {
				t.Fatalf("missing definition %q", ref)
			}
			return expandGoldenSchema(t, definitions, definition)
		}
		expanded := make(map[string]any, len(value))
		for key, nested := range value {
			expanded[key] = expandGoldenSchema(t, definitions, nested)
		}
		return expanded
	default:
		return value
	}
}

func TestPublicFabricToolDescriptorsMatchCompleteIndependentGolden(t *testing.T) {
	golden := readPublicDescriptorGolden(t)
	want := make([]ToolDescriptor, len(golden.Descriptors))
	copy(want, golden.Descriptors)
	for index := range want {
		want[index].InputSchema = expandGoldenSchema(t, golden.Definitions, want[index].InputSchema).(map[string]any)
		want[index].OutputSchema = expandGoldenSchema(t, golden.Definitions, want[index].OutputSchema).(map[string]any)
	}
	gotJSON, err := json.Marshal(PublicFabricToolDescriptors())
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("public descriptor schema drift\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestPublicDescriptorSchemasRejectPrivateRoutingFields(t *testing.T) {
	forbidden := map[string]bool{
		"project_id": true, "workspace_id": true, "fabric_instance_id": true,
		"remote_project_id": true, "stream_id": true, "actor_scope": true,
	}
	for _, descriptor := range PublicFabricToolDescriptors() {
		walkSchemaProperties(t, descriptor.Name, descriptor.InputSchema, forbidden)
	}
}

func walkSchemaProperties(t *testing.T, tool string, schema map[string]any, forbidden map[string]bool) {
	t.Helper()
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range properties {
			if forbidden[name] {
				t.Fatalf("%s exposes forbidden input %q", tool, name)
			}
			if nested, ok := raw.(map[string]any); ok {
				walkSchemaProperties(t, tool, nested, forbidden)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		walkSchemaProperties(t, tool, items, forbidden)
	}
}

func TestRetainedPrivateCreateTaskSchemaGolden(t *testing.T) {
	got := buildInputSchema(CreateTaskTool(nil))
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"project_id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"}, "parent_task_id": map[string]any{"type": "string"},
			"priority": map[string]any{"type": "integer"}, "due_by": map[string]any{"type": "string", "format": "date-time"},
		},
		"required": []string{"title", "description", "priority", "project_id"},
	}
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(want)
		t.Fatalf("retained private schema changed\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

type schemaEmbeddedFixture struct {
	Version int       `json:"version" const:"2"`
	When    time.Time `json:"when"`
}

type schemaFixture struct {
	schemaEmbeddedFixture
	Raw      json.RawMessage  `json:"raw"`
	Bytes    []byte           `json:"bytes"`
	Labels   map[string]string `json:"labels"`
	Anything any              `json:"anything"`
	Choice   string           `json:"choice" enum:"one,two"`
}

func TestClosedSchemaHandlesAnonymousTimeBytesRawMapsInterfacesConstAndOneOf(t *testing.T) {
	schema := closedJSONSchemaForType(reflect.TypeOf(schemaFixture{}))
	if schema["additionalProperties"] != false {
		t.Fatalf("outer schema is open: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"version", "when", "raw", "bytes", "labels", "anything", "choice"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("flattened schema lacks %q: %#v", name, properties)
		}
	}
	if properties["version"].(map[string]any)["const"] != 2 {
		t.Fatalf("version const = %#v", properties["version"])
	}
	if properties["when"].(map[string]any)["format"] != "date-time" {
		t.Fatalf("time schema = %#v", properties["when"])
	}
	if len(properties["raw"].(map[string]any)) != 0 || len(properties["anything"].(map[string]any)) != 0 {
		t.Fatalf("raw/interface schemas = %#v/%#v", properties["raw"], properties["anything"])
	}
	if properties["bytes"].(map[string]any)["contentEncoding"] != "base64" {
		t.Fatalf("bytes schema = %#v", properties["bytes"])
	}
	labels := properties["labels"].(map[string]any)
	if labels["type"] != "object" || labels["additionalProperties"].(map[string]any)["type"] != "string" {
		t.Fatalf("map schema = %#v", labels)
	}
	oneOf := schemaOneOf(projectstate.SyncPushAppliedV2Result{}, projectstate.SyncPushConflictV2Result{})
	if variants, ok := oneOf["oneOf"].([]any); !ok || len(variants) != 2 {
		t.Fatalf("oneOf schema = %#v", oneOf)
	}
	for _, typ := range []reflect.Type{nil, reflect.TypeOf((*any)(nil)).Elem(), reflect.TypeOf(map[int]string{}), reflect.TypeOf((chan int)(nil))} {
		_ = closedJSONSchemaForType(typ)
	}
}

func TestPublicDescriptorsAreReturnedInFreshOrder(t *testing.T) {
	first := PublicFabricToolDescriptors()
	first[0].Name = "mutated"
	second := PublicFabricToolDescriptors()
	names := make([]string, 0, len(second))
	for _, descriptor := range second {
		names = append(names, descriptor.Name)
	}
	if !sort.StringsAreSorted(names) || names[0] == "mutated" {
		t.Fatalf("descriptor copy/order = %q", names)
	}
	_ = types.PublicRequestProof{}
}
```

Create `internal/mcp/public_proof_test.go`:

```go
package mcp

import (
	"encoding/json"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

func publicProofFixture() *types.PublicRequestProof {
	return &types.PublicRequestProof{
		KeyID: "sha256:key", PublicKey: "public", Timestamp: "2026-08-28T12:00:00Z",
		Nonce: "nonce", Signature: "signature", SessionID: "session",
	}
}

func TestToolsCallParamsGoldenPlacesProofBesideArguments(t *testing.T) {
	got, err := json.Marshal(ToolsCallParams{
		Name: "wormhole.sync.push", Arguments: json.RawMessage(`{"version":2}`), Proof: publicProofFixture(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"wormhole.sync.push","arguments":{"version":2},"proof":{"key_id":"sha256:key","public_key":"public","timestamp":"2026-08-28T12:00:00Z","nonce":"nonce","signature":"signature","session_id":"session"}}`
	if string(got) != want {
		t.Fatalf("ToolsCallParams = %s, want %s", got, want)
	}
	private, err := json.Marshal(ToolsCallParams{Name: "wormhole.agent.whoami", Arguments: json.RawMessage(`{}`)})
	if err != nil || string(private) != `{"name":"wormhole.agent.whoami","arguments":{}}` {
		t.Fatalf("private ToolsCallParams = %s, %v", private, err)
	}
}

func TestProbeToolsCallNameKeepsPreIdentificationFailuresNumeric(t *testing.T) {
	for name, raw := range map[string]string{
		"missing": `{"arguments":{}}`, "duplicate": `{"name":"wormhole.sync.push","name":"wormhole.sync.pull","arguments":{}}`,
		"non-string": `{"name":2,"arguments":{}}`, "malformed": `{"name":"wormhole.sync.push","arguments":`,
		"trailing": `{"name":"wormhole.sync.push","arguments":{}} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if operation, err := probeToolsCallName(json.RawMessage(raw)); err == nil || operation != "" {
				t.Fatalf("probe = %q, %v; want unidentified error", operation, err)
			}
		})
	}
	operation, err := probeToolsCallName(json.RawMessage(`{"extra":1,"name":"wormhole.sync.push","arguments":{}}`))
	if err != nil || operation != "wormhole.sync.push" {
		t.Fatalf("identified operation = %q, %v", operation, err)
	}
}

func TestDecodeKnownPublicToolsCallParamsIsStrictAndAuthExclusive(t *testing.T) {
	valid, err := json.Marshal(ToolsCallParams{Name: "wormhole.sync.push", Arguments: json.RawMessage(`{"version":2}`), Proof: publicProofFixture()})
	if err != nil {
		t.Fatal(err)
	}
	got, code := decodeKnownPublicToolsCallParams(valid, "wormhole.sync.push", "")
	if code != "" || got.Name != "wormhole.sync.push" || got.Proof == nil || string(got.Arguments) != `{"version":2}` {
		t.Fatalf("decoded = %+v, code %q", got, code)
	}
	for name, fixture := range map[string]struct {
		raw        string
		authHeader string
		wantCode   string
	}{
		"unknown field": {raw: `{"name":"wormhole.sync.push","arguments":{"version":2},"proof":{"key_id":"k","public_key":"p","timestamp":"t","nonce":"n","signature":"s"},"extra":1}`, wantCode: "invalid_request"},
		"duplicate arguments": {raw: `{"name":"wormhole.sync.push","arguments":{},"arguments":{},"proof":{"key_id":"k","public_key":"p","timestamp":"t","nonce":"n","signature":"s"}}`, wantCode: "invalid_request"},
		"trailing": {raw: string(valid) + `{}`, wantCode: "invalid_request"},
		"missing proof": {raw: `{"name":"wormhole.sync.push","arguments":{"version":2}}`, wantCode: "authentication_failed"},
		"proof and bearer": {raw: string(valid), authHeader: "Bearer private", wantCode: "authentication_failed"},
		"wrong known name": {raw: string(valid), wantCode: ""},
	} {
		t.Run(name, func(t *testing.T) {
			expected := "wormhole.sync.push"
			if name == "wrong known name" {
				expected = "wormhole.sync.pull"
				fixture.wantCode = "invalid_request"
			}
			_, code := decodeKnownPublicToolsCallParams(json.RawMessage(fixture.raw), expected, fixture.authHeader)
			if code != fixture.wantCode {
				t.Fatalf("code = %q, want %q", code, fixture.wantCode)
			}
		})
	}
}

func TestDecodePublicArgumentsRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	type args struct {
		Version int `json:"version"`
	}
	for _, raw := range []string{`{"version":2,"extra":1}`, `{"version":2,"version":2}`, `{"version":2}{}`} {
		var destination args
		if err := decodePublicArguments(json.RawMessage(raw), &destination); err == nil {
			t.Fatalf("decodePublicArguments accepted %s", raw)
		}
	}
	var got args
	if err := decodePublicArguments(json.RawMessage(`{"version":2}`), &got); err != nil || got.Version != 2 {
		t.Fatalf("decodePublicArguments = %+v, %v", got, err)
	}
}
```

Create `internal/mcp/safe_tool_error_test.go`:

```go
package mcp

import (
	"strings"
	"testing"
)

func TestPublicToolFailureResultHasExactCanonicalSafeBytes(t *testing.T) {
	result, err := toolFailureResult("wormhole.sync.push", "authentication_failed")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"authentication_failed","operation":"wormhole.sync.push"}`
	if !result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" || result.Content[0].Text != want {
		t.Fatalf("result = %+v, want exact %s", result, want)
	}
	for _, secret := range []string{"wrapped database cause", "/private/path", "Bearer token", "attachment-ref"} {
		if strings.Contains(result.Content[0].Text, secret) {
			t.Fatalf("safe result leaked %q", secret)
		}
	}
}

func TestPublicToolFailureResultRejectsUnknownCodesAndPrivateOperations(t *testing.T) {
	if _, err := toolFailureResult("wormhole.sync.push", "database_exploded"); err == nil {
		t.Fatal("unknown public failure code accepted")
	}
	if _, err := toolFailureResult("wormhole.task.create", "internal_error"); err == nil {
		t.Fatal("retained private operation accepted by public safe encoder")
	}
}

func TestPublicToolFailureCodeSetIsClosed(t *testing.T) {
	want := []string{
		"activity_cursor_invalid", "activity_lifecycle_conflict", "activity_not_found", "activity_policy_changed",
		"activity_policy_required", "activity_replay_conflict", "authentication_failed", "attachment_not_found",
		"internal_error", "invalid_activity", "invalid_request", "permission_denied", "sync_conflict",
		"sync_observer_unavailable", "sync_precondition_failed", "sync_replay_conflict", "unknown_activity_version", "unknown_version",
	}
	got := publicToolFailureCodes()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("codes = %q, want %q", got, want)
	}
}
```

Still in Step 5, create the independently authored schema authority
docs/contracts/public-fabric-descriptors.json exactly as follows. This file is
not emitted by reflection, a Go test, or production code. $ref only removes
textual duplication; the contract test below expands it before comparing every
live descriptor byte-for-byte.

~~~json
{
  "definitions": {
    "RepositoryIdentity": {"type":"object","properties":{"provider":{"type":"string"},"immutable_id":{"type":"string"},"canonical_remote":{"type":"string"}},"required":["provider","immutable_id","canonical_remote"],"additionalProperties":false},
    "ActorEnvelope": {"type":"object","properties":{"actor_kind":{"type":"string"},"human_principal_id":{"type":"string"},"agent_id":{"type":"string"},"accountable_human_id":{"type":"string"},"session_id":{"type":"string"},"harness_name":{"type":"string"},"harness_version":{"type":"string"},"model_name":{"type":"string"},"model_version":{"type":"string"},"assurance":{"type":"string"},"occurred_at":{"type":"string","format":"date-time"}},"required":["actor_kind","assurance","occurred_at"],"additionalProperties":false},
    "File": {"type":"object","properties":{"Path":{"type":"string"},"Data":{"type":"string","contentEncoding":"base64"}},"required":["Path","Data"],"additionalProperties":false},
    "Tree": {"type":"array","items":{"$ref":"#/definitions/File"}},
    "ExtensionV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"data":{}},"required":["schema_version","data"],"additionalProperties":false},
    "ExtensionsV1": {"type":"object","additionalProperties":{"$ref":"#/definitions/ExtensionV1"}},
    "ProjectV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"name":{"type":"string"},"aliases":{"type":"array","items":{"type":"string"}},"created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","name","aliases","created_at","updated_at","extensions"],"additionalProperties":false},
    "PublicKeyV1": {"type":"object","properties":{"key_id":{"type":"string"},"algorithm":{"type":"string"},"public_key_base64":{"type":"string"}},"required":["key_id","algorithm","public_key_base64"],"additionalProperties":false},
    "ActorV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"actor_kind":{"type":"string"},"display_name":{"type":"string"},"public_keys":{"type":"array","items":{"$ref":"#/definitions/PublicKeyV1"}},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","actor_kind","display_name","public_keys","extensions"],"additionalProperties":false},
    "TaskV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"parent_task_id":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"},"owner_actor_id":{"type":"string"},"status":{"type":"string"},"priority":{"type":"integer"},"due_by":{"type":"string","format":"date-time"},"created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","title","description","status","priority","created_at","updated_at","extensions"],"additionalProperties":false},
    "TaskLinkV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"task_id":{"type":"string"},"link_type":{"type":"string"},"target_id":{"type":"string"},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","task_id","link_type","target_id","extensions"],"additionalProperties":false},
    "KBArticleV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"title":{"type":"string"},"frontmatter":{"type":"object","additionalProperties":{}},"author_actor_id":{"type":"string"},"related_article_ids":{"type":"array","items":{"type":"string"}},"created_at":{"type":"string","format":"date-time"},"updated_at":{"type":"string","format":"date-time"},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","title","frontmatter","author_actor_id","related_article_ids","created_at","updated_at","extensions"],"additionalProperties":false},
    "ChannelV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"name":{"type":"string"},"created_at":{"type":"string","format":"date-time"},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","name","created_at","extensions"],"additionalProperties":false},
    "EventV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"channel_id":{"type":"string"},"actor_id":{"type":"string"},"event_type":{"type":"string"},"payload":{},"note":{"type":"string"},"created_at":{"type":"string","format":"date-time"},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","channel_id","actor_id","event_type","payload","created_at","extensions"],"additionalProperties":false},
    "GitLinkV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"kind":{"type":"string"},"id":{"type":"string"},"task_id":{"type":"string"},"repository":{"type":"string"},"commit_sha":{"type":"string"},"pr_url":{"type":"string"},"summary":{"type":"string"},"actor_id":{"type":"string"},"created_at":{"type":"string","format":"date-time"},"extensions":{"$ref":"#/definitions/ExtensionsV1"}},"required":["schema_version","kind","id","repository","summary","actor_id","created_at","extensions"],"additionalProperties":false},
    "RecordValueV1": {"type":"object","properties":{"project":{"$ref":"#/definitions/ProjectV1"},"actor":{"$ref":"#/definitions/ActorV1"},"task":{"$ref":"#/definitions/TaskV1"},"task_link":{"$ref":"#/definitions/TaskLinkV1"},"channel":{"$ref":"#/definitions/ChannelV1"},"event":{"$ref":"#/definitions/EventV1"},"git_link":{"$ref":"#/definitions/GitLinkV1"}},"required":[],"additionalProperties":false},
    "PutRecordV1": {"type":"object","properties":{"record":{"$ref":"#/definitions/RecordValueV1"}},"required":["record"],"additionalProperties":false},
    "PutKBArticleV1": {"type":"object","properties":{"record":{"$ref":"#/definitions/KBArticleV1"},"body":{"type":"string"}},"required":["record","body"],"additionalProperties":false},
    "RecordKey": {"type":"object","properties":{"Kind":{"type":"string"},"ID":{"type":"string"}},"required":["Kind","ID"],"additionalProperties":false},
    "TombstoneOperationV1": {"type":"object","properties":{"key":{"$ref":"#/definitions/RecordKey"},"expected_content_digest":{"type":"string"},"expected_body_digest":{"type":"string"}},"required":["key","expected_content_digest"],"additionalProperties":false},
    "ResurrectOperationV1": {"type":"object","properties":{"key":{"$ref":"#/definitions/RecordKey"},"expected_tombstone_digest":{"type":"string"},"record":{"$ref":"#/definitions/RecordValueV1"},"kb_record":{"$ref":"#/definitions/KBArticleV1"},"kb_body":{"type":"string"}},"required":["key","expected_tombstone_digest","record"],"additionalProperties":false},
    "OperationV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"id":{"type":"string"},"kind":{"type":"string"},"expected_view_digest":{"type":"string"},"actor":{"$ref":"#/definitions/ActorEnvelope"},"put_record":{"$ref":"#/definitions/PutRecordV1"},"put_kb_article":{"$ref":"#/definitions/PutKBArticleV1"},"tombstone":{"$ref":"#/definitions/TombstoneOperationV1"},"resurrect":{"$ref":"#/definitions/ResurrectOperationV1"}},"required":["schema_version","id","kind","expected_view_digest","actor"],"additionalProperties":false},
    "EffectiveActivityPolicyV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"policy_version":{"type":"integer"},"ordinary_max_age_seconds":{"type":"integer"},"ordinary_max_rows":{"type":"integer"},"terminal_default_age_seconds":{"type":"integer"},"terminal_maximum_age_seconds":{"type":"integer"},"terminal_retention_seconds":{"type":"integer"}},"required":["schema_version","policy_version","ordinary_max_age_seconds","ordinary_max_rows","terminal_default_age_seconds","terminal_maximum_age_seconds","terminal_retention_seconds"],"additionalProperties":false},
    "ActivityReceiptV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"activity_id":{"type":"string"},"activity_digest":{"type":"string"},"sequence":{"type":"integer"},"policy_version":{"type":"integer"},"policy_digest":{"type":"string"},"accepted_at":{"type":"string","format":"date-time"}},"required":["schema_version","activity_id","activity_digest","sequence","policy_version","policy_digest","accepted_at"],"additionalProperties":false},
    "ActivityLifecycleProjectionV1": {"type":"object","properties":{"kind":{"type":"string"},"reference_id":{"type":"string"}},"required":["kind","reference_id"],"additionalProperties":false},
    "ActivityEventProjectionV1": {"type":"object","properties":{"channel_id":{"type":"string"},"actor_id":{"type":"string"},"event_type":{"type":"string"},"payload":{},"note":{"type":"string"},"created_at":{"type":"string","format":"date-time"}},"required":["channel_id","actor_id","event_type","payload","created_at"],"additionalProperties":false},
    "ActivityV1": {"type":"object","properties":{"schema_version":{"type":"integer"},"id":{"type":"string"},"class":{"type":"string"},"actor":{"$ref":"#/definitions/ActorEnvelope"},"event":{"$ref":"#/definitions/ActivityEventProjectionV1"},"lifecycle":{"$ref":"#/definitions/ActivityLifecycleProjectionV1"},"created_at":{"type":"string","format":"date-time"}},"required":["schema_version","id","class","actor","created_at"],"additionalProperties":false},
    "SyncStateV2": {"type":"object","properties":{"stream_version":{"type":"integer"},"accepted_commit_sha":{"type":"string"},"accepted_tree_digest":{"type":"string"},"live_tree_digest":{"type":"string"},"accepted_tree":{"$ref":"#/definitions/Tree"},"live_tree":{"$ref":"#/definitions/Tree"},"open_conflict_ids":{"type":"array","items":{"type":"string"}}},"required":["stream_version","accepted_commit_sha","accepted_tree_digest","live_tree_digest","accepted_tree","live_tree","open_conflict_ids"],"additionalProperties":false},
    "SyncAttachV2Args": {"type":"object","properties":{"version":{"type":"integer","const":2},"repository":{"$ref":"#/definitions/RepositoryIdentity"},"canonical_ref":{"type":"string"},"base_commit_sha":{"type":"string"},"base_tree_digest":{"type":"string"}},"required":["version","repository","canonical_ref","base_commit_sha","base_tree_digest"],"additionalProperties":false},
    "SyncAttachV2Result": {"type":"object","properties":{"version":{"type":"integer","const":2},"attachment_ref":{"type":"string"},"remote_project_id":{"type":"string"},"stream_id":{"type":"string"},"stream_version":{"type":"integer"},"effective_activity_policy":{"$ref":"#/definitions/EffectiveActivityPolicyV1"}},"required":["version","attachment_ref","remote_project_id","stream_id","stream_version","effective_activity_policy"],"additionalProperties":false},
    "PublicAgentSessionIssueV2Args": {"type":"object","properties":{"version":{"type":"integer","const":2},"attachment_ref":{"type":"string"},"agent_id":{"type":"string"},"harness_name":{"type":"string"},"harness_version":{"type":"string"},"model_name":{"type":"string"},"model_version":{"type":"string"}},"required":["version","attachment_ref","agent_id","harness_name","harness_version","model_name","model_version"],"additionalProperties":false},
    "PublicAgentSessionIssueV2Result": {"type":"object","properties":{"version":{"type":"integer","const":2},"session_id":{"type":"string"},"agent_id":{"type":"string"},"accountable_human_id":{"type":"string"},"harness_name":{"type":"string"},"harness_version":{"type":"string"},"model_name":{"type":"string"},"model_version":{"type":"string"},"assurance":{"type":"string","const":"public-key-continuity"},"expires_at":{"type":"string","format":"date-time"}},"required":["version","session_id","agent_id","accountable_human_id","harness_name","harness_version","model_name","model_version","assurance","expires_at"],"additionalProperties":false},
    "SyncBootstrapV2Args": {"type":"object","properties":{"version":{"type":"integer","const":2},"attachment_ref":{"type":"string"},"repository":{"$ref":"#/definitions/RepositoryIdentity"},"canonical_ref":{"type":"string"},"base_commit_sha":{"type":"string"},"base_tree_digest":{"type":"string"},"expected_stream_version":{"type":"integer"},"expected_live_tree_digest":{"type":"string"},"after_version":{"type":"integer"}},"required":["version","attachment_ref","repository","canonical_ref","base_commit_sha","base_tree_digest","expected_stream_version","expected_live_tree_digest","after_version"],"additionalProperties":false},
    "SyncBootstrapV2Result": {"type":"object","properties":{"version":{"type":"integer","const":2},"changed":{"type":"boolean"},"state":{"$ref":"#/definitions/SyncStateV2"},"effective_activity_policy":{"$ref":"#/definitions/EffectiveActivityPolicyV1"}},"required":["version","changed","state","effective_activity_policy"],"additionalProperties":false},
    "SyncPullV2Args": {"type":"object","properties":{"version":{"type":"integer","const":2},"attachment_ref":{"type":"string"},"repository":{"$ref":"#/definitions/RepositoryIdentity"},"canonical_ref":{"type":"string"},"base_commit_sha":{"type":"string"},"base_tree_digest":{"type":"string"},"expected_stream_version":{"type":"integer"},"expected_live_tree_digest":{"type":"string"},"after_version":{"type":"integer"}},"required":["version","attachment_ref","repository","canonical_ref","base_commit_sha","base_tree_digest","expected_stream_version","expected_live_tree_digest","after_version"],"additionalProperties":false},
    "SyncPullV2Result": {"type":"object","properties":{"version":{"type":"integer","const":2},"changed":{"type":"boolean"},"state":{"$ref":"#/definitions/SyncStateV2"}},"required":["version","changed","state"],"additionalProperties":false},
    "SyncPushV2Args": {"type":"object","properties":{"version":{"type":"integer","const":2},"attachment_ref":{"type":"string"},"repository":{"$ref":"#/definitions/RepositoryIdentity"},"canonical_ref":{"type":"string"},"base_commit_sha":{"type":"string"},"base_tree_digest":{"type":"string"},"expected_stream_version":{"type":"integer"},"expected_live_tree_digest":{"type":"string"},"operation":{"$ref":"#/definitions/OperationV1"}},"required":["version","attachment_ref","repository","canonical_ref","base_commit_sha","base_tree_digest","expected_stream_version","expected_live_tree_digest","operation"],"additionalProperties":false},
    "SyncPushAppliedV2Result": {"type":"object","properties":{"version":{"type":"integer","const":2},"status":{"type":"string","const":"applied"},"operation_id":{"type":"string"},"stream_version":{"type":"integer"},"live_tree_digest":{"type":"string"}},"required":["version","status","operation_id","stream_version","live_tree_digest"],"additionalProperties":false},
    "SyncPushConflictV2Result": {"type":"object","properties":{"version":{"type":"integer","const":2},"status":{"type":"string","const":"conflict"},"operation_id":{"type":"string"},"stream_version":{"type":"integer"},"live_tree_digest":{"type":"string"},"conflict_id":{"type":"string"}},"required":["version","status","operation_id","stream_version","live_tree_digest","conflict_id"],"additionalProperties":false},
    "SyncConflictV2Args": {"type":"object","properties":{"version":{"type":"integer","const":2},"attachment_ref":{"type":"string"},"repository":{"$ref":"#/definitions/RepositoryIdentity"},"canonical_ref":{"type":"string"},"base_commit_sha":{"type":"string"},"base_tree_digest":{"type":"string"},"expected_stream_version":{"type":"integer"},"expected_live_tree_digest":{"type":"string"},"conflict_id":{"type":"string"},"resolution":{"$ref":"#/definitions/OperationV1"}},"required":["version","attachment_ref","repository","canonical_ref","base_commit_sha","base_tree_digest","expected_stream_version","expected_live_tree_digest","conflict_id","resolution"],"additionalProperties":false},
    "SyncConflictResolvedV2Result": {"type":"object","properties":{"version":{"type":"integer","const":2},"status":{"type":"string","const":"resolved"},"conflict_id":{"type":"string"},"operation_id":{"type":"string"},"stream_version":{"type":"integer"},"live_tree_digest":{"type":"string"}},"required":["version","status","conflict_id","operation_id","stream_version","live_tree_digest"],"additionalProperties":false},
    "ActivityAcceptV1Args": {"type":"object","properties":{"version":{"type":"integer","const":1},"attachment_ref":{"type":"string"},"policy_version":{"type":"integer"},"policy_digest":{"type":"string"},"activity":{"$ref":"#/definitions/ActivityV1"},"activity_digest":{"type":"string"}},"required":["version","attachment_ref","policy_version","policy_digest","activity","activity_digest"],"additionalProperties":false},
    "ActivityPresenceV1Args": {"type":"object","properties":{"version":{"type":"integer","const":1},"attachment_ref":{"type":"string"},"policy_version":{"type":"integer"},"policy_digest":{"type":"string"},"activity":{"$ref":"#/definitions/ActivityV1"},"activity_digest":{"type":"string"}},"required":["version","attachment_ref","policy_version","policy_digest","activity","activity_digest"],"additionalProperties":false},
    "ActivityAcceptedV1Result": {"type":"object","properties":{"version":{"type":"integer","const":1},"status":{"type":"string","const":"accepted"},"receipt":{"$ref":"#/definitions/ActivityReceiptV1"},"effective_activity_policy":{"$ref":"#/definitions/EffectiveActivityPolicyV1"},"policy_digest":{"type":"string"}},"required":["version","status","receipt","effective_activity_policy","policy_digest"],"additionalProperties":false},
    "ActivityPolicyChangedV1Result": {"type":"object","properties":{"version":{"type":"integer","const":1},"status":{"type":"string","const":"policy_changed"},"effective_activity_policy":{"$ref":"#/definitions/EffectiveActivityPolicyV1"},"policy_digest":{"type":"string"}},"required":["version","status","effective_activity_policy","policy_digest"],"additionalProperties":false},
    "ActivityPresenceAcceptedV1Result": {"type":"object","properties":{"version":{"type":"integer","const":1},"status":{"type":"string","const":"accepted"}},"required":["version","status"],"additionalProperties":false},
    "ActivityPullV1Args": {"type":"object","properties":{"version":{"type":"integer","const":1},"attachment_ref":{"type":"string"},"after_sequence":{"type":"integer"},"limit":{"type":"integer"}},"required":["version","attachment_ref","after_sequence","limit"],"additionalProperties":false},
    "ActivityPolicyEvidenceV1": {"type":"object","properties":{"policy":{"$ref":"#/definitions/EffectiveActivityPolicyV1"},"policy_digest":{"type":"string"}},"required":["policy","policy_digest"],"additionalProperties":false},
    "ActivityDeliveryV1": {"type":"object","properties":{"source_ref":{"type":"string"},"activity":{"$ref":"#/definitions/ActivityV1"},"activity_digest":{"type":"string"},"receipt":{"$ref":"#/definitions/ActivityReceiptV1"}},"required":["source_ref","activity","activity_digest","receipt"],"additionalProperties":false},
    "ActivityPullV1Result": {"type":"object","properties":{"version":{"type":"integer","const":1},"effective_activity_policy":{"$ref":"#/definitions/EffectiveActivityPolicyV1"},"policy_digest":{"type":"string"},"historical_policies":{"type":"array","items":{"$ref":"#/definitions/ActivityPolicyEvidenceV1"}},"deliveries":{"type":"array","items":{"$ref":"#/definitions/ActivityDeliveryV1"}},"next_sequence":{"type":"integer"},"has_more":{"type":"boolean"}},"required":["version","effective_activity_policy","policy_digest","historical_policies","deliveries","next_sequence","has_more"],"additionalProperties":false},
    "ActivityLifecycleV1Args": {"type":"object","properties":{"version":{"type":"integer","const":1},"attachment_ref":{"type":"string"},"activity_id":{"type":"string"},"kind":{"type":"string"},"reference_id":{"type":"string"},"expected_state":{"type":"string"},"next_state":{"type":"string"}},"required":["version","attachment_ref","activity_id","kind","reference_id","expected_state","next_state"],"additionalProperties":false},
    "ActivityLifecycleV1Result": {"type":"object","properties":{"version":{"type":"integer","const":1},"state":{"type":"string"}},"required":["version","state"],"additionalProperties":false}
  },
  "descriptors": [
    {"name":"wormhole.activity.accept","description":"Accept durable Activity v1 or return current policy evidence.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/ActivityAcceptV1Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/ActivityAcceptedV1Result"},{"$ref":"#/definitions/ActivityPolicyChangedV1Result"}]}},
    {"name":"wormhole.activity.lifecycle","description":"Apply a source-owned Activity v1 lifecycle transition.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/ActivityLifecycleV1Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/ActivityLifecycleV1Result"}]}},
    {"name":"wormhole.activity.presence","description":"Accept ephemeral Activity v1 presence without durable Activity state.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/ActivityPresenceV1Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/ActivityPresenceAcceptedV1Result"},{"$ref":"#/definitions/ActivityPolicyChangedV1Result"}]}},
    {"name":"wormhole.activity.pull","description":"Pull ordered Activity v1 deliveries and policy evidence.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/ActivityPullV1Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/ActivityPullV1Result"}]}},
    {"name":"wormhole.sync.attach","description":"Attach an observed canonical repository ref to public sync v2.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/SyncAttachV2Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/SyncAttachV2Result"}]}},
    {"name":"wormhole.sync.bootstrap","description":"Read one complete validated sync v2 stream state and finite Activity policy.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/SyncBootstrapV2Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/SyncBootstrapV2Result"}]}},
    {"name":"wormhole.sync.conflict","description":"Resolve one durable sync v2 conflict with a canonical operation.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/SyncConflictV2Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/SyncConflictResolvedV2Result"}]}},
    {"name":"wormhole.sync.issue_agent_session","description":"Issue an accountable public agent session from a tracked human key.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/PublicAgentSessionIssueV2Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/PublicAgentSessionIssueV2Result"}]}},
    {"name":"wormhole.sync.pull","description":"Read one complete validated sync v2 stream state.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/SyncPullV2Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/SyncPullV2Result"}]}},
    {"name":"wormhole.sync.push","description":"Apply one canonical sync v2 operation or return its durable conflict.","auth_family":"public_proof","input_schema":{"$ref":"#/definitions/SyncPushV2Args"},"output_schema":{"oneOf":[{"$ref":"#/definitions/SyncPushAppliedV2Result"},{"$ref":"#/definitions/SyncPushConflictV2Result"}]}}
  ]
}
~~~

The ActivityPresenceV1Args and SyncPullV2Args aliases intentionally repeat
their complete exact shared shapes so every descriptor root is independently
visible in the golden. Every struct object is closed, dynamic maps
carry an explicit additionalProperties schema, raw JSON is {}, all required
arrays are explicit, every version/status/assurance constant is literal, every
result is under oneOf, and Tree.File.Data freezes contentEncoding as base64.

- [ ] **Step 6: Run MCP RED**

Run:

```bash
go test ./internal/mcp -run 'Test(SyncV2MCPAliases|PublicFabricToolDescriptors|PublicDescriptor|RetainedPrivateCreateTaskSchemaGolden|ClosedSchema|ToolsCallParams|ProbeToolsCallName|DecodeKnownPublic|DecodePublicArguments|PublicToolFailure)' -count=1
```

Expected: FAIL to compile because the aliases, descriptor API, strict helpers, and safe encoder do not exist.

- [ ] **Step 7: Add MCP aliases, closed descriptors, and the public-only safe encoder**

Create `internal/mcp/sync_v2_contract.go` exactly:

```go
package mcp

import (
	"bytes"
	"errors"
	"reflect"
	"sort"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type SyncV2Scope = projectstate.SyncV2Scope
type SyncStateV2 = projectstate.SyncStateV2
type SyncAttachV2Args = projectstate.SyncAttachV2Args
type SyncAttachV2Result = projectstate.SyncAttachV2Result
type PublicAgentSessionIssueV2Args = projectstate.PublicAgentSessionIssueV2Args
type PublicAgentSessionIssueV2Result = projectstate.PublicAgentSessionIssueV2Result
type SyncBootstrapV2Args = projectstate.SyncBootstrapV2Args
type SyncBootstrapV2Result = projectstate.SyncBootstrapV2Result
type SyncPullV2Args = projectstate.SyncPullV2Args
type SyncPullV2Result = projectstate.SyncPullV2Result
type SyncPushV2Args = projectstate.SyncPushV2Args
type SyncPushAppliedV2Result = projectstate.SyncPushAppliedV2Result
type SyncPushConflictV2Result = projectstate.SyncPushConflictV2Result
type SyncConflictV2Args = projectstate.SyncConflictV2Args
type SyncConflictResolvedV2Result = projectstate.SyncConflictResolvedV2Result
type ActivityAcceptV1Args = projectstate.ActivityAcceptV1Args
type ActivityAcceptedV1Result = projectstate.ActivityAcceptedV1Result
type ActivityPolicyChangedV1Result = projectstate.ActivityPolicyChangedV1Result
type ActivityPresenceV1Args = projectstate.ActivityPresenceV1Args
type ActivityPresenceAcceptedV1Result = projectstate.ActivityPresenceAcceptedV1Result
type ActivityPullV1Args = projectstate.ActivityPullV1Args
type ActivityPolicyEvidenceV1 = projectstate.ActivityPolicyEvidenceV1
type ActivityDeliveryV1 = projectstate.ActivityDeliveryV1
type ActivityPullV1Result = projectstate.ActivityPullV1Result
type ActivityLifecycleV1Args = projectstate.ActivityLifecycleV1Args
type ActivityLifecycleV1Result = projectstate.ActivityLifecycleV1Result

type ToolAuthFamily string

const PublicProofAuth ToolAuthFamily = "public_proof"

type ToolDescriptor struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	AuthFamily   ToolAuthFamily `json:"auth_family"`
	InputSchema  map[string]any `json:"input_schema"`
	OutputSchema map[string]any `json:"output_schema"`
}

func PublicFabricToolDescriptors() []ToolDescriptor {
	return []ToolDescriptor{
		publicDescriptor("wormhole.activity.accept", "Accept durable Activity v1 or return current policy evidence.", ActivityAcceptV1Args{}, ActivityAcceptedV1Result{}, ActivityPolicyChangedV1Result{}),
		publicDescriptor("wormhole.activity.lifecycle", "Apply a source-owned Activity v1 lifecycle transition.", ActivityLifecycleV1Args{}, ActivityLifecycleV1Result{}),
		publicDescriptor("wormhole.activity.presence", "Accept ephemeral Activity v1 presence without durable Activity state.", ActivityPresenceV1Args{}, ActivityPresenceAcceptedV1Result{}, ActivityPolicyChangedV1Result{}),
		publicDescriptor("wormhole.activity.pull", "Pull ordered Activity v1 deliveries and policy evidence.", ActivityPullV1Args{}, ActivityPullV1Result{}),
		publicDescriptor("wormhole.sync.attach", "Attach an observed canonical repository ref to public sync v2.", SyncAttachV2Args{}, SyncAttachV2Result{}),
		publicDescriptor("wormhole.sync.bootstrap", "Read one complete validated sync v2 stream state and finite Activity policy.", SyncBootstrapV2Args{}, SyncBootstrapV2Result{}),
		publicDescriptor("wormhole.sync.conflict", "Resolve one durable sync v2 conflict with a canonical operation.", SyncConflictV2Args{}, SyncConflictResolvedV2Result{}),
		publicDescriptor("wormhole.sync.issue_agent_session", "Issue an accountable public agent session from a tracked human key.", PublicAgentSessionIssueV2Args{}, PublicAgentSessionIssueV2Result{}),
		publicDescriptor("wormhole.sync.pull", "Read one complete validated sync v2 stream state.", SyncPullV2Args{}, SyncPullV2Result{}),
		publicDescriptor("wormhole.sync.push", "Apply one canonical sync v2 operation or return its durable conflict.", SyncPushV2Args{}, SyncPushAppliedV2Result{}, SyncPushConflictV2Result{}),
	}
}

func publicDescriptor(name, description string, input any, outputs ...any) ToolDescriptor {
	return ToolDescriptor{
		Name: name, Description: description, AuthFamily: PublicProofAuth,
		InputSchema: closedJSONSchemaForType(reflect.TypeOf(input)), OutputSchema: schemaOneOf(outputs...),
	}
}

type ToolFailureV1 struct {
	Code      string `json:"code"`
	Operation string `json:"operation"`
}

var errInvalidPublicToolFailure = errors.New("mcp: invalid public tool failure")

var publicFailureCodeSet = map[string]struct{}{
	"invalid_request": {}, "unknown_version": {}, "authentication_failed": {}, "permission_denied": {},
	"attachment_not_found": {}, "sync_precondition_failed": {}, "sync_conflict": {}, "sync_replay_conflict": {},
	"sync_observer_unavailable": {}, "internal_error": {}, "invalid_activity": {}, "unknown_activity_version": {},
	"activity_policy_required": {}, "activity_policy_changed": {}, "activity_not_found": {}, "activity_replay_conflict": {},
	"activity_cursor_invalid": {}, "activity_lifecycle_conflict": {},
}

func publicToolFailureCodes() []string {
	codes := make([]string, 0, len(publicFailureCodeSet))
	for code := range publicFailureCodeSet {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func isPublicFabricTool(operation string) bool {
	for _, descriptor := range PublicFabricToolDescriptors() {
		if descriptor.Name == operation {
			return true
		}
	}
	return false
}

func toolFailureResult(operation, code string) (toolCallResult, error) {
	if !isPublicFabricTool(operation) {
		return toolCallResult{}, errInvalidPublicToolFailure
	}
	if _, ok := publicFailureCodeSet[code]; !ok {
		return toolCallResult{}, errInvalidPublicToolFailure
	}
	canonical, err := projectstate.CanonicalJSON(ToolFailureV1{Code: code, Operation: operation})
	if err != nil {
		return toolCallResult{}, errInvalidPublicToolFailure
	}
	canonical = bytes.TrimSuffix(canonical, []byte{'\n'})
	return toolCallResult{
		Content: []toolCallResultContent{{Type: "text", Text: string(canonical)}}, IsError: true,
	}, nil
}
```

- [ ] **Step 8: Extend the existing schema helpers without changing retained private schemas**

In `internal/mcp/jsonrpc.go`, add `bytes`, `io`, and `strconv` to the standard-library imports and add `github.com/H4RL33/wormhole/internal/types`. Replace `reflectStructSchema`, `jsonSchemaForType`, and their supporting input-schema helpers with this complete implementation; leave `jsonResponseSchemaForType`, `jsonPresentResponseSchemaForType`, and `reflectResponseStructSchema` in place:

```go
type schemaOptions struct {
	closedObjects   bool
	flattenAnonymous bool
}

func reflectStructSchema(t reflect.Type) (map[string]any, []string) {
	return reflectStructSchemaWithOptions(t, schemaOptions{})
}

func reflectStructSchemaWithOptions(t reflect.Type, options schemaOptions) (map[string]any, []string) {
	properties := map[string]any{}
	required := []string{}
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return properties, required
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}
		tag := field.Tag.Get("json")
		if options.flattenAnonymous && field.Anonymous && (tag == "" || strings.HasPrefix(tag, ",")) {
			nested := field.Type
			for nested.Kind() == reflect.Ptr {
				nested = nested.Elem()
			}
			if nested.Kind() == reflect.Struct && nested != reflect.TypeOf(time.Time{}) {
				nestedProperties, nestedRequired := reflectStructSchemaWithOptions(nested, options)
				for name, schema := range nestedProperties {
					properties[name] = schema
				}
				for _, name := range nestedRequired {
					required = appendUnique(required, name)
				}
				continue
			}
		}
		name, omitempty := parseJSONTag(tag, field.Name)
		if name == "-" {
			continue
		}
		fieldType := field.Type
		optional := omitempty
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
			optional = true
		}
		schema := jsonSchemaForTypeWithOptions(fieldType, options)
		applySchemaTags(schema, field)
		properties[name] = schema
		if !optional {
			required = appendUnique(required, name)
		}
	}
	return properties, required
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func applySchemaTags(schema map[string]any, field reflect.StructField) {
	if enumTag := field.Tag.Get("enum"); enumTag != "" {
		values := strings.Split(enumTag, ",")
		enumValues := make([]any, len(values))
		for i, value := range values {
			enumValues[i] = value
		}
		schema["enum"] = enumValues
	}
	constant := field.Tag.Get("const")
	if constant == "" {
		return
	}
	t := field.Type
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value, err := strconv.ParseInt(constant, 10, 64); err == nil {
			schema["const"] = int(value)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value, err := strconv.ParseUint(constant, 10, 64); err == nil {
			schema["const"] = value
		}
	case reflect.Bool:
		if value, err := strconv.ParseBool(constant); err == nil {
			schema["const"] = value
		}
	default:
		schema["const"] = constant
	}
}

func jsonSchemaForType(t reflect.Type) map[string]any {
	return jsonSchemaForTypeWithOptions(t, schemaOptions{})
}

func closedJSONSchemaForType(t reflect.Type) map[string]any {
	return jsonSchemaForTypeWithOptions(t, schemaOptions{closedObjects: true, flattenAnonymous: true})
}

func jsonSchemaForTypeWithOptions(t reflect.Type, options schemaOptions) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch {
	case t == reflect.TypeOf(time.Time{}):
		return map[string]any{"type": "string", "format": "date-time"}
	case t == reflect.TypeOf(json.RawMessage{}):
		return map[string]any{}
	case t == reflect.TypeOf([]byte(nil)):
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": jsonSchemaForTypeWithOptions(t.Elem(), options)}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return map[string]any{"type": "object"}
		}
		return map[string]any{"type": "object", "additionalProperties": jsonSchemaForTypeWithOptions(t.Elem(), options)}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		properties, required := reflectStructSchemaWithOptions(t, options)
		schema := map[string]any{"type": "object", "properties": properties, "required": required}
		if options.closedObjects {
			schema["additionalProperties"] = false
		}
		return schema
	default:
		return map[string]any{}
	}
}

func schemaOneOf(examples ...any) map[string]any {
	variants := make([]any, 0, len(examples))
	for _, example := range examples {
		variants = append(variants, closedJSONSchemaForType(reflect.TypeOf(example)))
	}
	return map[string]any{"oneOf": variants}
}
```

This wrapper split is binding: `buildInputSchema` continues to call the open compatibility wrapper `reflectStructSchema`, so all sixteen retained private input schemas remain byte-identical. Only `PublicFabricToolDescriptors` calls the recursively closed wrapper.

- [ ] **Step 9: Add exact `ToolsCallParams` and strict public parsing primitives without changing live private dispatch**

Replace the existing unexported params declaration in `internal/mcp/jsonrpc.go` and insert the complete helpers below it. `HandleToolsCall` continues using the compatibility alias and its existing code path; no public descriptor is registered in this slice.

```go
type ToolsCallParams struct {
	Name      string                    `json:"name"`
	Arguments json.RawMessage           `json:"arguments"`
	Proof     *types.PublicRequestProof `json:"proof,omitempty"`
}

type toolsCallParams = ToolsCallParams

func probeToolsCallName(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", errors.New("mcp: unidentified tools/call")
	}
	name := ""
	count := 0
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", errors.New("mcp: unidentified tools/call")
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", errors.New("mcp: unidentified tools/call")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return "", errors.New("mcp: unidentified tools/call")
		}
		if key != "name" {
			continue
		}
		count++
		if count != 1 || json.Unmarshal(value, &name) != nil || name == "" {
			return "", errors.New("mcp: unidentified tools/call")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return "", errors.New("mcp: unidentified tools/call")
	}
	if err := requireJSONEOF(decoder); err != nil || count != 1 {
		return "", errors.New("mcp: unidentified tools/call")
	}
	return name, nil
}

func decodeKnownPublicToolsCallParams(raw json.RawMessage, expectedName, authHeader string) (ToolsCallParams, string) {
	fields, err := decodeUniqueJSONObject(raw, map[string]bool{"name": true, "arguments": true, "proof": true})
	if err != nil || fields["name"] == nil || fields["arguments"] == nil {
		return ToolsCallParams{}, "invalid_request"
	}
	var params ToolsCallParams
	if err := json.Unmarshal(fields["name"], &params.Name); err != nil || params.Name != expectedName {
		return ToolsCallParams{}, "invalid_request"
	}
	if len(bytes.TrimSpace(fields["arguments"])) == 0 || bytes.TrimSpace(fields["arguments"])[0] != '{' || !json.Valid(fields["arguments"]) {
		return ToolsCallParams{}, "invalid_request"
	}
	params.Arguments = append(json.RawMessage(nil), fields["arguments"]...)
	if proofRaw := fields["proof"]; proofRaw != nil {
		var proof types.PublicRequestProof
		if err := decodePublicArguments(proofRaw, &proof); err != nil {
			return ToolsCallParams{}, "invalid_request"
		}
		params.Proof = &proof
	}
	if params.Proof == nil || authHeader != "" {
		return ToolsCallParams{}, "authentication_failed"
	}
	return params, ""
}

func decodePublicArguments(raw json.RawMessage, destination any) error {
	if _, err := decodeUniqueJSONObject(raw, nil); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func decodeUniqueJSONObject(raw json.RawMessage, allowed map[string]bool) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("mcp: expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("mcp: object key is not a string")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("mcp: duplicate JSON member")
		}
		if allowed != nil && !allowed[key] {
			return nil, errors.New("mcp: unknown JSON member")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("mcp: malformed JSON object")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	return fields, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("mcp: trailing JSON value")
		}
		return err
	}
	return nil
}
```

- [ ] **Step 10: Run focused GREEN and the first repository-wide gate**

Run:

```bash
gofmt -w internal/types/public_proof.go internal/types/public_proof_test.go internal/types/projectstate/sync_protocol.go internal/types/projectstate/sync_protocol_test.go internal/mcp/jsonrpc.go internal/mcp/sync_v2_contract.go internal/mcp/sync_v2_contract_test.go internal/mcp/public_proof_test.go internal/mcp/safe_tool_error_test.go
jq -e '
  (.descriptors | length == 10 and length == (map(.name) | unique | length)) and
  (all(.descriptors[];
    .auth_family == "public_proof" and
    (.input_schema | has("$ref")) and
    (.output_schema.oneOf | length >= 1))) and
  (.definitions.File.properties.Data.contentEncoding == "base64")
' docs/contracts/public-fabric-descriptors.json
go test ./internal/types/... ./internal/mcp -count=1
go test ./... -count=1
go vet ./...
.github/scripts/check-contract-manifest.sh
make check
go list -json ./internal/runtime/... | jq -s -e '
  [.[] | .Imports[]? | select(
    . == "github.com/H4RL33/wormhole/internal/mcp" or
    startswith("github.com/H4RL33/wormhole/internal/core/"))] | length == 0'
```

Expected: every command exits 0; the retained Fabric manifest remains unchanged in this commit because the live registry still has its pre-cut four v1 tools.

- [ ] **Step 11: Commit the shared contract and strict non-live primitives**

```bash
git add internal/types/public_proof.go internal/types/public_proof_test.go internal/types/projectstate/sync_protocol.go internal/types/projectstate/sync_protocol_test.go internal/mcp/jsonrpc.go internal/mcp/sync_v2_contract.go internal/mcp/sync_v2_contract_test.go internal/mcp/public_proof_test.go internal/mcp/safe_tool_error_test.go docs/contracts/public-fabric-descriptors.json
git commit -m "feat(sync): freeze v2 public contracts"
make check
```

Expected: the commit succeeds and the post-commit repository-wide gate exits
0 against that exact commit.

### Task 2: Add the Single V2 Status Shell and Extract Activity-Surviving Interfaces

**Files:**
- Create: `internal/runtime/sync/contract_v2.go`
- Create: `internal/runtime/sync/engine_v2.go`
- Create: `internal/runtime/sync/engine_v2_test.go`
- Modify: `internal/runtime/sync/sync.go:38-46`

**Interfaces:**
- Consumes: the frozen `Status`, `ConnectionState`, and Activity transport dependencies.
- Produces: the only `V2Engine`, `NewV2Engine() *V2Engine`, `(*V2Engine).Status(context.Context) (Status, error)`, and surviving `CredentialSource`/`FabricRouteSource` interfaces.

- [ ] **Step 1: Write the failing shell/status and survivor tests**

Create `internal/runtime/sync/engine_v2_test.go`:

```go
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
)

func TestV2EngineStatusPreservesExactLocalWire(t *testing.T) {
	engine := NewV2Engine()
	status, err := engine.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Status{State: StateOffline, PendingWrites: 0}
	if status != want {
		t.Fatalf("status = %+v, want %+v", status, want)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"state":"offline","pending_writes":0}` {
		t.Fatalf("status JSON = %s", raw)
	}
}

func TestNilV2EngineStatusFailsWithoutPanic(t *testing.T) {
	var engine *V2Engine
	status, err := engine.Status(context.Background())
	if err == nil || status != (Status{}) {
		t.Fatalf("nil status = %+v, %v", status, err)
	}
}

type v2CredentialSourceFixture struct{}

func (v2CredentialSourceFixture) Read(context.Context, string) (string, error) {
	return "credential", nil
}

type v2RouteSourceFixture struct{}

func (v2RouteSourceFixture) GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error) {
	return types.FabricBinding{}, types.FabricProfile{}, errors.New("fixture")
}

func TestActivityV1DependenciesSurviveV2Shell(t *testing.T) {
	var credential CredentialSource = v2CredentialSourceFixture{}
	var route FabricRouteSource = v2RouteSourceFixture{}
	if credential == nil || route == nil {
		t.Fatal("Activity v1 dependency interfaces are unavailable")
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/runtime/sync -run 'Test(V2EngineStatus|NilV2EngineStatus|ActivityV1Dependencies)' -count=1
```

Expected: FAIL to compile because `V2Engine` and `NewV2Engine` do not exist.

- [ ] **Step 3: Move only the Activity-surviving interfaces to their permanent owner**

Create `internal/runtime/sync/contract_v2.go`:

```go
package sync

import (
	"context"

	"github.com/H4RL33/wormhole/internal/types"
)

// CredentialSource resolves one profile-owned credential reference for one request.
type CredentialSource interface {
	Read(context.Context, string) (string, error)
}

// FabricRouteSource resolves one exact workspace-scoped Fabric binding.
type FabricRouteSource interface {
	GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
}
```

Delete only these two declarations from `internal/runtime/sync/sync.go`; retain `IntegrationManifestReceiver` and `bootstrapIntegrationManifestRollback` until the atomic v1 deletion in Task 3:

```go
type CredentialSource interface {
	Read(context.Context, string) (string, error)
}

type FabricRouteSource interface {
	GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
}
```

- [ ] **Step 4: Add `V2Engine` once with the frozen status signature**

Create `internal/runtime/sync/engine_v2.go`:

```go
package sync

import (
	"context"
	"errors"
	"sync"
)

type V2Engine struct {
	statusMu sync.RWMutex
	status   Status
}

func NewV2Engine() *V2Engine {
	return &V2Engine{status: Status{State: StateOffline, PendingWrites: 0}}
}

func (e *V2Engine) Status(context.Context) (Status, error) {
	if e == nil {
		return Status{}, errors.New("sync: status unavailable")
	}
	e.statusMu.RLock()
	defer e.statusMu.RUnlock()
	return e.status, nil
}
```

Do not add another `V2Engine` declaration to `status.go`, `protocol_v2.go`, or a later task. The legacy `(*Engine).Status` remains temporarily in `status.go` only so this intermediate commit compiles; Task 3 deletes it together with the v1 `Engine`.

- [ ] **Step 5: Run focused GREEN, Activity preservation, and the second repository-wide gate**

Run:

```bash
gofmt -w internal/runtime/sync/contract_v2.go internal/runtime/sync/engine_v2.go internal/runtime/sync/engine_v2_test.go internal/runtime/sync/sync.go
go test ./internal/runtime/sync -run 'Test(V2EngineStatus|NilV2EngineStatus|ActivityV1Dependencies|Activity|Promotion|Status)' -count=1
go test ./internal/types/projectstate ./internal/core/git ./internal/runtime/localstore ./internal/runtime/sync -run 'Activity|Promotion|Status' -count=1
go test ./... -count=1
go vet ./...
.github/scripts/check-contract-manifest.sh
make check
go list -json ./internal/runtime/... | jq -s -e '
  [.[] | .Imports[]? | select(
    . == "github.com/H4RL33/wormhole/internal/mcp" or
    startswith("github.com/H4RL33/wormhole/internal/core/"))] | length == 0'
```

Expected: every command exits 0; both legacy and v2 status receivers compile only in this intermediate commit, while `V2Engine` itself is declared once.

- [ ] **Step 6: Commit the shell and survivor extraction**

```bash
git add internal/runtime/sync/contract_v2.go internal/runtime/sync/engine_v2.go internal/runtime/sync/engine_v2_test.go internal/runtime/sync/sync.go
git commit -m "refactor(sync): add v2 status shell"
make check
```

Expected: the commit succeeds and the post-commit repository-wide gate exits
0 against that exact commit.

### Task 3: Perform the Atomic V1 Deletion and Migrate Every Survivor, Caller, Contract, and Active Document

**Files:**
- Modify: `internal/mcp/fabric_registry.go`
- Modify: `internal/mcp/registry.go`
- Modify: `internal/mcp/registry_test.go`
- Modify: `internal/mcp/audit_test.go`
- Modify: `internal/mcp/jsonrpc_test.go`
- Modify: `internal/mcp/integration_manifest_test.go`
- Modify: `internal/mcp/tool_contract_coverage_test.go`
- Modify: `internal/mcp/contract_manifest_test.go`
- Modify: `internal/mcp/jsonrpc_toolscall_test.go`
- Modify: `internal/runtime/sync/status.go`
- Modify: `internal/runtime/sync/contract_manifest_test.go`
- Modify: `internal/runtime/localapi/localapi.go`
- Modify: `internal/runtime/localapi/enrolment.go`
- Modify: `internal/runtime/localapi/enrolment_test.go`
- Modify: `internal/runtime/localapi/manifest.go`
- Modify: `internal/runtime/localapi/manifest_test.go`
- Modify: `internal/runtime/localapi/localapi_test.go`
- Modify: `internal/runtime/localapi/localapi_p5_test.go`
- Modify: `internal/core/identity/identity.go`
- Modify: `internal/core/identity/unavailable_database_coverage_test.go`
- Modify: `internal/core/events/events.go`
- Modify: `internal/core/events/events_test.go`
- Modify: `internal/core/tasks/tasks.go`
- Modify: `internal/core/tasks/tasks_test.go`
- Modify: `internal/core/kb/kb.go`
- Modify: `internal/core/kb/kb_test.go`
- Modify: `cmd/gatewayd/gatewayd.go`
- Modify: `cmd/gatewayd/p7_e2e_integration_test.go`
- Modify: `cmd/fabric/m3_integration_test.go`
- Modify: `cmd/wormhole/active_documentation_cutover_test.go`
- Modify: `docs/contracts/alpha-contract.json`
- Modify: `docs/contracts/README.md`
- Modify: `docs/mcp-protocol.md`
- Modify: `docs/compatibility.md`
- Modify: `docs/rfcs/wormhole_rfc_local_runtime.md`
- Modify: `docs/testing/alpha-validation.md`
- Modify: `docs/testing/manual-alpha-validation-2026-07.md`
- Modify: `docs/testing/code-graph-benchmarks.md`
- Modify: `testdata/codegraph/benchmark-corpus.json`
- Modify: `README.md`
- Modify: `SECURITY.md`
- Modify: `agents/README.md`
- Modify: `docs/implementation-rules.md`
- Modify: `docs/architecture/gateway-enrolment-lifecycle.md`
- Modify: `docs/operators/alpha-validation-trial.md`
- Modify: `docs/wiki/CLI-Guide.md`
- Modify: `docs/wiki/Home.md`
- Modify: `docs/wiki/Security-Model.md`
- Delete: `internal/types/bootstrap.go`
- Delete: `internal/mcp/sync.go`
- Delete: `internal/mcp/sync_test.go`
- Delete: `internal/mcp/sync_error_paths_test.go`
- Delete: `internal/mcp/sync_ratelimit_test.go`
- Delete: `internal/mcp/alpha_acceptance_sync_test.go`
- Delete: `internal/runtime/sync/sync.go`
- Delete: `internal/runtime/sync/sync_test.go`
- Delete: `internal/runtime/sync/sync_retained_test.go`
- Delete: `internal/runtime/sync/sync_apply_test.go`
- Delete: `internal/runtime/sync/sync_error_paths_test.go`
- Delete: `internal/runtime/sync/sync_latency_test.go`
- Delete: `internal/runtime/sync/bootstrap_validation_test.go`
- Delete: `internal/runtime/sync/bootstrap_validation_coverage_test.go`
- Delete: `internal/runtime/sync/integration_manifest_test.go`
- Delete: `internal/runtime/sync/alpha_acceptance_sync_test.go`
- Delete: `internal/runtime/localapi/bootstrap.go`
- Delete: `internal/runtime/localapi/bootstrap_test.go`
- Delete: `internal/runtime/localstore/bootstrap.go`
- Delete: `internal/runtime/localstore/bootstrap_test.go`
- Delete: `internal/runtime/localstore/bootstrap_coverage_test.go`
- Delete: `internal/core/identity/bootstrap_coverage_test.go`
- Delete: `internal/core/events/bootstrap_coverage_test.go`
- Delete: `internal/core/tasks/bootstrap_coverage_test.go`
- Delete: `cmd/gatewayd/mcp_client_test.go`

**Interfaces:**
- Consumes: Tasks 1-2 descriptor-only public contract and the single v2 status shell.
- Produces: an exact sixteen-tool live private registry, exact ten-tool non-live public contract, no production sync-v1 owner, truthful durable enrolment stop, preserved local status bytes, preserved Activity behavior, and updated active contract/documentation inventories.

- [ ] **Step 1: Add concrete RED assertions for the cut boundary**

Append to `internal/mcp/registry_test.go`:

```go
func TestFabricRegistryRetainsExactPrivateSixteen(t *testing.T) {
	want := []string{
		"wormhole.agent.enrol", "wormhole.agent.whoami",
		"wormhole.channel.create", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.git.link_commit", "wormhole.git.request_review",
		"wormhole.kb.get", "wormhole.kb.get_links", "wormhole.kb.search", "wormhole.kb.write",
		"wormhole.task.assign", "wormhole.task.create", "wormhole.task.list", "wormhole.task.update_status",
	}
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	got := make([]string, 0, len(registry.List()))
	for _, tool := range registry.List() {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("private Fabric tools = %q, want %q", got, want)
	}
	for _, descriptor := range PublicFabricToolDescriptors() {
		if _, live := registry.Get(descriptor.Name); live {
			t.Fatalf("public descriptor %q is live before production assembly", descriptor.Name)
		}
	}
}
```

Add `reflect` and `sort` to that test file's imports. Replace `TestHandleToolsList_AllToolsPresent` in `internal/mcp/jsonrpc_test.go` with:

```go
func TestHandleToolsList_AllPrivateToolsPresent(t *testing.T) {
	result := HandleToolsList(NewFabricRegistry(FabricRegistryDependencies{}))
	entries := result.(map[string]any)["tools"].([]toolListEntry)
	if len(entries) != 16 {
		t.Fatalf("got %d live private tools, want 16", len(entries))
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, "wormhole.sync.") || strings.HasPrefix(entry.Name, "wormhole.activity.") {
			t.Fatalf("unexpected public protocol registration %q", entry.Name)
		}
	}
}
```

Replace `TestStage2AlphaValidationDocumentsExactMCPInventories` in `cmd/wormhole/active_documentation_cutover_test.go` with:

```go
func TestActiveValidationDocumentsLiveAndDescriptorOnlyMCPInventories(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile("../../docs/testing/alpha-validation.md")
	if err != nil {
		t.Fatal(err)
	}
	wantGateway := []string{
		"wormhole.agent.list", "wormhole.agent.presence", "wormhole.agent.register",
		"wormhole.channel.create", "wormhole.channel.events", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.kb.get", "wormhole.kb.list", "wormhole.kb.write", "wormhole.sync.status",
		"wormhole.workspace.checkpoint", "wormhole.workspace.diff", "wormhole.workspace.import", "wormhole.workspace.stash", "wormhole.workspace.status",
	}
	wantPrivateFabric := []string{
		"wormhole.agent.enrol", "wormhole.agent.whoami",
		"wormhole.channel.create", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
		"wormhole.git.link_commit", "wormhole.git.request_review",
		"wormhole.kb.get", "wormhole.kb.get_links", "wormhole.kb.search", "wormhole.kb.write",
		"wormhole.task.assign", "wormhole.task.create", "wormhole.task.list", "wormhole.task.update_status",
	}
	wantPublicContract := []string{
		"wormhole.activity.accept", "wormhole.activity.lifecycle", "wormhole.activity.presence", "wormhole.activity.pull",
		"wormhole.sync.attach", "wormhole.sync.bootstrap", "wormhole.sync.conflict", "wormhole.sync.issue_agent_session",
		"wormhole.sync.pull", "wormhole.sync.push",
	}
	for _, inventory := range []struct {
		heading string
		want    []string
	}{
		{heading: "### Gateway MCP (17 live tools)", want: wantGateway},
		{heading: "### Fabric private MCP (16 live tools)", want: wantPrivateFabric},
		{heading: "### Fabric public contract (10 descriptor-only tools)", want: wantPublicContract},
	} {
		got := markdownToolInventory(t, string(content), inventory.heading)
		sort.Strings(got)
		sort.Strings(inventory.want)
		if strings.Join(got, "\n") != strings.Join(inventory.want, "\n") {
			t.Errorf("%s inventory = %q, want exact %q", inventory.heading, got, inventory.want)
		}
	}
}
```

- [ ] **Step 2: Run RED and prove the legacy owners are still present**

Run:

```bash
go test ./internal/mcp -run 'Test(FabricRegistryRetainsExactPrivateSixteen|HandleToolsList_AllPrivateToolsPresent)' -count=1
go test ./cmd/wormhole -run TestActiveValidationDocumentsLiveAndDescriptorOnlyMCPInventories -count=1
test -n "$(rg -l 'BootstrapSchemaVersionV1|wormhole\.sync\.incremental_(pull|push)|wormhole\.sync\.conflict_report' internal cmd docs/contracts docs/testing/alpha-validation.md testdata/codegraph/benchmark-corpus.json)"
```

Expected: both Go commands FAIL because the registry/document still expose the old inventory; the final assertion exits 0 and prints legacy owner paths.

- [ ] **Step 3: Cut the live registry to the exact private sixteen and leave public values non-callable**

Replace `internal/mcp/fabric_registry.go` with this complete body. In particular, there is no limiter, public `Handler`, public `Registry.Register`, or Activity registration:

```go
package mcp

import (
	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/git"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/roles"
	"github.com/H4RL33/wormhole/internal/core/tasks"
)

// FabricRegistryDependencies contains the stores used by Fabric's private MCP
// surface. Nil stores are valid when callers only inspect descriptors.
type FabricRegistryDependencies struct {
	Identity             *identity.Store
	Events               *events.Store
	Tasks                *tasks.Store
	Git                  *git.Store
	KB                   *kb.Store
	Roles                *roles.Store
	IntegrationManifests *IntegrationManifestStore
}

// NewFabricRegistry composes the exact private MCP registry served by Fabric.
// Public sync-v2 and Activity-v1 contracts are descriptor-only until their
// production assembler is delivered by a later slice.
func NewFabricRegistry(deps FabricRegistryDependencies) *Registry {
	registry := NewRegistry()
	register := func(tool Tool, resultExample any) {
		tool.ResultExamples = map[string]any{"default": resultExample}
		registry.Register(tool)
	}
	register(EnrolAgentTool(deps.Identity, deps.Events, deps.KB), EnrolAgentOutput{})
	register(WhoAmITool(), WhoAmIOutput{})
	register(CreateTaskTool(deps.Tasks), CreateTaskOutput{})
	register(AssignTaskTool(deps.Tasks), AssignTaskOutput{})
	register(ListTasksTool(deps.Tasks, deps.Roles), ListTasksOutput{})
	register(UpdateTaskStatusTool(deps.Tasks), UpdateTaskStatusOutput{})
	register(CreateChannelTool(deps.Events), CreateChannelOutput{})
	register(PostEventTool(deps.Events), PostEventOutput{})
	register(SubscribeChannelTool(deps.Events), SubscribeChannelOutput{})
	register(ListChannelsTool(deps.Events), ListChannelsOutput{})
	register(LinkCommitTool(deps.Git), LinkCommitOutput{})
	register(RequestReviewTool(deps.Git), RequestReviewOutput{})
	register(WriteArticleTool(deps.KB), WriteArticleOutput{})
	register(SearchArticlesTool(deps.KB), SearchArticlesOutput{})
	register(GetArticleTool(deps.KB), GetArticleOutput{})
	register(GetArticleLinksTool(deps.KB), GetArticleLinksOutput{})
	return registry
}
```

In `internal/mcp/registry.go`, replace only the `RequiredPermission` field's
comment with the exact retained-private contract; the field and every other
`Tool` member stay byte-for-byte unchanged:

```go
	// RequiredPermission is the fine-grained permission string a caller's
	// AuthenticatedScope must carry to invoke this live private tool. Empty is
	// reserved for the authenticated self-identification tool, whoami.
	// Descriptor-only public contracts do not use Tool or Handler.
	RequiredPermission string `json:"required_permission,omitempty"`
```

Make the mixed-test mutations exact. In `internal/mcp/registry_test.go`, replace
`TestRegistry_EveryAuthedToolDeclaresPermission` completely:

```go
func TestRegistry_EveryAuthedToolDeclaresPermission(t *testing.T) {
	exempt := map[string]bool{"wormhole.agent.whoami": true}
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	for _, tool := range registry.List() {
		if !tool.RequiresAuth {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: RequiresAuth=false but RequiredPermission=%q; unauthenticated tools cannot gate on a permission", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if exempt[tool.Name] {
			if tool.RequiredPermission != "" {
				t.Errorf("%s: exempt tool must have empty RequiredPermission, got %q", tool.Name, tool.RequiredPermission)
			}
			continue
		}
		if tool.RequiredPermission == "" {
			t.Errorf("%s: authenticated tool must declare a RequiredPermission", tool.Name)
		}
	}
}
```

In `internal/mcp/audit_test.go`, replace
`TestMCPAudit_ToolSurfaceCompleteness` completely:

```go
func TestMCPAudit_ToolSurfaceCompleteness(t *testing.T) {
	r := NewFabricRegistry(FabricRegistryDependencies{})
	expectedTools := map[string]bool{
		"wormhole.agent.enrol": false, "wormhole.agent.whoami": true,
		"wormhole.channel.create": true, "wormhole.channel.post": true,
		"wormhole.channel.subscribe": true, "wormhole.channel.list": true,
		"wormhole.task.create": true, "wormhole.task.assign": true,
		"wormhole.task.update_status": true, "wormhole.task.list": true,
		"wormhole.kb.search": true, "wormhole.kb.write": true,
		"wormhole.kb.get": true, "wormhole.kb.get_links": true,
		"wormhole.git.link_commit": true, "wormhole.git.request_review": true,
	}
	for name, requiresAuth := range expectedTools {
		tool, ok := r.Get(name)
		if !ok {
			t.Errorf("missing retained private tool: %s", name)
			continue
		}
		if tool.RequiresAuth != requiresAuth {
			t.Errorf("tool %s RequiresAuth: got %v, want %v", name, tool.RequiresAuth, requiresAuth)
		}
	}
	for _, tool := range r.List() {
		if _, ok := expectedTools[tool.Name]; !ok {
			t.Errorf("unexpected tool registered: %s", tool.Name)
		}
	}
}
```

Delete the complete declaration
`TestFabricManifestBootstrapAndIncrementalDistribution` from
`internal/mcp/integration_manifest_test.go` and remove that file's now-unused
`time` import. No helper used by another test is deleted. Replace
`TestToolHandlersRejectMalformedArgumentsAtContractBoundary`
in `internal/mcp/tool_contract_coverage_test.go` completely and remove the now
unused `time` import:

```go
func TestToolHandlersRejectMalformedArgumentsAtContractBoundary(t *testing.T) {
	tools := []Tool{
		EnrolAgentTool(nil, nil, nil),
		CreateTaskTool(nil), AssignTaskTool(nil), ListTasksTool(nil, nil), UpdateTaskStatusTool(nil),
		CreateChannelTool(nil), PostEventTool(nil), SubscribeChannelTool(nil),
		WriteArticleTool(nil), SearchArticlesTool(nil), GetArticleTool(nil), GetArticleLinksTool(nil),
		LinkCommitTool(nil), RequestReviewTool(nil),
	}
	for _, tool := range tools {
		t.Run(tool.Name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), nil, "project-a", json.RawMessage(`{"unterminated"`))
			if err == nil || !strings.Contains(err.Error(), "decode") {
				t.Fatalf("%s malformed arguments error = %v", tool.Name, err)
			}
		})
	}
}
```

Use the exact `internal/mcp/jsonrpc_test.go` replacement from Step 1. In
`internal/mcp/jsonrpc_toolscall_test.go`, replace only the stale comment above
`TestHandleToolsCall_ForwardsAuthResolvedProjectID` with:

```go
// TestHandleToolsCall_ForwardsAuthResolvedProjectID guards the private MCP
// boundary: a caller may omit the untrusted project_id comparison claim, but
// dispatch must still pass the project resolved from its bearer credential to
// the handler. Descriptor-only public tools do not use this private path.
```

Do **not** edit `internal/mcp/server_test.go`, `permission_enforcement_test.go`, `hardening_test.go`, `e2e_test.go`, or `jsonrpc_boundary_coverage_test.go`. They continue to assert the retained private tools' current errors. `toolFailureResult` and strict public parsing remain dormant public-contract primitives in this slice.

- [ ] **Step 4: Make the v2 shell the sole engine status receiver without changing one local byte**

Replace `internal/runtime/sync/status.go` with this complete body. `V2Engine.Status` stays solely in `engine_v2.go`; no receiver is duplicated here:

```go
package sync

import "errors"

// ConnectionState is the exact RFC-0003/Milestone-2 Gateway connection
// state vocabulary exposed through local MCP and the CLI.
type ConnectionState string

const (
	StateOnline            ConnectionState = "online"
	StateOffline           ConnectionState = "offline"
	StateSynchronizing     ConnectionState = "synchronizing"
	StateAttentionRequired ConnectionState = "attention_required"
)

// ErrFabricUnavailable classifies retryable transport/server availability
// failures. Durable queued work remains pending and Gateway remains usable.
var ErrFabricUnavailable = errors.New("sync: Fabric unavailable")

// ErrAttentionRequired classifies a non-transient synchronization failure
// that must not be silently retried as though it were ordinary connectivity.
var ErrAttentionRequired = errors.New("sync: attention required")

// Status is the frozen read-only local status response.
type Status struct {
	State         ConnectionState `json:"state"`
	PendingWrites int             `json:"pending_writes"`
}
```

The deletion of `sync.go` removes every caller of `setConnectionState` and `stateForSyncError`; therefore those legacy helpers, and the now-unused `fmt` and `net` imports, disappear with the receiver. Do not touch `internal/runtime/localapi/mcp.go`, `internal/runtime/localapi/mcp_test.go`, `internal/runtime/localapi/stage2_cutover_test.go`, `internal/runtime/localapi/supervisor_test.go`, `cmd/gatewayd/alpha_validation_e2e_test.go`, or `cmd/gatewayd/owner_lock_linux_test.go`: their exact offline/zero status and Activity-independent behavior are preserved and gated below.

- [ ] **Step 5: Replace the alpha contract's legacy protocol with exact private/public projections**

First create a deterministic candidate from the current fixture, then replace the tracked file with the reviewed candidate. This is a formatting operation, not hand editing:

```bash
jq --slurpfile public docs/contracts/public-fabric-descriptors.json '
  .mcp_tools.fabric |= map(select(.name | startswith("wormhole.sync.") | not))
  | .mcp_tools.public_fabric_contract = $public[0].descriptors
  | .sync_protocol = {
      "version": 2,
      "activity_version": 1,
      "public_descriptor_only": true,
      "tools_call_fields": ["name","arguments","proof"],
      "safe_error_fields": ["code","operation"],
      "public_schema_definitions": $public[0].definitions
    }
' docs/contracts/alpha-contract.json > docs/contracts/alpha-contract.json.next
cmp -s docs/contracts/alpha-contract.json docs/contracts/alpha-contract.json.next && {
  echo 'contract projection unexpectedly unchanged' >&2
  exit 1
}
mv docs/contracts/alpha-contract.json.next docs/contracts/alpha-contract.json
```

Keep `alphaFabricMCPTool`, `alphaResponse`, `alphaSchema`, `alphaSchemaProperty`, `TestAlphaContractMCPRegistry`, `fabricMCPContract`, `readAlphaMCPContract`, and all schema snapshot helpers unchanged; they now compare the live sixteen against the pruned fixture. Remove `alphaSyncProtocol`, `alphaWireType`, `TestAlphaContractFabricSyncProtocol`, and `jsonFieldNames`. Change only the two owner structs and add the public test below:

```go
type alphaMCPContract struct {
	Mode         string                  `json:"mode"`
	MCPTools     alphaMCPInventories     `json:"mcp_tools"`
	SyncProtocol alphaPublicSyncProtocol `json:"sync_protocol"`
}

type alphaMCPInventories struct {
	Fabric               []alphaFabricMCPTool `json:"fabric"`
	PublicFabricContract []ToolDescriptor     `json:"public_fabric_contract"`
}

type alphaPublicSyncProtocol struct {
	PublicSchemaDefinitions map[string]map[string]any `json:"public_schema_definitions"`
}

func TestAlphaContractMatchesDescriptorOnlyPublicFabricTools(t *testing.T) {
	manifest := readAlphaMCPContract(t)
	golden := readPublicDescriptorGolden(t)
	if !reflect.DeepEqual(manifest.MCPTools.PublicFabricContract, golden.Descriptors) ||
		!reflect.DeepEqual(manifest.SyncProtocol.PublicSchemaDefinitions, golden.Definitions) {
		t.Fatal("alpha public projection differs from checked-in descriptor authority")
	}
	want := make([]ToolDescriptor, len(golden.Descriptors))
	copy(want, golden.Descriptors)
	for index := range want {
		want[index].InputSchema = expandGoldenSchema(t, golden.Definitions, want[index].InputSchema).(map[string]any)
		want[index].OutputSchema = expandGoldenSchema(t, golden.Definitions, want[index].OutputSchema).(map[string]any)
	}
	actual := PublicFabricToolDescriptors()
	for _, descriptor := range actual {
		if _, live := NewFabricRegistry(FabricRegistryDependencies{}).Get(descriptor.Name); live {
			t.Fatalf("public contract %q is live", descriptor.Name)
		}
	}
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actualJSON, wantJSON) {
		t.Fatalf("live public descriptors differ from alpha golden\ngot:  %s\nwant: %s", actualJSON, wantJSON)
	}
}
```

Add `bytes`; retain `encoding/json`, `os`, `reflect`, `sort`,
`strings`, and `testing` because the private and complete public schema
projections use them. Remove only imports made unused by the deleted v1 sync
projection.

Replace `internal/runtime/sync/contract_manifest_test.go` completely:

```go
package sync

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

type alphaSyncContract struct {
	SyncProtocol struct {
		Version              int      `json:"version"`
		ActivityVersion      int      `json:"activity_version"`
		PublicDescriptorOnly bool     `json:"public_descriptor_only"`
		ToolsCallFields      []string `json:"tools_call_fields"`
		SafeErrorFields      []string `json:"safe_error_fields"`
	} `json:"sync_protocol"`
}

func TestAlphaContractOwnsSharedV2RecordBoundary(t *testing.T) {
	data, err := os.ReadFile("../../../docs/contracts/alpha-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest alphaSyncContract
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SyncProtocol.Version != projectstate.SyncProtocolVersionV2 ||
		manifest.SyncProtocol.ActivityVersion != 1 ||
		!manifest.SyncProtocol.PublicDescriptorOnly {
		t.Fatalf("sync protocol boundary = %+v", manifest.SyncProtocol)
	}
	wantCall := []string{"name", "arguments", "proof"}
	wantError := []string{"code", "operation"}
	if !equalStrings(manifest.SyncProtocol.ToolsCallFields, wantCall) ||
		!equalStrings(manifest.SyncProtocol.SafeErrorFields, wantError) {
		t.Fatalf("envelope=%q error=%q", manifest.SyncProtocol.ToolsCallFields, manifest.SyncProtocol.SafeErrorFields)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
```

This runtime test imports shared records only. It deliberately does not import `internal/mcp`.

- [ ] **Step 6: Stop enrolment truthfully at its durable credential checkpoint and remove receiver/shutdown callers**

In `internal/runtime/localapi/localapi.go`, apply these exact structural edits:

```go
// Server: retain these fields exactly.
qr                  *syncpkg.QueueRepo
statusProvider      SyncStatusProvider
integrationGuidance IntegrationGuidanceProvider
integrationService  *IntegrationManifestService

// Server: delete these fields completely.
// integrationReceiver syncpkg.IntegrationManifestReceiver
// enrolmentBootstrapMu sync.Mutex
// enrolmentBootstrapEnabled bool
// enrolmentSyncConfig syncpkg.Config
// enrolmentSyncEngines map[string]*syncpkg.Engine

// Replace the service setter with this complete body and comment.
// SetIntegrationManifestService binds one authoritative service to the
// read-only MCP guidance and local integration-management surfaces.
func (s *Server) SetIntegrationManifestService(service *IntegrationManifestService) {
	if service == nil {
		return
	}
	s.integrationGuidance = service
	s.integrationService = service
}
```

In `(*Server).Close`, retain connection cancellation and `s.handlerWG.Wait()` exactly, and delete only `s.stopEnrolmentSyncEngines()`. `syncpkg` remains imported because queue and status types survive.

In `internal/runtime/localapi/enrolment.go`, replace the complete current
credential-profile read branch (from the `ReadCredentialProfile` call
through its non-`os.ErrNotExist` error return) with this exhaustive
branch:

```go
if credentials, readErr := runtimeconfig.ReadCredentialProfile(s.credentialsDir, req.CredentialProfile); readErr == nil {
	validCredential := attempt.AgentID != "" && attempt.PassportID != "" &&
		credentials.Server == req.FabricAddress && credentials.ProjectID == req.ProjectID &&
		credentials.AgentID == attempt.AgentID && credentials.PassportID == attempt.PassportID &&
		credentials.Token != ""
	if validCredential {
		switch attempt.State {
		case string(EnrolmentReady):
			// Ready is a historical terminal result; never move it backwards.
			return enrolmentReady(req, attempt.AgentID, attempt.PassportID)
		case string(EnrolmentCredentialsPersisted):
			return enrolmentPersisted(req, attempt.AgentID, attempt.PassportID)
		case string(EnrolmentBootstrapInProgress), string(EnrolmentRecoveryRequired):
			// A valid matching credential proves that the deleted bootstrap
			// continuation had already crossed the durable Slice-1 boundary.
			if err := s.store.UpdateEnrolmentAttempt(ctx, attempt,
				string(EnrolmentCredentialsPersisted), attempt.AgentID, attempt.PassportID, false); err != nil {
				return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
					"Gateway could not durably checkpoint the credential boundary.", attempt.AgentID, attempt.PassportID)
			}
			return enrolmentPersisted(req, attempt.AgentID, attempt.PassportID)
		default:
			if err := s.store.UpdateEnrolmentAttempt(ctx, attempt,
				string(EnrolmentCredentialsPersisted), attempt.AgentID, attempt.PassportID, false); err != nil {
				return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
					"Gateway could not durably checkpoint the credential boundary.", attempt.AgentID, attempt.PassportID)
			}
			return enrolmentPersisted(req, attempt.AgentID, attempt.PassportID)
		}
	}
	_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentFailed), attempt.AgentID, attempt.PassportID, true)
	return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
		"The selected credential profile belongs to a different enrolment; choose another profile.", attempt.AgentID, attempt.PassportID)
} else if !errors.Is(readErr, os.ErrNotExist) {
	_ = s.store.UpdateEnrolmentAttempt(ctx, attempt, string(EnrolmentFailed), attempt.AgentID, attempt.PassportID, true)
	return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
		"The existing credential profile could not be read safely.", attempt.AgentID, attempt.PassportID)
}
```

In the token-recovered-from-profile branch after Fabric replay, replace only
the matching-credential arm with:

```go
if credentials.Server == req.FabricAddress && credentials.ProjectID == req.ProjectID &&
	credentials.AgentID == attempt.AgentID && credentials.PassportID == attempt.PassportID && credentials.Token != "" {
	if err := s.store.UpdateEnrolmentAttempt(ctx, attempt,
		string(EnrolmentCredentialsPersisted), attempt.AgentID, attempt.PassportID, false); err != nil {
		return enrolmentFailure(req, EnrolmentCredentialPersistenceFailed,
			"Gateway could not durably checkpoint the credential boundary.", attempt.AgentID, attempt.PassportID)
	}
	return enrolmentPersisted(req, attempt.AgentID, attempt.PassportID)
}
```

The missing/invalid credential path remains exactly on the existing replay plus
controlled-`Reissue` path. After a newly issued token is atomically
written, retain the checked `UpdateEnrolmentAttempt(...,
credentials_persisted, ...)` call and replace only the deleted bootstrap
continuation with:

```go
return enrolmentPersisted(req, attempt.AgentID, attempt.PassportID)
```

Keep `EnrolmentBootstrapInProgress`, `EnrolmentReady`, their
result codes, validation, and historical transition tests as durable replay
compatibility. Update `handleEnrolmentContract`'s comment to “Slice 1
stops at credentials_persisted; v2 bootstrap assembly resumes it later.”
Retain the credential failure, restart/controlled-reissue, concurrency,
redaction, and transition suites. Add these complete regressions beside the
existing retained-boundary test:

```go
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
```

In `internal/runtime/localapi/manifest.go`, delete the complete
`ReceiveIntegrationManifest` and `RollbackBootstrapIntegrationManifest`
methods (the block immediately before `rawIntegrationManifestChange`). Keep
`ReceiveFabricChange` and all authoritative local guidance, verification,
planning, apply, rollback-journal, and drift behavior.

In `manifest_test.go`, remove the `syncpkg` import, the complete
`recordingManifestReceiverConfigurer` type and method, and
`TestBootstrapRollbackMakesExactCachedTupleReofferable`. Replace
`TestIntegrationManifestServiceWiresGuidanceAndEnrolmentSyncReceiver`
completely with this survivor test:

```go
func TestIntegrationManifestServiceWiresGuidanceAndManagement(t *testing.T) {
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	server.SetIntegrationManifestService(service)
	if server.integrationGuidance != service || server.integrationService != service {
		t.Fatal("one manifest service was not wired to guidance and management")
	}
}
```

Keep every direct `ReceiveFabricChange` test and every human-reviewed
guidance/apply/rollback-journal/drift test.

In `internal/runtime/localapi/localapi_test.go`, delete only
`TestServerCloseStopsWorkerPublishedByAdmittedHandler`, remove the now-unused
`syncpkg` import, and retain
`TestServerCloseWaitsForNonCooperativeTrackedHandler` plus all
connection/handler shutdown tests. In `localapi_p5_test.go`, replace the
obsolete narrative with:

```go
// A later sync-v2 assembly may refresh this cache through a public handler;
// this test exercises only local multi-org routing.

// TestBootstrapLifecycle reserves durable enrolment-state compatibility for
// the later sync-v2 bootstrap assembler; Slice 1 ends at credentials_persisted.
```

Delete the old numbered comments claiming `wormhole.sync.bootstrap` or incremental tools are live. Do not delete the test solely because its historical function name contains “Bootstrap”; the production-absence gate below targets deleted symbols and live tool names, not generic enrolment terminology.

- [ ] **Step 7: Remove the Core snapshot facade, preserve portable stable-ID helpers, and close command callers**

Delete these methods as complete declaration blocks. Their owner files retain
their existing imports: every current import is still used by a surviving
method after these blocks disappear.

```text
internal/core/identity/identity.go:
  (*Store).BeginBootstrapSnapshotTx
  (*Store).ReadBootstrapIdentityInTx
  (*Store).BootstrapTimestampInTx
internal/core/events/events.go:
  (*Store).ListBootstrapInTx
internal/core/tasks/tasks.go:
  (*Store).ListBootstrapInTx
internal/core/kb/kb.go:
  (*Store).ListBootstrapInTx
```

Delete the `"begin bootstrap"` table row from `internal/core/identity/unavailable_database_coverage_test.go`. Retain `BeginProjectTx`, `EnsureBootstrapArticle`, `EnsureBootstrapArticleInTx`, `SetPreparedBootstrapEmbedding`, and `PreparedBootstrapEmbedding`: the latter names describe KB onboarding and are not remote sync-v1 owners. Change only v1-specific stable-ID comments in `events.go`, `tasks.go`, `kb.go`, and their tests to this exact neutral form:

```go
// The caller-supplied stable ID supports portable project-state import while
// preserving project scoping and replay safety.
```

Do not rename exported methods or alter SQL for that comment-only migration.

In `cmd/gatewayd/gatewayd.go`, delete exactly the complete declaration that
starts with:

```go
func newRoutedSyncEngine(ctx context.Context, store *localstore.Store, scope types.WorkspaceScope,
	credentials syncpkg.CredentialSource, cfg syncpkg.Config) (*syncpkg.Engine, error) {
	repositories, err := newRoutedSyncRepositories(store)
	if err != nil {
		return nil, err
	}
	return syncpkg.NewRouted(ctx, scope, repositories.routes, credentials, repositories.conflicts,
		repositories.queue, repositories.audit, localstore.NewTaskRepo(store.DB(), localstore.NewEventRepo(store.DB())),
		localstore.NewKBRepo(store.DB()), cfg)
}
```

Delete only the now-unused `github.com/H4RL33/wormhole/internal/types`
import. Retain `routedSyncRepositories` and
`newRoutedSyncRepositories` byte-for-byte; `internal/runtime/sync`
remains imported for queue/audit repository types.
`cmd/gatewayd/gatewayd_test.go` has no caller of the deleted constructor,
is unchanged, and is absent from this task's Files list, gofmt list, and staged
manifest. Its `TestGatewayWiresExactWorkspaceConflictGate` test continues
to cover the retained owner.

In `cmd/fabric/m3_integration_test.go`, delete `TestFabricHTTPSyncRoundTripPersistsReplayAndOwner` as one complete function and remove `github.com/google/uuid`; keep `TestM3_MCPSeededStateReflectedInDashboard` and all private Fabric assembly coverage.

In `cmd/gatewayd/p7_e2e_integration_test.go`, delete exactly these declarations:

```text
gatewayTestCredentialSource and its Read method
gatewayTaskOperation
gatewayStatefulFabric
TestP7_MultiRuntimeSync
```

Keep `gatewayQueueFixture`, `gatewayQueueOperation`,
`TestP7_LocalQueueDeliveryLifecycle`, `TestP7_LocalTaskPersistence`,
`TestP7_SyncQueueDurability`, the Task-4 binary/process tests, and their
fixtures.

After those exact declarations are deleted, remove `encoding/json`, `fmt`,
`net/http`, and `net/http/httptest` from
`cmd/gatewayd/p7_e2e_integration_test.go`; the retained queue and binary/process
tests still require `bytes`, `errors`, `reflect`, `strings`, `syncpkg`,
`types`, and `projectstate`.

- [ ] **Step 8: Delete every v1 owner only after all callers are migrated**

Run this exact deletion from the repository root. These paths are the exhaustive owner/test list from reconnaissance; no glob is used:

```bash
git rm \
  internal/types/bootstrap.go \
  internal/mcp/sync.go \
  internal/mcp/sync_test.go \
  internal/mcp/sync_error_paths_test.go \
  internal/mcp/sync_ratelimit_test.go \
  internal/mcp/alpha_acceptance_sync_test.go \
  internal/runtime/sync/sync.go \
  internal/runtime/sync/sync_test.go \
  internal/runtime/sync/sync_retained_test.go \
  internal/runtime/sync/sync_apply_test.go \
  internal/runtime/sync/sync_error_paths_test.go \
  internal/runtime/sync/sync_latency_test.go \
  internal/runtime/sync/bootstrap_validation_test.go \
  internal/runtime/sync/bootstrap_validation_coverage_test.go \
  internal/runtime/sync/integration_manifest_test.go \
  internal/runtime/sync/alpha_acceptance_sync_test.go \
  internal/runtime/localapi/bootstrap.go \
  internal/runtime/localapi/bootstrap_test.go \
  internal/runtime/localstore/bootstrap.go \
  internal/runtime/localstore/bootstrap_test.go \
  internal/runtime/localstore/bootstrap_coverage_test.go \
  internal/core/identity/bootstrap_coverage_test.go \
  internal/core/events/bootstrap_coverage_test.go \
  internal/core/tasks/bootstrap_coverage_test.go \
  cmd/gatewayd/mcp_client_test.go
```

Immediately run this pure compile gate:

```bash
go test ./... -run '^$' -count=1
```

Expected: exit 0. Every caller migration is already specified in Steps 5-7;
there is no compile-contingent edit after owner deletion.

- [ ] **Step 9: Rewrite every active inventory, compatibility statement, and Code Graph fixture to the exact cut boundary**

Apply these exact active-document replacements. They deliberately distinguish
the sixteen callable private tools from the ten descriptor-only public values.

```diff
*** Begin Patch
*** Update File: docs/architecture/gateway-enrolment-lifecycle.md
@@
-Nothing below is a current user procedure, CLI promise, service bootstrap path,
-or Stage 2 acceptance dependency. The optional Fabric binary retains a
-server-side enrolment descriptor in its separate 20-tool inventory, but no
-current production Gateway route connects a harness to this lifecycle.
+Nothing below is a current user procedure, CLI promise, service bootstrap path,
+or Stage 2 acceptance dependency. The optional Fabric binary has exactly 16
+live private tools, including server-side enrolment. Its ten public sync-v2 and
+Activity-v1 contracts are descriptor-only and non-callable in Slice 1; no
+current production Gateway route connects a harness to either surface.
@@
-requested
-  -> remote_identity_issued
-  -> credential_persisted
-  -> bootstrap_pending
-  -> ready
+requested
+  -> registration_in_progress
+  -> registered
+  -> credentials_persisted
@@
-Failures after credential persistence had to resume from the owner-private
-credential reference rather than return or recreate a token. Terminal binding
-or configuration errors remained failed until a human changed the plan.
+Slice 1 stops only after the matching owner-private credential and durable
+attempt both say `credentials_persisted`. A matching historical `ready`
+attempt remains terminal; matching `bootstrap_in_progress` and
+post-credential `recovery_required` survivors are atomically normalized
+to `credentials_persisted` without a remote call. Missing credentials
+remain on controlled reissue; no deleted bootstrap continuation runs.
*** Update File: docs/operators/alpha-validation-trial.md
@@
-optional Fabric server's exact 20-tool registry is tested separately and is not
-a participant harness endpoint.
+optional Fabric server's exact 16-tool live private registry is tested
+separately. Its ten public sync-v2 and Activity-v1 contracts are descriptor-only
+and non-callable in Slice 1; neither surface is a participant harness endpoint.
*** Update File: docs/wiki/CLI-Guide.md
@@
-| `fabric` | Optional PostgreSQL-backed 20-tool server; not a Stage 2 runtime dependency |
+| `fabric` | Optional PostgreSQL server with 16 live private tools and ten descriptor-only public contracts; not a Stage 2 runtime dependency |
@@
-The optional Fabric binary has a distinct exact 20-tool registry and is not a
-harness endpoint for this release.
+The optional Fabric binary has exactly 16 live private tools plus ten public
+sync-v2 and Activity-v1 descriptor values that are non-callable in Slice 1.
+Neither surface is a harness endpoint for this release.
*** Update File: docs/wiki/Home.md
@@
-Fabric is optional. Its PostgreSQL-backed 20-tool server surface is retained
-for separate non-Stage 2 testing; Gateway setup, normal local work, acceptance,
-restart, and clone equivalence do not require or contact it. Live Gateway tools
-do not expose Task, Git-link, semantic KB search, enrolment, or managed-guidance
-operations.
+Fabric is optional. Its PostgreSQL-backed live private registry has exactly 16
+tools; ten public sync-v2 and Activity-v1 contracts are descriptor-only and
+non-callable in Slice 1. Gateway setup, normal local work, acceptance, restart,
+and clone equivalence do not require or contact either surface. Live Gateway
+tools do not expose Task, Git-link, semantic KB search, enrolment, or
+managed-guidance operations.
*** Update File: docs/wiki/Security-Model.md
@@
-The optional Fabric binary has a separate authenticated 20-tool HTTP MCP
-inventory backed by PostgreSQL. Its tokens, RLS, semantic embedding provider,
-remote sync, enrolment, and permission model are server concerns. None is a
-claim about the live Stage 2 Gateway. Do not expose Fabric on a non-loopback
-network without authenticated HTTPS, secret handling, database isolation, and
-an explicit deployment review.
+The optional Fabric binary has a separate authenticated HTTP MCP boundary
+backed by PostgreSQL: exactly 16 private tools are live, while ten public
+sync-v2 and Activity-v1 contracts are descriptor-only and non-callable in
+Slice 1. Its tokens, RLS, semantic embedding provider, remote sync, enrolment,
+and permission model are server concerns. None is a claim about the live Stage
+2 Gateway. Do not expose Fabric on a non-loopback network without authenticated
+HTTPS, secret handling, database isolation, and an explicit deployment review.
*** End Patch
```

```diff
*** Begin Patch
*** Update File: README.md
@@
-The repository also builds an optional Fabric binary with a separately tested
-20-tool PostgreSQL-backed server inventory. That inventory is not attached to
-the Stage 2 Gateway, is not part of Stage 2 acceptance, and is not a direct
-harness endpoint. See [Automated alpha validation](docs/testing/alpha-validation.md).
+The repository also builds an optional Fabric binary with a separately tested
+16-tool private PostgreSQL-backed registry. A separate ten-tool public sync-v2
+and Activity-v1 contract is descriptor-only until production assembly; it has
+no callable handlers. Neither surface is attached to the Stage 2 Gateway, is
+part of Stage 2 acceptance, or is a direct harness endpoint. See
+[Automated alpha validation](docs/testing/alpha-validation.md).
*** Update File: SECURITY.md
@@
-The optional Fabric server has a separate 20-tool PostgreSQL-backed HTTP MCP
-inventory. Its PostgreSQL RLS, bearer-token and Passport authentication,
-credential hashing, server audit, and sync rate-limiting assumptions belong to
-that optional future deployment. They are not protections supplied by the live
-local-only Stage 2 Gateway, and Fabric is not a direct Stage 2 harness endpoint
-or acceptance authority.
+The optional Fabric server has a separate 16-tool private PostgreSQL-backed
+HTTP MCP registry. Its ten-tool public sync-v2 and Activity-v1 contract is
+descriptor-only and has no callable handler in this slice. PostgreSQL RLS,
+bearer-token and Passport authentication, credential hashing, and server audit
+belong to that optional deployment. They are not protections supplied by the
+live local-only Stage 2 Gateway, and Fabric is not a direct Stage 2 harness
+endpoint or acceptance authority.
*** Update File: agents/README.md
@@
-- Fabric remains an optional separately tested 20-tool PostgreSQL server. It is
-  not attached to the Stage 2 Gateway and is not a direct harness endpoint.
+- Fabric remains an optional separately tested PostgreSQL server. Its live
+  private registry has exactly 16 tools; its ten public sync-v2/Activity-v1
+  values are descriptor-only until production assembly. It is not attached to
+  the Stage 2 Gateway and is not a direct harness endpoint.
@@
-- `fabric`: optional 20-tool HTTP MCP server backed by PostgreSQL; not a Stage 2
-  Gateway dependency or acceptance authority.
+- `fabric`: optional HTTP MCP server backed by PostgreSQL, with 16 live private
+  tools and ten descriptor-only public contracts; not a Stage 2 Gateway
+  dependency or acceptance authority.
*** Update File: docs/implementation-rules.md
@@
-The repository also retains an optional 20-tool Fabric server and broader Core,
-sync, Code Graph, and integration-manifest packages. Those packages have their
-own rules below, but their presence does not make their tools or network paths a
-live Stage 2 Gateway feature. Governance is optional and must not leak into Core.
+The repository also retains an optional Fabric server whose live private
+registry has exactly 16 tools. Its ten public sync-v2 and Activity-v1 contracts
+are descriptor-only until production assembly. Broader Core, sync, Code Graph,
+and integration-manifest packages have their own rules below, but their presence
+does not make their tools or network paths a live Stage 2 Gateway feature.
+Governance is optional and must not leak into Core.
@@
-- M2: Naming grammar is `wormhole.<namespace-noun>.<verb>`. The exact 17-tool Gateway
-  inventory uses only `agent`, `channel`, `kb`, `sync.status`, and the Gateway-local
-  `workspace` status/diff/import/checkpoint/stash operations. The optional 20-tool
-  Fabric registry separately uses agent, channel, task, kb, git, and sync namespaces.
+- M2: Naming grammar is `wormhole.<namespace-noun>.<verb>`. The exact 17-tool Gateway
+  inventory uses only `agent`, `channel`, `kb`, `sync.status`, and the Gateway-local
+  `workspace` status/diff/import/checkpoint/stash operations. The optional Fabric
+  live private registry has exactly 16 agent, channel, task, kb, and git tools; its
+  exact ten sync-v2 and Activity-v1 public contracts are descriptor-only in Slice 1.
*** Update File: docs/contracts/README.md
@@
-`mcp_tools.gateway` and `mcp_tools.fabric` are separate authorities.
+`mcp_tools.gateway`, `mcp_tools.fabric`, and
+`mcp_tools.public_fabric_contract` are separate projections.
@@
-- Optional Fabric has exactly 20 authenticated HTTP descriptors backed by
-  PostgreSQL. It is not a direct Stage 2 harness endpoint.
+- Optional Fabric has exactly 16 live private HTTP descriptors backed by
+  PostgreSQL. It is not a direct Stage 2 harness endpoint.
+- The Fabric public contract has exactly ten descriptor-only sync-v2 and
+  Activity-v1 values. They contain no handler and are not callable in Slice 1.
@@
-The optional server inventory is not additive to Gateway. Server enrolment,
-whoami, semantic KB search, task mutation, Git-link mutation, remote bootstrap,
-and remote sync are not in the Stage 2 Gateway inventory. A descriptor is live
-only in the registry where it appears.
+The optional server projections are not additive to Gateway. Server enrolment,
+whoami, semantic KB search, task mutation, Git-link mutation, remote bootstrap,
+and remote sync are not in the Stage 2 Gateway inventory. A private descriptor
+is live only in `mcp_tools.fabric`; a public contract value is non-callable.
@@
-Fabric bootstrap and incremental records retain their strict server wire
-shapes, including integration offers/revocations. Optional Fabric semantic
-search retains generation-scoped ranking metadata and a structured degraded
-error with no lexical fallback. These are server contracts, not Stage 2
-Gateway claims.
+Public sync-v2 and Activity-v1 records retain strict closed descriptor shapes,
+proof beside arguments, and safe `{code,operation}` failure values. They remain
+non-callable until production assembly. Optional Fabric semantic search retains
+generation-scoped ranking metadata and a structured degraded error with no
+lexical fallback. These are server contracts, not Stage 2 Gateway claims.
*** Update File: docs/compatibility.md
@@
-The live Gateway is a closed local-only 17-tool registry. The optional 20-tool Fabric
-registry remains a separate PostgreSQL-backed server surface and is not attached
-to the Stage 2 Gateway. Server-only enrolment, identity lookup, semantic search,
-Task, Git-link, bootstrap, and remote sync names have no Gateway compatibility
-aliases. `wormhole.sync.status` is a truthful offline/zero-pending local read.
+The live Gateway is a closed local-only 17-tool registry. The optional Fabric
+surface has a 16-tool private live registry and a separate ten-tool
+descriptor-only public contract. Neither is attached to the Stage 2 Gateway.
+Server-only enrolment, identity lookup, semantic search, Task, Git-link,
+bootstrap, and remote sync names have no Gateway compatibility aliases.
+`wormhole.sync.status` is a truthful offline/zero-pending local read.
*** Update File: docs/rfcs/wormhole_rfc_local_runtime.md
@@
-- The implemented version-one `wormhole.sync.bootstrap`, incremental pull/push,
-  and conflict-report envelopes remain frozen compatibility inventory until an
-  approved delivery slice introduces and tests a new protocol version. New
-  semantics must not be smuggled into version one.
+- Sync v1 has been destructively removed. Sync-v2 and Activity-v1 public values
+  are strict descriptor-only contract data until their approved production
+  assembler lands; no compatibility decoder or placeholder handler exists.
*** End Patch
```

Replace `docs/mcp-protocol.md`'s complete `## Optional Fabric protocol`
section with:

```markdown
## Optional Fabric protocol

The optional Fabric binary has a separate 16-tool private authenticated HTTP
MCP registry backed by PostgreSQL. It retains server enrolment, identity,
semantic KB, task, and Git-link operations. That surface is server
design/coverage, not the live Stage 2 harness path and not a Stage 2 acceptance
dependency.

Slice 1 also publishes exactly ten non-callable public descriptor values: six
`wormhole.sync.*` v2 tools and four `wormhole.activity.*` v1 tools. Their
`tools/call` contract carries `proof` beside `name` and `arguments`; their
closed failure result contains only `code` and `operation`. They have no
`Handler`, are absent from the live private registry, and cannot be dispatched
until production assembly lands.

Fabric private descriptors and public contract values remain inventoried in
[`contracts/alpha-contract.json`](contracts/alpha-contract.json). Their
presence does not authorise adding those names to Gateway `tools/list`.
```

Replace `docs/testing/alpha-validation.md` from
`## Optional Fabric/PostgreSQL coverage` through the closing tool inventory
fence with this exact section:

````markdown
## Optional Fabric/PostgreSQL coverage

The optional Fabric binary retains a distinct authenticated HTTP MCP private
inventory of exactly 16 live tools. Fabric is not a direct harness endpoint and
this coverage is not a Stage 2 acceptance dependency.

### Fabric private MCP (16 live tools)

```text
wormhole.agent.enrol
wormhole.agent.whoami
wormhole.channel.create
wormhole.channel.list
wormhole.channel.post
wormhole.channel.subscribe
wormhole.git.link_commit
wormhole.git.request_review
wormhole.kb.get
wormhole.kb.get_links
wormhole.kb.search
wormhole.kb.write
wormhole.task.assign
wormhole.task.create
wormhole.task.list
wormhole.task.update_status
```

### Fabric public contract (10 descriptor-only tools)

```text
wormhole.activity.accept
wormhole.activity.lifecycle
wormhole.activity.presence
wormhole.activity.pull
wormhole.sync.attach
wormhole.sync.bootstrap
wormhole.sync.conflict
wormhole.sync.issue_agent_session
wormhole.sync.pull
wormhole.sync.push
```
````

Replace the complete executable-replacement table immediately below that
inventory with this exact table so no row names a deleted test:

```markdown
| Retired or separated assertion | Executable boundary replacements |
|---|---|
| stdio bridge, Unix socket, local Gateway writes, private attribution, restart, checkpoint and portable clone convergence | `TestStage2LocalOnlyRealProcessAcceptance`; `TestConfiguredPrivateRuntimeDerivesAttributionAndIsolatesChannelAndKBHandlers`; `TestStage2ConfiguredSyncStatusReportsQueueFreeLocalOnlyState` |
| Fabric HTTP MCP enrolment and retained Postgres pillar persistence | `TestM3_MCPSeededStateReflectedInDashboard`; `TestEnrolAgentTool_DurableReplayAndControlledReissue`; `TestEnrolAgentToolBootstrapFailureRollsBackAndRetryIsIdempotent` |
| Fabric sync-v1 snapshots, incremental transfer, and compatibility decoders | `TestFabricRegistryRetainsExactPrivateSixteen`; `TestPublicFabricToolDescriptorsAreExactSortedDescriptorValues`; `TestAlphaContractMatchesDescriptorOnlyPublicFabricTools`; `TestAlphaContractOwnsSharedV2RecordBoundary` |
| Gateway sync-v1 engine construction and cross-runtime convergence | `TestV2EngineStatusPreservesExactLocalWire`; `TestNilV2EngineStatusFailsWithoutPanic`; existing Activity transport, localstore, policy, lifecycle, and promotion suites |
| durable local queue, restart, and delivery bookkeeping | `TestP7_LocalQueueDeliveryLifecycle`; `TestP7_SyncQueueDurability`; the complete `internal/runtime/sync/queue_repo_test.go` suite |
| single-Gateway multi-workspace and project isolation | `TestCrossWorkspacePrivateContextResolvesSiblingExactly`; `TestConfiguredPrivateRuntimeDerivesAttributionAndIsolatesChannelAndKBHandlers`; `TestWorkspaceScopeMismatchIsRejected`; `TestSyncQueueCrossNamespaceRejection` |
| enrolment credential/bootstrap continuation | `TestRetainedEnrolmentNormalizesPostCredentialSurvivorsWithoutRemoteCall`; `TestRetainedEnrolmentPersistedReplayIsIdempotentWithoutRemoteCall`; `TestRetainedHistoricalReadyReplayRemainsTerminalWithoutRemoteCall`; `TestRetainedEnrolmentNormalizationPersistenceFailureIsSafe`; `TestEnrolAgentTool_DurableReplayAndControlledReissue`; credential failure, restart/reissue, replay, concurrency, redaction, and transition-contract tests |
| descriptor/protocol drift | `TestSyncV2MCPAliasesAreTypeIdentical`; `TestClosedSchemaHandlesAnonymousTimeBytesRawMapsInterfacesConstAndOneOf`; `TestPublicToolFailureResultHasExactCanonicalSafeBytes` |
| production public sync/Activity execution | Descriptor-only in Slice 1; later slices own callable handler, auth, persistence, and production-assembly acceptance |
| Fabric token, tenant, and vector isolation | `TestMCP_MultiTenantIsolation`; `TestAgentEnrolmentsRLSHidesProjectBWhileScopedToProjectA`; `TestRestrictedRoleRLSOperationMatrix`; `TestRestrictedRoleRejectsCrossProjectForeignReferences`; `TestRestrictedRoleKBVectorQueryCannotCrossProject` |
```

Directly below `# Task 22 Manual Alpha Validation — July 2026` in
`docs/testing/manual-alpha-validation-2026-07.md`, insert this historical label:

```markdown
> Historical evidence: this record measures the removed pre-alpha sync-v1
> implementation. Its bootstrap/pull/push counts are not current contracts and
> do not imply a compatibility path.
```

Replace the `gateway-sync-response-version` query object in
`testdata/codegraph/benchmark-corpus.json` deterministically:

```bash
jq '(.queries[] | select(.id == "gateway-sync-response-version")) = {
  "id":"public-sync-v2-contract-boundary",
  "question":"Where are public sync-v2 descriptors, closed schemas, strict proof carriage, and safe failures frozen without live dispatch?",
  "entry_symbols":["PublicFabricToolDescriptors","publicDescriptor","closedJSONSchemaForType","probeToolsCallName","decodeKnownPublicToolsCallParams","toolFailureResult"],
  "expected_files":["internal/mcp/sync_v2_contract.go","internal/mcp/jsonrpc.go"],
  "expected_relationship_path":[
    {"from":"PublicFabricToolDescriptors","relationship":"calls","to":"publicDescriptor"},
    {"from":"publicDescriptor","relationship":"calls","to":"closedJSONSchemaForType"}
  ],
  "expected_authoritative_edges":[
    "PublicFabricToolDescriptors calls publicDescriptor",
    "publicDescriptor calls closedJSONSchemaForType",
    "toolFailureResult calls isPublicFabricTool"
  ],
  "expected_heuristic_edges":[],
  "sufficiency_criteria":"Finds the ten descriptor-only public values, recursive closed schemas, two-phase tools/call identification, proof/bearer exclusion, and canonical safe-error encoder without claiming a live handler."
}' testdata/codegraph/benchmark-corpus.json > testdata/codegraph/benchmark-corpus.json.next
mv testdata/codegraph/benchmark-corpus.json.next testdata/codegraph/benchmark-corpus.json
```

In `docs/testing/code-graph-benchmarks.md`, insert this paragraph before the
measurement table and replace the old incomplete-outcome bullet as shown:

```markdown
The measured `gateway-sync-response-version` row below is historical evidence
for the removed pre-alpha v1 corpus. The active corpus now uses
`public-sync-v2-contract-boundary`; it must be measured in the next Code Graph
benchmark run and this document must not fabricate a result in advance.
```

```diff
-- `gateway-sync-response-version` missed relationship segment
-  `pushBatch calls decodeIncrementalPushResult` and authoritative edges
-  `Bootstrap calls validateBootstrapResult` and
-  `pushBatch calls decodeIncrementalPushResult`.
+- Historical `gateway-sync-response-version` missed its recorded v1
+  relationships. Those results remain evidence only and are not expectations
+  of the active `public-sync-v2-contract-boundary` corpus entry.
```

Finally, in
`cmd/wormhole/active_documentation_cutover_test.go`, retain the complete
inventory test from Task 3 Step 1 and make these exact requirement-list
replacements inside
`TestStage2ActiveDocumentationStatesLocalOnlyAcceptanceBoundary`:

```go
		"SECURITY.md": {
			"local-only Stage 2 Gateway", "exactly 17 agent-facing tools", "Git is the sole acceptance authority",
			"same-OS-user boundary", "selected human", "durable agent", "connection session",
			"optional Fabric", "16-tool", "descriptor-only", "does not return a raw token",
		},
		"docs/testing/alpha-validation.md": {
			"TestStage2LocalOnlyRealProcessAcceptance", "requires neither PostgreSQL nor Fabric",
			"real `wormhole mcp` stdio bridge", "Gateway MCP (17 tools)",
			"Fabric private MCP (16 live tools)", "Fabric public contract (10 descriptor-only tools)",
			"service-manager installation is covered separately",
		},
		"docs/contracts/README.md": {
			"exactly 17", "exactly 16", "exactly ten", "descriptor-only",
			"not in the Stage 2 Gateway inventory",
		},
		"docs/compatibility.md": {
			"17-tool", "16-tool private live registry", "ten-tool", "descriptor-only",
			"Git acceptance", "machine-private",
		},
```

Leave every unshown requirement entry and the complete file-walk/assertion
loop unchanged. In the same function's `docs/mcp-protocol.md` entry, append
`"16-tool private"` and `"ten non-callable public descriptor"`; in its
`agents/README.md` entry append `"exactly 16"` and `"descriptor-only"`; in
its `docs/implementation-rules.md` entry append `"exactly 16"` and
`"descriptor-only"`. These are exact string literals, not regular
expressions. Also add these exact active-document entries to the same
requirements map:

```go
"docs/architecture/gateway-enrolment-lifecycle.md": {
	"exactly 16", "ten public", "descriptor-only", "credentials_persisted",
	"bootstrap_in_progress", "recovery_required", "controlled reissue",
},
"docs/operators/alpha-validation-trial.md": {
	"16-tool live private registry", "ten public", "descriptor-only", "non-callable",
},
"docs/wiki/CLI-Guide.md": {
	"16 live private tools", "ten descriptor-only public contracts", "non-callable",
},
"docs/wiki/Home.md": {
	"exactly 16", "ten public", "descriptor-only", "non-callable",
},
"docs/wiki/Security-Model.md": {
	"exactly 16 private tools", "ten public", "descriptor-only", "non-callable",
},
```

- [ ] **Step 10: Finish exact mixed-source cleanup and run the focused post-cut tests**

Replace the stale stable-ID comments without changing any function signature
or SQL. Use these complete comments above the four production methods:

```go
// CreateWithID inserts a task under a caller-supplied stable ID. This supports
// portable project-state import while preserving project scoping and replay
// safety; ordinary task creation keeps using the server-generated ID path.
```

```go
// CreateChannelWithID inserts a channel under a caller-supplied stable ID.
// This supports portable project-state import while preserving project
// scoping and replay safety; ordinary channel creation keeps using the
// server-generated ID path.
```

```go
// PublishEventWithID inserts an event under a caller-supplied stable ID. This
// supports portable project-state import while preserving project scoping and
// replay safety; ordinary event publication keeps using the server-generated
// ID path.
```

```go
// WriteArticleWithID inserts an article under a caller-supplied stable ID.
// This supports portable project-state import while preserving project
// scoping and replay safety; ordinary article writes keep using the
// server-generated ID path.
```

Use this exact comment above each matching stable-ID test in
`internal/core/tasks/tasks_test.go`, both matching tests in
`internal/core/events/events_test.go`, and `internal/core/kb/kb_test.go`:

```go
// This test proves the caller-supplied stable ID remains queryable exactly,
// preserving portable import replay without depending on a retired transport.
```

Delete the entire v1-only `TestBootstrapLifecycle` placeholder function from
`internal/runtime/localapi/localapi_p5_test.go`; a logging placeholder is not
an executable retained contract. Keep the concrete comment replacement at the
earlier multi-org cache test:

```go
// A later sync-v2 assembly may refresh this cache through a public handler;
// this test exercises only local multi-org routing.
```

Run:

```bash
gofmt -w \
  internal/mcp/fabric_registry.go internal/mcp/registry.go \
  internal/mcp/registry_test.go internal/mcp/audit_test.go \
  internal/mcp/jsonrpc_test.go internal/mcp/integration_manifest_test.go \
  internal/mcp/tool_contract_coverage_test.go internal/mcp/contract_manifest_test.go \
  internal/mcp/jsonrpc_toolscall_test.go internal/runtime/sync/status.go \
  internal/runtime/sync/contract_manifest_test.go internal/runtime/localapi/localapi.go \
  internal/runtime/localapi/enrolment.go internal/runtime/localapi/enrolment_test.go \
  internal/runtime/localapi/manifest.go internal/runtime/localapi/manifest_test.go \
  internal/runtime/localapi/localapi_test.go internal/runtime/localapi/localapi_p5_test.go \
  internal/core/identity/identity.go internal/core/identity/unavailable_database_coverage_test.go \
  internal/core/events/events.go internal/core/events/events_test.go \
  internal/core/tasks/tasks.go internal/core/tasks/tasks_test.go \
  internal/core/kb/kb.go internal/core/kb/kb_test.go cmd/gatewayd/gatewayd.go \
  cmd/gatewayd/p7_e2e_integration_test.go cmd/fabric/m3_integration_test.go \
  cmd/wormhole/active_documentation_cutover_test.go

go test ./... -run '^$' -count=1
go test ./internal/mcp -run 'Test(FabricRegistryRetainsExactPrivateSixteen|HandleToolsList_AllPrivateToolsPresent|AlphaContract|PublicFabricToolDescriptors|PublicDescriptor|PublicToolFailure|ClosedSchema|ToolsCallParams|ProbeToolsCallName|DecodeKnownPublic|DecodePublicArguments)' -count=1
go test ./internal/runtime/sync ./internal/runtime/localapi ./cmd/gatewayd -run 'Test(V2EngineStatus|NilV2EngineStatus|Activity|Promotion|Status|RetainedEnrolment|IntegrationManifestServiceWiresGuidance|Run_FreshSupervisorTruthfulInventory)' -count=1
go test ./internal/types/projectstate ./internal/core/git ./internal/runtime/localstore ./internal/runtime/sync -run 'Activity|Promotion|Status' -count=1
go test ./cmd/wormhole -run 'Test(ActiveValidationDocumentsLiveAndDescriptorOnlyMCPInventories|Stage2ActiveDocumentationStatesLocalOnlyAcceptanceBoundary)' -count=1
```

Expected: the compile-only repository pass and every focused command exit 0;
the fresh Gateway still reports the existing binding-aware local status, the
direct v2 shell marshals exactly `{"state":"offline","pending_writes":0}`,
all Activity suites remain green, enrolment returns `credentials_persisted`,
the private registry is exactly sixteen, and public values are descriptor-only.

- [ ] **Step 11: Run fail-closed path, ownership, order, absence, and import-boundary gates**

Run the production absence scan with a positive allowlist. Immutable migration
12, dated manual evidence, stored benchmark results, and inert private schema
`bootstrap_metadata` are allowed; active Go, contracts, procedures, and active
fixtures are not. `rg` exit 1 means clean and any other non-zero status is an
error:

```bash
scan_roots='internal cmd README.md SECURITY.md agents/README.md docs/implementation-rules.md docs/contracts docs/mcp-protocol.md docs/compatibility.md docs/rfcs/wormhole_rfc_local_runtime.md docs/architecture docs/operators docs/wiki docs/testing/alpha-validation.md docs/testing/code-graph-benchmarks.md testdata/codegraph/benchmark-corpus.json'
forbidden='BootstrapSchemaVersionV1|Bootstrap(Project|Identity|Agent|Passport|Channel|Event|Task|Article)V1|BootstrapSnapshot|SyncProtocolVersion([[:space:]]|=)|IncrementalPull|IncrementalPush|ConflictReport|wormhole\.sync\.(incremental_pull|incremental_push|conflict_report)'
set +e
matches=$(rg -n "$forbidden" $scan_roots \
  --glob '!migrations/000012_sync_conflict_event_type.up.sql' \
  --glob '!docs/testing/manual-alpha-validation-2026-07.md' \
  --glob '!docs/testing/results/**' 2>&1)
status=$?
set -e
case "$status" in
  1) ;;
  0) printf '%s\n' "$matches" >&2; exit 1 ;;
  *) printf '%s\n' "$matches" >&2; exit "$status" ;;
esac

active_doc_roots='README.md SECURITY.md agents/README.md docs/implementation-rules.md docs/contracts docs/mcp-protocol.md docs/compatibility.md docs/rfcs/wormhole_rfc_local_runtime.md docs/architecture docs/operators docs/wiki docs/testing/alpha-validation.md'
old_inventory='20-tool|20 tool|exactly 20|Fabric MCP \(20'
set +e
matches=$(rg -n -i "$old_inventory" $active_doc_roots 2>&1)
status=$?
set -e
case "$status" in
  1) ;;
  0) printf '%s\n' "$matches" >&2; exit 1 ;;
  *) printf '%s\n' "$matches" >&2; exit "$status" ;;
esac

test "$(rg -l '^type V2Engine struct' internal/runtime/sync --glob '*.go' | wc -l | tr -d ' ')" = 1
test "$(rg -l '^func NewV2Engine\(\) \*V2Engine' internal/runtime/sync --glob '*.go' | wc -l | tr -d ' ')" = 1
test "$(rg -l '^func \(e \*V2Engine\) Status\(context\.Context\) \(Status, error\)' internal/runtime/sync --glob '*.go' | wc -l | tr -d ' ')" = 1
test "$(rg -l '^type CredentialSource interface' internal/runtime/sync --glob '*.go' | wc -l | tr -d ' ')" = 1
test "$(rg -l '^type FabricRouteSource interface' internal/runtime/sync --glob '*.go' | wc -l | tr -d ' ')" = 1

test ! -e internal/types/bootstrap.go
test ! -e internal/mcp/sync.go
test ! -e internal/runtime/sync/sync.go
test ! -e internal/runtime/localapi/bootstrap.go
test ! -e internal/runtime/localstore/bootstrap.go
test -e internal/types/projectstate/sync_protocol.go
test -e internal/mcp/sync_v2_contract.go
test -e internal/runtime/sync/contract_v2.go
test -e internal/runtime/sync/engine_v2.go

go list -json ./internal/runtime/... | jq -s -e '
  [.[] | .Imports[]? |
   select(. == "github.com/H4RL33/wormhole/internal/mcp" or
          startswith("github.com/H4RL33/wormhole/internal/core/"))]
  | length == 0'
go list -json ./internal/types/projectstate | jq -e '
  [.Imports[]? | select(startswith("github.com/H4RL33/wormhole/internal/") and
    . != "github.com/H4RL33/wormhole/internal/types")] | length == 0'

jq --slurpfile public docs/contracts/public-fabric-descriptors.json -e '
  ([.mcp_tools.fabric[].name] | length == 16 and length == (unique|length)) and
  ([.mcp_tools.public_fabric_contract[].name] | length == 10 and length == (unique|length)) and
  (.mcp_tools.public_fabric_contract == $public[0].descriptors) and
  (.sync_protocol.version == 2) and
  (.sync_protocol.activity_version == 1) and
  (.sync_protocol.public_descriptor_only == true) and
  (.sync_protocol.tools_call_fields == ["name","arguments","proof"]) and
  (.sync_protocol.safe_error_fields == ["code","operation"]) and
  (.sync_protocol.public_schema_definitions == $public[0].definitions) and
  ($public[0].definitions.File.properties.Data.contentEncoding == "base64") and
  (all($public[0].descriptors[];
    (.input_schema | has("$ref")) and
    (.output_schema.oneOf | length >= 1)))' docs/contracts/alpha-contract.json

git diff --check
```

Expected: every assertion exits 0, the absence scan prints nothing, the shared
owner is below MCP/runtime, the public inventory is ten unique descriptors,
the private inventory is sixteen unique live tools, and the replacement-owner
paths exist before deleted-owner paths are asserted absent.

- [ ] **Step 12: Run the third complete repository gate**

Run every command from the repository root and stop at the first failure:

```bash
go test ./internal/types/... ./internal/mcp ./internal/runtime/sync ./internal/runtime/localapi ./cmd/fabric ./cmd/gatewayd ./cmd/wormhole -count=1
go test ./... -count=1
go test -race ./internal/mcp ./internal/runtime/sync ./internal/runtime/localapi
go vet ./...
.github/scripts/check-contract-manifest.sh
make check
git diff --check
```

Expected: every command exits 0; `make check` reports merged statement coverage
at or above 80%; no command mutates the alpha contract; no deleted v1 package,
test, command caller, active document, or fixture is required to compile or
pass.

- [ ] **Step 13: Commit the atomic destructive cut and rerun the repository-wide gate against the commit**

Stage the exact create/modify/delete set owned by Tasks 1-3, inspect it, then
commit. The approved-spec clarification and this implementation plan are
controller documentation and must not enter this implementation commit:

```bash
git add \
  internal/types internal/mcp internal/runtime/sync internal/runtime/localapi \
  internal/runtime/localstore internal/core/identity internal/core/events \
  internal/core/tasks internal/core/kb cmd/gatewayd cmd/fabric cmd/wormhole \
  README.md SECURITY.md agents/README.md docs/implementation-rules.md \
  docs/contracts/alpha-contract.json docs/contracts/README.md \
  docs/mcp-protocol.md docs/compatibility.md \
  docs/rfcs/wormhole_rfc_local_runtime.md docs/testing/alpha-validation.md \
  docs/testing/manual-alpha-validation-2026-07.md \
  docs/testing/code-graph-benchmarks.md docs/architecture/gateway-enrolment-lifecycle.md \
  docs/operators/alpha-validation-trial.md docs/wiki/CLI-Guide.md docs/wiki/Home.md \
  docs/wiki/Security-Model.md testdata/codegraph/benchmark-corpus.json

expected_staged_paths=$(printf '%s\n' \
  README.md SECURITY.md agents/README.md \
  cmd/fabric/m3_integration_test.go cmd/gatewayd/gatewayd.go \
  cmd/gatewayd/mcp_client_test.go cmd/gatewayd/p7_e2e_integration_test.go \
  cmd/wormhole/active_documentation_cutover_test.go \
  docs/architecture/gateway-enrolment-lifecycle.md docs/compatibility.md \
  docs/contracts/README.md docs/contracts/alpha-contract.json \
  docs/implementation-rules.md docs/mcp-protocol.md \
  docs/operators/alpha-validation-trial.md \
  docs/rfcs/wormhole_rfc_local_runtime.md \
  docs/testing/alpha-validation.md docs/testing/code-graph-benchmarks.md \
  docs/testing/manual-alpha-validation-2026-07.md \
  docs/wiki/CLI-Guide.md docs/wiki/Home.md docs/wiki/Security-Model.md \
  internal/core/events/bootstrap_coverage_test.go internal/core/events/events.go \
  internal/core/events/events_test.go \
  internal/core/identity/bootstrap_coverage_test.go internal/core/identity/identity.go \
  internal/core/identity/unavailable_database_coverage_test.go \
  internal/core/kb/kb.go internal/core/kb/kb_test.go \
  internal/core/tasks/bootstrap_coverage_test.go internal/core/tasks/tasks.go \
  internal/core/tasks/tasks_test.go \
  internal/mcp/alpha_acceptance_sync_test.go internal/mcp/audit_test.go \
  internal/mcp/contract_manifest_test.go internal/mcp/fabric_registry.go \
  internal/mcp/integration_manifest_test.go internal/mcp/jsonrpc_test.go \
  internal/mcp/jsonrpc_toolscall_test.go internal/mcp/registry.go \
  internal/mcp/registry_test.go internal/mcp/sync.go \
  internal/mcp/sync_error_paths_test.go internal/mcp/sync_ratelimit_test.go \
  internal/mcp/sync_test.go internal/mcp/tool_contract_coverage_test.go \
  internal/runtime/localapi/bootstrap.go internal/runtime/localapi/bootstrap_test.go \
  internal/runtime/localapi/enrolment.go internal/runtime/localapi/enrolment_test.go \
  internal/runtime/localapi/localapi.go internal/runtime/localapi/localapi_p5_test.go \
  internal/runtime/localapi/localapi_test.go internal/runtime/localapi/manifest.go \
  internal/runtime/localapi/manifest_test.go \
  internal/runtime/localstore/bootstrap.go \
  internal/runtime/localstore/bootstrap_coverage_test.go \
  internal/runtime/localstore/bootstrap_test.go \
  internal/runtime/sync/alpha_acceptance_sync_test.go \
  internal/runtime/sync/bootstrap_validation_coverage_test.go \
  internal/runtime/sync/bootstrap_validation_test.go \
  internal/runtime/sync/contract_manifest_test.go \
  internal/runtime/sync/integration_manifest_test.go internal/runtime/sync/status.go \
  internal/runtime/sync/sync.go internal/runtime/sync/sync_apply_test.go \
  internal/runtime/sync/sync_error_paths_test.go internal/runtime/sync/sync_latency_test.go \
  internal/runtime/sync/sync_retained_test.go internal/runtime/sync/sync_test.go \
  internal/types/bootstrap.go testdata/codegraph/benchmark-corpus.json | LC_ALL=C sort)
actual_staged_paths=$(git diff --cached --name-only | LC_ALL=C sort)
if test "$actual_staged_paths" != "$expected_staged_paths"; then
  diff -u <(printf '%s\n' "$expected_staged_paths") <(printf '%s\n' "$actual_staged_paths")
  exit 1
fi
git diff --cached --check
git commit -m "feat(sync): remove v1 contract surface"
make check
```

Expected: the exact sorted staged manifest matches with no extra or missing path
and therefore contains no spec, plan, controller report, review artifact, or
unchanged `cmd/gatewayd/gatewayd_test.go`; the commit succeeds; the
post-commit repository-wide gate exits 0 against that exact third commit.

- [ ] **Step 14: Hand the exact implementation head to the controller and stop**

The implementer runs only these read-only assertions after the post-commit
gate. The three-commit assertion applies only to this initial delivery. The
implementer writes no self-review, artifact, package, push, or evidence commit:

```bash
DELIVERY_BASE=72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0
IMPLEMENTATION_HEAD=$(git rev-parse --verify HEAD)
printf '%s\n' "$IMPLEMENTATION_HEAD" | grep -Eq '^[0-9a-f]{40}$'
test "$IMPLEMENTATION_HEAD" != "$DELIVERY_BASE"
git merge-base --is-ancestor "$DELIVERY_BASE" "$IMPLEMENTATION_HEAD"
test "$(git rev-list --count "$DELIVERY_BASE..$IMPLEMENTATION_HEAD")" = 3
printf 'DELIVERY_BASE=%s\nIMPLEMENTATION_HEAD=%s\n' "$DELIVERY_BASE" "$IMPLEMENTATION_HEAD"
```

The controller records those two exact values in its own handoff artifact,
without taking them from shell ambient state:

```bash
handoff=.superpowers/sdd/task6-slice1-controller-handoff.env
printf '%s\n' \
  'DELIVERY_BASE=72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0' \
  "IMPLEMENTATION_HEAD=$(git rev-parse --verify HEAD)" > "$handoff"
test "$(wc -l < "$handoff" | tr -d '[:space:]')" = 2
test "$(sed -n '1p' "$handoff")" = 'DELIVERY_BASE=72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0'
grep -Eq '^IMPLEMENTATION_HEAD=[0-9a-f]{40}$' "$handoff"
```

The controller sends the exact parsed
`DELIVERY_BASE..IMPLEMENTATION_HEAD` range to a distinct reviewer. The
reviewer replaces the controller-owned review artifact with exactly five lines:

```text
DELIVERY_BASE=72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0
REVIEWED_HEAD=<forty lowercase hexadecimal characters>
CRITICAL=<non-negative decimal integer>
IMPORTANT=<non-negative decimal integer>
MINOR=<non-negative decimal integer>
```

The controller validates both artifacts without sourcing either one. Every
value below is parsed in this shell:

```bash
handoff=.superpowers/sdd/task6-slice1-controller-handoff.env
review=.superpowers/sdd/task6-slice1-controller-review.env

test "$(wc -l < "$handoff" | tr -d '[:space:]')" = 2
sed -n '1p' "$handoff" | grep -Eq '^DELIVERY_BASE=[0-9a-f]{40}$'
sed -n '2p' "$handoff" | grep -Eq '^IMPLEMENTATION_HEAD=[0-9a-f]{40}$'
HANDOFF_BASE=$(sed -n 's/^DELIVERY_BASE=//p' "$handoff")
IMPLEMENTATION_HEAD=$(sed -n 's/^IMPLEMENTATION_HEAD=//p' "$handoff")
test "$(printf '%s\n' "$HANDOFF_BASE" | wc -l | tr -d '[:space:]')" = 1
test "$(printf '%s\n' "$IMPLEMENTATION_HEAD" | wc -l | tr -d '[:space:]')" = 1
test "$HANDOFF_BASE" = 72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0

test "$(wc -l < "$review" | tr -d '[:space:]')" = 5
sed -n '1p' "$review" | grep -Eq '^DELIVERY_BASE=[0-9a-f]{40}$'
sed -n '2p' "$review" | grep -Eq '^REVIEWED_HEAD=[0-9a-f]{40}$'
sed -n '3p' "$review" | grep -Eq '^CRITICAL=[0-9]+$'
sed -n '4p' "$review" | grep -Eq '^IMPORTANT=[0-9]+$'
sed -n '5p' "$review" | grep -Eq '^MINOR=[0-9]+$'
REVIEW_BASE=$(sed -n 's/^DELIVERY_BASE=//p' "$review")
REVIEWED_HEAD=$(sed -n 's/^REVIEWED_HEAD=//p' "$review")
CRITICAL=$(sed -n 's/^CRITICAL=//p' "$review")
IMPORTANT=$(sed -n 's/^IMPORTANT=//p' "$review")
MINOR=$(sed -n 's/^MINOR=//p' "$review")
test "$REVIEW_BASE" = "$HANDOFF_BASE"
git merge-base --is-ancestor "$HANDOFF_BASE" "$IMPLEMENTATION_HEAD"
test "$(git rev-parse --verify HEAD)" = "$IMPLEMENTATION_HEAD"
test "$REVIEWED_HEAD" = "$IMPLEMENTATION_HEAD"
test "$CRITICAL" = 0
test "$IMPORTANT" = 0
```

The five anchored line expressions, exact line counts, and single-value
`sed` parses reject duplicate, missing, reordered, or malformed fields.
`MINOR` is recorded but does not block this approved boundary.

If Critical or Important findings require fixes, the implementer makes one or
more new buildable fix commits and reruns `make check` after each commit.
The initial-only three-commit assertion is not repeated. The controller then
updates the handoff atomically to the new current implementation head:

```bash
handoff=.superpowers/sdd/task6-slice1-controller-handoff.env
HANDOFF_BASE=$(sed -n 's/^DELIVERY_BASE=//p' "$handoff")
OLD_IMPLEMENTATION_HEAD=$(sed -n 's/^IMPLEMENTATION_HEAD=//p' "$handoff")
NEW_IMPLEMENTATION_HEAD=$(git rev-parse --verify HEAD)
test "$HANDOFF_BASE" = 72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0
test "$NEW_IMPLEMENTATION_HEAD" != "$OLD_IMPLEMENTATION_HEAD"
git merge-base --is-ancestor "$HANDOFF_BASE" "$NEW_IMPLEMENTATION_HEAD"
printf 'DELIVERY_BASE=%s\nIMPLEMENTATION_HEAD=%s\n' \
  "$HANDOFF_BASE" "$NEW_IMPLEMENTATION_HEAD" > "$handoff.next"
mv "$handoff.next" "$handoff"
```

The controller commissions a new distinct full-range review of exactly
`72d34d5a4fd90e5ead60c6727d7a450b02ad0eb0..NEW_IMPLEMENTATION_HEAD`;
review of only the fix delta is invalid. The reviewer replaces all five review
lines, and the controller reruns the complete two-artifact validation block
above. This loop repeats until the artifacts bind the current head to the full
original-base range with zero Critical and Important findings. The controller,
not the implementer, owns later evidence packaging and the sole eventual push
boundary.
