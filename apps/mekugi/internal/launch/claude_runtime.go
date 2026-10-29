package launch

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/notifications/notice"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type claudeFork struct {
	Backend string `json:"backend"`
	Session string `json:"session"`
	Path    string `json:"path"`
}

func (e *threadExecution) createClaude(ctx context.Context, parent, importPath string, settings control.Settings) (control.Receipt, error) {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6], uuid[8] = uuid[6]&0x0f|0x40, uuid[8]&0x3f|0x80
	session := fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
	info := control.ThreadInfo{Thread: "claude:" + session, Session: session, Provider: "claude"}
	var fork *claudeFork
	if importPath != "" {
		data, err := history.ReadRecord(importPath)
		if err != nil {
			return control.Receipt{}, &binding.DispatchError{Outcome: binding.NotDispatched, Cause: err}
		}
		fork = new(claudeFork)
		member, admitted := e.shared.Member(parent)
		if err := json.Unmarshal(data, fork, json.RejectUnknownMembers(true)); err != nil || !admitted || fork.Backend != "claude" || fork.Session != member.Session || fork.Path != member.Path {
			return control.Receipt{}, &binding.DispatchError{Outcome: binding.NotDispatched, Cause: errors.New("Claude fork has no matching retained source")}
		}
		info.Fork = new(parent)
	}
	return e.startClaude(ctx, parent, "", settings, info, fork)
}

