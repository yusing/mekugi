package terminalsession

import (
	"errors"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

func TestPasteFramingRetainsDestination(t *testing.T) {
	var main Main
	var prompts Prompts
	var decoder Decoder
	view := sessionpresentation.View{Version: sessionpresentation.Version{Attachment: 3, Revision: 7}, Draft: conversationmodel.DraftView{Revision: 4, Available: true}, Menu: &sessionpresentation.MenuView{ID: 8}}
	main.Update(view)
	prompts.Update(view)
	feed := func(bytes string) *PasteResult {
		t.Helper()
		var result *PasteResult
		for _, b := range []byte(bytes) {
			r, err := main.PasteInput(decoder.Feed(b, time.Time{}), &prompts)
			if err != nil {
				t.Fatal(err)
			}
			if r != nil {
				result = r
			}
		}
		return result
	}
	if feed("\x1b[200~one\rtwo\n你好\x03\x1b[31mred\x1b[20") != nil || !main.Pasting() {
		t.Fatal("paste escaped before its closing marker")
	}
	view.Version.Revision++
	main.Update(view)
	prompts.Update(view)
	result := feed("1~")
	if result == nil || result.Text != "one\ntwo\n你好red" || result.Intent.Version != view.Version || result.Intent.Target.Revision != 4 || result.Intent.Menu != 8 || main.Pasting() {
		t.Fatalf("completed paste = %+v", result)
	}
	feed("\x1b[200~discard")
	main.CancelPaste()
	if feed("\x1b[201~") != nil || decoder.Pasting() {
		t.Fatal("cancelled payload or terminator escaped")
	}
	feed("\x1b[200~old attachment")
	view.Version.Attachment++
	main.Update(view)
	prompts.Update(view)
	if result, err := main.PasteInput(Input{Kind: PasteEnd}, &prompts); result != nil || !errors.Is(err, sessionpresentation.ErrStaleIntent) || main.Pasting() {
		t.Fatalf("replaced attachment: %+v, %v", result, err)
	}
}
