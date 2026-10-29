package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func TestActivityBatchDecorativeHeadings(t *testing.T) {
	for _, heading := range []string{`printf '\nHerdr environment: '`, `printf '\nLocal instructions:\n'`, `echo 'Local instructions:'`, `printf '%s\n' 'Local instructions:'`, `echo '--- instructions ---'`,
		`printf '\nToday session files\n'`, `printf '\nReplay-named owned paths\n'`, `printf '\nRole recovery\n'`, `printf '\nImmediate log/snapshot directory names\n'`,
		`printf '\nGUIDANCE\n'`, `printf '\nAPPROVAL\n'`,
		`printf '265 status\n'`, `printf '265 integration\n'`, `printf '266 status\n'`, `printf '266 integration\n'`, `printf 'main pre-existing changes\n'`,
		`mrun -n 5 printf '265 status\n'`, `env timeout 1 mrun -n 5 printf '265 status\n'`} {
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
			if got := toolActivityShell("cat before && " + heading + " && cat AGENTS.md"); got != "Read `before`\n\nRead `AGENTS.md`" {
				t.Fatalf("nested chain preview: %q", got)
			}
			if got := toolActivityShell(heading + `; cat "$file"`); got != "Read `\"$file\"`" {
				t.Fatalf("dynamic operand preview: %q", got)
			}
			entry := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{Command: command, Segments: []sessionpresentation.ActivitySegment{
				{Source: heading, Text: execSegmentText(heading), Tail: []string{"Local instructions:"}},
				{Source: "printenv HERDR_ENV", Text: execSegmentText("printenv HERDR_ENV"), Tail: []string{"1"}},
				{Source: "cat AGENTS.md", Text: execSegmentText("cat AGENTS.md")},
			}}}
			for _, running := range []bool{true, false} {
				entry.Native.Segments[0].Running = running
				blocks := parseLiveActivity(entry)
				if len(blocks) != 2 || blocks[0].Code != "printenv HERDR_ENV" || strings.Join(blocks[0].Tail, "") != "1" {
					t.Fatalf("tracked: %+v", blocks)
				}
			}
			entry.Native.Segments[0].Exit = 1
			if blocks := parseLiveActivity(entry); len(blocks) != 3 || blocks[0].ExitCode != 1 {
				t.Fatalf("failed heading lost: %+v", blocks)
			}
			entry.Native.Segments[0].Exit, entry.Native.Segments[0].Skipped = 0, true
			if blocks := parseLiveActivity(entry); len(blocks) != 3 || !blocks[0].Skipped {
				t.Fatalf("skipped heading lost: %+v", blocks)
			}
		})
	}
	for _, command := range []string{`printf '\nLocal instructions:\n'`, `echo 'Local instructions:'`, `cat a; echo "$heading:"`, `cat a; echo 'heading:' > out`, `cat a; printf '\theading:\n'`, `cat a; echo -n 'heading:'`,
		`printf 'Evidence-location metadata\n'`, `cat a; printf '%s\n' 'Evidence-location metadata'`, `cat a; printf 'Evidence-location metadata\n' > out`,
		`cat a; printf "$heading\n"`, `cat a; printf 'Evidence-location metadata'`, `cat a; printf 'count=42\n'`, `cat a; printf '{"result":"ok"}\n'`,
		`cat a; printf 'PASS\n'`, `cat a; printf '42\n'`, `cat a; printf '/tmp/session.log\n'`, `cat a; printf 'Evidence-location metadata\t\n'`,
		`printf '\nGUIDANCE\n'`, `cat a; printf 'GUIDANCE\n'`, `cat a; printf '\nAlice\n'`,
		`printf '265 status\n'`, `cat a; printf '265 status\n'`, `printf '265 status\n'; echo done`,
		`printf '265 status\n'; printf 'Alice Smith\n'; cat a`, `printf '12 34\n'; cat a`,
		`printf '%s\n' '265 status'; cat a`, `printf '265 status\n' > out; cat a`, `printf "$heading\n"; cat a`,
		`printf '265 status\t\n'; cat a`, `printf '265 status\n' | cat`,
		`cat a; printf '\n%s\n' GUIDANCE`, `cat a; printf '\nGUIDANCE\n' > out`} {
		if text := toolActivityShell(command); !strings.Contains(text, "Run") {
			t.Fatalf("meaningful command hidden: %s: %s", command, text)
		}
	}
}

