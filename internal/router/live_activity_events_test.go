package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func sharedEventsView(child bool) *liveActivityView {
	v := newLiveActivityView()
	v.conversation, v.childrenOnly = !child, child
	v.painter.Theme = livediff.DarkTheme
	v.clock = func() time.Time { return time.Date(2026, 10, 2, 12, 34, 56, 0, time.Local) }
	return v
}

func TestUISnapshotLiveActivitySharedEvents(t *testing.T) {
	for _, width := range []int{36, 80} {
		for _, child := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/child=%t", width, child), func(t *testing.T) {
				v := sharedEventsView(child)
				agent := "Main"
				if child {
					agent = "/root/reviewer"
				}
				entries := []activityPaneEntry{
					{Kind: "text", Text: "Journal\n- ◐ /1 Inspect shared rendering · working\n\n  Reviewing the same event presentation."},
					{Kind: "reasoning", Text: "Checking final changes", native: &liveActivityNativeItem{collapsed: true}},
					{Kind: "error", Text: "Provider request failed"},
					{Kind: "error", Text: "Provider request failed", ErrorDetail: "Provider request failed\nThe server returned a distinct diagnostic."},
					{Kind: "text", Text: "The same narrative event is now timestamped."},
					{Kind: "final", Text: "Validation passed."},
				}
				for i, entry := range entries {
					entry.Seq, entry.Agent, entry.Observed = uint64(i+1), agent, v.now().Add(time.Duration(i)*time.Second)
					v.appendEntry(entry, parseLiveActivity(entry))
				}
				feed := v.renderFeed(width, 100)
				uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/shared-events-%d-child-%t.txt", width, child), strings.Join(feed.lines, "\n")+"\n")
				if !strings.Contains(strings.Join(feed.lines, "\n"), journalStateColor(v.painter.Theme, "working")+"◐") {
					t.Fatal("journal task glyph lost its shared state color")
				}
			})
		}
	}
}

func TestLiveActivityChildHostJournalPresentation(t *testing.T) {
	v := sharedEventsView(true)
	// Exercise the host message path, not applyJournal, which is Main-only.
	text := "Journal\n- ● /1 Validate host delivery · done\n\n  Evidence retained."
	v.applyAppServerItem(true, "", "main", "child", "turn", "message", "item/completed", "", appServerItem{Type: "agentMessage", Text: text})
	feed := v.renderFeed(80, 40)
	plain := ansi.Strip(strings.Join(feed.lines, "\n"))
	for _, want := range []string{"◆ journal", "12:34:56", "│", "Validate host delivery"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("host child journal missing %q: %s", want, plain)
		}
	}
	if len(v.entries) != 1 || v.entries[0].Text != text || v.entries[0].native.thread != "child" {
		t.Fatal("presentation changed retained host text or ownership")
	}
}

func TestLiveActivityDetailsOnlyForHiddenContent(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, tc := range []struct {
			name, kind, text string
			details          bool
		}{
			{"short_reasoning", "reasoning", "Checking final changes", false},
			{"heading_reasoning", "reasoning", "**Checking final changes**", false},
			{"hidden_reasoning", "reasoning", "**Checking**\n\nFull retained reasoning", true},
			{"short_error", "error", "Request failed", false},
			{"wrapped_error", "error", strings.Repeat("failure ", 9), false},
			{"multiline_error", "error", "Request failed\nDistinct retained diagnostic", true},
			{"long_error", "error", strings.Repeat("failure ", 40), true},
		} {
			t.Run(fmt.Sprintf("%s/child=%t", tc.name, child), func(t *testing.T) {
				v := sharedEventsView(child)
				agent := "Main"
				if child {
					agent = "/root/worker"
				}
				entry := activityPaneEntry{Seq: 1, Agent: agent, Kind: tc.kind, Text: tc.text, Observed: v.now(), native: &liveActivityNativeItem{collapsed: true}}
				v.appendEntry(entry, parseLiveActivity(entry))
				feed := v.renderFeed(36, 40)
				v.viewport(feed, 40)
				var target liveActivitySnippet
				for _, snippet := range feed.snippets {
					if snippet.run != 0 {
						target = snippet
						break
					}
				}
				if (target.run != 0) != tc.details {
					t.Fatalf("detail target = %+v, want %t", target, tc.details)
				}
				v.snippet = liveActivitySnippet{run: 1, block: 0}
				hovered := v.renderFeed(36, 40)
				if !tc.details && strings.Contains(strings.Join(hovered.lines, "\n"), "\x1b[4m") {
					t.Fatal("fully shown content underlined")
				}
				if tc.details {
					u := &terminalUI{}
					if !u.openOutput(v, target) {
						t.Fatal("hidden content not reachable")
					}
					page := v.painter.DialogPage(u.output.pages[0], 36)
					if strings.TrimSpace(page.Text) != strings.TrimSpace(tc.text) {
						t.Fatalf("dialog lost original content: %q", page.Text)
					}
					if !v.following {
						t.Fatal("dialog changed transcript following")
					}
				}
			})
		}
	}
}

