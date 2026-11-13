package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

// nativeConnection is the invocation's actual execution owner. Its endpoints
// borrow one transport, request ledger and reader. The tree below is a passive
// native snapshot, never a Go membership/work state machine.
type nativeConnection struct {
	workers                     sync.WaitGroup
	families                    map[string]*nativeFamily
	boundRoot                   string
	suppressed                  map[string]bool // Failed/ended views require explicit reacquisition; native work is untouched.
	pendingGuards               []*sessioncontrol.ShellApprovalRequest
	root                        *nativeSession
	api                         *sessionapi.Server
	threads                     *threadExecution
	prefs                       *preferences.Service
	environment                 []string
	guards                      []binding.ShellApproval
	skillsManager, childSummary bool
	endpoints                   []*nativeEndpoint
	byThread                    map[string]*nativeEndpoint
	buffered                    map[string][]appserver.Message
	bufferedCount               int
	seenRequests                map[string]string
	attach                      chan nativeAttachRequest
	end                         chan *nativeEndRequest
	changed                     bool
	exited                      bool
}

func (c *nativeConnection) guardRequest(request *sessioncontrol.ShellApprovalRequest) error {
	if request.Finished() {
		return nil
	}
	endpoint := c.byThread[request.Thread]
	if endpoint == nil && request.Thread == c.root.Client.Thread {
		endpoint = c.endpoints[0]
	}
	if endpoint == nil || endpoint.session.Client.Thread == "" {
		c.pendingGuards = append(c.pendingGuards, request)
		c.invalidateTree(c.root.Client.Thread)
		return c.observeTree()
	}
	session := endpoint.session
	addGuardApproval(&session.attached, &session.Client, &session.picker, &session.Editor, &session.questions, &session.approvals, session.clock, session.overlayOpen(session.statusPanel, session.resumePicker), session.notifications, session.review, &session.pendingEditReceipts, request)
	return endpoint.after()
}

type nativeAttachRequest struct {
	ctx            context.Context
	parent, thread string
	reply          chan error
}

type nativeEndRequest struct {
	ctx    context.Context
	thread string
	reply  chan error
}

func newNativeConnection(root *nativeSession, api *sessionapi.Server, threads *threadExecution, prefs *preferences.Service, environment []string, _ string, guards []binding.ShellApproval, skillsManager, childSummary bool) *nativeConnection {
	return &nativeConnection{root: root, api: api, threads: threads, prefs: prefs, environment: environment, guards: guards, skillsManager: skillsManager, childSummary: childSummary,
		byThread: make(map[string]*nativeEndpoint), buffered: make(map[string][]appserver.Message), seenRequests: make(map[string]string), attach: make(chan nativeAttachRequest), end: make(chan *nativeEndRequest)}
}

func (c *nativeConnection) add(endpoint *nativeEndpoint) {
	c.endpoints = append(c.endpoints, endpoint)
	thread := endpoint.session.Client.Thread
	if endpoint.session.nativeAgent != nil {
		thread = endpoint.session.nativeAgent.ThreadID
	}
	c.byThread[thread] = endpoint
	c.changed = true
}

