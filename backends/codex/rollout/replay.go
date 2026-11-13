// Source: internal/router/session_ui_replay_read.go:47:397@[3e523196] retained UI item decoding.
package rollout

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/commandargs"
)

type replayRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Metadata  struct {
		Inherited         bool                                `json:"inherited_user_message"`
		Coordination      *observation.CoordinationProjection `json:"coordination"`
		SourceAcquisition *observation.SourceAcquisition      `json:"source_acquisition"`
	} `json:"metadata"`
	Payload struct {
		Type           string         `json:"type"`
		ID             string         `json:"id"`
		Cwd            string         `json:"cwd"`
		Thread         string         `json:"thread_id"`
		Turn           string         `json:"turn_id"`
		Started        int64          `json:"started_at_ms"`
		Completed      int64          `json:"completed_at_ms"`
		Item           jsontext.Value `json:"item"`
		NativeMetadata struct {
			Kinds []string `json:"content_item_kinds"`
		} `json:"internal_chat_message_metadata_passthrough"`
		History []struct {
			Type      string `json:"type"`
			Reference string `json:"encrypted_content"`
			Metadata  struct {
				SourceAcquisition *observation.SourceAcquisition `json:"source_acquisition"`
			} `json:"metadata"`
		} `json:"replacement_history"`
	} `json:"payload"`
}

// readReplayLines bounds all input, rejects devices/FIFOs, and reports malformed
// evidence rather than silently presenting a partial session as complete.
func readReplayLines(ctx context.Context, path string, visit func([]byte) error) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return errors.New("replay input must be a regular file at most 64 MiB")
	}
	return scanReplayLines(ctx, path, f, visit)
}

func scanReplayLines(ctx context.Context, path string, source io.Reader, visit func([]byte) error) error {
	bounded := &io.LimitedReader{R: source, N: (64 << 20) + 1}
	s := bufio.NewScanner(bounded)
	s.Buffer(make([]byte, 64<<10), 16<<20)
	for line := 1; s.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(s.Bytes()); err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	if bounded.N == 0 {
		return errors.New("replay input grew beyond 64 MiB")
	}
	return s.Err()
}

