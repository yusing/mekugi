package terminalview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
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
					item := presentation.ItemContent{Seq: seq, Agent: agent, Kind: kind, Text: body, Activity: seq + 100, Observed: now.Add(time.Duration(seq) * time.Second), Restored: restored}
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
				} else if !strings.Contains(plain, "duplicate_edits → duplicate_edits/correctness started") || !strings.Contains(plain, "duplicate_edits → duplicate_edits/correctness") || !strings.Contains(plain, "main → duplicate_edits") {
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

func TestUISnapshotConversationOwnAuthor(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			var snapshot []string
			for _, tc := range []struct {
				name, agent, kind, author string
				display                   Display
				child                     bool
			}{
				{"root", "Main", "text", "/root", Display{}, false},
				{"child runtime", "Main", "text", "/root/developer", Display{MainCaller: "/root/developer", Caller: "/root"}, false},
				{"selected child", "/root/developer/review", "final", "/root/developer/review", Display{MainCaller: "/root", Caller: "/root/developer/review"}, true},
			} {
				for _, restored := range []bool{false, true} {
					v := NewView()
					v.Conversation, v.ChildrenOnly, v.Painter.Theme = true, tc.child, theme
					entry := presentation.Entry{Seq: 1, Agent: tc.agent, Kind: tc.kind, Text: "## Summary\nClosed the recovery-evidence gap.\n\n## Details\nBoth actual compaction and a new host turn recover.", Observed: time.Date(2026, 11, 11, 10, 51, 20, 0, time.Local), ItemComplete: true, Restored: restored}
					publication := &presentation.Publication{Entries: []presentation.Entry{entry}}
					// Reusing the publication also covers a runtime caller arriving
					// after its local Main answer, without stale root rendering.
					v.Update(publication, Display{})
					v.RenderFeed(100, 100)
					v.Update(publication, tc.display)
					rows := v.RenderFeed(100, 100).Lines
					color := strings.Replace(terminalui.AgentColor(tc.author), "1;", "", 1)
					if color == "" {
						color = theme.Accent()
					}
					if len(rows) < 2 {
						t.Fatalf("%s answer has no body", tc.name)
					}
					heading, _ := terminalui.ExtractCopy(rows[0])
					if !strings.HasPrefix(heading, color+"●"+terminalui.Reset) || !strings.Contains(heading, v.Painter.Agent(tc.author)) || !strings.HasPrefix(rows[1], color+"┃"+terminalui.Reset) {
						t.Fatalf("%s restored=%t lost author heading or gutter: %q", tc.name, restored, rows)
					}
					clipped := v.activityEventItem(0, 100, 3, 0).Lines
					if !strings.HasPrefix(clipped[len(clipped)-1], color+"┃"+terminalui.Reset) {
						t.Fatalf("%s clipped answer lost author gutter: %q", tc.name, clipped)
					}
					if !restored {
						snapshot = append(snapshot, tc.name)
						snapshot = append(snapshot, rows...)
						snapshot = append(snapshot, clipped...)
						snapshot = append(snapshot, "plain after answer")
					}
					// The runtime Main address accepts its model-visible alias;
					// the selected view and its entries retain canonical identities.
					display := tc.display
					display.MainCaller = strings.Replace(display.MainCaller, "/root", "agent:main", 1)
					if display.MainCaller == "" {
						display.MainCaller = "agent:main"
					}
					v.Update(publication, display)
					if got := v.RenderFeed(100, 100).Lines; !slices.Equal(got, rows) {
						t.Fatalf("%s model address changed own answer:\n%q\n%q", tc.name, got, rows)
					}
				}
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/conversation_own_author_%d.txt", theme), snapshot, 100)
		})
	}
}
