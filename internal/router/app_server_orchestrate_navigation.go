package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/ui/diffview"
)

// Execution stays with the coordinator. Each subscribed root keeps the ordinary
// session controller, including its composer, request correlation and shell.
type orchestrateNavigation struct {
	closed        bool
	owner, viewed *appServerUI
	views         map[string]*appServerUI
	order         []string
	reads         map[string]string
	unavailable   map[string]bool
	pending       []appserver.Message
	promptOrder   uint64
}

func (u *appServerUI) viewedUI() *appServerUI {
	if u.navigation != nil {
		return u.navigation.viewed
	}
	return u
}

func (u *appServerUI) addOrchestratedView(child *orchestrateChild, raw []byte, ready func()) error {
	if u.navigation == nil {
		u.navigation = &orchestrateNavigation{owner: u, viewed: u, views: map[string]*appServerUI{u.thread: u}, order: []string{u.thread}, reads: make(map[string]string), unavailable: make(map[string]bool)}
	}
	n := u.navigation
	thread := child.batch.Launch.ThreadID
	if n.views[thread] != nil {
		ready()
		return nil
	}
	v := &appServerUI{ctx: u.ctx, client: u.client, proxy: u.proxy, execTrack: u.execTrack,
		issues: u.issues, view: newLiveActivityView(), agents: newLiveActivityView(),
		requests: make(map[string]string), status: "Ready", navigation: n,
		serviceTiers: u.serviceTiers, approvalMode: u.approvalMode, backendVersion: u.backendVersion,
		skillEnvironment: u.skillEnvironment,
		resumeConfig:     u.resumeConfig, notifications: u.notifications}
	if err := json.Unmarshal(raw, &v.statusConfig); err != nil {
		return err
	}
	var result struct {
		Model           string              `json:"model"`
		ReasoningEffort string              `json:"reasoningEffort"`
		ServiceTier     string              `json:"serviceTier"`
		Thread          appServerThreadInfo `json:"thread"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	v.thread, v.title = thread, result.Thread.Name
	v.awaitingTurn, v.status = true, "Starting"
	v.resumeArgv, v.replayDebugDirectory = u.resumeArgv, u.replayDebugDirectory
	v.shellEdits.shell = u.shellEdits.shell
	v.model, v.reasoningEffort, v.serviceTier = result.Model, result.ReasoningEffort, result.ServiceTier
	v.session.start(thread, child.batch.Cwd)
	v.session.registerThread(result.Thread)
	v.agents.apply(activityPaneEvent{Kind: "agents", Agents: slices.Clone(v.session.agents)})
	v.ensureShell()
	v.shell.faint = u.shell != nil && u.shell.faint
	v.shell.diff.workspace = child.batch.Cwd
	v.restoreDiffThread(result.Thread, true)
	if u.proxy.activity != nil {
		u.proxy.activity.attachNativePane(thread)
	}
	if u.panes != nil {
		v.panes = new(nativePanePersistence)
		v.paneError(v.panes.open(v.shell, child.batch.Cwd, thread, false))
		v.retainAppliedSettings()
	}
	v.guardHookCheck.hash = u.guardHookCheck.hash
	n.views[thread] = v
	n.order = append(n.order, thread)
	v.journal = u.proxy.journals.attachNative(child.batch.Cwd, thread)
	v.unscopedJournal = u.proxy.journals.attachNative("", thread)
	u.orchestrateWork(func() func() {
		ctx := u.orchestrateStorageContext
		var release func()
		var waitErr error
		if u.proxy.replayStore != nil {
			ctx, release, waitErr = u.proxy.replayStore.beginSession(ctx, thread, "")
		}
		if waitErr != nil {
			ctx = u.ctx
		}
		if release != nil {
			once := sync.OnceFunc(release)
			stop := context.AfterFunc(u.orchestrateStorageContext, once)
			release = func() { stop(); once() }
		}
		return func() {
			if n.closed {
				if release != nil {
					release()
				}
				u.finishOrchestrate(child, context.Canceled)
				return
			}
			if release != nil {
				v.ctx = ctx
				v.waitRelease = release
				v.session.waitStore, v.session.waitContext = u.proxy.replayStore.scoped(ctx), ctx
			}
			if waitErr != nil {
				v.setNotice("Wait targets: "+waitErr.Error(), true)
			}
			ready()
		}
	})
	return n.drain()
}

func (n *orchestrateNavigation) target(thread string) *appServerUI {
	return n.views[n.owner.session.threadRoot(thread)]
}

// Unknown descendants wait for metadata rather than borrowing the displayed
// root. This also covers notifications arriving before thread/started.
func (n *orchestrateNavigation) route(m appserver.Message) (bool, error) {
	if m.Method == "" {
		if thread := n.reads[string(m.ID)]; thread != "" {
			delete(n.reads, string(m.ID))
			var r struct {
				Thread appServerThreadInfo `json:"thread"`
			}
			if m.Error != nil || json.Unmarshal(m.Result, &r) != nil || r.Thread.ID != thread {
				n.unavailable[thread] = true
				n.owner.setNotice("Orchestration ancestry unavailable for "+thread+" · awaiting host metadata", true)
				return true, nil
			}
			n.owner.session.registerThread(r.Thread)
			if v := n.target(thread); v != nil {
				v.registerSessionThread(r.Thread)
			}
			return true, n.drain()
		}
		for _, v := range n.views {
			if v != n.owner && (v.requests[string(m.ID)] != "" || v.btwRequests[string(m.ID)].panel != nil || v.reset != nil && v.reset.requestID == string(m.ID)) {
				if v.requests[string(m.ID)] == "thread/read" && m.Error == nil {
					var r struct {
						Thread appServerThreadInfo `json:"thread"`
					}
					if json.Unmarshal(m.Result, &r) == nil && r.Thread.ID != "" {
						n.owner.session.registerThread(r.Thread)
					}
				}
				if err := v.message(m); err != nil {
					return true, err
				}
				return true, n.drain()
			}
		}
		return false, nil
	}
	var p appServerEvent
	if json.Unmarshal(m.Params, &p) != nil {
		return false, nil
	}
	thread := p.ThreadID
	if p.Thread.ID != "" {
		thread = p.Thread.ID
		n.owner.session.registerThread(p.Thread)
		delete(n.unavailable, thread)
		if err := n.drain(); err != nil {
			return true, err
		}
	}
	if thread == "" || n.owner.btwThreads[thread] != nil {
		return false, nil
	}
	if v := n.sideOwner(thread); v != nil {
		return true, v.message(m)
	}
	if m.Method == "turn/completed" {
		delete(n.unavailable, thread)
	}
	if n.target(thread) == nil && n.admitAuthenticated(thread) {
		if err := n.drain(); err != nil {
			return true, err
		}
	}
	if v := n.target(thread); v != nil {
		if v == n.owner {
			return false, nil
		}
		n.register(v, thread)
		return true, v.message(m)
	}
	if len(n.pending) == 256 {
		return true, errors.New("orchestration descendant event capacity exceeded")
	}
	n.pending = append(n.pending, m)
	return true, n.read(thread)
}

func (n *orchestrateNavigation) admitAuthenticated(thread string) bool {
	lineage := n.owner.proxy.activity.nativeLineage(thread)
	if len(lineage) == 0 || n.views[lineage[len(lineage)-1].ID] == nil {
		return false
	}
	for _, info := range lineage {
		if old := n.owner.session.threads[info.ID]; old.ParentThreadID != "" && old.ParentThreadID != info.ParentThreadID {
			return false
		}
	}
	for _, info := range slices.Backward(lineage) {
		if n.views[info.ID] != nil {
			continue
		}
		old := n.owner.session.threads[info.ID]
		old.ID, old.ParentThreadID, old.agentPath = info.ID, info.ParentThreadID, info.agentPath
		n.owner.session.registerThread(old)
	}
	return true
}

func (n *orchestrateNavigation) register(v *appServerUI, thread string) {
	var chain []appServerThreadInfo
	for remaining := len(n.owner.session.threads); thread != "" && thread != v.thread && remaining > 0; remaining-- {
		if v.session.threads[thread].ID != "" {
			break
		}
		info := n.owner.session.threads[thread]
		if info.ID == "" {
			break
		}
		chain = append(chain, info)
		thread = info.ParentThreadID
	}
	for _, info := range slices.Backward(chain) {
		v.registerSessionThread(info)
	}
}

func (n *orchestrateNavigation) sideOwner(thread string) *appServerUI {
	for _, v := range n.views {
		if v.btwThreads[thread] != nil {
			return v
		}
	}
	return nil
}

func (n *orchestrateNavigation) read(thread string) error {
	for range len(n.owner.session.threads) + 1 {
		info := n.owner.session.threads[thread]
		if info.ID == "" {
			break
		}
		if n.views[thread] != nil {
			return nil
		}
		if info.ParentThreadID == "" {
			return nil
		}
		thread = info.ParentThreadID
	}
	for _, pending := range n.reads {
		if pending == thread {
			return nil
		}
	}
	if n.unavailable[thread] {
		return nil
	}
	id, err := n.owner.client.Send("thread/read", map[string]any{"threadId": thread, "includeTurns": false}, true)
	if err == nil {
		n.reads[id] = thread
	}
	return err
}

func (n *orchestrateNavigation) drain() error {
	pending := n.pending
	n.pending = nil
	for _, m := range pending {
		var p appServerEvent
		_ = json.Unmarshal(m.Params, &p)
		thread := p.ThreadID
		if p.Thread.ID != "" {
			thread = p.Thread.ID
		}
		if v := n.sideOwner(thread); v != nil {
			if err := v.message(m); err != nil {
				return err
			}
			continue
		}
		if v := n.target(thread); v != nil {
			n.register(v, thread)
			if err := v.message(m); err != nil {
				return err
			}
		} else {
			n.pending = append(n.pending, m)
			if err := n.read(thread); err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *appServerUI) switchOrchestratedThread(thread string) bool {
	n := u.navigation
	if n == nil || n.views[thread] == nil {
		return false
	}
	n.viewed = n.views[thread]
	n.viewed.shell.paintedRows = nil
	n.viewed.dirty = true
	if len(n.viewed.models) == 0 && !n.viewed.modelsLoading {
		n.viewed.modelsLoading = true
		if err := n.viewed.request("model/list", map[string]any{"includeHidden": true}); err != nil {
			n.viewed.setNotice(err.Error(), true)
		}
	}
	return true
}

func (u *appServerUI) cycleOrchestratedThread(delta int) {
	if n := u.navigation; n != nil {
		index := slices.Index(n.order, n.viewed.thread)
		u.switchOrchestratedThread(n.order[(index+delta+len(n.order))%len(n.order)])
	}
}

func (u *appServerUI) orchestrationTitle() string {
	if n := u.navigation; n != nil {
		if u == n.owner {
			return "main"
		}
		return "main › " + n.owner.orchestrateThreads[u.thread].batch.TaskName
	}
	return "Main"
}

func (u *appServerUI) orchestrationRoster() {
	if n := u.navigation; n != nil {
		if u == n.owner {
			u.agents.agents = slices.DeleteFunc(u.agents.agents, func(agent activityPaneAgent) bool {
				return strings.HasPrefix(agent.Name, "/orchestrate/")
			})
		}
		u.agents.orchestration = nil
		u.agents.orchestrationLabels = map[string]string{}
		for _, thread := range n.order {
			v := n.views[thread]
			agent := *v.session.agent("/root")
			agent.Role = ""
			name, label := "main", v.status
			if child := n.owner.orchestrateThreads[thread]; child != nil {
				name, label = child.batch.TaskName, child.batch.Branch+" · "+v.status
				if name == "main" {
					name = "main batch"
				}
			}
			agent.Name = "/Orchestration/" + name
			u.agents.orchestration = append(u.agents.orchestration, agent)
			u.agents.orchestrationLabels[agent.Name] = label
		}
	}
}

// The broker observes all subscribed roots. Saved and provisional changes use
// the same per-controller lineage filter before reaching their existing owner.
func (u *appServerUI) applyOrchestrationDiff(ctx context.Context, event liveDiffEvent) {
	n := u.navigation
	if n == nil {
		u.shell.applyDiff(ctx, event)
		return
	}
	for _, v := range n.views {
		local := event
		if event.Kind == "preview" && event.Preview != nil && n.target(event.Preview.Thread) != v {
			continue
		}
		if event.Kind == "scope" && event.Scope != nil {
			scope := liveDiffScope{Workspaces: make(map[string]map[string]bool)}
			for workspace, threads := range event.Scope.Workspaces {
				for thread := range threads {
					if n.target(thread) != v {
						continue
					}
					if scope.Workspaces[workspace] == nil {
						scope.Workspaces[workspace] = make(map[string]bool)
					}
					scope.Workspaces[workspace][thread] = true
				}
			}
			local.Scope = &scope
		}
		v.shell.applyDiff(ctx, local)
		v.dirty = true
	}
}

func (u *appServerUI) tickOrchestratedViews(viewed *appServerUI) error {
	if n := u.navigation; n != nil {
		for _, v := range n.views {
			if v == viewed {
				continue
			}
			for draining := true; draining; {
				select {
				case key := <-v.commitReads:
					v.commitRead(key)
				case result := <-v.commandSegmentWrites:
					v.commandSegmentRetained(result)
				case result := <-v.picker.scanResults:
					v.applyPickerScan(result)
				case update := <-v.titleUpdates:
					v.persistSessionTitle(update)
				default:
					draining = false
				}
			}
			v.startSkillHistory()
			v.applyObservedActivity()
			v.applyPendingJournal()
			v.expireApprovals()
			v.flushStreamOutput()
			v.startCommitReads()
			if err := v.tickJournalReset(v.now()); err != nil {
				v.setNotice(err.Error(), true)
			}
			if err := v.flushInput(); err != nil {
				return err
			}
			if err := v.flushBTW(); err != nil {
				return err
			}
			if err := v.finishJournalAcknowledgements(false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *appServerUI) resetOrchestrationPreviews() {
	if n := u.navigation; n != nil {
		for _, v := range n.views {
			v.shell.liveDock = diffview.PreviewPane{}
			clear(v.shell.livePending)
			v.dirty = true
		}
	} else {
		u.shell.liveDock = diffview.PreviewPane{}
		clear(u.shell.livePending)
	}
}

func (u *appServerUI) orchestrationGuardApproval(request *vcsApproval) {
	if n := u.navigation; n != nil {
		if v := n.target(request.thread); v != nil {
			v.addGuardApproval(request)
			return
		}
	}
	u.addGuardApproval(request)
}

func (u *appServerUI) pickOrchestratedThread() bool {
	n := u.navigation
	if n == nil {
		return false
	}
	name := strings.TrimPrefix(u.agents.selected, "/Orchestration/")
	if name == u.agents.selected {
		return false
	}
	for _, thread := range n.order {
		batch := n.owner.orchestrateThreads[thread]
		if name == "main" && thread == n.owner.thread || batch != nil && (name == batch.batch.TaskName && name != "main" || name == "main batch" && batch.batch.TaskName == "main") {
			return u.switchOrchestratedThread(thread)
		}
	}
	return false
}

func (u *appServerUI) closeOrchestratedViews() {
	if n := u.navigation; n != nil {
		if n.closed {
			return
		}
		n.closed = true
		for _, v := range n.views {
			if v == n.owner {
				continue
			}
			v.cancelPickerScan()
			v.discardDraftImages()
			v.shell.diff.close()
			v.shell.diffScreen.Close()
			v.proxy.journals.detachNative(v.journal)
			v.proxy.journals.detachNative(v.unscopedJournal)
			v.proxy.activity.detachNativePane(v.thread)
			if v.waitRelease != nil {
				v.waitRelease()
			}
		}
	}
}

func (u *appServerUI) finishOrchestratedViews(out io.Writer) error {
	var result error
	if n := u.navigation; n != nil {
		for _, thread := range n.order {
			v := n.views[thread]
			if v == u {
				continue
			}
			v.finishCommandSegments()
			result = errors.Join(result, v.finishJournalAcknowledgements(true))
			if v.panes != nil {
				result = errors.Join(result, v.panes.save(v.shell, v.now(), true))
			}
			v.hideQuestions()
			v.hideApprovals()
			v.writeUnsettledInput(out, " ("+v.orchestrationTitle()+")")
		}
	}
	return result
}
