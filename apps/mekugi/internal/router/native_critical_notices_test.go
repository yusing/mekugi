package router

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
)

func TestNativeCriticalNoticesRenderInMainWithoutProviderMessages(t *testing.T) {
	activity := newSubagentActivity()
	if !activity.observe("main", "", "/root", false) ||
		!activity.observe("child", "main", "/root/worker", true) ||
		!activity.observe("other", "", "/root", false) {
		t.Fatal("failed to establish thread ownership")
	}
	issues := testCriticalErrors()
	queueCritical(issues, "main")
	queueCritical(issues, "child")
	queueCritical(issues, "other")
	issues.AddNotice("", "capacity", "router-wide capacity warning")
	u, _ := newAppServerTestUI()
	u.issues = issues
	u.proxy = &mekugiProxy{activity: activity}
	u.view.Conversation = true

	first := u.applyCriticalNotices()
	if first == nil || len(first.errors.Messages) != 3 {
		t.Fatalf("native delivery = %v, want root, child, and router-wide", first)
	}

	if len(u.view.Entries) != 0 || !u.feedback.Alert {
		t.Fatal("router errors entered the transcript or missed the composer")
	}
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	if err := u.testPaint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.feedback.Text, "/root/worker: ") || !strings.Contains(u.feedback.Text, "router-wide capacity warning") || strings.Count(u.feedback.Text, "Enable supported tools.") != 2 {
		t.Fatalf("composer omitted or mis-scoped native notices: %s", u.feedback.Text)
	}
	before := u.feedback.Text

	rect := u.mainView.NoticeDetails
	if rect.W == 0 {
		t.Fatal("router batch has no details target")
	}
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", rect.X+u.shell.Layout.Main.X+1, rect.Y+u.shell.Layout.Main.Y+1)); err != nil {
		t.Fatal(err)
	}
	if u.shell.output == nil {
		t.Fatal("router errors did not open on click")
	}
	drawOutputDialog(u.shell)
	if u.shell.output.Document.Text != before {
		t.Fatal("router details lost a scoped error")
	}
	first.finish(true)
	if len(issues.Notices.Pending()) != 4 {
		t.Fatal("dialog acknowledged a hidden composer batch")
	}
	u.shell.outputKey("q")
	first.finish(false) // Simulate an unpainted terminal frame.
	retry := u.applyCriticalNotices()
	if retry == nil || len(retry.errors.Messages) != 3 || len(u.view.Entries) != 0 || u.feedback.Text != before {
		t.Fatalf("retry duplicated or lost entries: delivery=%v entries=%d", retry, len(u.view.Entries))
	}
	if err := u.testPaint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	retry.finish(true)
	if len(issues.Notices.Pending()) != 1 || u.applyCriticalNotices() != nil {
		t.Fatalf("paint acknowledged unrelated root or queued duplicate: %v", issues.Notices.Pending())
	}
}

func TestNativeRequestNoticesWaitForHostTurnOutcome(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.issues = testCriticalErrors()
			record := func(cause string) {
				u.issues.Report(requestexecution.Failure{Session: "session", Thread: "main", Turn: "turn",
					Phase: "inspect_response"}, fmt.Errorf("upstream closed: %s", cause))
			}
			record("StatusServiceRestart")
			if delivery := u.applyCriticalNotices(); delivery != nil || u.feedback.Alert {
				t.Fatal("request attempt interrupted a retrying turn")
			}
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": status}})
			record("late NormalClosure") // Finalization can follow the host notification.
			delivery := u.applyCriticalNotices()
			if status != "failed" {
				if delivery != nil || len(u.issues.Notices.Pending()) != 0 || u.feedback.Alert {
					t.Fatal("recovered or interrupted turn retained a failure notice")
				}
				return
			}
			if delivery == nil || len(delivery.errors.Messages) != 2 || !strings.Contains(u.feedback.Text, "StatusServiceRestart") || !strings.Contains(u.feedback.Text, "NormalClosure") {
				t.Fatalf("terminal failure lost its diagnostic batch: %s", u.feedback.Text)
			}
			delivery.errors.Finish(true)
			if len(u.issues.Notices.Pending()) != 0 {
				t.Fatal("delivered terminal failure stayed pending")
			}
		})
	}
}

func TestNativeRequestNoticeChildOutcomeKeepsOtherTurnsPending(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.issues = testCriticalErrors()
	activity := newSubagentActivity()
	activity.observe("main", "", "/root", false)
	activity.observe("child", "main", "/root/worker", true)
	u.proxy = &mekugiProxy{activity: activity}
	for _, thread := range []string{"main", "child", "other"} {
		u.issues.Report(requestexecution.Failure{Session: "session", Thread: thread, Turn: "turn",
			Phase: "inspect_response"}, fmt.Errorf("upstream closed for %s", thread))
	}
	queueCritical(u.issues, "main") // Auxiliary/unidentified failures stay immediate.
	delivery := u.applyCriticalNotices()
	if delivery == nil || len(delivery.errors.Messages) != 1 {
		t.Fatal("retry gating hid an unrelated actionable notice")
	}
	delivery.errors.Finish(true)
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "child", "turn": map[string]any{"id": "turn", "status": "failed"}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "completed"}})
	delivery = u.applyCriticalNotices()
	if delivery == nil || len(delivery.errors.Messages) != 1 || !strings.Contains(u.feedback.Text, "/root/worker: ") || strings.Contains(u.feedback.Text, "upstream closed for main") {
		t.Fatalf("child completion crossed turn scope: %s", u.feedback.Text)
	}
	delivery.errors.Finish(true)
	if pending := u.issues.Notices.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "upstream closed for other") {
		t.Fatalf("unobserved turn lost launcher recovery: %v", pending)
	}
}

