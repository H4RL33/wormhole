package mcp

// Tool is a placeholder MCP tool descriptor. Real request/response schemas
// land per RFC-0001 §9 as each pillar's tools are implemented.
type Tool struct {
	Name        string
	Description string
}

// Registry holds the set of MCP tools this server exposes. Empty at boot
// per Day 1 scope — tools register themselves as each pillar lands.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func (r *Registry) Register(t Tool) {
	r.tools[t.Name] = t
}

func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
