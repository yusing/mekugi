package router

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	json "encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ObservationEndpoint is an invocation-private capability held by the SDK bridge.
// It is not placed in Claude's environment or model-visible tool configuration.
type ObservationEndpoint struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
}

// ObservationService provides capture, journals and read-only frontend binding.
// It exposes no tool executor, filesystem mutation command, permission decision
// or inference transport.
type ObservationService struct {
	owner           *nativeObservationOwner
	server          *http.Server
	directory       string
	endpoint        ObservationEndpoint
	journal         *runtimeJournalOwner
	frontendToken   string
	registry        *toolRegistry
	plugin          string
	stagedCompanion *runtimeSessionCompanion
	receipts        bool
	trackCancel     context.CancelFunc
}

type observationRequest struct {
	Operation     string              `json:"operation"`
	Binding       ObservationBinding  `json:"binding,omitzero"`
	Source        ObservationBinding  `json:"source,omitzero"`
	Call          ObservationCall     `json:"call,omitzero"`
	Terminal      ObservationTerminal `json:"terminal,omitzero"`
	Task          observationTask     `json:"task,omitzero"`
	NativeID      string              `json:"nativeID,omitempty"`
	Input         string              `json:"input,omitempty"`
	Child         string              `json:"child,omitempty"`
	MaxCharacters int                 `json:"maxCharacters,omitzero"`
}

type observationTask struct {
	ID      string `json:"id"`
	CallID  string `json:"callID,omitempty"`
	Session string `json:"session"`
	Status  string `json:"status"`
	Report  string `json:"report,omitempty"`
}

// StartObservationService owns the backend's invocation-local evidence service.
func StartObservationService(ctx context.Context, runtime, workspace string) (*ObservationService, error) {
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		return nil, err
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		return nil, err
	}
	owner, err := newNativeObservationOwner(ctx, store, runtime, workspace)
	if err != nil {
		return nil, err
	}
	return startObservationService(owner)
}

