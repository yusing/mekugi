package router

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi"
	"golang.org/x/term"
)

// Live views read immutable review projections, never workspace files or carriers.
type liveDiffFile struct {
	id          string
	path        string
	chunks      []liveDiffChunk
	highlighted bool
}
type liveDiffChunk struct {
	key, status   string
	stream        string
	captureOrder  uint64
	snapshotOrder int
	review        mekugi.ReviewFile
	applied       bool
	highlighted   bool
}
type liveDiffView struct {
	files        []liveDiffFile
	selected     int
	scroll       map[string]int
	reviewed     map[string]bool
	visible      map[string]liveDiffFile
	following    bool
	latest       string
	initialized  bool
	unseenUpdate bool
}

func (file liveDiffFile) key() string {
	if file.id != "" {
		return file.id
	}
	return file.path
}

func (v *liveDiffView) merge(files []liveDiffFile) {
	// The snapshot owns membership; retained capture order owns navigation.
	// In particular, confirmation can repartition a prepared move's groups.
	order := make(map[string]int)
	highlighted := make(map[string]bool)
	newCaptures := make(map[string]bool)
	oldFiles := make(map[string]liveDiffFile)
	fileOrder := make(map[string]int)
	selected := ""
	if len(v.files) > 0 {
		selected = v.files[v.selected].key()
	}
	for i, file := range v.files {
		oldFiles[file.key()], fileOrder[file.key()] = file, i+1
		for _, chunk := range file.chunks {
			order[chunk.key] = len(order) + 1
			if chunk.highlighted {
				highlighted[chunk.key] = true
			}
		}
	}
	latestCaptureOrder := v.latestChunk().captureOrder
	newestOrder := -1
	next := slices.Clone(files)
	for i := range next {
		file := &next[i]
		for _, chunk := range file.chunks {
			if order[chunk.key] == 0 {
				newCaptures[chunk.key] = true
				if chunk.snapshotOrder >= newestOrder && chunk.captureOrder >= latestCaptureOrder {
					v.latest, newestOrder = chunk.key, chunk.snapshotOrder
				}
			}
		}
		file.chunks = slices.Clone(file.chunks)
		slices.SortStableFunc(file.chunks, func(a, b liveDiffChunk) int {
			// Append new captures after the retained navigation order.
			return cmp.Compare(cmp.Or(order[a.key], len(order)+1), cmp.Or(order[b.key], len(order)+1))
		})
		if len(file.chunks) > 0 {
			file.id = file.chunks[0].key
		}
	}
	slices.SortStableFunc(next, func(a, b liveDiffFile) int {
		return cmp.Compare(cmp.Or(fileOrder[a.key()], len(fileOrder)+1), cmp.Or(fileOrder[b.key()], len(fileOrder)+1))
	})
	// A refresh may observe several captures without proving their execution order.
	// Initial history is a baseline; receipt-only refreshes retain the prior marks.
	if v.initialized && len(newCaptures) > 0 {
		highlighted = newCaptures
		v.unseenUpdate = !v.following
	}
	v.initialized = true
	cache := make(map[string]liveDiffFile)
	for i := range next {
		file := &next[i]
		for j := range file.chunks {
			file.chunks[j].highlighted = highlighted[file.chunks[j].key]
		}
		if old, ok := oldFiles[file.key()]; ok && old.path == file.path && slices.Equal(old.chunks, file.chunks) {
			if visible, ok := v.visible[file.key()]; ok {
				cache[file.key()] = visible
			}
		}
		if file.key() == selected || slices.ContainsFunc(file.chunks, func(c liveDiffChunk) bool { return c.key == selected }) {
			v.selected = i
			if file.key() != selected && v.scroll != nil {
				v.scroll[file.key()] = v.scroll[selected]
			}
		}
	}
	v.files, v.visible = next, cache
	if v.following {
		v.followLatest()
	}
	v.selected = min(v.selected, max(0, len(next)-1))
}

