package executionobservation

import (
	"context"

	"os"

	"path/filepath"

	"strings"

	"testing"

	"time"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"

	"github.com/yusing/mekugi/modules/commandsegments/segments"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func execEditThenTestObservation(t *testing.T, workspace string) (changecapture.Observation, string) {
	t.Helper()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "before\n")
	command := "python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('before', 'after')\nopen(p,'w').write(s)\nPY\nenv -u BASH_ENV go test ./..."
	observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, true, changecapture.Options{Directory: workspace})
	if !ok || observation == nil || len(observation.Files) != 1 {
		t.Fatalf("edit and test target capture = %+v, observed=%v", observation, ok)
	}
	return *observation, path
}

func TestExecEditCompletionUsesCapturedBaseline(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	observation, path := execEditThenTestObservation(t, workspace)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := previewmodel.Expected(t.Context(), observation)
	if len(expected) != 1 || expected[path].Diff == "" || !strings.Contains(expected[path].Diff, "-before") || !strings.Contains(expected[path].Diff, "+after") {
		t.Fatalf("expected edit must compare against pre-call capture, not current disk: %+v", expected)
	}
	for name, mutate := range map[string]func(*changecapture.Observation){
		"uncaptured": func(o *changecapture.Observation) { o.Files = nil },
		"binary":     func(o *changecapture.Observation) { o.Files[0].Kind = changecapture.Binary },
		"incomplete": func(o *changecapture.Observation) { o.Files[0].Error = "capture incomplete" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := observation
			copy.Files = append([]changecapture.File(nil), observation.Files...)
			mutate(&copy)
			if got := previewmodel.Expected(t.Context(), copy); len(got) != 0 {
				t.Fatalf("unsafe source yielded predicted edit: %+v", got)
			}
		})
	}
	testOnly := changecapture.Observation{Commands: []commandanalysis.Command{{Command: "env -u BASH_ENV go test ./...", Workdir: workspace, Shell: "bash"}}, Class: execscope.Opaque.String()}
	if got := previewmodel.Expected(t.Context(), testOnly); len(got) != 0 {
		t.Fatalf("test-only command yielded predicted edit: %+v", got)
	}
}

func TestExecEditCompletionBeforeFollowingTestFinishes(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	observation, path := execEditThenTestObservation(t, workspace)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	broker := newLivePreview(ctx)
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	hub := newExecTrackFixture(t, ctx, segments.Instrumentation{})
	registry := &Windows{Reports: hub.Observer}
	registry.open(&execWindow{ref: "edit-then-test", roots: []string{workspace}, thread: "thread", turn: "turn"})
	registry.preview("edit-then-test", observation, broker, workspace, "thread", "/root", nil)
	report := hub.begin(t, [3]string{"thread", "turn", "call"}, observation.Commands[0].Command)
	report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: 0})
	hub.await(t, [3]string{"thread", "turn", "call"}, func(view segments.Report) bool { return view.LastStarted() == 0 })
	registry.mu.Lock()
	state := registry.find("edit-then-test").tracking.state()
	registry.mu.Unlock()
	if !state.tracked || state.settled {
		t.Fatalf("fixture must retain an unfinished writer report: %+v", state)
	}
	if err := os.WriteFile(path, []byte("aft"), 0o600); err != nil {
		t.Fatal(err)
	}
	partial := waitExecScopePreview(t, broker, func(preview previewmodel.Preview) bool {
		return preview.ID == "running:edit-then-test" && len(preview.Files) == 1
	})
	if partial.Complete || partial.Tool != "python3" {
		t.Fatal("partial Python write completed the edit card")
	}
	// Consume the partial frame, as the native viewer does while the process runs.
	testReview(broker).TakePreviews(sub)
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var completed previewmodel.Preview
	deadline := time.After(5 * time.Second)
	for completed.ID == "" {
		select {
		case <-sub.Ready():
			for _, event := range testReview(broker).TakePreviews(sub) {
				if event.Preview != nil && event.Preview.ID == "running:edit-then-test" && event.Preview.Complete {
					completed = *event.Preview
				}
			}
		case <-deadline:
			t.Fatal("completion was replaced by a removal marker before the native viewer could render it")
		}
	}
	if completed.Tool != "python3" || completed.Status != previewmodel.PreviewRunning || completed.Footer != "observed edit" || len(completed.Files) != 1 {
		t.Fatalf("edit completion lost the observed content: %+v", completed)
	}
	registry.mu.Lock()
	window := registry.find("edit-then-test")
	open := window != nil && window.closed.IsZero()
	registry.mu.Unlock()
	if !open {
		t.Fatal("display completion closed the host observation window before test outcome")
	}
	// There is no completed change event until the host command finishes.
	for _, event := range testReview(broker).TakePreviews(sub) {
		if event.Kind == "change" {
			t.Fatalf("edit preview persisted change evidence before host result: %+v", event)
		}
	}
}
