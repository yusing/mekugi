package terminalview

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestActivityTimingVisibleForClassifiedRead(t *testing.T) {
	block := Block{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "sample.txt"}}, Code: "cat sample.txt", Duration: 125 * time.Millisecond}
	var p Painter
	page := p.DialogPage(block, 80)
	title := ansi.Strip(page.Title)
	if !strings.Contains(title, "125ms") || page.Text != block.Code {
		t.Fatalf("title=%q copy=%q", title, page.Text)
	}
	feed := ansi.Strip(strings.Join(p.Block(block, 80), "\n"))
	if !strings.Contains(feed, "125ms") {
		t.Fatalf("feed elapsed missing: %q", feed)
	}
}

func TestUISnapshotFeedElapsedRollover(t *testing.T) {
	for _, main := range []bool{true, false} {
		t.Run(fmt.Sprintf("Main=%t", main), func(t *testing.T) {
			start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)
			item := presentation.ItemContent{
				Facts:   presentation.Facts{Seq: 1, Agent: "Main", Kind: "tool", Text: presentation.ShellSource("pwd", ""), Observed: start, Running: true},
				Command: presentation.Command{Running: true, Started: start},
				Details: &presentation.ItemDetails{CommandText: "pwd"},
			}
			v := NewView()
			v.Conversation = main
			v.Painter.Theme = diffrender.DarkTheme
			v.Clock = func() time.Time { return start }
			v.Update(&presentation.Publication{Entries: []presentation.Entry{{Facts: item.Facts, Values: item.Values()}}}, Display{})
			// The next clock read crosses the boundary that shortens 999ms to 1s.
			now := start.Add(999 * time.Millisecond)
			v.Painter.Clock = func() time.Time {
				stamp := now
				now = now.Add(time.Millisecond)
				return stamp
			}
			var snapshot strings.Builder
			for _, elapsed := range []string{"999ms", "1s"} {
				stamp := now
				eager := *v
				eager.Runs = nil
				eager.Painter.Clock = func() time.Time { return stamp }
				want := eager.LayoutFeed(20)
				got := v.RenderFeed(20, 20)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("deferred frame differs from fixed-time eager render: got %#v, want %#v", got, want)
				}
				text := ansi.Strip(strings.Join(got.Lines, "\n"))
				if !strings.Contains(text, elapsed) {
					t.Fatalf("elapsed time did not advance to %s: %s", elapsed, text)
				}
				fmt.Fprintf(&snapshot, "== %s ==\n%s\n", elapsed, strings.Join(got.Lines, "\n"))
			}
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/feed_elapsed_rollover_main_%t.txt", main), snapshot.String())
		})
	}
}
