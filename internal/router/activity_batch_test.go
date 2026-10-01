package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestActivityBatchDecorativeHeadings(t *testing.T) {
	for _, heading := range []string{`printf '\nHerdr environment: '`, `printf '\nLocal instructions:\n'`, `echo 'Local instructions:'`, `printf '%s\n' 'Local instructions:'`, `echo '--- instructions ---'`,
		`printf '\nToday session files\n'`, `printf '\nReplay-named owned paths\n'`, `printf '\nRole recovery\n'`, `printf '\nImmediate log/snapshot directory names\n'`} {
		t.Run(heading, func(t *testing.T) {
			command := heading + "; printenv HERDR_ENV; cat AGENTS.md"
			text := toolActivityShell(command)
			if strings.Contains(text, heading) || !strings.Contains(text, "printenv HERDR_ENV") || !strings.Contains(text, "Read `AGENTS.md`") {
				t.Fatalf("batch preview: %q", text)
			}
			chain := toolActivityShell(heading + " && cat AGENTS.md")
			if chain != "Read `AGENTS.md`" {
				t.Fatalf("chain preview: %q", chain)
			}
			entry := activityPaneEntry{Kind: "tool", native: &liveActivityNativeItem{command: command, segments: []commandSegment{
				{source: heading, text: execSegmentText(heading), tail: []string{"Local instructions:"}},
				{source: "printenv HERDR_ENV", text: execSegmentText("printenv HERDR_ENV"), tail: []string{"1"}},
				{source: "cat AGENTS.md", text: execSegmentText("cat AGENTS.md")},
			}}}
			for _, running := range []bool{true, false} {
				entry.native.segments[0].running = running
				blocks := parseLiveActivity(entry)
				if len(blocks) != 2 || blocks[0].Code != "printenv HERDR_ENV" || strings.Join(blocks[0].Tail, "") != "1" {
					t.Fatalf("tracked: %+v", blocks)
				}
			}
			entry.native.segments[0].exit = 1
			if blocks := parseLiveActivity(entry); len(blocks) != 3 || blocks[0].ExitCode != 1 {
				t.Fatalf("failed heading lost: %+v", blocks)
			}
		})
	}
	for _, command := range []string{`printf '\nLocal instructions:\n'`, `echo 'Local instructions:'`, `cat a; echo "$heading:"`, `cat a; echo 'heading:' > out`, `cat a; printf '\theading:\n'`, `cat a; echo -n 'heading:'`,
		`printf 'Evidence-location metadata\n'`, `cat a; printf '%s\n' 'Evidence-location metadata'`, `cat a; printf 'Evidence-location metadata\n' > out`,
		`cat a; printf "$heading\n"`, `cat a; printf 'Evidence-location metadata'`, `cat a; printf 'count=42\n'`, `cat a; printf '{"result":"ok"}\n'`,
		`cat a; printf 'PASS\n'`, `cat a; printf '42\n'`, `cat a; printf '/tmp/session.log\n'`, `cat a; printf 'Evidence-location metadata\t\n'`} {
		if text := toolActivityShell(command); !strings.Contains(text, "Run") {
			t.Fatalf("meaningful command hidden: %s: %s", command, text)
		}
	}
}

func TestUISnapshotActivityPlainPrintfHeadings(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	const heading = `printf '\nToday session files\n'`
	const data = `printf 'Alice Smith\n'`
	const ambiguous = `printf 'Evidence-location metadata\n'`
	const command = heading + `; ls .codex/sessions/2026/09/30; ` + data + `; ` + ambiguous + `; printf '%s\n' 'count=42'`
	for _, state := range []string{"preview", "completed", "failed-heading"} {
		t.Run(state, func(t *testing.T) {
			entry := activityPaneEntry{Seq: 1, Agent: "/root/probe", Kind: "tool", Text: toolActivityShell(command), Observed: now,
				native: &liveActivityNativeItem{command: command}}
			if state != "preview" {
				entry.native.segments = []commandSegment{
					{source: heading, text: execSegmentText(heading), tail: []string{"Today session files"}},
					{source: "ls .codex/sessions/2026/09/30", text: execSegmentText("ls .codex/sessions/2026/09/30"), exit: 1,
						tail: []string{"ls: cannot access '.codex/sessions/2026/09/30': No such file or directory"}},
					{source: data, text: execSegmentText(data), tail: []string{"Alice Smith"}},
					{source: ambiguous, text: execSegmentText(ambiguous), tail: []string{"Evidence-location metadata"}},
					{source: `printf '%s\n' 'count=42'`, text: execSegmentText(`printf '%s\n' 'count=42'`), tail: []string{"count=42"}},
				}
				if state == "failed-heading" {
					entry.native.segments[0].exit = 1
					entry.native.segments[0].tail = []string{"printf: write error"}
				}
			}
			v := newLiveActivityView()
			v.painter.Theme = livediff.DarkTheme
			v.apply(activityPaneEvent{Kind: "snapshot", Agents: []activityPaneAgent{{Name: "/root/probe", Final: true}}, Entries: []activityPaneEntry{entry}})
			assertNativeUISnapshot(t, "activity-plain-printf-"+state, v.render(100, 18, now))
		})
	}
}

