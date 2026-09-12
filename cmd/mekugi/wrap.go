package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/router"
)

func runWrap(routerArgs, args []string) int {
	if len(args) == 0 || args[0] != "codex" {
		fmt.Fprintln(os.Stderr, "usage: mekugi [flags] codex [Codex arguments...]")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	// Codex shares the foreground process group and handles terminal Ctrl-C itself.
	// Catch it here without canceling the router or delivering a second interrupt.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	code, err := wrapCodex(ctx, routerArgs, args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "mekugi:", err)
	}
	return code
}

func wrapCodex(ctx context.Context, routerArgs, args []string) (code int, runErr error) {
	if err := validateCodexArgs(args); err != nil {
		return 2, err
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		return 1, fmt.Errorf("locate codex: %w", err)
	}
	issues := router.NewCriticalErrors()
	defer func() {
		for _, message := range issues.Pending() {
			fmt.Fprintln(os.Stderr, message)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan router.Session, 1)
	routerDone := make(chan error, 1)
	var debugPaths []string
	defer func() {
		for _, path := range debugPaths {
			fmt.Fprintf(os.Stderr, "mekugi debug: %s\n", path)
		}
	}()
	go func() {
		routerDone <- router.RunSession(ctx, routerArgs, issues, func(session router.Session) {
			ready <- session
		}, func(paths []string) { debugPaths = paths })
	}()
	var session router.Session
	select {
	case err := <-routerDone:
		return 1, err
	case session = <-ready:
	}
	if session.GrokEnabled {
		catalogDirectory, catalogPath, err := prepareGrokCatalog(ctx, executable, session.BaseURL, args)
		if err != nil {
			cancel()
			return 1, errors.Join(err, <-routerDone)
		}
		defer func() {
			if err := os.RemoveAll(catalogDirectory); err != nil {
				runErr = errors.Join(runErr, err)
				code = 1
			}
		}()
		index := slices.Index(args, "--")
		if index < 0 {
			index = len(args)
		}
		args = slices.Insert(slices.Clone(args), index, "-c", fmt.Sprintf("model_catalog_json=%q", catalogPath))
	}
	// Announce once before Codex takes over the terminal, never during its UI.
	fmt.Fprintf(os.Stderr, "mekugi dashboard: %s/\n", strings.TrimSuffix(session.BaseURL, "/v1"))
	cmd := exec.CommandContext(ctx, executable, codexArgs(session.BaseURL, args, session.JournalEnabled)...)
	cmd.Env = append(os.Environ(), "MEKUGI_BASE_URL="+session.BaseURL)
	if session.AXReadOutput != "" {
		cmd.Env = append(cmd.Env, capturer.AXReadOutputEnvironment+"="+session.AXReadOutput)
	}
	if session.FrontendDirectory != "" {
		cmd.Env = append(cmd.Env, "PATH="+session.FrontendDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return 1, errors.Join(fmt.Errorf("launch codex: %w", err), <-routerDone)
	}
	codexDone := make(chan error, 1)
	go func() { codexDone <- cmd.Wait() }()
	var codexErr, routerErr error
	select {
	case codexErr = <-codexDone:
		cancel()
		routerErr = <-routerDone
	case routerErr = <-routerDone:
		if routerErr == nil && ctx.Err() == nil {
			routerErr = errors.New("router stopped before codex exited")
		}
		cancel()
		codexErr = <-codexDone
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](codexErr); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), routerErr
		}
		return exitErr.ExitCode(), routerErr
	}
	if err := errors.Join(codexErr, routerErr); err != nil {
		return 1, err
	}
	return 0, nil
}

func codexArgs(baseURL string, args []string, journal bool) []string {
	// Keep overrides in the final command's config layer: Codex subcommands
	// can replace pre-subcommand -c settings with their own. Never cross --.
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	if journal {
		args = slices.Insert(slices.Clone(args), index, "-c", "tools.update_plan.enabled=false")
		index += 2
	}
	return slices.Insert(slices.Clone(args), index,
		"-c", `model_provider="mekugi_wrap"`,
		"-c", fmt.Sprintf(`model_providers.mekugi_wrap={name="mekugi",base_url=%q,wire_api="responses",requires_openai_auth=true,supports_websockets=true}`, baseURL),
		"-c", `include_collaboration_mode_instructions=false`,
	)
}

func validateCodexArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "--oss" || arg == "--local-provider" || strings.HasPrefix(arg, "--local-provider=") {
			return errors.New("mekugi codex does not support provider-selection arguments; custom providers are not supported")
		}
		var override string
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				i++
				override = args[i]
			}
		case strings.HasPrefix(arg, "--config="):
			override = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c"):
			override = strings.TrimPrefix(arg, "-c")
		}
		key, _, _ := strings.Cut(strings.TrimPrefix(override, "="), "=")
		root, _, _ := strings.Cut(key, ".")
		root = strings.Trim(strings.TrimSpace(root), `"'`)
		if root == "model_provider" || root == "model_providers" || root == "openai_base_url" || root == "oss_provider" {
			return errors.New("mekugi codex does not support provider overrides; it overrides config.toml provider selection and uses the router's default upstream")
		}
	}
	return nil
}
