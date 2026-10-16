package preferences

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Source: internal/router/app_server_panes.go:19:205@8bcb5cbf nativePanePersistence

type Service struct{ history *history.Service }

func New(history *history.Service) (*Service, error) {
	if history == nil {
		return nil, errors.New("session preferences require session history")
	}
	return &Service{history: history}, nil
}

type Scope struct{ Workspace, Thread string }

// Snapshot owns settings meaning while leaving presentation schema to its
// renderer. The historical flat version-1 record identifies the terminal renderer.
type Snapshot struct {
	Settings       Applied        `json:"settings,omitzero"`
	ResumeObserved time.Time      `json:"resumeObserved,omitzero"`
	ResumeEvidence Evidence       `json:"resumeEvidence,omitzero"`
	Presentation   jsontext.Value `json:",embed"`
}

type record struct {
	Version int `json:"version"`
	Snapshot
}

func recordPath(scope Scope, renderer string) (string, error) {
	if renderer != "terminal" {
		return "", fmt.Errorf("unsupported preference renderer %q", renderer)
	}
	if !filepath.IsAbs(scope.Workspace) || scope.Thread == "" {
		return "", errors.New("session preferences require a host workspace and thread")
	}
	base, err := history.StateDirectory()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(scope.Workspace + "\x00" + scope.Thread))
	return filepath.Join(base, "ui", fmt.Sprintf("panes-%x.json", key)), nil
}

// Read never retargets an attachment or creates files. The renderer must validate
// Presentation before accepting any part of the snapshot.
func (s *Service) Read(scope Scope, renderer string) (Snapshot, error) {
	path, err := recordPath(scope, renderer)
	if err != nil {
		return Snapshot{}, err
	}
	f, err := sourceparse.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, errors.New("pane state must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return Snapshot{}, err
	}
	if len(data) > 4096 {
		return Snapshot{}, errors.New("pane state exceeds size limit")
	}
	var state record
	if err := json.Unmarshal(data, &state); err != nil {
		return Snapshot{}, err
	}
	if state.Version != 1 {
		return Snapshot{}, errors.New("invalid or unsupported pane state")
	}
	if state.Settings != (Applied{}) && (state.Settings.Model == "" || state.Settings.Observed.IsZero()) {
		return Snapshot{}, errors.New("incomplete applied settings snapshot")
	}
	if len(state.Presentation) == 0 {
		state.Presentation = jsontext.Value("{}")
	}
	return state.Snapshot, nil
}

// Attachment has one write target and is owned by the attached session's
// serialized presentation owner. It starts no goroutine and opens no file.
type Attachment struct {
	service     *Service
	scope       Scope
	path        string
	state, last Snapshot
	pending     bool
	due         time.Time
}

func (s *Service) Attach(scope Scope, renderer string, initial jsontext.Value) (*Attachment, error) {
	path, err := recordPath(scope, renderer)
	if err != nil {
		return nil, err
	}
	a := &Attachment{service: s, scope: scope, path: path}
	a.Restore(Snapshot{Presentation: initial})
	return a, nil
}

func (a *Attachment) Scope() Scope { return a.scope }

// Restore accepts a validated snapshot and establishes a no-write baseline.
// The renderer supplies its normalized presentation after applying the record.
func (a *Attachment) Restore(state Snapshot) {
	state.Presentation = bytes.Clone(state.Presentation)
	a.state, a.last, a.pending = state, state, false
}

func (a *Attachment) Apply(settings Applied) {
	a.state.Settings = settings
	a.state.ResumeObserved, a.state.ResumeEvidence = time.Time{}, Evidence{}
}

func (a *Attachment) Recover(evidence Evidence, observed time.Time) {
	a.state.ResumeEvidence, a.state.ResumeObserved = evidence, observed
}

// Save coalesces layout bursts. Applied/recovery changes and exit use flush.
// A failed save is reported once, rather than retried on every animation frame.
func (a *Attachment) Save(presentation jsontext.Value, now time.Time, flush bool) error {
	a.state.Presentation = presentation
	if a.state.Settings != a.last.Settings || a.state.ResumeObserved != a.last.ResumeObserved || a.state.ResumeEvidence != a.last.ResumeEvidence || !bytes.Equal(presentation, a.last.Presentation) {
		a.state.Presentation = bytes.Clone(presentation)
		a.last, a.pending, a.due = a.state, true, now.Add(250*time.Millisecond)
	}
	if !a.pending || !flush && now.Before(a.due) {
		return nil
	}
	a.pending = false
	state := record{Version: 1, Snapshot: a.state}
	data, err := json.Marshal(&state)
	if err != nil {
		return err
	}
	return a.service.history.PublishFile(a.path, data)
}
