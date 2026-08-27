package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/google/uuid"
)

// IntegrationGuidanceProvider is the read-only boundary implemented by Task
// 20's project-scoped authoritative SQLite cache. Implementations must return
// one internally consistent snapshot and perform no refresh or mutation.
type IntegrationGuidanceProvider interface {
	ReadIntegrationGuidance(context.Context, string) (IntegrationGuidanceSnapshot, error)
}

// IntegrationGuidanceSnapshot carries the authoritative lifecycle projection
// and, when cached, the immutable approved manifest body used for role
// selection. Repository files are deliberately absent from this boundary.
type IntegrationGuidanceSnapshot struct {
	State    IntegrationState
	Manifest *IntegrationManifest
}

type integrationGuidanceArgs struct {
	ProjectID string `json:"project_id" format:"uuid"`
}

type integrationGuidanceItem struct {
	Kind          string `json:"kind" enum:"agents_bootstrap,reference,skill"`
	Content       string `json:"content" format:"utf8-markdown"`
	ContentDigest string `json:"content_digest" format:"sha256"`
	Required      bool   `json:"required"`
}

func (integrationGuidanceItem) closedResponseSchema() {}

type integrationGuidanceResult struct {
	SchemaVersion          int                       `json:"schema_version" const:"1"`
	ProjectID              string                    `json:"project_id" format:"uuid"`
	ManifestID             *string                   `json:"manifest_id" format:"uuid"`
	ManifestVersion        *int64                    `json:"manifest_version" minimum:"1"`
	ManifestDigest         *string                   `json:"manifest_digest" format:"sha256"`
	ResolvedRole           *string                   `json:"resolved_role" format:"role-slug"`
	Guidance               []integrationGuidanceItem `json:"guidance" nonnull:"true"`
	MaterializationState   string                    `json:"materialization_state" enum:"applied,drifted,not_applied,recovery_required,removal_required"`
	ApprovalState          string                    `json:"approval_state" enum:"none,offered,verified,awaiting_approval,postponed,rejected,approved,revoked,verification_failed"`
	PendingManifestVersion *int64                    `json:"pending_manifest_version" minimum:"1"`
	PendingManifestDigest  *string                   `json:"pending_manifest_digest" format:"sha256"`
	ConnectionState        string                    `json:"connection_state" enum:"online,offline,synchronizing,attention_required"`
	LastVerifiedAt         *string                   `json:"last_verified_at" format:"date-time"`
}

func (integrationGuidanceResult) closedResponseSchema() {}

func (s *Server) handleIntegrationGuidance(ctx context.Context, raw json.RawMessage) (integrationGuidanceResult, error) {
	var args integrationGuidanceArgs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return integrationGuidanceResult{}, fmt.Errorf("integration guidance: invalid request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return integrationGuidanceResult{}, errors.New("integration guidance: invalid request")
	}
	project, err := uuid.Parse(args.ProjectID)
	if err != nil || project == uuid.Nil || project.String() != args.ProjectID {
		return integrationGuidanceResult{}, errors.New("integration guidance: project_id must be a canonical non-nil UUID")
	}
	org, err := s.resolveOrgContext(args.ProjectID)
	if err != nil || org.ProjectID != args.ProjectID {
		if err != nil {
			return integrationGuidanceResult{}, err
		}
		return integrationGuidanceResult{}, errors.New("integration guidance: project scope is not configured")
	}
	if s.integrationGuidance == nil {
		return integrationGuidanceResult{}, errors.New("integration guidance: approved cache provider is unavailable")
	}
	snapshot, err := s.integrationGuidance.ReadIntegrationGuidance(ctx, args.ProjectID)
	if err != nil {
		return integrationGuidanceResult{}, fmt.Errorf("integration guidance: read approved cache: %w", err)
	}
	return resolveIntegrationGuidance(args.ProjectID, snapshot)
}

