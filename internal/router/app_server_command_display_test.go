package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestAppServerFrontendCommandClassification(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{`env -u BASH_ENV -u MEKUGI_EXEC_TRACK bash -c 'sed -n "28,85p" internal/router/app_server_ui.go; cat internal/router/subagent_activity_pane.go | head -85; rg -n "MCP|spawn_agent" internal/router/subagent_activity.go | head -80'`, "Read `internal/router/app_server_ui.go 28:85`\n\nRead `internal/router/subagent_activity_pane.go`\n\nSearch `MCP|spawn_agent` in `internal/router/subagent_activity.go`"},
		{`/usr/bin/sed -n '2,4p' app.go; /bin/cat other.go | /usr/bin/head -80; /usr/bin/rg needle src 2>/dev/null | /usr/bin/head -20`, "Read `app.go 2:4`\n\nRead `other.go`\n\nSearch `needle` in `src`"},
		{`/usr/bin/env --unset=BASH_ENV LANG=C /bin/sh -c 'go test ./...; cat app.go'`, "Run `go test ./...`\n\nRead `app.go`"},
		{`env -u BASH_ENV /bin/dash -c 'cat "$HOME/app.go"'`, "Read `\"$HOME/app.go\"`"},
		{`env -u BASH_ENV timeout 10 /bin/cat app.go | env LANG=C /usr/bin/sed -n '2,4p'`, "Read `app.go 2:4` (timeout 10)"},
		{`timeout 20 env LANG=C bash -c 'timeout 10 cat app.go; rg needle src'`, "Read `app.go` (timeout 10) (timeout 20)\n\nSearch `needle` in `src` (timeout 20)"},
		{`env -u BASH_ENV bash -c 'set -e; timeout 10 sleep 1; cat app.go'`, "Run (timeout 10)\n" + toolActivityFenced("bash", `env -u BASH_ENV bash -c 'set -e; timeout 10 sleep 1; cat app.go'`)},
		{"sed -n '390,432p' $(go env \\\nGOROOT)/src/encoding/json/v2/arshal_time.go", "Read\n```\n$(go env \\\nGOROOT)/src/encoding/json/v2/arshal_time.go 390:432\n```"},
		{`sed -n '1,2p' "$(project-root)/file.go"`, "Read `\"$(project-root)/file.go\" 1:2`"},
		{"cat /home/yusing/projects/codex/codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs | sed -n '45,150p'", "Read `/home/yusing/projects/codex/codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs 45:150`"},
		{"inspect_file app.go; mcat app.go 1:20; rg -n needle src | head -30", "Inspect `app.go`\n\nRead `app.go 1:20`\n\nSearch `needle` in `src`"},
		{"timeout 123 cat file.go | head -20", "Read `file.go` (timeout 123)"},
		{"timeout 123 cat file.go | sed -n '1,20p'", "Read `file.go 1:20` (timeout 123)"},
		{"timeout 123 nl -ba file.go | sed -n '1,20p'", "Read `file.go 1:20` (timeout 123)"},
		{"cat file.go | timeout 123 head -20", "Read `file.go` (timeout 123)"},
		{`timeout 123 cat file.go | head -n "$count"`, "Run (timeout 123)\n" + toolActivityFenced("bash", `cat file.go | head -n "$count"`)},
		{"timeout 123 skills-mgr get skill", "Skill `skill` (timeout 123)"},
		{"timeout 123 skills-mgr run skill/script --flag", "Skill `run skill/script --flag` (timeout 123)"},
		{`timeout 123 cat "$HOME/file"`, "Read `\"$HOME/file\"` (timeout 123)"},
		{`timeout --help skills-mgr get skill`, "Run\n" + toolActivityFenced("bash", `timeout --help skills-mgr get skill`)},
		{`timeout "$duration" skills-mgr get skill`, "Run\n" + toolActivityFenced("bash", `timeout "$duration" skills-mgr get skill`)},
		{`timeout 123 skills-mgr get skill > out`, "Run (timeout 123)\n" + toolActivityFenced("bash", `skills-mgr get skill > out`)},
		{`timeout 5 echo "$(timeout 10 probe)"`, "Run (timeout 5)\n" + toolActivityFenced("bash", `echo "$(timeout 10 probe)"`)},
		{`timeout 5 sleep 6`, "Run (timeout 5)\n" + toolActivityFenced("bash", `sleep 6`)},
		{`timeout .5m timeout 1h unknown "arg text" 2>err`, "Run (timeout .5m) (timeout 1h)\n" + toolActivityFenced("bash", `unknown "arg text" 2>err`)},
		{`timeout 123 unknown arg`, "Run (timeout 123)\n" + toolActivityFenced("bash", `unknown arg`)},
		{"skills-mgr get js-ts-best-practices; skills-mgr get user-experience", "Skill `js-ts-best-practices`\n\nSkill `user-experience`"},
		{"skills-mgr run use-modern-go/scripts/run-tool.sh list --go-version 1.27", "Skill `run use-modern-go/scripts/run-tool.sh list --go-version 1.27`"},
		{"mcat app.go 1:20; go test ./...", "Read `app.go 1:20`\n\nRun `go test ./...`"},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", tc.command, wrapped), func(t *testing.T) {
				command := tc.command
				if wrapped {
					command = workerCommand("/usr/bin/bash", []string{"-lc", command})
				}
				u := newAppServerSessionTestUI(t, t.TempDir())
				item := appServerItem{ID: "cmd", Type: "commandExecution", Command: command,
					CommandActions: []appServerCommandAction{{Type: "unknown", Command: command}}}
				for _, method := range []string{"item/started", "item/completed"} {
					appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
				}
				if len(u.view.entries) != 1 || u.view.entries[0].Text != tc.want || u.view.entries[0].native.command != command {
					t.Fatalf("classified live entries: %+v; want %q", u.view.entries, tc.want)
				}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "t", "item": item})
				if len(u.agents.entries) != 2 || u.agents.entries[1].Text != tc.want || u.agents.entries[1].native.command != command {
					t.Fatalf("classified child text: %q; want %q", u.agents.entries[len(u.agents.entries)-1].Text, tc.want)
				}
				restored := newAppServerSessionTestUI(t, t.TempDir())
				restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
				if len(restored.view.entries) != 1 || restored.view.entries[0].Text != tc.want {
					t.Fatalf("classified restored entries: %+v", restored.view.entries)
				}
				restored.session.path("child")
				restored.restoreActivityThread(appServerThreadInfo{ID: "child", Cwd: restored.session.cwd, Turns: []appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}}})
				if len(restored.agents.entries) != 1 || restored.agents.entries[0].Text != tc.want {
					t.Fatalf("classified restored child text: %q; want %q", restored.agents.entries[len(restored.agents.entries)-1].Text, tc.want)
				}
			})
		}
	}
}

