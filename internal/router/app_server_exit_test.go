package router

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestAppServerExitSummaryUsage(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	notify := func(thread string, input, cached, output, reasoning uint64) {
		appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{
			"threadId": thread, "tokenUsage": map[string]any{"total": map[string]any{
				"totalTokens": input + output, "inputTokens": input, "cachedInputTokens": cached,
				"outputTokens": output, "reasoningOutputTokens": reasoning,
			}},
		})
	}
	notify("main", 100, 0, 10, 0)
	notify("main", 25304, 9984, 1030, 852)
	notify("child", 999999, 0, 999999, 0)
	var out bytes.Buffer
	if err := u.writeExitSummary(&out, false); err != nil {
		t.Fatal(err)
	}
	want := "Token usage: total=16,350 input=15,320 (+ 9,984 cached) output=1,030 (reasoning 852)\nTo continue this session, run:\n  mekugi codex --yolo resume main\n"
	if out.String() != want {
		t.Fatalf("exit summary = %q, want %q", out.String(), want)
	}
}

func TestAppServerExitSummaryEmptyAndOptionalCounts(t *testing.T) {
	for _, tc := range []struct {
		name, thread string
		usage        appServerTokenUsage
		want         string
	}{
		{name: "startup failure"},
		{name: "no usage", thread: "saved", want: "To continue this session, run:\n  mekugi codex --yolo resume saved\n"},
		{name: "no optional counts", usage: appServerTokenUsage{TotalTokens: 1200, InputTokens: 1100, OutputTokens: 100}, want: "Token usage: total=1,200 input=1,100 output=100\n"},
		{name: "cached exceeds input", usage: appServerTokenUsage{TotalTokens: 15, InputTokens: 10, CachedInputTokens: 20, OutputTokens: 5}, want: "Token usage: total=5 input=0 (+ 20 cached) output=5\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &appServerUI{thread: tc.thread, exitUsage: tc.usage}
			var out bytes.Buffer
			if err := u.writeExitSummary(&out, false); err != nil || out.String() != tc.want {
				t.Fatalf("summary = %q, error = %v; want %q", out.String(), err, tc.want)
			}
		})
	}
	if err := (&appServerUI{thread: "main"}).writeExitSummary(exitFailWriter{}, false); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error = %v", err)
	}
}

type exitFailWriter struct{}

func (exitFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAppServerExitSummaryHighlight(t *testing.T) {
	u := &appServerUI{thread: "saved"}
	var out bytes.Buffer
	if err := u.writeExitSummary(&out, true); err != nil {
		t.Fatal(err)
	}
	want := "To continue this session, run:\n  \x1b[36mmekugi codex --yolo resume saved\x1b[39m\n"
	if out.String() != want {
		t.Fatalf("highlighted summary = %q, want %q", out.String(), want)
	}
}
