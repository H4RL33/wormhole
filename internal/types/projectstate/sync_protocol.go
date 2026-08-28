package projectstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/H4RL33/wormhole/internal/types"
)

const SyncProtocolVersionV2 = 2

var ErrInvalidRepositoryScope = errors.New("projectstate: invalid repository scope")

type repositoryScopeProjection struct {
	Provider     string `json:"provider"`
	ImmutableID  string `json:"immutable_id"`
	CanonicalRef string `json:"canonical_ref"`
}

func RepositoryScopeProjection(repository types.RepositoryIdentity, canonicalRef string) ([]byte, error) {
	if err := repository.Validate(); err != nil || repository.Provider == "" || canonicalRef == "" {
		return nil, ErrInvalidRepositoryScope
	}
	projection, err := CanonicalJSON(repositoryScopeProjection{
		Provider: repository.Provider, ImmutableID: repository.ImmutableID, CanonicalRef: canonicalRef,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: projection: %v", ErrInvalidRepositoryScope, err)
	}
	return projection, nil
}

func RepositoryScopeKey(repository types.RepositoryIdentity, canonicalRef string) (string, error) {
	projection, err := RepositoryScopeProjection(repository, canonicalRef)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(projection)
	return "repository:sha256:" + hex.EncodeToString(digest[:]), nil
}

type SyncV2Scope struct {
	Version                int                      `json:"version" const:"2"`
	AttachmentRef          string                   `json:"attachment_ref"`
	Repository             types.RepositoryIdentity `json:"repository"`
	CanonicalRef           string                   `json:"canonical_ref"`
	BaseCommitSHA          string                   `json:"base_commit_sha"`
	BaseTreeDigest         Digest                   `json:"base_tree_digest"`
	ExpectedStreamVersion  int64                    `json:"expected_stream_version"`
	ExpectedLiveTreeDigest Digest                   `json:"expected_live_tree_digest"`
}

type SyncStateV2 struct {
	StreamVersion      int64    `json:"stream_version"`
	AcceptedCommitSHA  string   `json:"accepted_commit_sha"`
	AcceptedTreeDigest Digest   `json:"accepted_tree_digest"`
	LiveTreeDigest     Digest   `json:"live_tree_digest"`
	AcceptedTree       Tree     `json:"accepted_tree"`
	LiveTree           Tree     `json:"live_tree"`
	OpenConflictIDs    []string `json:"open_conflict_ids"`
}

type SyncAttachV2Args struct {
	Version        int                      `json:"version" const:"2"`
	Repository     types.RepositoryIdentity `json:"repository"`
	CanonicalRef   string                   `json:"canonical_ref"`
	BaseCommitSHA  string                   `json:"base_commit_sha"`
	BaseTreeDigest Digest                   `json:"base_tree_digest"`
}

type SyncAttachV2Result struct {
	Version                 int                       `json:"version" const:"2"`
	AttachmentRef           string                    `json:"attachment_ref"`
	RemoteProjectID         string                    `json:"remote_project_id"`
	StreamID                string                    `json:"stream_id"`
	StreamVersion           int64                     `json:"stream_version"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
}

type PublicAgentSessionIssueV2Args struct {
	Version        int    `json:"version" const:"2"`
	AttachmentRef  string `json:"attachment_ref"`
	AgentID        string `json:"agent_id"`
	HarnessName    string `json:"harness_name"`
	HarnessVersion string `json:"harness_version"`
	ModelName      string `json:"model_name"`
	ModelVersion   string `json:"model_version"`
}

type PublicAgentSessionIssueV2Result struct {
	Version            int             `json:"version" const:"2"`
	SessionID          string          `json:"session_id"`
	AgentID            string          `json:"agent_id"`
	AccountableHumanID string          `json:"accountable_human_id"`
	HarnessName        string          `json:"harness_name"`
	HarnessVersion     string          `json:"harness_version"`
	ModelName          string          `json:"model_name"`
	ModelVersion       string          `json:"model_version"`
	Assurance          types.Assurance `json:"assurance" const:"public-key-continuity"`
	ExpiresAt          time.Time       `json:"expires_at"`
}

type SyncBootstrapV2Args struct {
	SyncV2Scope
	AfterVersion int64 `json:"after_version"`
}

type SyncBootstrapV2Result struct {
	Version                 int                       `json:"version" const:"2"`
	Changed                 bool                      `json:"changed"`
	State                   SyncStateV2               `json:"state"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
}

type SyncPullV2Args struct {
	SyncV2Scope
	AfterVersion int64 `json:"after_version"`
}

type SyncPullV2Result struct {
	Version int         `json:"version" const:"2"`
	Changed bool        `json:"changed"`
	State   SyncStateV2 `json:"state"`
}

type SyncPushV2Args struct {
	SyncV2Scope
	Operation OperationV1 `json:"operation"`
}

type SyncPushAppliedV2Result struct {
	Version        int    `json:"version" const:"2"`
	Status         string `json:"status" const:"applied"`
	OperationID    string `json:"operation_id"`
	StreamVersion  int64  `json:"stream_version"`
	LiveTreeDigest Digest `json:"live_tree_digest"`
}

type SyncPushConflictV2Result struct {
	Version        int    `json:"version" const:"2"`
	Status         string `json:"status" const:"conflict"`
	OperationID    string `json:"operation_id"`
	StreamVersion  int64  `json:"stream_version"`
	LiveTreeDigest Digest `json:"live_tree_digest"`
	ConflictID     string `json:"conflict_id"`
}

type SyncConflictV2Args struct {
	SyncV2Scope
	ConflictID string      `json:"conflict_id"`
	Resolution OperationV1 `json:"resolution"`
}

type SyncConflictResolvedV2Result struct {
	Version        int    `json:"version" const:"2"`
	Status         string `json:"status" const:"resolved"`
	ConflictID     string `json:"conflict_id"`
	OperationID    string `json:"operation_id"`
	StreamVersion  int64  `json:"stream_version"`
	LiveTreeDigest Digest `json:"live_tree_digest"`
}

type ActivityAcceptV1Args struct {
	Version        int        `json:"version" const:"1"`
	AttachmentRef  string     `json:"attachment_ref"`
	PolicyVersion  int64      `json:"policy_version"`
	PolicyDigest   Digest     `json:"policy_digest"`
	Activity       ActivityV1 `json:"activity"`
	ActivityDigest Digest     `json:"activity_digest"`
}

type ActivityAcceptedV1Result struct {
	Version                 int                       `json:"version" const:"1"`
	Status                  string                    `json:"status" const:"accepted"`
	Receipt                 ActivityReceiptV1         `json:"receipt"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
	PolicyDigest            Digest                    `json:"policy_digest"`
}

type ActivityPolicyChangedV1Result struct {
	Version                 int                       `json:"version" const:"1"`
	Status                  string                    `json:"status" const:"policy_changed"`
	EffectiveActivityPolicy EffectiveActivityPolicyV1 `json:"effective_activity_policy"`
	PolicyDigest            Digest                    `json:"policy_digest"`
}

type ActivityPresenceV1Args = ActivityAcceptV1Args

type ActivityPresenceAcceptedV1Result struct {
	Version int    `json:"version" const:"1"`
	Status  string `json:"status" const:"accepted"`
}

type ActivityPullV1Args struct {
	Version       int    `json:"version" const:"1"`
	AttachmentRef string `json:"attachment_ref"`
	AfterSequence int64  `json:"after_sequence"`
	Limit         int    `json:"limit"`
}

type ActivityPolicyEvidenceV1 struct {
	Policy       EffectiveActivityPolicyV1 `json:"policy"`
	PolicyDigest Digest                    `json:"policy_digest"`
}

type ActivityDeliveryV1 struct {
	SourceRef      string            `json:"source_ref"`
	Activity       ActivityV1        `json:"activity"`
	ActivityDigest Digest            `json:"activity_digest"`
	Receipt        ActivityReceiptV1 `json:"receipt"`
}

type ActivityPullV1Result struct {
	Version            int                        `json:"version" const:"1"`
	EffectivePolicy    EffectiveActivityPolicyV1  `json:"effective_activity_policy"`
	PolicyDigest       Digest                     `json:"policy_digest"`
	HistoricalPolicies []ActivityPolicyEvidenceV1 `json:"historical_policies"`
	Deliveries         []ActivityDeliveryV1       `json:"deliveries"`
	NextSequence       int64                      `json:"next_sequence"`
	HasMore            bool                       `json:"has_more"`
}

type ActivityLifecycleV1Args struct {
	Version       int    `json:"version" const:"1"`
	AttachmentRef string `json:"attachment_ref"`
	ActivityID    string `json:"activity_id"`
	Kind          string `json:"kind"`
	ReferenceID   string `json:"reference_id"`
	ExpectedState string `json:"expected_state"`
	NextState     string `json:"next_state"`
}

type ActivityLifecycleV1Result struct {
	Version int    `json:"version" const:"1"`
	State   string `json:"state"`
}
