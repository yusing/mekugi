package router

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execscope"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

func waitExecScopePreview(t *testing.T, broker *liveDiffBroker, match func(diffview.Preview) bool) diffview.Preview {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		broker.mu.Lock()
		for _, preview := range broker.previews {
			if match(preview) {
				broker.mu.Unlock()
				return preview
			}
		}
		broker.mu.Unlock()
		select {
		case <-timer.C:
			t.Fatal("timed out waiting for exec scope preview")
		case <-ticker.C:
		}
	}
}

func waitExecScopePreviewGone(t *testing.T, broker *liveDiffBroker, id string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		broker.mu.Lock()
		_, exists := broker.previews[id]
		broker.mu.Unlock()
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
	observation := execObservation{
		Class:  execscope.Scoped.String(),
		Reason: "additional VCS targets were unresolved",
		Files: []execFileSnapshot{
			{Path: "/scope/tracked.txt"},
			{Path: "/scope/tracked.txt"},
		},
		Omitted: []execOmission{{Path: "/scope/also.txt", Reason: "fixture capture bound"}},
	}
	if got, want := execScopePreviewFooter(observation), "may write · 2 scoped paths · tracked.txt, also.txt · other writes unknown · bounded scope"; got != want {
		t.Fatalf("bounded VCS scope footer = %q, want %q", got, want)
	}
}

func TestExecWatchWithoutCapturedPathsDoesNotCrowdStreamingInput(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
	})
	ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 22)
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	registry := &execWindowRegistry{}
	for index := range 8 {
		ref := fmt.Sprintf("watch-%d", index)
		registry.open(&execWindow{ref: ref, roots: []string{workspace}, thread: "thread"})
		registry.preview(ref, execObservation{Class: execscope.Opaque.String(), Reason: "unresolved command"}, broker, workspace, "thread", "same-caller", nil)
	}
	broker.publishPreview(diffview.Preview{ID: "script", Workspace: workspace, Thread: "thread", Caller: "same-caller",
		Status: diffview.PreviewEdit, Input: "cat /tmp/example\n"}, false)
	frame := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "cat /tmp/example") })
	plain := ansi.Strip(frame)
	if strings.Count(plain, "same-caller ·") != 1 || strings.Contains(plain, "No scoped changes") ||
		strings.Contains(plain, "other writes unknown") || !strings.Contains(plain, "cat /tmp/example") {
		t.Fatalf("empty exec watches crowded streaming input: %s", plain)
	}
	broker.mu.Lock()
	active := len(broker.previews)
	broker.mu.Unlock()
	if active != 1 {
		t.Fatalf("empty exec watches occupied %d preview slots, want only the script", active)
	}
	registry.close("watch-0", "watch-1", "watch-2", "watch-3", "watch-4", "watch-5", "watch-6", "watch-7")
	ui.quit(t)
}

func captureRunningExecScope(t *testing.T) (string, string, *execObservation) {
	t.Helper()
	repo := t.TempDir()
	tracked := filepath.Join(repo, "tracked.txt")
	writeTestFile(t, tracked, "before bytes\n")
	observation, observed := captureExecObservation([]execCommandInput{{Command: "cat > tracked.txt <<'EOF'\nafter\nEOF", Workdir: repo, Shell: "bash"}}, false, false, execCaptureEnv{directory: repo})
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
				commands := []execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}
				if mixed {
					commands = append(commands, execCommandInput{Command: "cat > authored.go <<'EOF'\npackage authored\nEOF", Workdir: workspace, Shell: "bash"})
				}
				observation, ok := captureExecObservation(commands, false, false, execCaptureEnv{directory: workspace})
				if !ok || observation == nil || len(observation.Files) == 0 {
					t.Fatal("ordinary capture was lost")
				}
				worker := liveDiffPreviewWorker{ctx: t.Context()}
				files, _, err := worker.projectShell(command, workspace, true)
				if err != nil || len(files) != 0 {
					t.Fatalf("non-content input projected live files: %+v, %v", files, err)
				}
				broker := newLiveDiffBroker(t.Context())
				broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"main": true}}})
				registry := &execWindowRegistry{}
				window := &execWindow{ref: "admission", thread: "main", roots: []string{workspace}}
				registry.open(window)
				registry.preview(window.ref, *observation, broker, workspace, "main", "/root", nil)
				if (window.previewCancel != nil) != mixed {
					t.Fatalf("source admission = %v, mixed = %v", window.previewCancel != nil, mixed)
				}
				if mixed {
					writeTestFile(t, filepath.Join(workspace, "authored.go"), "package arriving\n")
					preview := waitExecScopePreview(t, broker, func(p diffview.Preview) bool { return len(p.Files) > 0 })
					if len(preview.Files) != 1 || filepath.Base(preview.Files[0].AfterPath) != "authored.go" {
						t.Fatalf("unrelated effects entered the card: %+v", preview)
					}
				}
				registry.close(window.ref)
			}
			observation, _ := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
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
			observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: repo, Shell: "bash"}}, false, false, execCaptureEnv{directory: repo, previewOnly: true})
			if !observed || observation == nil || len(observation.Files) == 0 {
				t.Fatal("VCS scope missing")
			}
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{Workspaces: map[string]map[string]bool{repo: {"thread": true}}})
			registry := &execWindowRegistry{}
			window := &execWindow{ref: "vcs", roots: []string{repo}, thread: "thread"}
			registry.open(window)
			defer registry.close("vcs")
			registry.preview("vcs", *observation, broker, repo, "thread", "/root", nil)
			registry.mu.Lock()
			watching := window.previewCancel != nil
			registry.mu.Unlock()
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
				if authoredReview(file) {
					t.Fatalf("VCS scope became authored: %+v", file)
				}
			}
		})
	}
}