func (c *nativeConnection) run() (runErr error) {
	host := c.root.Client.Host
	ctx, cancel := context.WithCancel(c.root.ctx)
	c.root.finalCaptures = &nativeFinalCaptures{ctx: ctx, cancel: cancel, results: make(chan finalCaptureReply, 1)}
	c.root.finalCaptures.foreign = func(ctx context.Context, request appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
		return c.threads.hub.RouteForeign(ctx, c.api, request)
	}
	c.api.BindNativeForeign(func(ctx context.Context, request appserver.AgentForeignRequest) (appserver.AgentForeignResponse, error) {
		return c.root.Client.Requests.ExternalForeignReceive(ctx, c.root.Client.Thread, request)
	})
	if c.root.preparer != nil {
		c.root.finalCaptures.prepare = c.root.preparer.PrepareNativeFinalCapture
		c.root.finalCaptures.admit = c.root.preparer.AdmitNativeChild
		c.root.finalCaptures.freeze = c.root.preparer.FreezeNativeProgress
		c.root.finalCaptures.output = c.root.preparer.PrepareNativeProgressOutput
		c.root.finalCaptures.project = c.root.preparer.ProjectNativeFinalCapture
	}
	defer func() {
		c.root.finalCaptures.close()
		if !c.exited {
			var sides []*sessioncontrol.Side
			for _, endpoint := range c.endpoints {
				sides = append(sides, endpoint.session.Client.Requests.OwnedSides()...)
			}
			cleanup, stop := context.WithTimeout(context.WithoutCancel(c.root.ctx), 3*time.Second)
			runErr = errors.Join(runErr, c.root.Client.Requests.ArchiveSides(cleanup, host.Messages, sides...))
			stop()
			if c.root.ctx.Err() != nil {
				_ = host.Cancel()
			}
			runErr = errors.Join(runErr, host.Shutdown())
		}
		for _, endpoint := range c.endpoints {
			runErr = errors.Join(runErr, endpoint.finish(nil))
			for _, waiter := range endpoint.waiters {
				waiter.reply <- errors.New("native invocation ended during attachment")
			}
		}
		c.workers.Wait()
		if runErr != nil && c.threads.headless != nil {
			if diagnostic := host.Diagnostics.Snapshot(); diagnostic != "" {
				runErr = fmt.Errorf("%w\nCodex: %s", runErr, diagnostic)
			}
		}
		if c.root.audience != nil {
			c.root.audience.Detach()
		}
		_ = host.Input.Close()
		_ = host.Output.Close()
		if runErr != nil && c.threads.headless != nil {
			c.threads.headless.Fail(runErr)
		}
	}()
	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()
	global := &nativeEndpoint{session: c.root}
	nativeOn(global, func() <-chan struct{} { return c.root.ctx.Done() }, func(_ struct{}, _ bool) error { return c.root.ctx.Err() })
	nativeOn(global, func() <-chan appserver.Message { return host.Messages }, func(message appserver.Message, ok bool) error {
		if !ok {
			c.exited = true
			return errors.Join(errors.New("app-server disconnected; active work may be incomplete"), <-host.Done)
		}
		if c.threads.headless != nil {
			if err := c.threads.headless.Observe(message); err != nil {
				return err
			}
		}
		return c.message(message)
	})
	nativeOn(global, func() <-chan finalCaptureReply { return c.root.finalCaptures.results }, func(reply finalCaptureReply, _ bool) error { return c.root.finalCaptureReply(reply) })
	nativeOn(global, func() <-chan nativeAttachRequest { return c.attach }, func(request nativeAttachRequest, _ bool) error {
		if err := request.ctx.Err(); err != nil {
			request.reply <- err
			return nil
		}
		endpoint, err := c.attachEndpoint(request.parent, request.thread, true)
		if err != nil {
			request.reply <- err
			return nil
		}
		endpoint.waiters = append(endpoint.waiters, request)
		c.ready(endpoint)
		return nil
	})
	nativeOn(global, func() <-chan *nativeEndRequest { return c.end }, func(request *nativeEndRequest, _ bool) error { c.endEndpoint(request); return nil })
	nativeOn(global, func() <-chan time.Time { return tick.C }, func(_ time.Time, _ bool) error {
		for _, endpoint := range slices.Clone(c.endpoints) {
			if err := c.handle(endpoint, endpoint.tick); err != nil {
				return err
			}
			c.ready(endpoint)
		}
		return c.observeTree()
	})
	var sources []nativeEventSource
	var owners []*nativeEndpoint
	var cases []reflect.SelectCase
	for {
		if c.changed || sources == nil {
			sources, owners = sources[:0], owners[:0]
			for _, endpoint := range append([]*nativeEndpoint{global}, c.endpoints...) {
				for _, source := range endpoint.sources {
					sources = append(sources, source)
					owners = append(owners, endpoint)
				}
			}
			cases = make([]reflect.SelectCase, len(sources))
			c.changed = false
		}
		for i, source := range sources {
			cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: source.channel()}
		}
		index, value, ok := reflect.Select(cases)
		endpoint := owners[index]
		receive := func() error { return sources[index].receive(value, ok) }
		if endpoint == global {
			if err := receive(); err != nil {
				return err
			}
		} else if err := c.handle(endpoint, receive); err != nil {
			return err
		}
		if c.root.quitRequested {
			return nil
		}
		if c.threads.headless != nil {
			for _, current := range c.endpoints {
				session := current.session
				if session.questions.Count(session.Client.Thread) > 0 || len(session.approvals.Pending) > 0 {
					return errors.New("headless cannot answer pending questions or approvals; continue interactively")
				}
			}
			if agent := c.agent(c.root.Client.Thread); agent != nil && agent.Work != nil {
				switch agent.Work.Disposition {
				case "completed":
					return nil
				case "failed", "interrupted", "paused", "uncertain":
					return fmt.Errorf("headless native work: %s", agent.Work.Disposition)
				}
			}
		}
	}
}

