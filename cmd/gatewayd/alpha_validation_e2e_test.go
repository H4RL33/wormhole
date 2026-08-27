package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/core/events"
	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
	"github.com/H4RL33/wormhole/internal/core/tasks"
	"github.com/H4RL33/wormhole/internal/mcp"
	"github.com/H4RL33/wormhole/internal/runtime/localapi"
	"github.com/H4RL33/wormhole/internal/types"
)

type alphaValidationFixture struct {
	SchemaVersion int `json:"schema_version"`
	Project       struct {
		ID, Name, Owner string
	} `json:"project"`
	Publisher   alphaFixtureActor `json:"publisher"`
	Contributor alphaFixtureActor `json:"contributor"`
	Reviewer    alphaFixtureActor `json:"reviewer"`
	Task        struct {
		ID, Title, Description, Status string
		Priority                       int
	} `json:"task"`
	Channel struct {
		ID, Name string
	} `json:"channel"`
	SeedEvent struct {
		ID        string          `json:"id"`
		EventType string          `json:"event_type"`
		Payload   json.RawMessage `json:"payload"`
	} `json:"seed_event"`
	KBArticle struct {
		ID, Title, Body string
		Frontmatter     json.RawMessage `json:"frontmatter"`
	} `json:"kb_article"`
	Manifest struct {
		Path, ID, Digest string
		Version          int64
	} `json:"manifest"`
	RepositoryContext struct {
		ExpectedPath string `json:"expected_path"`
	} `json:"repository_context"`
	MeaningfulUpdate struct {
		EventType string `json:"event_type"`
		Note      string `json:"note"`
	} `json:"meaningful_update"`
	OfflineTask struct {
		Title, Description string
		Priority           int
	} `json:"offline_task"`
	Discovery struct {
		Title, Body string
		Frontmatter json.RawMessage
	} `json:"discovery"`
}

type alphaFixtureActor struct {
	Owner, Model string
	Capabilities []string
	Roles        []string
	Permissions  []string
}

type alphaCredential struct {
	Server     string `json:"server"`
	ProjectID  string `json:"project_id"`
	AgentID    string `json:"agent_id"`
	PassportID string `json:"passport_id"`
	Token      string `json:"token"`
}

