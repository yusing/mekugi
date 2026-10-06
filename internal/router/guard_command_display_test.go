package router

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/vcsguard"
)

const displayGuardDirectory = "/tmp/mekugi-tools-222415842-41c84ab42cecccced7ab3f107aa419ed127f6777c2b00517efb95c6723c6f413/vcs-guard"
const displayGuardHelper = "/opt/mekugi/bin/mekugi-exec"

func TestGuardCommandDisplayProjection(t *testing.T) {
	for _, source := range []string{
		"git log -1 --format='%H%n%s'",
		"git commit -F - <<'EOF'\nfix: quoted message\n\nKeep `$PATH`, \"quotes\", and \\ escapes literal.\nEOF",
		"git status --porcelain",
		`git cat-file commit HEAD | awk '/^gpgsig / {print "signature_present"; exit}'`,
		`PATH='/user tools' TOKEN="$token" git status; printf '%s' 'git status'`,
		`command -p git status && env -i '/user tools/git' log; "$tools/git" show HEAD`,
		`sh -c 'git status'; echo "$(git log -1)"; cat <<'EOF'
git status
EOF`,
	} {
		guarded, err := vcsguard.Rewrite(source, displayGuardHelper, displayGuardDirectory)
		if err != nil {
			t.Fatal(err)
		}
		if guarded == source {
			t.Fatalf("fixture was not instrumented: %s", source)
		}
		want := toolActivityShell(source)
		if got := execSegmentText(guarded); got != want {
			t.Fatalf("tracked display = %q, want %q", got, want)
		}
	}
}

func TestAppServerGuardCommandDisplay(t *testing.T) {
	source := "git log -1; git status --porcelain"
	guarded, err := vcsguard.Rewrite(source, displayGuardHelper, displayGuardDirectory)
	if err != nil {
		t.Fatal(err)
	}
	want := toolActivityShell(source)
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			command := workerCommand("/bin/bash", []string{"-lc", guarded})
			item := appServerItem{ID: "cmd", Type: "commandExecution", Command: command, Status: "completed", ExitCode: new(0)}
			check := func(u *appServerUI) {
				t.Helper()
				view := u.view
				if thread == "child" {
					view = u.agents
				}
				if len(view.entries) != 1 || view.entries[0].Text != want || view.entries[0].native.command != command {
					t.Fatalf("command entries = %+v; want display %q with unchanged host command", view.entries, want)
				}
			}
			u := newAppServerSessionTestUI(t, t.TempDir())
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "t", "item": item})
				check(u)
			}
			restored := newAppServerSessionTestUI(t, t.TempDir())
			turns := []appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}}
			if thread == "main" {
				restored.restoreHistory(turns)
			} else {
				info := appServerThreadInfo{ID: thread, AgentNickname: "worker", Turns: turns}
				restored.session.registerThread(info)
				restored.restoreActivityThread(info)
			}
			check(restored)
		})
	}
}

func TestGuardCommandDisplayPreservesUserSource(t *testing.T) {
	prefix := "PATH='" + displayGuardDirectory + "':\"${PATH-/bin:/usr/bin}\" "
	for _, source := range []string{
		`PATH='/user/vcs-guard':"${PATH-/bin:/usr/bin}" git status`,
		`PATH='/tmp/mekugi-tools-user/vcs-guard':"${PATH-/bin:/usr/bin}" git status`,
		"PATH='" + displayGuardDirectory + "':\"$PATH\" git status",
		prefix + "echo git status",
		"printf '%s' " + quoteShellWord(prefix+"git status"),
		"cat <<'EOF'\n" + prefix + "git status\nEOF",
		prefix + "git 'unfinished",
		quoteShellWord(displayGuardHelper) + " --other-mode " + quoteShellWord(displayGuardDirectory) + " git status",
	} {
		if got, want := toolActivityShell(source), toolActivityShellLanguage(source, "bash"); got != want {
			t.Fatalf("user source changed: %q; display = %q, want %q", source, got, want)
		}
	}
}

func TestGuardCommandDisplayCustomRuntimeDirectory(t *testing.T) {
	directory := strings.Replace(displayGuardDirectory, "/tmp/", "/run/user's tools/", 1)
	for _, source := range []string{"git status", "command git status"} {
		guarded, err := vcsguard.Rewrite(source, displayGuardHelper, directory)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := toolActivityShell(guarded), toolActivityShell(source); got != want {
			t.Fatalf("display = %q, want %q", got, want)
		}
	}
}

