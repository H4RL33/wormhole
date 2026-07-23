# GitHub enforcement

This record captures the repository controls audited on 2026-07-23 after
production-readiness PR
[#38](https://github.com/H4RL33/wormhole/pull/38) merged as
`1219ced2d5d5040e3ee3f870c2a3fc22c5537a78`.

## Main branch

Ruleset `main production readiness` (`19640066`) is active for
`refs/heads/main`. It:

- requires pull requests while allowing zero approving reviews for the solo
  maintainer;
- requires review-thread resolution;
- requires the branch to be current and all eleven checks to pass:
  `Contract Inventory`, `Static`, `Build`, `Integration`, `Race`, `Coverage`,
  `Migrations`, `Vulnerability`, `Secret Scan`, `Dependency Review`, and
  `Action Pins`;
- blocks deletion and non-fast-forward updates; and
- grants the repository owner an emergency bypass. Every use of that bypass
  requires a follow-up issue recording reason, impact, verification debt, and
  corrective action.

The merge commit was verified on `main` by
[CI](https://github.com/H4RL33/wormhole/actions/runs/30040683823),
[Migrations](https://github.com/H4RL33/wormhole/actions/runs/30040683754),
and [Security](https://github.com/H4RL33/wormhole/actions/runs/30040683818).

## Release environment

The `release` environment:

- requires approval by repository owner `H4RL33`;
- permits self-review because the project currently has one maintainer; and
- accepts deployments only from protected branches.

Repository variable `WORMHOLE_RELEASE_ENABLED=true` activates the guarded
annotated-tag publication path. This does not create a tag or release. The
workflow still requires the exact release tag, full tag-commit quality suites,
the protected environment approval, immutable tag checks, verified signatures,
and verified provenance before publishing.

## Security controls

The audited repository settings enable:

- dependency graph and Dependabot security updates;
- secret scanning and push protection; and
- full-length commit SHA pinning for GitHub Actions.

GitHub left secret-scanning validity checks and non-provider patterns disabled
for this user-owned public repository after update requests. These unavailable
settings are not represented as enforced controls. The repository-owned
Gitleaks check remains required in addition to GitHub secret scanning.

## Audit procedure

Read back these controls after changing workflows, check names, maintainers, or
repository ownership:

```sh
gh api repos/H4RL33/wormhole/rulesets/19640066
gh api repos/H4RL33/wormhole/environments/release
gh api repos/H4RL33/wormhole/actions/permissions
gh api repos/H4RL33/wormhole/actions/variables/WORMHOLE_RELEASE_ENABLED
gh api repos/H4RL33/wormhole --jq '.security_and_analysis'
```

Do not create `v0.3.0-beta.1` until the separately approved beta-readiness
decision. The current contract policy remains `alpha-inventory`.
