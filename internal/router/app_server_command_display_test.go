package router

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestAppServerFrontendCommandClassification(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"sed -n '390,432p' $(go env \\\nGOROOT)/src/encoding/json/v2/arshal_time.go", "Read\n```\n$(go env \\\nGOROOT)/src/encoding/json/v2/arshal_time.go 390:432\n```"},
		{`sed -n '1,2p' "$(project-root)/file.go"`, "Read `\"$(project-root)/file.go\" 1:2`"},
		{"cat /home/yusing/projects/codex/codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs | sed -n '45,150p'", "Read `/home/yusing/projects/codex/codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs 45:150`"},
		{"inspect_file app.go; mcat app.go 1:20; rg -n needle src | head -30", "Inspect `app.go`\n\nRead `app.go 1:20`\n\nSearch `needle` in `src`"},
		{"timeout 123 cat file.go | head -20", "Read `file.go`"},
		{"timeout 123 cat file.go | sed -n '1,20p'", "Read `file.go 1:20`"},
		{"timeout 123 nl -ba file.go | sed -n '1,20p'", "Read `file.go 1:20`"},
		{"cat file.go | timeout 123 head -20", "Read `file.go`"},
		{`timeout 123 cat file.go | head -n "$count"`, "Run\n" + toolActivityFenced("bash", `timeout 123 cat file.go | head -n "$count"`)},
		{"timeout 123 skills-mgr get skill", "Skill `skill`"},
		{"timeout 123 skills-mgr run skill/script --flag", "Skill `run skill/script --flag`"},
		{`timeout 123 cat "$HOME/file"`, "Read `\"$HOME/file\"`"},
		{`timeout --help skills-mgr get skill`, "Run\n" + toolActivityFenced("bash", `timeout --help skills-mgr get skill`)},
		{`timeout "$duration" skills-mgr get skill`, "Run\n" + toolActivityFenced("bash", `timeout "$duration" skills-mgr get skill`)},
		{`timeout 123 skills-mgr get skill > out`, "Run\n" + toolActivityFenced("bash", `timeout 123 skills-mgr get skill > out`)},
		{`timeout 123 unknown arg`, "Run\n" + toolActivityFenced("bash", `timeout 123 unknown arg`)},
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
				restored := newAppServerSessionTestUI(t, t.TempDir())
				restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
				if len(restored.view.entries) != 1 || restored.view.entries[0].Text != tc.want {
					t.Fatalf("classified restored entries: %+v", restored.view.entries)
				}
			})
		}
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
		{`/bin/zsh -lc 'python3 -c '\''print("Hello, world!")'\'''`, `python3 -c 'print("Hello, world!")'`},
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

func TestUISnapshotTimeoutSkill(t *testing.T) {
	p := activityui.Painter{Theme: livediff.DarkTheme}
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell("timeout 123 skills-mgr get skill")})
	assertNativeUISnapshot(t, "timeout-skill", p.Block(blocks[0], 90))
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
