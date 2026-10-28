package sessioncontrol

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"

	"github.com/yusing/mekugi/apps/mekugi/internal/recordtransactions"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Startup retains invocation overrides and pre-resume evidence independently of
// the current attachment. Replacement owns an in-session target until acceptance.
type Startup struct {
	History           *history.Store
	Prepared          *PreparedThread
	Thread, Workspace string
	Config            map[string]any
	Evidence          preferences.Evidence
	MissingSettings   bool
	ForkRetention     *ForkRetention
	ForkSettings      *Settings
	ForkSettingsAck   bool
	RewindOriginal    string
}

// PreparedThread is an internal server admission, never human creation input.
type PreparedThread struct {
	Parent, Import string
	Settings       threadcontrol.Settings
	Resume         *threadcontrol.ThreadInfo
}

const ResumePicker = "--pick"

type Opening uint8

const (
	OpeningFresh Opening = iota
	OpeningResume
	OpeningLatest
	OpeningPicker
)

func (s *Startup) Target(control *Live) string { return cmp.Or(control.Replacement.Target, s.Thread) }

// Initialized preserves notification-before-request ordering. Environment
// discovery is observational and only issued when shell capture is present.
func (r *Requests[T]) Initialized(observeShell bool) error {
	if err := r.Notify("initialized", map[string]any{}); err != nil {
		return err
	}
	if observeShell {
		var owner T
		_, err := r.Send("environment/info", map[string]any{"environmentId": "local"}, "main", owner)
		return err
	}
	return nil
}

// OpenSession starts a fresh thread or obtains evidence before resuming. The
// supplied owner routes that evidence response through the same request ledger.
// Source: apps/mekugi/internal/router/app_server_ui.go:653:672@5ce9382b.
func (r *Requests[T]) OpenSession(s *Startup, control *Live, approvals bool, resumeOwner T) (Opening, error) {
	var owner T
	target := s.Target(control)
	switch {
	case s.Prepared != nil && control.Thread == "":
		params, err := s.Prepared.parameters()
		if err != nil {
			return OpeningFresh, err
		}
		r.preparation.Store(s.Prepared)
		method := "thread/start"
		opening := OpeningFresh
		if retained := s.Prepared.Resume; retained != nil {
			if err := confirmNativeSettings(retained.Thread, retained.Path, s.Prepared.Settings); err != nil {
				return OpeningResume, err
			}
			s.Thread = retained.Thread
			opening = OpeningResume
			method, params["threadId"] = "thread/resume", retained.Thread
		} else if s.Prepared.Import != "" {
			method = "thread/fork"
			params["path"], params["threadId"] = s.Prepared.Import, s.Prepared.Parent
		}
		_, err = r.Send(method, params, "main", owner)
		return opening, err
	case control.Replacement.Target == "" && target == ResumePicker:
		return OpeningPicker, nil
	case control.Replacement.Target == "" && target == "--last":
		params := ResumableThreads(25)
		params["cwd"] = s.Workspace
		_, err := r.Send("thread/list", params, "main", owner)
		return OpeningLatest, err
	case target == "":
		_, err := r.Send("thread/start", ThreadPermissions(map[string]any{}, approvals), "main", owner)
		return OpeningFresh, err
	default:
		if found, err := s.History.ContainsSession(target); err != nil {
			return OpeningResume, err
		} else if !found {
			return OpeningResume, fmt.Errorf("session %s is not a retained Mekugi session", target)
		}
		_, err := r.Send("thread/read", map[string]any{"threadId": target, "includeTurns": false}, "main", resumeOwner)
		return OpeningResume, err
	}
}

// ResumableThreads excludes exec and child sessions while retaining all
// interactive providers and source applications.
func ResumableThreads(limit int) map[string]any {
	return map[string]any{
		"limit": limit, "sortKey": "updated_at", "archived": false,
		"modelProviders": []string{}, "sourceKinds": []string{"cli", "vscode", "appServer"},
	}
}

