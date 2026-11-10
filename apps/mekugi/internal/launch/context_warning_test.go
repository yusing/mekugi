package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/responses"
	modelresponses "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// Exercise the admitted request's milestone and invocation notice queue together.
func queueContextWarning(t *testing.T, issues *requestexecution.Failures, thread string) {
	t.Helper()
	tokens, window := uint64(600), uint64(1000)
	preparer := executionobservation.New(executionobservation.Config{
		Registry: sharedProxyTestRegistry(t), Control: new(sessioncontrol.Observations), Event: issues.AddThreadEvent,
		Inspect: func(context.Context, string) (control.SessionDetails, error) {
			return control.SessionDetails{ContextTokens: &tokens, ContextWindow: &window}, nil
		},
	})
	request := &responses.Request{Fields: map[string]jsontext.Value{"input": jsontext.Value(`[{"role":"user","content":"continue"}]`)}}
	prepared, err := preparer.Prepare(t.Context(), request, requestexecution.Preparation{Session: t.Name(), Thread: thread, MetadataValid: true, Metadata: responses.TurnMetadata{RequestKind: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared != nil {
		prepared.Close()
	}
}

func TestContextUrgentInstructionPreparedRequest(t *testing.T) {
	var tokens, compactions uint64
	window := uint64(1000)
	var events []string
	preparer := executionobservation.New(executionobservation.Config{
		Registry: sharedProxyTestRegistry(t), Control: new(sessioncontrol.Observations),
		Event: func(_, _, _, text string) { events = append(events, text) },
		Inspect: func(context.Context, string) (control.SessionDetails, error) {
			return control.SessionDetails{ContextTokens: &tokens, ContextWindow: &window, Compactions: &compactions}, nil
		},
	})
	for _, test := range []struct {
		name                string
		tokens, compactions uint64
		input, want         string
	}{
		{"below", 899, 0, `[{"role":"user","content":"continue"}]`, "Mekugi context usage: 89% (899/1000 tokens)."},
		{"threshold", 900, 0, `[{"type":"function_call_output","call_id":"call","output":"done"}]`, "Mekugi context usage: 90% (900/1000 tokens). You must compact now."},
		{"already emitted", 950, 0, `[{"type":"function_call_output","call_id":"next","output":"done"}]`, ""},
		{"reset", 0, 1, `[{"role":"user","content":"after compaction"}]`, ""},
		{"above after reset", 950, 1, `[{"role":"user","content":"continue after compaction"}]`, "Mekugi context usage: 95% (950/1000 tokens). You must compact now."},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokens, compactions = test.tokens, test.compactions
			before := len(events)
			request := &responses.Request{Fields: map[string]jsontext.Value{"input": jsontext.Value(test.input)}}
			prepared, err := preparer.Prepare(t.Context(), request, requestexecution.Preparation{Session: "session", Thread: "main", MetadataValid: true, Metadata: responses.TurnMetadata{RequestKind: "turn"}})
			if err != nil {
				t.Fatal(err)
			}
			if prepared != nil {
				prepared.Close()
			}
			var items []map[string]jsontext.Value
			if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
				t.Fatal(err)
			}
			items = slices.DeleteFunc(items, func(item map[string]jsontext.Value) bool {
				return strings.HasPrefix(modelresponses.StringField(item, "content"), "<mekugi_environment_facts>")
			})
			if test.want == "" {
				if len(items) != 1 || len(events) != before {
					t.Fatalf("unexpected repeated milestone: input=%s events=%v", request.Fields["input"], events[before:])
				}
				return
			}
			if len(items) != 2 || modelresponses.StringField(items[1], "role") != "developer" || modelresponses.StringField(items[1], "content") != "<system-reminder>\n"+test.want+"\n</system-reminder>" {
				t.Fatalf("model-facing milestone: %s", request.Fields["input"])
			}
			if !slices.Equal(events[before:], []string{test.want}) {
				t.Fatalf("progress differs from model instruction: %v", events[before:])
			}
		})
	}
}

