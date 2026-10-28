package terminalui_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/libraries/terminalui"
)

func TestFaintFallbackRestoresStyles(t *testing.T) {
	// Color changes and resets inside a dim region must not strand the muted shade.
	input := "\x1b[38;5;170mA\x1b[2mB\x1b[38;5;39mC\x1b[22mD\x1b[0mE"
	expected := "\x1b[38;5;170mA\x1b[38;5;133mB\x1b[38;5;31mC\x1b[38;5;39mD\x1b[0mE"
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
		{"/root/agent6", 39, 31}, {"/root/agent1", 170, 133}, {"/root/agent5", 38, 30},
		{"/root/agent0", 99, 61}, {"/root/agent3", 37, 29}, {"/root/agent7", 133, 96}, {"/root/agent2", 74, 67},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen := vt.NewEmulator(4, 1)
			defer screen.Close()
			fmt.Fprint(screen, terminalui.FaintFallback(terminalui.AgentColor(tc.name)+"N"+terminalui.Reset+terminalui.DimAgentColor(tc.name)+"D"+terminalui.Reset+"X"))
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
		if color, dim := terminalui.AgentColor(address), terminalui.DimAgentColor(address); color != terminalui.AgentColor(task) || dim != terminalui.DimAgentColor(task) {
			t.Fatalf("address %q differs from task %q: %q, %q", address, task, color, dim)
		}
	}
}
