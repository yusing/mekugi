package router

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubagentActivityAncestryReplayAndOrder(t *testing.T) {
	a := newSubagentActivity()
	a.observe("root-a", "", "/root", false)
	a.observe("root-b", "", "/root", false)
	a.observe("nested", "child", "/root/alpha/nested", true)
	a.collect("nested", "one", "operation", "Reading.")
	if got := a.drain("root-a", time.Now(), maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatal("unknown ancestry delivered", got)
	}
	a.observe("child", "root-a", "/root/alpha", true)
	a.collect("nested", "two", "reply", "Reply received: evidence.")
	a.collect("nested", "three", "operation", "Checking.")
	a.collect("nested", "three", "operation", "duplicate")
	if len(a.drain("root-b", time.Now(), maxCommentaryPublicationBytes)) != 0 {
		t.Fatal("cross-root delivery")
	}
	messages := a.drain("root-a", time.Now(), maxCommentaryPublicationBytes)
	if len(messages) != 2 || !strings.Contains(commentaryText(t, messages[0]), "Reply received") || !strings.Contains(commentaryText(t, messages[1]), "Checking.") {
		t.Fatal(messages)
	}
	if commentaryText(t, messages[1]) != "[`/root/alpha/nested`] Checking." {
		t.Fatal(messages)
	}
	if len(a.drain("root-a", time.Now(), maxCommentaryPublicationBytes)) != 0 {
		t.Fatal("duplicate delivery")
	}
	original := assistantCommentaryMessage("original-child-message", "result")
	fields := map[string]json.RawMessage{"input": mustMarshalJSON(append(messages, original))}
	a.stripInput(fields)
	var remaining []map[string]json.RawMessage
	_ = json.Unmarshal(fields["input"], &remaining)
	if len(remaining) != 1 || jsonString(remaining[0], "id") != "original-child-message" {
		t.Fatal(string(fields["input"]))
	}
}

func TestSubagentActivityCapacityExpiryConflictAndConcurrentRoots(t *testing.T) {
	a := newSubagentActivity()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			root, child := fmt.Sprint("root", i), fmt.Sprint("child", i)
			a.observe(root, "", "/root", false)
			a.observe(child, root, "/root/worker", true)
			for j := range 100 {
				a.collect(child, fmt.Sprint(j), "reply", fmt.Sprint(i))
			}
			messages := a.drain(root, time.Time{}, maxCommentaryPublicationBytes)
			if len(messages) != maxCommentaryEventsPerRoute {
				t.Errorf("root %d: %d notices", i, len(messages))
			}
			for _, m := range messages {
				if commentaryText(t, m) != "[`/root/worker`] "+fmt.Sprint(i) {
					t.Error("cross-root text", m)
				}
			}
		})
	}
	wg.Wait()
	a.observe("conflict", "root0", "/root/c", true)
	a.collect("conflict", "one", "operation", "queued")
	a.observe("conflict", "root1", "/root/c", true)
	if len(a.drain("root0", time.Time{}, maxCommentaryPublicationBytes)) != 0 || len(a.drain("root1", time.Time{}, maxCommentaryPublicationBytes)) != 0 {
		t.Fatal("reparented thread delivered")
	}
	a.mu.Lock()
	for i := range a.events {
		a.events[i].observed = time.Now().Add(-2 * commentaryRouteTTL)
	}
	a.mu.Unlock()
	a.drain("root0", time.Time{}, maxCommentaryPublicationBytes)
	if len(a.events) != 0 {
		t.Fatal("expired queue retained")
	}
	a.observe("large", "root0", "/root/large", true)
	a.collect("large", "large", "reply", strings.Repeat("x", maxCommentaryPublicationBytes))
	if len(a.events) != 0 {
		t.Fatal("rendered byte budget exceeded")
	}
}

func TestActivityBudgetPreservesNoticeOrderAndReplayAfterExpiry(t *testing.T) {
	a := newSubagentActivity()
	a.observe("r", "", "/root", false)
	a.observe("c", "r", "/root/c", true)
	a.collect("c", "1", "reply", "first notice")
	a.collect("c", "2", "reply", "x")
	if len(a.drain("r", time.Time{}, len("[`/root/c`] x"))) != 0 {
		t.Fatal("later notice overtook blocked earlier notice")
	}
	delivered := a.drain("r", time.Time{}, maxCommentaryPublicationBytes)
	if len(delivered) != 2 {
		t.Fatal(delivered)
	}
	a.mu.Lock()
	a.expireLocked(time.Now().Add(2 * commentaryRouteTTL))
	a.mu.Unlock()
	fields := map[string]json.RawMessage{"input": mustMarshalJSON(delivered)}
	a.stripInput(fields)
	if string(fields["input"]) != "[]" {
		t.Fatal("expiry lost replay provenance", string(fields["input"]))
	}
	a.close()
	a.observe("new", "", "/root", false)
	a.collect("c", "3", "reply", "late")
	if len(a.events) != 0 || len(a.threads) != 0 {
		t.Fatal("closed collector accepted activity")
	}
}

