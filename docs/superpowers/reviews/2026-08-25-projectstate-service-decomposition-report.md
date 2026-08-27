# Projectstate Service Decomposition Report

Scope: the approved coordinator decomposition tranche only. No schema, protocol,
publication, recovery, or portable-state behavior was intentionally changed.

`projectstate.Service` is now a nil-safe public facade over six package-private
coordinators: registration, workspace, publication, checkpoint/recovery, Git-base,
and import/stash/restore transition. The facade has exactly six pointer fields and
does not own repository, filesystem, observer, clock, writer, or gate state.
Checkpoint and recovery retain one shared gate in the checkpoint coordinator.

Measured structural result:

| Measure | Before tranche | After tranche |
| --- | ---: | ---: |
| `service.go` lines | 600 | 393 |
| projectstate production Go LOC | 16,727 | 16,980 |
| projectstate test Go LOC | 31,287 | 31,550 |
| Service coordinator pointer fields | 0 | 6 |
| Service lifecycle dependency fields | 17 | 0 |

The package-level LOC increase reflects concrete coordinator structure and retained
architecture coverage; `service.go` itself shrank by 207 lines. Test fault-injection
seams now address their owning coordinator directly.
The architecture test `TestProjectstateServiceIsCoordinatorFacade` enforces the
field and authority boundary; the earlier per-coordinator architecture tests remain
in place.

Verification run for this final task:

```text
go test ./internal/runtime/localstore ./internal/runtime/projectstate -count=1  PASS (4.3s / 82.6s)
go test -race ./internal/runtime/projectstate -count=1                         PASS (230.8s)
make check                                                                      PASS (84.8% merged coverage)
make release-test                                                               PASS
make release-rehearsal                                                          PASS
gofmt + git diff --check                                                        PASS
```

Independent broad review found no Critical, Important, or Minor findings. Clean-clone
verification is recorded by the orchestrator against the exact final candidate commit.