func TestActivityBatchSectionLabelSuccessors(t *testing.T) {
	const heading = `printf '265 status\n'`
	for _, tc := range []struct {
		next string
		hide bool
	}{
		{`cat a && cat b`, true},
		{`env timeout 1 cat "$file"`, true},
		{`env env printf 'Alice Smith\n'`, false},
		{`env timeout 1 printf 'Alice Smith\n'`, false},
		{`command printf 'Alice Smith\n'`, false},
		{`builtin printf 'Alice Smith\n'`, false},
		{`command echo done`, false},
		{`mrun -n 5 env timeout 1 echo done`, false},
		{`env "$program"`, false},
		{`printf 'Alice Smith\n' && cat a`, false},
	} {
		script := heading + "; " + tc.next
		if got := toolActivityShell(script); strings.Contains(got, heading) == tc.hide {
			t.Fatalf("preview %q: %q", script, got)
		}
		segments, ok := shellinstrumentation.Split(script)
		if !ok {
			t.Fatalf("untrackable script: %q", script)
		}
		entry := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{Command: script}}
		for _, segment := range segments {
			entry.Native.Segments = append(entry.Native.Segments, sessionpresentation.ActivitySegment{Source: segment.Source, Text: execSegmentText(segment.Source)})
		}
		found := false
		for _, block := range parseLiveActivity(entry) {
			found = found || strings.Contains(block.Code, heading)
		}
		if found == tc.hide {
			t.Fatalf("tracked heading hidden=%t, want %t: %q", !found, tc.hide, script)
		}
	}
}

func TestActivityBatchSessionDiscoveryHeadings(t *testing.T) {
	for _, tc := range []struct{ heading, before, next, preview, target string }{
		{`printf 'Session files\n'`, "", `rg --files --hidden sessions 2>/dev/null | rg 'session|manifest' | head -60`, "List `sessions`", "sessions"},
		{`printf '\nState directories\n'`, "python3 - <<'PY'\nprint('session_meta')\nPY\n", `ls -d state/* share/* 2>/dev/null`, "List `state/*`", "state/*"},
	} {
		for _, join := range []string{"; ", " && "} {
			t.Run(tc.heading+join, func(t *testing.T) {
				script := tc.before + tc.heading + join + tc.next
				if got := toolActivityShell(script); strings.Contains(got, tc.heading) || !strings.Contains(got, tc.preview) {
					t.Fatalf("discovery preview retained heading or lost operation: %q", got)
				}
				segments, ok := shellinstrumentation.Split(script)
				if !ok {
					t.Fatalf("untrackable discovery: %q", script)
				}
				entry := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{Command: script}}
				for _, segment := range segments {
					entry.Native.Segments = append(entry.Native.Segments, sessionpresentation.ActivitySegment{Source: segment.Source, Text: execSegmentText(segment.Source)})
				}
				found := false
				for _, block := range parseLiveActivity(entry) {
					if strings.Contains(block.Code, tc.heading) {
						t.Fatalf("tracked discovery retained heading: %+v", block)
					}
					found = found || strings.Contains(block.Code+block.Label, tc.target)
					for _, target := range block.Reads {
						found = found || strings.Contains(target.Path, tc.target)
					}
				}
				if !found {
					t.Fatalf("tracked discovery lost %q", tc.target)
				}
			})
		}
	}
}

