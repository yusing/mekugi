package responses_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/responses"
	protocol "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

type deliveryTransform struct {
	json      func([]byte) ([]byte, error)
	sse       func([]byte) ([][]byte, error)
	finish    func(bool) error
	delivered func([]byte)
}

func (d deliveryTransform) TransformJSON(p []byte) ([]byte, error)  { return d.json(p) }
func (d deliveryTransform) TransformSSE(p []byte) ([][]byte, error) { return d.sse(p) }
func (d deliveryTransform) Finish(stream bool) error {
	if d.finish != nil {
		return d.finish(stream)
	}
	return nil
}
func (d deliveryTransform) Delivered(p []byte) {
	if d.delivered != nil {
		d.delivered(p)
	}
}

type deliveryObserver struct {
	observe   func([]byte, bool)
	delivered func([]byte)
	readEnded func(error, bool)
}

func (d deliveryObserver) Observe(p []byte, stream bool) { d.observe(p, stream) }
func (d deliveryObserver) Delivered(p []byte) {
	if d.delivered != nil {
		d.delivered(p)
	}
}
func (d deliveryObserver) ReadEnded(err error, pending bool) {
	if d.readEnded != nil {
		d.readEnded(err, pending)
	}
}

type deliveryWriter struct {
	body       bytes.Buffer
	steps      *[]string
	failAt     string
	flushed    bool
	flushError error
	fault      error
}

func (w *deliveryWriter) Header() http.Header { return make(http.Header) }
func (w *deliveryWriter) WriteHeader(int)     {}
func (w *deliveryWriter) String() string      { return w.body.String() }
func (w *deliveryWriter) Write(p []byte) (int, error) {
	if w.failAt != "" {
		if at := strings.Index(w.body.String()+string(p), w.failAt); at >= 0 {
			n, _ := w.body.Write(p[:max(0, at-w.body.Len())])
			return n, w.fault
		}
	}
	return w.body.Write(p)
}
func (w *deliveryWriter) FlushError() error {
	*w.steps = append(*w.steps, "flush")
	w.flushed = w.flushError == nil
	return w.flushError
}

func TestDeliveryStreamConfirmsOnlyAfterEveryWriteAndFlush(t *testing.T) {
	raw := `{"type":"response.completed"}`
	first := []byte(`{"type":"response.output_text.delta","delta":"answer"}`)
	last := []byte(raw)
	wire := "event: response.output_text.delta\ndata: " + string(first) + "\n\nevent: response.completed\ndata: " + raw + "\n\n"
	fault := errors.New("downstream fault")
	for _, mode := range []string{"success", "second event write", "flush"} {
		t.Run(mode, func(t *testing.T) {
			var steps []string
			writer := &deliveryWriter{steps: &steps, fault: fault}
			if mode == "second event write" {
				writer.failAt = "event: response.completed"
			}
			if mode == "flush" {
				writer.flushError = fault
			}
			confirmed := func(stage string, p []byte) {
				if writer.String() != wire || !writer.flushed {
					t.Fatalf("confirmation before complete flushed output: %q, flushed %v", writer.String(), writer.flushed)
				}
				steps = append(steps, stage+":"+string(p))
			}
			transform := responses.Compose(deliveryTransform{
				sse: func(p []byte) ([][]byte, error) {
					steps = append(steps, "expand")
					if string(p) != raw {
						t.Fatalf("transform got %q", p)
					}
					return [][]byte{first, last}, nil
				},
				delivered: func(p []byte) { confirmed("first confirmed", p) },
			}, deliveryTransform{
				sse: func(p []byte) ([][]byte, error) {
					steps = append(steps, "second transform:"+string(p))
					return [][]byte{p}, nil
				},
				delivered: func(p []byte) { confirmed("second confirmed", p) },
			})
			observer := deliveryObserver{
				observe: func(p []byte, stream bool) {
					if !stream || string(p) != raw {
						t.Fatalf("raw observation = %q, stream %v", p, stream)
					}
					steps = append(steps, "observe")
				},
				delivered: func(p []byte) { confirmed("observed delivery", p) },
			}
			state, err := responses.CopyBody(writer, strings.NewReader("event: response.completed\ndata: "+raw+"\n\n"), true, transform, observer)
			want := []string{"observe", "expand", "second transform:" + string(first), "second transform:" + string(last)}
			if mode != "second event write" {
				want = append(want, "flush")
			}
			if mode == "success" {
				if err != nil || state != protocol.TerminalCompleted {
					t.Fatalf("CopyBody = %v, %v", state, err)
				}
				for _, p := range [][]byte{first, last} {
					want = append(want, "first confirmed:"+string(p), "second confirmed:"+string(p), "observed delivery:"+string(p))
				}
				if writer.String() != wire {
					t.Fatalf("stream = %q, want %q", writer.String(), wire)
				}
			} else if !errors.Is(err, responses.ErrWrite) || !errors.Is(err, fault) {
				t.Fatalf("write failure lost cause: %v", err)
			}
			if !slices.Equal(steps, want) {
				t.Fatalf("delivery order = %q, want %q", steps, want)
			}
		})
	}
}

