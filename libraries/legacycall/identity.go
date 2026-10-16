package legacycall

import "strconv"

// Source: internal/router/native_apply_patch.go:476:478@[d9f28a74] nativePatchDerivedCallID
func PatchCallID(callID string, index int) string { return callID + ":apply:" + strconv.Itoa(index+1) }

// Source: internal/router/exec_observation.go:1278:1283@[d9f28a74] execDerivedCallID
func ExecCallID(callID string, codeMode bool) string {
	if codeMode {
		return callID + ":effects"
	}
	return callID + ":exec:1"
}