func startObservationService(owner *nativeObservationOwner) (*ObservationService, error) {
	directory, err := os.MkdirTemp("", "mekugi-observe-")
	if err != nil {
		owner.close()
		return nil, err
	}
	endpoint := ObservationEndpoint{Socket: filepath.Join(directory, "service.sock"), Token: rand.Text()}
	listener, err := net.Listen("unix", endpoint.Socket)
	if err != nil {
		os.Remove(directory)
		owner.close()
		return nil, err
	}
	s := &ObservationService{owner: owner, directory: directory, endpoint: endpoint, frontendToken: rand.Text()}
	slots := make(chan struct{}, 16)
	s.server = &http.Server{ReadTimeout: 4 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			owner.mu.Lock()
			frontend := r.URL.Path == "/frontend/context" && s.registry != nil
			frontendToken := s.frontendToken
			owner.mu.Unlock()
			if r.Method != http.MethodPost || r.URL.Path != "/observe" && !frontend {
				http.NotFound(w, r)
				return
			}
			token := endpoint.Token
			if frontend {
				token = frontendToken
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				http.Error(w, "invalid observation capability", http.StatusUnauthorized)
				return
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				http.Error(w, "observation capacity reached", http.StatusServiceUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if frontend {
				owner.mu.Lock()
				if frontendToken != s.frontendToken {
					owner.mu.Unlock()
					http.Error(w, "retired frontend capability", http.StatusUnauthorized)
					return
				}
				binding := ObservationBinding{Runtime: owner.runtime, Session: owner.session, Workspace: owner.workspace}
				_, bound := owner.bindings[binding]
				owner.mu.Unlock()
				if !bound {
					http.Error(w, "native session identity pending", http.StatusUnprocessableEntity)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.MarshalWrite(w, map[string]ObservationBinding{"binding": binding})
				return
			}
			var request observationRequest
			if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, 8<<20), &request, json.RejectUnknownMembers(true)); err != nil {
				http.Error(w, "invalid observation request", http.StatusBadRequest)
				return
			}
			var err error
			var changeID string
			switch request.Operation {
			case "session_switch_check", "session_switch":
				err = s.switchSession(ctx, request.Source, request.Binding, request.Operation == "session_switch_check")
				if err == nil && request.Operation == "session_switch" {
					owner.mu.Lock()
					result := CompanionPresentation{Plugin: s.plugin}
					if s.registry != nil {
						result.FrontendDirectory = s.registry.frontendDirectory
					}
					owner.mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_ = json.MarshalWrite(w, result)
					return
				}
			case "bind":
				err = owner.bind(ctx, request.Binding)
				if err == nil && s.journal != nil {
					err = s.journal.bind(ctx, request.Binding)
				}
			case "before":
				err = owner.before(ctx, request.Call)
			case "after":
				changeID, err = owner.after(ctx, request.Call, request.Terminal)
			case "task":
				err = owner.task(ctx, request.Task)
				if err == nil && s.journal != nil {
					err = s.journal.task(ctx, request.Task, false)
				}
			case "journal_task_start":
				if s.journal == nil {
					err = errors.New("companion journal is disabled")
				} else {
					err = s.journal.task(ctx, request.Task, true)
				}
			case "journal_before":
				if s.journal == nil {
					err = errors.New("companion journal is disabled")
				} else {
					err = s.journal.before(ctx, request.Call)
				}
			case "journal_batch", "journal_read", "mchanges":
				if s.journal == nil {
					err = errors.New("companion journal is disabled")
				} else {
					var result any
					result, err = s.journal.invoke(ctx, request.Operation, request.NativeID, request.Input)
					if err == nil {
						w.Header().Set("Content-Type", "application/json")
						_ = json.MarshalWrite(w, result)
						return
					}
				}
			case "journal_recovery":
				if s.journal == nil {
					err = errors.New("companion journal is disabled")
				} else {
					var text string
					capacity := request.MaxCharacters
					if capacity == 0 {
						capacity = 10000
					}
					text, err = s.journal.recoverBounded(ctx, request.Binding, capacity)
					if err == nil {
						w.Header().Set("Content-Type", "application/json")
						_ = json.MarshalWrite(w, map[string]string{"text": text})
						return
					}
				}
			case "journal_reset_check", "journal_reset", "journal_reset_installed":
				if s.journal == nil {
					err = errors.New("native journal is unavailable")
				} else {
					var result any
					result, err = s.journal.resetSession(ctx, request.Operation, request.Binding)
					if err == nil {
						w.Header().Set("Content-Type", "application/json")
						_ = json.MarshalWrite(w, result)
						return
					}
				}
			case "journal_parent":
				if s.journal == nil {
					err = errors.New("companion journal is disabled")
				} else {
					err = s.journal.parent(ctx, request.NativeID, request.Child, request.Terminal.Status)
				}
			default:
				err = errors.New("unsupported observation operation")
			}
			if err != nil {
				http.Error(w, request.Operation+": "+err.Error(), http.StatusUnprocessableEntity)
				return
			}
			// Capture-only replies add no context. Utility-enabled replies may
			// include a durable change receipt without replacing native results.
			w.Header().Set("Content-Type", "application/json")
			if s.receipts && changeID != "" {
				_ = json.MarshalWrite(w, map[string]string{"changeID": changeID})
				return
			}
			w.Write([]byte("{}"))
		})}
	go func() { _ = s.server.Serve(listener) }()
	return s, nil
}
func (s *ObservationService) Endpoint() ObservationEndpoint { return s.endpoint }
func (s *ObservationService) Close() error {
	err := s.server.Close()
	if s.trackCancel != nil {
		s.trackCancel()
		s.owner.execTrack.close()
		s.owner.execTrack.wg.Wait()
	}
	companionErr := s.closeCompanion()
	s.owner.close()
	// Remove only the exact resources this launch created. Retained evidence stays.
	return errors.Join(err, companionErr, removeObservationSocket(s.endpoint.Socket), s.closeCommandTracking(), os.Remove(s.directory))
}
func removeObservationSocket(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (o *nativeObservationOwner) task(ctx context.Context, event observationTask) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if event.Session != o.session || event.ID == "" {
		return errors.New("background task binding mismatch")
	}
	if event.Status != "completed" && event.Status != "failed" && event.Status != "stopped" {
		return nil
	}
	key := o.tasks[event.ID]
	if key == "" {
		if o.pendingCount.Load() == 0 {
			return nil
		}
		// Terminal edges can race PostToolUse. Keep a bounded, process-local edge;
		// it cannot settle anything without the hook's persisted task/call mapping.
		if len(o.taskEvents) >= 1024 {
			return errors.New("pending background event limit reached")
		}
		event.Report = execTruncateReport(event.Report, maxExecReportBytes)
		o.taskEvents[event.ID] = event
		return nil
	}
	record, found, err := o.store.lookup(ctx, o.workspace, key+"/background")
	if err != nil {
		return err
	}
	if !found || record.NativeObservation == nil || record.NativeObservation.Call == nil {
		return errors.New("background call mapping unavailable")
	}
	call := *record.NativeObservation.Call
	if event.CallID != "" && event.CallID != call.ID {
		return errors.New("background event tool ID mismatch")
	}
	ctx, err = o.callContext(ctx, call)
	if err != nil {
		return err
	}
	_, err = o.finishLocked(ctx, call, ObservationTerminal{Task: event.ID, Status: event.Status, Report: event.Report, Interrupted: event.Status == "stopped"})
	return err
}