type deliverySource struct {
	io.Reader
	closed bool
}

func (s *deliverySource) Close() error { s.closed = true; return nil }

type deliveryReadError struct {
	err   error
	reads int
}

func (r *deliveryReadError) Read([]byte) (int, error) { r.reads++; return 0, r.err }

func TestDeliveryTerminalDoesNotDrainOrCloseSource(t *testing.T) {
	tail := &deliveryReadError{err: errors.New("read beyond terminal")}
	source := &deliverySource{Reader: io.MultiReader(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"), tail)}
	state, err := responses.CopyBody(io.Discard, source, true, nil, nil)
	if err != nil || state != protocol.TerminalCompleted || tail.reads != 0 || source.closed {
		t.Fatalf("terminal copy = %v, %v; tail reads %d, closed %v", state, err, tail.reads, source.closed)
	}
}

func TestDeliveryDisconnectWrappingEOFSkipsFinish(t *testing.T) {
	disconnect := errors.Join(responses.ErrDownstreamDisconnected, io.EOF)
	finishCalls := 0
	observed := false
	transform := deliveryTransform{finish: func(bool) error { finishCalls++; return errors.New("incomplete translation") }}
	observer := deliveryObserver{readEnded: func(err error, pending bool) {
		observed = true
		if err != disconnect || !pending {
			t.Fatalf("read end = %v, pending %v", err, pending)
		}
	}}
	source := io.MultiReader(strings.NewReader("data: unfinished"), &deliveryReadError{err: disconnect})
	_, err := responses.CopyBody(io.Discard, source, true, transform, observer)
	if err != disconnect || finishCalls != 0 || !observed {
		t.Fatalf("disconnect = %v; finish calls %d, observed %v", err, finishCalls, observed)
	}
}

func TestDeliveryJSONStagesUntilCallerConfirms(t *testing.T) {
	var steps []string
	raw := `{"status":"in_progress"}`
	visible := []byte(`{"status":"completed"}`)
	transform := deliveryTransform{
		json: func(p []byte) ([]byte, error) {
			steps = append(steps, "transform")
			if string(p) != raw {
				t.Fatalf("transform got %q", p)
			}
			return visible, nil
		},
		finish: func(stream bool) error {
			if stream {
				t.Fatal("JSON finished as stream")
			}
			steps = append(steps, "finish")
			return nil
		},
		delivered: func(p []byte) {
			if !bytes.Equal(p, visible) {
				t.Fatalf("confirmed %q", p)
			}
			steps = append(steps, "confirmed")
		},
	}
	observer := deliveryObserver{
		observe: func(p []byte, stream bool) {
			if stream || string(p) != raw {
				t.Fatalf("raw observation = %q, stream %v", p, stream)
			}
			steps = append(steps, "observe")
		},
		delivered: func([]byte) { t.Fatal("staged JSON reported streaming delivery") },
	}
	source := &deliverySource{Reader: strings.NewReader(raw)}
	var staged bytes.Buffer
	state, err := responses.CopyBody(&staged, source, false, transform, observer)
	if err != nil || state != protocol.TerminalCompleted || !bytes.Equal(staged.Bytes(), visible) || source.closed {
		t.Fatalf("JSON copy = %v, %v; staged %q, closed %v", state, err, staged.Bytes(), source.closed)
	}
	if !slices.Equal(steps, []string{"observe", "transform", "finish"}) {
		t.Fatalf("staging order = %q", steps)
	}
	responses.ConfirmDelivery(transform, staged.Bytes())
	if !slices.Equal(steps, []string{"observe", "transform", "finish", "confirmed"}) {
		t.Fatalf("confirmation order = %q", steps)
	}
}

func TestDeliveryPrepareBodyReplaysSniffedPrefix(t *testing.T) {
	for _, body := range []string{"\xef\xbb\xbf\r\n: keepalive\r\ndata: {}\r\n\r\n", "\n  {\"status\":\"completed\"}"} {
		source := &deliverySource{Reader: strings.NewReader(body)}
		response := &http.Response{Header: make(http.Header), Body: source}
		stream, err := responses.PrepareBody(response, true)
		if err != nil || stream != strings.Contains(body, "data:") {
			t.Fatalf("PrepareBody(%q) = %v, %v", body, stream, err)
		}
		replayed, err := io.ReadAll(response.Body)
		if err != nil || string(replayed) != body || source.closed {
			t.Fatalf("replayed %q, %v; closed %v", replayed, err, source.closed)
		}
		if err := response.Body.Close(); err != nil || !source.closed {
			t.Fatalf("replayed body close = %v, source closed %v", err, source.closed)
		}
	}
}
