# Repository Branch Alignment Design

## Goal
Tidy up the repository branches to match the separation of concerns for local runtime Phases P1-P7, removing redundant `-real` and `-finish` suffixes, and ensuring the clean branch names point to the correct implementations.

## Proposed Mapping

| Phase | Branch Name | Target Commit SHA | Phase Target Scope |
|---|---|---|---|
| **P1** | `main` | `92d12f5def236183d14d2b714e8d0ac31e1a0511` | Walking Skeleton (`cmd/wormholed`, config loading, cache SQLite, `/whoami` proxy) |
| **P2** | `p2-local-storage-replica` | `ff68e8125de6b4cdec1f872c5b632aa5ad04fb36` | Local replica storage for Tasks, Events, KB, and cross-namespace tests |
| **P3** | `p3-local-runtime-event-bus-scheduler` | `6aafd2d1654f20e08d3d3198a76c7dfb894ff3b0` | In-memory pub/sub, presence scheduler, and local task routing |
| **P4** | `p4-local-runtime-sync-engine` | `d555919912705153f4d224899f35d1f203be08a7` | Outbound sync queue, bootstrap and incremental sync engines |
| **P5** | `p5-local-runtime-org-bootstrap-multi-org` | `525ecda1db703ee50f9923db261835464b1555c9` | RETARGET join/connect flows, project bindings, multi-org routing |
| **P6** | `p6-local-runtime-coordination-server-hardening` | `525ecda1db703ee50f9923db261835464b1555c9` | Coordination server sync tools, protocol negotiation, and connection safety |
| **P7** | `p7-local-runtime-launch` | `0955b74236cf64892c82c4b6cac1b2624e3e717c` | E2E validation tests and companion architecture updates |

## Branches to Delete

### Local Branches
- `a-` (accidental branch at same head as `main`)

### Remote Branches (`origin`)
- `origin/p3-local-runtime-event-bus-scheduler-finish`
- `origin/p3-local-runtime-event-bus-scheduler-real`
- `origin/p4-local-runtime-sync-engine-real`
- `origin/p5-local-runtime-org-bootstrap-multi-org-real`
- `origin/p6-local-runtime-coordination-server-hardening-real`
- `origin/p7-local-runtime-launch-real`

## Verification
For each branch after setting its reference, run:
`git show --name-only <branch-name>`
Verify that only files relevant to the scope of that phase (and cumulative preceding phases) are present.
