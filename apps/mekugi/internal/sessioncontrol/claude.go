package sessioncontrol

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// ClaudeRequester borrows the admitted native transport. Launch owns its lifetime
// and feeds Events to HandleEvent concurrently with request dispatch.
type ClaudeRequester interface {
	Request(context.Context, string, jsontext.Value) (jsontext.Value, error)
}

// ClaudeEvent contains native lifecycle facts for the application bridge. A
// started command confirms dispatch; only a native result confirms consumption
// and completes a turn.
type ClaudeEvent struct {
	Turn, Answer, TerminalReason string
	Started, Completed           bool
	Auxiliary                    bool
	Status                       control.Status
	Failure                      control.HostFailure
	Consumed                     []string
}

type claudeLedger struct {
	Version  int
	Info     control.ThreadInfo
	Settings control.Settings
	Model    string
	Pending  map[string]string // Native UUID to operation record, not transcript content.
	Retained []string          // Ordered source UUIDs awaiting native consumption.
	Metrics  *claudeMetrics    `json:",omitzero"`
}

type claudeReceipt struct {
	Version                                      int
	Thread, Session, ID, Digest                  string
	UUID, Turn                                   string
	Run, Accepted, Consumed, Cancelled           bool
	Input                                        *control.NativeInput `json:",omitzero"`
	Bundled                                      []string             `json:",omitzero"`
	Context                                      string               `json:",omitzero"`
	Terminal                                     *claudeTerminal      `json:",omitzero"`
	CompactResult, CompactBoundary, CompactError string
}

type claudeTerminal struct {
	Version                     int
	Thread, Session, Turn, UUID string
	Status                      control.Status
	Reason                      string
	Cancellation                string `json:",omitzero"`
}

type claudeInterrupt struct {
	Version               int
	Thread, Session, Turn string
	Accepted              bool
}

// ClaudeRuntime owns only request/receipt identities and unconsumed input. The
// SDK remains the sole transcript owner; logical work belongs to Thread Control.
type ClaudeRuntime struct {
	mu           sync.Mutex
	dispatch     sync.Mutex
	history      history.Session
	native       ClaudeRequester
	ledger       claudeLedger
	pending      map[string]*claudeReceipt
	changed      chan struct{}
	turn         string
	running      bool
	model        string
	answer       string
	active       map[string]bool
	closed       error
	inputContext func(context.Context, control.NativeInput) (string, error)
	eventSink    func(context.Context, ClaudeEvent) error
	lastTerminal *claudeTerminal
	admissions   int
	retiring     *ClaudeEvent
	metricResult string
}

var _ control.Runtime = (*ClaudeRuntime)(nil)

func claudeRecordName(thread, kind, id string) string {
	return fmt.Sprintf("threadcontrol-runtime-claude-%x-%s-%x.json", sha256.Sum256([]byte(thread)), kind, sha256.Sum256([]byte(id)))
}

