package requestexecution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/coder/websocket"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestDownstreamEOFDuringResponseSniffing(t *testing.T) {
	disconnected := inferencegateway.DownstreamDisconnectError(io.EOF)
	response := &http.Response{
		Header: make(http.Header),
		Body:   io.NopCloser(finalAnswerErrorReader{disconnected}),
	}
	defer response.Body.Close()
	if _, err := codexresponses.PrepareBody(response, true); !errors.Is(err, codexresponses.ErrDownstreamDisconnected) {
		t.Fatalf("response sniffing lost disconnect: %v", err)
	}
}

func TestDownstreamDisconnectFinalization(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
		disconnected bool
	}{
		{"eof", io.EOF, true},
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"closed", net.ErrClosed, true},
		{"pipe", syscall.EPIPE, true},
		{"reset", syscall.ECONNRESET, true},
		{"normal", websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: "private"}, true},
		{"going_away", websocket.CloseError{Code: websocket.StatusGoingAway}, true},
		{"abnormal", websocket.CloseError{Code: websocket.StatusAbnormalClosure}, true},
		{"policy", websocket.CloseError{Code: websocket.StatusPolicyViolation}, false},
		{"internal", websocket.CloseError{Code: websocket.StatusInternalError}, false},
		{"deadline", context.DeadlineExceeded, false},
		{"unknown", errors.New("private failure"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, started := range []bool{false, true} {
				// The reader has not canceled the context yet.
				err := fmt.Errorf("%w: %w", codexresponses.ErrWrite, inferencegateway.DownstreamDisconnectError(fmt.Errorf("private address: %w", test.err)))
				if !errors.Is(err, test.err) {
					t.Fatal("lost original cause")
				}
				f := &requestFinalization{}
				f.classifyCopyError(err)
				issues := testCriticalErrors()
				f.finish(t.Context(), err, startedWriter(started), issues)
				want := requestOutcomeFailed
				if test.disconnected {
					want = requestOutcomeCanceledBeforeResponse
					if started {
						want = requestOutcomeCanceledAfterResponse
					}
				} else if errors.Is(test.err, context.DeadlineExceeded) {
					want = requestOutcomeTimedOut
				}
				if f.outcome != want {
					t.Fatalf("started=%v: outcome=%v, want %v", started, f.outcome, want)
				}
				if (len(issues.Notices.Pending()) == 0) != test.disconnected {
					t.Fatalf("unexpected notices: %v", issues.Notices.Pending())
				}
				if test.disconnected && requestCancellationCause(t.Context(), err) != "downstream_disconnected" {
					t.Fatal("missing disconnect evidence")
				}
			}
		})
	}
}

type disconnectHTTPResponseWriter struct {
	header  http.Header
	err     error
	wrote   bool
	flushed bool
}

func (w *disconnectHTTPResponseWriter) Header() http.Header { return w.header }
func (w *disconnectHTTPResponseWriter) WriteHeader(int)     {}
func (w *disconnectHTTPResponseWriter) Write([]byte) (int, error) {
	w.wrote = true
	return 2, w.err
}
func (w *disconnectHTTPResponseWriter) FlushError() error {
	w.flushed = true
	return w.err
}

func TestHTTPDownstreamDisconnectFinalization(t *testing.T) {
	for _, test := range []struct {
		name         string
		err          error
		disconnected bool
	}{
		{"eof", io.EOF, true},
		{"unexpected_eof", io.ErrUnexpectedEOF, true},
		{"closed", net.ErrClosed, true},
		{"pipe", syscall.EPIPE, true},
		{"reset", syscall.ECONNRESET, true},
		{"deadline", context.DeadlineExceeded, false},
		{"unknown", errors.New("private writer failure"), false},
	} {
		for _, wrapped := range []bool{false, true} {
			for _, operation := range []string{"write", "flush"} {
				t.Run(fmt.Sprintf("%s/wrapped=%v/%s", test.name, wrapped, operation), func(t *testing.T) {
					original := test.err
					if wrapped {
						original = &net.OpError{Op: operation, Net: "tcp", Err: test.err}
					}
					underlying := &disconnectHTTPResponseWriter{header: make(http.Header), err: original}
					writer := &inferencegateway.ResponseWriter{ResponseWriter: underlying}
					var err error
					if operation == "write" {
						var n int
						n, err = writer.Write([]byte("body"))
						if n != 2 || !underlying.wrote || underlying.flushed {
							t.Fatalf("write result=%d, called write=%v flush=%v", n, underlying.wrote, underlying.flushed)
						}
					} else {
						err = writer.FlushError()
						if !underlying.flushed || underlying.wrote {
							t.Fatalf("called write=%v flush=%v", underlying.wrote, underlying.flushed)
						}
					}
					if !errors.Is(err, original) || !errors.Is(err, test.err) {
						t.Fatalf("lost original error: %v", err)
					}
					if errors.Is(err, codexresponses.ErrDownstreamDisconnected) != test.disconnected {
						t.Fatalf("disconnect classification=%v, want %v: %v", errors.Is(err, codexresponses.ErrDownstreamDisconnected), test.disconnected, err)
					}
					if !requestResponseStarted(writer) || writer.StatusCode() != http.StatusOK {
						t.Fatalf("write/flush did not commit response: %+v", writer)
					}
					requestErr := fmt.Errorf("%w: %w", codexresponses.ErrWrite, err)
					// The actual failed operation commits HTTP output. Also check
					// finalization when this error reaches an uncommitted output.
					for _, output := range []*inferencegateway.ResponseWriter{{ResponseWriter: httptest.NewRecorder()}, writer} {
						f := &requestFinalization{}
						f.classifyCopyError(requestErr)
						issues := testCriticalErrors()
						f.finish(t.Context(), requestErr, output, issues)
						if t.Context().Err() != nil {
							t.Fatal("finalization relied on a canceled context")
						}
						want := requestOutcomeFailed
						if test.disconnected {
							want = requestOutcomeCanceledBeforeResponse
							if output.ResponseStarted() {
								want = requestOutcomeCanceledAfterResponse
							}
						} else if errors.Is(test.err, context.DeadlineExceeded) {
							want = requestOutcomeTimedOut
						}
						if f.outcome != want || f.failurePhase != requestFailureWriteResponse {
							t.Fatalf("committed=%v: outcome=%v want %v, phase=%v", output.ResponseStarted(), f.outcome, want, f.failurePhase)
						}
						if (len(issues.Notices.Pending()) == 0) != test.disconnected {
							t.Fatalf("unexpected notices: %v", issues.Notices.Pending())
						}
						if test.disconnected && requestCancellationCause(t.Context(), requestErr) != "downstream_disconnected" {
							t.Fatal("missing disconnect evidence")
						}
					}
				})
			}
		}
	}
}

func TestDownstreamWriteDiagnostics(t *testing.T) {
	original := websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private reason"}
	d := &transportcapture.Stream{ReadOrigin: "upstream", ReadTermination: "upstream_eof"}
	recordStreamCopyStop(d, errors.Join(codexresponses.ErrWrite, inferencegateway.DownstreamDisconnectError(original)))
	if d.WriteTermination != "downstream_websocket_close_1001" || d.WriteWebSocketCloseCode != 1001 {
		t.Fatalf("missing write cause: %+v", d)
	}
	if d.ReadOrigin != "upstream" || d.ReadTermination != "upstream_eof" {
		t.Fatal("write diagnostics overwrote read evidence")
	}
	if strings.Contains(string(responses.MustMarshalJSON(d.Snapshot())), "private") {
		t.Fatal("diagnostics leaked close reason")
	}
}
