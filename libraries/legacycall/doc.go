// Package legacycall reads the frozen version-1 and version-2 mixed call format.
// It validates the whole envelope and expands retained snapshot dependencies,
// even when a consumer needs only one feature's fields. It neither publishes
// records nor grants authority to execute or read live workspace paths.
//
// The schema preserves historical JSON semantics, including numeric byte arrays,
// case-insensitive names and legacy Applied/Sweep fields. New feature formats
// belong to their feature owners and must not extend this compatibility schema.
package legacycall
