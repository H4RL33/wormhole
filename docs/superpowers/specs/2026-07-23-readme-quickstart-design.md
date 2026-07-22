# README Quickstart and Usage Design

## Goal

Make the repository README give a new Linux or WSL user one short, correct
path from a fresh checkout to a running Coordination Server, local
`wormholed` daemon, and connected coding harness.

## Scope

Keep the existing project philosophy, status, architecture, dashboard, design
documents, stack, and license sections. Replace the current Quickstart / Local
Demo section with an operational guide that matches the current binaries and
CLI flags, and update documentation links that target the renamed heading.

The guide will:

- state that `wormholed` is supported on Linux only and that Windows users run
  the entire local-runtime and connector workflow inside WSL;
- build all three binaries with `make build` and use `./dist/...` consistently;
- start Postgres, install and run `golang-migrate`, create a demo project, and
  start `wormhole-server`;
- recommend `wormhole connect` for credential creation plus harness wiring,
  with `wormhole join` documented as the credential-only alternative;
- start the daemon with the exact credential profile name:
  `wormholed <profile>`;
- explain that harnesses connect through `wormhole mcp` to the daemon Unix
  socket rather than directly to the Coordination Server;
- provide concise verification, common-command, file-location, configuration
  precedence, and troubleshooting references.

## Publication

Commit the README work separately from the existing QA commits. Push the
`qa-simplification` branch to `origin`, open a pull request into the remote
default branch, and merge it after required checks permit. Exclude the existing
unrelated scratch changes in `.superpowers/sdd/progress.md`,
`.superpowers/sdd/task-4-brief.md`, and the untracked Task 5 follow-up plan.

## Verification

Validate every displayed CLI command against the current command help and run
Markdown whitespace checks. Re-run the project documentation-proportional
quality gates, then confirm the pull request merged and remote `main` contains
the README commit.
