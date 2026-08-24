package localstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
	projectstate "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const workspaceCheckpointCommitStateVersion uint8 = 1

// WorkspaceCheckpointCommitState is the legacy-only complete checkpoint token
// retained until Task 12 replaces checkpoint and recovery confirmation.
type WorkspaceCheckpointCommitState struct {
	version         uint8
	scope           types.WorkspaceScope
	binding         workspacePublicationBindingEvidence
	policy          workspacePublicationRawRecord
	policyHistory   []workspacePublicationRawRecord
	materialization WorkspaceMaterializationDisposition
	adjacent        workspaceMaterializationAdjacentEvidence
}

type WorkspaceCheckpointCommitMatch uint8

const (
	WorkspaceCheckpointCommitThird WorkspaceCheckpointCommitMatch = iota
	WorkspaceCheckpointCommitPrior
	WorkspaceCheckpointCommitNext
)

// CaptureCheckpointCommitState is legacy-only; new current readers must not
// route through this complete-state scanner.
func (tx *WorkspaceMutationTx) CaptureCheckpointCommitState(ctx context.Context) (WorkspaceCheckpointCommitState, error) {
	if tx == nil || tx.conn == nil || !validWorkspaceScope(tx.scope) {
		return WorkspaceCheckpointCommitState{}, ErrNotFound
	}
	binding, err := tx.publicationBindingEvidence(ctx)
	if err != nil {
		return WorkspaceCheckpointCommitState{}, fmt.Errorf("localstore: capture checkpoint binding: %w", err)
	}
	policy, history, err := tx.publicationPolicyState(ctx)
	if err != nil {
		return WorkspaceCheckpointCommitState{}, fmt.Errorf("localstore: capture checkpoint publication policy: %w", err)
	}
	disposition, err := tx.MaterializationDisposition(ctx)
	if err != nil {
		return WorkspaceCheckpointCommitState{}, fmt.Errorf("localstore: capture checkpoint materialization disposition: %w", err)
	}
	candidate, err := tx.Candidate(ctx)
	if err != nil {
		return WorkspaceCheckpointCommitState{}, fmt.Errorf("localstore: capture checkpoint candidate: %w", err)
	}
	adjacent, err := tx.materializationAdjacentEvidence(ctx, binding.Record.Binding, disposition, candidate)
	if err != nil {
		return WorkspaceCheckpointCommitState{}, fmt.Errorf("localstore: capture checkpoint adjacent evidence: %w", err)
	}
	projectedRevision, err := tx.projectedWorkspaceRevision(ctx)
	if err != nil {
		return WorkspaceCheckpointCommitState{}, fmt.Errorf("localstore: project checkpoint binding revision: %w", err)
	}
	binding.Record.WorkspaceRevision = projectedRevision
	state := cloneWorkspaceCheckpointCommitState(WorkspaceCheckpointCommitState{
		version:         workspaceCheckpointCommitStateVersion,
		scope:           tx.scope,
		binding:         binding,
		policy:          policy,
		policyHistory:   history,
		materialization: disposition,
		adjacent:        adjacent,
	})
	if !validWorkspaceCheckpointCommitState(state) {
		return WorkspaceCheckpointCommitState{}, fmt.Errorf("localstore: captured malformed checkpoint commit state")
	}
	return state, nil
}

