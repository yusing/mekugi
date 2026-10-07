package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/vcsguard"
)

type nativeVCSGuard struct {
	helper, directory string
}

type nativeGuardItem struct {
	Binding ObservationBinding `json:"binding"`
	ID      string             `json:"id"`
}

// PrepareVCSGuard uses the command tracker and shared reached-command helper.
// It changes neither native permissions nor persistent Claude configuration.
func (s *ObservationService) PrepareVCSGuard(ctx context.Context, helper string) error {
	if s.owner.execTrack == nil || helper == "" {
		return errors.New("VCS guard requires command tracking and mekugi-exec beside mekugi; use --vcs-guard=false to disable it")
	}
	directory, channel := vcsguard.Paths(filepath.Join(s.directory, "bin"))
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	for _, name := range slices.Concat(vcsguard.Tools, vcsguard.Shells) {
		if err := os.Symlink(helper, filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	if err := s.owner.execTrack.listenVCSGuard(ctx, channel); err != nil {
		return err
	}
	s.guard = &nativeVCSGuard{helper: helper, directory: directory}
	socket, reports := ExecTrackPaths(filepath.Join(s.directory, "bin"))
	tracker := filepath.Join(s.directory, "exec-track.bash")
	if err := os.WriteFile(tracker, []byte(execsegment.ClaudeGuardTracker(helper, socket, reports, directory)), 0600); err != nil {
		return err
	}
	startup := filepath.Join(s.directory, "bash-env")
	source, err := os.ReadFile(startup)
	if err != nil {
		return err
	}
	hook := execsegment.ClaudeHook(tracker)
	if !strings.HasSuffix(string(source), hook) {
		return errors.New("native startup observer unavailable")
	}
	return os.WriteFile(startup, []byte(strings.TrimSuffix(string(source), hook)+execsegment.ClaudeGuardHook(tracker)), 0600)
}

func (s *ObservationService) guardScript(call ObservationCall) (string, error) {
	if s.guard == nil || call.Tool != "Bash" {
		return call.Command, nil
	}
	s.owner.mu.Lock()
	_, bound := s.owner.bindings[call.Binding]
	s.owner.mu.Unlock()
	if !bound || call.ID == "" {
		return "", errors.New("VCS guard requires a bound native command")
	}
	var input map[string]jsontext.Value
	var original string
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil || json.Unmarshal(input["command"], &original) != nil || call.Command == "" || original != call.Command {
		return "", errors.New("VCS guard requires exact native Bash input")
	}
	item, err := json.Marshal(nativeGuardItem{Binding: call.Binding, ID: call.ID})
	if err != nil {
		return "", err
	}
	return vcsguard.RewriteForItem(call.Command, s.guard.helper, s.guard.directory, string(item))
}

func (u *appServerUI) runtimeGuardApproval(request *vcsApproval) {
	s := u.runtime.observations
	var item nativeGuardItem
	if s != nil && s.guard != nil && request.item == "" {
		// Unsupported or ambiguous observer claims have no native item owner.
		// The private channel still asks independently, without a guessed row.
		s.owner.mu.Lock()
		request.thread = s.owner.session
		s.owner.mu.Unlock()
		u.addGuardApproval(request)
		return
	}
	if s == nil || s.guard == nil || json.Unmarshal([]byte(request.item), &item) != nil || item.ID == "" {
		request.reply <- vcsguard.Reply{Reason: "native command identity unavailable"}
		return
	}
	s.owner.mu.Lock()
	_, bound := s.owner.bindings[item.Binding]
	current := item.Binding.Session == s.owner.session
	s.owner.mu.Unlock()
	if !bound || !current {
		request.reply <- vcsguard.Reply{Reason: "native command binding retired"}
		return
	}
	// CODEX_THREAD_ID belongs to another host. Only the official hook tuple
	// carried by this reached command establishes native session identity.
	request.thread, request.item = item.Binding.Session, item.ID
	// Reaching this native command proves execution consumed its permission.
	// Confirm it before the independent guard state updates the same row.
	u.confirmRuntimePermission(item.ID)
	u.addGuardApproval(request)
}
