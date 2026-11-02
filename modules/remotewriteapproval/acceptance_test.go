package remotewriteapproval_test

import (
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
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
	// Detailed VCS and alias cases remain in remote's policy tests.
	if !remote.Writes([]string{"/tools/git", "push", "origin", "main"}, nil) || remote.Writes([]string{"/tools/git", "status"}, nil) {
		t.Fatal("public remote-write policy lost write/read classification")
	}
}
