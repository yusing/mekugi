package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func runtimeFrontendContextFixture(t *testing.T, handler http.HandlerFunc) runtimeFrontendBinding {
	t.Helper()
	// Keep Unix socket names below the OS limit, independent of the worktree path.
	directory, err := os.MkdirTemp("", "frontend-context-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "context.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("fixture serve: %v", err)
		}
	})
	return runtimeFrontendBinding{
		Runtime: "claude", Workspace: t.TempDir(),
		Endpoint: ObservationEndpoint{Socket: socket, Token: "context-test-capability"},
	}
}

func runtimeFrontendContextReply(t *testing.T, binding ObservationBinding) string {
	t.Helper()
	reply := struct {
		Binding ObservationBinding `json:"binding"`
	}{Binding: binding}
	data, err := json.Marshal(&reply)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRuntimeFrontendContextReplies(t *testing.T) {
	for _, name := range []string{
		"success", "exact_limit", "wrong_runtime", "wrong_workspace", "missing_session", "agent", "branch",
		"empty", "malformed", "trailing_value", "trailing_garbage", "oversized", "missing_binding", "null_binding",
		"unknown_member", "unknown_binding_member", "wrong_type", "duplicate_binding", "unauthorized", "server_error", "redirect",
	} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			var body string
			status := http.StatusOK
			type fixtureReply struct {
				status int
				body   string
			}
			replies := make(chan fixtureReply, 1)
			binding := runtimeFrontendContextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/frontend/context" || r.URL.RawQuery != "" {
					t.Errorf("request = %s %s", r.Method, r.URL)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer context-test-capability" {
					t.Errorf("Authorization = %q", got)
				}
				requestBody, err := io.ReadAll(r.Body)
				if err != nil || len(requestBody) != 0 {
					t.Errorf("request body = %q, error = %v", requestBody, err)
				}
				if name == "redirect" {
					w.Header().Set("Location", "/redirected")
				}
				select {
				case reply := <-replies:
					w.WriteHeader(reply.status)
					_, _ = io.WriteString(w, reply.body)
				default:
					w.WriteHeader(http.StatusInternalServerError)
				}
			})
			want := ObservationBinding{Runtime: binding.Runtime, Workspace: binding.Workspace, Session: "native-root-session"}
			replyBinding := want
			switch name {
			case "wrong_runtime":
				replyBinding.Runtime = "codex"
			case "wrong_workspace":
				replyBinding.Workspace = t.TempDir()
			case "missing_session":
				replyBinding.Session = ""
			case "agent":
				replyBinding.Agent = "child"
			case "branch":
				replyBinding.Branch = "side"
			}
			body = runtimeFrontendContextReply(t, replyBinding)
			switch name {
			case "exact_limit":
				body += strings.Repeat(" ", (8<<10)-len(body))
			case "oversized":
				body += strings.Repeat(" ", (8<<10)+1-len(body))
			case "empty":
				body = ""
			case "malformed":
				body = `{"binding":`
			case "trailing_value":
				body += ` {}`
			case "trailing_garbage":
				body += " garbage"
			case "missing_binding":
				body = `{}`
			case "null_binding":
				body = `{"binding":null}`
			case "unknown_member":
				body = strings.TrimSuffix(body, "}") + `,"extra":true}`
			case "unknown_binding_member":
				body = strings.TrimSuffix(body, "}}") + `,"extra":true}}`
			case "wrong_type":
				body = `{"binding":"root"}`
			case "duplicate_binding":
				body = strings.TrimSuffix(body, "}") + `,"binding":null}`
			case "unauthorized":
				status = http.StatusUnauthorized
			case "server_error":
				status = http.StatusInternalServerError
			case "redirect":
				status = http.StatusTemporaryRedirect
			}
			replies <- fixtureReply{status: status, body: body}
			got, err := runtimeFrontendContext(t.Context(), binding)
			if name == "success" || name == "exact_limit" {
				if err != nil || got != want {
					t.Fatalf("context = %#v, %v; want %#v", got, err, want)
				}
			} else if err == nil || got != (ObservationBinding{}) {
				t.Fatalf("invalid reply returned context = %#v, %v", got, err)
			}
			if got := requests.Load(); got != 1 {
				t.Errorf("requests = %d, want 1 (no redirect follow)", got)
			}
		})
	}
}

func TestRuntimeFrontendContextInvalidBinding(t *testing.T) {
	var requests atomic.Int32
	valid := runtimeFrontendContextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	file := filepath.Join(valid.Workspace, "not-a-directory")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime", "relative_workspace", "unclean_workspace", "missing_workspace", "file_workspace", "relative_socket", "token"} {
		t.Run(name, func(t *testing.T) {
			binding := valid
			switch name {
			case "runtime":
				binding.Runtime = ""
			case "relative_workspace":
				binding.Workspace = "relative"
			case "unclean_workspace":
				binding.Workspace += "/."
			case "missing_workspace":
				binding.Workspace = filepath.Join(valid.Workspace, "missing")
			case "file_workspace":
				binding.Workspace = file
			case "relative_socket":
				binding.Endpoint.Socket = "relative.sock"
			case "token":
				binding.Endpoint.Token = ""
			}
			if got, err := runtimeFrontendContext(t.Context(), binding); err == nil || got != (ObservationBinding{}) {
				t.Fatalf("invalid binding returned %#v, %v", got, err)
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("invalid bindings made %d requests", got)
	}
}

func TestRuntimeFrontendContextCancellation(t *testing.T) {
	for _, name := range []string{"already_canceled", "in_flight", "deadline"} {
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			binding := runtimeFrontendContextFixture(t, func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-r.Context().Done()
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantErr := error(context.Canceled)
			if name == "already_canceled" {
				cancel()
			} else if name == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer deadlineCancel()
				wantErr = context.DeadlineExceeded
			} else {
				go func() {
					select {
					case <-started:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			got, err := runtimeFrontendContext(ctx, binding)
			if !errors.Is(err, wantErr) || got != (ObservationBinding{}) {
				t.Fatalf("canceled request returned %#v, %v; want %v", got, err, wantErr)
			}
			if name == "already_canceled" {
				select {
				case <-started:
					t.Error("already-canceled request reached server")
				default:
				}
			}
		})
	}
}

func TestRuntimeFrontendContextClientTimeout(t *testing.T) {
	// Real Unix I/O cannot use synctest's virtual clock. Keep one real timeout
	// check to distinguish the client's bound from caller-driven cancellation.
	binding := runtimeFrontendContextFixture(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	started := time.Now()
	got, err := runtimeFrontendContext(ctx, binding)
	elapsed := time.Since(started)
	networkErr, ok := errors.AsType[net.Error](err)
	if !ok || !networkErr.Timeout() || got != (ObservationBinding{}) {
		t.Fatalf("client timeout returned %#v, %v", got, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("request waited for caller deadline instead of client timeout: %v", ctx.Err())
	}
	if elapsed < 4*time.Second {
		t.Errorf("client timeout fired before 4s: %v", elapsed)
	}
}
