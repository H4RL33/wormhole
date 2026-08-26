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
	case toolName == "wormhole.agent.get_guidance":
		return "integration guidance"
	case toolName == "wormhole.agent.enrol" || hasToolPrefix(toolName, "wormhole.agent."):
		return "identity"
	case hasToolPrefix(toolName, "wormhole.code_graph."):
		return "Code Graph"
	case hasToolPrefix(toolName, "wormhole.task."):
		return "tasks"
	case hasToolPrefix(toolName, "wormhole.channel."):
		return "channels and events"
	case hasToolPrefix(toolName, "wormhole.kb."):
		return "knowledge"
	case hasToolPrefix(toolName, "wormhole.git."):
		return "Git pointers"
	default:
		return ""
	}
}

func hasToolPrefix(toolName, prefix string) bool {
	return len(toolName) >= len(prefix) && toolName[:len(prefix)] == prefix
}

func gatewayGuidanceText() map[string]guidanceText {
	const localReplica = "Reads reflect this Gateway's local replica; check sync status when remote freshness matters."
	const noSource = "This tool does not read or return repository source."
	return map[string]guidanceText{
		// Identity.
		"wormhole.agent.whoami": {
			Purpose:                  "Inspect the calling identity, capabilities, and permissions.",
			UseWhen:                  "At session start or before a permission-sensitive operation.",
			DoNotUseWhen:             "Do not use it to register an agent or change permissions.",
			MutatesState:             false,
			Prerequisites:            "An authenticated Gateway session.",
			FreshnessImplications:    localReplica,
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use the reported permissions to select an allowed tool.",
			MisuseWarning:            "Do not treat local identity information as a substitute for checking a specific operation's result.",
		},
		"wormhole.agent.get_guidance": {
			Purpose:                  "Read the current approved, role-applicable integration guidance and its lifecycle state from Gateway's local cache.",
			UseWhen:                  "At session start or before relying on managed organisational guidance for this project.",
			DoNotUseWhen:             "Do not use it to approve, apply, update, remove, roll back, refresh, or repair guidance.",
			MutatesState:             false,
			Prerequisites:            "An explicitly bound project and a compatible approved manifest cached by Gateway.",
			FreshnessImplications:    "The result is one local cached read; offline responses retain approved content while separately reporting newer unapproved pending state.",
			SourceAccessImplications: "This tool returns approved Markdown only; it does not read repository files or expose materialisation target paths.",
			RecommendedFollowUp:      "Use applicable returned guidance, or ask a human to inspect the integration CLI when approval, compatibility, drift, or recovery needs attention.",
			MisuseWarning:            "Empty guidance can mean no approved cache, revocation, or incompatibility; never infer approval or trigger a mutation from this read.",
		},
		"wormhole.agent.register": {
			Purpose:                  "Create a Passport-backed agent or register an existing agent's local presence.",
			UseWhen:                  "When joining an agent or making a known agent available to local routing.",
			DoNotUseWhen:             "Do not use it for a routine presence heartbeat.",
			MutatesState:             true,
			Prerequisites:            "Use the join or local-presence request shape, and obtain any required Fabric authorization for joins.",
			FreshnessImplications:    "Join state may require sync before other Gateways observe it.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Set presence after local registration, then verify with agent.list.",
			MisuseWarning:            "Do not mix join and presence fields; request shape selects the operation.",
		},
		"wormhole.agent.presence": {
			Purpose:                  "Update an existing locally registered agent's availability.",
			UseWhen:                  "When an agent starts, becomes busy, or stops accepting work.",
			DoNotUseWhen:             "Do not use it to create an agent or assign work.",
			MutatesState:             true,
			Prerequisites:            "The agent must already be locally registered.",
			FreshnessImplications:    "Presence is Gateway-local and is not durable shared task state.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use agent.list or task.route after advertising a capability.",
			MisuseWarning:            "Do not assume a local presence update grants permissions.",
		},
		"wormhole.agent.list": {
			Purpose:                  "List agents known to the local scheduler.",
			UseWhen:                  "Before routing work by capability or checking local availability.",
			DoNotUseWhen:             "Do not use it as a complete organisation-wide identity directory.",
			MutatesState:             false,
			Prerequisites:            "A bound Gateway project.",
			FreshnessImplications:    "Results cover current local scheduler state, not necessarily remote changes.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use task.route only after choosing a suitable local agent.",
			MisuseWarning:            "Do not infer remote presence or authorization from this local list.",
		},

		// Local status and synchronisation.
		"wormhole.sync.status": {
			Purpose:                  "Inspect Gateway-to-Fabric connection state and queued durable writes.",
			UseWhen:                  "Before relying on a remote observer seeing recent local changes.",
			DoNotUseWhen:             "Do not use it as a Fabric health probe or to force synchronization.",
			MutatesState:             false,
			Prerequisites:            "A bound Gateway project.",
			FreshnessImplications:    "Reports the current local queue and connection state; it does not refresh remote data.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Retry or defer remote-dependent work when the state needs attention.",
			MisuseWarning:            "Do not assume pending_writes is zero merely because a local write succeeded.",
		},
		"wormhole.agent.enrol": {
			Purpose:                  "Enroll a Gateway project and persist its credential profile before Passport credentials exist.",
			UseWhen:                  "During explicit Gateway-owned project enrollment or credential recovery.",
			DoNotUseWhen:             "Do not use it for normal authenticated agent registration.",
			MutatesState:             true,
			Prerequisites:            "A human-approved project binding, Fabric address, and credential-profile identifier beneath the Gateway credential root.",
			FreshnessImplications:    "Enrollment performs durable local lifecycle work and may need recovery or sync after an interrupted attempt.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Inspect sync.status and follow the returned lifecycle code before retrying.",
			MisuseWarning:            "Do not expose credential material or reuse an attempt key for a different enrollment.",
		},

		// Code Graph.
		"wormhole.code_graph.query": {
			Purpose:                  "Narrow Go source discovery through a bounded local Code Graph query.",
			UseWhen:                  "When an enabled, sufficiently current graph can locate symbols, callers, references, or a code path before broad search.",
			DoNotUseWhen:             "Do not use it for known files, non-code assets, untracked or ignored files, or when strict current source is required from a stale graph.",
			MutatesState:             false,
			Prerequisites:            "An enabled project graph and code_graph.query permission; source slices also require code_graph.source.read.",
			FreshnessImplications:    "Check code_graph.status and independently verify Git HEAD and the working-tree state before relying on graph results; the graph narrows discovery and does not replace Git, direct inspection, builds, or tests.",
			SourceAccessImplications: "Returned source is bounded by the request's source budget and is metadata-only without code_graph.source.read; never treat a slice as complete file context.",
			RecommendedFollowUp:      "Inspect the returned paths and symbols, then verify the live working tree with targeted reads and Git commands.",
			MisuseWarning:            "Edges are heuristic discovery aids, not proof of complete call, reference, or type-use coverage.",
		},
		"wormhole.code_graph.status": {
			Purpose:                  "Inspect local Code Graph health, revision, and freshness without changing it.",
			UseWhen:                  "Before a Code Graph query or when deciding whether graph output is current enough for a task.",
			DoNotUseWhen:             "Do not use it as proof that Git or the working tree is unchanged.",
			MutatesState:             false,
			Prerequisites:            "A bound project and code_graph.status permission.",
			FreshnessImplications:    "It reports graph freshness against tracked Go inventory and approved remote state; verify current Git HEAD and working-tree state directly for task-critical conclusions.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Query with a bounded source budget only when status is usable, or use ordinary repository inspection.",
			MisuseWarning:            "Do not call status as a rebuild request or mistake degraded health for current source.",
		},
		"wormhole.code_graph.rebuild": {
			Purpose:                  "Request one normal balanced copy-on-write rebuild from persisted approved Code Graph configuration.",
			UseWhen:                  "When status recommends a rebuild and a human-approved graph configuration already exists.",
			DoNotUseWhen:             "Do not use it to change graph configuration, force an unsafe rebuild, or compensate for unverified Git changes.",
			MutatesState:             true,
			Prerequisites:            "A bound project, code_graph.rebuild permission, enabled graph, and persisted approved configuration.",
			FreshnessImplications:    "The rebuild snapshots the approved checkout; verify Git HEAD and working-tree state before and after using the rebuilt graph for current-source decisions.",
			SourceAccessImplications: "Rebuild does not grant source access or bypass query source budgets and code_graph.source.read.",
			RecommendedFollowUp:      "Recheck code_graph.status, then query with bounded source access if needed.",
			MisuseWarning:            "Do not expect exact semantic completeness: graph edges remain heuristic, while the rebuild preserves the active graph on failure.",
		},

		// Tasks.
		"wormhole.task.list": {
			Purpose:                  "List tasks in the local task-graph replica.",
			UseWhen:                  "When orienting to available or status-filtered work.",
			DoNotUseWhen:             "Do not use it when a known task ID is all that is needed.",
			MutatesState:             false,
			Prerequisites:            "A bound project.",
			FreshnessImplications:    localReplica,
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Fetch a selected task or create/route an agreed item of work.",
			MisuseWarning:            "Do not treat a local list as proof that remote task updates have already synchronized.",
		},
		"wormhole.task.get": {
			Purpose:                  "Get one task from the local task-graph replica.",
			UseWhen:                  "When a task ID is known and its details are needed.",
			DoNotUseWhen:             "Do not use it to discover tasks by broad status.",
			MutatesState:             false,
			Prerequisites:            "A bound project and task ID.",
			FreshnessImplications:    localReplica,
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Use task.list for discovery or record a durable event for progress.",
			MisuseWarning:            "Do not assume an absent task has never existed remotely while offline.",
		},
		"wormhole.task.create": {
			Purpose:                  "Create a local task and enqueue it for synchronization.",
			UseWhen:                  "When intended work needs durable ownership-independent tracking.",
			DoNotUseWhen:             "Do not use it for ephemeral discussion or an already-existing task.",
			MutatesState:             true,
			Prerequisites:            "A bound project and task.create permission.",
			FreshnessImplications:    "The write is durable locally first and becomes shared after synchronization.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Route the new task or post a typed channel event with the resulting ID.",
			MisuseWarning:            "Do not claim a created task is remotely visible until sync has caught up.",
		},
		"wormhole.task.update_status": {
			Purpose:                  "Transition a task through the validated local workflow and enqueue the status update for synchronization.",
			UseWhen:                  "When meaningful work begins, blocks, resumes, or completes and the shared task state should reflect it.",
			DoNotUseWhen:             "Do not use it for narration, an invalid workflow jump, or without a durable status-event channel.",
			MutatesState:             true,
			Prerequisites:            "A bound project, existing task and channel, and task.update_status permission.",
			FreshnessImplications:    "The validated transition and event commit locally first; Fabric and other Gateways observe them after synchronization.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Verify task.get and sync.status, then leave concise handoff context before marking work done.",
			MisuseWarning:            "Do not report remote completion while the durable update is still pending synchronization.",
		},
		"wormhole.task.route": {
			Purpose:                  "Create a task and route it to a capable locally registered agent.",
			UseWhen:                  "When work should be created and assigned in one local scheduling action.",
			DoNotUseWhen:             "Do not use it when assignment must target a remote or unregistered agent.",
			MutatesState:             true,
			Prerequisites:            "A bound project, task.create and task.assign permissions, and a matching local agent capability.",
			FreshnessImplications:    "Routing is local; remote observers see the task only after synchronization.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Confirm the assigned agent's local presence and post an event for material handoffs.",
			MisuseWarning:            "Do not assume capability matching proves workload capacity or remote availability.",
		},

		// Channels and events.
		"wormhole.channel.list": {
			Purpose:                  "List channels in the local event-bus replica.",
			UseWhen:                  "When choosing an existing durable channel for a typed event.",
			DoNotUseWhen:             "Do not use it to inspect event history.",
			MutatesState:             false,
			Prerequisites:            "A bound project.",
			FreshnessImplications:    localReplica,
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Read channel.events or create a missing channel.",
			MisuseWarning:            "Do not infer that a locally absent channel is absent from Fabric while offline.",
		},
		"wormhole.channel.create": {
			Purpose:                  "Create a local channel and enqueue it for synchronization.",
			UseWhen:                  "When durable event routing needs a new named channel.",
			DoNotUseWhen:             "Do not use it for one-off messages better represented by an existing channel.",
			MutatesState:             true,
			Prerequisites:            "A bound project and channel.create permission.",
			FreshnessImplications:    "The channel exists locally before it becomes shared through sync.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Post a typed event after creation.",
			MisuseWarning:            "Do not create duplicate channels to represent the same ongoing topic.",
		},
		"wormhole.channel.events": {
			Purpose:                  "List recent durable events from local channels.",
			UseWhen:                  "When reconstructing recent local collaboration context.",
			DoNotUseWhen:             "Do not use it as a live subscription or a guarantee of complete remote history.",
			MutatesState:             false,
			Prerequisites:            "A bound project.",
			FreshnessImplications:    localReplica,
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Subscribe for subsequent events or inspect a referenced task or KB article.",
			MisuseWarning:            "Do not treat the local event window as an audit-complete remote log.",
		},
		"wormhole.channel.post": {
			Purpose:                  "Publish a durable typed event locally and enqueue it for synchronization.",
			UseWhen:                  "When recording a handoff, discovery, decision, or progress update.",
			DoNotUseWhen:             "Do not use it for sensitive credentials or unstructured chatter.",
			MutatesState:             true,
			Prerequisites:            "A bound project, a channel ID, and channel.post permission.",
			FreshnessImplications:    "The event is durable locally first; remote delivery follows synchronization.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Link the event to the relevant task or KB article in its payload or note.",
			MisuseWarning:            "Do not put secrets, source copies, or unsupported event types in the payload.",
		},
		"wormhole.channel.subscribe": {
			Purpose:                  "Subscribe this MCP connection to all future event notifications in its resolved workspace.",
			UseWhen:                  "When subsequent events from the exact local workspace are needed during the active session.",
			DoNotUseWhen:             "Do not use it to recover historical events or create durable shared state.",
			MutatesState:             true,
			Prerequisites:            "An initialized MCP connection and a bound project.",
			FreshnessImplications:    "Notifications reflect future local delivery and can be delayed by synchronization.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Keep the connection open and use channel.events for prior context.",
			MisuseWarning:            "Do not assume a subscription survives reconnects or provides a complete audit stream.",
		},

		// Knowledge.
		"wormhole.kb.list": {
			Purpose:                  "List KB articles in the local knowledge-base replica.",
			UseWhen:                  "When locating durable organisational context by article metadata.",
			DoNotUseWhen:             "Do not use it when a known article ID can be fetched directly.",
			MutatesState:             false,
			Prerequisites:            "A bound project.",
			FreshnessImplications:    localReplica,
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Read a selected article or write a new durable fact.",
			MisuseWarning:            "Do not treat this local inventory as a substitute for a remote freshness check.",
		},
		"wormhole.kb.get": {
			Purpose:                  "Get a named KB article or list articles when no ID is supplied.",
			UseWhen:                  "When reading a known durable procedure, decision, or discovery.",
			DoNotUseWhen:             "Do not use it to retrieve code as an authoritative source.",
			MutatesState:             false,
			Prerequisites:            "A bound project; supply an article ID for a specific record.",
			FreshnessImplications:    localReplica,
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Verify referenced Git pointers and update stale durable knowledge with kb.write.",
			MisuseWarning:            "Git remains code truth; do not rely on KB prose instead of current source verification.",
		},
		"wormhole.kb.write": {
			Purpose:                  "Write a KB article locally and enqueue it for synchronization.",
			UseWhen:                  "When preserving a durable fact, decision, discovery, or procedure.",
			DoNotUseWhen:             "Do not use it for transient status chatter or source-file copies.",
			MutatesState:             true,
			Prerequisites:            "A bound project and kb.write permission.",
			FreshnessImplications:    "The article is durable locally before synchronization makes it shared.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Post a typed event or link the article from the relevant task.",
			MisuseWarning:            "Do not store credentials or present Git-derived prose as authoritative code.",
		},
		"wormhole.kb.search": {
			Purpose:                  "Search the shared Fabric knowledge base with generation-scoped semantic ranking.",
			UseWhen:                  "When organisational decisions, procedures, or durable discoveries could answer the question before broad repository reconstruction.",
			DoNotUseWhen:             "Do not use it as source-code authority or silently substitute lexical/local search when semantic ranking is unavailable.",
			MutatesState:             false,
			Prerequisites:            "An online project-bound Fabric connection and kb.search permission.",
			FreshnessImplications:    "Results come from Fabric's active semantic generation; provider or index degradation returns a structured error with fallback=none.",
			SourceAccessImplications: noSource,
			RecommendedFollowUp:      "Read relevant durable context, then verify any code claim against Git and current source.",
			MisuseWarning:            "Never reinterpret a semantic degradation error as a successful empty result or permission to fall back silently.",
		},

		// Git pointers.
		"wormhole.git.link_commit": {
			Purpose:                  "Record a metadata-only task-to-commit pointer locally and enqueue it for synchronization.",
			UseWhen:                  "When a verified commit materially advances or completes a tracked task and reviewers need the exact Git reference.",
			DoNotUseWhen:             "Do not use it before the commit exists, for a pull-request review request, or to copy source into Wormhole.",
			MutatesState:             true,
			Prerequisites:            "A bound project, existing task, repository identifier, exact commit SHA, concise summary, and git.link_commit permission.",
			FreshnessImplications:    "The pointer is durable locally first and becomes visible to other Gateways after synchronization.",
			SourceAccessImplications: "This tool stores only a Git pointer and summary; it never reads, mirrors, or proves repository source.",
			RecommendedFollowUp:      "Verify the commit directly with Git, check sync.status, and include the pointer in the reviewer handoff.",
			MisuseWarning:            "A stored pointer is not proof that the commit is correct, reachable, reviewed, or remotely synchronized.",
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
