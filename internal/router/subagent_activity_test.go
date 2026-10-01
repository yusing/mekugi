package router

import (
	"strings"
	"testing"
)

func (a *subagentActivity) collect(thread, source, kind, text string) {
	a.collectEvent(activityEvent{thread: thread, source: source, kind: kind, text: text})
}

func TestNativeActivityIsolatedByAncestryAndIdentity(t *testing.T) {
	a := newSubagentActivity()
	a.attachNativePane("root-a")
	if !a.observe("root-a", "", "/root", false) || !a.observe("root-b", "", "/root", false) ||
		!a.observe("child-a", "root-a", "/root/a", true) || !a.observe("child-b", "root-b", "/root/b", true) {
		t.Fatal("valid activity identity rejected")
	}
	a.collect("child-a", "reply-a", "reply", "Message from A")
	a.collect("child-b", "reply-b", "reply", "Message from B")
	entries := a.takeNativeActivity("root-a")
	if len(entries) != 1 || entries[0].Agent != "/root/a" || !strings.Contains(entries[0].Text, "Message from A") {
		t.Fatalf("cross-root activity leakage: %+v", entries)
	}
	if a.observe("child-a", "root-b", "/root/a", true) {
		t.Fatal("conflicted identity accepted")
	}
	a.collect("child-a", "later", "reply", "Must not appear")
	if got := a.takeNativeActivity("root-a"); len(got) != 0 {
		t.Fatalf("conflicted activity retained: %+v", got)
	}
}

func TestNativeActivityDeduplicatesRepliesAndRetainsAssignments(t *testing.T) {
	a := newSubagentActivity()
	a.attachNativePane("root")
	a.observe("root", "", "/root", false)
	a.observe("child", "root", "/root/reviewer", true)
	a.collect("child", "same-reply", "reply", "Full directed reply")
	a.collect("child", "same-reply", "reply", "Full directed reply")
	assignment := &activityAssignment{id: "task-1", from: "/root", to: "/root/reviewer", text: "Review complete behavior."}
	a.collectEvent(activityEvent{thread: "child", source: "start-1", kind: "start", text: "Started", assignment: assignment})
	got := a.takeNativeActivity("root")
	if len(got) != 2 || got[0].Kind != "reply" || got[1].assignment == nil || got[1].assignment.text != assignment.text {
		t.Fatalf("native reply or assignment lost: %+v", got)
	}
}

func TestNativeActivityOmitsHostOwnedToolSpeech(t *testing.T) {
	a := newSubagentActivity()
	a.attachNativePane("root")
	a.observe("root", "", "/root", false)
	a.observe("child", "root", "/root/reviewer", true)
	a.collect("child", "tool", "tool", "Read private.go")
	a.collect("child", "commentary", "commentary", "Assistant speech")
	got := a.takeNativeActivity("root")
	if len(got) != 0 {
		t.Fatalf("host-owned speech duplicated: %+v", got)
	}
}
