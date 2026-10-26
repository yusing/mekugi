// Package sessionapi exposes detached session publications and control intents
// to admitted clients. Launch applies those intents through the session owners.
package sessionapi

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"

	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

// Action is one renderer-neutral request from a viewer.
type Action string

const (
	TogglePause Action = "toggle-pause"
	Faster      Action = "faster"
	Slower      Action = "slower"
	SeekBack    Action = "back"
	SeekForward Action = "forward"
	Restart     Action = "restart"
	Seek        Action = "seek" // Position selects the recorded offset.

	Send            Action = "send" // Text is Main's input.
	Stop            Action = "stop"
	AnswerQuestions Action = "answer"
	DecideApproval  Action = "decide"
	Redraw          Action = "redraw"
	RunShell        Action = "shell"
)

// Intent is one viewer request for the owner goroutine. A live session's
// intent carries Reply, which receives why the session refused it, or "".
type Intent struct {
	Context   context.Context
	Thread    string
	Workspace string
	Action    Action
	Position  time.Duration
	Text      string
	Prepared  *PreparedInput
	Answer    Answer
	Decide    Decide
	Reply     chan<- string
}

type Answer struct {
	Call    string           `json:"call"`
	Answers []QuestionAnswer `json:"answers"`
}

type QuestionAnswer struct {
	Question string `json:"question"`
	Choice   *int   `json:"choice,omitzero"`
	Text     string `json:"text,omitzero"`
	Skip     bool   `json:"skip,omitzero"`
}

type Decide struct {
	Approval string `json:"approval"`
	Choice   int    `json:"choice"`
	Reason   string `json:"reason,omitzero"`
}

// subscriberBuffer bounds queued messages before reconstructible publications
// catch up with a snapshot. Ordered deliveries retain their own capacity limit.
const subscriberBuffer = 64

const subscriptionBytes = RPCPublicationLimit

// inputLimit bounds one input request's body.
const inputLimit = 1 << 20

// Recorded text and output are not guaranteed to be valid UTF-8; invalid
// bytes become U+FFFD rather than failing the stream.
var encoding = jsontext.AllowInvalidUTF8(true)

// Server serves one session to any number of viewers. Publish and SetSummary
// are called by the session's owner goroutine, which also receives Intents.
type Server struct {
	live          bool
	native        *Native
	intents       chan Intent
	prepare       func(context.Context, string, InputRequest) (*PreparedInput, error)
	activeThread  func(string) bool
	reserveThread func(string) error
	family        func() (*Family, *changes.ReviewView)

	mu              sync.Mutex
	requests        threadcontrol.Runtime
	childAttachment func(context.Context, string, string) error
	childAttach     sync.Mutex
	published       bool
	attachment      uint64
	session         Session
	playback        *Playback
	input           *Input
	prompts         jsontext.Value
	status          jsontext.Value
	main            feed
	agents          feed
	roster          jsontext.Value
	rosterSource    *activitypresentation.Publication
	review          jsontext.Value
	reviewRevision  uint64
	reviewWorkspace string
	livePreviews    jsontext.Value
	subscribers     map[chan publication]struct{}
	identity        Summary
	frame           atomic.Pointer[Frame] // Raw local input; family projections never feed back here.
	summary         []byte                // The latest summary event.
	summaryChanged  chan struct{}
}

// ActiveThread queries the supplied execution registry. It grants no ownership
// and reserves no lifetime; native clients confirm their destination by attaching.
func (s *Server) ActiveThread(id string) bool {
	return s.activeThread != nil && s.activeThread(id)
}

// ReserveThread excludes another owner while a saved thread is being opened.
// An empty identity releases a failed opening; confirmed publications release
// their matching reservation while retaining the registered runtime ownership.
func (s *Server) ReserveThread(id string) error {
	if s.reserveThread != nil {
		return s.reserveThread(id)
	}
	return nil
}

