//go:build linux

package localapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/eventbus"
	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestWorkspaceDomainBuildsCanonicalChannelAndKBOperations(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "domain-operation-shape")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()

	now := time.Date(2026, 8, 27, 9, 30, 0, 123, time.UTC)
	ids := []string{
		"99999999-9999-4999-8999-999999999991",
		"99999999-9999-4999-8999-999999999992",
		"99999999-9999-4999-8999-999999999993",
		"99999999-9999-4999-8999-999999999994",
	}
	domain := newWorkspaceDomain(service, func() time.Time { return now }, func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	actor := portableTrackedActor()
	ctx := context.Background()
	initial, err := domain.View(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}

	channel, err := domain.CreateChannel(ctx, binding, actor, "release-updates")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if channel.ID != "99999999-9999-4999-8999-999999999991" || channel.Name != "release-updates" || !channel.CreatedAt.Equal(now) {
		t.Fatalf("channel = %+v", channel)
	}
	afterChannel, err := domain.View(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}

	article, err := domain.WriteArticle(ctx, binding, actor, "Portable finding", "line one\r\nline two\r\n\r\n", json.RawMessage(`{"type":"decision","nested":{"ok":true}}`))
	if err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}
	if article.ID != "99999999-9999-4999-8999-999999999993" || article.Body != "line one\nline two\n" || article.AuthorActorID != actor.PrincipalID() || !article.CreatedAt.Equal(now) || !article.UpdatedAt.Equal(now) {
		t.Fatalf("article = %+v", article)
	}
	if got := string(article.Frontmatter); got != `{"nested":{"ok":true},"type":"decision"}` {
		t.Fatalf("canonical frontmatter = %s", got)
	}

	rows, err := store.DB().QueryContext(ctx, `SELECT operation_json FROM workspace_overlay_operations ORDER BY generation`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var operations []state.OperationV1
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		operation, err := state.DecodeOperation(raw)
		if err != nil {
			t.Fatalf("DecodeOperation: %v", err)
		}
		operations = append(operations, operation)
	}
	if len(operations) != 2 {
		t.Fatalf("operations = %d, want 2", len(operations))
	}
	if operations[0].ID != "99999999-9999-4999-8999-999999999992" || operations[0].Kind != state.OperationPutRecord || operations[0].ExpectedViewDigest != initial.Digest || operations[0].Actor != actor || operations[0].PutRecord == nil || operations[0].PutRecord.Record.Channel == nil || operations[0].PutRecord.Record.Channel.Extensions == nil {
		t.Fatalf("channel operation = %+v", operations[0])
	}
	if operations[1].ID != "99999999-9999-4999-8999-999999999994" || operations[1].Kind != state.OperationPutKBArticle || operations[1].ExpectedViewDigest != afterChannel.Digest || operations[1].Actor != actor || operations[1].PutKBArticle == nil || operations[1].PutKBArticle.Record.Extensions == nil || operations[1].PutKBArticle.Record.RelatedArticleIDs == nil || operations[1].PutKBArticle.Body != article.Body {
		t.Fatalf("KB operation = %+v", operations[1])
	}
	assertPortableSurfaceHasNoLegacyRows(t, store)
}

func TestNewWorkspaceDomainRequiresProjectStateService(t *testing.T) {
	if domain, err := NewWorkspaceDomain(nil); domain != nil || !errors.Is(err, ErrWorkspaceDomainUnavailable) {
		t.Fatalf("NewWorkspaceDomain(nil) = (%v, %v), want unavailable", domain, err)
	}
}

func TestWorkspaceDomainRejectsMissingPortableActorWithoutWrites(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "domain-missing-actor")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	domain, err := NewWorkspaceDomain(service)
	if err != nil {
		t.Fatal(err)
	}
	missing := types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 9, 45, 0, 0, time.UTC),
	}
	before := workspaceMutationCounts(t, store)
	if _, err := domain.WriteArticle(context.Background(), binding, missing, "Must fail", "body", nil); !errors.Is(err, ErrPortableActorMissing) {
		t.Fatalf("WriteArticle error = %v, want ErrPortableActorMissing", err)
	}
	if after := workspaceMutationCounts(t, store); after != before {
		t.Fatalf("missing actor mutated workspace: before=%+v after=%+v", before, after)
	}
	assertPortableSurfaceHasNoLegacyRows(t, store)
}

