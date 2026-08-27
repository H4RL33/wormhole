package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/H4RL33/wormhole/internal/runtime/projectstate"
	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
	"github.com/google/uuid"
)

var (
	ErrBindingAwareProviderUnavailable = errors.New("localapi: binding-aware provider unavailable")
	ErrPortableActorMissing            = errors.New("localapi: portable actor is missing from composed project state")
	ErrPortableChannelNotFound         = errors.New("localapi: portable channel not found")
	ErrPortableArticleNotFound         = errors.New("localapi: portable KB article not found")
	ErrWorkspaceDomainUnavailable      = errors.New("localapi: workspace domain unavailable")
)

type WorkspaceChannel struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

type WorkspaceArticle struct {
	ID                string
	Title             string
	Body              string
	Frontmatter       json.RawMessage
	AuthorActorID     string
	RelatedArticleIDs []string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// WorkspaceDomain is the binding-aware projection and mutation adapter for
// portable project state. It has no legacy repository or Fabric fallback.
type WorkspaceDomain struct {
	service *projectstate.Service
	now     func() time.Time
	newID   func() (string, error)
}

func NewWorkspaceDomain(service *projectstate.Service) (*WorkspaceDomain, error) {
	domain := newWorkspaceDomain(service, time.Now, func() (string, error) { return uuid.NewString(), nil })
	if domain == nil {
		return nil, ErrWorkspaceDomainUnavailable
	}
	return domain, nil
}

func newWorkspaceDomain(service *projectstate.Service, now func() time.Time, newID func() (string, error)) *WorkspaceDomain {
	if service == nil || now == nil || newID == nil {
		return nil
	}
	return &WorkspaceDomain{service: service, now: now, newID: newID}
}

func (d *WorkspaceDomain) View(ctx context.Context, binding types.WorkspaceBinding) (state.Snapshot, error) {
	if d == nil || d.service == nil {
		return state.Snapshot{}, ErrWorkspaceDomainUnavailable
	}
	if err := binding.Validate(); err != nil {
		return state.Snapshot{}, fmt.Errorf("localapi: workspace domain binding: %w", err)
	}
	view, err := d.service.View(ctx, binding.Scope)
	if err != nil {
		return state.Snapshot{}, fmt.Errorf("localapi: workspace domain view: %w", err)
	}
	return view.Snapshot, nil
}

func (d *WorkspaceDomain) Apply(ctx context.Context, binding types.WorkspaceBinding, actor types.ActorEnvelope, operation state.OperationV1) (projectstate.WorkspaceStatus, error) {
	if d == nil || d.service == nil {
		return projectstate.WorkspaceStatus{}, ErrWorkspaceDomainUnavailable
	}
	if err := binding.Validate(); err != nil {
		return projectstate.WorkspaceStatus{}, fmt.Errorf("localapi: workspace domain binding: %w", err)
	}
	if err := actor.ValidateLocalAction(); err != nil {
		return projectstate.WorkspaceStatus{}, fmt.Errorf("localapi: workspace domain actor: %w", err)
	}
	if operation.Actor != actor {
		return projectstate.WorkspaceStatus{}, fmt.Errorf("localapi: workspace operation actor does not match server-owned actor")
	}
	status, err := d.service.Apply(ctx, binding.Scope, operation)
	if err != nil {
		return projectstate.WorkspaceStatus{}, err
	}
	return status, nil
}

func (d *WorkspaceDomain) ListChannels(ctx context.Context, binding types.WorkspaceBinding) ([]WorkspaceChannel, error) {
	view, err := d.View(ctx, binding)
	if err != nil {
		return nil, err
	}
	channels := make([]WorkspaceChannel, 0, len(view.Channels))
	for _, record := range view.Channels {
		if record.Value == nil || record.Tombstone != nil {
			continue
		}
		channels = append(channels, WorkspaceChannel{ID: record.Value.ID, Name: record.Value.Name, CreatedAt: record.Value.CreatedAt})
	}
	sort.Slice(channels, func(i, j int) bool {
		if channels[i].Name != channels[j].Name {
			return channels[i].Name < channels[j].Name
		}
		return channels[i].ID < channels[j].ID
	})
	return channels, nil
}

func (d *WorkspaceDomain) Channel(ctx context.Context, binding types.WorkspaceBinding, channelID string) (WorkspaceChannel, error) {
	view, err := d.View(ctx, binding)
	if err != nil {
		return WorkspaceChannel{}, err
	}
	record, ok := view.Channels[channelID]
	if !ok || record.Value == nil || record.Tombstone != nil {
		return WorkspaceChannel{}, ErrPortableChannelNotFound
	}
	return WorkspaceChannel{ID: record.Value.ID, Name: record.Value.Name, CreatedAt: record.Value.CreatedAt}, nil
}

func (d *WorkspaceDomain) CreateChannel(ctx context.Context, binding types.WorkspaceBinding, actor types.ActorEnvelope, name string) (WorkspaceChannel, error) {
	view, err := d.View(ctx, binding)
	if err != nil {
		return WorkspaceChannel{}, err
	}
	recordID, err := d.canonicalID("channel")
	if err != nil {
		return WorkspaceChannel{}, err
	}
	operationID, err := d.canonicalID("channel operation")
	if err != nil {
		return WorkspaceChannel{}, err
	}
	createdAt := d.now().UTC()
	record := state.ChannelV1{SchemaVersion: 1, Kind: "channel", ID: recordID, Name: name, CreatedAt: createdAt, Extensions: state.ExtensionsV1{}}
	operation := state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutRecord,
		ExpectedViewDigest: view.Digest, Actor: actor,
		PutRecord: &state.PutRecordV1{Record: state.RecordValueV1{Channel: &record}},
	}
	if _, err := d.Apply(ctx, binding, actor, operation); err != nil {
		return WorkspaceChannel{}, err
	}
	return WorkspaceChannel{ID: record.ID, Name: record.Name, CreatedAt: record.CreatedAt}, nil
}

