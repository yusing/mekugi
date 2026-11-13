package launch

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func nativeValue[T any](value *T) (result T) {
	if value != nil {
		result = *value
	}
	return result
}

func nativeWorkStatus(agent *appserver.AgentObservation) sessionpresentation.WorkStatus {
	if agent.Work == nil {
		return ""
	}
	switch agent.Work.Disposition {
	case "pending_start":
		return sessionpresentation.Pending
	case "running":
		return sessionpresentation.WorkRunning
	case "waiting":
		return sessionpresentation.Waiting
	case "paused":
		return sessionpresentation.Paused
	case "uncertain":
		return sessionpresentation.Uncertain
	case "completed":
		return sessionpresentation.Completed
	case "failed":
		return sessionpresentation.Failed
	case "interrupted":
		return sessionpresentation.Interrupted
	}
	return ""
}

func (c *nativeConnection) treeResponse(root string, message appserver.Message) error {
	family := c.families[root]
	if family == nil {
		return nil
	}
	family.pending = false
	if message.Error != nil {
		setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Native agents unavailable: "+message.Error.Message, true)
		// Retry on the next native invalidation or explicit attachment, not on
		// every paint of the same failed observation.
		family.wanted = family.tree.ObservationRevision
		return nil
	}
	var response struct {
		Tree appserver.AgentTreeObservation `json:"tree"`
	}
	if err := json.Unmarshal(message.Result, &response); err != nil {
		setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Native agent observation could not be decoded.", true)
		family.wanted, family.dirty = family.tree.ObservationRevision, false
		return nil
	}
	if response.Tree.RootThreadID != root {
		return nil
	}
	identities := make(map[string]bool, len(response.Tree.Agents))
	for _, agent := range response.Tree.Agents {
		if agent.ThreadID == "" || agent.RootThreadID != root || identities[agent.ThreadID] {
			setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Native agent observation returned inconsistent identity.", true)
			family.wanted, family.dirty = family.tree.ObservationRevision, false
			return nil
		}
		identities[agent.ThreadID] = true
	}
	if !identities[root] {
		return nil
	}
	family.tree = response.Tree
	// Side and One-off roots share this connection but never join the source's
	// family. Project their own native work, including turnless interruption.
	for _, endpoint := range c.endpoints {
		for _, side := range endpoint.session.Client.Requests.OwnedSides() {
			if side.Thread != root || side.Closed {
				continue
			}
			for _, agent := range family.tree.Agents {
				if agent.ThreadID == side.Thread {
					side.ObserveNativeWork(agent)
				}
			}
		}
	}
	for _, endpoint := range slices.Clone(c.endpoints) {
		if endpoint.session != c.root && endpoint.session.nativeAgent.RootThreadID == root && !identities[endpoint.session.nativeAgent.ThreadID] {
			c.retire(endpoint, errors.New("native child admission is no longer present"))
		}
	}
	// Publish canonical identity before any endpoint can consume shared
	// provider supplements. This is routing metadata, not logical admission.
	if audience := c.root.audience; audience != nil {
		for _, agent := range family.tree.Agents {
			if agent.Path != nil {
				audience.ObserveAdmitted(agent.ThreadID, nativeValue(agent.ParentThreadID), *agent.Path, agent.ParentThreadID != nil)
			}
		}
	}
	family.members = make([]sessionpresentation.Participant, 0, len(family.tree.Agents))
	for i := range family.tree.Agents {
		agent := &family.tree.Agents[i]
		family.members = append(family.members, sessionpresentation.Participant{Thread: agent.ThreadID, Parent: nativeValue(agent.ParentThreadID), Task: nativeValue(agent.Path), Role: nativeValue(agent.Role), Status: nativeWorkStatus(agent), NonAttachable: agent.Attachable == nil || !*agent.Attachable})
		if endpoint := c.byThread[agent.ThreadID]; endpoint != nil {
			endpoint.session.nativeAgent = agent
			c.projectAgent(endpoint.session)
			continue
		}
		// Retained and external members remain passive until explicitly selected.
		if c.suppressed[agent.ThreadID] || !agent.Loaded || agent.ParentThreadID == nil || !nativeValue(agent.Attachable) || nativeValue(agent.Status) == "shutdown" {
			continue
		}
		if _, err := c.attachEndpoint(family.tree.RootThreadID, agent.ThreadID, false); err != nil {
			setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Agent view unavailable: "+err.Error(), true)
		}
	}
	for _, endpoint := range slices.Clone(c.endpoints) {
		c.projectAgent(endpoint.session)
		if err := endpoint.after(); err != nil {
			return err
		}
	}
	return c.observeRoot(root, family.wanted)
}

