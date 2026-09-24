package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func waitExecScopePreview(t *testing.T, broker *liveDiffBroker, match func(liveDiffPreview) bool) liveDiffPreview {
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
	observation := execObservation{
		Class:  execScoped.String(),
		Reason: "additional VCS targets were unresolved",
		Files: []execFileSnapshot{
			{Path: "/scope/tracked.txt"},
			{Path: "/scope/tracked.txt"},
		},
		Omitted: []execOmission{{Path: "/scope/also.txt", Reason: "fixture capture bound"}},
	}
	if got, want := execScopePreviewFooter(observation), "may write · 2 scoped paths · tracked.txt, also.txt · unresolved targets · bounded scope"; got != want {
		t.Fatalf("bounded VCS scope footer = %q, want %q", got, want)
	}
}

func TestExecWatchWithoutCapturedPathsDoesNotCrowdStreamingInput(t *testing.T) {
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
		registry.preview(ref, execObservation{Class: execOpaque.String(), Reason: "unresolved command"}, broker, workspace, "thread", "same-caller")
	}
	broker.publishPreview(liveDiffPreview{ID: "script", Workspace: workspace, Thread: "thread", Caller: "same-caller",
		Status: "STREAMING SCRIPT", Input: "cat /tmp/example\n"}, false)
	frame := ui.frame(t, func(frame string) bool { return strings.Contains(ansi.Strip(frame), "cat /tmp/example") })
	plain := ansi.Strip(frame)
	if strings.Count(plain, "same-caller ·") != 1 || strings.Contains(plain, "No scoped changes") ||
		strings.Contains(plain, "unresolved targets") || !strings.Contains(plain, "cat /tmp/example") {
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

func captureRunningExecVCSScope(t *testing.T) (string, string, *execObservation) {
	t.Helper()
	repo := newExecVCSTestRepo(t, map[string]string{"tracked.txt": "committed bytes\n"})
	tracked := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation := captureExecVCSTestCommand(t, repo, "git restore -- tracked.txt")
	assertCapturedExecVCSTestFile(t, observation, tracked, "before bytes\n")
	return repo, tracked, observation
}

func TestExecPendingScopeParsesCapturedGitCommands(t *testing.T) {
	repo, tracked, _ := captureRunningExecVCSScope(t)
	for _, command := range []string{
		"git -C " + repo + " restore -- tracked.txt",
		"printf untouched && git restore -- tracked.txt",
	} {
		t.Run(command, func(t *testing.T) {
			observation := captureExecVCSTestCommand(t, repo, command)
			if got := execPendingScope(*observation); !strings.Contains(got, "will restore (pending)") || !strings.Contains(got, tracked) {
				t.Fatalf("captured VCS target had no actionable pending card: %q", got)
			}
		})
	}
}

func TestExecPendingScopeIgnoresUncalledFunction(t *testing.T) {
	workspace := t.TempDir()
	command := "cleanup() { git clean -fd; }; printf x > ordinary.txt"
	observation, observed := captureExecObservation([]execCommandInput{{
		Command: command, Workdir: workspace, Shell: "bash",
	}}, false, false, execCaptureEnv{directory: workspace})
	if !observed || len(observation.Files) == 0 {
		t.Fatalf("ordinary write had no captured path: %+v", observation)
	}
	if pending := execPendingScope(*observation); pending != "" {
		t.Fatalf("uncalled function created an actionable pending card: %q", pending)
	}
}

func TestExecRunningPreviewShowsScopedVCSAndCancelsWithoutEvidence(t *testing.T) {
	repo, tracked, observation := captureRunningExecVCSScope(t)
	outside := filepath.Join(repo, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Exercise both qualification markers on a real captured VCS scope: the
	// omitted target represents a path beyond the bounded pre-call capture.
	observation.Reason = "additional VCS targets were unresolved"
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
	ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
	registry.preview("call-1", *observation, broker, repo, "thread", "exec-test-caller")

	pending := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "PENDING · scoped effects") &&
			strings.Contains(plain, "will restore (pending)") && strings.Contains(plain, tracked)
	})
	if !strings.Contains(ansi.Strip(pending), "may write") ||
		!strings.Contains(ansi.Strip(pending), "unresolved targets") ||
		!strings.Contains(ansi.Strip(pending), "bounded scope") {
		t.Fatalf("pending VCS card omitted bounded/unresolved scope qualification: %s", ansi.Strip(pending))
	}
	preview := waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
		return preview.ID == "running:call-1" && preview.Status == "PENDING · scoped effects"
	})
	if preview.Workspace != repo || preview.Thread != "thread" || !strings.Contains(preview.Input, tracked) {
		t.Fatalf("pending preview lost its authorized caller scope: %+v", preview)
	}

	if err := os.WriteFile(tracked, []byte("restored bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside secret marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	running := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return strings.Contains(plain, "RUNNING · observed so far") && strings.Contains(plain, "+restored bytes")
	})
	plainRunning := ansi.Strip(running)
	if !strings.Contains(plainRunning, "bounded scope") || !strings.Contains(plainRunning, "unresolved targets") ||
		strings.Contains(plainRunning, "outside secret marker") || strings.Contains(plainRunning, "outside.txt") {
		t.Fatalf("running preview escaped its captured scope or lost its qualification: %s", plainRunning)
	}
	preview = waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
		return preview.ID == "running:call-1" && preview.Status == "RUNNING · observed so far" && len(preview.Files) == 1
	})
	if path := preview.Files[0].AfterPath; path != tracked {
		t.Fatalf("running preview reported path %q outside captured target %q", path, tracked)
	}

	registry.close("call-1")
	waitExecScopePreviewGone(t, broker, "running:call-1")
	removed := ui.frame(t, func(frame string) bool {
		plain := ansi.Strip(frame)
		return !strings.Contains(plain, "exec-test-caller") && !strings.Contains(plain, "RUNNING · observed so far")
	})
	if strings.Contains(ansi.Strip(removed), "restored bytes") {
		t.Fatalf("closed running preview remained rendered: %s", ansi.Strip(removed))
	}
	if err := os.WriteFile(tracked, []byte("after cancellation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(600 * time.Millisecond): // Longer than one poll interval.
	case <-t.Context().Done():
		t.Fatal("test canceled while checking preview cancellation")
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
	t.Run("active preview bound", func(t *testing.T) {
		repo, _, observation := captureRunningExecVCSScope(t)
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
			registry.preview(ref, *observation, broker, repo, "thread", "bounded-test")
		}
		for _, ref := range refs[:16] {
			waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
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
			repo, tracked, observation := captureRunningExecVCSScope(t)
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			_, broker, stopBroker := liveDiffTestBroker(t, store, liveDiffScope{
				Workspaces: map[string]map[string]bool{repo: {"thread": true}},
			})
			registry := &execWindowRegistry{}
			registry.open(&execWindow{ref: "lifecycle", roots: []string{repo}, thread: "thread", turn: "old-turn", session: "session:42"})
			registry.preview("lifecycle", *observation, broker, repo, "thread", "lifecycle-test")
			waitExecScopePreview(t, broker, func(preview liveDiffPreview) bool {
				return preview.ID == "running:lifecycle" && preview.Status == "PENDING · scoped effects"
			})

			if lifecycle == "background" {
				registry.markBackground("thread", "new-turn")
				registry.preview("lifecycle", *observation, broker, repo, "thread", "lifecycle-test")
			} else {
				stopBroker()
			}
			waitExecScopePreviewGone(t, broker, "running:lifecycle")
			if err := os.WriteFile(tracked, []byte("after cancellation\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			select {
			case <-time.After(600 * time.Millisecond):
			case <-t.Context().Done():
				t.Fatal("test canceled while checking lifecycle cancellation")
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
