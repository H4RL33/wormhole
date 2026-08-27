package localapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	runtimeconfig "github.com/H4RL33/wormhole/internal/runtime/config"
	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/scheduler"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
	"github.com/H4RL33/wormhole/internal/types"
)

func TestConfiguredPrivateRuntimeKeepsTargetAgentOperationsUsableAndWorkspaceScoped(t *testing.T) {
	first, second := privateDispatchSiblingBindings(t)
	server := privateDispatchTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), first, second)
	targetA := "00000000-0000-4000-8000-000000000031"
	targetB := "00000000-0000-4000-8000-000000000032"

	privateDispatchSuccess(t, server, first, "wormhole.agent.register", map[string]any{"agent_id": targetA, "capabilities": []string{"review"}}, nil)
	privateDispatchSuccess(t, server, second, "wormhole.agent.register", map[string]any{"agent_id": targetB, "capabilities": []string{"code"}}, nil)
	privateDispatchSuccess(t, server, first, "wormhole.agent.presence", map[string]any{"agent_id": targetA, "status": "busy"}, nil)
	crossRegister := privateDispatchResult(t, server, second, "wormhole.agent.register", map[string]any{"agent_id": targetA, "capabilities": []string{"cross-mutation"}}, nil)
	if !crossRegister.IsError || !strings.Contains(crossRegister.Content[0].Text, "outside resolved workspace") {
		t.Fatalf("cross-workspace target registration = %+v", crossRegister)
	}

	secondList := privateDispatchSuccess(t, server, second, "wormhole.agent.list", nil, nil)
	agents, ok := secondList["agents"].([]any)
	if !ok || len(agents) != 1 || agents[0].(map[string]any)["agent_id"] != targetB {
		t.Fatalf("second workspace agents = %#v, want only %s", secondList["agents"], targetB)
	}
	cross := privateDispatchResult(t, server, second, "wormhole.agent.presence", map[string]any{"agent_id": targetA, "status": "idle"}, nil)
	if !cross.IsError || !strings.Contains(cross.Content[0].Text, "outside resolved workspace") {
		t.Fatalf("cross-workspace target presence = %+v", cross)
	}
	firstList := privateDispatchSuccess(t, server, first, "wormhole.agent.list", nil, nil)
	firstAgents := firstList["agents"].([]any)
	if len(firstAgents) != 1 || firstAgents[0].(map[string]any)["status"] != "busy" {
		t.Fatalf("cross-workspace presence changed first agent: %#v", firstList["agents"])
	}

	removedRegistration := privateDispatchResult(t, server, first, "wormhole.agent.register", map[string]any{
		"owner": "agent-owner", "model": "test", "permissions": []string{}, "capabilities": []string{}, "repositories": []string{}, "roles": []string{},
	}, nil)
	if !removedRegistration.IsError {
		t.Fatalf("removed registration shape was accepted: %+v", removedRegistration)
	}
}

func TestConfiguredPrivateRuntimeRejectsFormerJoinAndUnknownRegistrationFields(t *testing.T) {
	first, second := privateDispatchSiblingBindings(t)
	server := privateDispatchTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), first, second)
	former := map[string]any{
		"name": "legacy", "permissions": []string{"task.create"}, "owner": "legacy-owner",
		"model": "legacy-model", "repositories": []string{"https://example.test/repo"},
		"roles": []string{"reviewer"}, "role": "reviewer", "unknown": true,
	}
	for field, value := range former {
		t.Run(field, func(t *testing.T) {
			result := privateDispatchResult(t, server, first, "wormhole.agent.register", map[string]any{
				"agent_id": "00000000-0000-4000-8000-000000000041", "capabilities": []string{"review"}, field: value,
			}, nil)
			if !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "invalid") {
				t.Fatalf("registration with %s = %+v, want strict rejection", field, result)
			}
		})
	}

	private := fmt.Sprintf(`{"working_directory":%q}`, first.Checkout.CanonicalPath)
	arguments := json.RawMessage(`{"agent_id":"00000000-0000-4000-8000-000000000042","agent_id":"00000000-0000-4000-8000-000000000043","capabilities":[],"` + privateRequestContextKey + `":` + private + `}`)
	params, err := json.Marshal(toolsCallParams{Name: "wormhole.agent.register", Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	got, rpcErr := server.handleToolsCall(context.Background(), &mcpSession{}, nil, server.registry, params)
	if rpcErr != nil {
		t.Fatalf("duplicate registration RPC error = %+v", rpcErr)
	}
	result, ok := got.(toolCallResult)
	if !ok || !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "invalid") {
		t.Fatalf("duplicate registration = %#v, want strict rejection", got)
	}
}

