package router

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/textsanitize"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// markdownFileTarget admits local destinations lexically. Only opening touches
// the filesystem, and relative paths require the selected workspace metadata.
func markdownFileTarget(workspace, target string) (path string, ok bool) {
	path = target
	if strings.HasPrefix(target, "file:") {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host != "" && parsed.Host != "localhost" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return
		}
		path = parsed.Path
	} else {
		candidate, _, _, _ := markdownFileLocation(target)
		if !filepath.IsAbs(candidate) && (candidate == "" || strings.HasPrefix(candidate, "#") || strings.HasPrefix(candidate, "?") || strings.Contains(strings.SplitN(candidate, "/", 2)[0], ":")) {
			return
		}
	}
	if strings.Contains(path, "://") || path == "" || strings.ContainsAny(path, "\x00\r\n\x1b") {
		return
	}
	if !filepath.IsAbs(path) {
		if !filepath.IsAbs(workspace) {
			return
		}
		path = filepath.Join(workspace, path)
	}
	return path, true
}

func (u *appServerUI) markdownFileAllowed(target string) bool {
	_, ok := markdownFileTarget(u.session.cwd, target)
	return ok
}

type markdownFileResult struct {
	dialog       *outputDialog
	attachment   *scope.Store
	thread       string
	workspace    string
	path, source string
	location     string
	first, last  int
	err          error
}

func (u *terminalUI) markdownFileUpdates() <-chan markdownFileResult {
	if u.output == nil {
		return nil
	}
	return u.output.fileRead
}

// openMarkdownFile publishes loading before starting one bounded physical read.
// Retiring a dialog cannot release the slot of a still-blocked filesystem call.
func (u *terminalUI) openMarkdownFile(view *activityAdapter, target string) bool {
	workspace := ""
	if u.main != nil {
		workspace = u.main.session.cwd
	}
	path, ok := markdownFileTarget(workspace, target)
	if !ok {
		return false
	}
	display := pathdisplay.ForWorkspace(workspace, path)
	u.openBlocks(view, []activityui.Block{{Kind: "reads", Verb: "Read", Path: display, Body: "Loading…"}})
	d := u.output
	d.file = &activitypresentation.DetailFile{Path: display}
	result := markdownFileResult{dialog: d, workspace: workspace, path: path}
	if u.main != nil {
		result.attachment = u.main.session.Scope()
		result.thread = u.main.sessionPresentation().View().Displayed.ID
	}
	if u.fileReadSlot == nil {
		u.fileReadSlot = make(chan struct{}, 1)
	}
	select {
	case u.fileReadSlot <- struct{}{}:
	default:
		result.err = fmt.Errorf("local file read capacity exhausted; close and reopen after the pending read finishes")
		u.loadedMarkdownFile(result)
		return true
	}
	results := make(chan markdownFileResult, 1)
	d.fileRead = results
	go func(slot chan struct{}) {
		data, err := readMarkdownFile(path)
		if os.IsNotExist(err) {
			base, location, first, last := markdownFileLocation(path)
			if first > 0 {
				result.path, result.location, result.first, result.last = base, location, first, last
				data, err = readMarkdownFile(base)
			}
		}
		result.source, result.err = string(data), err
		<-slot
		results <- result
	}(u.fileReadSlot)
	return true
}

func (u *terminalUI) loadedMarkdownFile(result markdownFileResult) bool {
	d := result.dialog
	if u.output != d {
		return false
	}
	if u.main != nil && (u.main.session.Scope() != result.attachment || u.main.sessionPresentation().View().Displayed.ID != result.thread || u.main.session.cwd != result.workspace) {
		u.output = nil
		return true
	}
	display := pathdisplay.ForWorkspace(result.workspace, result.path)
	block := activityui.Block{Kind: "reads", Verb: "Read", Path: display + result.location, SyntaxPath: result.path, Reads: []activitymodel.Read{{Path: display + result.location}}}
	content := textsanitize.Safe(result.source, false)
	if result.err != nil {
		block.Kind, block.Verb, block.Body = "error", "", textsanitize.Safe(result.err.Error(), false)
	} else {
		block.Tail = strings.Split(content, "\n")
	}
	pages := []activityui.Block{block}
	if format, _ := sourcekind.Classify(result.path); result.err == nil && format.Kind == "markdown" {
		rendered := block
		rendered.Body, rendered.Tail = content, nil
		rendered.DetailLines = len(block.Tail)
		pages = []activityui.Block{rendered, block}
	}
	d.fileRead = nil
	d.file = &activitypresentation.DetailFile{Path: display, Source: result.source, First: result.first, Last: result.last}
	// Publish source now; layout applies tabs and the reveal after any frozen
	// loading selection is cleared. Changing chrome here would dismiss it.
	d.pages = nil
	for _, page := range activitypresentation.DetailPages(activityui.Values(pages)) {
		d.pages = append(d.pages, outputPage{DetailPage: page})
	}
	return true
}

// A location is optional. Opening tries the exact name before removing it,
// preserving files whose names end in a colon and digits.
func markdownFileLocation(path string) (base, location string, first, last int) {
	colon := strings.LastIndexByte(path, ':')
	if colon < 0 {
		return path, "", 0, 0
	}
	from, to, ranged := strings.Cut(path[colon+1:], "-")
	first, err := strconv.Atoi(from)
	if err != nil || first <= 0 {
		return path, "", 0, 0
	}
	last = first
	if ranged {
		last, err = strconv.Atoi(to)
		if err != nil || last < first {
			return path, "", 0, 0
		}
	}
	return path[:colon], path[colon:], first, last
}

// Keep this auxiliary read bounded without applying the composer's attachment
// envelope budget. Larger or binary files show a copyable read error.
func readMarkdownFile(path string) ([]byte, error) {
	const limit = 8 << 20
	f, err := sourceparse.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The selected path may have been replaced before it was opened.
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("file exceeds %d-byte dialog limit", limit)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("file is not UTF-8 text")
	}
	return data, nil
}
