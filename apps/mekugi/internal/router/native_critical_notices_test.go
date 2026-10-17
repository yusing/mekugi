package router

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

func TestNativeCriticalNoticesRenderInMainWithoutProviderMessages(t *testing.T) {
	activity := sessioncontrol.NewAudience(nil)
	if !activity.Observe("main", "", "/root", false) ||
		!activity.Observe("child", "main", "/root/worker", true) ||
		!activity.Observe("other", "", "/root", false) {
		t.Fatal("failed to establish thread ownership")
	}
	issues := testCriticalErrors()
	queueCritical(issues, "main")
	queueCritical(issues, "child")
	queueCritical(issues, "other")
	issues.AddNotice("", "capacity", "router-wide capacity warning")
	u, _ := newAppServerTestUI()
	u.issues = issues
	setTestAppServerIntegration(u, &observationFixture{activity: activity})
	u.view.Conversation = true
	u.testActivity(u.view).testAudience()

	first := u.applyCriticalNotices()
	if first == nil || len(first.errors.Messages) != 3 {
		t.Fatalf("native delivery = %v, want root, child, and router-wide", first)
	}

	if len(u.source.Entries) != 0 || !u.feedback.Alert {
		t.Fatal("router errors entered the transcript or missed the composer")
	}
	u.ensureShell()
	defer terminalTest(u).Diff.Close()
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
	if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dM", rect.X+terminalTest(u).Layout.Main.X+1, rect.Y+terminalTest(u).Layout.Main.Y+1)); err != nil {
		t.Fatal(err)
	}
	if terminalTest(u).Dialog == nil {
		t.Fatal("router errors did not open on click")
	}
	drawOutputDialog(terminalTest(u))
	if terminalTest(u).Dialog.Document.Text != before {
		t.Fatal("router details lost a scoped error")
	}
	if err := u.testPaint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	first.finish(terminalTest(u).Frame.Notices)
	if len(issues.Notices.Pending()) != 4 {
		t.Fatal("dialog acknowledged a hidden composer batch")
	}
	terminalTest(u).outputKey("q")
	first.finish(terminalsession.NoticeReceipt{}) // Simulate an unpainted terminal frame.
	retry := u.applyCriticalNotices()
	if retry == nil || len(retry.errors.Messages) != 3 || len(u.source.Entries) != 0 || u.feedback.Text != before {
		t.Fatalf("retry duplicated or lost entries: delivery=%v entries=%d", retry, len(u.source.Entries))
	}
	if err := u.testPaint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	retry.finish(terminalTest(u).Frame.Notices)
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
	activity := sessioncontrol.NewAudience(nil)
	activity.Observe("main", "", "/root", false)
	activity.Observe("child", "main", "/root/worker", true)
	setTestAppServerIntegration(u, &observationFixture{activity: activity})
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
	if delivery := other.applyCriticalNotices(); delivery != nil || len(other.source.Entries) != 0 {
		t.Fatal("another root claimed the storage notice")
	}

	owner, _ := newAppServerTestUI()
	owner.Thread = failure.Thread
	owner.issues = issues
	owner.view.Conversation = true
	owner.testActivity(owner.view).testAudience()
	delivery := owner.applyCriticalNotices()
	if delivery == nil || len(delivery.errors.Messages) != 1 || len(owner.source.Entries) != 0 || !owner.feedback.Alert {
		t.Fatalf("owning root did not receive the storage notice: delivery=%v entries=%d", delivery, len(owner.source.Entries))
	}
	rows, receipt := owner.testDeliveredMainFrame(t, 100, 12, 0)
	feed := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(feed, "could not retain the failure reference") {
		t.Fatalf("storage failure did not appear in the composer before shutdown: %s", feed)
	}
	delivery.finish(receipt)
	if len(issues.Notices.Pending()) != 0 {
		t.Fatalf("painted notice remained pending: %v", issues.Notices.Pending())
	}
}

