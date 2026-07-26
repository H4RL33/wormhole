package localapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/runtime/localstore"
	"github.com/google/uuid"
)

const maxIntegrationManifestVersion = int64(9007199254740991)

var (
	ErrIntegrationManifestVerification = errors.New("integration manifest verification failed")
	ErrIntegrationManifestEquivocation = errors.New("integration manifest equivocation")
	ErrIntegrationManifestReplay       = errors.New("integration manifest replay")
	ErrIntegrationManifestOffline      = errors.New("integration manifest operation requires an online binding")
	ErrIntegrationManifestUnavailable  = errors.New("integration manifest candidate unavailable")
)

// IntegrationManifestChange is the strict Gateway-side representation of the
// Fabric bootstrap/incremental wire record. Offered changes include a body;
// authenticated revocations deliberately carry a null body.
type IntegrationManifestChange struct {
	Operation       string               `json:"operation"`
	ProjectID       string               `json:"project_id"`
	ManifestID      string               `json:"manifest_id"`
	ManifestVersion int64                `json:"manifest_version"`
	ManifestDigest  string               `json:"manifest_digest"`
	Manifest        *IntegrationManifest `json:"manifest"`
	ChangedAt       string               `json:"changed_at"`
}

// IntegrationManifestBinding is established from the authenticated Fabric
// credential. Manifest content and callers cannot override either member.
type IntegrationManifestBinding struct {
	ProjectID string
	Roles     []string
}

type VerifiedIntegrationManifest struct {
	Manifest        IntegrationManifest
	ResolvedRole    string
	SelectedEntries []IntegrationManifestEntry
	ChangedAt       time.Time
}

// IntegrationPlan is a digest-bound, human-readable repository plan. Private
// fields bind Commit to the exact authoritative cache snapshot used by Plan.
type IntegrationPlan struct {
	Operation      IntegrationOperation
	ProjectID      string
	ResolvedRole   string
	ExpectedDigest string
	Preview        IntegrationMaterializationPreview
	State          IntegrationState

	root             string
	manifest         *IntegrationManifest
	prior            IntegrationState
	offline          bool
	plannedOperation IntegrationOperation
	plannedRole      string
	plannedDigest    string
}

// IntegrationCommandRequest is the private same-user CLI RPC boundary. It is
// deliberately not an MCP tool and is never included in tools/list.
type IntegrationCommandRequest struct {
	Operation      string `json:"operation"`
	ProjectID      string `json:"project_id"`
	RepositoryRoot string `json:"repository_root"`
	ExpectedDigest string `json:"expected_digest"`
}

type IntegrationCommandPlan struct {
	Operation      string           `json:"operation"`
	ProjectID      string           `json:"project_id"`
	ResolvedRole   string           `json:"resolved_role"`
	ExpectedDigest string           `json:"expected_digest"`
	Diff           string           `json:"diff"`
	State          IntegrationState `json:"state"`
}

// IntegrationManifestService is Gateway's authoritative SQLite owner for
// verified bodies, lifecycle state, audit, and materialization journals.
type IntegrationManifestService struct {
	store      *localstore.Store
	db         *sql.DB
	toolDigest string
	mu         sync.Mutex
}

func NewIntegrationManifestService(store *localstore.Store) (*IntegrationManifestService, error) {
	if store == nil || store.DB() == nil {
		return nil, errors.New("integration manifest service requires local storage")
	}
	toolDigest, err := generatedToolContractDigest(newLocalRegistry(&Server{}).List())
	if err != nil {
		return nil, fmt.Errorf("compute Gateway tool contract digest: %w", err)
	}
	service := &IntegrationManifestService{store: store, db: store.DB(), toolDigest: toolDigest}
	if err := service.markIncompleteJournalsForRecovery(context.Background()); err != nil {
		return nil, err
	}
	if err := service.revalidateAllToolCompatibility(context.Background()); err != nil {
		return nil, err
	}
	return service, nil
}

// ReceiveIntegrationManifest implements sync.IntegrationManifestReceiver.
// Passport presence is required here; project and singular-role binding are
// validated again by the common bootstrap/incremental verifier below.
func (s *IntegrationManifestService) ReceiveIntegrationManifest(ctx context.Context, projectID, passportID string, roles []string, raw json.RawMessage) error {
	if passportID == "" {
		return integrationVerificationError("missing_passport_binding")
	}
	return s.ReceiveFabricChange(ctx, IntegrationManifestBinding{ProjectID: projectID, Roles: roles}, raw)
}

