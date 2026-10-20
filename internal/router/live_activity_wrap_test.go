package router

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestLiveActivityWrappedUserBand(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.LightTheme, livediff.DarkTheme} {
		v := liveActivityView{painter: activityui.Painter{Theme: theme}}
		var out conversationLines
		v.userItemContinued(&out, activityPaneEntry{Text: "one two three four five six seven eight"}, 12, false)
		if len(out.lines) < 2 {
			t.Fatal("expected wrapped user message")
		}
		screen := vt.NewEmulator(13, len(out.lines))
		for y, line := range out.lines {
			_, _ = fmt.Fprintf(screen, "\x1b[%d;1H\x1b[0m%s", y+1, line)
		}
		want := screen.CellAt(0, 0).Style.Bg
		for y := range out.lines {
			for x := range 12 {
				got := screen.CellAt(x, y).Style.Bg
				if got == nil || want == nil {
					t.Fatalf("missing user band at (%d,%d)", x, y)
				}
				r, g, b, a := got.RGBA()
				wr, wg, wb, wa := want.RGBA()
				if r != wr || g != wg || b != wb || a != wa {
					t.Fatalf("user band changed at (%d,%d)", x, y)
				}
			}
		}
		screen.Close()
	}
}
