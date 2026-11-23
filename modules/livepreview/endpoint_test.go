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
	if !endpoint.Advance(t.Context(), false) || reads != 1 {
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

func TestRunningPreviewRepeatedWritesWaitForSettlement(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.go")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("package p; var A=1\n")
	observation, ok := capture.Capture(t.Context(), []commandanalysis.Command{{
		Command: "tee source.go <<'EOF'\npackage p; var B=2\nEOF\nprintf '// observed suffix\\n' >> source.go", Workdir: workspace, Shell: "sh",
	}}, false, true, capture.Options{Directory: workspace, PreviewOnly: true})
	if !ok || observation == nil || !observation.RepeatedPaths {
		t.Fatal("fixture must capture repeated writes to one path")
	}
	service := moduletest.New(t, livepreview.Module, livepreview.Deps{ReadEndpoint: capture.ReadFile}, module.Slots{}, nil)
	var attachment scope.Store
	session := service.Attach(t.Context(), &attachment)
	defer session.Close()
	session.SetScope(preview.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	endpoint := session.ObserveEndpoints(t.Context(), preview.SelectEndpoints(t.Context(), *observation), preview.Preview{ID: "edit", Workspace: workspace, Thread: "thread"})
	defer endpoint.Close()
	for _, text := range []string{"package p; var B=2\n", "package p; var B=2\n// observed suffix\n"} {
		write(text)
		if endpoint.Advance(t.Context(), false) {
			t.Fatal("intermediate write completed the preview before native settlement")
		}
		if current := session.Current(); len(current) != 1 || current[0].Complete || len(current[0].Files) != 1 {
			t.Fatalf("unfinished writer lost its live preview: %+v", current)
		}
	}
	if !endpoint.Advance(t.Context(), true) || !endpoint.View().Complete || len(session.Current()) != 0 || !strings.Contains(endpoint.View().Files[0].Diff, "+// observed suffix") {
		t.Fatalf("settled writer lost its final content: %+v", endpoint.View())
	}
}
