package livepreview_test

import (
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/livepreview"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestLivePreviewAttachmentOwnership(t *testing.T) {
	service := moduletest.New(t, livepreview.Module, livepreview.Deps{}, module.Slots{}, nil)
	var first, other scope.Store
	old := service.Attach(t.Context(), &first)
	old.SetScope(preview.Scope{Workspaces: map[string]map[string]bool{"/work": {"main": true}}})
	old.Enable()
	old.Update(preview.Preview{ID: "edit", Workspace: "/work", Thread: "main", Input: "+source"}, false)
	if service.Attach(t.Context(), &first) != old {
		t.Fatal("one attachment acquired duplicate preview state")
	}
	separate := service.Attach(t.Context(), &other)
	defer separate.Close()
	if separate == old || len(separate.Current()) != 0 {
		t.Fatal("separate attachments shared provisional state")
	}
	old.Close()
	first = scope.Store{}
	replacement := service.Attach(t.Context(), &first)
	defer replacement.Close()
	if old.Context().Err() == nil || replacement == old || len(replacement.Current()) != 0 || replacement.Context().Err() != nil || separate.Context().Err() != nil {
		t.Fatal("retirement did not isolate replacement and unrelated attachments")
	}
	var absent *preview.Service
	if absent.Attach(t.Context(), nil) != nil {
		t.Fatal("omitted preview allocated attachment state")
	}
}
