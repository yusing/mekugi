package executionobservation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/livepreview/projection"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func waitExecScopePreview(t *testing.T, broker *previewmodel.Session, match func(previewmodel.Preview) bool) previewmodel.Preview {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, preview := range broker.Current() {
			if match(preview) {
				return preview
			}
		}
		select {
		case <-timer.C:
			t.Fatal("timed out waiting for exec scope preview")
		case <-ticker.C:
		}
	}
}

func waitExecScopePreviewGone(t *testing.T, broker *previewmodel.Session, id string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, exists := currentLivePreview(broker, id)
		if !exists {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf("exec preview %q remained after its window closed", id)
		case <-ticker.C:
		}
	}
}

func TestExecScopePreviewFooterDeduplicatesBoundedTargets(t *testing.T) {
	t.Parallel()
	observation := changecapture.Observation{
		Class:  execscope.Scoped.String(),
		Reason: "additional VCS targets were unresolved",
		Files: []changecapture.File{
			{Path: "/scope/tracked.txt"},
			{Path: "/scope/tracked.txt"},
		},
		Omitted: []changecapture.Omission{{Path: "/scope/also.txt", Reason: "fixture capture bound"}},
	}
	if got, want := previewmodel.CaptureFooter(observation), "may write · 2 scoped paths · tracked.txt, also.txt · other writes unknown · bounded scope"; got != want {
		t.Fatalf("bounded VCS scope footer = %q, want %q", got, want)
	}
}

func captureRunningExecScope(t *testing.T) (string, string, *changecapture.Observation) {
	t.Helper()
	repo := t.TempDir()
	tracked := filepath.Join(repo, "tracked.txt")
	writeTestFile(t, tracked, "before bytes\n")
	observation, observed := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: "cat > tracked.txt <<'EOF'\nafter\nEOF", Workdir: repo, Shell: "bash"}}, false, false, changecapture.Options{Directory: repo})
	if !observed || observation == nil {
		t.Fatal("ordinary write scope missing")
	}
	return repo, tracked, observation
}

func TestLiveDiffStreamableAdmissionPreservesCapture(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"cp source.go copied.go", "mv source.go moved.go", "rm source.go",
		"gofmt -w source.go", "printf changed > source.go",
	} {
		t.Run(command, func(t *testing.T) {
			workspace := t.TempDir()
			writeTestFile(t, filepath.Join(workspace, "source.go"), "package p; var A=1\n")
			for _, mixed := range []bool{false, true} {
				commands := []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}
				if mixed {
					commands = append(commands, commandanalysis.Command{Command: "cat > authored.go <<'EOF'\npackage authored\nEOF", Workdir: workspace, Shell: "bash"})
				}
				observation, ok := changecapture.Capture(t.Context(), commands, false, false, changecapture.Options{Directory: workspace})
				if !ok || observation == nil || len(observation.Files) == 0 {
					t.Fatal("ordinary capture was lost")
				}
				previewContext := t.Context()
				files, _, _, err := projection.Shell(previewContext, command, workspace, true)
				if err != nil || len(files) != 0 {
					t.Fatalf("non-content input projected live files: %+v, %v", files, err)
				}
				broker := newLivePreview(t.Context())
				setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
				registry := &Windows{}
				window := &execWindow{ref: "admission", thread: "main", roots: []string{workspace}}
				registry.open(window)
				registry.preview(window.ref, *observation, broker, workspace, "main", "/root", nil)
				if (window.previewCancel != nil) != mixed {
					t.Fatalf("source admission = %v, mixed = %v", window.previewCancel != nil, mixed)
				}
				if mixed {
					writeTestFile(t, filepath.Join(workspace, "authored.go"), "package arriving\n")
					preview := waitExecScopePreview(t, broker, func(p previewmodel.Preview) bool { return len(p.Files) > 0 })
					if len(preview.Files) != 1 || filepath.Base(preview.Files[0].AfterPath) != "authored.go" {
						t.Fatalf("unrelated effects entered the card: %+v", preview)
					}
				}
				registry.close(window.ref)
			}
			observation, _ := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, changecapture.Options{Directory: workspace})
			process := exec.Command(execTrackShellExecutable(t, "bash"), "-c", command)
			process.Dir = workspace
			process.Env = append(os.Environ(), "BASH_ENV=", "MEKUGI_EXEC_TRACK=")
			if output, err := process.CombinedOutput(); err != nil {
				t.Fatalf("fixture execution: %v: %s", err, output)
			}
			if files := reconcileExecVCSTestExact(t, observation); len(files) == 0 {
				t.Fatal("non-streamable effects lost completed evidence")
			}
		})
	}
}

