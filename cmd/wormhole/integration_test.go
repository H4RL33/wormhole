package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localapi"
)

const integrationTestDigest = "sha256:719a185b670128590f3522d2b34e2c213edf0305a114de3ca53661141826e054"

type fakeIntegrationBackend struct {
	plan        integrationCommandPlan
	planErr     error
	commitErr   error
	planCalls   int
	commitCalls int
	operation   string
	project     string
}

func (backend *fakeIntegrationBackend) Plan(_ context.Context, operation, project string) (integrationCommandPlan, error) {
	backend.planCalls++
	backend.operation, backend.project = operation, project
	if backend.planErr != nil {
		return integrationCommandPlan{}, backend.planErr
	}
	plan := backend.plan
	plan.Operation, plan.ProjectID = operation, project
	return plan, nil
}

func (backend *fakeIntegrationBackend) Commit(_ context.Context, _ integrationCommandPlan) (localapi.IntegrationState, error) {
	backend.commitCalls++
	return backend.plan.State, backend.commitErr
}

func TestIntegrationCLI_NoninteractiveMutationRequiresExactDigest(t *testing.T) {
	for _, test := range []struct {
		name        string
		args        []string
		wantExit    int
		wantCommits int
		wantError   string
	}{
		{name: "missing", args: []string{"apply", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"}, wantExit: 2, wantError: "requires --confirm-digest"},
		{name: "abbreviated", args: []string{"apply", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--confirm-digest", "sha256:719a"}, wantExit: 2, wantError: "full lowercase sha256 digest"},
		{name: "mismatch", args: []string{"apply", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--confirm-digest", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, wantExit: 1, wantError: "does not match expected digest"},
		{name: "matching", args: []string{"apply", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--confirm-digest", integrationTestDigest}, wantExit: 0, wantCommits: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := integrationTestBackend()
			var stdout, stderr bytes.Buffer
			exit := runIntegration(test.args, strings.NewReader(""), &stdout, &stderr, false, backend)
			if exit != test.wantExit || backend.commitCalls != test.wantCommits {
				t.Fatalf("exit/commits = %d/%d, want %d/%d; stderr=%q", exit, backend.commitCalls, test.wantExit, test.wantCommits, stderr.String())
			}
			if test.wantError != "" && !strings.Contains(stderr.String(), test.wantError) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.wantError)
			}
			if test.name == "matching" {
				for _, required := range []string{"operation=apply", "project=e724dd25-5bc9-40db-bcad-0b21716d1ca4", "role=contributor", "digest=" + integrationTestDigest, "--- a/AGENTS.md"} {
					if !strings.Contains(stdout.String(), required) {
						t.Errorf("mutation preview missing %q: %q", required, stdout.String())
					}
				}
			}
		})
	}
}

func TestIntegrationCLI_TTYConfirmationValues(t *testing.T) {
	for _, test := range []struct {
		answer      string
		wantExit    int
		wantCommits int
	}{
		{answer: "y\n", wantExit: 0, wantCommits: 1},
		{answer: "YeS\n", wantExit: 0, wantCommits: 1},
		{answer: strings.ToUpper(integrationTestDigest) + "\n", wantExit: 0, wantCommits: 1},
		{answer: "no\n", wantExit: 1},
		{answer: "\n", wantExit: 1},
	} {
		t.Run(strings.TrimSpace(test.answer), func(t *testing.T) {
			backend := integrationTestBackend()
			var stdout, stderr bytes.Buffer
			exit := runIntegration([]string{"update", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"}, strings.NewReader(test.answer), &stdout, &stderr, true, backend)
			if exit != test.wantExit || backend.commitCalls != test.wantCommits {
				t.Fatalf("exit/commits = %d/%d, want %d/%d; stdout=%q stderr=%q", exit, backend.commitCalls, test.wantExit, test.wantCommits, stdout.String(), stderr.String())
			}
		})
	}
}

func TestIntegrationCLI_PreviewAndStatusAreReadOnlyAndOfflineCapable(t *testing.T) {
	backend := integrationTestBackend()
	backend.plan.State.ConnectionState = "offline"
	for _, test := range []struct {
		args     []string
		contains string
	}{
		{args: []string{"preview", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"}, contains: "--- a/AGENTS.md"},
		{args: []string{"status", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"}, contains: "connection_state=offline"},
		{args: []string{"status", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--json"}, contains: `"connection_state": "offline"`},
	} {
		var stdout, stderr bytes.Buffer
		exit := runIntegration(test.args, strings.NewReader(""), &stdout, &stderr, false, backend)
		if exit != 0 || !strings.Contains(stdout.String(), test.contains) {
			t.Fatalf("runIntegration(%v) exit=%d stdout=%q stderr=%q", test.args, exit, stdout.String(), stderr.String())
		}
	}
	if backend.commitCalls != 0 {
		t.Fatalf("read-only commands committed %d times", backend.commitCalls)
	}
}

func TestIntegrationCLI_StatusIncludesGuidanceCompatibilityDriftAndRollback(t *testing.T) {
	backend := integrationTestBackend()
	rollbackVersion := int64(1)
	rollbackDigest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	backend.plan.State.GuidanceActive = false
	backend.plan.State.CompatibilityState = "tool_contract_mismatch"
	backend.plan.State.DriftDetected = true
	backend.plan.State.RollbackCandidateManifestVersion = &rollbackVersion
	backend.plan.State.RollbackCandidateManifestDigest = &rollbackDigest
	backend.plan.State.ApprovalState = "revoked"
	backend.plan.State.MaterializationState = "removal_required"
	backend.plan.State.ConnectionState = "attention_required"
	backend.plan.State.PreservedTargets = []string{"AGENTS.md"}
	var stdout, stderr bytes.Buffer
	if exit := runIntegration([]string{"status", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--json"}, strings.NewReader(""), &stdout, &stderr, false, backend); exit != 0 {
		t.Fatalf("JSON status exit=%d stderr=%q", exit, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"guidance_active": false, "compatibility_state": "tool_contract_mismatch", "drift_detected": true,
		"rollback_candidate_manifest_digest": rollbackDigest, "approval_state": "revoked",
		"materialization_state": "removal_required", "connection_state": "attention_required",
	} {
		if got := payload[key]; !reflect.DeepEqual(got, want) {
			t.Errorf("status %s = %#v, want %#v", key, got, want)
		}
	}
	stdout.Reset()
	if exit := runIntegration([]string{"status", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"}, strings.NewReader(""), &stdout, &stderr, false, backend); exit != 0 {
		t.Fatalf("human status exit=%d", exit)
	}
	for _, required := range []string{"guidance_active=false", "compatibility_state=tool_contract_mismatch", "drift_detected=true", "rollback_candidate_manifest_digest=" + rollbackDigest, "preserved_target=AGENTS.md"} {
		if !strings.Contains(stdout.String(), required) {
			t.Errorf("human status missing %q: %q", required, stdout.String())
		}
	}
}

func TestIntegrationCLI_ExactSurfaceAndMainDispatch(t *testing.T) {
	backend := integrationTestBackend()
	for _, args := range [][]string{
		{},
		{"unknown"},
		{"preview", "operand", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"},
		{"preview", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--json"},
		{"status", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--confirm-digest", integrationTestDigest},
		{"apply", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4", "--force"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := runIntegration(args, strings.NewReader(""), &stdout, &stderr, false, backend); exit != 2 {
			t.Fatalf("runIntegration(%v) exit = %d, want 2", args, exit)
		}
	}

	oldFactory := integrationBackendFactory
	integrationBackendFactory = func() (integrationCommandBackend, error) { return backend, nil }
	t.Cleanup(func() { integrationBackendFactory = oldFactory })
	var stdout, stderr bytes.Buffer
	if exit := run([]string{"integration", "preview", "--project", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("main integration dispatch exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestGatewayIntegrationBackendUsesPrivatePlanCommitSocketMethods(t *testing.T) {
	var calls []string
	backend := &gatewayIntegrationBackend{
		socketPath: "/tmp/wormholed-test.sock", repositoryRoot: "/repo",
		call: func(_ context.Context, _ string, method string, request any, response any) error {
			calls = append(calls, method)
			switch method {
			case "wormhole/integration/plan":
				out := response.(*integrationCommandPlan)
				*out = integrationTestBackend().plan
				out.Operation, out.ProjectID = "apply", "e724dd25-5bc9-40db-bcad-0b21716d1ca4"
			case "wormhole/integration/commit":
				out := response.(*localapi.IntegrationState)
				*out = integrationTestBackend().plan.State
			}
			return nil
		},
	}
	plan, err := backend.Plan(context.Background(), "apply", "e724dd25-5bc9-40db-bcad-0b21716d1ca4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Commit(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"wormhole/integration/plan", "wormhole/integration/commit"}) {
		t.Fatalf("private socket methods = %v", calls)
	}
}

func TestGatewayIntegrationBackendRejectsConfiguredProjectMismatch(t *testing.T) {
	backend := &gatewayIntegrationBackend{projectID: "e724dd25-5bc9-40db-bcad-0b21716d1ca4", call: func(context.Context, string, string, any, any) error {
		t.Fatal("mismatched project reached Gateway socket")
		return nil
	}}
	if _, err := backend.Plan(context.Background(), "status", "f724dd25-5bc9-40db-bcad-0b21716d1ca4"); err == nil {
		t.Fatal("backend accepted a project different from nearest repository config")
	}
}

func TestGatewayPrivateResponseDecoderIsClosed(t *testing.T) {
	type result struct {
		ProjectID string `json:"project_id"`
	}
	for _, raw := range []string{
		`{"project_id":"project-a","credential_profile":"caller-claim"}`,
		`{"project_id":"project-a"} {}`,
	} {
		var decoded result
		if err := decodeClosedGatewayJSON([]byte(raw), &decoded); err == nil {
			t.Fatalf("private response decoder accepted %s", raw)
		}
	}
}

func TestIntegrationStateFixture(t *testing.T) {
	data, err := os.ReadFile("../../testdata/alpha/manifests/integration-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var state localapi.IntegrationState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != 1 || state.ProjectID == "" || state.ActiveManifestDigest == nil ||
		state.ApprovalState != "approved" || state.MaterializationState != "applied" || len(state.Targets) != 6 {
		t.Fatalf("integration state fixture is incomplete: %+v", state)
	}
	targets := make([]string, len(state.Targets))
	for index, target := range state.Targets {
		targets[index] = target.Target
	}
	sorted := append([]string(nil), targets...)
	slices.Sort(sorted)
	if !reflect.DeepEqual(targets, sorted) {
		t.Fatalf("fixture targets not sorted: %v", targets)
	}
}

func integrationTestBackend() *fakeIntegrationBackend {
	activeID := "52b860cd-0db7-4ee0-a3fd-672ad9da0c95"
	activeVersion := int64(1)
	activeDigest := integrationTestDigest
	return &fakeIntegrationBackend{plan: integrationCommandPlan{
		ResolvedRole: "contributor", ExpectedDigest: integrationTestDigest,
		Diff: "--- a/AGENTS.md\n+++ b/AGENTS.md\n@@ -0,0 +1,1 @@\n+managed\n",
		State: localapi.IntegrationState{
			SchemaVersion: 1, ProjectID: "e724dd25-5bc9-40db-bcad-0b21716d1ca4",
			ActiveManifestID: &activeID, ActiveManifestVersion: &activeVersion, ActiveManifestDigest: &activeDigest,
			ResolvedRole: "contributor", ApprovalState: "approved", MaterializationState: "applied", ConnectionState: "online",
			GuidanceActive: true, CompatibilityState: "compatible",
		},
	}}
}
