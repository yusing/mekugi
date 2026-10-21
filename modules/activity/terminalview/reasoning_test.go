package terminalview_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotThinkingBlock(t *testing.T) {
	body := "one\n\ntwo\n\nthree\n\nfour\n\nfive"
	for _, tc := range []struct {
		name  string
		block activityui.Block
	}{
		{"streaming", activityui.Block{Kind: "summary", Body: body, Live: true}},
		{"short_streaming", activityui.Block{Kind: "summary", Body: "one\n\ntwo", Live: true}},
		{"completed", activityui.Block{Kind: "summary", Body: body, Thought: 12 * time.Second}},
		{"collapsed", activityui.Block{Kind: "summary", Body: body, Thought: 12 * time.Second, Collapsed: true}},
		{"untimed", activityui.Block{Kind: "summary", Body: "one"}},
		{"single_streaming", activityui.Block{Kind: "summary", Body: "Checking tests", Live: true}},
		{"heading_streaming", activityui.Block{Kind: "summary", Body: "**Checking tests**\n\n", Live: true}},
		{"heading_completed", activityui.Block{Kind: "summary", Body: "**Checking tests**\n\n", Thought: 2 * time.Second, Collapsed: true}},
		{"heading_comment", activityui.Block{Kind: "summary", Body: "**Checking tests**\n<!-- -->", Collapsed: true}},
		{"hash_heading_streaming", activityui.Block{Kind: "summary", Body: "# Checking tests\n<!-- -->", Live: true}},
		{"hash_heading_completed", activityui.Block{Kind: "summary", Body: "# Checking tests\n<!-- -->", Thought: 2 * time.Second, Collapsed: true}},
		{"timed_period", activityui.Block{Kind: "summary", Body: "Checking tests.", Thought: 2 * time.Second, Collapsed: true}},
		{"timed_bold_period", activityui.Block{Kind: "summary", Body: "**Checking tests.**", Thought: 2 * time.Second, Collapsed: true}},
		{"empty_completed", activityui.Block{Kind: "summary", Collapsed: true}},
		{"titled", activityui.Block{Kind: "summary", Body: "**Checking**\n\nPublic summary.", Live: true}},
		{"titled_completed", activityui.Block{Kind: "summary", Body: "**Checking**\n\nPublic summary.", Thought: 2 * time.Second}},
		{"partial_title", activityui.Block{Kind: "summary", Body: "**Check", Live: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := activityui.Painter{Theme: diffrender.DarkTheme}
			rows := p.Block(tc.block, 40)
			if !tc.block.Live && tc.block.Thought >= time.Second && (len(rows) == 0 || !strings.HasPrefix(ansi.Strip(rows[0]), "• Thought for ")) {
				t.Fatalf("completed reasoning must lead with its duration: %q", rows)
			}
			uisnapshot.Assert(t, "testdata/snapshots/thinking_block_"+tc.name+".txt", strings.Join(rows, "\n")+"\n")
		})
	}
}

func TestUISnapshotReasoningMultilineStyles(t *testing.T) {
	var p activityui.Painter
	body := "**Checking**\n\nFirst **bold** then normal and `code` then normal.\nSoft continuation with [link](https://example.com) then normal.\n\n- One long explicit list item that wraps across lines\n  still the same item.\n\nLast paragraph."
	for _, width := range []int{24, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			rows := p.Block(activityui.Block{Kind: "summary", Body: body}, width)
			rows = append(rows, "Answer")
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/reasoning_multiline_styles_%d.txt", width), rows, width)
		})
	}
}
