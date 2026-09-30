package router

import (
	"bytes"
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"
)

func regressionFinalResponse(t *testing.T, id, body string) []byte {
	t.Helper()
	answer := map[string]any{"type": "message", "id": "raw-" + id, "role": "assistant", "phase": "final_answer", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": body}}}
	return mustTestJSON(t, map[string]any{"id": id, "status": "completed", "output": []any{answer}})
}

func regressionCardText(t *testing.T, wire []byte) string {
	t.Helper()
	var response struct {
		Output []map[string]jsonv1.RawMessage `json:"output"`
	}
	if err := jsonv2.Unmarshal(wire, &response); err != nil {
		t.Fatal(err)
	}
	for _, item := range response.Output {
		if text := commentaryMessageText(item); strings.HasPrefix(text, "Journal") {
			return text
		}
	}
	t.Fatalf("missing terminal card: %s", wire)
	return ""
}

func TestJournalTreeNaturalMarkdownAnswerKeepsShape(t *testing.T) {
	for _, final := range []string{
		"\nResult\n\n- first\n- second",
		"| Key | Value |\n| --- | --- |\n| A | B |",
		"Result\r\n\r\n| Key | Value |\r\n| --- | --- |\r\n| A | B |",
	} {
		t.Run(strings.TrimSpace(strings.Split(final, "\n")[0]), func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Work"), State: new("working")},
			}); err != nil {
				t.Fatal(err)
			}
			wire, err := transform.TransformJSON(regressionFinalResponse(t, "markdown-final", final))
			if err != nil {
				t.Fatal(err)
			}
			card := regressionCardText(t, wire)
			if strings.Contains(card, final) || !strings.Contains(string(wire), `"id":"raw-markdown-final"`) {
				t.Fatalf("work report copied or replaced original Markdown: %q", card)
			}
			var response struct {
				Output []map[string]jsonv1.RawMessage `json:"output"`
			}
			if err := jsonv2.Unmarshal(wire, &response); err != nil {
				t.Fatal(err)
			}
			if commentaryMessageText(response.Output[len(response.Output)-1]) != final {
				t.Fatalf("ordinary final changed its Markdown: %s", wire)
			}
			transform.Delivered(wire)
			transform.ReleaseDelivery()
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{{Op: "log", P: "/1", Text: new("Post-answer fact")}}); err != nil {
				t.Fatalf("captured final left invalid v2 tree: %v", err)
			}
		})
	}
}

func TestJournalTreeEventCapacityPreservesNaturalAnswer(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "json"
		if stream {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, func(j *threadJournal, exists bool) error {
				if !exists {
					t.Fatal("missing initialized journal")
				}
				j.Version = 2
				j.TreeAuthored = true
				j.Events = make([]journalEvent, maxJournalEvents)
				for i := range j.Events {
					j.Events[i] = journalEvent{Seq: uint64(i + 1), Op: "log", Path: "/1", Fields: journalNode{Path: "/1", Kind: "note", Title: "Retained fact"}}
				}
				j.Sequence = maxJournalEvents
				j.LiveSeq, j.FlushSeq = j.Sequence, j.Sequence
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{{Op: "log", Text: new("Overflow")}}); !errors.Is(err, errJournalEventLimit) {
				t.Fatalf("irreducible event cap must have distinct error: %v", err)
			}
			response := regressionFinalResponse(t, "capacity-final", "Provider answer survives")
			var visible []byte
			var err error
			if stream {
				var events [][]byte
				events, err = transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": jsonv1.RawMessage(response)}))
				visible = bytes.Join(events, []byte("\n"))
			} else {
				visible, err = transform.TransformJSON(response)
			}
			if err != nil || !strings.Contains(string(visible), "Provider answer survives") || strings.Contains(string(visible), "Journal flush") {
				t.Fatalf("event limit lost provider answer or fabricated flush: %s: %v", visible, err)
			}
		})
	}
}

