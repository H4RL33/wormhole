package projectstate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/H4RL33/wormhole/internal/types"
)

var (
	extensionKeyPattern  = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)+$`)
	fabricAliasPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	contentDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	objectIDPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type referenceTarget struct {
	kind      string
	tombstone bool
}

func Validate(snapshot Snapshot) error {
	if snapshot.Config.SnapshotVersion != 1 {
		return fmt.Errorf("%w: config snapshot_version=%d", ErrUnknownVersion, snapshot.Config.SnapshotVersion)
	}
	if !types.CanonicalUUID(snapshot.Config.ProjectID) || snapshot.Project.ID != snapshot.Config.ProjectID {
		return fmt.Errorf("%w: project ID does not match config", ErrInvalidSnapshot)
	}
	if err := snapshot.Config.Handle.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	if err := snapshot.Config.Repository.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	if snapshot.Remotes != nil {
		if err := validateRemotes(*snapshot.Remotes); err != nil {
			return err
		}
	}
	if snapshot.Actors == nil || snapshot.Tasks == nil || snapshot.TaskLinks == nil || snapshot.Articles == nil || snapshot.Channels == nil || snapshot.Events == nil || snapshot.GitLinks == nil {
		return fmt.Errorf("%w: record maps must be initialized", ErrInvalidSnapshot)
	}
	if err := validateProject(snapshot.Project); err != nil {
		return err
	}
	targets := map[string]referenceTarget{snapshot.Project.ID: {kind: "project"}}
	if err := validateRecordMap(snapshot.Actors, "actor", targets, validateActor); err != nil {
		return err
	}
	if err := validateRecordMap(snapshot.Tasks, "task", targets, validateTask); err != nil {
		return err
	}
	if err := validateRecordMap(snapshot.TaskLinks, "task_link", targets, validateTaskLink); err != nil {
		return err
	}
	for id, record := range snapshot.Articles {
		if !types.CanonicalUUID(id) || (record.Value == nil) == (record.Tombstone == nil) {
			return fmt.Errorf("%w: invalid kb_article record %q", ErrInvalidSnapshot, id)
		}
		if _, duplicate := targets[id]; duplicate {
			return fmt.Errorf("%w: duplicate semantic ID %s", ErrInvalidSnapshot, id)
		}
		if record.Value != nil {
			if record.Value.ID != id {
				return fmt.Errorf("%w: kb_article map key and ID differ", ErrInvalidSnapshot)
			}
			if err := validateArticle(*record.Value); err != nil {
				return err
			}
			canonical, err := CanonicalMarkdown(record.Body)
			if err != nil || len(record.Body) == 0 || string(canonical) != string(record.Body) {
				return fmt.Errorf("%w: live kb_article %s requires canonical body", ErrInvalidSnapshot, id)
			}
			targets[id] = referenceTarget{kind: "kb_article"}
		} else {
			if record.Tombstone.ID != id || record.Tombstone.EntityKind != "kb_article" || record.Tombstone.DeletedBodyDigest == nil || record.Body != nil {
				return fmt.Errorf("%w: invalid kb_article tombstone %s", ErrInvalidSnapshot, id)
			}
			if err := validateTombstone(*record.Tombstone); err != nil {
				return err
			}
			targets[id] = referenceTarget{kind: "kb_article", tombstone: true}
		}
	}
	if err := validateRecordMap(snapshot.Channels, "channel", targets, validateChannel); err != nil {
		return err
	}
	for id, event := range snapshot.Events {
		if id != event.ID || !types.CanonicalUUID(id) {
			return fmt.Errorf("%w: event map key and ID differ", ErrInvalidSnapshot)
		}
		if _, duplicate := targets[id]; duplicate {
			return fmt.Errorf("%w: duplicate semantic ID %s", ErrInvalidSnapshot, id)
		}
		if err := validateEvent(event); err != nil {
			return err
		}
		targets[id] = referenceTarget{kind: "event"}
	}
	for id, record := range snapshot.GitLinks {
		if !types.CanonicalUUID(id) || record.Value == nil || record.Tombstone != nil {
			return fmt.Errorf("%w: invalid live git_link record %q", ErrInvalidSnapshot, id)
		}
		if _, duplicate := targets[id]; duplicate {
			return fmt.Errorf("%w: duplicate semantic ID %s", ErrInvalidSnapshot, id)
		}
		if record.Value.ID != id {
			return fmt.Errorf("%w: git_link map key and ID differ", ErrInvalidSnapshot)
		}
		if err := validateGitLink(*record.Value); err != nil {
			return err
		}
		targets[id] = referenceTarget{kind: "git_link"}
	}
	return validateReferences(snapshot, targets)
}

func validateRecordMap[T any](records map[string]Record[T], kind string, targets map[string]referenceTarget, validateValue func(T) error) error {
	for id, record := range records {
		if !types.CanonicalUUID(id) || (record.Value == nil) == (record.Tombstone == nil) {
			return fmt.Errorf("%w: invalid %s record %q", ErrInvalidSnapshot, kind, id)
		}
		if _, duplicate := targets[id]; duplicate {
			return fmt.Errorf("%w: duplicate semantic ID %s", ErrInvalidSnapshot, id)
		}
		if record.Value != nil {
			if recordID(*record.Value) != id {
				return fmt.Errorf("%w: %s map key and ID differ", ErrInvalidSnapshot, kind)
			}
			if err := validateValue(*record.Value); err != nil {
				return err
			}
			targets[id] = referenceTarget{kind: kind}
			continue
		}
		if record.Tombstone.ID != id || record.Tombstone.EntityKind != kind || record.Tombstone.DeletedBodyDigest != nil {
			return fmt.Errorf("%w: invalid %s tombstone %s", ErrInvalidSnapshot, kind, id)
		}
		if err := validateTombstone(*record.Tombstone); err != nil {
			return err
		}
		targets[id] = referenceTarget{kind: kind, tombstone: true}
	}
	return nil
}

func recordID[T any](value T) string {
	switch typed := any(value).(type) {
	case ActorV1:
		return typed.ID
	case TaskV1:
		return typed.ID
	case TaskLinkV1:
		return typed.ID
	case ChannelV1:
		return typed.ID
	case GitLinkV1:
		return typed.ID
	default:
		return ""
	}
}

func validateProject(project ProjectV1) error {
	if err := validateHeader(project.SchemaVersion, project.Kind, "project", project.ID); err != nil {
		return err
	}
	if !requiredText(project.Name) || project.Aliases == nil || !validTimes(project.CreatedAt, project.UpdatedAt) {
		return fmt.Errorf("%w: invalid project fields", ErrInvalidSnapshot)
	}
	seen := make(map[string]struct{}, len(project.Aliases))
	for _, alias := range project.Aliases {
		if !requiredText(alias) {
			return fmt.Errorf("%w: invalid project alias", ErrInvalidSnapshot)
		}
		if _, duplicate := seen[alias]; duplicate {
			return fmt.Errorf("%w: duplicate project alias", ErrInvalidSnapshot)
		}
		seen[alias] = struct{}{}
	}
	return validateExtensions(project.Extensions)
}

func validateActor(actor ActorV1) error {
	if err := validateHeader(actor.SchemaVersion, actor.Kind, "actor", actor.ID); err != nil {
		return err
	}
	if (actor.ActorKind != types.ActorHuman && actor.ActorKind != types.ActorAgent) || !requiredText(actor.DisplayName) || actor.PublicKeys == nil {
		return fmt.Errorf("%w: invalid actor fields", ErrInvalidSnapshot)
	}
	seen := make(map[string]struct{}, len(actor.PublicKeys))
	for _, key := range actor.PublicKeys {
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKeyBase64)
		if !requiredText(key.KeyID) || key.Algorithm != "ed25519" || err != nil || len(decoded) != 32 {
			return fmt.Errorf("%w: invalid actor public key", ErrInvalidSnapshot)
		}
		if _, duplicate := seen[key.KeyID]; duplicate {
			return fmt.Errorf("%w: duplicate actor key ID", ErrInvalidSnapshot)
		}
		seen[key.KeyID] = struct{}{}
	}
	return validateExtensions(actor.Extensions)
}

func validateTask(task TaskV1) error {
	if err := validateHeader(task.SchemaVersion, task.Kind, "task", task.ID); err != nil {
		return err
	}
	if !requiredText(task.Title) || !validTimes(task.CreatedAt, task.UpdatedAt) {
		return fmt.Errorf("%w: invalid task fields", ErrInvalidSnapshot)
	}
	switch task.Status {
	case "todo", "wip", "blocked", "done":
	default:
		return fmt.Errorf("%w: invalid task status", ErrInvalidSnapshot)
	}
	if task.ParentTaskID != nil && !types.CanonicalUUID(*task.ParentTaskID) {
		return fmt.Errorf("%w: invalid parent task ID", ErrInvalidSnapshot)
	}
	if task.OwnerActorID != nil && !types.CanonicalUUID(*task.OwnerActorID) {
		return fmt.Errorf("%w: invalid owner actor ID", ErrInvalidSnapshot)
	}
	if task.DueBy != nil && !validUTC(*task.DueBy) {
		return fmt.Errorf("%w: invalid task due_by", ErrInvalidSnapshot)
	}
	return validateExtensions(task.Extensions)
}

func validateTaskLink(link TaskLinkV1) error {
	if err := validateHeader(link.SchemaVersion, link.Kind, "task_link", link.ID); err != nil {
		return err
	}
	if !types.CanonicalUUID(link.TaskID) || !types.CanonicalUUID(link.TargetID) {
		return fmt.Errorf("%w: invalid task link IDs", ErrInvalidSnapshot)
	}
	switch link.LinkType {
	case "kb_article", "task", "event", "git_link":
	default:
		return fmt.Errorf("%w: invalid task link type", ErrInvalidSnapshot)
	}
	return validateExtensions(link.Extensions)
}

func validateArticle(article KBArticleV1) error {
	if err := validateHeader(article.SchemaVersion, article.Kind, "kb_article", article.ID); err != nil {
		return err
	}
	if !requiredText(article.Title) || article.Frontmatter == nil || article.RelatedArticleIDs == nil || !types.CanonicalUUID(article.AuthorActorID) || !validTimes(article.CreatedAt, article.UpdatedAt) {
		return fmt.Errorf("%w: invalid kb_article fields", ErrInvalidSnapshot)
	}
	for key, value := range article.Frontmatter {
		if secretKey(key) {
			return ErrTrackedSecret
		}
		if !requiredText(key) {
			return fmt.Errorf("%w: invalid kb_article frontmatter", ErrInvalidSnapshot)
		}
		if err := validateRawJSON(value, false); err != nil {
			return err
		}
	}
	seen := make(map[string]struct{}, len(article.RelatedArticleIDs))
	for _, id := range article.RelatedArticleIDs {
		if !types.CanonicalUUID(id) {
			return fmt.Errorf("%w: invalid related article ID", ErrInvalidSnapshot)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate related article ID", ErrInvalidSnapshot)
		}
		seen[id] = struct{}{}
	}
	return validateExtensions(article.Extensions)
}

func validateChannel(channel ChannelV1) error {
	if err := validateHeader(channel.SchemaVersion, channel.Kind, "channel", channel.ID); err != nil {
		return err
	}
	if !requiredText(channel.Name) || !validUTC(channel.CreatedAt) {
		return fmt.Errorf("%w: invalid channel fields", ErrInvalidSnapshot)
	}
	return validateExtensions(channel.Extensions)
}

func validateEvent(event EventV1) error {
	if err := validateHeader(event.SchemaVersion, event.Kind, "event", event.ID); err != nil {
		return err
	}
	if !types.CanonicalUUID(event.ChannelID) || !types.CanonicalUUID(event.ActorID) || !requiredText(event.EventType) || !validUTC(event.CreatedAt) {
		return fmt.Errorf("%w: invalid event fields", ErrInvalidSnapshot)
	}
	if err := validateRawJSON(event.Payload, false); err != nil {
		return err
	}
	return validateExtensions(event.Extensions)
}

func validateGitLink(link GitLinkV1) error {
	if err := validateHeader(link.SchemaVersion, link.Kind, "git_link", link.ID); err != nil {
		return err
	}
	if !requiredText(link.Repository) || !types.CanonicalUUID(link.ActorID) || !validUTC(link.CreatedAt) {
		return fmt.Errorf("%w: invalid git_link fields", ErrInvalidSnapshot)
	}
	if link.TaskID != nil && !types.CanonicalUUID(*link.TaskID) {
		return fmt.Errorf("%w: invalid git_link task ID", ErrInvalidSnapshot)
	}
	if link.CommitSHA != nil && !objectIDPattern.MatchString(*link.CommitSHA) {
		return fmt.Errorf("%w: invalid git_link commit", ErrInvalidSnapshot)
	}
	if link.PRURL != nil {
		parsed, err := url.Parse(*link.PRURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("%w: invalid git_link PR URL", ErrInvalidSnapshot)
		}
	}
	return validateExtensions(link.Extensions)
}

func validateTombstone(tombstone TombstoneV1) error {
	if err := validateHeader(tombstone.SchemaVersion, tombstone.Kind, "tombstone", tombstone.ID); err != nil {
		return err
	}
	switch tombstone.EntityKind {
	case "actor", "task", "task_link", "kb_article", "channel":
	default:
		return fmt.Errorf("%w: invalid tombstone entity kind", ErrInvalidSnapshot)
	}
	if !contentDigestPattern.MatchString(string(tombstone.DeletedContentDigest)) || (tombstone.DeletedBodyDigest != nil && !contentDigestPattern.MatchString(string(*tombstone.DeletedBodyDigest))) || !validUTC(tombstone.DeletedAt) {
		return fmt.Errorf("%w: invalid tombstone fields", ErrInvalidSnapshot)
	}
	if err := tombstone.DeletedBy.ValidateHistorical(); err != nil {
		return fmt.Errorf("%w: tombstone actor: %v", ErrInvalidSnapshot, err)
	}
	return validateExtensions(tombstone.Extensions)
}

func validateHeader(version int, kind, expectedKind, id string) error {
	if version != 1 {
		return fmt.Errorf("%w: %s schema_version=%d", ErrUnknownVersion, expectedKind, version)
	}
	if kind != expectedKind {
		return fmt.Errorf("%w: got %q, want %q", ErrUnknownKind, kind, expectedKind)
	}
	if !types.CanonicalUUID(id) {
		return fmt.Errorf("%w: invalid %s ID", ErrInvalidSnapshot, expectedKind)
	}
	return nil
}

func validateExtensions(extensions ExtensionsV1) error {
	if extensions == nil {
		return fmt.Errorf("%w: extensions must be an object", ErrInvalidSnapshot)
	}
	for key, extension := range extensions {
		if !extensionKeyPattern.MatchString(key) || extension.SchemaVersion != 1 {
			return fmt.Errorf("%w: invalid extension %q", ErrInvalidSnapshot, key)
		}
		if err := validateRawJSON(extension.Data, true); err != nil {
			return err
		}
	}
	return nil
}

func validateRawJSON(data json.RawMessage, requireObject bool) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: invalid dynamic JSON", ErrInvalidSnapshot)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: invalid dynamic JSON", ErrInvalidSnapshot)
	}
	if requireObject {
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("%w: extension data must be an object", ErrInvalidSnapshot)
		}
	}
	if hasSecretKey(value) {
		return ErrTrackedSecret
	}
	return nil
}

func validateRemotes(remotes RemotesV1) error {
	if remotes.Version != 1 {
		return fmt.Errorf("%w: remotes version=%d", ErrUnknownVersion, remotes.Version)
	}
	if remotes.Fabrics == nil {
		return fmt.Errorf("%w: fabric list must be an array", ErrInvalidSnapshot)
	}
	seen := make(map[string]struct{}, len(remotes.Fabrics))
	for _, fabric := range remotes.Fabrics {
		if !fabricAliasPattern.MatchString(fabric.Alias) || !validateFabricURL(fabric.URL) || !safeTOMLText(fabric.InstanceID) || !safeTOMLText(fabric.RemoteProjectID) || (fabric.Mode != "public" && fabric.Mode != "private") {
			return fmt.Errorf("%w: invalid fabric hint %q", ErrInvalidSnapshot, fabric.Alias)
		}
		if _, duplicate := seen[fabric.Alias]; duplicate {
			return fmt.Errorf("%w: duplicate fabric alias %q", ErrInvalidSnapshot, fabric.Alias)
		}
		seen[fabric.Alias] = struct{}{}
		if err := fabric.ExpectedRepository.Validate(); err != nil {
			return fmt.Errorf("%w: invalid expected repository: %v", ErrInvalidSnapshot, err)
		}
	}
	return nil
}

func validateReferences(snapshot Snapshot, targets map[string]referenceTarget) error {
	for _, record := range snapshot.Tasks {
		if record.Value == nil {
			continue
		}
		if record.Value.ParentTaskID != nil {
			if err := requireReference(targets, *record.Value.ParentTaskID, "task", true); err != nil {
				return err
			}
		}
		if record.Value.OwnerActorID != nil {
			if err := requireReference(targets, *record.Value.OwnerActorID, "actor", true); err != nil {
				return err
			}
		}
	}
	for _, record := range snapshot.TaskLinks {
		if record.Value == nil {
			continue
		}
		if err := requireReference(targets, record.Value.TaskID, "task", false); err != nil {
			return err
		}
		if err := requireReference(targets, record.Value.TargetID, record.Value.LinkType, false); err != nil {
			return err
		}
	}
	for _, record := range snapshot.Articles {
		if record.Value == nil {
			continue
		}
		if err := requireReference(targets, record.Value.AuthorActorID, "actor", false); err != nil {
			return err
		}
		for _, related := range record.Value.RelatedArticleIDs {
			if err := requireReference(targets, related, "kb_article", false); err != nil {
				return err
			}
		}
	}
	for _, event := range snapshot.Events {
		if err := requireReference(targets, event.ChannelID, "channel", false); err != nil {
			return err
		}
		if err := requireReference(targets, event.ActorID, "actor", false); err != nil {
			return err
		}
	}
	for _, record := range snapshot.GitLinks {
		if record.Value == nil {
			continue
		}
		if record.Value.TaskID != nil {
			if err := requireReference(targets, *record.Value.TaskID, "task", false); err != nil {
				return err
			}
		}
		if err := requireReference(targets, record.Value.ActorID, "actor", false); err != nil {
			return err
		}
	}
	for _, tombstone := range allTombstones(snapshot) {
		if err := requireReference(targets, tombstone.DeletedBy.PrincipalID(), "actor", false); err != nil {
			return err
		}
	}
	return nil
}

func requireReference(targets map[string]referenceTarget, id, kind string, live bool) error {
	target, exists := targets[id]
	if !exists || target.kind != kind || (live && target.tombstone) {
		return fmt.Errorf("%w: %s %s", ErrBrokenReference, kind, id)
	}
	return nil
}

func allTombstones(snapshot Snapshot) []*TombstoneV1 {
	var tombstones []*TombstoneV1
	for _, record := range snapshot.Actors {
		if record.Tombstone != nil {
			tombstones = append(tombstones, record.Tombstone)
		}
	}
	for _, record := range snapshot.Tasks {
		if record.Tombstone != nil {
			tombstones = append(tombstones, record.Tombstone)
		}
	}
	for _, record := range snapshot.TaskLinks {
		if record.Tombstone != nil {
			tombstones = append(tombstones, record.Tombstone)
		}
	}
	for _, record := range snapshot.Articles {
		if record.Tombstone != nil {
			tombstones = append(tombstones, record.Tombstone)
		}
	}
	for _, record := range snapshot.Channels {
		if record.Tombstone != nil {
			tombstones = append(tombstones, record.Tombstone)
		}
	}
	return tombstones
}

func requiredText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsRune(value, 0)
}

func safeTOMLText(value string) bool {
	if !requiredText(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validTimes(created, updated time.Time) bool {
	return validUTC(created) && validUTC(updated) && !updated.Before(created)
}

func validUTC(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}
