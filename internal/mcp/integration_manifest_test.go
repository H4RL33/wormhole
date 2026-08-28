package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/H4RL33/wormhole/internal/core/identity"
)

func TestIntegrationManifestStorePreservesAuthorisedProjectHistory(t *testing.T) {
	db := testDB(t)
	store := NewIntegrationManifestStore(db)
	projectA := mustCreateProject(t, "integration-manifest-history-a")
	projectB := mustCreateProject(t, "integration-manifest-history-b")
	agentA, _ := mustRegisterAgentWithPerms(t, projectA, []string{IntegrationManifestPublishPermission, IntegrationManifestRevokePermission})
	agentB, _ := mustRegisterAgentWithPerms(t, projectB, []string{IntegrationManifestPublishPermission})
	scopeA := &identity.AuthenticatedScope{Agent: identity.Agent{ID: agentA}, ProjectID: projectA, Permissions: []string{IntegrationManifestPublishPermission, IntegrationManifestRevokePermission}}
	scopeB := &identity.AuthenticatedScope{Agent: identity.Agent{ID: agentB}, ProjectID: projectB, Permissions: []string{IntegrationManifestPublishPermission}}

	first := readFabricManifestFixture(t, "valid.json")
	first.ProjectID = projectA
	if _, err := store.Publish(context.Background(), scopeA, first); err != nil {
		t.Fatalf("publish v1: %v", err)
	}
	second := first
	second.ManifestVersion = 2
	second.ManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	second.Entries = append([]IntegrationManifestEntry(nil), first.Entries...)
	second.Entries[0].Content = "Use approved version two guidance.\n"
	second.Entries[0].ContentDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := store.Publish(context.Background(), scopeA, second); err != nil {
		t.Fatalf("publish v2: %v", err)
	}

	history, err := store.History(context.Background(), projectA)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Manifest.ManifestVersion != 1 || history[1].Manifest.ManifestVersion != 2 {
		t.Fatalf("history = %+v", history)
	}
	if history[0].Manifest.Entries[0].Content != first.Entries[0].Content ||
		!reflect.DeepEqual(history[0].Manifest.RoleFilters, []string{"contributor"}) {
		t.Fatalf("historical content/roles changed: %+v", history[0])
	}
	if _, err := db.ExecContext(context.Background(), `
		UPDATE integration_manifest_versions SET entries = '[]'::jsonb
		WHERE project_id = $1 AND manifest_id = $2 AND manifest_version = 1`, projectA, first.ManifestID); err == nil {
		t.Fatal("direct historical content mutation unexpectedly succeeded")
	}
	if _, err := store.Current(context.Background(), projectB); !errors.Is(err, ErrIntegrationManifestNotFound) {
		t.Fatalf("cross-project current = %v", err)
	}
	if _, err := store.Publish(context.Background(), scopeB, second); !errors.Is(err, ErrIntegrationManifestProject) {
		t.Fatalf("cross-project publish = %v", err)
	}
	unauthorised := *scopeA
	unauthorised.Permissions = nil
	third := second
	third.ManifestVersion = 3
	third.ManifestDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := store.Publish(context.Background(), &unauthorised, third); !errors.Is(err, ErrIntegrationManifestPermission) {
		t.Fatalf("unauthorised publish = %v", err)
	}
	equivocation := first
	equivocation.ManifestDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := store.Publish(context.Background(), scopeA, equivocation); !errors.Is(err, ErrIntegrationManifestEquivocation) {
		t.Fatalf("same version/different digest = %v", err)
	}
}

func TestIntegrationManifestStoreRejectsProhibitedKindsAndAuthorisesRevocation(t *testing.T) {
	db := testDB(t)
	store := NewIntegrationManifestStore(db)
	projectID := mustCreateProject(t, "integration-manifest-revocation")
	agentID, _ := mustRegisterAgentWithPerms(t, projectID, []string{IntegrationManifestPublishPermission, IntegrationManifestRevokePermission})
	scope := &identity.AuthenticatedScope{Agent: identity.Agent{ID: agentID}, ProjectID: projectID, Permissions: []string{IntegrationManifestPublishPermission, IntegrationManifestRevokePermission}}
	manifest := readFabricManifestFixture(t, "valid.json")
	manifest.ProjectID = projectID

	prohibited := manifest
	prohibited.ManifestID = "2a8ef657-9815-49d0-9b3f-6f3dd4fd2be6"
	prohibited.Entries = append([]IntegrationManifestEntry(nil), manifest.Entries...)
	prohibited.Entries[0].Kind = "executable"
	if _, err := store.Publish(context.Background(), scope, prohibited); !errors.Is(err, ErrIntegrationManifestInvalid) {
		t.Fatalf("prohibited kind publish = %v", err)
	}
	if _, err := store.Publish(context.Background(), scope, manifest); err != nil {
		t.Fatal(err)
	}
	unauthorised := *scope
	unauthorised.Permissions = []string{IntegrationManifestPublishPermission}
	if _, err := store.Revoke(context.Background(), &unauthorised, projectID, manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest); !errors.Is(err, ErrIntegrationManifestPermission) {
		t.Fatalf("unauthorised revoke = %v", err)
	}
	change, err := store.Revoke(context.Background(), scope, projectID, manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if change.Operation != IntegrationManifestRevoked || change.Manifest != nil || change.ManifestDigest != manifest.ManifestDigest {
		t.Fatalf("revocation change = %+v", change)
	}
	history, err := store.History(context.Background(), projectID)
	if err != nil || len(history) != 1 || history[0].RevokedAt == nil || history[0].Manifest.Entries[0].Content != manifest.Entries[0].Content {
		t.Fatalf("revoked history = %+v, err %v", history, err)
	}
}

func readFabricManifestFixture(t *testing.T, name string) IntegrationManifest {
	t.Helper()
	data, err := os.ReadFile("../../testdata/alpha/manifests/fabric/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var manifest IntegrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
