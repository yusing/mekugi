package launch

import (
	"fmt"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestLiveActivityWrappedUserBand(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.LightTheme, diffrender.DarkTheme} {
		v := newActivityAdapter()
		v.Painter = activityui.Painter{Theme: theme}
		out := v.testUserItem(sessionpresentation.ActivityEntry{Agent: "You", Text: "one two three four five six seven eight"}, 12)
		if len(out.Lines) < 2 {
			t.Fatal("expected wrapped user message")
		}
		screen := vt.NewEmulator(13, len(out.Lines))
		for y, line := range out.Lines {
			_, _ = fmt.Fprintf(screen, "\x1b[%d;1H\x1b[0m%s", y+1, line)
		}
		want := screen.CellAt(0, 0).Style.Bg
		for y := range out.Lines {
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
