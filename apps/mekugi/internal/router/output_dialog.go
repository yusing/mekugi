package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// outputDialog binds admitted feature content to the terminal's dialog controller.
// Direct output handles stay here so feed aging does not retire an open document.
type outputDialog struct {
	terminalsession.Dialog
	view      *activityui.View
	source    *sessionpresentation.ActivitySource
	pages     []outputPage
	processes bool
	file      *activitypresentation.DetailFile
	fileRead  <-chan markdownFileResult
	laidKey   outputDialogKey
}

type outputPage struct {
	activitypresentation.DetailPage
	output *activitymodel.Output
}

type outputDialogKey struct {
	id             string
	revision       uint64
	frame          uint64
	width, version int
	output         *activitymodel.Output
	theme          diffrender.Theme
	file           *activitypresentation.DetailFile
}

// openOutput opens the operation a snippet names in the dialog, reporting
// whether it names one.
func (u *terminalUI) openOutput(view *activityui.View, snippet activityui.Snippet) bool {
	block, ok := view.SnippetBlock(snippet)
	if !ok || block.EditSource != "" || len(block.Questions) > 0 {
		return false
	}
	pages := activitypresentation.DetailPages([]activitypresentation.Value{block.Value})
	if len(block.Members) > 0 {
		pages = nil
		seen := make(map[uint64]bool)
		for _, value := range block.Members {
			member := activityui.Block{Value: value}
			if member.EditSource != "" {
				if seen[member.Source] {
					continue
				}
				if edits, _ := u.activityEditPages(view, member.Source, ""); len(edits) > 0 {
					pages = append(pages, edits...)
					seen[member.Source] = true
					continue
				}
			}
			pages = append(pages, activitypresentation.DetailPages([]activitypresentation.Value{member.Value})...)
		}
	}
	u.openDetails(view, pages)
	page := 0
	if block.Output != 0 && (len(block.Members) == 0 || block.Kind == "op") {
		for i, command := range u.output.pages {
			if command.Source == block.Source && command.Output == block.Output {
				page = i
				break
			}
		}
	}
	u.output.ShowPage(page)
	return true
}

// openBlocks is the shared modal entry point. It never changes pane navigation.
func (u *terminalUI) openBlocks(view *activityui.View, pages []activityui.Block) {
	u.openDetails(view, activitypresentation.DetailPages(activityui.Values(pages)))
}

func (u *terminalUI) openDetails(view *activityui.View, pages []activitypresentation.DetailPage) {
	if len(pages) == 0 {
		return
	}
	u.selection = nil
	u.output = &outputDialog{view: view, source: u.main.activitySource(view)}
	u.output.refreshPages(pages)
}

func (u *terminalUI) openEntry(view *activityui.View, seq uint64) bool {
	for index, entry := range u.main.activitySource(view).Entries {
		if entry.Source.Seq != seq {
			continue
		}
		blocks := u.main.activitySource(view).SourceValues(index)
		for i := range blocks {
			blocks[i].Source = entry.Source.Seq
			if blocks[i].Verb == "" {
				blocks[i].Verb = entry.Source.Agent
			}
		}
		u.openDetails(view, activitypresentation.DetailPages(blocks))
		return len(blocks) > 0
	}
	return false
}

// openRequested opens an operation the view's pointer handling clicked.
func (u *terminalUI) openRequested(view *activityui.View) {
	if snippet := view.Opening; snippet != (activityui.Snippet{}) {
		view.Opening = activityui.Snippet{}
		if snippet.Block == activityui.EditNavigationSnippet {
			u.openActivityEdit(view, snippet.Run, snippet.Path)
		} else if !u.openOutput(view, snippet) {
			u.openEntry(view, snippet.Run)
		}
	}
}