func TestUISnapshotContextEventReachesTerminal(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.Client.Thread = t.Name()
	queueContextWarning(t, u.issues, u.Client.Thread)
	delivery := applyCriticalNotices(&u.attached, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, u.audience, u.issues)
	if delivery == nil || u.attached.feedback.Text != "" || len(u.attached.source.Entries) != 1 {
		t.Fatalf("context event missing from the transcript or shown as composer feedback: %+v", u.attached.feedback)
	}
	if event := u.attached.source.Entries[0].Source; event.Kind != "progress" || event.Text != "Mekugi context usage: 60% (600/1000 tokens)." || len(delivery.entries) != 1 || delivery.entries[0] != event.Seq {
		t.Fatalf("context event row = %+v, claimed rows = %v", event, delivery.entries)
	}
	// The server owner mirrors and releases claims. Another view of the shared
	// transcript then retries the event and names its existing row.
	row := delivery.entries[0]
	delivery.finish(terminalsession.NoticeReceipt{})
	view := u.attached
	view.feedback = noticepresentation.Model{}
	delivery = applyCriticalNotices(&view, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, u.title, u.backendVersion, &u.restoring, &u.side, u.audience, u.issues)
	if delivery == nil || len(u.attached.source.Entries) != 1 || !slices.Equal(delivery.entries, []uint64{row}) {
		t.Fatalf("retried context event duplicated its row: %d rows, claimed %v", len(u.attached.source.Entries), delivery.entries)
	}
	defer terminalTest(u).Diff.Close()
	if err := u.testPaint(io.Discard, 100, 16); err != nil {
		t.Fatal(err)
	}
	if !terminalTest(u).Frame.Notices.Content {
		t.Fatal("context event was not visibly painted")
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/context-event-terminal.txt", append(slices.Clone(terminalTest(u).Frame.Rows), "plain after event"), 100)
	delivery.finish(terminalTest(u).Frame.Notices)
	if pending := u.issues.Notices.Pending(); len(pending) != 0 {
		t.Fatalf("painted warning remained pending: %v", pending)
	}
}

func TestNativeContextEventReceiptRequiresItsVisibleRow(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	queueContextWarning(t, u.issues, u.Thread)
	claim := func() *nativeCriticalNoticeDelivery {
		return applyCriticalNotices(&u.attached, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, "", "", &u.restoring, &u.side, u.audience, u.issues)
	}
	delivery := claim()
	delivery.finish(terminalsession.NoticeReceipt{})
	for range 30 {
		u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: u.attached.source.LastSeq + 1, Agent: "Main", Kind: "final", Text: "Later transcript content"}}})
	}
	u.issues.AddThreadEvent("", u.Thread, "second-event", "second context event")
	u.issues.AddThreadNotice("", u.Thread, "failure", "actionable failure")
	delivery = claim()
	c := nativeTestPromptClient(u)
	defer c.renderer.terminal.Diff.Close()
	c.renderer.terminal.Width, c.renderer.terminal.Height = 100, 16
	paint := func() terminalsession.NoticeReceipt {
		t.Helper()
		c.renderer.frame.NoticeEntries = delivery.entries
		c.renderer.frame.Notice = delivery.id
		c.renderer.frame.NoticeFeedback = 0
		if len(delivery.errors.Messages) > 0 {
			c.renderer.frame.NoticeFeedback = delivery.feedback
		}
		frame, err := c.renderer.render(t.Context(), io.Discard, presentationTime(u.clock), "test")
		if err != nil {
			t.Fatal(err)
		}
		return frame.Notices
	}
	view := &nativeSessionView{notices: delivery}
	settle := func(receipt terminalsession.NoticeReceipt) {
		t.Helper()
		c.stageReceipt(&sessionapi.NativeNoticeReceipt{Claim: delivery.id, Attachment: receipt.Attachment, Feedback: receipt.Feedback, Composer: receipt.Composer, Content: receipt.Content})
		for _, call := range c.calls {
			if call.operation.Receipt != nil {
				if refusal := view.applyView(call.operation); refusal != "" {
					t.Fatal(refusal)
				}
			}
		}
		c.calls = nil
	}
	receipt := paint()
	if !receipt.Composer || receipt.Content {
		t.Fatalf("scrolled-away event acknowledged with visible composer: %+v", receipt)
	}
	settle(receipt)
	if pending := u.issues.Notices.Pending(); len(pending) != 2 || !strings.Contains(pending[0], "context usage") {
		t.Fatalf("independent error settlement lost pending event: %v", pending)
	}
	delivery = claim()
	if delivery == nil || len(delivery.errors.Messages) != 0 || len(u.attached.source.Entries) != 32 {
		t.Fatal("retry duplicated the event or reclaimed the delivered error")
	}
	view.notices = delivery
	c.renderer.terminal.Transcript.Following, c.renderer.terminal.Transcript.Offset = false, 0
	receipt = paint()
	if receipt.Content {
		t.Fatal("painting only the first event acknowledged both rows")
	}
	// The production loop stages every paint. An incomplete notice must not
	// release the claim, even when an applied effect needs acknowledgement.
	view.effects.add(sessionapi.NativeEffect{Clipboard: new("applied effect")})
	c.effect = view.effects.next
	settle(receipt)
	if view.notices != delivery || len(view.effects.pending) != 0 {
		t.Fatal("incomplete event receipt released its claim or blocked an effect")
	}
	c.renderer.terminal.Transcript.Following = true
	receipt = paint()
	if !receipt.Content {
		t.Fatalf("visible event has no receipt: %+v, spans=%v", receipt, c.renderer.terminal.Transcript.FeedSpans)
	}
	settle(receipt)
	if pending := u.issues.Notices.Pending(); len(pending) != 0 {
		t.Fatal(pending)
	}
}

