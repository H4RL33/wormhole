package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenSupportsRelativeSQLitePaths(t *testing.T) {
	t.Chdir(t.TempDir())
	store, err := Open("wormholed.db")
	if err != nil {
		t.Fatalf("Open(relative path): %v", err)
	}
	defer store.Close()

	if err := store.CacheWhoAmI(context.Background(), WhoAmICache{
		AgentID:   "relative-path-agent",
		ProjectID: "project",
		CachedAt:  time.Date(2026, 7, 23, 9, 8, 7, 6, time.UTC),
	}); err != nil {
		t.Fatalf("CacheWhoAmI: %v", err)
	}
}

func TestEnrolmentAttemptPersistsAndResumesAcrossStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wormholed.db")
	ctx := context.Background()
	firstKey := "018f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open first: %v", err)
	}
	first, created, err := store.ResolveEnrolmentAttempt(ctx, EnrolmentAttemptRecord{
		ProjectID: "project-a", IdempotencyKey: firstKey, RequestHash: strings.Repeat("a", 64),
		State: "requested", CredentialProfile: "project-a__contributor",
	})
	if err != nil || !created || first.IdempotencyKey != firstKey {
		t.Fatalf("first resolve: created=%v key=%q err=%v", created, first.IdempotencyKey, err)
	}
	if err := store.UpdateEnrolmentAttempt(ctx, first, "registered", "agent-a", "passport-a", false); err != nil {
		t.Fatalf("update registered attempt: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatalf("Open second: %v", err)
	}
	defer store.Close()
	resumed, created, err := store.ResolveEnrolmentAttempt(ctx, EnrolmentAttemptRecord{
		ProjectID: "project-a", IdempotencyKey: "118f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1", RequestHash: strings.Repeat("a", 64),
		State: "requested", CredentialProfile: "project-a__contributor",
	})
	if err != nil || created {
		t.Fatalf("resume: created=%v err=%v", created, err)
	}
	if resumed.IdempotencyKey != firstKey || resumed.State != "registered" || resumed.AgentID != "agent-a" || resumed.PassportID != "passport-a" {
		t.Fatalf("resumed refs: key=%q state=%q agent=%q passport=%q", resumed.IdempotencyKey, resumed.State, resumed.AgentID, resumed.PassportID)
	}

	_, _, err = store.ResolveEnrolmentAttempt(ctx, EnrolmentAttemptRecord{
		ProjectID: "project-a", IdempotencyKey: "218f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1", RequestHash: strings.Repeat("b", 64),
		State: "requested", CredentialProfile: "project-a__contributor",
	})
	if !errors.Is(err, ErrEnrolmentAttemptConflict) {
		t.Fatalf("digest conflict error = %v, want ErrEnrolmentAttemptConflict", err)
	}

	rows, err := store.DB().QueryContext(ctx, `PRAGMA table_info(enrolment_attempts)`)
	if err != nil {
		t.Fatalf("inspect enrolment_attempts: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan enrolment_attempts column: %v", err)
		}
		if strings.Contains(strings.ToLower(name), "token") {
			t.Fatalf("enrolment_attempts has secret-bearing column %q", name)
		}
	}
}

