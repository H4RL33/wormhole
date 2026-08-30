package projectstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/H4RL33/wormhole/internal/types"
)

type OperationKind string

const (
	OperationPutRecord    OperationKind = "put_record"
	OperationPutKBArticle OperationKind = "put_kb_article"
	OperationTombstone    OperationKind = "tombstone"
	OperationResurrect    OperationKind = "resurrect"
)

type RecordValueV1 struct {
	Project  *ProjectV1  `json:"project,omitempty"`
	Actor    *ActorV1    `json:"actor,omitempty"`
	Task     *TaskV1     `json:"task,omitempty"`
	TaskLink *TaskLinkV1 `json:"task_link,omitempty"`
	Channel  *ChannelV1  `json:"channel,omitempty"`
	Event    *EventV1    `json:"event,omitempty"`
	GitLink  *GitLinkV1  `json:"git_link,omitempty"`
}

type PutRecordV1 struct {
	Record RecordValueV1 `json:"record"`
}

type PutKBArticleV1 struct {
	Record KBArticleV1 `json:"record"`
	Body   string      `json:"body"`
}

type TombstoneOperationV1 struct {
	Key                   RecordKey `json:"key"`
	ExpectedContentDigest Digest    `json:"expected_content_digest"`
	ExpectedBodyDigest    *Digest   `json:"expected_body_digest"`
}

type ResurrectOperationV1 struct {
	Key                     RecordKey     `json:"key"`
	ExpectedTombstoneDigest Digest        `json:"expected_tombstone_digest"`
	Record                  RecordValueV1 `json:"record"`
	KBRecord                *KBArticleV1  `json:"kb_record,omitempty"`
	KBBody                  *string       `json:"kb_body,omitempty"`
}

type OperationV1 struct {
	SchemaVersion      int                   `json:"schema_version"`
	ID                 string                `json:"id"`
	Kind               OperationKind         `json:"kind"`
	ExpectedViewDigest Digest                `json:"expected_view_digest"`
	Actor              types.ActorEnvelope   `json:"actor"`
	PutRecord          *PutRecordV1          `json:"put_record,omitempty"`
	PutKBArticle       *PutKBArticleV1       `json:"put_kb_article,omitempty"`
	Tombstone          *TombstoneOperationV1 `json:"tombstone,omitempty"`
	Resurrect          *ResurrectOperationV1 `json:"resurrect,omitempty"`
}

type OperationFailureKind string

const (
	OperationFailureInvalid       OperationFailureKind = "invalid"
	OperationFailureStateConflict OperationFailureKind = "state_conflict"
)

type OperationFailure struct {
	Kind OperationFailureKind
	Err  error
}

func (e *OperationFailure) Error() string { return e.Err.Error() }

func (e *OperationFailure) Unwrap() error { return e.Err }

func operationFailure(kind OperationFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &OperationFailure{Kind: kind, Err: err}
}

func ClassifyOperationFailure(err error) (OperationFailureKind, bool) {
	var failure *OperationFailure
	if !errors.As(err, &failure) || failure == nil || failure.Err == nil {
		return "", false
	}
	return failure.Kind, true
}

func ValidateOperationForApply(operation OperationV1) error {
	if err := validateOperation(operation); err != nil {
		return operationFailure(OperationFailureInvalid, err)
	}
	if operation.Actor.Assurance == types.AssuranceLegacy || operation.Actor.Assurance == types.AssuranceUnknown {
		return operationFailure(OperationFailureInvalid, fmt.Errorf("%w: historical assurance cannot issue operations", ErrInvalidActorEnvelope))
	}
	return nil
}

func stateDependentOperationFailure(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrOperationPrecondition), errors.Is(err, ErrImmutableRecord),
		errors.Is(err, ErrTombstoneDigest), errors.Is(err, ErrResurrectionDigest),
		errors.Is(err, ErrBrokenReference):
		return operationFailure(OperationFailureStateConflict, err)
	default:
		return err
	}
}

func CanonicalOperation(operation OperationV1) ([]byte, error) {
	if err := validateOperation(operation); err != nil {
		return nil, err
	}
	return CanonicalJSON(operation)
}

