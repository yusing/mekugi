// Package shellinstrumentation generates guard and segment-tracking shell
// source and recognizes its exact generated forms. Callers retain execution,
// approval policy, invocation-path authentication and report transport.
package shellinstrumentation

// VCSDirectory and SudoDirectory name invocation-private command guard paths.
const (
	VCSDirectory  = "vcs-guard"
	SudoDirectory = "sudo-guard"
)

// ItemEnvironment carries the exact host command identity to a PATH guard.
const ItemEnvironment = "MEKUGI_VCS_ITEM"

// ItemFlag carries that identity through wrappers that clear the environment.
const ItemFlag = "--vcs-item"
