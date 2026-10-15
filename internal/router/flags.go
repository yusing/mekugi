package router

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

type routerFlags struct {
	*flag.FlagSet
	SessionConfig
}

// SessionConfig is a parsed invocation configuration. It contains values only;
// execution derives effective output paths without modifying the selected options.
type SessionConfig struct {
	timeout           time.Duration
	streamIdleTimeout time.Duration
	mode              string
	ansiFaint         string
	vcsGuard          bool
	duplicateOutput   bool
	grokAuthFile      string
	captureOutput     string
	debug             bool
	grok              bool
	standalone        bool
}

// FrontendsEnabled reports whether this parsed invocation exposes native helpers.
func (c SessionConfig) FrontendsEnabled() bool { return c.mode == "mekugi" }

// ParseSessionConfig selects the launch scope without opening resources.
func ParseSessionConfig(args []string) (SessionConfig, error) {
	flags := newRouterFlags(io.Discard)
	if err := flags.Parse(args); err != nil {
		return SessionConfig{}, fmt.Errorf("parse flags: %w", err)
	}
	config := flags.SessionConfig
	config.grok = flags.NArg() == 1 && flags.Arg(0) == "grok"
	config.standalone = flags.NArg() == 1 && flags.Arg(0) == "third-party"

	switch {
	case flags.NArg() != 0 && !config.grok && !config.standalone:
		return SessionConfig{}, errors.New("positional arguments are not supported")
	case config.mode != "mekugi" && config.mode != "passthrough":
		return SessionConfig{}, errors.New("--mode must be mekugi or passthrough")
	case (config.grok || config.standalone) && config.mode != "mekugi":
		return SessionConfig{}, errors.New("third-party launches require --mode mekugi")
	case !config.grok && !config.standalone && config.grokAuthFile != "":
		return SessionConfig{}, errors.New("--grok-auth-file requires standalone mekugi or mekugi grok")
	case config.timeout <= 0:
		return SessionConfig{}, errors.New("--timeout must be positive")
	case config.streamIdleTimeout <= 0:
		return SessionConfig{}, errors.New("--stream-idle-timeout must be positive")
	}
	return config, nil
}

func newRouterFlags(stderr io.Writer) *routerFlags {
	flags := &routerFlags{FlagSet: flag.NewFlagSet("mekugi", flag.ContinueOnError)}
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mekugi [flags] [Codex arguments...]")
		fmt.Fprintln(stderr, "       mekugi [flags] codex [Codex arguments...]")
		fmt.Fprintln(stderr, "       mekugi [flags] grok [Codex arguments...]")
		fmt.Fprintln(stderr, "       mekugi [flags] codex headless --yolo [-m MODEL] [-c KEY=VALUE] < prompt.txt")
		fmt.Fprintln(stderr, "       mekugi inspect-session --session PATH [options]")
		fmt.Fprintln(stderr, "       mekugi inspect-storage --workspace DIR --call-id ID [options]")
		flags.PrintDefaults()
	}
	flags.StringVar(&flags.ansiFaint, "ansi-faint", "auto", "dim text: auto (detect mosh), on (ANSI faint), or off (fixed colors)")
	flags.DurationVar(&flags.timeout, "timeout", defaultRequestTimeout, "upstream response-start timeout")
	flags.DurationVar(&flags.streamIdleTimeout, "stream-idle-timeout", defaultStreamIdleTimeout, "maximum upstream inactivity between WebSocket messages or HTTP response bytes")
	flags.StringVar(&flags.mode, "mode", defaultRewriteMode, "response mode: mekugi or passthrough")
	flags.BoolVar(&flags.vcsGuard, "vcs-guard", true, "ask before remote VCS writes in the UI, independently of Codex approval policy")
	flags.BoolVar(&flags.duplicateOutput, "deduplicate-output", true, "deduplicate output and attachments in model input")
	flags.StringVar(&flags.grokAuthFile, "grok-auth-file", "", "Grok OAuth credential file (default ~/.grok/auth.json)")
	flags.StringVar(&flags.captureOutput, "capture-output", "", "optional sanitized capture JSONL path")
	flags.BoolVar(&flags.debug, "debug", false, "record diagnostics, capture, metrics, instructions, runtime reads, and AX report; print session diagnosis command on exit")
	return flags
}

// SplitCommand parses router flags without consuming the following command or its arguments.
func SplitCommand(args []string) (routerArgs, command []string, err error) {
	flags := newRouterFlags(io.Discard)
	index := 0
	for index < len(args) {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			break
		}
		name := strings.TrimLeft(arg, "-")
		name, _, inline := strings.Cut(name, "=")
		item := flags.Lookup(name)
		if item == nil {
			if name == "grok" || name == "h" || name == "help" {
				return nil, nil, flags.Parse(args)
			}
			break // Codex options belong to standalone mode.
		}
		index++
		boolean, isBoolean := item.Value.(interface{ IsBoolFlag() bool })
		if !inline && (!isBoolean || !boolean.IsBoolFlag()) && index < len(args) {
			index++
		}
	}
	if err := flags.Parse(args[:index]); err != nil {
		return nil, nil, err
	}
	command = args[index:]
	routerArgs = args[:index]
	return routerArgs, command, err
}

// HasModelOverride distinguishes a user's model selection from an automatic
// launch default, without interpreting option operands or prompt text.
func HasModelOverride(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-m" || arg == "--model" || strings.HasPrefix(arg, "--model=") || strings.HasPrefix(arg, "-m") {
			return true
		}
		var value string
		switch {
		case arg == "-c" || arg == "--config":
			if i+1 < len(args) {
				i++
				value = args[i]
			}
		case strings.HasPrefix(arg, "--config="):
			value = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-c"):
			value = strings.TrimPrefix(strings.TrimPrefix(arg, "-c"), "=")
		default:
			switch arg {
			case "--enable", "--disable", "-i", "--image", "-p", "--profile", "-s", "--sandbox", "-a", "--ask-for-approval", "-C", "--cd", "--add-dir", "-o", "--output-last-message", "--output-schema", "--color", "--local-provider", "--thread-source":
				i++
			}
		}
		key, _, _ := strings.Cut(value, "=")
		if strings.Trim(strings.TrimSpace(key), `"'`) == "model" {
			return true
		}
	}
	return false
}

// PrintUsage describes the session-only launch interface.
func PrintUsage(w io.Writer) { newRouterFlags(w).Usage() }