func TestReadyEnrolmentAttemptResumesWithNewCandidateKey(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	first, created, err := store.ResolveEnrolmentAttempt(ctx, EnrolmentAttemptRecord{
		ProjectID: "project-ready", IdempotencyKey: "018f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1", RequestHash: strings.Repeat("a", 64),
		State: "requested", CredentialProfile: "project-ready__contributor",
	})
	if err != nil || !created {
		t.Fatalf("create attempt: created=%v err=%v", created, err)
	}
	if err := store.UpdateEnrolmentAttempt(ctx, first, "ready", "agent-ready", "passport-ready", true); err != nil {
		t.Fatal(err)
	}
	resumed, created, err := store.ResolveEnrolmentAttempt(ctx, EnrolmentAttemptRecord{
		ProjectID: "project-ready", IdempotencyKey: "118f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1", RequestHash: strings.Repeat("a", 64),
		State: "requested", CredentialProfile: "project-ready__contributor",
	})
	if err != nil || created {
		t.Fatalf("resolve ready attempt: created=%v err=%v", created, err)
	}
	if resumed.IdempotencyKey != first.IdempotencyKey || resumed.State != "ready" || !resumed.Terminal || resumed.PassportID != "passport-ready" {
		t.Fatalf("resumed attempt = %+v", resumed)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT count(*) FROM enrolment_attempts WHERE project_id = ?`, first.ProjectID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("attempt count=%d err=%v, want 1", count, err)
	}
}

func TestActiveEnrolmentAttemptLookupCannotExposeAnotherProject(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	profile := "shared-profile"
	projectA := EnrolmentAttemptRecord{
		ProjectID: "project-a", IdempotencyKey: "018f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1",
		RequestHash: strings.Repeat("a", 64), State: "requested", CredentialProfile: profile,
	}
	if _, created, err := store.ResolveEnrolmentAttempt(ctx, projectA); err != nil || !created {
		t.Fatalf("create project A attempt: created=%v err=%v", created, err)
	}
	if _, err := store.getActiveEnrolmentAttempt(ctx, "project-b", profile); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project B lookup error = %v, want ErrNotFound", err)
	}
	projectB := projectA
	projectB.ProjectID = "project-b"
	projectB.IdempotencyKey = "118f47a2-7b1d-7e42-8d4b-1c99c6a8f2b1"
	got, created, err := store.ResolveEnrolmentAttempt(ctx, projectB)
	if !errors.Is(err, ErrEnrolmentAttemptConflict) || created {
		t.Fatalf("project B resolve: created=%v err=%v, want conflict", created, err)
	}
	if got.ProjectID != "" || got.IdempotencyKey != "" || got.RequestHash != "" || got.CredentialProfile != "" {
		t.Fatalf("cross-project conflict exposed record fields: project=%q key=%q hash_empty=%v profile=%q",
			got.ProjectID, got.IdempotencyKey, got.RequestHash == "", got.CredentialProfile)
	}
}

func TestCacheAndGetWhoAmI(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	want := WhoAmICache{
		AgentID:      "agent-1",
		Owner:        "harley",
		Model:        "claude-sonnet-5",
		Capabilities: []string{"code", "review"},
		ProjectID:    "project-1",
		Permissions:  []string{"read_kb", "create_task"},
		CachedAt:     time.Date(2026, 7, 23, 9, 8, 7, 123456789, time.FixedZone("west", -5*60*60)),
	}

	if err := store.CacheWhoAmI(ctx, want); err != nil {
		t.Fatalf("CacheWhoAmI: %v", err)
	}

	got, err := store.GetCachedWhoAmI(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if got.AgentID != want.AgentID || got.Owner != want.Owner || got.Model != want.Model ||
		got.ProjectID != want.ProjectID || !got.CachedAt.Equal(want.CachedAt) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "code" || got.Capabilities[1] != "review" {
		t.Fatalf("capabilities mismatch: got %v", got.Capabilities)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "read_kb" || got.Permissions[1] != "create_task" {
		t.Fatalf("permissions mismatch: got %v", got.Permissions)
	}
}

func TestGetCachedWhoAmI_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	_, err = store.GetCachedWhoAmI(context.Background(), "no-such-agent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got err %v, want ErrNotFound", err)
	}
}

func TestCacheWhoAmI_Overwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	first := WhoAmICache{AgentID: "agent-1", Owner: "harley", Model: "claude-sonnet-5", ProjectID: "project-1", CachedAt: time.Now().UTC().Truncate(time.Second)}
	if err := store.CacheWhoAmI(ctx, first); err != nil {
		t.Fatalf("CacheWhoAmI (first): %v", err)
	}
	second := first
	second.Model = "claude-opus-4-8"
	if err := store.CacheWhoAmI(ctx, second); err != nil {
		t.Fatalf("CacheWhoAmI (second): %v", err)
	}

	got, err := store.GetCachedWhoAmI(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetCachedWhoAmI: %v", err)
	}
	if got.Model != "claude-opus-4-8" {
		t.Fatalf("got model %q, want overwrite to take effect", got.Model)
	}
}

func TestWhoAmICache_SameAgentKeepsIndependentProjectScopes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, cached := range []WhoAmICache{
		{AgentID: "shared-agent", ProjectID: "project-a", Permissions: []string{"task.create"}, CachedAt: time.Now().UTC()},
		{AgentID: "shared-agent", ProjectID: "project-b", Permissions: []string{"kb.write"}, CachedAt: time.Now().UTC().Add(time.Second)},
	} {
		if err := store.CacheWhoAmI(ctx, cached); err != nil {
			t.Fatalf("CacheWhoAmI(%s): %v", cached.ProjectID, err)
		}
	}
	a, err := store.GetCachedWhoAmIForProject(ctx, "project-a")
	if err != nil || len(a.Permissions) != 1 || a.Permissions[0] != "task.create" {
		t.Fatalf("project A cache = %+v err=%v", a, err)
	}
	b, err := store.GetCachedWhoAmIForProject(ctx, "project-b")
	if err != nil || len(b.Permissions) != 1 || b.Permissions[0] != "kb.write" {
		t.Fatalf("project B cache = %+v err=%v", b, err)
	}
}

func TestEventRepoGetChannelRespectsNamespace(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := NewEventRepo(store.DB())
	channelID, err := events.CreateChannel(ctx, "project-a", "engineering")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}

	name, err := events.GetChannel(ctx, "project-a", channelID)
	if err != nil {
		t.Fatalf("GetChannel(project-a): %v", err)
	}
	if name != "engineering" {
		t.Fatalf("GetChannel name = %q, want engineering", name)
	}

	for _, namespaceID := range []string{"project-b", "project-a"} {
		id := channelID
		if namespaceID == "project-a" {
			id = "missing"
		}
		if _, err := events.GetChannel(ctx, namespaceID, id); !errors.Is(err, ErrEventNotFound) {
			t.Fatalf("GetChannel(%q, %q) error = %v, want ErrEventNotFound", namespaceID, id, err)
		}
	}
}

func TestOperationalEventDoesNotRequireLegacyChannelDefinition(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "operational-event.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	repo := NewEventRepo(store.DB())
	event, err := repo.PublishOperationalEvent(ctx, "workspace-a", "11111111-1111-4111-8111-111111111111", "actor-a", "review.ready", json.RawMessage(`{"ready":true}`), nil)
	if err != nil {
		t.Fatalf("PublishOperationalEvent: %v", err)
	}
	if event.NamespaceID != "workspace-a" || event.ChannelID != "11111111-1111-4111-8111-111111111111" || event.AgentID != "actor-a" {
		t.Fatalf("operational event = %+v", event)
	}

	var channels, events int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM channels`).Scan(&channels); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM events WHERE namespace_id=? AND channel_id=?`, event.NamespaceID, event.ChannelID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if channels != 0 || events != 1 {
		t.Fatalf("legacy channels=%d operational events=%d, want 0/1", channels, events)
	}
}

func TestTaskRepoAssignPersistsOnlyInItsNamespace(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := NewEventRepo(store.DB())
	tasks := NewTaskRepo(store.DB(), events)
	task, err := tasks.CreateTask(ctx, "project-a", "route me", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	assigned, err := tasks.Assign(ctx, "project-a", task.ID, "agent-a")
	if err != nil {
		t.Fatalf("Assign(project-a): %v", err)
	}
	if assigned.OwnerAgentID == nil || *assigned.OwnerAgentID != "agent-a" {
		t.Fatalf("Assign owner = %v, want agent-a", assigned.OwnerAgentID)
	}

	persisted, err := tasks.GetTask(ctx, "project-a", task.ID)
	if err != nil {
		t.Fatalf("GetTask(project-a): %v", err)
	}
	if persisted.OwnerAgentID == nil || *persisted.OwnerAgentID != "agent-a" {
		t.Fatalf("persisted owner = %v, want agent-a", persisted.OwnerAgentID)
	}

	if _, err := tasks.Assign(ctx, "project-b", task.ID, "agent-b"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Assign(project-b) error = %v, want ErrTaskNotFound", err)
	}
	persisted, err = tasks.GetTask(ctx, "project-a", task.ID)
	if err != nil {
		t.Fatalf("GetTask after cross-namespace assign: %v", err)
	}
	if persisted.OwnerAgentID == nil || *persisted.OwnerAgentID != "agent-a" {
		t.Fatalf("cross-namespace Assign changed owner to %v", persisted.OwnerAgentID)
	}
}

func TestLocalRepositoriesPreserveDurableTaskEventAndKBState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	events := NewEventRepo(store.DB())
	tasks := NewTaskRepo(store.DB(), events)
	kb := NewKBRepo(store.DB())
	channelID, err := events.CreateChannel(ctx, "project-a", "general")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	task, err := tasks.CreateTask(ctx, "project-a", "build coverage", "exercise durable methods", nil, 2, nil)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	updated, err := tasks.UpdateStatus(ctx, "project-a", task.ID, "wip", channelID, "agent-a")
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if updated.Status != "wip" {
		t.Fatalf("updated status = %q, want wip", updated.Status)
	}
	if _, err := tasks.UpdateStatus(ctx, "project-a", task.ID, "todo", channelID, "agent-a"); err == nil {
		t.Fatal("UpdateStatus accepted an illegal wip -> todo transition")
	}
	status := "wip"
	listedTasks, err := tasks.ListTasks(ctx, "project-a", &status)
	if err != nil || len(listedTasks) != 1 || listedTasks[0].ID != task.ID {
		t.Fatalf("ListTasks(wip) = %+v, err=%v", listedTasks, err)
	}

	event, err := events.PublishEvent(ctx, "project-a", channelID, "agent-a", "coverage.checked", json.RawMessage(`{"passed":true}`), nil)
	if err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	gotEvent, err := events.GetEvent(ctx, "project-a", event.ID)
	if err != nil || gotEvent.EventType != "coverage.checked" {
		t.Fatalf("GetEvent = %+v, err=%v", gotEvent, err)
	}
	listedEvents, err := events.ListEvents(ctx, "project-a", channelID, 10, 0)
	if err != nil || len(listedEvents) != 2 {
		t.Fatalf("ListEvents = %+v, err=%v", listedEvents, err)
	}
	projectEvents, err := events.ListEventsByNamespace(ctx, "project-a", 10, 0)
	if err != nil || len(projectEvents) != 2 {
		t.Fatalf("ListEventsByNamespace = %+v, err=%v", projectEvents, err)
	}
	channels, err := events.ListChannels(ctx, "project-a")
	if err != nil || len(channels) != 1 || channels[0].ID != channelID {
		t.Fatalf("ListChannels = %+v, err=%v", channels, err)
	}

	first, err := kb.WriteArticle(ctx, "project-a", "agent-a", "first", "first body", json.RawMessage(`{"kind":"note"}`))
	if err != nil {
		t.Fatalf("WriteArticle(first): %v", err)
	}
	second, err := kb.WriteArticle(ctx, "project-a", "agent-a", "second", "second body", nil)
	if err != nil {
		t.Fatalf("WriteArticle(second): %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO kb_links (id, namespace_id, from_article_id, to_article_id) VALUES (?, ?, ?, ?)`, "link-1", "project-a", first.ID, second.ID); err != nil {
		t.Fatalf("insert link fixture: %v", err)
	}
	gotArticle, err := kb.GetArticle(ctx, "project-a", first.ID)
	if err != nil || gotArticle.Title != "first" {
		t.Fatalf("GetArticle = %+v, err=%v", gotArticle, err)
	}
	articles, err := kb.ListArticles(ctx, "project-a")
	if err != nil || len(articles) != 2 {
		t.Fatalf("ListArticles = %+v, err=%v", articles, err)
	}
	links, err := kb.GetArticleLinks(ctx, "project-a", first.ID)
	if err != nil || len(links) != 1 || links[0].ToArticleID != second.ID {
		t.Fatalf("GetArticleLinks = %+v, err=%v", links, err)
	}
}