func (e *threadExecution) resumeClaude(ctx context.Context, member *control.Member) (control.Runtime, error) {
	info, err := sessioncontrol.ReadClaudeThread(ctx, member.Thread, member.Path)
	if err != nil || info.Session != member.Session {
		return nil, errors.Join(err, errors.New("retained Claude session identity is unavailable"))
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
	_, err = e.startClaude(ctx, parent, member.Thread, member.Settings, info, nil)
	if err != nil {
		return nil, err
	}
	return e.Runtime(member.Thread)
}

func (e *threadExecution) startClaude(ctx context.Context, parent, resume string, settings control.Settings, info control.ThreadInfo, fork *claudeFork) (control.Receipt, error) {
	child := &threadExecution{hub: e.hub, shared: e.shared, workers: e.workers, setup: e.setup, driver: e.driver, approvals: e.approvals, retained: e.retained, windows: e.windows.ForRuntime()}
	id, err := e.hub.StartPrepared(ctx, parent, resume, settings.Workspace, func(runtime context.Context, api *sessionapi.Server) error {
		return child.runClaude(runtime, api, parent, resume != "", settings, info, fork)
	})
	receipt := control.Receipt{Thread: id, Session: info.Session, Uncertain: true}
	if err == nil {
		var runtime control.Runtime
		runtime, err = e.Runtime(id)
		if err == nil {
			var confirmed control.ThreadInfo
			confirmed, err = runtime.Thread(ctx, id)
			if err == nil && (confirmed.Thread != info.Thread || confirmed.Session != info.Session || confirmed.Path == "") {
				err = errors.New("Claude prepared runtime changed its admitted identity")
			}
			if err == nil {
				receipt.Path, receipt.Uncertain = confirmed.Path, false
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

func (e *threadExecution) runClaude(ctx context.Context, api *sessionapi.Server, parent string, resume bool, settings control.Settings, info control.ThreadInfo, fork *claudeFork) (runErr error) {
	if e.retained.Store == nil || e.setup == nil || e.driver == nil {
		return errors.New("Claude runtime has no admitted retained execution owners")
	}
	defer e.windows.Retire()
	// skills-mgr on the delegate PATH owns skill acquisition, as it does for Codex.
	_, noSkillsManager := managedSkillsExecutable(settings.Workspace, e.setup.environment)
	policy, err := claude.CompilePolicy(settings.ApprovalPolicy, settings.ApprovalsReviewer, noSkillsManager != nil)
	if err != nil {
		return err
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		return fmt.Errorf("locate Claude SDK Bun runtime: %w", err)
	}
	nativeExecutable, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("locate Claude executable: %w", err)
	}
	if err := api.ReserveThread(info.Thread); err != nil {
		return err
	}
	owners, err := composition.Frontends(ctx, false, e.setup.options.ChildReplySummary, e.retained.Store.Directory(), e.shared)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, owners.Close(context.WithoutCancel(ctx))) }()
	owners.Threads.InheritCatalog(e.driver)
	e.driver = owners.Threads
	registry, err := frontends.New(ctx, frontends.Config{Commands: owners.Commands, Guidance: owners.Guidance, ReplayDirectory: e.retained.Store.Directory(), WorkerImage: e.workers})
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, registry.Close()) }()
	logicalParent := parent
	if member, ok := e.shared.Member(info.Thread); ok {
		logicalParent = member.Parent
	}
	if err := e.retained.Store.Transaction(ctx, e.retained.Writes, func(tx *history.Transaction) error {
		namespace, err := e.retained.Store.NamespaceForThread(logicalParent)
		if err != nil {
			return err
		}
		return tx.InitializeScope(history.Scope{Thread: info.Thread, Namespace: namespace, Parent: logicalParent}, nil)
	}); err != nil {
		return err
	}
	selected, err := e.retained.Store.SelectSession(ctx, e.retained.Writes, e.retained.Limits, info.Thread)
	if err != nil {
		return err
	}
	defer selected.Close()
	e.retained = changes.Scope{Store: selected.Store, Thread: selected.Thread, Namespace: selected.Namespace, Limits: selected.Limits, Writes: selected.Writes}
	checkpoints, err := owners.Changes.Checkpoints(selected.Store.Directory())
	if err != nil {
		return err
	}
	environment, err := frontendShellEnvironment(slices.Clone(e.setup.environment), registry.FrontendDirectory, "", nil, segmenttransport.Endpoints{})
	if err != nil {
		return err
	}
	environment = append(environment, "CODEX_THREAD_ID="+info.Thread)
	environment = codexEnvironmentCapability(environment, false)
	options := make(map[string]jsontext.Value)
	if err := json.Unmarshal(policy.Options(), &options); err != nil {
		return err
	}
	options["sessionId"], _ = json.Marshal(info.Session)
	options["model"], _ = json.Marshal(strings.TrimPrefix(settings.Model, "claude:"))
	if settings.Effort != nil {
		options["effort"], _ = json.Marshal(*settings.Effort)
	}
	options["systemPrompt"], err = claudeSystemPrompt(settings, control.Identity{Thread: info.Thread, Session: info.Session})
	if err != nil {
		return err
	}
	if options["settings"], err = claudeGuidanceSettings(registry.FrontendGuidance.Shell); err != nil {
		return err
	}
	if resume {
		options["resume"], _ = json.Marshal(info.Session)
		delete(options, "sessionId")
	} else if fork != nil {
		options["resume"], _ = json.Marshal(fork.Session)
		options["forkSession"] = jsontext.Value("true")
	}
	encodedOptions, err := json.Marshal(options)
	if err != nil {
		return err
	}
	var tools []claude.Tool
	for _, tool := range threadbinding.ToolCatalog(true) {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return err
		}
		tools = append(tools, claude.Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
	}
	updatesCtx, stopUpdates := context.WithCancel(ctx)
	defer stopUpdates()
	bridge := &claudeBridge{ctx: updatesCtx, stopUpdates: stopUpdates, execution: e, info: info, settings: settings, selected: e.retained, session: selected, owners: owners, checkpoints: checkpoints, windows: e.windows, policy: policy,
		captures: make(map[string]*claudeToolCapture), changes: make(chan []changes.Update, 64)}
	defer bridge.discard()
	client, err := claude.Start(ctx, claude.Config{BunPath: bun, ClaudePath: nativeExecutable, Workspace: settings.Workspace, Env: environment, Options: encodedOptions, Tools: tools, OnRequest: bridge.request})
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, bridge.close(client)) }()
	runtime, err := sessioncontrol.NewClaudeRuntime(ctx, selected, info, settings, client)
	if err != nil {
		return err
	}
	bridge.runtime = runtime
	owners.Usage.Configure(selected.Store, selected.Writes, selected.Limits, func(_ string, err error) {
		owners.Notifications.Submit(notice.Input{Session: info.Session, Thread: info.Thread, Category: "claude-metrics", Text: "Claude usage could not be retained: " + err.Error()})
	})
	if resume {
		owners.Usage.Restore(info.Thread, true)
	} else {
		owners.Usage.MarkNew(info.Thread)
	}
	if _, report := runtime.Metrics(); report.TotalsKnown {
		// A retained native snapshot may outlive an interrupted accounting write.
		owners.Usage.ObserveCumulative(info.Thread, report)
	}
	runtime.SetEventSink(bridge.reduceEvent)
	runtime.SetInputContext(func(ctx context.Context, input control.NativeInput) (string, error) {
		member, ok := e.shared.Member(input.Thread)
		if !ok || member.Session != info.Session || !member.AudienceAdmitted {
			return "", control.ErrNotAdmitted
		}
		// Inherited full-session context already belongs to the SDK transcript.
		// Acquire only this child's selected initial sources, once in its ledger.
		return claudePreload(ctx, member.Preload.Selection, settings.Workspace, environment, compact.ChangeReader{Service: owners.Changes.Service, Scope: e.retained})
	})
	defer func() { runtime.TransportClosed(runErr) }()
	audience := sessioncontrol.NewAudience(nil)
	defer audience.Close()
	root, family := e.shared.Family(parent)
	for _, member := range family {
		audience.Observe(member.Thread, member.Parent, member.Task, member.Parent != "")
	}
	if err := e.driver.Connect(e, audience); err != nil {
		return err
	}
	api.BindRequests(runtime)
	api.BindChildAttachment(e.AttachChild)
	projection := sessionreview.Projection{Changes: owners.Changes.Service, Retained: e.retained}
	scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{settings.Workspace: {info.Thread: true}}}
	if _, err := projection.Apply(ctx, sessionreview.Event{Kind: "scope", Scope: &scope}); err != nil {
		return err
	}
	started := time.Now()
	publication := claudePublication{frame: sessionapi.Frame{Session: sessionapi.Session{Thread: info.Thread, Role: settings.Role, Workspace: settings.Workspace, Started: started.UnixMilli()}, FamilyRoot: root}}
	publish := func() error {
		currentRoot, members := e.shared.Family(parent)
		member, _ := e.shared.Member(info.Thread)
		if err := runtime.ObserveLogicalWork(ctx, member, time.Now()); err != nil {
			owners.Notifications.Submit(notice.Input{Session: info.Session, Thread: info.Thread, Category: "claude-metrics", Text: "Claude work timing could not be retained: " + err.Error()})
		}
		facts, _ := runtime.Metrics()
		agent := sessionpresentation.ActivityAgent{}
		observeCost(owners.Usage, info.Thread, &agent)
		usage := agent.ActivityAgent()
		facts.InputTokens, facts.OutputTokens, facts.TokensKnown = usage.InputTokens, usage.OutputTokens, usage.TokensKnown
		facts.Cost, facts.CostKnown, facts.CostPartial = usage.Cost, usage.CostKnown, usage.CostPartial
		facts.Roundtrips, facts.RoundtripsPartial, facts.UsagePartial = usage.Roundtrips, usage.RoundtripsPartial, usage.UsagePartial
		if !publication.update(currentRoot, members, member, projection.Data, facts) {
			return nil
		}
		if err := api.Publish(publication.frame); err != nil {
			return err
		}
		return api.SetSummary(sessionapi.Summary{Thread: info.Thread, Workspace: settings.Workspace, Started: started, Status: publication.frame.Status, Ready: true})
	}
	if err := publish(); err != nil {
		return err
	}
	var waitTimer *time.Timer
	defer func() {
		if waitTimer != nil {
			waitTimer.Stop()
		}
	}()
	for {
		var deadline <-chan time.Time
		if member, ok := e.shared.Member(info.Thread); ok && member.Wait != nil && member.Wait.Deadline != nil && member.Wait.State == "registered" && !member.HostRunning && !member.Wait.CompactionPending() {
			if waitTimer == nil {
				waitTimer = time.NewTimer(time.Until(*member.Wait.Deadline))
			} else {
				waitTimer.Reset(time.Until(*member.Wait.Deadline))
			}
			deadline = waitTimer.C
		} else if waitTimer != nil {
			waitTimer.Stop()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			e.driver.Tick(time.Now())
		case <-e.driver.Changed():
			for _, failure := range e.driver.Failures() {
				if _, auxiliary := errors.AsType[*threadbinding.CompactionFailure](failure); !auxiliary {
					return failure
				}
				owners.Notifications.Submit(notice.Input{Session: info.Session, Thread: info.Thread, Category: "threadcontrol", Text: failure.Error()})
			}
		case updates := <-bridge.changes:
			event := sessionreview.Event{Kind: "change"}
			for _, update := range updates {
				event.Changes = append(event.Changes, sessionreview.FromUpdate(update))
			}
			if _, err := projection.Apply(ctx, event); err != nil {
				return err
			}
		case raw, ok := <-client.Events():
			if !ok {
				return client.Err()
			}
			if err := bridge.event(ctx, raw); err != nil {
				return err
			}
		}
		if err := publish(); err != nil {
			return err
		}
	}
}

