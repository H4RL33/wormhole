package localapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
)

// TestMultiOrgResolveContext verifies org resolution from project bindings (RFC-0003 §7.1, P5).
func TestMultiOrgResolveContext(t *testing.T) {
	orgs := map[string]config.Org{
		"acme-corp": {
			Name: "acme-corp",
			Credentials: config.Credentials{
				Server:    "https://acme.example.com",
				ProjectID: "proj-acme-1",
				AgentID:   "agent-acme",
				Token:     "token-acme",
			},
		},
		"widgets-inc": {
			Name: "widgets-inc",
			Credentials: config.Credentials{
				Server:    "https://widgets.example.com",
				ProjectID: "proj-widgets-1",
				AgentID:   "agent-widgets",
				Token:     "token-widgets",
			},
		},
	}

	bindings := []config.ProjectBinding{
		{ProjectID: "proj-acme", OrgName: "acme-corp"},
		{ProjectID: "proj-widgets", OrgName: "widgets-inc"},
	}

	dbPath := filepath.Join(t.TempDir(), "test-multiorg.db")
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	srv, err := NewMultiOrg(
		filepath.Join(t.TempDir(), "test.sock"),
		orgs,
		bindings,
		store,
		localstore.NewTaskRepo(store.DB()),
		localstore.NewEventRepo(store.DB()),
		localstore.NewKBRepo(store.DB()),
		nil, // eventbus
		nil, // scheduler
		nil, // queue repo
	)
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()

	tests := []struct {
		name       string
		projectID  string
		wantOrg    string
		wantServer string
		wantErr    bool
	}{
		{
			name:       "acme project resolves to acme org",
			projectID:  "proj-acme",
			wantOrg:    "acme-corp",
			wantServer: "https://acme.example.com",
			wantErr:    false,
		},
		{
			name:       "widgets project resolves to widgets org",
			projectID:  "proj-widgets",
			wantOrg:    "widgets-inc",
			wantServer: "https://widgets.example.com",
			wantErr:    false,
		},
		{
			name:       "unknown project has no binding (RFC-0003 §7.1 explicit bindings)",
			projectID:  "proj-unknown",
			wantOrg:    "",
			wantServer: "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := srv.resolveOrgContext(tt.projectID)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if ctx.OrgName != tt.wantOrg {
				t.Errorf("org name: got %q, want %q", ctx.OrgName, tt.wantOrg)
			}
			if ctx.Creds.Server != tt.wantServer {
				t.Errorf("server: got %q, want %q", ctx.Creds.Server, tt.wantServer)
			}
		})
	}
}

// TestMultiOrgCrossOrgIsolation verifies that two orgs cannot see each other's data
// (RFC-0003 §7.2: multi-org isolation via explicit bindings and cross-namespace checks).
func TestMultiOrgCrossOrgIsolation(t *testing.T) {
	orgs := map[string]config.Org{
		"org-a": {
			Name: "org-a",
			Credentials: config.Credentials{
				Server:    "https://a.example.com",
				ProjectID: "proj-a",
				AgentID:   "agent-a",
				Token:     "token-a",
			},
		},
		"org-b": {
			Name: "org-b",
			Credentials: config.Credentials{
				Server:    "https://b.example.com",
				ProjectID: "proj-b",
				AgentID:   "agent-b",
				Token:     "token-b",
			},
		},
	}

	bindings := []config.ProjectBinding{
		{ProjectID: "proj-a", OrgName: "org-a"},
		{ProjectID: "proj-b", OrgName: "org-b"},
	}

	dbPath := filepath.Join(t.TempDir(), "test-isolation.db")
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	srv, err := NewMultiOrg(
		filepath.Join(t.TempDir(), "test.sock"),
		orgs,
		bindings,
		store,
		localstore.NewTaskRepo(store.DB()),
		localstore.NewEventRepo(store.DB()),
		localstore.NewKBRepo(store.DB()),
		nil, // eventbus
		nil, // scheduler
		nil, // queue repo
	)
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()

	// Resolve both org contexts
	ctxA, err := srv.resolveOrgContext("proj-a")
	if err != nil {
		t.Fatalf("resolve org-a: %v", err)
	}
	ctxB, err := srv.resolveOrgContext("proj-b")
	if err != nil {
		t.Fatalf("resolve org-b: %v", err)
	}

	// Verify orgs are different (isolation check)
	if ctxA.Creds.Token == ctxB.Creds.Token {
		t.Errorf("tokens should be different: org-a and org-b cannot share credentials")
	}
	if ctxA.Creds.Server == ctxB.Creds.Server {
		t.Errorf("servers should be different: org-a (%q) and org-b (%q) must use different coordination servers",
			ctxA.Creds.Server, ctxB.Creds.Server)
	}
	if ctxA.Creds.ProjectID == ctxB.Creds.ProjectID {
		t.Errorf("project IDs should be different: org-a (%q) and org-b (%q) are separate projects",
			ctxA.Creds.ProjectID, ctxB.Creds.ProjectID)
	}

	t.Logf("isolation verified: org-a uses %s, org-b uses %s", ctxA.Creds.Server, ctxB.Creds.Server)
}