// refreshPages joins selected sources once, then binds their detached detail
// values to app-owned output handles. Missing feed entries retain their last page.
func (d *outputDialog) refreshPages(requested []activitypresentation.DetailPage) {
	previous := make(map[string]outputPage, len(d.pages))
	details := make([]activitypresentation.DetailPage, len(d.pages))
	for i, page := range d.pages {
		previous[page.ID], details[i] = page, page.DetailPage
	}
	if requested != nil {
		details = requested
	}
	selected := make(map[uint64]bool)
	for _, page := range details {
		selected[page.Source] = true
	}
	sources := make(map[uint64]activitypresentation.DetailSource)
	outputs := make(map[uint64]map[int]*activitymodel.Output)
	for _, entry := range d.source.Entries {
		if !selected[entry.Source.Seq] {
			continue
		}
		source := activitypresentation.DetailSource{Entry: entry.Publish()}
		if n := entry.Source.Native; n != nil {
			command := sessionpresentation.CommandPresentation(entry.Source)
			if n.Output == nil && n.Running {
				if frame, _, ok := d.view.CurrentCommandFrame(entry.Source.Seq); ok {
					source.Entry.Command = new(sessionpresentation.ActivityContent(entry.Source))
					source.Entry = frame.Project(source.Entry)
					command.Tail, command.TailOmitted, command.Segments = frame.Output.Tail, frame.Output.Omitted, frame.Segments
				}
			}
			source.Command = &command
			source.Streaming = n.Phase == "summary" || n.Phase == "item/started" || n.Phase == "item/agentMessage/delta"
			retained := map[int]*activitymodel.Output{1: n.Output}
			for i, segment := range n.Segments {
				retained[i+2] = segment.Output
			}
			outputs[entry.Source.Seq] = retained
		}
		sources[entry.Source.Seq] = source
	}
	if !d.processes {
		details = activitypresentation.RefreshDetails(details, sources)
	}
	pages := make([]outputPage, len(details))
	tabs := make([]terminalsession.DialogTab, len(details))
	for i, detail := range details {
		detail.Revision = sources[detail.Source].Revision
		if detail.Revision == 0 {
			detail.Revision = previous[detail.ID].Revision
		}
		output := outputs[detail.Source][detail.Output]
		if output == nil {
			output = previous[detail.ID].output
		}
		pages[i] = outputPage{detail, output}
		tab := terminalsession.DialogTab{ID: detail.ID, Name: detail.Verb, Path: detail.Path, Code: detail.Code, Live: detail.Live || detail.Running}
		if output != nil {
			state, _ := output.Context(0)
			tab.Live = tab.Live || !state.Done
		}
		if tab.Path == "" && len(detail.Reads) == 1 {
			tab.Path = detail.Reads[0].Path
		}
		if d.processes {
			tab.Name = terminalui.AgentDisplayName(detail.Owner)
		}
		if d.file != nil {
			tab.Name, tab.Exact = "Markdown", true
			if i == 1 || len(details) == 1 {
				tab.Name = "File"
			}
			tab.Line = d.file.First
		}
		tabs[i] = tab
	}
	d.pages = pages
	d.Navigation = "command"
	if d.processes {
		d.Navigation = "process"
	}
	if d.file != nil {
		d.Navigation = "view"
	}
	d.ForceTabs = d.processes && len(pages) > 0 && pages[0].Source != 0
	d.SetPages(tabs)
}

// layout renders the current retained snapshot without owning output lifetime.
func (d *outputDialog) layout(width int) {
	if !d.processes {
		d.refreshPages(nil)
	}
	page := d.pages[d.Page]
	key := outputDialogKey{id: page.ID, revision: page.Revision, width: width, output: page.output, theme: d.view.Painter.Theme, file: d.file}
	var output *activitymodel.OutputView
	if page.output != nil {
		key.version = page.output.Version()
		snapshot := page.output.View()
		output = &snapshot
	} else if page.Running {
		_, key.frame, _ = d.view.CurrentCommandFrame(page.Source)
	}
	if key == d.laidKey {
		d.Document.Title = d.view.Painter.DialogPageTitle(activityui.Block{Value: page.Value, OutputSnapshot: output}, d.view.Now(), width)
		return
	}
	if d.file != nil && d.file != d.laidKey.file {
		d.RevealLine(d.file.First)
	}
	d.SetDocument(d.view.Painter.DetailDocument(page.DetailPage, output, d.file, width), width)
	d.laidKey = key
}

func (u *terminalUI) outputKey(key string) { u.outputIntent(u.output.Key(key)) }
func (u *terminalUI) outputMouse(button, x, y int, release bool) {
	u.outputIntent(u.output.Mouse(button, x, y, release))
}

func (u *terminalUI) outputIntent(intent terminalsession.DialogIntent) {
	switch intent.Action {
	case terminalsession.DialogClose:
		u.output = nil
	case terminalsession.DialogCopy:
		u.copyText(intent.Text)
	case terminalsession.DialogReference:
		if u.main.composerReadOnly() {
			u.selectNativePane(0)
		}
		u.main.insertSelection("dialog", intent.Text, "")
		u.main.refreshPicker()
		u.main.Run = composer.NoRun
		u.Focus = 0
		u.output = nil
	case terminalsession.DialogOpenLink:
		u.openMarkdownFile(u.output.view, intent.Text)
	}
}

func (u *terminalUI) paintOutput(rows []string, width, height int) {
	d := u.output
	bodyWidth := d.Prepare(width, height)
	if bodyWidth == 0 {
		return
	}
	if d.Selection == nil {
		if d.processes {
			d.refreshPages(activitypresentation.DetailPages(activityui.Values(u.main.processPages(d.pages))))
		}
		d.layout(bodyWidth)
	}
	d.Paint(rows, width, height, &d.view.Painter.Painter)
}