// Latest selects the first Mekugi member or returns the next host page cursor.
func (s *Startup) Latest(result jsontext.Value) (string, error) {
	var response struct {
		Data       []binding.ThreadInfo `json:"data"`
		NextCursor string               `json:"nextCursor"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return "", fmt.Errorf("find latest thread: %w", err)
	}
	for _, thread := range response.Data {
		if thread.ThreadSource == SideThreadSource {
			continue
		}
		found, err := s.History.ContainsSession(thread.ID)
		if err != nil {
			return "", err
		}
		if found {
			s.Thread = thread.ID
			return "", nil
		}
	}
	if response.NextCursor != "" {
		return response.NextCursor, nil
	}
	return "", fmt.Errorf("no resumable Mekugi thread found for %s", s.Workspace)
}

// Resume uses evidence acquired before the host can checkpoint invocation
// defaults. Explicit per-field overrides remain unchanged for later switches.
// Source: apps/mekugi/internal/router/app_server_resume.go:55:94,357:372@5ce9382b.
func (r *Requests[T]) Resume(s *Startup, control *Live, evidence preferences.Evidence, approvals bool) error {
	config := make(map[string]any, len(s.Config)+3)
	maps.Copy(config, s.Config)
	s.Evidence, s.MissingSettings = evidence, evidence.Model == ""
	var saved map[string]any
	if evidence.Model != "" {
		saved = map[string]any{"model": evidence.Model, "model_reasoning_effort": nil}
		if evidence.Effort != "" {
			saved["model_reasoning_effort"] = evidence.Effort
		}
		if evidence.TierKnown {
			saved["service_tier"] = nil
			if evidence.Tier != "" {
				saved["service_tier"] = evidence.Tier
			}
		}
	}
	params := ThreadPermissions(map[string]any{"threadId": s.Target(control), "config": config, "modelProvider": config["model_provider"]}, approvals)
	control.ResumeEffort = false
	for field, value := range saved {
		if _, explicit := config[field]; explicit {
			continue
		}
		if field == "model_reasoning_effort" && value == nil {
			control.ResumeEffort = true // TOML cannot express null; repair after acceptance.
		} else {
			config[field] = value
		}
	}
	if tier, ok := config["service_tier"]; ok {
		params["serviceTier"] = tier
		delete(config, "service_tier")
	}
	var owner T
	_, err := r.Send("thread/resume", params, "main", owner)
	return err
}

// Fork branches the idle conversation with its currently applied settings. The
// replacement remains pending until the host returns a distinct thread identity.
func (r *Requests[T]) Fork(s *Startup, control *Live, applied preferences.Applied, provider string, approvals bool, beforeTurn string) error {
	params := ThreadPermissions(map[string]any{"threadId": control.Thread, "model": applied.Model, "serviceTier": nil}, approvals)
	if provider != "" {
		params["modelProvider"] = provider
	}
	if applied.Effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": applied.Effort}
	}
	if applied.Tier != "" {
		params["serviceTier"] = applied.Tier
	}
	if beforeTurn != "" {
		params["beforeTurnId"] = beforeTurn
	}
	var owner T
	if _, err := r.Send("thread/fork", params, "main", owner); err != nil {
		return err
	}
	s.RewindOriginal = ""
	if beforeTurn != "" {
		s.RewindOriginal = control.Thread
	}
	s.ForkSettings, s.ForkSettingsAck = nil, false
	control.Replacement.Fork()
	control.ResumeEffort = applied.Effort == ""
	s.Evidence = preferences.Evidence{Model: applied.Model, Effort: applied.Effort, Tier: applied.Tier, TierKnown: true}
	s.MissingSettings = false
	return nil
}

// ThreadPermissions preserves --yolo's thread policy. Approval mode leaves both
// settings to Codex configuration and invocation overrides.
func ThreadPermissions(params map[string]any, approvals bool) map[string]any {
	if !approvals {
		params["approvalPolicy"], params["sandbox"] = "never", "danger-full-access"
	}
	return params
}

type AcceptedThread struct {
	PermissionState
	Model             string             `json:"model"`
	ReasoningEffort   string             `json:"reasoningEffort"`
	ServiceTier       string             `json:"serviceTier"`
	CollaborationMode map[string]any     `json:"collaborationMode"`
	Thread            binding.ThreadInfo `json:"thread"`
}

// Accept validates identity before the application retires any old attachment.
func (s *Startup) Accept(control *Live, method string, data jsontext.Value) (AcceptedThread, error) {
	var result AcceptedThread
	if err := json.Unmarshal(data, &result); err != nil {
		return result, err
	}
	if result.Thread.ID == "" {
		return result, fmt.Errorf("%s returned no thread identity", method)
	}
	if method == "thread/resume" && result.Thread.ID != s.Target(control) {
		return result, errors.New("thread/resume returned a different thread identity")
	}
	if method == "thread/fork" && (s.Prepared == nil && !control.Replacement.Forking() || result.Thread.ID == control.Thread) {
		return result, errors.New("thread/fork returned an invalid replacement identity")
	}
	return result, nil
}

// RepairDefaultEffort prepares the host's complete mode snapshot without changing
// its other settings. The caller retains Evidence before dispatching this repair.
// Source: apps/mekugi/internal/router/app_server_ui.go:773:801@5ce9382b.
func (result *AcceptedThread) RepairDefaultEffort(control *Live) (map[string]any, error) {
	if !control.Settings.RestoreEffort {
		return nil, nil
	}
	settings, ok := result.CollaborationMode["settings"].(map[string]any)
	if !ok {
		if result.ReasoningEffort != "" {
			return nil, errors.New("Codex did not return the settings needed to restore default reasoning")
		}
		control.Settings.RestoredEffort()
	} else if settings["reasoning_effort"] != nil {
		settings["reasoning_effort"] = nil
		return map[string]any{"collaborationMode": result.CollaborationMode}, nil
	} else {
		control.Settings.RestoredEffort()
		result.ReasoningEffort = ""
	}
	return nil, nil
}

// ForkRetention holds the unchanged host response until its inherited evidence
// is durable. The event loop remains available while storage is pending.
type ForkRetention struct {
	Message  appserver.Message
	Done     <-chan error
	Err      error
	Complete bool
}

func (s *Startup) RetainFork(ctx context.Context, selected changes.Scope, source changes.Service, original string, message appserver.Message, accepted AcceptedThread) {
	selected.Thread, selected.Namespace = accepted.Thread.ID, accepted.Thread.ID
	rewind := s.RewindOriginal != ""
	done := make(chan error, 1)
	s.ForkRetention = &ForkRetention{Message: message, Done: done}
	go func() {
		if rewind {
			var err error
			selected.Namespace, err = selected.Store.NamespaceForThread(original)
			if err != nil {
				done <- err
				return
			}
		}
		done <- recordtransactions.RetainInput(ctx, selected, source, nil, recordtransactions.Input{
			Workspace:  accepted.Thread.Cwd,
			Initialize: &history.Scope{Thread: accepted.Thread.ID, Namespace: selected.Namespace, Fork: original},
		})
	}()
}

func (s *Startup) ForkRetained() <-chan error {
	if s.ForkRetention == nil || s.ForkRetention.Complete {
		return nil
	}
	return s.ForkRetention.Done
}

// PrepareForkEffort keeps the original attachment admitted until the fork's
// nullable effort is confirmed. Fork responses omit the complete mode; a cheap
// loaded-thread resume supplies it without changing the fork's settings.
func (r *Requests[T]) PrepareForkEffort(s *Startup, control *Live, method string, result *AcceptedThread, settingsOwner T) (bool, error) {
	if !control.Replacement.Forking() || !control.ResumeEffort || result.ReasoningEffort == "" {
		return false, nil
	}
	control.Replacement.Target = result.Thread.ID
	if s.ForkSettings != nil {
		return true, errors.New("Codex did not restore default reasoning")
	}
	if method == "thread/fork" {
		var owner T
		_, err := r.Send("thread/resume", map[string]any{"threadId": result.Thread.ID}, "main", owner)
		return true, err
	}
	target := Live{Settings: Settings{RestoreEffort: true}}
	repair, err := result.RepairDefaultEffort(&target)
	if err != nil {
		return true, err
	}
	if repair == nil {
		return true, errors.New("Codex did not supply the settings needed to restore default reasoning")
	}
	repair["threadId"] = result.Thread.ID
	if _, err := r.Send("thread/settings/update", repair, "fork", settingsOwner); err != nil {
		return true, err
	}
	s.ForkSettings = &Settings{RestoreEffort: true}
	s.ForkSettings.Admit(SettingsPlan{Action: SettingsThread, Change: repair})
	return true, nil
}

// ObserveForkSettings settles only the fork target, independently of Main's
// settings owner. Both acknowledgement and confirmation precede final admission.
func (r *Requests[T]) ObserveForkSettings(s *Startup, control *Live, m appserver.Message, ack bool) (bool, error) {
	if !control.Replacement.Forking() || s.ForkSettings == nil {
		return false, nil
	}
	confirmed := false
	if m.Method == "thread/settings/updated" && s.ForkSettings.BlocksInput() {
		var event struct {
			ThreadID string `json:"threadId"`
			Settings struct {
				Effort string `json:"effort"`
			} `json:"threadSettings"`
		}
		if err := json.Unmarshal(m.Params, &event); err != nil {
			return true, err
		}
		if event.ThreadID == control.Replacement.Target {
			if event.Settings.Effort != "" {
				return true, errors.New("Codex did not restore default reasoning")
			}
			s.ForkSettings.Confirmed("", event.Settings.Effort)
			confirmed = true
		}
	}
	if ack {
		if m.Error != nil {
			return true, errors.New(m.Error.Message)
		}
		s.ForkSettingsAck = true
	}
	if !ack && !confirmed {
		return false, nil
	}
	if s.ForkSettingsAck && !s.ForkSettings.BlocksInput() {
		s.ForkSettingsAck = false
		var owner T
		_, err := r.Send("thread/resume", map[string]any{"threadId": control.Replacement.Target}, "main", owner)
		return true, err
	}
	return true, nil
}
