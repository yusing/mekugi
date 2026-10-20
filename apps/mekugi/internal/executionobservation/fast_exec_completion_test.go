package executionobservation

import (
	"bufio"

	"context"

	"io"

	"os/exec"

	"path/filepath"

	"strings"

	"testing"

	"time"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestExecEditCompletionFastPythonWriteWhileTestRuns(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python is required for this shell execution fixture")
	}
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "source.go"), strings.Repeat("context\n", 20)+"before\n"+strings.Repeat("context\n", 20))
	// The second process is an actual read-only test. It signals that the
	// edit landed, then waits for us to inspect the UI before it can exit.
	command := "python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('before', 'after')\nopen(p,'w').write(s)\nPY\n" +
		`python3 -c 'import sys; assert open("source.go").read() == "context\n"*20 + "after\n" + "context\n"*20; print("test running", flush=True); assert sys.stdin.readline().strip() == "continue"'`
	observation, ok := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, changecapture.Options{Directory: workspace})
	if !ok || observation == nil {
		t.Fatal("missing edit observation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "env", "-u", "BASH_ENV", "bash", "-c", command)
	cmd.Dir = workspace
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cancel()
			_ = cmd.Wait()
		}
	}()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "test running\n" {
		t.Fatalf("test did not start after Python edit: %q, %v", line, err)
	}
	broker := newLivePreview(ctx)
	setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
	sub := testReview(broker).Subscribe()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runExecScopePreview(ctx, broker, *observation, previewmodel.Preview{ID: "fast-edit", Workspace: workspace, Thread: "thread", Caller: "/root"}, nil, nil)
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("edit display waited for the blocked test to finish")
	}
	completed := false
	for _, event := range testReview(broker).TakePreviews(sub) {
		if event.Preview != nil {
			completed = completed || event.Preview.Complete
		}
	}
	if !completed {
		t.Fatal("fast observed edit waited for the test to exit")
	}

	if _, err := io.WriteString(input, "continue\n"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("read-only test failed: %v", err)
	}
}
