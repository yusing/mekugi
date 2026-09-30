package router

import (
	"bufio"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Replay uses retained semantic items, never shell execution or provider calls.
// Missing token/chunk arrival times are simulated between the recorded bounds.
type sessionUIReplay struct {
	Thread, Cwd       string
	Start, End        time.Time
	Events            []uiReplayEvent
	Threads           map[string]string
	Missing           []string
	Unsupported       map[string]int
	Items, Providers  int
	JournalUnverified int
}

type uiReplayEvent struct {
	At     time.Time
	Method string
	Params appServerEvent
}

type uiReplayRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type      string         `json:"type"`
		ID        string         `json:"id"`
		Cwd       string         `json:"cwd"`
		Thread    string         `json:"thread_id"`
		Turn      string         `json:"turn_id"`
		Started   int64          `json:"started_at_ms"`
		Completed int64          `json:"completed_at_ms"`
		Item      jsontext.Value `json:"item"`
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
	bounded := &io.LimitedReader{R: f, N: (64 << 20) + 1}
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

func readSessionUIReplay(ctx context.Context, path, debugDir string, seed uint64) (*sessionUIReplay, error) {
	r := &sessionUIReplay{Threads: make(map[string]string), Unsupported: make(map[string]int)}
	// Read only published records. Opening the writable store would create it
	// and acquire process resources that offline presentation must not own.
	directory, _ := defaultMekugiReplayDirectory()
	store := &mekugiReplayStore{directory: directory}
	rng := rand.New(rand.NewPCG(seed, seed^0xa0761d6478bd642f))
	queue := []string{path}
	loaded := make(map[string]bool)
	totalBytes := 0
	turns := make(map[string]string)
	for len(queue) > 0 {
		path, queue = queue[0], queue[1:]
		var thread, scope, workspace string
		ancestors := make(map[string]bool)
		firstEvent := len(r.Events)
		var children []string
		err := readReplayLines(ctx, path, func(line []byte) error {
			totalBytes += len(line)
			if totalBytes > 256<<20 {
				return errors.New("replay corpus exceeds 256 MiB")
			}
			var rec uiReplayRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				return err
			}
			p := rec.Payload
			if rec.Type == "session_meta" {
				if thread != "" {
					ancestors[p.ID] = true
					scope = p.ID
					return nil
				} // Fork rollouts retain ancestor metadata as history.
				thread, scope = p.ID, p.ID
				workspace = p.Cwd
				if thread == "" {
					return errors.New("session metadata has no thread id")
				}
				if loaded[thread] {
					return errors.New("duplicate thread rollout")
				}
				loaded[thread] = true
				if r.Thread == "" {
					r.Thread, r.Cwd = thread, p.Cwd
					r.Threads[thread] = "/root"
				}
				return nil
			}
			if rec.Type != "event_msg" {
				return nil
			}
			if thread == "" {
				return errors.New("event precedes session metadata")
			}
			if rec.Timestamp.IsZero() {
				return errors.New("event has no timestamp")
			}
			e := uiReplayEvent{At: rec.Timestamp, Params: appServerEvent{ThreadID: thread, TurnID: p.Turn}}
			switch p.Type {
			case "thread_settings_applied":
				// Codex appends this thread-scoped marker after a fork's copied
				// prefix. It establishes local ownership even for itemless turns.
				if p.Thread == thread {
					scope = thread
				}
				return nil
			case "task_started", "turn_started":
				if owner := turns[p.Turn]; owner != "" && owner != thread {
					return nil
				}
				turns[p.Turn] = scope
				e.Method, e.Params.Turn = "turn/started", appServerTurn{ID: p.Turn, Status: "inProgress"}
			case "task_complete", "turn_complete":
				if owner := turns[p.Turn]; owner != "" && owner != thread {
					return nil
				}
				e.Method, e.Params.Turn = "turn/completed", appServerTurn{ID: p.Turn, Status: "completed"}
			case "turn_aborted":
				e.Method, e.Params.Turn = "turn/completed", appServerTurn{ID: p.Turn, Status: "interrupted"}
			case "item_completed":
				if p.Thread != "" && p.Thread != thread {
					if loaded[p.Thread] || ancestors[p.Thread] {
						turns[p.Turn] = p.Thread
						return nil
					} // Inherited evidence belongs to its original thread.
					return errors.New("item thread does not match rollout metadata")
				}
				turns[p.Turn] = thread
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
				if hidden, candidate := replayJournalTransport(store, workspace, thread, item); hidden {
					// Hiding presentation must not shorten the recorded interval,
					// including rollouts without explicit turn lifecycle records.
					for _, at := range []int64{p.Started, p.Completed} {
						r.Events = append(r.Events, uiReplayEvent{At: time.UnixMilli(at), Method: "replay/transportBoundary", Params: e.Params})
					}
					if len(r.Events) > 500000 {
						return errors.New("replay exceeds 500000 events")
					}
					return nil
				} else if candidate {
					r.JournalUnverified++
				}
				r.Items++
				item.DurationMS = new(p.Completed - p.Started)
				start, end := time.UnixMilli(p.Started), time.UnixMilli(p.Completed)
				e.At, e.Method, e.Params.Item, e.Params.ItemID = end, "item/completed", item, item.ID
				if item.AgentThreadID != "" && r.Threads[item.AgentThreadID] == "" {
					r.Threads[item.AgentThreadID] = cmp.Or(item.AgentPath, appServerPlaceholder(item.AgentThreadID))
					children = append(children, item.AgentThreadID)
				}
				if end.After(start) {
					begin := e
					begin.At, begin.Method = start, "item/started"
					begin.Params.Item.Text, begin.Params.Item.Summary, begin.Params.Item.AggregatedOutput = "", nil, nil
					begin.Params.Item.Status, begin.Params.Item.ExitCode, begin.Params.Item.DurationMS = "inProgress", nil, nil
					r.Events = append(r.Events, begin)
					r.Events = append(r.Events, replayDeltas(rng, e, start, end)...)
				}
			default:
				return nil
			}
			r.Events = append(r.Events, e)
			if len(r.Events) > 500000 {
				return errors.New("replay exceeds 500000 events")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		// Item thread IDs resolve the ownership of lifecycle records that have
		// no thread field, including inherited history in a standalone fork.
		r.Events = append(r.Events[:firstEvent], slices.DeleteFunc(r.Events[firstEvent:], func(e uiReplayEvent) bool {
			return strings.HasPrefix(e.Method, "turn/") && turns[e.Params.Turn.ID] != "" && turns[e.Params.Turn.ID] != thread
		})...)
		for _, child := range children {
			if loaded[child] {
				continue
			}
			// IDs are literal names, not paths or glob expressions from content.
			if strings.ContainsAny(child, `/\*?[]`) {
				return nil, errors.New("invalid child thread id")
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "rollout-*-"+child+".jsonl"))
			if err != nil {
				return nil, err
			}
			if len(matches) != 1 {
				r.Missing = append(r.Missing, child)
				continue
			}
			queue = append(queue, matches[0])
		}
		if len(loaded)+len(queue) > 128 {
			return nil, errors.New("replay exceeds 128 threads")
		}
	}
	if len(r.Events) == 0 {
		return nil, errors.New("session has no timestamped UI events")
	}
	if debugDir != "" {
		if err := r.readProviders(ctx, filepath.Join(debugDir, "capture.jsonl"), loaded); err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(r.Events, func(a, b uiReplayEvent) int { return a.At.Compare(b.At) })
	r.Start, r.End = r.Events[0].At, r.Events[len(r.Events)-1].At
	return r, nil
}

// The generated command grammar alone is not provenance. Reuse the durable
// carrier's classifier and executing-thread scope, without a live proxy or
// authorization token. Semantic journal messages remain normal replay items.
func replayJournalTransport(store *mekugiReplayStore, workspace, thread string, item appServerItem) (hidden, candidate bool) {
	if item.Type != "commandExecution" {
		return false, false
	}
	parts := nativeJournalCommand.FindStringSubmatch(appServerDisplayCommand(item.Command))
	if parts == nil {
		return false, false
	}
	encoded, _, _ := strings.Cut(parts[2], ".")
	callID, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || store.directory == "" {
		return false, true
	}
	for _, scope := range []string{workspace, ""} {
		record, found, err := store.read(scope, string(callID), false)
		if err == nil && found && record.History.ExecutingThread == thread && record.History.lowersJournalCommand(parts) {
			return true, true
		}
	}
	return false, true
}

func replayItem(raw jsontext.Value) (appServerItem, bool, error) {
	var p struct {
		Type, ID, Status, Text, Delivery, Phase, Cwd, Kind, Path string
		Content                                                  jsontext.Value            `json:"content"`
		Command                                                  jsontext.Value            `json:"command"`
		Summary                                                  []string                  `json:"summary_text"`
		Output                                                   *string                   `json:"aggregated_output"`
		Exit                                                     *int                      `json:"exit_code"`
		Process                                                  string                    `json:"process_id"`
		Client                                                   string                    `json:"client_id"`
		AgentThread                                              string                    `json:"agent_thread_id"`
		AgentPath                                                string                    `json:"agent_path"`
		Sender                                                   string                    `json:"sender_thread_id"`
		Receivers                                                []string                  `json:"receiver_thread_ids"`
		Tool                                                     string                    `json:"tool"`
		States                                                   map[string]jsontext.Value `json:"agents_states"`
		Questions                                                []nativeQuestion          `json:"questions"`
		Changes                                                  map[string]struct {
			Type string `json:"type"`
			Diff string `json:"unified_diff"`
			Move string `json:"move_path"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(raw, &p, json.MatchCaseInsensitiveNames(true)); err != nil {
		return appServerItem{}, false, err
	}
	i := appServerItem{ID: p.ID, Type: p.Type, Status: p.Status, Cwd: p.Cwd, Text: p.Text, Summary: p.Summary, Content: p.Content, AggregatedOutput: p.Output, ExitCode: p.Exit, ProcessID: p.Process, ClientID: p.Client, AgentThreadID: p.AgentThread, AgentPath: p.AgentPath, Kind: p.Kind, Path: p.Path, Delivery: p.Delivery, Phase: p.Phase, Questions: p.Questions, Tool: p.Tool, SenderThreadID: p.Sender, ReceiverThreadIDs: p.Receivers}
	i.AgentsStates = make(map[string]appServerAgentState, len(p.States))
	for id, raw := range p.States {
		var status string
		if len(raw) > 0 && raw[0] == '"' {
			if err := json.Unmarshal(raw, &status); err != nil {
				return i, false, err
			}
		} else {
			var tagged map[string]jsontext.Value
			if err := json.Unmarshal(raw, &tagged); err != nil {
				return i, false, err
			}
			if len(tagged) != 1 {
				return i, false, errors.New("invalid retained agent status")
			}
			for tag := range tagged {
				status = tag
			}
		}
		switch status {
		case "pending_init":
			status = "pendingInit"
		case "not_found":
			status = "notFound"
		case "running", "interrupted", "completed", "errored", "shutdown":
		default:
			return i, false, fmt.Errorf("unsupported retained agent status %q", status)
		}
		i.AgentsStates[id] = appServerAgentState{Status: status}
	}
	if i.Type != "" {
		i.Type = strings.ToLower(i.Type[:1]) + i.Type[1:]
	}
	switch i.Type {
	case "userMessage", "reasoning", "subAgentActivity", "contextCompaction", "collabAgentToolCall", "imageView":
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
		var args []string
		if err := json.Unmarshal(p.Command, &args); err != nil {
			return i, false, err
		}
		if len(args) >= 3 && (args[1] == "-c" || args[1] == "-lc") {
			i.Command = args[2]
		} else {
			for j := range args {
				args[j] = shellQuoteArgument(args[j])
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
			change := appServerFileChange{Path: path, Diff: c.Diff}
			change.Kind.Type, change.Kind.MovePath = c.Type, c.Move
			i.Changes = append(i.Changes, change)
		}
	default:
		return i, false, nil
	}
	return i, true, nil
}

func replayDeltas(rng *rand.Rand, endEvent uiReplayEvent, start, end time.Time) []uiReplayEvent {
	i := endEvent.Params.Item
	text, method, chunk := i.Text, "item/agentMessage/delta", 12
	switch i.Type {
	case "agentMessage":
		if len(i.Questions) > 0 {
			return nil
		}
	case "reasoning":
		text, method = strings.Join(i.Summary, "\n\n"), "item/reasoning/summaryTextDelta"
	case "commandExecution":
		if i.AggregatedOutput == nil {
			return nil
		}
		text, method, chunk = *i.AggregatedOutput, "item/commandExecution/outputDelta", 96
	default:
		return nil
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	n := min(2048, max(1, len(runes)/chunk), max(1, int(end.Sub(start)/(35*time.Millisecond))))
	weights := make([]int, n)
	total := 0
	for j := range weights {
		weights[j] = 1 + rng.IntN(7)
		total += weights[j]
	}
	var events []uiReplayEvent
	at, pos := 0, 0
	for j, w := range weights {
		at += w
		next := len(runes) * (j + 1) / n
		e := uiReplayEvent{At: start.Add(time.Duration(float64(end.Sub(start)) * 0.95 * float64(at) / float64(total))), Method: method, Params: appServerEvent{ThreadID: endEvent.Params.ThreadID, TurnID: endEvent.Params.TurnID, ItemID: i.ID, Delta: string(runes[pos:next])}}
		// Agent message deltas share the same typed identity as live messages.
		if i.Type == "agentMessage" {
			e.Params.Item = appServerItem{Type: i.Type, ID: i.ID}
		}
		events = append(events, e)
		pos = next
	}
	return events
}

func (r *sessionUIReplay) readProviders(ctx context.Context, path string, loaded map[string]bool) error {
	return readReplayLines(ctx, path, func(line []byte) error {
		var p struct {
			Boundary string    `json:"boundary"`
			Thread   string    `json:"thread_id"`
			At       time.Time `json:"captured_at"`
			Duration int64     `json:"duration_ms"`
		}
		if err := json.Unmarshal(line, &p); err != nil {
			return err
		}
		if p.Boundary != "provider" || !loaded[p.Thread] {
			return nil
		}
		if p.At.IsZero() || p.Duration < 0 || p.Duration > 86400000 {
			return errors.New("invalid provider timing")
		}
		r.Providers++
		r.Events = append(r.Events, uiReplayEvent{At: p.At.Add(-time.Duration(p.Duration) * time.Millisecond), Method: "replay/providerStarted", Params: appServerEvent{ThreadID: p.Thread}}, uiReplayEvent{At: p.At, Method: "replay/providerCompleted", Params: appServerEvent{ThreadID: p.Thread}})
		return nil
	})
}