// Follow the latest newly observed capture, including edits received while paused.
// Capture ordering here drives navigation, not a claim about execution order.
func (v *liveDiffView) followLatest() {
	v.following = true
	v.unseenUpdate = false
	for i, file := range v.files {
		if slices.ContainsFunc(file.chunks, func(chunk liveDiffChunk) bool { return chunk.key == v.latest }) {
			v.selected = i
			return
		}
	}
}

func (v *liveDiffView) latestChunk() liveDiffChunk {
	for _, file := range v.files {
		for _, chunk := range file.chunks {
			if chunk.key == v.latest {
				return chunk
			}
		}
	}
	return liveDiffChunk{}
}

// Rebuild the combined result from captures and acknowledgement IDs. A late
// receipt cannot revive flushed changes; a later overlapping edit can.
func (v *liveDiffView) refreshVisible() {
	if v.visible == nil {
		v.visible = make(map[string]liveDiffFile, len(v.files))
	}
	for _, file := range v.files {
		if _, cached := v.visible[file.key()]; cached {
			continue
		}
		visible := liveDiffFile{id: file.id, path: file.path}
		var composition mekugi.ReviewComposition
		var pending []liveDiffChunk
		var failure error
		unreviewed := false
		legacy, mixed := false, false
		stream := ""
		chunks := slices.Clone(file.chunks)
		slices.SortStableFunc(chunks, func(a, b liveDiffChunk) int {
			return cmp.Compare(a.captureOrder, b.captureOrder)
		})
		for _, chunk := range chunks {
			reviewed := v.reviewed[chunk.key]
			visible.highlighted = visible.highlighted || chunk.highlighted && !reviewed
			unreviewed = unreviewed || !reviewed
			if !chunk.applied {
				if !reviewed {
					pending = append(pending, chunk)
				}
				continue
			}
			legacy = legacy || chunk.captureOrder == 0
			mixed = mixed || stream != "" && stream != chunk.stream
			stream = chunk.stream
			if failure == nil {
				failure = composition.ApplyWithHighlight(chunk.review, reviewed, chunk.highlighted)
			}
		}
		if legacy && mixed {
			failure = errors.New("older captures have no shared order")
		}
		if failure != nil {
			if unreviewed {
				visible.chunks = append(visible.chunks, liveDiffChunk{status: "Unable to combine changes: " + failure.Error()})
			}
		} else {
			for _, region := range composition.FilesWithHighlights() {
				visible.chunks = append(visible.chunks, liveDiffChunk{
					review: region.ReviewFile, highlighted: region.Highlighted,
				})
			}
		}
		visible.chunks = append(visible.chunks, pending...)
		v.visible[file.key()] = visible
	}
}

func (v *liveDiffView) flush(all bool) {
	if v.reviewed == nil {
		v.reviewed = make(map[string]bool)
	}
	for i, file := range v.files {
		if !all && i != v.selected {
			continue
		}
		delete(v.visible, file.key())
		for _, chunk := range file.chunks {
			v.reviewed[chunk.key] = true
		}
	}
	v.refreshVisible()
	if v.unseenUpdate {
		v.unseenUpdate = slices.ContainsFunc(v.files, func(file liveDiffFile) bool {
			return v.visible[file.key()].highlighted
		})
	}
}

func groupLiveDiffCaptures(captures []liveDiffChunk) []liveDiffFile {
	// Merge streams before following moves or composing files. Agent letters,
	// receipt arrival, and workspace iteration do not order captured edits.
	slices.SortStableFunc(captures, func(a, b liveDiffChunk) int {
		return cmp.Compare(a.captureOrder, b.captureOrder)
	})
	var files []liveDiffFile
	current, deleted := make(map[string]int), make(map[string]int)
	for n, chunk := range captures {
		chunk.snapshotOrder = n + 1
		file := chunk.review
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
			files = append(files, liveDiffFile{path: path})
			current[path] = i
		}
		if chunk.applied && file.BeforePath != file.AfterPath {
			delete(current, file.BeforePath)
			if file.AfterPath == "" {
				deleted[file.BeforePath] = i
			} else {
				current[file.AfterPath] = i
				delete(deleted, file.AfterPath)
				files[i].path = file.AfterPath
			}
		}
		if len(files[i].chunks) == 0 {
			files[i].id = chunk.key
		}
		files[i].chunks = append(files[i].chunks, chunk)
	}
	return files
}

