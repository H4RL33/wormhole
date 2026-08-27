package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
	"github.com/H4RL33/wormhole/internal/core/kb"
)

type countingOnboardingEmbedder struct{ calls atomic.Int32 }

func (*countingOnboardingEmbedder) Descriptor() kb.EmbeddingDescriptor {
	return kb.StubEmbedder{}.Descriptor()
}

func (e *countingOnboardingEmbedder) Embed(ctx context.Context, request kb.EmbeddingRequest) ([][]float32, error) {
	e.calls.Add(1)
	return kb.StubEmbedder{}.Embed(ctx, request)
}

func TestEnrolmentInvalidAndReplayRequestsDoNotCallEmbeddingProvider(t *testing.T) {
	embedder := &countingOnboardingEmbedder{}
	kbStore := kb.NewStore(testDB(t), embedder, 0.85, 2000, 1, 1, 1)
	if err := PrepareOnboardingArticleEmbedding(context.Background(), kbStore); err != nil {
		t.Fatalf("PrepareOnboardingArticleEmbedding: %v", err)
	}
	tool := EnrolAgentTool(testIdentityStore(t), testEventsStore(t), kbStore)
	projectID := mustCreateProject(t, "enrol-no-provider-amplification")
	invalid, _ := json.Marshal(EnrolAgentInput{
		IdempotencyKey: "928f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1",
		RequestHash:    "not-a-valid-request-hash", Owner: "invalid", Model: "gpt-5",
	})
	if _, err := tool.Handler(context.Background(), nil, projectID, invalid); err == nil {
		t.Fatal("invalid enrolment error = nil")
	}
	valid, _ := json.Marshal(EnrolAgentInput{
		IdempotencyKey: "218f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1",
		RequestHash:    strings.Repeat("c", 64), Permissions: []string{"task.create"},
		Owner: "valid", Model: "gpt-5", Capabilities: []string{"code"},
		Repositories: []string{"https://github.com/H4RL33/wormhole"}, Roles: []string{"contributor"},
	})
	if _, err := tool.Handler(context.Background(), nil, projectID, valid); err != nil {
		t.Fatalf("first enrolment: %v", err)
	}
	if _, err := tool.Handler(context.Background(), nil, projectID, valid); err != nil {
		t.Fatalf("replay enrolment: %v", err)
	}
	if got := embedder.calls.Load(); got != 1 {
		t.Fatalf("embedding calls = %d, want startup-only call", got)
	}
}

func TestEnrolAgentTool_DurableReplayAndControlledReissue(t *testing.T) {
	tool := EnrolAgentTool(testIdentityStore(t), testEventsStore(t), testKBStore(t))
	if tool.Name != "wormhole.agent.enrol" || tool.RequiresAuth {
		t.Fatalf("tool contract = name %q auth=%t", tool.Name, tool.RequiresAuth)
	}
	projectID := mustCreateProject(t, "mcp-enrol-replay")
	in := EnrolAgentInput{
		IdempotencyKey: "218f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1",
		RequestHash:    strings.Repeat("c", 64), Permissions: []string{"task.create"},
		Owner: "enrol-owner", Model: "gpt-5", Capabilities: []string{"code"},
		Repositories: []string{"https://github.com/H4RL33/wormhole"}, Roles: []string{"contributor"},
	}
	arguments, _ := json.Marshal(in)
	firstRaw, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("first enrolment: %v", err)
	}
	first := firstRaw.(EnrolAgentOutput)
	if first.AgentID == "" || first.PassportID == "" || first.Token == "" || first.Replay {
		t.Fatalf("invalid first output: %+v", first)
	}
	replayRaw, err := tool.Handler(context.Background(), nil, projectID, arguments)
	if err != nil {
		t.Fatalf("replay enrolment: %v", err)
	}
	replay := replayRaw.(EnrolAgentOutput)
	if replay.AgentID != first.AgentID || replay.PassportID != first.PassportID || replay.Token != "" || !replay.Replay {
		t.Fatalf("invalid replay output: %+v", replay)
	}
	in.Reissue = true
	reissueArguments, _ := json.Marshal(in)
	reissuedRaw, err := tool.Handler(context.Background(), nil, projectID, reissueArguments)
	if err != nil {
		t.Fatalf("reissue enrolment: %v", err)
	}
	reissued := reissuedRaw.(EnrolAgentOutput)
	if reissued.AgentID != first.AgentID || reissued.PassportID != first.PassportID || reissued.Token == "" || reissued.Token == first.Token || !reissued.Reissued {
		t.Fatalf("invalid reissue output: %+v", reissued)
	}
}