func TestUISnapshotGuardedHeredocCommit(t *testing.T) {
	source := "timeout 90s git commit -F - <<'EOF'\ndocs(ui): describe content-detected inline highlighting\nEOF"
	guarded, err := vcsguard.Rewrite(source, displayGuardHelper, displayGuardDirectory)
	if err != nil {
		t.Fatal(err)
	}
	command := workerCommand("/bin/bash", []string{"-lc", guarded})
	output := "[main e9085377] docs(ui): describe content-detected inline highlighting\n 2 files changed, 6 insertions(+), 4 deletions(-)\n"
	u := vcsCommandUI(t, "")
	u.view.painter.Theme = livediff.DarkTheme
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "cmd", Type: "commandExecution", Command: command, AggregatedOutput: &output, ExitCode: new(0), DurationMS: new(int64(500)),
	}}}})
	if entry := u.view.entries[0]; entry.native.command != command {
		t.Fatal("host command changed")
	}
	assertNativeUISnapshot(t, "guarded-heredoc-commit", strings.Split(mainFeed(u, 90), "\n"))
}

func TestUISnapshotGuardCommandDisplay(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	u.view.painter.Theme = livediff.DarkTheme
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	source := "git log -1 --format='%H%n%s'; git status --porcelain; git cat-file commit HEAD | awk '/^gpgsig / {print \"signature_present\"; exit}'"
	guarded, err := vcsguard.Rewrite(source, displayGuardHelper, displayGuardDirectory)
	if err != nil {
		t.Fatal(err)
	}
	command := workerCommand("/bin/bash", []string{"-lc", guarded})
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": command, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, guarded)
	outputs := []string{"9a9860a81d8a73a8d36981d3854ab11edac5cdaf\nfix(ui): align loaded skills links and dialog selection\n", "", "signature_present\n"}
	for i, ms := range []int{8, 17, 6} {
		duration := time.Duration(ms) * time.Millisecond
		report.send(
			execsegment.Message{Type: execsegment.Begin, Index: i, Timing: execsegment.Timing{Started: now}},
			execsegment.Message{Type: execsegment.Output, Index: i, Data: outputs[i]},
			execsegment.Message{Type: execsegment.End, Index: i, Code: new(0), Timing: execsegment.Timing{Started: now, Ended: now.Add(duration), ElapsedNS: int64(duration)}},
		)
	}
	report.send(execsegment.Message{Type: execsegment.Done, Code: new(0)})
	report.conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		hub.mu.Lock()
		track := hub.tracks[[3]string{"main", "t", "cmd"}]
		ready := track != nil && track.ended && track.done
		changed := hub.changed
		hub.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-changed:
		case <-time.After(time.Until(deadline)):
			t.Fatal("segment report did not finish")
		}
	}
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, strings.Join(outputs, "")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	awaitMain(t, u, "signature_present")
	assertNativeUISnapshot(t, "guard-command-display", u.view.renderFeed(90, 12).lines)
}

func TestUISnapshotGuardCommandOutputDialog(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.painter.Theme = livediff.DarkTheme
	source := "git log -1 --format='%H%n%s'; git status --porcelain"
	guarded, err := vcsguard.Rewrite(source, displayGuardHelper, displayGuardDirectory)
	if err != nil {
		t.Fatal(err)
	}
	command := workerCommand("/bin/bash", []string{"-lc", guarded})
	output := "9a9860a fix(ui): align loaded skills links and dialog selection\n"
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "cmd", Type: "commandExecution", Command: command, AggregatedOutput: &output, ExitCode: new(0),
	}}}})
	entry := u.view.entries[0]
	pages := u.view.commandOutputPages(entry.Seq)
	if len(pages) != 1 || pages[0].Code != source || entry.native.command != command || pages[0].Output != entry.native.output {
		t.Fatalf("combined output pages = %+v; want original command display and unchanged host output", pages)
	}
	terminal := &terminalUI{main: u}
	terminal.openBlocks(u.view, pages)
	assertNativeUISnapshot(t, "guard-command-output-dialog", strings.Split(drawOutputDialog(terminal), "\n"))
}
