package router

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotNativeRuntimeJournalReads(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprintf("child=%t", child), func(t *testing.T) {
			u, s, root, c := nativeRuntimeJournalFixture(t)
			u.thread, u.session.cwd = root.Session, root.Workspace
			binding, caller, view := root, "", u.view
			if child {
				binding.Agent, caller, view = "reader", "spawn-reader", u.agents
				if err := u.runtimeEvent(session.Event{Kind: "task", Task: &session.Task{ID: binding.Agent, ToolID: caller, Kind: "local_agent", Description: "Scoped journal reader", Status: "running"}}); err != nil {
					t.Fatal(err)
				}
			}
			runtimeJournalAdd(t, s, binding, c, "task", "Read scoped journal")
			input := `{"journal":[{"op":"log","p":"/1","text":"Confirmed note"}]}`
			runtimeJournalReceipt(t, s, binding, c, "note", "journal_batch", input)
			runtimeJournalInvoke(t, s, c, "note", "journal_batch", input)
			view.conversation, view.bare, view.feedOnly = !child, child, child
			view.clock = u.clock
			const read = `{"p":"/1","depth":1}`
			var outputs []string
			for page := range 2 {
				id := fmt.Sprintf("page-%d", page)
				e := session.Event{Kind: "tool", ID: id, Role: "mcp__mekugi__journal_read", Text: read, Caller: caller}
				// The SDK announcement can precede the authenticated pre-tool hook.
				if page == 1 {
					runtimeJournalReceipt(t, s, binding, c, id, "journal_read", read)
				}
				u.runtimeEntry(e)
				if page == 1 {
					feed := view.renderFeed(100, 40)
					if len(feed.snippets) == 0 || strings.Count(ansi.Strip(strings.Join(feed.lines, "\n")), "Read") != 2 {
						t.Fatal("running read merged or lost its original click target")
					}
				}
				if page == 0 {
					runtimeJournalReceipt(t, s, binding, c, id, "journal_read", read)
				}
				outputs = append(outputs, runtimeJournalInvoke(t, s, c, id, "journal_read", read))
				now := u.now().Add(time.Duration(30+10*page) * time.Millisecond)
				u.clock = func() time.Time { return now }
				e.Kind, e.Text = "tool_result", outputs[page]
				u.runtimeEntry(e)
			}
			// A real rejected read keeps its failure and cannot join settled pages.
			runtimeJournalReceipt(t, s, binding, c, "failed", "journal_read", `{"p":"/999"}`)
			u.runtimeEntry(session.Event{Kind: "tool", ID: "failed", Role: "mcp__mekugi__journal_read", Text: `{"p":"/999"}`, Caller: caller})
			status, failure := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "journal_read", NativeID: "failed", Input: `{"p":"/999"}`})
			if status != http.StatusUnprocessableEntity {
				t.Fatalf("invalid read accepted: %d %s", status, failure)
			}
			failure = strings.TrimSpace(failure)
			u.runtimeEntry(session.Event{Kind: "tool_result", ID: "failed", Text: failure, Caller: caller, Failed: true})
			check := func() {
				t.Helper()
				finishPacing(view)
				feed := view.renderFeed(100, 40)
				grouped, failedTarget := false, false
				for row := range feed.lines {
					block, ok := view.snippetBlock(feed.snippets[row])
					if ok && block.Failed {
						if block.Results != nil || block.ExitCode != 0 || block.Collapsed {
							t.Fatal("failed native read gained success or numeric exit evidence")
						}
						if !u.shell.openOutput(view, feed.snippets[row]) {
							t.Fatal("failed read lost output target")
						}
						u.shell.output.layout(90)
						failedTarget = u.shell.output.laid.Text == failure
					}
					if !ok || !block.JournalTransport || len(block.Members) != 2 {
						continue
					}
					if block.Results == nil || *block.Results != 4 || !u.shell.openOutput(view, feed.snippets[row]) || len(u.shell.output.pages) != 2 {
						t.Fatal("read group lost native output pages")
					}
					for page := range 2 {
						u.shell.output.showPage(page)
						u.shell.output.layout(90)
						if u.shell.output.laid.Text != outputs[page] {
							t.Fatal("read dialog replaced original MCP output")
						}
					}
					grouped = true
				}
				if !grouped || !failedTarget {
					t.Fatal("read group or failure lost its original output")
				}
			}
			check()
			feed := view.renderFeed(100, 40)
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/native-runtime-journal-reads-%t.txt", child), strings.Join(feed.lines, "\n")+"\n")
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-runtime-journal-reads-%t-style.txt", child), append(feed.lines, "following plain row"), 100)
			// A fresh display consumes only retained receipts and native history.
			*view = *newLiveActivityView()
			view.conversation, view.bare, view.feedOnly = !child, child, child
			view.clock = u.clock
			for page := range 2 {
				id := fmt.Sprintf("history/page-%d", page)
				u.runtimeEntry(session.Event{Kind: "tool", ID: id, Role: "mcp__mekugi__journal_read", Text: read, Caller: caller, Historical: true})
				u.runtimeEntry(session.Event{Kind: "tool_result", ID: id, Text: outputs[page], Caller: caller, Historical: true})
			}
			u.runtimeEntry(session.Event{Kind: "tool", ID: "history/failed", Role: "mcp__mekugi__journal_read", Text: `{"p":"/999"}`, Caller: caller, Historical: true})
			u.runtimeEntry(session.Event{Kind: "tool_result", ID: "history/failed", Text: failure, Caller: caller, Historical: true, Failed: true})
			check()
		})
	}
}

func TestNativeRuntimeJournalReadAuthority(t *testing.T) {
	u, s, b, c := nativeRuntimeJournalFixture(t)
	u.thread, u.session.cwd = b.Session, b.Workspace
	for _, tc := range []struct{ name, receipt, input, caller, output string }{
		{"empty", `{}`, `{}`, "", `{"nodes":[]}`},
		{"unverified", "", `{}`, "", `{"nodes":[]}`},
		{"different input", `{"view":"tasks"}`, `{}`, "", `{"nodes":[]}`},
		{"different caller", `{}`, `{}`, "unknown-child", `{"nodes":[]}`},
		{"incomplete result", `{}`, `{}`, "", `{"incomplete":true,"next_call":"mread missing"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.receipt != "" {
				runtimeJournalReceipt(t, s, b, c, tc.name, "journal_read", tc.receipt)
			}
			u.runtimeEntry(session.Event{Kind: "tool", ID: tc.name, Role: "mcp__mekugi__journal_read", Text: tc.input, Caller: tc.caller})
			u.runtimeEntry(session.Event{Kind: "tool_result", ID: tc.name, Text: tc.output, Caller: tc.caller})
			entry := u.view.entries[len(u.view.entries)-1]
			blocks := parseLiveActivity(entry.activityPaneEntry)
			verified := tc.name == "empty" || tc.name == "incomplete result"
			if len(blocks) != 1 || blocks[0].JournalTransport != verified {
				t.Fatalf("receipt changed native classification: %+v", blocks)
			}
			if tc.name == "empty" {
				if blocks[0].Results == nil || *blocks[0].Results != 0 {
					t.Fatal("confirmed empty tree lost known zero")
				}
			} else if blocks[0].Results != nil {
				t.Fatal("unverified/incomplete response gained a count")
			}
		})
	}
}
