package binding

import (
	"testing"

	"github.com/yusing/mekugi/contracts/approval"
)

type approvalTestGrants map[approval.Command]bool

func (g approvalTestGrants) Allows(command approval.Command) bool { return g[command] }
func (g approvalTestGrants) Allow(command approval.Command)       { g[command] = true }

func TestShellApprovalRegistry(t *testing.T) {
	var registry Registry
	if len(registry.RegisteredGuards()) != 0 {
		t.Fatal("empty registry enabled a guard")
	}
	factory := func() CommandGrants { return make(approvalTestGrants) }
	for _, invalid := range []ShellApproval{
		{Kind: "unknown", NewSession: factory},
		{Kind: RemoteWrite},
		{Kind: Sudo},
	} {
		if err := registry.ShellApproval(invalid); err == nil {
			t.Fatalf("accepted invalid registration: %+v", invalid)
		}
	}
	if len(registry.RegisteredGuards()) != 0 {
		t.Fatal("rejected registrations changed the registry")
	}
	for _, kind := range []ShellGuardKind{RemoteWrite, Sudo} {
		guard := ShellApproval{Kind: kind, NewSession: factory}
		if err := registry.ShellApproval(guard); err != nil {
			t.Fatal(err)
		}
		if err := registry.ShellApproval(guard); err == nil {
			t.Fatalf("accepted duplicate guard %q", kind)
		}
	}
	guards := registry.RegisteredGuards()
	if len(guards) != 2 || guards[0].Kind != RemoteWrite || guards[1].Kind != Sudo {
		t.Fatalf("registered guards = %+v", guards)
	}
	guards[0] = ShellApproval{Kind: "changed"}
	if got := registry.RegisteredGuards(); got[0].Kind != RemoteWrite || got[0].NewSession == nil {
		t.Fatal("caller mutated retained registrations")
	}
}

func TestShellApprovalRequestKinds(t *testing.T) {
	for _, tc := range []struct {
		request string
		kind    ShellGuardKind
		valid   bool
	}{
		{"", RemoteWrite, true},
		{"sudo", Sudo, true},
		{"sudo-password", Sudo, true},
		{"unknown", "", false},
	} {
		if kind, valid := GuardForRequest(tc.request); kind != tc.kind || valid != tc.valid {
			t.Errorf("request %q = (%q, %v), want (%q, %v)", tc.request, kind, valid, tc.kind, tc.valid)
		}
	}
}
