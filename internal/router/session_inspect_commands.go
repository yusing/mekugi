package router

import (
	"encoding/json"
	"strings"

	"github.com/yusing/mekugi/capturer"
)

const axCarrierCallIDPrefix = "# mekugi:ax:call_id="

// Read explicit router-owned invocation metadata, not arbitrary environment
// assignments. Historical outer markers remain readable for retained rollouts.
// This is local correlation evidence, not authentication of a supplied rollout.
func inspectionAXCallID(encoded json.RawMessage) string {
	var command string
	if json.Unmarshal(encoded, &command) != nil {
		var argv []string
		if json.Unmarshal(encoded, &argv) != nil || len(argv) != 3 ||
			(shellInterpreterName(argv[0]) != "bash" && shellInterpreterName(argv[0]) != "sh") ||
			(argv[1] != "-c" && argv[1] != "-lc") {
			return ""
		}
		command = argv[2]
	}
	metadata, body, newline := strings.Cut(command, "\n")
	identity, marked := strings.CutPrefix(metadata, axCarrierCallIDPrefix)
	if marked {
		if !newline || !capturer.ValidAXIdentity(identity) {
			return ""
		}
		command = body
	}
	if marked {
		return identity
	}
	return ""
}