func (c *nativeConnection) projectAgent(session *nativeSession) {
	attached := &session.attached
	thread := session.Client.Thread
	if thread == "" {
		return
	}
	if attached.threadInput == nil {
		attached.threadInput = &threadInput{}
	}
	agent := c.agent(thread)
	if agent == nil {
		return
	}
	session.nativeAgent = agent
	session.Client.NativeRoot = agent.RootThreadID
	session.Client.NativeInputPending = agent.CanAcceptDirectInput != nil && !*agent.CanAcceptDirectInput
	family := c.families[agent.RootThreadID]
	attached.threadInput.admitted = true
	attached.threadInput.caller, attached.threadInput.role = nativeValue(agent.Path), nativeValue(agent.Role)
	if attached.threadInput.caller == "" && agent.ParentThreadID != nil {
		attached.threadInput.caller = sessionpresentation.Placeholder(thread)
	}
	attached.role = nativeValue(agent.Role)
	metrics := sessioncontrol.NativeAgentDetails(*agent).SessionMetrics
	attached.session.ObserveNativeMetrics(thread, metrics)
	// Root-local presentation aliases stay internal; the API supplies the
	// canonical native caller on every frame and family roster.
	if local := attached.session.Agent("/root"); local != nil {
		local.Role = nativeValue(agent.Role)
		local.ContextKnown = metrics.ContextTokens != nil
		local.ContextTokens, local.ContextWindow = nativeValue(metrics.ContextTokens), nativeValue(metrics.ContextWindow)
	}
	status := nativeWorkStatus(agent)
	if checkpoint := session.nativeCheckpoint; checkpoint != nil && (agent.Work == nil || agent.Work.WorkID != checkpoint.WorkID) {
		// A prior generation's passive snapshot cannot settle a newer native
		// checkpoint while its forced observation is in flight.
		return
	}
	previousWork := session.Client.NativeWork
	session.Client.NativeWork = ""
	if status == sessionpresentation.WorkRunning || status == sessionpresentation.Pending || status == sessionpresentation.Waiting || status == sessionpresentation.Paused && agent.Wait != nil {
		session.Client.NativeWork = agent.Work.WorkID
	}
	lifecycle := attached.session.Lifecycle(thread)
	wasPaused := lifecycle.Paused
	lifecycle.Paused = status == sessionpresentation.Paused || status == sessionpresentation.Uncertain
	if lifecycle.Paused {
		attached.presentation.Status.Set("Paused", false)
		lifecycle.ObserveActive(false, presentationTime(session.clock))
	}
	lifecycle.SetWaiting(status == sessionpresentation.Waiting, presentationTime(session.clock))
	if status == sessionpresentation.Waiting {
		lifecycle.Responding = false
	}
	if status == sessionpresentation.Completed {
		lifecycle.Final = true
		lifecycle.Responding = false
	}
	if status == sessionpresentation.Failed || status == sessionpresentation.Interrupted {
		lifecycle.Final = false
		lifecycle.Responding = false
	}
	if status == sessionpresentation.WorkRunning || status == sessionpresentation.Pending {
		if wasPaused {
			attached.presentation.Status.Set("Working", false)
		}
		lifecycle.Final = false
		lifecycle.ObserveActive(true, presentationTime(session.clock))
	}
	if previousWork != "" && session.Client.NativeWork == "" && session.Client.Turn == "" && (status == sessionpresentation.Completed || status == sessionpresentation.Failed || status == sessionpresentation.Interrupted) {
		lifecycle.TurnCompleted(status == sessionpresentation.Completed, presentationTime(session.clock))
		attached.presentation.Status.Complete(string(status), nil, presentationTime(session.clock))
	}
	session.Client.Waiting, session.Client.WaitLabel = "", ""
	lifecycle.WaitingFor = ""
	if agent.Wait == nil || status != sessionpresentation.Waiting && status != sessionpresentation.Paused {
		lifecycle.Wait = nil
	}
	if agent.Wait != nil && (status == sessionpresentation.Waiting || status == sessionpresentation.Paused) {
		session.Client.Waiting = agent.Wait.WaitID
		value := &activity.Value{Kind: "progress", Body: "Waiting for", Live: status == sessionpresentation.Waiting}
		var names []string
		for _, target := range agent.Wait.Selected {
			name := target.ThreadID
			if member := c.agent(target.ThreadID); member != nil && member.Path != nil {
				name = *member.Path
			}
			names = append(names, pathdisplay.AgentDisplayName(name))
			value.WaitTargets = append(value.WaitTargets, activity.WaitTarget{Name: name})
		}
		lifecycle.WaitingFor = strings.Join(names, ", ")
		if status == sessionpresentation.Paused && agent.Wait.Reason == nil {
			session.Client.WaitLabel = "Paused"
		} else {
			session.Client.WaitLabel = sessioncontrol.NativeWaitLabel(*agent.Wait)
		}
		if agent.Wait.Reason != nil && (*agent.Wait.Reason == "providerCapacity" || *agent.Wait.Reason == "providerUsageLimit") {
			value.Body, value.WaitTargets = session.Client.WaitLabel, nil
			lifecycle.WaitingFor = ""
		} else if status == sessionpresentation.Paused {
			value.Body, value.WaitTargets = "Paused", nil
			lifecycle.WaitingFor = ""
		} else if agent.Wait.TimeoutMS != nil {
			value.WaitTimeout = (time.Duration(*agent.Wait.TimeoutMS) * time.Millisecond).String()
		}
		if lifecycle.Wait == nil || lifecycle.Wait.Body != value.Body || lifecycle.Wait.Live != value.Live || lifecycle.Wait.WaitTimeout != value.WaitTimeout || !slices.Equal(lifecycle.Wait.WaitTargets, value.WaitTargets) {
			lifecycle.Wait = value
		}
	}
	// Cold and nonattachable members still appear from passive native facts.
	if session.nativeRosterRoot == family.tree.RootThreadID && session.nativeRosterRevision == family.tree.ObservationRevision {
		return
	}
	session.nativeRosterRoot, session.nativeRosterRevision = family.tree.RootThreadID, family.tree.ObservationRevision
	for i := range family.tree.Agents {
		member := &family.tree.Agents[i]
		if member.ThreadID == thread {
			continue
		}
		attached.session.RegisterThread(observation.Thread{ID: member.ThreadID, Path: nativeValue(member.Path), Nickname: nativeValue(member.Nickname), Role: nativeValue(member.Role), NonAttachable: !nativeValue(member.Attachable)})
		if row := attached.session.Agent(attached.session.KnownCaller(member.ThreadID)); row != nil {
			row.ParentThread = nativeValue(member.ParentThreadID)
			row.Settings.Model, row.Settings.Effort, row.Settings.Tier = nativeValue(member.Model), nativeValue(member.ReasoningEffort), nativeValue(member.ServiceTier)
			row.Settings.TierKnown = member.ServiceTier != nil
			details := sessioncontrol.NativeAgentDetails(*member)
			attached.session.ObserveNativeMetrics(member.ThreadID, details.SessionMetrics)
			row.ContextKnown = details.ContextTokens != nil
			row.ContextTokens, row.ContextWindow = nativeValue(details.ContextTokens), nativeValue(details.ContextWindow)
		}
	}
	attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
}

