package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// AdmitRequest binds a root selected by the application to its first actual host
// turn. Children must already have entered the same owner before their inference.
// Acquisition runs on the request worker, never the terminal/host event loop.
type effectiveConfig struct {
	Layers []ConfigLayer `json:"layers"`
	Config struct {
		Base        *string        `json:"base_instructions"`
		Developer   *string        `json:"developer_instructions"`
		Permissions jsontext.Value `json:"permissions"`
		Agents      struct {
			Model  *string `json:"default_subagent_model"`
			Effort *string `json:"default_subagent_reasoning_effort"`
		} `json:"agents"`
		Features struct {
			MultiAgent struct {
				ChildInstructions *string `json:"subagent_developer_instructions"`
				ChildUsage        *string `json:"subagent_usage_hint_text"`
				RootUsage         *string `json:"root_agent_usage_hint_text"`
				Min               *int64  `json:"min_wait_timeout_ms"`
				Max               *int64  `json:"max_wait_timeout_ms"`
			} `json:"multi_agent_v2"`
		} `json:"features"`
	} `json:"config"`
}

func (d *Driver) capture(ctx context.Context, id control.Identity) (settings control.Settings, config effectiveConfig, path string, err error) {
	owner, err := d.runtime(id.Thread)
	if err != nil {
		return
	}
	info, err := owner.Thread(ctx, id.Thread)
	if err != nil {
		return
	}
	if info.Thread != id.Thread || info.Session != id.Session || info.Path == "" {
		err = errors.New("request has no matching retained native thread identity")
		return
	}
	path = info.Path
	admission, err := binding.ReadAdmissionSettings(id.Thread, path)
	if err != nil {
		return
	}
	runtime, err := binding.ReadRuntimeContext(id.Thread, path, id.Turn)
	if err != nil {
		return
	}
	raw, err := owner.Configuration(ctx, id.Thread, runtime.Workspace)
	if err != nil {
		return
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		return
	}
	if config.Layers == nil {
		err = errors.New("host omitted configuration-layer provenance")
		return
	}
	settings = control.Settings{BaseInstructions: config.Config.Base, DeveloperInstructions: config.Config.Developer}
	if member, ok := d.control.Member(id.Thread); ok {
		settings = member.Settings
	}
	settings.Workspace, settings.Model, settings.Provider = runtime.Workspace, runtime.Model, admission.Provider
	settings.RuntimeRoots, settings.Effort, settings.Summary, settings.Tier = admission.RuntimeRoots, runtime.Effort, admission.Summary, admission.Tier
	settings.PermissionProfile, settings.ApprovalPolicy, settings.ApprovalsReviewer = runtime.PermissionProfile, runtime.ApprovalPolicy, runtime.ApprovalsReviewer
	if runtime.ActivePermissionProfile != nil {
		settings.PermissionID = runtime.ActivePermissionProfile.ID
	}
	overrides := make(map[string]jsontext.Value)
	if len(settings.Config) > 0 {
		if err = json.Unmarshal(settings.Config, &overrides); err != nil {
			return
		}
	}
	if config.Config.Permissions.Kind() == '{' {
		overrides["permissions"], err = PermissionConfig(config.Config.Permissions)
		if err != nil {
			return
		}
	}
	if text := config.Config.Features.MultiAgent.RootUsage; text != nil {
		overrides["features.multi_agent_v2.root_agent_usage_hint_text"], _ = json.Marshal(*text)
	}
	settings.Config, err = json.Marshal(overrides)
	return
}

func (d *Driver) AdmitRequest(ctx context.Context, session history.Session, identity control.Identity, root bool) error {
	d.mu.Lock()
	d.thread = identity.Thread
	d.mu.Unlock()
	if member, ok := d.control.Member(identity.Thread); ok {
		root = member.Parent == ""
	}
	if identity.Thread != session.Thread || identity.Session == "" || identity.Turn == "" {
		return errors.New("thread request has no admitted session identity")
	}
	if err := d.control.RequestStarted(ctx, identity, root); !errors.Is(err, control.ErrNotAdmitted) {
		return err
	}
	if !root {
		return errors.New("independent child request preceded logical admission")
	}
	settings, config, path, err := d.capture(ctx, identity)
	if err != nil {
		return err
	}
	// Host execution remains authoritative. Mekugi captures wait bounds but
	// imposes no independent thread-count admission limit.
	limits := control.Limits{MinWaitMS: 10_000, MaxWaitMS: 3_600_000}
	if value := config.Config.Features.MultiAgent.Min; value != nil {
		limits.MinWaitMS = *value
	}
	if value := config.Config.Features.MultiAgent.Max; value != nil {
		limits.MaxWaitMS = *value
	}
	if limits.MinWaitMS < 0 || limits.MaxWaitMS < limits.MinWaitMS {
		return errors.New("invalid effective host thread limits")
	}
	member := control.Member{Thread: identity.Thread, Session: identity.Session, Path: path, Task: "/root", Generation: 1, Status: control.Working, Turn: identity.Turn, HostRunning: true, AudienceAdmitted: true, Settings: settings, Limits: limits}
	if err := d.control.AdmitRoot(ctx, session, &member); err != nil {
		return err
	}
	return d.control.RequestStarted(ctx, identity, true)
}

// Restore queues paused coordination on the owned observation worker. The caller
// gates new input on its result without blocking the host event loop.
func (d *Driver) Restore(session history.Session) chan error {
	done := make(chan error, 1)
	d.mu.Lock()
	d.thread = session.Thread
	d.mu.Unlock()
	d.enqueue(hostObservation{thread: session.Thread, method: "thread/restore", session: session, restored: done})
	return done
}