func TestWorkspaceDomainRejectsInvalidFrontmatterAndStaleDigestWithoutPartialWrites(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "domain-preconditions")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	domain, err := NewWorkspaceDomain(service)
	if err != nil {
		t.Fatal(err)
	}
	actor := portableTrackedActor()
	ctx := context.Background()
	for name, frontmatter := range map[string]json.RawMessage{
		"duplicate": json.RawMessage(`{"type":"first","type":"second"}`),
		"array":     json.RawMessage(`["not","an","object"]`),
	} {
		t.Run(name, func(t *testing.T) {
			before := workspaceMutationCounts(t, store)
			if _, err := domain.WriteArticle(ctx, binding, actor, "Invalid metadata", "body", frontmatter); err == nil {
				t.Fatal("WriteArticle accepted invalid frontmatter")
			}
			if after := workspaceMutationCounts(t, store); after != before {
				t.Fatalf("invalid frontmatter mutated workspace: before=%+v after=%+v", before, after)
			}
		})
	}
	beforeSecret := workspaceMutationCounts(t, store)
	if _, err := domain.WriteArticle(ctx, binding, actor, "Secret metadata", "body", json.RawMessage(`{"token":"must-not-track"}`)); !errors.Is(err, state.ErrTrackedSecret) {
		t.Fatalf("tracked secret error = %v, want ErrTrackedSecret", err)
	}
	if after := workspaceMutationCounts(t, store); after != beforeSecret {
		t.Fatalf("tracked secret mutated workspace: before=%+v after=%+v", beforeSecret, after)
	}

	initial, err := domain.View(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.CreateChannel(ctx, binding, actor, "advances-digest"); err != nil {
		t.Fatal(err)
	}
	beforeStale := workspaceMutationCounts(t, store)
	_, err = domain.Apply(ctx, binding, actor, state.OperationV1{
		SchemaVersion: 1, ID: "88888888-8888-4888-8888-888888888881", Kind: state.OperationPutRecord,
		ExpectedViewDigest: initial.Digest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Channel: &state.ChannelV1{
			SchemaVersion: 1, Kind: "channel", ID: "88888888-8888-4888-8888-888888888882",
			Name: "stale", CreatedAt: time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), Extensions: state.ExtensionsV1{},
		}}},
	})
	if !errors.Is(err, state.ErrOperationPrecondition) {
		t.Fatalf("stale Apply error = %v, want ErrOperationPrecondition", err)
	}
	if after := workspaceMutationCounts(t, store); after != beforeStale {
		t.Fatalf("stale operation mutated workspace: before=%+v after=%+v", beforeStale, after)
	}
	assertPortableSurfaceHasNoLegacyRows(t, store)
}

