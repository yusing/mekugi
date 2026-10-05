package router

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

type routerFlags struct {
	*flag.FlagSet
	timeout             *time.Duration
	streamIdleTimeout   *time.Duration
	mode                *string
	ansiFaint           *string
	postCompactRecovery *bool
	vcsGuard            *bool
	journalCompaction   *string
	grokAuthFile        *string
	captureOutput       *string
	debug               *bool
}

func newRouterFlags(stderr io.Writer) routerFlags {
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
	return routerFlags{
		FlagSet:             flags,
		ansiFaint:           flags.String("ansi-faint", "auto", "dim text: auto (detect mosh), on (ANSI faint), or off (fixed colors)"),
		timeout:             flags.Duration("timeout", defaultRequestTimeout, "upstream response-start timeout"),
		streamIdleTimeout:   flags.Duration("stream-idle-timeout", defaultStreamIdleTimeout, "maximum upstream inactivity between WebSocket messages or HTTP response bytes"),
		mode:                flags.String("mode", defaultRewriteMode, "response mode: mekugi or passthrough"),
		postCompactRecovery: flags.Bool("post-compact-recovery", true, "restore journal and change context after compaction through a pre-trusted Codex hook"),
		vcsGuard:            flags.Bool("vcs-guard", true, "ask before remote VCS writes in the UI, independently of Codex approval policy"),
		journalCompaction:   flags.String("journal-compaction", "off", "journal compaction: auto, slice, or off (default remains gated on evaluation)"),
		grokAuthFile:        flags.String("grok-auth-file", "", "Grok OAuth credential file (default ~/.grok/auth.json)"),
		captureOutput:       flags.String("capture-output", "", "optional sanitized capture JSONL path"),
		debug:               flags.Bool("debug", false, "record diagnostics, capture, metrics, instructions, runtime reads, and AX report; print session diagnosis command on exit"),
	}
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