func TestLiveActivityClippedNarrativeRetainsDialog(t *testing.T) {
	v := sharedEventsView(true)
	body := strings.Repeat("A full narrative paragraph.\n\n", 12)
	entry := activityPaneEntry{Seq: 1, Agent: "/root/worker", Kind: "text", Text: body, Observed: v.now()}
	v.appendEntry(entry, parseLiveActivity(entry))
	feed := v.renderFeed(40, 20)
	if len(feed.lines) != 6 || feed.snippets[5].run != 1 {
		t.Fatalf("unbounded or inaccessible Activity excerpt: %+v", feed)
	}
	u := &terminalUI{}
	if !u.openOutput(v, feed.snippets[5]) {
		t.Fatal("excerpt did not open")
	}
	if got := v.painter.DialogPage(u.output.pages[0], 40).Text; strings.TrimSpace(got) != strings.TrimSpace(body) {
		t.Fatalf("lost body: %q", got)
	}
}

func TestLiveActivitySharedEventJumpFlash(t *testing.T) {
	v := sharedEventsView(true)
	entry := activityPaneEntry{Seq: 1, Agent: "/root/worker", Kind: "final", Text: "Answer to the assignment.", Observed: v.now()}
	v.appendEntry(entry, parseLiveActivity(entry))
	before := v.renderFeed(60, 20)
	v.pendingTarget = 1
	after := v.renderFeed(60, 20)
	fill := v.painter.Theme.SelectionBackground()
	if strings.Contains(after.lines[0], fill) || strings.Contains(after.lines[1], fill) || !strings.Contains(after.lines[2], fill) {
		t.Fatal("jump flash must highlight the answer, not the event heading")
	}
	if ansi.Strip(strings.Join(before.lines, "\n")) != ansi.Strip(strings.Join(after.lines, "\n")) || v.questionRows[1] != 0 {
		t.Fatal("jump flash changed content or lost the navigation anchor")
	}
}

func TestLiveActivitySharedNarrativeDialogRefresh(t *testing.T) {
	v := sharedEventsView(true)
	entry := activityPaneEntry{Seq: 1, Agent: "/root/worker", Kind: "text", Text: strings.Repeat("First paragraph.\n\n", 12), Observed: v.now(),
		native: &liveActivityNativeItem{thread: "child", item: "text", live: true, phase: "item/agentMessage/delta"}}
	v.appendEntry(entry, parseLiveActivity(entry))
	feed := v.renderFeed(40, 20)
	u := &terminalUI{}
	if !u.openOutput(v, feed.snippets[5]) {
		t.Fatal("clipped message cannot open")
	}
	entry.Text += "Newly streamed detail."
	v.replaceEntry(0, entry, parseLiveActivity(entry))
	u.output.refreshPages()
	if got := u.output.pages[0]; got.Source != 1 || !got.Live || !strings.HasSuffix(got.Body, "Newly streamed detail.") {
		t.Fatalf("dialog did not refresh its exact source: %+v", got)
	}
	if !v.following {
		t.Fatal("dialog refresh changed transcript follow state")
	}
}

func TestLiveActivityTruncatedReasoningHeading(t *testing.T) {
	for _, child := range []bool{false, true} {
		for _, live := range []bool{false, true} {
			for _, width := range []int{36, 160} {
				t.Run(fmt.Sprintf("child=%t/live=%t/width=%d", child, live, width), func(t *testing.T) {
					v := sharedEventsView(child)
					agent, phase := "Main", "item/completed"
					if child {
						agent = "/root/worker"
					}
					if live {
						phase = "summary"
					}
					body := "**Investigating the very long reasoning heading with UNIQUE_HIDDEN_SUFFIX**\n\nShort body."
					entry := activityPaneEntry{Seq: 1, Agent: agent, Kind: "reasoning", Text: body, native: &liveActivityNativeItem{phase: phase}}
					v.appendEntry(entry, parseLiveActivity(entry))
					feed := v.renderFeed(width, 40)
					var target liveActivitySnippet
					for _, snippet := range feed.snippets {
						if snippet.run != 0 {
							target = snippet
							break
						}
					}
					if hidden := !strings.Contains(ansi.Strip(strings.Join(feed.lines, "\n")), "UNIQUE_HIDDEN_SUFFIX"); hidden != (target.run != 0) {
						t.Fatalf("heading elision=%t, detail target=%+v", hidden, target)
					}
					if target.run != 0 {
						u := &terminalUI{}
						if !u.openOutput(v, target) || v.painter.DialogPage(u.output.pages[0], width).Text != body {
							t.Fatal("elided heading cannot open full original content")
						}
						v.snippet = target
						if !strings.Contains(strings.Join(v.renderFeed(width, 40).lines, "\n"), "\x1b[4m") {
							t.Fatal("truncated heading lacks hover hint")
						}
					}
				})
			}
		}
	}
}