// TestAlphaValidation_FullAutomatedAcceptanceLoop is the alpha release gate.
// It uses real gatewayd/fabric processes, Fabric HTTP, PostgreSQL, two
// independent SQLite replicas, two deterministic socket clients, human CLI
// approval, and a real indexed Wormhole checkout. Integration-required mode
// fails closed when Postgres is unavailable.
func TestAlphaValidation_FullAutomatedAcceptanceLoop(t *testing.T) {
	fixture := loadAlphaValidationFixture(t)
	db := e2eTestDB(t)
	seed := seedAlphaValidationFixture(t, db, fixture)

	wormholeBin := e2eBuildStdioBridgeBinary(t)
	if stdioBridgeBinErr != nil {
		t.Fatalf("build wormhole CLI: %v", stdioBridgeBinErr)
	}
	gatewayBin := task4BuildGatewayBinary(t)
	if task4GatewayBinErr != nil {
		t.Fatalf("build gatewayd: %v", task4GatewayBinErr)
	}
	fabricBin := task4BuildFabricBinary(t)
	if task4FabricBinErr != nil {
		t.Fatalf("build Fabric: %v", task4FabricBinErr)
	}
	fabric, fabricURL := startTask4FabricProcess(t, fabricBin, typesDatabaseURL())

	repoRoot := filepath.Clean(repoRootForTest(t))
	checkoutA := cloneAlphaWormhole(t, repoRoot, fixture.Project.ID, "contributor")
	checkoutB := cloneAlphaWormhole(t, repoRoot, fixture.Project.ID, "reviewer")
	commitSHA := alphaGitOutput(t, checkoutA, "rev-parse", "HEAD")
	remote := alphaGitOutput(t, checkoutA, "remote", "get-url", "origin")

	a := startAlphaGateway(t, gatewayBin, fabricURL, fixture.Project.ID, remote, "alpha-a", checkoutA, fixture.Contributor)
	b := startAlphaGateway(t, gatewayBin, fabricURL, fixture.Project.ID, remote, "alpha-b", checkoutB, fixture.Reviewer)
	if a.dbPath == b.dbPath {
		t.Fatalf("Gateways share SQLite path %q", a.dbPath)
	}
	alphaAssertIndependentFiles(t, a.dbPath, b.dbPath)

	applyAlphaManifest(t, wormholeBin, a, fixture.Manifest.Digest)
	applyAlphaManifest(t, wormholeBin, b, fixture.Manifest.Digest)
	a.restart(t, gatewayBin)
	b.restart(t, gatewayBin)
	defer a.closeClient()
	defer b.closeClient()

	t.Run("required Gateway operating-loop contracts are live", func(t *testing.T) {
		got := alphaListGatewayTools(t, a.client)
		want := []string{
			"wormhole.agent.enrol", "wormhole.agent.get_guidance", "wormhole.agent.list", "wormhole.agent.presence", "wormhole.agent.register", "wormhole.agent.whoami",
			"wormhole.channel.create", "wormhole.channel.events", "wormhole.channel.list", "wormhole.channel.post", "wormhole.channel.subscribe",
			"wormhole.git.link_commit",
			"wormhole.kb.get", "wormhole.kb.list", "wormhole.kb.search", "wormhole.kb.write", "wormhole.sync.status",
			"wormhole.task.create", "wormhole.task.get", "wormhole.task.list", "wormhole.task.route", "wormhole.task.update_status",
			"wormhole.workspace.checkpoint", "wormhole.workspace.diff", "wormhole.workspace.import", "wormhole.workspace.stash", "wormhole.workspace.status",
		}
		sort.Strings(want)
		if !equalAlphaStrings(got, want) {
			t.Fatalf("Gateway tools = %q, want exact 27 %q", got, want)
		}
	})

	alphaWaitOnline(t, a)
	alphaWaitOnline(t, b)
	assertAlphaGuidance(t, a.client, fixture.Project.ID, fixture.Manifest.Digest, "contributor")
	assertAlphaGuidance(t, b.client, fixture.Project.ID, fixture.Manifest.Digest, "reviewer")

	var meaningfulEventID, wipStatusEventID string
	t.Run("Agent A follows the operating loop", func(t *testing.T) {
		who := alphaMustGatewayCall(t, a.client, "wormhole.agent.whoami", map[string]interface{}{})
		alphaJSONFieldEquals(t, who, "agent_id", a.credential.AgentID)
		alphaJSONFieldEquals(t, who, "project_id", fixture.Project.ID)

		tasksRaw := alphaMustGatewayCall(t, a.client, "wormhole.task.list", map[string]interface{}{"project_id": fixture.Project.ID})
		if !alphaJSONArrayHas(t, tasksRaw, "tasks", "id", fixture.Task.ID) {
			t.Fatalf("Agent A task list lacks fixture task: %s", tasksRaw)
		}
		kbRaw := alphaMustGatewayCall(t, a.client, "wormhole.kb.list", map[string]interface{}{"project_id": fixture.Project.ID})
		if !alphaJSONArrayHas(t, kbRaw, "articles", "id", fixture.KBArticle.ID) {
			t.Fatalf("Agent A KB replica lacks fixture article: %s", kbRaw)
		}
		searchRaw := alphaMustGatewayCall(t, a.client, "wormhole.kb.search", map[string]interface{}{
			"project_id": fixture.Project.ID, "query": fixture.KBArticle.Title + "\n\n" + fixture.KBArticle.Body, "limit": 5,
		})
		if !alphaJSONArrayHas(t, searchRaw, "articles", "article_id", fixture.KBArticle.ID) {
			t.Fatalf("semantic search lacks fixture article: %s", searchRaw)
		}
		eventsRaw := alphaMustGatewayCall(t, a.client, "wormhole.channel.events", map[string]interface{}{"project_id": fixture.Project.ID})
		if !alphaJSONArrayHas(t, eventsRaw, "events", "id", fixture.SeedEvent.ID) {
			t.Fatalf("Agent A event inspection lacks seed event: %s", eventsRaw)
		}

		meaningfulEventID = alphaJSONID(t, alphaMustGatewayCall(t, a.client, "wormhole.channel.post", map[string]interface{}{
			"project_id": fixture.Project.ID, "channel_id": fixture.Channel.ID, "agent_id": a.credential.AgentID,
			"event_type": fixture.MeaningfulUpdate.EventType, "payload": map[string]interface{}{"task_id": fixture.Task.ID}, "note": fixture.MeaningfulUpdate.Note,
		}))
		alphaMustGatewayCall(t, a.client, "wormhole.task.update_status", map[string]interface{}{
			"project_id": fixture.Project.ID, "task_id": fixture.Task.ID, "new_status": "wip", "channel_id": fixture.Channel.ID,
		})
		wipStatusEventID = alphaSQLiteStatusEventID(t, a.dbPath, fixture.Task.ID, "todo", "wip", a.credential.AgentID)
		alphaMustGatewayCall(t, a.client, "wormhole.git.link_commit", map[string]interface{}{
			"project_id": fixture.Project.ID, "task_id": fixture.Task.ID, "repo": remote,
			"commit_sha": commitSHA, "summary": "Process path verified for reviewer handoff.",
		})
	})

	// Fabric goes away during work. Gateway A is restarted while offline, then
	// accepts the documented local-first Task/KB/Event writes.
	fabric.Stop(t)
	a.closeClient()
	a.daemon.Stop(t)
	a.restart(t, gatewayBin)
	discoveryBody := fixture.Discovery.Body + " Git commit: " + commitSHA + ". Task: " + fixture.Task.ID + "."
	offlineTask := alphaMustGatewayCall(t, a.client, "wormhole.task.create", map[string]interface{}{
		"project_id": fixture.Project.ID, "title": fixture.OfflineTask.Title,
		"description": fixture.OfflineTask.Description, "priority": fixture.OfflineTask.Priority,
	})
	offlineTaskID := alphaJSONID(t, offlineTask)
	offlineKB := alphaMustGatewayCall(t, a.client, "wormhole.kb.write", map[string]interface{}{
		"project_id": fixture.Project.ID, "agent_id": a.credential.AgentID, "title": fixture.Discovery.Title,
		"body": discoveryBody, "frontmatter": alphaDecodeObject(t, fixture.Discovery.Frontmatter),
	})
	offlineKBID := alphaJSONID(t, offlineKB)
	offlineEvent := alphaMustGatewayCall(t, a.client, "wormhole.channel.post", map[string]interface{}{
		"project_id": fixture.Project.ID, "channel_id": fixture.Channel.ID, "agent_id": a.credential.AgentID,
		"event_type": "discovery.logged", "payload": map[string]interface{}{
			"summary": fixture.Discovery.Body, "kb_article_id": offlineKBID, "agent_id": a.credential.AgentID,
			"task_id": fixture.Task.ID, "commit_sha": commitSHA, "code_path": fixture.RepositoryContext.ExpectedPath,
		},
	})
	offlineEventID := alphaJSONID(t, offlineEvent)
	alphaWaitPending(t, a, 3)

	fabric = startTask4FabricProcessAtURL(t, fabricBin, typesDatabaseURL(), fabricURL)
	_ = fabric
	alphaWaitPostgresCount(t, db, "tasks", offlineTaskID, 1)
	alphaWaitPostgresCount(t, db, "kb_articles", offlineKBID, 1)
	alphaWaitPostgresCount(t, db, "events", offlineEventID, 1)
	alphaWaitSQLiteCount(t, b.dbPath, "tasks", offlineTaskID, 1)
	alphaWaitSQLiteCount(t, b.dbPath, "kb_articles", offlineKBID, 1)
	alphaWaitSQLiteCount(t, b.dbPath, "events", offlineEventID, 1)
	alphaWaitSQLiteCount(t, b.dbPath, "events", meaningfulEventID, 1)
	alphaWaitOnline(t, a)
	alphaWaitOnline(t, b)

	alphaMustGatewayCall(t, a.client, "wormhole.task.update_status", map[string]interface{}{
		"project_id": fixture.Project.ID, "task_id": fixture.Task.ID, "new_status": "done", "channel_id": fixture.Channel.ID,
	})
	doneStatusEventID := alphaSQLiteStatusEventID(t, a.dbPath, fixture.Task.ID, "wip", "done", a.credential.AgentID)
	alphaWaitSQLiteTaskStatus(t, b.dbPath, fixture.Task.ID, "done")
	gitLinkID := alphaPostgresGitLinkID(t, db, fixture.Project.ID, fixture.Task.ID, commitSHA)
	alphaWaitPostgresCount(t, db, "git_links", gitLinkID, 1)
	alphaWaitSQLiteCount(t, b.dbPath, "git_links", gitLinkID, 1)
	reviewerGit := alphaSQLiteGitLink(t, b.dbPath, gitLinkID)
	if reviewerGit.ProjectID != fixture.Project.ID || reviewerGit.TaskID != fixture.Task.ID || reviewerGit.Repo != remote || reviewerGit.CommitSHA != commitSHA || reviewerGit.AgentID != a.credential.AgentID {
		t.Fatalf("reviewer Git replica has wrong stable handoff pointer: %+v", reviewerGit)
	}

	t.Run("Agent B reconstructs the handoff without human relay", func(t *testing.T) {
		tasksRaw := alphaMustGatewayCall(t, b.client, "wormhole.task.list", map[string]interface{}{"project_id": fixture.Project.ID})
		if !alphaJSONArrayObjectMatches(t, tasksRaw, "tasks", map[string]string{"id": fixture.Task.ID, "status": "done"}) {
			t.Fatalf("reviewer lacks completed task intent: %s", tasksRaw)
		}
		kbRaw := alphaMustGatewayCall(t, b.client, "wormhole.kb.list", map[string]interface{}{"project_id": fixture.Project.ID})
		if !bytes.Contains(kbRaw, []byte(reviewerGit.CommitSHA)) || !bytes.Contains(kbRaw, []byte(fixture.RepositoryContext.ExpectedPath)) {
			t.Fatalf("reviewer KB lacks Git pointer or discovery path: %s", kbRaw)
		}
		eventsRaw := alphaMustGatewayCall(t, b.client, "wormhole.channel.events", map[string]interface{}{"project_id": fixture.Project.ID})
		if !alphaJSONArrayHas(t, eventsRaw, "events", "id", offlineEventID) || !bytes.Contains(eventsRaw, []byte(reviewerGit.CommitSHA)) {
			t.Fatalf("reviewer events lack durable discovery/Git handoff: %s", eventsRaw)
		}
	})

	// A valid newer Fabric offer remains pending and cannot replace approved
	// guidance until a human explicitly approves it.
	manifestV2 := alphaManifestV2(t, seed.manifest)
	if _, err := seed.manifestStore.Publish(context.Background(), seed.publisherScope, manifestV2); err != nil {
		t.Fatalf("publish manifest v2: %v", err)
	}
	alphaWaitPendingManifest(t, b.client, fixture.Project.ID, fixture.Manifest.Digest, manifestV2.ManifestDigest)

	// Remove the real active semantic generation. Gateway must return the
	// Fabric structured degraded error, never a successful lexical fallback.
	if _, err := db.Exec(`UPDATE kb_embedding_generations SET state = 'retired', retired_at = now() WHERE project_id = $1 AND state = 'active'`, fixture.Project.ID); err != nil {
		t.Fatalf("make semantic index unavailable: %v", err)
	}
	degraded, transportErr := a.client.call("wormhole.kb.search", map[string]interface{}{
		"project_id": fixture.Project.ID, "query": "alpha handoff", "limit": 5,
	})
	if transportErr != nil || degraded.Error == "" || !strings.Contains(degraded.Error, `"degraded":true`) || !strings.Contains(degraded.Error, `"fallback":"none"`) {
		t.Fatalf("unavailable embedder/index transport=%v response=%+v", transportErr, degraded)
	}

	// Replayed pull cycles and Gateway restarts must not duplicate the three
	// outage writes in either authoritative or reviewer replicas.
	time.Sleep(6 * time.Second)
	alphaAssertCount(t, db, `SELECT count(*) FROM tasks WHERE id=$1`, offlineTaskID, 1)
	alphaAssertCount(t, db, `SELECT count(*) FROM kb_articles WHERE id=$1`, offlineKBID, 1)
	alphaAssertCount(t, db, `SELECT count(*) FROM events WHERE id=$1`, offlineEventID, 1)
	alphaAssertSQLiteCount(t, b.dbPath, "tasks", offlineTaskID, 1)
	alphaAssertSQLiteCount(t, b.dbPath, "kb_articles", offlineKBID, 1)
	alphaAssertSQLiteCount(t, b.dbPath, "events", offlineEventID, 1)
	alphaAssertSQLiteCount(t, b.dbPath, "git_links", gitLinkID, 1)

	// Reopen both durable replicas and force another pull/replay cycle. The
	// Gateway-owned status Event ID must remain the sole ID for the exact
	// transition in Gateway A, Fabric, and Gateway B after restart.
	a.restart(t, gatewayBin)
	b.restart(t, gatewayBin)
	alphaWaitOnline(t, a)
	alphaWaitOnline(t, b)
	alphaAssertStatusEventStableEverywhere(t, db, a.dbPath, b.dbPath, fixture.Project.ID, fixture.Task.ID, "todo", "wip", fixture.Channel.ID, a.credential.AgentID, wipStatusEventID)
	alphaAssertStatusEventStableEverywhere(t, db, a.dbPath, b.dbPath, fixture.Project.ID, fixture.Task.ID, "wip", "done", fixture.Channel.ID, a.credential.AgentID, doneStatusEventID)
}

