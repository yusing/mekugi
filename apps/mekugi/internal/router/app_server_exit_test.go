package router

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotAppServerExitSummaryUsage(t *testing.T) {
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
	uisnapshot.Assert(t, "testdata/snapshots/native-exit-usage.txt", out.String())
}

func TestUISnapshotAppServerExitSummaryOptionalCounts(t *testing.T) {
	for _, tc := range []struct {
		name, thread string
		usage        binding.TokenUsage
	}{
		{name: "startup failure"},
		{name: "no usage", thread: "saved"},
		{name: "no optional counts", usage: binding.TokenUsage{TotalTokens: 1200, InputTokens: 1100, OutputTokens: 100}},
		{name: "cached exceeds input", usage: binding.TokenUsage{TotalTokens: 15, InputTokens: 10, CachedInputTokens: 20, OutputTokens: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := appServerTestFeatures(&appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, Thread: tc.thread, exitUsage: tc.usage})
			if tc.name == "startup failure" {
				u.resumeArgv = []string{"mekugi", "--debug", "codex", "--yolo"}
				u.replayDebugDirectory = "/tmp/debug"
			}
			var out bytes.Buffer
			if err := u.writeExitSummary(&out, false); err != nil {
				t.Fatal(err)
			}
			uisnapshot.Assert(t, "testdata/snapshots/native-exit-"+strings.ReplaceAll(tc.name, " ", "-")+".txt", out.String())
		})
	}
	if err := (appServerTestFeatures(&appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, Thread: "main"})).writeExitSummary(exitFailWriter{}, false); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error = %v", err)
	}
}

type exitFailWriter struct{}

func (exitFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAppServerExitSummaryHighlight(t *testing.T) {
	u := appServerTestFeatures(&appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, Thread: "saved"})
	var out bytes.Buffer
	if err := u.writeExitSummary(&out, true); err != nil {
		t.Fatal(err)
	}
	want := "To continue this session, run:\n  \x1b[36mmekugi codex --yolo resume saved\x1b[39m\nTo replay this session offline, run:\n  \x1b[36mmekugi replay-session --session saved\x1b[39m\n"
	if out.String() != want {
		t.Fatalf("highlighted summary = %q, want %q", out.String(), want)
	}
}

func TestUISnapshotAppServerExitSummaryOriginalArgv(t *testing.T) {
	u := appServerTestFeatures(&appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, Thread: "saved", resumeArgv: []string{"/opt/my tools/mekugi", "--debug", "codex", "--yolo", "--enable", "instant_interrupt", "--high", "-c", "model='custom'"}, replayDebugDirectory: "/tmp/debug directory"})
	var out bytes.Buffer
	if err := u.writeExitSummary(&out, false); err != nil {
		t.Fatal(err)
	}
	uisnapshot.Assert(t, "testdata/snapshots/native-exit-original-argv.txt", out.String())
}

func TestAppServerExitSummaryArgvRoundTrip(t *testing.T) {
	argv := []string{"/opt/my tools/mekugi", "codex", "--yolo", "--high", "-c", "quoted='value'", "", "$(false); `false`", "tab\tline\n\x1b[2J"}
	u := appServerTestFeatures(&appServerUI{session: appServerSession{activity: new(activitymodel.Service)}, Thread: "saved", resumeArgv: argv, replayDebugDirectory: "/tmp/debug 'quoted'; $(false)"})
	var out bytes.Buffer
	if err := u.writeExitSummary(&out, false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for i, want := range [][]string{
		append(append([]string{}, argv...), "resume", "saved"),
		{argv[0], "replay-session", "--session", "saved", "--debug-dir", u.replayDebugDirectory},
	} {
		command := strings.TrimPrefix(lines[i*2+1], "  ")
		got, err := exec.Command(execTrackShellExecutable(t, "bash"), "-c", "set -- "+command+"; printf '%s\\0' \"$@\"").CombinedOutput()
		if err != nil {
			t.Fatalf("argv round trip: %v: %s", err, got)
		}
		if string(got) != strings.Join(want, "\x00")+"\x00" {
			t.Fatalf("argv round trip: %q, want %q", got, want)
		}
	}
}
