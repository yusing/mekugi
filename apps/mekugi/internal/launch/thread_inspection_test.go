package launch

import (
	json "encoding/json/v2"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestThreadInspectionUsesLiveAndRestoredSessionMetrics(t *testing.T) {
	u := newAppServerSessionTestUI(t, t.TempDir())
	appServerTestRequest(t, u, "1", "thread/start")
	appServerTestMessage(t, u, `{"id":1,"result":{"model":"applied","reasoningEffort":"high","serviceTier":"fast","thread":{"id":"main"}}}`)
	applied := preferences.Applied{Model: "applied", Effort: "high", Tier: "fast"}
	tiers := inferencegateway.NewTiers(nil)
	read := func() {
		t.Helper()
		got := threadInspection(&u.attached.session, "main", applied, tiers)
		if (got.Model == nil || *got.Model != "applied") || *got.ReasoningEffort != "high" || *got.ServiceTier != "priority" || got.Compactions == nil {
			t.Fatalf("applied snapshot = %+v", got)
		}
	}
	read()
	initial := threadInspection(&u.attached.session, "main", applied, tiers)
	if initial.ContextTokens != nil || initial.Compactions == nil || *initial.Compactions != 0 {
		t.Fatalf("fresh metrics = %+v", initial)
	}
	checkWire := func(want string) {
		t.Helper()
		data, err := json.Marshal(threadInspection(&u.attached.session, "main", applied, tiers).SessionMetrics)
		if err != nil || string(data) != want {
			t.Fatalf("metrics = %s, err=%v, want %s", data, err, want)
		}
	}
	checkWire(`{"context_tokens":null,"context_window":null,"compactions":0}`)
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "main", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 0}}})
	checkWire(`{"context_tokens":0,"context_window":null,"compactions":0}`)
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "main", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 1234}, "modelContextWindow": 10000}})
	for _, item := range []string{"compact-1", "compact-1", "compact-2"} {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{"id": item, "type": "contextCompaction"}})
	}
	got := threadInspection(&u.attached.session, "main", applied, tiers)
	if got.ContextTokens == nil || *got.ContextTokens != 1234 || got.ContextWindow == nil || *got.ContextWindow != 10000 || got.Compactions == nil || *got.Compactions != 2 {
		t.Fatalf("live metrics = %+v", got)
	}
	// Full native history on resume/fork supplies the same baseline; replaying it
	// must not double-count already observed native completion identities.
	info := binding.ThreadInfo{ID: "main", Turns: []binding.HistoryTurn{{Items: []binding.ThreadItem{{Type: "contextCompaction", ID: "compact-1"}, {Type: "contextCompaction", ID: "compact-2"}, {Type: "contextCompaction", ID: "pending", Status: "inProgress"}}}}}
	restoreCompactionMetrics(&u.attached.session, info)
	if got := u.attached.session.InspectionMetrics("main"); got.Compactions == nil || *got.Compactions != 2 {
		t.Fatalf("restored metrics = %+v", got)
	}
	if got := u.attached.session.InspectionMetrics("foreign"); got.ContextTokens != nil || got.Compactions != nil {
		t.Fatalf("foreign metrics = %+v", got)
	}
	var restored sessionpresentation.Attachment
	restored.Start("main")
	restoreCompactionMetrics(&restored, info)
	if got := restored.InspectionMetrics("main"); got.Compactions == nil || *got.Compactions != 2 || got.ContextTokens != nil {
		t.Fatalf("fresh restored metrics = %+v", got)
	}
	u.attached.session.Retire()
	if got := u.attached.session.InspectionMetrics("main"); got.ContextTokens != nil || got.Compactions != nil {
		t.Fatalf("retired metrics = %+v", got)
	}
}