type alphaSeedState struct {
	publisherScope *identity.AuthenticatedScope
	manifestStore  *mcp.IntegrationManifestStore
	manifest       mcp.IntegrationManifest
}

func loadAlphaValidationFixture(t *testing.T) alphaValidationFixture {
	t.Helper()
	path := filepath.Join(repoRootForTest(t), "testdata", "alpha", "projects", "full-loop", "fixture.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("load full alpha loop fixture %q: %v", path, err)
	}
	var fixture alphaValidationFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode full alpha loop fixture: %v", err)
	}
	if fixture.SchemaVersion != 1 || fixture.Project.ID == "" || fixture.Task.Status != "todo" || len(fixture.KBArticle.Body) == 0 || fixture.Manifest.Digest == "" {
		t.Fatalf("incomplete alpha fixture: %+v", fixture)
	}
	return fixture
}

func seedAlphaValidationFixture(t *testing.T, db *sql.DB, fixture alphaValidationFixture) alphaSeedState {
	t.Helper()
	clean := func() {
		_, _ = db.Exec(`DELETE FROM projects WHERE id=$1`, fixture.Project.ID)
		for _, owner := range []string{fixture.Publisher.Owner, fixture.Contributor.Owner, fixture.Reviewer.Owner} {
			_, _ = db.Exec(`DELETE FROM agents WHERE owner=$1`, owner)
		}
	}
	clean()
	t.Cleanup(clean)
	if _, err := db.Exec(`INSERT INTO projects(id,name,owner) VALUES($1,$2,$3)`, fixture.Project.ID, fixture.Project.Name, fixture.Project.Owner); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	identityStore := identity.NewStore(db)
	publisher, _, _, err := identityStore.Register(context.Background(), fixture.Project.ID, fixture.Publisher.Permissions,
		fixture.Publisher.Owner, fixture.Publisher.Model, fixture.Publisher.Capabilities, nil, fixture.Publisher.Roles)
	if err != nil {
		t.Fatalf("seed publisher: %v", err)
	}
	eventsStore := events.NewStore(db)
	if _, err := eventsStore.CreateChannelWithID(context.Background(), fixture.Channel.ID, fixture.Project.ID, fixture.Channel.Name); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	tasksStore := tasks.NewStore(db, eventsStore)
	if _, err := tasksStore.CreateWithID(context.Background(), fixture.Task.ID, fixture.Project.ID, fixture.Task.Title, fixture.Task.Description, nil, fixture.Task.Priority, nil); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	kbStore := kb.NewStore(db, kb.StubEmbedder{}, 0.85, 4000, 1, 1, 1)
	if _, err := kbStore.WriteArticleWithID(context.Background(), fixture.KBArticle.ID, fixture.Project.ID, publisher.ID,
		fixture.KBArticle.Title, fixture.KBArticle.Body, fixture.KBArticle.Frontmatter, nil, true); err != nil {
		t.Fatalf("seed KB: %v", err)
	}
	if _, err := eventsStore.PublishEventWithID(context.Background(), fixture.SeedEvent.ID, fixture.Project.ID, fixture.Channel.ID,
		publisher.ID, fixture.SeedEvent.EventType, fixture.SeedEvent.Payload, nil); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(repoRootForTest(t), fixture.Manifest.Path))
	if err != nil {
		t.Fatalf("read fixture manifest: %v", err)
	}
	var manifest mcp.IntegrationManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode fixture manifest: %v", err)
	}
	if manifest.ProjectID != fixture.Project.ID || manifest.ManifestID != fixture.Manifest.ID || manifest.ManifestVersion != fixture.Manifest.Version || manifest.ManifestDigest != fixture.Manifest.Digest {
		t.Fatalf("fixture manifest reference drift: got %+v, fixture %+v", manifest, fixture.Manifest)
	}
	scope := &identity.AuthenticatedScope{Agent: publisher, ProjectID: fixture.Project.ID, Permissions: fixture.Publisher.Permissions, Roles: fixture.Publisher.Roles}
	manifestStore := mcp.NewIntegrationManifestStore(db)
	if _, err := manifestStore.Publish(context.Background(), scope, manifest); err != nil {
		t.Fatalf("publish fixture manifest: %v", err)
	}
	return alphaSeedState{publisherScope: scope, manifestStore: manifestStore, manifest: manifest}
}

