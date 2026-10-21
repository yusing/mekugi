package launch

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

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/libraries/textsanitize"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourcekind"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// markdownFileTarget resolves local destinations lexically. Relative paths
// require the selected workspace metadata.
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

// Capture only detached workspace metadata for lexical rendering admission.
func markdownFileLinks(workspace string) func(string) bool {
	return func(target string) bool {
		_, ok := markdownFileTarget(workspace, target)
		return ok
	}
}

// detailFiles bounds asynchronous acquisition independently of live session control.
// The physical slot survives attachment replacement until its read completes.
type detailFiles struct {
	links        markdownLinkChecks
	fileReadSlot chan struct{}
	fileReads    <-chan markdownFileResult
}

type markdownFileResult struct {
	id           sessionpresentation.DetailID
	thread       string
	workspace    string
	path, source string
	location     string
	first, last  int
	err          error
}

// openMarkdownFile publishes loading before starting one bounded physical read.
// Retiring a dialog cannot release the slot of a still-blocked filesystem call.
func (u *detailFiles) openMarkdownFile(session *sessionpresentation.Attachment, model *sessionpresentation.Model, workspace string, source *sessionpresentation.ActivitySource, terminal *terminalsession.Attachment, view *activityui.View, target string) bool {
	path, ok := markdownFileTarget(workspace, target)
	if !ok {
		return false
	}
	display := pathdisplay.ForWorkspace(workspace, path)
	u.openDetailSelection(session, model.View().Version, source, terminal, view, activitypresentation.DetailPages([]activitypresentation.Value{{Kind: "reads", Verb: "Read", Path: display, Body: "Loading…"}}), false)
	d := terminal.Dialog
	d.File = &activitypresentation.DetailFile{Path: display}
	result := markdownFileResult{id: d.ID, workspace: workspace, path: path, thread: model.View().Displayed.ID}
	if u.fileReadSlot == nil {
		u.fileReadSlot = make(chan struct{}, 1)
	}
	select {
	case u.fileReadSlot <- struct{}{}:
	default:
		result.err = fmt.Errorf("local file read capacity exhausted; close and reopen after the pending read finishes")
		u.finishMarkdownFile(session, terminal, result)
		return true
	}
	results := make(chan markdownFileResult, 1)
	u.fileReads = results
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

func (u *detailFiles) loadedMarkdownFile(session *sessionpresentation.Attachment, presentations *sessionpresentation.Service, workspace string, terminal *terminalsession.Attachment, result markdownFileResult) bool {
	d := terminal.Dialog
	if d == nil || d.ID != result.id {
		return false
	}
	_, current := session.DetailView(result.id)
	if !current || presentations.Session(session.Scope()).View().Displayed.ID != result.thread || workspace != result.workspace {
		u.closeOutput(session, terminal)
		return true
	}
	return u.finishMarkdownFile(session, terminal, result)
}

func (u *detailFiles) finishMarkdownFile(session *sessionpresentation.Attachment, terminal *terminalsession.Attachment, result markdownFileResult) bool {
	d := terminal.Dialog
	display := pathdisplay.ForWorkspace(result.workspace, result.path)
	block := activitypresentation.Value{Kind: "reads", Verb: "Read", Path: display + result.location, SyntaxPath: result.path, Reads: []activitymodel.Read{{Path: display + result.location}}}
	content := textsanitize.Safe(result.source, false)
	if result.err != nil {
		block.Kind, block.Verb, block.Body = "error", "", textsanitize.Safe(result.err.Error(), false)
	} else {
		block.Tail = strings.Split(content, "\n")
	}
	pages := []activitypresentation.Value{block}
	if format, _ := sourcekind.Classify(result.path); result.err == nil && format.Kind == "markdown" {
		rendered := block
		rendered.Body, rendered.Tail = content, nil
		rendered.DetailLines = len(block.Tail)
		pages = []activitypresentation.Value{rendered, block}
	}
	u.fileReads = nil
	d.File = &activitypresentation.DetailFile{Path: display, Source: result.source, First: result.first, Last: result.last}
	// Publish source now; layout applies tabs and the reveal after any frozen
	// loading selection is cleared. Changing chrome here would dismiss it.
	u.replaceDetails(session, terminal, activitypresentation.DetailPages(pages))
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