func TestUISnapshotActivityPlainPrintfHeadings(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	const heading = `printf '265 status\n'`
	const label = `printf '\nGUIDANCE\n'`
	const data = `printf 'Alice Smith\n'`
	const ambiguous = `printf 'Evidence-location metadata\n'`
	const command = label + `; ` + heading + `; ls .codex/sessions/2026/09/30; ` + data + `; ` + ambiguous + `; printf '%s\n' 'count=42'`
	for _, state := range []string{"preview", "completed", "failed-heading"} {
		t.Run(state, func(t *testing.T) {
			entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: "/root/probe", Kind: "tool", Text: toolActivityShell(command), Observed: now,
				Native: &sessionpresentation.NativeItem{Command: command}}
			if state != "preview" {
				entry.Native.Segments = []sessionpresentation.ActivitySegment{
					{Source: label, Text: execSegmentText(label), Tail: []string{"GUIDANCE"}},
					{Source: heading, Text: execSegmentText(heading), Tail: []string{"265 status"}},
					{Source: "ls .codex/sessions/2026/09/30", Text: execSegmentText("ls .codex/sessions/2026/09/30"), Exit: 1,
						Tail: []string{"ls: cannot access '.codex/sessions/2026/09/30': No such file or directory"}},
					{Source: data, Text: execSegmentText(data), Tail: []string{"Alice Smith"}},
					{Source: ambiguous, Text: execSegmentText(ambiguous), Tail: []string{"Evidence-location metadata"}},
					{Source: `printf '%s\n' 'count=42'`, Text: execSegmentText(`printf '%s\n' 'count=42'`), Tail: []string{"count=42"}},
				}
				if state == "failed-heading" {
					entry.Native.Segments[1].Exit = 1
					entry.Native.Segments[1].Tail = []string{"printf: write error"}
				}
			}
			v := newActivityAdapter()
			v.Painter.Theme = diffrender.DarkTheme
			v.Coverage = ""
			v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/probe", Final: true}}, Entries: []sessionpresentation.ActivityEntry{entry}})
			assertNativeUISnapshot(t, "activity-plain-printf-"+state, v.Render(100, 18, now))
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
		entry := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{Command: script, Segments: []sessionpresentation.ActivitySegment{
			{Source: "cat profile.txt", Text: execSegmentText("cat profile.txt"), Tail: []string{"profile"}},
			{Source: command, Text: execSegmentText(command), Tail: []string{value}},
		}}}
		blocks := parseLiveActivity(entry)
		if len(blocks) != 2 || blocks[1].Code != command || strings.Join(blocks[1].Tail, "\n") != value {
			t.Fatalf("unframed data/output lost: %+v", blocks)
		}
	}
}

func TestActivityBatchLiteralSuccessMarkers(t *testing.T) {
	for _, marker := range []string{
		`printf 'Recovered rich-cli dependency-lock evidence remains available.\n'`,
		`printf 'Final syntax and whitespace checks passed.\n'`,
	} {
		command := "test -f evidence.txt && " + marker
		if got := toolActivityShell(command); got != "Run `test -f evidence.txt`" {
			t.Fatalf("success marker leaked: %q", got)
		}
		if got := toolActivityShell(marker); !strings.Contains(got, marker) {
			t.Fatalf("standalone output hidden: %q", got)
		}
		entry := sessionpresentation.ActivityEntry{Kind: "tool", Native: &sessionpresentation.NativeItem{Command: command, Segments: []sessionpresentation.ActivitySegment{
			{Source: "test -f evidence.txt", Text: execSegmentText("test -f evidence.txt")},
			{Source: marker, Text: execSegmentText(marker)},
		}}}
		for _, state := range []string{"running", "completed", "failed", "skipped"} {
			segment := &entry.Native.Segments[1]
			segment.Running, segment.Skipped, segment.Exit = state == "running", state == "skipped", 0
			if state == "failed" {
				segment.Exit = 1
			}
			blocks := parseLiveActivity(entry)
			if state == "failed" || state == "skipped" {
				if len(blocks) != 2 || blocks[1].Code != marker || blocks[1].ExitCode != segment.Exit || blocks[1].Skipped != segment.Skipped {
					t.Fatalf("%s marker lost: %+v", state, blocks)
				}
			} else if len(blocks) != 1 {
				t.Fatalf("%s marker leaked: %+v", state, blocks)
			}
		}
	}
	for _, command := range []string{
		`printf '%s\n' 'Final syntax checks passed.'`,
		`printf 'Final syntax checks passed.\n' > results.txt`,
		`printf "$status\n"`, `printf 'Final syntax checks passed.'`,
		`printf 'Alice Smith\n'`, `printf 'Final score: 42\n'`,
		`printf '{"checks":"passed"}\n'`, `printf 'Final syntax checks passed.\t\n'`,
	} {
		if got := toolActivityShell("test -f evidence.txt && " + command); !strings.Contains(got, command) {
			t.Fatalf("data-bearing operation hidden: %s: %s", command, got)
		}
	}
}