type alphaGateway struct {
	profile, checkout, home, runtimeDir, dataDir, dbPath, socketPath string
	env                                                              []string
	credential                                                       alphaCredential
	daemon                                                           *task4ProcessDaemon
	client                                                           *gateBMCPClient
}

func startAlphaGateway(t *testing.T, gatewayBin, fabricURL, projectID, remote, profile, checkout string, actor alphaFixtureActor) *alphaGateway {
	t.Helper()
	home := t.TempDir()
	runtimeDir := filepath.Join(home, "run")
	dataDir := filepath.Join(home, "data")
	goEnvironment := exec.Command("go", "env", "GOMODCACHE", "GOCACHE")
	goEnvironment.Dir = repoRootForTest(t)
	goEnvironmentOutput, err := goEnvironment.Output()
	if err != nil {
		t.Fatalf("resolve Go cache directories: %v", err)
	}
	goCachePaths := strings.Fields(string(goEnvironmentOutput))
	if len(goCachePaths) != 2 {
		t.Fatalf("resolve Go cache directories: unexpected output shape")
	}
	env := append(os.Environ(),
		"HOME="+home, "XDG_RUNTIME_DIR="+runtimeDir, "XDG_DATA_HOME="+dataDir,
		"GOMODCACHE="+goCachePaths[0], "GOCACHE="+goCachePaths[1],
		"WORMHOLE_ENROLMENT_ROLES="+strings.Join(actor.Roles, ","),
		"WORMHOLE_ENROLMENT_PERMISSIONS="+strings.Join(actor.Permissions, ","),
	)
	gateway := &alphaGateway{
		profile: profile, checkout: checkout, home: home, runtimeDir: runtimeDir, dataDir: dataDir,
		dbPath: filepath.Join(dataDir, "wormhole", "wormholed.db"), socketPath: filepath.Join(runtimeDir, "wormhole", "wormholed.sock"), env: env,
	}
	gateway.daemon = startTask4ProcessDaemon(t, gatewayBin, profile, env, gateway.socketPath)
	idempotencyKey := "018f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	if profile == "alpha-b" {
		idempotencyKey = "028f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	}
	client := gateBDialMCPClient(t, gateway.socketPath)
	response, err := client.call(localapi.EnrolmentToolName, map[string]interface{}{
		"version": localapi.EnrolmentProtocolVersion, "project_id": projectID,
		"owner": actor.Owner, "model": actor.Model, "capabilities": actor.Capabilities,
		"repositories": []string{remote}, "roles": actor.Roles, "requested_permissions": actor.Permissions,
		"fabric_address": fabricURL, "idempotency_key": idempotencyKey, "credential_profile": profile,
	})
	client.Close()
	if err != nil || response.Error != "" {
		t.Fatalf("Gateway enrolment %s: error=%v response=%q; gateway stderr=%q", profile, err, response.Error, gateway.daemon.stderr.String())
	}
	var result localapi.EnrolmentResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("decode Gateway enrolment %s: %v", profile, err)
	}
	if result.Code != localapi.EnrolmentSuccess || result.State != localapi.EnrolmentReady || result.PassportID == "" {
		t.Fatalf("Gateway enrolment %s did not become ready: %+v", profile, result)
	}
	credentialData, err := os.ReadFile(filepath.Join(home, ".wormhole", "credentials", profile+".json"))
	if err != nil {
		t.Fatalf("read %s credentials: %v", profile, err)
	}
	if err := json.Unmarshal(credentialData, &gateway.credential); err != nil {
		t.Fatalf("decode %s credentials: %v", profile, err)
	}
	if gateway.credential.ProjectID != projectID || gateway.credential.Token == "" {
		t.Fatalf("%s credential binding incomplete", profile)
	}
	return gateway
}

