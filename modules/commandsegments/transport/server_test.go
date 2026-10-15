package transport_test

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/commandsegments/transport"
)

const transportTimeout = 3 * time.Second

type reportProbe struct {
	messages chan transport.Message
	ended    chan struct{}
	aborted  chan struct{}
	endGate  <-chan struct{}
}

func newReportProbe() *reportProbe {
	return &reportProbe{messages: make(chan transport.Message, 16), ended: make(chan struct{}), aborted: make(chan struct{})}
}

func (p *reportProbe) Observe(m transport.Message) { p.messages <- m }
func (p *reportProbe) End() {
	close(p.ended)
	if p.endGate != nil {
		<-p.endGate
	}
}
func (p *reportProbe) Abort() { close(p.aborted) }

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(transportTimeout):
		t.Fatal("transport operation did not complete")
		var zero T
		return zero
	}
}

func listen(t *testing.T) *transport.Server {
	t.Helper()
	s, err := transport.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func register(t *testing.T, s *transport.Server, claim transport.Claim) {
	t.Helper()
	if err := s.Register(claim); err != nil {
		t.Fatal(err)
	}
}

type peer struct {
	report, reply *os.File
	work          string
}

func connect(t *testing.T, s *transport.Server) peer {
	t.Helper()
	e := s.Endpoints()
	report, reply, work, err := transport.OpenReport(e.Requests, e.Directory, transportTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { report.Close(); reply.Close() })
	return peer{report, reply, work}
}

func (p peer) send(t *testing.T, messages ...transport.Message) {
	t.Helper()
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.report.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func (p peer) accepted(t *testing.T) bool {
	t.Helper()
	deadline := time.Now().Add(transportTimeout)
	if err := p.reply.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(p.reply)
	for {
		data, err := reader.ReadBytes('\n')
		if len(data) != 0 {
			var reply transport.Reply
			if err := json.Unmarshal(data, &reply); err != nil {
				t.Fatal(err)
			}
			return reply.OK
		}
		// A newly opened nonblocking FIFO has EOF until the server opens
		// its writer. This is the helper's real connection sequence.
		if !errors.Is(err, io.EOF) || time.Now().After(deadline) {
			t.Fatalf("read acceptance reply: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func hello() transport.Message {
	return transport.Message{Type: transport.Hello, Version: transport.Protocol, Thread: "caller", Script: "echo first; echo second", Segments: []string{"echo first", "echo second"}}
}

func TestServerAcceptsOrderedReport(t *testing.T) {
	s := listen(t)
	probe := newReportProbe()
	claimed := make(chan transport.Message, 1)
	register(t, s, func(_ context.Context, m transport.Message) transport.Report {
		claimed <- m
		return probe
	})
	p := connect(t, s)
	wantHello := hello()
	p.send(t, wantHello)
	if !p.accepted(t) {
		t.Fatal("valid source sequence declined")
	}
	if got := receive(t, claimed); !reflect.DeepEqual(got, wantHello) {
		t.Fatalf("claim = %#v, want %#v", got, wantHello)
	}
	if script, err := os.ReadFile(filepath.Join(p.work, "script")); err != nil || len(script) == 0 {
		t.Fatalf("multi-segment helper script missing: %q, %v", script, err)
	}
	code := 0
	messages := []transport.Message{
		{Type: transport.Begin, Index: 0},
		{Type: transport.Output, Index: 0, Data: "first\n"},
		{Type: transport.End, Index: 0, Code: &code},
		{Type: transport.Lossy, Index: 1},
		{Type: transport.Done, Code: &code},
		{Type: transport.Closed},
	}
	p.send(t, messages...)
	receive(t, probe.ended)
	for _, want := range messages {
		if got := receive(t, probe.messages); !reflect.DeepEqual(got, want) {
			t.Fatalf("message = %#v, want %#v", got, want)
		}
	}
	select {
	case <-probe.aborted:
		t.Fatal("accepted report aborted")
	default:
	}
}

func TestServerRejectsUnverifiedSourcesBeforeClaim(t *testing.T) {
	for _, tc := range []struct {
		name     string
		segments []string
	}{
		{"reordered", []string{"echo second", "echo first"}},
		{"changed", []string{"echo first", "echo different"}},
		{"missing", []string{"echo first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := listen(t)
			claims := make(chan transport.Message, 2)
			register(t, s, func(_ context.Context, m transport.Message) transport.Report {
				claims <- m
				return nil
			})
			p := connect(t, s)
			m := hello()
			m.Segments = tc.segments
			p.send(t, m)
			if p.accepted(t) {
				t.Fatal("unverified source sequence accepted")
			}
			s.Close()
			if len(claims) != 0 {
				t.Fatal("claim ran before source verification")
			}
		})
	}
}

func TestServerRegistrationAndUnknownInput(t *testing.T) {
	s := listen(t)
	if err := s.Register(nil); err == nil {
		t.Fatal("nil consumer registered")
	}
	claims := make(chan transport.Message, 1)
	register(t, s, func(_ context.Context, m transport.Message) transport.Report {
		claims <- m
		return nil
	})
	if err := s.Register(func(context.Context, transport.Message) transport.Report { return nil }); err == nil {
		t.Fatal("second consumer registered")
	}
	for _, id := range []string{"../escape", "unknown", "", "0000"} {
		if err := transport.RequestReport(s.Endpoints().Requests, id); err != nil {
			t.Fatal(err)
		}
	}
	p := connect(t, s)
	p.send(t, hello())
	if p.accepted(t) {
		t.Fatal("unclaimed report accepted")
	}
	receive(t, claims)
	s.Close()
	if err := s.Register(func(context.Context, transport.Message) transport.Report { return nil }); err == nil {
		t.Fatal("closed consumer replaced")
	}
	s.Close()
	if len(claims) != 0 {
		t.Fatal("unknown input claimed")
	}
}

func TestServerCloseJoinsClaimAndAbortsUndeliveredAcceptance(t *testing.T) {
	s := listen(t)
	probe := newReportProbe()
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	register(t, s, func(ctx context.Context, _ transport.Message) transport.Report {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return probe
	})
	t.Cleanup(releaseOnce)
	p := connect(t, s)
	p.send(t, hello())
	receive(t, entered)
	p.reply.Close()
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	receive(t, cancelled)
	select {
	case <-closed:
		t.Fatal("shutdown returned before claim finished")
	default:
	}
	releaseOnce()
	receive(t, closed)
	receive(t, probe.aborted)
	select {
	case <-probe.ended:
		t.Fatal("undelivered acceptance ended instead of aborting")
	default:
	}
}

func TestServerShutdownJoinsAcceptedReaderAndRemovesResources(t *testing.T) {
	s := listen(t)
	probe := newReportProbe()
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	probe.endGate = release
	register(t, s, func(context.Context, transport.Message) transport.Report { return probe })
	t.Cleanup(releaseOnce)
	p := connect(t, s)
	p.send(t, hello())
	if !p.accepted(t) {
		t.Fatal("report declined")
	}
	closed := make(chan struct{}, 4)
	for range 4 {
		go func() { s.Close(); closed <- struct{}{} }()
	}
	receive(t, probe.ended)
	select {
	case <-closed:
		t.Fatal("shutdown returned before End finished")
	default:
	}
	releaseOnce()
	for range 4 {
		receive(t, closed)
	}
	for _, path := range []string{s.Endpoints().Requests, s.Endpoints().Directory, p.work} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("private resource remains at %s: %v", path, err)
		}
	}
	select {
	case <-probe.aborted:
		t.Fatal("accepted report aborted during shutdown")
	default:
	}
	s.Close()
}

func TestServerUnregisteredInputAndCancelledListen(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s, err := transport.Listen(ctx); !errors.Is(err, context.Canceled) || s != nil {
		t.Fatalf("Listen(cancelled) = %v, %v", s, err)
	}
	s := listen(t)
	if err := transport.RequestReport(s.Endpoints().Requests, "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := s.Register(func(context.Context, transport.Message) transport.Report {
		t.Error("closed, unregistered server claimed input")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Register(closed) = %v", err)
	}
}
