package router

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"os"
	"strconv"
	"testing"
)

func TestCompletionUsageAggregation(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	t.Run("main turn excludes children and interleaved turns", func(t *testing.T) {
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
		if !ok || report.turn == nil || report.turn.InputTokens != 300 {
			t.Fatalf("multi-agent report = %+v, ok=%v", report, ok)
		}
		if report.InputTokens != 1_330 || report.rows == nil || len(*report.rows) != 2 || (*report.rows)[0].report.InputTokens != 330 || (*report.rows)[1].report.InputTokens != 1_000 {
			t.Fatalf("router total did not include root and child totals: %+v", report)
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
	if !ok || report.turn == nil || !report.turn.Incomplete {
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

func TestMainCompletionPersistsTokenMetricsWithoutCommentary(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	proxy := newManagedMekugiProxy(t)
	first := &mekugiResponseTransform{
		ctx: t.Context(), proxy: proxy, threadID: "root", shellThreadID: "root",
		usageTracker: proxy.usage.observationForTurn("root", "root", "turn-mentor", "gpt-6-astra", ""),
	}
	first.observeResponseUsage(tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 10})
	proxy.usage.mentorTransition("root", "gpt-6-astra")

	main := &mekugiResponseTransform{
		ctx: t.Context(), proxy: proxy, threadID: "root", shellThreadID: "root",
		usageTracker: proxy.usage.observationForTurn("root", "root", "turn-configured", "gpt-5.6-sol", ""),
	}
	main.observeResponseUsage(tokenCounts{InputTokens: 50, UncachedInputTokens: 50, OutputTokens: 5})
	response := []byte(`{"id":"mentor-switch-response","status":"completed","output":[{"type":"message","id":"answer","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"The task is complete."}]}]}`)
	transformed, err := main.TransformJSON(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(transformed, []byte("Router session usage")) || !bytes.Contains(transformed, []byte("The task is complete.")) {
		t.Fatalf("completion replaced the answer or emitted usage commentary: %s", transformed)
	}
	paths := proxy.tokenMetricPaths()
	if len(paths) != 1 {
		t.Fatalf("main completion metric paths = %q", paths)
	}
	markdown, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Contains(markdown, []byte("| Total |")) || !bytes.Contains(markdown, []byte("| 150 (0.0%) | 0 | 15 |")) {
		t.Fatalf("completion metrics = %q, %v", markdown, err)
	}
}

func TestChildCompletionDoesNotPersistTokenMetrics(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	proxy := newManagedMekugiProxy(t)
	child, _ := prepareActivityTest(t, proxy, "child-session", "child", "root", "/root/worker", nil)
	child.subagentTurn = true
	child.usageTracker = proxy.usage.observationForTurn("child", "child", "turn-child", "gpt-5.6-sol", "")
	child.observeResponseUsage(tokenCounts{InputTokens: 20, UncachedInputTokens: 8, OutputTokens: 5})
	response := []byte(`{"id":"child-response","status":"completed","output":[{"type":"message","id":"answer","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"Child result."}]}]}`)
	transformed, err := child.TransformJSON(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(transformed, []byte("Router session usage")) || len(proxy.tokenMetricPaths()) != 0 {
		t.Fatalf("child completion emitted commentary or persisted root metrics: %s paths=%q", transformed, proxy.tokenMetricPaths())
	}
}
