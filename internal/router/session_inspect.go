package router

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/capturer"
)

const maxSessionInspectionBytes = 64 << 20

type inspectedText struct {
	Text         string `json:"text,omitempty"`
	Bytes        int    `json:"bytes"`
	OmittedBytes int    `json:"omitted_bytes"`
}

type inspectedCall struct {
	CallID        string                   `json:"call_id"`
	Workspace     string                   `json:"workspace,omitempty"`
	Tool          string                   `json:"tool"`
	Replay        string                   `json:"replay"`
	Outcome       string                   `json:"outcome"`
	CorrelationID string                   `json:"correlation_id,omitempty"`
	Attempt       int                      `json:"attempt,omitempty"`
	Rejections    int                      `json:"rejections"`
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
	ThreadID   string
	Completion capturer.AXCompletionAccumulator
	Commands   capturer.AXCommandAccumulator
}

type sessionAXReport struct {
	Commands       capturer.AXCommandMetrics    `json:"commands"`
	Scope          string                       `json:"scope"`
	ThreadID       string                       `json:"thread_id"`
	Edits          capturer.AXEditMetrics       `json:"edits"`
	Reads          capturer.AXReadMetrics       `json:"reads"`
	Completion     capturer.AXCompletionMetrics `json:"completion"`
	Defects        capturer.AXDefectMetrics     `json:"defect_assessments"`
	UnmatchedCalls uint64                       `json:"unmatched_calls"`
}

