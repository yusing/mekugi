package sessionpresentation

import (
	"reflect"
	"slices"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/activity/feed"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

type DetailID struct{ Attachment, Selection uint64 }

// DetailItem publishes page membership and output availability without a handle.
type DetailItem struct {
	activity.DetailPage
	OutputVersion        uint64
	Retained, OutputLive bool
}

type DetailView struct {
	Revision  uint64
	ID        DetailID
	Pages     []DetailItem
	Processes bool
}

type DetailOutputRequest struct {
	ID      DetailID
	Page    string
	Version uint64
}

type DetailOutput struct {
	DetailOutputRequest
	View feed.OutputView
}

type detailPage struct {
	activity.DetailPage
	output      *feed.Output
	version     int
	publication uint64
	live        bool
}

// The active selection owns the same retained references previously held by the
// dialog. Closing drops those references without releasing another owner's output.
type detailSelection struct {
	id                 DetailID
	sequence, revision uint64
	source             *ActivitySource
	pages              []detailPage
	processes          bool
}

var detailSelections scope.Key[detailSelection]

func (a *Attachment) OpenDetails(version Version, source *ActivitySource, pages []activity.DetailPage, processes bool) DetailID {
	if a.retired {
		return DetailID{}
	}
	d := detailSelections.Value(a.Scope())
	d.sequence++
	d.id = DetailID{version.Attachment, d.sequence}
	d.source, d.pages, d.processes = source, nil, processes
	a.RefreshDetails(d.id, pages)
	return d.id
}

func (a *Attachment) CloseDetails(id DetailID) {
	if a.retired {
		return
	}
	d := detailSelections.Value(a.Scope())
	if d.id == id {
		d.id, d.source, d.pages = DetailID{}, nil, nil
	}
}

// RefreshDetails joins selected sources at an application transition. Missing
// feed entries preserve the original page and retained output reference.
// Source: apps/mekugi/internal/router/output_dialog.go:128:181@41b4b1af.
func (a *Attachment) RefreshDetails(id DetailID, requested []activity.DetailPage) (uint64, bool) {
	if a.retired {
		return 0, false
	}
	d := detailSelections.Value(a.Scope())
	if d.id != id || d.source == nil {
		return 0, false
	}
	previous := make(map[string]detailPage, len(d.pages))
	details := make([]activity.DetailPage, len(d.pages))
	for i, page := range d.pages {
		previous[page.ID], details[i] = page, page.DetailPage
	}
	if requested != nil {
		details = requested
	}
	if d.processes {
		details = activity.DetailPages(a.processValues(d.source, details))
	}
	selected := make(map[uint64]bool)
	for _, page := range details {
		selected[page.Source] = true
	}
	sources := make(map[uint64]activity.DetailSource)
	outputs := make(map[uint64]map[int]*feed.Output)
	for _, entry := range d.source.Entries {
		if !selected[entry.Source.Seq] {
			continue
		}
		source := activity.DetailSource{Entry: entry.Publish()}
		if n := entry.Source.Native; n != nil {
			source.Command = new(CommandPresentation(entry.Source))
			source.Streaming = n.Phase == "summary" || n.Phase == "item/started" || n.Phase == "item/agentMessage/delta"
			retained := map[int]*feed.Output{1: n.Output}
			for i, segment := range n.Segments {
				retained[i+2] = segment.Output
			}
			outputs[entry.Source.Seq] = retained
		}
		sources[entry.Source.Seq] = source
	}
	if !d.processes {
		details = activity.RefreshDetails(details, sources)
	}
	pages := make([]detailPage, len(details))
	for i, detail := range details {
		prior := previous[detail.ID]
		detail.Revision = sources[detail.Source].Revision
		if detail.Revision == 0 {
			detail.Revision = prior.Revision
		}
		output := outputs[detail.Source][detail.Output]
		if output == nil {
			output = prior.output
		}
		version, live := 0, false
		if output != nil {
			version = output.Version()
			state, _ := output.Context(0)
			live = !state.Done
		}
		publication := prior.publication
		if output != prior.output || version != prior.version {
			d.revision++
			publication = d.revision
		}
		if !reflect.DeepEqual(detail, prior.DetailPage) {
			detail.Value = activity.CloneValues([]activity.Value{detail.Value})[0]
		}
		pages[i] = detailPage{detail, output, version, publication, live}
	}
	if !reflect.DeepEqual(d.pages, pages) {
		d.revision++
		d.pages = pages
	}
	return d.revision, true
}

func (a *Attachment) DetailView(id DetailID) (DetailView, bool) {
	if a.retired {
		return DetailView{}, false
	}
	d := detailSelections.Value(a.Scope())
	if d.id != id || d.source == nil {
		return DetailView{}, false
	}
	view := DetailView{Revision: d.revision, ID: d.id, Processes: d.processes, Pages: make([]DetailItem, len(d.pages))}
	for i, page := range d.pages {
		item := DetailItem{DetailPage: page.DetailPage, Retained: page.output != nil, OutputVersion: page.publication}
		item.Value = activity.CloneValues([]activity.Value{item.Value})[0]
		item.OutputLive = page.live
		view.Pages[i] = item
	}
	return view, true
}

func (a *Attachment) DetailOutput(request DetailOutputRequest) (DetailOutput, bool) {
	if a.retired {
		return DetailOutput{}, false
	}
	d := detailSelections.Value(a.Scope())
	if d.id != request.ID || d.source == nil {
		return DetailOutput{}, false
	}
	for _, page := range d.pages {
		if page.ID != request.Page || page.publication != request.Version || page.output == nil || page.output.Version() != page.version {
			continue
		}
		view := page.output.View()
		view.Lines = slices.Clone(view.Lines)
		return DetailOutput{request, view}, true
	}
	return DetailOutput{}, false
}
