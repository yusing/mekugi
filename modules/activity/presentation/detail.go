package presentation

import (
	"strconv"

	"github.com/yusing/mekugi/contracts/activitydetail"
)

// ContributedDetails preserves provider identities and frozen source separately
// from live feed refresh, whose Source identities belong to Activity itself.
func ContributedDetails(pages []activitydetail.Page) []DetailPage {
	result := make([]DetailPage, len(pages))
	for i, page := range pages {
		result[i] = DetailPage{ID: page.ID, Kind: "op", Verb: page.Verb, Code: page.Code, Lang: page.Language, Fenced: true, Path: page.Path}
	}
	return result
}

// DetailPage retains original operation identity independently of feed grouping.
// Output remains an identity within Source; the attached owner resolves its snapshot.
type DetailPage struct {
	Value
	ID       string
	Revision uint64
	// PreparedSource links frozen contributed pages to the host observation
	// without allowing live source refresh to replace their original content.
	PreparedSource uint64
	ModelVisible   bool
	Prepared       *PreparedContent
}

// DetailFile is supplied local source, after the application resolves and reads it.
// Source preserves original bytes for copying independently of display sanitization.
type DetailFile struct {
	Path, Source string
	First, Last  int
	ImagePath    string `json:"ImagePath,omitzero"` // Admitted raster source for native frontend acquisition.
}

// DetailSource joins an existing feed publication to admitted command facts.
// Streaming is the host phase projected by the adapter, not a rendering clock.
type DetailSource struct {
	Entry
	Command   *Command
	Streaming bool
}

// RefreshDetails follows retained sources without discarding a page whose feed
// entry aged out. Command membership follows observed boundaries, not display pace.
// Source: apps/mekugi/internal/router/output_dialog.go:364:426@6d403a5a.
func RefreshDetails(pages []DetailPage, sources map[uint64]DetailSource) []DetailPage {
	var refreshed []DetailPage
	seen := make(map[uint64]bool)
	for _, page := range pages {
		if page.Source != 0 && seen[page.Source] {
			continue
		}
		source, exists := sources[page.Source]
		if !exists || page.Source == 0 {
			refreshed = append(refreshed, page)
			continue
		}
		if page.AttachmentID != "" {
			for _, current := range source.Values {
				if current.AttachmentID == page.AttachmentID {
					page.Value = current
					page.Source = source.Seq
					break
				}
			}
			page.Revision = source.Revision
			refreshed = append(refreshed, page)
			continue
		}
		if source.Command != nil {
			if commands := source.Command.DetailValues(); len(commands) > 0 {
				for _, command := range DetailPages(commands) {
					command.Revision = source.Revision
					refreshed = append(refreshed, command)
				}
				seen[page.Source] = true
				continue
			}
			page.Approval = source.Command.Approval
		}
		if page.Hook != nil && len(source.Values) == 1 {
			page.Value = source.Values[0]
			page.Source = source.Seq
		} else if (page.Kind == "op" || page.Kind == "reads") && source.Command != nil {
			values := []Value{page.Value}
			source.Command.ApplyFacts(values)
			page.Value = values[0]
			page.Running = source.Command.Running
			if page.Output == 0 && len(source.Values) > 0 {
				page.Tail, page.TailOmitted, page.TailClipped = source.Values[0].Tail, source.Values[0].TailOmitted, source.Values[0].TailClipped
			}
		} else if page.Kind != "op" && page.Kind != "reads" {
			if page.Kind != "summary" && len(source.Values) == 1 || page.Kind == "summary" && page.Section < len(source.Values) {
				section := 0
				if page.Kind == "summary" {
					section = page.Section
				}
				current := source.Values[section]
				current.Source = page.Source
				if current.Verb == "" {
					current.Verb = page.Verb
				}
				page.Value = current
			}
			if source.Live {
				page.Live = source.Streaming
			}
		}
		page.Revision = source.Revision
		refreshed = append(refreshed, page)
	}
	return refreshed
}

// DetailPages expands canonical membership without creating another member tree.
// Source: apps/mekugi/internal/router/output_dialog.go:149:169,326:339@563f0c37.
func DetailPages(values []Value) []DetailPage {
	var pages []DetailPage
	for _, value := range values {
		for _, original := range Originals(value) {
			index := original.Operation
			kind := "p"
			if original.Output != 0 {
				kind, index = "o", original.Output
			} else if original.Kind == "summary" {
				index = original.Section + 1
			}
			if index == 0 {
				index = len(pages) + 1
			}
			id := strconv.FormatUint(original.Source, 10) + "/" + kind + strconv.Itoa(index)
			pages = append(pages, DetailPage{Value: original, ID: id})
		}
	}
	return pages
}