func (d *WorkspaceDomain) ListArticles(ctx context.Context, binding types.WorkspaceBinding) ([]WorkspaceArticle, error) {
	view, err := d.View(ctx, binding)
	if err != nil {
		return nil, err
	}
	articles := make([]WorkspaceArticle, 0, len(view.Articles))
	for _, record := range view.Articles {
		if record.Value == nil || record.Tombstone != nil {
			continue
		}
		article, err := workspaceArticle(record)
		if err != nil {
			return nil, err
		}
		articles = append(articles, article)
	}
	sort.Slice(articles, func(i, j int) bool {
		if articles[i].Title != articles[j].Title {
			return articles[i].Title < articles[j].Title
		}
		return articles[i].ID < articles[j].ID
	})
	return articles, nil
}

func (d *WorkspaceDomain) GetArticle(ctx context.Context, binding types.WorkspaceBinding, articleID string) (WorkspaceArticle, error) {
	view, err := d.View(ctx, binding)
	if err != nil {
		return WorkspaceArticle{}, err
	}
	record, ok := view.Articles[articleID]
	if !ok || record.Value == nil || record.Tombstone != nil {
		return WorkspaceArticle{}, ErrPortableArticleNotFound
	}
	return workspaceArticle(record)
}

func (d *WorkspaceDomain) WriteArticle(ctx context.Context, binding types.WorkspaceBinding, actor types.ActorEnvelope, title, body string, frontmatter json.RawMessage) (WorkspaceArticle, error) {
	view, err := d.View(ctx, binding)
	if err != nil {
		return WorkspaceArticle{}, err
	}
	principalID := actor.PrincipalID()
	portableActor, ok := view.Actors[principalID]
	if !ok || portableActor.Value == nil || portableActor.Tombstone != nil || portableActor.Value.ActorKind != actor.ActorKind {
		return WorkspaceArticle{}, fmt.Errorf("%w: publish actor %s as portable state before writing KB", ErrPortableActorMissing, principalID)
	}
	metadata, err := decodePortableFrontmatter(frontmatter)
	if err != nil {
		return WorkspaceArticle{}, err
	}
	canonicalBody, err := state.CanonicalMarkdown([]byte(body))
	if err != nil {
		return WorkspaceArticle{}, err
	}
	recordID, err := d.canonicalID("KB article")
	if err != nil {
		return WorkspaceArticle{}, err
	}
	operationID, err := d.canonicalID("KB operation")
	if err != nil {
		return WorkspaceArticle{}, err
	}
	now := d.now().UTC()
	record := state.KBArticleV1{
		SchemaVersion: 1, Kind: "kb_article", ID: recordID, Title: title,
		Frontmatter: metadata, AuthorActorID: principalID, RelatedArticleIDs: []string{},
		CreatedAt: now, UpdatedAt: now, Extensions: state.ExtensionsV1{},
	}
	operation := state.OperationV1{
		SchemaVersion: 1, ID: operationID, Kind: state.OperationPutKBArticle,
		ExpectedViewDigest: view.Digest, Actor: actor,
		PutKBArticle: &state.PutKBArticleV1{Record: record, Body: string(canonicalBody)},
	}
	if _, err := d.Apply(ctx, binding, actor, operation); err != nil {
		return WorkspaceArticle{}, err
	}
	return workspaceArticle(state.KBRecord{Value: &record, Body: canonicalBody})
}

func (d *WorkspaceDomain) canonicalID(kind string) (string, error) {
	id, err := d.newID()
	if err != nil {
		return "", fmt.Errorf("localapi: allocate %s ID: %w", kind, err)
	}
	if !canonicalUUIDv4(id) {
		return "", fmt.Errorf("localapi: allocate %s ID: non-canonical UUIDv4", kind)
	}
	return id, nil
}