func TestNativeCriticalNoticeComposerVisibility(t *testing.T) {
	for _, state := range []string{"status", "replaced", "identical replacement", "compact"} {
		t.Run(state, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.issues = testCriticalErrors()
			queueCritical(u.issues, u.Thread)
			delivery := u.applyCriticalNotices()
			claimedText := u.feedback.Text
			u.ensureShell()
			defer terminalTest(u).Diff.Close()
			switch state {
			case "status":
				u.statusPanel = u.newStatusReport()
			case "replaced":
				u.setNotice("another error", true)
			case "identical replacement":
				u.setNotice(claimedText, true)
			}
			height := 12
			if state == "compact" {
				height = 3
			}
			_, receipt := u.testDeliveredMainFrame(t, 80, height, 0)
			delivery.finish(receipt)
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
			u.sessionPresentation().ClosePanel()
			retry := u.applyCriticalNotices()
			if retry == nil || u.feedback.Text != claimedText {
				t.Fatal("hidden batch could not retry")
			}
			_, receipt = u.testDeliveredMainFrame(t, 80, 12, 0)
			retry.finish(receipt)
			if len(u.issues.Notices.Pending()) != 0 {
				t.Fatal("visible retry remained pending")
			}
			u.applyCriticalNotices().finish(terminalsession.NoticeReceipt{}) // Empty polls retain nil-safe acknowledgement.
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
			defer terminalTest(u).Diff.Close()
			height := 3
			if delivered {
				height = 12
			}
			_, receipt := u.testDeliveredMainFrame(t, 80, height, 0)
			first.finish(receipt)
			if got := len(u.issues.Notices.Pending()); (got == 0) != delivered {
				t.Fatalf("initial delivery: pending=%d delivered=%t", got, delivered)
			}
			if err := u.clearSessionPresentation(); err != nil {
				t.Fatal(err)
			}
			u.Thread = "replacement"
			retry := u.applyCriticalNotices()
			if delivered {
				if retry != nil || len(u.source.Entries) != 0 {
					t.Fatal("delivered progress returned in replacement transcript")
				}
				return
			}
			if retry == nil {
				t.Fatal("pending progress did not retry")
			}
			rows, receipt := u.testDeliveredMainFrame(t, 80, 12, 0)
			if !strings.Contains(ansi.Strip(strings.Join(rows, "\n")), "pending cleanup plan") {
				t.Fatal("replacement transcript did not paint pending progress")
			}
			retry.finish(receipt)
			if len(u.source.Entries) != 1 || len(u.issues.Notices.Pending()) != 0 {
				t.Fatalf("retry: entries=%d pending=%v", len(u.source.Entries), u.issues.Notices.Pending())
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
	defer terminalTest(u).Diff.Close()
	_, receipt := u.testDeliveredMainFrame(t, 80, 3, 0)
	delivery.finish(receipt)
	if pending := u.issues.Notices.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "cleanup progress") {
		t.Fatalf("mixed batch acknowledged the wrong surface: %v", pending)
	}
	if err := terminalTest(u).mouse(fmt.Sprintf("\x1b[<0;%d;%dM", u.mainView.NoticeDismiss.X+terminalTest(u).Layout.Main.X+1, u.mainView.NoticeDismiss.Y+terminalTest(u).Layout.Main.Y+1)); err != nil {
		t.Fatal(err)
	}
	retry := u.applyCriticalNotices()
	if u.feedback.Text != "" || len(u.source.Entries) != 1 {
		t.Fatal("dismissed error returned or progress duplicated")
	}
	_, receipt = u.testDeliveredMainFrame(t, 80, 12, 0)
	retry.finish(receipt)
	if len(u.issues.Notices.Pending()) != 0 {
		t.Fatal("visible progress remained pending")
	}
}

func TestNativeNoticeReceiptKeepsPaintedIdentity(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.issues = testCriticalErrors()
	queueCritical(u.issues, u.Thread)
	delivery := u.applyCriticalNotices()
	_, receipt := u.testDeliveredMainFrame(t, 80, 12, 0)
	u.setNotice("later feedback", true)
	u.statusPanel = u.newStatusReport()
	u.settlePresentation()
	delivery.finish(receipt)
	if len(u.issues.Notices.Pending()) != 0 {
		t.Fatal("later UI state invalidated an already delivered notice")
	}
}

func TestNativeNoticeChildFrameCannotAcknowledgeMainProgress(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.issues.CleanupNotice("", "", "planning", "Main cleanup progress")
	delivery := u.applyCriticalNotices()
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
	view := u.sessionPresentation().View()
	if !u.displaySession(sessionpresentation.DisplayIntent{Version: view.Version, Thread: sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}}) {
		t.Fatal(u.feedback.Text)
	}
	if err := u.testPaint(io.Discard, 100, 24); err != nil {
		t.Fatal(err)
	}
	delivery.finish(terminalTest(u).Frame.Notices)
	if len(u.issues.Notices.Pending()) != 1 {
		t.Fatal("child transcript acknowledged hidden Main progress")
	}
	view = u.sessionPresentation().View()
	if !u.displaySession(sessionpresentation.DisplayIntent{Version: view.Version, Thread: view.Main}) {
		t.Fatal(u.feedback.Text)
	}
	retry := u.applyCriticalNotices()
	if err := u.testPaint(io.Discard, 100, 24); err != nil {
		t.Fatal(err)
	}
	retry.finish(terminalTest(u).Frame.Notices)
	if len(u.issues.Notices.Pending()) != 0 || len(u.source.Entries) != 1 {
		t.Fatal("visible Main retry was lost or duplicated")
	}
}