func (s *IntegrationManifestService) RollbackBootstrapIntegrationManifest(ctx context.Context, projectID, passportID string, roles []string, raw json.RawMessage) error {
	if passportID == "" || len(roles) != 1 {
		return integrationVerificationError("invalid_bootstrap_rollback_binding")
	}
	var change rawIntegrationManifestChange
	if !utf8.Valid(raw) || rejectDuplicateJSONMembers(raw) != nil || decodeClosedJSON(raw, &change) != nil ||
		change.Operation != "offered" || change.ProjectID != projectID {
		return integrationVerificationError("invalid_bootstrap_rollback_change")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if state.PendingManifestID == nil || state.PendingManifestVersion == nil || state.PendingManifestDigest == nil ||
		*state.PendingManifestID != change.ManifestID || *state.PendingManifestVersion != change.ManifestVersion ||
		*state.PendingManifestDigest != change.ManifestDigest {
		return tx.Commit()
	}
	state.PendingManifestID = nil
	state.PendingManifestVersion = nil
	state.PendingManifestDigest = nil
	if state.ActiveManifestID != nil {
		state.ApprovalState = "approved"
	} else {
		state.ApprovalState = "none"
		state.ConnectionState = "offline"
	}
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	return tx.Commit()
}

type rawIntegrationManifestChange struct {
	Operation       string          `json:"operation"`
	ProjectID       string          `json:"project_id"`
	ManifestID      string          `json:"manifest_id"`
	ManifestVersion int64           `json:"manifest_version"`
	ManifestDigest  string          `json:"manifest_digest"`
	Manifest        json.RawMessage `json:"manifest"`
	ChangedAt       string          `json:"changed_at"`
}

func (s *IntegrationManifestService) ReceiveFabricChange(ctx context.Context, binding IntegrationManifestBinding, raw json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !utf8.Valid(raw) || rejectDuplicateJSONMembers(raw) != nil {
		err := integrationVerificationError("invalid_change_json")
		_ = s.recordVerificationFailure(ctx, binding.ProjectID, raw, err)
		return err
	}
	var change rawIntegrationManifestChange
	if err := decodeClosedJSON(raw, &change); err != nil {
		verificationErr := integrationVerificationError("invalid_change_schema")
		_ = s.recordVerificationFailure(ctx, binding.ProjectID, raw, verificationErr)
		return verificationErr
	}
	if change.Operation == "revoked" {
		return s.receiveRevocation(ctx, binding, change)
	}
	verified, err := VerifyIntegrationManifestOffer(raw, binding, s.toolDigest)
	if err != nil {
		if persistErr := s.recordVerificationFailure(ctx, binding.ProjectID, raw, err); persistErr != nil {
			return persistErr
		}
		return err
	}
	return s.cacheVerifiedOffer(ctx, verified)
}

func (s *IntegrationManifestService) receiveRevocation(ctx context.Context, binding IntegrationManifestBinding, change rawIntegrationManifestChange) error {
	revokedAt, timeErr := canonicalIntegrationTime(change.ChangedAt)
	if change.Operation != "revoked" || binding.ProjectID != change.ProjectID || !canonicalUUIDString(change.ProjectID) ||
		!canonicalUUIDString(change.ManifestID) || change.ManifestVersion < 1 || change.ManifestVersion > maxIntegrationManifestVersion ||
		!digestPattern.MatchString(change.ManifestDigest) || len(binding.Roles) != 1 || validateSortedRoles(binding.Roles) != nil ||
		len(change.Manifest) == 0 || !bytes.Equal(bytes.TrimSpace(change.Manifest), []byte("null")) || timeErr != nil {
		verificationErr := integrationVerificationError("invalid_revocation")
		raw, _ := json.Marshal(change)
		_ = s.recordVerificationFailure(ctx, binding.ProjectID, raw, verificationErr)
		return verificationErr
	}
	manifest := IntegrationManifest{
		ProjectID: change.ProjectID, ManifestID: change.ManifestID,
		ManifestVersion: change.ManifestVersion, ManifestDigest: change.ManifestDigest,
	}
	operationID := uuid.NewString()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, change.ProjectID)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO integration_manifest_revocations
		(project_id, manifest_id, manifest_version, digest, revoked_at) VALUES (?, ?, ?, ?, ?)`,
		change.ProjectID, change.ManifestID, change.ManifestVersion, change.ManifestDigest, revokedAt)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	pendingMatch := state.PendingManifestID != nil && state.PendingManifestVersion != nil && state.PendingManifestDigest != nil &&
		*state.PendingManifestID == change.ManifestID && *state.PendingManifestVersion == change.ManifestVersion &&
		*state.PendingManifestDigest == change.ManifestDigest
	activeMatch := state.ActiveManifestID != nil && state.ActiveManifestVersion != nil && state.ActiveManifestDigest != nil &&
		*state.ActiveManifestID == change.ManifestID && *state.ActiveManifestVersion == change.ManifestVersion &&
		*state.ActiveManifestDigest == change.ManifestDigest
	if pendingMatch {
		state.PendingManifestID = nil
		state.PendingManifestVersion = nil
		state.PendingManifestDigest = nil
		if !activeMatch && state.ActiveManifestID != nil {
			state.ApprovalState = "approved"
		}
	}
	if activeMatch {
		state.ApprovalState = "revoked"
		state.GuidanceActive = false
		state.MaterializationState = "removal_required"
	}
	if inserted != 0 {
		if err := appendIntegrationAudit(ctx, tx, change.ProjectID, "integration_manifest.revoked", "fabric", operationID, manifest, "revoked", ""); err != nil {
			return err
		}
	}
	if activeMatch {
		payload, marshalErr := json.Marshal(map[string]any{
			"operation": IntegrationRemove, "revoked": true, "revoked_at": revokedAt,
			"prior_state": state, "repository_root": root,
		})
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_journal
			(operation_id, project_id, operation, status, payload) VALUES (?, ?, ?, 'prepared', ?)`,
			operationID, change.ProjectID, string(IntegrationRemove), string(payload)); err != nil {
			return err
		}
	}
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !activeMatch {
		return nil
	}
	if root == "" {
		return s.finishRevocationRemoval(ctx, operationID, state, root, manifest, IntegrationState{},
			errors.New("integration manifest repository root unavailable"))
	}
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		return s.finishRevocationRemoval(ctx, operationID, state, root, manifest, IntegrationState{}, err)
	}
	next, removeErr := materializer.Apply(IntegrationMaterializationRequest{
		Operation: IntegrationRemove, State: &state, ProjectID: change.ProjectID,
		ResolvedRole: state.ResolvedRole, Revoked: true, Offline: state.ConnectionState != "online",
	})
	return s.finishRevocationRemoval(ctx, operationID, state, root, manifest, next, removeErr)
}