func TestFailureStorageNoticeReachesOwningNativeRootBeforeShutdown(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(stateHome, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)
	issues := testCriticalErrors()
	issues.Configure(requestexecution.FailureStorage{Diagnostics: testDiagnostics(nil, nil)})
	failure := failureStoreTestFailure()
	failure.Turn = "completed-host-turn"
	issues.Report(failure, errors.New("retained test failure"))
	issues.Notices.ObserveTurn(failure.Thread, failure.Turn, "completed")

	other, _ := newAppServerTestUI()
	other.Thread = "unrelated-root"
	other.issues = issues
	if delivery := other.applyCriticalNotices(); delivery != nil || len(other.view.Entries) != 0 {
		t.Fatal("another root claimed the storage notice")
	}

	owner, _ := newAppServerTestUI()
	owner.Thread = failure.Thread
	owner.issues = issues
	owner.view.Conversation = true
	delivery := owner.applyCriticalNotices()
	if delivery == nil || len(delivery.errors.Messages) != 1 || len(owner.view.Entries) != 0 || !owner.feedback.Alert {
		t.Fatalf("owning root did not receive the storage notice: delivery=%v entries=%d", delivery, len(owner.view.Entries))
	}
	rows, _ := owner.testMainFrame(100, 12, 0)
	feed := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(feed, "could not retain the failure reference") {
		t.Fatalf("storage failure did not appear in the composer before shutdown: %s", feed)
	}
	delivery.finish(true)
	if len(issues.Notices.Pending()) != 0 {
		t.Fatalf("painted notice remained pending: %v", issues.Notices.Pending())
	}
}

func TestNativeCriticalNoticeComposerVisibility(t *testing.T) {
	for _, state := range []string{"status", "replaced", "compact"} {
		t.Run(state, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.issues = testCriticalErrors()
			queueCritical(u.issues, u.Thread)
			delivery := u.applyCriticalNotices()
			u.ensureShell()
			defer u.shell.diffScreen.Close()
			switch state {
			case "status":
				u.statusPanel = &appServerStatusReport{}
			case "replaced":
				u.setNotice("another error", true)
			}
			height := 12
			if state == "compact" {
				height = 3
			}
			u.testMainFrame(80, height, 0)
			delivery.finish(true)
			if state == "compact" {
				if u.mainView.ContentPainted || len(u.issues.Notices.Pending()) != 0 {
					t.Fatal("compact composer did not deliver independently of transcript")
				}
				return
			}
			if len(u.issues.Notices.Pending()) != 1 {
				t.Fatal("hidden or replaced notice was acknowledged")
			}
			u.statusPanel = nil
			retry := u.applyCriticalNotices()
			if retry == nil || u.feedback.Text != delivery.composerText {
				t.Fatal("hidden batch could not retry")
			}
			u.testMainFrame(80, 12, 0)
			retry.finish(true)
			if len(u.issues.Notices.Pending()) != 0 {
				t.Fatal("visible retry remained pending")
			}
			u.applyCriticalNotices().finish(false) // Empty polls retain nil-safe acknowledgement.
		})
	}
}

func TestNativeCleanupNoticeTranscriptReplacement(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(fmt.Sprintf("delivered=%t", delivered), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.issues.CleanupNotice("", "", "planning", "pending cleanup plan")
			first := u.applyCriticalNotices()
			u.ensureShell()
			defer u.shell.diffScreen.Close()
			height := 3
			if delivered {
				height = 12
			}
			u.testMainFrame(80, height, 0)
			first.finish(true)
			if got := len(u.issues.Notices.Pending()); (got == 0) != delivered {
				t.Fatalf("initial delivery: pending=%d delivered=%t", got, delivered)
			}
			if err := u.clearSessionPresentation(); err != nil {
				t.Fatal(err)
			}
			u.Thread = "replacement"
			retry := u.applyCriticalNotices()
			if delivered {
				if retry != nil || len(u.view.Entries) != 0 {
					t.Fatal("delivered progress returned in replacement transcript")
				}
				return
			}
			if retry == nil {
				t.Fatal("pending progress did not retry")
			}
			rows, _ := u.testMainFrame(80, 12, 0)
			if !strings.Contains(ansi.Strip(strings.Join(rows, "\n")), "pending cleanup plan") {
				t.Fatal("replacement transcript did not paint pending progress")
			}
			retry.finish(true)
			if len(u.view.Entries) != 1 || len(u.issues.Notices.Pending()) != 0 {
				t.Fatalf("retry: entries=%d pending=%v", len(u.view.Entries), u.issues.Notices.Pending())
			}
		})
	}
}

func TestNativeCriticalNoticeMixedBatchDismissal(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.issues = testCriticalErrors()
	u.issues.AddNotice("", "capacity", "router error")
	u.issues.CleanupNotice("", "", "planning", "cleanup progress")
	delivery := u.applyCriticalNotices()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	u.testMainFrame(80, 3, 0)
	delivery.finish(true)
	if pending := u.issues.Notices.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "cleanup progress") {
		t.Fatalf("mixed batch acknowledged the wrong surface: %v", pending)
	}
	u.composerNoticeMouse(0, u.mainView.NoticeDismiss.X, u.mainView.NoticeDismiss.Y, false)
	retry := u.applyCriticalNotices()
	if u.feedback.Text != "" || len(u.view.Entries) != 1 {
		t.Fatal("dismissed error returned or progress duplicated")
	}
	u.testMainFrame(80, 12, 0)
	retry.finish(true)
	if len(u.issues.Notices.Pending()) != 0 {
		t.Fatal("visible progress remained pending")
	}
}