func TestJournalTreeForkDoesNotRedeliverFlushedHistory(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{{Op: "log", Text: new("Earlier fact")}}); err != nil {
		t.Fatal(err)
	}
	first, err := transform.prepareJournalDelivery(true)
	if err != nil || len(first) != 1 {
		t.Fatalf("first terminal card: %v %v", first, err)
	}
	transform.Delivered(mustTestJSON(t, map[string]any{"status": "completed", "output": first}))
	transform.ReleaseDelivery()
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", transform.shellThreadID); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "fork", "", []journalMutation{{Op: "log", Text: new("Fork fact")}}); err != nil {
		t.Fatal(err)
	}
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)}, "tools": []any{map[string]any{"type": "function", "name": "lookup"}}}))
	if err != nil {
		t.Fatal(err)
	}
	fork, err := proxy.prepareRequest(t.Context(), &request, "fork", "fork", codexTurnMetadata{RequestKind: "turn", ThreadID: "fork", AgentName: "/root", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Close()
	card, err := fork.prepareJournalDelivery(true)
	if err != nil || len(card) != 1 {
		t.Fatalf("fork terminal card: %v %v", card, err)
	}
	got := commentaryMessageText(card[0])
	if !strings.Contains(got, "Fork fact") || strings.Contains(got, "Earlier fact") {
		t.Fatalf("fork re-delivered flushed source events: %q", got)
	}
	fork.ReleaseDelivery()
}

func TestJournalTreeMigratedV1FlushDoesNotRedeliverHistory(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	if err := proxy.journals.transaction(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, func(j *threadJournal, exists bool) error {
		if !exists {
			t.Fatal("missing initialized journal")
		}
		j.Version = 1
		j.Sequence = 1
		j.NextID = 1
		j.Items = []journalItem{{ID: "amber", Text: "Old flushed fact", Author: "/root", Created: 1, Updated: 1, Reported: true, Flushed: true}}
		j.Events = nil
		j.NextOrdinal = nil
		j.LiveSeq, j.FlushSeq = 0, 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, transform.shellThreadID, "", []journalMutation{{Op: "log", Text: new("New fact")}}); err != nil {
		t.Fatal(err)
	}
	card, err := transform.prepareJournalDelivery(true)
	if err != nil || len(card) != 1 {
		t.Fatalf("migrated terminal card: %v %v", card, err)
	}
	got := commentaryMessageText(card[0])
	if !strings.Contains(got, "New fact") || strings.Contains(got, "Old flushed fact") {
		t.Fatalf("v1 migration re-delivered flushed item: %q", got)
	}
	transform.ReleaseDelivery()
}

func TestJournalTreeBodyOnlyEditVisibleToMainAndChild(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "child"}[child], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			parent := ""
			if child {
				parent = "root"
			}
			transform, _ := prepareActivityTest(t, proxy, "body", "body", parent, "/root/body", nil)
			workspace := transform.directory
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "body", "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Finding"), Body: new("Old body")},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "body", "", []journalMutation{{Op: "set", P: "/1", Body: new("New body")}}); err != nil {
				t.Fatal(err)
			}
			requestJournalFinish(t, transform)
			wire, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "body-finish", "status": "completed", "output": []any{}}))
			if err != nil {
				t.Fatal(err)
			}
			got := regressionCardText(t, wire)
			if !strings.Contains(got, "New body") {
				t.Fatalf("body-only edit not visible: %q", got)
			}
		})
	}
}

func TestJournalTreeChildChecksEventCardCapacity(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
	key := journalKey(child.directory, "child")
	current := proxy.journals.memory[key]
	current.TreeAuthored = true
	current.Items = []journalItem{{ID: "/1", Path: "/1", Kind: "note", Title: "Latest", Text: "Latest", Updated: 1}}
	body := strings.Repeat("x", maxJournalItemBytes-1)
	for i := range maxJournalFlushBytes/len(body) + 1 {
		current.Events = append(current.Events, journalEvent{Seq: uint64(i + 1), Path: "/1", Op: "set", Fields: journalNode{Path: "/1", Kind: "note", Title: body}})
	}
	current.Sequence = uint64(len(current.Events))
	proxy.journals.memory[key] = current
	if _, err := child.prepareJournalDelivery(true); err == nil {
		t.Fatal("oversized event card accepted because current tree was small")
	}
	child.ReleaseDelivery()
}

func TestJournalTreeEventContentCapacityAndCompaction(t *testing.T) {
	repeated := journalEvent{Seq: 1, Path: "/1", Op: "set", Fields: journalNode{Kind: "task", Title: strings.Repeat("x", maxJournalItemBytes-1)}}
	count := maxJournalEventBytes / journalEventSize(repeated)
	journal := threadJournal{Events: make([]journalEvent, count)}
	for i := range journal.Events {
		journal.Events[i] = repeated
	}
	if err := journal.appendEvent(repeated); err != nil || len(journal.Events) != 2 {
		t.Fatalf("consecutive edits did not compact at byte capacity: %d %v", len(journal.Events), err)
	}
	repeated.Op = "add"
	journal.Events = make([]journalEvent, count)
	for i := range journal.Events {
		journal.Events[i] = repeated
	}
	if err := journal.appendEvent(repeated); !errors.Is(err, errJournalEventLimit) {
		t.Fatalf("irreducible note bytes exceeded event capacity: %v", err)
	}
}

