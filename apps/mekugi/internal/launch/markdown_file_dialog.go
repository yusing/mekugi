package launch

import (
	"bytes"
	"context"
	"errors"
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
	details      sessionpresentation.Details
	context      context.Context
	cancelRead   context.CancelFunc
	links        markdownLinkChecks
	fileReadSlot chan struct{}
	fileReads    <-chan markdownFileResult
	pendingFile  *terminalsession.ContentDialog
	copyFilePath func(string)
}

type markdownFileResult struct {
	summary      bool
	id           sessionpresentation.DetailID
	workspace    string
	path, source string
	location     string
	first, last  int
	err          error
	classified   bool
	copyPath     bool
}

// openMarkdownFile admits a selection before starting one bounded physical read.
// Its loading dialog remains hidden until the path is known to be regular.
// Retiring a dialog cannot release the slot of a still-blocked filesystem call.
func (u *detailFiles) openMarkdownFile(session *sessionpresentation.Attachment, model *sessionpresentation.Model, workspace string, source *sessionpresentation.ActivitySource, terminal *terminalsession.Attachment, view *activityui.View, target string) bool {
	path, ok := markdownFileTarget(workspace, target)
	if !ok {
		return false
	}
	display := pathdisplay.ForWorkspace(workspace, path)
	u.openDetailSelection(session, model.View().Version, source, terminal, view, activitypresentation.DetailPages([]activitypresentation.Value{{Kind: "reads", Verb: "Read", Path: display, Body: "Loading…"}}), false)
	d := terminal.Dialog
	u.pendingFile, terminal.Dialog = d, nil
	d.File = &activitypresentation.DetailFile{Path: display}
	result := markdownFileResult{id: d.ID, workspace: workspace, path: path}
	if u.fileReadSlot == nil {
		u.fileReadSlot = make(chan struct{}, 1)
	}
	select {
	case u.fileReadSlot <- struct{}{}:
	default:
		terminal.Dialog, u.pendingFile = d, nil
		result.err = fmt.Errorf("local file read capacity exhausted; close and reopen after the pending read finishes")
		u.finishMarkdownFile(session, terminal, result)
		return true
	}
	parent := u.context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	u.cancelRead = cancel
	results := make(chan markdownFileResult, 2)
	u.fileReads = results
	go func(slot chan struct{}) {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			base, location, first, last := markdownFileLocation(path)
			if first > 0 {
				result.path, result.location, result.first, result.last = base, location, first, last
				info, err = os.Stat(base)
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		var data []byte
		if err == nil && !info.Mode().IsRegular() {
			result.copyPath = true
		} else if err == nil {
			loading := result
			loading.classified = true
			results <- loading
			data, err = readMarkdownFile(ctx, result.path)
			result.copyPath = errors.Is(err, errMarkdownNonregular)
			if result.copyPath {
				err = nil
			}
		}
		result.source, result.err = string(data), err
		<-slot
		results <- result
	}(u.fileReadSlot)
	return true
}

func (u *detailFiles) loadedMarkdownFile(session *sessionpresentation.Attachment, model *sessionpresentation.Model, workspace string, terminal *terminalsession.Attachment, result markdownFileResult) (changed bool, copyPath string) {
	d := terminal.Dialog
	if u.pendingFile != nil {
		d = u.pendingFile
	}
	if d == nil || d.ID != result.id {
		return false, ""
	}
	_, current := u.details.DetailView(result.id)
	if !current || session.Scope() == nil || model.View().Version.Attachment != result.id.Attachment || workspace != result.workspace {
		u.closeOutput(terminal)
		return true, ""
	}
	if result.classified {
		terminal.Dialog, u.pendingFile = d, nil
		return true, ""
	}
	if result.summary {
		block := activitypresentation.Value{Kind: "compaction", Body: textsanitize.MarkdownSource(result.source)}
		if result.err != nil {
			block.Kind, block.Body = "error", result.err.Error()
		}
		u.stopFileRead()
		u.replaceDetails(session, terminal, activitypresentation.DetailPages([]activitypresentation.Value{block}))
		return true, ""
	}
	if result.copyPath {
		if u.copyFilePath != nil {
			u.copyFilePath(result.path)
			u.closeOutput(terminal)
			return true, ""
		}
		u.closeOutput(terminal)
		return true, result.path
	}
	terminal.Dialog, u.pendingFile = d, nil
	return u.finishMarkdownFile(session, terminal, result), ""
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
	u.stopFileRead()
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

var errMarkdownNonregular = errors.New("file is not a regular file")

// Keep this auxiliary read bounded without applying the composer's attachment
// envelope budget. Larger or binary files show a copyable read error.
func readMarkdownFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const limit = 8 << 20
	f, err := sourceparse.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stop := context.AfterFunc(ctx, func() { _ = f.Close() })
	defer stop()
	// The selected path may have been replaced before it was opened.
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errMarkdownNonregular
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func (u *detailFiles) stopFileRead() {
	if u.cancelRead != nil {
		u.cancelRead()
		u.cancelRead = nil
	}
	u.fileReads = nil
	u.pendingFile = nil
	u.copyFilePath = nil
}