func TestWorkspaceDomainOmitsTombstonedPortableRecords(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "domain-tombstone")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	domain, err := NewWorkspaceDomain(service)
	if err != nil {
		t.Fatal(err)
	}
	actor := portableTrackedActor()
	view, err := domain.View(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	record := view.Channels["55555555-5555-4555-8555-555555555555"]
	digest, err := state.DigestCanonicalJSON(*record.Value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = domain.Apply(context.Background(), binding, actor, state.OperationV1{
		SchemaVersion: 1, ID: "99999999-9999-4999-8999-999999999995", Kind: state.OperationTombstone,
		ExpectedViewDigest: view.Digest, Actor: actor,
		Tombstone: &state.TombstoneOperationV1{Key: state.RecordKey{Kind: "channel", ID: record.Value.ID}, ExpectedContentDigest: digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	channels, err := domain.ListChannels(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range channels {
		if channel.ID == record.Value.ID {
			t.Fatalf("ListChannels returned tombstoned channel %+v", channel)
		}
	}
}

func portableTrackedActor() types.ActorEnvelope {
	return types.ActorEnvelope{
		ActorKind: types.ActorHuman, HumanPrincipalID: "11111111-1111-4111-8111-111111111111",
		Assurance: types.AssuranceLocal, OccurredAt: time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC),
	}
}

func assertPortableSurfaceHasNoLegacyRows(t *testing.T, store interface{ DB() *sql.DB }) {
	t.Helper()
	wantZero := []string{"channels", "kb_articles", "sync_queue"}
	for _, table := range wantZero {
		var count int
		if err := store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("legacy %s rows = %d, err=%v; want zero", table, count, err)
		}
	}
}

func TestWorkspaceDomainSortsPortableReadsDeterministically(t *testing.T) {
	fixture := newPortableLoopGitFixture(t)
	root := fixture.clone(t, "domain-sort")
	store, service, binding := openPortableLoopWorkspace(t, root, filepath.Join(t.TempDir(), "private.db"))
	defer store.Close()
	ids := []string{
		"99999999-9999-4999-8999-999999999996", "99999999-9999-4999-8999-999999999997",
		"99999999-9999-4999-8999-999999999998", "99999999-9999-4999-8999-999999999999",
	}
	domain := newWorkspaceDomain(service, func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) }, func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	})
	actor := portableTrackedActor()
	if _, err := domain.CreateChannel(context.Background(), binding, actor, "aaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := domain.WriteArticle(context.Background(), binding, actor, "AAA", "body", nil); err != nil {
		t.Fatal(err)
	}
	channels, err := domain.ListChannels(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	articles, err := domain.ListArticles(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	channelNames := []string{channels[0].Name, channels[1].Name}
	articleTitles := []string{articles[0].Title, articles[1].Title}
	if !reflect.DeepEqual(channelNames, []string{"aaa", "general"}) || !reflect.DeepEqual(articleTitles, []string{"AAA", "Portable state"}) {
		t.Fatalf("sorted channels=%v articles=%v", channelNames, articleTitles)
	}
}

func TestPortableChannelAndKBRoundTripAcrossTwoRealClones(t *testing.T) {
	ctx := context.Background()
	fixture := newPortableLoopGitFixture(t)
	cloneOne := fixture.clone(t, "portable-surface-one")
	privateOne := filepath.Join(t.TempDir(), "clone-one.db")
	storeOne, serviceOne, bindingOne := openPortableLoopWorkspace(t, cloneOne, privateOne)
	serverOne := newPortableSurfaceTestServer(t, storeOne, serviceOne)
	actor := portableTrackedActor()
	callContext := withServerOwnedActor(WithResolvedBinding(ctx, bindingOne), actor)

	channel, err := serverOne.handleChannelCreate(callContext, json.RawMessage(`{"name":"portable-review"}`))
	if err != nil {
		t.Fatalf("create portable channel: %v", err)
	}
	channelID := channel["id"].(string)
	article, err := serverOne.handleKBWrite(callContext, json.RawMessage(`{"title":"Portable decision","body":"clone A\r\n","frontmatter":{"type":"decision"}}`))
	if err != nil {
		t.Fatalf("write portable KB: %v", err)
	}
	articleID := article["id"].(string)
	activity, err := serverOne.handleChannelPost(callContext, json.RawMessage(`{"channel_id":"`+channelID+`","event_type":"review.ready","payload":{"clone":"A"}}`))
	if err != nil {
		t.Fatalf("post operational activity: %v", err)
	}
	activityID := activity["id"].(string)
	viewAfterActivity, err := serviceOne.View(ctx, bindingOne.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, portable := viewAfterActivity.Snapshot.Events[activityID]; portable {
		t.Fatalf("operational activity %s entered portable EventV1 state", activityID)
	}
	assertPortableSurfaceHasNoLegacyRows(t, storeOne)
	var activityRows int
	if err := storeOne.DB().QueryRow(`SELECT count(*) FROM events WHERE id=?`, activityID).Scan(&activityRows); err != nil || activityRows != 1 {
		t.Fatalf("clone A activity rows=%d err=%v, want one", activityRows, err)
	}

	// A real private-state restart must retain the candidate and clone-local
	// activity without consulting legacy Channel/KB definitions.
	if err := storeOne.Close(); err != nil {
		t.Fatal(err)
	}
	storeOne, err = localstore.Open(privateOne)
	if err != nil {
		t.Fatal(err)
	}
	defer storeOne.Close()
	serviceOne, err = projectstate.NewService(localstore.NewWorkspaceRepo(storeOne.DB()), projectstate.ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	bindingOne, err = serviceOne.ResolveWorkingDirectory(ctx, types.WorkspaceContext{WorkingDirectory: cloneOne})
	if err != nil {
		t.Fatal(err)
	}
	serverOne = newPortableSurfaceTestServer(t, storeOne, serviceOne)
	callContext = withServerOwnedActor(WithResolvedBinding(ctx, bindingOne), actor)
	channelsAfterRestart, err := serverOne.localListChannels(callContext, nil)
	if err != nil || !resultSliceContainsID(channelsAfterRestart["channels"], channelID) {
		t.Fatalf("channels after restart = (%+v, %v)", channelsAfterRestart, err)
	}
	articleAfterRestart, err := serverOne.localGetArticle(callContext, json.RawMessage(`{"article_id":"`+articleID+`"}`))
	if err != nil || articleAfterRestart["body"] != "clone A\n" || articleAfterRestart["author_actor_id"] != actor.PrincipalID() {
		t.Fatalf("article after restart = (%+v, %v)", articleAfterRestart, err)
	}
	activityAfterRestart, err := serverOne.localListChannelEvents(callContext, json.RawMessage(`{"project_id":"`+bindingOne.Scope.ProjectID+`"}`))
	if err != nil || !resultSliceContainsID(activityAfterRestart["events"], activityID) {
		t.Fatalf("activity after restart = (%+v, %v)", activityAfterRestart, err)
	}
	assertPortableSurfaceHasNoLegacyRows(t, storeOne)

	publication, err := serviceOne.PublicationConfiguration(ctx, bindingOne.Scope)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest, err := projectstate.DigestPublicationBindingConstraint(bindingOne.Repository, publication.ObservedOriginDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serviceOne.ReconfigurePublication(ctx, projectstate.ReconfigurePublicationRequest{
		Scope: bindingOne.Scope, ExpectedBinding: bindingOne, ExpectedPublicationBindingDigest: bindingDigest,
		Expected: publication, Classification: types.PublicationPublicGit, Actor: actor,
	}); err != nil {
		t.Fatalf("configure publication: %v", err)
	}
	diff, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{Operation: WorkspaceOperationDiff})
	if err != nil || diff.Diff == nil || diff.Diff.PublicationReviewDigest == "" {
		t.Fatalf("portable surface diff = (%+v, %v)", diff, err)
	}
	if _, err := serverOne.executeWorkspaceCommand(callContext, WorkspaceCommandRequest{
		Operation: WorkspaceOperationCheckpoint, PublicationReviewDigest: string(diff.Diff.PublicationReviewDigest),
	}); err != nil {
		t.Fatalf("portable surface checkpoint: %v", err)
	}
	gitRun(t, cloneOne, "config", "user.name", "Portable Surface Fixture")
	gitRun(t, cloneOne, "config", "user.email", "fixture@example.test")
	gitRun(t, cloneOne, "add", ".wormhole/state/v1")
	gitRun(t, cloneOne, "commit", "-m", "test: accept portable channel and KB")
	gitRun(t, cloneOne, "push", "origin", "HEAD:main")
	grep := exec.Command("git", "-C", cloneOne, "grep", "-n", activityID, "HEAD", "--", ".wormhole/state/v1")
	if output, err := grep.CombinedOutput(); err == nil {
		t.Fatalf("operational activity crossed checkpoint: %s", output)
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("inspect checkpoint for operational activity: %v: %s", err, output)
	}

	cloneTwo := fixture.clone(t, "portable-surface-two")
	storeTwo, serviceTwo, bindingTwo := openPortableLoopWorkspace(t, cloneTwo, filepath.Join(t.TempDir(), "clone-two.db"))
	defer storeTwo.Close()
	serverTwo := newPortableSurfaceTestServer(t, storeTwo, serviceTwo)
	cloneTwoContext := withServerOwnedActor(WithResolvedBinding(ctx, bindingTwo), actor)
	channelsTwo, err := serverTwo.localListChannels(cloneTwoContext, nil)
	if err != nil || !resultSliceContainsID(channelsTwo["channels"], channelID) {
		t.Fatalf("clone B channels = (%+v, %v)", channelsTwo, err)
	}
	articleTwo, err := serverTwo.localGetArticle(cloneTwoContext, json.RawMessage(`{"article_id":"`+articleID+`"}`))
	if err != nil || articleTwo["title"] != articleAfterRestart["title"] || articleTwo["body"] != articleAfterRestart["body"] || string(articleTwo["frontmatter"].(json.RawMessage)) != string(articleAfterRestart["frontmatter"].(json.RawMessage)) || articleTwo["author_actor_id"] != articleAfterRestart["author_actor_id"] {
		t.Fatalf("clone B article = (%+v, %v), want %+v", articleTwo, err, articleAfterRestart)
	}
	eventsTwo, err := serverTwo.localListChannelEvents(cloneTwoContext, json.RawMessage(`{"project_id":"`+bindingTwo.Scope.ProjectID+`"}`))
	if err != nil || resultSliceContainsID(eventsTwo["events"], activityID) {
		t.Fatalf("clone B operational events = (%+v, %v)", eventsTwo, err)
	}
	assertPortableSurfaceHasNoLegacyRows(t, storeTwo)
	viewTwo, err := serviceTwo.View(ctx, bindingTwo.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, portable := viewTwo.Snapshot.Events[activityID]; portable {
		t.Fatalf("clone B reconstructed operational activity %s as portable state", activityID)
	}
}

func newPortableSurfaceTestServer(t *testing.T, store *localstore.Store, service *projectstate.Service) *Server {
	t.Helper()
	domain, err := NewWorkspaceDomain(service)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		projectState: service, workspaceDomain: domain, store: store,
		er: localstore.NewEventRepo(store.DB()), eventbus: eventbus.NewEventBus(),
	}
}

func resultSliceContainsID(raw interface{}, id string) bool {
	items, ok := raw.([]interface{})
	if !ok {
		return false
	}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]interface{})
		if ok && item["id"] == id {
			return true
		}
	}
	return false
}
