package launch

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestPaneOperationRegressionMCatReadLabels(t *testing.T) {
	cases := []struct {
		name, command, want string
	}{
		{"line limit", "mcat -n 40 a.go", "Read `a.go`"},
		{"token limit", "mcat --max-tokens 400 a.go", "Read `a.go`"},
		{"tail", "mcat --tail --max-tokens 400 a.go", "Read `a.go`"},
		{"multiple files", "mcat --max-tokens 500 a.go 1:3 b.go", "Read `a.go 1:3`\n\nRead `b.go`"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/native", func(t *testing.T) {
			got := toolActivityShell(tc.command)
			if got != tc.want {
				t.Fatalf("display = %q, want %q", got, tc.want)
			}
		})

	}

}

func TestPaneOperationRegressionMReadIsOmitted(t *testing.T) {
	cases := []struct {
		name, command, want string
	}{
		{"only", "mread amber", ""},
		{"before read", "mread amber\nmcat source.go", "Read `source.go`"},
		{"after read", "mcat source.go\nmread amber", "Read `source.go`"},
		{"between runs", "mread amber\necho first\nmread maple\necho second", "Run `echo first`\n\nRun `echo second`"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/native", func(t *testing.T) {
			got := toolActivityShell(tc.command)
			if got != tc.want {
				t.Fatalf("display = %q, want %q", got, tc.want)
			}
		})
	}

}

func TestPaneOperationRegressionDecorativePrintf(t *testing.T) {
	const decorative = `printf '\n--- source context ---\n'`
	for _, source := range []string{
		"mcat first.go 1:4\n" + decorative + "\nmcat second.go",
		decorative + "\nmcat source.go",
		"mcat source.go\n" + decorative,
	} {
		got := toolActivityShell(source)
		if strings.Contains(got, "printf") || strings.Contains(got, "source context") || !strings.Contains(got, "Read") {
			t.Errorf("decorative printf was not omitted beside a read: %q", got)
		}
	}

	for _, source := range []string{
		"mcat source.go\nprintf 'loaded\\n'",
		"mcat source.go\nprintf '\\n--- %s ---\\n' heading",
		"mcat source.go\nprintf '\\n--- source context ---\\n' > /tmp/activity.log",
	} {
		got := toolActivityShell(source)
		if !strings.Contains(got, "printf") || !strings.Contains(got, "Run") {
			t.Errorf("non-decorative printf was omitted: %q", got)
		}
	}
}

func TestPaneOperationRegressionDecorativeEcho(t *testing.T) {
	for _, source := range []string{
		"mcat source.go; echo '---plugins---'; mcat other.go",
		"rg -n 'mcat|mread' internal/router/tool_registry.go | head -n 40; echo '---plugins---'; mcat plugins.go",
		"mcat source.go\necho '=== source context ==='",
	} {
		got := toolActivityShell(source)
		if strings.Contains(got, "echo") || strings.Contains(got, "---plugins---") {
			t.Errorf("decorative echo was not omitted beside a classified operation: %q", got)
		}
	}

	for _, source := range []string{
		"mcat source.go; echo done",
		"mcat source.go; echo \"--- $heading ---\"",
		"mcat source.go; echo -e '\\n--- source context ---'",
		"mcat source.go; echo '--- source context ---' > /tmp/activity.log",
		"echo '--- source context ---'",
	} {
		got := toolActivityShell(source)
		if !strings.Contains(got, "echo") || !strings.Contains(got, "Run") {
			t.Errorf("non-decorative echo was omitted: %q", got)
		}
	}
}

func TestPaneOperationRegressionApplyPatchDisplay(t *testing.T) {
	const patch = "*** Begin Patch\n*** Add File: pane.txt\n+first\n*** End Patch\n"
	codeModes := []string{
		"const patch = " + string(responses.MustMarshalJSON(patch)) + "; text(await tools.apply_patch(patch));",
		"const patch = `" + patch + "`; text(await tools.apply_patch(patch));",
		"text(await tools.apply_patch(`" + patch + "`));",
		"const patch = `*** Begin Patch\n*** Add File: pane.txt\n+Use \\`mcat\\` and \\${literal}\n*** End Patch\n`; text(await tools.apply_patch(patch));",
	}
	for _, source := range codeModes {
		if patches := binding.LiteralPatchInputs(source); len(patches) != 1 {
			t.Fatalf("static patch was not captured: %q", patches)
		}
	}
	for _, source := range []string{
		"const patch = `*** Begin Patch\n${dynamic}`; text(await tools.apply_patch(patch));",
		"text(await tools.apply_patch(`*** Begin Patch\n${dynamic}`));",
	} {
		if patches := binding.LiteralPatchInputs(source); len(patches) != 0 {
			t.Fatalf("dynamic template entered patch capture: %q", patches)
		}
	}
}