func TestNativeContextEventSurvivesFeedTrim(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	queueContextWarning(t, u.issues, u.Thread)
	delivery := applyCriticalNotices(&u.attached, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, "", "", &u.restoring, &u.side, u.audience, u.issues)
	original := delivery.entries[0]
	entries := make([]sessionpresentation.ActivityEntry, activitypresentation.FeedLimit)
	for i := range entries {
		entries[i] = sessionpresentation.ActivityEntry{Seq: u.attached.source.LastSeq + 1 + uint64(i), Agent: "Main", Kind: "final", Text: "Later transcript content"}
	}
	u.attached.source.Apply(sessionpresentation.ActivityEvent{Entries: entries})
	// Publication refreshes even a held claim when its old row ages out.
	delivery.refreshProgress(&u.attached, presentationTime(u.clock))
	if delivery.entries[0] == original || !slices.ContainsFunc(u.attached.source.Entries, func(row sessionpresentation.ActivityRecord) bool { return row.Source.Seq == delivery.entries[0] }) {
		t.Fatal("pending event still names a trimmed row")
	}
	rows := slices.Clone(delivery.entries)
	delivery.finish(terminalsession.NoticeReceipt{})
	delivery = applyCriticalNotices(&u.attached, &u.Client, &u.picker, &u.Editor, u.clock, preferences.Applied{}, u.serviceTiers, "", "", &u.restoring, &u.side, u.audience, u.issues)
	if !slices.Equal(rows, delivery.entries) {
		t.Fatal("retry duplicated a surviving replacement row")
	}
	c := nativeTestPromptClient(u)
	defer c.renderer.terminal.Diff.Close()
	c.renderer.terminal.Width, c.renderer.terminal.Height = 100, 16
	c.renderer.frame.Notice, c.renderer.frame.NoticeEntries = delivery.id, delivery.entries
	frame, err := c.renderer.render(t.Context(), io.Discard, presentationTime(u.clock), "test")
	if err != nil || !frame.Notices.Content {
		t.Fatalf("replacement event was not painted: %+v, %v", frame.Notices, err)
	}
	delivery.finish(frame.Notices)
	if pending := u.issues.Notices.Pending(); len(pending) != 0 {
		t.Fatal(pending)
	}
}
