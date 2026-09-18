package router

import (
	"strings"
	"testing"
)

func TestRecoveryCommandValues(t *testing.T) {
	for _, command := range []string{
		"type \"old\" \"bad\"\n", "add EOF \"bad\"\n", "type \"bad\"\n",
		"type 1:abcd <<END\nbad\nEND\n",
	} {
		baseline := "in f.txt\n" + command + "new untouched.txt\ntype \"keep\"\n"
		handle := recoveryCommands(baseline, testRecoveryHandles(baseline))[1].handle
		for _, value := range []string{`"good\n"`, "<<END\ngood\nEND", "<<'END' \ngood\nEND", "<<-END\t\n\tgood\n\tEND"} {
			result, err := recoverScriptDetailed(t.Context(), baseline, handle+" value "+value, testRecoveryHandles(baseline))
			if err != nil {
				t.Fatal(err)
			}
			parts := recoveryCommands(result.script, testRecoveryHandles(result.script))
			if parts[1].parts.value != "good\n" || parts[1].parts.target != recoveryCommands(baseline, testRecoveryHandles(baseline))[1].parts.target ||
				!strings.HasSuffix(result.script, "new untouched.txt\ntype \"keep\"\n") {
				t.Fatalf("unexpected reconstructed script: %q", result.script)
			}
		}
		for _, payload := range []string{
			handle + ` value "bad"` + "\n" + handle + ` target "different"`,
			handle + " value <<PATCH\nunterminated",
			handle + ` value "good" trailing`,
			handle + " value <<END\nbody\nWRONG",
		} {
			if _, err := recoverScriptDetailed(t.Context(), baseline, payload, testRecoveryHandles(baseline)); err == nil {
				t.Fatalf("accepted invalid correction %q", payload)
			}
		}
	}
}
