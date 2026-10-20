package terminalsession

// Source: apps/mekugi/internal/router/output_dialog.go:14:40,128:238,265:278@41b4b1af;
// source ownership is supplied by sessionpresentation detail publications.
import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// ContentDialog binds admitted feature content to the terminal's dialog controller.
// Retained content arrives as detached, identity-addressed publications.
type ContentDialog struct {
	Dialog
	View      *activityui.View
	ID        sessionpresentation.DetailID
	Revision  uint64
	Output    *sessionpresentation.DetailOutput
	Pages     []sessionpresentation.DetailItem
	Processes bool
	File      *activitypresentation.DetailFile
	laidKey   contentDialogKey
}

type contentDialogKey struct {
	id       string
	revision uint64
	frame    uint64
	width    int
	version  uint64
	retained bool
	theme    diffrender.Theme
	file     *activitypresentation.DetailFile
}

// Update applies only detached membership and terminal tab navigation.
func (d *ContentDialog) Update(view sessionpresentation.DetailView) {
	if d.ID != view.ID {
		return
	}
	d.Pages, d.Processes, d.Revision = view.Pages, view.Processes, view.Revision
	tabs := make([]DialogTab, len(d.Pages))
	for i, item := range d.Pages {
		detail := item.DetailPage
		tab := DialogTab{ID: detail.ID, Name: detail.Verb, Path: detail.Path, Code: detail.Code, Live: detail.Live || detail.Running || item.OutputLive}
		if tab.Path == "" && len(detail.Reads) == 1 {
			tab.Path = detail.Reads[0].Path
		}
		if d.Processes {
			tab.Name = pathdisplay.AgentDisplayName(detail.Owner)
		}
		if d.File != nil {
			tab.Name, tab.Exact = "Markdown", true
			if i == 1 || len(d.Pages) == 1 {
				tab.Name = "File"
			}
			tab.Line = d.File.First
		}
		if detail.ModelVisible {
			tab = DialogTab{ID: detail.ID, Name: "Model-visible", Exact: true}
		}
		tabs[i] = tab
	}
	d.Navigation = "command"
	if d.Processes {
		d.Navigation = "process"
	}
	if d.File != nil {
		d.Navigation = "view"
	}
	for _, page := range d.Pages {
		if page.ModelVisible {
			d.Navigation = "view"
			break
		}
	}
	d.ForceTabs = d.Processes && len(d.Pages) > 0 && d.Pages[0].Source != 0
	d.SetPages(tabs)
	if d.Output != nil && (len(d.Pages) == 0 || d.Output.Page != d.Pages[d.Page].ID || d.Output.Version != d.Pages[d.Page].OutputVersion) {
		d.Output = nil
	}
}

// Layout renders the current retained snapshot without owning output lifetime.
func (d *ContentDialog) Layout(width int) {
	page := d.Pages[d.Page]
	key := contentDialogKey{id: page.ID, revision: page.Revision, width: width, version: page.OutputVersion, retained: page.Retained, theme: d.View.Painter.Theme, file: d.File}
	var output *activitymodel.OutputView
	if d.Output != nil {
		output = &d.Output.View
	}
	if !page.Retained && page.Running {
		if frame, version, ok := d.View.CurrentCommandFrame(page.Source); ok {
			key.frame = version
			page.Tail, page.TailOmitted = frame.Output.Tail, frame.Output.Omitted
		}
	}
	if key == d.laidKey {
		if !page.ModelVisible {
			d.Document.Title = d.View.Painter.DialogPageTitle(activityui.Block{Value: page.Value, OutputSnapshot: output}, d.View.Now(), width)
		}
		return
	}
	if d.File != nil && d.File != d.laidKey.file {
		d.RevealLine(d.File.First)
	}
	d.SetDocument(d.View.Painter.DetailDocument(page.DetailPage, output, d.File, width), width)
	d.laidKey = key
}

func (d *ContentDialog) Paint(rows []string, width, height int) {
	bodyWidth := d.Prepare(width, height)
	if bodyWidth == 0 {
		return
	}
	if d.Selection == nil {
		d.Layout(bodyWidth)
	}
	d.Dialog.Paint(rows, width, height, &d.View.Painter.Painter)
}

// Invalidate discards geometry derived from replaced detached content.
func (d *ContentDialog) Invalidate() { d.laidKey = contentDialogKey{} }