func TestAppServerOpaqueCommandWrappers(t *testing.T) {
	for _, command := range []string{
		`env MODE=x -u BASH_ENV cat app.go`,
		`env -C /other cat app.go`, `env PATH=/other cat app.go`, `env -u PATH cat app.go`,
		`env MODE="$(touch marker)" cat app.go`, `env -u "$NAME" cat app.go`,
		`env -u BASH_ENV bash -c "$SCRIPT"`, `env -u BASH_ENV bash -c 'cat "$1"' name app.go`,
		`env -u BASH_ENV bash -c 'cat app.go' > out`, `env -u BASH_ENV bash -c 'cat app.go' &`,
		`env -u BASH_ENV bash -c 'cat app.go' | unknown`, `./cat app.go`,
	} {
		if got := appServerCommandText(appServerItem{Command: command}, ""); got != "Run\n"+toolActivityFenced("bash", command) {
			t.Errorf("unsafe wrapper %q classified: %s", command, got)
		}
	}
}

func TestUISnapshotWrappedCommands(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		u := newAppServerSessionTestUI(t, "/workspace")
		u.clock = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC) }
		view := u.view
		if thread == "child" {
			view = u.agents
		}
		view.clock, view.painter.Theme = u.clock, livediff.DarkTheme
		for i, command := range []string{
			`env -u BASH_ENV -u MEKUGI_EXEC_TRACK bash -c 'sed -n "28,85p" internal/router/app_server_ui.go; cat internal/router/subagent_activity_pane.go | head -85; rg -n "MCP|spawn_agent" internal/router/subagent_activity.go | head -80'`,
			`/usr/bin/sed -n '2,4p' app.go; /bin/cat other.go | /usr/bin/head -80`,
			`env -u BASH_ENV bash -c 'set -e; timeout 10 sleep 1; cat app.go'`,
		} {
			item := appServerItem{ID: fmt.Sprint(i), Type: "commandExecution", Command: command, Cwd: "/workspace", Status: "completed", ExitCode: new(0), DurationMS: new(int64(500))}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": item})
		}
		view.pace(u.clock().Add(time.Hour))
		assertNativeUISnapshot(t, "wrapped-commands-"+thread, view.renderFeed(100, 30).lines)
	}
}

