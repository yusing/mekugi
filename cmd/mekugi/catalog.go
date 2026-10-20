package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/yusing/mekugi/internal/router"
	"golang.org/x/term"
)

// prepareProviderCatalog uses bundled metadata for third-party-only launches,
// without depending on Codex login.
// The launched session uses a private static catalog instead of rereading the
// provider-neutral models cache that other Codex processes can replace.
func prepareProviderCatalog(ctx context.Context, executable string, args []string, session router.Session) (directory, path string, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stopProgress := startCatalogProgress(ctx, os.Stderr, term.IsTerminal(int(os.Stderr.Fd())))
	defer func() {
		if stopProgress() != nil {
			// Progress is auxiliary. A failed display must not replace the catalog
			// result or cancel startup; this content-free notice is best-effort too.
			fmt.Fprintln(os.Stderr, "mekugi: catalog progress unavailable")
		}
	}()
	cmd := exec.CommandContext(ctx, executable, "debug", "models", "--bundled")
	cmd.Dir = catalogWorkingDirectory(args)
	cmd.WaitDelay = 5 * time.Second
	// Do not forward diagnostics from the bootstrap command into the TUI or
	// include configuration/authentication details in a startup error.
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("prepare Codex model catalog: %w", err)
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("prepare Codex model catalog: %w", ctx.Err())
		}
		return "", "", fmt.Errorf("start codex debug models: %w", err)
	}
	const maxCatalogBytes = 8 << 20
	body, readErr := io.ReadAll(io.LimitReader(stdout, maxCatalogBytes+1))
	if readErr != nil || len(body) > maxCatalogBytes {
		cancel()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return "", "", fmt.Errorf("read Codex model catalog: %w", readErr)
	}
	if len(body) > maxCatalogBytes {
		return "", "", errors.New("codex model catalog exceeds 8 MiB")
	}
	if err := ctx.Err(); err != nil {
		return "", "", fmt.Errorf("prepare Codex model catalog: %w", err)
	}
	if waitErr != nil {
		return "", "", fmt.Errorf("codex debug models failed: %w; check that Codex supports this command and its catalog configuration is valid", waitErr)
	}
	body, err = router.ProviderModelCatalog(body, session)
	if err != nil {
		return "", "", fmt.Errorf("prepare provider model catalog: %w", err)
	}
	directory, err = os.MkdirTemp("", "mekugi-models-")
	if err != nil {
		return "", "", fmt.Errorf("create private model catalog directory: %w", err)
	}
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return "", "", errors.Join(fmt.Errorf("resolve private model catalog directory: %w", err), os.RemoveAll(directory))
	}
	directory = absoluteDirectory
	path = filepath.Join(directory, "models.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", "", errors.Join(fmt.Errorf("write private model catalog: %w", err), os.RemoveAll(directory))
	}
	return directory, path, nil
}

// startCatalogProgress owns startup rendering until its returned stop function
// joins the renderer and clears interactive output before terminal handoff.
func startCatalogProgress(ctx context.Context, output io.Writer, interactive bool) func() error {
	render := func(message string) error {
		format := "%s\n"
		if interactive {
			// Keep ASCII status strictly narrower than the terminal so it cannot
			// wrap or scroll, even at the bottom row. Unknown width hides status.
			columns := 0
			if file, ok := output.(*os.File); ok {
				columns, _, _ = term.GetSize(int(file.Fd()))
			}
			message = message[:min(len(message), max(columns-1, 0))]
			format = "\r\x1b[2K%s"
		}
		_, err := fmt.Fprintf(output, format, message)
		return err
	}
	if err := render("mekugi: preparing Grok/OpenCode model catalog..."); err != nil {
		return func() error { return err }
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	var progressErr error
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if progressErr = render("mekugi: still waiting for Codex model catalog (1m limit; Ctrl-C cancels)"); progressErr != nil {
					return
				}
			}
		}
	}()
	return func() error {
		close(done)
		<-stopped
		if interactive && progressErr == nil {
			progressErr = render("")
		}
		return progressErr
	}
}

// Bundled metadata needs no host configuration. Apply -C through the bootstrap
// process's working directory, without interpreting config operands or prompts.
func catalogWorkingDirectory(args []string) (cwd string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if _, ok := codexArgumentValue(args, &i, "-c", "--config"); ok {
			continue
		}
		if value, ok := codexArgumentValue(args, &i, "-C", "--cd"); ok {
			cwd = value
		}
	}
	return cwd
}