func NewClaudeRuntime(ctx context.Context, selected history.Session, info control.ThreadInfo, settings control.Settings, native ClaudeRequester) (*ClaudeRuntime, error) {
	if selected.Store == nil || selected.Context == nil || selected.Thread == "" || selected.Thread != info.Thread || selected.Namespace == "" || info.Session == "" {
		return nil, errors.New("Claude runtime requires an admitted history session and exact native identity")
	}
	if len(settings.Preparation) == 0 || !settings.Preparation.IsValid() {
		return nil, errors.New("Claude runtime requires frozen preparation")
	}
	name := claudeRecordName(info.Thread, "session", info.Thread)
	fresh := false
	info.Path, info.Provider = filepath.Join(selected.Store.Directory(), name), "claude"
	r := &ClaudeRuntime{history: selected, native: native, changed: make(chan struct{}), pending: make(map[string]*claudeReceipt), active: make(map[string]bool)}
	err := selected.Store.Transaction(ctx, selected.Writes, func(tx *history.Transaction) error {
		admitted, err := selected.Store.ScopeInNamespace(info.Thread, selected.Namespace)
		if err != nil || !admitted {
			return errors.Join(err, errors.New("Claude runtime namespace is not admitted"))
		}
		data, err := history.ReadRecord(info.Path)
		if errors.Is(err, os.ErrNotExist) {
			fresh = true
			r.ledger = claudeLedger{Version: 1, Info: info, Settings: settings, Pending: make(map[string]string)}
			// Detach caller-owned slices and pointers, including preparation.
			data, err = json.Marshal(r.ledger)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(data, &r.ledger); err != nil {
				return err
			}
			return r.write(tx, name, r.ledger)
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &r.ledger, json.RejectUnknownMembers(true)); err != nil {
			return err
		}
		if r.ledger.Version != 1 || r.ledger.Pending == nil || !reflect.DeepEqual(r.ledger.Info, info) || !sameNativeJSON(r.ledger.Settings.Preparation, settings.Preparation) || r.ledger.Settings.Workspace != settings.Workspace {
			return errors.New("Claude runtime identity or frozen preparation differs from retained receipt")
		}
		for uuid, name := range r.ledger.Pending {
			var receipt claudeReceipt
			if err := r.read(name, &receipt); err != nil {
				return err
			}
			if receipt.UUID != uuid || receipt.Thread != info.Thread || receipt.Session != info.Session || name != claudeRecordName(info.Thread, "input", receipt.ID) || receipt.Version != 1 {
				return errors.New("invalid Claude pending operation identity")
			}
			r.pending[uuid] = &receipt
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.model = r.ledger.Model
	if r.ledger.Metrics == nil {
		r.ledger.Metrics = &claudeMetrics{BaselineSet: info.Fork == nil, Partial: !fresh, CompactionsKnown: fresh && info.Fork == nil}
	}
	// A retained observation never revives an active stopwatch across downtime.
	r.ledger.Metrics.Lifecycle.WorkTimer.Since = time.Time{}
	r.ledger.Metrics.Lifecycle.Waiting, r.ledger.Metrics.Lifecycle.Responding = false, false
	return r, nil
}

func (r *ClaudeRuntime) read(name string, value any) error {
	return readClaudeRecord(filepath.Dir(r.ledger.Info.Path), name, value)
}

func readClaudeRecord(directory, name string, value any) error {
	if filepath.Base(name) != name {
		return errors.New("invalid Claude ledger record name")
	}
	data, err := history.ReadRecord(filepath.Join(directory, name))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value, json.RejectUnknownMembers(true))
}

func (r *ClaudeRuntime) write(tx *history.Transaction, name string, value any) error {
	data, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		return err
	}
	if err := tx.Admit(r.history.Limits, name, int64(len(data))); err != nil {
		return err
	}
	if err := tx.Write(name, "threadcontrol-runtime-", data); err != nil {
		return err
	}
	return tx.Retain(r.history.Thread, r.history.Limits, name)
}

func (r *ClaudeRuntime) saveLedger(tx *history.Transaction) error {
	return r.write(tx, filepath.Base(r.ledger.Info.Path), r.ledger)
}

func (r *ClaudeRuntime) identity(thread string) error {
	if thread != r.ledger.Info.Thread {
		return errors.New("Claude runtime thread identity mismatch")
	}
	return nil
}

func (r *ClaudeRuntime) Thread(_ context.Context, thread string) (control.ThreadInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := r.ledger.Info
	if info.Parent != nil {
		info.Parent = new(*info.Parent)
	}
	if info.Fork != nil {
		info.Fork = new(*info.Fork)
	}
	return info, r.identity(thread)
}

func (r *ClaudeRuntime) Configuration(_ context.Context, thread, workspace string) (jsontext.Value, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.identity(thread); err != nil {
		return nil, err
	}
	if workspace != r.ledger.Settings.Workspace {
		return nil, errors.New("Claude configuration workspace mismatch")
	}
	return append(jsontext.Value(nil), r.ledger.Settings.Preparation...), nil
}

func (r *ClaudeRuntime) Inspect(_ context.Context, thread string) (control.SessionDetails, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var details control.SessionDetails
	if r.model != "" {
		details = r.ledger.Metrics.inspection()
		details.Model = new(r.model)
	}
	return details, r.identity(thread)
}

func (r *ClaudeRuntime) CurrentTurn() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turn
}