func (session *nativeSession) observeNativeCheckpoint(message appserver.Message) (bool, error) {
	if message.Method != "turn/checkpointed" {
		return false, nil
	}
	var receipt appserver.TurnCheckpointed
	if err := json.Unmarshal(message.Params, &receipt); err != nil {
		return true, err
	}
	if receipt.ThreadID != session.Client.Thread {
		return false, nil
	}
	if receipt.WorkID == "" || receipt.TurnID == "" || receipt.Checkpoint.Kind != "completion_pending" {
		return true, errors.New("native checkpoint omitted its work, turn or disposition")
	}
	if receipt.Checkpoint.DeliveryID == "" {
		return true, errors.New("native checkpoint omitted its retained identity")
	}
	if session.Client.Turn != "" && session.Client.Turn != receipt.TurnID {
		return true, nil
	}
	session.nativeCheckpoint = &receipt
	session.Client.Turn, session.Client.AwaitingTurn = "", false
	session.Client.NativeWork = receipt.WorkID
	session.Client.Waiting, session.Client.WaitLabel = "", ""
	lifecycle := session.attached.session.Lifecycle(receipt.ThreadID)
	lifecycle.Final = false
	lifecycle.SetWaiting(false, presentationTime(session.clock))
	session.attached.presentation.Status.SetText("Working")
	if session.connection != nil {
		root := session.connection.root.Client.Thread
		if session.nativeAgent != nil {
			root = session.nativeAgent.RootThreadID
		}
		session.connection.invalidateTree(root)
		return true, session.connection.observeRoot(root, 0)
	}
	return true, nil
}

