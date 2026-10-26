package terminalsession

// Source: apps/mekugi/internal/router/output_dialog.go:14:40,128:238,265:278@41b4b1af;
// source ownership is supplied by sessionpresentation detail publications.
import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// ContentDialog binds admitted feature content to the terminal's dialog controller.
// Retained content arrives as detached, identity-addressed publications.
type ContentDialog struct {
	Dialog
	View         *activityui.View
	ID           sessionpresentation.DetailID
	Revision     uint64
	Output       *sessionpresentation.DetailOutput
	Pages        []sessionpresentation.DetailItem
	Processes    bool
	File         *activitypresentation.DetailFile
	Image        *terminalui.ImagePreview // Borrowed from the physical frontend's image cache.
	ImageReady   bool
	imageLine    int
	laidKey      contentDialogKey
	modelVisible map[string]bool
}

type contentDialogKey struct {
	id           string
	revision     uint64
	frame        uint64
	width        int
	version      uint64
	retained     bool
	output       bool
	modelVisible bool
	theme        diffrender.Theme
	file         *activitypresentation.DetailFile
	image        *terminalui.ImagePreview
	imageColumns int
	imageRows    int
	imageMessage string
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
		if detail.Prepared == nil {
			delete(d.modelVisible, detail.ID)
		} else if d.modelVisible[detail.ID] {
			tab.ID += "/model"
			tab.Live = false
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
	d.ForceTabs = d.Processes && len(d.Pages) > 0 && d.Pages[0].Source != 0
	d.SetPages(tabs)
	if d.Output != nil && (len(d.Pages) == 0 || d.Output.Page != d.Pages[d.Page].ID || d.Output.Version != d.Pages[d.Page].OutputVersion) {
		d.Output = nil
	}
}

// Layout renders the current retained snapshot without owning output lifetime.
func (d *ContentDialog) Layout(width int) {
	page := d.Pages[d.Page]
	d.StopAvailable = d.Processes && page.Process != nil
	page.ModelVisible = d.modelVisible[page.ID] && page.Prepared != nil
	d.OutputToggle = ""
	if page.Prepared != nil {
		d.OutputToggle = "Model-visible"
		if page.ModelVisible {
			d.OutputToggle = "Original"
		}
	}
	key := contentDialogKey{modelVisible: page.ModelVisible, id: page.ID, revision: page.Revision, width: width, version: page.OutputVersion, retained: page.Retained, theme: d.View.Painter.Theme, file: d.File}
	key.image = d.Image
	key.imageColumns, key.imageRows = d.Image.Size(width, d.View.ImageProtocol, d.View.ImageCellWidth, d.View.ImageCellHeight)
	if d.ImageSource().Path != "" {
		switch {
		case d.View.ImageProtocol == terminalui.NoImages:
			key.imageMessage = "Image preview unavailable: terminal graphics are unsupported."
		case d.ImageReady && d.Image == nil:
			key.imageMessage = "Image preview unavailable: unreadable, invalid, or exceeds image decoding limits."
		case d.Image == nil:
			key.imageMessage = "Loading image…"
		}
	}
	var output *activitymodel.OutputView
	if d.Output != nil && d.Output.Page == page.ID && d.Output.Version == page.OutputVersion {
		output = &d.Output.View
	}
	key.output = output != nil
	if !page.Retained && page.Running {
		if frame, version, ok := d.View.CurrentCommandFrame(page.Source); ok {
			key.frame = version
			page.Tail, page.TailOmitted = frame.Output.Tail, frame.Output.Omitted
		}
	}
	if key == d.laidKey {
		if !page.ModelVisible {
			d.Document.Title = d.View.Painter.DialogPageTitle(activityui.Block{Value: page.Value, OutputSnapshot: output}, d.View.Now(), width)
			if page.Prepared != nil {
				d.Document.Title += " · Original"
			}
		}
		return
	}
	if d.File != nil && d.File != d.laidKey.file {
		d.RevealLine(d.File.First)
	}
	document := d.View.Painter.DetailDocument(page.DetailPage, output, d.File, width)
	if d.ImageSource().Path != "" {
		document.Text = d.File.Path
		d.imageLine = len(document.Lines)
		if key.imageMessage != "" {
			document.Add(terminalui.DialogLine{Text: key.imageMessage, Wrap: true})
			document.Text += "\n\n" + key.imageMessage
		}
		for range key.imageRows {
			document.Add(terminalui.DialogLine{})
		}
	}
	d.SetDocument(document, width)
	d.laidKey = key
}

// ImageSource keeps file previews separate from transcript entries and binds
// every reopen to its new detail selection within the attachment.
func (d *ContentDialog) ImageSource() terminalui.ImageSource {
	if d.File == nil || d.File.ImagePath == "" {
		return terminalui.ImageSource{}
	}
	return terminalui.ImageSource{Path: d.File.ImagePath, Dialog: d.ID.Selection}
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

// Images places the acquired pixels only inside the current dialog body.
// Frozen text selections cover graphics just as they do in the transcript.
func (d *ContentDialog) Images() terminalui.ImageFrame {
	frame := terminalui.ImageFrame{Protocol: d.View.ImageProtocol, CellWidth: d.View.ImageCellWidth, CellHeight: d.View.ImageCellHeight}
	if d.Image == nil || d.Selection != nil || d.rect.W == 0 || d.laidKey.imageRows == 0 {
		return frame
	}
	row := d.starts[d.imageLine]
	first := max(0, d.top-row)
	y := max(0, row-d.top)
	rows := min(d.laidKey.imageRows-first, d.rows-y)
	if rows > 0 {
		frame.Placements = []terminalui.ImagePlacement{{Image: d.Image, X: d.rect.X + 2, Y: d.rect.Y + d.Chrome() - 1 + y, Columns: d.laidKey.imageColumns, Rows: rows, First: first, TotalRows: d.laidKey.imageRows}}
	}
	return frame
}

// Invalidate discards geometry derived from replaced detached content.
func (d *ContentDialog) Invalidate() { d.laidKey = contentDialogKey{} }

// Key toggles the selected output without changing the application's page identity.
func (d *ContentDialog) Key(key string) DialogIntent {
	if d.Selection == nil && len(d.Pages) > 0 && d.width > 0 {
		d.Layout(d.width)
	}
	if key == "s" && !d.Typing && d.Selection == nil && d.Processes && len(d.Pages) > 0 {
		if process := d.Pages[d.Page].Process; process != nil {
			return DialogIntent{Action: DialogStopProcess, Process: new(*process)}
		}
	}
	if key == "o" && !d.Typing {
		if d.Selection == nil && len(d.Pages) > 0 {
			page := d.Pages[d.Page]
			if page.Prepared != nil {
				if d.modelVisible == nil {
					d.modelVisible = make(map[string]bool)
				}
				d.modelVisible[page.ID] = !d.modelVisible[page.ID]
				d.Update(sessionpresentation.DetailView{ID: d.ID, Revision: d.Revision, Pages: d.Pages, Processes: d.Processes})
				d.Invalidate()
				// Copy and search immediately after toggling use the selected document.
				if d.width > 0 {
					d.Layout(d.width)
				}
			}
		}
		return DialogIntent{}
	}
	return d.Dialog.Key(key)
}

// Mouse synchronizes selected content before a gesture can freeze its document.
func (d *ContentDialog) Mouse(button, x, y int, release bool) DialogIntent {
	if d.Selection == nil && len(d.Pages) > 0 && d.width > 0 {
		d.Layout(d.width)
		if button&^28 == 0 && !release {
			d.layoutBody()
		}
	}
	return d.Dialog.Mouse(button, x, y, release)
}