func (c *nativeConnection) handle(endpoint *nativeEndpoint, receive func() error) error {
	err := endpoint.before()
	if err == nil {
		err = receive()
	}
	if err == nil {
		err = endpoint.after()
	}
	if errors.Is(err, errNativeEndpointStopped) {
		if endpoint.session == c.root {
			c.root.quitRequested = true
			return nil
		}
		endpoint.session.quitRequested = false
		c.endEndpoint(&nativeEndRequest{ctx: c.root.ctx, thread: endpoint.session.Client.Thread, reply: make(chan error, 1)})
		return nil
	}
	if err != nil && endpoint.session != c.root {
		c.retire(endpoint, err)
		setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), "Agent view unavailable: "+err.Error(), true)
		return nil
	}
	c.ready(endpoint)
	return err
}

func nativeMessageThread(message appserver.Message) string {
	var params struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(message.Params, &params)
	if params.ThreadID != "" {
		return params.ThreadID
	}
	return params.Thread.ID
}

func (c *nativeConnection) message(message appserver.Message) error {
	if handled, err := c.root.finalCaptureMessage(message); handled {
		return err
	}
	if c.threads != nil && c.threads.headless != nil && message.Method != "" && len(message.ID) != 0 {
		err := fmt.Errorf("headless cannot answer host request %s; continue interactively", message.Method)
		c.threads.headless.Fail(err)
		return err
	}
	if message.Method == "skills/changed" {
		for _, endpoint := range slices.Clone(c.endpoints) {
			if err := c.handle(endpoint, func() error { return endpoint.session.dispatch(message) }); err != nil {
				return err
			}
		}
		return nil
	}
	if message.Method == "agent/treeChanged" {
		var changed appserver.AgentTreeChanged
		if err := json.Unmarshal(message.Params, &changed); err != nil {
			return err
		}
		if changed.RootThreadID == c.root.Client.Thread || c.families[changed.RootThreadID] != nil {
			return c.observeRoot(changed.RootThreadID, changed.ObservationRevision)
		}
		return nil
	}
	var endpoint *nativeEndpoint
	if message.Method == "" {
		request, ok := c.root.Client.Requests.Lookup(string(message.ID))
		if !ok {
			return nil
		}
		if request.Owner.nativeTree != "" {
			c.root.Client.Requests.Take(string(message.ID))
			return c.treeResponse(request.Owner.nativeTree, message)
		}
		if request.Owner.nativeEnd != nil {
			c.root.Client.Requests.Take(string(message.ID))
			c.ended(request.Owner.nativeEnd, message)
			return nil
		}
		endpoint = c.byThread[request.Consumer]
		if request.Consumer == "" {
			endpoint = c.byThread[c.root.Client.Thread]
			if endpoint == nil {
				endpoint = c.endpoints[0]
			}
		}
		if endpoint != nil && endpoint.session != c.root && request.Method == "thread/resume" && message.Error != nil {
			c.retire(endpoint, fmt.Errorf("native child attachment failed: %s", message.Error.Message))
			return nil
		}
	} else {
		thread := nativeMessageThread(message)
		endpoint = c.byThread[thread]
		if endpoint == nil {
			for _, candidate := range c.endpoints {
				if candidate.session.Client.Requests.HasSideThread(thread) || candidate.session.Client.Requests.HasPredictionThread(thread) {
					endpoint = candidate
					break
				}
			}
		}
		if endpoint == nil && (thread == "" || c.root.Client.Thread == "") {
			endpoint = c.endpoints[0]
		}
		if endpoint != nil && endpoint.session != c.root && endpoint.session.Client.Thread == "" {
			c.bufferHydration(thread, message, endpoint)
			return nil
		}
		if endpoint == nil {
			if c.suppressed[thread] {
				return nil
			}
			// A notification is not membership authority. Hold only a bounded
			// hydration prefix until passive native discovery admits the endpoint.
			c.bufferHydration(thread, message, nil)
			c.invalidateTree(c.root.Client.Thread)
			return c.observeTree()
		}
		if len(message.ID) != 0 {
			key := string(message.ID)
			if _, exists := c.seenRequests[key]; exists {
				return nil
			}
			c.seenRequests[key] = thread
		}
		if message.Method == "serverRequest/resolved" {
			var resolved struct {
				RequestID jsontext.Value `json:"requestId"`
			}
			if json.Unmarshal(message.Params, &resolved) == nil {
				delete(c.seenRequests, string(resolved.RequestID))
			}
		}
	}
	if endpoint == nil {
		return nil
	}
	if err := c.handle(endpoint, func() error { return endpoint.session.dispatch(message) }); err != nil {
		return err
	}
	// Root startup confirms the only process owner's actual identity.
	if c.root.Client.Thread != "" && c.boundRoot != c.root.Client.Thread {
		c.root.audience.DetachEndpoint(c.boundRoot)
		delete(c.byThread, c.boundRoot)
		c.boundRoot = c.root.Client.Thread
		c.byThread[c.boundRoot] = c.endpoints[0]
	}
	return c.observeTree()
}