// ConfirmCheckpointCommit is legacy-only until Task 12 installs targeted
// revision and journal confirmation.
func (r *WorkspaceRepo) ConfirmCheckpointCommit(
	ctx context.Context,
	prior WorkspaceCheckpointCommitState,
	next WorkspaceCheckpointCommitState,
) (WorkspaceCheckpointCommitMatch, error) {
	if r == nil || r.db == nil {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: confirm checkpoint commit: %w", ErrNotFound)
	}
	if !validWorkspaceCheckpointCommitState(prior) {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: invalid prior checkpoint commit state")
	}
	if !validWorkspaceCheckpointCommitState(next) {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: invalid next checkpoint commit state")
	}
	if prior.scope != next.scope {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: checkpoint commit states have different scopes")
	}
	if equalWorkspaceCheckpointCommitStates(prior, next) {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: checkpoint commit states are identical")
	}

	conn, err := r.db.Conn(ctx)
	if err != nil {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: acquire checkpoint commit confirmation connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: begin checkpoint commit confirmation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	current, err := (&WorkspaceMutationTx{conn: conn, scope: prior.scope}).CaptureCheckpointCommitState(ctx)
	if err != nil {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: read checkpoint commit confirmation: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return WorkspaceCheckpointCommitThird, fmt.Errorf("localstore: commit checkpoint confirmation read: %w", err)
	}
	committed = true
	return classifyWorkspaceCheckpointCommitState(current, prior, next), nil
}

func classifyWorkspaceCheckpointCommitState(
	current WorkspaceCheckpointCommitState,
	prior WorkspaceCheckpointCommitState,
	next WorkspaceCheckpointCommitState,
) WorkspaceCheckpointCommitMatch {
	if equalWorkspaceCheckpointCommitStates(current, next) {
		return WorkspaceCheckpointCommitNext
	}
	if equalWorkspaceCheckpointCommitStates(current, prior) {
		return WorkspaceCheckpointCommitPrior
	}
	return WorkspaceCheckpointCommitThird
}

func cloneWorkspaceCheckpointCommitState(value WorkspaceCheckpointCommitState) WorkspaceCheckpointCommitState {
	cloned := value
	cloned.binding = cloneWorkspaceCheckpointBindingEvidence(value.binding)
	cloned.policy = cloneWorkspaceCheckpointPublicationRawRecord(value.policy)
	if value.policyHistory != nil {
		cloned.policyHistory = make([]workspacePublicationRawRecord, len(value.policyHistory))
		for index := range value.policyHistory {
			cloned.policyHistory[index] = cloneWorkspaceCheckpointPublicationRawRecord(value.policyHistory[index])
		}
	}
	cloned.materialization = cloneWorkspaceCheckpointMaterializationDisposition(value.materialization)
	cloned.adjacent = cloneWorkspaceCheckpointAdjacentEvidence(value.adjacent)
	return cloned
}

func equalWorkspaceCheckpointCommitStates(left, right WorkspaceCheckpointCommitState) bool {
	if left.version != right.version || left.scope != right.scope ||
		!equalWorkspaceCheckpointPublicationBindingEvidence(left.binding, right.binding) ||
		!equalWorkspaceCheckpointPublicationRawRecords(left.policy, right.policy) ||
		(left.policyHistory == nil) != (right.policyHistory == nil) ||
		len(left.policyHistory) != len(right.policyHistory) ||
		!equalWorkspaceCheckpointMaterializationDispositions(left.materialization, right.materialization) ||
		!equalWorkspaceCheckpointAdjacentEvidence(left.adjacent, right.adjacent) {
		return false
	}
	for index := range left.policyHistory {
		if !equalWorkspaceCheckpointPublicationRawRecords(left.policyHistory[index], right.policyHistory[index]) {
			return false
		}
	}
	return true
}

func equalWorkspaceCheckpointPublicationBindingEvidence(
	left workspacePublicationBindingEvidence,
	right workspacePublicationBindingEvidence,
) bool {
	leftCopy := left
	rightCopy := right
	leftCopy.Record.WorkspaceRevision = 0
	rightCopy.Record.WorkspaceRevision = 0
	return equalWorkspacePublicationBindingEvidence(leftCopy, rightCopy)
}

func validWorkspaceCheckpointCommitState(value WorkspaceCheckpointCommitState) bool {
	if value.version != workspaceCheckpointCommitStateVersion || !validWorkspaceScope(value.scope) ||
		!validWorkspaceCheckpointBindingEvidence(value.binding, value.scope) || value.policyHistory == nil ||
		value.materialization.Journals == nil || value.materialization.Operations == nil || value.adjacent.Operations == nil {
		return false
	}
	if !validWorkspaceCheckpointPublicationState(value.policy, value.policyHistory) ||
		!validWorkspaceCheckpointMaterialization(value.materialization, value.binding.Record.Binding) ||
		!validWorkspaceCheckpointAdjacent(value.adjacent, value.binding.Record.Binding) ||
		len(value.materialization.Operations) != len(value.adjacent.Operations) {
		return false
	}
	for index := range value.adjacent.Operations {
		evidence := value.adjacent.Operations[index]
		if evidence.ProjectID != value.scope.ProjectID || evidence.WorkspaceID != string(value.scope.WorkspaceID) ||
			!equalWorkspaceCheckpointOperations(value.materialization.Operations[index], evidence.Operation) {
			return false
		}
	}
	if value.adjacent.Candidate != nil &&
		(value.adjacent.Candidate.ProjectID != value.scope.ProjectID ||
			value.adjacent.Candidate.WorkspaceID != string(value.scope.WorkspaceID)) {
		return false
	}
	return true
}

func validWorkspaceCheckpointBindingEvidence(
	value workspacePublicationBindingEvidence,
	scope types.WorkspaceScope,
) bool {
	if value.Record.Binding.Scope != scope || value.Record.Binding.Validate() != nil ||
		!validWorkspaceBindingState(value.Record.State) || value.SnapshotBytes == nil ||
		value.StorageClasses != ([13]string{
			"text", "text", "text", "integer", "integer", "text", "text",
			"text", "text", "blob", "text", "text", "text",
		}) || !validStoredWorkspaceTimestamp(value.CreatedAt, value.CreatedAtRaw, value.StorageClasses[11]) ||
		!validStoredWorkspaceTimestamp(value.UpdatedAt, value.UpdatedAtRaw, value.StorageClasses[12]) ||
		value.UpdatedAt.Before(value.CreatedAt) {
		return false
	}
	_, canonical, err := canonicalWorkspaceRecordSnapshot(value.Record)
	if err != nil || !bytes.Equal(canonical, value.SnapshotBytes) {
		return false
	}
	repositoryJSON, err := json.Marshal(value.Record.Binding.Repository)
	return err == nil && string(repositoryJSON) == value.RepositoryJSON
}

func validWorkspaceCheckpointPublicationState(current workspacePublicationRawRecord, history []workspacePublicationRawRecord) bool {
	if len(history) == 0 || int64(len(history)) != current.Record.PolicyRevision ||
		!validWorkspaceCheckpointPublicationRawRecord(current, false) {
		return false
	}
	for index := range history {
		if history[index].Record.PolicyRevision != int64(index+1) ||
			!validWorkspaceCheckpointPublicationRawRecord(history[index], true) {
			return false
		}
		if index == 0 {
			if history[index].Record.TransitionKind != "bootstrap" {
				return false
			}
			continue
		}
		if history[index].RecordedAt.Before(history[index-1].RecordedAt) ||
			validateWorkspacePublicationPolicyProgression(history[index-1].Record, history[index].Record) != nil {
			return false
		}
	}
	final := history[len(history)-1]
	return equalWorkspacePublicationPolicyRecords(current.Record, final.Record) &&
		current.RepositoryJSON == final.RepositoryJSON &&
		equalNullableStrings(current.OriginValue, final.OriginValue) &&
		equalNullableStrings(current.ActorJSON, final.ActorJSON) &&
		equalNullableStrings(current.ChangedAtRaw, final.ChangedAtRaw)
}

func validWorkspaceCheckpointPublicationRawRecord(value workspacePublicationRawRecord, history bool) bool {
	if value.OriginValue.Valid != (value.Record.OriginDigest != nil) ||
		value.ActorJSON.Valid != (value.Record.ChangedBy != nil) ||
		value.ChangedAtRaw.Valid != (value.Record.ChangedAt != nil) {
		return false
	}
	decoded := cloneWorkspaceCheckpointPublicationRawRecord(value)
	return decodeWorkspacePublicationPolicyRaw(&decoded, history) == nil &&
		equalWorkspaceCheckpointPublicationRawRecords(decoded, value)
}

func validWorkspaceCheckpointMaterialization(
	value WorkspaceMaterializationDisposition,
	binding types.WorkspaceBinding,
) bool {
	for index := range value.Journals {
		if index > 0 && value.Journals[index-1].JournalID >= value.Journals[index].JournalID {
			return false
		}
		if !validWorkspaceCheckpointJournal(value.Journals[index], binding) {
			return false
		}
	}
	operationIDs := make(map[string]struct{}, len(value.Operations))
	for index := range value.Operations {
		operation := value.Operations[index]
		if (index > 0 && value.Operations[index-1].Generation >= operation.Generation) ||
			validateWorkspaceOperation(operation) != nil {
			return false
		}
		if _, exists := operationIDs[operation.OperationID]; exists {
			return false
		}
		operationIDs[operation.OperationID] = struct{}{}
	}
	return true
}

func validWorkspaceCheckpointJournal(value WorkspaceMaterializationRecord, binding types.WorkspaceBinding) bool {
	if !validLegacyMaterializationJournalID(value.JournalID) || !validMaterializationDigest(value.ExpectedLiveDigest) ||
		!validMaterializationDigest(value.AcceptedBaseDigest) || value.Checkout != binding.Checkout ||
		!validMaterializationDigest(value.PriorTreeDigest) || !validMaterializationDigest(value.CandidateDigest) ||
		value.ExpectedLiveDigest != value.PriorTreeDigest || value.ThroughGeneration < 0 ||
		!validWorkspaceMaterializationMutationMetadata(value.mutationMetadata) ||
		!validMaterializationPath(value.StagePath) || !validMaterializationPath(value.BackupPath) ||
		value.StagePath == value.BackupPath || !validWorkspaceMaterializationPublicationProof(value) {
		return false
	}
	switch value.State {
	case "prepared", "published", "recovered_new":
		if value.AcceptedBaseDigest != projectstate.Digest(binding.AcceptedTreeDigest) {
			return false
		}
	case "accepted", "recovered_old":
	default:
		return false
	}
	priorRaw, err := encodeFileList(value.PriorTree)
	if err != nil {
		return false
	}
	prior, err := strictMaterializationTree(priorRaw, value.PriorTreeDigest, binding)
	if err != nil || !equalWorkspaceTrees(prior, value.PriorTree) {
		return false
	}
	candidateRaw, err := encodeFileList(value.CandidateTree)
	if err != nil {
		return false
	}
	candidate, err := strictMaterializationTree(candidateRaw, value.CandidateDigest, binding)
	return err == nil && equalWorkspaceTrees(candidate, value.CandidateTree)
}

func validWorkspaceCheckpointAdjacent(
	value workspaceMaterializationAdjacentEvidence,
	binding types.WorkspaceBinding,
) bool {
	for index := range value.Operations {
		evidence := value.Operations[index]
		wantClasses := [8]string{"text", "text", "integer", "text", "text", "text", "null", "text"}
		if evidence.Operation.StashedByStashID != nil {
			wantClasses[6] = "text"
		}
		if evidence.ProjectID != binding.Scope.ProjectID || evidence.WorkspaceID != string(binding.Scope.WorkspaceID) ||
			evidence.StorageClasses != wantClasses || validateWorkspaceOperation(evidence.Operation) != nil ||
			!validStoredWorkspaceTimestamp(evidence.CreatedAt, evidence.CreatedAtRaw, evidence.StorageClasses[7]) {
			return false
		}
	}
	if value.Candidate == nil {
		return true
	}
	candidate := value.Candidate
	wantClasses := [9]string{"text", "text", "text", "text", "blob", "null", "integer", "text", "text"}
	if candidate.RebasedBytes != nil {
		wantClasses[5] = "blob"
	}
	if candidate.ProjectID != binding.Scope.ProjectID || candidate.WorkspaceID != string(binding.Scope.WorkspaceID) ||
		candidate.AcceptedBaseDigest != projectstate.Digest(binding.AcceptedTreeDigest) ||
		!validMaterializationDigest(candidate.WorkingTreeDigest) || candidate.DirectBytes == nil ||
		candidate.RebasedThroughGeneration < 0 || !types.ValidCandidateImportOrigin(candidate.ImportedBy) ||
		!validStoredWorkspaceTimestamp(candidate.ImportedAt, candidate.ImportedAtRaw, candidate.StorageClasses[8]) ||
		candidate.StorageClasses != wantClasses {
		return false
	}
	direct, err := decodeCandidateSnapshot(candidate.DirectBytes, binding)
	if err != nil || direct.Digest != candidate.WorkingTreeDigest {
		return false
	}
	if candidate.RebasedBytes == nil {
		return candidate.RebasedThroughGeneration == 0
	}
	_, err = decodeCandidateSnapshot(candidate.RebasedBytes, binding)
	return err == nil
}

func cloneWorkspaceCheckpointBindingEvidence(value workspacePublicationBindingEvidence) workspacePublicationBindingEvidence {
	cloned := value
	cloned.Record.Snapshot = cloneWorkspaceCheckpointSnapshot(value.Record.Snapshot)
	cloned.SnapshotBytes = bytes.Clone(value.SnapshotBytes)
	return cloned
}

func cloneWorkspaceCheckpointPublicationRawRecord(value workspacePublicationRawRecord) workspacePublicationRawRecord {
	cloned := value
	cloned.Record = cloneWorkspacePublicationPolicyRecord(value.Record)
	return cloned
}

func cloneWorkspaceCheckpointMaterializationDisposition(value WorkspaceMaterializationDisposition) WorkspaceMaterializationDisposition {
	cloned := WorkspaceMaterializationDisposition{}
	if value.Journals != nil {
		cloned.Journals = make([]WorkspaceMaterializationRecord, len(value.Journals))
		for index := range value.Journals {
			cloned.Journals[index] = cloneWorkspaceMaterializationRecord(value.Journals[index])
		}
	}
	if value.Operations != nil {
		cloned.Operations = make([]WorkspaceOperation, len(value.Operations))
		for index := range value.Operations {
			cloned.Operations[index] = cloneWorkspaceCheckpointOperation(value.Operations[index])
		}
	}
	return cloned
}

func cloneWorkspaceCheckpointAdjacentEvidence(value workspaceMaterializationAdjacentEvidence) workspaceMaterializationAdjacentEvidence {
	cloned := workspaceMaterializationAdjacentEvidence{}
	if value.Operations != nil {
		cloned.Operations = make([]workspaceMaterializationOperationEvidence, len(value.Operations))
		for index := range value.Operations {
			cloned.Operations[index] = value.Operations[index]
			cloned.Operations[index].Operation = cloneWorkspaceCheckpointOperation(value.Operations[index].Operation)
		}
	}
	if value.Candidate != nil {
		candidate := *value.Candidate
		candidate.DirectBytes = bytes.Clone(value.Candidate.DirectBytes)
		candidate.RebasedBytes = bytes.Clone(value.Candidate.RebasedBytes)
		cloned.Candidate = &candidate
	}
	return cloned
}

func cloneWorkspaceCheckpointOperation(value WorkspaceOperation) WorkspaceOperation {
	cloned := value
	cloned.OperationJSON = bytes.Clone(value.OperationJSON)
	if value.StashedByStashID != nil {
		owner := *value.StashedByStashID
		cloned.StashedByStashID = &owner
	}
	return cloned
}

func equalWorkspaceCheckpointPublicationRawRecords(left, right workspacePublicationRawRecord) bool {
	return equalWorkspacePublicationPolicyRecords(left.Record, right.Record) &&
		left.RepositoryJSON == right.RepositoryJSON && left.OriginValue == right.OriginValue &&
		left.ActorJSON == right.ActorJSON && left.ChangedAtRaw == right.ChangedAtRaw &&
		left.CreatedAt.Equal(right.CreatedAt) && left.CreatedAtRaw == right.CreatedAtRaw &&
		left.UpdatedAt.Equal(right.UpdatedAt) && left.UpdatedAtRaw == right.UpdatedAtRaw &&
		left.RecordedAt.Equal(right.RecordedAt) && left.RecordedAtRaw == right.RecordedAtRaw &&
		left.StorageClasses == right.StorageClasses
}

func equalWorkspaceCheckpointMaterializationDispositions(left, right WorkspaceMaterializationDisposition) bool {
	if (left.Journals == nil) != (right.Journals == nil) || (left.Operations == nil) != (right.Operations == nil) ||
		len(left.Journals) != len(right.Journals) || len(left.Operations) != len(right.Operations) {
		return false
	}
	for index := range left.Journals {
		if !equalWorkspaceMaterializationRecords(left.Journals[index], right.Journals[index]) {
			return false
		}
	}
	return equalWorkspaceMaterializationOperations(left.Operations, right.Operations)
}

func equalWorkspaceCheckpointAdjacentEvidence(left, right workspaceMaterializationAdjacentEvidence) bool {
	return (left.Operations == nil) == (right.Operations == nil) &&
		equalWorkspaceMaterializationAdjacentEvidence(left, right)
}

func equalWorkspaceCheckpointOperations(left, right WorkspaceOperation) bool {
	return left.Generation == right.Generation && left.OperationID == right.OperationID &&
		bytes.Equal(left.OperationJSON, right.OperationJSON) && left.State == right.State &&
		equalOptionalMaterializationString(left.StashedByStashID, right.StashedByStashID)
}

func cloneWorkspaceCheckpointSnapshot(value projectstate.Snapshot) projectstate.Snapshot {
	cloned := value
	cloned.Project = cloneWorkspaceCheckpointProject(value.Project)
	if value.Remotes != nil {
		remotes := *value.Remotes
		remotes.Fabrics = cloneWorkspaceCheckpointSlice(value.Remotes.Fabrics)
		cloned.Remotes = &remotes
	}
	cloned.Actors = cloneWorkspaceCheckpointRecords(value.Actors, cloneWorkspaceCheckpointActor)
	cloned.Tasks = cloneWorkspaceCheckpointRecords(value.Tasks, cloneWorkspaceCheckpointTask)
	cloned.TaskLinks = cloneWorkspaceCheckpointRecords(value.TaskLinks, cloneWorkspaceCheckpointTaskLink)
	if value.Articles == nil {
		cloned.Articles = nil
	} else {
		cloned.Articles = make(map[string]projectstate.KBRecord, len(value.Articles))
		for id, record := range value.Articles {
			copyRecord := projectstate.KBRecord{Body: bytes.Clone(record.Body)}
			if record.Value != nil {
				article := cloneWorkspaceCheckpointArticle(*record.Value)
				copyRecord.Value = &article
			}
			copyRecord.Tombstone = cloneWorkspaceCheckpointTombstone(record.Tombstone)
			cloned.Articles[id] = copyRecord
		}
	}
	cloned.Channels = cloneWorkspaceCheckpointRecords(value.Channels, cloneWorkspaceCheckpointChannel)
	if value.Events == nil {
		cloned.Events = nil
	} else {
		cloned.Events = make(map[string]projectstate.EventV1, len(value.Events))
		for id, event := range value.Events {
			cloned.Events[id] = cloneWorkspaceCheckpointEvent(event)
		}
	}
	cloned.GitLinks = cloneWorkspaceCheckpointRecords(value.GitLinks, cloneWorkspaceCheckpointGitLink)
	return cloned
}

func cloneWorkspaceCheckpointRecords[T any](
	values map[string]projectstate.Record[T],
	cloneValue func(T) T,
) map[string]projectstate.Record[T] {
	if values == nil {
		return nil
	}
	cloned := make(map[string]projectstate.Record[T], len(values))
	for id, record := range values {
		copyRecord := projectstate.Record[T]{Tombstone: cloneWorkspaceCheckpointTombstone(record.Tombstone)}
		if record.Value != nil {
			value := cloneValue(*record.Value)
			copyRecord.Value = &value
		}
		cloned[id] = copyRecord
	}
	return cloned
}

func cloneWorkspaceCheckpointProject(value projectstate.ProjectV1) projectstate.ProjectV1 {
	value.Aliases = cloneWorkspaceCheckpointSlice(value.Aliases)
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointActor(value projectstate.ActorV1) projectstate.ActorV1 {
	value.PublicKeys = cloneWorkspaceCheckpointSlice(value.PublicKeys)
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointTask(value projectstate.TaskV1) projectstate.TaskV1 {
	value.ParentTaskID = cloneWorkspaceCheckpointPointer(value.ParentTaskID)
	value.OwnerActorID = cloneWorkspaceCheckpointPointer(value.OwnerActorID)
	value.DueBy = cloneWorkspaceCheckpointPointer(value.DueBy)
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointTaskLink(value projectstate.TaskLinkV1) projectstate.TaskLinkV1 {
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointArticle(value projectstate.KBArticleV1) projectstate.KBArticleV1 {
	if value.Frontmatter == nil {
		value.Frontmatter = nil
	} else {
		frontmatter := make(map[string]json.RawMessage, len(value.Frontmatter))
		for key, raw := range value.Frontmatter {
			frontmatter[key] = bytes.Clone(raw)
		}
		value.Frontmatter = frontmatter
	}
	value.RelatedArticleIDs = cloneWorkspaceCheckpointSlice(value.RelatedArticleIDs)
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointChannel(value projectstate.ChannelV1) projectstate.ChannelV1 {
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointEvent(value projectstate.EventV1) projectstate.EventV1 {
	value.Payload = bytes.Clone(value.Payload)
	value.Note = cloneWorkspaceCheckpointPointer(value.Note)
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointGitLink(value projectstate.GitLinkV1) projectstate.GitLinkV1 {
	value.TaskID = cloneWorkspaceCheckpointPointer(value.TaskID)
	value.CommitSHA = cloneWorkspaceCheckpointPointer(value.CommitSHA)
	value.PRURL = cloneWorkspaceCheckpointPointer(value.PRURL)
	value.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return value
}

func cloneWorkspaceCheckpointTombstone(value *projectstate.TombstoneV1) *projectstate.TombstoneV1 {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.DeletedBodyDigest = cloneWorkspaceCheckpointPointer(value.DeletedBodyDigest)
	cloned.Extensions = cloneWorkspaceCheckpointExtensions(value.Extensions)
	return &cloned
}

func cloneWorkspaceCheckpointExtensions(value projectstate.ExtensionsV1) projectstate.ExtensionsV1 {
	if value == nil {
		return nil
	}
	cloned := make(projectstate.ExtensionsV1, len(value))
	for key, extension := range value {
		extension.Data = bytes.Clone(extension.Data)
		cloned[key] = extension
	}
	return cloned
}

func cloneWorkspaceCheckpointSlice[T any](value []T) []T {
	if value == nil {
		return nil
	}
	return append(make([]T, 0, len(value)), value...)
}

func cloneWorkspaceCheckpointPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