func resolveIntegrationGuidance(projectID string, snapshot IntegrationGuidanceSnapshot) (integrationGuidanceResult, error) {
	state := snapshot.State
	if state.SchemaVersion != 1 || state.ProjectID != projectID {
		return integrationGuidanceResult{}, errors.New("integration guidance: cached state project or schema mismatch")
	}
	if !validIntegrationGuidanceState(state.ApprovalState, state.MaterializationState, state.ConnectionState) {
		return integrationGuidanceResult{}, errors.New("integration guidance: cached lifecycle state is invalid")
	}
	if state.ResolvedRole != "" && !validSlug(state.ResolvedRole) {
		return integrationGuidanceResult{}, errors.New("integration guidance: cached resolved role is invalid")
	}
	activeFields := presentPointerCount(state.ActiveManifestID, state.ActiveManifestVersion, state.ActiveManifestDigest)
	if activeFields != 0 && activeFields != 3 {
		return integrationGuidanceResult{}, errors.New("integration guidance: cached active manifest metadata is incomplete")
	}
	if activeFields == 3 {
		activeID, parseErr := uuid.Parse(*state.ActiveManifestID)
		if parseErr != nil || activeID == uuid.Nil || activeID.String() != *state.ActiveManifestID || *state.ActiveManifestVersion < 1 || !digestPattern.MatchString(*state.ActiveManifestDigest) {
			return integrationGuidanceResult{}, errors.New("integration guidance: cached active manifest metadata is invalid")
		}
	}
	pendingFields := presentPointerCount(state.PendingManifestVersion, state.PendingManifestDigest)
	if pendingFields != 0 && pendingFields != 2 {
		return integrationGuidanceResult{}, errors.New("integration guidance: cached pending manifest metadata is incomplete")
	}
	if pendingFields == 2 && (*state.PendingManifestVersion < 1 || !digestPattern.MatchString(*state.PendingManifestDigest)) {
		return integrationGuidanceResult{}, errors.New("integration guidance: cached pending manifest metadata is invalid")
	}

	result := integrationGuidanceResult{
		SchemaVersion: 1, ProjectID: projectID,
		ManifestID: cloneStringPointer(state.ActiveManifestID), ManifestVersion: cloneInt64Pointer(state.ActiveManifestVersion),
		ManifestDigest: cloneStringPointer(state.ActiveManifestDigest),
		Guidance:       []integrationGuidanceItem{}, MaterializationState: state.MaterializationState,
		ApprovalState: state.ApprovalState, PendingManifestVersion: cloneInt64Pointer(state.PendingManifestVersion),
		PendingManifestDigest: cloneStringPointer(state.PendingManifestDigest), ConnectionState: state.ConnectionState,
	}
	if state.ResolvedRole != "" {
		result.ResolvedRole = stringPointer(state.ResolvedRole)
	}
	if state.LastVerifiedAt != "" {
		verified, parseErr := time.Parse(time.RFC3339Nano, state.LastVerifiedAt)
		if parseErr != nil || verified.UTC().Format(time.RFC3339Nano) != state.LastVerifiedAt {
			return integrationGuidanceResult{}, errors.New("integration guidance: cached verification time is not canonical UTC RFC3339Nano")
		}
		result.LastVerifiedAt = stringPointer(state.LastVerifiedAt)
	}

	compatible := state.CompatibilityState == "compatible"
	if activeFields == 3 && !compatible {
		result.ConnectionState = "attention_required"
	}
	if !state.GuidanceActive || state.ApprovalState == "revoked" || !compatible || activeFields == 0 {
		return result, nil
	}
	if snapshot.Manifest == nil || state.ResolvedRole == "" {
		return integrationGuidanceResult{}, errors.New("integration guidance: active approved manifest body or role is unavailable")
	}
	manifest := *snapshot.Manifest
	if manifest.ProjectID != projectID || manifest.ManifestID != *state.ActiveManifestID || manifest.ManifestVersion != *state.ActiveManifestVersion || manifest.ManifestDigest != *state.ActiveManifestDigest {
		return integrationGuidanceResult{}, errors.New("integration guidance: approved manifest does not match active state")
	}
	if err := validateMaterializationManifest(manifest, state.ResolvedRole); err != nil {
		// Materialization validation errors may contain repository targets. The
		// model-facing contract intentionally exposes content, never paths.
		return integrationGuidanceResult{}, errors.New("integration guidance: cached approved manifest is invalid")
	}
	entries := append([]IntegrationManifestEntry(nil), manifest.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Target < entries[j].Target })
	for _, entry := range entries {
		if !matchesRole(entry.RoleFilters, state.ResolvedRole) {
			continue
		}
		result.Guidance = append(result.Guidance, integrationGuidanceItem{
			Kind: entry.Kind, Content: entry.Content, ContentDigest: entry.ContentDigest, Required: entry.Required,
		})
	}
	return result, nil
}

