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

	"github.com/yusing/mekugi/internal/persistence"
)

// Session preferences are separate from correctness/replay records. Applied
// settings are host-confirmed defaults. Batch sandbox snapshots restore host
// settings through Codex; they never authorize execution or revive live work.
type nativePaneState struct {
	Version          int                   `json:"version"`
	Focus            int                   `json:"focus"`
	Split            int                   `json:"split"`
	DiffOpen         bool                  `json:"diffOpen"`
	JournalOpen      bool                  `json:"journalOpen,omitzero"`
	InterruptLocked  bool                  `json:"interruptLocked,omitzero"`
	NavigatorColumns int                   `json:"navigatorColumns"`
	Settings         nativeAppliedSettings `json:"settings,omitzero"`
	ResumeObserved   time.Time             `json:"resumeObserved,omitzero"`
	ResumeEvidence   nativeResumeEvidence  `json:"resumeEvidence,omitzero"`
}

// Applied sandbox policies can contain more than the old layout-only budget.
const nativePaneStateBytes = 1 << 20

// Pre-resume evidence is already authoritative history, not confirmation of
// the corrective update. TierKnown preserves absence in older turn contexts.
type nativeResumeEvidence struct {
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Tier      string `json:"tier"`
	TierKnown bool   `json:"tierKnown"`
}

func (s nativeResumeEvidence) config() map[string]any {
	config := (nativeAppliedSettings{Model: s.Model, Effort: s.Effort, Tier: s.Tier}).config()
	if !s.TierKnown {
		delete(config, "service_tier")
	}
	return config
}

// Model identifies a complete snapshot. Empty effort/tier explicitly select the
// host default, rather than meaning that a saved value is unknown.
type nativeAppliedSettings struct {
	PermissionProfile string    `json:"permissionProfile,omitzero"`
	SandboxPolicy     string    `json:"sandboxPolicy,omitzero"`
	Model             string    `json:"model"`
	Effort            string    `json:"effort"`
	Tier              string    `json:"tier"`
	Observed          time.Time `json:"observed"`
}

func (s nativeAppliedSettings) config() map[string]any {
	if s.Model == "" {
		return nil
	}
	config := map[string]any{"model": s.Model, "model_reasoning_effort": nil, "service_tier": nil}
	if s.Effort != "" {
		config["model_reasoning_effort"] = s.Effort
	}
	if s.Tier != "" {
		config["service_tier"] = s.Tier
	}
	return config
}

func (u *terminalUI) paneState() nativePaneState {
	state := nativePaneState{Version: 1, Focus: u.focus, Split: u.split, DiffOpen: u.diffOpen, JournalOpen: !u.diffOpen && (u.journalOpen || u.autoActivity), NavigatorColumns: u.diff.navigation.Columns}
	if u.main != nil {
		state.InterruptLocked = u.main.interruptLocked
	}
	return state
}

type nativePanePersistence struct {
	path              string
	workspace, thread string
	settings          nativeAppliedSettings
	resumeObserved    time.Time
	resumeEvidence    nativeResumeEvidence
	last              nativePaneState
	pending           bool
	due               time.Time
}

func (p *nativePanePersistence) open(u *terminalUI, workspace, thread string, resume bool) error {
	p.path, p.workspace, p.thread = "", workspace, thread
	p.settings, p.pending = nativeAppliedSettings{}, false
	p.resumeObserved = time.Time{}
	p.resumeEvidence = nativeResumeEvidence{}
	path, err := nativePaneStatePath(workspace, thread)
	if err != nil {
		return err
	}
	p.path = path
	defer func() {
		p.last = u.paneState()
		p.last.Settings = p.settings
		p.last.ResumeObserved = p.resumeObserved
		p.last.ResumeEvidence = p.resumeEvidence
	}()
	if !resume {
		return nil
	}
	state, err := p.read(workspace, thread)
	if err != nil {
		return err
	}
	if state.Version == 0 {
		return nil
	}
	p.settings = state.Settings
	p.resumeObserved = state.ResumeObserved
	p.resumeEvidence = state.ResumeEvidence
	u.focus, u.split, u.diffOpen, u.journalOpen = state.Focus, state.Split, state.DiffOpen, state.JournalOpen
	u.main.interruptLocked = state.InterruptLocked
	u.diff.navigation.Columns = state.NavigatorColumns
	return nil
}

