package router

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
	"time"

	"github.com/coder/websocket"
)

func TestDownstreamEOFDuringResponseSniffing(t *testing.T) {
	disconnected := downstreamDisconnectError(io.EOF)
	response := &http.Response{
		Header: make(http.Header),
		Body:   io.NopCloser(finalAnswerErrorReader{disconnected}),
	}
	defer response.Body.Close()
	if _, err := prepareUpstreamBody(response, true); !errors.Is(err, errDownstreamDisconnected) {
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
				err := fmt.Errorf("%w: %w", errResponseWrite, downstreamDisconnectError(fmt.Errorf("private address: %w", test.err)))
				if !errors.Is(err, test.err) {
					t.Fatal("lost original cause")
				}
				f := &requestFinalization{}
				f.classifyCopyError(err)
				issues := NewCriticalErrors()
				if err := f.finish(t.Context(), err, &webSocketOutput{committed: started}, issues); err != nil {
					t.Fatal(err)
				}
				want := requestOutcomeFailed
				if test.disconnected {
					want = requestOutcomeCanceledBeforeResponse
					if started {
						want = requestOutcomeCanceledAfterResponse
					}
				} else if errors.Is(test.err, context.DeadlineExceeded) {
					want = requestOutcomeTimedOut
				}
				if f.observation.outcome != want {
					t.Fatalf("started=%v: outcome=%v, want %v", started, f.observation.outcome, want)
				}
				if (len(issues.Pending()) == 0) != test.disconnected {
					t.Fatalf("unexpected notices: %v", issues.Pending())
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
					writer := &trackedResponseWriter{ResponseWriter: underlying}
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
					if errors.Is(err, errDownstreamDisconnected) != test.disconnected {
						t.Fatalf("disconnect classification=%v, want %v: %v", errors.Is(err, errDownstreamDisconnected), test.disconnected, err)
					}
					if !requestResponseStarted(writer) || writer.statusCode != http.StatusOK {
						t.Fatalf("write/flush did not commit response: %+v", writer)
					}
					requestErr := fmt.Errorf("%w: %w", errResponseWrite, err)
					// The actual failed operation commits HTTP output. Also check
					// finalization when this error reaches an uncommitted output.
					for _, output := range []*trackedResponseWriter{{ResponseWriter: httptest.NewRecorder()}, writer} {
						f := &requestFinalization{}
						f.classifyCopyError(requestErr)
						issues := NewCriticalErrors()
						if err := f.finish(t.Context(), requestErr, output, issues); err != nil {
							t.Fatal(err)
						}
						if t.Context().Err() != nil {
							t.Fatal("finalization relied on a canceled context")
						}
						want := requestOutcomeFailed
						if test.disconnected {
							want = requestOutcomeCanceledBeforeResponse
							if output.committed {
								want = requestOutcomeCanceledAfterResponse
							}
						} else if errors.Is(test.err, context.DeadlineExceeded) {
							want = requestOutcomeTimedOut
						}
						if f.observation.outcome != want || f.failurePhase != requestFailureWriteResponse {
							t.Fatalf("committed=%v: outcome=%v want %v, phase=%v", output.committed, f.observation.outcome, want, f.failurePhase)
						}
						if (len(issues.Pending()) == 0) != test.disconnected {
							t.Fatalf("unexpected notices: %v", issues.Pending())
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

func TestWebSocketOutputDisconnectBeforeReaderCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- conn
	}))
	defer server.Close()
	client, _, err := websocket.Dial(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	var downstream *websocket.Conn
	select {
	case downstream = <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer downstream.CloseNow()
	output := &webSocketOutput{exchange: &webSocketExchange{
		ctx: ctx, session: &responsesWebSocket{downstream: downstream},
	}}
	// A successful event must count as response-started for WebSocket output.
	read := make(chan error, 1)
	go func() {
		_, _, err := client.Read(ctx)
		read <- err
	}()
	event := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
	if _, err := output.Write(event); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if !requestResponseStarted(output) {
		t.Fatal("WebSocket delivery was not tracked")
	}
	// No downstream reader runs here: closing the socket cannot cancel ctx.
	if err := downstream.CloseNow(); err != nil {
		t.Fatal(err)
	}
	_, err = output.Write(event)
	if !errors.Is(err, errDownstreamDisconnected) || ctx.Err() != nil {
		t.Fatalf("write error=%v, context=%v", err, ctx.Err())
	}
	f := &requestFinalization{}
	issues := NewCriticalErrors()
	_ = f.finish(ctx, errors.Join(errResponseWrite, err), output, issues)
	if f.observation.outcome != requestOutcomeCanceledAfterResponse || len(issues.Pending()) != 0 {
		t.Fatalf("disconnect produced outcome %v, notices %v", f.observation.outcome, issues.Pending())
	}
}

func TestDownstreamWriteDiagnostics(t *testing.T) {
	original := websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private reason"}
	d := &streamDiagnostics{ReadOrigin: "upstream", ReadTermination: "upstream_eof"}
	d.copyStopped(errors.Join(errResponseWrite, downstreamDisconnectError(original)))
	if d.WriteTermination != "downstream_websocket_close_1001" || d.WriteWebSocketCloseCode != 1001 {
		t.Fatalf("missing write cause: %+v", d)
	}
	if d.ReadOrigin != "upstream" || d.ReadTermination != "upstream_eof" {
		t.Fatal("write diagnostics overwrote read evidence")
	}
	if strings.Contains(string(mustMarshalJSON(d.snapshot())), "private") {
		t.Fatal("diagnostics leaked close reason")
	}
}

func TestDownstreamDisconnectDuringAnswerStreaming(t *testing.T) {
	transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
	reader := strings.NewReader(finalAnswerTestWire(finalAnswerTestEvents(t, "final_answer")))
	disconnected := downstreamDisconnectError(websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private"})
	diagnostics := &streamDiagnostics{}
	_, err := copySSETransformed(serverErrorWriter{err: disconnected}, reader, transform, &responseHooks{streamDiagnostics: diagnostics})
	f := &requestFinalization{}
	f.classifyCopyError(err)
	issues := NewCriticalErrors()
	_ = f.finish(t.Context(), err, &webSocketOutput{}, issues)
	if f.observation.outcome != requestOutcomeCanceledBeforeResponse || len(issues.Pending()) != 0 {
		t.Fatalf("stream disconnect: %v, outcome %v, notices %v", err, f.observation.outcome, issues.Pending())
	}
	if diagnostics.WriteTermination != "downstream_websocket_close_1001" || diagnostics.WriteWebSocketCloseCode != 1001 {
		t.Fatalf("lost stream diagnostics: %+v", diagnostics)
	}
}
