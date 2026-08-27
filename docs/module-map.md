# Module Map

## `internal/runtime/localidentity`

Owner-only local human identity persistence: the selected human, private Ed25519
key, setup-intent receipts, and bounded public human profiles. It imports only
`internal/types` and the standard library (plus the existing platform syscall
adapter) and never imports `localapi`; only Gateway wiring may construct public
actor envelopes from this store.