func presentPointerCount(values ...any) int {
	count := 0
	for _, value := range values {
		if value != nil && !reflectValueIsNil(value) {
			count++
		}
	}
	return count
}

func reflectValueIsNil(value any) bool {
	switch typed := value.(type) {
	case *string:
		return typed == nil
	case *int64:
		return typed == nil
	default:
		return true
	}
}

func validIntegrationGuidanceState(approval, materialization, connection string) bool {
	validApproval := map[string]bool{"none": true, "offered": true, "verified": true, "awaiting_approval": true, "postponed": true, "rejected": true, "approved": true, "revoked": true, "verification_failed": true}
	validMaterialization := map[string]bool{"applied": true, "drifted": true, "not_applied": true, "recovery_required": true, "removal_required": true}
	validConnection := map[string]bool{"online": true, "offline": true, "synchronizing": true, "attention_required": true}
	return validApproval[approval] && validMaterialization[materialization] && validConnection[connection]
}

// toolGuidance is the canonical, structured usage guidance for one live
// Gateway descriptor. RequiredPermissions and MinimalExample are derived from
// that descriptor so guidance cannot duplicate its permission or schema
// contract.
type toolGuidance struct {
	ToolName                 string         `json:"tool_name"`
	Concept                  string         `json:"concept"`
	Purpose                  string         `json:"purpose"`
	UseWhen                  string         `json:"use_when"`
	DoNotUseWhen             string         `json:"do_not_use_when"`
	MutatesState             bool           `json:"mutates_state"`
	RequiredPermissions      []string       `json:"required_permission"`
	Prerequisites            string         `json:"prerequisites"`
	FreshnessImplications    string         `json:"freshness_implications"`
	SourceAccessImplications string         `json:"source_access_implications"`
	RecommendedFollowUp      string         `json:"recommended_follow_up"`
	MinimalExample           map[string]any `json:"minimal_example"`
	MisuseWarning            string         `json:"misuse_warning"`
}

type guidanceText struct {
	Purpose                  string
	UseWhen                  string
	DoNotUseWhen             string
	MutatesState             bool
	Prerequisites            string
	FreshnessImplications    string
	SourceAccessImplications string
	RecommendedFollowUp      string
	MisuseWarning            string
}

// gatewayToolGuidance attaches curated operational advice to each descriptor
// in the live registry. It deliberately has no entry for designed-only tools:
// a tool and its guidance become live together when the tool is registered.
func gatewayToolGuidance(registry *localRegistry) []toolGuidance {
	textByTool := gatewayGuidanceText()
	names := make([]string, 0, len(textByTool))
	for name := range textByTool {
		names = append(names, name)
	}
	sort.Strings(names)

	guidance := make([]toolGuidance, 0, len(names))
	for _, name := range names {
		text := textByTool[name]
		tool, exists := registry.Get(name)
		permissions := []string{}
		example := map[string]any{}
		if exists {
			permissions = make([]string, len(tool.RequiredPermissions))
			copy(permissions, tool.RequiredPermissions)
			example = minimalInputExample(tool)
		}
		guidance = append(guidance, toolGuidance{
			ToolName:                 name,
			Concept:                  gatewayGuidanceConcept(name),
			Purpose:                  text.Purpose,
			UseWhen:                  text.UseWhen,
			DoNotUseWhen:             text.DoNotUseWhen,
			MutatesState:             text.MutatesState,
			RequiredPermissions:      permissions,
			Prerequisites:            text.Prerequisites,
			FreshnessImplications:    text.FreshnessImplications,
			SourceAccessImplications: text.SourceAccessImplications,
			RecommendedFollowUp:      text.RecommendedFollowUp,
			MinimalExample:           example,
			MisuseWarning:            text.MisuseWarning,
		})
	}
	return guidance
}

func gatewayGuidanceConcept(toolName string) string {
	switch {
	case toolName == "wormhole.sync.status":
		return "local status and synchronisation"
	case hasToolPrefix(toolName, "wormhole.workspace."):
		return "portable workspace"
	case hasToolPrefix(toolName, "wormhole.agent."):
		return "identity"
	case hasToolPrefix(toolName, "wormhole.channel."):
		return "channels and events"
	case hasToolPrefix(toolName, "wormhole.kb."):
		return "knowledge"
	default:
		return ""
	}
}

