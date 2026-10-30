package launch

import (
	"encoding/json/jsontext"
	"fmt"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffmodel"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestClaudePublicationPreservesUnchangedReview(t *testing.T) {
	f := newClaudeHookFixture(t)
	b := f.bridge
	var chunks []changes.Chunk
	for i := range 64 {
		path := fmt.Sprintf("file-%d", i)
		chunks = append(chunks, changes.Chunk{Key: path, CaptureOrder: uint64(i + 1), Workspace: b.settings.Workspace,
			Change: path, Caller: "/root/child", Review: diffmodel.RenderReviewFile(path, path, "old\n", "new\n")})
	}
	review := &changes.Snapshot{Publication: (changes.Publication{}).With(changes.GroupCaptures(chunks))}
	publication := claudePublication{frame: sessionapi.Frame{Session: sessionapi.Session{Thread: b.info.Thread, Workspace: b.settings.Workspace}}}
	root, family := b.execution.shared.Family("parent")
	member, _ := b.execution.shared.Member(b.info.Thread)
	metrics := activity.AgentFacts{}
	if !publication.update(root, family, member, review, metrics) {
		t.Fatal("initial publication missing")
	}
	api := sessionapi.NewLive()
	if err := api.Publish(publication.frame); err != nil {
		t.Fatal(err)
	}
	first := publication.frame
	for range 32 {
		if err := b.event(t.Context(), jsontext.Value(`{"type":"system","subtype":"status","session_id":"native-session"}`)); err != nil {
			t.Fatal(err)
		}
		root, family = b.execution.shared.Family("parent")
		member, _ = b.execution.shared.Member(b.info.Thread)
		if publication.update(root, family, member, review, metrics) {
			t.Fatal("ignored native event replaced the published frame")
		}
	}
	if publication.frame.Review != first.Review || publication.frame.Agents != first.Agents {
		t.Fatal("unchanged native events invalidated review or roster identity")
	}
	if allocations := testing.AllocsPerRun(20, func() { publication.update(root, family, member, review, metrics) }); allocations != 0 {
		t.Fatalf("unchanged publication materialized retained history: %.0f allocations", allocations)
	}
	member.Status, member.HostRunning = control.Waiting, false
	if !publication.update(root, family, member, review, metrics) || publication.frame.Status.Text != "waiting" || publication.frame.Review != first.Review || publication.frame.Agents != first.Agents {
		t.Fatal("lifecycle update lost progress or invalidated unrelated publications")
	}
	replacement := *review
	if !publication.update(root, family, member, &replacement, metrics) || publication.frame.Review.Revision != first.Review.Revision+1 {
		t.Fatal("changed capture did not advance its review revision")
	}
	if err := api.Publish(publication.frame); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeNativeMetricsReachCanonicalRoster(t *testing.T) {
	f := newClaudeHookFixture(t)
	b := f.bridge
	b.owners.Usage.MarkNew(b.info.Thread)
	f.mu.Lock()
	uuid := f.inputs[0].UUID
	f.mu.Unlock()
	for _, raw := range []string{
		`{"type":"assistant","session_id":"native-session","uuid":"block-1","message":{"id":"msg-1"},"parent_tool_use_id":null}`,
		`{"type":"assistant","session_id":"native-session","uuid":"block-2","message":{"id":"msg-1"},"parent_tool_use_id":null}`,
		`{"type":"mekugi_live_metrics","session_id":"native-session","message_id":"msg-1","context_tokens":null,"context_window":null,"modelUsage":null,"total_cost_usd":null}`,
		`{"type":"mekugi_live_metrics","session_id":"native-session","message_id":"msg-1","context_tokens":41000,"context_window":200000,"modelUsage":{"native-model":{"inputTokens":10000,"cacheReadInputTokens":20000,"cacheCreationInputTokens":5000,"outputTokens":2000}},"total_cost_usd":0.25}`,
	} {
		if err := b.event(t.Context(), jsontext.Value(raw)); err != nil {
			t.Fatal(err)
		}
	}
	earlyFacts, earlyReport := b.runtime.Metrics()
	earlyAgent := sessionpresentation.ActivityAgent{}
	observeCost(b.owners.Usage, b.info.Thread, &earlyAgent)
	earlyUsage := earlyAgent.ActivityAgent()
	if earlyReport.Roundtrips != 1 || !earlyFacts.ContextKnown || earlyFacts.ContextTokens != 41000 || earlyFacts.ContextWindow != 200000 || earlyUsage.InputTokens != 35000 || earlyUsage.OutputTokens != 2000 || !earlyUsage.TokensKnown || !earlyUsage.CostKnown || earlyUsage.Cost != .25 || earlyUsage.Roundtrips != 1 {
		t.Fatalf("metrics unavailable before native terminal settlement: %+v %+v %+v", earlyFacts, earlyReport, earlyUsage)
	}
	for _, raw := range []string{
		`{"type":"system","subtype":"init","session_id":"native-session","model":"native-model"}`,
		fmt.Sprintf(`{"type":"result","subtype":"success","session_id":"native-session","uuid":"result-1","user_message_uuid":%q,"num_turns":3,"result":"done","modelUsage":{"native-model":{"inputTokens":10000,"cacheReadInputTokens":20000,"cacheCreationInputTokens":5000,"outputTokens":2000}},"total_cost_usd":0.25}`, uuid),
		`{"type":"mekugi_context_usage","session_id":"native-session","result_uuid":"result-1","context_tokens":42000,"context_window":200000}`,
	} {
		if err := b.event(t.Context(), jsontext.Value(raw)); err != nil {
			t.Fatal(err)
		}
	}
	facts, _ := b.runtime.Metrics()
	agent := sessionpresentation.ActivityAgent{}
	observeCost(b.owners.Usage, b.info.Thread, &agent)
	usage := agent.ActivityAgent()
	facts.InputTokens, facts.OutputTokens, facts.TokensKnown = usage.InputTokens, usage.OutputTokens, usage.TokensKnown
	facts.Cost, facts.CostKnown, facts.Roundtrips = usage.Cost, usage.CostKnown, usage.Roundtrips
	root, family := b.execution.shared.Family("parent")
	member, _ := b.execution.shared.Member(b.info.Thread)
	publication := claudePublication{frame: sessionapi.Frame{Session: sessionapi.Session{Thread: b.info.Thread}}}
	publication.update(root, family, member, nil, facts)
	view := activityui.NewView()
	view.UpdateRoster(publication.frame.Agents)
	row := view.Roster()[0].Agent
	if row.InputTokens != 35000 || row.OutputTokens != 2000 || !row.TokensKnown || !row.CostKnown || row.Cost != .25 || row.Roundtrips != 3 || row.Turns != 1 || !row.ContextKnown || row.ContextTokens != 42000 || row.ContextWindow != 200000 || !row.WorkKnown || row.Started.IsZero() || row.LastResponse.IsZero() || !row.WorkSince.IsZero() || row.WorkElapsed < 0 || row.WorkElapsed > time.Minute {
		t.Fatalf("native measurements did not reach roster: %+v", row)
	}
	if activityui.Cost(row) != "$0.25" || ansi.Strip(activityui.Turns(row)) != "T+3" || !row.NonAttachable {
		t.Fatalf("native metrics or retained capability rendered incorrectly: %+v", row)
	}
	if _, known := row.OutputThroughput.Rate(); known {
		t.Fatal("aggregate SDK duration invented provider throughput")
	}
	first := publication.frame.Agents
	facts.ContextTokens++
	if !publication.update(root, family, member, nil, facts) || publication.frame.Agents == first || first.Roster[0].Agent.ContextTokens != 42000 {
		t.Fatal("metric update failed to replace a detached roster publication")
	}
}
