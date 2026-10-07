package activity_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotThinkingBlock(t *testing.T) {
	body := "one\n\ntwo\n\nthree\n\nfour\n\nfive"
	for _, tc := range []struct {
		name  string
		block activityui.Block
	}{
		{"streaming", activityui.Block{Kind: "summary", Body: body, Live: true}},
		{"short_streaming", activityui.Block{Kind: "summary", Body: "one\n\ntwo", Live: true}},
		{"completed", activityui.Block{Kind: "summary", Body: body, Elapsed: "12s"}},
		{"collapsed", activityui.Block{Kind: "summary", Body: body, Elapsed: "12s", Collapsed: true}},
		{"untimed", activityui.Block{Kind: "summary", Body: "one"}},
		{"single_streaming", activityui.Block{Kind: "summary", Body: "Checking tests", Live: true}},
		{"heading_streaming", activityui.Block{Kind: "summary", Body: "**Checking tests**\n\n", Live: true}},
		{"heading_completed", activityui.Block{Kind: "summary", Body: "**Checking tests**\n\n", Elapsed: "2s", Collapsed: true}},
		{"heading_comment", activityui.Block{Kind: "summary", Body: "**Checking tests**\n<!-- -->", Collapsed: true}},
		{"hash_heading_streaming", activityui.Block{Kind: "summary", Body: "# Checking tests\n<!-- -->", Live: true}},
		{"hash_heading_completed", activityui.Block{Kind: "summary", Body: "# Checking tests\n<!-- -->", Elapsed: "2s", Collapsed: true}},
		{"timed_period", activityui.Block{Kind: "summary", Body: "Checking tests.", Elapsed: "2s", Collapsed: true}},
		{"timed_bold_period", activityui.Block{Kind: "summary", Body: "**Checking tests.**", Elapsed: "2s", Collapsed: true}},
		{"empty_completed", activityui.Block{Kind: "summary", Collapsed: true}},
		{"titled", activityui.Block{Kind: "summary", Body: "**Checking**\n\nPublic summary.", Live: true}},
		{"partial_title", activityui.Block{Kind: "summary", Body: "**Check", Live: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := activityui.Painter{Theme: livediff.DarkTheme}
			rows := p.Block(tc.block, 40)
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
