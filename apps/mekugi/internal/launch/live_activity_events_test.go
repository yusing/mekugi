package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func sharedEventsView(child bool) *activityAdapter {
	v := newActivityAdapter()
	v.Conversation, v.ChildrenOnly = !child, child
	v.testAudience()
	v.Painter.Theme = diffrender.DarkTheme
	v.Clock = func() time.Time { return time.Date(2026, 10, 2, 12, 34, 56, 0, time.Local) }
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
				entries := []sessionpresentation.ActivityEntry{
					{Kind: "text", Text: "Reviewing the same event presentation."},
					{Kind: "reasoning", Text: "Checking final changes"},
					{Kind: "error", Text: "Provider request failed"},
					{Kind: "error", Text: "Provider request failed", ErrorDetail: "Provider request failed\nThe server returned a distinct diagnostic."},
					{Kind: "text", Text: "The same narrative event is now timestamped."},
					{Kind: "final", Text: "Validation passed."},
				}
				for i, entry := range entries {
					entry.Seq, entry.Agent, entry.Observed = uint64(i+1), agent, v.Now().Add(time.Duration(i)*time.Second)
					v.appendEntry(entry, parseLiveActivity(entry))
				}
				feed := v.RenderFeed(width, 100)
				uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/shared-events-%d-child-%t.txt", width, child), strings.Join(feed.Lines, "\n")+"\n")
			})
		}
	}
}

func TestUISnapshotSymbolQueryDisplay(t *testing.T) {
	v := sharedEventsView(false)
	for i, source := range []string{`msymbol refs sample.go Target`, `msymbol -- refs 'other file.go' Other`, `msymbol def sample.go Target refs other.go Other`, `msymbol refs sample.go`} {
		entry := sessionpresentation.ActivityEntry{Seq: uint64(i + 1), Kind: "tool", Agent: "Main", Text: toolActivityShell(source), Observed: v.Now()}
		v.appendEntry(entry, parseLiveActivity(entry))
	}
	uisnapshot.Assert(t, "testdata/snapshots/symbol-query-display.txt", strings.Join(v.RenderFeed(80, 100).Lines, "\n")+"\n")
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
				entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: agent, Kind: tc.kind, Text: tc.text, Observed: v.Now(), Native: &sessionpresentation.NativeItem{}}
				v.appendEntry(entry, parseLiveActivity(entry))
				feed := v.RenderFeed(36, 40)
				v.Viewport(feed, 40)
				var target activityui.Snippet
				for _, snippet := range feed.Snippets {
					if snippet.Run != 0 {
						target = snippet
						break
					}
				}
				if (target.Run != 0) != tc.details {
					t.Fatalf("detail target = %+v, want %t", target, tc.details)
				}
				v.Snippet = activityui.Snippet{Run: 1, Block: 0}
				hovered := v.RenderFeed(36, 40)
				if !tc.details && strings.Contains(strings.Join(hovered.Lines, "\n"), "\x1b[4m") {
					t.Fatal("fully shown content underlined")
				}
				if tc.details {
					u := &terminalUI{Attachment: &terminalsession.Attachment{}}
					if !u.openOutput(v.bind(u), target) {
						t.Fatal("hidden content not reachable")
					}
					page := v.Painter.DetailDocument(u.Dialog.Pages[0].DetailPage, nil, nil, 36)
					if strings.TrimSpace(page.Text) != strings.TrimSpace(tc.text) {
						t.Fatalf("dialog lost original content: %q", page.Text)
					}
					if !v.Following {
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
	entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: "/root/worker", Kind: "text", Text: body, Observed: v.Now()}
	v.appendEntry(entry, parseLiveActivity(entry))
	feed := v.RenderFeed(40, 20)
	if len(feed.Lines) != 6 || feed.Snippets[5].Run != 1 {
		t.Fatalf("unbounded or inaccessible Activity excerpt: %+v", feed)
	}
	u := &terminalUI{Attachment: &terminalsession.Attachment{}}
	if !u.openOutput(v.bind(u), feed.Snippets[5]) {
		t.Fatal("excerpt did not open")
	}
	if got := v.Painter.DetailDocument(u.Dialog.Pages[0].DetailPage, nil, nil, 40).Text; strings.TrimSpace(got) != strings.TrimSpace(body) {
		t.Fatalf("lost body: %q", got)
	}
}