func (s *IntegrationManifestService) finishRevocationRemoval(ctx context.Context, operationID string, prior IntegrationState, root string, manifest IntegrationManifest, next IntegrationState, removeErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Join(removeErr, err)
	}
	defer tx.Rollback()
	action := "integration_manifest.revocation_removed"
	outcome := "complete"
	status := "complete"
	if next.SchemaVersion == 1 {
		prior = next
	}
	if removeErr != nil {
		action = "integration_manifest.revocation_removal_required"
		outcome = "removal_required"
		if next.SchemaVersion == 0 {
			prior.ApprovalState = "revoked"
			prior.MaterializationState = "removal_required"
			prior.GuidanceActive = false
			prior.ConnectionState = "attention_required"
			status = "forward_required"
		}
	}
	if err := writeIntegrationStateTx(ctx, tx, prior, root); err != nil {
		return errors.Join(removeErr, err)
	}
	if err := appendIntegrationAudit(ctx, tx, prior.ProjectID, action, "gateway", operationID, manifest, outcome, verificationReason(removeErr)); err != nil {
		return errors.Join(removeErr, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE integration_manifest_journal SET status=?, updated_at=CURRENT_TIMESTAMP WHERE operation_id = ?`, status, operationID); err != nil {
		return errors.Join(removeErr, err)
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(removeErr, err)
	}
	return removeErr
}

func (s *IntegrationManifestService) cacheVerifiedOffer(ctx context.Context, verified VerifiedIntegrationManifest) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("integration manifest: begin verified offer: %w", err)
	}
	defer tx.Rollback()
	manifest := verified.Manifest
	if err := appendIntegrationAudit(ctx, tx, manifest.ProjectID, "integration_manifest.offered", "fabric", "", manifest, "received", ""); err != nil {
		return err
	}
	var existingDigest, existingBody string
	err = tx.QueryRowContext(ctx, `SELECT digest, body FROM integration_manifest_bodies
		WHERE project_id = ? AND manifest_id = ? AND manifest_version = ?`,
		manifest.ProjectID, manifest.ManifestID, manifest.ManifestVersion).Scan(&existingDigest, &existingBody)
	if err == nil {
		body, marshalErr := json.Marshal(manifest)
		if marshalErr != nil {
			return marshalErr
		}
		if existingDigest != manifest.ManifestDigest || existingBody != string(body) {
			state, root, stateErr := readIntegrationStateTx(ctx, tx, manifest.ProjectID)
			if stateErr != nil {
				return stateErr
			}
			state.ConnectionState = "attention_required"
			if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
				return err
			}
			if err := appendIntegrationAudit(ctx, tx, manifest.ProjectID, "integration_manifest.equivocation_detected", "gateway", "", manifest, "rejected", "same_version_different_bytes"); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			return ErrIntegrationManifestEquivocation
		}
		state, root, stateErr := readIntegrationStateTx(ctx, tx, manifest.ProjectID)
		if stateErr != nil {
			return stateErr
		}
		isPending := state.PendingManifestID != nil && state.PendingManifestVersion != nil && state.PendingManifestDigest != nil &&
			*state.PendingManifestID == manifest.ManifestID && *state.PendingManifestVersion == manifest.ManifestVersion &&
			*state.PendingManifestDigest == manifest.ManifestDigest
		isActive := state.ActiveManifestID != nil && state.ActiveManifestVersion != nil && state.ActiveManifestDigest != nil &&
			*state.ActiveManifestID == manifest.ManifestID && *state.ActiveManifestVersion == manifest.ManifestVersion &&
			*state.ActiveManifestDigest == manifest.ManifestDigest
		var decisions int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM integration_manifest_decisions
			WHERE project_id=? AND manifest_id=? AND manifest_version=? AND digest=?`, manifest.ProjectID,
			manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest).Scan(&decisions); err != nil {
			return err
		}
		if !isPending && !isActive && decisions == 0 {
			state.PendingManifestID = stringPointer(manifest.ManifestID)
			state.PendingManifestVersion = int64Pointer(manifest.ManifestVersion)
			state.PendingManifestDigest = stringPointer(manifest.ManifestDigest)
			if state.ActiveManifestID == nil {
				state.ResolvedRole = verified.ResolvedRole
			}
			state.ApprovalState = "awaiting_approval"
			state.ConnectionState = "online"
			state.CompatibilityState = "compatible"
			state.LastVerifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
				return err
			}
			if err := appendIntegrationAudit(ctx, tx, manifest.ProjectID, "integration_manifest.awaiting_approval", "gateway", "", manifest, "pending", ""); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("integration manifest: inspect cached version: %w", err)
	}
	state, root, err := readIntegrationStateTx(ctx, tx, manifest.ProjectID)
	if err != nil {
		return err
	}
	if state.ActiveManifestID != nil && *state.ActiveManifestID != manifest.ManifestID {
		return fmt.Errorf("%w: another lineage remains active", ErrIntegrationManifestReplay)
	}
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT max(manifest_version) FROM integration_manifest_bodies
		WHERE project_id = ? AND manifest_id = ?`, manifest.ProjectID, manifest.ManifestID).Scan(&latest); err != nil {
		return fmt.Errorf("integration manifest: inspect latest version: %w", err)
	}
	if latest.Valid && manifest.ManifestVersion <= latest.Int64 {
		return ErrIntegrationManifestReplay
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_bodies
		(project_id, manifest_id, manifest_version, digest, body, tool_contract_digest, resolved_role, verified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, manifest.ProjectID, manifest.ManifestID, manifest.ManifestVersion,
		manifest.ManifestDigest, string(body), manifest.ToolContractDigest, verified.ResolvedRole, time.Now().UTC()); err != nil {
		return fmt.Errorf("integration manifest: cache verified body: %w", err)
	}
	state.ProjectID = manifest.ProjectID
	state.PendingManifestID = stringPointer(manifest.ManifestID)
	state.PendingManifestVersion = int64Pointer(manifest.ManifestVersion)
	state.PendingManifestDigest = stringPointer(manifest.ManifestDigest)
	if state.ActiveManifestID == nil {
		state.ResolvedRole = verified.ResolvedRole
	}
	state.ApprovalState = "awaiting_approval"
	state.ConnectionState = "online"
	state.CompatibilityState = "compatible"
	state.LastVerifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	if err := appendIntegrationAudit(ctx, tx, manifest.ProjectID, "integration_manifest.verified", "gateway", "", manifest, "verified", ""); err != nil {
		return err
	}
	if err := appendIntegrationAudit(ctx, tx, manifest.ProjectID, "integration_manifest.awaiting_approval", "gateway", "", manifest, "pending", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *IntegrationManifestService) recordVerificationFailure(ctx context.Context, projectID string, raw json.RawMessage, verificationErr error) error {
	if !canonicalUUIDString(projectID) {
		return verificationErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if state.ActiveManifestID == nil {
		state.ApprovalState = "verification_failed"
		state.GuidanceActive = false
	}
	state.ConnectionState = "attention_required"
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	manifest := bestEffortManifestMetadata(raw, projectID)
	if err := appendIntegrationAudit(ctx, tx, projectID, "integration_manifest.offered", "fabric", "", manifest, "received", ""); err != nil {
		return err
	}
	if err := appendIntegrationAudit(ctx, tx, projectID, "integration_manifest.verification_failed", "gateway", "", manifest, "rejected", verificationReason(verificationErr)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *IntegrationManifestService) DecideCandidate(ctx context.Context, projectID, digest, decision string) (IntegrationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if decision != "postponed" && decision != "rejected" {
		return IntegrationState{}, errors.New("only postpone or reject are valid non-approval decisions")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationState{}, err
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return IntegrationState{}, err
	}
	if state.PendingManifestID == nil || state.PendingManifestVersion == nil || state.PendingManifestDigest == nil || *state.PendingManifestDigest != digest {
		return IntegrationState{}, ErrIntegrationManifestUnavailable
	}
	manifest, err := readCachedManifestTx(ctx, tx, projectID, *state.PendingManifestID, *state.PendingManifestVersion, *state.PendingManifestDigest)
	if err != nil {
		return IntegrationState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_decisions
		(project_id, manifest_id, manifest_version, digest, decision) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, manifest_id, manifest_version, digest) DO UPDATE SET decision=excluded.decision, decided_at=CURRENT_TIMESTAMP`,
		projectID, manifest.ManifestID, manifest.ManifestVersion, digest, decision); err != nil {
		return IntegrationState{}, err
	}
	state.ApprovalState = decision
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return IntegrationState{}, err
	}
	if err := appendIntegrationAudit(ctx, tx, projectID, "integration_manifest."+decision, "human_cli", "", manifest, decision, ""); err != nil {
		return IntegrationState{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationState{}, err
	}
	return state, nil
}

func (s *IntegrationManifestService) SetConnectionState(ctx context.Context, projectID, connection string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if connection != "online" && connection != "offline" && connection != "synchronizing" && connection != "attention_required" {
		return errors.New("invalid integration connection state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	state.ConnectionState = connection
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *IntegrationManifestService) ReadIntegrationGuidance(ctx context.Context, projectID string) (IntegrationGuidanceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return IntegrationGuidanceSnapshot{}, err
	}
	defer tx.Rollback()
	state, _, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return IntegrationGuidanceSnapshot{}, err
	}
	snapshot := IntegrationGuidanceSnapshot{State: state}
	if state.GuidanceActive && state.CompatibilityState == "compatible" &&
		state.ActiveManifestID != nil && state.ActiveManifestVersion != nil && state.ActiveManifestDigest != nil {
		manifest, readErr := readCachedManifestTx(ctx, tx, projectID, *state.ActiveManifestID, *state.ActiveManifestVersion, *state.ActiveManifestDigest)
		if readErr != nil {
			return IntegrationGuidanceSnapshot{}, readErr
		}
		if !constantTimeDigestEqual(manifest.ToolContractDigest, s.toolDigest) {
			snapshot.State.CompatibilityState = "tool_contract_mismatch"
			snapshot.State.GuidanceActive = false
			snapshot.State.ConnectionState = "attention_required"
		} else {
			snapshot.Manifest = &manifest
		}
	}
	if err := tx.Commit(); err != nil {
		return IntegrationGuidanceSnapshot{}, err
	}
	return snapshot, nil
}