type sessionInspectionItem struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Input     string          `json:"input"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

type sessionInspectionCall struct {
	item      sessionInspectionItem
	workspace string
	hasCall   bool
	outputs   []sessionInspectionItem
}

// RunSessionInspection is an offline, read-only entry point. It deliberately does
// not open the writable replay store or install a router/runtime.
func RunSessionInspection(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("inspect-session", flag.ContinueOnError)
	flags.SetOutput(stderr)
	session := flags.String("session", "", "Codex rollout JSONL file (required)")
	workspace := flags.String("workspace", "", "override workspace inferred from rollout metadata")
	replayDir := flags.String("replay-dir", "", "replay directory (default platform state directory)")
	ax := flags.Bool("ax", false, "include whole-rollout AX measurements, independent of call pagination")
	readLog := flags.String("read-log", "", "runtime AX read journal; implies --ax")
	defects := flags.String("defects", "", "JSON defect assessments with evidence paths; implies --ax")
	callID := flags.String("call-id", "", "select one call identity")
	offset := flags.Int("offset", 0, "skip this many matching logical calls")
	limit := flags.Int("limit", 50, "maximum calls returned (1-500)")
	field := flags.String("field", "", "include text: script, evaluated, patch, report, diagnostic, rejections, output, or all")
	textBytes := flags.Int("text-bytes", 4096, "maximum UTF-8 bytes per included field (1-65536)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mekugi inspect-session --session PATH [options]")
		fmt.Fprintln(stderr, "Read local logical calls without running them. Text is omitted unless --field is selected.")
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
	fields := []string{"script", "evaluated", "patch", "report", "diagnostic", "rejections", "output"}
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
	store := &mekugiReplayStore{directory: *replayDir}
	var observations sessionAXInput
	calls, err := readSessionInspection(ctx, *session, &observations)
	if err != nil {
		return fail(err)
	}
	allCalls := calls
	if *callID != "" {
		calls = slices.DeleteFunc(slices.Clone(calls), func(call sessionInspectionCall) bool { return call.item.CallID != *callID })
	}
	result := sessionInspection{
		Schema: "mekugi.session.v1", WorkspaceOverride: override, TotalCalls: len(calls),
		Offset: *offset, Calls: []inspectedCall{},
	}
	start := min(*offset, len(calls))
	end := min(len(calls), start+*limit)
	withAX := *ax || *readLog != "" || *defects != ""
	inspected := calls[start:end]
	selected := make(map[string]bool, len(inspected))
	for _, call := range inspected {
		selected[call.item.CallID] = true
	}
	var edits capturer.AXEditAccumulator
	editCalls := make(map[string]bool)
	if withAX {
		inspected = allCalls
		result.AX = &sessionAXReport{Scope: "entire supplied rollout; edit metrics require matched replay",
			ThreadID: observations.ThreadID, Completion: observations.Completion.Result(), Commands: observations.Commands.Result()}
	}
	for _, call := range inspected {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		root := override
		if root == "" {
			root, err = canonicalInspectionWorkspace(call.workspace)
			if err != nil {
				return fail(fmt.Errorf("call %q: %w", call.item.CallID, err))
			}
		}
		var record replayRecord
		found := false
		if root != "" {
			record, found, err = store.read(root, call.item.CallID, false)
			if err != nil {
				return fail(fmt.Errorf("call %q: %w", call.item.CallID, err))
			}
		}
		selectedField := ""
		if selected[call.item.CallID] {
			selectedField = *field
		}
		projected, err := inspectSessionCall(call, record, found, selectedField, *textBytes)
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
			} else if projected.Tool == mekugiToolName || projected.Tool == mekugiRecoveryToolName {
				editCalls[call.item.CallID] = true
				edits.Observe(record.History.Script, record.History.Attempt > 1,
					projected.Outcome == "rejected",
					projected.Outcome == "unconfirmed" || projected.Outcome == "translated_unconfirmed")
			}
		}
		if selected[call.item.CallID] {
			result.Calls = append(result.Calls, projected)
		}
	}
	if withAX {
		result.AX.Edits = edits.Metrics
		result.AX.Reads, err = capturer.ReadAXReads(ctx, *readLog, observations.ThreadID)
		if err != nil {
			return fail(err)
		}
		result.AX.Defects, err = capturer.ReadAXDefects(*defects, editCalls)
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

func readSessionInspection(ctx context.Context, path string, observations *sessionAXInput) ([]sessionInspectionCall, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSessionInspectionBytes {
		return nil, errors.New("session must be a regular file no larger than 64 MiB")
	}
	// Like retained-shell reads, reject a FIFO substituted after the mode check
	// without blocking before descriptor validation.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSessionInspectionBytes {
		return nil, errors.New("session must be a regular file no larger than 64 MiB")
	}
	reader := &io.LimitedReader{R: file, N: maxSessionInspectionBytes + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxReplayRecordBytes)
	calls := []sessionInspectionCall{}
	indices := make(map[string]int)
	workspace := ""
	for line := 1; scanner.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var envelope struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if !utf8.Valid(scanner.Bytes()) || json.Unmarshal(scanner.Bytes(), &envelope) != nil {
			return nil, fmt.Errorf("session line %d: invalid JSON or UTF-8", line)
		}
		if envelope.Type == "session_meta" || envelope.Type == "turn_context" {
			var metadata struct {
				ID  string `json:"id"`
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(envelope.Payload, &metadata) != nil {
				return nil, fmt.Errorf("session line %d: invalid workspace metadata", line)
			}
			// id matches CODEX_THREAD_ID; session_id may instead name a fork's root thread.
			if envelope.Type == "session_meta" && observations != nil && metadata.ID != "" {
				if observations.ThreadID != "" && observations.ThreadID != metadata.ID {
					return nil, errors.New("session has conflicting thread identities")
				}
				observations.ThreadID = metadata.ID
			}
			if metadata.Cwd != "" {
				workspace = metadata.Cwd
			}
		}
		if envelope.Type == "event_msg" && observations != nil {
			var event struct {
				Type          string          `json:"type"`
				TurnID        string          `json:"turn_id"`
				StartedAtMS   json.RawMessage `json:"started_at_ms"`
				CompletedAtMS json.RawMessage `json:"completed_at_ms"`
				Item          *struct {
					Type     string          `json:"type"`
					ID       string          `json:"id"`
					ExitCode *int            `json:"exit_code"`
					Command  json.RawMessage `json:"command"`
				} `json:"item"`
			}
			if json.Unmarshal(envelope.Payload, &event) != nil {
				return nil, fmt.Errorf("session line %d: invalid event", line)
			}
			if (event.Type == "item_started" || event.Type == "item_completed") && event.Item != nil && event.Item.Type == "CommandExecution" {
				at, _ := time.Parse(time.RFC3339Nano, envelope.Timestamp)
				if event.Type == "item_started" && (event.StartedAtMS != nil || event.CompletedAtMS != nil) {
					at, err = inspectionCommandTime(event.StartedAtMS)
					if err != nil {
						return nil, fmt.Errorf("session line %d: invalid start timestamp: %w", line, err)
					}
				}
				callID := inspectionAXCallID(event.Item.Command)
				var err error
				if event.Type == "item_completed" && (event.StartedAtMS != nil || event.CompletedAtMS != nil) {
					// Persisted completion times describe execution, unlike the envelope's
					// recording time. A missing endpoint must not borrow that timestamp.
					start, startErr := inspectionCommandTime(event.StartedAtMS)
					end, endErr := inspectionCommandTime(event.CompletedAtMS)
					if startErr != nil || endErr != nil {
						return nil, fmt.Errorf("session line %d: invalid command timestamp: %w", line, errors.Join(startErr, endErr))
					}
					err = observations.Commands.ObserveCompleted(event.Item.ID, callID, start, end, event.Item.ExitCode)
				} else {
					err = observations.Commands.Observe(event.Type, event.Item.ID, callID, at, event.Item.ExitCode)
				}
				if err != nil {
					return nil, err
				}
			}
			switch event.Type {
			case "task_started", "turn_started", "task_complete", "turn_complete":
				at, _ := time.Parse(time.RFC3339Nano, envelope.Timestamp)
				observations.Completion.Observe(event.Type, event.TurnID, at)
			}
		}
		if envelope.Type != "response_item" {
			continue
		}
		var item sessionInspectionItem
		if json.Unmarshal(envelope.Payload, &item) != nil {
			return nil, fmt.Errorf("session line %d: invalid response item", line)
		}
		isCall := item.Type == "function_call" || item.Type == "custom_tool_call"
		isOutput := item.Type == "function_call_output" || item.Type == "custom_tool_call_output"
		if !isCall && !isOutput {
			continue
		}
		if item.CallID == "" {
			return nil, fmt.Errorf("session line %d: tool item has no call_id", line)
		}
		index, exists := indices[item.CallID]
		if !exists {
			if len(calls) == 10000 {
				return nil, errors.New("session exceeds 10000 logical calls")
			}
			index = len(calls)
			indices[item.CallID] = index
			calls = append(calls, sessionInspectionCall{item: item, workspace: workspace})
		}
		call := &calls[index]
		if isCall {
			if call.hasCall && (call.item.Type != item.Type || call.item.Name != item.Name ||
				call.item.Namespace != item.Namespace ||
				call.item.Input != item.Input || call.item.Arguments != item.Arguments) {
				return nil, fmt.Errorf("session line %d: conflicting call identity", line)
			}
			if !call.hasCall {
				call.workspace = workspace
			} else if call.workspace != workspace {
				return nil, fmt.Errorf("session line %d: conflicting call workspace", line)
			}
			call.item, call.hasCall = item, true
		} else {
			call.outputs = append(call.outputs, item)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read session (maximum line size %d bytes): %w", maxReplayRecordBytes, err)
	}
	if reader.N == 0 {
		return nil, errors.New("session exceeds 64 MiB")
	}
	return calls, nil
}

func inspectSessionCall(call sessionInspectionCall, record replayRecord, found bool, field string, limit int) (inspectedCall, error) {
	result := inspectedCall{
		CallID: call.item.CallID, Tool: qualifiedToolName(call.item.Namespace, call.item.Name), Replay: "missing",
		Outcome: "unavailable", Text: make(map[string]inspectedText),
	}
	values := map[string]string{"script": sessionInspectionPayload(call.item)}
	if len(call.outputs) != 0 {
		output := call.outputs[len(call.outputs)-1].Output
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
			return result, fmt.Errorf("call %q: invalid replay history identity", call.item.CallID)
		}
		history := record.History.history()
		if call.hasCall {
			kind := history.effectiveCarrierKind()
			carrierMatches := call.item.Type == carrierItemType(kind) &&
				call.item.Name == history.carrierName &&
				call.item.Namespace == jsonString(history.upstreamItem, "namespace") &&
				sessionInspectionPayload(call.item) == history.carrierInput()
			originalMatches := call.item.Type == jsonString(history.upstreamItem, "type") &&
				call.item.Name == jsonString(history.upstreamItem, "name") &&
				call.item.Namespace == jsonString(history.upstreamItem, "namespace") &&
				sessionInspectionPayload(call.item) == jsonString(history.upstreamItem, carrierPayloadFieldForItem(call.item.Type))
			if !carrierMatches && !originalMatches {
				return result, fmt.Errorf("call %q: replay payload does not match session", call.item.CallID)
			}
		}
		result.Tool, result.Replay = history.toolName, "matched"
		result.CorrelationID, result.Attempt = history.correlationID, history.attempt
		result.Rejections = len(history.rejections)
		values["script"], values["evaluated"] = history.script, history.evaluated
		if values["evaluated"] == "" && !history.unevaluated {
			values["evaluated"] = history.script
		}
		values["patch"], values["report"], values["diagnostic"] = history.patch, history.report, history.translationError
		rejections, _ := json.Marshal(history.rejections)
		values["rejections"] = string(rejections)
		switch {
		case history.translationError != "":
			result.Outcome = "rejected"
		case history.applied:
			result.Outcome = "applied"
		case history.alreadySatisfied:
			result.Outcome = "already_satisfied"
		case history.patch != "":
			result.Outcome = "translated_unconfirmed"
		default:
			result.Outcome = "unconfirmed"
		}
		// Use the same exact report confirmation as request-visible replay.
		// A translated patch alone is not evidence that the host applied it.
		if history.translationError == "" && !history.applied && !history.alreadySatisfied && history.report != "" {
			for _, output := range call.outputs {
				if output.Type == carrierOutputItemType(history.effectiveCarrierKind()) && history.confirmsReport(output.Output) {
					result.Outcome = "confirmed"
					break
				}
			}
		}
	}
	for name, value := range values {
		shown := ""
		if field == "all" || field == name {
			end := min(len(value), limit)
			for end > 0 && !utf8.ValidString(value[:end]) {
				end--
			}
			shown = value[:end]
		}
		result.Text[name] = inspectedText{Text: shown, Bytes: len(value), OmittedBytes: len(value) - len(shown)}
	}
	return result, nil
}

func sessionInspectionPayload(item sessionInspectionItem) string {
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

// An absent or explicitly null execution endpoint is unavailable, not record time.
func inspectionCommandTime(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 {
		return time.Time{}, nil
	}
	var ms *int64
	if err := json.Unmarshal(raw, &ms); err != nil {
		return time.Time{}, err
	}
	if ms == nil {
		return time.Time{}, nil
	}
	return time.UnixMilli(*ms), nil
}