func TestDeferredBoundarySizedActivityPreservesLaterNotice(t *testing.T) {
	a := newSubagentActivity()
	a.observe("r", "", "/root", false)
	a.observe("c", "r", "/root/c", true)
	a.collect("c", "large", "reply", strings.Repeat("x", maxCommentaryPublicationBytes-len("[`/root/c`] ")))
	a.collect("c", "small", "reply", "later notice")
	if len(a.events) != 2 {
		t.Fatal("boundary-sized notice was not admitted")
	}
	messages := a.drain("r", time.Now(), maxCommentaryPublicationBytes)
	if len(messages) != 1 || len(commentaryText(t, messages[0])) != maxCommentaryPublicationBytes {
		t.Fatal("boundary-sized deferred notice was not delivered")
	}
	messages = a.drain("r", time.Now(), maxCommentaryPublicationBytes)
	if len(messages) != 1 || commentaryText(t, messages[0]) != "[`/root/c`] later notice" {
		t.Fatal("later notice was not preserved")
	}
	if len(a.events) != 0 {
		t.Fatal("delivered notices retained")
	}
}

func TestCriticalErrorProjectionUsesOriginDespiteSharedSession(t *testing.T) {
	a := newSubagentActivity()
	a.observe("root-a", "", "/root", false)
	a.observe("root-b", "", "/root", false)
	a.observe("child-a", "root-a", "/root/a", true)
	a.observe("child-b", "root-b", "/root/b", true)
	issues := NewCriticalErrors()
	record := func(thread string, outcome requestOutcome) {
		issues.record(&requestFinalization{sessionID: "shared-session", failurePhase: requestFailurePrepare,
			observation:           requestObservation{outcome: outcome},
			observeCriticalNotice: func(source, text string) { a.collect(thread, source, "error", text) },
		}, incompatibleRequest("unsupported_tool_catalog", "Enable supported tools."))
	}
	record("child-a", requestOutcomeFailed)
	record("child-b", requestOutcomeCompleted)
	if len(a.drain("root-b", time.Time{}, maxCommentaryPublicationBytes)) != 0 {
		t.Fatal("successful sibling inherited another thread's failure")
	}
	projected := a.drain("root-a", time.Time{}, maxCommentaryPublicationBytes)
	if len(projected) != 1 || !strings.Contains(commentaryText(t, projected[0]), "[`/root/a`] Mekugi") {
		t.Fatal(projected)
	}
	record("child-b", requestOutcomeFailed)
	if len(a.drain("root-b", time.Time{}, maxCommentaryPublicationBytes)) != 1 {
		t.Fatal("new sibling failure was suppressed by another thread's session notice")
	}
	if len(issues.Pending()) != 1 || len(issues.transform("shared-session", true).messages) != 1 {
		t.Fatal("root copy consumed or changed child notice deduplication")
	}
}

