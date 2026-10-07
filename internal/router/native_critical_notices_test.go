package router

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestNativeCriticalNoticesRenderInMainWithoutProviderMessages(t *testing.T) {
	activity := newSubagentActivity()
	if !activity.observe("main", "", "/root", false) ||
		!activity.observe("child", "main", "/root/worker", true) ||
		!activity.observe("other", "", "/root", false) {
		t.Fatal("failed to establish thread ownership")
	}
	issues := NewCriticalErrors()
	queueCritical(issues, "main")
	queueCritical(issues, "child")
	queueCritical(issues, "other")
	issues.addNotice("", "capacity", "router-wide capacity warning")
	u, _ := newAppServerTestUI()
	u.issues = issues
	u.proxy = &mekugiProxy{activity: activity}
	u.view.conversation = true

	first := u.applyCriticalNotices()
	if first == nil || len(first.errors.notices) != 3 {
		t.Fatalf("native delivery = %v, want root, child, and router-wide", first)
	}

	if len(u.view.entries) != 0 || !u.noticeAlert {
		t.Fatal("router errors entered the transcript or missed the composer")
	}
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	if err := u.paint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.notice, "/root/worker: ") || !strings.Contains(u.notice, "router-wide capacity warning") || strings.Count(u.notice, "Enable supported tools.") != 2 {
		t.Fatalf("composer omitted or mis-scoped native notices: %s", u.notice)
	}
	before := u.notice

	rect := u.noticeDetails
	if rect.w == 0 {
		t.Fatal("router batch has no details target")
	}
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", rect.x+u.shell.layout.codex.x+1, rect.y+u.shell.layout.codex.y+1)); err != nil {
		t.Fatal(err)
	}
	if u.shell.output == nil {
		t.Fatal("router errors did not open on click")
	}
	drawOutputDialog(u.shell)
	if u.shell.output.laid.Text != before {
		t.Fatal("router details lost a scoped error")
	}
	first.finish(true)
	if len(issues.Pending()) != 4 {
		t.Fatal("dialog acknowledged a hidden composer batch")
	}
	u.shell.outputKey("q")
	first.finish(false) // Simulate an unpainted terminal frame.
	retry := u.applyCriticalNotices()
	if retry == nil || len(retry.errors.notices) != 3 || len(u.view.entries) != 0 || u.notice != before {
		t.Fatalf("retry duplicated or lost entries: delivery=%v entries=%d", retry, len(u.view.entries))
	}
	if err := u.paint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	retry.finish(true)
	if len(issues.Pending()) != 1 || u.applyCriticalNotices() != nil {
		t.Fatalf("paint acknowledged unrelated root or queued duplicate: %v", issues.Pending())
	}
}

func TestFailureStorageNoticeReachesOwningNativeRootBeforeShutdown(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(stateHome, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateHome)
	issues := NewCriticalErrors()
	issues.persistFailures = true
	failure := failureStoreTestFinalization("native-storage-failure")
	issues.persistFailure(failure)

	other, _ := newAppServerTestUI()
	other.thread = "unrelated-root"
	other.issues = issues
	if delivery := other.applyCriticalNotices(); delivery != nil || len(other.view.entries) != 0 {
		t.Fatal("another root claimed the storage notice")
	}

	owner, _ := newAppServerTestUI()
	owner.thread = failure.threadID
	owner.issues = issues
	owner.view.conversation = true
	delivery := owner.applyCriticalNotices()
	if delivery == nil || len(delivery.errors.notices) != 1 || len(owner.view.entries) != 0 || !owner.noticeAlert {
		t.Fatalf("owning root did not receive the storage notice: delivery=%v entries=%d", delivery, len(owner.view.entries))
	}
	rows, _ := owner.mainFrame(100, 12, 0)
	feed := ansi.Strip(strings.Join(rows, "\n"))
	if !strings.Contains(feed, "could not retain the failure reference") {
		t.Fatalf("storage failure did not appear in the composer before shutdown: %s", feed)
	}
	delivery.finish(true)
	if len(issues.Pending()) != 0 {
		t.Fatalf("painted notice remained pending: %v", issues.Pending())
	}
}

func TestNativeCriticalNoticeComposerVisibility(t *testing.T) {
	for _, state := range []string{"status", "replaced", "compact"} {
		t.Run(state, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.issues = NewCriticalErrors()
			queueCritical(u.issues, u.thread)
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
			u.mainFrame(80, height, 0)
			delivery.finish(true)
			if state == "compact" {
				if u.mainContentPainted || len(u.issues.Pending()) != 0 {
					t.Fatal("compact composer did not deliver independently of transcript")
				}
				return
			}
			if len(u.issues.Pending()) != 1 {
				t.Fatal("hidden or replaced notice was acknowledged")
			}
			u.statusPanel = nil
			retry := u.applyCriticalNotices()
			if retry == nil || u.notice != delivery.composerText {
				t.Fatal("hidden batch could not retry")
			}
			u.mainFrame(80, 12, 0)
			retry.finish(true)
			if len(u.issues.Pending()) != 0 {
				t.Fatal("visible retry remained pending")
			}
			u.applyCriticalNotices().finish(false) // Empty polls retain nil-safe acknowledgement.
		})
	}
}

func TestNativeCriticalNoticeMixedBatchDismissal(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.issues = NewCriticalErrors()
	u.issues.addNotice("", "capacity", "router error")
	u.issues.addNotice("", "storage_cleanup_planning", "cleanup progress")
	delivery := u.applyCriticalNotices()
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	u.mainFrame(80, 3, 0)
	delivery.finish(true)
	if pending := u.issues.Pending(); len(pending) != 1 || !strings.Contains(pending[0], "cleanup progress") {
		t.Fatalf("mixed batch acknowledged the wrong surface: %v", pending)
	}
	u.composerNoticeMouse(0, u.noticeDismiss.x, u.noticeDismiss.y, false)
	retry := u.applyCriticalNotices()
	if u.notice != "" || len(u.view.entries) != 1 {
		t.Fatal("dismissed error returned or progress duplicated")
	}
	u.mainFrame(80, 12, 0)
	retry.finish(true)
	if len(u.issues.Pending()) != 0 {
		t.Fatal("visible progress remained pending")
	}
}