func (gateway *alphaGateway) restart(t *testing.T, gatewayBin string) {
	t.Helper()
	gateway.closeClient()
	if gateway.daemon != nil {
		gateway.daemon.Stop(t)
	}
	gateway.daemon = startTask4ProcessDaemon(t, gatewayBin, gateway.profile, gateway.env, gateway.socketPath)
	gateway.client = gateBDialMCPClient(t, gateway.socketPath)
}

func (gateway *alphaGateway) closeClient() {
	if gateway.client != nil {
		gateway.client.Close()
		gateway.client = nil
	}
}

func cloneAlphaWormhole(t *testing.T, repoRoot, projectID, role string) string {
	t.Helper()
	checkout := filepath.Join(t.TempDir(), "wormhole")
	command := exec.Command("git", "clone", "--quiet", "--no-hardlinks", repoRoot, checkout)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone Wormhole checkout: %v: %s", err, output)
	}
	configDir := filepath.Join(checkout, ".wormhole")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("project = %q\nrole = %q\n", projectID, role)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return checkout
}

func applyAlphaManifest(t *testing.T, wormholeBin string, gateway *alphaGateway, digest string) {
	t.Helper()
	output := runAlphaCLI(t, wormholeBin, gateway.env, gateway.checkout, "integration", "apply", "--project", gateway.credential.ProjectID, "--confirm-digest", digest)
	if !bytes.Contains(output, []byte("integration apply committed for project "+gateway.credential.ProjectID+" at "+digest)) {
		t.Fatalf("integration apply did not report the committed project/digest")
	}
}

