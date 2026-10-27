// Source: apps/mekugi/internal/router/subagent_activity_test.go@cf02d9b2.
package sessioncontrol

import (
	"fmt"
	"strings"
	"testing"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestAudienceResolutionMatchesPointQueries(t *testing.T) {
	a := NewAudience(nil)
	a.Attach("root")
	a.Observe("child", "missing", "/root/child", true)
	a.Observe("cycle-a", "cycle-b", "/root/a", true)
	a.Observe("cycle-b", "cycle-a", "/root/b", true)
	check := func() {
		t.Helper()
		for _, root := range []string{"root", "missing", "child", "cycle-a", "unknown"} {
			got, valid := a.Snapshot(root)
			wantValid := a.threads[root] == nil || a.rootLocked(root) == root
			if valid != wantValid {
				t.Fatalf("root %q validity = %t, want %t", root, valid, wantValid)
			}
			for thread, node := range a.threads {
				name, present := got.Threads[thread]
				if want := wantValid && a.rootLocked(thread) == root; present != want || present && name != node.name {
					t.Fatalf("root %q thread %q: %q, present=%t, want=%t", root, thread, name, present, want)
				}
			}
		}
	}
	check()
	a.Observe("missing", "root", "/root/parent", true)
	check()
	a.AttachRuntime("missing")
	check()
	a.BorrowSide("child")
	check()
	a.Detach()
	check()
	a.Invalidate("missing")
	check()
}

func BenchmarkAudienceIdleTake(b *testing.B) {
	for _, count := range []int{100, 1000} {
		for _, chain := range []bool{false, true} {
			b.Run(fmt.Sprintf("nodes=%d/chain=%t", count, chain), func(b *testing.B) {
				a := NewAudience(nil)
				a.Attach("root")
				parent := "root"
				for i := range count {
					id := fmt.Sprint(i)
					a.Observe(id, parent, "/root/child", true)
					if chain {
						parent = id
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					got, _ := a.Take("root")
					if len(got.Threads) != count+1 {
						b.Fatal("lost descendants")
					}
				}
			})
		}
	}
}

func TestNativeActivityIsolatedByAncestryAndIdentity(t *testing.T) {
	a := NewAudience(nil)
	a.Attach("root-a")
	if !a.Observe("root-a", "", "/root", false) || !a.Observe("root-b", "", "/root", false) ||
		!a.Observe("child-a", "root-a", "/root/a", true) || !a.Observe("child-b", "root-b", "/root/b", true) {
		t.Fatal("valid activity identity rejected")
	}
	a.Collect(activitypresentation.Supplement{Thread: "child-a", Source: "reply-a", Kind: "error", Text: "Message from A"})
	a.Collect(activitypresentation.Supplement{Thread: "child-b", Source: "reply-b", Kind: "error", Text: "Message from B"})
	audience, entries := a.Take("root-a")
	if len(entries) != 1 || audience.Threads[entries[0].Thread] != "/root/a" || !strings.Contains(entries[0].Text, "Message from A") {
		t.Fatalf("cross-root activity leakage: %+v", entries)
	}
	if a.Observe("child-a", "root-b", "/root/a", true) {
		t.Fatal("conflicted identity accepted")
	}
	a.Collect(activitypresentation.Supplement{Thread: "child-a", Source: "later", Kind: "error", Text: "Must not appear"})
	if _, got := a.Take("root-a"); len(got) != 0 {
		t.Fatalf("conflicted activity retained: %+v", got)
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
