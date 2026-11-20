package terminalui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
)

func TestFaintFallbackRestoresStyles(t *testing.T) {
	// Color changes and resets inside a dim region must not strand the muted shade.
	input := "\x1b[38;5;170mA\x1b[2mB\x1b[38;5;209mC\x1b[22mD\x1b[0mE"
	expected := "\x1b[38;5;170mA\x1b[38;5;133mB\x1b[38;5;166mC\x1b[38;5;209mD\x1b[0mE"
	got, want := vt.NewEmulator(10, 1), vt.NewEmulator(10, 1)
	defer got.Close()
	defer want.Close()
	fmt.Fprint(got, terminalui.FaintFallback(input))
	fmt.Fprint(want, expected)
	for x := range 5 {
		if !got.CellAt(x, 0).Style.Equal(&want.CellAt(x, 0).Style) {
			t.Fatalf("cell %d: %+v != %+v", x, got.CellAt(x, 0).Style, want.CellAt(x, 0).Style)
		}
	}
	link := "\x1b]8;;https://example.com\x1b\\"
	if !strings.Contains(terminalui.FaintFallback(link+input), link) {
		t.Fatal("hyperlink changed")
	}
}

func TestMutedAgentPalette(t *testing.T) {
	for _, tc := range []struct {
		name        string
		normal, dim int
	}{
		{"/root/agent6", 209, 166}, {"/root/agent1", 170, 133}, {"/root/agent5", 111, 68},
		{"/root/agent0", 150, 107}, {"/root/agent3", 222, 136}, {"/root/agent7", 183, 140}, {"/root/agent2", 174, 131},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen := vt.NewEmulator(4, 1)
			defer screen.Close()
			fmt.Fprint(screen, terminalui.FaintFallback(terminalui.AgentColor(diffrender.DarkTheme, tc.name)+"N"+terminalui.Reset+terminalui.DimAgentColor(diffrender.DarkTheme, tc.name)+"D"+terminalui.Reset+"X"))
			expected := vt.NewEmulator(4, 1)
			defer expected.Close()
			fmt.Fprintf(expected, "\x1b[1;38;5;%dmN\x1b[0;38;5;%dmD\x1b[0mX", tc.normal, tc.dim)
			for x := range 3 {
				if !screen.CellAt(x, 0).Style.Equal(&expected.CellAt(x, 0).Style) {
					t.Fatalf("cell %d style = %+v, want %+v", x, screen.CellAt(x, 0).Style, expected.CellAt(x, 0).Style)
				}
			}
		})
	}
}

func TestAgentAddressColors(t *testing.T) {
	for _, task := range []string{"/root", "/root/worker", "/root/worker/reviewer"} {
		address := "agent:main" + strings.TrimPrefix(task, "/root")
		if color, dim := terminalui.AgentColor(diffrender.TerminalTheme, address), terminalui.DimAgentColor(diffrender.TerminalTheme, address); color != terminalui.AgentColor(diffrender.TerminalTheme, task) || dim != terminalui.DimAgentColor(diffrender.TerminalTheme, task) {
			t.Fatalf("address %q differs from task %q: %q, %q", address, task, color, dim)
		}
	}
}