func resumeJournalRegression(t *testing.T, proxy *mekugiProxy, workspace, thread string) *mekugiResponseTransform {
	t.Helper()
	request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription)}, "tools": []any{map[string]any{"type": "function", "name": "lookup"}}}))
	if err != nil {
		t.Fatal(err)
	}
	transform, err := proxy.prepareRequest(t.Context(), &request, thread+"-resume", thread, codexTurnMetadata{RequestKind: "turn", ThreadID: thread, AgentName: "/root", Directories: map[string]jsonv1.RawMessage{workspace: nil}}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transform.Close)
	return transform
}

func TestJournalTreeEmptyOutcomeDoesNotReportUnchangedRemainingNextTurn(t *testing.T) {
	first, proxy, _, workspace := newDurableTreeTransform(t)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, first.shellThreadID, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Pending task")}}); err != nil {
		t.Fatal(err)
	}
	wire, err := first.TransformJSON(regressionFinalResponse(t, "first-empty", "Done."))
	if err != nil {
		t.Fatal(err)
	}
	first.Delivered(wire)
	first.ReleaseDelivery()
	second := resumeJournalRegression(t, proxy, workspace, first.shellThreadID)
	next, err := second.TransformJSON(regressionFinalResponse(t, "second-empty", "Done."))
	if err != nil {
		t.Fatal(err)
	}
	var a, b struct {
		Output []map[string]jsonv1.RawMessage `json:"output"`
	}
	if err := jsonv2.Unmarshal(wire, &a); err != nil {
		t.Fatal(err)
	}
	if err := jsonv2.Unmarshal(next, &b); err != nil {
		t.Fatal(err)
	}
	if len(a.Output) != 1 || !strings.Contains(commentaryMessageText(a.Output[0]), "Pending task") || len(b.Output) != 1 || jsonString(b.Output[0], "id") != "raw-second-empty" || commentaryMessageText(b.Output[0]) != "Done." {
		t.Fatalf("unchanged remaining work produced another card: %s", next)
	}
	second.ReleaseDelivery()
}

func TestJournalTreeLegacyAndSparseNativeReceiptsDoNotRepeat(t *testing.T) {
	first, proxy, _, workspace := newDurableTreeTransform(t)
	wire, err := first.TransformJSON(regressionFinalResponse(t, "legacy-final", "Earlier answer"))
	if err != nil {
		t.Fatal(err)
	}
	first.Delivered(wire)
	first.ReleaseDelivery()
	second := resumeJournalRegression(t, proxy, workspace, first.shellThreadID)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, second.shellThreadID, "", []journalMutation{{Op: "log", Text: new("New fact")}}); err != nil {
		t.Fatal(err)
	}
	next, err := second.TransformJSON(regressionFinalResponse(t, "tree-final", "Done."))
	if err != nil {
		t.Fatal(err)
	}
	if got := regressionCardText(t, next); strings.Contains(got, "Earlier answer") || !strings.Contains(got, "New fact") {
		t.Fatalf("legacy receipt was not carried into v2: %s", got)
	}
	second.Delivered(next)
	second.ReleaseDelivery()
	ids, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, second.shellThreadID, "", []journalMutation{{Op: "log", Text: new("Unseen earlier event")}, {Op: "log", Text: new("Seen later event")}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := proxy.journals.list(t.Context(), proxy.replayStore, workspace, second.shellThreadID)
	if err != nil {
		t.Fatal(err)
	}
	later := items[len(items)-1]
	if err := proxy.journals.acknowledge(t.Context(), proxy.replayStore, workspace, second.shellThreadID, map[string]uint64{ids[1]: later.Updated}, true); err != nil {
		t.Fatal(err)
	}
	third := resumeJournalRegression(t, proxy, workspace, first.shellThreadID)
	final, err := third.TransformJSON(regressionFinalResponse(t, "sparse-final", "Done."))
	if err != nil {
		t.Fatal(err)
	}
	if got := regressionCardText(t, final); strings.Contains(got, "Seen later event") || !strings.Contains(got, "Unseen earlier event") {
		t.Fatalf("sparse receipt lost or repeated events: %s", got)
	}
	third.ReleaseDelivery()
}
