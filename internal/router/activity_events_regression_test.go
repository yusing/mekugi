package router

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestActivityCollectsReasoningWithoutRenderingOrChangingProviderEvents(t *testing.T) {
	p := newManagedMekugiProxy(t)
	p.activity.attachPane(newActivityPane(t.Context(), func() bool { return true }))
	root, _ := prepareActivityTest(t, p, "root", "r", "", "/root", nil)
	v := newLiveActivityView()
	for i, delta := range []string{"Checking ", "the event path."} {
		payload := mustTestJSON(t, map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "reasoning-1", "delta": delta})
		out, err := root.TransformSSE(payload)
		if err != nil || len(out) != 1 || !bytes.Equal(out[0], payload) {
			t.Fatalf("provider event changed: %q, %v", out, err)
		}
		p.activity.mu.Lock()
		if len(p.activity.events) != 1 {
			p.activity.mu.Unlock()
			t.Fatal("reasoning snapshots duplicated")
		}
		event := p.activity.events[0]
		p.activity.mu.Unlock()
		v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: uint64(i + 1), Agent: "/root", Kind: event.kind, CallID: event.callID, Text: event.raw, Observed: time.Now()}}})
	}
	if len(v.entries) != 0 {
		t.Fatalf("reasoning entered activity history: %+v", v.entries)
	}
	frame := strings.Join(plainLines(v.renderFeed(80, 15).lines), "\n")
	if strings.Contains(frame, "Thinking") || strings.Contains(frame, "Checking the event path.") {
		t.Fatal(frame)
	}
	root.collectReasoningDelta("reasoning-1", strings.Repeat("x", maxCommentaryPublicationBytes))
	if root.activityReasoningBytes > maxCommentaryPublicationBytes {
		t.Fatal("unbounded reasoning")
	}
	raw := mustTestJSON(t, map[string]any{"type": "response.reasoning_text.delta", "item_id": "raw", "delta": "not a visible summary"})
	if _, err := root.TransformSSE(raw); err != nil {
		t.Fatal(err)
	}
	if len(root.activityReasoning) != 1 {
		t.Fatal("raw reasoning exposed")
	}
	p.activity.releasePane()
	for _, message := range p.activity.drain("r", time.Time{}, maxCommentaryPublicationBytes) {
		if strings.Contains(commentaryText(t, message), "Checking") {
			t.Fatal("reasoning leaked inline")
		}
	}
}

func TestActivityWaitAndHostedMCPPreviews(t *testing.T) {
	p := newManagedMekugiProxy(t)
	p.activity.attachPane(newActivityPane(t.Context(), func() bool { return true }))
	root, _ := prepareActivityTest(t, p, "root", "r", "", "/root", nil)
	for _, tc := range []struct{ payload, want string }{
		{`{"type":"function_call","namespace":"mekugi_collaboration","name":"wait_agent","arguments":"{}"}`, "Waiting for agent"},
		{`{"type":"function_call","namespace":"collaboration","name":"thread_wait","arguments":"{}"}`, "Waiting for agent"},
		{`{"type":"mcp_call","name":"lookup","server_label":"docs","arguments":"{}"}`, "MCP `docs.lookup`"},
		{`{"type":"mcp_call","name":"wait_agent","server_label":"jobs","arguments":"{}"}`, "MCP `jobs.wait_agent`"},
		{`{"type":"function_call","name":"request_user_input","namespace":"mcp__jobs","arguments":"{}"}`, "MCP `jobs.request_user_input`"},
		{`{"type":"mcp_list_tools","server_label":"docs"}`, "List MCP tools"},
		{`{"type":"mcp_approval_request","server_label":"docs","name":"lookup","arguments":"{}"}`, "MCP approval `docs.lookup`"},
	} {
		item, ok := decodeResponsesItem([]byte(tc.payload))
		if !ok {
			t.Fatal(tc.payload)
		}
		got := subagentToolPreview(item.fields, qualifiedToolName(item.Namespace, item.Name), nil)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%s: got %q want %q", tc.payload, got, tc.want)
		}
		item.fields["id"] = mustTestJSON(t, tc.payload)
		root.collectSubagentToolCall(item.fields)
		if len(p.activity.events) == 0 || !strings.Contains(p.activity.events[len(p.activity.events)-1].raw, tc.want) {
			t.Fatalf("collector lost call identity: %s", tc.payload)
		}
	}
}

func TestActivityRoleGlyphAndResponsiveLegend(t *testing.T) {
	v := newLiveActivityView()
	v.agents = []activityPaneAgent{{Name: "/root/a", Role: "explorer", Responding: true}, {Name: "/root/b", Role: "worker", Responding: true}}
	a, b := v.glyph(v.agents[0]), v.glyph(v.agents[1])
	if a == b || ansi.Strip(a) != "◐" || ansi.StringWidth(a) != 1 {
		t.Fatalf("roles lack unique zero-extra-cell styles: %q %q", a, b)
	}
	u := &terminalUI{width: 200, activityOpen: true, agents: v}
	status, legend := u.statusLines()
	if legend != "" || !strings.Contains(status, "explorer") || !strings.Contains(status, "worker") {
		t.Fatalf("wide legend: %q / %q", status, legend)
	}
	u.width = 80
	status, legend = u.statusLines()
	if legend == "" || strings.Contains(status, "explorer") || !strings.Contains(legend, "worker") {
		t.Fatalf("narrow legend: %q / %q", status, legend)
	}
	if v.glyph(v.agents[0]) != a {
		t.Fatal("role style shifted")
	}
	if strings.Contains(strings.Join(v.metricTable(v.roster(), time.Now()), ""), "explorer") {
		t.Fatal("role still takes metric space")
	}
}