func TestConfiguredPublicSchemasExposeOnlySupportedAgentTargetsAndNamespaceOnlySubscription(t *testing.T) {
	registry := newLocalRegistry(&Server{})
	for _, name := range []string{"wormhole.channel.post", "wormhole.kb.write"} {
		tool, _ := registry.Get(name)
		raw, err := json.Marshal(buildInputSchema(tool))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"agent_id"`) {
			t.Fatalf("%s still exposes caller-owned attribution: %s", name, raw)
		}
	}
	for _, name := range []string{"wormhole.agent.register", "wormhole.agent.presence"} {
		tool, _ := registry.Get(name)
		raw, err := json.Marshal(buildInputSchema(tool))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"agent_id"`) {
			t.Fatalf("%s lost legitimate target agent_id: %s", name, raw)
		}
	}
	subscription, _ := registry.Get("wormhole.channel.subscribe")
	raw, err := json.Marshal(buildInputSchema(subscription))
	if err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"agent_id", "capability", "event_type"} {
		if strings.Contains(string(raw), `"`+unsupported+`"`) {
			t.Errorf("channel.subscribe exposes unsupported filter %s: %s", unsupported, raw)
		}
	}
	response, err := json.Marshal(subscription.ResultExamples["default"])
	if err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"agent_id", "capability", "event_type"} {
		if strings.Contains(string(response), `"`+unsupported+`"`) {
			t.Errorf("channel.subscribe response exposes unsupported filter %s: %s", unsupported, response)
		}
	}
}

func TestConfiguredPrivateRuntimeDerivesAttributionAndIsolatesChannelAndKBHandlers(t *testing.T) {
	first, second := privateDispatchSiblingBindings(t)
	actor := privateRoutingTestActor("00000000-0000-4000-8000-000000000021")
	server := privateDispatchTestServer(t, actor, first, second)

	createdChannel := privateDispatchSuccess(t, server, first, "wormhole.channel.create", map[string]any{"name": "first-only"}, nil)
	channelID := createdChannel["id"].(string)
	if createdChannel["namespace_id"] != string(first.Scope.WorkspaceID) {
		t.Fatalf("channel namespace = %v, want workspace %s", createdChannel["namespace_id"], first.Scope.WorkspaceID)
	}
	secondChannels := privateDispatchSuccess(t, server, second, "wormhole.channel.list", nil, nil)
	if channels := secondChannels["channels"].([]any); len(channels) != 0 {
		t.Fatalf("second workspace observed first channels: %#v", channels)
	}
	crossPost := privateDispatchResult(t, server, second, "wormhole.channel.post", map[string]any{"channel_id": channelID, "event_type": "review.ready"}, nil)
	if !crossPost.IsError {
		t.Fatalf("second workspace posted to first channel: %+v", crossPost)
	}
	posted := privateDispatchSuccess(t, server, first, "wormhole.channel.post", map[string]any{"channel_id": channelID, "event_type": "review.ready"}, nil)
	if posted["agent_id"] != actor.PrincipalID() {
		t.Fatalf("channel attribution = %v, want server actor %s", posted["agent_id"], actor.PrincipalID())
	}
	forgedPost := privateDispatchResult(t, server, first, "wormhole.channel.post", map[string]any{"channel_id": channelID, "event_type": "review.ready", "agent_id": "forged"}, nil)
	if !forgedPost.IsError || !strings.Contains(forgedPost.Content[0].Text, "agent_id") {
		t.Fatalf("forged channel attribution accepted: %+v", forgedPost)
	}
	firstEvents := privateDispatchSuccess(t, server, first, "wormhole.channel.events", nil, nil)["events"].([]any)
	secondEvents := privateDispatchSuccess(t, server, second, "wormhole.channel.events", nil, nil)["events"].([]any)
	if len(firstEvents) != 1 || len(secondEvents) != 0 {
		t.Fatalf("workspace event lists first=%#v second=%#v", firstEvents, secondEvents)
	}

	written := privateDispatchSuccess(t, server, first, "wormhole.kb.write", map[string]any{"title": "first-only", "body": "private sibling draft"}, nil)
	articleID := written["id"].(string)
	if written["author_agent_id"] != actor.PrincipalID() {
		t.Fatalf("KB attribution = %v, want server actor %s", written["author_agent_id"], actor.PrincipalID())
	}
	firstGet := privateDispatchSuccess(t, server, first, "wormhole.kb.get", map[string]any{"article_id": articleID}, nil)
	if firstGet["id"] != articleID {
		t.Fatalf("first workspace KB get = %#v", firstGet)
	}
	secondKB := privateDispatchSuccess(t, server, second, "wormhole.kb.list", nil, nil)
	if articles := secondKB["articles"].([]any); len(articles) != 0 {
		t.Fatalf("second workspace observed first KB: %#v", articles)
	}
	crossGet := privateDispatchResult(t, server, second, "wormhole.kb.get", map[string]any{"article_id": articleID}, nil)
	if !crossGet.IsError {
		t.Fatalf("second workspace read first KB article: %+v", crossGet)
	}
	forgedKB := privateDispatchResult(t, server, first, "wormhole.kb.write", map[string]any{"title": "forged", "agent_id": "forged"}, nil)
	if !forgedKB.IsError || !strings.Contains(forgedKB.Content[0].Text, "agent_id") {
		t.Fatalf("forged KB attribution accepted: %+v", forgedKB)
	}
}