func (c *nativeConnection) attachChild(ctx context.Context, parent, thread string) error {
	request := nativeAttachRequest{ctx: ctx, parent: parent, thread: thread, reply: make(chan error, 1)}
	select {
	case c.attach <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.root.ctx.Done():
		return c.root.ctx.Err()
	}
	select {
	case err := <-request.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.root.ctx.Done():
		return c.root.ctx.Err()
	}
}

func (c *nativeConnection) attachEndpoint(parent, thread string, explicit bool) (*nativeEndpoint, error) {
	if explicit {
		delete(c.suppressed, thread)
	}
	agent := c.agent(thread)
	if agent != nil && parent == "" {
		parent = agent.RootThreadID
	}
	if agent == nil || agent.ParentThreadID == nil || thread == agent.RootThreadID {
		return nil, errors.New("thread is not an admitted native descendant")
	}
	ancestor := nativeValue(agent.ParentThreadID)
	for ancestor != parent && ancestor != "" {
		member := c.agent(ancestor)
		if member == nil {
			break
		}
		ancestor = nativeValue(member.ParentThreadID)
	}
	if ancestor != parent {
		return nil, errors.New("attachment owner is not a native ancestor")
	}
	if !nativeValue(agent.Attachable) {
		return nil, errors.New("the selected native agent is not attachable")
	}
	if endpoint := c.byThread[thread]; endpoint != nil {
		return endpoint, nil
	}
	if !explicit && !agent.Loaded {
		return nil, errors.New("cold attachment requires explicit selection")
	}
	if agent.Cwd == nil || *agent.Cwd == "" {
		return nil, errors.New("native child workspace is unavailable")
	}
	api, err := c.threads.hub.AdoptThread(c.api, thread, *agent.ParentThreadID, *agent.Cwd, func(ctx context.Context) error { return c.endChild(ctx, thread) })
	if err != nil {
		return nil, err
	}
	r := c.root
	session := &nativeSession{connection: c, nativeAgent: agent, preparer: r.preparer, reviewSource: r.reviewSource,
		presentationState: r.presentationState, conversationState: r.conversationState, activity: r.activity, history: r.history, segments: r.segments,
		ctx: r.ctx, clock: r.clock, controlObservations: r.controlObservations, usage: r.usage, audience: r.audience, review: r.review.NewEndpoint(r.ctx),
		execWindows: r.execWindows, nativeTrace: r.nativeTrace, retained: r.retained, changeSource: r.changeSource, snapshots: r.snapshots, recovered: r.recovered,
		publishChanges: r.publishChanges, captureNotice: r.captureNotice, execTrack: r.execTrack, issues: r.issues, serviceTiers: r.serviceTiers,
		sessionCapture: r.sessionCapture, replayDebugDirectory: r.replayDebugDirectory, observations: r.observations, approvalMode: r.approvalMode,
		deduplicateOutput: r.deduplicateOutput, backendVersion: r.backendVersion}
	session.Client = r.Client.ForThread(thread)
	session.Client.Thread = "" // Native identity is known; the presentation is not hydrated yet.
	session.Client.Startup.Thread = thread
	endpoint, err := session.prepareEndpoint(c.prefs, c.environment, *agent.Cwd, nil, c.guards, c.skillsManager, nil, api, c.childSummary, c.threads, "")
	if err != nil {
		c.threads.hub.RetireThread(api, thread, err)
		return nil, err
	}
	session.attached.role = nativeValue(agent.Role)
	session.attached.nativeIdentity = true
	c.add(endpoint)
	session.audience.AttachEndpoint(thread)
	// No root configuration or permission overrides cross this attachment.
	if err := session.Client.Request("thread/resume", map[string]any{"threadId": thread}); err != nil {
		c.retire(endpoint, err)
		return nil, err
	}
	return endpoint, nil
}