// Only text and SGR colors may reach the viewport. In particular, captured
// source must not emit cursor controls, OSC clipboard writes, or terminal titles.
func liveDiffSafe(text string, colors bool) string {
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

func liveDiffDisplayPath(workspace, path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return path
	}
	if relative, err := filepath.Rel(workspace, path); err == nil && filepath.IsLocal(relative) {
		return relative
	}
	return path
}

func liveDiffAction(file mekugi.ReviewFile, workspace string) string {
	switch {
	case file.BeforePath == "" && file.AfterPath != "":
		return "New file"
	case file.AfterPath == "" && file.BeforePath != "":
		return "Deleted file"
	case file.BeforePath != file.AfterPath:
		return "Rename: " + liveDiffDisplayPath(workspace, file.BeforePath) + " → " + liveDiffDisplayPath(workspace, file.AfterPath)
	default:
		return ""
	}
}

func liveDiffGutter(highlighted bool, theme liveDiffTheme) string {
	if highlighted {
		return theme.accent() + "▎\x1b[0m "
	}
	return "  "
}

// Give each file a clear section boundary without painting over source colors.
func liveDiffHeader(text string, width int, counts liveDiffCounts, theme liveDiffTheme) string {
	width = max(0, width)
	stats := fmt.Sprintf(" %s+%d\x1b[39m %s-%d\x1b[39m", theme.foreground(chroma.GenericInserted), counts.added, theme.foreground(chroma.GenericDeleted), counts.removed)
	text = ansi.Truncate(liveDiffSafe(text, false), max(0, width-ansi.StringWidth(stats)), "")
	header := ansi.Truncate("\x1b[1m"+text+"\x1b[22m"+stats, width, "")
	if remaining := width - ansi.StringWidth(header); remaining > 0 {
		header += "\x1b[2m " + strings.Repeat("─", remaining-1) + "\x1b[22m"
	}
	return header
}

type liveDiffCounts struct {
	added, removed int
}

type liveDiffRender struct {
	lines       []string
	starts      []int
	rowStarts   []int // First display row of each logical row, including chrome.
	counts      []liveDiffCounts
	focusOffset int // Hunk/context anchor retained while locating the target.
	focusRow    int // Latest changed row to center in the viewport.
}

func (r liveDiffRender) followOffset(rows int) int {
	if rows <= 0 || len(r.lines) == 0 {
		return 0
	}
	// Let preceding file or hunk context remain visible: the target, rather
	// than its file heading, owns the center of a continuous viewport. Near
	// EOF, pull earlier content into view instead of leaving the bottom blank.
	return max(0, min(r.focusRow-rows/2, len(r.lines)-rows))
}

// Keep the viewport anchored to a file and its local row when preceding files grow.
func (v *liveDiffView) scrollTo(render liveDiffRender, offset int) {
	if len(v.files) == 0 {
		return
	}
	offset = max(0, min(offset, len(render.lines)-1))
	v.selected = 0
	for i, start := range render.starts {
		if start > offset {
			break
		}
		end := len(render.lines)
		if i+1 < len(render.starts) {
			end = render.starts[i+1]
		}
		if end > start {
			v.selected = i
		}
	}
	v.scroll[v.files[v.selected].key()] = max(0, offset-render.starts[v.selected])
}

// Reflow saved positions when wrapping changes, keeping the same logical row.
func (v *liveDiffView) reflow(before, after liveDiffRender) {
	for i, file := range v.files {
		if i >= len(before.starts) || len(before.rowStarts) == 0 {
			continue
		}
		offset := before.starts[i] + v.scroll[file.key()]
		row, exact := slices.BinarySearch(before.rowStarts, offset)
		if !exact {
			row--
		}
		if row >= 0 && row < len(after.rowStarts) {
			v.scroll[file.key()] = max(0, after.rowStarts[row]-after.starts[i])
		}
	}
}

type liveDiffOutput struct{ strings.Builder }

