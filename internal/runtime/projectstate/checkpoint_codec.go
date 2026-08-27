package projectstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	maxCheckpointPriorTreeFiles      = maxCommittedTreeFiles
	maxCheckpointPriorTreePathBytes  = maxCommittedTreePathBytes
	maxCheckpointPriorTreeFileBytes  = maxCommittedObjectBytes
	maxCheckpointPriorTreeTotalBytes = maxCommittedTreeBytes
)

type checkpointPublicationReviewV1 struct {
	SchemaVersion  int                         `json:"schema_version"`
	Kind           string                      `json:"kind"`
	Review         publicationReviewEnvelopeV1 `json:"review"`
	ReviewDigest   state.Digest                `json:"review_digest"`
	CheckpointedBy types.ActorEnvelope         `json:"checkpointed_by"`
}

type checkpointPriorCandidateV1 struct {
	SchemaVersion int                              `json:"schema_version"`
	Kind          string                           `json:"kind"`
	Candidate     *checkpointPriorCandidateStateV1 `json:"candidate"`
}

type checkpointPriorCandidateStateV1 struct {
	AcceptedBaseDigest       state.Digest           `json:"accepted_base_digest"`
	WorkingTreeDigest        state.Digest           `json:"working_tree_digest"`
	DirectTree               checkpointPriorTreeV1  `json:"direct_tree"`
	RebasedTree              *checkpointPriorTreeV1 `json:"rebased_tree"`
	RebasedThroughGeneration int64                  `json:"rebased_through_generation"`
	ImportedBy               string                 `json:"imported_by"`
	ImportedAt               time.Time              `json:"imported_at"`
}

type checkpointPriorTreeV1 struct {
	Digest state.Digest            `json:"digest"`
	Files  []checkpointPriorFileV1 `json:"files"`
}

type checkpointPriorFileV1 struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type checkpointPriorTreeLimits struct {
	maxFiles      int
	maxPathBytes  int
	maxFileBytes  int64
	maxTotalBytes int64
}

func encodeCheckpointPublicationReview(value checkpointPublicationReviewV1) (string, error) {
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("projectstate: encode checkpoint publication review: %w", err)
	}
	if _, err := decodeCheckpointPublicationReview(string(canonical)); err != nil {
		return "", err
	}
	return string(canonical), nil
}

func decodeCheckpointPublicationReview(raw string) (checkpointPublicationReviewV1, error) {
	value, err := decodeCanonicalCheckpointJSON[checkpointPublicationReviewV1](raw, "checkpoint publication review")
	if err != nil {
		return checkpointPublicationReviewV1{}, err
	}
	if value.SchemaVersion != 1 || value.Kind != "checkpoint_publication_review" {
		return checkpointPublicationReviewV1{}, fmt.Errorf("projectstate: invalid checkpoint publication review envelope")
	}
	if err := validatePublicationReviewEnvelope(value.Review); err != nil {
		return checkpointPublicationReviewV1{}, fmt.Errorf("projectstate: invalid checkpoint publication review: %w", err)
	}
	_, digest, err := encodePublicationReviewEnvelope(value.Review)
	if err != nil {
		return checkpointPublicationReviewV1{}, fmt.Errorf("projectstate: encode nested publication review: %w", err)
	}
	if digest != value.ReviewDigest {
		return checkpointPublicationReviewV1{}, fmt.Errorf("projectstate: checkpoint publication review digest mismatch")
	}
	if err := value.CheckpointedBy.ValidateLocalAction(); err != nil {
		return checkpointPublicationReviewV1{}, fmt.Errorf("projectstate: invalid checkpoint actor: %w", err)
	}
	return value, nil
}

func encodeCheckpointPriorCandidate(value checkpointPriorCandidateV1) (string, error) {
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("projectstate: encode checkpoint prior candidate: %w", err)
	}
	if _, err := decodeCheckpointPriorCandidate(string(canonical)); err != nil {
		return "", err
	}
	return string(canonical), nil
}

func decodeCheckpointPriorCandidate(raw string) (checkpointPriorCandidateV1, error) {
	return decodeCheckpointPriorCandidateWithLimits(raw, checkpointPriorTreeProductionLimits())
}

func checkpointPriorTreeProductionLimits() checkpointPriorTreeLimits {
	return checkpointPriorTreeLimits{
		maxFiles:      maxCheckpointPriorTreeFiles,
		maxPathBytes:  maxCheckpointPriorTreePathBytes,
		maxFileBytes:  maxCheckpointPriorTreeFileBytes,
		maxTotalBytes: maxCheckpointPriorTreeTotalBytes,
	}
}

func decodeCheckpointPriorCandidateWithLimits(raw string, limits checkpointPriorTreeLimits) (checkpointPriorCandidateV1, error) {
	value, err := decodeCanonicalCheckpointJSON[checkpointPriorCandidateV1](raw, "checkpoint prior candidate")
	if err != nil {
		return checkpointPriorCandidateV1{}, err
	}
	if value.SchemaVersion != 1 || value.Kind != "checkpoint_prior_candidate" {
		return checkpointPriorCandidateV1{}, fmt.Errorf("projectstate: invalid checkpoint prior candidate envelope")
	}
	if value.Candidate == nil {
		return value, nil
	}
	if err := validateCheckpointPriorCandidate(*value.Candidate, limits); err != nil {
		return checkpointPriorCandidateV1{}, err
	}
	return value, nil
}

