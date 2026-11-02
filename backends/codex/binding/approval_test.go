package binding

import (
	"testing"
)

func TestShellApprovalRegistry(t *testing.T) {
	var registry Registry
	if len(registry.RegisteredGuards()) != 0 {
		t.Fatal("empty registry enabled a guard")
	}
	for _, invalid := range []ShellApproval{
		{Kind: "unknown"},
	} {
		if err := registry.ShellApproval(invalid); err == nil {
			t.Fatalf("accepted invalid registration: %+v", invalid)
		}
	}
	if len(registry.RegisteredGuards()) != 0 {
		t.Fatal("rejected registrations changed the registry")
	}
	for _, kind := range []ShellGuardKind{RemoteWrite, Sudo} {
		guard := ShellApproval{Kind: kind}
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
	if got := registry.RegisteredGuards(); got[0].Kind != RemoteWrite {
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