func TestConfiguredChannelSubscriptionUsesOnlyResolvedWorkspaceNamespace(t *testing.T) {
	first, second := privateDispatchSiblingBindings(t)
	server := privateDispatchTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), first, second)

	forged := privateDispatchResult(t, server, first, "wormhole.channel.subscribe", map[string]any{"namespace": "forged"}, nil)
	if !forged.IsError || !strings.Contains(forged.Content[0].Text, "namespace") {
		t.Fatalf("caller-selected subscription namespace accepted: %+v", forged)
	}

	client, gateway := net.Pipe()
	defer client.Close()
	defer gateway.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ack := privateDispatchSuccessContext(t, ctx, server, first, "wormhole.channel.subscribe", nil, gateway)
	if ack["namespace"] != string(first.Scope.WorkspaceID) {
		t.Fatalf("subscription namespace = %v, want %s", ack["namespace"], first.Scope.WorkspaceID)
	}
	for _, unsupported := range []string{"agent_id", "capability", "event_type"} {
		if _, exposed := ack[unsupported]; exposed {
			t.Errorf("namespace-only subscription ack exposes unsupported filter %s: %#v", unsupported, ack)
		}
	}

	server.eventbus.Publish(context.Background(), string(second.Scope.WorkspaceID), "presence.online", "", "", []byte(`{"workspace":"second"}`))
	if err := client.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(client).ReadString('\n'); err == nil {
		t.Fatalf("first subscription received sibling event: %s", line)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("read sibling notification: %v", err)
		}
	}

	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	server.eventbus.Publish(context.Background(), string(first.Scope.WorkspaceID), "presence.online", "", "", []byte(`{"workspace":"first"}`))
	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil || !strings.Contains(line, `"workspace":"first"`) {
		t.Fatalf("read same-workspace notification = %q, %v", line, err)
	}
}