// WriteString bounds rendered output without per-token byte-slice allocation.
func (b *liveDiffOutput) WriteString(s string) (int, error) {
	if b.Len()+len(s) > maxChangeReadBytes {
		return 0, errors.New("rendered diff exceeds 64 MiB")
	}
	return b.Builder.WriteString(s)
}

// Write preserves the output bound when the embedded builder is used as io.Writer.
func (b *liveDiffOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxChangeReadBytes {
		return 0, errors.New("rendered diff exceeds 64 MiB")
	}
	return b.Builder.Write(p)
}

// RunLiveDiff is the internal entry point for a router-owned terminal pane.
func RunLiveDiff(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	flags := flag.NewFlagSet("live-diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace for displayed paths (default current directory)")
	replay := flags.String("replay-dir", "", "replay directory (default platform state directory)")
	simulate := flags.Bool("simulate", false, "replay an isolated streaming UI demonstration; no Codex or Herdr required")
	speed := flags.Float64("speed", 1, "simulation playback speed (0.1 to 20)")
	repeat := flags.Bool("repeat", false, "repeat the simulation until q")
	sessionFile := flags.String("session-file", "", "private router event connection")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi live-diff:", err)
		return 1
	}
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected arguments"))
	}
	if *simulate {
		if *workspace != "" || *replay != "" || *sessionFile != "" {
			return fail(errors.New("simulation owns its temporary workspace, replay store, and connection"))
		}
		if err := runLiveDiffSimulation(ctx, stdin, stdout, *speed, *repeat); err != nil {
			return fail(err)
		}
		return 0
	}
	var err error
	if *workspace == "" {
		*workspace, err = os.Getwd()
	}
	if err == nil {
		*workspace, err = filepath.Abs(*workspace)
	}
	if err == nil {
		*workspace, err = canonicalInspectionWorkspace(*workspace)
	}
	if err != nil {
		return fail(err)
	}
	if *replay == "" {
		*replay, err = defaultMekugiReplayDirectory()
	} else {
		*replay, err = filepath.Abs(*replay)
	}
	if err != nil {
		return fail(err)
	}
	if *sessionFile == "" {
		return fail(errors.New("live-diff is a router-owned pane; start an interactive mekugi codex session"))
	}
	store := &mekugiReplayStore{directory: *replay}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return fail(errors.New("live view needs a terminal"))
	}
	if err := runLiveDiffTerminal(ctx, store, *workspace, stdin, stdout, *sessionFile); err != nil {
		return fail(err)
	}
	return 0
}

func runLiveDiffTerminal(ctx context.Context, store *mekugiReplayStore, workspace string, stdin, stdout *os.File, sessionFile string) (err error) {
	connection, err := readLiveDiffConnection(sessionFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	old, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(int(stdin.Fd()), old)) }()
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[?25l\x1b[?1000;1006h\x1b]11;?\x1b\\"); err != nil {
		return err
	}
	defer func() {
		_, e := io.WriteString(stdout, "\x1b[?2026l\x1b[?1000;1006l\x1b[0m\x1b[?25h\x1b[?1049l")
		err = errors.Join(err, e)
	}()
	// A private descriptor makes cancellation interrupt Read without closing the
	// caller's stdin. The goroutine is joined before restoring terminal state.
	input, err := os.Open(stdin.Name())
	if err != nil {
		return err
	}
	keys := make(chan byte, 64)
	done := make(chan struct{})
	inputCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(done)
		defer close(keys)
		var buf [32]byte
		for {
			n, e := input.Read(buf[:])
			for _, b := range buf[:n] {
				select {
				case keys <- b:
				case <-inputCtx.Done():
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	defer func() { cancel(); input.Close(); <-done }()
	streamCtx, cancelStream := context.WithCancel(ctx)
	events := make(chan liveDiffEvent, 32)
	streamDone := make(chan struct{})
	go func() { defer close(streamDone); liveDiffStream(streamCtx, connection, events) }()
	defer func() { cancelStream(); <-streamDone }()
	controller := newLiveDiffTerminalController(store, workspace, stdout)
	defer controller.close()
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	defer signal.Stop(resizes)
	return controller.run(ctx, events, keys, resizes)
}
