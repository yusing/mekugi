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

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	"github.com/yusing/mekugi/internal/sourcekind"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// markdownFileTarget shares existence and workspace resolution between painting
// and opening. Opening checks again because the file can change after painting.
func markdownFileTarget(workspace, target string) (path, location string, first, last int, ok bool) {
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
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		path, location, first, last = markdownFileLocation(path)
		if first > 0 {
			info, err = os.Stat(path)
		}
	}
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	return path, location, first, last, true
}

func (u *appServerUI) markdownFileExists(target string) bool {
	_, _, _, _, ok := markdownFileTarget(u.session.cwd, target)
	return ok
}

// openMarkdownFile uses only session workspace metadata for relative paths.
func (u *terminalUI) openMarkdownFile(view *liveActivityView, target string) bool {
	workspace := ""
	if u.main != nil {
		workspace = u.main.session.cwd
	}
	path, location, first, last, ok := markdownFileTarget(workspace, target)
	if !ok {
		return false
	}
	display := pathdisplay.ForWorkspace(workspace, path)
	block := activityui.Block{Kind: "reads", Verb: "Read", Path: display + location, SyntaxPath: path, Reads: []activityui.Read{{Path: display + location}}}
	data, err := readMarkdownFile(path)
	source := string(data)
	content := livediff.Safe(source, false)
	if err != nil {
		block.Kind, block.Verb, block.Body = "error", "", err.Error()
	} else {
		block.Tail = strings.Split(content, "\n")
	}
	pages := []activityui.Block{block}
	if format, _ := sourcekind.Classify(path); err == nil && format.Kind == "markdown" {
		rendered := block
		rendered.Body, rendered.Tail = content, nil
		rendered.Detail = activityui.Dim + fmt.Sprintf("%d lines", len(block.Tail)) + activityui.Undim
		pages = []activityui.Block{rendered, block}
	}
	u.openBlocks(view, pages)
	u.output.filePath, u.output.pendingLine = display, first
	u.output.fileFirst, u.output.fileLast = first, last
	u.output.fileText = source
	return true
}

// A location is optional. Exact existing file names are checked before it is
// removed, so files whose names end in a colon and digits still open normally.
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
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
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
