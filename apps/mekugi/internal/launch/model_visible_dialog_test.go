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
	if !u.openEntry(view.bind(u), 1) || len(u.Dialog.Pages) != 1 {
		t.Fatal("missing original page")
	}
	u.Dialog.View.Painter.Theme = diffrender.DarkTheme
	u.layoutOutput(76)
	if u.Dialog.Document.Text != "original output" {
		t.Fatal("original output changed")
	}
	for _, state := range []string{"unavailable", "equal", "prepared", "updated", "evicted"} {
		switch state {
		case "equal":
			audience.ObservePrepared("root", activity.PreparedContent{ID: "cell", Text: "original output\n", Calls: []string{"command"}, Cell: true})
		case "prepared":
			audience.ObservePrepared("root", activity.PreparedContent{ID: "cell", Text: "[same as O1]\n", Calls: []string{"command"}, Cell: true, Altered: true})
		case "updated":
			view.Model.Remove(0, len(view.Entries))
			audience.ObservePrepared("root", activity.PreparedContent{ID: "cell", Text: "new actual projection\n", Calls: []string{"command"}, Cell: true, Altered: true})
		case "evicted":
			audience.ObservePrepared("root", activity.PreparedContent{ID: "cell", Text: strings.Repeat("x", 300<<10)})
		}
		u.layoutOutput(76)
		if state == "prepared" {
			if u.Dialog.Document.Text != "original output" || u.Dialog.OutputToggle != "Model-visible" {
				t.Fatal("altered content did not default to original")
			}
			rows := make([]string, 18)
			u.Dialog.Paint(rows, 90, len(rows))
			assertNativeUISnapshot(t, "model-visible-dialog-original", rows)
			frozen := terminalsession.NewSelection(terminalsession.Surface{Rows: []string{"original output"}}, 0, 0)
			u.Dialog.Selection = frozen
			u.outputKey("o")
			if u.Dialog.Selection != frozen || u.Dialog.Document.Text != "original output" {
				t.Fatal("toggle replaced frozen selection")
			}
			u.outputKey("\x1b")
			u.outputKey("o")
			u.layoutOutput(76)
		}
		if state == "unavailable" || state == "equal" || state == "evicted" {
			u.outputKey("o")
			if u.Dialog.OutputToggle != "" {
				t.Fatalf("%s: unexpected toggle", state)
			}
		}
		text := u.Dialog.Document.Text
		if copied := u.Dialog.Key("y"); copied.Text != text || copied.Action != terminalsession.DialogCopy {
			t.Fatalf("%s: wrong variant copy %+v", state, copied)
		}
		if (state == "unavailable" || state == "equal" || state == "evicted") && text != "original output" || state == "prepared" && text != "[same as O1]\n" || state == "updated" && text != "new actual projection\n" {
			t.Fatalf("%s: %q", state, text)
		}
		rows := make([]string, 18)
		u.Dialog.Paint(rows, 90, len(rows))
		if state == "unavailable" || state == "prepared" {
			assertNativeUISnapshot(t, "model-visible-dialog-"+state, rows)
		}
	}
	u.outputKey("o")
	u.layoutOutput(76)
	if u.Dialog.Document.Text != "original output" {
		t.Fatal("model refresh replaced original copy")
	}
}
