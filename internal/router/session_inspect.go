package router

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/backends/codex/rollout"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const maxSessionInspectionBytes = 64 << 20

type inspectedText struct {
	Text         string `json:"text,omitempty"`
	Bytes        int    `json:"bytes"`
	OmittedBytes int    `json:"omitted_bytes"`
}

type inspectedEffects struct {
	CallID      string `json:"call_id"`
	Replay      string `json:"replay"`
	Status      string `json:"status"`
	Coverage    string `json:"coverage"`
	Exit        *int   `json:"exit,omitempty"`
	ReviewFiles *int   `json:"review_files,omitempty"`
	ChangeID    string `json:"change_id,omitempty"`
	SharedWith  string `json:"shared_with,omitempty"`
}

type inspectedCall struct {
	Effects       *inspectedEffects        `json:"effects,omitempty"`
	CallID        string                   `json:"call_id"`
	Workspace     string                   `json:"workspace,omitempty"`
	Tool          string                   `json:"tool"`
	Replay        string                   `json:"replay"`
	Outcome       string                   `json:"outcome"`
	CorrelationID string                   `json:"correlation_id,omitempty"`
	Attempt       int                      `json:"attempt,omitempty"`
	Text          map[string]inspectedText `json:"text"`
}

type sessionInspection struct {
	AX                *sessionAXReport `json:"ax,omitempty"`
	Schema            string           `json:"schema"`
	WorkspaceOverride string           `json:"workspace_override,omitempty"`
	TotalCalls        int              `json:"total_calls"`
	Offset            int              `json:"offset"`
	NextOffset        *int             `json:"next_offset,omitempty"`
	Calls             []inspectedCall  `json:"calls"`
}

type sessionAXInput struct {
	rollout.InspectionMetadata
	Completion evidence.AXCompletionAccumulator
	Commands   evidence.AXCommandAccumulator
}

type sessionAXReport struct {
	Commands       evidence.AXCommandMetrics    `json:"commands"`
	Scope          string                       `json:"scope"`
	ThreadID       string                       `json:"thread_id"`
	Edits          evidence.AXEditMetrics       `json:"edits"`
	Reads          evidence.AXReadMetrics       `json:"reads"`
	Completion     evidence.AXCompletionMetrics `json:"completion"`
	Defects        evidence.AXDefectMetrics     `json:"defect_assessments"`
	UnmatchedCalls uint64                       `json:"unmatched_calls"`
}

