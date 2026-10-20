package launch

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestUISnapshotModelVisibleDialog(t *testing.T) {
	audience := sessioncontrol.NewAudience(nil)
	audience.Attach("root")
	view := newActivityAdapter()
	view.Prepared = func(thread, id string) (activity.PreparedContent, bool) { return audience.Prepared("root", thread, id) }
	output := new(activitymodel.Retention).New()
	output.Write("original output\n")
	output.Finish(nil, new(0))
	view.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "tool", Text: "Run `echo original`", Native: &sessionpresentation.NativeItem{Thread: "root", Item: "command", Command: "echo original", Output: output}}}})
	u := &terminalUI{Attachment: &terminalsession.Attachment{}}
	if !u.openEntry(view.bind(u), 1) || len(u.Dialog.Pages) != 2 {
		t.Fatal("missing original/model pages")
	}
	u.Dialog.View.Painter.Theme = diffrender.DarkTheme
	u.layoutOutput(76)
	if u.Dialog.Document.Text != "original output" {
		t.Fatal("original output changed")
	}
	u.outputKey("\x1b[C")
	for _, state := range []string{"unavailable", "prepared", "updated", "evicted"} {
		switch state {
		case "prepared":
			audience.ObservePrepared("root", activity.PreparedContent{ID: "cell", Text: "[same as O1]\n", Calls: []string{"command"}, Cell: true})
		case "updated":
			view.Model.Remove(0, len(view.Entries))
			audience.ObservePrepared("root", activity.PreparedContent{ID: "cell", Text: "new actual projection\n", Calls: []string{"command"}, Cell: true})
		case "evicted":
			audience.ObservePrepared("root", activity.PreparedContent{ID: "cell", Text: strings.Repeat("x", 300<<10)})
		}
		u.layoutOutput(76)
		text := u.Dialog.Document.Text
		if (state == "unavailable" || state == "evicted") && !strings.HasPrefix(text, "Unavailable:") || state == "prepared" && text != "[same as O1]\n" || state == "updated" && text != "new actual projection\n" {
			t.Fatalf("%s: %q", state, text)
		}
		rows := make([]string, 18)
		u.Dialog.Paint(rows, 90, len(rows))
		if state == "unavailable" || state == "prepared" {
			assertNativeUISnapshot(t, "model-visible-dialog-"+state, rows)
		}
	}
	u.outputKey("\x1b[D")
	u.layoutOutput(76)
	if u.Dialog.Document.Text != "original output" {
		t.Fatal("model refresh replaced original copy")
	}
}
