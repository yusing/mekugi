package sessionapi

import (
	"bytes"
	"context"
	"crypto/rand"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// Native owns only presentation subscriptions. Execution and operation admission
// remain on the runtime's existing owner loop. Route authority seals each view;
// public control views share this queue without gaining native capabilities.
type Native struct {
	mu       sync.Mutex
	requests chan NativeRequest
	views    map[string]*nativeView
	prepare  func(context.Context, string, InputRequest) (*PreparedInput, error)
	done     chan struct{}
	exit     *NativeExit
}

// NativeExit is the joined runtime outcome, distinct from a disconnected client.
// Only the private native stream publishes the diagnostic.
type NativeExit struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *NativeExit) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "The session ended."
}

func (e NativeExit) forView(authority viewAuthority) NativeExit {
	if authority != localNativeView {
		e.Message = ""
	}
	return e
}

type viewAuthority uint8

const (
	localNativeView viewAuthority = iota + 1
	remoteHumanView
)

type nativeView struct {
	authority       viewAuthority
	target          requestTarget
	startup         string
	version         sessionpresentation.Version
	ctx             context.Context
	cancel          context.CancelCauseFunc
	queue           *nativePublications
	started         bool
	frame           []byte
	main            *activity.Publication
	agents          *activity.Publication
	family          *Family
	state           []byte
	reviewState     []byte
	ownerBytes      [4]int
	reviewBytes     int
	reviewLimited   bool
	detailsKey      []byte
	details         []byte
	detailsRevision uint64
}

// nativePublications retains ordered deliveries while coalescing superseded
// complete frames. Socket writes never hold its lock or block the runtime owner.
type nativePublications struct {
	mu     sync.Mutex
	ready  chan struct{}
	events []publication
	bytes  int
}

func (q *nativePublications) push(event publication) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	var replace bool
	if len(q.events) > 0 {
		if merged, ok := coalesceNative(q.events[len(q.events)-1], event); ok {
			event, replace = merged, true
		}
	}
	event = normalizeNativeOutput(event)
	queuedBytes := q.bytes + len(event.Data)
	if replace {
		queuedBytes -= len(q.events[len(q.events)-1].Data)
	}
	// The joined exit has a reserved slot and follows all retained deliveries.
	if len(event.Data) > subscriptionBytes || event.Kind != "exit" && (queuedBytes > subscriptionBytes || !replace && len(q.events) >= subscriberBuffer) {
		return false
	}
	if replace {
		q.events[len(q.events)-1] = event
	} else {
		q.events = append(q.events, event)
	}
	q.bytes = queuedBytes
	select {
	case q.ready <- struct{}{}:
	default:
	}
	return true
}

func (q *nativePublications) pop() (publication, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.events) == 0 {
		return publication{}, false
	}
	event := q.events[0]
	q.bytes -= len(event.Data)
	q.events[0] = publication{}
	q.events = q.events[1:]
	if len(q.events) > 0 {
		select {
		case q.ready <- struct{}{}:
		default:
		}
	}
	return event, true
}

var nativeEncoding = json.JoinOptions(encoding, jsonv1.FormatDurationAsNano(true), json.Deterministic(true), json.WithMarshalers(json.MarshalToFunc(marshalNativeImageEntry)))

// Native thumbnails consume tool artifacts only. User images may contain large
// inline payloads; their existing browser read must not inflate every native feed.
func marshalNativeImageEntry(enc *jsontext.Encoder, entry activity.Entry) error {
	type nativeEntry activity.Entry
	value := struct {
		nativeEntry
		Images []string `json:"Images,omitzero"`
	}{nativeEntry: nativeEntry(entry)}
	if entry.Kind == "tool" {
		value.Images = entry.Images
	}
	return json.MarshalEncode(enc, value)
}

func (s *Server) Native() *Native {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		s.native = &Native{requests: make(chan NativeRequest), views: make(map[string]*nativeView), done: make(chan struct{}), prepare: s.prepare}
	}
	return s.native
}

func (n *Native) Requests() <-chan NativeRequest { return n.requests }

// Finish publishes once, after the execution owner has joined its resources.
// Buffered frames remain ordered before the exit; slow views close without
// blocking runtime cleanup.
func (n *Native) Finish(err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.exit != nil {
		return
	}
	outcome := NativeExit{}
	if result, ok := errors.AsType[*NativeExit](err); ok {
		outcome = *result
	} else if err != nil {
		outcome.Code, outcome.Message = 1, err.Error()
	}
	outcome.Message = textsanitize.Safe(outcome.Message, false)
	n.exit = &outcome
	data, _ := encodeEvent("exit", outcome)
	remoteData, _ := encodeEvent("exit", outcome.forView(remoteHumanView))
	for _, view := range n.views {
		event := data
		if view.authority == remoteHumanView {
			event = remoteData
		}
		view.queue.push(event)
	}
	close(n.done)
}