func TestExecRunningPreviewExcludesVCSScope(t *testing.T) {
	for _, command := range []string{"git restore -- tracked.txt", "git -C . reset --hard", "git clean -fd", "git rm -f tracked.txt", "git mv tracked.txt moved.txt", "git show HEAD:tracked.txt > exported.txt", "env GIT_PAGER=cat git show HEAD:tracked.txt >> exported.txt", "git show HEAD:tracked.txt | tee exported.txt", "git show HEAD:tracked.txt | cat > exported.txt", "(git show HEAD:tracked.txt; true) > exported.txt", "{ git show HEAD:tracked.txt; true; } | tee exported.txt", "if true; then git show HEAD:tracked.txt; fi > exported.txt"} {
		t.Run(command, func(t *testing.T) {
			repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "committed bytes\n"})
			writeTestFile(t, filepath.Join(repo, "tracked.txt"), "dirty bytes\n")
			writeTestFile(t, filepath.Join(repo, "untracked.txt"), "untracked bytes\n")
			observation, observed := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: repo, Shell: "bash"}}, false, false, changecapture.Options{Directory: repo, PreviewOnly: true})
			if !observed || observation == nil || len(observation.Files) == 0 {
				t.Fatal("VCS scope missing")
			}
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{Workspaces: map[string]map[string]bool{repo: {"thread": true}}})
			registry := &Windows{}
			window := &execWindow{ref: "vcs", roots: []string{repo}, thread: "thread"}
			registry.open(window)
			defer registry.close("vcs")
			registry.preview("vcs", *observation, broker, repo, "thread", "/root", nil)
			registry.state().mu.Lock()
			watching := window.previewCancel != nil
			registry.state().mu.Unlock()
			if watching {
				t.Fatal("diagnostic VCS scope opened a live diff watch")
			}
			// Filtering the display must not change the retained capture.
			process := exec.Command(execTrackShellExecutable(t, "bash"), "-c", command)
			process.Dir = repo
			process.Env = append(os.Environ(), "BASH_ENV=", "MEKUGI_EXEC_TRACK=", "GIT_TERMINAL_PROMPT=0")
			if output, err := process.CombinedOutput(); err != nil {
				t.Fatalf("execute fixture: %v: %s", err, output)
			}
			files := reconcileExecVCSTestExact(t, observation)
			if len(files) == 0 {
				t.Fatal("VCS diagnostic evidence missing")
			}
			for _, file := range files {
				if changes.AuthoredReview(file) {
					t.Fatalf("VCS scope became authored: %+v", file)
				}
			}
		})
	}
}

