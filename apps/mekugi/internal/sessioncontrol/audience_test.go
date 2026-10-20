// Source: apps/mekugi/internal/router/subagent_activity_test.go@cf02d9b2.
package sessioncontrol

import (
	"strings"
	"testing"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestNativeActivityIsolatedByAncestryAndIdentity(t *testing.T) {
	a := NewAudience(nil)
	a.Attach("root-a")
	if !a.Observe("root-a", "", "/root", false) || !a.Observe("root-b", "", "/root", false) ||
		!a.Observe("child-a", "root-a", "/root/a", true) || !a.Observe("child-b", "root-b", "/root/b", true) {
		t.Fatal("valid activity identity rejected")
	}
	a.Collect(activitypresentation.Supplement{Thread: "child-a", Source: "reply-a", Kind: "reply", Text: "Message from A"})
	a.Collect(activitypresentation.Supplement{Thread: "child-b", Source: "reply-b", Kind: "reply", Text: "Message from B"})
	audience, entries := a.Take("root-a")
	if len(entries) != 1 || audience.Threads[entries[0].Thread] != "/root/a" || !strings.Contains(entries[0].Text, "Message from A") {
		t.Fatalf("cross-root activity leakage: %+v", entries)
	}
	if a.Observe("child-a", "root-b", "/root/a", true) {
		t.Fatal("conflicted identity accepted")
	}
	a.Collect(activitypresentation.Supplement{Thread: "child-a", Source: "later", Kind: "reply", Text: "Must not appear"})
	if _, got := a.Take("root-a"); len(got) != 0 {
		t.Fatalf("conflicted activity retained: %+v", got)
	}
}

func TestNativeActivityDeduplicatesRepliesAndRetainsAssignments(t *testing.T) {
	a := NewAudience(nil)
	a.Attach("root")
	a.Observe("root", "", "/root", false)
	a.Observe("child", "root", "/root/reviewer", true)
	a.Collect(activitypresentation.Supplement{Thread: "child", Source: "same-reply", Kind: "reply", Text: "Full directed reply"})
	a.Collect(activitypresentation.Supplement{Thread: "child", Source: "same-reply", Kind: "reply", Text: "Full directed reply"})
	assignment := &activitypresentation.SupplementAssignment{ID: "task-1", From: "/root", To: "/root/reviewer", Text: "Review complete behavior."}
	a.Collect(activitypresentation.Supplement{Thread: "child", Source: "start-1", Kind: "start", Text: "Started", Assignment: assignment})
	_, got := a.Take("root")
	if len(got) != 2 || got[0].Kind != "reply" || got[1].Assignment == nil || got[1].Assignment.Text != assignment.Text {
		t.Fatalf("native reply or assignment lost: %+v", got)
	}
}

func TestNativeActivityOmitsHostOwnedToolSpeech(t *testing.T) {
	a := NewAudience(nil)
	a.Attach("root")
	a.Observe("root", "", "/root", false)
	a.Observe("child", "root", "/root/reviewer", true)
	a.Collect(activitypresentation.Supplement{Thread: "child", Source: "tool", Kind: "tool", Text: "Read private.go"})
	a.Collect(activitypresentation.Supplement{Thread: "child", Source: "commentary", Kind: "commentary", Text: "Assistant speech"})
	_, got := a.Take("root")
	if len(got) != 0 {
		t.Fatalf("host-owned speech duplicated: %+v", got)
	}
}

func TestRepetitionLookupRetainsAuthenticatedOrigin(t *testing.T) {
	a := NewAudience(nil)
	a.Attach("root")
	a.Observe("child", "root", "/root/reader", true)
	a.Collect(activitypresentation.Supplement{Thread: "child", Source: "receipt", Kind: "output_dedupe", CallID: "snapshot", Text: "repeated"})
	a.Take("root")
	if a.Repetition("root", "child", "snapshot") != "repeated" {
		t.Fatal("queue drain lost retained annotation")
	}
	a.Detach()
	if a.Repetition("root", "child", "snapshot") != "" {
		t.Fatal("detached audience exposed annotation")
	}
	a.Attach("other")
	if a.Repetition("other", "child", "snapshot") != "" {
		t.Fatal("foreign root exposed annotation")
	}
	a.Attach("root")
	if a.Repetition("root", "child", "snapshot") != "repeated" {
		t.Fatal("reattachment lost annotation")
	}
	a.Invalidate("child")
	if a.Repetition("root", "child", "snapshot") != "" {
		t.Fatal("invalidated ancestry exposed annotation")
	}
	a.Close()
	if a.Repetition("root", "child", "snapshot") != "" {
		t.Fatal("closed audience exposed annotation")
	}
}