// RunSessionInspection is an offline, read-only entry point. It deliberately does
// not open the writable replay store or install a router/runtime.
func RunSessionInspection(service *history.Service, agentMetrics *evidence.Service, diagnostics *transportcapture.Service, ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	if withAX && agentMetrics == nil {
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
		return inspectDebugSession(agentMetrics, diagnostics, ctx, *debugDir, *requestID, *field, *offset, *limit, *textBytes, stdout, stderr)
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
			*replayDir, err = defaultMekugiReplayDirectory()
			if err != nil {
				return fail(err)
			}
		}
		if err := inspectFailures(ctx, *replayDir, flags.Arg(0), stdout); err != nil {
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
	var err error
	override := ""
	if *workspace != "" {
		override, err = filepath.Abs(*workspace)
		if err == nil {
			override, err = canonicalInspectionWorkspace(override)
		}
		if err != nil {
			return fail(err)
		}
	}
	if *replayDir == "" {
		*replayDir, err = defaultMekugiReplayDirectory()
		if err != nil {
			return fail(err)
		}
	}
	// read validates record size, identity and version. Atomic store replacement
	// permits lock-free inspection without creating store.lock or changing modes.
	store := &mekugiReplayStore{publication: service.Attach(*replayDir), directory: *replayDir}
	var observations sessionAXInput
	var observer func(rollout.InspectionEvent) error
	if agentMetrics != nil {
		observer = observations.observe
	}
	decoded, err := rollout.Inspect(ctx, *session, observer)
	if err != nil {
		return fail(err)
	}
	observations.InspectionMetadata = decoded.InspectionMetadata
	calls := decoded.Calls
	allCalls := calls
	if *callID != "" {
		calls = slices.DeleteFunc(slices.Clone(calls), func(call rollout.Call) bool { return call.Item.CallID != *callID })
	}
	result := sessionInspection{
		Schema: "mekugi.session.v1", WorkspaceOverride: override, TotalCalls: len(calls),
		Offset: *offset, Calls: []inspectedCall{},
	}
	start := min(*offset, len(calls))
	end := min(len(calls), start+*limit)
	inspected := calls[start:end]
	selected := make(map[string]bool, len(inspected))
	for _, call := range inspected {
		selected[call.Item.CallID] = true
	}
	var edits evidence.AXEditAccumulator
	editCalls := make(map[string]bool)
	if withAX {
		inspected = allCalls
		result.AX = &sessionAXReport{Scope: "entire supplied rollout; edit metrics require matched observed patches",
			ThreadID: observations.ThreadID, Completion: observations.Completion.Result(), Commands: observations.Commands.Result()}
	}
	for _, call := range inspected {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		root := override
		if root == "" {
			root, err = canonicalInspectionWorkspace(call.Workspace)
			if err != nil {
				return fail(fmt.Errorf("call %q: %w", call.Item.CallID, err))
			}
		}
		var record replayRecord
		found := false
		if root != "" {
			record, found, err = store.read(root, call.Item.CallID, false)
			if err != nil {
				return fail(fmt.Errorf("call %q: %w", call.Item.CallID, err))
			}
		}
		var observed []mekugiHistory
		if found && len(record.History.NativePatches) != 0 {
			for index := range record.History.NativePatches {
				derived, exists, readErr := store.read(root, nativePatchDerivedCallID(call.Item.CallID, index), false)
				if readErr != nil {
					return fail(fmt.Errorf("call %q patch %d: %w", call.Item.CallID, index+1, readErr))
				}
				if exists {
					if derived.History.CorrelationID != call.Item.CallID+"\x00"+fmt.Sprint(index) ||
						derived.History.Script != record.History.NativePatches[index].Input {
						return fail(fmt.Errorf("call %q patch %d: inconsistent observation", call.Item.CallID, index+1))
					}
					observed = append(observed, derived.History)
				} else {
					observed = append(observed, mekugiHistory{})
				}
			}
		}
		var effects *mekugiHistory
		if found && record.History.ExecObservation != nil {
			derivedID := execDerivedCallID(call.Item.CallID, record.History.ExecObservation.CodeMode)
			derived, exists, readErr := store.read(root, derivedID, false)
			if readErr != nil {
				return fail(fmt.Errorf("call %q effects: %w", call.Item.CallID, readErr))
			}
			if exists {
				if derived.History.CorrelationID != call.Item.CallID+"\x00exec" ||
					derived.History.ToolName != nativeExecCommandToolName || derived.History.ExecOutcome == nil {
					return fail(fmt.Errorf("call %q: inconsistent effect observation", call.Item.CallID))
				}
				effects = &derived.History
			}
		}
		selectedField := ""
		if selected[call.Item.CallID] {
			selectedField = *field
		}
		projected, err := inspectSessionCall(call, record, found, observed, effects, selectedField, *textBytes)
		if err != nil {
			return fail(err)
		}
		projected.Workspace = root
		if root == "" {
			projected.Replay = "workspace_unavailable"
		}
		if withAX {
			if !found {
				result.AX.UnmatchedCalls++
			} else if len(observed) != 0 {
				editCalls[call.Item.CallID] = true
				for index, patch := range record.History.NativePatches {
					outcome := observed[index]
					edits.Observe(patch.Input, outcome.TranslationError != "", outcome.ToolName == "")
				}
			}
		}
		if selected[call.Item.CallID] {
			result.Calls = append(result.Calls, projected)
		}
	}
	if withAX {
		result.AX.Edits = edits.Metrics
		result.AX.Reads, err = agentMetrics.ReadReads(ctx, *readLog, observations.ThreadID)
		if err != nil {
			return fail(err)
		}
		result.AX.Defects, err = agentMetrics.ReadDefects(*defects, editCalls)
		if err != nil {
			return fail(err)
		}
	}
	if end < len(calls) {
		result.NextOffset = new(end)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return fail(err)
	}
	return 0
}

func inspectSessionCall(call rollout.Call, record replayRecord, found bool, observed []mekugiHistory, effects *mekugiHistory, field string, limit int) (inspectedCall, error) {
	result := inspectedCall{
		CallID: call.Item.CallID, Tool: qualifiedToolName(call.Item.Namespace, call.Item.Name), Replay: "missing",
		Outcome: "unavailable", Text: make(map[string]inspectedText),
	}
	values := map[string]string{"script": sessionInspectionPayload(call.Item)}
	if len(call.Outputs) != 0 {
		output := call.Outputs[len(call.Outputs)-1].Output
		var text string
		if json.Unmarshal(output, &text) == nil {
			values["output"] = text
		} else {
			values["output"] = string(output)
		}
	}
	if found {
		if record.History.ToolName == "" || record.History.CarrierName == "" ||
			(record.History.CarrierKind != "" && record.History.CarrierKind != codeModeCarrierCustom &&
				record.History.CarrierKind != codeModeCarrierFunction) {
			return result, fmt.Errorf("call %q: invalid replay history identity", call.Item.CallID)
		}
		history := record.History
		if call.HasCall {
			kind := history.effectiveCarrierKind()
			carrierMatches := call.Item.Type == carrierItemType(kind) &&
				call.Item.Name == history.CarrierName &&
				call.Item.Namespace == responses.StringField(history.UpstreamItem, "namespace") &&
				sessionInspectionPayload(call.Item) == history.carrierInput()
			originalMatches := call.Item.Type == responses.StringField(history.UpstreamItem, "type") &&
				call.Item.Name == responses.StringField(history.UpstreamItem, "name") &&
				call.Item.Namespace == responses.StringField(history.UpstreamItem, "namespace") &&
				sessionInspectionPayload(call.Item) == responses.StringField(history.UpstreamItem, carrierPayloadFieldForItem(call.Item.Type))
			if !carrierMatches && !originalMatches {
				return result, fmt.Errorf("call %q: replay payload does not match session", call.Item.CallID)
			}
		}
		result.Tool, result.Replay = history.ToolName, "matched"
		result.CorrelationID, result.Attempt = history.CorrelationID, history.Attempt
		values["script"] = history.Script
		values["report"], values["diagnostic"] = history.Report, history.TranslationError
		switch {
		case len(history.ReviewFiles) != 0:
			result.Outcome = "applied"
		case history.AlreadySatisfied:
			result.Outcome = "already_satisfied"
		case history.TranslationError != "":
			result.Outcome = "rejected"
		default:
			result.Outcome = "no_changes"
		}
		if len(observed) != 0 {
			applied, noOp, failed := false, true, false
			var reports, diagnostics []string
			for _, patch := range observed {
				applied = applied || len(patch.ReviewFiles) != 0
				noOp = noOp && patch.ToolName != "" && patch.AlreadySatisfied
				failed = failed || patch.TranslationError != ""
				if patch.Report != "" {
					reports = append(reports, patch.Report)
				}
				if patch.TranslationError != "" {
					diagnostics = append(diagnostics, patch.TranslationError)
				}
			}
			values["report"] = strings.Join(reports, "\n")
			values["diagnostic"] = strings.Join(diagnostics, "\n")
			switch {
			case applied:
				result.Outcome = "applied"
			case failed:
				result.Outcome = "rejected"
			case noOp:
				result.Outcome = "already_satisfied"
			default:
				result.Outcome = "no_changes"
			}
		}
	}
	if found && record.History.ExecObservation != nil {
		result.Effects = &inspectedEffects{
			CallID: execDerivedCallID(call.Item.CallID, record.History.ExecObservation.CodeMode),
			Replay: "missing", Status: "unavailable", Coverage: "unavailable",
		}
		if effects == nil {
			result.Outcome = "unavailable"
		} else {
			outcome := effects.ExecOutcome
			result.Effects.Replay = "matched"
			result.Effects.Status, result.Effects.Coverage = outcome.Status, outcome.Coverage
			if outcome.Status == "" {
				result.Effects.Status = "unconfirmed"
			}
			result.Effects.Exit, result.Effects.ChangeID = outcome.Exit, effects.ChangeID
			result.Effects.SharedWith = outcome.SharedWith
			if outcome.SharedWith == "" {
				result.Effects.ReviewFiles = new(len(effects.ReviewFiles))
			}
			for _, extra := range []struct{ name, text string }{
				{"report", effects.Report}, {"diagnostic", outcome.ScopeReason}, {"diagnostic", outcome.Unswept},
			} {
				if extra.text != "" {
					if values[extra.name] != "" {
						values[extra.name] += "\n"
					}
					values[extra.name] += extra.text
				}
			}
			switch {
			case outcome.Status == execStatusFailed:
				result.Outcome = "rejected"
			case outcome.Status != execStatusCompleted || outcome.SharedWith != "":
				result.Outcome = "unconfirmed"
			case len(effects.ReviewFiles) != 0:
				result.Outcome = "applied"
			case outcome.Coverage != execCoverageExact && result.Outcome == "no_changes":
				result.Outcome = "unconfirmed"
			}
		}
	}
	for name, value := range values {
		result.Text[name] = inspectText(value, field == "all" || field == name, limit)
	}
	return result, nil
}

func inspectText(value string, include bool, limit int) inspectedText {
	end := 0
	if include {
		end = min(len(value), limit)
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
	}
	return inspectedText{Text: value[:end], Bytes: len(value), OmittedBytes: len(value) - end}
}

func sessionInspectionPayload(item rollout.ToolItem) string {
	if item.Type == "function_call" {
		return item.Arguments
	}
	return item.Input
}

func carrierPayloadFieldForItem(itemType string) string {
	if itemType == "function_call" {
		return "arguments"
	}
	return "input"
}

// Historical workspace paths can outlive the directory itself. Resolve existing
// symlinks, but retain an absent absolute identity for archived replay lookup.
func canonicalInspectionWorkspace(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("recorded workspace is not absolute; use --workspace to override it")
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workspace is not a directory")
	}
	return resolved, nil
}
