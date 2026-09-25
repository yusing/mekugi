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
	usageReport              *string
	mainMentorHandoffEnabled *bool
	mentorHandoffEnabled     *bool
	postCompactRecovery      *bool
	grokEnabled              *bool
	grokAuthFile             *string
	exploreFilter            *bool
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
	usageReport := new(string)
	flags.Func("usage-report", "usage report: off, compact, or table (default compact; table for multiple agents)", func(value string) error {
		switch value {
		case "off", "compact", "table":
			*usageReport = value
			return nil
		}
		return fmt.Errorf("--usage-report must be off, compact, or table")
	})
	return routerFlags{
		usageReport:              usageReport,
		FlagSet:                  flags,
		timeout:                  flags.Duration("timeout", defaultRequestTimeout, "upstream response-start timeout"),
		streamIdleTimeout:        flags.Duration("stream-idle-timeout", defaultStreamIdleTimeout, "maximum upstream inactivity between WebSocket messages or HTTP response bytes"),
		mode:                     flags.String("mode", defaultRewriteMode, "response mode: mekugi or passthrough"),
		mainMentorHandoffEnabled: flags.Bool("main-mentor-handoff", true, "start eligible main threads with a mentor model"),
		mentorHandoffEnabled:     flags.Bool("mentor-handoff", true, "start eligible spawned subagents with a mentor model"),
		postCompactRecovery:      flags.Bool("post-compact-recovery", true, "restore journal and change context after compaction through a pre-trusted Codex hook"),
		grokEnabled:              flags.Bool("grok", false, "enable Grok models and plaintext collaboration projection"),
		grokAuthFile:             flags.String("grok-auth-file", "", "Grok OAuth credential file (default ~/.grok/auth.json)"),
		exploreFilter:            flags.Bool("explore-filter", true, "omit search results that TypeSafe Jev judges unrelated to the task when TYPESAFE_API_KEY is set"),
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
