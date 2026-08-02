package projectstate

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	publicationSemanticDiffSchemaVersion = 1
	publicationSemanticDiffKind          = "semantic_diff"
	publicationReviewSchemaVersion       = 1
	publicationReviewKind                = "publication_review"
)

type publicationSemanticDiffV1 struct {
	SchemaVersion       int                   `json:"schema_version"`
	Kind                string                `json:"kind"`
	AcceptedTreeDigest  state.Digest          `json:"accepted_tree_digest"`
	CandidateTreeDigest state.Digest          `json:"candidate_tree_digest"`
	Changes             []publicationChangeV1 `json:"changes"`
}

type publicationRecordKeyV1 struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type publicationChangeV1 struct {
	Key              publicationRecordKeyV1     `json:"key"`
	Kind             ChangeKind                 `json:"kind"`
	BeforeDigest     *state.Digest              `json:"before_digest"`
	AfterDigest      *state.Digest              `json:"after_digest"`
	BeforeBodyDigest *state.Digest              `json:"before_body_digest"`
	AfterBodyDigest  *state.Digest              `json:"after_body_digest"`
	Fields           []publicationFieldChangeV1 `json:"fields"`
	Actor            *types.ActorEnvelope       `json:"actor"`
}

type publicationFieldChangeV1 struct {
	Path   string                  `json:"path"`
	Before publicationFieldValueV1 `json:"before"`
	After  publicationFieldValueV1 `json:"after"`
	Actor  *types.ActorEnvelope    `json:"actor"`
}

type publicationFieldValueV1 struct {
	Present bool            `json:"present"`
	Value   json.RawMessage `json:"value,omitempty"`
}

type publicationReviewEnvelopeV1 struct {
	SchemaVersion       int                             `json:"schema_version"`
	Kind                string                          `json:"kind"`
	Scope               types.WorkspaceScope            `json:"scope"`
	Repository          types.RepositoryIdentity        `json:"repository_identity"`
	OriginDigest        state.Digest                    `json:"origin_digest"`
	Classification      types.PublicationClassification `json:"classification"`
	PolicyRevision      int64                           `json:"policy_revision"`
	AcceptedRef         string                          `json:"accepted_ref"`
	AcceptedCommitSHA   string                          `json:"accepted_commit_sha"`
	AcceptedTreeDigest  state.Digest                    `json:"accepted_tree_digest"`
	CandidateTreeDigest state.Digest                    `json:"candidate_tree_digest"`
	SemanticDiffDigest  state.Digest                    `json:"semantic_diff_digest"`
	OverlayGeneration   int64                           `json:"overlay_generation"`
}

func encodePublicationSemanticDiff(diff Diff) ([]byte, state.Digest, error) {
	projection, err := projectPublicationSemanticDiff(diff)
	if err != nil {
		return nil, "", err
	}
	encoded, err := state.CanonicalJSON(projection)
	if err != nil {
		return nil, "", fmt.Errorf("projectstate: canonical publication semantic diff: %w", err)
	}
	digest, err := state.DigestCanonicalJSON(projection)
	if err != nil {
		return nil, "", fmt.Errorf("projectstate: digest publication semantic diff: %w", err)
	}
	return bytes.Clone(encoded), digest, nil
}

func projectPublicationSemanticDiff(diff Diff) (publicationSemanticDiffV1, error) {
	if !validPublicationDigest(diff.BaseDigest) || !validPublicationDigest(diff.ViewDigest) {
		return publicationSemanticDiffV1{}, fmt.Errorf("projectstate: publication semantic diff has malformed tree digest")
	}
	if diff.Changes == nil {
		return publicationSemanticDiffV1{}, fmt.Errorf("projectstate: publication semantic diff changes must be a non-nil array")
	}
	projection := publicationSemanticDiffV1{
		SchemaVersion:       publicationSemanticDiffSchemaVersion,
		Kind:                publicationSemanticDiffKind,
		AcceptedTreeDigest:  diff.BaseDigest,
		CandidateTreeDigest: diff.ViewDigest,
		Changes:             make([]publicationChangeV1, 0, len(diff.Changes)),
	}
	var prior state.RecordKey
	for index, change := range diff.Changes {
		if err := validatePublicationRecordKey(change.Key); err != nil {
			return publicationSemanticDiffV1{}, err
		}
		if index > 0 && comparePublicationRecordKeys(prior, change.Key) >= 0 {
			return publicationSemanticDiffV1{}, fmt.Errorf("projectstate: publication semantic diff changes are not in stable order")
		}
		prior = change.Key
		projected, err := projectPublicationChange(change)
		if err != nil {
			return publicationSemanticDiffV1{}, fmt.Errorf("projectstate: publication semantic diff change %s %s: %w", change.Key.Kind, change.Key.ID, err)
		}
		projection.Changes = append(projection.Changes, projected)
	}
	return projection, nil
}

