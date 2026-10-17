package livepreview_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/livepreview"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestRunningPreviewUsesComposedReaderAndCapturedScope(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, ok := capture.Capture(t.Context(), []commandanalysis.Command{{
		Command: "cat > source.txt <<'EOF'\nafter\nEOF\nsleep 60", Workdir: workspace, Shell: "bash",
	}}, false, false, capture.Options{Directory: workspace})
	if !ok || observation == nil {
		t.Fatal("edit did not produce captured scope")
	}
	reads := 0
	service := moduletest.New(t, livepreview.Module, livepreview.Deps{ReadEndpoint: func(target string, budget *int) capture.File {
		reads++
		if target != path {
			t.Fatalf("endpoint read escaped captured scope: %s", target)
		}
		return capture.ReadFile(target, budget)
	}}, module.Slots{}, nil)
	var attachment scope.Store
	session := service.Attach(t.Context(), &attachment)
	defer session.Close()
	session.SetScope(preview.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub, _ := session.Subscribe()
	selected := preview.SelectEndpoints(t.Context(), *observation)
	endpoint := session.ObserveEndpoints(t.Context(), selected, preview.Preview{ID: "edit", Workspace: workspace, Thread: "thread"})
	defer endpoint.Close()
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !endpoint.Advance(t.Context(), false, false) || reads != 1 {
		t.Fatalf("source edit did not settle independently of the following command: reads=%d", reads)
	}
	for _, event := range session.TakePreviews(sub) {
		if p := event.Preview; p != nil && p.ID == "edit" && p.Complete && len(p.Files) == 1 && strings.Contains(p.Files[0].UnifiedDiff(), "-before\n+after") {
			if len(observation.Files) != 1 || observation.Files[0].Content != "before\n" {
				t.Fatal("preview changed the original capture")
			}
			return
		}
	}
	t.Fatal("settled source edit was not published")
}
