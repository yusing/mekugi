// Package presets owns reusable prompt slots and their editing workflow.
package presets

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const Limit = 8000

// Slot is Codex's native tui.presets value. An empty label shows the input's
// first line instead.
type Slot struct {
	Input string `json:"input"`
	Label string `json:"label"`
}

// Store borrows the active host configuration owner, never a second persistence path.
type Store interface {
	Load() ([10]Slot, error)
	// Save persists a validated workflow choice; empty input deletes it.
	Save(slot int, value Slot) (Slot, error)
}

// Source: codex-rs/config/src/types.rs QuickDial::validate@rust-v0.164.0.
func Validate(input string) error {
	switch {
	case strings.TrimSpace(input) == "":
		return errors.New("Preset input cannot be empty")
	case strings.HasPrefix(strings.TrimSpace(input), "!"):
		return errors.New("Presets support prompts and slash commands, not shell commands")
	case !utf8.ValidString(input):
		return errors.New("Preset input must be valid UTF-8")
	case len(input) > Limit:
		return errors.New("Preset input cannot exceed 8,000 UTF-8 bytes")
	}
	return nil
}

// ValidateLabel accepts the trimmed label a menu row can show. Codex leaves
// labels unconstrained and shows only their first line, so Mekugi keeps them
// single-line rather than saving text the menu would hide.
// Source: codex-rs/config/src/types.rs:832@0df4aa677 QuickDial::label and
// codex-rs/tui/src/chatwidget/quick_dials.rs:39@0df4aa677 quick_dial_label.
func ValidateLabel(label string) error {
	switch {
	case !utf8.ValidString(label):
		return errors.New("Preset label must be valid UTF-8")
	case strings.ContainsAny(label, "\r\n"):
		return errors.New("Preset label must be a single line")
	}
	return nil
}