// feed holds one publication's projected entries by sequence.
type feed struct {
	publication *activitypresentation.Publication
	summaries   bool
	entries     map[uint64]projected
	order       []uint64
}

// projected is one entry's encoding at the revision it was encoded from; an
// entry's revision advances whenever anything it publishes changes.
type projected struct {
	revision uint64
	data     jsontext.Value
}

type feedPatch struct {
	Entries []jsontext.Value `json:"entries,omitzero"`
	Order   []uint64         `json:"order,omitzero"`
}

type message struct {
	Session  *Session       `json:"session,omitzero"`
	Playback *Playback      `json:"playback,omitzero"`
	Input    *Input         `json:"input,omitzero"`
	Prompts  jsontext.Value `json:"prompts,omitzero"`
	Status   jsontext.Value `json:"status,omitzero"`
	Main     *feedPatch     `json:"main,omitzero"`
	Agents   *feedPatch     `json:"agents,omitzero"`
	Roster   jsontext.Value `json:"roster,omitzero"`
	Review   jsontext.Value `json:"review,omitzero"`
	Live     jsontext.Value `json:"live,omitzero"`
}

// New serves a replay, whose viewers request playback.
func New() *Server {
	return &Server{intents: make(chan Intent, 16), subscribers: make(map[chan publication]struct{}), summaryChanged: make(chan struct{}, 1)}
}

// NewLive serves a live session, whose viewers send input and interrupt it.
// Its intents are unbuffered: one is received only when the owner is ready
// to apply it.
func NewLive() *Server {
	return &Server{live: true, intents: make(chan Intent), subscribers: make(map[chan publication]struct{}), summaryChanged: make(chan struct{}, 1)}
}

// NewPreparedLive binds acquisition to the execution owner. Preparation runs
// on the request goroutine, outside the session's admission loop.
func NewPreparedLive(prepare func(context.Context, string, InputRequest) (*PreparedInput, error)) *Server {
	s := NewLive()
	s.prepare = prepare
	return s
}

// Intents delivers viewer requests to the owner goroutine.
func (s *Server) Intents() <-chan Intent { return s.intents }

// SetSummary records the session's list entry and notifies its runtime owner.
func (s *Server) SetSummary(summary Summary) error {
	data, err := json.Marshal(struct {
		listed
		Ready bool `json:"ready,omitzero"`
	}{projectSummary(summary), summary.Ready}, encoding)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes.Equal(data, s.summary) {
		return nil
	}
	s.identity = summary
	s.summary = data
	select {
	case s.summaryChanged <- struct{}{}:
	default:
	}
	return nil
}

