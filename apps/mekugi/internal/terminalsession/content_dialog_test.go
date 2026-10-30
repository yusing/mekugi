package terminalsession

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestContentDialogQueuedNavigation(t *testing.T) {
	for _, pointer := range []bool{false, true} {
		t.Run(fmt.Sprint(pointer), func(t *testing.T) {
			d := ContentDialog{View: activityui.NewView()}
			for i, name := range []string{"ALPHA", "BRAVO"} {
				d.Pages = append(d.Pages, sessionpresentation.DetailItem{
					ID: name, Kind: "op", Verb: "Read", Code: strings.Repeat(name+"\n", 40+i*10)})
			}
			d.Update(sessionpresentation.DetailView{Pages: d.Pages})
			d.Paint(make([]string, 24), 100, 24)
			var state Interaction
			in := FrameInput{Dialog: &d, Now: time.Unix(1, 0)}
			var copied string
			feed := func(keys string) {
				for _, b := range []byte(keys) {
					for effect := range in.Dispatch(&state, state.Decoder.Feed(b, in.Now)) {
						if effect.Dialog != nil && effect.Dialog.Action == DialogCopy {
							copied = effect.Dialog.Text
						}
					}
				}
			}
			feed("jj")
			d.Output = &sessionpresentation.DetailOutput{
				Page: "ALPHA",
				View: activitymodel.OutputView{Lines: []string{"ALPHA retained output"}, Done: true},
			}
			if pointer {
				feed(fmt.Sprintf("\x1b[<0;%d;%dM", d.rect.X+3+d.tabs[1].from, d.rect.Y+3))
				x, y := d.rect.X+3+d.Document.Indent(0), d.rect.Y+d.Chrome()
				feed(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<32;%d;%dM\x1b[<0;%d;%dm", x, y, x+4, y, x+4, y))
				feed("y")
				if copied != "BRAVO" {
					t.Fatalf("queued pointer selection copied %q", copied)
				}
			} else {
				feed("\x1b[C")
			}
			feed("y/BRAVO\rG") // No intervening paint or publication.
			if copied != d.Pages[1].Code || d.query != "bravo" || d.match < 0 || d.top != d.bottom() {
				t.Fatalf("queued input used stale content: copy=%q query=%q match=%d top=%d", copied, d.query, d.match, d.top)
			}
			d.Output = nil
			feed("\x1b[Dy")
			if copied != d.Pages[0].Code || d.top != 2 || d.query != "" {
				t.Fatal("returning without paint lost the original page's content or navigation")
			}
		})
	}
}

func TestContentDialogQueuedNavigationThenHydration(t *testing.T) {
	d := ContentDialog{View: activityui.NewView()}
	for _, name := range []string{"ALPHA", "BRAVO"} {
		d.Pages = append(d.Pages, sessionpresentation.DetailItem{Retained: true, OutputVersion: 1,
			ID: name, Kind: "op", Verb: "Run", Code: "source " + name})
	}
	d.Update(sessionpresentation.DetailView{Pages: d.Pages})
	d.Paint(make([]string, 24), 100, 24)
	d.Key("\x1b[C")
	d.Key("/") // Prepare the selected page before its output arrives.
	d.Output = &sessionpresentation.DetailOutput{
		Page: "BRAVO", Version: 1,
		View: activitymodel.OutputView{Lines: []string{"output BRAVO"}, Done: true},
	}
	d.Paint(make([]string, 24), 100, 24)
	d.Key("\x1b")
	if d.Document.Text != "output BRAVO" || d.Key("y").Text != "output BRAVO" {
		t.Fatal("hydration left selected-page display and copy on source fallback")
	}
}
