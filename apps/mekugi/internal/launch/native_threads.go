package launch

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/headless"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// threadExecution joins the supplied runtime registry and feature owner. It owns
// no process registry, request ledger, or retained thread state.
type threadExecution struct {
	headless      *headless.Stream
	workers       *frontends.WorkerImage
	sourceSocket  string
	hub           *sessionapi.Hub
	shared        *control.Service
	features      *sessioncontrol.NativeFeatureUse
	driver        *threadbinding.Driver
	retained      changes.Scope
	recovery      *recovery.Service
	windows       *executionobservation.Windows
	deduplication *transportcapture.OutputDeduplicationTotals
	tools         []binding.MCPServer
	setup         *threadSetup
	start         *sessioncontrol.PreparedThread
	contexts      *sessioncontrol.Observations
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

func (e *threadExecution) Peer(caller control.Identity, target string) (control.Peer, error) {
	return e.hub.Peer(caller, target)
}

func (e *threadExecution) Runtime(thread string) (control.Runtime, error) {
	return e.hub.Requests(thread)
}

func (e *threadExecution) WaitProgress(thread string, seen map[string]int) (control.WaitProgress, map[string]int) {
	wait := e.shared.WaitPublication(thread)
	if wait == nil {
		return control.WaitProgress{Error: "Wait is unavailable"}, nil
	}
	return e.hub.WaitProgress(thread, wait.Targets, seen)
}

func (e *threadExecution) BoundWaitProgress(ctx context.Context, thread string, progress control.WaitProgress) (control.WaitProgress, error) {
	raw, err := json.Marshal(progress)
	if err != nil || len(raw) <= 12<<10 {
		return progress, err
	}
	if e.recovery == nil || e.retained.Store == nil {
		return progress, errors.New("progress recovery storage is unavailable")
	}
	namespace, err := e.retained.Store.NamespaceForThread(thread)
	if err != nil {
		return progress, err
	}
	store, err := e.recovery.AttachStore(e.retained.Store, namespace, thread, recovery.DefaultLimits(), e.retained.Writes)
	if err != nil {
		return progress, err
	}
	ref, err := store.Publish(ctx, command.OmittedOutput{Stdout: string(raw)}, 0)
	if err != nil {
		return progress, err
	}
	return control.WaitProgress{More: true, Recovery: "mread " + ref}, nil
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
	if !ok || member.Parent == "" {
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
	child := &threadExecution{hub: e.hub, shared: e.shared, features: e.features, workers: e.workers, sourceSocket: e.sourceSocket, headless: e.headless, setup: e.setup, windows: e.windows.ForRuntime(), deduplication: e.deduplication, start: &sessioncontrol.PreparedThread{Parent: member.Parent, Settings: settings, Resume: &control.ThreadInfo{Thread: thread, Session: member.Session, Path: member.Path}}}
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
	child := &threadExecution{hub: e.hub, shared: e.shared, features: e.features, workers: e.workers, sourceSocket: e.sourceSocket, headless: e.headless, setup: e.setup, windows: e.windows.ForRuntime(), deduplication: e.deduplication, start: &sessioncontrol.PreparedThread{Parent: parent, Import: importPath, Settings: settings}}
	if importPath != "" {
		child.start.InputPrefix = e.contexts.CaptureInputPrefix(parent, settings.Workspace)
	}
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
	if ok && member.Settings.NonAttachable {
		return errors.New("the selected child does not support attachment")
	}
	if !admitted || !ok || !member.AudienceAdmitted {
		return errors.New("the selected child has no admitted resumable runtime")
	}
	_, err := e.Resume(ctx, thread)
	return err
}

// childOutputs derives independent recorder destinations from the frozen family
// selection. Descendants borrow this same setup, never a derived child's paths.
func (s *threadSetup) childOutputs() (launchoptions.Config, []string, error) {
	options, environment := s.options, slices.Clone(s.environment)
	var reads string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, evidence.AXReadOutputEnvironment+"="); ok {
			reads = value
		}
	}
	root := options.CaptureOutput
	if root == "" {
		root = reads
	}
	if root == "" {
		return options, environment, nil
	}
	directory := root + ".runtimes"
	if err := os.MkdirAll(directory, 0700); err != nil {
		return options, environment, err
	}
	directory, err := os.MkdirTemp(directory, "")
	if err != nil {
		return options, environment, err
	}
	if options.CaptureOutput != "" {
		options.CaptureOutput = filepath.Join(directory, "capture.jsonl")
	}
	if reads != "" {
		for i, entry := range environment {
			if strings.HasPrefix(entry, evidence.AXReadOutputEnvironment+"=") {
				environment[i] = evidence.AXReadOutputEnvironment + "=" + filepath.Join(directory, "reads.jsonl")
			}
		}
	}
	return options, environment, nil
}
