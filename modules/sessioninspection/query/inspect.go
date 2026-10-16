// Source: internal/router/session_inspect.go@[d9f28a74] logical query and projection.
package query

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/libraries/legacycall"
)

type Text struct {
	Text         string `json:"text,omitempty"`
	Bytes        int    `json:"bytes"`
	OmittedBytes int    `json:"omitted_bytes"`
}

type Effects struct {
	CallID      string `json:"call_id"`
	Replay      string `json:"replay"`
	Status      string `json:"status"`
	Coverage    string `json:"coverage"`
	Exit        *int   `json:"exit,omitempty"`
	ReviewFiles *int   `json:"review_files,omitempty"`
	ChangeID    string `json:"change_id,omitempty"`
	SharedWith  string `json:"shared_with,omitempty"`
}

type CallResult struct {
	Effects       *Effects        `json:"effects,omitempty"`
	CallID        string          `json:"call_id"`
	Workspace     string          `json:"workspace,omitempty"`
	Tool          string          `json:"tool"`
	Replay        string          `json:"replay"`
	Outcome       string          `json:"outcome"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	Attempt       int             `json:"attempt,omitempty"`
	Text          map[string]Text `json:"text"`
}

type Result struct {
	ThreadID          string         `json:"-"`
	AX                jsontext.Value `json:"ax,omitempty"`
	Schema            string         `json:"schema"`
	WorkspaceOverride string         `json:"workspace_override,omitempty"`
	TotalCalls        int            `json:"total_calls"`
	Offset            int            `json:"offset"`
	NextOffset        *int           `json:"next_offset,omitempty"`
	Calls             []CallResult   `json:"calls"`
}

type SingleOptions struct {
	Session, ReplayDirectory, Workspace, CallID, Field string
	Offset, Limit, TextBytes                           int
	AX                                                 bool
	ReadLog, Defects                                   string
	// Collector lets debug export reuse the metrics owner's validated read snapshot.
	Collector inspection.AXCollector
}

// Inspect joins one decoded source with retained evidence without opening a writer.
func (s *Service) Inspect(ctx context.Context, options SingleOptions) (Result, error) {
	if s == nil || s.source == nil {
		return Result{}, errors.New("inspection source is unavailable")
	}
	if options.Session == "" || options.Offset < 0 || options.Limit < 1 || options.Limit > 500 || options.TextBytes < 1 || options.TextBytes > 65536 ||
		!slices.Contains([]string{"", "all", "script", "report", "diagnostic", "output"}, options.Field) {
		return Result{}, errors.New("invalid inspection options")
	}
	withAX := options.AX || options.ReadLog != "" || options.Defects != ""
	if withAX && !s.HasAX() {
		return Result{}, errors.New("agent metrics are unavailable in this composition")
	}
	var err error
	override := ""
	if options.Workspace != "" {
		override, err = filepath.Abs(options.Workspace)
		if err == nil {
			override, err = Workspace(override)
		}
		if err != nil {
			return Result{}, err
		}
	}
	var collector inspection.AXCollector
	var observer func(inspection.Event) error
	if s.HasAX() {
		collector = options.Collector
		if collector == nil {
			collector = s.contributions.NewAX()
		}
		observer = collector.Observe
	}
	decoded, err := s.source(ctx, options.Session, observer)
	if err != nil {
		return Result{}, err
	}
	calls := decoded.Calls
	allCalls := calls
	if options.CallID != "" {
		calls = slices.DeleteFunc(slices.Clone(calls), func(call Call) bool { return call.ID != options.CallID })
	}
	result := Result{
		Schema: "mekugi.session.v1", ThreadID: decoded.ThreadID, WorkspaceOverride: override, TotalCalls: len(calls),
		Offset: options.Offset, Calls: []CallResult{},
	}
	start := min(options.Offset, len(calls))
	end := min(len(calls), start+options.Limit)
	inspected := calls[start:end]
	selected := make(map[string]bool, len(inspected))
	for _, call := range inspected {
		selected[call.ID] = true
	}
	var unmatched uint64
	if withAX {
		inspected = allCalls
	}
	for _, call := range inspected {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		root := override
		if root == "" {
			root, err = Workspace(call.Workspace)
			if err != nil {
				return Result{}, fmt.Errorf("call %q: %w", call.ID, err)
			}
		}
		var record legacycall.Record
		found := false
		if root != "" {
			record, found, err = legacycall.Read(options.ReplayDirectory, root, call.ID, false, 32<<20)
			if err != nil {
				return Result{}, fmt.Errorf("call %q: %w", call.ID, err)
			}
		}
		var observed []legacycall.History
		if found && len(record.History.NativePatches) != 0 {
			for index := range record.History.NativePatches {
				derived, exists, readErr := legacycall.Read(options.ReplayDirectory, root, legacycall.PatchCallID(call.ID, index), false, 32<<20)
				if readErr != nil {
					return Result{}, fmt.Errorf("call %q patch %d: %w", call.ID, index+1, readErr)
				}
				if exists {
					if derived.History.CorrelationID != call.ID+"\x00"+fmt.Sprint(index) ||
						derived.History.Script != record.History.NativePatches[index].Input {
						return Result{}, fmt.Errorf("call %q patch %d: inconsistent observation", call.ID, index+1)
					}
					observed = append(observed, derived.History)
				} else {
					observed = append(observed, legacycall.History{})
				}
			}
		}
		var effects *legacycall.History
		if found && record.History.ExecObservation != nil {
			derivedID := legacycall.ExecCallID(call.ID, record.History.ExecObservation.CodeMode)
			derived, exists, readErr := legacycall.Read(options.ReplayDirectory, root, derivedID, false, 32<<20)
			if readErr != nil {
				return Result{}, fmt.Errorf("call %q effects: %w", call.ID, readErr)
			}
			if exists {
				if derived.History.CorrelationID != call.ID+"\x00exec" ||
					derived.History.ToolName != "exec_command" || derived.History.ExecOutcome == nil {
					return Result{}, fmt.Errorf("call %q: inconsistent effect observation", call.ID)
				}
				effects = &derived.History
			}
		}
		selectedField := ""
		if selected[call.ID] {
			selectedField = options.Field
		}
		projected, err := inspectSessionCall(call, record, found, observed, effects, selectedField, options.TextBytes)
		if err != nil {
			return Result{}, err
		}
		projected.Workspace = root
		if root == "" {
			projected.Replay = "workspace_unavailable"
		}
		if withAX {
			if !found {
				unmatched++
			} else {
				for index, patch := range record.History.NativePatches {
					outcome := observed[index]
					collector.ObserveEdit(inspection.Edit{CallID: call.ID, Script: patch.Input, Failed: outcome.TranslationError != "", Unconfirmed: outcome.ToolName == ""})
				}
			}
		}
		if selected[call.ID] {
			result.Calls = append(result.Calls, projected)
		}
	}
	if withAX {
		result.AX, err = collector.Report(ctx, inspection.AXOptions{ThreadID: decoded.ThreadID, ReadLog: options.ReadLog, Defects: options.Defects, UnmatchedCalls: unmatched})
		if err != nil {
			return Result{}, err
		}
	}
	if end < len(calls) {
		result.NextOffset = new(end)
	}
	return result, nil
}

func inspectSessionCall(call Call, record legacycall.Record, found bool, observed []legacycall.History, effects *legacycall.History, field string, limit int) (CallResult, error) {
	result := CallResult{
		CallID: call.ID, Tool: call.Tool, Replay: "missing",
		Outcome: "unavailable", Text: make(map[string]Text),
	}
	values := map[string]string{"script": call.Input}
	if call.HasOutput {
		values["output"] = call.Output
	}

	if found {
		history := record.History
		if err := call.Evidence.Verify(history); err != nil {
			return result, err
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
		result.Effects = &Effects{
			CallID: legacycall.ExecCallID(call.ID, record.History.ExecObservation.CodeMode),
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
			case outcome.Status == "failed":
				result.Outcome = "rejected"
			case outcome.Status != "completed" || outcome.SharedWith != "":
				result.Outcome = "unconfirmed"
			case len(effects.ReviewFiles) != 0:
				result.Outcome = "applied"
			case outcome.Coverage != "exact" && result.Outcome == "no_changes":
				result.Outcome = "unconfirmed"
			}
		}
	}
	for name, value := range values {
		result.Text[name] = TextValue(value, field == "all" || field == name, limit)
	}
	return result, nil
}

func TextValue(value string, include bool, limit int) Text {
	end := 0
	if include {
		end = min(len(value), limit)
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
	}
	return Text{Text: value[:end], Bytes: len(value), OmittedBytes: len(value) - end}
}

// Historical workspace paths can outlive the directory itself. Resolve existing
// symlinks, but retain an absent absolute identity for archived replay lookup.
func Workspace(path string) (string, error) {
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