func canonicalUUIDv4(value string) bool {
	if !types.CanonicalUUID(value) || value[14] != '4' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

func decodePortableFrontmatter(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	if err := rejectDuplicateJSONMembers(raw); err != nil {
		return nil, fmt.Errorf("localapi: KB frontmatter must be a strict JSON object: %w", err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, fmt.Errorf("localapi: KB frontmatter must be a strict JSON object")
	}
	return value, nil
}

func workspaceArticle(record state.KBRecord) (WorkspaceArticle, error) {
	frontmatter, err := state.CanonicalJSON(record.Value.Frontmatter)
	if err != nil {
		return WorkspaceArticle{}, err
	}
	return WorkspaceArticle{
		ID: record.Value.ID, Title: record.Value.Title, Body: string(bytes.Clone(record.Body)),
		Frontmatter: bytes.TrimSpace(frontmatter), AuthorActorID: record.Value.AuthorActorID,
		RelatedArticleIDs: append([]string(nil), record.Value.RelatedArticleIDs...),
		CreatedAt:         record.Value.CreatedAt, UpdatedAt: record.Value.UpdatedAt,
	}, nil
}

// bindResolvedProjectArguments adapts legacy handler inputs to the Gateway-
// owned project selected by the exact resolved workspace. Public callers never
// supply this field.
func bindResolvedProjectArguments(ctx context.Context, public json.RawMessage) (json.RawMessage, error) {
	binding, err := ResolvedBinding(ctx)
	if err != nil {
		return nil, err
	}
	var arguments map[string]json.RawMessage
	if len(public) == 0 {
		arguments = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(public, &arguments); err != nil || arguments == nil {
		return nil, fmt.Errorf("localapi: public tool arguments must be an object")
	}
	projectID, err := json.Marshal(binding.Scope.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("localapi: encode resolved project: %w", err)
	}
	arguments["project_id"] = projectID
	bound, err := json.Marshal(arguments)
	if err != nil {
		return nil, fmt.Errorf("localapi: encode resolved arguments: %w", err)
	}
	return bound, nil
}

func (s *Server) privateRuntimeConfigured() bool {
	return s != nil && (s.projectState != nil || s.actorResolver != nil || s.identityStore != nil)
}

// authorizePrivateToolProvider is a defensive descriptor/provider consistency
// check. The registry is the sole live inventory; this check must not maintain
// a second name allowlist.
func authorizePrivateToolProvider(tool localTool, _ json.RawMessage) error {
	if tool.Handler == nil && tool.Name != "wormhole.channel.subscribe" {
		return fmt.Errorf("%w: %s", ErrBindingAwareProviderUnavailable, tool.Name)
	}
	return nil
}

// validatePrivateAgentSemantics permits agent_id only when it identifies the
// target of an operation or subscription filter. Action attribution is never
// caller-owned on the configured path.
func validatePrivateAgentSemantics(toolName string, public json.RawMessage) error {
	if toolName == "wormhole.agent.register" {
		var request agentLocalRegisterArgs
		if rejectDuplicateJSONMembers(public) != nil || decodeClosedJSON(public, &request) != nil {
			return fmt.Errorf("localapi: agent register: invalid public arguments")
		}
	}
	var arguments map[string]json.RawMessage
	if len(public) == 0 {
		return nil
	}
	if err := json.Unmarshal(public, &arguments); err != nil || arguments == nil {
		return fmt.Errorf("localapi: public tool arguments must be an object")
	}
	if _, supplied := arguments["agent_id"]; !supplied {
		return nil
	}
	switch toolName {
	case "wormhole.agent.register", "wormhole.agent.presence", "wormhole.channel.subscribe":
		return nil
	default:
		return fmt.Errorf("%w: agent_id", ErrPrivateAuthorityClaim)
	}
}

// resolvedLocalNamespace preserves the exact workspace boundary for the
// existing local SQLite/scheduler/eventbus primitives. A configured runtime
// has no fallback when the binding is absent or mismatched.
func (s *Server) resolvedLocalNamespace(ctx context.Context, projectID string) (string, error) {
	if !s.privateRuntimeConfigured() {
		return projectID, nil
	}
	binding, err := ResolvedBinding(ctx)
	if err != nil {
		return "", err
	}
	if binding.Scope.ProjectID != projectID {
		return "", fmt.Errorf("localapi: resolved binding project mismatch")
	}
	return string(binding.Scope.WorkspaceID), nil
}

// resolvedAgentNamespace keeps retained scheduler-presence tools on the
// Gateway-owned workspace binding. Legacy test-only servers continue to use
// their configured project route.
func (s *Server) resolvedAgentNamespace(ctx context.Context, legacyProjectID string) (string, error) {
	if s.privateRuntimeConfigured() {
		binding, err := ResolvedBinding(ctx)
		if err != nil {
			return "", err
		}
		return string(binding.Scope.WorkspaceID), nil
	}
	orgCtx, err := s.resolveOrgContext(legacyProjectID)
	if err != nil {
		return "", err
	}
	return s.resolvedLocalNamespace(ctx, orgCtx.ProjectID)
}

func (s *Server) resolvedActionPrincipal(ctx context.Context, legacy string) (string, error) {
	if !s.privateRuntimeConfigured() {
		return legacy, nil
	}
	actor, err := ServerOwnedActor(ctx)
	if err != nil {
		return "", err
	}
	principal := actor.PrincipalID()
	if principal == "" {
		return "", ErrServerOwnedActorMissing
	}
	return principal, nil
}
