package launch

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"
)

func nativeValue[T any](value *T) (result T) {
	if value != nil {
		result = *value
	}
	return result
}

func nativeWorkStatus(agent *binding.ThreadInfo) sessionpresentation.WorkStatus {
	switch agent.AgentState() {
	case "pending_init":
		return sessionpresentation.Pending
	case "running":
		return sessionpresentation.WorkRunning
	case "completed":
		return sessionpresentation.Completed
	case "errored":
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
		family.dirty = false
		return nil
	}
	var response struct {
		Data []binding.ThreadInfo `json:"data"`
	}
	if err := json.Unmarshal(message.Result, &response); err != nil {
		setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Native agent observation could not be decoded.", true)
		family.dirty = false
		return nil
	}
	identities := make(map[string]bool, len(response.Data))
	for _, agent := range response.Data {
		if agent.ID == "" || identities[agent.ID] {
			setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Native agent observation returned inconsistent identity.", true)
			family.dirty = false
			return nil
		}
		identities[agent.ID] = true
	}
	if !identities[root] {
		return nil
	}
	family.threads = response.Data
	family.response = string(message.ID)
	for _, endpoint := range slices.Clone(c.endpoints) {
		if endpoint.session != c.root && endpoint.session.Client.NativeRoot == root && !identities[endpoint.session.nativeAgent.ID] {
			c.retire(endpoint, errors.New("native child admission is no longer present"))
		}
	}
	// Publish canonical identity before any endpoint can consume shared
	// provider supplements. This is routing metadata, not logical admission.
	if audience := c.root.audience; audience != nil {
		for _, agent := range family.threads {
			if agent.AgentPath() != "" {
				audience.ObserveAdmitted(agent.ID, agent.ParentThreadID, agent.AgentPath(), agent.ParentThreadID != "")
			}
		}
	}
	family.members = make([]sessionpresentation.Participant, 0, len(family.threads))
	for i := range family.threads {
		agent := &family.threads[i]
		family.members = append(family.members, sessionpresentation.Participant{Thread: agent.ID, Parent: agent.ParentThreadID, Task: agent.AgentPath(), Role: agent.AgentRole, Status: nativeWorkStatus(agent)})
		if endpoint := c.byThread[agent.ID]; endpoint != nil {
			session := endpoint.session
			session.nativeAgent = agent
			c.projectAgent(session)
			if session.nativeReadOnly && agent.IsLoaded() && session.Client.Thread != "" && session.restoring == nil {
				// The parent reactivated a retained child while this view was open.
				// Join the loaded runtime without replacing the selected endpoint.
				if err := session.Client.Request("thread/resume", map[string]any{"threadId": agent.ID}); err != nil {
					setNotice(&session.attached.feedback, presentationTime(session.clock), "Could not join live agent: "+err.Error(), true)
				} else {
					session.restoring = new(appServerActivityRestore)
				}
			}
			continue
		}
		// Retained and external members remain passive until explicitly selected.
		if c.suppressed[agent.ID] || !agent.IsLoaded() || agent.ParentThreadID == "" {
			continue
		}
		if _, err := c.attachEndpoint(root, agent.ID, false); err != nil {
			setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Agent view unavailable: "+err.Error(), true)
		}
	}
	for _, endpoint := range slices.Clone(c.endpoints) {
		c.projectAgent(endpoint.session)
		if err := endpoint.after(); err != nil {
			return err
		}
	}
	return c.observeRoot(root)
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
	root, _ := c.family(thread)
	session.Client.NativeRoot = root
	session.Client.NativeInputPending = session.nativeReadOnly || agent.CanAcceptDirectInput != nil && !*agent.CanAcceptDirectInput
	family := c.families[root]
	attached.threadInput.admitted = true
	attached.threadInput.caller, attached.threadInput.role = agent.AgentPath(), agent.AgentRole
	if attached.threadInput.caller == "" && agent.ParentThreadID != "" {
		attached.threadInput.caller = sessionpresentation.Placeholder(thread)
	}
	attached.role = agent.AgentRole
	oldCaller := attached.session.KnownCaller(thread)
	attached.session.RegisterThread(observation.Thread{ID: thread, Path: attached.threadInput.caller, Nickname: agent.AgentNickname, Role: agent.AgentRole})
	if caller := attached.session.KnownCaller(thread); oldCaller != "" && oldCaller != caller {
		attached.presentation.RenameCaller(oldCaller, caller)
		attached.source.RenameCaller(oldCaller, caller)
		attached.childSource.RenameCaller(oldCaller, caller)
	}
	metrics := agent.Details().SessionMetrics
	attached.session.ObserveNativeMetrics(thread, metrics)
	if local := attached.session.Activity().Agent(thread); local != nil {
		local.Role = agent.AgentRole
		local.ContextKnown = metrics.ContextTokens != nil
		local.ContextTokens, local.ContextWindow = nativeValue(metrics.ContextTokens), nativeValue(metrics.ContextWindow)
	}
	// Retained members still appear from passive native facts.
	if session.nativeRosterRoot == root && session.nativeRosterResponse == family.response {
		return
	}
	session.nativeRosterRoot, session.nativeRosterResponse = root, family.response
	for i := range family.threads {
		member := &family.threads[i]
		if member.ID == thread {
			continue
		}
		attached.session.RegisterThread(observation.Thread{ID: member.ID, Path: member.AgentPath(), Nickname: member.AgentNickname, Role: member.AgentRole})
		if row := attached.session.Agent(attached.session.KnownCaller(member.ID)); row != nil {
			row.ParentThread = member.ParentThreadID
			row.Settings.Model, row.Settings.Effort, row.Settings.Tier = nativeValue(member.Model), nativeValue(member.ReasoningEffort), nativeValue(member.ServiceTier)
			row.Settings.TierKnown = member.ServiceTier != nil
			details := member.Details()
			attached.session.ObserveNativeMetrics(member.ID, details.SessionMetrics)
			row.ContextKnown = details.ContextTokens != nil
			row.ContextTokens, row.ContextWindow = nativeValue(details.ContextTokens), nativeValue(details.ContextWindow)
		}
	}
	attached.childSource.Apply(sessionpresentation.ActivityEvent{Agents: attached.session.Roster()})
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
	root, _ := c.family(thread)
	if agent != nil && parent == "" {
		parent = root
	}
	if agent == nil || agent.ParentThreadID == "" || thread == root {
		return nil, errors.New("thread is not an admitted native descendant")
	}
	ancestor := agent.ParentThreadID
	for ancestor != parent && ancestor != "" {
		member := c.agent(ancestor)
		if member == nil {
			break
		}
		ancestor = member.ParentThreadID
	}
	if ancestor != parent {
		return nil, errors.New("attachment owner is not a native ancestor")
	}
	if endpoint := c.byThread[thread]; endpoint != nil {
		return endpoint, nil
	}
	if !explicit && !agent.IsLoaded() {
		return nil, errors.New("cold attachment requires explicit selection")
	}
	if agent.Cwd == "" {
		return nil, errors.New("native child workspace is unavailable")
	}
	api, err := c.threads.hub.AdoptThread(c.api, thread, agent.ParentThreadID, agent.Cwd, func(ctx context.Context) error { return c.endChild(ctx, thread) })
	if err != nil {
		return nil, err
	}
	r := c.root
	session := &nativeSession{connection: c, nativeAgent: agent, nativeReadOnly: !agent.IsLoaded(), preparer: r.preparer, reviewSource: r.reviewSource,
		presentationState: r.presentationState, conversationState: r.conversationState, activity: r.activity, history: r.history, segments: r.segments,
		ctx: r.ctx, clock: r.clock, controlObservations: r.controlObservations, usage: r.usage, audience: r.audience, review: r.review.NewEndpoint(r.ctx),
		execWindows: r.execWindows, nativeTrace: r.nativeTrace, retained: r.retained, changeSource: r.changeSource, snapshots: r.snapshots, recovered: r.recovered,
		publishChanges: r.publishChanges, captureNotice: r.captureNotice, execTrack: r.execTrack, issues: r.issues, serviceTiers: r.serviceTiers,
		sessionCapture: r.sessionCapture, replayDebugDirectory: r.replayDebugDirectory, observations: r.observations, approvalMode: r.approvalMode,
		backendVersion: r.backendVersion}
	session.Client = r.Client.ForThread(thread)
	session.Client.NativeRoot = root
	session.Client.Thread = "" // Native identity is known; the presentation is not hydrated yet.
	session.Client.Startup.Thread = thread
	endpoint, err := session.prepareEndpoint(c.prefs, c.environment, agent.Cwd, nil, c.guards, c.skillsManager, nil, api, c.childSummary, c.threads, "")
	if err != nil {
		c.threads.hub.RetireThread(api, thread, err)
		return nil, err
	}
	session.attached.role = agent.AgentRole
	session.attached.nativeIdentity = true
	c.add(endpoint)
	session.audience.AttachEndpoint(thread)
	// Attaching a retained-only child cannot start native work. Loaded
	// children need resume's listener and live snapshot, not a passive read.
	method, params := "thread/resume", map[string]any{"threadId": thread}
	if session.nativeReadOnly {
		method = "thread/read"
		params["includeTurns"] = true
	}
	if err := session.Client.Request(method, params); err != nil {
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
	_, err := c.root.Client.Requests.Send("agent/end", map[string]any{"rootThreadId": endpoint.session.Client.NativeRoot, "threadId": request.thread}, "native/end", appServerRequest{nativeEnd: request})
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
	thread := endpoint.session.nativeAgent.ID
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