// Close retires one renderer subscription. It never cancels the session runtime.
func (n *Native) Close(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if view := n.views[id]; view != nil {
		view.cancel(nil)
	}
}

// Publish encodes detached values before releasing them to a client. Subsequent
// patches replace the complete native frame; they never merge editor state.
// A slow client releases only its presentation attachment.
func (n *Native) Publish(id string, frame NativeFrame) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	v := n.views[id]
	if v == nil || v.ctx.Err() != nil || n.exit != nil {
		return errors.New("native attachment is closed")
	}
	thread := frame.View.Main.ID
	if frame.Switch != nil {
		s := frame.Switch
		if s.Origin != id || s.From != v.target.thread || s.To != thread || s.To == "" {
			return errors.New("native replacement does not match its originating attachment")
		}
	} else if thread != v.target.thread || frame.View.Info.Workspace != v.target.workspace {
		return errors.New("native publication changed thread without an admitted replacement")
	}
	startup := v.startup
	if frame.Switch != nil {
		startup = ""
	}
	// Activity publications are immutable and reused by their owner. Compare
	// their identity, plus encoded non-feed facts, before serializing history.
	// Keep mutable caller values detached through bytes, not retained aliases.
	main, agents, family := frame.Main, frame.Agents, frame.Family
	frame = frame.withoutEditors()
	if v.authority == remoteHumanView {
		frame = frame.remote()
		if err := v.deferDetails(id, &frame); err != nil {
			return err
		}
	}
	var state, reviewState []byte
	if frame.Review != nil && frame.Review.Revision != 0 {
		review := *frame.Review
		review.Files, review.Unfiltered, review.Counts.Callers, review.Counts.Net = nil, nil, nil, nil
		var err error
		reviewState, err = json.Marshal(review, nativeEncoding)
		if err != nil {
			return err
		}
	}
	if len(frame.Effects) == 0 && len(frame.MainOutput) == 0 && len(frame.AgentOutput) == 0 {
		// Consequence suffixes and hydration must go through bounded prefix
		// selection without first encoding their unbounded aggregate.
		metadata := frame
		metadata.Main, metadata.Agents = nil, nil
		metadata.Family = nil
		// Saved reviews are immutable source replacements. Their revision and
		// coverage describe changes without re-encoding every retained diff.
		if frame.Review != nil && frame.Review.Revision != 0 {
			review := *frame.Review
			review.Files, review.Unfiltered, review.Counts.Callers, review.Counts.Net = nil, nil, nil, nil
			metadata.Review = &review
		}
		var err error
		state, err = json.Marshal(NativeEvent{Attachment: id, Startup: startup, Frame: metadata}, nativeEncoding)
		if err != nil {
			return err
		}
		if v.started && v.main == main && v.agents == agents && v.family == family && bytes.Equal(v.state, state) {
			return nil
		}
	}
	event := "patch"
	if !v.started || v.version.Attachment != frame.View.Version.Attachment || frame.Switch != nil {
		event = "snapshot"
	}
	var retained nativeOwners
	if event == "patch" && v.authority == localNativeView {
		if v.main == main {
			retained |= nativeMain
		}
		if v.agents == agents {
			retained |= nativeAgents
		}
		if reviewState != nil && bytes.Equal(v.reviewState, reviewState) {
			retained |= nativeReview
		}
		if v.family == family {
			retained |= nativeFamily
		}
	}
	sizes := v.ownerBytes
	var data []byte
	var err error
	reviewBytes, reviewLimited := v.reviewBytes, false
	if v.reviewLimited && reviewState != nil && bytes.Equal(v.reviewState, reviewState) {
		// Probe the original immutable review's footprint without serializing
		// it again on each frame while a consequence or hydration is pending.
		probe := sizes
		probe[2] = reviewBytes
		_, err = encodeNativePrefix(id, startup, frame, retained|nativeReview, &probe)
		if err == nil {
			retained &^= nativeReview // Replace the client's failure projection.
		}
	}
	if err == nil {
		data, err = encodeNativePrefix(id, startup, frame, retained, &sizes)
		reviewBytes = sizes[2]
	}
	if errors.Is(err, errNativePublicationLimit) && frame.Review != nil && len(frame.Review.Files)+len(frame.Review.Unfiltered) > 0 {
		// Auxiliary evidence failure cannot retire input or semantic delivery.
		// The complete source remains available to details and retained reads.
		review := *frame.Review
		review.Publication = changes.Publication{}
		review.Failure = "Saved Diff exceeds the native publication capacity; use mchanges with a narrower range."
		frame.Review = &review
		data, err = encodeNativePrefix(id, startup, frame, retained&^nativeReview, &sizes)
		reviewLimited = true
	}
	if err != nil {
		if errors.Is(err, errNativePublicationLimit) {
			v.cancel(err)
		}
		return err
	}
	if v.started && bytes.Equal(v.frame, data) {
		v.main, v.agents, v.family, v.state = main, agents, family, state
		v.reviewBytes, v.reviewLimited = reviewBytes, reviewLimited
		return nil
	}
	notification := publication{Kind: event, Data: data}
	if v.queue.push(notification) {
		v.frame = data
		v.reviewState, v.ownerBytes = reviewState, sizes
		v.reviewBytes, v.reviewLimited = reviewBytes, reviewLimited
		v.main, v.agents, v.family, v.state = main, agents, family, state
		v.started, v.version = true, frame.View.Version
		if frame.Switch != nil {
			v.target = requestTarget{thread, frame.View.Info.Workspace}
			v.startup = ""
		}
		return nil
	}
	err = errors.New("native attachment fell behind; reconnect for a snapshot")
	v.cancel(err)
	return err
}