func TestAppServerShellDisplay(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{`/usr/bin/bash -lc "sed -i 's/a/b/' file; go test ./..."`, `sed -i 's/a/b/' file; go test ./...`},
		{`bash -c 'printf '\''hello'\'''`, `printf 'hello'`},
		{"/bin/bash -lc 'echo one\necho two'", "echo one\necho two"},
		{"bash -lc 'echo ```'", "echo ```"},
		{`bash -lc 'echo hi' > out`, `bash -lc 'echo hi' > out`},
		{`bash -lc 'echo "$1"' name value`, `bash -lc 'echo "$1"' name value`},
		{`bash -lc "$SCRIPT"`, `bash -lc "$SCRIPT"`},
		{`bash -lc 'echo hi'; echo after`, `bash -lc 'echo hi'; echo after`},
		{`ENV=value bash -lc 'echo hi'`, `ENV=value bash -lc 'echo hi'`},
		{`bash -lc 'unfinished`, `bash -lc 'unfinished`},
		{`echo ordinary`, `echo ordinary`},
		{`zsh -lc 'echo hi'`, `zsh -lc 'echo hi'`},
		{`/bin/bash -lc 'python3 -c '\''print("Hello, world!")'\'''`, `python3 -c 'print("Hello, world!")'`},
		{`sh -c 'echo hello'`, `echo hello`},
		{`bash.exe -lc 'echo hi'`, `echo hi`},
		{`powershell.exe -Command 'Write-Host hi'`, `Write-Host hi`},
		{`pwsh -NoLogo -NoProfile -c 'Get-ChildItem | Select-String foo'`, `Get-ChildItem | Select-String foo`},
		{`pwsh -nOpRoFiLe -COMMAND 'Write-Host hi'`, `Write-Host hi`},
		{`pwsh -File script.ps1`, `pwsh -File script.ps1`},
		{`pwsh -Command 'Write-Host $args' extra`, `pwsh -Command 'Write-Host $args' extra`},
		{`pwsh -Unknown -Command 'Write-Host hi'`, `pwsh -Unknown -Command 'Write-Host hi'`},
		{`bash -lc 'echo hi' &`, `bash -lc 'echo hi' &`},
		{`! bash -lc 'echo hi'`, `! bash -lc 'echo hi'`},
		{`bash -lc 'echo hi' | cat`, `bash -lc 'echo hi' | cat`},
	} {
		t.Run(tt.input, func(t *testing.T) {
			item := appServerItem{Command: tt.input}
			if got := appServerDisplayCommand(item.Command); got != tt.want {
				t.Fatalf("display = %q, want %q", got, tt.want)
			}
			if item.Command != tt.input {
				t.Fatal("changed original command")
			}
		})
	}
}

