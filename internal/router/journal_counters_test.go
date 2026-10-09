package router

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/capturer"
)

func TestJournalCountersRuntimeCarriersReachMetricsExport(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	recorder, err := capturer.New(capturer.Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		transform.ctx = request.Context()
		for range 2 {
			if _, err := proxy.applyJournal(request.Context(), workspace, thread, "runtime:runtime-call", []journalMutation{{Op: "log", Text: new("private progress note")}}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := proxy.readJournalTree(request.Context(), workspace, thread, "", "", nil, ""); err != nil {
			t.Fatal(err)
		}
		proxy.countJournalRead(request.Context(), workspace, thread, "", "read")
		response := mustTestJSON(t, map[string]any{"id": "runtime-final", "status": "completed", "output": []any{map[string]any{"type": "message", "id": "runtime-message", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Done."}}}}})
		visible, err := transform.TransformJSON(response)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(visible)
	}))
	request := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(`{"model":"test","input":"fixture"}`))
	request.Header.Set("thread-id", thread)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	var output bytes.Buffer
	if err := recorder.WriteMetrics(&output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Exchanges []struct {
			Journal *capturer.JournalMetrics `json:"journal"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Exchanges) != 1 || result.Exchanges[0].Journal == nil {
		t.Fatalf("missing counters: %s", &output)
	}
	counts := result.Exchanges[0].Journal
	if counts.Operations["log"] != 1 || counts.Operations["read"] != 1 || counts.StandaloneRequests != 0 || counts.FinalAnswerBytes != 5 || counts.EmptyOutcomes != 1 {
		t.Fatalf("runtime counters: %+v", counts)
	}
	if bytes.Contains(output.Bytes(), []byte("private progress note")) {
		t.Fatal("journal text leaked into metrics")
	}
}

func journalTestCounters(t *testing.T, proxy *mekugiProxy, workspace, thread string) (*capturer.JournalMetrics, uint64) {
	t.Helper()
	j, exists, err := readThreadJournal(proxy.replayStore, workspace, thread)
	if err != nil || !exists {
		t.Fatalf("journal: exists=%t err=%v", exists, err)
	}
	return j.Counters, j.Sequence
}

func TestJournalCountersAtomicReplayRestartAndFork(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	mutations := []journalMutation{{Op: "plan", Tasks: []jsontext.Value{jsontext.Value(`"First"`), jsontext.Value(`"Second"`)}}}
	for range 2 {
		if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "plan", mutations); err != nil {
			t.Fatal(err)
		}
	}
	counts, seq := journalTestCounters(t, proxy, workspace, thread)
	if counts == nil || counts.Operations["plan"] != 1 || len(counts.Operations) != 1 || counts.StartedAt == "" {
		t.Fatalf("plan count: %+v", counts)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "bad", []journalMutation{{Op: "set", P: "/1", State: new("done")}, {Op: "set", P: "/999", State: new("done")}}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	counts, after := journalTestCounters(t, proxy, workspace, thread)
	if counts.Operations["set"] != 0 || after != seq {
		t.Fatalf("failed batch altered counters/events: %+v seq=%d", counts, after)
	}
	proxy.countJournalRead(t.Context(), workspace, thread, "read-one", "read")
	proxy.countJournalRead(t.Context(), workspace, thread, "read-one", "read")
	counts, after = journalTestCounters(t, proxy, workspace, thread)
	if counts.Operations["read"] != 1 || after != seq {
		t.Fatalf("read moved event cursor or counted replay: %+v seq=%d", counts, after)
	}
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	j, _, err := readThreadJournal(reopened, workspace, thread)
	if err != nil || j.Counters.Sequence != counts.Sequence {
		t.Fatalf("counter restart: %+v %v", j.Counters, err)
	}
	if err := proxy.journals.initialize(t.Context(), reopened, workspace, "counter-fork", "/root", thread); err != nil {
		t.Fatal(err)
	}
	fork, _, err := readThreadJournal(reopened, workspace, "counter-fork")
	if err != nil || fork.Counters != nil {
		t.Fatalf("fork inherited performed-operation counters: %+v %v", fork.Counters, err)
	}
}

func TestJournalCounterReceiptsStayBounded(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	for i := range maxJournalCounterReceipts + 8 {
		proxy.countJournalRead(t.Context(), workspace, thread, fmt.Sprintf("counter-read:%d", i), "read")
	}
	latest := fmt.Sprintf("counter-read:%d", maxJournalCounterReceipts+7)
	proxy.countJournalRead(t.Context(), workspace, thread, latest, "read")
	proxy.journalCounters(t.Context(), workspace, thread, "", nil)
	j, _, err := readThreadJournal(proxy.replayStore, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if got := j.Counters.Operations["read"]; got != maxJournalCounterReceipts+8 {
		t.Fatalf("reads=%d, want replay of a recent receipt ignored", got)
	}
	if len(j.CounterReceipts) != maxJournalCounterReceipts || j.CounterReceipts[len(j.CounterReceipts)-1] != latest {
		t.Fatalf("counter receipts=%d last=%q", len(j.CounterReceipts), j.CounterReceipts[len(j.CounterReceipts)-1])
	}
	for receipt := range j.Receipts {
		if strings.HasPrefix(receipt, "counter-") {
			t.Fatalf("counter receipt %q joined permanent call receipts", receipt)
		}
	}
}

func TestJournalCountersNaturalOutcomeIncludesDoneAndUncapturableFinal(t *testing.T) {
	for _, text := range []string{"Done.", "", "Changed two things."} {
		for _, disabled := range []bool{false, true} {
			t.Run(text+map[bool]string{false: "/captured", true: "/uncapturable"}[disabled], func(t *testing.T) {
				transform, proxy, _, workspace := newDurableTreeTransform(t)
				transform.finalAnswer.disabled = disabled
				response := mustTestJSON(t, map[string]any{"id": "counter-final", "status": "completed", "output": []any{map[string]any{"type": "message", "id": "final-item", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}}}})
				for range 2 {
					if err := transform.captureNaturalJournalAnswer(response); err != nil {
						t.Fatal(err)
					}
				}
				counts, _ := journalTestCounters(t, proxy, workspace, transform.shellThreadID)
				wantEmpty := text == "Done." || text == ""
				if counts == nil || counts.FinalAnswers != 1 || counts.FinalAnswerBytes != uint64(len(text)) || counts.LastOutcomeEmpty == nil || *counts.LastOutcomeEmpty != wantEmpty || len(counts.Operations) != 0 {
					t.Fatalf("outcome counters: %+v", counts)
				}
				if (counts.EmptyOutcomes == 1) != wantEmpty {
					t.Fatalf("empty count: %+v", counts)
				}
			})
		}
	}
}

func TestJournalCountersStandaloneRequestNotToolCount(t *testing.T) {
	for _, kind := range []string{"", "function_call", "web_search_call", "shell_call", "tool_search_call"} {
		transform, proxy, _, workspace := newDurableTreeTransform(t)
		calls := []any{
			map[string]any{"type": "function_call", "name": "journal", "call_id": "one", "arguments": `{"op":"read"}`},
			map[string]any{"type": "function_call", "name": "journal", "call_id": "two", "arguments": `{"op":"read"}`},
		}
		if kind != "" {
			calls = append(calls, map[string]any{"type": kind, "status": "completed", "execution": "server", "name": "exec_command", "call_id": "host", "arguments": `{}`})
		}
		response := mustTestJSON(t, map[string]any{"id": "counter-request", "status": "completed", "output": calls})
		for range 2 {
			if err := transform.captureNaturalJournalAnswer(response); err != nil {
				t.Fatal(err)
			}
		}
		counts, _ := journalTestCounters(t, proxy, workspace, transform.shellThreadID)
		if kind != "" {
			if counts != nil && counts.StandaloneRequests != 0 {
				t.Fatalf("mixed useful work counted standalone: %+v", counts)
			}
		} else if counts == nil || counts.StandaloneRequests != 1 {
			t.Fatalf("counted tools instead of requests: %+v", counts)
		}
	}
}

func TestJournalCountersMixedResponseExportsAcceptedMutationImmediately(t *testing.T) {
	transform, _, _, _ := newDurableTreeTransform(t)
	recorder, err := capturer.New(capturer.Config{Mode: "mekugi"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		transform.ctx = request.Context()
		response := mustTestJSON(t, map[string]any{"id": "mixed-response", "status": "completed", "output": []any{
			map[string]any{"id": "note", "type": "function_call", "name": "journal", "call_id": "journal-mixed", "arguments": `{"op":"log","text":"Established fact"}`},
			map[string]any{"id": "host", "type": "function_call", "name": "exec_command", "call_id": "exec-mixed", "arguments": `{"cmd":"true"}`},
		}})
		visible, err := transform.TransformJSON(response)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(visible)
	}))
	request := httptest.NewRequest("POST", "/v1/responses", bytes.NewBufferString(`{"model":"test","input":"fixture"}`))
	request.Header.Set("thread-id", transform.shellThreadID)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	var output bytes.Buffer
	if err := recorder.WriteMetrics(&output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Exchanges []struct {
			Journal *capturer.JournalMetrics `json:"journal"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Exchanges) != 1 || result.Exchanges[0].Journal == nil {
		t.Fatalf("accepted mutation missing from exchange: %s", &output)
	}
	counts := result.Exchanges[0].Journal
	if counts.Operations["log"] != 1 || counts.StandaloneRequests != 0 || counts.FinalAnswers != 0 {
		t.Fatalf("mixed response counters: %+v", counts)
	}
}
