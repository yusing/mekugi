// Package uisnapshot supports rendered UI regression fixtures in tests.
// It is not used by production renderers.
package uisnapshot

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/pmezard/go-difflib/difflib"
)

// Assert compares rendered output with a reviewed fixture, stripping only ANSI
// sequences. A mismatch writes path + ".new" without replacing the fixture.
// MEKUGI_UPDATE_UI_SNAPSHOTS=1 explicitly replaces fixtures instead.
func Assert(t testing.TB, path, rendered string) {
	t.Helper()
	if err := check(path, rendered, os.Getenv("MEKUGI_UPDATE_UI_SNAPSHOTS") == "1"); err != nil {
		t.Fatal(err)
	}
}

// AssertTerminal compares terminal cells, including styles and links, with a
// reviewed fixture. Rows come from the actual renderer, not a snapshot renderer.
// Canonical ANSI output is quoted so equivalent escape sequences compare equal
// and fixture diffs cannot execute terminal controls. A following plain row can
// expose styles that leak out of the rendered content.
func AssertTerminal(t testing.TB, path string, rows []string, width int) {
	t.Helper()
	Assert(t, path, terminalFrame(t, rows, width))
}

func terminalFrame(t testing.TB, rows []string, width int) string {
	t.Helper()
	if width < 1 || len(rows) == 0 {
		t.Fatal("terminal snapshot needs a positive width and at least one row")
	}
	screen := vt.NewEmulator(width, len(rows))
	defer screen.Close()
	for y, row := range rows {
		if ansi.StringWidth(row) > width {
			t.Fatalf("terminal snapshot row %d exceeds width %d", y, width)
		}
		if _, err := fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row); err != nil {
			t.Fatal(err)
		}
	}
	var frame strings.Builder
	for _, row := range strings.Split(screen.Render(), "\n") {
		fmt.Fprintf(&frame, "%q\n", row)
	}
	return frame.String()
}

func check(path, rendered string, update bool) error {
	got := ansi.Strip(rendered)
	if update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			return fmt.Errorf("update UI snapshot %s: %w", path, err)
		}
		return removeCandidate(path)
	}
	want, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read UI snapshot %s: %w", path, err)
	}
	if err == nil && string(want) == got {
		return removeCandidate(path)
	}
	if err := os.WriteFile(path+".new", []byte(got), 0o644); err != nil {
		return fmt.Errorf("write UI snapshot candidate %s.new: %w", path, err)
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(string(want)), B: difflib.SplitLines(got),
		FromFile: path, ToFile: path + ".new", Context: 3,
	})
	if err != nil {
		return fmt.Errorf("diff UI snapshot %s: %w", path, err)
	}
	return fmt.Errorf("UI snapshot mismatch; review %s.new before updating the fixture:\n%s", path, diff)
}

func removeCandidate(path string) error {
	if err := os.Remove(path + ".new"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove UI snapshot candidate %s.new: %w", path, err)
	}
	return nil
}
