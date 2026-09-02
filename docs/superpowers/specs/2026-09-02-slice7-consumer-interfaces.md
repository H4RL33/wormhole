# Slice 7 Consumer Interface Note

**Status:** frozen for Slice 7 written-spec review; implementation remains pending
approval and independent re-review.

**Base:** `0735306a3dacd02a0e197ab56cbfeb90728c7397`

This note is the consumer-facing handoff for the parallel private-identity and Code
Graph branches. It publishes the exact seams without transferring Slice 7 ownership or
authorising either parallel implementation in this branch.

## Private identity

Use the existing route source. Slice 7 does not add another attachment aggregate or
resolver:

```go
package sync

type FabricRouteSource interface {
	GetRoute(context.Context, types.WorkspaceScope) (types.FabricBinding, types.FabricProfile, error)
}
```

`GetRoute` returns owned value copies from the exact active local route. The values map
the complete attach result as follows:

| Required identity | Existing value |
|---|---|
| Fabric instance identity | `types.FabricBinding.FabricInstanceID` (and matching `FabricProfile.FabricInstanceID`) |
| Remote project identity | `types.FabricBinding.RemoteProjectID` |
| Stream identity | `types.FabricBinding.StreamID` |
| Workspace identity | `types.FabricBinding.Workspace.Scope.WorkspaceID` |
| Local project identity | `types.FabricBinding.Workspace.Scope.ProjectID` |
| Credential reference | `types.FabricProfile.CredentialRef` |

`AttachmentRef`, `CanonicalRef`, and the complete
`WorkspaceBinding.Repository` are also part of the exact route match. The credential
field is a logical reference only; it must never contain a token, private key, seed,
proof preimage, or other credential bytes. The server-derived `SyncAttachV2Result`
supplies the remote project/stream/attachment and source-version evidence; the local
route is persisted only after the result exactly matches the locally selected
workspace/repository/Fabric route. Consumers must treat the returned pair as immutable
for the attempt and must not expose or mutate a route writer.

The attach boundary guarantees:

1. exact workspace, repository identity, Fabric instance, remote project, stream,
   attachment, and canonical-ref matching;
2. route selection only from the exact persisted `WorkspaceScope` and active route,
   never from public arguments, display handles, URL similarity, environment, or a
   last-used profile;
3. repository/fork mismatch before credential lookup, DNS, HTTP-client construction,
   Git-provider observation, or identity-provider activity;
4. explicit human CLI action for every rebind; no automatic or consumer-requested
   rebinding;
5. failure isolation, so one Fabric route's outage does not block local work or any
   other route; and
6. no consumer capability to mutate Activity queues, cursors, conflicts, receipts,
   policy state, or binding identity.

The private-identity branch may consume this read-only boundary and the credential
reference. It must not infer a route, resolve a display handle, inspect a URL to choose
a Fabric, call an identity provider before exact local route validation, or create a
new migration/interface for this information. Private OIDC, issuer discovery, browser
callbacks, refresh, private bearer handling, and private-session implementation remain
outside Slice 7.

## Migration allocation

Slice 7 has no Fabric PostgreSQL migration. `000022` is owned by sync-v2/public
authentication and remains unchanged. The frozen allocation is:

- final Fabric migration after Slice 7: `000022`;
- first available migration for private identity: `000023`.

This allocation is design/ledger state, not a production constant. The private-identity
branch must not create or rename migrations until its migration is reviewed against this
allocation.

## Code Graph

Code Graph receives only an already validated binding through this minimal additive
local API seam:

```go
package localapi

type CodeGraphProvider interface {
	Status(context.Context, types.WorkspaceBinding) (codegraphmanager.Status, error)
	Query(context.Context, types.WorkspaceBinding, codegraphquery.Request) (codegraphquery.Result, error)
	Rebuild(context.Context, types.WorkspaceBinding) (codegraphmanager.Status, error)
}
```

The Code Graph branch owns `codegraphmanager.Status` and the manager package, plus the
provider implementation and worker construction. Query inputs and outputs reuse the
existing `codegraphquery.Request` and `codegraphquery.Result` types. `Status` reports local availability/freshness and
active revision; `Query` is the existing bounded lexical/structural query with
freshness-gated source behavior; `Rebuild` is an explicit local rebuild and returns the
resulting status/revision. All methods revalidate the binding and fail closed for an
invalid, cross-workspace, or stale binding.

The provider is completely outside Fabric routing, Activity, identity, and sync. It has
no credential, remote-profile, cursor, conflict, receipt, or ambient-workspace input;
it uses the binding to select the local checkout only. It must remain local,
deterministic, model-free, and network-free under the existing Code Graph package
dependency rules.

`codegraphquery.Request.SourceAuthorized` is never accepted from public or client JSON.
The authenticated local API derives `code_graph.source.read`, overwrites or constructs
that field at the provider boundary, and passes only the trusted decision. Without that
permission, query retains metadata-only degradation; a caller-controlled Boolean can
never enable source reads.

The Code Graph branch owns any changes required to:

- `internal/runtime/localapi/providers.go`;
- `internal/runtime/localapi/supervisor.go`;
- `internal/runtime/localapi/mcp.go`;
- `cmd/gatewayd/gatewayd.go`; and
- `docs/contracts/alpha-contract.json`.

Slice 7 does not edit those collision points, implement Code Graph, or copy parallel
branch work into this range. Its exact captured diff must remain based on
`0735306a3dacd02a0e197ab56cbfeb90728c7397` and contain no Code Graph or private-OIDC
production changes.
