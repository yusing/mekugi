// Package legacycall reads the frozen version-1 and version-2 mixed call format.
// It validates the whole envelope and expands retained snapshot dependencies,
// even when a consumer needs only one feature's fields. It neither publishes
// records nor grants authority to execute or read live workspace paths. Opened
// envelopes and snapshot dependencies must be regular files within their bounds.
// Atomic replacement may supply either complete published version.
//
// The schema preserves historical JSON semantics, including numeric byte arrays,
// case-insensitive names and legacy Applied/Sweep fields. Retired journal
// bookkeeping, commentary replacement and review CreatedPath metadata is
// accepted with its historical types and discarded without restoring journal
// behavior or read authority.
// New feature formats belong to their owners, not this compatibility schema.
package legacycall