func (s *IntegrationManifestService) Status(ctx context.Context, projectID string) (IntegrationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !canonicalUUIDString(projectID) {
		return IntegrationState{}, ErrIntegrationManifestUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return IntegrationState{}, err
	}
	defer tx.Rollback()
	state, _, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return IntegrationState{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationState{}, err
	}
	return state, nil
}

func (s *IntegrationManifestService) Preview(ctx context.Context, projectID, root string) (IntegrationPlan, error) {
	state, err := s.Status(ctx, projectID)
	if err != nil {
		return IntegrationPlan{}, err
	}
	operation := IntegrationApply
	switch {
	case state.ApprovalState == "revoked" && state.ActiveManifestID != nil:
		operation = IntegrationRemove
	case state.PendingManifestID != nil && state.ActiveManifestID != nil:
		operation = IntegrationUpdate
	case state.PendingManifestID != nil:
		operation = IntegrationApply
	case state.ActiveManifestID != nil:
		operation = IntegrationApply
	default:
		return IntegrationPlan{}, ErrIntegrationManifestUnavailable
	}
	return s.Plan(ctx, operation, projectID, root)
}

func (s *Server) PlanIntegrationCommand(ctx context.Context, request IntegrationCommandRequest) (IntegrationCommandPlan, error) {
	if s.integrationService == nil {
		return IntegrationCommandPlan{}, errors.New("Gateway approved integration cache is unavailable")
	}
	if request.Operation == "status" {
		state, err := s.integrationService.Status(ctx, request.ProjectID)
		return IntegrationCommandPlan{Operation: "status", ProjectID: request.ProjectID, ResolvedRole: state.ResolvedRole, State: state}, err
	}
	var plan IntegrationPlan
	var err error
	if request.Operation == "preview" {
		plan, err = s.integrationService.Preview(ctx, request.ProjectID, request.RepositoryRoot)
	} else {
		operation := IntegrationOperation(request.Operation)
		if operation != IntegrationApply && operation != IntegrationUpdate && operation != IntegrationRemove && operation != IntegrationRollback {
			return IntegrationCommandPlan{}, errors.New("invalid integration command operation")
		}
		plan, err = s.integrationService.Plan(ctx, operation, request.ProjectID, request.RepositoryRoot)
	}
	if err != nil {
		return IntegrationCommandPlan{}, err
	}
	return IntegrationCommandPlan{
		Operation: string(plan.Operation), ProjectID: plan.ProjectID, ResolvedRole: plan.ResolvedRole,
		ExpectedDigest: plan.ExpectedDigest, Diff: plan.Preview.Diff, State: plan.State,
	}, nil
}

func (s *Server) CommitIntegrationCommand(ctx context.Context, request IntegrationCommandRequest) (IntegrationState, error) {
	if s.integrationService == nil {
		return IntegrationState{}, errors.New("Gateway approved integration cache is unavailable")
	}
	operation := IntegrationOperation(request.Operation)
	if operation != IntegrationApply && operation != IntegrationUpdate && operation != IntegrationRemove && operation != IntegrationRollback {
		return IntegrationState{}, errors.New("invalid integration command operation")
	}
	if !digestPattern.MatchString(request.ExpectedDigest) {
		return IntegrationState{}, errors.New("integration confirmation requires a full digest")
	}
	plan, err := s.integrationService.Plan(ctx, operation, request.ProjectID, request.RepositoryRoot)
	if err != nil {
		return IntegrationState{}, err
	}
	if !constantTimeDigestEqual(plan.ExpectedDigest, request.ExpectedDigest) {
		return IntegrationState{}, errors.New("integration confirmation digest does not match fresh plan")
	}
	return s.integrationService.Commit(ctx, plan)
}

func (s *IntegrationManifestService) Plan(ctx context.Context, operation IntegrationOperation, projectID, root string) (IntegrationPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !canonicalUUIDString(projectID) {
		return IntegrationPlan{}, ErrIntegrationManifestUnavailable
	}
	if err := s.revalidateProjectToolCompatibility(ctx, projectID); err != nil {
		return IntegrationPlan{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return IntegrationPlan{}, err
	}
	defer tx.Rollback()
	state, storedRoot, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return IntegrationPlan{}, err
	}
	offline := state.ConnectionState != "online"
	var manifest *IntegrationManifest
	resolvedRole := state.ResolvedRole
	readManifest := func(id *string, version *int64, digest *string) error {
		if id == nil || version == nil || digest == nil {
			return ErrIntegrationManifestUnavailable
		}
		cached, cachedRole, readErr := readCachedManifestAndRoleTx(ctx, tx, projectID, *id, *version, *digest)
		if readErr != nil {
			return readErr
		}
		var revoked int
		if readErr := tx.QueryRowContext(ctx, `SELECT count(*) FROM integration_manifest_revocations
			WHERE project_id = ? AND manifest_id = ? AND manifest_version = ? AND digest = ?`,
			projectID, *id, *version, *digest).Scan(&revoked); readErr != nil {
			return readErr
		}
		if revoked != 0 {
			return ErrIntegrationManifestUnavailable
		}
		if !constantTimeDigestEqual(cached.ToolContractDigest, s.toolDigest) {
			return integrationVerificationError("tool_contract_mismatch")
		}
		manifest = &cached
		resolvedRole = cachedRole
		return nil
	}
	switch operation {
	case IntegrationApply:
		if state.ActiveManifestID == nil {
			if offline {
				return IntegrationPlan{}, ErrIntegrationManifestOffline
			}
			if err := readManifest(state.PendingManifestID, state.PendingManifestVersion, state.PendingManifestDigest); err != nil {
				return IntegrationPlan{}, err
			}
		} else if err := readManifest(state.ActiveManifestID, state.ActiveManifestVersion, state.ActiveManifestDigest); err != nil {
			return IntegrationPlan{}, err
		}
	case IntegrationUpdate:
		if offline {
			return IntegrationPlan{}, ErrIntegrationManifestOffline
		}
		if state.ActiveManifestID == nil {
			return IntegrationPlan{}, ErrIntegrationManifestUnavailable
		}
		if err := readManifest(state.PendingManifestID, state.PendingManifestVersion, state.PendingManifestDigest); err != nil {
			return IntegrationPlan{}, err
		}
		if manifest.ManifestID != *state.ActiveManifestID || manifest.ManifestVersion <= *state.ActiveManifestVersion {
			return IntegrationPlan{}, ErrIntegrationManifestReplay
		}
	case IntegrationRollback:
		if state.ActiveManifestID == nil || state.RollbackCandidateManifestVersion == nil || state.RollbackCandidateManifestDigest == nil {
			return IntegrationPlan{}, ErrIntegrationManifestUnavailable
		}
		if err := readManifest(state.ActiveManifestID, state.RollbackCandidateManifestVersion, state.RollbackCandidateManifestDigest); err != nil {
			return IntegrationPlan{}, err
		}
	case IntegrationRemove:
		if state.ActiveManifestID == nil || len(state.Targets) == 0 {
			return IntegrationPlan{}, ErrIntegrationManifestUnavailable
		}
	default:
		return IntegrationPlan{}, fmt.Errorf("unknown integration operation %q", operation)
	}
	if err := tx.Commit(); err != nil {
		return IntegrationPlan{}, err
	}
	materializer, err := NewIntegrationMaterializer(root)
	if err != nil {
		return IntegrationPlan{}, err
	}
	if storedRoot != "" && storedRoot != materializer.root {
		return IntegrationPlan{}, errors.New("integration repository root does not match the project binding")
	}
	request := IntegrationMaterializationRequest{
		Operation: operation, Manifest: manifest, State: &state, ProjectID: projectID,
		ResolvedRole: resolvedRole, Offline: offline,
	}
	preview, err := materializer.Preview(request)
	if err != nil {
		return IntegrationPlan{}, err
	}
	return IntegrationPlan{
		Operation: operation, ProjectID: projectID, ResolvedRole: resolvedRole,
		ExpectedDigest: preview.ExpectedDigest, Preview: preview, State: state,
		root: materializer.root, manifest: manifest, prior: state, offline: offline,
		plannedOperation: operation, plannedRole: resolvedRole, plannedDigest: preview.ExpectedDigest,
	}, nil
}

