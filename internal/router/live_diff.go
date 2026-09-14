package router

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	key, status, diff string
	stream            string
	captureOrder      uint64
	snapshotOrder     int
	review            mekugi.ReviewFile
	applied           bool
	highlighted       bool
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
		oldFiles[file.key()], fileOrder[file.key()] = file, i
		for _, chunk := range file.chunks {
			order[chunk.key] = len(order)
			if chunk.highlighted {
				highlighted[chunk.key] = true
			}
		}
	}
	newestOrder := -1
	next := slices.Clone(files)
	for i := range next {
		file := &next[i]
		for _, chunk := range file.chunks {
			if _, known := order[chunk.key]; !known {
				newCaptures[chunk.key] = true
				if chunk.snapshotOrder >= newestOrder {
					v.latest, newestOrder = chunk.key, chunk.snapshotOrder
				}
			}
		}
		file.chunks = slices.Clone(file.chunks)
		slices.SortStableFunc(file.chunks, func(a, b liveDiffChunk) int {
			x, xOK := order[a.key]
			y, yOK := order[b.key]
			if xOK && yOK {
				return x - y
			}
			if xOK {
				return -1
			}
			if yOK {
				return 1
			}
			return 0
		})
		if len(file.chunks) > 0 {
			file.id = file.chunks[0].key
		}
	}
	slices.SortStableFunc(next, func(a, b liveDiffFile) int {
		x, xOK := fileOrder[a.key()]
		y, yOK := fileOrder[b.key()]
		if xOK && yOK {
			return x - y
		}
		if xOK {
			return -1
		}
		if yOK {
			return 1
		}
		return 0
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
					diff: region.UnifiedDiff(), review: region.ReviewFile, highlighted: region.Highlighted,
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

func (s *mekugiReplayStore) liveDiffFilesFromIndexes(ctx context.Context, indexes []changeIndex) ([]liveDiffFile, error) {
	var captures []liveDiffChunk
	total := 0
	for _, index := range indexes {
		prefix := index.Workspace + "\x00"
		for stream, info := range index.Streams {
			for number := 1; number <= info.Next; number++ {
				id := "hp_" + changeStreamName(stream) + strconv.Itoa(number)
				change := index.Changes[id]
				for _, call := range change.Calls {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					record, found, err := s.read(index.Workspace, call.ID, false)
					if err != nil {
						return nil, err
					}
					if !found || record.History.ChangeID != id || record.History.CorrelationID != change.Correlation {
						return nil, fmt.Errorf("change %s has a missing or inconsistent attempt", id)
					}
					history := record.History
					// Private retained scripts are not workspace edits.
					if strings.HasPrefix(strings.TrimLeft(history.recoveryBaseline(), "\r\n"), "in "+shellArtifactPrefix) {
						continue
					}
					for n, file := range history.ReviewFiles {
						diff := file.UnifiedDiff()
						canonical := func(path string) string {
							if path == "" {
								return ""
							}
							if !filepath.IsAbs(path) {
								path = filepath.Join(index.Workspace, path)
							}
							return filepath.Clean(path)
						}
						file.BeforePath, file.AfterPath = canonical(file.BeforePath), canonical(file.AfterPath)
						total += len(diff)
						if total > maxChangeReadBytes {
							return nil, errors.New("live diff exceeds 64 MiB; use hchanges with a narrower range")
						}
						captures = append(captures, liveDiffChunk{
							key:          prefix + call.ID + "/" + strconv.Itoa(n),
							stream:       index.Workspace + "\x00" + strconv.Itoa(stream),
							captureOrder: record.CaptureOrder,
							status:       id + " " + trackedStatus(history, call.Confirmed),
							review:       file,
							applied:      trackedStatus(history, call.Confirmed) == "applied",
							diff:         diff,
						})
					}
				}
			}
		}
	}
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
	return files, nil
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

func liveDiffDisplayDiff(chunk liveDiffChunk, workspace string) string {
	// Only rewrite captured header paths, never matching text in source hunks.
	header, hunks, hasHunks := strings.Cut(chunk.diff, "\n@@ ")
	replacements := make([]string, 0, 4)
	for _, path := range []string{chunk.review.BeforePath, chunk.review.AfterPath} {
		if path != "" {
			replacements = append(replacements, strconv.Quote(path), strconv.Quote(liveDiffDisplayPath(workspace, path)))
		}
	}
	header = strings.NewReplacer(replacements...).Replace(header)
	if hasHunks {
		return header + "\n@@ " + hunks
	}
	return header
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

// Give each file a clear section boundary without painting over source colors.
func liveDiffHeader(text string, width int, counts liveDiffCounts) string {
	width = max(0, width)
	stats := fmt.Sprintf(" \x1b[32m+%d\x1b[39m \x1b[31m-%d\x1b[39m", counts.added, counts.removed)
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
	counts      []liveDiffCounts
	focusOffset int
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
		v.selected = i
	}
	v.scroll[v.files[v.selected].key()] = offset - render.starts[v.selected]
}

func renderLiveDiff(ctx context.Context, files []liveDiffFile, delta string, workspace string, width, focusFile int, focus liveDiffChunk) (liveDiffRender, error) {
	// One delta invocation renders the whole view. Private boundaries map each
	// file and source hunk to terminal rows under user-configured delta styling.
	marker := "mekugi-live-diff-" + rand.Text() + "-"
	var raw strings.Builder
	var targets []string
	var highlights []bool
	fileTargets := make([]int, len(files))
	fileCounts := make([]liveDiffCounts, len(files))
	addTarget := func(highlighted bool) int {
		index := len(targets)
		target := marker + strconv.Itoa(index)
		targets = append(targets, target)
		highlights = append(highlights, highlighted)
		fmt.Fprintln(&raw, target)
		return index
	}
	focusIndex, bestDistance := 0, int(^uint(0)>>1)
	focusHunks, _ := focus.review.Hunks()
	focusLine := 0
	if len(focusHunks) > 0 {
		focusLine = focusHunks[len(focusHunks)-1].ChangedStart
	}
	for i, file := range files {
		action := ""
		for _, chunk := range file.chunks {
			added, removed := (mekugi.ReviewFile{Diff: chunk.diff}).LineCounts()
			fileCounts[i].added += added
			fileCounts[i].removed += removed
			// Composed regions share one file action. Prepared captures keep
			// their own action alongside their application status below.
			if action == "" && chunk.status == "" {
				action = liveDiffAction(chunk.review, workspace)
			}
		}
		fileTargets[i] = addTarget(file.highlighted)
		if i == focusFile {
			focusIndex = fileTargets[i]
		}
		label := liveDiffDisplayPath(workspace, file.path)
		if action != "" {
			label += " · " + action
		}
		fmt.Fprintf(&raw, "%d/%d  %s\n", i+1, len(files), label)
		if len(file.chunks) == 0 {
			fmt.Fprintln(&raw, "No unreviewed changes")
		}
		for _, chunk := range file.chunks {
			hunks, err := chunk.review.Hunks()
			if err == nil && len(hunks) == 0 && chunk.review.Diff != "" && liveDiffAction(chunk.review, workspace) != "" {
				// The file heading or capture caption already describes path-only changes.
				if chunk.status == "" {
					continue
				}
				hunks = []mekugi.ReviewHunk{{}}
			} else if err != nil || len(hunks) == 0 {
				hunks = []mekugi.ReviewHunk{{Diff: chunk.diff}}
			}
			for hunkIndex, hunk := range hunks {
				index := addTarget(chunk.highlighted)
				distance := max(hunk.AfterStart-focusLine, focusLine-(hunk.AfterStart+max(1, hunk.AfterCount)-1), 0)
				if i == focusFile {
					if focus.key != "" && chunk.key == focus.key {
						// Follow this capture, not an older edit at the same line.
						focusIndex, bestDistance = index, -1
					} else if bestDistance >= 0 && distance < bestDistance {
						focusIndex, bestDistance = index, distance
					}
				}
				part := chunk
				part.diff = hunk.Diff
				if hunkIndex == 0 && chunk.status != "" {
					label := chunk.status
					if action := liveDiffAction(chunk.review, workspace); action != "" {
						label += " · " + action
					}
					fmt.Fprintln(&raw, label)
				}
				diff := liveDiffDisplayDiff(part, workspace)
				raw.WriteString(diff)
				if diff != "" && !strings.HasSuffix(diff, "\n") {
					raw.WriteByte('\n')
				}
			}
		}
	}
	text := liveDiffSafe(raw.String(), false)
	if raw.Len() != 0 {
		renderCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(renderCtx, delta, "--paging=never", "--width="+strconv.Itoa(max(1, width-3)),
			"--file-style=omit", "--file-decoration-style=none",
			// Inline numbers are navigation, not optional source styling. Fixed
			// formats also distinguish genuine blank source rows from padding.
			"--hunk-header-style=omit", "--hunk-header-decoration-style=none", "--line-numbers",
			"--line-numbers-left-format={nm:>4}⋮", "--line-numbers-right-format={np:>4}│",
			"--minus-style=syntax normal", "--plus-style=syntax normal",
			"--minus-emph-style=bold syntax normal", "--plus-emph-style=bold syntax normal",
			"--minus-non-emph-style=minus-style", "--plus-non-emph-style=plus-style",
			"--minus-empty-line-marker-style=normal normal", "--plus-empty-line-marker-style=normal normal")
		cmd.Dir = workspace
		cmd.Stdin = strings.NewReader(text)
		output, diagnostic := &liveDiffOutput{}, &liveDiffOutput{}
		cmd.Stdout, cmd.Stderr = output, diagnostic
		if err := cmd.Run(); err != nil {
			message := ansi.Truncate(liveDiffSafe(diagnostic.String(), false), 1000, "...")
			return liveDiffRender{}, fmt.Errorf("delta rendering failed: %w: %s", err, message)
		}
		text = liveDiffSafe(output.String(), true)
	}
	render := liveDiffRender{starts: make([]int, len(files)), counts: fileCounts}
	next, fileIndex := 0, 0
	highlighted, fileHeading := false, false
	renderedBytes := 0
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		// Delta leaves an empty separator for omitted hunk headings. Source
		// blanks have inline numbers or styling; never trim or strip those rows.
		if line == "" {
			continue
		}
		if next < len(targets) && strings.TrimSpace(ansi.Strip(line)) == targets[next] {
			fileHeading = fileIndex < len(fileTargets) && next == fileTargets[fileIndex]
			if fileHeading {
				render.starts[fileIndex] = len(render.lines)
				fileIndex++
			}
			if next == focusIndex {
				render.focusOffset = len(render.lines)
			}
			highlighted = highlights[next]
			next++
			continue
		}
		// Own only the two-column gutter and heading emphasis, leaving delta's
		// syntax colors intact. Reserve the final terminal column against wrapping.
		gutter := "  "
		if highlighted {
			gutter = "\x1b[36m▎\x1b[0m "
		}
		lines := []string{line}
		if fileHeading {
			counts := fileCounts[fileIndex-1]
			statsWidth := len(fmt.Sprintf(" +%d -%d", counts.added, counts.removed))
			// Keep actions and both rename endpoints in one heading block.
			// Unlike the sticky title, this block can wrap instead of losing metadata.
			lines = strings.Split(ansi.Wrap(line, max(1, width-3-statsWidth), ""), "\n")
			lines[0] = liveDiffHeader(lines[0], width-3, counts)
			for i := 1; i < len(lines); i++ {
				lines[i] = "\x1b[1m" + lines[i] + "\x1b[22m"
			}
		}
		fileHeading = false
		for _, line := range lines {
			renderedBytes += len(gutter) + len(line) + 1
			if renderedBytes > maxChangeReadBytes {
				return liveDiffRender{}, errors.New("live diff rendering exceeds 64 MiB; use hchanges with a narrower range")
			}
			render.lines = append(render.lines, gutter+line)
		}
	}
	if next != len(targets) {
		return liveDiffRender{}, errors.New("delta omitted live diff hunk boundaries")
	}
	return render, nil
}

type liveDiffOutput struct{ strings.Builder }

func (b *liveDiffOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxChangeReadBytes {
		return 0, errors.New("rendered diff exceeds 64 MiB")
	}
	return b.Builder.Write(p)
}

// RunLiveDiff is a read-only standalone viewer. It does not start a router.
func RunLiveDiff(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	flags := flag.NewFlagSet("live-diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "workspace to watch (default current directory)")
	replay := flags.String("replay-dir", "", "replay directory (default platform state directory)")
	sessionFile := flags.String("session-file", "", "private live session scope and lifetime file")
	split := flags.Bool("herdr", false, "open a sibling Herdr pane without taking focus")
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
	delta, err := exec.LookPath("delta")
	if err != nil {
		if *split {
			fmt.Fprintln(stderr, "mekugi live-diff: delta unavailable; skipping Herdr live view.")
			return 0
		}
		return fail(fmt.Errorf("delta is required: %w", err))
	}
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
	if *split {
		if err := splitLiveDiff(ctx, *workspace, *replay, stdout, nil); err != nil {
			return fail(err)
		}
		return 0
	}
	store := &mekugiReplayStore{directory: *replay}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return fail(errors.New("live view needs a terminal; use --herdr"))
	}
	if err := runLiveDiffTerminal(ctx, store, *workspace, delta, stdin, stdout, *sessionFile); err != nil {
		return fail(err)
	}
	return 0
}

func runLiveDiffTerminal(ctx context.Context, store *mekugiReplayStore, workspace string, delta string, stdin, stdout *os.File, sessionFile string) (err error) {
	old, err := term.MakeRaw(int(stdin.Fd()))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Restore(int(stdin.Fd()), old)) }()
	if _, err := io.WriteString(stdout, "\x1b[?1049h\x1b[?25l"); err != nil {
		return err
	}
	defer func() { _, e := io.WriteString(stdout, "\x1b[0m\x1b[?25h\x1b[?1049l"); err = errors.Join(err, e) }()
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
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	view := liveDiffView{scroll: make(map[string]int), following: true}
	var previous map[string]changeIndex
	var rendering liveDiffRender
	var rendered []liveDiffFile
	var renderedFocus liveDiffChunk
	renderedFocusFile := -1
	lastWidth, lastHeight := 0, 0
	dirty := true
	refresh := true
	escape := ""
	for {
		width, height, e := term.GetSize(int(stdout.Fd()))
		if e != nil {
			return e
		}
		width, height = max(1, width), max(3, height)
		if refresh {
			indexes, e := store.liveDiffIndexes(workspace, sessionFile)
			if errors.Is(e, errLiveDiffSessionEnded) {
				return nil
			}
			if e != nil {
				return e
			}
			if !reflect.DeepEqual(indexes, previous) {
				for path, prior := range previous {
					for id := range prior.Changes {
						if _, exists := indexes[path].Changes[id]; !exists {
							return errors.New("change records were removed; restart the live view")
						}
					}
				}
				files, e := store.liveDiffSnapshotFiles(ctx, indexes)
				if e != nil {
					return e
				}
				view.merge(files)
				view.refreshVisible()
				previous, dirty = indexes, true
			}
			refresh = false
		}
		files := make([]liveDiffFile, len(view.files))
		focusFile := -1
		for i, file := range view.files {
			files[i] = view.visible[file.key()]
			if slices.ContainsFunc(file.chunks, func(c liveDiffChunk) bool { return c.key == view.latest }) {
				focusFile = i
			}
		}
		focus := view.latestChunk()
		focus.snapshotOrder = 0 // Snapshot numbering does not change a capture's geometry.
		if !reflect.DeepEqual(rendered, files) || renderedFocus != focus || renderedFocusFile != focusFile || width != lastWidth {
			rendering, e = renderLiveDiff(ctx, files, delta, workspace, width, focusFile, focus)
			if e != nil {
				return e
			}
			rendered, renderedFocus, renderedFocusFile = files, focus, focusFile
			dirty = true
		}
		if height != lastHeight {
			dirty = true
		}
		lastWidth, lastHeight = width, height
		lines := rendering.lines
		rows := height - 2
		offset := 0
		if len(view.files) > 0 {
			start := rendering.starts[view.selected]
			end := len(lines)
			if view.selected+1 < len(view.files) {
				end = rendering.starts[view.selected+1]
			}
			offset = start + min(view.scroll[view.files[view.selected].key()], max(0, end-start-1))
		}
		if view.following {
			offset = min(rendering.focusOffset, max(0, len(lines)-rows))
		}
		view.scrollTo(rendering, offset)
		var active liveDiffFile
		if len(view.files) > 0 {
			active = files[view.selected]
		}
		if dirty {
			header := "Waiting for captured workspace edits..."
			if len(view.files) > 0 {
				label := liveDiffDisplayPath(workspace, active.path)
				end := len(lines)
				if view.selected+1 < len(files) {
					end = rendering.starts[view.selected+1]
				}
				start := rendering.starts[view.selected]
				header = fmt.Sprintf("%d/%d  %s  | row %d/%d", view.selected+1, len(view.files), label, offset-start+1, end-start)
				if len(active.chunks) == 0 {
					header = fmt.Sprintf("%d/%d  %s  | No unreviewed changes", view.selected+1, len(view.files), label)
				}
			}
			var screen strings.Builder
			writeRow := func(row int, text string) {
				fmt.Fprintf(&screen, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", row, ansi.Truncate(text, max(0, width-1), ""))
			}
			if len(files) > 0 {
				header = liveDiffHeader(header, width-1, rendering.counts[view.selected])
			} else {
				header = liveDiffSafe(header, false)
			}
			writeRow(1, header)
			for row := range rows {
				text := ""
				if offset+row < len(lines) {
					text = lines[offset+row]
				}
				writeRow(row+2, text)
			}
			mode := "FOLLOW"
			if !view.following {
				mode = "PAUSED"
				if view.unseenUpdate {
					mode += " · new changes available"
				}
			}
			writeRow(height, mode+" · r resume · j/k scroll · n/p file · f/F flush · q quit")
			if _, e := io.WriteString(stdout, screen.String()); e != nil {
				return e
			}
			dirty = false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			refresh = true
		case key, open := <-keys:
			if !open {
				return nil
			}
			// Decode common terminal keys incrementally, including fragmented reads.
			if key == 27 {
				escape = "\x1b"
				continue
			}
			if escape != "" {
				escape += string(key)
				switch escape {
				case "\x1b[", "\x1b[5", "\x1b[6", "\x1bO":
					continue
				case "\x1b[A", "\x1bOA":
					key = 'k'
				case "\x1b[B", "\x1bOB":
					key = 'j'
				case "\x1b[C", "\x1bOC":
					key = 'n'
				case "\x1b[D", "\x1bOD":
					key = 'p'
				case "\x1b[5~":
					key = 'b'
				case "\x1b[6~":
					key = ' '
				}
				escape = ""
			}
			if strings.ContainsRune("np\tjk bgG", rune(key)) {
				view.following = false
			}
			switch key {
			case 'q', 3:
				return nil
			case 'r':
				view.followLatest()
			case 'f', 'F':
				view.flush(key == 'F')
			case 'n', '\t':
				if len(view.files) > 0 {
					view.selected = (view.selected + 1) % len(view.files)
				}
			case 'p':
				if len(view.files) > 0 {
					view.selected = (view.selected + len(view.files) - 1) % len(view.files)
				}
			case 'j':
				view.scrollTo(rendering, offset+1)
			case 'k':
				view.scrollTo(rendering, offset-1)
			case ' ':
				view.scrollTo(rendering, offset+rows)
			case 'b':
				view.scrollTo(rendering, offset-rows)
			case 'g':
				view.scrollTo(rendering, 0)
			case 'G':
				view.scrollTo(rendering, max(0, len(lines)-rows))
			}
			dirty = true
		}
	}
}
