package launch

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// threadExecution joins the supplied runtime registry and feature owner. It owns
// no process registry, request ledger, or retained thread state.
type threadExecution struct {
	workers *frontends.WorkerImage
	hub     *sessionapi.Hub
	shared  *control.Service
	driver  *threadbinding.Driver
	tools   []binding.MCPServer
	setup   *threadSetup
	start   *sessioncontrol.PreparedThread
}

// The invoking runtime freezes configuration before child admission. Descendants
// borrow this selection, not subsequently changed user configuration or argv.
type threadSetup struct {
	options     launchoptions.Config
	settings    launchoptions.Settings
	executable  string
	args        []string
	environment []string
	approvals   bool
}

func (e *threadExecution) Runtime(thread string) (control.Runtime, error) {
	return e.hub.Requests(thread)
}

func (e *threadExecution) Confirm(ctx context.Context, input control.NativeInput) error {
	if runtime, err := e.Runtime(input.Thread); err == nil {
		return runtime.Confirm(ctx, input)
	}
	member, ok := e.shared.Member(input.Thread)
	if !ok {
		return control.ErrNotAdmitted
	}
	return sessioncontrol.ConfirmNativeInput(ctx, member.Path, input)
}

// Resume acquires execution only for an explicitly admitted operation. Restoring
// the retained tree and reconciling its history never call this path.
func (e *threadExecution) Resume(ctx context.Context, thread string) (control.Runtime, error) {
	if runtime, err := e.Runtime(thread); err == nil {
		return runtime, nil
	}
	member, ok := e.shared.Member(thread)
	if !ok || member.Parent == "" || e.setup == nil {
		return nil, errors.New("retained child execution is unavailable")
	}
	canonical, err := binding.ReadAdmissionSettings(thread, member.Path)
	if err != nil && member.AudienceAdmitted {
		return nil, err
	}
	settings := member.Settings
	if err == nil {
		settings.Model, settings.Provider, settings.Workspace = canonical.Model, canonical.Provider, canonical.Workspace
		settings.RuntimeRoots, settings.Effort, settings.Summary, settings.Tier = canonical.RuntimeRoots, canonical.Effort, canonical.Summary, canonical.Tier
		settings.PermissionProfile, settings.ApprovalPolicy, settings.ApprovalsReviewer = canonical.PermissionProfile, canonical.ApprovalPolicy, canonical.ApprovalsReviewer
	}
	parent := member.Parent
	for {
		if _, err := e.Runtime(parent); err == nil {
			break
		}
		ancestor, ok := e.shared.Member(parent)
		if !ok || ancestor.Parent == "" {
			return nil, errors.New("retained child has no running ancestor")
		}
		parent = ancestor.Parent
	}
	child := &threadExecution{hub: e.hub, shared: e.shared, workers: e.workers, setup: e.setup, start: &sessioncontrol.PreparedThread{Parent: member.Parent, Settings: settings, Resume: &control.ThreadInfo{Thread: thread, Session: member.Session, Path: member.Path}}}
	id, err := e.hub.StartPrepared(ctx, parent, thread, settings.Workspace, func(runtime context.Context, api *sessionapi.Server) error {
		_, err := runOwnedInvocation(runtime, nil, nil, api, settings.Workspace, child)
		return err
	})
	if err != nil {
		return nil, err
	}
	runtime, err := e.Runtime(id)
	if err == nil {
		var info control.ThreadInfo
		info, err = runtime.Thread(ctx, id)
		if err == nil {
			_, err = child.start.Confirm(info)
		}
	}
	return runtime, err
}

func (e *threadExecution) Create(ctx context.Context, parent, importPath string, settings control.Settings) (control.Receipt, error) {
	if e.setup == nil {
		return control.Receipt{}, &binding.DispatchError{Outcome: binding.NotDispatched, Cause: errors.New("child execution has no captured preparation")}
	}
	child := &threadExecution{hub: e.hub, shared: e.shared, workers: e.workers, setup: e.setup, start: &sessioncontrol.PreparedThread{Parent: parent, Import: importPath, Settings: settings}}
	id, err := e.hub.StartPrepared(ctx, parent, "", settings.Workspace, func(runtime context.Context, api *sessionapi.Server) error {
		_, err := runOwnedInvocation(runtime, nil, nil, api, settings.Workspace, child)
		return err
	})
	receipt := control.Receipt{Thread: id, Uncertain: true}
	if err == nil {
		var runtime control.Runtime
		runtime, err = e.Runtime(id)
		if err == nil {
			var info control.ThreadInfo
			info, err = runtime.Thread(ctx, id)
			if err == nil {
				receipt, err = child.start.Confirm(info)
			}
		}
	}
	if err != nil {
		outcome := binding.Uncertain
		if errors.Is(err, sessionapi.ErrPreparedNotStarted) {
			outcome, receipt.Uncertain = binding.NotDispatched, false
		}
		return receipt, &binding.DispatchError{Outcome: outcome, Cause: err}
	}
	return receipt, nil
}

// AttachChild resumes only an admitted descendant's prepared runtime. Selection
// does not reserve a work generation, dispatch input, or wake a retained wait.
func (e *threadExecution) AttachChild(ctx context.Context, parent, thread string) error {
	admitted := false
	for _, child := range e.shared.Descendants(parent) {
		if child.Thread == thread {
			admitted = true
			break
		}
	}
	member, ok := e.shared.Member(thread)
	if !admitted || !ok || !member.AudienceAdmitted {
		return errors.New("the selected child has no admitted resumable runtime")
	}
	_, err := e.Resume(ctx, thread)
	return err
}