// Preserve source identities until their admitted facts change. The family
// consumer uses these identities to keep saved-review pages and composition.
type claudePublication struct {
	frame  sessionapi.Frame
	review *changepresentation.Snapshot
}

func (p *claudePublication) update(root string, members []control.Participant, member *control.Member, review *changepresentation.Snapshot, metrics activitypresentation.AgentFacts) bool {
	label, caller := sessionpresentation.Label{Text: "Ready"}, ""
	if member != nil {
		caller = member.Task
		label.Text, label.Running = string(member.Status), member.HostRunning
	}
	if root == "" {
		root = p.frame.FamilyRoot
	}
	metrics.Thread, metrics.Name, metrics.Role, metrics.NonAttachable = p.frame.Session.Thread, caller, p.frame.Session.Role, true
	metricsChanged := p.frame.Agents == nil || p.frame.Agents.Roster[0].Agent.AgentFacts != metrics
	changed := metricsChanged || p.frame.Session.Caller != caller || p.frame.Status != label || p.frame.FamilyRoot != root || !slices.Equal(p.frame.Members, members) || p.review != review
	if !changed {
		return false
	}
	if metricsChanged {
		p.frame.Agents = &activitypresentation.Publication{Roster: []activitypresentation.AgentRow{{Agent: activitypresentation.Agent{AgentFacts: metrics}}}}
	}
	if p.review != review {
		revision := uint64(1)
		if p.frame.Review != nil {
			revision += p.frame.Review.Revision
		}
		p.frame.Review = nil
		if review != nil {
			p.frame.Review = &changepresentation.ReviewView{Publication: review.Publication, Workspace: p.frame.Session.Workspace, Revision: revision}
		}
		p.review = review
	}
	p.frame.Session.Caller, p.frame.Status, p.frame.FamilyRoot, p.frame.Members = caller, label, root, members
	return true
}
