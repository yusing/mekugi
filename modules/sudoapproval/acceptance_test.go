package sudoapproval_test

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/approval"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sudoapproval"
	"github.com/yusing/mekugi/modules/sudoapproval/sudo"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestSudoApprovalAcceptance(t *testing.T) {
	script := new(moduletest.Scripted)
	moduletest.New(t, sudoapproval.Module, sudoapproval.Deps{}, module.Slots{}, script)
	guards := script.RegisteredGuards()
	if len(guards) != 1 || guards[0].Kind != binding.Sudo {
		t.Fatalf("registered guards = %+v", guards)
	}
	if len(script.Commands()) != 0 {
		t.Fatal("approval registration exposed an executable frontend")
	}
	command := approval.Command{Directory: "/workspace", Executable: "/tools/sudo", Arguments: `["printf","a b"]`}
	current, other := guards[0].NewSession(), guards[0].NewSession()
	if current.Allows(command) || other.Allows(command) {
		t.Fatal("new UI session already grants a command")
	}
	current.Allow(command)
	if !current.Allows(command) || other.Allows(command) || guards[0].NewSession().Allows(command) {
		t.Fatal("exact grant was lost or escaped its UI session")
	}
	for _, changed := range []approval.Command{
		{Directory: "/other", Executable: command.Executable, Arguments: command.Arguments},
		{Directory: command.Directory, Executable: "/other/sudo", Arguments: command.Arguments},
		{Directory: command.Directory, Executable: command.Executable, Arguments: `["printf","a","b"]`},
	} {
		if current.Allows(changed) {
			t.Errorf("session authorized changed identity: %+v", changed)
		}
	}
}

func TestSudoApprovalPasswordFraming(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		valid        bool
	}{
		{"empty", "", true},
		{"spaces and unicode", " p a s s 密碼 ", true},
		{"newline", "first\nsecond", false},
		{"carriage return", "first\rsecond", false},
		{"CRLF", "answer\r\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sudo.ValidPassword(tc.answer); got != tc.valid {
				t.Fatalf("password framing valid = %v, want %v", got, tc.valid)
			}
		})
	}
}
