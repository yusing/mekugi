package router

import (
	"strings"
	"testing"
	"time"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

const displayGuardDirectory = "/tmp/mekugi-tools-222415842-41c84ab42cecccced7ab3f107aa419ed127f6777c2b00517efb95c6723c6f413/vcs-guard"

var displaySudoDirectory = strings.TrimSuffix(displayGuardDirectory, shellinstrumentation.VCSDirectory) + shellinstrumentation.SudoDirectory

const displayGuardHelper = "/opt/mekugi/bin/mekugi-exec"

func TestGuardCommandDisplayProjection(t *testing.T) {
	for _, source := range []string{
		"git log -1 --format='%H%n%s'",
		"git commit -F - <<'EOF'\nfix: quoted message\n\nKeep `$PATH`, \"quotes\", and \\ escapes literal.\nEOF",
		"git status --porcelain",
		`sudo journalctl -k --since '2026-10-14 10:20:00' --until '2026-10-14 10:29:00' --no-pager -g 'oom|Out of memory|Killed process'`,
		`PATH='/user tools' sudo id; git status`,
		`command -p sudo id; env -i '/user tools/sudo' id; "$tools/sudo" id`,
		`echo "$(sudo id)"; cat <<'EOF'
sudo id
EOF`,
		`git cat-file commit HEAD | awk '/^gpgsig / {print "signature_present"; exit}'`,
		`PATH='/user tools' TOKEN="$token" git status; printf '%s' 'git status'`,
		`command -p git status && env -i '/user tools/git' log; "$tools/git" show HEAD`,
		`sh -c 'git status'; echo "$(git log -1)"; cat <<'EOF'
git status
EOF`,
	} {
		guarded, err := shellinstrumentation.RewriteCommands(source, displayGuardHelper, displayGuardDirectory, displaySudoDirectory, "cmd")
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
	for _, source := range []string{"git log -1; git status --porcelain", "sudo journalctl -k --no-pager"} {
		guarded, err := shellinstrumentation.RewriteCommands(source, displayGuardHelper, displayGuardDirectory, displaySudoDirectory, "cmd")
		if err != nil {
			t.Fatal(err)
		}
		want := toolActivityShell(source)
		for _, thread := range []string{"main", "child"} {
			t.Run(thread, func(t *testing.T) {
				command := shellCommandString("/bin/bash", []string{"-lc", guarded})
				item := appServerItem{ID: "cmd", Type: "commandExecution", Command: command, Status: "completed", ExitCode: new(0)}
				check := func(u *appServerUI) {
					t.Helper()
					view := u.view
					if thread == "child" {
						view = u.agents
					}
					if len(view.Entries) != 1 || view.Entries[0].Source.Text != want || view.Entries[0].Source.native.command != command {
						t.Fatalf("command entries = %+v; want display %q with unchanged host command", view.Entries, want)
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
}

func TestSudoGuardDisplayWithoutVCS(t *testing.T) {
	const source = "sudo id; command -p sudo id; git status"
	guarded, err := shellinstrumentation.RewriteCommands(source, displayGuardHelper, "", displaySudoDirectory, "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if got := toolActivityShellSource(guarded); got != source || guarded == source {
		t.Fatalf("sudo-only display = %q; guarded = %q", got, guarded)
	}
}

func TestGuardCommandDisplayPreservesUserSource(t *testing.T) {
	prefix := "PATH='" + displayGuardDirectory + "':\"${PATH-/bin:/usr/bin}\" "
	sudoPrefix := strings.Replace(prefix, shellinstrumentation.VCSDirectory, shellinstrumentation.SudoDirectory, 1)
	for _, source := range []string{
		`PATH='/user/vcs-guard':"${PATH-/bin:/usr/bin}" git status`,
		`PATH='/tmp/mekugi-tools-user/vcs-guard':"${PATH-/bin:/usr/bin}" git status`,
		"PATH='" + displayGuardDirectory + "':\"$PATH\" git status",
		prefix + "echo git status",
		`PATH='/user/sudo-guard':"${PATH-/bin:/usr/bin}" sudo id`,
		strings.Replace(sudoPrefix, "${PATH-/bin:/usr/bin}", "$PATH", 1) + "sudo id",
		sudoPrefix + "git status",
		prefix + "sudo id",
		"printf '%s' " + quoteShellWord(sudoPrefix+"sudo id"),
		"cat <<'EOF'\n" + sudoPrefix + "sudo id\nEOF",
		quoteShellWord(displayGuardHelper) + " --vcs-command " + quoteShellWord(displaySudoDirectory) + " sudo id",
		"printf '%s' " + quoteShellWord(prefix+"git status"),
		"cat <<'EOF'\n" + prefix + "git status\nEOF",
		prefix + "git 'unfinished",
		quoteShellWord(displayGuardHelper) + " --other-mode " + quoteShellWord(displayGuardDirectory) + " git status",
	} {
		if got, want := toolActivityShell(source), activitypresentation.ShellSource(source, ""); got != want {
			t.Fatalf("user source changed: %q; display = %q, want %q", source, got, want)
		}
	}
}

func TestGuardCommandDisplayCustomRuntimeDirectory(t *testing.T) {
	directory := strings.Replace(displayGuardDirectory, "/tmp/", "/run/user's tools/", 1)
	for _, source := range []string{"git status", "command git status", "sudo id", "command sudo id"} {
		guarded, err := shellinstrumentation.RewriteCommands(source, displayGuardHelper, directory, strings.Replace(directory, shellinstrumentation.VCSDirectory, shellinstrumentation.SudoDirectory, 1), "cmd")
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
	guarded, err := shellinstrumentation.RewriteCommands(source, displayGuardHelper, displayGuardDirectory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	command := shellCommandString("/bin/bash", []string{"-lc", guarded})
	output := "[main e9085377] docs(ui): describe content-detected inline highlighting\n 2 files changed, 6 insertions(+), 4 deletions(-)\n"
	u := vcsCommandUI(t, "")
	u.view.Painter.Theme = diffrender.DarkTheme
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "cmd", Type: "commandExecution", Command: command, AggregatedOutput: &output, ExitCode: new(0), DurationMS: new(int64(500)),
	}}}})
	if entry := u.view.Entries[0]; entry.Source.native.command != command {
		t.Fatal("host command changed")
	}
	assertNativeUISnapshot(t, "guarded-heredoc-commit", strings.Split(mainFeed(u, 90), "\n"))
}

func TestUISnapshotGuardCommandDisplay(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	u.view.Painter.Theme = diffrender.DarkTheme
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	source := "git log -1 --format='%H%n%s'; git status --porcelain; git cat-file commit HEAD | awk '/^gpgsig / {print \"signature_present\"; exit}'"
	guarded, err := shellinstrumentation.RewriteCommands(source, displayGuardHelper, displayGuardDirectory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	command := shellCommandString("/bin/bash", []string{"-lc", guarded})
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": command, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	report := dialExecTrackReport(t, hub, guarded)
	outputs := []string{"9a9860a81d8a73a8d36981d3854ab11edac5cdaf\nfix(ui): align loaded skills links and dialog selection\n", "", "signature_present\n"}
	for i, ms := range []int{8, 17, 6} {
		duration := time.Duration(ms) * time.Millisecond
		report.send(
			segmenttransport.Message{Type: segmenttransport.Begin, Index: i, Timing: segmenttransport.Timing{Started: now}},
			segmenttransport.Message{Type: segmenttransport.Output, Index: i, Data: outputs[i]},
			segmenttransport.Message{Type: segmenttransport.End, Index: i, Code: new(0), Timing: segmenttransport.Timing{Started: now, Ended: now.Add(duration), ElapsedNS: int64(duration)}},
		)
	}
	report.send(segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed})
	report.conn.Close()
	hub.await(t, [3]string{"main", "t", "cmd"}, func(view execTrackView) bool { return view.complete })
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, strings.Join(outputs, "")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	awaitMain(t, u, "signature_present")
	assertNativeUISnapshot(t, "guard-command-display", u.view.RenderFeed(90, 12).Lines)
}

func TestUISnapshotGuardCommandOutputDialog(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.Painter.Theme = diffrender.DarkTheme
	source := "git status --porcelain; sudo journalctl -k --no-pager"
	guarded, err := shellinstrumentation.RewriteCommands(source, displayGuardHelper, displayGuardDirectory, displaySudoDirectory, "cmd")
	if err != nil {
		t.Fatal(err)
	}
	command := shellCommandString("/bin/bash", []string{"-lc", guarded})
	output := "-- No entries --\n"
	u.restoreHistory([]appServerHistoryTurn{{ID: "past", Status: "completed", Items: []appServerItem{{
		ID: "cmd", Type: "commandExecution", Command: command, AggregatedOutput: &output, ExitCode: new(1),
	}}}})
	entry := u.view.Entries[0]
	pages := activityui.Disclosure(nil).Blocks(commandPresentation(entry.Source).DetailValues())
	if len(pages) != 1 || pages[0].Code != source || entry.Source.native.command != command || u.view.retainedOutput(pages[0]) != entry.Source.native.output {
		t.Fatalf("combined output pages = %+v; want original command display and unchanged host output", pages)
	}
	terminal := &terminalUI{main: u}
	terminal.openBlocks(u.view, pages)
	assertNativeUISnapshot(t, "guard-command-output-dialog", strings.Split(drawOutputDialog(terminal), "\n"))
}
