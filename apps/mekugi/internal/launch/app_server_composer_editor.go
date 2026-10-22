package launch

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

func runEditor(ctx context.Context, path string, stdin, stdout *os.File) error {
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("VISUAL"))
	}
	if editor == "" {
		editor = "vi"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// EDITOR is a user-owned shell command; the file path is a positional
	// argument, never interpolated into that command.
	cmd := exec.CommandContext(ctx, "sh", "-c", "exec "+editor+` "$1"`, "mekugi-editor", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stdout
	return cmd.Run()
}