func (s *IntegrationManifestService) revalidateAllToolCompatibility(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id FROM integration_manifest_project_state`)
	if err != nil {
		return err
	}
	var projects []string
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, projectID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, projectID := range projects {
		if err := s.revalidateProjectToolCompatibility(ctx, projectID); err != nil {
			return err
		}
	}
	return nil
}

func (s *IntegrationManifestService) revalidateProjectToolCompatibility(ctx context.Context, projectID string) error {
	if !canonicalUUIDString(projectID) {
		return ErrIntegrationManifestUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if state.ActiveManifestID == nil || state.ActiveManifestVersion == nil || state.ActiveManifestDigest == nil {
		return tx.Commit()
	}
	previousCompatibility, previousGuidance, previousConnection := state.CompatibilityState, state.GuidanceActive, state.ConnectionState
	var cachedToolDigest string
	err = tx.QueryRowContext(ctx, `SELECT tool_contract_digest FROM integration_manifest_bodies
		WHERE project_id=? AND manifest_id=? AND manifest_version=? AND digest=?`, projectID,
		*state.ActiveManifestID, *state.ActiveManifestVersion, *state.ActiveManifestDigest).Scan(&cachedToolDigest)
	compatible := err == nil && constantTimeDigestEqual(cachedToolDigest, s.toolDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !compatible {
		state.CompatibilityState = "tool_contract_mismatch"
		state.GuidanceActive = false
		state.ConnectionState = "attention_required"
	} else if state.CompatibilityState == "tool_contract_mismatch" {
		var revoked int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM integration_manifest_revocations
			WHERE project_id=? AND manifest_id=? AND manifest_version=? AND digest=?`, projectID,
			*state.ActiveManifestID, *state.ActiveManifestVersion, *state.ActiveManifestDigest).Scan(&revoked); err != nil {
			return err
		}
		state.CompatibilityState = "compatible"
		state.GuidanceActive = revoked == 0 && state.ApprovalState != "revoked" && state.MaterializationState == "applied"
	}
	if state.CompatibilityState == previousCompatibility && state.GuidanceActive == previousGuidance && state.ConnectionState == previousConnection {
		return tx.Commit()
	}
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *IntegrationManifestService) Commit(ctx context.Context, plan IntegrationPlan) (IntegrationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if plan.ProjectID == "" || plan.root == "" || plan.Operation != plan.plannedOperation || plan.Operation != plan.Preview.Operation ||
		plan.ProjectID != plan.Preview.ProjectID || plan.ExpectedDigest != plan.Preview.ExpectedDigest {
		return IntegrationState{}, errors.New("invalid integration plan binding")
	}
	if plan.ResolvedRole != plan.plannedRole || plan.ResolvedRole != plan.Preview.ResolvedRole || plan.ExpectedDigest != plan.plannedDigest {
		return IntegrationState{}, errors.New("invalid integration plan role or digest binding")
	}
	if plan.Operation != IntegrationRemove && (plan.manifest == nil || plan.ExpectedDigest != plan.manifest.ManifestDigest) {
		return IntegrationState{}, errors.New("invalid integration plan digest")
	}
	operationID := uuid.NewString()
	payload, err := json.Marshal(map[string]any{
		"operation": plan.Operation, "expected_digest": plan.ExpectedDigest,
		"preview": plan.Preview, "prior_state": plan.prior,
	})
	if err != nil {
		return IntegrationState{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationState{}, err
	}
	defer tx.Rollback()
	current, _, err := readIntegrationStateTx(ctx, tx, plan.ProjectID)
	if err != nil {
		return IntegrationState{}, err
	}
	if currentStateDigest(current, plan.Operation) != currentStateDigest(plan.prior, plan.Operation) {
		return IntegrationState{}, errors.New("integration plan is stale")
	}
	manifest := IntegrationManifest{ProjectID: plan.ProjectID}
	if plan.manifest != nil {
		manifest = *plan.manifest
		if _, err := readCachedManifestTx(ctx, tx, plan.ProjectID, manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest); err != nil {
			return IntegrationState{}, err
		}
		var revoked int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM integration_manifest_revocations
			WHERE project_id=? AND manifest_id=? AND manifest_version=? AND digest=?`, plan.ProjectID,
			manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest).Scan(&revoked); err != nil {
			return IntegrationState{}, err
		}
		if revoked != 0 {
			return IntegrationState{}, ErrIntegrationManifestUnavailable
		}
	}
	if plan.Operation == IntegrationApply && current.ActiveManifestID == nil || plan.Operation == IntegrationUpdate {
		if current.PendingManifestDigest == nil || *current.PendingManifestDigest != plan.ExpectedDigest || current.ConnectionState != "online" {
			return IntegrationState{}, ErrIntegrationManifestOffline
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_decisions
			(project_id, manifest_id, manifest_version, digest, decision) VALUES (?, ?, ?, ?, 'approved')
			ON CONFLICT(project_id, manifest_id, manifest_version, digest) DO UPDATE SET decision='approved', decided_at=CURRENT_TIMESTAMP`,
			plan.ProjectID, manifest.ManifestID, manifest.ManifestVersion, manifest.ManifestDigest); err != nil {
			return IntegrationState{}, err
		}
		current.ApprovalState = "approved"
		if err := appendIntegrationAudit(ctx, tx, plan.ProjectID, "integration_manifest.approved", "human_cli", operationID, manifest, "approved", ""); err != nil {
			return IntegrationState{}, err
		}
	}
	if err := writeIntegrationStateTx(ctx, tx, current, plan.root); err != nil {
		return IntegrationState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO integration_manifest_journal
		(operation_id, project_id, operation, status, payload) VALUES (?, ?, ?, 'prepared', ?)`,
		operationID, plan.ProjectID, string(plan.Operation), string(payload)); err != nil {
		return IntegrationState{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationState{}, err
	}

	materializer, err := NewIntegrationMaterializer(plan.root)
	if err != nil {
		return s.finishFailedIntegration(ctx, operationID, plan, manifest, IntegrationState{}, err)
	}
	verifiedAt, _ := time.Parse(time.RFC3339Nano, plan.prior.LastVerifiedAt)
	next, applyErr := materializer.Apply(IntegrationMaterializationRequest{
		Operation: plan.Operation, Manifest: plan.manifest, State: &plan.prior, ProjectID: plan.ProjectID,
		ResolvedRole: plan.ResolvedRole, Offline: plan.offline, VerifiedAt: verifiedAt,
	})
	if applyErr != nil && next.SchemaVersion == 0 {
		return s.finishFailedIntegration(ctx, operationID, plan, manifest, next, applyErr)
	}
	if plan.Operation != IntegrationRemove {
		next.PendingManifestID = nil
		next.PendingManifestVersion = nil
		next.PendingManifestDigest = nil
	}
	if (plan.Operation == IntegrationUpdate || plan.Operation == IntegrationRollback) && plan.prior.ActiveManifestVersion != nil {
		next.RollbackCandidateManifestVersion = cloneInt64Pointer(plan.prior.ActiveManifestVersion)
		next.RollbackCandidateManifestDigest = cloneStringPointer(plan.prior.ActiveManifestDigest)
	}
	finishTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationState{}, err
	}
	defer finishTx.Rollback()
	if err := writeIntegrationStateTx(ctx, finishTx, next, plan.root); err != nil {
		return IntegrationState{}, err
	}
	action := "integration_manifest.applied"
	if plan.Operation == IntegrationRemove {
		action = "integration_manifest.removed"
	} else if plan.Operation == IntegrationRollback {
		action = "integration_manifest.rollback_applied"
	}
	if applyErr != nil {
		action = "integration_manifest.drift_detected"
	}
	if err := appendIntegrationAudit(ctx, finishTx, plan.ProjectID, action, "human_cli", operationID, manifest, "complete", verificationReason(applyErr)); err != nil {
		return IntegrationState{}, err
	}
	if _, err := finishTx.ExecContext(ctx, `UPDATE integration_manifest_journal SET status='complete', updated_at=CURRENT_TIMESTAMP WHERE operation_id = ?`, operationID); err != nil {
		return IntegrationState{}, err
	}
	if err := finishTx.Commit(); err != nil {
		return IntegrationState{}, err
	}
	return next, applyErr
}

func (s *IntegrationManifestService) finishFailedIntegration(ctx context.Context, operationID string, plan IntegrationPlan, manifest IntegrationManifest, result IntegrationState, applyErr error) (IntegrationState, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationState{}, errors.Join(applyErr, err)
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, plan.ProjectID)
	if err != nil {
		return IntegrationState{}, errors.Join(applyErr, err)
	}
	if result.SchemaVersion == 1 {
		state = result
	} else {
		state.DriftDetected = errors.Is(applyErr, ErrIntegrationDrift)
		state.ConnectionState = "attention_required"
		if state.ActiveManifestID == nil {
			state.GuidanceActive = false
			state.MaterializationState = "not_applied"
		}
	}
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return IntegrationState{}, errors.Join(applyErr, err)
	}
	action := "integration_manifest.apply_failed"
	if errors.Is(applyErr, ErrIntegrationDrift) {
		action = "integration_manifest.drift_detected"
	}
	if err := appendIntegrationAudit(ctx, tx, plan.ProjectID, action, "human_cli", operationID, manifest, "failed", verificationReason(applyErr)); err != nil {
		return IntegrationState{}, errors.Join(applyErr, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE integration_manifest_journal SET status='complete', updated_at=CURRENT_TIMESTAMP WHERE operation_id = ?`, operationID); err != nil {
		return IntegrationState{}, errors.Join(applyErr, err)
	}
	if err := tx.Commit(); err != nil {
		return IntegrationState{}, errors.Join(applyErr, err)
	}
	return state, applyErr
}