func projectPublicationChange(change Change) (publicationChangeV1, error) {
	switch change.Kind {
	case ChangeAdd, ChangeModify, ChangeTombstone, ChangeResurrect:
	default:
		return publicationChangeV1{}, fmt.Errorf("invalid change kind %q", change.Kind)
	}
	if change.BeforeDigest == nil && change.AfterDigest == nil {
		return publicationChangeV1{}, fmt.Errorf("change has neither before nor after digest")
	}
	for name, digest := range map[string]*state.Digest{
		"before": change.BeforeDigest, "after": change.AfterDigest,
		"before body": change.BeforeBodyDigest, "after body": change.AfterBodyDigest,
	} {
		if digest != nil && !validPublicationDigest(*digest) {
			return publicationChangeV1{}, fmt.Errorf("malformed %s digest", name)
		}
	}
	if change.Fields == nil {
		return publicationChangeV1{}, fmt.Errorf("fields must be a non-nil array")
	}
	projected := publicationChangeV1{
		Key:  publicationRecordKeyV1{Kind: change.Key.Kind, ID: change.Key.ID},
		Kind: change.Kind, BeforeDigest: clonePublicationDigest(change.BeforeDigest), AfterDigest: clonePublicationDigest(change.AfterDigest),
		BeforeBodyDigest: clonePublicationDigest(change.BeforeBodyDigest), AfterBodyDigest: clonePublicationDigest(change.AfterBodyDigest),
		Fields: make([]publicationFieldChangeV1, 0, len(change.Fields)),
	}
	priorPath := ""
	for index, field := range change.Fields {
		if !validPublicationJSONPointer(field.Path) {
			return publicationChangeV1{}, fmt.Errorf("invalid field path %q", field.Path)
		}
		if index > 0 && priorPath >= field.Path {
			return publicationChangeV1{}, fmt.Errorf("fields are not in JSON-pointer order")
		}
		priorPath = field.Path
		before, err := projectPublicationFieldValue(field.Before)
		if err != nil {
			return publicationChangeV1{}, fmt.Errorf("field %q before: %w", field.Path, err)
		}
		after, err := projectPublicationFieldValue(field.After)
		if err != nil {
			return publicationChangeV1{}, fmt.Errorf("field %q after: %w", field.Path, err)
		}
		actor, err := clonePublicationActor(field.Actor)
		if err != nil {
			return publicationChangeV1{}, fmt.Errorf("field %q actor: %w", field.Path, err)
		}
		projected.Fields = append(projected.Fields, publicationFieldChangeV1{
			Path: field.Path, Before: before, After: after, Actor: actor,
		})
	}

	wantActor, err := publicationCommonFieldActor(change.Fields)
	if err != nil {
		return publicationChangeV1{}, err
	}
	if (wantActor == nil) != (change.Actor == nil) {
		return publicationChangeV1{}, fmt.Errorf("enclosing actor does not match conservative field projection")
	}
	if wantActor != nil {
		wantCanonical, err := publicationCanonicalActor(*wantActor)
		if err != nil {
			return publicationChangeV1{}, err
		}
		gotCanonical, err := publicationCanonicalActor(*change.Actor)
		if err != nil {
			return publicationChangeV1{}, err
		}
		if wantCanonical != gotCanonical {
			return publicationChangeV1{}, fmt.Errorf("enclosing actor differs from field actor")
		}
		projected.Actor = publicationActorCopy(*change.Actor)
	}
	return projected, nil
}

func projectPublicationFieldValue(value FieldValue) (publicationFieldValueV1, error) {
	if !value.Present {
		if len(value.Value) != 0 {
			return publicationFieldValueV1{}, fmt.Errorf("absent field has a value")
		}
		return publicationFieldValueV1{}, nil
	}
	canonical, err := cloneCanonicalFieldValue(value)
	if err != nil {
		return publicationFieldValueV1{}, err
	}
	return publicationFieldValueV1{Present: true, Value: bytes.Clone(canonical.Value)}, nil
}

