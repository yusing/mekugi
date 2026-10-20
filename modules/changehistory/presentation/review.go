package presentation

import (
	"cmp"
	"slices"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Source: internal/livediff/model.go:10:357@[ebde9ad5]
// Live views read immutable review projections, never workspace files or carriers.
type File struct {
	id          string
	Path        string
	Chunks      []Chunk
	Highlighted bool
	// Origins are the shown changes a visible file composes, in capture
	// order. Baseline counts changes a caller filter folded into its base.
	Origins  []Origin
	Baseline int
	// incomplete also covers hidden gaps and failed composition. Separate
	// captures remain reviewable, but their sum is not a final outcome.
	incomplete bool
}
type Chunk struct {
	Key, Status  string
	Stream       string
	Workspace    string // The capturing workspace; empty when unknown.
	CaptureOrder uint64
	Review       diffmodel.ReviewFile
	Highlighted  bool
	Origin
}

// Origin attributes a capture: its change ID, the canonical agent path that
// made it, and the tool or program that wrote it.
type Origin struct {
	Change, Caller, Source string
}

type View struct {
	Files       []File
	Selected    int
	Visible     map[string]File
	Initialized bool
	// Caller, when set, shows only captures whose CallerKey matches. Other
	// callers' captures compose as baseline, so the net diff stays exact.
	Caller     string
	unfiltered map[string]File
}

// Update installs immutable published content while preserving local selection
// and caller filtering. Unfiltered consumers share the published composition.
func (v *View) Update(publication Publication) {
	selected := ""
	if len(v.Files) > 0 {
		selected = v.Files[v.Selected].Key()
	}
	v.Files, v.unfiltered = publication.Files, publication.Unfiltered
	v.Visible = nil
	if v.Caller == "" {
		v.Visible = v.unfiltered
	}
	for i, file := range v.Files {
		if file.Key() == selected || slices.ContainsFunc(file.Chunks, func(c Chunk) bool { return c.Key == selected }) {
			v.Selected = i
			break
		}
	}
	v.Selected = min(v.Selected, max(0, len(v.Files)-1))
	v.RefreshVisible()
}

func (file File) Key() string {
	if file.id != "" {
		return file.id
	}
	return file.Path
}

// Merge replaces membership and marks newly observed captures after initial history.
func (v *View) Merge(files []File) {
	v.unfiltered = nil
	// The snapshot owns membership; retained capture order owns navigation.
	// In particular, confirmation can repartition a prepared move's groups.
	order := make(map[string]int)
	highlighted := make(map[string]bool)
	newCaptures := make(map[string]bool)
	oldFiles := make(map[string]File)
	fileOrder := make(map[string]int)
	selected := ""
	if len(v.Files) > 0 {
		selected = v.Files[v.Selected].Key()
	}
	for i, file := range v.Files {
		oldFiles[file.Key()], fileOrder[file.Key()] = file, i+1
		for _, chunk := range file.Chunks {
			order[chunk.Key] = len(order) + 1
			if chunk.Highlighted {
				highlighted[chunk.Key] = true
			}
		}
	}
	next := slices.Clone(files)
	for i := range next {
		file := &next[i]
		for _, chunk := range file.Chunks {
			if order[chunk.Key] == 0 {
				newCaptures[chunk.Key] = true
			}
		}
		file.Chunks = slices.Clone(file.Chunks)
		slices.SortStableFunc(file.Chunks, func(a, b Chunk) int {
			// Append new captures after the retained navigation order.
			return cmp.Compare(cmp.Or(order[a.Key], len(order)+1), cmp.Or(order[b.Key], len(order)+1))
		})
		if len(file.Chunks) > 0 {
			file.id = file.Chunks[0].Key
		}
	}
	slices.SortStableFunc(next, func(a, b File) int {
		return cmp.Compare(cmp.Or(fileOrder[a.Key()], len(fileOrder)+1), cmp.Or(fileOrder[b.Key()], len(fileOrder)+1))
	})
	// A refresh may observe several captures without proving their execution order.
	// Initial history is a baseline; receipt-only refreshes retain the prior marks.
	updated := v.Initialized && len(newCaptures) > 0
	if updated {
		highlighted = newCaptures
	}
	v.Initialized = true
	cache := make(map[string]File)
	for i := range next {
		file := &next[i]
		for j := range file.Chunks {
			file.Chunks[j].Highlighted = highlighted[file.Chunks[j].Key]
		}
		if old, ok := oldFiles[file.Key()]; ok && old.Path == file.Path && slices.Equal(old.Chunks, file.Chunks) {
			if visible, ok := v.Visible[file.Key()]; ok {
				cache[file.Key()] = visible
			}
		}
		if file.Key() == selected || slices.ContainsFunc(file.Chunks, func(c Chunk) bool { return c.Key == selected }) {
			v.Selected = i
		}
	}
	v.Files, v.Visible = next, cache
	v.Selected = min(v.Selected, max(0, len(next)-1))
}

// Rebuild the combined result from captures.
func (v *View) RefreshVisible() {
	if v.Visible == nil {
		v.Visible = make(map[string]File, len(v.Files))
	}
	for _, file := range v.Files {
		if _, cached := v.Visible[file.Key()]; cached {
			continue
		}
		visible := File{id: file.id, Path: file.Path}
		var composition diffmodel.ReviewComposition
		var failure error
		chunks := slices.Clone(file.Chunks)
		slices.SortStableFunc(chunks, func(a, b Chunk) int {
			return cmp.Compare(a.CaptureOrder, b.CaptureOrder)
		})
		baseline := make(map[string]bool)
		directoryChunk := -1
		for _, chunk := range chunks {
			visible.incomplete = visible.incomplete || chunk.Review.Incomplete != "" || chunk.Review.Binary
			baselineChunk := !v.Shows(chunk)
			if baselineChunk {
				// One change may hold several captures of this file.
				if id := cmp.Or(chunk.Change, chunk.Key); !baseline[id] {
					baseline[id] = true
					visible.Baseline++
				}
			}
			if !baselineChunk && chunk.Change != "" && !slices.ContainsFunc(visible.Origins, func(origin Origin) bool { return origin.Change == chunk.Change }) {
				visible.Origins = append(visible.Origins, chunk.Origin)
			}
			visible.Highlighted = visible.Highlighted || chunk.Highlighted && !baselineChunk
			if chunk.Review.Directory && failure == nil {
				for _, region := range composition.FilesWithHighlights() {
					visible.Chunks = append(visible.Chunks, Chunk{Review: region.ReviewFile, Highlighted: region.Highlighted})
				}
				composition = diffmodel.ReviewComposition{}
				if !baselineChunk {
					if directoryChunk < 0 {
						directoryChunk = len(visible.Chunks)
						visible.Chunks = append(visible.Chunks, Chunk{Review: chunk.Review, Status: "directory"})
					}
					summary := &visible.Chunks[directoryChunk]
					summary.Review.AfterPath = chunk.Review.AfterPath
					summary.Highlighted = summary.Highlighted || chunk.Highlighted
				}
				continue
			}
			directoryChunk = -1
			if chunk.Review.Incomplete != "" && failure == nil {
				// Unknown bytes end this composition epoch. Keep known captures
				// on either side separate rather than hiding all later edits.
				for _, region := range composition.FilesWithHighlights() {
					visible.Chunks = append(visible.Chunks, Chunk{
						Review: region.ReviewFile, Highlighted: region.Highlighted,
					})
				}
				composition = diffmodel.ReviewComposition{}
				if !baselineChunk {
					status := "incomplete history: " + chunk.Review.Incomplete
					if chunk.Review.Origin != "" || chunk.Review.Directory {
						status = chunk.Review.Incomplete
					}
					visible.Chunks = append(visible.Chunks, Chunk{
						Status: status,
						Review: chunk.Review, Highlighted: chunk.Highlighted, Origin: chunk.Origin,
					})
				}
				continue
			}
			if failure == nil {
				failure = composition.ApplyWithHighlight(chunk.Review, baselineChunk, chunk.Highlighted)
			}
		}
		if failure != nil {
			visible.incomplete = true
			// A gap in retained history must not hide valid edits or invent a
			// combined diff. Show each captured edit independently instead.
			visible.Chunks = nil
			for _, chunk := range chunks {
				if !v.Shows(chunk) {
					continue
				}
				chunk.Status = "Separate edit"
				if chunk.Change != "" {
					chunk.Status = chunk.Change + " · separate edit"
				}
				visible.Chunks = append(visible.Chunks, chunk)
			}
		} else {
			for _, region := range composition.FilesWithHighlights() {
				visible.Chunks = append(visible.Chunks, Chunk{
					Review: region.ReviewFile, Highlighted: region.Highlighted,
				})
			}
		}
		visible.Chunks = slices.DeleteFunc(visible.Chunks, func(chunk Chunk) bool {
			return chunk.Review.Directory && chunk.Review.BeforePath == "" && chunk.Review.AfterPath == ""
		})
		v.Visible[file.Key()] = visible
	}
}

// NetCounts reports the line counts of an already composed visible file.
// Unknown or inconsistent evidence never becomes a cumulative-edit total.
func (file File) NetCounts() diffmodel.Counts {
	if file.incomplete {
		return diffmodel.Counts{-1, -1}
	}
	var total diffmodel.Counts
	for _, chunk := range file.Chunks {
		added, removed := chunk.Review.LineCounts()
		if added < 0 || removed < 0 || chunk.Review.Binary || chunk.Status != "" {
			return diffmodel.Counts{-1, -1}
		}
		total.Added += added
		total.Removed += removed
	}
	return total
}

// UnknownCaller is the filter key for captures recorded without a caller, so
// filtering to them stays distinct from showing all.
const UnknownCaller = "?"

// CallerKey is the caller filter value that selects a caller's captures.
func CallerKey(caller string) string {
	if caller == "" {
		return UnknownCaller
	}
	return caller
}

// Shows reports whether the caller filter admits a capture.
func (v *View) Shows(chunk Chunk) bool {
	if v.Caller != "" && CallerKey(chunk.Caller) != v.Caller {
		return false
	}
	return true
}

// FilterCaller shows only one CallerKey's captures; an empty key shows all.
func (v *View) FilterCaller(caller string) {
	if caller == v.Caller {
		return
	}
	v.Caller, v.Visible = caller, nil
	if caller == "" && v.unfiltered != nil {
		v.Visible = v.unfiltered
	}
	v.RefreshVisible()
}

func GroupCaptures(captures []Chunk) []File {
	// Merge streams before following moves or composing files. Agent letters,
	// receipt arrival, and workspace iteration do not order captured edits.
	slices.SortStableFunc(captures, func(a, b Chunk) int {
		return cmp.Compare(a.CaptureOrder, b.CaptureOrder)
	})
	reviews := make([]diffmodel.ReviewFile, len(captures))
	for i := range captures {
		reviews[i] = captures[i].Review
	}
	groups := diffmodel.GroupFiles(reviews)
	files := make([]File, len(groups))
	for i, group := range groups {
		file := &files[i]
		file.Path = group.Path
		for _, at := range group.Captures {
			file.Chunks = append(file.Chunks, captures[at])
		}
		file.id = file.Chunks[0].Key
	}
	return files
}