// Replay decodes one caller-selected rollout without starting Codex or reading
// retained feature records. Known threads identify inherited sibling evidence.
func Replay(ctx context.Context, path string, known map[string]bool) (observation.Source, error) {
	r := observation.Source{Unsupported: make(map[string]int)}
	var owner, pendingCompaction, activeTurn string
	ancestors := make(map[string]bool)
	turns := make(map[string]string)
	err := readReplayLines(ctx, path, func(line []byte) error {
		r.Bytes += len(line)
		var rec replayRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return err
		}
		p := rec.Payload
		boundary, reference := compactionBoundary(rec, r.Thread, &pendingCompaction)
		if rec.Type == "session_meta" {
			if r.Thread != "" {
				ancestors[p.ID] = true
				owner = p.ID
				return nil
			}
			if p.ID == "" {
				return errors.New("session metadata has no thread id")
			}
			r.Thread, r.Cwd, owner = p.ID, p.Cwd, p.ID
			return nil
		}
		if rec.Type == "compacted" {
			for _, item := range p.History {
				if item.Metadata.SourceAcquisition == nil {
					continue
				}
				if r.Thread == "" || rec.Timestamp.IsZero() {
					return errors.New("source acquisition requires session metadata and timestamp")
				}
				if owner == r.Thread || rec.Metadata.Inherited {
					r.Events = append(r.Events, observation.Event{At: rec.Timestamp, Kind: observation.SourceAcquisitionObserved, ThreadID: r.Thread, TurnID: activeTurn, SourceAcquisition: item.Metadata.SourceAcquisition, Inherited: rec.Metadata.Inherited})
				}
			}
		}
		if native, supported, err := replayNativeEvent(rec, line, r.Thread, activeTurn); err != nil {
			return err
		} else if supported {
			if r.Thread == "" {
				return errors.New("event precedes session metadata")
			}
			if rec.Timestamp.IsZero() {
				return errors.New("event has no timestamp")
			}
			if rec.Type == "response_item" && owner != r.Thread && !native.Inherited {
				return nil // Copied ancestor evidence is not new child traffic.
			}
			if native.ThreadID == "" {
				return errors.New("native event has no thread identity")
			}
			if native.ThreadID != r.Thread && !native.Inherited {
				if known[native.ThreadID] || ancestors[native.ThreadID] {
					return nil
				}
				return errors.New("native event thread does not match rollout metadata")
			}
			// Inherited content belongs to this source, but retains its frozen
			// sender/recipient and reset identity for semantic presentation.
			native.ThreadID = r.Thread
			r.Events = append(r.Events, native)
			return nil
		}
		if rec.Type != "event_msg" {
			return nil
		}
		if r.Thread == "" {
			return errors.New("event precedes session metadata")
		}
		if rec.Timestamp.IsZero() {
			return errors.New("event has no timestamp")
		}
		e := observation.Event{At: rec.Timestamp, ThreadID: r.Thread, TurnID: p.Turn}
		switch p.Type {
		case "thread_settings_applied":
			if p.Thread == r.Thread {
				owner = r.Thread
			}
			return nil
		case "task_started", "turn_started":
			if previous := turns[p.Turn]; previous != "" && previous != r.Thread {
				return nil
			}
			turns[p.Turn] = owner
			activeTurn = p.Turn
			e.Kind, e.TurnStatus = observation.TurnStarted, "inProgress"
		case "task_complete", "turn_complete":
			if previous := turns[p.Turn]; previous != "" && previous != r.Thread {
				return nil
			}
			e.Kind, e.TurnStatus = observation.TurnCompleted, "completed"
		case "turn_aborted":
			e.Kind, e.TurnStatus = observation.TurnCompleted, "interrupted"
		case "item_completed":
			if p.Thread != "" && p.Thread != r.Thread {
				if known[p.Thread] || ancestors[p.Thread] {
					turns[p.Turn] = p.Thread
					return nil
				}
				return errors.New("item thread does not match rollout metadata")
			}
			turns[p.Turn] = r.Thread
			item, supported, err := replayItem(p.Item)
			if err != nil {
				return err
			}
			if !supported {
				r.Unsupported[item.Type]++
				return nil
			}
			if item.ID == "" || p.Turn == "" || p.Started <= 0 || p.Completed < p.Started {
				return errors.New("item lacks valid identity or start/end timing")
			}
			if boundary == item.ID {
				item.CompactionReference = reference
			}
			item.DurationMS = new(p.Completed - p.Started)
			e.At, e.Started = time.UnixMilli(p.Completed), time.UnixMilli(p.Started)
			e.Kind, e.Item, e.ItemID = observation.ItemCompleted, item, item.ID
		default:
			return nil
		}
		r.Events = append(r.Events, e)
		return nil
	})
	if err != nil {
		return observation.Source{}, err
	}
	r.Events = slices.DeleteFunc(r.Events, func(e observation.Event) bool {
		return (e.Kind == observation.TurnStarted || e.Kind == observation.TurnCompleted) && turns[e.TurnID] != "" && turns[e.TurnID] != r.Thread
	})
	return r, nil
}

