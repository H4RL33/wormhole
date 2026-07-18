# Wormhole Agents and Custom Skills

**System Prompt:** Use `CLAUDE.md` as the system prompt for any agent.

**Custom Agents & Skills:** Wormhole contains local custom agent workflows stored in `.agents/`:

- `.agents/skills/` — custom skill definitions. Read `SKILL.md` in each directory before invoking.
- `.agents/agents/` — custom subagent implementations and their capabilities.
- `.agents/plugins/` — custom plugins for the agent runtime.

Before starting feature work, planning, or executing tasks, explore `.agents/` to find available custom workflows.

For implementation tasks, see `docs/implementation-rules.md` for guardrails and dispatch heuristic.
