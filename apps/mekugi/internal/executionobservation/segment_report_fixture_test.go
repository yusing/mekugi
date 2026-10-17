package executionobservation

import (
	"bufio"

	"github.com/yusing/mekugi/libraries/commandargs"

	"context"

	"github.com/yusing/mekugi/backends/codex/binding"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/modules/commandsegments/segments"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	json "encoding/json/v2"

	"errors"

	"os"

	"testing"

	"time"

	"golang.org/x/sys/unix"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

type execTrackReport struct {
	t    *testing.T
	conn *os.File
}

func dialExecTrackReport(t *testing.T, hub *execTrackFixture, script string) *execTrackReport {
	return dialExecTrackReportThread(t, hub, "main", script)
}

func dialExecTrackReportThread(t *testing.T, hub *execTrackFixture, thread, script string) *execTrackReport {
	t.Helper()
	conn, answer, _, err := segmenttransport.OpenReport(hub.server.Endpoints().Requests, hub.server.Endpoints().Directory, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); answer.Close() })
	segments, ok := shellinstrumentation.Split(script)
	if !ok {
		t.Fatalf("script %q is not tracked", script)
	}
	var sources []string
	for _, segment := range segments {
		sources = append(sources, segment.Source)
	}
	r := &execTrackReport{t: t, conn: conn}
	r.send(segmenttransport.Message{Type: segmenttransport.Hello, Version: segmenttransport.Protocol, Thread: thread, Script: script, Segments: sources})
	deadline := time.Now().Add(time.Second)
	for {
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(answer.Fd()), Events: unix.POLLIN}}, max(0, int(time.Until(deadline).Milliseconds())))
		if errors.Is(err, unix.EINTR) && time.Now().Before(deadline) {
			continue
		}
		if err != nil || n == 0 {
			t.Fatalf("reply not ready: %d, %v", n, err)
		}
		break
	}
	line, err := bufio.NewReader(answer).ReadBytes('\n')
	if err != nil || string(line) != "{\"ok\":true}\n" {
		t.Fatalf("reply = %q, %v", line, err)
	}
	return r
}

func (r *execTrackReport) send(messages ...segmenttransport.Message) {
	r.t.Helper()
	for _, message := range messages {
		line, err := json.Marshal(message)
		if err != nil {
			r.t.Fatal(err)
		}
		if _, err := r.conn.Write(append(line, '\n')); err != nil {
			r.t.Fatal(err)
		}
	}
}

type execTrackFixture struct {
	*segments.Observer
	server *segmenttransport.Server
}

func newExecTrackFixture(t *testing.T, ctx context.Context, instrumentation segments.Instrumentation) *execTrackFixture {
	t.Helper()
	server, err := segmenttransport.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	observer, err := segments.NewObserver(server, instrumentation, activitymodel.OutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	return &execTrackFixture{Observer: observer, server: server}
}
func (h *execTrackFixture) start(key [3]string, command string) {
	if script, ok := binding.CommandScript(command); ok {
		h.Start(key, script)
	}
}
func (h *execTrackFixture) begin(t *testing.T, key [3]string, script string) *execTrackReport {
	t.Helper()
	h.start(key, "bash -lc "+quoteShellWord(script))
	return dialExecTrackReportThread(t, h, key[0], script)
}
func (h *execTrackFixture) await(t *testing.T, key [3]string, ready func(segments.Report) bool) segments.Report {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		view, _ := h.Read(key)
		if ready(view) {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("report did not reach expected state: %+v", view)
		}
		time.Sleep(time.Millisecond)
	}
}
func quoteShellWord(value string) string { return commandargs.QuoteArgument(value) }
