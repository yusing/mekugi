package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/router"
)

// prepareGrokCatalog leaves authentication and configuration loading to Codex.
// The launched session uses a private static catalog instead of rereading the
// provider-neutral models cache that other Codex processes can replace.
func prepareGrokCatalog(ctx context.Context, executable, baseURL string, args []string) (directory, path string, err error) {
	overrides, cwd, err := catalogConfigArgs(args)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, codexArgs(baseURL, append([]string{"debug", "models"}, overrides...), false)...)
	cmd.Dir = cwd
	cmd.WaitDelay = 5 * time.Second
	// Do not forward diagnostics from the bootstrap command into the TUI or
	// include configuration/authentication details in a startup error.
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("prepare Codex model catalog: %w", err)
	}
	if err := cmd.Start(); err != nil {
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
		return "", "", errors.New("Codex model catalog exceeds 8 MiB")
	}
	if waitErr != nil {
		return "", "", fmt.Errorf("codex debug models failed: %w; check that Codex supports this command and its catalog configuration is valid", waitErr)
	}
	body, err = router.GrokModelCatalog(body)
	if err != nil {
		return "", "", fmt.Errorf("prepare Grok model catalog: %w", err)
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

// Only configuration selectors belong to debug models, not the user's command,
// prompt, or arguments after --. Reject configuration modes debug models cannot
// honor, and apply -C through the bootstrap process's working directory.
func catalogConfigArgs(args []string) (overrides []string, cwd string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--profile" || strings.HasPrefix(arg, "--profile=") || strings.HasPrefix(arg, "-p") {
			return nil, "", errors.New("--grok does not support --profile: codex debug models cannot load named profiles; use the default configuration or -c model_catalog_json instead")
		}
		if arg == "--ignore-user-config" || strings.HasPrefix(arg, "--ignore-user-config=") {
			return nil, "", errors.New("--grok does not support --ignore-user-config: codex debug models cannot exclude user configuration")
		}
		var kind, value string
		switch {
		case arg == "-c" || arg == "--config" || arg == "-C" || arg == "--cd":
			if i+1 == len(args) {
				continue
			}
			i++
			kind, value = arg, args[i]
		case strings.HasPrefix(arg, "--config="):
			kind, value = "-c", strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "--cd="):
			kind, value = "-C", strings.TrimPrefix(arg, "--cd=")
		case strings.HasPrefix(arg, "-c"):
			kind, value = "-c", strings.TrimPrefix(strings.TrimPrefix(arg, "-c"), "=")
		case strings.HasPrefix(arg, "-C"):
			kind, value = "-C", strings.TrimPrefix(strings.TrimPrefix(arg, "-C"), "=")
		}
		switch kind {
		case "-c", "--config":
			overrides = append(overrides, "-c", value)
		case "-C", "--cd":
			cwd = value
		}
	}
	return overrides, cwd, nil
}
