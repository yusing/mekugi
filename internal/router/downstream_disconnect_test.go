package router

import (
	"bytes"
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

func TestDownstreamEOFDoesNotBecomeIncompletePatchFailure(t *testing.T) {
	for _, stop := range []string{"before_input_done", "after_input_done"} {
		t.Run(stop, func(t *testing.T) {
			calls := 0
			transform, _, _, _ := newMekugiTestTransform(t)
			added := testMekugiItem()
			added["status"], added["input"] = "in_progress", ""
			events := [][]byte{mustTestJSON(t, map[string]any{
				"type": "response.output_item.added", "item": added,
			})}
			if stop == "after_input_done" {
				events = append(events, mustTestJSON(t, map[string]any{
					"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testShellEditSource,
				}))
			}
			disconnected := downstreamWebSocketError(io.EOF)
			reader := io.MultiReader(strings.NewReader(finalAnswerTestWire(events)), finalAnswerErrorReader{disconnected})
			var output bytes.Buffer
			_, err := copySSETransformed(&output, reader, transform, nil)
			if !errors.Is(err, errDownstreamDisconnected) || errors.Is(err, errResponseTransform) {
				t.Fatalf("disconnect became translation failure: %v", err)
			}
			wantCalls := 0
			if calls != wantCalls {
				t.Fatalf("translation count = %d, want %d", calls, wantCalls)
			}
			if stop == "before_input_done" && strings.Contains(output.String(), "custom_tool_call") {
				t.Fatal("unfinished call escaped")
			}
			f := &requestFinalization{}
			f.classifyCopyError(err)
			issues := NewCriticalErrors()
			_ = f.finish(t.Context(), err, &webSocketOutput{committed: true}, issues)
			if f.observation.outcome != requestOutcomeCanceledAfterResponse || len(issues.Pending()) != 0 {
				t.Fatalf("disconnect produced outcome %v, notices %v", f.observation.outcome, issues.Pending())
			}
		})
	}
}

func TestDownstreamEOFDuringResponseSniffing(t *testing.T) {
	disconnected := downstreamWebSocketError(io.EOF)
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
				err := fmt.Errorf("%w: %w", errResponseWrite, downstreamWebSocketError(fmt.Errorf("private address: %w", test.err)))
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
	d.copyStopped(errors.Join(errResponseWrite, downstreamWebSocketError(original)))
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

func TestDownstreamDisconnectDuringAnswerDrain(t *testing.T) {
	for _, primary := range []error{nil, io.ErrUnexpectedEOF, errors.Join(errResponseTransform, errors.New("bad transform"))} {
		t.Run(fmt.Sprint(primary), func(t *testing.T) {
			transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
			var reader io.Reader = strings.NewReader(finalAnswerTestWire(finalAnswerTestEvents(t, "final_answer")))
			if primary != nil {
				reader = io.MultiReader(reader, finalAnswerErrorReader{primary})
			}
			disconnected := downstreamWebSocketError(websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private"})
			diagnostics := &streamDiagnostics{}
			_, err := copySSETransformed(serverErrorWriter{err: disconnected}, reader, transform, &responseHooks{streamDiagnostics: diagnostics})
			f := &requestFinalization{}
			f.classifyCopyError(err)
			issues := NewCriticalErrors()
			_ = f.finish(t.Context(), err, &webSocketOutput{}, issues)
			if primary != nil {
				if !errors.Is(err, primary) || f.observation.outcome != requestOutcomeFailed || len(issues.Pending()) == 0 {
					t.Fatalf("lost primary failure: %v, outcome %v, notices %v", err, f.observation.outcome, issues.Pending())
				}
			} else if f.observation.outcome != requestOutcomeCanceledBeforeResponse || len(issues.Pending()) != 0 {
				t.Fatalf("drain disconnect: %v, outcome %v, notices %v", err, f.observation.outcome, issues.Pending())
			}
			if diagnostics.WriteTermination != "downstream_websocket_close_1001" || diagnostics.WriteWebSocketCloseCode != 1001 {
				t.Fatalf("lost drain diagnostics: %+v", diagnostics)
			}
		})
	}
}
