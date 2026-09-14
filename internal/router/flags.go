package router

import (
	"flag"
	"fmt"
	"io"
	"time"
)

type routerFlags struct {
	*flag.FlagSet
	timeout                  *time.Duration
	streamIdleTimeout        *time.Duration
	mode                     *string
	modelProtocol            *string
	mainMentorHandoffEnabled *bool
	mentorHandoffEnabled     *bool
	grokEnabled              *bool
	grokAuthFile             *string
	captureOutput            *string
	metricsOutput            *string
	debug                    *bool
}

func newRouterFlags(stderr io.Writer) routerFlags {
	flags := flag.NewFlagSet("mekugi", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mekugi [flags] codex [Codex arguments...]")
		fmt.Fprintln(stderr, "       mekugi inspect-session --session PATH [options]")
		flags.PrintDefaults()
	}
	return routerFlags{
		FlagSet:                  flags,
		timeout:                  flags.Duration("timeout", defaultRequestTimeout, "upstream response-start timeout"),
		streamIdleTimeout:        flags.Duration("stream-idle-timeout", defaultStreamIdleTimeout, "maximum upstream inactivity between WebSocket messages or HTTP response bytes"),
		mode:                     flags.String("mode", defaultRewriteMode, "response mode: mekugi or passthrough"),
		modelProtocol:            flags.String("model-protocol", defaultModelProtocol, "model protocol: native or ctp2"),
		mainMentorHandoffEnabled: flags.Bool("main-mentor-handoff", true, "start eligible main threads with a mentor model"),
		mentorHandoffEnabled:     flags.Bool("mentor-handoff", true, "start eligible spawned subagents with a mentor model"),
		grokEnabled:              flags.Bool("grok", false, "enable native Grok subagents and plaintext collaboration projection"),
		grokAuthFile:             flags.String("grok-auth-file", "", "Grok OAuth credential file (default ~/.grok/auth.json)"),
		metricsOutput:            flags.String("metrics-output", "", "optional final metrics JSON path"),
		captureOutput:            flags.String("capture-output", "", "optional sanitized capture JSONL path"),
		debug:                    flags.Bool("debug", false, "record diagnostics, capture, metrics, instructions, runtime reads, and AX report; print artifact paths on exit"),
	}
}

// SplitCommand parses router flags without consuming the following command or its arguments.
func SplitCommand(args []string) (routerArgs, command []string, err error) {
	flags := newRouterFlags(io.Discard)
	if err := flags.Parse(args); err != nil {
		return nil, nil, err
	}
	command = flags.Args()
	routerArgs = args[:len(args)-len(command)]
	return routerArgs, command, err
}

// PrintUsage describes the session-only launch interface.
func PrintUsage(w io.Writer) { newRouterFlags(w).Usage() }