func TestExecRunningPreviewShowsAuthoredScopeAndCancelsWithoutEvidence(t *testing.T) {
	repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "before bytes\n", "restored.txt": "committed bytes\n"})
	tracked := filepath.Join(repo, "tracked.txt")
	writeTestFile(t, filepath.Join(repo, "restored.txt"), "dirty bytes\n")
	observation, observed := captureExecObservation([]execCommandInput{{Command: "cat > tracked.txt <<'EOF'\nafter\nEOF\ngit restore -- restored.txt", Workdir: repo, Shell: "bash"}}, false, false, execCaptureEnv{directory: repo})
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
	observation.Omitted = append(observation.Omitted, execOmission{Path: "also.txt", Reason: "fixture capture bound"})

	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
		Workspaces: map[string]map[string]bool{repo: {"thread": true}},
	})
	registry := &execWindowRegistry{}
	registry.open(&execWindow{ref: "call-1", roots: []string{repo}, thread: "thread"})
	ui := startLiveDiffTerminal(t, repo, store.directory, connection, 22)
	// Keep the literal path on one row regardless of the build's temp root.
	if err := pty.Setsize(ui.pty, &pty.Winsize{Rows: 22, Cols: uint16(max(100, ansi.StringWidth(tracked)+16))}); err != nil {
		t.Fatal(err)
	}
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	registry.preview("call-1", *observation, broker, repo, "thread", "exec-test-caller", nil)
	runExecVCSTestGit(t, repo, "restore", "--", "restored.txt")

	if err := os.WriteFile(tracked, []byte("restored bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside secret marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	running := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "observed so far") && strings.Contains(plain, "+restored bytes")
	})
	plainRunning := ansi.Strip(running)
	if strings.Contains(plainRunning, "may write") || !strings.Contains(plainRunning, "observed changes") ||
		strings.Contains(plainRunning, "other writes unknown") ||
		strings.Contains(plainRunning, "outside secret marker") || strings.Contains(plainRunning, "outside.txt") ||
		strings.Contains(plainRunning, "restored.txt") || strings.Contains(plainRunning, "will restore") {
		t.Fatalf("running preview escaped its captured scope or lost its qualification: %s", plainRunning)
	}
	preview := waitExecScopePreview(t, broker, func(preview diffview.Preview) bool {
		return preview.ID == "running:call-1" && preview.Status == diffview.PreviewRunning && len(preview.Files) == 1
	})
	if preview.Workspace != repo || preview.Thread != "thread" {
		t.Fatalf("active observation lost caller scope: %+v", preview)
	}
	if path := preview.Files[0].AfterPath; path != tracked {
		t.Fatalf("running preview reported path %q outside captured target %q", path, tracked)
	}

	registry.close("call-1")
	waitExecScopePreviewGone(t, broker, "running:call-1")
	removed := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return !strings.Contains(plain, "exec-test-caller") && !strings.Contains(plain, "observed so far")
	})
	if strings.Contains(ansi.Strip(removed), "restored bytes") {
		t.Fatalf("closed running preview remained rendered: %s", ansi.Strip(removed))
	}
	if err := os.WriteFile(tracked, []byte("after cancellation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.lookup(t.Context(), repo, "call-1"); err != nil || found {
		t.Fatalf("display-only running observation persisted change evidence: found=%v err=%v", found, err)
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{repo: {"thread": true}}})
	if err != nil || len(files) != 0 {
		t.Fatalf("display-only running observation entered durable live history: files=%+v err=%v", files, err)
	}
	ui.quit(t)
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
		_, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
			Workspaces: map[string]map[string]bool{repo: {"thread": true}},
		})
		registry := &execWindowRegistry{}
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
			waitExecScopePreview(t, broker, func(preview diffview.Preview) bool {
				return preview.ID == "running:"+ref
			})
		}
		broker.mu.Lock()
		count := len(broker.previews)
		_, overflow := broker.previews["running:"+refs[16]]
		broker.mu.Unlock()
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
			_, broker, stopBroker := liveDiffTestBroker(t, store, liveDiffScope{
				Workspaces: map[string]map[string]bool{repo: {"thread": true}},
			})
			registry := &execWindowRegistry{}
			registry.open(&execWindow{ref: "lifecycle", roots: []string{repo}, thread: "thread", turn: "old-turn", session: "session:42"})
			registry.preview("lifecycle", *observation, broker, repo, "thread", "lifecycle-test", nil)
			writeTestFile(t, tracked, "observed bytes\n")
			waitExecScopePreview(t, broker, func(preview diffview.Preview) bool {
				return preview.ID == "running:lifecycle" && preview.Status == diffview.PreviewRunning
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
			broker.mu.Lock()
			_, exists := broker.previews["running:lifecycle"]
			broker.mu.Unlock()
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
	observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, true, execCaptureEnv{directory: workspace})
	if !observed || observation == nil || len(observation.Files) != 1 || observation.Files[0].Path != path || observation.Files[0].Content != "before\n" {
		t.Fatalf("lost literal edit target: %+v", observation)
	}
	if observation.Class != execscope.Opaque.String() || !strings.Contains(observation.Reason, "go") {
		t.Fatalf("test side effects incorrectly treated as fully scoped: %+v", observation)
	}
	footer := execScopePreviewFooter(*observation)
	if !strings.Contains(footer, "source.go") || !strings.Contains(footer, "other writes unknown") {
		t.Fatalf("footer confuses known edit with unknown test effects: %q", footer)
	}
}
