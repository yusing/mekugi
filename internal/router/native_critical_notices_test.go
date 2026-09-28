package router

import (
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
	if first == nil || len(first.notices) != 3 {
		t.Fatalf("native delivery = %v, want root, child, and router-wide", first)
	}
	if len(u.view.entries) != 3 {
		t.Fatalf("Main entries = %d, want 3", len(u.view.entries))
	}
	for _, entry := range u.view.entries {
		if entry.Agent != "Main" || entry.Kind != "error" || entry.native != nil {
			t.Fatalf("notice became provider-authored message: %+v", entry)
		}
	}
	feed := ansi.Strip(strings.Join(u.view.renderFeed(100, 30).lines, "\n"))
	if !strings.Contains(feed, "/root/worker: ") || !strings.Contains(feed, "router-wide capacity warning") ||
		strings.Count(feed, "Enable supported tools.") != 2 {
		t.Fatalf("Main feed omitted or mis-scoped native notices: %s", feed)
	}
	first.finish(false) // Simulate an unpainted terminal frame.
	retry := u.applyCriticalNotices()
	if retry == nil || len(retry.notices) != 3 || len(u.view.entries) != 3 {
		t.Fatalf("retry duplicated or lost entries: delivery=%v entries=%d", retry, len(u.view.entries))
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
	if delivery == nil || len(delivery.notices) != 1 || len(owner.view.entries) != 1 {
		t.Fatalf("owning root did not receive the storage notice: delivery=%v entries=%d", delivery, len(owner.view.entries))
	}
	feed := ansi.Strip(strings.Join(owner.view.renderFeed(100, 20).lines, "\n"))
	if !strings.Contains(feed, "could not retain the failure reference") {
		t.Fatalf("storage failure did not appear in Main before shutdown: %s", feed)
	}
	delivery.finish(true)
	if len(issues.Pending()) != 0 {
		t.Fatalf("painted notice remained pending: %v", issues.Pending())
	}
}