func (c *nativeConnection) ready(endpoint *nativeEndpoint) {
	if endpoint.session.Client.Thread == "" || endpoint.session.restoring != nil {
		return
	}
	thread := endpoint.session.Client.Thread
	if endpoint.session != c.root && c.byThread[thread] != endpoint {
		return
	}
	pending := c.buffered[thread]
	delete(c.buffered, thread)
	c.bufferedCount -= len(pending)
	for _, message := range pending {
		if err := c.message(message); err != nil {
			setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Native event replay failed: "+err.Error(), true)
		}
		if endpoint.session != c.root && c.byThread[thread] != endpoint {
			return
		}
	}
	for _, request := range endpoint.waiters {
		request.reply <- request.ctx.Err()
	}
	endpoint.waiters = nil
	if len(c.pendingGuards) != 0 {
		pending := c.pendingGuards
		c.pendingGuards = nil
		for _, request := range pending {
			if request.Finished() {
				continue
			}
			if request.Thread == endpoint.session.Client.Thread {
				session := endpoint.session
				addGuardApproval(&session.attached, &session.Client, &session.picker, &session.Editor, &session.questions, &session.approvals, session.clock, session.overlayOpen(session.statusPanel, session.resumePicker), session.notifications, session.review, &session.pendingEditReceipts, request)
			} else {
				c.pendingGuards = append(c.pendingGuards, request)
			}
		}
	}
}

func (c *nativeConnection) endChild(ctx context.Context, thread string) error {
	request := &nativeEndRequest{ctx: ctx, thread: thread, reply: make(chan error, 1)}
	select {
	case c.end <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.root.ctx.Done():
		return c.root.ctx.Err()
	}
	select {
	case err := <-request.reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.root.ctx.Done():
		return c.root.ctx.Err()
	}
}

func (c *nativeConnection) endEndpoint(request *nativeEndRequest) {
	endpoint := c.byThread[request.thread]
	if err := request.ctx.Err(); err != nil {
		request.reply <- err
		return
	}
	if endpoint == nil || endpoint.session == c.root {
		request.reply <- errors.New("native end requires an admitted non-root endpoint")
		return
	}
	if endpoint.ending {
		request.reply <- errors.New("native end is already pending")
		return
	}
	_, err := c.root.Client.Requests.Send("agent/end", map[string]any{"rootThreadId": endpoint.session.nativeAgent.RootThreadID, "threadId": request.thread}, "native/end", appServerRequest{nativeEnd: request})
	if err != nil {
		request.reply <- err
		return
	}
	endpoint.ending = true
}

func (c *nativeConnection) ended(request *nativeEndRequest, message appserver.Message) {
	if message.Error != nil {
		if endpoint := c.byThread[request.thread]; endpoint != nil {
			endpoint.ending = false
		}
		request.reply <- fmt.Errorf("native end rejected: %s", message.Error.Message)
		return
	}
	// The native reply is the join fence. Retirement follows that fact, never
	// a transport acknowledgment of an interrupt or an archive operation.
	var result struct {
		ThreadIDs []string `json:"threadIds"`
	}
	if err := json.Unmarshal(message.Result, &result); err != nil || !slices.Contains(result.ThreadIDs, request.thread) {
		request.reply <- errors.New("native end returned no confirmed target join")
		return
	}
	for _, thread := range result.ThreadIDs {
		if endpoint := c.byThread[thread]; endpoint != nil && endpoint.session != c.root {
			c.retire(endpoint, nil)
		}
	}
	request.reply <- nil
}

func (c *nativeConnection) retire(endpoint *nativeEndpoint, outcome error) {
	thread := endpoint.session.nativeAgent.ThreadID
	endpoint.session.audience.DetachEndpoint(thread)
	if c.suppressed == nil {
		c.suppressed = make(map[string]bool)
	}
	c.suppressed[thread] = true
	delete(c.byThread, thread)
	c.endpoints = slices.DeleteFunc(c.endpoints, func(candidate *nativeEndpoint) bool { return candidate == endpoint })
	c.changed = true
	for id, owner := range c.seenRequests {
		if owner == thread {
			delete(c.seenRequests, id)
		}
	}
	outcome = errors.Join(outcome, endpoint.finish(nil))
	for _, request := range endpoint.waiters {
		request.reply <- errors.Join(errors.New("native view ended during attachment"), outcome)
	}
	endpoint.waiters = nil
	c.threads.hub.RetireThread(endpoint.api, thread, outcome)
}
