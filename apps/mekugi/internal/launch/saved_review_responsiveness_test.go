//go:build unix

package launch

import (
	"context"
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

	"github.com/yusing/mekugi/backends/codex/binding"

	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/backends/codex/shellhook"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory/changes"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

type blockedReviewChanges struct {
	changes.Service
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
	failure     error
}

func (s *blockedReviewChanges) InspectVisible(scope changes.Scope, workspace string, threads map[string]bool) ([]changes.Snapshot, error) {
	if threads["blocked-child"] {
		s.once.Do(func() { close(s.entered) })
		<-s.release
		if s.failure != nil {
			return nil, s.failure
		}
	}
	return s.Service.InspectVisible(scope, workspace, threads)
}

func TestSavedReviewLoadingSeparatesPreviewAuthorityFromRestoredInput(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "loaded", true: "unavailable"}[failure], func(t *testing.T) {
			loader, store, workspace := savedReviewLoaderFixture(t)
			liveDiffScopeCapture(t, store, workspace, "blocked-child", "edit", filepath.Join(workspace, "restored.go"), "before", "after")
			blocked, release := savedReviewLoaderBlock(t, store)
			if failure {
				blocked.failure = errors.New("retained index unavailable")
			}
			u := newAppServerSessionTestUI(t, workspace)
			u.review.Projection, u.review.Loader = loader.Projection(), loader
			sub := u.review.Subscribe()
			defer func() { u.review.Unsubscribe(sub) }()
			if err := u.restoreContent(binding.ThreadInfo{ID: "main", Cwd: workspace}); err != nil {
				t.Fatal(err)
			}
			u.review.IncludeThread(workspace, "blocked-child", false)

			for _, event := range u.review.TakePreviews(sub) {
				applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), event)
			}
		waiting:
			for {
				select {
				case <-blocked.entered:
					break waiting
				case result := <-loader.Updates():
					loadedReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, result)
				case <-time.After(5 * time.Second):
					t.Fatal("restored Diff did not reach blocked retained read")
				}
			}
			preview := previewmodel.Preview{ID: "stream", Workspace: workspace, Thread: "blocked-child", Caller: "/root/worker", Input: "live edit", Status: "active"}
			applyReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sessionreview.Event{Kind: "preview", Preview: &preview})
			revealNativeDock(terminalTest(u))
			if terminalTest(u).Live.Views[preview.ID] == nil || u.review.Projection.Scope.Workspaces[workspace][preview.Thread] {
				t.Fatal("preview waited for retained scope hydration")
			}
			if err := u.finishRestoredContent(); err != nil {
				t.Fatal(err)
			}
			u.Text = "next turn"
			appServerTestKeys(t, u, "\r")
			w := u.Host.Input.(*appServerTestInput)
			if u.restoring == nil || len(restoreContentRequests(t, w)) != 0 {
				t.Fatal("restored input became available before retained read settled")
			}
			release()
			for loader.Pending() && u.review.Failure == "" {
				loadedReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, savedReviewLoaderResult(t, loader))
				if err := u.finishRestoredContent(); err != nil {
					t.Fatal(err)
				}
			}
			if u.restoring != nil || (u.review.Failure != "") != failure {
				t.Fatalf("restoration did not settle after retained read: restoring=%v failure=%q", u.restoring != nil, u.review.Failure)
			}
			if !failure && len(u.review.Projection.Data.Publication.Files) != 1 {
				t.Fatal("successful hydration did not publish saved Diff")
			}
			if failure {
				// Returning from the editor creates a fresh subscription even
				// when retained Diff is unavailable. Its barrier must settle,
				// not trigger an endless chain of replacement subscriptions.
				u.review.Unsubscribe(sub)
				loader.AwaitResync()
				sub = u.review.Subscribe()
				if !admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sub, <-sub.Events()) {
					t.Fatal("failed Diff requested another subscription after editor reattachment")
				}
				preview.ID = "after-editor"
				if !admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sub, sessionreview.Event{Kind: "preview", Preview: &preview}) {
					t.Fatal("failed Diff forced resubscription during preview delivery")
				}
				revealNativeDock(terminalTest(u))
				if terminalTest(u).Live.Views[preview.ID] == nil {
					t.Fatal("failed Diff disabled previews after editor reattachment")
				}
			}
			appServerTestKeys(t, u, "\r")
			requests := restoreContentRequests(t, w)
			if len(requests) != 1 || requests[0].Method != "turn/start" {
				t.Fatal("settled hydration left restored input unavailable")
			}
		})
	}
}

func TestSavedReviewReplacementRejectsQueuedOldResync(t *testing.T) {
	loader, store, workspace := savedReviewLoaderFixture(t)
	liveDiffScopeCapture(t, store, workspace, "old", "old-edit", filepath.Join(workspace, "old.go"), "a", "b")
	liveDiffScopeCapture(t, store, workspace, "replacement", "new-edit", filepath.Join(workspace, "new.go"), "a", "b")
	u := newAppServerSessionTestUI(t, workspace)
	u.review.Projection, u.review.Loader = loader.Projection(), loader
	u.review.IncludeThread(workspace, "old", true)
	old := u.review.Subscribe()
	defer u.review.Unsubscribe(old)
	resetDiffScope(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, u.ctx)
	u.review.IncludeThread(workspace, "replacement", true)
	if admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), old, <-old.Events()) {
		t.Fatal("queued old resync entered the replacement attachment")
	}
	if loader.Pending() || len(u.review.PreviewScope.Workspaces) != 0 {
		t.Fatal("invalidated subscription authorized or started an old-session read")
	}
	sub := u.review.Subscribe()
	defer u.review.Unsubscribe(sub)
	if !admitReviewEvents(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, u.clock, t.Context(), sub, <-sub.Events()) {
		t.Fatal("current replacement scope was rejected")
	}
	loadedReview(&u.attached, u.shell, u.review, u.reviewSource, u.preparePane, &u.pendingEditReceipts, savedReviewLoaderResult(t, loader))
	files := u.review.Projection.Data.Publication.Files
	if u.review.Failure != "" || len(files) != 1 || filepath.Base(files[0].Path) != "new.go" {
		t.Fatalf("old subscription poisoned replacement Diff: failure=%q files=%+v", u.review.Failure, files)
	}
}