func TestAppServerShellDisplayHighlight(t *testing.T) {
	source := `printf '%s' "$HOME"; go test ./...`
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: appServerCommandText(appServerItem{Command: `/bin/bash -lc 'printf '\''%s'\'' "$HOME"; go test ./...'`}, "")})
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := activityui.Painter{Theme: theme}
		var rows []string
		for _, block := range blocks {
			rows = append(rows, p.Block(block, 120)...)
		}
		got := strings.Join(rows, "\n")
		highlighted := strings.Join(p.Highlight("bash", source), " ")
		if ansi.Strip(highlighted) != source || highlighted == source || !strings.Contains(ansi.Strip(got), "printf") || !strings.Contains(ansi.Strip(got), "go test ./...") || strings.Contains(ansi.Strip(got), "/bin/bash") {
			t.Fatalf("inner source not highlighted: %q", got)
		}
		for _, block := range blocks {
			highlighted := strings.Join(p.Highlight(block.Lang, block.Code), " ")
			if !strings.Contains(got, highlighted) {
				t.Fatalf("command lost shared highlighting: %q", got)
			}
		}
	}
}

func TestAppServerImageViewLiveAndRestored(t *testing.T) {
	workspace := t.TempDir()
	item := appServerItem{ID: "image", Type: "imageView", Path: workspace + "/images/a.png"}
	for _, child := range []bool{false, true} {
		u := newAppServerSessionTestUI(t, workspace)
		thread := "main"
		view := u.view
		if child {
			thread = "child"
			view = u.agents
		}
		for _, method := range []string{"item/started", "item/completed"} {
			appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "t", "item": item})
		}
		check := func(v *liveActivityView) {
			t.Helper()
			if len(v.entries) != 1 || v.entries[0].Text != "View `images/a.png`" {
				t.Fatalf("image activity: %+v", v.entries)
			}
			blocks := parseLiveActivity(v.entries[0].activityPaneEntry)
			if len(blocks) != 1 || blocks[0].Verb != "View" || len(blocks[0].Reads) != 1 || blocks[0].Reads[0].Path != "images/a.png" {
				t.Fatalf("image blocks: %+v", blocks)
			}
		}
		check(view)
		restored := newAppServerSessionTestUI(t, workspace)
		turns := []appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}}
		if child {
			restored.session.path(thread)
			restored.restoreActivityThread(appServerThreadInfo{ID: thread, Cwd: workspace, Turns: turns})
			check(restored.agents)
		} else {
			restored.restoreHistory(turns)
			check(restored.view)
		}
	}
}

func TestAppServerCommandTimeoutInput(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	f := newNativeTraceFixture(t)
	u.proxy = &mekugiProxy{nativeTrace: &nativeToolTrace{directory: f.root}}
	for _, thread := range []string{"main", "child"} {
		f.start(thread, thread, "outer", "source")
		timeoutMS, wantTimeout := 12500, "12.5s"
		if thread == "child" {
			timeoutMS, wantTimeout = 90000, "90s"
		}
		for _, tc := range []struct {
			id, args, want string
		}{
			{"timed", fmt.Sprintf(`{"cmd":"sleep 1","timeout_ms":%d}`, timeoutMS), wantTimeout},
			{"yielded", `{"cmd":"sleep 1","yield_time_ms":12500}`, ""},
			{"unknown", "", ""},
		} {
			if tc.args != "" {
				f.tool(thread, thread, tc.id, "exec_command", tc.args)
			}
			item := appServerItem{ID: tc.id, Type: "commandExecution", Command: "sleep 1", DurationMS: new(int64(500))}
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "t", "item": item})
				view := u.view
				if thread != "main" {
					view = u.agents
				}
				entry := view.entries[len(view.entries)-1]
				if entry.native.command != item.Command || entry.native.commandTimeout != tc.want {
					t.Fatalf("%s/%s timeout = %q, want %q", thread, tc.id, entry.native.commandTimeout, tc.want)
				}
				blocks := parseLiveActivity(entry.activityPaneEntry)
				if got := strings.Join(blocks[0].Timeouts, " "); got != tc.want {
					t.Fatalf("%s/%s displayed timeout = %q", thread, tc.id, got)
				}
				if method == "item/completed" {
					rendered := strings.Join(view.painter.Block(entry.blocks[0], 90), "\n")
					if strings.Contains(rendered, "(timeout ") != (tc.want != "") {
						t.Fatalf("%s/%s rendered timeout: %s", thread, tc.id, rendered)
					}
				}
			}
		}
	}
	restored := newAppServerSessionTestUI(t, t.TempDir())
	restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{{ID: "timed", Type: "commandExecution", Command: "skills-mgr get skill"}}}})
	if len(restored.view.entries[0].blocks[0].Timeouts) != 0 {
		t.Fatal("restored a timeout without original invocation input")
	}
}

