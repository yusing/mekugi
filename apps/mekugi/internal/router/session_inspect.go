package router

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"

	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

// RunSessionInspection is an offline, read-only entry point. It deliberately does
// not open the writable replay store or install a router/runtime.
func RunSessionInspection(inspector *query.Service, diagnostics *diagnostic.Service, ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("inspect-session", flag.ContinueOnError)
	flags.SetOutput(stderr)
	session := flags.String("session", "", "Codex rollout JSONL file")
	debugDir := flags.String("debug-dir", "", "inspect one debug bundle instead of a rollout")
	requestID := flags.String("request-id", "", "select one request in a debug bundle")
	workspace := flags.String("workspace", "", "override workspace inferred from rollout metadata")
	replayDir := flags.String("replay-dir", "", "replay directory (default platform state directory)")
	ax := flags.Bool("ax", false, "include whole-rollout AX measurements, independent of call pagination")
	failures := flags.Bool("failures", false, "list private failures with complete error text, optionally selecting one reference")
	readLog := flags.String("read-log", "", "runtime AX read log; implies --ax")
	defects := flags.String("defects", "", "JSON defect assessments with evidence paths; implies --ax")
	callID := flags.String("call-id", "", "select one call identity")
	offset := flags.Int("offset", 0, "skip this many matching calls")
	limit := flags.Int("limit", 50, "maximum calls returned (1-500)")
	field := flags.String("field", "", "include text: script, report, diagnostic, output, or all")
	textBytes := flags.Int("text-bytes", 4096, "maximum UTF-8 bytes per included field (1-65536)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mekugi inspect-session --session PATH [options]")
		fmt.Fprintln(stderr, "       mekugi inspect-session --debug-dir DIR [--request-id ID] [options]")
		fmt.Fprintln(stderr, "       mekugi inspect-session [--replay-dir PATH] --failures [REF]")
		fmt.Fprintln(stderr, "Read local evidence without running operations. Rollout and debug text requires --field.")
		fmt.Fprintln(stderr, "--failures includes complete private errors, which may contain request content or credentials.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi inspect-session:", err)
		return 1
	}
	withAX := *ax || *readLog != "" || *defects != ""
	if withAX && !inspector.HasAX() {
		return fail(errors.New("agent metrics are unavailable in this composition"))
	}
	fields := []string{"script", "report", "diagnostic", "output"}
	if *debugDir != "" {
		if flags.NArg() != 0 || *session != "" || *failures || *ax || *readLog != "" || *defects != "" ||
			*callID != "" || *workspace != "" || *replayDir != "" || *offset < 0 || *limit < 1 || *limit > 500 ||
			*textBytes < 1 || *textBytes > 65536 || (*field != "" && *field != "all" && *field != "diagnostic") {
			flags.Usage()
			return 2
		}
		if diagnostics == nil {
			return fail(errors.New("diagnostics are unavailable in this composition"))
		}
		result, failures := diagnostics.InspectBundle(ctx, diagnostic.InspectOptions{Directory: *debugDir, RequestID: *requestID, Field: *field, Offset: *offset, Limit: *limit, TextBytes: *textBytes})
		code := 0
		for _, failure := range failures {
			fmt.Fprintf(stderr, "mekugi inspect-session: %s: %v\n", failure.Path, failure.Err)
			code = 1
		}
		if err := json.MarshalEncode(jsontext.NewEncoder(stdout), &result, json.Deterministic(true)); err != nil {
			return fail(err)
		}
		return code
	}
	if *requestID != "" {
		flags.Usage()
		return 2
	}
	if *failures {
		if flags.NArg() > 1 || *session != "" || *ax || *readLog != "" || *defects != "" || *field != "" || *callID != "" || *workspace != "" ||
			*offset != 0 || *limit != 50 || *textBytes != 4096 {
			flags.Usage()
			return 2
		}
		if *replayDir == "" {
			var err error
			*replayDir, err = ReplayDirectory()
			if err != nil {
				return fail(err)
			}
		}
		if diagnostics == nil {
			return fail(errors.New("diagnostics are unavailable in this composition"))
		}
		records, err := diagnostics.ReadFailures(ctx, *replayDir, flags.Arg(0))
		if err != nil {
			return fail(err)
		}
		if err := json.MarshalEncode(jsontext.NewEncoder(stdout), records); err != nil {
			return fail(err)
		}
		return 0
	}
	if *session == "" || flags.NArg() != 0 || *offset < 0 ||
		*limit < 1 || *limit > 500 || *textBytes < 1 || *textBytes > 65536 ||
		(*field != "" && *field != "all" && !slices.Contains(fields, *field)) {
		flags.Usage()
		return 2
	}
	if *replayDir == "" {
		var err error
		*replayDir, err = ReplayDirectory()
		if err != nil {
			return fail(err)
		}
	}
	result, err := inspector.Inspect(ctx, query.SingleOptions{Session: *session, ReplayDirectory: *replayDir, Workspace: *workspace,
		CallID: *callID, Offset: *offset, Limit: *limit, Field: *field, TextBytes: *textBytes, AX: withAX, ReadLog: *readLog, Defects: *defects})
	if err != nil {
		return fail(err)
	}
	if err := json.MarshalEncode(jsontext.NewEncoder(stdout), &result, jsonv1.DefaultOptionsV1(), jsontext.EscapeForHTML(false)); err != nil {
		return fail(err)
	}
	return 0
}
