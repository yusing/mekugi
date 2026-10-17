package livepreview_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/livepreview"
	"github.com/yusing/mekugi/modules/livepreview/codexbinding"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/livepreview/projection"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestLivePreviewStreamPublication(t *testing.T) {
	service := moduletest.New(t, livepreview.Module, livepreview.Deps{}, module.Slots{}, nil)
	var attachment scope.Store
	session := service.Attach(t.Context(), &attachment)
	defer session.Close()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(path, []byte("executed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session.SetScope(preview.Scope{Workspaces: map[string]map[string]bool{workspace: {"child": true}}})
	session.SetTurn("child", "turn", true)
	session.Enable()
	sub, _ := session.Subscribe()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := codexbinding.Start(ctx, session, codexbinding.Identity{
		Workspace: workspace, Thread: "child", Turn: "turn", Caller: "/root/child", Kind: "apply_patch",
	}, codexbinding.Timing{Frame: time.Millisecond, Unit: time.Millisecond, Finish: time.Second}, projection.ReadFile)
	defer stream.Stop()
	stream.Prime(path, "before\n", true)
	stream.Append("*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+arriving\n")
	deadline := time.After(5 * time.Second)
	var id string
	for id == "" {
		select {
		case <-sub.Ready():
			for _, event := range session.TakePreviews(sub) {
				if p := event.Preview; p != nil && len(p.Files) == 1 && strings.Contains(p.Files[0].UnifiedDiff(), "+arriving") {
					id = p.ID
				}
			}
		case <-deadline:
			t.Fatal("source did not publish before completion")
		}
	}
	stream.Prime(path, "late baseline\n", true)
	stream.Finish("*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+authoritative\n*** End Patch\n")
	cancel()
	select {
	case <-stream.Done():
	case <-deadline:
		t.Fatal("accepted final input did not finish after cancellation")
	}
	for _, event := range session.TakePreviews(sub) {
		if p := event.Preview; p != nil && p.ID == id && p.Complete && p.Workspace == workspace && p.Thread == "child" && p.Turn == "turn" && p.Caller == "/root/child" && len(p.Files) == 1 {
			diff := p.Files[0].UnifiedDiff()
			if strings.Contains(diff, "-before") && strings.Contains(diff, "+authoritative") && !strings.Contains(diff, "executed") {
				return
			}
		}
	}
	t.Fatal("final publication lost identity, authoritative input or the original baseline")
}

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
