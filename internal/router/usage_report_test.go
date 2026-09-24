package router

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"io"
	"strconv"
	"strings"
	"testing"
)

func marshalUsageReportFixture(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestUsageReportFlagParsing(t *testing.T) {
	flags := newRouterFlags(io.Discard)
	if *flags.usageReport != "" {
		t.Fatalf("default usage-report = %q, want automatic selection", *flags.usageReport)
	}
	if err := flags.Parse(nil); err != nil || *flags.usageReport != "" {
		t.Fatalf("default parse: value=%q err=%v", *flags.usageReport, err)
	}
	for _, value := range []string{"off", "compact", "table"} {
		t.Run(value, func(t *testing.T) {
			flags := newRouterFlags(io.Discard)
			if err := flags.Parse([]string{"--usage-report=" + value}); err != nil {
				t.Fatal(err)
			}
			if *flags.usageReport != value {
				t.Fatalf("usage-report = %q, want %q", *flags.usageReport, value)
			}
		})
	}
	flags = newRouterFlags(io.Discard)
	if err := flags.Parse([]string{"--usage-report", "table"}); err != nil || *flags.usageReport != "table" {
		t.Fatalf("separate flag operand: value=%q err=%v", *flags.usageReport, err)
	}
	flags = newRouterFlags(io.Discard)
	if err := flags.Parse([]string{"--usage-report=verbose"}); err == nil || err.Error() != `invalid value "verbose" for flag -usage-report: --usage-report must be off, compact, or table` {
		t.Fatalf("invalid usage-report error = %v", err)
	}
}

func TestUsageReportFormatsExactOutput(t *testing.T) {
	totalCounts := tokenCounts{InputTokens: 100_000, UncachedInputTokens: 40_000, OutputTokens: 30_000, ReasoningTokens: 20_000}
	total := tokenUsageReport{
		tokenCounts: totalCounts,
		cost:        estimateTokenCost("gpt-6-astra", "", totalCounts),
		model:       "gpt-6-astra",
	}
	turnCounts := tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 25}
	turn := tokenUsageReport{
		tokenCounts: turnCounts,
		cost:        tokenCost{uncachedInput: 0.0001, output: 0.0002, known: true},
	}
	total.layout = "compact"
	total.turn = &turn
	if got, want := formatUsageReport(total), "Router session usage · Main turn: 100 in / 25 out, $0.0003 · Total: 100K in / 30K out, $1.9600"; got != want {
		t.Fatalf("compact report = %q, want %q", got, want)
	}

	total.layout = "table"
	const wantTable = "Router session usage\n\n" +
		"| Agent | Role | Model | Input (cache hit) | Cache write | Output | Reasoning | Input cost (cached + uncached) | Output cost | Total cost | Missing usage |\n" +
		"| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n" +
		"| /root | main | gpt-6-astra | 100K (60.0%) | 0 | 30K | 20K | $0.0600+$0.4000=$0.4600 | $1.5000 | $1.9600 | 0 |\n" +
		"| Total | — | — | 100K (60.0%) | 0 | 30K | 20K | $0.0600+$0.4000=$0.4600 | $1.5000 | $1.9600 | 0 |\n" +
		"\nRouter session API estimates since router startup; reasoning is included in output, and cache writes are included in input."
	if got := formatUsageReport(total); got != wantTable {
		t.Fatalf("table report:\n%s\nwant:\n%s", got, wantTable)
	}

	total.layout = "off"
	if got := formatUsageReport(total); got != "" {
		t.Fatalf("off report = %q, want empty", got)
	}
}