// Publish records the latest frame and sends changed entries to viewers.
// A new attachment or thread replaces every viewer's state with a snapshot.
func (s *Server) Publish(f Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frame.Store(new(f))
	var family *Family
	if s.family != nil {
		var review *changes.ReviewView
		family, review = s.family()
		if family != nil {
			f.Review = review
			if f.Session.Thread == family.Root {
				f.Live = family.Live
			}
		}
	}
	reset := !s.published || f.Attachment != s.attachment || f.Session.Thread != s.session.Thread
	s.published, s.attachment = true, f.Attachment
	if reset {
		s.input, s.status, s.main, s.agents, s.roster = nil, nil, feed{}, feed{}, nil
		s.rosterSource = nil
		s.prompts = nil
		s.review, s.livePreviews = nil, nil
	}
	var patch message
	f.Session.Title = textsanitize.Safe(f.Session.Title, false)
	if f.Session != s.session {
		s.session, patch.Session = f.Session, &f.Session
	}
	if p := f.Playback; p != nil && (s.playback == nil || *p != *s.playback) {
		s.playback, patch.Playback = new(*p), p
	}
	if i := f.Input; i != nil && (s.input == nil || *i != *s.input) {
		s.input, patch.Input = new(*i), i
	}
	if f.Input != nil {
		prompts := f.Prompts
		if prompts == nil {
			prompts = &Prompts{}
		}
		data, err := json.Marshal(prompts, encoding)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, s.prompts) {
			s.prompts, patch.Prompts = data, data
		}
	}
	data, err := json.Marshal(projectStatus(f.Status), encoding)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, s.status) {
		s.status, patch.Status = data, data
	}
	if f.Review != nil && (s.review == nil || f.Review.Revision != s.reviewRevision || f.Review.Workspace != s.reviewWorkspace) {
		review := projectReview(f.Review)
		data, err := json.Marshal(&review, encoding)
		if err != nil {
			return err
		}
		s.review, patch.Review, s.reviewRevision = data, data, f.Review.Revision
		s.reviewWorkspace = f.Review.Workspace
	} else if f.Review == nil && s.review != nil {
		s.review, patch.Review = nil, jsontext.Value("null")
	}
	if s.live || f.Live != nil || s.livePreviews != nil {
		data, err := json.Marshal(f.Live, encoding)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, s.livePreviews) {
			s.livePreviews, patch.Live = data, data
		}
	}
	if patch.Main, err = s.main.update(f.Main, nil, f.ChildReplySummary); err != nil {
		return err
	}
	rosterSource := f.Agents
	if family != nil {
		rosterSource = family.Agents
	}
	if rosterSource != nil && rosterSource != s.rosterSource {
		data, err := json.Marshal(projectRoster(rosterSource), encoding)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, s.roster) {
			s.roster, patch.Roster = data, data
		}
		s.rosterSource = rosterSource
	}
	// The child feed repeats Main's activity under Main's caller path or name;
	// viewers read that from Main's own feed.
	if patch.Agents, err = s.agents.update(f.Agents, func(e activitypresentation.Entry) bool { return e.Agent != "/root" && e.Agent != "Main" }, f.ChildReplySummary); err != nil {
		return err
	}
	if reset {
		data, err := s.snapshot()
		if err != nil {
			return err
		}
		return s.broadcast(data, true)
	}
	if patch.Session == nil && patch.Playback == nil && patch.Input == nil && patch.Prompts == nil && patch.Status == nil && patch.Main == nil && patch.Agents == nil && patch.Roster == nil && patch.Review == nil && patch.Live == nil {
		return nil
	}
	event, err := encodeEvent("patch", &patch)
	if err != nil {
		return err
	}
	return s.broadcast(event, false)
}

// update projects the visible entries that keep admits, or all of them when
// keep is nil, and returns those whose projection changed. An unchanged
// publication pointer is immutable, so it is skipped without encoding, and so
// is an entry whose revision is unchanged.
func (f *feed) update(publication *activitypresentation.Publication, keep func(activitypresentation.Entry) bool, summaries bool) (*feedPatch, error) {
	if publication == nil || publication == f.publication {
		return nil, nil
	}
	if f.entries == nil || f.summaries != summaries {
		f.entries, f.summaries = map[uint64]projected{}, summaries
	}
	f.publication = publication
	var patch feedPatch
	order := make([]uint64, 0, len(publication.Entries))
	for _, e := range publication.Entries {
		if e.Hidden() || keep != nil && !keep(e) {
			continue
		}
		order = append(order, e.Seq)
		previous, ok := f.entries[e.Seq]
		if ok && previous.revision == e.Revision {
			continue
		}
		data, err := json.Marshal(projectEntry(e, summaries), encoding)
		if err != nil {
			return nil, err
		}
		f.entries[e.Seq] = projected{e.Revision, data}
		if !ok || !bytes.Equal(previous.data, data) {
			patch.Entries = append(patch.Entries, data)
		}
	}
	if !slices.Equal(order, f.order) {
		kept := make(map[uint64]bool, len(order))
		for _, seq := range order {
			kept[seq] = true
		}
		for _, seq := range f.order {
			if !kept[seq] {
				delete(f.entries, seq)
			}
		}
		f.order, patch.Order = order, order
	}
	if patch.Entries == nil && patch.Order == nil {
		return nil, nil
	}
	return &patch, nil
}

