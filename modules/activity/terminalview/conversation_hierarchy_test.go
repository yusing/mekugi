package terminalview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotConversationHierarchy(t *testing.T) {
	const parent, child, sibling = "/root/duplicate_edits", "/root/duplicate_edits/correctness", "/root/other"
	for _, caller := range []string{"/root", parent} {
		for _, restored := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restored=%t", caller, restored), func(t *testing.T) {
				v := NewView()
				v.Conversation, v.ChildrenOnly = true, caller != "/root"
				v.Painter.Theme = diffrender.DarkTheme
				now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local)
				v.Clock = func() time.Time { return now }
				var entries []presentation.Entry
				add := func(kind, from, to, body string) {
					seq := uint64(len(entries) + 1)
					agent := from
					if agent == "/root" {
						agent = "Main"
					}
					if caller != "/root" && kind != "final" {
						agent = caller
					} // Child feed's admitted owner.
					item := presentation.ItemContent{Facts: presentation.Facts{Seq: seq, Agent: agent, Kind: kind, Text: body, Activity: seq + 100, Observed: now.Add(time.Duration(seq) * time.Second), Restored: restored}}
					directed := &presentation.DirectedContent{From: from, To: to, Text: body}
					switch kind {
					case "start", "assignment":
						item.Assignment, item.AssignmentTo, item.AssignmentText, item.Facts.Assignment = directed, to, body, true
						label := "review-correctness"
						item.StartLabel = &label
					case "reply":
						item.Message = directed
					}
					entries = append(entries, presentation.Entry{Facts: item.Facts, Values: item.Values()})
				}
				add("assignment", "/root", parent, "Check duplicate edits.")
				add("reply", parent, "/root", "Review started.")
				add("start", parent, child, "Review the fix.")
				add("final", child, "", "First review complete.")
				add("assignment", parent, child, strings.Repeat("Re-review the cumulative change.\n", 8))
				add("reply", child, parent, "No remaining findings.")
				add("reply", parent, sibling, "Please check the tests.")
				add("reply", sibling, parent, "Tests checked.")
				add("reply", parent, "/root", "Review complete.")
				// Delivered messages use the local transcript's Main alias.
				add("reply", parent, "Main", "Message delivered to main.")
				v.Update(&presentation.Publication{Entries: entries}, Display{Caller: caller})
				v.Following = false
				v.Passed = map[uint64]bool{5: true}
				feed := v.RenderConversation(100)
				plain := ansi.Strip(strings.Join(feed.Lines, "\n"))
				if caller == "/root" {
					for _, route := range []string{"duplicate_edits → duplicate_edits/correctness", "duplicate_edits/correctness → duplicate_edits", "duplicate_edits → other", "other → duplicate_edits"} {
						if !strings.Contains(plain, route) {
							t.Fatalf("missing route %q:\n%s", route, plain)
						}
					}
					if !slices.Contains(feed.Questions, uint64(5)) || !strings.Contains(plain, "Open assignment") {
						t.Fatalf("assignment lost source link:\n%s", plain)
					}
				} else if !strings.Contains(plain, "▶ duplicate_edits/correctness started") || !strings.Contains(plain, "├─→ follow-up") || !strings.Contains(plain, "← main") {
					t.Fatalf("direction is not relative to displayed child:\n%s", plain)
				}
				if !slices.ContainsFunc(feed.Passing, func(p liveActivityPassing) bool { return p.seq == 7 }) {
					t.Fatal("outgoing peer message lost scrollback excerpt tracking")
				}
				name := "main"
				if caller != "/root" {
					name = "child"
				}
				uisnapshot.AssertTerminal(t, "testdata/snapshots/conversation_hierarchy_"+name+".txt", append(feed.Lines, "plain after traffic"), 100)
			})
		}
	}
}