func TestToolActivityGroupsSamePathWithoutWaiting(t *testing.T) {
	a := newSubagentActivity()
	a.observe("r", "", "/root", false)
	a.observe("c", "r", "/root/c", true)
	for index := range 5 {
		a.collect("c", fmt.Sprint(index), "tool", "Read "+commentaryCode(fmt.Sprint(index)))
	}
	messages := a.drain("r", time.Time{}, maxCommentaryPublicationBytes)
	if len(messages) != 1 {
		t.Fatalf("group count: %d", len(messages))
	}
	for index, want := range []string{
		"[`/root/c`] Read `0` `1` `2` `3` `4`",
	} {
		if got := commentaryText(t, messages[index]); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	for index := range 5 {
		a.collect("c", fmt.Sprint(index), "tool", "Read `duplicate`")
	}
	if got := a.drain("r", time.Time{}, maxCommentaryPublicationBytes); len(got) != 0 {
		t.Fatal("grouped sources repeated")
	}
	fields := map[string]json.RawMessage{"input": mustMarshalJSON(messages)}
	a.stripInput(fields)
	if string(fields["input"]) != "[]" {
		t.Fatal("grouped messages entered replay")
	}
	a.collect("c", "single", "tool", "Read `single`")
	if got := a.drain("r", time.Time{}, maxCommentaryPublicationBytes); len(got) != 1 || commentaryText(t, got[0]) != "[`/root/c`] Read `single`" {
		t.Fatal("single call wrapped or held waiting for a group")
	}
}

func TestToolActivityGroupingBoundariesAndBudget(t *testing.T) {
	a := newSubagentActivity()
	a.observe("r", "", "/root", false)
	a.observe("c", "r", "/root/c", true)
	a.observe("d", "r", "/root/d", true)
	for index, item := range []struct{ thread, kind, text string }{
		{"c", "tool", "Read `a`"},
		{"d", "tool", "Read `b`"},
		{"c", "tool", "Read `c`"},
		{"c", "tool", "Search `d`"},
		{"c", "reply", "notice"},
		{"c", "tool", "Search `e`"},
	} {
		a.collect(item.thread, fmt.Sprint(index), item.kind, item.text)
	}
	if got := a.drain("r", time.Time{}, maxCommentaryPublicationBytes); len(got) != 5 {
		t.Fatalf("group crossed a boundary: %d", len(got))
	}
	for _, name := range []string{"first", "second"} {
		a.collect("c", name, "tool", "Read "+commentaryCode(name))
	}
	if got := a.drain("r", time.Time{}, len("[`/root/c`] Read `first`")); len(got) != 1 ||
		commentaryText(t, got[0]) != "[`/root/c`] Read `first`" {
		t.Fatal("grouping blocked a deliverable first item")
	}
	if got := a.drain("r", time.Time{}, maxCommentaryPublicationBytes); len(got) != 1 ||
		commentaryText(t, got[0]) != "[`/root/c`] Read `second`" {
		t.Fatal("budget lost the remaining item")
	}
}

func TestToolActivityGroupingPreservesMultilineSource(t *testing.T) {
	a := newSubagentActivity()
	a.observe("r", "", "/root", false)
	a.observe("c", "r", "/root/c", true)
	a.collect("c", "one", "tool", "Run\n```\necho a\n  echo b\n```")
	a.collect("c", "two", "tool", "Run\n```\necho c\n  echo d\n```")
	messages := a.drain("r", time.Time{}, maxCommentaryPublicationBytes)
	want := "[`/root/c`] Run\n```\necho a\n  echo b\n```\n```\necho c\n  echo d\n```"
	if len(messages) != 1 || commentaryText(t, messages[0]) != want {
		t.Fatalf("multiline grouping: %v", messages)
	}
}

func TestToolActivityCollapsesActionsWithinAndAcrossCalls(t *testing.T) {
	for _, tc := range []struct {
		name  string
		calls []string
		want  string
	}{
		{
			name:  "one call",
			calls: []string{"Read `a`\n\nRead `b`"},
			want:  "[`/root/c`] Read `a` `b`",
		},
		{
			name:  "mixed actions",
			calls: []string{"Read `a`", "Read `b`\n\nSearch `c`", "Search `d`", "Read `e`"},
			want:  "In `/root/c`\n\n- Read `a` `b`\n\n- Search `c` `d`\n\n- Read `e`",
		},
		{
			name:  "separate single-file edits",
			calls: []string{"Edit `a`\n```diff\n-old\n+new\n```", "Edit `b`\n```diff\n-before\n+after\n```"},
			want:  "[`/root/c`] Edit `a`\n```diff\n-old\n+new\n```\n`b`\n```diff\n-before\n+after\n```",
		},
		{
			name:  "created files",
			calls: []string{"Create `a` +1 -0", "Create `b` +2 -0"},
			want:  "[`/root/c`] Create `a` +1 -0 `b` +2 -0",
		},
		{
			name:  "source fences",
			calls: []string{"Run\n````bash\nprintf '```'\n\n# Run\n````", "Run\n```python\nprint(1)\n```"},
			want:  "[`/root/c`] Run\n````bash\nprintf '```'\n\n# Run\n````\n```python\nprint(1)\n```",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newSubagentActivity()
			a.observe("r", "", "/root", false)
			a.observe("c", "r", "/root/c", true)
			for index, call := range tc.calls {
				a.collect("c", fmt.Sprint(index), "tool", call)
			}
			messages := a.drain("r", time.Time{}, maxCommentaryPublicationBytes)
			if len(messages) != 1 || commentaryText(t, messages[0]) != tc.want {
				t.Fatalf("collapsed display: %v", messages)
			}
		})
	}
}

func TestToolActivityGroupingKeepsDeferredKindsDistinct(t *testing.T) {
	a := newSubagentActivity()
	a.observe("r", "", "/root", false)
	a.observe("c", "r", "/root/c", true)
	a.collect("c", "old", "tool", "Read `old`")
	started := time.Now()
	a.events[0].observed = started.Add(-time.Second)
	a.collect("c", "new", "tool", "Read `new`")
	a.collect("c", "web", "tool", "Search web\n`query`")
	a.collect("c", "files", "tool", "Search files\n`query`")
	a.collect("c", "search", "tool", "Search `query`")
	messages := a.drain("r", started, maxCommentaryPublicationBytes)
	if len(messages) != 2 {
		t.Fatalf("action/deferred boundary lost: %d", len(messages))
	}
	if got := commentaryText(t, messages[0]); got != "[`/root/c`] Read `old`" {
		t.Fatalf("deferred single-item formatting: %q", got)
	}
}

func TestToolActivityGroupingPreservesMixedFencedOperations(t *testing.T) {
	a := newSubagentActivity()
	a.observe("r", "", "/root", false)
	a.observe("c", "r", "/root/c", true)
	mixed := toolActivityShell("hgrep -F 'alpha\nbeta' a\ncat b")
	a.collect("c", "mixed", "tool", mixed)
	a.collect("c", "search", "tool", toolActivityShell("hgrep gamma c"))
	messages := a.drain("r", time.Time{}, maxCommentaryPublicationBytes)
	if len(messages) != 1 || commentaryText(t, messages[0]) != "In `/root/c`\n\n- Search\n  ```\n  -F 'alpha\n  beta' a\n  ```\n\n- Read `b`\n\n- Search `gamma c`" {
		t.Fatalf("mixed operation rendering: %v", messages)
	}
}
