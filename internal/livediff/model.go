package livediff

import (
	"cmp"
	"slices"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
)

// Live views read immutable review projections, never workspace files or carriers.
type File struct {
	id          string
	Path        string
	Chunks      []Chunk
	Highlighted bool
	// Origins are the shown changes a visible file composes, in capture
	// order. Baseline counts changes a caller or task filter folded into its base.
	Origins  []Origin
	Baseline int
	// incomplete also covers hidden gaps and failed composition. Separate
	// captures remain reviewable, but their sum is not a final outcome.
	incomplete bool
}
type Chunk struct {
	Key, Status   string
	Stream        string
	Workspace     string // The capturing workspace; empty when unknown.
	CaptureOrder  uint64
	SnapshotOrder int
	Review        diffmodel.ReviewFile
	Highlighted   bool
	Origin
}

// Origin attributes a capture: its change ID, the canonical agent path that
// made it, and the tool or program that wrote it.
type Origin struct {
	Change, Caller, Source string
}

type View struct {
	Files        []File
	Selected     int
	Scroll       map[string]int
	Visible      map[string]File
	Following    bool
	Latest       string
	Initialized  bool
	UnseenUpdate bool
	// Caller, when set, shows only captures whose CallerKey matches. Other
	// callers' captures compose as baseline, so the net diff stays exact.
	Caller string
}

func (file File) Key() string {
	if file.id != "" {
		return file.id
	}
	return file.Path
}