func currentStateDigest(state IntegrationState, operation IntegrationOperation) string {
	if operation == IntegrationApply && state.ActiveManifestDigest != nil || operation == IntegrationRemove || operation == IntegrationRollback {
		if state.ActiveManifestDigest != nil {
			return *state.ActiveManifestDigest
		}
		return ""
	}
	if state.PendingManifestDigest != nil {
		return *state.PendingManifestDigest
	}
	return ""
}

func VerifyIntegrationManifestOffer(raw json.RawMessage, binding IntegrationManifestBinding, expectedToolDigest string) (VerifiedIntegrationManifest, error) {
	var verified VerifiedIntegrationManifest
	if !utf8.Valid(raw) {
		return verified, integrationVerificationError("invalid_utf8")
	}
	if err := rejectDuplicateJSONMembers(raw); err != nil {
		return verified, integrationVerificationError("duplicate_member")
	}
	var change rawIntegrationManifestChange
	if err := decodeClosedJSON(raw, &change); err != nil {
		return verified, integrationVerificationError("invalid_change_schema")
	}
	if change.Operation != "offered" || !canonicalUUIDString(change.ProjectID) || !canonicalUUIDString(change.ManifestID) ||
		change.ManifestVersion < 1 || change.ManifestVersion > maxIntegrationManifestVersion || !digestPattern.MatchString(change.ManifestDigest) ||
		len(change.Manifest) == 0 || bytes.Equal(bytes.TrimSpace(change.Manifest), []byte("null")) {
		return verified, integrationVerificationError("invalid_change")
	}
	changedAt, err := canonicalIntegrationTime(change.ChangedAt)
	if err != nil {
		return verified, integrationVerificationError("invalid_change_time")
	}
	if binding.ProjectID != change.ProjectID || !canonicalUUIDString(binding.ProjectID) {
		return verified, integrationVerificationError("project_binding_mismatch")
	}
	if len(binding.Roles) != 1 || validateSortedRoles(binding.Roles) != nil {
		return verified, integrationVerificationError("invalid_binding_role")
	}

	var manifest IntegrationManifest
	if err := decodeClosedJSON(change.Manifest, &manifest); err != nil {
		return verified, integrationVerificationError("invalid_manifest_schema")
	}
	if manifest.SchemaVersion != 1 || manifest.Source != "fabric" || !canonicalUUIDString(manifest.ManifestID) ||
		!canonicalUUIDString(manifest.ProjectID) || manifest.ManifestVersion < 1 || manifest.ManifestVersion > maxIntegrationManifestVersion ||
		manifest.ProjectID != binding.ProjectID || manifest.ManifestID != change.ManifestID || manifest.ManifestVersion != change.ManifestVersion ||
		manifest.ManifestDigest != change.ManifestDigest || !digestPattern.MatchString(manifest.ToolContractDigest) || !digestPattern.MatchString(manifest.ManifestDigest) {
		return verified, integrationVerificationError("manifest_binding_mismatch")
	}
	if _, err := canonicalIntegrationTime(manifest.CreatedAt); err != nil {
		return verified, integrationVerificationError("invalid_manifest_time")
	}
	if manifest.RoleFilters == nil || validateSortedRoles(manifest.RoleFilters) != nil || !matchesRole(manifest.RoleFilters, binding.Roles[0]) {
		return verified, integrationVerificationError("invalid_manifest_roles")
	}
	if len(manifest.Entries) == 0 || len(manifest.Entries) > 64 {
		return verified, integrationVerificationError("invalid_entries")
	}
	previousTarget := ""
	selected := make([]IntegrationManifestEntry, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if entry.RoleFilters == nil || (previousTarget != "" && entry.Target <= previousTarget) {
			return verified, integrationVerificationError("invalid_entry_order_or_roles")
		}
		previousTarget = entry.Target
		if err := validateIntegrationEntry(entry); err != nil {
			return verified, integrationVerificationError("invalid_entry")
		}
		if matchesRole(entry.RoleFilters, binding.Roles[0]) {
			selected = append(selected, entry)
		}
	}
	if len(selected) == 0 {
		return verified, integrationVerificationError("no_applicable_entries")
	}
	computedDigest, err := canonicalIntegrationManifestDigest(manifest)
	if err != nil || !constantTimeDigestEqual(computedDigest, manifest.ManifestDigest) {
		return verified, integrationVerificationError("manifest_digest_mismatch")
	}
	if !digestPattern.MatchString(expectedToolDigest) || !constantTimeDigestEqual(expectedToolDigest, manifest.ToolContractDigest) {
		return verified, integrationVerificationError("tool_contract_mismatch")
	}
	verified.Manifest = manifest
	verified.ResolvedRole = binding.Roles[0]
	verified.SelectedEntries = selected
	verified.ChangedAt = changedAt
	return verified, nil
}

