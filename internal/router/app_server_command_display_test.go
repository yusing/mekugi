package router

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
)

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
			blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: appServerCommandText(item, "")})
			if len(blocks) != 1 || blocks[0].code != tt.want || blocks[0].lang != "bash" {
				t.Fatalf("blocks = %+v", blocks)
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
		got := strings.Join(p.block(blocks[0], 120), "\n")
		highlighted := strings.Join(p.highlight("bash", source), " ")
		if ansi.Strip(highlighted) != source || highlighted == source || !strings.Contains(got, highlighted) || strings.Contains(ansi.Strip(got), "/bin/bash") {
			t.Fatalf("inner source not highlighted: %q", got)
		}
	}
}
