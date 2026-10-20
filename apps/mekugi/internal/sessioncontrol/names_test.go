package sessioncontrol_test

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

func TestNamesSerializeWithoutRendererOrWireIdentity(t *testing.T) {
	var names sessioncontrol.Names
	names.Manual("", "first")
	names.Manual("", "latest before start")
	if name := names.Attach("main"); name != "latest before start" || names.Pending("") != "" {
		t.Fatal("attachment did not admit the latest name")
	}
	first := names.Next("main")
	if first == nil || first.Name != "latest before start" || names.Next("main") != nil {
		t.Fatal("name save was lost or issued twice")
	}
	names.Manual("main", "newer")
	if names.AllowsGenerated("main") || names.Generated("main", "late candidate") != nil || names.Next("main") != nil {
		t.Fatal("a pending save was bypassed")
	}
	other := names.Generated("other", "independent")
	if other == nil || names.Complete(*first) {
		t.Fatal("independent thread blocked or newer manual name lost")
	}
	latest := names.Next("main")
	if latest == nil || latest.Name != "newer" || !names.Complete(*other) || names.Pending("main") != "newer" {
		t.Fatal("completion crossed thread identity")
	}
	if !names.Complete(*latest) || names.Pending("main") != "" || names.Next("main") != nil {
		t.Fatal("settled save remained pending")
	}
}