func nativePaneStatePath(workspace, thread string) (string, error) {
	if !filepath.IsAbs(workspace) || thread == "" {
		return "", errors.New("session preferences require a host workspace and thread")
	}
	base, err := defaultMekugiReplayDirectory()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(workspace + "\x00" + thread))
	return filepath.Join(filepath.Dir(base), "ui", fmt.Sprintf("panes-%x.json", key)), nil
}

// Read another namespace without moving the live owner's write target.
func (p *nativePanePersistence) read(workspace, thread string) (nativePaneState, error) {
	path, err := nativePaneStatePath(workspace, thread)
	if err != nil {
		return nativePaneState{}, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nativePaneState{}, nil
	}
	if err != nil {
		return nativePaneState{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, nativePaneStateBytes+1))
	if err != nil {
		return nativePaneState{}, err
	}
	if len(data) > nativePaneStateBytes {
		return nativePaneState{}, errors.New("pane state exceeds size limit")
	}
	var state nativePaneState
	if err := json.Unmarshal(data, &state); err != nil {
		return nativePaneState{}, err
	}
	if state.Version == 1 && state.Focus == 5 {
		state.Focus = 3 // The former Orchestration selector now belongs to Agents.
	}
	if state.Version != 1 || state.Focus < 0 || state.Focus > 4 || state.Split < 0 || state.Split > 10000 || state.NavigatorColumns < 0 || state.NavigatorColumns > 10000 || state.Focus == 1 && !state.DiffOpen || state.Focus == 2 && (state.DiffOpen || state.JournalOpen) || state.DiffOpen && state.JournalOpen || state.Focus == 4 && !state.JournalOpen {
		return nativePaneState{}, errors.New("invalid or unsupported pane state")
	}
	if state.Settings != (nativeAppliedSettings{}) && (state.Settings.Model == "" || state.Settings.Observed.IsZero()) {
		return nativePaneState{}, errors.New("incomplete applied settings snapshot")
	}
	return state, nil
}

// Coalesce dragging and shortcut bursts, then flush a pending change on exit.
// Failed saves are reported once, not retried on every animation frame.
func (p *nativePanePersistence) save(u *terminalUI, now time.Time, flush bool) error {
	if p == nil || p.path == "" {
		return nil
	}
	state := u.paneState()
	state.Settings = p.settings
	state.ResumeObserved = p.resumeObserved
	state.ResumeEvidence = p.resumeEvidence
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
	if len(data) > nativePaneStateBytes {
		return errors.New("pane state exceeds size limit")
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0700); err != nil {
		return err
	}
	return persistence.AtomicFile(p.path, "panes-pending-", data, nil)
}

// Only authoritative start/resume results and settings notifications reach here.
// Flush applied settings immediately; dragging still uses the existing debounce.
func (u *appServerUI) retainAppliedSettings() {
	if u.panes == nil || u.panes.path == "" || u.model == "" || u.settings.restoreEffort || u.resumeSandbox != nil || u.resumePermissionProfile != "" {
		return
	}
	if u.panes.workspace != u.session.cwd || u.panes.thread != u.thread {
		if err := u.panes.open(u.shell, u.session.cwd, u.thread, false); err != nil {
			u.paneError(err)
			return
		}
	}
	u.panes.settings = nativeAppliedSettings{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier, Observed: u.now()}
	var profile struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(u.statusConfig.PermissionProfile, &profile) == nil {
		u.panes.settings.PermissionProfile = profile.ID
	}
	if u.statusConfig.PermissionProfile.Kind() == 'n' && u.statusConfig.Sandbox.Type != "" {
		policy, err := json.Marshal(&u.statusConfig.Sandbox)
		if err != nil {
			u.paneError(err)
			return
		}
		u.panes.settings.SandboxPolicy = string(policy)
	}
	u.panes.resumeObserved = time.Time{}
	u.panes.resumeEvidence = nativeResumeEvidence{}
	u.paneError(u.panes.save(u.shell, u.now(), true))
}

func (u *appServerUI) paneError(err error) {
	if err == nil {
		return
	}
	u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: u.view.lastSeq + 1, Agent: "Session", Kind: "text", Text: "Pane layout or settings could not be restored or saved: " + err.Error(), Observed: time.Now()}}})
	u.dirty = true
}