func TestIntegrationManifestSchemaExistsAfterOpen(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	for _, table := range []string{
		"integration_manifest_bodies",
		"integration_manifest_project_state",
		"integration_manifest_journal",
		"integration_manifest_audit",
	} {
		var found string
		err := store.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found)
		if err != nil {
			t.Fatalf("required integration manifest table %q is absent: %v", table, err)
		}
	}
}

func TestIntegrationManifestSchemaBodyIsImmutableAndRejectsEquivocation(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	db := store.DB()
	if _, err := db.Exec(`INSERT INTO integration_manifest_bodies (project_id, manifest_id, manifest_version, digest, body) VALUES (?, ?, ?, ?, ?)`, "project-a", "manifest-1", 1, "digest-a", `{"schema_version":1}`); err != nil {
		t.Fatalf("insert verified manifest body: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO integration_manifest_bodies (project_id, manifest_id, manifest_version, digest, body) VALUES (?, ?, ?, ?, ?)`, "project-a", "manifest-1", 1, "digest-b", `{"schema_version":1,"changed":true}`); err == nil {
		t.Fatal("equivocating manifest body insert succeeded")
	}
	if _, err := db.Exec(`UPDATE integration_manifest_bodies SET digest = ?, body = ? WHERE project_id = ? AND manifest_id = ? AND manifest_version = ?`, "digest-b", `{"schema_version":1,"changed":true}`, "project-a", "manifest-1", 1); err == nil {
		t.Fatal("verified manifest body update succeeded")
	}
}

func TestIntegrationManifestSchemaAuditIsAppendOnly(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	db := store.DB()
	if _, err := db.Exec(`INSERT INTO integration_manifest_audit (project_id, id, action, payload) VALUES (?, ?, ?, ?)`, "project-a", "audit-1", "verified", `{}`); err != nil {
		t.Fatalf("insert audit record: %v", err)
	}
	if _, err := db.Exec(`UPDATE integration_manifest_audit SET action = ? WHERE project_id = ? AND id = ?`, "altered", "project-a", "audit-1"); err == nil {
		t.Fatal("audit UPDATE succeeded")
	}
	if _, err := db.Exec(`DELETE FROM integration_manifest_audit WHERE project_id = ? AND id = ?`, "project-a", "audit-1"); err == nil {
		t.Fatal("audit DELETE succeeded")
	}
}

func TestIntegrationManifestSchemaProjectScopedKeysPermitSameIDs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	db := store.DB()
	for _, projectID := range []string{"project-a", "project-b"} {
		if _, err := db.Exec(`INSERT INTO integration_manifest_bodies (project_id, manifest_id, manifest_version, digest, body) VALUES (?, ?, ?, ?, ?)`, projectID, "manifest-1", 1, "digest-"+projectID, `{"schema_version":1}`); err != nil {
			t.Fatalf("insert %s manifest: %v", projectID, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM integration_manifest_bodies WHERE manifest_id = ? AND manifest_version = ?`, "manifest-1", 1).Scan(&count); err != nil {
		t.Fatalf("count project-scoped manifest IDs: %v", err)
	}
	if count != 2 {
		t.Fatalf("manifest IDs shared across projects count = %d, want 2", count)
	}
}