func TestUISnapshotActivityPrintfSuccessMarkers(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, producer := range []struct {
		name  string
		parts []string
	}{
		{"recovered", []string{"test -f /tmp/.tmpw4Pr3l/pyproject.toml", "test -f /tmp/.tmpw4Pr3l/uv.lock", `printf 'Recovered rich-cli dependency-lock evidence remains available.\n'`}},
		{"validated", []string{"bash -n setup.sh .local/tests/setup_config_test.sh", "git diff --check -- setup.sh setup.changelog.md README.md .local/tests/setup_config_test.sh", `printf 'Final syntax and whitespace checks passed.\n'`}},
	} {
		for _, state := range []string{"preview", "completed", "failed-marker"} {
			t.Run(producer.name+"/"+state, func(t *testing.T) {
				script := strings.Join(producer.parts, " && ")
				command := "/usr/bin/bash -lc " + quoteShellWord(script)
				entry := sessionpresentation.ActivityEntry{Seq: 1, Agent: "/root/probe", Kind: "tool", Text: toolActivityShell(script), Observed: now,
					Native: &sessionpresentation.NativeItem{Command: command}}
				if state != "preview" {
					for _, source := range producer.parts {
						entry.Native.Segments = append(entry.Native.Segments, sessionpresentation.ActivitySegment{Source: source, Text: execSegmentText(source)})
					}
					if state == "failed-marker" {
						last := &entry.Native.Segments[len(entry.Native.Segments)-1]
						last.Exit, last.Tail = 1, []string{"printf: write error"}
					}
				}
				v := newActivityAdapter()
				v.Painter.Theme = diffrender.DarkTheme
				v.Coverage = ""
				v.Apply(sessionpresentation.ActivityEvent{Agents: []sessionpresentation.ActivityAgent{{Name: "/root/probe", Final: true}}, Entries: []sessionpresentation.ActivityEntry{entry}})
				assertNativeUISnapshot(t, "activity-printf-success-"+producer.name+"-"+state, v.Render(100, 18, now))
			})
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
			entry := sessionpresentation.ActivityEntry{Kind: "tool", Text: "Run `sleep 2`", Observed: time.Now().Add(-time.Second), Native: &sessionpresentation.NativeItem{Command: source, Duration: 3 * time.Second}}
			for _, running := range []bool{false, true} {
				entry.Native.Running = running
				for _, block := range parseLiveActivity(entry) {
					if !block.BatchExit && (block.Duration != 0 || !block.Started.IsZero() || activityui.RunElapsed(block, time.Now()) != "") {
						t.Fatalf("aggregate attributed to command: %+v", block)
					}
				}
			}
		}
	}
	entry := sessionpresentation.ActivityEntry{Kind: "tool", Text: "Run `sleep 2`", Native: &sessionpresentation.NativeItem{Command: "sleep 2", Duration: 2 * time.Second}}
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
	for _, entry := range u.attached.source.Entries {
		if entry.Source.CallID != "timestamps" || entry.Source.Native == nil {
			continue
		}
		blocks := parseLiveActivity(entry.Source)
		if len(blocks) != 1 || !blocks[0].NotificationTiming || blocks[0].Started.Before(started) || blocks[0].Ended.Before(blocks[0].Started) || blocks[0].Duration != 123*time.Millisecond {
			t.Fatalf("notification timestamps/host duration: %+v", blocks)
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
	setTestAppServerIntegration(u, testProxyWithStore(store))
	output := "2\t1\ta.go\n"
	item := binding.ThreadItem{ID: "vcs", Type: "commandExecution", Command: "bash -lc 'echo before; git diff --numstat'", ExitCode: new(0), AggregatedOutput: new("before\n" + output)}
	entry := sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Thread: "main", Turn: "t", Item: item.ID}}
	first, second := u.activity.Outputs(u.attached.session.Scope()).New(), u.activity.Outputs(u.attached.session.Scope()).New()
	first.Finish(new("before\n"), new(0))
	second.Finish(&output, new(0))
	retainCommandSegments(u.ctx, u.retained, u.segments, &u.attached, &u.commandSegmentWrites, &u.commandSegmentPending, u.issues, entry, item.Observation(), execTrackView{complete: true, output: true, segments: []sessionpresentation.ActivitySegment{{Output: first}, {Output: second, Raw: output}}})
	awaitCommandSegments(t, u)
	restoreCommandSegments(u.ctx, u.retained, u.segments, &u.attached, u.activity, u.restoredSegments, &entry, item, u.attached.cwd)
	if len(entry.Native.Segments) != 2 {
		t.Fatalf("missing segments: %+v", entry.Native.Segments)
	}
	rows := entry.Native.Segments[1].Changes
	if len(rows) != 1 || rows[0].Label != "a.go" || rows[0].Added != 2 || rows[0].Removed != 1 {
		t.Fatalf("VCS rows lost on replay: %+v", rows)
	}
}