func TestUISnapshotTimeoutSkill(t *testing.T) {
	p := activityui.Painter{Theme: livediff.DarkTheme}
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell("timeout 123 skills-mgr get skill")})
	blocks[0].Duration = 500 * time.Millisecond
	assertNativeUISnapshot(t, "timeout-skill", p.Block(blocks[0], 90))
}

func TestUISnapshotTimeoutRun(t *testing.T) {
	p := activityui.Painter{Theme: livediff.DarkTheme}
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell("timeout 5 sleep 6")})
	blocks[0].Duration, blocks[0].ExitCode = 5*time.Second, 124
	assertNativeUISnapshot(t, "timeout-run", p.Block(blocks[0], 90))
}

func TestUISnapshotShellSetupEvent(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.view.painter.Theme = livediff.DarkTheme
	command := "probe_root=$(mktemp -d /tmp/setup-apt-baseline.XXXXXX)\n" +
		"trap 'rm -rf \"$probe_root\"' EXIT\nexport SETUP_CONFIG=/workspace/setup.json\n" +
		"smoke_root=\"$probe_root\"\ngit show 99076ca:setup.sh > \"$probe_root/baseline.sh\"\n" +
		"sed -n '1,20p' setup_test.sh\nsource \"$probe_root/baseline.sh\"\ntrap - ERR\n" +
		"source \"$probe_root/policy.sh\"\nif check_apt_policy; then exit 1; fi"
	item := appServerItem{ID: "setup", Type: "commandExecution", Command: command, Cwd: "/workspace", Status: "completed", ExitCode: new(0)}
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	finishPacing(u.view)
	assertNativeUISnapshot(t, "shell-setup-event", u.view.renderFeed(100, 24).lines)
}

func TestUISnapshotClassifiedCommandPaths(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/workspace")
	u.view.painter.Theme = livediff.DarkTheme
	for i, command := range []string{
		"mcat /workspace/internal/router/app.go 1:3",
		"inspect_file /workspace/app.go /external/other.go",
		"rg '/workspace/literal' /workspace/src /external",
	} {
		item := appServerItem{ID: fmt.Sprint(i), Type: "commandExecution", Command: command, Cwd: "/workspace", Status: "completed", ExitCode: new(0), DurationMS: new(int64(20))}
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	}
	finishPacing(u.view)
	assertNativeUISnapshot(t, "classified-command-paths", u.view.renderFeed(100, 30).lines)
}

func TestLiveActivitySkillRendering(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := activityui.Painter{Theme: theme}
		for _, command := range []string{"skills-mgr get golang-best-practices", "skills-mgr run use-modern-go/scripts/run-tool.sh list --go-version 1.27"} {
			blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(command)})
			if len(blocks) != 1 || blocks[0].Verb != "Skill" {
				t.Fatalf("skill blocks: %+v", blocks)
			}
			rows := strings.Join(p.Block(blocks[0], 120), "\n")
			if !strings.Contains(rows, activityui.VerbColor("Skill")+"\x1b[1mSkill") || activityui.VerbColor("Skill") == "" {
				t.Fatalf("uncolored skill: %q", rows)
			}
			if strings.Contains(command, " run ") && !strings.Contains(ansi.Strip(rows), "Skill  run use-modern-go/scripts/run-tool.sh list --go-version 1.27") {
				t.Fatalf("skill run spacing: %q", rows)
			}
		}
	}
}

