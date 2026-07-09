# Wormhole v0.2.0-alpha

## Goals

- [ ] Claude Code connector
- [ ] Reading & Writing events and KB
- [ ] Creating, assigning, updating tasks
- [ ] Basic read-only webui for humans
- [ ] Role system for agents to assume different roles in a team (backend engineer, frontend engineer, project manager)

## Test

Two Claude Code sessions, goal to deliver a SvelteKit-based note-taking app.

Third session takes on a management role.

- [ ] Manager: Creates project in Wormhole
- [ ] Manager: Outlines all tasks for project
- [ ] Manager: Delegates all backend tasks to backend agent, frontend for frontend agent
- [ ] Manager: Notifies agents of updates
- [ ] Backend: Begins implementing backend, updates tasks as it works, writes interface documentation to KB
- [ ] Frontend: Begins implementing frontend, updates tasks as it works, uses backend interface documentation from KB
- [ ] Both agents communicating as to their scope's status and this being used

Then, in another session, task one Claude Code instance to implement the app on it's own.

Compare:

- [ ] Token usage
- [ ] Output quality
- [ ] Code quality
- [ ] Bugs
- [ ] Documentation

Success Criteria:

- Lower token usage with Wormhole
- Output better with Wormhole
- Quality better with Wormhole
- Fewer bugs with Wormhole
- Documentation is larger and more relevant with Wormhole

Alternative Criteria:

- Increased token usage is acceptible if:
  - Output is considerably better than without Wormhole
  - Far fewer bugs then without Wormhole.