func TestFabricRegistryIncludesGatewayEnrolmentEndpoint(t *testing.T) {
	registry := NewFabricRegistry(FabricRegistryDependencies{})
	tool, ok := registry.Get("wormhole.agent.enrol")
	if !ok || tool.RequiresAuth {
		t.Fatalf("Fabric enrolment contract = present %t auth %t", ok, tool.RequiresAuth)
	}
	if _, ok := tool.ResultExamples["default"].(EnrolAgentOutput); !ok {
		t.Fatalf("result example = %T, want EnrolAgentOutput", tool.ResultExamples["default"])
	}
}

func TestEnrolAgentToolBootstrapsDefaultChannelsOnce(t *testing.T) {
	identityStore := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	tool := EnrolAgentTool(identityStore, eventsStore, testKBStore(t))
	projectID := mustCreateProject(t, "mcp-enrol-bootstrap")
	for index, owner := range []string{"harley", "harley2"} {
		arguments := mustMarshalEnrolment(t, index+1, owner)
		if _, err := tool.Handler(context.Background(), nil, projectID, arguments); err != nil {
			t.Fatalf("enrolment %d: %v", index+1, err)
		}
		channels, err := eventsStore.ListChannels(context.Background(), projectID)
		if err != nil {
			t.Fatal(err)
		}
		if len(channels) != 2 {
			t.Fatalf("channel count after enrolment %d = %d, want 2", index+1, len(channels))
		}
		names := map[string]bool{}
		for _, channel := range channels {
			names[channel.Name] = true
		}
		if !names["introductions"] || !names["general"] {
			t.Fatalf("default channels = %+v", channels)
		}
	}
}

func TestEnrolAgentToolConcurrentBootstrapIsIdempotent(t *testing.T) {
	identityStore := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	tool := EnrolAgentTool(identityStore, eventsStore, testKBStore(t))
	projectID := mustCreateProject(t, "mcp-enrol-concurrent-bootstrap")
	const registrations = 20
	arguments := make([]json.RawMessage, registrations)
	for index := range arguments {
		arguments[index] = mustMarshalEnrolment(t, index+1, fmt.Sprintf("concurrent-agent-%d", index))
	}
	start := make(chan struct{})
	errs := make(chan error, registrations)
	var wg sync.WaitGroup
	for index := 0; index < registrations; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := tool.Handler(context.Background(), nil, projectID, arguments[index])
			errs <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent enrolment: %v", err)
		}
	}
	assertFixedBootstrapCounts(t, projectID)
}

func TestEnrolAgentToolBootstrapFailureRollsBackAndRetryIsIdempotent(t *testing.T) {
	identityStore := testIdentityStore(t)
	eventsStore := testEventsStore(t)
	kbStore := testKBStore(t)
	projectID := mustCreateProject(t, "enrol-bootstrap-channel-failure-retry")
	stopRejectingChannels := rejectChannelInsertsForProject(t, projectID)
	arguments := mustMarshalEnrolment(t, 1, "bootstrap-retry-agent")
	failingTool := EnrolAgentTool(identityStore, eventsStore, kbStore)
	if _, err := failingTool.Handler(context.Background(), nil, projectID, arguments); err == nil || !strings.Contains(err.Error(), "default channel bootstrap") {
		t.Fatalf("enrolment error = %v, want default-channel bootstrap failure", err)
	}
	assertRegistrationCount(t, projectID, "bootstrap-retry-agent", 0)
	stopRejectingChannels()
	retryTool := EnrolAgentTool(identityStore, eventsStore, kbStore)
	if _, err := retryTool.Handler(context.Background(), nil, projectID, arguments); err != nil {
		t.Fatalf("enrolment retry: %v", err)
	}
	assertFixedBootstrapCounts(t, projectID)
	assertRegistrationCount(t, projectID, "bootstrap-retry-agent", 1)

	failingKB := kb.NewStore(testDB(t), failingEmbedder{err: errors.New("forced onboarding embedding failure")}, 0.85, 2000, 1, 1, 1)
	if err := PrepareOnboardingArticleEmbedding(context.Background(), failingKB); err == nil || !strings.Contains(err.Error(), "forced onboarding embedding failure") {
		t.Fatalf("startup preparation error = %v, want onboarding embedding failure", err)
	}
}