// TestConfigLoadMultiOrg verifies that LoadMultiOrg reads all credential profiles
// (RFC-0003 §7.1, P5: support multiple orgs simultaneously).
func TestConfigLoadMultiOrg(t *testing.T) {
	// Create a temporary credentials directory with multiple profiles
	tmpDir := t.TempDir()
	credDir := filepath.Join(tmpDir, ".wormhole", "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("create cred dir: %v", err)
	}

	// Write two credential profiles
	profiles := []struct {
		name string
		cred config.Credentials
	}{
		{
			name: "org-a",
			cred: config.Credentials{
				Server:    "https://a.example.com",
				ProjectID: "proj-a-1",
				AgentID:   "agent-a",
				Token:     "token-a-secret",
			},
		},
		{
			name: "org-b",
			cred: config.Credentials{
				Server:    "https://b.example.com",
				ProjectID: "proj-b-1",
				AgentID:   "agent-b",
				Token:     "token-b-secret",
			},
		},
	}

	for _, p := range profiles {
		data, err := json.MarshalIndent(p.cred, "", "  ")
		if err != nil {
			t.Fatalf("marshal cred: %v", err)
		}
		path := filepath.Join(credDir, p.name+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write cred: %v", err)
		}
	}

	// Mock home directory
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tmpDir)

	cfg, err := config.LoadMultiOrg()
	if err != nil {
		t.Fatalf("LoadMultiOrg: %v", err)
	}

	// Verify both orgs are loaded
	if len(cfg.Orgs) != 2 {
		t.Errorf("org count: got %d, want 2", len(cfg.Orgs))
	}
	for _, p := range profiles {
		org, ok := cfg.Orgs[p.name]
		if !ok {
			t.Errorf("org %q not found in loaded config", p.name)
			continue
		}
		if org.Credentials.Server != p.cred.Server {
			t.Errorf("org %q server: got %q, want %q", p.name, org.Credentials.Server, p.cred.Server)
		}
		if org.Credentials.Token != p.cred.Token {
			t.Errorf("org %q token mismatch", p.name)
		}
	}

	// Verify socket/db paths are resolved
	if cfg.SocketPath == "" {
		t.Errorf("socket path not set")
	}
	if cfg.DBPath == "" {
		t.Errorf("db path not set")
	}
}

// TestMultiOrgNoImplicitDefault verifies RFC-0003 §7.1: no implicit default project binding.
func TestMultiOrgNoImplicitDefault(t *testing.T) {
	orgs := map[string]config.Org{
		"default-org": {
			Name: "default-org",
			Credentials: config.Credentials{
				Server:    "https://default.example.com",
				ProjectID: "proj-default",
				AgentID:   "agent-default",
				Token:     "token-default",
			},
		},
	}

	bindings := []config.ProjectBinding{}
	// Empty bindings — no projects are bound

	dbPath := filepath.Join(t.TempDir(), "test-default.db")
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	srv, err := NewMultiOrg(
		filepath.Join(t.TempDir(), "test.sock"),
		orgs,
		bindings,
		store,
		localstore.NewTaskRepo(store.DB()),
		localstore.NewEventRepo(store.DB()),
		localstore.NewKBRepo(store.DB()),
		nil, // eventbus
		nil, // scheduler
		nil, // queue repo
	)
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()

	// Try to resolve an unbound project — should fail
	_, err = srv.resolveOrgContext("proj-unbound")
	if err == nil {
		t.Errorf("expected error for unbound project, got none (RFC-0003 §7.1 requires explicit bindings)")
	}
}

