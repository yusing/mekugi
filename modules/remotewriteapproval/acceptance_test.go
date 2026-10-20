package remotewriteapproval_test

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/approval"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/remotewriteapproval"
	"github.com/yusing/mekugi/modules/remotewriteapproval/remote"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestRemoteWriteApprovalAcceptance(t *testing.T) {
	script := new(moduletest.Scripted)
	moduletest.New(t, remotewriteapproval.Module, remotewriteapproval.Deps{}, module.Slots{}, script)
	guards := script.RegisteredGuards()
	if len(guards) != 1 || guards[0].Kind != binding.RemoteWrite {
		t.Fatalf("registered guards = %+v", guards)
	}
	if len(script.Commands()) != 0 {
		t.Fatal("approval registration exposed an executable frontend")
	}
	command := approval.Command{Directory: "/workspace", Executable: "/tools/git", Arguments: `["push","origin","main"]`}
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
		{Directory: command.Directory, Executable: "/other/git", Arguments: command.Arguments},
		{Directory: command.Directory, Executable: command.Executable, Arguments: `["push","origin main"]`},
		{Directory: command.Directory, Executable: command.Executable, Arguments: `["push","origin","topic"]`},
	} {
		if current.Allows(changed) {
			t.Errorf("session authorized changed identity: %+v", changed)
		}
	}
	// Detailed VCS and alias cases remain in remote's policy tests.
	if !remote.Writes([]string{"/tools/git", "push", "origin", "main"}, nil) || remote.Writes([]string{"/tools/git", "status"}, nil) {
		t.Fatal("public remote-write policy lost write/read classification")
	}
}