func TestExecRunningPreviewRegistryBoundsBackgroundAndShutdown(t *testing.T) {
	// The capacity case needs all process-wide preview slots. Keep its parent
	// serial; the lifecycle cases can share slots after the capacity case closes.
	t.Run("active preview bound", func(t *testing.T) {
		repo, tracked, observation := captureRunningExecScope(t)
		store, err := openMekugiReplayStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{
			Workspaces: map[string]map[string]bool{repo: {"thread": true}},
		})
		registry := &Windows{}
		refs := make([]string, 17)
		for index := range refs {
			refs[index] = fmt.Sprintf("bounded-%02d", index)
			registry.open(&execWindow{ref: refs[index], roots: []string{repo}, thread: "thread"})
		}
		for _, ref := range refs {
			registry.preview(ref, *observation, broker, repo, "thread", "bounded-test", nil)
		}
		writeTestFile(t, tracked, "observed bytes\n")
		for _, ref := range refs[:16] {
			waitExecScopePreview(t, broker, func(preview previewmodel.Preview) bool {
				return preview.ID == "running:"+ref
			})
		}
		count := len(broker.Current())
		_, overflow := currentLivePreview(broker, "running:"+refs[16])
		if count != 16 || overflow {
			t.Fatalf("running preview registry exceeded its display bound: count=%d overflow=%v", count, overflow)
		}
		registry.close(refs...)
		for _, ref := range refs[:16] {
			waitExecScopePreviewGone(t, broker, "running:"+ref)
		}
	})

	for _, lifecycle := range []string{"background", "broker shutdown"} {
		t.Run(lifecycle, func(t *testing.T) {
			t.Parallel()
			repo, tracked, observation := captureRunningExecScope(t)
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			broker, stopBroker := liveDiffTestBroker(t, store, previewmodel.Scope{
				Workspaces: map[string]map[string]bool{repo: {"thread": true}},
			})
			registry := &Windows{}
			registry.open(&execWindow{ref: "lifecycle", roots: []string{repo}, thread: "thread", turn: "old-turn", session: "session:42"})
			registry.preview("lifecycle", *observation, broker, repo, "thread", "lifecycle-test", nil)
			writeTestFile(t, tracked, "observed bytes\n")
			waitExecScopePreview(t, broker, func(preview previewmodel.Preview) bool {
				return preview.ID == "running:lifecycle" && preview.Status == previewmodel.PreviewRunning
			})

			if lifecycle == "background" {
				registry.markBackground("thread", "new-turn")
				registry.preview("lifecycle", *observation, broker, repo, "thread", "lifecycle-test", nil)
			} else {
				stopBroker()
			}
			waitExecScopePreviewGone(t, broker, "running:lifecycle")
			if err := os.WriteFile(tracked, []byte("after cancellation\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, exists := currentLivePreview(broker, "running:lifecycle")
			if exists {
				t.Fatalf("%s lifecycle kept publishing a running preview", lifecycle)
			}
		})
	}
}

func TestExecPreviewEditThenTestKeepsKnownTarget(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.go")
	writeTestFile(t, path, "before\n")
	command := "python3 - <<'PY'\np='source.go'\ns=open(p).read().replace('before', 'after')\nopen(p,'w').write(s)\nPY\nenv -u BASH_ENV go test ./..."
	observation, observed := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: command, Workdir: workspace, Shell: "bash"}}, false, true, changecapture.Options{Directory: workspace})
	if !observed || observation == nil || len(observation.Files) != 1 || observation.Files[0].Path != path || observation.Files[0].Content != "before\n" {
		t.Fatalf("lost literal edit target: %+v", observation)
	}
	if observation.Class != execscope.Opaque.String() || !strings.Contains(observation.Reason, "go") {
		t.Fatalf("test side effects incorrectly treated as fully scoped: %+v", observation)
	}
	footer := previewmodel.CaptureFooter(*observation)
	if !strings.Contains(footer, "source.go") || !strings.Contains(footer, "other writes unknown") {
		t.Fatalf("footer confuses known edit with unknown test effects: %q", footer)
	}
}

func TestExecWatchWithoutCapturedPathsDoesNotCrowdStreamingInput(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	registry := &Windows{}
	for index := range 8 {
		ref := fmt.Sprintf("watch-%d", index)
		registry.open(&execWindow{ref: ref, roots: []string{workspace}, thread: "thread"})
		registry.preview(ref, changecapture.Observation{Class: execscope.Opaque.String(), Reason: "unresolved command"}, broker, workspace, "thread", "same-caller", nil)
	}
	broker.Update(previewmodel.Preview{ID: "script", Workspace: workspace, Thread: "thread", Caller: "same-caller",
		Status: previewmodel.PreviewEdit, Input: "cat /tmp/example\n"}, false)
	var pane previewview.PreviewPane
	for _, snapshot := range broker.Current() {
		pane.Update(snapshot, time.Now())
	}
	rows, err := pane.RenderBatch(t.Context(), "same-caller", workspace, diffrender.TerminalTheme, 120, 22, 15)
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(strings.Join(rows, "\n"))
	if strings.Count(plain, "same-caller ·") != 1 || strings.Contains(plain, "No scoped changes") ||
		strings.Contains(plain, "other writes unknown") || !strings.Contains(plain, "cat /tmp/example") {
		t.Fatalf("empty exec watches crowded streaming input: %s", plain)
	}
	active := len(broker.Current())
	if active != 1 {
		t.Fatalf("empty exec watches occupied %d preview slots, want only the script", active)
	}
	registry.close("watch-0", "watch-1", "watch-2", "watch-3", "watch-4", "watch-5", "watch-6", "watch-7")
}