func TestConfiguredWhoAmIFailsClosedWithoutTransportOrCredentialAttribution(t *testing.T) {
	ambientProject := "00000000-0000-4000-8000-000000000001"
	resolvedProject := "00000000-0000-4000-8000-000000000002"
	binding := privateRoutingTestBinding(t, t.TempDir(), resolvedProject, "00000000-0000-4000-8000-000000000012")
	actorID := "00000000-0000-4000-8000-000000000021"
	server := privateDispatchTestServer(t, privateRoutingTestActor(actorID), binding)
	transport := &privateDispatchCountingTransport{}
	server.httpClient = &http.Client{Transport: transport}
	server.projectID = ambientProject
	server.isMultiOrg = true
	server.orgs = map[string]runtimeconfig.Org{
		"ambient":  {Name: "ambient", Credentials: runtimeconfig.Credentials{Server: "http://ambient.invalid", ProjectID: ambientProject, AgentID: "ambient-agent", Token: "ambient-token"}},
		"resolved": {Name: "resolved", Credentials: runtimeconfig.Credentials{Server: "http://resolved.invalid", ProjectID: resolvedProject, AgentID: "resolved-agent", Token: "resolved-token"}},
	}
	server.bindings = []runtimeconfig.ProjectBinding{{ProjectID: ambientProject, OrgName: "ambient"}, {ProjectID: resolvedProject, OrgName: "resolved"}}
	if err := server.store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "ambient-agent", ProjectID: ambientProject, CachedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := server.store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{AgentID: "resolved-agent", ProjectID: resolvedProject, CachedAt: time.Now().UTC().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	result := privateDispatchResult(t, server, binding, "wormhole.agent.whoami", nil, nil)
	if !result.IsError || !strings.Contains(result.Content[0].Text, "binding-aware provider unavailable") {
		t.Errorf("configured whoami = %+v, want binding-aware provider unavailable", result)
	}
	if transport.calls != 0 {
		t.Errorf("configured whoami made %d transport calls, want zero", transport.calls)
	}
	if strings.Contains(result.Content[0].Text, "resolved-agent") || strings.Contains(result.Content[0].Text, actorID) {
		t.Errorf("configured whoami attributed an identity through incompatible contract: %+v", result)
	}
}

func TestConfiguredPrivateRuntimeFailsEveryUnscopedRealHandlerClosedRegistryWide(t *testing.T) {
	first, second := privateDispatchSiblingBindings(t)
	server := privateDispatchTestServer(t, privateRoutingTestActor("00000000-0000-4000-8000-000000000021"), first, second)
	allowed := map[string]bool{
		"wormhole.sync.status":      true,
		"wormhole.workspace.status": true, "wormhole.workspace.diff": true, "wormhole.workspace.import": true, "wormhole.workspace.checkpoint": true, "wormhole.workspace.stash": true,
		"wormhole.agent.register": true, "wormhole.agent.presence": true, "wormhole.agent.list": true,
		"wormhole.channel.list": true, "wormhole.channel.create": true, "wormhole.channel.events": true, "wormhole.channel.post": true, "wormhole.channel.subscribe": true,
		"wormhole.kb.list": true, "wormhole.kb.get": true, "wormhole.kb.write": true,
	}
	for _, tool := range server.registry.List() {
		if allowed[tool.Name] {
			continue
		}
		for _, binding := range []types.WorkspaceBinding{first, second} {
			t.Run(tool.Name+"/"+string(binding.Scope.WorkspaceID), func(t *testing.T) {
				result := privateDispatchResult(t, server, binding, tool.Name, privateDispatchValidBlockedArguments(tool.Name), nil)
				if !result.IsError || !strings.Contains(result.Content[0].Text, "binding-aware provider unavailable") {
					t.Fatalf("configured unscoped handler result = %+v", result)
				}
			})
		}
	}
}

func privateDispatchValidBlockedArguments(name string) map[string]any {
	switch name {
	case "wormhole.task.get":
		return map[string]any{"task_id": "00000000-0000-4000-8000-000000000041"}
	case "wormhole.task.create":
		return map[string]any{"title": "must not persist"}
	case "wormhole.task.update_status":
		return map[string]any{"task_id": "00000000-0000-4000-8000-000000000041", "new_status": "wip", "channel_id": "00000000-0000-4000-8000-000000000042"}
	case "wormhole.task.route":
		return map[string]any{"capability": "review", "title": "must not persist"}
	case "wormhole.kb.search":
		return map[string]any{"query": "must not escape"}
	case "wormhole.git.link_commit":
		return map[string]any{"task_id": "00000000-0000-4000-8000-000000000041", "repo": "acme/repo", "commit_sha": strings.Repeat("a", 40), "summary": "must not persist"}
	default:
		return nil
	}
}

func privateDispatchSiblingBindings(t *testing.T) (types.WorkspaceBinding, types.WorkspaceBinding) {
	t.Helper()
	root := t.TempDir()
	projectID := "00000000-0000-4000-8000-000000000001"
	return privateRoutingTestBinding(t, root+"/first", projectID, "00000000-0000-4000-8000-000000000011"),
		privateRoutingTestBinding(t, root+"/second", projectID, "00000000-0000-4000-8000-000000000012")
}

func privateDispatchTestServer(t *testing.T, actor types.ActorEnvelope, bindings ...types.WorkspaceBinding) *Server {
	t.Helper()
	server := privateRoutingTestServer(t, actor, bindings...)
	store, err := localstore.Open(t.TempDir() + "/local.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server.store = store
	server.er = localstore.NewEventRepo(store.DB())
	server.tr = localstore.NewTaskRepo(store.DB(), server.er)
	server.kb = localstore.NewKBRepo(store.DB())
	server.gr = localstore.NewGitRepo(store.DB())
	server.qr = syncpkg.NewQueueRepo(store.DB())
	server.scheduler = scheduler.NewScheduler()
	server.eventbus = eventbus.NewEventBus()
	server.httpClient = &http.Client{Transport: privateDispatchRejectTransport{}}
	server.coordServer = "http://unavailable.invalid"
	server.projectID = bindings[0].Scope.ProjectID
	server.registry = newLocalRegistry(server)
	if err := store.CacheWhoAmI(context.Background(), localstore.WhoAmICache{
		AgentID: "cached-agent", ProjectID: bindings[0].Scope.ProjectID,
		Permissions: []string{"task.create", "task.assign", "task.update_status", "channel.create", "channel.post", "kb.write", "kb.search", "git.link_commit"}, CachedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return server
}

type privateDispatchRejectTransport struct{}

func (privateDispatchRejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("network disabled in private dispatch test")
}

type privateDispatchCountingTransport struct {
	calls int
}

func (t *privateDispatchCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, fmt.Errorf("network disabled in private dispatch test")
}

func privateDispatchResult(t *testing.T, server *Server, binding types.WorkspaceBinding, name string, args map[string]any, conn net.Conn) toolCallResult {
	t.Helper()
	return privateDispatchResultContext(t, context.Background(), server, binding, name, args, conn)
}

func privateDispatchResultContext(t *testing.T, ctx context.Context, server *Server, binding types.WorkspaceBinding, name string, args map[string]any, conn net.Conn) toolCallResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args[privateRequestContextKey] = map[string]any{"working_directory": binding.Checkout.CanonicalPath}
	arguments, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(toolsCallParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	got, rpcErr := server.handleToolsCall(ctx, &mcpSession{}, conn, server.registry, params)
	if rpcErr != nil {
		t.Fatalf("handleToolsCall(%s) RPC error = %+v", name, rpcErr)
	}
	result, ok := got.(toolCallResult)
	if !ok {
		t.Fatalf("handleToolsCall(%s) result type = %T", name, got)
	}
	return result
}

func privateDispatchSuccess(t *testing.T, server *Server, binding types.WorkspaceBinding, name string, args map[string]any, conn net.Conn) map[string]any {
	t.Helper()
	return privateDispatchSuccessContext(t, context.Background(), server, binding, name, args, conn)
}

func privateDispatchSuccessContext(t *testing.T, ctx context.Context, server *Server, binding types.WorkspaceBinding, name string, args map[string]any, conn net.Conn) map[string]any {
	t.Helper()
	result := privateDispatchResultContext(t, ctx, server, binding, name, args, conn)
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("handleToolsCall(%s) = %+v", name, result)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &decoded); err != nil {
		t.Fatalf("decode handleToolsCall(%s) result %q: %v", name, result.Content[0].Text, err)
	}
	return decoded
}