func TestEnrolAgentSeedsOnboardingArticleOnce(t *testing.T) {
	kbStore := testKBStore(t)
	tool := EnrolAgentTool(testIdentityStore(t), testEventsStore(t), kbStore)
	projectID := mustCreateProject(t, "onboarding-article-test")
	for index := 1; index <= 2; index++ {
		if _, err := tool.Handler(context.Background(), nil, projectID, mustMarshalEnrolment(t, index, fmt.Sprintf("agent-%d", index))); err != nil {
			t.Fatalf("enrolment %d: %v", index, err)
		}
	}
	articles, err := kbStore.ListArticles(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, article := range articles {
		if article.Title == onboardingArticleTitle {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("onboarding article count = %d, want 1", count)
	}
}

func mustMarshalEnrolment(t *testing.T, sequence int, owner string) json.RawMessage {
	t.Helper()
	input := EnrolAgentInput{
		IdempotencyKey: fmt.Sprintf("218f47a2-7b1d-4e42-8d4b-%012d", sequence),
		RequestHash:    fmt.Sprintf("%064x", sequence), Permissions: []string{"event.publish"},
		Owner: owner, Model: "claude",
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type failingEmbedder struct{ err error }

func (e failingEmbedder) Descriptor() kb.EmbeddingDescriptor { return kb.StubEmbedder{}.Descriptor() }
func (e failingEmbedder) Embed(context.Context, kb.EmbeddingRequest) ([][]float32, error) {
	return nil, e.err
}

func rejectChannelInsertsForProject(t *testing.T, projectID string) func() {
	t.Helper()
	db := testDB(t)
	suffix := strings.ReplaceAll(projectID, "-", "")
	functionName := "mcp_reject_channel_" + suffix
	triggerName := functionName
	if _, err := db.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced default channel bootstrap failure'; END $$`, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON channels FOR EACH ROW WHEN (NEW.project_id = '%s'::uuid) EXECUTE FUNCTION %s()`, triggerName, projectID, functionName)); err != nil {
		_, _ = db.Exec("DROP FUNCTION " + functionName + "()")
		t.Fatal(err)
	}
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			if _, err := db.Exec("DROP TRIGGER " + triggerName + " ON channels"); err != nil {
				t.Errorf("drop trigger: %v", err)
			}
			if _, err := db.Exec("DROP FUNCTION " + functionName + "()"); err != nil {
				t.Errorf("drop function: %v", err)
			}
		})
	}
	t.Cleanup(cleanup)
	return cleanup
}

func assertRegistrationCount(t *testing.T, projectID, owner string, want int) {
	t.Helper()
	var got int
	if err := testDB(t).QueryRowContext(context.Background(), `SELECT count(*) FROM agents a JOIN passports p ON p.agent_id=a.id WHERE p.project_id=$1 AND a.owner=$2`, projectID, owner).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("registration count = %d, want %d", got, want)
	}
}

func assertFixedBootstrapCounts(t *testing.T, projectID string) {
	t.Helper()
	db := testDB(t)
	var channels, articles int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM channels WHERE project_id=$1 AND name IN ('introductions','general')`, projectID).Scan(&channels); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM kb_articles WHERE project_id=$1 AND bootstrap_key=$2`, projectID, onboardingArticleBootstrapKey).Scan(&articles); err != nil {
		t.Fatal(err)
	}
	if channels != 2 || articles != 1 {
		t.Fatalf("fixed bootstrap counts = channels %d articles %d", channels, articles)
	}
}

func TestBootstrapMarkerDoesNotConstrainOrdinaryArticleTitles(t *testing.T) {
	projectID := mustCreateProject(t, "ordinary-duplicate-kb-titles")
	agentID, _ := mustRegisterAgent(t, projectID)
	store := testKBStore(t)
	for i := 0; i < 2; i++ {
		if _, err := store.WriteArticle(context.Background(), projectID, agentID, onboardingArticleTitle,
			fmt.Sprintf("ordinary article body %d", i), nil, nil, true); err != nil {
			t.Fatalf("write ordinary article %d: %v", i, err)
		}
	}
	var count int
	if err := testDB(t).QueryRowContext(context.Background(),
		`SELECT count(*) FROM kb_articles WHERE project_id = $1 AND title = $2 AND bootstrap_key IS NULL`,
		projectID, onboardingArticleTitle).Scan(&count); err != nil {
		t.Fatalf("count ordinary duplicate-title articles: %v", err)
	}
	if count != 2 {
		t.Fatalf("ordinary duplicate-title article count = %d, want 2", count)
	}
}

func mustCreateProject(t *testing.T, name string) string {
	t.Helper()
	db := testDB(t)
	var id string
	if err := db.QueryRow(`INSERT INTO projects (name, owner) VALUES ($1, $2) RETURNING id`, name, "harley").Scan(&id); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM projects WHERE id = $1`, id); err != nil {
			t.Logf("cleanup project %s: %v", id, err)
		}
	})
	return id
}

func TestWhoAmITool_Handler(t *testing.T) {
	tool := WhoAmITool()
	if tool.Name != "wormhole.agent.whoami" || !tool.RequiresAuth {
		t.Fatalf("tool contract = name %q auth=%t", tool.Name, tool.RequiresAuth)
	}
	scope := &identity.AuthenticatedScope{
		Agent:     identity.Agent{ID: "agent-1", Owner: "harley", Model: "claude", Capabilities: []string{"code_review"}},
		ProjectID: "proj-1", Permissions: []string{"event.publish"},
	}
	result, err := tool.Handler(context.Background(), scope, "proj-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	out := result.(WhoAmIOutput)
	if out.AgentID != "agent-1" || out.ProjectID != "proj-1" {
		t.Fatalf("output: %+v", out)
	}
}