func encodePublicationReviewEnvelope(envelope publicationReviewEnvelopeV1) ([]byte, state.Digest, error) {
	if err := validatePublicationReviewEnvelope(envelope); err != nil {
		return nil, "", err
	}
	encoded, err := state.CanonicalJSON(envelope)
	if err != nil {
		return nil, "", fmt.Errorf("projectstate: canonical publication review envelope: %w", err)
	}
	digest, err := state.DigestCanonicalJSON(envelope)
	if err != nil {
		return nil, "", fmt.Errorf("projectstate: digest publication review envelope: %w", err)
	}
	return bytes.Clone(encoded), digest, nil
}

func validatePublicationReviewEnvelope(envelope publicationReviewEnvelopeV1) error {
	if envelope.SchemaVersion != publicationReviewSchemaVersion || envelope.Kind != publicationReviewKind {
		return fmt.Errorf("projectstate: invalid publication review schema or kind")
	}
	if !types.CanonicalUUID(envelope.Scope.ProjectID) || !types.CanonicalUUID(string(envelope.Scope.WorkspaceID)) {
		return fmt.Errorf("projectstate: invalid publication review scope")
	}
	if err := envelope.Repository.Validate(); err != nil {
		return fmt.Errorf("projectstate: invalid publication review repository: %w", err)
	}
	if !validPublicationDigest(envelope.OriginDigest) || !validPublicationDigest(envelope.AcceptedTreeDigest) ||
		!validPublicationDigest(envelope.CandidateTreeDigest) || !validPublicationDigest(envelope.SemanticDiffDigest) {
		return fmt.Errorf("projectstate: malformed publication review digest")
	}
	if err := envelope.Classification.Validate(); err != nil {
		return fmt.Errorf("projectstate: invalid publication review classification: %w", err)
	}
	if envelope.PolicyRevision <= 0 {
		return fmt.Errorf("projectstate: invalid publication review policy revision")
	}
	if !validDiscardRef(envelope.AcceptedRef) || !validPublicationCommit(envelope.AcceptedCommitSHA) {
		return fmt.Errorf("projectstate: invalid publication review accepted Git base")
	}
	if envelope.OverlayGeneration < 0 {
		return fmt.Errorf("projectstate: invalid publication review overlay generation")
	}
	return nil
}

func clonePublicationActor(actor *types.ActorEnvelope) (*types.ActorEnvelope, error) {
	if actor == nil {
		return nil, nil
	}
	if _, err := publicationCanonicalActor(*actor); err != nil {
		return nil, err
	}
	return publicationActorCopy(*actor), nil
}

func clonePublicationDigest(digest *state.Digest) *state.Digest {
	if digest == nil {
		return nil
	}
	copy := *digest
	return &copy
}

func validatePublicationRecordKey(key state.RecordKey) error {
	switch key.Kind {
	case "project", "actor", "task", "task_link", "kb_article", "channel", "event", "git_link":
	default:
		return fmt.Errorf("projectstate: invalid publication record kind %q", key.Kind)
	}
	if !types.CanonicalUUID(key.ID) {
		return fmt.Errorf("projectstate: invalid publication record ID")
	}
	return nil
}

func comparePublicationRecordKeys(left, right state.RecordKey) int {
	if leftRank, rightRank := diffKindRank(left.Kind), diffKindRank(right.Kind); leftRank != rightRank {
		if leftRank < rightRank {
			return -1
		}
		return 1
	}
	if left.ID < right.ID {
		return -1
	}
	if left.ID > right.ID {
		return 1
	}
	if left.Kind < right.Kind {
		return -1
	}
	if left.Kind > right.Kind {
		return 1
	}
	return 0
}

func validPublicationCommit(commit string) bool {
	return (len(commit) == 40 || len(commit) == 64) && validPublicationLowerHex(commit)
}

func validPublicationLowerHex(value string) bool {
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validPublicationJSONPointer(path string) bool {
	if path == "" {
		return true
	}
	if path[0] != '/' {
		return false
	}
	for index := 0; index < len(path); index++ {
		if path[index] != '~' {
			continue
		}
		if index+1 >= len(path) || (path[index+1] != '0' && path[index+1] != '1') {
			return false
		}
		index++
	}
	return true
}