func (v *View) Merge(files []File) {
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
	latestCaptureOrder := v.LatestChunk().CaptureOrder
	newestOrder := -1
	next := slices.Clone(files)
	for i := range next {
		file := &next[i]
		for _, chunk := range file.Chunks {
			if order[chunk.Key] == 0 {
				newCaptures[chunk.Key] = true
				if chunk.SnapshotOrder >= newestOrder && chunk.CaptureOrder >= latestCaptureOrder {
					v.Latest, newestOrder = chunk.Key, chunk.SnapshotOrder
				}
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
	if v.Initialized && len(newCaptures) > 0 {
		highlighted = newCaptures
		v.UnseenUpdate = !v.Following
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
			if file.Key() != selected && v.Scroll != nil {
				v.Scroll[file.Key()] = v.Scroll[selected]
			}
		}
	}
	v.Files, v.Visible = next, cache
	if v.Following {
		v.FollowLatest()
	}
	v.Selected = min(v.Selected, max(0, len(next)-1))
}

// Follow the latest newly observed capture, including edits received while paused.
// Capture ordering here drives navigation, not a claim about execution order.
func (v *View) FollowLatest() {
	v.Following = true
	v.UnseenUpdate = false
	for i, file := range v.Files {
		if slices.ContainsFunc(file.Chunks, func(chunk Chunk) bool { return chunk.Key == v.Latest }) {
			v.Selected = i
			return
		}
	}
}

func (v *View) LatestChunk() Chunk {
	for _, file := range v.Files {
		for _, chunk := range file.Chunks {
			if chunk.Key == v.Latest {
				return chunk
			}
		}
	}
	return Chunk{}
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
	v.RefreshVisible()
}

func GroupCaptures(captures []Chunk) []File {
	// Merge streams before following moves or composing files. Agent letters,
	// receipt arrival, and workspace iteration do not order captured edits.
	slices.SortStableFunc(captures, func(a, b Chunk) int {
		return cmp.Compare(a.CaptureOrder, b.CaptureOrder)
	})
	var files []File
	current, deleted := make(map[string]int), make(map[string]int)
	for n, chunk := range captures {
		chunk.SnapshotOrder = n + 1
		file := chunk.Review
		path := file.BeforePath
		if path == "" {
			path = file.AfterPath
		}
		i, exists := current[path]
		if file.BeforePath == "" {
			if prior, found := deleted[path]; found {
				i, exists = prior, true
			}
		}
		if !exists {
			i = len(files)
			files = append(files, File{Path: path})
			current[path] = i
		}
		if file.BeforePath != file.AfterPath {
			delete(current, file.BeforePath)
			if file.AfterPath == "" {
				deleted[file.BeforePath] = i
			} else {
				current[file.AfterPath] = i
				delete(deleted, file.AfterPath)
				files[i].Path = file.AfterPath
			}
		}
		if len(files[i].Chunks) == 0 {
			files[i].id = chunk.Key
		}
		files[i].Chunks = append(files[i].Chunks, chunk)
	}
	return files
}

func fileAction(file diffmodel.ReviewFile, workspace string) string {
	switch {
	case file.BeforePath == "" && file.AfterPath != "":
		return "New file"
	case file.AfterPath == "" && file.BeforePath != "":
		return "Deleted file"
	case file.BeforePath != file.AfterPath:
		return pathdisplay.ForWorkspace(workspace, file.BeforePath) + " → " + pathdisplay.ForWorkspace(workspace, file.AfterPath)
	default:
		return ""
	}
}

// gutterWidth is the display width of diffrender.Gutter.
const gutterWidth = 2

// LineSource attributes a display row. Chrome rows have no Line.
type LineSource struct {
	// Change names the capture a row shows, or for a composed file the
	// comma-separated changes it combines.
	Change, Path string
	Line         int  // diffrender.Gutter coordinate: old for deletions, new otherwise.
	Deleted      bool // Line is an old-file coordinate.
	Content      int  // Display column where the row's text starts, after gutters.
}

type Render struct {
	syntaxHunks   []syntaxHunk
	renderedBytes int
	Lines         []string
	Starts        []int
	Hunks         []int        // Display offsets for hunk navigation.
	RowStarts     []int        // First display row of each logical row, including chrome.
	Sources       []LineSource // Aligned with Lines.
	Counts        []diffmodel.Counts
}

// Keep the viewport anchored to a file and its local row when preceding files grow.
// Scrolling out of a file forgets its position: only a file the viewport left by
// a jump (n/p, Enter, JumpTo) reopens where it was, never where a scroll passed through.
func (v *View) ScrollTo(render Render, offset int) {
	v.moveTo(render, offset, true)
}

// JumpTo moves the viewport like ScrollTo, but a file it leaves keeps its position.
func (v *View) JumpTo(render Render, offset int) {
	v.moveTo(render, offset, false)
}

func (v *View) moveTo(render Render, offset int, forget bool) {
	// Events and keys can arrive before the next paint. Never use absent or
	// stale file geometry to change the viewport's file-local positions.
	if len(v.Files) == 0 || len(render.Starts) != len(v.Files) {
		return
	}
	offset = max(0, min(offset, len(render.Lines)-1))
	previous := v.Selected
	v.Selected = 0
	for i, start := range render.Starts {
		if start > offset {
			break
		}
		end := len(render.Lines)
		if i+1 < len(render.Starts) {
			end = render.Starts[i+1]
		}
		if end > start {
			v.Selected = i
		}
	}
	if forget && previous != v.Selected && previous < len(v.Files) {
		delete(v.Scroll, v.Files[previous].Key())
	}
	v.Scroll[v.Files[v.Selected].Key()] = max(0, offset-render.Starts[v.Selected])
}

// Open selects a file at its header, as an explicit navigation target.
func (v *View) Open(file int) {
	if file < 0 || file >= len(v.Files) {
		return
	}
	v.Selected, v.Following = file, false
	if v.Scroll != nil {
		delete(v.Scroll, v.Files[file].Key())
	}
}

// Reflow saved positions when wrapping changes, keeping the same logical row.
func (v *View) Reflow(before, after Render) {
	for i, file := range v.Files {
		if i >= len(before.Starts) || len(before.RowStarts) == 0 {
			continue
		}
		offset := before.Starts[i] + v.Scroll[file.Key()]
		row, exact := slices.BinarySearch(before.RowStarts, offset)
		if !exact {
			row--
		}
		if row >= 0 && row < len(after.RowStarts) {
			v.Scroll[file.Key()] = max(0, after.RowStarts[row]-after.Starts[i])
		}
	}
}