func TestExecRunningPreviewShowsAuthoredScopeAndCancelsWithoutEvidence(t *testing.T) {
	repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "before bytes\n", "restored.txt": "committed bytes\n"})
	tracked := filepath.Join(repo, "tracked.txt")
	writeTestFile(t, filepath.Join(repo, "restored.txt"), "dirty bytes\n")
	observation, observed := changecapture.Capture(t.Context(), []commandanalysis.Command{{Command: "cat > tracked.txt <<'EOF'\nafter\nEOF\ngit restore -- restored.txt", Workdir: repo, Shell: "bash"}}, false, false, changecapture.Options{Directory: repo})
	if !observed || observation == nil {
		t.Fatal("mixed scope missing")
	}
	outside := filepath.Join(repo, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Exercise both qualification markers on an ordinary captured scope: the
	// omitted target represents a path beyond the bounded pre-call capture.
	observation.Reason = "additional targets were unresolved"
	observation.Omitted = append(observation.Omitted, changecapture.Omission{Path: "also.txt", Reason: "fixture capture bound"})

	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	broker, _ := liveDiffTestBroker(t, store, previewmodel.Scope{
		Workspaces: map[string]map[string]bool{repo: {"thread": true}},
	})
	registry := &Windows{}
	registry.open(&execWindow{ref: "call-1", roots: []string{repo}, thread: "thread"})
	registry.preview("call-1", *observation, broker, repo, "thread", "exec-test-caller", nil)
	runExecVCSTestGit(t, repo, "restore", "--", "restored.txt")

	if err := os.WriteFile(tracked, []byte("restored bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside secret marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := waitExecScopePreview(t, broker, func(preview previewmodel.Preview) bool {
		return preview.ID == "running:call-1" && preview.Status == previewmodel.PreviewRunning && len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, "+restored bytes")
	})
	var pane previewview.PreviewPane
	pane.Update(preview, time.Now())
	rows, err := pane.RenderBatch(t.Context(), preview.Caller, repo, diffrender.TerminalTheme, 240, 22, 15)
	if err != nil {
		t.Fatal(err)
	}
	plainRunning := ansi.Strip(strings.Join(rows, "\n"))
	if strings.Contains(plainRunning, "may write") || !strings.Contains(plainRunning, "observed changes") ||
		strings.Contains(plainRunning, "other writes unknown") ||
		strings.Contains(plainRunning, "outside secret marker") || strings.Contains(plainRunning, "outside.txt") ||
		strings.Contains(plainRunning, "restored.txt") || strings.Contains(plainRunning, "will restore") {
		t.Fatalf("running preview escaped its captured scope or lost its qualification: %s", plainRunning)
	}
	if preview.Workspace != repo || preview.Thread != "thread" {
		t.Fatalf("active observation lost caller scope: %+v", preview)
	}
	if path := preview.Files[0].AfterPath; path != tracked {
		t.Fatalf("running preview reported path %q outside captured target %q", path, tracked)
	}

	registry.close("call-1")
	waitExecScopePreviewGone(t, broker, "running:call-1")
	if err := os.WriteFile(tracked, []byte("after cancellation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.lookup(t.Context(), repo, "call-1"); err != nil || found {
		t.Fatalf("display-only running observation persisted change evidence: found=%v err=%v", found, err)
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), previewmodel.Scope{Workspaces: map[string]map[string]bool{repo: {"thread": true}}})
	if err != nil || len(files) != 0 {
		t.Fatalf("display-only running observation entered durable live history: files=%+v err=%v", files, err)
	}
}
