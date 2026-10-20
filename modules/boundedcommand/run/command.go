package run

import (
	"github.com/yusing/mekugi/contracts/command"
)

// Source: internal/router/toolplugin/native_builtins.go:12:12@[e702dffa] mrun description.
var Command = command.Definition[Service]{Spec: command.Spec{Name: "mrun", Description: "Bound one foreground command's output, retaining its beginning or end. Usage: `mrun (-n N|--max-tokens N) [--tail] [--] COMMAND [ARG...]`. Stock yielded sessions and write_stdin still own interactive continuation. Output outside the selected window is discarded. Requires an explicit -n or --max-tokens limit. -n N selects up to N rows; --tail selects the last rows in source order and requires -n or --max-tokens. The selected token window may end within a row; delivery overflow keeps row boundaries, except oversized or unterminated generic streams use byte continuation."}, Run: Service.Execute}

// Service carries no background state; each command owns its local capture.
type Service struct{}