// newView registers observation before startup can fail. Admission is separate:
// native/start installs this view before launching the execution owner.
func (n *Native) newView(ctx context.Context, target requestTarget, startup string, authority viewAuthority) (*rpcSubscription, NativeRequest, *RPCError) {
	if authority == 0 || target.thread == "" && (startup == "" || authority != localNativeView) || target.workspace == "" {
		return nil, NativeRequest{}, rpcFailure(409, "A confirmed thread and workspace are required.")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	v := &nativeView{authority: authority, target: target, startup: startup, ctx: ctx, cancel: cancel, queue: &nativePublications{ready: make(chan struct{}, 1)}}
	id := rand.Text()
	n.mu.Lock()
	if n.exit != nil {
		event, _ := encodeEvent("exit", n.exit.forView(authority))
		v.queue.push(event)
	} else {
		n.views[id] = v
	}
	n.mu.Unlock()
	subscription := newRPCSubscription(ctx, nil, func() {
		cancel(nil)
		n.mu.Lock()
		delete(n.views, id)
		n.mu.Unlock()
	})
	subscription.native = v.queue
	request := NativeRequest{authority: authority, Attach: true, Attachment: id, Startup: startup, Thread: target.thread, Workspace: target.workspace, Context: ctx, ViewContext: ctx, Reply: make(chan string, 1)}
	return subscription, request, nil
}

func (n *Native) attach(ctx, viewContext context.Context, target requestTarget, startup string, authority viewAuthority) (*rpcSubscription, *RPCError) {
	subscription, request, failure := n.newView(viewContext, target, startup, authority)
	request.Context = ctx
	if failure != nil {
		return nil, failure
	}
	if failure = n.deliver(request); failure != nil {
		subscription.close()
		return nil, failure
	}
	return subscription, nil
}

func (n *Native) request(ctx context.Context, target requestTarget, startup string, authority viewAuthority, body jsontext.Value, preparations chan struct{}) (NativeResult, *RPCError) {
	var mutation NativeMutation
	if authority == remoteHumanView {
		var remote remoteMutation
		if err := decodeBody(body, &remote); err != nil {
			return NativeResult{}, err
		}
		mutation = remote.NativeMutation
	} else if err := decodeBody(body, &mutation); err != nil {
		return NativeResult{}, err
	}
	if !mutation.Operation.valid() {
		return NativeResult{}, rpcFailure(400, "Exactly one semantic operation is required.")
	}
	n.mu.Lock()
	v := n.views[mutation.Attachment]
	valid := authority != 0 && v != nil && v.authority == authority && v.ctx.Err() == nil && v.started && v.target == target && v.startup == startup && mutation.Version.SameAttachment(v.version)
	var viewContext context.Context
	if valid {
		viewContext = v.ctx
	}
	n.mu.Unlock()
	if !valid {
		return NativeResult{}, rpcFailure(409, "The requested session attachment is unavailable.")
	}
	if reason := mutation.Operation.routeRefusal(authority); reason != "" {
		return NativeResult{}, rpcFailure(400, reason)
	}
	if answers := mutation.Operation.Answers; answers != nil && answers.public != nil {
		if err := answers.public.validate(); err != nil {
			return NativeResult{}, inputError(err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(viewContext, cancel)
	defer stop()
	defer cancel()
	request := NativeRequest{authority: authority, Attachment: mutation.Attachment, Startup: startup, Thread: target.thread, Workspace: target.workspace, Context: ctx, ViewContext: viewContext, Version: mutation.Version, Operation: mutation.Operation, Reply: make(chan string, 1)}
	if input := mutation.Operation.Composer; input != nil && (input.Intent.Operation == sessionpresentation.SubmitComposer || input.Intent.Operation == sessionpresentation.QueueComposer) {
		if n.prepare == nil {
			return NativeResult{}, rpcFailure(503, "Native attachment preparation is unavailable.")
		}
		source := InputRequest{NativeDraft: &input.Draft}
		if authority == remoteHumanView {
			source = *input.Input
		}
		prepared, failure := acquireInput(preparations, func() (*PreparedInput, error) { return n.prepare(ctx, target.workspace, source) })
		if failure != nil {
			return NativeResult{}, failure
		}
		defer prepared.Cleanup()
		request.Prepared = prepared
	}
	if answers := mutation.Operation.Answers; answers != nil && (authority == localNativeView || answers.public != nil && answers.public.hasAttachments()) {
		prepared, failure := acquireInput(preparations, func() (*PreparedInput, error) { return n.prepareAnswers(ctx, target.workspace, answers) })
		if failure != nil {
			return NativeResult{}, failure
		}
		defer prepared.Cleanup()
		request.Prepared = prepared
	}
	if err := n.deliver(request); err != nil {
		return NativeResult{}, err
	}
	n.mu.Lock()
	result := NativeResult{Version: v.version}
	n.mu.Unlock()
	return result, nil
}

func (n *Native) deliver(request NativeRequest) *RPCError {
	ctx := request.Context
	cancel := func() {}
	if request.Startup == "" || !request.Attach {
		ctx, cancel = context.WithTimeout(ctx, answerWithin)
	}
	defer cancel()
	exited := func(dispatched bool) *RPCError {
		// The registered subscription already owns the joined exit event, even
		// when initialization failed before accepting an Attach request.
		if request.Attach {
			return nil
		}
		n.mu.Lock()
		outcome := n.exit.forView(request.authority)
		n.mu.Unlock()
		if dispatched {
			return rpcUnknown("Native request outcome is unknown; the runtime ended before its reply.")
		}
		return rpcFailure(502, outcome.Error())
	}
	select {
	case n.requests <- request:
	case <-n.done:
		return exited(false)
	case <-ctx.Done():
		return rpcFailure(503, "Native presentation owner unavailable.")
	}
	select {
	case refusal := <-request.Reply:
		if refusal != "" {
			return rpcFailure(409, refusal)
		}
		if ctx.Err() == nil {
			return nil
		}
	case <-n.done:
		return exited(true)
	case <-ctx.Done():
	}
	return rpcUnknown("Native request outcome is unknown; do not resubmit automatically.")
}

// prepareAnswers acquires each answer outside the owner loop, then groups all
// acquired files under a single admission. Secret text remains only in the
// local answer payload, including when validating its attachment spans.
func (n *Native) prepareAnswers(ctx context.Context, workspace string, answers *NativeAnswers) (*PreparedInput, error) {
	if answers.public != nil {
		return answers.public.prepare(ctx, workspace, n.prepare)
	}
	if n.prepare == nil {
		return nil, errors.New("Native attachment preparation is unavailable.")
	}
	prepared := make([]*PreparedInput, 0, len(answers.Answers))
	defer func() {
		for _, input := range prepared {
			input.Cleanup()
		}
	}()
	for i := range answers.Answers {
		answer := &answers.Answers[i]
		if answer.Skip {
			answer.Draft, answer.Secret = composer.Draft{}, ""
			continue
		}
		draft := answer.Draft.Clone()
		secret := answer.Secret != ""
		if secret {
			if draft.Text != "" {
				return nil, &InputError{Message: "Secret answers require the secret input."}
			}
			draft.Text = answer.Secret
		}
		input, err := n.prepare(ctx, workspace, InputRequest{NativeDraft: &draft})
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, input)
		answer.Draft = input.Draft
		// The local editor retains bindings for restoration. Delivery consumes the
		// admitted snapshots and must not reread files or reframe selected text.
		answer.Draft.Files, answer.Draft.Selections = nil, nil
		if secret {
			answer.Secret, answer.Draft.Text = answer.Draft.Text, ""
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return joinPreparedInputs(prepared), nil
}