func runAlphaCLI(t *testing.T, binary string, env []string, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Env, command.Dir = env, directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return output
}

func alphaGitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func alphaMustGatewayCall(t *testing.T, client *gateBMCPClient, tool string, args map[string]interface{}) json.RawMessage {
	t.Helper()
	return client.mustCall(t, tool, args)
}

func alphaListGatewayTools(t *testing.T, client *gateBMCPClient) []string {
	t.Helper()
	client.nextID++
	id, _ := json.Marshal(client.nextID)
	request, _ := json.Marshal(mcpRpcRequest{JSONRPC: "2.0", ID: id, Method: "tools/list", Params: json.RawMessage(`{}`)})
	if err := client.connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer client.connection.SetDeadline(time.Time{})
	if _, err := client.connection.Write(append(request, '\n')); err != nil {
		t.Fatal(err)
	}
	line, err := client.reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response mcpRpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(line), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("tools/list: %s", response.Error.Message)
	}
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func alphaWaitOnline(t *testing.T, gateway *alphaGateway) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var lastState, lastError string
	var lastPending int
	for {
		result, err := gateway.client.call("wormhole.sync.status", map[string]interface{}{"project_id": gateway.credential.ProjectID})
		switch {
		case err != nil:
			lastError = err.Error()
		case result.Error != "":
			lastError = result.Error
		default:
			var status struct {
				State   string `json:"state"`
				Pending int    `json:"pending_writes"`
			}
			if err := json.Unmarshal(result.Result, &status); err != nil {
				t.Fatalf("decode %s sync status: %v", gateway.profile, err)
			}
			lastState, lastPending, lastError = status.State, status.Pending, ""
			if status.State == "online" && status.Pending == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s online with empty queue: state=%q pending=%d call_error=%q gateway_stderr=%q",
				gateway.profile, lastState, lastPending, lastError, gateway.daemon.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func alphaWaitPending(t *testing.T, gateway *alphaGateway, minimum int) {
	t.Helper()
	waitForCondition(t, 5*time.Second, gateway.profile+" durable offline queue", func() (bool, error) {
		result, err := gateway.client.call("wormhole.sync.status", map[string]interface{}{"project_id": gateway.credential.ProjectID})
		if err != nil || result.Error != "" {
			return false, nil
		}
		var status struct {
			Pending int `json:"pending_writes"`
		}
		if err := json.Unmarshal(result.Result, &status); err != nil {
			return false, err
		}
		return status.Pending >= minimum, nil
	})
}

func assertAlphaGuidance(t *testing.T, client *gateBMCPClient, projectID, digest, role string) {
	t.Helper()
	raw := alphaMustGatewayCall(t, client, "wormhole.agent.get_guidance", map[string]interface{}{"project_id": projectID})
	var guidance struct {
		ManifestDigest       *string           `json:"manifest_digest"`
		ResolvedRole         *string           `json:"resolved_role"`
		ApprovalState        string            `json:"approval_state"`
		MaterializationState string            `json:"materialization_state"`
		Guidance             []json.RawMessage `json:"guidance"`
	}
	if err := json.Unmarshal(raw, &guidance); err != nil {
		t.Fatal(err)
	}
	if guidance.ManifestDigest == nil || *guidance.ManifestDigest != digest || guidance.ResolvedRole == nil || *guidance.ResolvedRole != role || guidance.ApprovalState != "approved" || guidance.MaterializationState != "applied" || len(guidance.Guidance) == 0 {
		t.Fatalf("approved cached %s guidance = %+v", role, guidance)
	}
}

func alphaJSONFieldEquals(t *testing.T, raw json.RawMessage, field, want string) {
	t.Helper()
	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if got, _ := object[field].(string); got != want {
		t.Fatalf("%s = %q, want %q in %s", field, got, want, raw)
	}
}

func alphaJSONID(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	id, _ := object["id"].(string)
	if id == "" {
		t.Fatalf("response lacks stable id: %s", raw)
	}
	return id
}

func alphaDecodeObject(t *testing.T, raw json.RawMessage) map[string]interface{} {
	t.Helper()
	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func alphaJSONArrayHas(t *testing.T, raw json.RawMessage, array, field, value string) bool {
	t.Helper()
	return alphaJSONArrayObjectMatches(t, raw, array, map[string]string{field: value})
}

func alphaJSONArrayObjectMatches(t *testing.T, raw json.RawMessage, array string, fields map[string]string) bool {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	var values []map[string]interface{}
	if err := json.Unmarshal(object[array], &values); err != nil {
		t.Fatalf("decode %s in %s: %v", array, raw, err)
	}
	for _, value := range values {
		matches := true
		for field, want := range fields {
			if got, _ := value[field].(string); got != want {
				matches = false
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func alphaWaitPostgresCount(t *testing.T, db *sql.DB, table, id string, want int) {
	t.Helper()
	waitForCondition(t, 25*time.Second, "Postgres "+table+" exactly once", func() (bool, error) {
		var count int
		err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE id=$1`, id).Scan(&count)
		return count == want, err
	})
}

func alphaWaitSQLiteCount(t *testing.T, dbPath, table, id string, want int) {
	t.Helper()
	waitForCondition(t, 25*time.Second, "reviewer SQLite "+table+" exactly once", func() (bool, error) {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return false, err
		}
		defer db.Close()
		var count int
		err = db.QueryRow(`SELECT count(*) FROM `+table+` WHERE id=?`, id).Scan(&count)
		return count == want, err
	})
}

func alphaWaitSQLiteTaskStatus(t *testing.T, dbPath, taskID, status string) {
	t.Helper()
	waitForCondition(t, 25*time.Second, "reviewer completed task", func() (bool, error) {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return false, err
		}
		defer db.Close()
		var got string
		err = db.QueryRow(`SELECT status FROM tasks WHERE id=?`, taskID).Scan(&got)
		return got == status, err
	})
}

func alphaSQLiteStatusEventID(t *testing.T, dbPath, taskID, fromStatus, toStatus, agentID string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id string
	if err := db.QueryRow(`SELECT id FROM events
		WHERE event_type='task.status_changed' AND agent_id=?
		AND json_extract(payload, '$.task_id')=?
		AND json_extract(payload, '$.from_status')=?
		AND json_extract(payload, '$.to_status')=?`, agentID, taskID, fromStatus, toStatus).Scan(&id); err != nil {
		t.Fatalf("read Gateway status Event %s -> %s: %v", fromStatus, toStatus, err)
	}
	return id
}

func alphaAssertStatusEventStableEverywhere(t *testing.T, fabric *sql.DB, gatewayAPath, gatewayBPath, projectID, taskID, fromStatus, toStatus, channelID, agentID, eventID string) {
	t.Helper()
	if eventID == "" {
		t.Fatalf("empty stable status Event ID for %s -> %s", fromStatus, toStatus)
	}
	var fabricCount int
	if err := fabric.QueryRow(`SELECT count(*) FROM events
		WHERE id=$1 AND project_id=$2 AND channel_id=$3 AND agent_id=$4
		AND event_type='task.status_changed' AND note IS NULL
		AND payload->>'task_id'=$5 AND payload->>'from_status'=$6 AND payload->>'to_status'=$7`,
		eventID, projectID, channelID, agentID, taskID, fromStatus, toStatus).Scan(&fabricCount); err != nil || fabricCount != 1 {
		t.Fatalf("Fabric stable status Event %s (%s -> %s) count=%d err=%v, want 1", eventID, fromStatus, toStatus, fabricCount, err)
	}
	var fabricTransitionCount int
	if err := fabric.QueryRow(`SELECT count(*) FROM events
		WHERE project_id=$1 AND channel_id=$2 AND agent_id=$3
		AND event_type='task.status_changed' AND note IS NULL
		AND payload->>'task_id'=$4 AND payload->>'from_status'=$5 AND payload->>'to_status'=$6`,
		projectID, channelID, agentID, taskID, fromStatus, toStatus).Scan(&fabricTransitionCount); err != nil || fabricTransitionCount != 1 {
		t.Fatalf("Fabric status transition %s -> %s count=%d err=%v, want exactly 1", fromStatus, toStatus, fabricTransitionCount, err)
	}
	for name, path := range map[string]string{"Gateway A": gatewayAPath, "Gateway B": gatewayBPath} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		var matchingIDCount, transitionCount int
		matchingErr := db.QueryRow(`SELECT count(*) FROM events
			WHERE id=? AND namespace_id=? AND channel_id=? AND agent_id=?
			AND event_type='task.status_changed' AND note IS NULL
			AND json_extract(payload, '$.task_id')=?
			AND json_extract(payload, '$.from_status')=?
			AND json_extract(payload, '$.to_status')=?`,
			eventID, projectID, channelID, agentID, taskID, fromStatus, toStatus).Scan(&matchingIDCount)
		transitionErr := db.QueryRow(`SELECT count(*) FROM events
			WHERE namespace_id=? AND channel_id=? AND agent_id=?
			AND event_type='task.status_changed' AND note IS NULL
			AND json_extract(payload, '$.task_id')=?
			AND json_extract(payload, '$.from_status')=?
			AND json_extract(payload, '$.to_status')=?`,
			projectID, channelID, agentID, taskID, fromStatus, toStatus).Scan(&transitionCount)
		_ = db.Close()
		if matchingErr != nil || transitionErr != nil || matchingIDCount != 1 || transitionCount != 1 {
			t.Fatalf("%s stable status Event %s (%s -> %s): matching ID count=%d err=%v transition count=%d err=%v; want 1/1", name, eventID, fromStatus, toStatus, matchingIDCount, matchingErr, transitionCount, transitionErr)
		}
	}
}

func alphaPostgresGitLinkID(t *testing.T, db *sql.DB, projectID, taskID, commit string) string {
	t.Helper()
	var id string
	waitForCondition(t, 25*time.Second, "linked Git pointer", func() (bool, error) {
		err := db.QueryRow(`SELECT id FROM git_links WHERE project_id=$1 AND task_id=$2 AND commit_sha=$3`, projectID, taskID, commit).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && id != "", err
	})
	return id
}

func alphaSQLiteGitLink(t *testing.T, dbPath, id string) alphaSQLiteGitLinkRecord {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var link alphaSQLiteGitLinkRecord
	if err := db.QueryRow(`SELECT id, project_id, task_id, repo, commit_sha, summary, agent_id FROM git_links WHERE id=?`, id).
		Scan(&link.ID, &link.ProjectID, &link.TaskID, &link.Repo, &link.CommitSHA, &link.Summary, &link.AgentID); err != nil {
		t.Fatalf("read reviewer Git pointer %s: %v", id, err)
	}
	return link
}

type alphaSQLiteGitLinkRecord struct {
	ID        string
	ProjectID string
	TaskID    string
	Repo      string
	CommitSHA string
	Summary   string
	AgentID   string
}

func alphaAssertCount(t *testing.T, db *sql.DB, query, id string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(query, id).Scan(&count); err != nil || count != want {
		t.Fatalf("count for %s = %d, err=%v, want %d", id, count, err, want)
	}
}

func alphaAssertSQLiteCount(t *testing.T, dbPath, table, id string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM `+table+` WHERE id=?`, id).Scan(&count); err != nil || count != want {
		t.Fatalf("SQLite %s/%s count=%d err=%v want=%d", table, id, count, err, want)
	}
}

func alphaAssertIndependentFiles(t *testing.T, left, right string) {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(leftInfo, rightInfo) {
		t.Fatal("Gateway replicas resolve to the same SQLite file")
	}
}

func alphaManifestV2(t *testing.T, first mcp.IntegrationManifest) mcp.IntegrationManifest {
	t.Helper()
	next := first
	next.ManifestVersion = first.ManifestVersion + 1
	next.CreatedAt = "2026-07-26T14:00:00Z"
	next.Entries = append([]mcp.IntegrationManifestEntry(nil), first.Entries...)
	next.Entries[0].Content += "\nVersion two remains unapproved during alpha acceptance.\n"
	next.Entries[0].ContentDigest = alphaSHA256([]byte(next.Entries[0].Content))
	next.ManifestDigest = ""
	digest, err := alphaCanonicalManifestDigest(next)
	if err != nil {
		t.Fatal(err)
	}
	next.ManifestDigest = digest
	return next
}

func alphaWaitPendingManifest(t *testing.T, client *gateBMCPClient, projectID, active, pending string) {
	t.Helper()
	waitForCondition(t, 90*time.Second, "unapproved manifest candidate", func() (bool, error) {
		result, err := client.call("wormhole.agent.get_guidance", map[string]interface{}{"project_id": projectID})
		if err != nil || result.Error != "" {
			return false, nil
		}
		var state struct {
			ManifestDigest *string `json:"manifest_digest"`
			PendingDigest  *string `json:"pending_manifest_digest"`
			PendingVersion *int64  `json:"pending_manifest_version"`
			Guidance       []struct {
				Content string `json:"content"`
			} `json:"guidance"`
		}
		if err := json.Unmarshal(result.Result, &state); err != nil {
			return false, err
		}
		if state.ManifestDigest == nil || *state.ManifestDigest != active || state.PendingDigest == nil || *state.PendingDigest != pending || state.PendingVersion == nil || *state.PendingVersion != 2 {
			return false, nil
		}
		for _, item := range state.Guidance {
			if strings.Contains(item.Content, "Version two remains unapproved") {
				return false, errors.New("unapproved guidance became active")
			}
		}
		return true, nil
	})
}

func alphaCanonicalManifestDigest(manifest mcp.IntegrationManifest) (string, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]interface{}
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	delete(value, "manifest_digest")
	var canonical bytes.Buffer
	if err := alphaAppendCanonicalJSON(&canonical, value); err != nil {
		return "", err
	}
	return alphaSHA256(canonical.Bytes()), nil
}

func alphaAppendCanonicalJSON(output *bytes.Buffer, value interface{}) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		if typed {
			output.WriteString("true")
		} else {
			output.WriteString("false")
		}
	case string:
		alphaAppendCanonicalString(output, typed)
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			return errors.New("non-integer canonical number")
		}
		if _, err := strconv.ParseInt(string(typed), 10, 64); err != nil {
			return err
		}
		output.WriteString(string(typed))
	case []interface{}:
		output.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				output.WriteByte(',')
			}
			if err := alphaAppendCanonicalJSON(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				output.WriteByte(',')
			}
			alphaAppendCanonicalString(output, key)
			output.WriteByte(':')
			if err := alphaAppendCanonicalJSON(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func alphaAppendCanonicalString(output *bytes.Buffer, value string) {
	const hexadecimal = "0123456789abcdef"
	output.WriteByte('"')
	for _, character := range []byte(value) {
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteByte(character)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 0x20 {
				output.WriteString(`\u00`)
				output.WriteByte(hexadecimal[character>>4])
				output.WriteByte(hexadecimal[character&0x0f])
			} else {
				output.WriteByte(character)
			}
		}
	}
	output.WriteByte('"')
}

func alphaSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func equalAlphaStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func typesDatabaseURL() string {
	return types.LoadConfig().DatabaseURL
}