// Capacity invalidates only the affected presentation, never native work. A
// subsequent explicit attachment reloads history and native pending requests.
func (c *nativeConnection) bufferHydration(thread string, message appserver.Message, endpoint *nativeEndpoint) {
	if c.bufferedCount < 256 && len(c.buffered[thread]) < 64 {
		c.buffered[thread] = append(c.buffered[thread], message)
		c.bufferedCount++
		return
	}
	err := errors.New("agent view event backlog exceeded; select the agent again to reload history and pending prompts; native work continues")
	c.bufferedCount -= len(c.buffered[thread])
	delete(c.buffered, thread)
	if endpoint != nil {
		c.retire(endpoint, err)
	} else {
		if c.suppressed == nil {
			c.suppressed = make(map[string]bool)
		}
		c.suppressed[thread] = true
	}
	setNotice(&c.root.attached.feedback, presentationTime(c.root.clock), err.Error(), true)
}

// nativeFamily caches one authoritative observation for projection. It owns no
// membership transitions, work admission or execution lifecycle.
type nativeFamily struct {
	tree                     appserver.AgentTreeObservation
	members                  []sessionpresentation.Participant
	wanted                   uint64
	pending, dirty, observed bool
}

func (c *nativeConnection) invalidateTree(root string) {
	if root == "" {
		return
	}
	if c.families == nil {
		c.families = make(map[string]*nativeFamily)
	}
	if c.families[root] == nil {
		c.families[root] = new(nativeFamily)
	}
	c.families[root].dirty = true
}

func (c *nativeConnection) observeTree() error { return c.observeRoot(c.root.Client.Thread, 0) }

func (c *nativeConnection) observeRoot(root string, revision uint64) error {
	if root == "" {
		return nil
	}
	if c.families == nil {
		c.families = make(map[string]*nativeFamily)
	}
	family := c.families[root]
	if family == nil {
		family = new(nativeFamily)
		c.families[root] = family
	}
	family.wanted = max(family.wanted, revision)
	if family.pending || family.observed && !family.dirty && family.wanted <= family.tree.ObservationRevision {
		return nil
	}
	_, err := c.root.Client.Requests.Send("agent/observeTree", map[string]any{"rootThreadId": root}, "native/tree", appServerRequest{nativeTree: root})
	if err == nil {
		family.pending, family.dirty, family.observed = true, false, true
	}
	return err
}

func (c *nativeConnection) agent(thread string) *appserver.AgentObservation {
	for _, family := range c.families {
		for i := range family.tree.Agents {
			if family.tree.Agents[i].ThreadID == thread {
				return &family.tree.Agents[i]
			}
		}
	}
	return nil
}

func (c *nativeConnection) family(thread string) (string, []sessionpresentation.Participant) {
	agent := c.agent(thread)
	if agent == nil {
		return thread, nil
	}
	return agent.RootThreadID, c.families[agent.RootThreadID].members
}
