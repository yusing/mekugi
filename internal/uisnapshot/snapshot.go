// Package uisnapshot supports plain-text rendered UI regression fixtures in tests.
// It is not used by production renderers.
package uisnapshot

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