// Native history facts are observations, not reconstructed tool results. The
// message DTO remains uninterpreted here; its existing consumer validates
// attribution and provenance before publishing any traffic.
func replayNativeEvent(rec replayRecord, line []byte, thread, turn string) (observation.Event, bool, error) {
	e := observation.Event{At: rec.Timestamp, ThreadID: thread, TurnID: turn, Inherited: rec.Metadata.Inherited}
	switch rec.Type + "/" + rec.Payload.Type {
	case "event_msg/agent_coordination":
		var record struct {
			Payload observation.AgentCoordination `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return e, false, err
		}
		e.Kind, e.ThreadID, e.TurnID, e.Coordination = observation.CoordinationObserved, record.Payload.ThreadID, record.Payload.TurnID, &record.Payload.Projection
		return e, true, nil
	case "event_msg/context_reset":
		var record struct {
			Payload observation.ContextResetEvent `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return e, false, err
		}
		e.Kind, e.ThreadID, e.TurnID, e.Reset = observation.ContextResetObserved, record.Payload.Thread, record.Payload.Turn, &record.Payload
		return e, true, nil
	}
	if rec.Type != "response_item" {
		return e, false, nil
	}
	message := rec.Payload.Type == "agent_message" || rec.Payload.Type == "message" &&
		(slices.Contains(rec.Payload.NativeMetadata.Kinds, "multi_agent.inter_agent_message") || slices.Contains(rec.Payload.NativeMetadata.Kinds, "multi_agent.inter_agent_completion_message"))
	if !message && rec.Metadata.Coordination == nil && rec.Metadata.SourceAcquisition == nil {
		return e, false, nil
	}
	e.Kind, e.Coordination = observation.CoordinationObserved, rec.Metadata.Coordination
	e.SourceAcquisition = rec.Metadata.SourceAcquisition
	if e.SourceAcquisition != nil && !message && e.Coordination == nil {
		e.Kind = observation.SourceAcquisitionObserved
	}
	if message {
		var record struct {
			Payload observation.NativeMessage `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return e, false, err
		}
		e.NativeMessage = &record.Payload
	}
	return e, true, nil
}

func replayItem(raw jsontext.Value) (observation.Item, bool, error) {
	var p struct {
		SavedPath                                          string `json:"saved_path"`
		SavedArtifact                                      string `json:"savedPath"`
		Kind                                               string `json:"kind"`
		Source                                             string `json:"source"`
		Type, ID, Status, Text, Delivery, Phase, Cwd, Path string
		Content                                            jsontext.Value `json:"content"`
		Command                                            jsontext.Value `json:"command"`
		Summary                                            []string       `json:"summary_text"`
		Output                                             *string        `json:"aggregated_output"`
		Exit                                               *int           `json:"exit_code"`
		Process                                            string         `json:"process_id"`
		Client                                             string         `json:"client_id"`
		Tool                                               string         `json:"tool"`
		ReceiverThreadIDs                                  []string       `json:"receiver_thread_ids"`
		TimeoutMS                                          *int64         `json:"timeout_ms"`
		Server                                             string         `json:"server"`
		Name, Namespace                                    string
		ToolOutput                                         jsontext.Value `json:"output"`
		Arguments, Result, Error                           jsontext.Value
		Questions                                          []observation.Question `json:"questions"`
		Changes                                            map[string]struct {
			Type string `json:"type"`
			Diff string `json:"unified_diff"`
			Move string `json:"move_path"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(raw, &p, json.MatchCaseInsensitiveNames(true)); err != nil {
		return observation.Item{}, false, err
	}
	i := observation.Item{ID: p.ID, Type: p.Type, Status: p.Status, Cwd: p.Cwd, Text: p.Text, Summary: p.Summary, Content: p.Content, AggregatedOutput: p.Output, ExitCode: p.Exit, ProcessID: p.Process, ClientID: p.Client, Path: p.Path, Delivery: p.Delivery, Phase: p.Phase, Questions: p.Questions, Tool: p.Tool}
	if i.Type != "" {
		i.Type = strings.ToLower(i.Type[:1]) + i.Type[1:]
	}
	if i.Type == "extension" && p.Kind == "image_gen.generation" {
		i.Type, p.SavedPath = "imageGeneration", p.SavedArtifact
	}
	switch i.Type {
	case "collabAgentToolCall":
		i.ReceiverThreadIDs, i.TimeoutMS = p.ReceiverThreadIDs, p.TimeoutMS
	case "mcpToolCall":
		i.Server, i.Arguments, i.Result, i.Error = p.Server, p.Arguments, p.Result, p.Error
	case "functionCallOutput":
		// Historical standalone wakes are presentation evidence, not live work.
		if p.Namespace != "threadcontrol" || p.Name != "wait_agent" && p.Name != "compact" {
			return i, false, nil
		}
		i.Server, i.Tool, i.Result = p.Namespace, p.Name, p.ToolOutput
	case "imageGeneration":
		i.SavedPath = p.SavedPath
	case "userMessage", "reasoning", "contextCompaction", "imageView":
	case "agentMessage":
		var content []struct {
			Text string `json:"text"`
		}
		if len(p.Content) > 0 {
			if err := json.Unmarshal(p.Content, &content); err != nil {
				return i, false, err
			}
		}
		for _, block := range content {
			i.Text += block.Text
		}
	case "commandExecution":
		i.UserShell = p.Source == "user_shell"
		var args []string
		if err := json.Unmarshal(p.Command, &args); err != nil {
			return i, false, err
		}
		if len(args) >= 3 && (args[1] == "-c" || args[1] == "-lc") {
			i.Command = args[2]
		} else {
			for j := range args {
				args[j] = commandargs.QuoteArgument(args[j])
			}
			i.Command = strings.Join(args, " ")
		}
	case "fileChange":
		var paths []string
		for path := range p.Changes {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		for _, path := range paths {
			c := p.Changes[path]
			change := observation.FileChange{Path: path, Diff: c.Diff}
			change.Kind.Type, change.Kind.MovePath = c.Type, c.Move
			i.Changes = append(i.Changes, change)
		}
	default:
		return i, false, nil
	}
	if i.Type != "userMessage" {
		i.Content = nil
	}
	return i, true, nil
}
