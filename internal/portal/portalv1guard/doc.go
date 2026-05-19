// Package portalv1guard contains build-time invariants enforced over the
// portal.proto IDL. The tests here are the contract the rest of the
// portal Connect-RPC surface relies on; if any of them fail, the proto
// has drifted from the Phase 9b design rules and must be reverted before
// implementation work continues.
package portalv1guard