// Run the production stdio client, terminal select loop, and renderer. The host
// fixture completes its turn while a real retained scope read remains stalled.
func TestAppServerSavedReviewHydrationKeepsTerminalResponsive(t *testing.T) {
	const helper = "MEKUGI_TEST_SAVED_REVIEW_HOST"
	if os.Getenv(helper) == "1" {
		serveSavedReviewHost(t)
		os.Exit(0)
	}
	workspace := savedDiffWorkspace(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffScopeCapture(t, store, workspace, "blocked-child", "edit", filepath.Join(workspace, "retained.txt"), "before", "retained-after")
	blocked := &blockedReviewChanges{Service: store.changes, entered: make(chan struct{}), release: make(chan struct{})}
	store.changes = blocked
	release := sync.OnceFunc(func() { close(blocked.release) })
	defer release()
	proxy := newManagedMekugiProxy(t)
	setTestProxyStore(proxy, store)
	proxy.review.IncludeThread(workspace, "main", true)

	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: 120, Rows: 35}); err != nil {
		t.Fatal(err)
	}
	control, sendCompletion, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	defer sendCompletion.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAppServerSavedReviewHydrationKeepsTerminalResponsive$", "--")
	cmd.Dir = workspace
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(value string) bool {
		return strings.HasPrefix(value, shellhook.Environment+"=")
	}), helper+"=1")
	cmd.ExtraFiles = []*os.File{control}
	conversation := new(conversationmodel.Service)
	observations := conversationbinding.Codex.Observation(conversation)
	reviewSource := new(changecapture.Service)
	wait, err := startAppServerUI(testTerminalViews(reviewSource), reviewSource, testPresentation(), conversation, new(activitymodel.Service), testHistory(), testPreferences(), testReports(), ctx, cmd, terminal, terminal, testAppServerIntegration(proxy), nil, "", true, nil, nil, nil, "", nil, &observations, false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	joined := make(chan struct{})
	go func() { defer close(joined); done <- wait() }()
	chunks := readPTYChunks(ctx, master, 65536, 32)
	screen := vt.NewEmulator(120, 35)
	stopReplies := startEmulatorReplies(master, screen)
	defer func() {
		release()
		cancel()
		master.Close()
		terminal.Close()
		<-joined
		for range chunks {
		}
		stopReplies()
	}()
	await := func(timeout time.Duration, visible func(string) bool) bool {
		t.Helper()
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for !visible(screen.String()) {
			select {
			case chunk, open := <-chunks:
				if !open {
					t.Fatal("terminal closed before the expected frame")
				}
				if _, err := screen.Write(chunk); err != nil {
					t.Fatal(err)
				}
			case err := <-done:
				t.Fatalf("UI exited before the expected frame: %v", err)
			case <-timer.C:
				return false
			}
		}
		return true
	}
	if !await(5*time.Second, func(frame string) bool { return strings.Contains(frame, "Working") }) {
		t.Fatalf("host fixture did not start a turn:\n%s", screen.String())
	}
	proxy.review.IncludeThread(workspace, "blocked-child", false)
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("saved review did not reach the retained scope read")
	}
	if _, err := io.WriteString(master, "responsive_draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := sendCompletion.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	updated := func(frame string) bool {
		return strings.Contains(frame, "responsive_draft") && strings.Contains(frame, "Completed") && !strings.Contains(frame, "Working")
	}
	responsive := await(500*time.Millisecond, updated)
	release()
	if !await(5*time.Second, updated) {
		t.Fatalf("input and host completion did not arrive after storage resumed:\n%s", screen.String())
	}
	// This retained capture has unknown ownership; inspect all callers.
	if _, err := io.WriteString(master, "\x0220"); err != nil {
		t.Fatal(err)
	}
	if !await(5*time.Second, func(frame string) bool { return strings.Contains(frame, "retained-after") }) {
		t.Fatalf("hydrated evidence did not reach saved Diff:\n%s", screen.String())
	}
	if !responsive {
		t.Error("saved-review hydration blocked composer input and host turn completion until storage was released")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("UI cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UI did not stop after cancellation")
	}
}

func serveSavedReviewHost(t *testing.T) {
	t.Helper()
	control := os.NewFile(3, "saved-review-completion")
	defer control.Close()
	decoder, encoder := jsontext.NewDecoder(os.Stdin), jsontext.NewEncoder(os.Stdout)
	write := func(value any) {
		t.Helper()
		if err := json.MarshalEncode(encoder, value); err != nil {
			t.Fatal(err)
		}
	}
	for {
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
		}
		if err := json.UnmarshalDecode(decoder, &request); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			return
		}
		if len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		if request.Method == "thread/start" {
			workspace, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			result["thread"] = map[string]any{"id": "main", "cwd": workspace}
		}
		write(map[string]any{"id": request.ID, "result": result})
		if request.Method == "thread/start" {
			write(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn"}}})
			if _, err := io.ReadFull(control, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "completed"}}})
		}
	}
}