func TestUsageReportDefaultLayoutAndTurnAggregation(t *testing.T) {
	t.Run("single agent defaults to compact", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t)
		root, _ := prepareActivityTest(t, proxy, "session", "root", "", "/root", nil)
		root.usageTracker = proxy.usage.observationForTurn("root", "root", "turn-a", "gpt-6-sol", "")
		root.observeResponseUsage(tokenCounts{InputTokens: 10, UncachedInputTokens: 10, OutputTokens: 2})
		report, ok := root.completionUsageReport()
		if !ok || report.layout != "compact" || report.turn == nil || report.turn.InputTokens != 10 {
			t.Fatalf("single-agent report = %+v, ok=%v", report, ok)
		}
		if got, want := formatUsageReport(report), "Router session usage · Main turn: 10 in / 2 out, $0.0000 · Total: 10 in / 2 out, $0.0000"; got != want {
			t.Fatalf("default report = %q, want %q", got, want)
		}
	})

	t.Run("main turn excludes child and interleaved turns", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t)
		root, _ := prepareActivityTest(t, proxy, "root-session", "root", "", "/root", nil)
		child, _ := prepareActivityTest(t, proxy, "child-session", "child", "root", "/root/worker", nil)

		firstMainRequest := proxy.usage.observationForTurn("root", "root", "turn-a", "gpt-6-sol", "")
		firstMainRequest.observe(tokenCounts{InputTokens: 100, UncachedInputTokens: 100})
		firstMainRequest.observe(tokenCounts{InputTokens: 999}) // One response observation counts once.
		proxy.usage.observationForTurn("root", "root", "turn-b", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 30, UncachedInputTokens: 30})
		child.usageTracker = proxy.usage.observationForTurn("child", "child", "turn-a", "gpt-6-sol", "")
		child.observeResponseUsage(tokenCounts{InputTokens: 1_000, UncachedInputTokens: 1_000})
		proxy.usage.observationForTurn("root", "root", "turn-a", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 200, UncachedInputTokens: 200})

		root.usageTracker = proxy.usage.observationForTurn("root", "root", "turn-a", "gpt-6-sol", "")
		report, ok := root.completionUsageReport()
		if !ok || report.layout != "table" || report.turn == nil || report.turn.InputTokens != 300 {
			t.Fatalf("multi-agent report = %+v, ok=%v", report, ok)
		}
		if report.InputTokens != 1_330 || report.rows == nil || len(*report.rows) != 2 || (*report.rows)[0].report.InputTokens != 330 || (*report.rows)[1].report.InputTokens != 1_000 {
			t.Fatalf("router-session total did not include the complete root and child totals: %+v", report)
		}

		laterRoot, _ := prepareActivityTest(t, proxy, "remapped-session", "root", "", "/root", nil)
		laterRoot.usageTracker = proxy.usage.observationForTurn("root", "root", "turn-c", "gpt-6-sol", "")
		laterRoot.observeResponseUsage(tokenCounts{InputTokens: 50, UncachedInputTokens: 50})
		later, ok := laterRoot.completionUsageReport()
		if !ok || later.InputTokens != 1_380 || later.turn == nil || later.turn.InputTokens != 50 {
			t.Fatalf("router-lifetime/session-remapped aggregate = %+v, ok=%v", later, ok)
		}
	})
}

func TestUsageReportExplicitLayoutsOverrideAutomaticSelection(t *testing.T) {
	for _, layout := range []string{"compact", "table", "off"} {
		t.Run(layout, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.usageReport = layout
			root, _ := prepareActivityTest(t, proxy, "session", "root", "", "/root", nil)
			root.usageTracker = proxy.usage.observationForTurn("root", "root", "turn-explicit", "gpt-6-sol", "")
			root.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
			report, ok := root.completionUsageReport()
			if layout == "off" {
				if ok {
					t.Fatalf("off mode produced a report: %+v", report)
				}
				return
			}
			if !ok || report.layout != layout {
				t.Fatalf("explicit layout %q yielded %+v, ok=%v", layout, report, ok)
			}
			text := formatUsageReport(report)
			if layout == "compact" && !strings.HasPrefix(text, "Router session usage · Main turn: ") {
				t.Fatalf("explicit compact selection rendered as %q", text)
			}
			if layout == "table" && !strings.HasPrefix(text, "Router session usage\n\n| Agent | Role | Model | ") {
				t.Fatalf("explicit table selection rendered as %q", text)
			}
		})
	}
}

func TestUsageReportMissingTurnMetadataAndCapacityDoNotRevive(t *testing.T) {
	totals := newThreadUsage()
	totals.observation("root", "root", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 123, UncachedInputTokens: 123})
	if _, ok := totals.turnSnapshot("root", ""); ok {
		t.Fatal("usage without canonical TurnID was assigned to a turn")
	}
	transform := &mekugiResponseTransform{
		ctx:          t.Context(),
		proxy:        &mekugiProxy{usage: totals},
		threadID:     "root",
		usageTracker: totals.observation("root", "root", "gpt-6-sol", ""),
	}
	report, ok := transform.completionUsageReport()
	if !ok || report.turn == nil || !report.turn.Incomplete || !strings.Contains(formatUsageReport(report), "Main turn: n/a (usage incomplete)") {
		t.Fatalf("missing TurnID was guessed from lifetime usage: %+v, ok=%v", report, ok)
	}

	bounded := newThreadUsage()
	for i := range maxTrackedUsageTurns {
		bounded.observationForTurn("root", "root", "turn-"+strconv.Itoa(i), "gpt-6-sol", "").observe(tokenCounts{InputTokens: 1, UncachedInputTokens: 1})
	}
	bounded.observationForTurn("root", "root", "overflow", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 100, UncachedInputTokens: 100})
	bounded.observationForTurn("root", "root", "overflow", "gpt-6-sol", "").observe(tokenCounts{InputTokens: 200, UncachedInputTokens: 200})
	if len(bounded.turns) != maxTrackedUsageTurns {
		t.Fatalf("tracked turns = %d, want fixed capacity %d", len(bounded.turns), maxTrackedUsageTurns)
	}
	if snapshot, ok := bounded.turnSnapshot("root", "turn-0"); !ok || snapshot.InputTokens != 1 {
		t.Fatalf("capacity evicted an established turn: %+v, ok=%v", snapshot, ok)
	}
	if _, ok := bounded.turnSnapshot("root", "overflow"); ok {
		t.Fatal("capacity overflow became a partial, apparently complete turn")
	}
}

