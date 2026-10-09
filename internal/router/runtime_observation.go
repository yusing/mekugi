package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"

	"github.com/gofrs/flock"
)

// ObservationBinding comes only from the launcher's authenticated native adapter,
// never from model arguments. Branch is empty unless the host proves one.
type ObservationBinding struct {
	Runtime   string `json:"runtime"`
	Session   string `json:"session"`
	Branch    string `json:"branch,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Workspace string `json:"workspace"`
}

// ObservationCall retains native identity and input separately from the decoded
// read-only scope. Shell must be host-verified; unknown shells have no literal scope.
type ObservationCall struct {
	Binding ObservationBinding `json:"binding"`
	ID      string             `json:"id"`
	Tool    string             `json:"tool"`
	Input   string             `json:"input"`
	Paths   []string           `json:"paths,omitempty"`
	Command string             `json:"command,omitempty"`
	Shell   string             `json:"shell,omitempty"`
	Workdir string             `json:"workdir,omitempty"`
}

// Hooks can serialize the same native JSON object in different member orders.
// Compare those objects, not serialization order. Retained evidence stays exact;
// numbers are not converted to floats or canonicalized with a loss of precision.
func sameObservationCall(a, b *ObservationCall) bool {
	if a == nil || b == nil {
		return a == b
	}
	left, right := *a, *b
	left.Input, right.Input = "", ""
	if !reflect.DeepEqual(left, right) {
		return false
	}
	if a.Input == b.Input {
		return true
	}
	x, y := jsontext.Value(a.Input), jsontext.Value(b.Input)
	if x.Format(jsontext.ReorderRawObjects(true)) != nil || y.Format(jsontext.ReorderRawObjects(true)) != nil {
		return false
	}
	return bytes.Equal(x, y)
}

type ObservationTerminal struct {
	Task        string `json:"task,omitempty"`
	Status      string `json:"status"`
	Report      string `json:"report,omitempty"`
	Interrupted bool   `json:"interrupted,omitzero"`
}

// nativeObservationOwner is a runtime-neutral capture adapter, not an executor.
// Pending baselines, background correlation and completed reviews use the same
// retained records, change allocator, capture order and publication owner as Codex.
type nativeObservationOwner struct {
	pendingCount                atomic.Int32
	mu                          sync.Mutex
	store                       *mekugiReplayStore
	workspace, runtime, session string
	bindings                    map[ObservationBinding]context.Context
	release                     []func()
	windows                     *execWindowRegistry
	live                        map[string]bool
	pending                     map[string]mekugiHistory // Frozen completion retries never reread the disk.
	tasks                       map[string]string
	taskEvents                  map[string]observationTask
	broker                      *liveDiffBroker
	execTrack                   *execTrackHub
}

func newNativeObservationOwner(ctx context.Context, store *mekugiReplayStore, runtime, workspace string) (*nativeObservationOwner, error) {
	if store == nil || runtime == "" || !filepath.IsAbs(workspace) {
		return nil, errors.New("observation requires a runtime, store and absolute workspace")
	}
	o := &nativeObservationOwner{store: store, runtime: runtime, workspace: filepath.Clean(workspace), bindings: make(map[ObservationBinding]context.Context), windows: &execWindowRegistry{}, live: make(map[string]bool), pending: make(map[string]mekugiHistory), tasks: make(map[string]string), taskEvents: make(map[string]observationTask), broker: newLiveDiffBroker(ctx)}
	o.store.liveDiff = o.broker.publish
	return o, nil
}

func observationThread(b ObservationBinding) string {
	// Hash a structured tuple so delimiters in native identities cannot alias.
	data, _ := json.Marshal(b)
	return fmt.Sprintf("runtime-%x", sha256.Sum256(data))
}
func observationKey(call ObservationCall) string {
	return observationThread(call.Binding) + fmt.Sprintf("/call-%x", sha256.Sum256([]byte(call.ID)))
}

func (o *nativeObservationOwner) bind(ctx context.Context, b ObservationBinding) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.bindLocked(ctx, b)
}

func (o *nativeObservationOwner) bindLocked(ctx context.Context, b ObservationBinding) error {
	if b.Runtime != o.runtime || b.Session == "" || b.Workspace != o.workspace || b.Branch != "" {
		return errors.New("native observation binding does not match this launch")
	}
	if o.session != "" && b.Session != o.session {
		return errors.New("native session changed during this launch")
	}
	if _, ok := o.bindings[b]; ok {
		return nil
	}
	if len(o.bindings) >= 256 {
		return errors.New("native agent observation limit reached")
	}
	root := b
	root.Agent = ""
	if b.Agent != "" {
		if _, ok := o.bindings[root]; !ok {
			return errors.New("native root session is not bound")
		}
	}
	thread, namespace := observationThread(b), observationThread(root)
	if b.Agent == "" {
		lease := flock.New(filepath.Join(o.store.directory, namespace+".observation.lock"), flock.SetPermissions(0600))
		ok, err := lease.TryLock()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("native session already has an observation owner")
		}
		bound := false
		defer func() {
			if !bound {
				_ = lease.Unlock()
			}
		}()
		// Release on any later failure, but keep one exclusive owner after bind.
		defer func() {
			if _, ok := o.bindings[b]; ok {
				bound = true
				o.release = append(o.release, func() { _ = lease.Unlock() })
			}
		}()
	}
	identity := storageSessionIdentity{Thread: thread, Namespace: namespace, Initialize: true}
	if b.Agent != "" {
		identity.Parent = namespace
	}
	scopeCtx := context.WithValue(ctx, storageSessionKey{}, identity)
	store := o.store.scoped(scopeCtx)
	if err := store.locked(scopeCtx, func() error { return store.initializeHandleScope(nil, nil) }); err != nil {
		return err
	}
	// Keep the runtime-qualified mapping as retained evidence, not just a hash.
	record := mekugiHistory{ToolName: "native_session", ExecutingThread: thread, Root: o.workspace, NativeObservation: &nativeObservationRecord{Binding: b}}
	if err := store.put(scopeCtx, o.workspace, map[string]mekugiHistory{thread + "/binding": record}); err != nil {
		return err
	}
	_, release, err := store.beginSession(ctx, thread, "")
	if err != nil {
		return err
	}
	o.release = append(o.release, release)
	// Do not retain an HTTP request's cancellable context for future calls.
	o.bindings[b] = context.WithValue(context.Background(), storageSessionKey{}, identity)
	o.session = b.Session
	threads := make(map[string]bool, len(o.bindings))
	for binding := range o.bindings {
		threads[observationThread(binding)] = true
	}
	// Restore saved child membership in this session's namespace, without
	// authorizing that child to send new calls or reviving its process.
	if index, err := store.readChangeIndex(o.workspace); err == nil {
		for _, stream := range index.Streams {
			threads[stream.Thread] = true
		}
	} else {
		return err
	}
	o.broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{o.workspace: threads}})
	return nil
}

func (o *nativeObservationOwner) callContext(ctx context.Context, call ObservationCall) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bound, ok := o.bindings[call.Binding]
	if !ok || call.ID == "" || call.Tool == "" || len(call.ID) > 1024 || len(call.Input) > 8<<20 || call.Workdir != "" && !filepath.IsAbs(call.Workdir) {
		return nil, errors.New("unbound or invalid native call")
	}
	return context.WithValue(ctx, storageSessionKey{}, bound.Value(storageSessionKey{})), nil
}

type nativeObservationRecord struct {
	Binding  ObservationBinding
	Call     *ObservationCall     `json:",omitempty"`
	Terminal *ObservationTerminal `json:",omitempty"`
	Started  time.Time            `json:",omitzero"`
	Ended    time.Time            `json:",omitzero"`
}

func captureNativePaths(paths []string, workspace string) *execObservation {
	observation, _ := captureObservationWithinBudget(func() (*execObservation, bool) {
		c := newExecCapture(time.Now().Add(execCaptureHold))
		for _, path := range paths {
			if path == "" {
				continue
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(workspace, path)
			}
			c.add(path)
			c.through(path, "")
		}
		result := &execObservation{Class: execDeclared.String(), Files: c.files, Omitted: c.omitted, Roots: []string{workspace}}
		boundExecObservation(result)
		return result, true
	}, func() *execObservation {
		return &execObservation{Class: execOpaque.String(), Reason: "capture deadline"}
	})
	return observation
}

func prepareNativeObservation(ctx context.Context, store *mekugiReplayStore, workspace string, call ObservationCall) (observation *execObservation, err error) {
	started := time.Now().UTC()
	workdir := call.Workdir
	if workdir == "" {
		workdir = workspace
	}
	if len(call.Paths) > 0 {
		if len(call.Paths) > 32 {
			return nil, errors.New("native path scope exceeds limit")
		}
		observation = captureNativePaths(call.Paths, workdir)
	} else if call.Command != "" {
		commands := []execCommandInput{{Command: call.Command, Workdir: workdir, Shell: call.Shell}}
		var observed bool
		observation, observed = captureExecObservation(commands, false, false, execCaptureEnv{directory: workdir, changes: storeChangeResolver(ctx, store)})
		source := observation
		observation, _ = snapshotObservedWorkspace(ctx, store, workspace, observation, observed)
		if observation == nil && source != nil && source.Class != execNeutral.String() {
			observation = source
			observation.Reason += "; workspace baseline unavailable"
		}
	}
	if observation == nil {
		observation = &execObservation{Class: execNeutral.String(), Reason: "no captured write scope"}
	}
	observation.WindowStart = started
	return observation, nil
}

func (o *nativeObservationOwner) before(ctx context.Context, call ObservationCall) error {
	return o.beforePrepared(ctx, call, nil, nil)
}

// A local user command can capture its baseline before the native host assigns
// its first session ID. Admission still requires the confirmed binding.
func (o *nativeObservationOwner) beforePrepared(ctx context.Context, call ObservationCall, observation *execObservation, window *execWindow) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	ctx, err := o.callContext(ctx, call)
	if err != nil {
		return err
	}
	key := observationKey(call)
	if prior, found, err := o.store.lookup(ctx, o.workspace, key+"/before"); err != nil {
		return err
	} else if found {
		if prior.NativeObservation == nil || !sameObservationCall(prior.NativeObservation.Call, &call) {
			return errors.New("native call identity/input changed")
		}
		// Replay never opens another observation window or captures a later baseline.
		return nil
	}
	// A terminal receipt may arrive without a successful pre-tool observation.
	// Its coverage gap is final: a delayed hook cannot supply a baseline after
	// the effect or reopen display settlement for the completed call.
	if prior, found, err := o.store.lookup(ctx, o.workspace, key+"/after"); err != nil {
		return err
	} else if found {
		if prior.NativeObservation == nil || !sameObservationCall(prior.NativeObservation.Call, &call) {
			return errors.New("native call identity/input changed")
		}
		return nil
	}
	if len(o.live) >= 1024 {
		return errors.New("native pending observation limit reached")
	}
	if observation == nil {
		observation, err = prepareNativeObservation(ctx, o.store, o.workspace, call)
		if err != nil {
			return err
		}
	}
	history := mekugiHistory{ToolName: call.Tool, Root: o.workspace, ExecutingThread: observationThread(call.Binding), ExecObservation: observation,
		NativeObservation: &nativeObservationRecord{Binding: call.Binding, Call: &call, Started: observation.WindowStart}}
	if err := o.store.put(ctx, o.workspace, map[string]mekugiHistory{key + "/before": history}); err != nil {
		return err
	}
	o.live[key] = true
	if o.execTrack != nil && call.Tool == "Bash" && call.Command != "" {
		if _, trackable := execsegment.Split(call.Command); trackable {
			o.execTrack.startScript([3]string{call.Binding.Session, call.Binding.Agent, call.ID}, call.Command)
		}
	}
	o.pendingCount.Add(1)
	if window != nil {
		// Keep the overlap claims collected before the first native session was
		// bound. Reopening here would attribute intervening tool effects twice.
		o.windows.mu.Lock()
		window.ref, window.thread = key, history.ExecutingThread
		o.windows.mu.Unlock()
	} else {
		o.windows.open(&execWindow{ref: key, roots: observation.Roots, thread: history.ExecutingThread, named: true, endpointScope: observation.scopePaths(), endpointWide: call.Command != "" && observation.Class != execNeutral.String()})
	}
	return nil
}

func (o *nativeObservationOwner) after(ctx context.Context, call ObservationCall, terminal ObservationTerminal) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ctx, err := o.callContext(ctx, call)
	if err != nil {
		return "", err
	}
	return o.finishLocked(ctx, call, terminal)
}

func (o *nativeObservationOwner) finishLocked(ctx context.Context, call ObservationCall, terminal ObservationTerminal) (string, error) {
	key := observationKey(call)
	terminal.Report = execTruncateReport(terminal.Report, maxExecReportBytes)
	if prior, found, err := o.store.lookup(ctx, o.workspace, key+"/after"); err != nil {
		return "", err
	} else if found {
		if prior.NativeObservation == nil || !sameObservationCall(prior.NativeObservation.Call, &call) {
			return "", errors.New("native completion identity/input changed")
		}
		// put repairs a publication that failed after writing its durable record.
		if err := o.store.put(ctx, o.workspace, map[string]mekugiHistory{key + "/after": prior}); err != nil {
			return "", err
		}
		o.settled(key, terminal.Task)
		return prior.ChangeID, nil
	}
	before, found, err := o.store.lookup(ctx, o.workspace, key+"/before")
	if err != nil {
		return "", err
	}
	if found && (before.NativeObservation == nil || !sameObservationCall(before.NativeObservation.Call, &call)) {
		return "", errors.New("native completion differs from pre-tool input")
	}
	if terminal.Task != "" && terminal.Status == "running" {
		if !found || !o.live[key] {
			return "", errors.New("background observation has no live pre-tool baseline")
		}
		if prior := o.tasks[terminal.Task]; prior != "" && prior != key {
			return "", errors.New("ambiguous native background task")
		}
		background := mekugiHistory{ToolName: call.Tool, Root: o.workspace, ExecutingThread: before.ExecutingThread, NativeObservation: &nativeObservationRecord{Binding: call.Binding, Call: &call, Terminal: &terminal}}
		if err := o.store.put(ctx, o.workspace, map[string]mekugiHistory{key + "/background": background}); err != nil {
			return "", err
		}
		o.tasks[terminal.Task] = key
		if event, ok := o.taskEvents[terminal.Task]; ok {
			if event.CallID != "" && event.CallID != call.ID {
				return "", errors.New("background event tool ID mismatch")
			}
			return o.finishLocked(ctx, call, ObservationTerminal{Task: terminal.Task, Status: event.Status, Report: event.Report, Interrupted: event.Status == "stopped"})
		}
		return "", nil
	}
	if terminal.Status != "completed" && terminal.Status != "failed" && terminal.Status != "stopped" {
		return "", errors.New("native completion is not terminal")
	}
	record, frozen := o.pending[key]
	if !frozen {
		observation := before.ExecObservation
		if !found || !o.live[key] || observation == nil {
			// A restart restores evidence, never a live observation interval. Reading a
			// current file here would misattribute changes made while disconnected.
			observation = &execObservation{Class: execOpaque.String(), Reason: "pre-tool observation window unavailable"}
		}
		if observation.Tree != "" {
			defer o.store.snapshots.serialize(o.workspace)()
		}
		reviews, _, outcome := reconcileObservedWindow(ctx, o.store, o.windows, o.workspace, []string{key}, *observation)
		outcome.Status = terminal.Status // No invented native numeric exit code.
		if !found || !o.live[key] || observation.Class != execNeutral.String() && len(observation.Files) == 0 && observation.Tree == "" {
			outcome.Coverage = execCoverageUnswept
		}
		caller := "/root"
		if call.Binding.Agent != "" {
			caller = "native/" + call.Binding.Agent
		}
		record = mekugiHistory{ToolName: call.Tool, Root: o.workspace, Script: call.Command, ExecutingThread: observationThread(call.Binding), Caller: caller, Source: call.Tool,
			ReviewFiles: reviews, ExecOutcome: outcome, Report: execTruncateReport(terminal.Report, maxExecReportBytes), CorrelationID: key, Attempt: 1,
			NativeObservation: &nativeObservationRecord{Binding: call.Binding, Call: &call, Terminal: &terminal, Started: observation.WindowStart, Ended: time.Now().UTC()}}
		o.pending[key] = record
	}
	if len(record.ReviewFiles) > 0 && record.ChangeID == "" {
		record.ChangeID, err = o.store.reserveChange(ctx, o.workspace, record.ExecutingThread, key)
		if err != nil {
			return "", err
		}
		o.pending[key] = record
	}
	if err := o.store.put(ctx, o.workspace, map[string]mekugiHistory{key + "/after": record}); err != nil {
		return "", err
	}
	o.execTrack.nativeCompleted([3]string{call.Binding.Session, call.Binding.Agent, call.ID})
	o.settled(key, terminal.Task)
	return record.ChangeID, nil
}

func (o *nativeObservationOwner) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, release := range o.release {
		release()
	}
	o.release = nil
	o.store.snapshots.close()
}

func (o *nativeObservationOwner) settled(key, task string) {
	delete(o.pending, key)
	if o.live[key] {
		o.pendingCount.Add(-1)
	}
	delete(o.live, key)
	if task != "" {
		delete(o.taskEvents, task)
		delete(o.tasks, task)
	}
	// Publication already persisted. This edge also releases display settlement
	// when the change event raced the pending-count update or the call had no edit.
	o.broker.publishTurn(false)
}
