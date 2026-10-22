package sessionpresentation

import (
	"iter"
	"reflect"
	"slices"

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

// Details owns one renderer attachment's semantic selection. It borrows actual
// Activity/output references; closing never releases another owner's output.
type Details struct {
	id                 DetailID
	sequence, revision uint64
	source             *ActivitySource
	pages              []detailPage
	processes          bool
	identities         map[uint64][2]string
}

// OpenDetails accepts source-free static pages as well as identity-bound Activity
// pages. Static selection cannot resolve retained output or process membership.
func (d *Details) OpenDetails(version Version, source *ActivitySource, pages []activity.DetailPage, processes bool, running iter.Seq[ActivityEntry]) DetailID {
	d.sequence++
	d.id = DetailID{version.Attachment, d.sequence}
	d.source, d.pages, d.processes = source, nil, processes
	d.identities = make(map[uint64][2]string)
	if _, ok := d.RefreshDetails(d.id, pages, running); !ok {
		d.CloseDetails(d.id)
	}
	return d.id
}

func (d *Details) CloseDetails(id DetailID) {
	if d.id == id {
		d.id, d.source, d.pages = DetailID{}, nil, nil
		d.identities = nil
	}
}

// RefreshDetails joins selected sources at an application transition. Missing
// feed entries preserve the original page and retained output reference.
// Source: apps/mekugi/internal/router/output_dialog.go:128:181@41b4b1af.
func (d *Details) RefreshDetails(id DetailID, requested []activity.DetailPage, running iter.Seq[ActivityEntry]) (uint64, bool) {
	if d.id != id || d.id.Selection == 0 {
		return 0, false
	}
	previous := make(map[string]detailPage, len(d.pages))
	var details []activity.DetailPage
	for _, page := range d.pages {
		previous[page.ID] = page
		details = append(details, page.DetailPage)
	}
	if requested != nil {
		details = requested
	}
	if d.source == nil && (d.processes || slices.ContainsFunc(details, func(page activity.DetailPage) bool {
		return page.Source != 0 || page.PreparedSource != 0 || page.Output != 0
	})) {
		return 0, false
	}
	if d.processes {
		details = activity.DetailPages(processValues(d.source, details, running))
	}
	selected := make(map[uint64]bool)
	for _, page := range details {
		selected[page.Source] = true
		if page.PreparedSource != 0 {
			selected[page.PreparedSource] = true
		}
	}
	sources := make(map[uint64]activity.DetailSource)
	outputs := make(map[uint64]map[int]*feed.Output)
	if d.source != nil {
		for _, entry := range d.source.Entries {
			if !selected[entry.Source.Seq] {
				continue
			}
			source := activity.DetailSource{Entry: entry.Publish()}
			if n := entry.Source.Native; n != nil {
				if entry.Source.Kind == "tool" || entry.Source.Kind == "attachments" {
					d.identities[entry.Source.Seq] = [2]string{n.Thread, n.Item}
				}
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
	}
	if !d.processes {
		details = activity.RefreshDetails(details, sources)
	}
	for i := range details {
		detail := &details[i]
		detail.Prepared = nil
		source := detail.Source
		if detail.PreparedSource != 0 {
			source = detail.PreparedSource
		}
		identity, ok := d.identities[source]
		if !ok || d.source == nil || d.source.Prepared == nil {
			continue
		}
		id := identity[1]
		if detail.AttachmentID != "" {
			id = detail.AttachmentID
		}
		if content, available := d.source.Prepared(identity[0], id); available && content.Altered {
			detail.Prepared = &content
		}
	}

	pages := make([]detailPage, len(details))
	for i, detail := range details {
		prior := previous[detail.ID]
		detail.Revision = prior.Revision
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
			d.revision++
			detail.Revision = d.revision
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

func (d *Details) DetailView(id DetailID) (DetailView, bool) {
	if d.id != id || d.id.Selection == 0 {
		return DetailView{}, false
	}
	view := DetailView{Revision: d.revision, ID: d.id, Processes: d.processes, Pages: make([]DetailItem, len(d.pages))}
	for i, page := range d.pages {
		item := DetailItem{DetailPage: page.DetailPage, Retained: page.output != nil, OutputVersion: page.publication}
		if page.Prepared != nil {
			content := *page.Prepared
			content.Calls = slices.Clone(content.Calls)
			item.Prepared = &content
		}
		item.Value = activity.CloneValues([]activity.Value{item.Value})[0]
		item.OutputLive = page.live
		view.Pages[i] = item
	}
	return view, true
}

func (d *Details) DetailOutput(request DetailOutputRequest) (DetailOutput, bool) {
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

// Clear retires references while keeping selection IDs monotonic for this view.
func (d *Details) Clear() { *d = Details{sequence: d.sequence} }
