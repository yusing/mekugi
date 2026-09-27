package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestAppServerFrontendCommandClassification(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"inspect_file app.go; mcat app.go 1:20; rg -n needle src | head -30", "Inspect `app.go`\n\nRead `app.go 1:20`\n\nSearch `needle` in `src`"},
		{"skills-mgr get js-ts-best-practices; skills-mgr get user-experience", "Skill `js-ts-best-practices`\n\nSkill `user-experience`"},
		{"skills-mgr run use-modern-go/scripts/run-tool.sh list --go-version 1.27", "Skill `run use-modern-go/scripts/run-tool.sh list --go-version 1.27`"},
		{"mcat app.go 1:20; go test ./...", "Read `app.go 1:20`\n\nRun `go test ./...`"},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%t", tc.command, wrapped), func(t *testing.T) {
				command := tc.command
				if wrapped {
					command = "/usr/bin/bash -lc '" + command + "'"
				}
				u := newAppServerSessionTestUI(t, t.TempDir())
				item := appServerItem{ID: "cmd", Type: "commandExecution", Command: command,
					CommandActions: []appServerCommandAction{{Type: "unknown", Command: command}}}
				for _, method := range []string{"item/started", "item/completed"} {
					appServerTestNotify(t, u, method, map[string]any{"threadId": "main", "turnId": "t", "item": item})
				}
				if len(u.view.entries) != 1 || u.view.entries[0].Text != tc.want {
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
		p := liveActivityPainter{theme: theme}
		var rows []string
		for _, block := range blocks {
			rows = append(rows, p.block(block, 120)...)
		}
		got := strings.Join(rows, "\n")
		highlighted := strings.Join(p.highlight("bash", source), " ")
		if ansi.Strip(highlighted) != source || highlighted == source || !strings.Contains(ansi.Strip(got), "printf") || !strings.Contains(ansi.Strip(got), "go test ./...") || strings.Contains(ansi.Strip(got), "/bin/bash") {
			t.Fatalf("inner source not highlighted: %q", got)
		}
		for _, block := range blocks {
			highlighted := strings.Join(p.highlight(block.lang, block.code), " ")
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
			blocks := parseLiveActivity(v.entries[0])
			if len(blocks) != 1 || blocks[0].verb != "View" || len(blocks[0].reads) != 1 || blocks[0].reads[0].path != "images/a.png" {
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

func TestLiveActivitySkillRendering(t *testing.T) {
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		p := liveActivityPainter{theme: theme}
		for _, command := range []string{"skills-mgr get golang-best-practices", "skills-mgr run use-modern-go/scripts/run-tool.sh list --go-version 1.27"} {
			blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(command)})
			if len(blocks) != 1 || blocks[0].verb != "Skill" {
				t.Fatalf("skill blocks: %+v", blocks)
			}
			rows := strings.Join(p.block(blocks[0], 120), "\n")
			if !strings.HasPrefix(rows, liveActivityVerbColor("Skill")) || liveActivityVerbColor("Skill") == "" {
				t.Fatalf("uncolored skill: %q", rows)
			}
			if strings.Contains(command, " run ") && !strings.Contains(ansi.Strip(rows), "Skill  run use-modern-go/scripts/run-tool.sh list --go-version 1.27") {
				t.Fatalf("skill run spacing: %q", rows)
			}
		}
	}
}