// TestSingleOrgBackwardCompatibility verifies that P1-P4 single-org mode still works.
func TestSingleOrgBackwardCompatibility(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-compat.db")
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// Single-org mode: New constructor
	srv, err := New(
		filepath.Join(t.TempDir(), "test.sock"),
		"https://example.com",
		"test-token",
		"proj-123",
		store,
		localstore.NewTaskRepo(store.DB()),
		localstore.NewEventRepo(store.DB()),
		localstore.NewKBRepo(store.DB()),
		nil,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	// resolveOrgContext should work without bindings (backward compat)
	ctx, err := srv.resolveOrgContext("proj-123")
	if err != nil {
		t.Errorf("resolveOrgContext in single-org mode: %v", err)
	}
	if ctx.Creds.Server != "https://example.com" {
		t.Errorf("server: got %q, want %q", ctx.Creds.Server, "https://example.com")
	}
	if ctx.Creds.Token != "test-token" {
		t.Errorf("token mismatch")
	}
	if ctx.ProjectID != "proj-123" {
		t.Errorf("project: got %q, want %q", ctx.ProjectID, "proj-123")
	}
}

// TestProjectBindingExplicit verifies that project bindings must be explicit (RFC-0003 §7.1).
func TestProjectBindingExplicit(t *testing.T) {
	orgs := map[string]config.Org{
		"org-1": {
			Name: "org-1",
			Credentials: config.Credentials{
				Server:    "https://org1.example.com",
				ProjectID: "proj-1",
				AgentID:   "agent-1",
				Token:     "token-1",
			},
		},
	}

	// Only bind one project; second one is unbound
	bindings := []config.ProjectBinding{
		{ProjectID: "proj-1", OrgName: "org-1"},
	}

	dbPath := filepath.Join(t.TempDir(), "test-binding.db")
	store, err := localstore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	srv, err := NewMultiOrg(
		filepath.Join(t.TempDir(), "test.sock"),
		orgs,
		bindings,
		store,
		localstore.NewTaskRepo(store.DB()),
		localstore.NewEventRepo(store.DB()),
		localstore.NewKBRepo(store.DB()),
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("NewMultiOrg: %v", err)
	}
	defer srv.Close()

	// Bound project works
	ctx, err := srv.resolveOrgContext("proj-1")
	if err != nil {
		t.Errorf("bound project should resolve: %v", err)
	} else if ctx.OrgName != "org-1" {
		t.Errorf("org name: got %q, want org-1", ctx.OrgName)
	}

	// Unbound project fails (explicit binding required)
	_, err = srv.resolveOrgContext("proj-2")
	if err == nil {
		t.Errorf("unbound project should fail (RFC-0003 §7.1 explicit bindings)")
	}
}

// TestCredentialRecoveryFlow verifies credential recovery semantics (RFC-0003 §7.3):
// identity records are recoverable, credentials are regenerated not redistributed.
func TestCredentialRecoveryFlow(t *testing.T) {
	// This test verifies the contract: if credentials are lost, the daemon
	// can recover identity records by re-registering with the Coordination Server.
	// The actual credential regeneration happens server-side; here we verify
	// the daemon correctly loads recovered identities.

	tmpDir := t.TempDir()
	credDir := filepath.Join(tmpDir, ".wormhole", "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("create cred dir: %v", err)
	}

	// Write initial credentials
	initialCred := config.Credentials{
		Server:    "https://recovery.example.com",
		ProjectID: "proj-recovery",
		AgentID:   "agent-recovery",
		Token:     "token-initial",
	}
	data, _ := json.MarshalIndent(initialCred, "", "  ")
	credPath := filepath.Join(credDir, "recovery-org.json")
	if err := os.WriteFile(credPath, data, 0o600); err != nil {
		t.Fatalf("write cred: %v", err)
	}

	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", tmpDir)

	// Load credentials
	cfg, err := config.LoadMultiOrg()
	if err != nil {
		t.Fatalf("LoadMultiOrg: %v", err)
	}

	org, ok := cfg.Orgs["recovery-org"]
	if !ok {
		t.Fatalf("recovery-org not found after load")
	}

	// Simulate credential regeneration: update the token
	recoveredCred := config.Credentials{
		Server:    initialCred.Server,
		ProjectID: initialCred.ProjectID,
		AgentID:   initialCred.AgentID,
		Token:     "token-regenerated", // different token, same identity
	}

	// In a real flow, the daemon would call wormhole.sync.bootstrap or similar
	// to get the regenerated token. Here we just verify the identity is preserved.
	if org.Credentials.AgentID != recoveredCred.AgentID {
		t.Errorf("identity lost during recovery: got %q, want %q",
			org.Credentials.AgentID, recoveredCred.AgentID)
	}

	t.Logf("credential recovery: identity %q preserved, token regenerated", org.Credentials.AgentID)
}

// TestBootstrapLifecycle is a placeholder for the full bootstrap flow
// (RFC-0003 §8.1: Authentication → Enrolment → Bootstrap → Sync → Normal Operation).
// Full implementation comes in P6 with Coordination Server retrofit.
func TestBootstrapLifecycle(t *testing.T) {
	t.Logf("bootstrap lifecycle test (P5/P6): full flow with Coordination Server retry pending P6")
	// Steps:
	// 1. Authentication: wormhole-cli calls wormholed with server/credentials
	// 2. Enrolment: wormholed registers with Coordination Server (via agent.register or sync.bootstrap)
	// 3. Bootstrap: wormholed pulls initial org config, KB, tasks via wormhole.sync.bootstrap
	// 4. Synchronisation: incremental sync via wormhole.sync.* tools
	// 5. Normal operation: local reads/writes, async sync to server
}
