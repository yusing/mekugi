package router

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// sessionTitleScanBuffer bounds one client session-index record.
const sessionTitleScanBuffer = 1 << 20

type sessionTitleCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]func() string
}

func newSessionTitleCache() *sessionTitleCache {
	return newSessionTitleCacheAt(codexSessionIndexPath())
}

func newSessionTitleCacheAt(path string) *sessionTitleCache {
	return &sessionTitleCache{path: path, entries: make(map[string]func() string)}
}

func (c *sessionTitleCache) title(sessionID string) string {
	if c == nil || sessionID == "" {
		return ""
	}
	c.mu.Lock()
	entry := c.entries[sessionID]
	if entry == nil {
		entry = sync.OnceValue(func() string {
			return scanSessionTitle(c.path, sessionID)
		})
		c.entries[sessionID] = entry
	}
	c.mu.Unlock()
	return entry()
}

func codexSessionIndexPath() string {
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexHome, "session_index.jsonl")
}

func scanSessionTitle(path, sessionID string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	idNeedle := `"id":` + strconv.Quote(sessionID)
	var title string
	scanner := bufio.NewScanner(file)
	// Index records are whole JSON lines that can exceed the default 64 KiB
	// token, which would otherwise end the scan early and silently resolve an
	// older title than the newest matching record.
	scanner.Buffer(make([]byte, 0, sessionTitleScanBuffer), sessionTitleScanBuffer)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, idNeedle) {
			continue
		}
		if candidate, ok := jsonStringField(line, "thread_name"); ok && candidate != "" {
			title = candidate
		}
	}
	// The title is an optional display label, so an unreadable tail keeps the
	// newest title already resolved rather than failing the snapshot.
	_ = scanner.Err()
	return title
}

func jsonStringField(line, name string) (string, bool) {
	key := strconv.Quote(name)
	_, after, ok := strings.Cut(line, key)
	if !ok {
		return "", false
	}
	remaining := strings.TrimLeft(after, " \t")
	if !strings.HasPrefix(remaining, ":") {
		return "", false
	}
	remaining = strings.TrimLeft(remaining[1:], " \t")
	if !strings.HasPrefix(remaining, `"`) {
		return "", false
	}
	escaped := false
	for end := 1; end < len(remaining); end++ {
		switch {
		case escaped:
			escaped = false
		case remaining[end] == '\\':
			escaped = true
		case remaining[end] == '"':
			quoted := strings.ReplaceAll(remaining[:end+1], `\/`, `/`)
			value, err := strconv.Unquote(quoted)
			return value, err == nil
		}
	}
	return "", false
}