// SetInputContext installs launch's initial-context acquisition before admission.
// The selected body is retained with the unchanged operation identity before IPC.
func (r *ClaudeRuntime) SetInputContext(acquire func(context.Context, control.NativeInput) (string, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputContext = acquire
}

// SetEventSink installs the same lifecycle bridge used for HandleEvent results.
// Interrupt receipts can settle work after its native result already arrived.
// Install it before admission; terminal events then await RetireCompleted.
func (r *ClaudeRuntime) SetEventSink(sink func(context.Context, ClaudeEvent) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventSink = sink
}

// AdmitControl keeps logical admission on the same side of native retirement as
// its dispatch. An already-admitted input may still need a native started event,
// so terminal observation never waits for these callers on the event reader.
func (r *ClaudeRuntime) AdmitControl(ctx context.Context) (func() error, error) {
	for {
		r.mu.Lock()
		if r.closed != nil || r.eventSink == nil {
			err := r.closed
			if err == nil {
				err = errors.New("Claude control admission requires an application lifecycle sink")
			}
			r.mu.Unlock()
			return nil, err
		}
		if r.retiring == nil {
			r.admissions++
			r.mu.Unlock()
			return r.releaseControl, nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.history.Context.Done():
			return nil, r.history.Context.Err()
		}
	}
}

func (r *ClaudeRuntime) releaseControl() error {
	r.mu.Lock()
	r.admissions--
	if r.admissions != 0 || r.retiring == nil {
		r.mu.Unlock()
		return nil
	}
	event, terminal, sink := *r.retiring, *r.lastTerminal, r.eventSink
	err := r.history.Store.Transaction(r.history.Context, r.history.Writes, func(tx *history.Transaction) error {
		return r.write(tx, claudeRecordName(terminal.Thread, "terminal", terminal.Turn), terminal)
	})
	r.mu.Unlock()
	if err == nil {
		err = sink(r.history.Context, event)
	}
	if err != nil {
		r.TransportClosed(err)
	}
	return err
}

// RetireCompleted acknowledges logical terminal reduction before the lifecycle
// owner dispatches a released follow-up or wake. Failed reduction closes admission.
func (r *ClaudeRuntime) RetireCompleted(turn string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retiring == nil || r.retiring.Turn != turn {
		return
	}
	if err != nil {
		r.closed = err
	} else {
		r.turn, r.running, r.answer, r.lastTerminal, r.retiring = "", false, "", nil, nil
		clear(r.active)
	}
	close(r.changed)
	r.changed = make(chan struct{})
}

// TransportClosed releases acceptance waiters without changing retained facts or
// claiming that interrupted transport completed the native generation.
func (r *ClaudeRuntime) TransportClosed(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed == nil {
		if err == nil {
			err = errors.New("Claude native transport closed")
		}
		r.closed = err
		close(r.changed)
		r.changed = make(chan struct{})
	}
}

func claudeInputDigest(input control.NativeInput) (string, error) {
	data, err := json.Marshal(input, json.Deterministic(true))
	return fmt.Sprintf("%x", sha256.Sum256(data)), err
}

func claudeDispatchError(outcome binding.DispatchOutcome, method string, err error) error {
	return &binding.DispatchError{Outcome: outcome, Method: method, Cause: err}
}

func (r *ClaudeRuntime) Input(ctx context.Context, input control.NativeInput) (string, error) {
	receipt, err := r.dispatchInput(ctx, input)
	if err != nil {
		return "", err
	}
	// Queued is not native acceptance. An exact native started receipt releases
	// this wait; payloads stay retained until the result confirms consumption.
	for {
		r.mu.Lock()
		accepted, cancelled, turn, changed, closed := receipt.Accepted, receipt.Cancelled, receipt.Turn, r.changed, r.closed
		terminal, settled := receipt.Terminal, receipt.Consumed && r.turn != receipt.Turn
		r.mu.Unlock()
		if cancelled {
			return "", claudeDispatchError(binding.HostRejected, "input", errors.New("Claude input was cancelled before native consumption"))
		}
		if input.Kind == "COMPACT" && settled && terminal != nil {
			if terminal.Status != control.Completed {
				return "", claudeDispatchError(binding.HostRejected, "input", errors.New(terminal.Reason))
			}
			return "", nil
		}
		if accepted && input.Kind != "COMPACT" {
			return turn, nil
		}
		if closed != nil {
			return "", claudeDispatchError(binding.Uncertain, "input", closed)
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return "", claudeDispatchError(binding.Uncertain, "input", ctx.Err())
		case <-r.history.Context.Done():
			return "", claudeDispatchError(binding.Uncertain, "input", r.history.Context.Err())
		}
	}
}

func (r *ClaudeRuntime) dispatchInput(ctx context.Context, input control.NativeInput) (*claudeReceipt, error) {
	r.dispatch.Lock()
	defer r.dispatch.Unlock()
	r.mu.Lock()
	acquire := r.inputContext
	valid := r.identity(input.Thread) == nil && input.ID != "" && r.closed == nil
	r.mu.Unlock()
	var inputContext string
	if valid && acquire != nil && (input.Run && input.Settings != nil || input.Kind == "FINAL_ANSWER" && input.Outcome == control.Completed) {
		var previous claudeReceipt
		if err := r.read(claudeRecordName(input.Thread, "input", input.ID), &previous); errors.Is(err, os.ErrNotExist) {
			var err error
			inputContext, err = acquire(ctx, input)
			if err != nil {
				return nil, claudeDispatchError(binding.NotDispatched, "input", err)
			}
		}
	}
	receipt, fresh, err := r.retainInput(ctx, input, inputContext)
	if err != nil {
		return nil, claudeDispatchError(binding.NotDispatched, "input", err)
	}
	if receipt.Accepted || receipt.Cancelled {
		return receipt, nil
	}
	if !fresh {
		return nil, claudeDispatchError(binding.Uncertain, "input", errors.New("Claude input already dispatched; exact acceptance requires reconciliation"))
	}
	var fragments []string
	r.mu.Lock()
	for _, uuid := range receipt.Bundled {
		pending := r.pending[uuid]
		if pending == nil || pending.Input == nil {
			r.mu.Unlock()
			return nil, claudeDispatchError(binding.NotDispatched, "input", errors.New("Claude retained input payload is unavailable"))
		}
		text, err := claudeInputText(*pending.Input)
		if err != nil {
			r.mu.Unlock()
			return nil, claudeDispatchError(binding.NotDispatched, "input", err)
		}
		fragments = append(fragments, text)
		if pending.Context != "" {
			fragments = append(fragments, pending.Context)
		}
	}
	if receipt.Context != "" {
		fragments = append(fragments, receipt.Context)
	}
	text, err := claudeInputText(*receipt.Input)
	r.mu.Unlock()
	if err != nil {
		return nil, claudeDispatchError(binding.NotDispatched, "input", err)
	}
	fragments = append(fragments, text)
	message, err := json.Marshal(map[string]any{"role": "user", "content": strings.Join(fragments, "\n\n")})
	if err != nil {
		return nil, claudeDispatchError(binding.NotDispatched, "input", err)
	}
	origin, _ := json.Marshal(map[string]string{"kind": "peer", "from": control.AgentName(input.Sender)})
	if input.Wake != nil || input.Kind == "COMPLETION_GUARD" || input.Kind == "COMPACT" || input.Sender == "" {
		origin = jsontext.Value(`{"kind":"coordinator"}`)
	}
	priority := ""
	if receipt.Turn != receipt.UUID {
		priority = "now"
	}
	params, err := json.Marshal(claude.Input{Type: "user", UUID: receipt.UUID, SessionID: receipt.Session, Message: message, IsSynthetic: true, Origin: origin, Priority: priority})
	if err != nil {
		return nil, claudeDispatchError(binding.NotDispatched, "input", err)
	}
	if _, err = r.native.Request(ctx, "input", params); err != nil {
		r.mu.Lock()
		accepted := receipt.Accepted
		r.mu.Unlock()
		if !accepted {
			return nil, claudeDispatchError(binding.Uncertain, "input", err)
		}
	}
	return receipt, nil
}

func claudeInputText(input control.NativeInput) (string, error) {
	if input.Kind == "COMPACT" {
		return "/compact", nil
	}
	projected := input
	if input.Kind == "FINAL_ANSWER" {
		projected.Message = compact.ReloadText(input.Message)
	}
	projected.Sender, projected.Recipient = control.AgentName(input.Sender), control.AgentName(input.Recipient)
	if input.Kind == "COMPLETION_GUARD" {
		const prefix, suffix = "Active descendants: ", ". Call wait_agent before completing."
		if names, found := strings.CutPrefix(input.Message, prefix); found {
			if names, found := strings.CutSuffix(names, suffix); found {
				parts := strings.Split(names, ", ")
				for i, task := range parts {
					parts[i] = control.AgentName(task)
				}
				projected.Message = prefix + strings.Join(parts, ", ") + suffix
			}
		}
	}
	_, tool, output := nativeContent(projected)
	if input.Wake != nil {
		wake := *input.Wake
		wake.Targets = make([]string, len(input.Wake.Targets))
		for i, target := range input.Wake.Targets {
			wake.Targets[i] = control.AgentName(target)
		}
		wake.Metrics = make(map[string]control.SessionMetrics, len(input.Wake.Metrics))
		for task, metrics := range input.Wake.Metrics {
			wake.Metrics[control.AgentName(task)] = metrics
		}
		wake.Events = slices.Clone(wake.Events)
		for i := range wake.Events {
			wake.Events[i].Task = control.AgentName(wake.Events[i].Task)
		}
		if wake.Event != nil {
			wake.Event = new(*wake.Event)
			wake.Event.Task = control.AgentName(wake.Event.Task)
		}
		// Native UUID already binds this delivery. Host-only dispatch identities
		// remain in the receipt, not the provider-visible continuation text.
		raw, marshalErr := json.Marshal(wake)
		if marshalErr != nil {
			return "", marshalErr
		}
		output = string(raw)
	}
	// These are attributed text envelopes, never invented native tool_result blocks.
	return "Thread Control " + tool + "\nSender: " + projected.Sender + "\n" + output, nil
}

func (r *ClaudeRuntime) retainInput(ctx context.Context, input control.NativeInput, inputContext string) (*claudeReceipt, bool, error) {
	r.mu.Lock()
	for input.Run && r.retiring != nil && r.admissions == 0 && r.closed == nil {
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-r.history.Context.Done():
			return nil, false, r.history.Context.Err()
		}
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if err := r.identity(input.Thread); err != nil {
		return nil, false, err
	}
	if input.ID == "" || input.Kind == "HUMAN_INPUT" || input.Run && r.native == nil {
		return nil, false, errors.New("Claude input requires an attributed operation and live native transport")
	}
	if input.Run && r.closed != nil {
		return nil, false, r.closed
	}
	if input.Settings != nil && !sameNativeJSON(input.Settings.Preparation, r.ledger.Settings.Preparation) {
		return nil, false, errors.New("Claude input preparation differs from the admitted runtime")
	}
	digest, err := claudeInputDigest(input)
	if err != nil {
		return nil, false, err
	}
	name := claudeRecordName(input.Thread, "input", input.ID)
	var old claudeReceipt
	if err := r.read(name, &old); err == nil {
		if old.Digest != digest || old.Thread != input.Thread || old.Session != r.ledger.Info.Session || old.ID != input.ID || old.Version != 1 {
			return nil, false, errors.New("Claude operation identity was reused with different input")
		}
		return &old, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if input.Kind == "COMPACT" && r.turn != "" {
		return nil, false, errors.New("Claude compaction requires settled host work")
	}
	if input.Run && r.turn != "" && !r.running {
		return nil, false, errors.New("Claude has an unconfirmed native start")
	}
	if input.Run {
		for _, pending := range r.pending {
			if pending.Run && !pending.Accepted && !pending.Cancelled {
				return nil, false, errors.New("Claude native input requires reconciliation before further work")
			}
		}
	}
	uuid := make([]byte, 16)
	if _, err := rand.Read(uuid); err != nil {
		return nil, false, err
	}
	uuid[6], uuid[8] = uuid[6]&15|64, uuid[8]&63|128
	receipt := &claudeReceipt{Version: 1, Thread: input.Thread, Session: r.ledger.Info.Session, ID: input.ID, Digest: digest, UUID: fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), Run: input.Run, Accepted: !input.Run, Input: &input}
	receipt.Context = inputContext
	if input.Run {
		reserved := make(map[string]bool)
		for _, pending := range r.pending {
			if pending.Run && !pending.Cancelled {
				for _, uuid := range pending.Bundled {
					reserved[uuid] = true
				}
			}
		}
		for _, uuid := range r.ledger.Retained {
			if input.Kind != "COMPACT" && !reserved[uuid] {
				receipt.Bundled = append(receipt.Bundled, uuid)
			}
		}
		receipt.Turn = r.turn
		if receipt.Turn == "" {
			receipt.Turn = receipt.UUID
		}
	}
	frozen, err := json.Marshal(receipt)
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(frozen, &receipt); err != nil {
		return nil, false, err
	}
	retained := r.ledger.Retained
	err = r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error {
		if err := r.write(tx, name, receipt); err != nil {
			return err
		}
		r.ledger.Pending[receipt.UUID] = name
		if !input.Run {
			r.ledger.Retained = append(append([]string(nil), retained...), receipt.UUID)
		}
		return r.saveLedger(tx)
	})
	if err != nil {
		delete(r.ledger.Pending, receipt.UUID)
		r.ledger.Retained = retained
		return nil, false, err
	}
	r.pending[receipt.UUID] = receipt
	if input.Run && r.retiring != nil {
		// Admission preceded this carrier's terminal. The new input still owns
		// the same logical turn, just like priority steering observed earlier.
		r.retiring, r.answer = nil, ""
		close(r.changed)
		r.changed = make(chan struct{})
	}
	if input.Run && r.turn == "" {
		r.turn = receipt.Turn
	}
	return receipt, true, nil
}