func TestAppServerCommandWorkdirDisplay(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	for _, tc := range []struct {
		name, cwd, workdir string
		actions            []appServerCommandAction
		command, text      string
	}{
		{name: "workspace", cwd: workspace, command: "go test ./...", text: "Run\n```bash\ngo test ./...\n```"},
		{name: "unknown", command: "go test ./...", text: "Run\n```bash\ngo test ./...\n```"},
		{name: "outside", cwd: other, workdir: other, command: "go test ./...", text: "Run\n```bash\ngo test ./...\n```"},
		{name: "subdirectory", cwd: filepath.Join(workspace, "sub"), workdir: "sub", command: "cat a.go",
			actions: []appServerCommandAction{{Type: "read", Command: "cat a.go", Path: filepath.Join(workspace, "sub", "a.go")}}, text: "Read `a.go`"},
		{name: "frontend read", cwd: workspace, command: "mcat " + quoteShellWord(workspace+"/a.go") + " 1:3", text: "Read `a.go 1:3`"},
		{name: "inspect other directory", cwd: other, workdir: other, command: "inspect_file " + quoteShellWord(other+"/a.go") + " " + quoteShellWord(workspace+"/b.go"), text: "Inspect `a.go`\n\nInspect `" + workspace + "/b.go`"},
		{name: "search path and literal query", cwd: workspace, command: "rg " + quoteShellWord(workspace+"/literal") + " " + quoteShellWord(workspace+"/src") + " " + quoteShellWord(other), text: "Search `" + workspace + "/literal` in `src` `" + other + "`"},
		{name: "piped read", cwd: workspace, command: "nl -ba " + quoteShellWord(workspace+"/a.go") + " | sed -n '2,4p'", text: "Read `a.go 2:4`"},
		{name: "list", cwd: other, workdir: other, command: "ls src",
			actions: []appServerCommandAction{{Type: "listFiles", Command: "ls src", Path: "src"}}, text: "List `src`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actions == nil {
				tc.actions = []appServerCommandAction{{Type: "unknown", Command: tc.command}}
			}
			item := appServerItem{ID: "cmd", Type: "commandExecution", Command: tc.command, Cwd: tc.cwd, CommandActions: tc.actions}
			check := func(u *appServerUI) {
				t.Helper()
				if len(u.view.entries) != 1 || u.view.entries[0].Text != tc.text || u.view.entries[0].native.workdir != tc.workdir {
					t.Fatalf("entries = %+v; want text %q in %q", u.view.entries, tc.text, tc.workdir)
				}
				if u.view.entries[0].native.command != tc.command {
					t.Fatal("retained command source changed")
				}
				blocks := parseLiveActivity(u.view.entries[0].activityPaneEntry)
				if len(blocks) == 0 || blocks[0].Workdir != tc.workdir {
					t.Fatalf("blocks = %+v; want workdir %q", blocks, tc.workdir)
				}
				row := ansi.Strip((&activityui.Painter{Theme: livediff.DarkTheme}).Block(blocks[0], 200)[0])
				if want := "· in " + tc.workdir; (tc.workdir != "") != strings.Contains(row, want) || tc.workdir == "" && strings.Contains(row, "· in ") {
					t.Fatalf("row = %q; want workdir %q", row, tc.workdir)
				}
			}
			u := newAppServerSessionTestUI(t, workspace)
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
			}
			check(u)
			restored := newAppServerSessionTestUI(t, workspace)
			restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
			check(restored)
		})
	}
}