func hasToolPrefix(toolName, prefix string) bool {
	return len(toolName) >= len(prefix) && toolName[:len(prefix)] == prefix
}

func gatewayGuidanceText() map[string]guidanceText {
	const noSource = "This tool does not read or return repository source."
	return map[string]guidanceText{
		// Identity.
		"wormhole.agent.register": {
			Purpose:                  "Register an existing agent's local presence and declared capabilities.",
			UseWhen:                  "When making a known agent available to local routing.",
			DoNotUseWhen:             "Do not use it for a routine presence heartbeat.",
			MutatesState:             true,
			Prerequisites:            "A server-resolved workspace and an existing agent identifier.",
			FreshnessImplications:    "Presence registration is Gateway-local scheduler state.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Set presence after local registration, then verify with agent.list.",
			MisuseWarning:            "Do not infer shared identity creation or new permissions from local presence registration.",
		},
		"wormhole.agent.presence": {
			Purpose:                  "Update an existing locally registered agent's availability.",
			UseWhen:                  "When an agent starts, becomes busy, or stops accepting work.",
			DoNotUseWhen:             "Do not use it to create an agent or assign work.",
			MutatesState:             true,
			Prerequisites:            "The agent must already be locally registered.",
			FreshnessImplications:    "Presence is Gateway-local and is not durable shared task state.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use agent.list to verify the current local scheduler view.",
			MisuseWarning:            "Do not assume a local presence update grants permissions.",
		},
		"wormhole.agent.list": {
			Purpose:                  "List agents known to the local scheduler.",
			UseWhen:                  "Before routing work by capability or checking local availability.",
			DoNotUseWhen:             "Do not use it as a complete organisation-wide identity directory.",
			MutatesState:             false,
			Prerequisites:            "A bound Gateway project.",
			FreshnessImplications:    "Results cover only the current clone-local scheduler state.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Update presence when a registered agent's local availability changes.",
			MisuseWarning:            "Do not infer shared identity, remote presence, or authorization from this local list.",
		},

		// Local status and synchronisation.
		"wormhole.sync.status": {
			Purpose:                  "Report the truthful local-only synchronization state and pending Fabric-write count.",
			UseWhen:                  "When confirming that this Stage 2 Gateway is offline and has no Fabric queue.",
			DoNotUseWhen:             "Do not use it as a Fabric health probe or assume it contacts a remote service.",
			MutatesState:             false,
			Prerequisites:            "A bound Gateway project.",
			FreshnessImplications:    "Always reports offline with zero pending writes in the local-only Stage 2 runtime.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use workspace.status or workspace.diff to inspect local portable state.",
			MisuseWarning:            "Offline is the expected local-only state, not a failed network probe.",
		},
		"wormhole.workspace.status": {
			Purpose:                  "Inspect the bound workspace candidate, overlay generation, and publication review state.",
			UseWhen:                  "Before importing, checkpointing, or deciding whether a public-Git acknowledgement is required.",
			DoNotUseWhen:             "Do not use it as a Git status replacement or to mutate portable state.",
			MutatesState:             false,
			Prerequisites:            "A registered workspace resolved by Gateway.",
			FreshnessImplications:    "Reports current private workspace bookkeeping and the accepted tracked snapshot without changing either.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use workspace.diff to inspect exact portable changes.",
			MisuseWarning:            "Do not treat candidate presence as Git acceptance.",
		},
		"wormhole.workspace.diff": {
			Purpose:                  "Return the attributed semantic portable-state diff and exact publication review digest.",
			UseWhen:                  "Before checkpointing or reviewing tracked portable-state changes.",
			DoNotUseWhen:             "Do not use it to inspect arbitrary source-code changes.",
			MutatesState:             false,
			Prerequisites:            "A registered workspace with accepted portable state.",
			FreshnessImplications:    "Compares the current composed candidate against the accepted portable snapshot.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Review changes and pass the exact digest to checkpoint when public publication requires acknowledgement.",
			MisuseWarning:            "A review digest is bound to the exact candidate and becomes stale after any mutation.",
		},
		"wormhole.workspace.import": {
			Purpose:                  "Import direct tracked portable-state edits into the attributed workspace candidate.",
			UseWhen:                  "After editing .wormhole/state/v1 through ordinary repository tools.",
			DoNotUseWhen:             "Do not use it to import private databases, credentials, or operational journals.",
			MutatesState:             true,
			Prerequisites:            "A registered workspace and a valid portable working tree.",
			FreshnessImplications:    "Reads the exact current portable tree and rebases the private overlay through its imported generation.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Inspect workspace.diff before checkpointing.",
			MisuseWarning:            "Import does not stage, commit, or push Git.",
		},
		"wormhole.workspace.checkpoint": {
			Purpose:                  "Materialize the current portable candidate without performing Git publication.",
			UseWhen:                  "After reviewing the semantic diff and any required public-Git acknowledgement.",
			DoNotUseWhen:             "Do not use it as a substitute for Git staging, commit, or push.",
			MutatesState:             true,
			Prerequisites:            "A registered workspace; public Git requires the exact current publication review digest.",
			FreshnessImplications:    "The supplied acknowledgement is rejected if the candidate changed after review.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Review the unstaged tracked tree, then accept it with ordinary Git commands.",
			MisuseWarning:            "Checkpoint never stages, commits, or pushes Git.",
		},
		"wormhole.workspace.stash": {
			Purpose:                  "Durably stash the current private overlay under an explicit request ID and label.",
			UseWhen:                  "When pausing attributed work without changing accepted portable state.",
			DoNotUseWhen:             "Do not use it as Git stash or as a publication action.",
			MutatesState:             true,
			Prerequisites:            "A registered workspace, unique request ID, and non-empty label.",
			FreshnessImplications:    "Captures the exact current overlay and candidate digest in private state.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Confirm workspace.status and resume through the supported workspace lifecycle.",
			MisuseWarning:            "Private stash rows are not portable and never enter tracked Git state.",
		},

		// Channels and events.
		"wormhole.channel.list": {
			Purpose:                  "List channels from this workspace's composed portable project state.",
			UseWhen:                  "When choosing an existing durable channel for a typed event.",
			DoNotUseWhen:             "Do not use it to inspect event history.",
			MutatesState:             false,
			Prerequisites:            "A Gateway-resolved workspace.",
			FreshnessImplications:    "Includes accepted tracked state plus the current private candidate overlay.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Read channel.events or create a missing channel.",
			MisuseWarning:            "A candidate channel is not accepted Git state until checkpoint and ordinary Git acceptance.",
		},
		"wormhole.channel.create": {
			Purpose:                  "Create a portable channel in this workspace's private candidate overlay.",
			UseWhen:                  "When durable event routing needs a new named channel.",
			DoNotUseWhen:             "Do not use it for one-off messages better represented by an existing channel.",
			MutatesState:             true,
			Prerequisites:            "A Gateway-resolved workspace and channel.create permission.",
			FreshnessImplications:    "The channel is immediately visible in the composed candidate and becomes portable through checkpoint and Git acceptance.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Post a typed event after creation.",
			MisuseWarning:            "Do not create duplicate channels to represent the same ongoing topic.",
		},
		"wormhole.channel.events": {
			Purpose:                  "List recent durable events from local channels.",
			UseWhen:                  "When reconstructing recent local collaboration context.",
			DoNotUseWhen:             "Do not use it as a live subscription or a portable audit history.",
			MutatesState:             false,
			Prerequisites:            "A Gateway-resolved workspace.",
			FreshnessImplications:    "Reads clone-private operational activity for the resolved workspace only.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use channel.subscribe for subsequent local events or inspect a referenced KB article.",
			MisuseWarning:            "Operational events do not enter checkpointed portable state or another clone.",
		},
		"wormhole.channel.post": {
			Purpose:                  "Publish clone-private operational activity after validating its portable channel.",
			UseWhen:                  "When recording a handoff, discovery, decision, or progress update.",
			DoNotUseWhen:             "Do not use it for sensitive credentials or unstructured chatter.",
			MutatesState:             true,
			Prerequisites:            "A Gateway-resolved workspace, a live portable channel ID, and channel.post permission.",
			FreshnessImplications:    "The event is durable in this clone's private operational store and is not queued for Fabric.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use channel.events to confirm the local activity or reference a relevant KB article.",
			MisuseWarning:            "Do not put secrets, source copies, or unsupported event types in the payload.",
		},
		"wormhole.channel.subscribe": {
			Purpose:                  "Subscribe this MCP connection to all future event notifications in its resolved workspace.",
			UseWhen:                  "When subsequent events from the exact local workspace are needed during the active session.",
			DoNotUseWhen:             "Do not use it to recover historical events or create durable shared state.",
			MutatesState:             true,
			Prerequisites:            "An initialized MCP connection and a bound project.",
			FreshnessImplications:    "Notifications reflect future clone-local delivery only and are not replayed after reconnect.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Keep the connection open and use channel.events for prior context.",
			MisuseWarning:            "Do not assume a subscription survives reconnects or provides a complete audit stream.",
		},

		// Knowledge.
		"wormhole.kb.list": {
			Purpose:                  "List KB articles from this workspace's composed portable project state.",
			UseWhen:                  "When locating durable organisational context by article metadata.",
			DoNotUseWhen:             "Do not use it when a known article ID can be fetched directly.",
			MutatesState:             false,
			Prerequisites:            "A Gateway-resolved workspace.",
			FreshnessImplications:    "Includes accepted tracked state plus the current private candidate overlay.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Read a selected article or write a new durable fact.",
			MisuseWarning:            "This is deterministic listing, not semantic search.",
		},
		"wormhole.kb.get": {
			Purpose:                  "Get a named KB article or list articles when no ID is supplied.",
			UseWhen:                  "When reading a known durable procedure, decision, or discovery.",
			DoNotUseWhen:             "Do not use it to retrieve code as an authoritative source.",
			MutatesState:             false,
			Prerequisites:            "A Gateway-resolved workspace; supply an article ID for a specific record.",
			FreshnessImplications:    "Reads the current composed portable view, including uncheckpointed candidate operations.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Verify referenced Git pointers and update stale durable knowledge with kb.write.",
			MisuseWarning:            "Git remains code truth; do not rely on KB prose instead of current source verification.",
		},
		"wormhole.kb.write": {
			Purpose:                  "Write a portable KB article into this workspace's private candidate overlay.",
			UseWhen:                  "When preserving a durable fact, decision, discovery, or procedure.",
			DoNotUseWhen:             "Do not use it for transient status chatter or source-file copies.",
			MutatesState:             true,
			Prerequisites:            "A Gateway-resolved workspace, a published matching portable actor, and kb.write permission.",
			FreshnessImplications:    "The article is immediately visible in the composed candidate and becomes portable through checkpoint and Git acceptance.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use kb.get to verify the article, then inspect workspace.diff before checkpointing.",
			MisuseWarning:            "Do not store credentials or present Git-derived prose as authoritative code.",
		},
	}
}

