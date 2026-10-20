// Package options owns shared invocation parsing and local configuration.
package options

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// Source: apps/mekugi/internal/router/flags.go:12:160@[e448711c]

// DefaultRequestTimeout bounds the wait for the upstream response to start.
const DefaultRequestTimeout = 10 * time.Minute

// Config is a parsed invocation configuration. It contains values only;
// execution derives effective output paths without modifying the selected options.
type Config struct {
	Timeout           time.Duration
	StreamIdleTimeout time.Duration
	Mode              string
	ANSIFaint         string
	ShellGuard        bool
	DeduplicateOutput bool
	GrokAuthFile      string
	CaptureOutput     string
	Debug             bool
	Grok              bool
	ThirdParty        bool
}

// FrontendsEnabled reports whether this parsed invocation exposes native helpers.
func (c Config) FrontendsEnabled() bool { return c.Mode == "mekugi" }

// Parse selects the launch scope without opening resources.
func Parse(args []string) (Config, error) {
	config := Config{Mode: "mekugi"}
	flags := newFlags(io.Discard, &config)
	if err := flags.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}
	config.Grok = flags.NArg() == 1 && flags.Arg(0) == "grok"
	config.ThirdParty = flags.NArg() == 1 && flags.Arg(0) == "third-party"

	switch {
	case flags.NArg() != 0 && !config.Grok && !config.ThirdParty:
		return Config{}, errors.New("positional arguments are not supported")
	case !config.Grok && !config.ThirdParty && config.GrokAuthFile != "":
		return Config{}, errors.New("--grok-auth-file requires standalone mekugi or mekugi grok")
	case config.Timeout <= 0:
		return Config{}, errors.New("--timeout must be positive")
	case config.StreamIdleTimeout <= 0:
		return Config{}, errors.New("--stream-idle-timeout must be positive")
	}
	return config, nil
}

func newFlags(stderr io.Writer, config *Config) *flag.FlagSet {
	flags := flag.NewFlagSet("mekugi", flag.ContinueOnError)
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
	flags.StringVar(&config.ANSIFaint, "ansi-faint", "auto", "dim text: auto (detect mosh), on (ANSI faint), or off (fixed colors)")
	flags.DurationVar(&config.Timeout, "timeout", DefaultRequestTimeout, "upstream response-start timeout")
	flags.DurationVar(&config.StreamIdleTimeout, "stream-idle-timeout", 4*time.Minute, "maximum upstream inactivity between WebSocket messages or HTTP response bytes")
	flags.BoolVar(&config.ShellGuard, "shell-guard", true, "ask before remote VCS writes and sudo commands in the UI, independently of Codex approval policy")
	flags.BoolVar(&config.DeduplicateOutput, "deduplicate-output", true, "deduplicate output and attachments in model input")
	flags.StringVar(&config.GrokAuthFile, "grok-auth-file", "", "Grok OAuth credential file (default ~/.grok/auth.json)")
	flags.StringVar(&config.CaptureOutput, "capture-output", "", "optional sanitized capture JSONL path")
	flags.BoolVar(&config.Debug, "debug", false, "record diagnostics, capture, metrics, instructions, runtime reads, and AX report; print session diagnosis command on exit")
	return flags
}

// SplitCommand parses invocation flags without consuming the following command or its arguments.
func SplitCommand(args []string) (routerArgs, command []string, err error) {
	flags := newFlags(io.Discard, new(Config))
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
func PrintUsage(w io.Writer) { newFlags(w, new(Config)).Usage() }
