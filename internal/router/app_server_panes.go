package router

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Presentation preferences are separate from correctness/replay records. They
// never contain transcript text, live resources, or execution authorization.
type nativePaneState struct {
	Version          int  `json:"version"`
	Focus            int  `json:"focus"`
	Split            int  `json:"split"`
	DiffOpen         bool `json:"diffOpen"`
	NavigatorColumns int  `json:"navigatorColumns"`
}

func (u *terminalUI) paneState() nativePaneState {
	return nativePaneState{Version: 1, Focus: u.focus, Split: u.split, DiffOpen: u.diffOpen, NavigatorColumns: u.diff.navigation.columns}
}

type nativePanePersistence struct {
	path    string
	last    nativePaneState
	pending bool
	due     time.Time
}

func (p *nativePanePersistence) open(u *terminalUI, workspace, thread string, resume bool) error {
	base, err := defaultMekugiReplayDirectory()
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(workspace + "\x00" + thread))
	p.path = filepath.Join(filepath.Dir(base), "ui", fmt.Sprintf("panes-%x.json", key))
	defer func() { p.last = u.paneState() }()
	if !resume {
		return nil
	}
	f, err := os.Open(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return errors.New("pane state exceeds size limit")
	}
	var state nativePaneState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != 1 || state.Focus < 0 || state.Focus > 3 || state.Split < 0 || state.Split > 10000 || state.NavigatorColumns < 0 || state.NavigatorColumns > 10000 || state.Focus == 1 && !state.DiffOpen || state.Focus == 2 && state.DiffOpen {
		return errors.New("invalid or unsupported pane state")
	}
	u.focus, u.split, u.diffOpen = state.Focus, state.Split, state.DiffOpen
	u.diff.navigation.columns = state.NavigatorColumns
	return nil
}

// Coalesce dragging and shortcut bursts, then flush a pending change on exit.
// Failed saves are reported once, not retried on every animation frame.
func (p *nativePanePersistence) save(u *terminalUI, now time.Time, flush bool) error {
	if p == nil || p.path == "" {
		return nil
	}
	state := u.paneState()
	if state != p.last {
		p.last, p.pending, p.due = state, true, now.Add(250*time.Millisecond)
	}
	if !p.pending || !flush && now.Before(p.due) {
		return nil
	}
	p.pending = false
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0700); err != nil {
		return err
	}
	return writeAtomicFile(p.path, "panes-pending-", data, true)
}

func (u *appServerUI) paneError(err error) {
	if err == nil {
		return
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: "Pane layout could not be restored or saved: " + err.Error(), Observed: time.Now()}}})
	u.dirty = true
}