func (f *feed) snapshot() *feedPatch {
	p := &feedPatch{Order: f.order}
	for _, seq := range f.order {
		p.Entries = append(p.Entries, f.entries[seq].data)
	}
	return p
}

// publication is a detached payload shared by all transport subscribers.
type publication struct {
	Kind string
	Data jsontext.Value
}

func (s *Server) snapshot() (publication, error) {
	m := message{Session: &s.session, Playback: s.playback, Input: s.input, Prompts: s.prompts, Status: s.status, Main: s.main.snapshot(), Agents: s.agents.snapshot(), Roster: s.roster, Review: s.review, Live: s.livePreviews}
	return encodeEvent("snapshot", &m)
}

func encodeEvent(name string, v any) (publication, error) {
	data, err := json.Marshal(v, encoding)
	return publication{Kind: name, Data: data}, err
}

// broadcast queues data for every viewer. A snapshot replaces anything still
// queued, since it carries the complete state.
func (s *Server) broadcast(data publication, snapshot bool) error {
	var catchup publication
	for ch := range s.subscribers {
		for snapshot && len(ch) > 0 {
			select {
			case <-ch:
			default:
			}
		}
		select {
		case ch <- data:
		default:
			// The owner already holds the complete state. Drop queued deltas,
			// never the subscription or its semantic interaction lifetime.
			if catchup.Kind == "" {
				var err error
				catchup, err = s.snapshot()
				if err != nil {
					return err
				}
			}
			for len(ch) > 0 {
				select {
				case <-ch:
				default:
				}
			}
			ch <- catchup
		}
	}
	return nil
}

func (s *Server) subscribe(ctx context.Context) (*rpcSubscription, *RPCError) {
	ch := make(chan publication, subscriberBuffer)
	s.mu.Lock()
	if s.published {
		first, err := s.snapshot()
		if err != nil {
			s.mu.Unlock()
			return nil, rpcFailure(500, "Publication unavailable.")
		}
		ch <- first
	}
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	return newRPCSubscription(ctx, ch, func() {
		s.mu.Lock()
		if _, ok := s.subscribers[ch]; ok {
			delete(s.subscribers, ch)
			close(ch)
		}
		s.mu.Unlock()
	}), nil
}