func DecodeOperation(raw []byte) (OperationV1, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var operation OperationV1
	if err := decoder.Decode(&operation); err != nil {
		return OperationV1{}, fmt.Errorf("%w: strict operation JSON: %v", ErrOperationPrecondition, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return OperationV1{}, fmt.Errorf("%w: strict operation JSON: %v", ErrOperationPrecondition, err)
	}
	if err := validateOperation(operation); err != nil {
		return OperationV1{}, err
	}
	canonical, err := CanonicalOperation(operation)
	if err != nil {
		return OperationV1{}, err
	}
	if !bytes.Equal(canonical, raw) {
		return OperationV1{}, fmt.Errorf("%w: non-canonical operation JSON", ErrOperationPrecondition)
	}
	return operation, nil
}

func ApplyOperation(snapshot Snapshot, operation OperationV1) (Snapshot, error) {
	if err := ValidateOperationForApply(operation); err != nil {
		return snapshot, err
	}
	if operation.ExpectedViewDigest != snapshot.Digest {
		return snapshot, operationFailure(OperationFailureStateConflict, fmt.Errorf("%w: expected view digest", ErrOperationPrecondition))
	}
	next := cloneSnapshot(snapshot)
	err := applyOperationPayload(&next, operation)
	if err != nil {
		return snapshot, stateDependentOperationFailure(err)
	}
	if err := Validate(next); err != nil {
		return snapshot, stateDependentOperationFailure(err)
	}
	tree, err := EncodeTree(next)
	if err != nil {
		return snapshot, err
	}
	next.Digest, err = DigestTree(tree)
	if err != nil {
		return snapshot, err
	}
	return next, nil
}

func applyOperationPayload(next *Snapshot, operation OperationV1) error {
	switch operation.Kind {
	case OperationPutRecord:
		return applyPutRecord(next, operation.PutRecord.Record)
	case OperationPutKBArticle:
		return applyPutKBArticle(next, *operation.PutKBArticle)
	case OperationTombstone:
		return applyTombstone(next, operation.Actor, *operation.Tombstone)
	case OperationResurrect:
		return applyResurrection(next, *operation.Resurrect)
	default:
		return fmt.Errorf("%w: operation kind %q", ErrUnknownKind, operation.Kind)
	}
}

func validateOperation(operation OperationV1) error {
	if operation.SchemaVersion != 1 {
		return fmt.Errorf("%w: operation schema_version=%d", ErrUnknownVersion, operation.SchemaVersion)
	}
	if !types.CanonicalUUID(operation.ID) {
		return fmt.Errorf("%w: invalid operation ID", ErrOperationPrecondition)
	}
	if !contentDigestPattern.MatchString(string(operation.ExpectedViewDigest)) {
		return fmt.Errorf("%w: malformed expected view digest", ErrOperationPrecondition)
	}
	if err := operation.Actor.Validate(); err != nil {
		return err
	}
	payloads := 0
	if operation.PutRecord != nil {
		payloads++
	}
	if operation.PutKBArticle != nil {
		payloads++
	}
	if operation.Tombstone != nil {
		payloads++
	}
	if operation.Resurrect != nil {
		payloads++
	}
	if payloads != 1 {
		return fmt.Errorf("%w: operation requires exactly one payload", ErrOperationPrecondition)
	}
	switch operation.Kind {
	case OperationPutRecord:
		if operation.PutRecord == nil || recordValueCount(operation.PutRecord.Record) != 1 {
			return fmt.Errorf("%w: invalid put_record payload", ErrOperationPrecondition)
		}
		return validateRecordValue(operation.PutRecord.Record)
	case OperationPutKBArticle:
		if operation.PutKBArticle == nil {
			return fmt.Errorf("%w: invalid put_kb_article payload", ErrOperationPrecondition)
		}
		if err := validateArticle(operation.PutKBArticle.Record); err != nil {
			return err
		}
		if _, err := CanonicalMarkdown([]byte(operation.PutKBArticle.Body)); err != nil {
			return err
		}
	case OperationTombstone:
		if operation.Tombstone == nil {
			return fmt.Errorf("%w: invalid tombstone payload", ErrOperationPrecondition)
		}
		return validateTombstoneOperation(*operation.Tombstone)
	case OperationResurrect:
		if operation.Resurrect == nil {
			return fmt.Errorf("%w: invalid resurrect payload", ErrOperationPrecondition)
		}
		return validateResurrectOperation(*operation.Resurrect)
	default:
		return fmt.Errorf("%w: operation kind %q", ErrUnknownKind, operation.Kind)
	}
	return nil
}

func validateRecordValue(value RecordValueV1) error {
	switch {
	case value.Project != nil:
		return validateProject(*value.Project)
	case value.Actor != nil:
		return validateActor(*value.Actor)
	case value.Task != nil:
		return validateTask(*value.Task)
	case value.TaskLink != nil:
		return validateTaskLink(*value.TaskLink)
	case value.Channel != nil:
		return validateChannel(*value.Channel)
	case value.Event != nil:
		return validateEvent(*value.Event)
	case value.GitLink != nil:
		return validateGitLink(*value.GitLink)
	default:
		return fmt.Errorf("%w: empty record", ErrOperationPrecondition)
	}
}

func validateTombstoneOperation(operation TombstoneOperationV1) error {
	if !types.CanonicalUUID(operation.Key.ID) || !contentDigestPattern.MatchString(string(operation.ExpectedContentDigest)) {
		return fmt.Errorf("%w: malformed tombstone precondition", ErrTombstoneDigest)
	}
	switch operation.Key.Kind {
	case "kb_article":
		if operation.ExpectedBodyDigest == nil || !contentDigestPattern.MatchString(string(*operation.ExpectedBodyDigest)) {
			return fmt.Errorf("%w: kb_article body digest", ErrTombstoneDigest)
		}
	case "actor", "task", "task_link", "channel":
		if operation.ExpectedBodyDigest != nil {
			return fmt.Errorf("%w: body digest on non-KB record", ErrTombstoneDigest)
		}
	case "project", "event", "git_link":
		return fmt.Errorf("%w: %s cannot be tombstoned", ErrUnknownKind, operation.Key.Kind)
	default:
		return fmt.Errorf("%w: tombstone entity %q", ErrUnknownKind, operation.Key.Kind)
	}
	return nil
}

func validateResurrectOperation(operation ResurrectOperationV1) error {
	if !types.CanonicalUUID(operation.Key.ID) || !contentDigestPattern.MatchString(string(operation.ExpectedTombstoneDigest)) {
		return fmt.Errorf("%w: malformed resurrection precondition", ErrResurrectionDigest)
	}
	switch operation.Key.Kind {
	case "kb_article":
		if operation.KBRecord == nil || operation.KBBody == nil || recordValueCount(operation.Record) != 0 || operation.KBRecord.ID != operation.Key.ID {
			return fmt.Errorf("%w: invalid kb_article resurrection", ErrOperationPrecondition)
		}
		if err := validateArticle(*operation.KBRecord); err != nil {
			return err
		}
		if _, err := CanonicalMarkdown([]byte(*operation.KBBody)); err != nil {
			return err
		}
		return nil
	case "actor", "task", "task_link", "channel":
		if operation.KBRecord != nil || operation.KBBody != nil || recordValueCount(operation.Record) != 1 {
			return fmt.Errorf("%w: invalid record resurrection", ErrOperationPrecondition)
		}
		kind, id := recordValueKey(operation.Record)
		if kind != operation.Key.Kind || id != operation.Key.ID {
			return fmt.Errorf("%w: resurrection record does not match key", ErrOperationPrecondition)
		}
		return validateRecordValue(operation.Record)
	case "project", "event", "git_link":
		return fmt.Errorf("%w: %s cannot be resurrected", ErrUnknownKind, operation.Key.Kind)
	default:
		return fmt.Errorf("%w: resurrection entity %q", ErrUnknownKind, operation.Key.Kind)
	}
}

func applyPutRecord(snapshot *Snapshot, value RecordValueV1) error {
	switch {
	case value.Project != nil:
		if value.Project.ID != snapshot.Config.ProjectID || value.Project.ID != snapshot.Project.ID {
			return fmt.Errorf("%w: project ID is immutable", ErrOperationPrecondition)
		}
		if value.Project.CreatedAt != snapshot.Project.CreatedAt {
			return fmt.Errorf("%w: project.created_at is immutable", ErrOperationPrecondition)
		}
		snapshot.Project = cloneProject(*value.Project)
	case value.Actor != nil:
		if existing, ok := snapshot.Actors[value.Actor.ID]; ok && existing.Tombstone != nil {
			return fmt.Errorf("%w: actor requires resurrection", ErrOperationPrecondition)
		}
		record := cloneActor(*value.Actor)
		snapshot.Actors[record.ID] = Record[ActorV1]{Value: &record}
	case value.Task != nil:
		if existing, ok := snapshot.Tasks[value.Task.ID]; ok {
			if existing.Tombstone != nil {
				return fmt.Errorf("%w: task requires resurrection", ErrOperationPrecondition)
			}
			if existing.Value != nil && value.Task.CreatedAt != existing.Value.CreatedAt {
				return fmt.Errorf("%w: task.%s.created_at is immutable", ErrOperationPrecondition, value.Task.ID)
			}
		}
		record := cloneTask(*value.Task)
		snapshot.Tasks[record.ID] = Record[TaskV1]{Value: &record}
	case value.TaskLink != nil:
		if existing, ok := snapshot.TaskLinks[value.TaskLink.ID]; ok && existing.Tombstone != nil {
			return fmt.Errorf("%w: task_link requires resurrection", ErrOperationPrecondition)
		}
		record := cloneTaskLink(*value.TaskLink)
		snapshot.TaskLinks[record.ID] = Record[TaskLinkV1]{Value: &record}
	case value.Channel != nil:
		if existing, ok := snapshot.Channels[value.Channel.ID]; ok {
			if existing.Tombstone != nil {
				return fmt.Errorf("%w: channel requires resurrection", ErrOperationPrecondition)
			}
			if existing.Value != nil && value.Channel.CreatedAt != existing.Value.CreatedAt {
				return fmt.Errorf("%w: channel.%s.created_at is immutable", ErrOperationPrecondition, value.Channel.ID)
			}
		}
		record := cloneChannel(*value.Channel)
		snapshot.Channels[record.ID] = Record[ChannelV1]{Value: &record}
	case value.Event != nil:
		if existing, ok := snapshot.Events[value.Event.ID]; ok {
			existingJSON, existingErr := CanonicalJSON(existing)
			incomingJSON, incomingErr := CanonicalJSON(*value.Event)
			if existingErr != nil {
				return existingErr
			}
			if incomingErr != nil {
				return incomingErr
			}
			if !bytes.Equal(existingJSON, incomingJSON) {
				return ErrImmutableRecord
			}
			return nil
		}
		snapshot.Events[value.Event.ID] = cloneEvent(*value.Event)
	case value.GitLink != nil:
		if existing, ok := snapshot.GitLinks[value.GitLink.ID]; ok {
			if existing.Value == nil {
				return ErrImmutableRecord
			}
			existingJSON, existingErr := CanonicalJSON(*existing.Value)
			incomingJSON, incomingErr := CanonicalJSON(*value.GitLink)
			if existingErr != nil {
				return existingErr
			}
			if incomingErr != nil {
				return incomingErr
			}
			if !bytes.Equal(existingJSON, incomingJSON) {
				return ErrImmutableRecord
			}
			return nil
		}
		record := cloneGitLink(*value.GitLink)
		snapshot.GitLinks[record.ID] = Record[GitLinkV1]{Value: &record}
	default:
		return fmt.Errorf("%w: empty record", ErrOperationPrecondition)
	}
	return nil
}

func applyPutKBArticle(snapshot *Snapshot, put PutKBArticleV1) error {
	if existing, ok := snapshot.Articles[put.Record.ID]; ok {
		if existing.Tombstone != nil {
			return fmt.Errorf("%w: kb_article requires resurrection", ErrOperationPrecondition)
		}
		if existing.Value != nil && put.Record.CreatedAt != existing.Value.CreatedAt {
			return fmt.Errorf("%w: kb_article.%s.created_at is immutable", ErrOperationPrecondition, put.Record.ID)
		}
	}
	body, err := CanonicalMarkdown([]byte(put.Body))
	if err != nil {
		return err
	}
	record := cloneArticle(put.Record)
	snapshot.Articles[record.ID] = KBRecord{Value: &record, Body: body}
	return nil
}

func applyTombstone(snapshot *Snapshot, actor types.ActorEnvelope, operation TombstoneOperationV1) error {
	if !types.CanonicalUUID(operation.Key.ID) || !contentDigestPattern.MatchString(string(operation.ExpectedContentDigest)) {
		return fmt.Errorf("%w: malformed tombstone precondition", ErrTombstoneDigest)
	}
	var value any
	var body []byte
	switch operation.Key.Kind {
	case "actor":
		record, ok := snapshot.Actors[operation.Key.ID]
		if !ok || record.Value == nil {
			return fmt.Errorf("%w: actor is not live", ErrOperationPrecondition)
		}
		value = *record.Value
	case "task":
		record, ok := snapshot.Tasks[operation.Key.ID]
		if !ok || record.Value == nil {
			return fmt.Errorf("%w: task is not live", ErrOperationPrecondition)
		}
		value = *record.Value
	case "task_link":
		record, ok := snapshot.TaskLinks[operation.Key.ID]
		if !ok || record.Value == nil {
			return fmt.Errorf("%w: task_link is not live", ErrOperationPrecondition)
		}
		value = *record.Value
	case "kb_article":
		record, ok := snapshot.Articles[operation.Key.ID]
		if !ok || record.Value == nil {
			return fmt.Errorf("%w: kb_article is not live", ErrOperationPrecondition)
		}
		value, body = *record.Value, record.Body
	case "channel":
		record, ok := snapshot.Channels[operation.Key.ID]
		if !ok || record.Value == nil {
			return fmt.Errorf("%w: channel is not live", ErrOperationPrecondition)
		}
		value = *record.Value
	case "project", "event", "git_link":
		return fmt.Errorf("%w: %s cannot be tombstoned", ErrUnknownKind, operation.Key.Kind)
	default:
		return fmt.Errorf("%w: tombstone entity %q", ErrUnknownKind, operation.Key.Kind)
	}
	contentDigest, err := DigestCanonicalJSON(value)
	if err != nil {
		return err
	}
	if contentDigest != operation.ExpectedContentDigest {
		return fmt.Errorf("%w: content digest got %s", ErrTombstoneDigest, contentDigest)
	}
	var bodyDigest *Digest
	if operation.Key.Kind == "kb_article" {
		digest, markdownErr := DigestCanonicalMarkdown(body)
		if markdownErr != nil {
			return markdownErr
		}
		if operation.ExpectedBodyDigest == nil || *operation.ExpectedBodyDigest != digest {
			return fmt.Errorf("%w: body digest got %s", ErrTombstoneDigest, digest)
		}
		bodyDigest = &digest
	} else if operation.ExpectedBodyDigest != nil {
		return ErrTombstoneDigest
	}
	tombstone := &TombstoneV1{
		SchemaVersion: 1, Kind: "tombstone", ID: operation.Key.ID, EntityKind: operation.Key.Kind,
		DeletedContentDigest: contentDigest, DeletedBodyDigest: bodyDigest, DeletedBy: actor,
		DeletedAt: actor.OccurredAt, Extensions: ExtensionsV1{},
	}
	switch operation.Key.Kind {
	case "actor":
		snapshot.Actors[operation.Key.ID] = Record[ActorV1]{Tombstone: tombstone}
	case "task":
		snapshot.Tasks[operation.Key.ID] = Record[TaskV1]{Tombstone: tombstone}
	case "task_link":
		snapshot.TaskLinks[operation.Key.ID] = Record[TaskLinkV1]{Tombstone: tombstone}
	case "kb_article":
		snapshot.Articles[operation.Key.ID] = KBRecord{Tombstone: tombstone}
	case "channel":
		snapshot.Channels[operation.Key.ID] = Record[ChannelV1]{Tombstone: tombstone}
	}
	return nil
}

func applyResurrection(snapshot *Snapshot, operation ResurrectOperationV1) error {
	if !types.CanonicalUUID(operation.Key.ID) || !contentDigestPattern.MatchString(string(operation.ExpectedTombstoneDigest)) {
		return ErrResurrectionDigest
	}
	tombstone := findTombstone(*snapshot, operation.Key)
	if tombstone == nil {
		return fmt.Errorf("%w: no matching tombstone", ErrOperationPrecondition)
	}
	digest, err := DigestCanonicalJSON(*tombstone)
	if err != nil {
		return err
	}
	if digest != operation.ExpectedTombstoneDigest {
		return ErrResurrectionDigest
	}
	if operation.Key.Kind == "kb_article" {
		if operation.KBRecord == nil || operation.KBBody == nil || recordValueCount(operation.Record) != 0 || operation.KBRecord.ID != operation.Key.ID {
			return fmt.Errorf("%w: invalid kb_article resurrection", ErrOperationPrecondition)
		}
		body, markdownErr := CanonicalMarkdown([]byte(*operation.KBBody))
		if markdownErr != nil {
			return markdownErr
		}
		record := cloneArticle(*operation.KBRecord)
		snapshot.Articles[operation.Key.ID] = KBRecord{Value: &record, Body: body}
		return nil
	}
	if operation.KBRecord != nil || operation.KBBody != nil || recordValueCount(operation.Record) != 1 {
		return fmt.Errorf("%w: invalid record resurrection", ErrOperationPrecondition)
	}
	kind, id := recordValueKey(operation.Record)
	if kind != operation.Key.Kind || id != operation.Key.ID {
		return fmt.Errorf("%w: resurrection record does not match key", ErrOperationPrecondition)
	}
	clearTombstone(snapshot, operation.Key)
	return applyPutRecord(snapshot, operation.Record)
}

func clearTombstone(snapshot *Snapshot, key RecordKey) {
	switch key.Kind {
	case "actor":
		delete(snapshot.Actors, key.ID)
	case "task":
		delete(snapshot.Tasks, key.ID)
	case "task_link":
		delete(snapshot.TaskLinks, key.ID)
	case "channel":
		delete(snapshot.Channels, key.ID)
	}
}

func findTombstone(snapshot Snapshot, key RecordKey) *TombstoneV1 {
	switch key.Kind {
	case "actor":
		return snapshot.Actors[key.ID].Tombstone
	case "task":
		return snapshot.Tasks[key.ID].Tombstone
	case "task_link":
		return snapshot.TaskLinks[key.ID].Tombstone
	case "kb_article":
		return snapshot.Articles[key.ID].Tombstone
	case "channel":
		return snapshot.Channels[key.ID].Tombstone
	default:
		return nil
	}
}

func recordValueCount(value RecordValueV1) int {
	count := 0
	for _, present := range []bool{value.Project != nil, value.Actor != nil, value.Task != nil, value.TaskLink != nil, value.Channel != nil, value.Event != nil, value.GitLink != nil} {
		if present {
			count++
		}
	}
	return count
}

func recordValueKey(value RecordValueV1) (string, string) {
	switch {
	case value.Project != nil:
		return "project", value.Project.ID
	case value.Actor != nil:
		return "actor", value.Actor.ID
	case value.Task != nil:
		return "task", value.Task.ID
	case value.TaskLink != nil:
		return "task_link", value.TaskLink.ID
	case value.Channel != nil:
		return "channel", value.Channel.ID
	case value.Event != nil:
		return "event", value.Event.ID
	case value.GitLink != nil:
		return "git_link", value.GitLink.ID
	default:
		return "", ""
	}
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Project = cloneProject(snapshot.Project)
	if snapshot.Remotes != nil {
		remotes := *snapshot.Remotes
		if snapshot.Remotes.Fabrics != nil {
			remotes.Fabrics = append(make([]FabricHintV1, 0, len(snapshot.Remotes.Fabrics)), snapshot.Remotes.Fabrics...)
		}
		clone.Remotes = &remotes
	}
	clone.Actors = make(map[string]Record[ActorV1], len(snapshot.Actors))
	for id, record := range snapshot.Actors {
		clone.Actors[id] = cloneActorRecord(record)
	}
	clone.Tasks = make(map[string]Record[TaskV1], len(snapshot.Tasks))
	for id, record := range snapshot.Tasks {
		clone.Tasks[id] = cloneTaskRecord(record)
	}
	clone.TaskLinks = make(map[string]Record[TaskLinkV1], len(snapshot.TaskLinks))
	for id, record := range snapshot.TaskLinks {
		clone.TaskLinks[id] = cloneTaskLinkRecord(record)
	}
	clone.Articles = make(map[string]KBRecord, len(snapshot.Articles))
	for id, record := range snapshot.Articles {
		copyRecord := KBRecord{Body: bytes.Clone(record.Body)}
		if record.Value != nil {
			value := cloneArticle(*record.Value)
			copyRecord.Value = &value
		}
		if record.Tombstone != nil {
			copyRecord.Tombstone = cloneTombstone(record.Tombstone)
		}
		clone.Articles[id] = copyRecord
	}
	clone.Channels = make(map[string]Record[ChannelV1], len(snapshot.Channels))
	for id, record := range snapshot.Channels {
		clone.Channels[id] = cloneChannelRecord(record)
	}
	clone.Events = make(map[string]EventV1, len(snapshot.Events))
	for id, event := range snapshot.Events {
		clone.Events[id] = cloneEvent(event)
	}
	clone.GitLinks = make(map[string]Record[GitLinkV1], len(snapshot.GitLinks))
	for id, record := range snapshot.GitLinks {
		clone.GitLinks[id] = cloneGitLinkRecord(record)
	}
	return clone
}

func cloneProject(value ProjectV1) ProjectV1 {
	value.Aliases = cloneStrings(value.Aliases)
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneActor(value ActorV1) ActorV1 {
	if value.PublicKeys != nil {
		value.PublicKeys = append(make([]PublicKeyV1, 0, len(value.PublicKeys)), value.PublicKeys...)
	}
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneTask(value TaskV1) TaskV1 {
	value.ParentTaskID = cloneStringPointer(value.ParentTaskID)
	value.OwnerActorID = cloneStringPointer(value.OwnerActorID)
	if value.DueBy != nil {
		due := *value.DueBy
		value.DueBy = &due
	}
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneTaskLink(value TaskLinkV1) TaskLinkV1 {
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneArticle(value KBArticleV1) KBArticleV1 {
	frontmatter := value.Frontmatter
	value.Frontmatter = make(map[string]json.RawMessage, len(value.Frontmatter))
	for key, raw := range frontmatter {
		value.Frontmatter[key] = bytes.Clone(raw)
	}
	value.RelatedArticleIDs = cloneStrings(value.RelatedArticleIDs)
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneChannel(value ChannelV1) ChannelV1 {
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneEvent(value EventV1) EventV1 {
	value.Payload = bytes.Clone(value.Payload)
	value.Note = cloneStringPointer(value.Note)
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneGitLink(value GitLinkV1) GitLinkV1 {
	value.TaskID = cloneStringPointer(value.TaskID)
	value.CommitSHA = cloneStringPointer(value.CommitSHA)
	value.PRURL = cloneStringPointer(value.PRURL)
	value.Extensions = cloneExtensions(value.Extensions)
	return value
}

func cloneExtensions(extensions ExtensionsV1) ExtensionsV1 {
	if extensions == nil {
		return nil
	}
	clone := make(ExtensionsV1, len(extensions))
	for key, extension := range extensions {
		extension.Data = bytes.Clone(extension.Data)
		clone[key] = extension
	}
	return clone
}

func cloneTombstone(value *TombstoneV1) *TombstoneV1 {
	if value == nil {
		return nil
	}
	clone := *value
	if value.DeletedBodyDigest != nil {
		digest := *value.DeletedBodyDigest
		clone.DeletedBodyDigest = &digest
	}
	clone.Extensions = cloneExtensions(value.Extensions)
	return &clone
}

func cloneActorRecord(record Record[ActorV1]) Record[ActorV1] {
	if record.Value != nil {
		value := cloneActor(*record.Value)
		return Record[ActorV1]{Value: &value}
	}
	return Record[ActorV1]{Tombstone: cloneTombstone(record.Tombstone)}
}

func cloneTaskRecord(record Record[TaskV1]) Record[TaskV1] {
	if record.Value != nil {
		value := cloneTask(*record.Value)
		return Record[TaskV1]{Value: &value}
	}
	return Record[TaskV1]{Tombstone: cloneTombstone(record.Tombstone)}
}

func cloneTaskLinkRecord(record Record[TaskLinkV1]) Record[TaskLinkV1] {
	if record.Value != nil {
		value := cloneTaskLink(*record.Value)
		return Record[TaskLinkV1]{Value: &value}
	}
	return Record[TaskLinkV1]{Tombstone: cloneTombstone(record.Tombstone)}
}

func cloneChannelRecord(record Record[ChannelV1]) Record[ChannelV1] {
	if record.Value != nil {
		value := cloneChannel(*record.Value)
		return Record[ChannelV1]{Value: &value}
	}
	return Record[ChannelV1]{Tombstone: cloneTombstone(record.Tombstone)}
}

func cloneGitLinkRecord(record Record[GitLinkV1]) Record[GitLinkV1] {
	if record.Value != nil {
		value := cloneGitLink(*record.Value)
		return Record[GitLinkV1]{Value: &value}
	}
	return Record[GitLinkV1]{Tombstone: cloneTombstone(record.Tombstone)}
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}
