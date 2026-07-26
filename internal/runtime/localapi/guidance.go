package localapi

import "sort"

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
			Purpose:                  "Subscribe this MCP connection to matching event notifications.",
			UseWhen:                  "When subsequent local events are needed during the active session.",
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
