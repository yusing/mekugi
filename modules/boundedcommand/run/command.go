package run

import (
	"github.com/yusing/mekugi/contracts/command"
)

// Source: internal/router/toolplugin/native_builtins.go:12:12@[e702dffa] mrun description.
var Command = command.Contribution{Spec: command.Spec{Name: "mrun", Description: "Bound one foreground command's output, retaining its beginning or end. Use for noisy commands when a bounded head or tail is sufficient; output outside the selected window is discarded. Usage: `mrun (-n N|--max-tokens N) [--tail] [--] COMMAND [ARG...]`. Stock yielded sessions and write_stdin still own interactive continuation."}, Run: Execute}

// Service carries no background state; each command owns its local capture.
type Service struct{}