func decodeCanonicalCheckpointJSON[T any](raw, name string) (T, error) {
	var zero T
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, fmt.Errorf("projectstate: decode %s: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return zero, fmt.Errorf("projectstate: decode %s: trailing JSON", name)
		}
		return zero, fmt.Errorf("projectstate: decode %s: %w", name, err)
	}
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return zero, fmt.Errorf("projectstate: canonicalize %s: %w", name, err)
	}
	if !bytes.Equal(canonical, []byte(raw)) {
		return zero, fmt.Errorf("projectstate: %s is not canonical", name)
	}
	return value, nil
}

func validateCheckpointPriorCandidate(candidate checkpointPriorCandidateStateV1, limits checkpointPriorTreeLimits) error {
	if !validImportDigest(candidate.AcceptedBaseDigest) || !validImportDigest(candidate.WorkingTreeDigest) {
		return fmt.Errorf("projectstate: checkpoint prior candidate has malformed digest")
	}
	if candidate.RebasedThroughGeneration < 0 || (candidate.RebasedTree == nil && candidate.RebasedThroughGeneration != 0) {
		return fmt.Errorf("projectstate: checkpoint prior candidate has invalid rebase boundary")
	}
	if !types.ValidCandidateImportOrigin(candidate.ImportedBy) {
		return fmt.Errorf("projectstate: checkpoint prior candidate has invalid import origin")
	}
	_, offset := candidate.ImportedAt.Zone()
	if candidate.ImportedAt.IsZero() || offset != 0 {
		return fmt.Errorf("projectstate: checkpoint prior candidate imported_at must be non-zero UTC")
	}
	direct, err := validateCheckpointPriorTree(candidate.DirectTree, limits)
	if err != nil {
		return fmt.Errorf("projectstate: invalid direct checkpoint prior tree: %w", err)
	}
	if candidate.WorkingTreeDigest != candidate.DirectTree.Digest {
		return fmt.Errorf("projectstate: checkpoint prior candidate working-tree digest mismatch")
	}
	if candidate.RebasedTree == nil {
		return nil
	}
	rebased, err := validateCheckpointPriorTree(*candidate.RebasedTree, limits)
	if err != nil {
		return fmt.Errorf("projectstate: invalid rebased checkpoint prior tree: %w", err)
	}
	if rebased.Config.ProjectID != direct.Config.ProjectID || rebased.Config.Repository != direct.Config.Repository {
		return fmt.Errorf("projectstate: direct and rebased checkpoint prior trees have different identity")
	}
	return nil
}

func validateCheckpointPriorTree(value checkpointPriorTreeV1, limits checkpointPriorTreeLimits) (state.Snapshot, error) {
	if limits.maxFiles <= 0 || limits.maxPathBytes <= 0 || limits.maxFileBytes <= 0 || limits.maxTotalBytes <= 0 {
		return state.Snapshot{}, fmt.Errorf("projectstate: invalid checkpoint prior tree limits")
	}
	if value.Files == nil || len(value.Files) > limits.maxFiles || !validImportDigest(value.Digest) {
		return state.Snapshot{}, fmt.Errorf("projectstate: checkpoint prior tree shape or digest is invalid")
	}
	tree := make(state.Tree, len(value.Files))
	var prior string
	var total int64
	for index, file := range value.Files {
		if !validCheckpointPriorPath(file.Path, limits.maxPathBytes) || (index > 0 && prior >= file.Path) {
			return state.Snapshot{}, fmt.Errorf("projectstate: checkpoint prior tree paths are invalid, unsorted, or duplicate")
		}
		prior = file.Path
		pathBytes, fileBytes := int64(len(file.Path)), int64(len(file.Data))
		if fileBytes > limits.maxFileBytes || pathBytes > limits.maxTotalBytes-total {
			return state.Snapshot{}, fmt.Errorf("projectstate: checkpoint prior tree exceeds byte limits")
		}
		total += pathBytes
		if fileBytes > limits.maxTotalBytes-total {
			return state.Snapshot{}, fmt.Errorf("projectstate: checkpoint prior tree exceeds byte limits")
		}
		total += fileBytes
		tree[index] = state.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	snapshot, err := state.DecodeTree(tree)
	if err != nil {
		return state.Snapshot{}, err
	}
	encoded, err := state.EncodeTree(snapshot)
	if err != nil || !checkpointPriorTreeBytesEqual(tree, encoded) {
		if err != nil {
			return state.Snapshot{}, err
		}
		return state.Snapshot{}, fmt.Errorf("projectstate: checkpoint prior tree is not a complete canonical snapshot")
	}
	digest, err := state.DigestTree(tree)
	if err != nil {
		return state.Snapshot{}, err
	}
	if digest != value.Digest || snapshot.Digest != value.Digest {
		return state.Snapshot{}, fmt.Errorf("projectstate: checkpoint prior tree digest mismatch")
	}
	return snapshot, nil
}

func validCheckpointPriorPath(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		!strings.Contains(value, `\`) && !path.IsAbs(value) && value != "." && value != ".." &&
		!strings.HasPrefix(value, "../") && path.Clean(value) == value
}

func checkpointPriorTreeBytesEqual(left, right state.Tree) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Path != right[index].Path || !bytes.Equal(left[index].Data, right[index].Data) {
			return false
		}
	}
	return true
}