// minimalInputExample synthesizes a request from the registry's generated
// input schema. It intentionally knows no tool-specific parameter objects.
func minimalInputExample(tool localTool) map[string]any {
	value, _ := minimalSchemaValue(buildInputSchema(tool)).(map[string]any)
	return value
}

func minimalSchemaValue(schema map[string]any) any {
	if _, hasType := schema["type"]; !hasType {
		if alternatives, ok := schema["anyOf"].([]map[string]any); ok && len(alternatives) > 0 {
			return minimalSchemaValue(alternatives[0])
		}
	}
	if typeName, _ := schema["type"].(string); typeName == "object" {
		properties, _ := schema["properties"].(map[string]any)
		fields := schemaStringSlice(schema["required"])
		if alternatives, ok := schema["anyOf"].([]map[string]any); ok && len(alternatives) > 0 {
			fields = append(fields, schemaStringSlice(alternatives[0]["required"])...)
		}
		sort.Strings(fields)
		result := make(map[string]any, len(fields))
		for _, field := range fields {
			if _, exists := result[field]; exists {
				continue
			}
			property, _ := properties[field].(map[string]any)
			result[field] = minimalSchemaValue(property)
		}
		return result
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	switch schema["type"] {
	case "string":
		return "example"
	case "integer":
		return 0
	case "number":
		return 0.0
	case "boolean":
		return false
	case "array":
		return []any{}
	default:
		return map[string]any{}
	}
}

func schemaStringSlice(value any) []string {
	values, _ := value.([]string)
	return append([]string(nil), values...)
}
