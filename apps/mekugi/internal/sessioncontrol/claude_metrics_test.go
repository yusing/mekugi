package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestClaudeMetricsRetainNativeFactsWithoutDoubleCounting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runtime *ClaudeRuntime
		var input claude.Input
		runtime, selected, settings := claudeFixture(t, claudeRequestFunc(func(_ context.Context, method string, raw jsontext.Value) (jsontext.Value, error) {
			if method != "input" {
				t.Fatalf("unexpected native metrics request: %s", method)
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				t.Fatal(err)
			}
			claudeStarted(t, runtime, input)
			return jsontext.Value(`{"status":"queued"}`), nil
		}))
		emit := func(raw string) {
			t.Helper()
			if _, err := runtime.HandleEvent(t.Context(), jsontext.Value(raw)); err != nil {
				t.Fatal(err)
			}
			if err := runtime.ObserveMetrics(t.Context(), jsontext.Value(raw)); err != nil {
				t.Fatal(err)
			}
		}
		emit(`{"type":"system","subtype":"init","session_id":"native-session","model":"native-model"}`)
		emit(`{"type":"mekugi_effort","session_id":"native-session","effort":"high"}`)
		if err := runtime.ObserveMetrics(t.Context(), jsontext.Value(`{"type":"mekugi_effort","session_id":"other-session","effort":"low"}`)); err != nil {
			t.Fatal(err)
		}
		for turn := 1; turn <= 2; turn++ {
			if _, err := runtime.Input(t.Context(), control.NativeInput{Thread: "child", ID: fmt.Sprintf("turn-%d", turn), Run: true, Message: "work"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
			boundary := `{"type":"system","subtype":"compact_boundary","session_id":"native-session","uuid":"compact-1"}`
			emit(boundary)
			emit(boundary)
			result := fmt.Sprintf(`{"type":"result","subtype":"success","session_id":"native-session","uuid":"result-%d","user_message_uuid":%q,"num_turns":2,"result":"done","modelUsage":{"native-model":{"inputTokens":%d,"cacheReadInputTokens":%d,"cacheCreationInputTokens":%d,"outputTokens":%d,"thinkingTokens":%d}},"total_cost_usd":%g}`, turn, input.UUID, 100*turn, 200*turn, 50*turn, 20*turn, 5*turn, .25*float64(turn))
			emit(result)
			emit(result)
			if details, _ := runtime.Inspect(t.Context(), "child"); details.ContextTokens != nil || details.ContextWindow != nil {
				t.Fatal("new generating result retained stale context while its read was pending")
			}
			if turn == 2 {
				emit(`{"type":"mekugi_context_usage","session_id":"native-session","result_uuid":"result-1","context_tokens":null,"context_window":null}`)
				if facts, _ := runtime.Metrics(); facts.ContextKnown {
					t.Fatal("obsolete context timeout left stale context current")
				}
			}
			emit(fmt.Sprintf(`{"type":"mekugi_context_usage","session_id":"native-session","result_uuid":"result-%d","context_tokens":1234,"context_window":200000}`, turn))
			facts, report := runtime.Metrics()
			if facts.Turns != uint64(turn) || facts.WorkElapsed != time.Duration(turn)*time.Second || !facts.WorkKnown || !facts.WorkSince.IsZero() || report.InputTokens != uint64(350*turn) || report.OutputTokens != uint64(20*turn) || report.ReasoningTokens != uint64(5*turn) || report.Roundtrips != uint64(2*turn) || !report.Cost.Known || report.Cost.Total() != .25*float64(turn) || report.PriorUnknown {
				t.Fatalf("native metrics were lost or doubled: facts=%+v report=%+v", facts, report)
			}
			info, _ := runtime.Thread(t.Context(), "child")
			details, err := ReadClaudeDetails(t.Context(), "child", info.Path)
			if err != nil || details.ReasoningEffort == nil || *details.ReasoningEffort != "high" || details.ContextTokens == nil || *details.ContextTokens != 1234 || details.ContextWindow == nil || *details.ContextWindow != 200000 || details.Compactions == nil || *details.Compactions != 1 {
				t.Fatalf("retained inspection lost context or compaction identity: %+v %v", details, err)
			}
		}
		info, _ := runtime.Thread(t.Context(), "child")
		reopened, err := NewClaudeRuntime(t.Context(), selected, info, settings, runtime.native)
		if err != nil {
			t.Fatal(err)
		}
		facts, report := reopened.Metrics()
		if facts.Turns != 2 || facts.WorkElapsed != 2*time.Second || !facts.WorkSince.IsZero() || report.InputTokens != 700 || report.Roundtrips != 4 {
			t.Fatalf("restored metrics changed lifetime observations: %+v %+v", facts, report)
		}
		emit(`{"type":"mekugi_context_usage","session_id":"native-session","result_uuid":"result-1","context_tokens":9999,"context_window":200000}`)
		if facts, _ := runtime.Metrics(); facts.ContextTokens != 1234 {
			t.Fatal("late context observation replaced a newer result")
		}
		emit(`{"type":"mekugi_context_usage","session_id":"native-session","result_uuid":"result-2","context_tokens":null,"context_window":null}`)
		if details, _ := runtime.Inspect(t.Context(), "child"); details.ContextTokens != nil || details.ContextWindow != nil {
			t.Fatal("unavailable context was fabricated as zero or retained as current")
		}
		if err := selected.Store.Transaction(t.Context(), selected.Writes, func(tx *history.Transaction) error {
			return tx.WriteScope(history.Scope{Version: 1, Thread: "fork", Namespace: "root", Parent: "child"})
		}); err != nil {
			t.Fatal(err)
		}
		forkScope, err := selected.Store.SelectSession(t.Context(), selected.Writes, selected.Limits, "fork")
		if err != nil {
			t.Fatal(err)
		}
		defer forkScope.Close()
		fork, err := NewClaudeRuntime(t.Context(), forkScope, control.ThreadInfo{Thread: "fork", Session: "fork-session", Fork: new("child")}, settings, runtime.native)
		if err != nil {
			t.Fatal(err)
		}
		baseline := `{"type":"mekugi_usage_baseline","session_id":"fork-session","modelUsage":{"native-model":{"inputTokens":200,"cacheReadInputTokens":400,"cacheCreationInputTokens":100,"outputTokens":40,"thinkingTokens":10}},"total_cost_usd":0.5}`
		if err := fork.ObserveMetrics(t.Context(), jsontext.Value(baseline)); err != nil {
			t.Fatal(err)
		}
		fork.ledger.Metrics.Native = runtime.ledger.Metrics.Native
		_, forkReport := fork.Metrics()
		if forkReport.InputTokens != 0 || forkReport.OutputTokens != 0 || forkReport.Cost.Total() != 0 {
			t.Fatalf("fork inherited source consumption: %+v", forkReport)
		}
	})
}

func TestClaudeReportedUsageRejectsMissingAndInvalidCounts(t *testing.T) {
	for _, model := range []string{
		`{}`, `{"m":{"inputTokens":null,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}`,
		`{"m":{"inputTokens":-1,"outputTokens":1,"cacheReadInputTokens":0,"cacheCreationInputTokens":0}}`,
		`{"m":{"inputTokens":18446744073709551615,"outputTokens":1,"cacheReadInputTokens":1,"cacheCreationInputTokens":0}}`,
	} {
		if _, err := claudeReportedUsage(jsontext.Value(`{"modelUsage":` + model + `}`)); err == nil {
			t.Fatalf("invalid native usage claimed complete totals: %s", model)
		}
	}
}
