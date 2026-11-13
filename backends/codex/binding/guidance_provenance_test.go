package binding

import (
	"testing"

	"github.com/yusing/mekugi/contracts/guidance"
)

func TestInstructionFragmentMatchesAdmittedIdentityAndContent(t *testing.T) {
	const role = `<multi_agent_role>Use native role policy.</multi_agent_role>`
	f := guidance.Fragment{Thread: "thread", Item: "msg_root_role", Kind: "multi_agent.role_instructions", Text: role}
	if err := MatchInstructionFragment(f, "thread", "msg_root_role", 0, role); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, thread, item, text string
		index                    int
	}{
		{"foreign-thread", "other", f.Item, role, 0},
		{"caller-lookalike", f.Thread, "caller-authored", role, 0},
		{"different-part", f.Thread, f.Item, role, 1},
		{"modified-policy", f.Thread, f.Item, role + " changed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := MatchInstructionFragment(f, tc.thread, tc.item, tc.index, tc.text); err == nil {
				t.Fatal("accepted mismatched provenance")
			}
		})
	}
}
