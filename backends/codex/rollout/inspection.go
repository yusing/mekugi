package rollout

import (
	"bufio"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// Source: internal/router/session_inspect.go:336:505@[fcd0381b] readSessionInspection.
// Source: internal/router/session_inspect_commands.go:10:38@[fcd0381b] command correlation.

const (
	maxInspectionBytes     = 64 << 20
	maxInspectionLineBytes = 32 << 20
)

// InspectionMetadata identifies the supplied rollout, without granting authority
// to access its workspace. Models retain their first-observed order.
type InspectionMetadata struct {
	Source   jsonv1.RawMessage
	Cwd      string
	Models   []string
	ThreadID string
}

// Inspection is a complete, validated logical call sequence.
type Inspection struct {
	InspectionMetadata
	Calls []Call
}

// ToolItem preserves the original Codex tool fields, including inactive payload
// fields that participate in duplicate-identity validation.
type ToolItem struct {
	Type      string            `json:"type"`
	CallID    string            `json:"call_id"`
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Input     string            `json:"input"`
	Arguments string            `json:"arguments"`
	Output    jsonv1.RawMessage `json:"output"`
}

// Call groups one identity's call and outputs in first-observed order. Output-only
// identities retain their first output's metadata until a call is observed.
type Call struct {
	Line               int
	Timestamp          string
	CompletedTimestamp string
	Model              string
	Item               ToolItem
	Workspace          string
	HasCall            bool
	Outputs            []ToolItem
}

// InspectionEvent contains recorded host facts. The consumer validates metric
// identities and pairs events; persisted endpoints may explicitly be unknown.
type InspectionEvent struct {
	Kind, TurnID, ItemID, CallID string
	At, Started, Completed       time.Time
	PersistedTimes               bool
	ExitCode                     *int
}

// Inspect reads only the selected source. A nil observer skips event-specific
// decoding. Observer errors abort the read; emitted events alone do not establish
// a complete source. Historical rollouts retain JSON v1 decoding semantics.
func Inspect(ctx context.Context, path string, observe func(InspectionEvent) error) (Inspection, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Inspection{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxInspectionBytes {
		return Inspection{}, errors.New("session must be a regular file no larger than 64 MiB")
	}
	// Reject a FIFO substituted after the mode check
	// without blocking before descriptor validation.
	file, err := sourceparse.Open(path)
	if err != nil {
		return Inspection{}, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return Inspection{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxInspectionBytes {
		return Inspection{}, errors.New("session must be a regular file no larger than 64 MiB")
	}
	reader := &io.LimitedReader{R: file, N: maxInspectionBytes + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxInspectionLineBytes)
	var result Inspection
	calls := []Call{}
	indices := make(map[string]int)
	workspace := ""
	model := ""
	for line := 1; scanner.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return Inspection{}, err
		}
		var envelope struct {
			Timestamp string            `json:"timestamp"`
			Type      string            `json:"type"`
			Payload   jsonv1.RawMessage `json:"payload"`
		}
		if !utf8.Valid(scanner.Bytes()) || json.Unmarshal(scanner.Bytes(), &envelope, jsonv1.DefaultOptionsV1()) != nil {
			return Inspection{}, fmt.Errorf("session line %d: invalid JSON or UTF-8", line)
		}
		if envelope.Type == "session_meta" || envelope.Type == "turn_context" {
			var metadata struct {
				ID     string            `json:"id"`
				Cwd    string            `json:"cwd"`
				Model  string            `json:"model"`
				Source jsonv1.RawMessage `json:"source"`
			}
			if json.Unmarshal(envelope.Payload, &metadata, jsonv1.DefaultOptionsV1()) != nil {
				return Inspection{}, fmt.Errorf("session line %d: invalid workspace metadata", line)
			}
			if metadata.Model != "" {
				model = metadata.Model
				if !slices.Contains(result.Models, model) {
					result.Models = append(result.Models, model)
				}
			}
			if envelope.Type == "session_meta" {
				result.Source = metadata.Source
				result.Cwd = metadata.Cwd
			}
			// id matches CODEX_THREAD_ID; session_id may instead name a fork's root thread.
			if envelope.Type == "session_meta" && metadata.ID != "" {
				if result.ThreadID != "" && result.ThreadID != metadata.ID {
					return Inspection{}, errors.New("session has conflicting thread identities")
				}
				result.ThreadID = metadata.ID
			}
			if metadata.Cwd != "" {
				workspace = metadata.Cwd
			}
		}
		if observe != nil && envelope.Type == "event_msg" {
			var event struct {
				Type          string            `json:"type"`
				TurnID        string            `json:"turn_id"`
				StartedAtMS   jsonv1.RawMessage `json:"started_at_ms"`
				CompletedAtMS jsonv1.RawMessage `json:"completed_at_ms"`
				Item          *struct {
					Type     string            `json:"type"`
					ID       string            `json:"id"`
					ExitCode *int              `json:"exit_code"`
					Command  jsonv1.RawMessage `json:"command"`
				} `json:"item"`
			}
			if json.Unmarshal(envelope.Payload, &event, jsonv1.DefaultOptionsV1()) != nil {
				return Inspection{}, fmt.Errorf("session line %d: invalid event", line)
			}
			decoded := InspectionEvent{Kind: event.Type, TurnID: event.TurnID}
			decoded.At, _ = time.Parse(time.RFC3339Nano, envelope.Timestamp)
			if (event.Type == "item_started" || event.Type == "item_completed") && event.Item != nil && event.Item.Type == "CommandExecution" {
				decoded.ItemID, decoded.CallID, decoded.ExitCode = event.Item.ID, commandCallID(event.Item.Command), event.Item.ExitCode
				decoded.PersistedTimes = event.StartedAtMS != nil || event.CompletedAtMS != nil
				if event.Type == "item_started" && decoded.PersistedTimes {
					decoded.At, err = commandTime(event.StartedAtMS)
					if err != nil {
						return Inspection{}, fmt.Errorf("session line %d: invalid start timestamp: %w", line, err)
					}
				}
				if event.Type == "item_completed" && decoded.PersistedTimes {
					// Execution endpoints, including unknown ones, must not borrow record time.
					var startErr, endErr error
					decoded.Started, startErr = commandTime(event.StartedAtMS)
					decoded.Completed, endErr = commandTime(event.CompletedAtMS)
					if startErr != nil || endErr != nil {
						return Inspection{}, fmt.Errorf("session line %d: invalid command timestamp: %w", line, errors.Join(startErr, endErr))
					}
				}
				if err := observe(decoded); err != nil {
					return Inspection{}, err
				}
			}
			switch event.Type {
			case "task_started", "turn_started", "task_complete", "turn_complete":
				if err := observe(decoded); err != nil {
					return Inspection{}, err
				}
			}
		}
		if envelope.Type != "response_item" {
			continue
		}
		var item ToolItem
		if json.Unmarshal(envelope.Payload, &item, jsonv1.DefaultOptionsV1()) != nil {
			return Inspection{}, fmt.Errorf("session line %d: invalid response item", line)
		}
		isCall := item.Type == "function_call" || item.Type == "custom_tool_call"
		isOutput := item.Type == "function_call_output" || item.Type == "custom_tool_call_output"
		if !isCall && !isOutput {
			continue
		}
		if item.CallID == "" {
			var raw map[string]jsontext.Value
			if json.Unmarshal(envelope.Payload, &raw) == nil && codexresponses.StandaloneFunctionOutput(raw) {
				continue
			}
			return Inspection{}, fmt.Errorf("session line %d: tool item has no call_id", line)
		}
		index, exists := indices[item.CallID]
		if !exists {
			if len(calls) == 10000 {
				return Inspection{}, errors.New("session exceeds 10000 logical calls")
			}
			index = len(calls)
			indices[item.CallID] = index
			calls = append(calls, Call{Item: item, Workspace: workspace, Line: line, Timestamp: envelope.Timestamp, Model: model})
		}
		call := &calls[index]
		if isCall {
			if call.HasCall && (call.Item.Type != item.Type || call.Item.Name != item.Name ||
				call.Item.Namespace != item.Namespace ||
				call.Item.Input != item.Input || call.Item.Arguments != item.Arguments) {
				return Inspection{}, fmt.Errorf("session line %d: conflicting call identity", line)
			}
			if !call.HasCall {
				call.Workspace = workspace
				call.Line, call.Timestamp, call.Model = line, envelope.Timestamp, model
			} else if call.Workspace != workspace {
				return Inspection{}, fmt.Errorf("session line %d: conflicting call workspace", line)
			}
			call.Item, call.HasCall = item, true
		} else {
			call.Outputs = append(call.Outputs, item)
			call.CompletedTimestamp = envelope.Timestamp
		}
	}
	if err := scanner.Err(); err != nil {
		return Inspection{}, fmt.Errorf("cannot read session (maximum line size %d bytes): %w", maxInspectionLineBytes, err)
	}
	if reader.N == 0 {
		return Inspection{}, errors.New("session exceeds 64 MiB")
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	result.Calls = calls
	return result, nil
}

// An absent or explicitly null execution endpoint is unavailable, not record time.
func commandTime(raw jsonv1.RawMessage) (time.Time, error) {
	if len(raw) == 0 {
		return time.Time{}, nil
	}
	var ms *int64
	if err := json.Unmarshal(raw, &ms, jsonv1.DefaultOptionsV1()); err != nil {
		return time.Time{}, err
	}
	if ms == nil {
		return time.Time{}, nil
	}
	return time.UnixMilli(*ms), nil
}

// Read explicit router-owned invocation metadata, not arbitrary environment
// assignments. Historical outer markers remain readable for retained rollouts.
// This is local correlation evidence, not authentication of a supplied rollout.
func commandCallID(encoded jsonv1.RawMessage) string {
	var command string
	if json.Unmarshal(encoded, &command, jsonv1.DefaultOptionsV1()) != nil {
		var argv []string
		if json.Unmarshal(encoded, &argv, jsonv1.DefaultOptionsV1()) != nil || len(argv) != 3 ||
			(shellsyntax.InterpreterIdentity(argv[0]) != "bash" && shellsyntax.InterpreterIdentity(argv[0]) != "sh") ||
			(argv[1] != "-c" && argv[1] != "-lc") {
			return ""
		}
		command = argv[2]
	}
	metadata, _, newline := strings.Cut(command, "\n")
	identity, marked := strings.CutPrefix(metadata, "# mekugi:ax:call_id=")
	if marked && newline {
		return identity
	}
	return ""
}