func TestActivityBatchPreservesUnframedPrintfData(t *testing.T) {
	for _, value := range []string{"Alice Smith", "Evidence-location metadata"} {
		command := "printf '" + value + `\n'`
		script := "cat profile.txt; " + command
		want := "Read `profile.txt`\n\nRun `" + command + "`"
		if got := toolActivityShell(script); got != want {
			t.Fatalf("unframed data preview: got %q, want %q", got, want)
		}
		entry := activityPaneEntry{Kind: "tool", native: &liveActivityNativeItem{command: script, segments: []commandSegment{
			{source: "cat profile.txt", text: execSegmentText("cat profile.txt"), tail: []string{"profile"}},
			{source: command, text: execSegmentText(command), tail: []string{value}},
		}}}
		blocks := parseLiveActivity(entry)
		if len(blocks) != 2 || blocks[1].Code != command || strings.Join(blocks[1].Tail, "\n") != value {
			t.Fatalf("unframed data/output lost: %+v", blocks)
		}
	}
}

func TestActivityBatchTimingEvidence(t *testing.T) {
	for _, command := range []string{"sleep 1; sleep 2", "sleep 1 && sleep 2", "printf 'Heading:'; sleep 2", "trap true EXIT; sleep 2"} {
		for _, wrapped := range []bool{false, true} {
			source := command
			if wrapped {
				source = "/bin/bash -lc " + quoteShellWord(command)
			}
			entry := activityPaneEntry{Kind: "tool", Text: "Run `sleep 2`", Observed: time.Now().Add(-time.Second), native: &liveActivityNativeItem{command: source, duration: 3 * time.Second}}
			for _, running := range []bool{false, true} {
				entry.native.running = running
				for _, block := range parseLiveActivity(entry) {
					if block.Duration != 0 || !block.Started.IsZero() || activityui.RunElapsed(block, time.Now()) != "" {
						t.Fatalf("aggregate attributed to command: %+v", block)
					}
				}
			}
		}
	}
	entry := activityPaneEntry{Kind: "tool", Text: "Run `sleep 2`", native: &liveActivityNativeItem{command: "sleep 2", duration: 2 * time.Second}}
	blocks := parseLiveActivity(entry)
	if len(blocks) != 1 || activityui.RunElapsed(blocks[0], time.Now()) != "2s" {
		t.Fatalf("single command evidence lost: %+v", blocks)
	}
}

func TestActivitySingleCommandObservedTimestamps(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	started := time.Now()
	item := map[string]any{"id": "timestamps", "type": "commandExecution", "command": "sleep .01", "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	item["status"], item["durationMs"], item["exitCode"] = "completed", 123, 0
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	for _, entry := range u.view.entries {
		if entry.CallID != "timestamps" || entry.native == nil {
			continue
		}
		blocks := parseLiveActivity(entry)
		if len(blocks) != 1 || blocks[0].Started.Before(started) || blocks[0].Ended.Before(blocks[0].Started) || blocks[0].Duration != 123*time.Millisecond {
			t.Fatalf("observed host timestamps/duration: %+v", blocks)
		}
		return
	}
	t.Fatal("command missing")
}

func TestActivityBatchRestoresVCSRows(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u.proxy = &mekugiProxy{replayStore: store}
	output := "2\t1\ta.go\n"
	item := appServerItem{ID: "vcs", Type: "commandExecution", Command: "bash -lc 'echo before; git diff --numstat'", ExitCode: new(0), AggregatedOutput: new("before\n" + output)}
	entry := activityPaneEntry{native: &liveActivityNativeItem{thread: "main", turn: "t", item: item.ID}}
	first, second := u.session.outputs.New(), u.session.outputs.New()
	first.Finish(new("before\n"), new(0))
	second.Finish(&output, new(0))
	u.retainCommandSegments(entry, item, execTrackView{complete: true, output: true, segments: []commandSegment{{output: first}, {output: second, raw: output}}})
	awaitCommandSegments(t, u)
	u.restoreCommandSegments(&entry, item, u.session.cwd)
	if len(entry.native.segments) != 2 {
		t.Fatalf("missing segments: %+v", entry.native.segments)
	}
	rows := entry.native.segments[1].changes
	if len(rows) != 1 || rows[0].Label != "a.go" || rows[0].Added != 2 || rows[0].Removed != 1 {
		t.Fatalf("VCS rows lost on replay: %+v", rows)
	}
}
