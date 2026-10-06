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
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// openMarkdownFile uses only session workspace metadata for relative paths.
// Unrecognized and missing destinations retain their existing click behavior.
func (u *terminalUI) openMarkdownFile(view *liveActivityView, target string) bool {
	path := target
	if strings.HasPrefix(target, "file:") {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host != "" && parsed.Host != "localhost" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return false
		}
		path = parsed.Path
	} else {
		candidate := target
		if colon := strings.LastIndexByte(candidate, ':'); colon >= 0 {
			if n, err := strconv.Atoi(candidate[colon+1:]); err == nil && n > 0 {
				candidate = candidate[:colon]
			}
		}
		if !filepath.IsAbs(candidate) && (candidate == "" || strings.HasPrefix(candidate, "#") || strings.HasPrefix(candidate, "?") || strings.Contains(strings.SplitN(candidate, "/", 2)[0], ":")) {
			return false
		}
	}
	if strings.Contains(path, "://") || path == "" || strings.ContainsAny(path, "\x00\r\n\x1b") {
		return false
	}
	workspace := ""
	if u.main != nil {
		workspace = u.main.session.cwd
	}
	if !filepath.IsAbs(path) {
		if !filepath.IsAbs(workspace) {
			return false
		}
		path = filepath.Join(workspace, path)
	}
	info, err := os.Stat(path)
	line := 0
	if os.IsNotExist(err) {
		if colon := strings.LastIndexByte(path, ':'); colon >= 0 {
			if n, parseErr := strconv.Atoi(path[colon+1:]); parseErr == nil && n > 0 {
				path, line = path[:colon], n
				info, err = os.Stat(path)
			}
		}
	}
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	display := pathdisplay.ForWorkspace(workspace, path)
	block := activityui.Block{Kind: "reads", Verb: "Read", Path: display, SyntaxPath: path, Reads: []activityui.Read{{Path: display}}}
	data, err := readMarkdownFile(path)
	if err != nil {
		block.Kind, block.Verb, block.Body = "error", "", err.Error()
	} else {
		block.Tail = strings.Split(livediff.Safe(string(data), false), "\n")
	}
	u.openBlocks(view, []activityui.Block{block})
	u.output.filePath, u.output.pendingLine = display, line
	u.output.fileText = string(data)
	return true
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
