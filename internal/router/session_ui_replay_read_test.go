package router

import (
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const replayTestEpoch int64 = 1700000000000

func replayTestRecord(kind string, at int64, payload map[string]any) map[string]any {
	return map[string]any{"type": kind, "timestamp": time.UnixMilli(at), "payload": payload}
}

func replayTestMeta(id string) map[string]any {
	return replayTestRecord("session_meta", replayTestEpoch, map[string]any{"id": id, "cwd": "/workspace/replay"})
}

func replayTestItem(thread, turn string, start, end int64, item map[string]any) map[string]any {
	return replayTestRecord("event_msg", end, map[string]any{
		"type": "item_completed", "thread_id": thread, "turn_id": turn,
		"started_at_ms": start, "completed_at_ms": end, "item": item,
	})
}

func replayTestWrite(t *testing.T, dir, name string, records ...map[string]any) string {
	t.Helper()
	var data strings.Builder
	for _, record := range records {
		b, err := json.Marshal(&record)
		if err != nil {
			t.Fatal(err)
		}
		data.Write(b)
		data.WriteByte('\n')
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionUIReplayNormalizesItemsAndRecordedTiming(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "must-not-exist")
	command := "touch " + shellQuoteArgument(sentinel)
	items := []map[string]any{
		{"type": "UserMessage", "id": "user", "content": []map[string]any{{"type": "text", "text": "question"}}},
		{"type": "AgentMessage", "id": "message", "content": []map[string]any{{"type": "text", "text": "hello "}, {"type": "text", "text": "世界"}}, "phase": "final_answer"},
		{"type": "Reasoning", "id": "reasoning", "summary_text": []string{"first", "second"}},
		{"type": "CommandExecution", "id": "command", "command": []string{"sh", "-c", command}, "aggregated_output": "retained output", "exit_code": 0, "status": "completed", "cwd": "/workspace/replay"},
		{"type": "FileChange", "id": "files", "changes": map[string]any{"z.go": map[string]any{"type": "update", "unified_diff": "z diff"}, "a.go": map[string]any{"type": "add", "unified_diff": "a diff"}}},
		{"type": "SubAgentActivity", "id": "activity", "kind": "message"},
		{"type": "ContextCompaction", "id": "compact"},
		{"type": "CollabAgentToolCall", "id": "collab", "tool": "wait", "sender_thread_id": "root", "receiver_thread_ids": []string{"root"}, "agents_states": map[string]any{"root": map[string]any{"completed": "retained answer"}}},
	}
	records := []map[string]any{
		replayTestMeta("root"),
		// Retained line order must not replace actual item timing.
		replayTestRecord("event_msg", replayTestEpoch+20000, map[string]any{"type": "task_complete", "turn_id": "turn"}),
	}
	for j := len(items) - 1; j >= 0; j-- {
		records = append(records, replayTestItem("root", "turn", replayTestEpoch+int64(j+1)*1000, replayTestEpoch+int64(j+1)*1000+500, items[j]))
	}
	records = append(records,
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+18000, replayTestEpoch+19000, map[string]any{"type": "FutureItem", "id": "unknown"}),
	)
	path := replayTestWrite(t, dir, "root.jsonl", records...)
	r, err := readSessionUIReplay(t.Context(), path, "", 17)
	if err != nil {
		t.Fatal(err)
	}
	if r.Thread != "root" || r.Cwd != "/workspace/replay" || r.Items != len(items) || r.Unsupported["futureItem"] != 1 {
		t.Fatalf("unexpected replay metadata: %+v", r)
	}
	if !r.Start.Equal(time.UnixMilli(replayTestEpoch)) || !r.End.Equal(time.UnixMilli(replayTestEpoch+20000)) {
		t.Fatalf("recorded bounds changed: %v to %v", r.Start, r.End)
	}
	completed := make(map[string]appServerItem)
	started := make(map[string]appServerItem)
	for j, event := range r.Events {
		if j > 0 && event.At.Before(r.Events[j-1].At) {
			t.Fatal("events are not chronological")
		}
		if event.Params.ThreadID != "root" || event.Params.TurnID != "turn" {
			t.Fatalf("lost event identity: %+v", event)
		}
		switch event.Method {
		case "item/completed", "item/started":
			index := slices.IndexFunc(items, func(item map[string]any) bool { return item["id"] == event.Params.ItemID })
			if index < 0 {
				t.Fatalf("unexpected item: %+v", event)
			}
			at := replayTestEpoch + int64(index+1)*1000
			if event.Method == "item/completed" {
				at += 500
				completed[event.Params.ItemID] = event.Params.Item
			} else {
				started[event.Params.ItemID] = event.Params.Item
			}
			if !event.At.Equal(time.UnixMilli(at)) {
				t.Fatalf("%s %s timing = %v, want %v", event.Method, event.Params.ItemID, event.At, time.UnixMilli(at))
			}
		case "turn/started":
			if event.Params.Turn.ID != "turn" || event.Params.Turn.Status != "inProgress" {
				t.Fatalf("bad turn start: %+v", event)
			}
		case "turn/completed":
			if event.Params.Turn.ID != "turn" || event.Params.Turn.Status != "completed" {
				t.Fatalf("bad turn completion: %+v", event)
			}
		}
	}
	if len(completed) != len(items) || len(started) != len(items) {
		t.Fatalf("missing lifecycle events: starts=%d ends=%d", len(started), len(completed))
	}
	for _, item := range items {
		id := item["id"].(string)
		rawType := item["type"].(string)
		wantType := strings.ToLower(rawType[:1]) + rawType[1:]
		if completed[id].Type != wantType {
			t.Errorf("%s type = %q, want %q", id, completed[id].Type, wantType)
		}
	}
	if completed["message"].Text != "hello 世界" || completed["message"].Phase != "final_answer" || !reflect.DeepEqual(completed["reasoning"].Summary, []string{"first", "second"}) {
		t.Fatal("message/reasoning content was not normalized")
	}
	cmd := completed["command"]
	if cmd.Command != command || cmd.AggregatedOutput == nil || *cmd.AggregatedOutput != "retained output" || cmd.ExitCode == nil || *cmd.ExitCode != 0 {
		t.Fatalf("command evidence changed: %+v", cmd)
	}
	if started["command"].AggregatedOutput != nil || started["command"].ExitCode != nil || started["command"].Status != "inProgress" || started["message"].Text != "" || len(started["reasoning"].Summary) != 0 {
		t.Fatal("item start exposes completed content")
	}
	if changes := completed["files"].Changes; len(changes) != 2 || changes[0].Path != "a.go" || changes[0].Diff != "a diff" || changes[1].Path != "z.go" {
		t.Fatalf("file evidence lost or unstable: %+v", changes)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("replay executed retained command: stat sentinel = %v", err)
	}
}

func TestSessionUIReplayDiscoversSiblingThreadsAndFiltersProviders(t *testing.T) {
	dir := t.TempDir()
	root := replayTestWrite(t, dir, "rollout-2023-root.jsonl", replayTestMeta("root"),
		replayTestItem("root", "turn", replayTestEpoch+1000, replayTestEpoch+2000, map[string]any{"type": "SubAgentActivity", "id": "spawn", "agent_thread_id": "child", "agent_path": "/root/worker", "kind": "spawn"}),
		replayTestItem("root", "turn", replayTestEpoch+2000, replayTestEpoch+3000, map[string]any{"type": "SubAgentActivity", "id": "missing", "agent_thread_id": "absent", "agent_path": "/root/absent", "kind": "spawn"}),
	)
	replayTestWrite(t, dir, "rollout-2023-child.jsonl", replayTestMeta("child"), replayTestItem("child", "child-turn", replayTestEpoch+2200, replayTestEpoch+2800, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "child response"}}}))
	// An unrelated sibling must not be discovered just because it exists.
	replayTestWrite(t, dir, "rollout-2023-unrelated.jsonl", replayTestMeta("unrelated"), replayTestItem("unrelated", "other-turn", replayTestEpoch+10, replayTestEpoch+20, map[string]any{"type": "ContextCompaction", "id": "other"}))
	debugDir := t.TempDir()
	replayTestWrite(t, debugDir, "capture.jsonl",
		map[string]any{"boundary": "provider", "thread_id": "root", "captured_at": time.UnixMilli(replayTestEpoch + 1800), "duration_ms": 700},
		map[string]any{"boundary": "provider", "thread_id": "child", "captured_at": time.UnixMilli(replayTestEpoch + 2600), "duration_ms": 300},
		map[string]any{"boundary": "provider", "thread_id": "absent", "captured_at": time.UnixMilli(replayTestEpoch + 9000), "duration_ms": 200},
		map[string]any{"boundary": "provider", "thread_id": "unrelated", "captured_at": time.UnixMilli(replayTestEpoch + 9000), "duration_ms": 200},
		map[string]any{"boundary": "command", "thread_id": "root", "captured_at": time.UnixMilli(replayTestEpoch + 9000), "duration_ms": 200},
	)
	r, err := readSessionUIReplay(t.Context(), root, debugDir, 3)
	if err != nil {
		t.Fatal(err)
	}
	if r.Items != 3 || r.Threads["root"] != "/root" || r.Threads["child"] != "/root/worker" || r.Threads["unrelated"] != "" || !reflect.DeepEqual(r.Missing, []string{"absent"}) {
		t.Fatalf("incorrect thread discovery: %+v", r)
	}
	if r.Providers != 2 {
		t.Fatalf("provider count = %d, want only two loaded threads", r.Providers)
	}
	got := make(map[string][]int64)
	for j, event := range r.Events {
		if j > 0 && event.At.Before(r.Events[j-1].At) {
			t.Fatal("merged thread/provider events are not chronological")
		}
		if strings.HasPrefix(event.Method, "replay/provider") {
			key := event.Params.ThreadID + ":" + event.Method
			got[key] = append(got[key], event.At.UnixMilli()-replayTestEpoch)
		}
	}
	want := map[string][]int64{"root:replay/providerStarted": {1100}, "root:replay/providerCompleted": {1800}, "child:replay/providerStarted": {2300}, "child:replay/providerCompleted": {2600}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider timing = %v, want %v", got, want)
	}
}

func TestSessionUIReplayRejectsInvalidEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing start", func(p map[string]any) { delete(p, "started_at_ms") }},
		{"missing completion", func(p map[string]any) { delete(p, "completed_at_ms") }},
		{"completion before start", func(p map[string]any) { p["completed_at_ms"] = replayTestEpoch - 1 }},
		{"missing item id", func(p map[string]any) { delete(p["item"].(map[string]any), "id") }},
		{"missing turn id", func(p map[string]any) { delete(p, "turn_id") }},
		{"wrong thread", func(p map[string]any) { p["thread_id"] = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := replayTestItem("root", "turn", replayTestEpoch, replayTestEpoch+100, map[string]any{"type": "ContextCompaction", "id": "item"})
			tc.mutate(record["payload"].(map[string]any))
			path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"), record)
			if _, err := readSessionUIReplay(t.Context(), path, "", 1); err == nil || !strings.Contains(err.Error(), path+":2:") {
				t.Fatalf("invalid evidence lacks source-qualified error: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		tail string
	}{
		{"malformed JSON", `{"type":"event_msg",`},
		{"missing event timestamp", `{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}`},
		{"invalid event timestamp", `{"timestamp":"not-a-time","type":"event_msg","payload":{"type":"task_started","turn_id":"turn"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"))
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := fmt.Fprintln(f, tc.tail)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("fixture write: %v, close: %v", writeErr, closeErr)
			}
			if _, err := readSessionUIReplay(t.Context(), path, "", 1); err == nil || !strings.Contains(err.Error(), path+":2:") {
				t.Fatalf("invalid evidence lacks source-qualified error: %v", err)
			}
		})
	}
}

func TestSessionUIReplaySimulatedDeltasPreserveUTF8AndSeed(t *testing.T) {
	message := strings.Repeat("你好🌿 café ", 80)
	summaries := []string{strings.Repeat("考慮 αβ ", 60), strings.Repeat("結論 ✅ ", 60)}
	output := strings.Repeat("stdout 世界 🧪\n", 80)
	start, end := replayTestEpoch+1000, replayTestEpoch+9000
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", replayTestMeta("root"),
		replayTestItem("root", "turn", start, end, map[string]any{"type": "AgentMessage", "id": "message", "content": []map[string]any{{"text": message}}}),
		replayTestItem("root", "turn", start, end, map[string]any{"type": "Reasoning", "id": "reasoning", "summary_text": summaries}),
		replayTestItem("root", "turn", start, end, map[string]any{"type": "CommandExecution", "id": "output", "command": []string{"echo", "not executed"}, "aggregated_output": output}),
	)
	read := func(seed uint64) *sessionUIReplay {
		t.Helper()
		r, err := readSessionUIReplay(t.Context(), path, "", seed)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r, same, other := read(123), read(123), read(456)
	if !reflect.DeepEqual(r.Events, same.Events) {
		t.Fatal("same seed does not reproduce identical reconstruction")
	}
	texts := make(map[string]string)
	counts := make(map[string]int)
	cadence := func(events []uiReplayEvent) []time.Time {
		var times []time.Time
		for _, event := range events {
			if strings.HasSuffix(event.Method, "Delta") || strings.HasSuffix(event.Method, "/delta") {
				times = append(times, event.At)
			}
		}
		return times
	}
	if reflect.DeepEqual(cadence(r.Events), cadence(other.Events)) {
		t.Fatal("different seeds do not change simulated cadence")
	}
	methods := map[string]string{"message": "item/agentMessage/delta", "reasoning": "item/reasoning/summaryTextDelta", "output": "item/commandExecution/outputDelta"}
	for _, event := range r.Events {
		if event.Method != methods[event.Params.ItemID] {
			continue
		}
		if !event.At.After(time.UnixMilli(start)) || !event.At.Before(time.UnixMilli(end)) || !utf8.ValidString(event.Params.Delta) || event.Params.Delta == "" {
			t.Fatalf("delta violates recorded bounds or UTF8: %+v", event)
		}
		if event.Params.ThreadID != "root" || event.Params.TurnID != "turn" {
			t.Fatalf("delta identity lost: %+v", event)
		}
		texts[event.Params.ItemID] += event.Params.Delta
		counts[event.Params.ItemID]++
	}
	want := map[string]string{"message": message, "reasoning": strings.Join(summaries, "\n\n"), "output": output}
	if !reflect.DeepEqual(texts, want) {
		t.Fatal("simulated deltas do not reconstruct exact retained text")
	}
	for id := range want {
		if counts[id] < 2 {
			t.Errorf("%s was not simulated as streaming chunks", id)
		}
	}
}

func TestSessionUIReplayChildInheritedHistoryDoesNotDuplicateAncestor(t *testing.T) {
	dir := t.TempDir()
	rootStart := replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "root-turn"})
	rootComplete := replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_complete", "turn_id": "root-turn"})
	spawn := replayTestItem("root", "root-turn", replayTestEpoch+1000, replayTestEpoch+2000, map[string]any{"type": "SubAgentActivity", "id": "spawn", "agent_thread_id": "child", "agent_path": "/root/worker", "kind": "spawn"})
	root := replayTestWrite(t, dir, "rollout-2023-root.jsonl", replayTestMeta("root"), rootStart, spawn, rootComplete)
	replayTestWrite(t, dir, "rollout-2023-child.jsonl",
		replayTestMeta("child"),
		// Forked rollouts retain ancestor metadata and events after their own identity.
		replayTestMeta("root"), rootStart, spawn, rootComplete,
		replayTestRecord("event_msg", replayTestEpoch+2000, map[string]any{"type": "task_started", "turn_id": "child-turn"}),
		replayTestItem("child", "child-turn", replayTestEpoch+2100, replayTestEpoch+2900, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "child response"}}}),
		replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_complete", "turn_id": "child-turn"}),
	)
	r, err := readSessionUIReplay(t.Context(), root, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Items != 2 || len(r.Missing) != 0 {
		t.Fatalf("inherited items were duplicated or child lost: items=%d missing=%v", r.Items, r.Missing)
	}
	counts := make(map[string]int)
	for _, event := range r.Events {
		if event.Method == "turn/started" || event.Method == "turn/completed" || event.Method == "item/completed" {
			counts[event.Params.ThreadID+":"+event.Params.TurnID+":"+event.Method]++
		}
	}
	want := map[string]int{
		"root:root-turn:turn/started": 1, "root:root-turn:turn/completed": 1, "root:root-turn:item/completed": 1,
		"child:child-turn:turn/started": 1, "child:child-turn:turn/completed": 1, "child:child-turn:item/completed": 1,
	}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("inherited history acquired child identity or duplicated events: %v", counts)
	}
}