// request dispatches typed operations without an intermediate HTTP request.
func (s *Server) request(ctx context.Context, target requestTarget, method string, body jsontext.Value, preparations chan struct{}) (any, *RPCError) {
	intent := Intent{Thread: target.thread, Workspace: target.workspace}
	switch method {
	case "session/control":
		if s.live {
			return nil, rpcFailure(405, "a live session has no playback")
		}
		var control struct {
			Action   Action `json:"action"`
			Position int64  `json:"position"`
		}
		if err := decodeBody(body, &control); err != nil {
			return nil, err
		}
		switch control.Action {
		case TogglePause, Faster, Slower, SeekBack, SeekForward, Restart, Seek:
		default:
			return nil, rpcFailure(400, "unknown action "+string(control.Action))
		}
		intent.Action = control.Action
		intent.Position = time.Duration(min(max(control.Position, 0), math.MaxInt64/int64(time.Millisecond))) * time.Millisecond
		intent.Context = ctx
		select {
		case s.intents <- intent:
			return nil, nil
		case <-ctx.Done():
			return nil, rpcFailure(503, "Request canceled before admission.")
		}
	case "session/input":
		if !s.live {
			return nil, rpcFailure(405, "a replay takes no input")
		}
		var input InputRequest
		if err := decodeBody(body, &input); err != nil {
			return nil, err
		}
		if strings.TrimSpace(input.Text) == "" && len(input.Attachments) == 0 {
			return nil, rpcFailure(400, "input is empty")
		}
		intent.Action, intent.Text = Send, input.Text
		if s.prepare != nil {
			prepared, failure := acquireInput(preparations, func() (*PreparedInput, error) { return s.prepare(ctx, target.workspace, input) })
			if failure != nil {
				return nil, failure
			}
			defer prepared.Cleanup()
			intent.Prepared = prepared
		} else if len(input.Attachments) > 0 {
			return nil, rpcFailure(503, "Attachment preparation is unavailable.")
		} else if len(input.Text) > inputLimit {
			return nil, rpcFailure(413, "input text exceeds the request limit")
		}
	case "session/interrupt", "session/redraw":
		if !s.live {
			return nil, rpcFailure(405, "a replay cannot be interrupted or redrawn")
		}
		intent.Action = Stop
		if method == "session/redraw" {
			intent.Action = Redraw
		}
	case "session/shell":
		if !s.live {
			return nil, rpcFailure(405, "a replay cannot execute shell commands")
		}
		var shell struct {
			Command string `json:"command"`
		}
		if err := decodeBody(body, &shell); err != nil {
			return nil, err
		}
		if strings.TrimSpace(shell.Command) == "" {
			return nil, rpcFailure(400, "shell command is empty")
		}
		intent.Action, intent.Text = RunShell, shell.Command
	case "session/answer":
		if !s.live {
			return nil, rpcFailure(405, "A replay has no pending questions.")
		}
		if err := decodeBody(body, &intent.Answer); err != nil {
			return nil, err
		}
		intent.Action = AnswerQuestions
	case "session/decide":
		if !s.live {
			return nil, rpcFailure(405, "A replay has no pending approvals.")
		}
		var decide struct {
			Approval string `json:"approval"`
			Choice   *int   `json:"choice"`
			Reason   string `json:"reason"`
		}
		if err := decodeBody(body, &decide); err != nil {
			return nil, err
		}
		if decide.Approval == "" || decide.Choice == nil {
			return nil, rpcFailure(400, "An approval and choice are required.")
		}
		intent.Action, intent.Decide = DecideApproval, Decide{Approval: decide.Approval, Choice: *decide.Choice, Reason: decide.Reason}
	default:
		return nil, rpcFailure(404, "Unknown session method.")
	}
	return s.deliver(ctx, intent)
}

// answerWithin bounds how long a request waits for its owner.
const answerWithin = 5 * time.Second

func (s *Server) deliver(ctx context.Context, intent Intent) (any, *RPCError) {
	reply := make(chan string, 1)
	intent.Reply, intent.Context = reply, ctx
	timer := time.NewTimer(answerWithin)
	defer timer.Stop()
	select {
	case s.intents <- intent:
	case <-timer.C:
		return nil, rpcFailure(503, "The terminal session is busy; try again.")
	case <-ctx.Done():
		return nil, rpcFailure(503, "Request canceled before admission.")
	}
	select {
	case refusal := <-reply:
		if refusal != "" {
			return nil, rpcFailure(409, refusal)
		}
		if intent.Prepared != nil && intent.Prepared.Draft.AttachmentNotice != "" {
			return struct {
				Notice string `json:"notice"`
			}{intent.Prepared.Draft.AttachmentNotice}, nil
		}
		return nil, nil
	case <-ctx.Done():
		return nil, rpcUnknown("Admission outcome unknown; do not automatically retry.")
	}
}

// Serve runs handler on listener until ctx ends. Request contexts derive from
// ctx so open event streams end with the invocation.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	stopping := false
	tracked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if stopping {
			mu.Unlock()
			http.Error(w, "Server stopping.", 503)
			return
		}
		wg.Add(1)
		mu.Unlock()
		defer wg.Done()
		handler.ServeHTTP(w, r)
	})
	server := &http.Server{Handler: tracked, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 10 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	err := server.Serve(listener)
	mu.Lock()
	stopping = true
	mu.Unlock()
	cancel()
	_ = server.Close()
	wg.Wait() // http.Server.Close does not join upgraded WebSocket handlers.
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