func TestActivityReadsKeepInvocationWorkdirsApart(t *testing.T) {
	other := t.TempDir()
	u := newAppServerSessionTestUI(t, t.TempDir())
	for _, item := range []appServerItem{
		{ID: "a", Type: "commandExecution", Command: "rg x; cat a.go", Cwd: other, CommandActions: []appServerCommandAction{{Type: "search", Command: "rg x", Query: "x"}, {Type: "read", Command: "cat a.go", Path: other + "/a.go"}}},
		{ID: "b", Type: "commandExecution", Command: "cat b.go", Cwd: u.session.cwd, CommandActions: []appServerCommandAction{{Type: "read", Command: "cat b.go", Path: u.session.cwd + "/b.go"}}},
	} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
	}
	var blocks []activityui.Block
	for _, entry := range u.view.entries {
		blocks = append(blocks, parseLiveActivity(entry.activityPaneEntry)...)
	}
	if len(blocks) != 3 || !blocks[0].ShowWorkdir || blocks[1].ShowWorkdir || blocks[1].Workdir != other {
		t.Fatalf("blocks = %+v", blocks)
	}
	if merged := activityui.MergeLiveActivityReads(blocks); len(merged) != 3 {
		t.Fatalf("reads across directories merged: %+v", merged)
	}
}

func TestActivityReadsKeepWorkdirsApart(t *testing.T) {
	read := func(workdir string) activityui.Block {
		return activityui.Block{Kind: "reads", Verb: "Read", Reads: []activityui.Read{{Path: "a.go"}}, Workdir: workdir}
	}
	if merged := activityui.MergeLiveActivityReads([]activityui.Block{read(""), read("/other")}); len(merged) != 2 {
		t.Fatalf("reads in different directories merged: %+v", merged)
	}
	if merged := activityui.MergeLiveActivityReads([]activityui.Block{read("/other"), read("/other")}); len(merged) != 1 {
		t.Fatalf("reads in one directory did not merge: %+v", merged)
	}
}

func TestAppServerWholeScriptTimeoutSuffix(t *testing.T) {
	for _, command := range []string{"timeout 10 bash <<'EOF'\nsleep 1\nEOF", "set -e\ntimeout 10 sleep 1", "set -e\necho start && timeout 10 sleep 1"} {
		t.Run(command, func(t *testing.T) {
			item := appServerItem{ID: "cmd", Type: "commandExecution", Command: command, DurationMS: new(int64(500))}
			u := newAppServerSessionTestUI(t, t.TempDir())
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": item})
			entry := u.view.entries[0]
			blocks := parseLiveActivity(entry.activityPaneEntry)
			var p activityui.Painter
			var rows []string
			for _, block := range blocks {
				rows = append(rows, p.Block(block, 90)...)
			}
			if got := strings.Join(rows, "\n"); !strings.Contains(got, "(timeout 10s)") {
				t.Fatalf("missing timeout suffix: %s", got)
			}
			if entry.native.command != command || blocks[0].Code != command {
				t.Fatalf("whole script source changed: %+v", blocks[0])
			}
		})
	}
}

func TestUISnapshotShellSetupTimeoutEvent(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		u := newAppServerSessionTestUI(t, "/workspace")
		now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
		u.clock = func() time.Time { return now }
		view := u.view
		if thread == "child" {
			view = u.agents
		}
		view.clock, view.painter.Theme = u.clock, livediff.DarkTheme
		item := appServerItem{ID: "setup", Type: "commandExecution", Command: "set -e\ntimeout 10 sleep 1", Cwd: "/workspace", Status: "inProgress"}
		appServerTestNotify(t, u, "item/started", map[string]any{"threadId": thread, "turnId": "t", "item": item})
		now = now.Add(500 * time.Millisecond)
		assertNativeUISnapshot(t, "timeout-setup-"+thread+"-running", view.renderFeed(70, 24).lines)
		item.Status, item.ExitCode, item.DurationMS = "completed", new(0), new(int64(500))
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": "t", "item": item})
		assertNativeUISnapshot(t, "timeout-setup-"+thread+"-completed", view.renderFeed(70, 24).lines)
	}
}