func (r *ClaudeRuntime) Interrupt(ctx context.Context, thread, turn string) error {
	events, err := r.dispatchInterrupt(ctx, thread, turn)
	if err != nil {
		return err
	}
	r.mu.Lock()
	sink := r.eventSink
	r.mu.Unlock()
	for _, event := range events {
		if sink == nil {
			return claudeDispatchError(binding.Uncertain, "interrupt", errors.New("Claude interrupt completion has no application lifecycle sink"))
		}
		if err := sink(ctx, event); err != nil {
			return claudeDispatchError(binding.Uncertain, "interrupt", err)
		}
	}
	return nil
}

func (r *ClaudeRuntime) dispatchInterrupt(ctx context.Context, thread, turn string) ([]ClaudeEvent, error) {
	r.dispatch.Lock()
	defer r.dispatch.Unlock()
	r.mu.Lock()
	if err := r.identity(thread); err != nil {
		r.mu.Unlock()
		return nil, claudeDispatchError(binding.NotDispatched, "interrupt", err)
	}
	if turn == "" || r.turn != turn || r.native == nil {
		r.mu.Unlock()
		return nil, claudeDispatchError(binding.NotDispatched, "interrupt", errors.New("Claude interruption requires the exact active native turn"))
	}
	name := claudeRecordName(thread, "interrupt", turn)
	var intent claudeInterrupt
	if err := r.read(name, &intent); err == nil {
		r.mu.Unlock()
		if intent.Accepted {
			return nil, nil
		}
		return nil, claudeDispatchError(binding.Uncertain, "interrupt", errors.New("Claude interruption already dispatched; terminal receipt required"))
	} else if !errors.Is(err, os.ErrNotExist) {
		r.mu.Unlock()
		return nil, err
	}
	intent = claudeInterrupt{Version: 1, Thread: thread, Session: r.ledger.Info.Session, Turn: turn}
	err := r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error { return r.write(tx, name, intent) })
	r.mu.Unlock()
	if err != nil {
		return nil, claudeDispatchError(binding.NotDispatched, "interrupt", err)
	}
	raw, err := r.native.Request(ctx, "interrupt", jsontext.Value(`{}`))
	if err != nil {
		return nil, claudeDispatchError(binding.Uncertain, "interrupt", err)
	}
	var receipt struct {
		Cancelled       []string `json:"cancelled"`
		StillQueued     []string `json:"still_queued"`
		WorkerCancelled []string `json:"worker_cancelled"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, claudeDispatchError(binding.Uncertain, "interrupt", err)
	}
	if len(receipt.StillQueued) != 0 {
		return nil, claudeDispatchError(binding.Uncertain, "interrupt", errors.New("Claude native interruption left queued commands runnable"))
	}
	var events []ClaudeEvent
	r.mu.Lock()
	for _, source := range []struct {
		prefix string
		uuids  []string
	}{{"native_interrupt:", receipt.Cancelled}, {"worker_interrupt:", receipt.WorkerCancelled}} {
		for _, uuid := range source.uuids {
			event, err := r.cancelled(ctx, uuid, source.prefix+turn, "")
			if err != nil {
				r.mu.Unlock()
				return nil, claudeDispatchError(binding.Uncertain, "interrupt", err)
			}
			if event.Completed {
				events = append(events, event)
			}
		}
	}
	r.mu.Unlock()
	intent.Accepted = true
	err = r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error { return r.write(tx, name, intent) })
	if err != nil {
		return nil, claudeDispatchError(binding.Uncertain, "interrupt", err)
	}
	return events, nil
}

func (r *ClaudeRuntime) Confirm(ctx context.Context, input control.NativeInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.confirm(input)
}

var errClaudeAcceptance = errors.New("Claude input acceptance is uncertain; exact native started or consumption receipt has not been observed")

func (r *ClaudeRuntime) confirm(input control.NativeInput) error {
	return confirmClaudeReceipt(r.ledger, input)
}

func confirmClaudeReceipt(ledger claudeLedger, input control.NativeInput) error {
	if input.Thread != ledger.Info.Thread {
		return errors.New("Claude runtime thread identity mismatch")
	}
	directory := filepath.Dir(ledger.Info.Path)
	if input.Kind == "INTERRUPT" {
		var terminal claudeTerminal
		if err := readClaudeRecord(directory, claudeRecordName(input.Thread, "terminal", input.TargetTurn), &terminal); err != nil {
			return err
		}
		if terminal.Version != 1 || terminal.Thread != input.Thread || terminal.Session != ledger.Info.Session || terminal.Turn != input.TargetTurn || terminal.UUID == "" && terminal.Cancellation == "" || terminal.Status != control.Completed && terminal.Status != control.Interrupted && terminal.Status != control.Failed {
			return errors.New("Claude terminal receipt identity mismatch")
		}
		return nil
	}
	var receipt claudeReceipt
	if err := readClaudeRecord(directory, claudeRecordName(input.Thread, "input", input.ID), &receipt); err != nil {
		return err
	}
	digest, err := claudeInputDigest(input)
	if err != nil {
		return err
	}
	if receipt.Version != 1 || receipt.Thread != input.Thread || receipt.Session != ledger.Info.Session || receipt.ID != input.ID || receipt.Digest != digest {
		return errors.New("Claude input receipt identity mismatch")
	}
	if input.Kind == "COMPACT" {
		if !receipt.Consumed || receipt.Terminal == nil || receipt.Terminal.Status != control.Completed || receipt.CompactResult != "success" || receipt.CompactBoundary == "" {
			return errors.New("Claude compaction has no confirmed successful settlement")
		}
	}
	if !receipt.Accepted {
		if receipt.Cancelled {
			return errors.New("Claude input was cancelled before native acceptance")
		}
		return errClaudeAcceptance
	}
	return nil
}

// HandleEvent processes a native SDK event once, without querying or scanning
// the transcript. Unknown UUIDs cannot confirm an application operation.
func (r *ClaudeRuntime) HandleEvent(ctx context.Context, raw jsontext.Value) (ClaudeEvent, error) {
	var event struct {
		claude.Event
		CommandUUID     string   `json:"command_uuid"`
		State           string   `json:"state"`
		Model           string   `json:"model"`
		Result          string   `json:"result"`
		IsError         bool     `json:"is_error"`
		Errors          []string `json:"errors"`
		UserUUID        string   `json:"user_message_uuid"`
		UserUUIDs       []string `json:"user_message_uuids"`
		APIStatus       int      `json:"api_error_status"`
		CompactResult   string   `json:"compact_result"`
		CompactError    string   `json:"compact_error"`
		CompactMetadata struct {
			Trigger string `json:"trigger"`
		} `json:"compact_metadata"`
	}
	var result ClaudeEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return result, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if event.SessionID != "" && event.SessionID != r.ledger.Info.Session {
		return result, errors.New("Claude event native session identity mismatch")
	}
	if event.Type == "system" && event.Subtype == "init" {
		if event.SessionID == "" {
			return result, errors.New("Claude initialization omitted its native session identity")
		}
		if r.ledger.Model != event.Model {
			updated := r.ledger
			updated.Model = event.Model
			if err := r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error {
				return r.write(tx, filepath.Base(updated.Info.Path), updated)
			}); err != nil {
				return result, err
			}
			r.ledger = updated
		}
		r.model = event.Model
		return result, nil
	}
	if event.Type == "command_lifecycle" && event.State == "started" {
		if event.SessionID == "" {
			return result, errors.New("Claude command start omitted its native session identity")
		}
		return r.started(ctx, event.CommandUUID)
	}
	if event.Type == "command_lifecycle" && event.State == "cancelled" {
		if event.SessionID == "" || event.UUID == "" {
			return result, errors.New("Claude command cancellation omitted its native identity")
		}
		// Active commands emit their result before cancelled. A queued command
		// has no result; the exact lifecycle closes that dispatch instead.
		if pending := r.pending[event.CommandUUID]; pending != nil && !pending.Accepted {
			return r.cancelled(ctx, event.CommandUUID, "command_lifecycle:"+event.UUID, event.UUID)
		}
		return result, nil
	}
	if event.Type == "system" && r.running && event.SessionID != "" {
		for uuid := range r.active {
			receipt := r.pending[uuid]
			if receipt == nil || receipt.Input == nil || receipt.Input.Kind != "COMPACT" {
				continue
			}
			if event.Subtype == "status" && event.CompactResult != "" && receipt.CompactResult != "failed" {
				receipt.CompactResult, receipt.CompactError = event.CompactResult, event.CompactError
			}
			if event.Subtype == "compact_boundary" && event.CompactMetadata.Trigger == "manual" && event.UUID != "" {
				receipt.CompactBoundary = event.UUID
			}
		}
	}
	if event.Type == "assistant" && r.running {
		var message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(event.Message, &message); err != nil {
			return result, err
		}
		var answer strings.Builder
		for _, block := range message.Content {
			if block.Type == "text" {
				answer.WriteString(block.Text)
			}
		}
		if answer.Len() != 0 {
			r.answer = answer.String()
		}
		r.ledger.Metrics.Lifecycle.Response(time.Now())
		return result, nil
	}
	if event.Type != "result" || r.turn == "" || r.retiring != nil || event.SessionID == "" || event.UUID == "" {
		return result, nil
	}
	uuids := append([]string(nil), event.UserUUIDs...)
	if event.UserUUID != "" && !slices.Contains(uuids, event.UserUUID) {
		uuids = append(uuids, event.UserUUID)
	}
	matched := false
	for _, uuid := range uuids {
		if pending := r.pending[uuid]; pending != nil && pending.Run && pending.Turn == r.turn {
			started, err := r.started(ctx, uuid)
			if err != nil {
				return result, err
			}
			result.Started = result.Started || started.Started
		}
		matched = matched || r.active[uuid]
		if receipt := r.pending[uuid]; receipt != nil && receipt.Input != nil && receipt.Input.Kind == "COMPACT" {
			result.Auxiliary = true
		}
	}
	if !matched {
		return result, errors.New("Claude result has no accepted input belonging to the active turn")
	}
	r.metricResult = event.UUID
	result.Turn, result.Completed, result.TerminalReason = r.turn, true, event.TerminalReason
	result.Status = control.Completed
	if event.TerminalReason == "aborted_streaming" || event.TerminalReason == "aborted_tools" {
		result.Status = control.Interrupted
	} else if event.TerminalReason != "hook_stopped" && (event.IsError || event.Subtype != "success" || event.TerminalReason != "" && event.TerminalReason != "completed") {
		result.Status = control.Failed
		result.Failure.Recoverable = event.APIStatus == 429 || event.APIStatus == 500 || event.APIStatus == 503 || event.APIStatus == 529
		result.Failure.Message = strings.Join(event.Errors, "\n")
		if result.Failure.Message == "" {
			result.Failure.Message = event.Result
		}
		if result.Failure.Message == "" {
			result.Failure.Message = event.Subtype + ": " + event.TerminalReason
		}
	}
	if result.Auxiliary {
		for _, uuid := range uuids {
			receipt := r.pending[uuid]
			if receipt == nil || receipt.Input == nil || receipt.Input.Kind != "COMPACT" {
				continue
			}
			if receipt.CompactResult != "success" || receipt.CompactBoundary == "" || result.Status != control.Completed {
				result.Status = control.Failed
				result.Failure = control.HostFailure{Message: receipt.CompactError}
				if result.Failure.Message == "" {
					result.Failure.Message = "Claude compaction did not confirm success, manual boundary and terminal settlement"
				}
				result.TerminalReason = result.Failure.Message
			}
		}
	}
	if result.Status == control.Completed && !result.Auxiliary {
		result.Answer = event.Result
		if result.Answer == "" && event.TerminalReason != "hook_stopped" {
			result.Answer = r.answer
		}
	}
	terminal := claudeTerminal{Version: 1, Thread: r.ledger.Info.Thread, Session: r.ledger.Info.Session, Turn: r.turn, UUID: event.UUID, Status: result.Status, Reason: result.TerminalReason}
	continuing := false
	for uuid, pending := range r.pending {
		if pending.Run && pending.Turn == r.turn && !slices.Contains(uuids, uuid) {
			continuing = true
			break
		}
	}
	if !continuing && r.admissions == 0 {
		if err := r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error {
			return r.write(tx, claudeRecordName(terminal.Thread, "terminal", terminal.Turn), terminal)
		}); err != nil {
			return ClaudeEvent{}, err
		}
	}
	for _, uuid := range uuids {
		consumed, err := r.consume(ctx, uuid, terminal)
		if err != nil {
			return ClaudeEvent{}, err
		}
		result.Consumed = append(result.Consumed, consumed...)
		delete(r.active, uuid)
	}
	// Native priority-now steering aborts the preceding carrier before the
	// followup's started event. Its terminal is evidence for that carrier only;
	// the admitted followup still owns this application turn.
	if continuing {
		result.Completed, result.Answer = false, ""
		r.answer = ""
		r.lastTerminal = &terminal
		return result, nil
	}
	if !result.Auxiliary {
		r.ledger.Metrics.Lifecycle.TurnCompleted(result.Status == control.Completed, time.Now())
	}
	if r.eventSink != nil {
		r.retiring, r.lastTerminal = new(result), &terminal
		r.retiring.Started, r.retiring.Consumed = false, nil
		if r.admissions != 0 {
			result.Completed, result.Answer = false, ""
		}
	} else {
		r.turn, r.running, r.answer = "", false, ""
		r.lastTerminal = nil
		clear(r.active)
	}
	return result, nil
}

func (r *ClaudeRuntime) started(ctx context.Context, uuid string) (ClaudeEvent, error) {
	var result ClaudeEvent
	receipt := r.pending[uuid]
	if receipt == nil || !receipt.Run {
		return result, nil
	}
	accepted := *receipt
	accepted.Accepted = true
	if r.running && receipt.Accepted && receipt.Turn != r.turn {
		return result, errors.New("Claude command start conflicts with the active native turn")
	}
	accepted.Turn = r.turn
	if accepted.Turn == "" {
		accepted.Turn = accepted.UUID
	}
	if !receipt.Accepted || receipt.Turn != accepted.Turn {
		if err := r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error {
			return r.write(tx, r.ledger.Pending[uuid], accepted)
		}); err != nil {
			return result, err
		}
		*receipt = accepted
	}
	result.Turn, result.Started = accepted.Turn, !r.running
	result.Auxiliary = receipt.Input != nil && receipt.Input.Kind == "COMPACT"
	if result.Started && !result.Auxiliary {
		r.ledger.Metrics.Lifecycle.TurnStarted(time.Now())
	}
	r.turn, r.running = accepted.Turn, true
	r.active[uuid] = true
	close(r.changed)
	r.changed = make(chan struct{})
	return result, nil
}

func (r *ClaudeRuntime) consume(ctx context.Context, uuid string, terminal claudeTerminal) ([]string, error) {
	receipt := r.pending[uuid]
	if receipt == nil || !receipt.Run {
		return nil, nil
	}
	accepted := *receipt
	accepted.Accepted, accepted.Consumed, accepted.Input, accepted.Context = true, true, nil, ""
	accepted.Terminal = &terminal
	name := r.ledger.Pending[uuid]
	retained := r.ledger.Retained
	cleared := make(map[string]claudeReceipt, len(accepted.Bundled))
	names := map[string]string{uuid: name}
	var consumed []string
	for _, bundled := range accepted.Bundled {
		pending := r.pending[bundled]
		if pending == nil {
			return nil, errors.New("Claude carrier has an unavailable retained input receipt")
		}
		copy := *pending
		copy.Input, copy.Context, copy.Consumed = nil, "", true
		cleared[bundled], names[bundled] = copy, r.ledger.Pending[bundled]
		consumed = append(consumed, control.MessageItemID(copy.ID))
	}
	err := r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error {
		if err := r.write(tx, name, accepted); err != nil {
			return err
		}
		for bundled, receipt := range cleared {
			if err := r.write(tx, names[bundled], receipt); err != nil {
				return err
			}
		}
		for uuid := range names {
			delete(r.ledger.Pending, uuid)
		}
		r.ledger.Retained = slices.DeleteFunc(slices.Clone(retained), func(uuid string) bool { return names[uuid] != "" })
		return r.saveLedger(tx)
	})
	if err != nil {
		maps.Copy(r.ledger.Pending, names)
		r.ledger.Retained = retained
		return nil, err
	}
	*receipt = accepted
	delete(r.pending, uuid)
	for uuid, receipt := range cleared {
		*r.pending[uuid] = receipt
		delete(r.pending, uuid)
	}
	close(r.changed)
	r.changed = make(chan struct{})
	return consumed, nil
}

// cancelled records only observed native/worker queue removal. Bundled retained
// messages return to host retention; cancellation never dispatches them.
func (r *ClaudeRuntime) cancelled(ctx context.Context, uuid, evidence, nativeUUID string) (ClaudeEvent, error) {
	var event ClaudeEvent
	receipt := r.pending[uuid]
	if receipt == nil || !receipt.Run {
		return event, nil
	}
	terminal := claudeTerminal{Version: 1, Thread: receipt.Thread, Session: receipt.Session, Turn: receipt.Turn, UUID: nativeUUID, Status: control.Interrupted, Reason: "cancelled", Cancellation: evidence}
	if terminal.UUID == "" && r.lastTerminal != nil && r.lastTerminal.Turn == receipt.Turn {
		terminal.UUID = r.lastTerminal.UUID
	}
	cancelled := *receipt
	cancelled.Cancelled, cancelled.Input, cancelled.Context, cancelled.Terminal = true, nil, "", &terminal
	ending := r.turn == receipt.Turn
	for pendingUUID, pending := range r.pending {
		if pendingUUID != uuid && pending.Run && pending.Turn == r.turn {
			ending = false
			break
		}
	}
	name := r.ledger.Pending[uuid]
	err := r.history.Store.Transaction(ctx, r.history.Writes, func(tx *history.Transaction) error {
		if err := r.write(tx, name, cancelled); err != nil {
			return err
		}
		if ending && r.admissions == 0 {
			if err := r.write(tx, claudeRecordName(terminal.Thread, "terminal", terminal.Turn), terminal); err != nil {
				return err
			}
		}
		delete(r.ledger.Pending, uuid)
		return r.saveLedger(tx)
	})
	if err != nil {
		r.ledger.Pending[uuid] = name
		return event, err
	}
	*receipt = cancelled
	delete(r.pending, uuid)
	delete(r.active, uuid)
	if ending {
		event = ClaudeEvent{Turn: r.turn, Completed: true, Status: control.Interrupted, TerminalReason: "cancelled"}
		if r.eventSink != nil {
			r.retiring, r.lastTerminal = new(event), &terminal
			if r.admissions != 0 {
				event.Completed = false
			}
		} else {
			r.turn, r.running, r.answer, r.lastTerminal = "", false, "", nil
			clear(r.active)
		}
	}
	close(r.changed)
	r.changed = make(chan struct{})
	return event, nil
}