func TestLiveActivitySharedEventJumpFlash(t *testing.T) {
	v := sharedEventsView(true)
	entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: "/root/worker", Kind: "final", Text: "Answer to the assignment.", Observed: v.Now()}
	v.appendEntry(entry, parseLiveActivity(entry))
	before := v.RenderFeed(60, 20)
	v.PendingTarget = 1
	after := v.RenderFeed(60, 20)
	fill := v.Painter.Theme.SelectionBackground()
	if strings.Contains(after.Lines[0], fill) || strings.Contains(after.Lines[1], fill) || !strings.Contains(after.Lines[2], fill) {
		t.Fatal("jump flash must highlight the answer, not the event heading")
	}
	if ansi.Strip(strings.Join(before.Lines, "\n")) != ansi.Strip(strings.Join(after.Lines, "\n")) || v.QuestionRows[1] != 0 {
		t.Fatal("jump flash changed content or lost the navigation anchor")
	}
}

func TestLiveActivitySharedNarrativeDialogRefresh(t *testing.T) {
	v := sharedEventsView(true)
	entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: "/root/worker", Kind: "text", Text: strings.Repeat("First paragraph.\n\n", 12), Observed: v.Now(),
		Native: &sessionpresentation.NativeItem{Thread: "child", Item: "text", Live: true, Phase: "item/agentMessage/delta"}}
	v.appendEntry(entry, parseLiveActivity(entry))
	feed := v.RenderFeed(40, 20)
	u := &terminalUI{Attachment: &terminalsession.Attachment{}}
	if !u.openOutput(v.bind(u), feed.Snippets[5]) {
		t.Fatal("clipped message cannot open")
	}
	entry.Text += "Newly streamed detail."
	v.replaceEntry(0, entry, parseLiveActivity(entry))
	u.main.detailFiles.prepareOutput(&u.main.attached.session, u.Attachment)
	if got := u.Dialog.Pages[0]; got.Source != 1 || !got.Live || !strings.HasSuffix(got.Body, "Newly streamed detail.") {
		t.Fatalf("dialog did not refresh its exact source: %+v", got)
	}
	if !v.Following {
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
					entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: agent, Kind: "reasoning", Text: body, Native: &sessionpresentation.NativeItem{Phase: phase}}
					v.appendEntry(entry, parseLiveActivity(entry))
					feed := v.RenderFeed(width, 40)
					var target activityui.Snippet
					for _, snippet := range feed.Snippets {
						if snippet.Run != 0 {
							target = snippet
							break
						}
					}
					if hidden := !live || !strings.Contains(ansi.Strip(strings.Join(feed.Lines, "\n")), "UNIQUE_HIDDEN_SUFFIX"); hidden != (target.Run != 0) {
						t.Fatalf("heading elision=%t, detail target=%+v", hidden, target)
					}
					if target.Run != 0 {
						u := &terminalUI{Attachment: &terminalsession.Attachment{}}
						if !u.openOutput(v.bind(u), target) || v.Painter.DetailDocument(u.Dialog.Pages[0].DetailPage, nil, nil, width).Text != body {
							t.Fatal("elided heading cannot open full original content")
						}
						v.Snippet = target
						if !strings.Contains(strings.Join(v.RenderFeed(width, 40).Lines, "\n"), "\x1b[4m") {
							t.Fatal("truncated heading lacks hover hint")
						}
					}
				})
			}
		}
	}
}