func integrationVerificationError(reason string) error {
	return fmt.Errorf("%w: %s", ErrIntegrationManifestVerification, reason)
}

func decodeClosedJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func rejectDuplicateJSONMembers(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("non-string object member")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object member %q", key)
				}
				seen[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func canonicalIntegrationManifestDigest(manifest IntegrationManifest) (string, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	delete(value, "manifest_digest")
	canonical, err := canonicalIntegrationJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonicalIntegrationJSON(value any) ([]byte, error) {
	var output bytes.Buffer
	if err := appendCanonicalIntegrationJSON(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func appendCanonicalIntegrationJSON(output *bytes.Buffer, value any) error {
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
		appendCanonicalIntegrationString(output, typed)
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			return errors.New("non-integer canonical number")
		}
		if _, err := strconv.ParseInt(string(typed), 10, 64); err != nil {
			return err
		}
		output.WriteString(string(typed))
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := appendCanonicalIntegrationJSON(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			appendCanonicalIntegrationString(output, key)
			output.WriteByte(':')
			if err := appendCanonicalIntegrationJSON(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func appendCanonicalIntegrationString(output *bytes.Buffer, value string) {
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

func canonicalIntegrationTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("time is not canonical UTC RFC3339Nano")
	}
	return parsed.UTC(), nil
}

func canonicalUUIDString(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func constantTimeDigestEqual(left, right string) bool {
	decode := func(value string) ([]byte, error) {
		if !digestPattern.MatchString(value) {
			return nil, errors.New("invalid digest")
		}
		return hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	}
	leftBytes, leftErr := decode(left)
	rightBytes, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}

func defaultIntegrationState(projectID string) IntegrationState {
	return IntegrationState{
		SchemaVersion: 1, ProjectID: projectID, ApprovalState: "none", MaterializationState: "not_applied",
		ConnectionState: "offline", CompatibilityState: "compatible", PreservedTargets: []string{}, Targets: []IntegrationTargetState{},
	}
}

func readIntegrationStateTx(ctx context.Context, tx *sql.Tx, projectID string) (IntegrationState, string, error) {
	state := defaultIntegrationState(projectID)
	var raw, root string
	err := tx.QueryRowContext(ctx, `SELECT state, repository_root FROM integration_manifest_project_state WHERE project_id = ?`, projectID).Scan(&raw, &root)
	if errors.Is(err, sql.ErrNoRows) {
		return state, "", nil
	}
	if err != nil {
		return IntegrationState{}, "", fmt.Errorf("integration manifest: read project state: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return IntegrationState{}, "", fmt.Errorf("integration manifest: decode project state: %w", err)
	}
	if state.SchemaVersion != 1 || state.ProjectID != projectID {
		return IntegrationState{}, "", errors.New("integration manifest: corrupt project state binding")
	}
	if state.PreservedTargets == nil {
		state.PreservedTargets = []string{}
	}
	if state.Targets == nil {
		state.Targets = []IntegrationTargetState{}
	}
	return state, root, nil
}

func writeIntegrationStateTx(ctx context.Context, tx *sql.Tx, state IntegrationState, root string) error {
	if !canonicalUUIDString(state.ProjectID) || state.SchemaVersion != 1 {
		return errors.New("integration manifest: invalid authoritative project state")
	}
	if state.PreservedTargets == nil {
		state.PreservedTargets = []string{}
	}
	if state.Targets == nil {
		state.Targets = []IntegrationTargetState{}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO integration_manifest_project_state (project_id, state, repository_root, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id) DO UPDATE SET state=excluded.state, repository_root=excluded.repository_root, updated_at=CURRENT_TIMESTAMP`,
		state.ProjectID, string(raw), root)
	if err != nil {
		return fmt.Errorf("integration manifest: persist project state: %w", err)
	}
	return nil
}

func readCachedManifestTx(ctx context.Context, tx *sql.Tx, projectID, manifestID string, version int64, digest string) (IntegrationManifest, error) {
	manifest, _, err := readCachedManifestAndRoleTx(ctx, tx, projectID, manifestID, version, digest)
	return manifest, err
}

func readCachedManifestAndRoleTx(ctx context.Context, tx *sql.Tx, projectID, manifestID string, version int64, digest string) (IntegrationManifest, string, error) {
	var raw, resolvedRole string
	err := tx.QueryRowContext(ctx, `SELECT body, resolved_role FROM integration_manifest_bodies
		WHERE project_id = ? AND manifest_id = ? AND manifest_version = ? AND digest = ?`, projectID, manifestID, version, digest).Scan(&raw, &resolvedRole)
	if errors.Is(err, sql.ErrNoRows) {
		return IntegrationManifest{}, "", ErrIntegrationManifestUnavailable
	}
	if err != nil {
		return IntegrationManifest{}, "", err
	}
	var manifest IntegrationManifest
	if err := decodeClosedJSON([]byte(raw), &manifest); err != nil {
		return IntegrationManifest{}, "", fmt.Errorf("integration manifest: corrupt cached body: %w", err)
	}
	if !validSlug(resolvedRole) || !matchesRole(manifest.RoleFilters, resolvedRole) {
		return IntegrationManifest{}, "", errors.New("integration manifest: corrupt cached resolved role")
	}
	return manifest, resolvedRole, nil
}

func appendIntegrationAudit(ctx context.Context, tx *sql.Tx, projectID, action, actorKind, operationID string, manifest IntegrationManifest, outcome, reason string) error {
	payload, err := json.Marshal(map[string]any{
		"manifest_id": manifest.ManifestID, "manifest_version": manifest.ManifestVersion,
		"manifest_digest": manifest.ManifestDigest, "outcome": outcome, "reason_code": reason,
	})
	if err != nil {
		return err
	}
	var version any
	if manifest.ManifestVersion > 0 {
		version = manifest.ManifestVersion
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO integration_manifest_audit
		(project_id, id, action, payload, actor_kind, operation_id, manifest_id, manifest_version, manifest_digest, outcome, reason_code, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, projectID, uuid.NewString(), action, string(payload), actorKind, operationID,
		manifest.ManifestID, version, manifest.ManifestDigest, outcome, reason, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("integration manifest: append audit %s: %w", action, err)
	}
	return nil
}

func bestEffortManifestMetadata(raw json.RawMessage, projectID string) IntegrationManifest {
	manifest := IntegrationManifest{ProjectID: projectID}
	var change rawIntegrationManifestChange
	if json.Unmarshal(raw, &change) == nil {
		manifest.ManifestID = change.ManifestID
		manifest.ManifestVersion = change.ManifestVersion
		manifest.ManifestDigest = change.ManifestDigest
	}
	return manifest
}

func verificationReason(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if index := strings.LastIndex(message, ": "); index >= 0 {
		message = message[index+2:]
	}
	if validSlug(strings.ReplaceAll(message, "_", "-")) {
		return message
	}
	return "verification_failed"
}

type integrationJournalPayload struct {
	Operation      IntegrationOperation              `json:"operation"`
	ExpectedDigest string                            `json:"expected_digest"`
	Preview        IntegrationMaterializationPreview `json:"preview"`
	PriorState     IntegrationState                  `json:"prior_state"`
	Revoked        bool                              `json:"revoked"`
	RepositoryRoot string                            `json:"repository_root"`
}

func (s *IntegrationManifestService) markIncompleteJournalsForRecovery(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id, project_id, operation, payload
		FROM integration_manifest_journal WHERE status <> 'complete' ORDER BY created_at, operation_id`)
	if err != nil {
		return fmt.Errorf("integration manifest: inspect recovery journals: %w", err)
	}
	type incomplete struct{ operationID, projectID, operation, payload string }
	var items []incomplete
	for rows.Next() {
		var item incomplete
		if err := rows.Scan(&item.operationID, &item.projectID, &item.operation, &item.payload); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		var payload integrationJournalPayload
		if err := json.Unmarshal([]byte(item.payload), &payload); err != nil || payload.PriorState.ProjectID != item.projectID {
			if err := s.failClosedRecoveryJournal(ctx, item, defaultIntegrationState(item.projectID), "", errors.New("invalid recovery journal payload"), false); err != nil {
				return err
			}
			continue
		}
		state, root, err := s.recoveryState(ctx, item.projectID)
		if err != nil {
			return err
		}
		if payload.RepositoryRoot != "" {
			root = payload.RepositoryRoot
		}
		manifest := integrationManifestMetadataFromState(payload.PriorState, payload.ExpectedDigest)
		startTx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := appendIntegrationAudit(ctx, startTx, item.projectID, "integration_manifest.recovery_started", "gateway", item.operationID, manifest, "started", ""); err != nil {
			startTx.Rollback()
			return err
		}
		if err := startTx.Commit(); err != nil {
			return err
		}
		var materializer *IntegrationMaterializer
		var recoveryErr error
		if root == "" {
			recoveryErr = errors.New("integration recovery repository root unavailable")
		} else {
			materializer, recoveryErr = NewIntegrationMaterializer(root)
		}
		if payload.Revoked {
			var next IntegrationState
			if recoveryErr == nil {
				next, recoveryErr = materializer.Apply(IntegrationMaterializationRequest{
					Operation: IntegrationRemove, State: &payload.PriorState, ProjectID: item.projectID,
					ResolvedRole: payload.PriorState.ResolvedRole, Revoked: true,
					Offline: payload.PriorState.ConnectionState != "online",
				})
			}
			if recoveryErr != nil && next.SchemaVersion == 0 {
				if err := s.failClosedRecoveryJournal(ctx, item, state, root, recoveryErr, true); err != nil {
					return err
				}
				continue
			}
			if err := s.completeRecoveryJournal(ctx, item, next, root, manifest, recoveryErr); err != nil {
				return err
			}
			continue
		}
		recovered := payload.PriorState
		if state.ApprovalState == "approved" {
			recovered.ApprovalState = "approved"
		}
		if recoveryErr == nil {
			recoveryErr = materializer.recoverRollback(payload.Preview, recovered)
		}
		if recoveryErr != nil {
			if err := s.failClosedRecoveryJournal(ctx, item, state, root, recoveryErr, false); err != nil {
				return err
			}
			continue
		}
		if err := s.completeRecoveryJournal(ctx, item, recovered, root, manifest, nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *IntegrationManifestService) recoveryState(ctx context.Context, projectID string) (IntegrationState, string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return IntegrationState{}, "", err
	}
	defer tx.Rollback()
	state, root, err := readIntegrationStateTx(ctx, tx, projectID)
	if err != nil {
		return IntegrationState{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationState{}, "", err
	}
	return state, root, nil
}

func (s *IntegrationManifestService) completeRecoveryJournal(ctx context.Context, item struct{ operationID, projectID, operation, payload string }, state IntegrationState, root string, manifest IntegrationManifest, recoveryErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	if err := appendIntegrationAudit(ctx, tx, item.projectID, "integration_manifest.recovery_completed", "gateway", item.operationID, manifest, "complete", verificationReason(recoveryErr)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE integration_manifest_journal SET status='complete', updated_at=CURRENT_TIMESTAMP WHERE operation_id=?`, item.operationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *IntegrationManifestService) failClosedRecoveryJournal(ctx context.Context, item struct{ operationID, projectID, operation, payload string }, state IntegrationState, root string, recoveryErr error, forward bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state.GuidanceActive = false
	state.ConnectionState = "attention_required"
	status := "recovery_required"
	if forward {
		state.ApprovalState = "revoked"
		state.MaterializationState = "removal_required"
		status = "forward_required"
	} else {
		state.MaterializationState = "recovery_required"
	}
	if err := writeIntegrationStateTx(ctx, tx, state, root); err != nil {
		return err
	}
	manifest := integrationManifestMetadataFromState(state, "")
	if err := appendIntegrationAudit(ctx, tx, item.projectID, "integration_manifest.drift_detected", "gateway", item.operationID, manifest, "failed", verificationReason(recoveryErr)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE integration_manifest_journal SET status=?, updated_at=CURRENT_TIMESTAMP WHERE operation_id=?`, status, item.operationID); err != nil {
		return err
	}
	return tx.Commit()
}

func integrationManifestMetadataFromState(state IntegrationState, expectedDigest string) IntegrationManifest {
	manifest := IntegrationManifest{ProjectID: state.ProjectID, ManifestDigest: expectedDigest}
	if state.ActiveManifestID != nil {
		manifest.ManifestID = *state.ActiveManifestID
	}
	if state.ActiveManifestVersion != nil {
		manifest.ManifestVersion = *state.ActiveManifestVersion
	}
	if manifest.ManifestDigest == "" && state.ActiveManifestDigest != nil {
		manifest.ManifestDigest = *state.ActiveManifestDigest
	}
	if manifest.ManifestID == "" && state.PendingManifestID != nil {
		manifest.ManifestID = *state.PendingManifestID
	}
	if manifest.ManifestVersion == 0 && state.PendingManifestVersion != nil {
		manifest.ManifestVersion = *state.PendingManifestVersion
	}
	if manifest.ManifestDigest == "" && state.PendingManifestDigest != nil {
		manifest.ManifestDigest = *state.PendingManifestDigest
	}
	return manifest
}
