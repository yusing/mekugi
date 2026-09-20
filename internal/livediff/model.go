package livediff

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/pathdisplay"
)

// Live views read immutable review projections, never workspace files or carriers.
type File struct {
	id          string
	Path        string
	Chunks      []Chunk
	Highlighted bool
}
type Chunk struct {
	Key, Status   string
	Stream        string
	CaptureOrder  uint64
	SnapshotOrder int
	Review        mekugi.ReviewFile
	Applied       bool
	Highlighted   bool
}
type View struct {
	Files        []File
	Selected     int
	Scroll       map[string]int
	Reviewed     map[string]bool
	Visible      map[string]File
	Following    bool
	Latest       string
	Initialized  bool
	UnseenUpdate bool
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

// Rebuild the combined result from captures and acknowledgement IDs. A late
// receipt cannot revive flushed changes; a later overlapping edit can.
func (v *View) RefreshVisible() {
	if v.Visible == nil {
		v.Visible = make(map[string]File, len(v.Files))
	}
	for _, file := range v.Files {
		if _, cached := v.Visible[file.Key()]; cached {
			continue
		}
		visible := File{id: file.id, Path: file.Path}
		var composition mekugi.ReviewComposition
		var pending []Chunk
		var failure error
		unreviewed := false
		legacy, mixed := false, false
		stream := ""
		chunks := slices.Clone(file.Chunks)
		slices.SortStableFunc(chunks, func(a, b Chunk) int {
			return cmp.Compare(a.CaptureOrder, b.CaptureOrder)
		})
		for _, chunk := range chunks {
			reviewed := v.Reviewed[chunk.Key]
			visible.Highlighted = visible.Highlighted || chunk.Highlighted && !reviewed
			unreviewed = unreviewed || !reviewed
			if !chunk.Applied {
				if !reviewed {
					pending = append(pending, chunk)
				}
				continue
			}
			if chunk.Review.Incomplete != "" && failure == nil {
				// Unknown bytes end this composition epoch. Keep known captures
				// on either side separate rather than hiding all later edits.
				for _, region := range composition.FilesWithHighlights() {
					visible.Chunks = append(visible.Chunks, Chunk{
						Review: region.ReviewFile, Highlighted: region.Highlighted,
					})
				}
				composition = mekugi.ReviewComposition{}
				if !reviewed {
					visible.Chunks = append(visible.Chunks, Chunk{
						Status: "incomplete history: " + chunk.Review.Incomplete,
						Review: chunk.Review, Highlighted: chunk.Highlighted,
					})
				}
				continue
			}
			legacy = legacy || chunk.CaptureOrder == 0
			mixed = mixed || stream != "" && stream != chunk.Stream
			stream = chunk.Stream
			if failure == nil {
				failure = composition.ApplyWithHighlight(chunk.Review, reviewed, chunk.Highlighted)
			}
		}
		if legacy && mixed {
			failure = errors.New("older captures have no shared order")
		}
		if failure != nil {
			if unreviewed {
				visible.Chunks = append(visible.Chunks, Chunk{Status: "Unable to combine changes: " + failure.Error(), Review: mekugi.ReviewFile{Incomplete: failure.Error()}})
			}
		} else {
			for _, region := range composition.FilesWithHighlights() {
				visible.Chunks = append(visible.Chunks, Chunk{
					Review: region.ReviewFile, Highlighted: region.Highlighted,
				})
			}
		}
		visible.Chunks = append(visible.Chunks, pending...)
		v.Visible[file.Key()] = visible
	}
}

func (v *View) Flush(all bool) {
	if v.Reviewed == nil {
		v.Reviewed = make(map[string]bool)
	}
	for i, file := range v.Files {
		if !all && i != v.Selected {
			continue
		}
		delete(v.Visible, file.Key())
		for _, chunk := range file.Chunks {
			v.Reviewed[chunk.Key] = true
		}
	}
	v.RefreshVisible()
	if v.UnseenUpdate {
		v.UnseenUpdate = slices.ContainsFunc(v.Files, func(file File) bool {
			return v.Visible[file.Key()].Highlighted
		})
	}
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
		if chunk.Applied && file.BeforePath != file.AfterPath {
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

// Only text and SGR colors may reach the viewport. In particular, captured
// source must not emit cursor controls, OSC clipboard writes, or terminal titles.
func Safe(text string, colors bool) string {
	var out strings.Builder
	var state byte
	for len(text) > 0 {
		seq, width, n, next := ansi.DecodeSequence(text, state, nil)
		if n == 0 {
			break
		}
		state, text = next, text[n:]
		if width > 0 || seq == "\n" {
			out.WriteString(seq)
		} else if seq == "\t" {
			out.WriteString("    ")
		} else if colors && strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") &&
			strings.Trim(seq[2:len(seq)-1], "0123456789;:") == "" {
			out.WriteString(seq)
		} else if r, _ := utf8.DecodeRuneInString(seq); unicode.IsMark(r) || r == '\u200c' || r == '\u200d' {
			// Zero-width marks and joiners are source text, not terminal controls.
			out.WriteString(seq)
		}
	}
	return out.String()
}

func fileAction(file mekugi.ReviewFile, workspace string) string {
	switch {
	case file.BeforePath == "" && file.AfterPath != "":
		return "New file"
	case file.AfterPath == "" && file.BeforePath != "":
		return "Deleted file"
	case file.BeforePath != file.AfterPath:
		return "Rename: " + pathdisplay.ForWorkspace(workspace, file.BeforePath) + " → " + pathdisplay.ForWorkspace(workspace, file.AfterPath)
	default:
		return ""
	}
}

func Gutter(highlighted bool, theme Theme) string {
	if highlighted {
		return theme.Accent() + "▎\x1b[0m "
	}
	return "  "
}

// Give each file a clear section boundary without painting over source colors.
func Header(text string, width int, counts Counts, theme Theme) string {
	width = max(0, width)
	stats := fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.Foreground(chroma.GenericInserted), counts.Added, theme.Foreground(chroma.GenericDeleted), counts.Removed)
	if counts.Added < 0 || counts.Removed < 0 {
		stats = " counts unavailable"
	}
	text = ansi.Truncate(Safe(text, false), max(0, width-ansi.StringWidth(stats)), "")
	header := ansi.Truncate("\x1b[1m"+text+"\x1b[22m"+stats, width, "")
	if remaining := width - ansi.StringWidth(header); remaining > 0 {
		header += "\x1b[2m " + strings.Repeat("─", remaining-1) + "\x1b[22m"
	}
	return header
}

type Counts struct {
	Added, Removed int
}

type Render struct {
	Lines       []string
	Starts      []int
	RowStarts   []int // First display row of each logical row, including chrome.
	Counts      []Counts
	FocusOffset int // Hunk/context anchor retained while locating the target.
	FocusRow    int // Latest changed row to center in the viewport.
}

func (r Render) FollowOffset(rows int) int {
	if rows <= 0 || len(r.Lines) == 0 {
		return 0
	}
	// Let preceding file or hunk context remain visible: the target, rather
	// than its file heading, owns the center of a continuous viewport. Near
	// EOF, pull earlier content into view instead of leaving the bottom blank.
	return max(0, min(r.FocusRow-rows/2, len(r.Lines)-rows))
}

// Keep the viewport anchored to a file and its local row when preceding files grow.
func (v *View) ScrollTo(render Render, offset int) {
	if len(v.Files) == 0 {
		return
	}
	offset = max(0, min(offset, len(render.Lines)-1))
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
	v.Scroll[v.Files[v.Selected].Key()] = max(0, offset-render.Starts[v.Selected])
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

const MaxSourceBytes = 64 << 20

type Output struct{ strings.Builder }

// WriteString bounds rendered output without per-token byte-slice allocation.
func (b *Output) WriteString(s string) (int, error) {
	if b.Len()+len(s) > MaxSourceBytes {
		return 0, errors.New("rendered diff exceeds 64 MiB")
	}
	return b.Builder.WriteString(s)
}