func TestUsageTurnAggregationUsesCanonicalMetadataAcrossJournalContinuation(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	workspace := t.TempDir()
	const thread = "host-thread"
	prepare := func(ctx context.Context, turnID, session string) *mekugiResponseTransform {
		t.Helper()
		request := serverRequest(t, func(fields map[string]any) { fields["model"] = "gpt-6-sol" })
		metadata := codexTurnMetadata{
			RequestKind: "turn", ThreadID: thread, TurnID: turnID,
			Directories: map[string]jsonv1.RawMessage{workspace: nil},
		}
		transform, err := proxy.prepareRequest(ctx, &request, session, thread, metadata, true)
		if err != nil {
			t.Fatalf("prepare turn %s: %v", turnID, err)
		}
		return transform
	}

	first := prepare(t.Context(), "host-turn-a", "session-a1")
	first.observeResponseUsage(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
	continuationContext, err := continueJournalContext(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	continued := prepare(continuationContext, "host-turn-a", "session-a2")
	continued.observeResponseUsage(tokenCounts{InputTokens: 20, UncachedInputTokens: 20})
	continuedContinuation, err := continueJournalContext(continuationContext, continued)
	if err != nil {
		t.Fatal(err)
	}

	otherTurn := prepare(t.Context(), "host-turn-b", "session-b")
	otherTurn.observeResponseUsage(tokenCounts{InputTokens: 90, UncachedInputTokens: 90})
	otherTurn.Close()

	last := prepare(continuedContinuation, "host-turn-a", "session-a3")
	defer last.Close()
	last.observeResponseUsage(tokenCounts{InputTokens: 30, UncachedInputTokens: 30})
	report, ok := last.completionUsageReport()
	if !ok || report.turn == nil || report.turn.Incomplete || report.turn.InputTokens != 60 || report.InputTokens != 150 {
		t.Fatalf("canonical TurnID was not retained across continuations: %+v, ok=%v", report, ok)
	}
	if other, ok := proxy.usage.turnSnapshot(thread, "host-turn-b"); !ok || other.InputTokens != 90 {
		t.Fatalf("interleaved host turn borrowed another turn's usage: %+v, ok=%v", other, ok)
	}
}

func TestUsageReportMentorSwitchWaitsForActualModelAndDelivery(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	first := &mekugiResponseTransform{
		ctx: t.Context(), proxy: proxy, threadID: "root", shellThreadID: "root",
		usageTracker: proxy.usage.observationForTurn("root", "root", "turn-mentor", "gpt-6-astra", ""),
	}
	first.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
	if report, ok := first.completionUsageReport(); !ok || report.mentor != "" {
		t.Fatalf("report before Mentor transition claimed a switch: %+v, ok=%v", report, ok)
	}
	proxy.usage.mentorTransition("root", "gpt-6-astra")
	if report, ok := first.completionUsageReport(); !ok || report.mentor != "" {
		t.Fatalf("report still on Mentor model advanced the switch: %+v, ok=%v", report, ok)
	}

	second := &mekugiResponseTransform{
		ctx: t.Context(), proxy: proxy, threadID: "root", shellThreadID: "root",
		usageTracker: proxy.usage.observationForTurn("root", "root", "turn-configured", "gpt-5.6-sol", ""),
	}
	second.observeResponseUsage(tokenCounts{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 5})
	response := []byte(`{"id":"mentor-switch-report","status":"completed"}`)
	messages, err := second.journalTerminalMessages(response)
	if err != nil || len(messages) != 1 {
		t.Fatalf("terminal usage messages = %v, err=%v", messages, err)
	}
	text := commentaryMessageText(messages[0])
	if text != "Router session usage · Main turn: 50 in / 5 out, $0.0003 · Total: 150 in / 15 out, $0.0018 · Mentor gpt-6-astra → gpt-5.6-sol" {
		t.Fatalf("actual model-switch report = %q", text)
	}
	if got, _ := proxy.usage.mentorNote("root", ""); got == "" {
		t.Fatal("report construction acknowledged the Mentor note before delivery")
	}
	second.Delivered(marshalUsageReportFixture(t, map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": "not-the-usage-report"}}))
	if got, _ := proxy.usage.mentorNote("root", ""); got == "" {
		t.Fatal("an unrelated delivered item acknowledged the Mentor note")
	}
	second.Delivered(marshalUsageReportFixture(t, map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": second.journalUsageID}}))
	if got, _ := proxy.usage.mentorNote("root", ""); got != "" {
		t.Fatalf("delivered usage report did not acknowledge the Mentor note: %q", got)
	}

	t.Run("child transition is carried by the main report", func(t *testing.T) {
		proxy := newManagedMekugiProxy(t)
		root, _ := prepareActivityTest(t, proxy, "root-session", "root", "", "/root", nil)
		child, _ := prepareActivityTest(t, proxy, "child-session", "child", "root", "/root/worker", nil)
		child.usageTracker = proxy.usage.observationForTurn("child", "child", "child-mentor-turn", "gpt-6-astra", "")
		child.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
		proxy.usage.mentorTransition("child", "gpt-6-astra")
		root.usageTracker = proxy.usage.observationForTurn("root", "root", "root-turn-before-child-switch", "gpt-6-sol", "")
		root.observeResponseUsage(tokenCounts{InputTokens: 10, UncachedInputTokens: 10})
		if report, ok := root.completionUsageReport(); !ok || report.mentor != "" {
			t.Fatalf("main report advanced before the child used its selected model: %+v, ok=%v", report, ok)
		}

		proxy.usage.observationForTurn("child", "child", "child-configured-turn", "gpt-5.6-sol", "").observe(tokenCounts{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 5})
		messages, err := root.journalTerminalMessages([]byte(`{"id":"child-mentor-report","status":"completed"}`))
		if err != nil || len(messages) != 1 {
			t.Fatalf("main report messages = %v, err=%v", messages, err)
		}
		text := commentaryMessageText(messages[0])
		if !strings.Contains(text, "/root/worker Mentor gpt-6-astra → gpt-5.6-sol") {
			t.Fatalf("main report omitted the child's actual model switch: %q", text)
		}
		if got, _ := proxy.usage.mentorNote("child", ""); got == "" {
			t.Fatal("child Mentor note was acknowledged before main report delivery")
		}
		root.Delivered(marshalUsageReportFixture(t, map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": root.journalUsageID}}))
		if got, _ := proxy.usage.mentorNote("child", ""); got != "" {
			t.Fatalf("delivered main report did not acknowledge child Mentor note: %q", got)
		}
	})
}

func TestCompactUsageReportDurableIDIsStrippedFromLaterInput(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	transform, _, _, workspace := newMekugiTestTransformWithProxy(t, proxy)
	transform.usageTracker = proxy.usage.observationForTurn(transform.shellThreadID, transform.shellThreadID, "turn-with-usage", "gpt-6-sol", "")
	transform.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20})
	response := []byte(`{"id":"compact-usage-response","status":"completed"}`)
	messages, err := transform.journalTerminalMessages(response)
	if err != nil || len(messages) != 1 {
		t.Fatalf("terminal report messages = %v, err=%v", messages, err)
	}
	usageText := commentaryMessageText(messages[0])
	if !strings.HasPrefix(usageText, "Router session usage · Main turn: ") {
		t.Fatalf("generated report was not compact: %q", usageText)
	}
	unknown := assistantCommentaryMessage(subagentCommentaryMessageID("unretained-usage-lookalike"), "keep this model-authored message")
	input, err := json.Marshal([]any{messages[0], unknown})
	if err != nil {
		t.Fatal(err)
	}
	request := &parsedResponsesRequest{fields: map[string]jsonv1.RawMessage{
		"input": jsonv1.RawMessage(input),
	}}
	if _, err := proxy.reconcileVisibleInput(t.Context(), request, workspace, transform.historySessionID); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request.fields["input"], []byte(usageText)) || bytes.Contains(request.fields["input"], []byte(jsonString(messages[0], "id"))) {
		t.Fatalf("durable compact usage report survived replay: %s", request.fields["input"])
	}
	if !bytes.Contains(request.fields["input"], []byte("keep this model-authored message")) {
		t.Fatalf("replay removed an unretained lookalike: %s", request.fields["input"])
	}
}
