package router

import (
	"fmt"
	"strings"
	"testing"
)

func TestJournalSummaryCountsFinalGuidanceAtCapacity(t *testing.T) {
	proxy, workspace := treeTestJournal(t)
	ctx, store := t.Context(), proxy.replayStore
	// Completed package checks still need integration, so their retained answers
	// remain relevant without inflating titles or the mandatory compact budget.
	answer := strings.Repeat("Verified package behavior and retained focused validation evidence. ", 8)[:512]
	var tasks []journalMutation
	for i := range 80 {
		thread := fmt.Sprintf("check-%02d", i)
		author := "/root/" + thread
		if err := proxy.journals.initialize(ctx, store, workspace, thread, author, ""); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.bindIdentity(ctx, store, workspace, thread, "tree", author, true); err != nil {
			t.Fatal(err)
		}
		if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Text: &answer, Answer: new(true)}}); err != nil {
			t.Fatal(err)
		}
		if err := proxy.journals.observeLifecycle(ctx, store, workspace, thread, "done", ""); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, journalMutation{Op: "add", Kind: "task", Title: new("Integrate " + thread), State: new("working"), Agent: author})
	}
	paths := treeApply(t, proxy, workspace, tasks...)
	summary, err := summaryForTest(t, ctx, store, workspace, "tree")
	if err != nil {
		t.Fatal(err)
	}
	const guidance = "Read more: journal({op:\"read\",p:\"PATH\",depth:1}); journal({op:\"read\",view:\"outline\"}) finds own older paths. For an agent, add agent:\"NAME\",view:\"own\" using its heading. Discover older agents with journal({op:\"read\",depth:1}). Read relevant context paths before acting.\n"
	if !strings.HasSuffix(summary.Text, guidance) || strings.Count(summary.Text, answer) != len(tasks) {
		t.Fatal("accepted summary omitted retained child answers or final recovery guidance")
	}
	// Fill ordinary integration bodies, each below the 512-byte excerpt limit.
	// Existing newlines account for two rendered bytes per newly nonempty body.
	remaining := maxJournalSummaryBytes - len(summary.Text)
	detail := strings.Repeat("Integrate the retained checks, inspect package changes, and run the combined validation. ", 6)
	var updates []journalMutation
	for _, path := range paths {
		if remaining == 0 {
			break
		}
		size := min(500, remaining-2)
		if size < 1 {
			t.Fatalf("fixture cannot fill remaining %d bytes", remaining)
		}
		updates = append(updates, journalMutation{Op: "set", P: path, Body: new(detail[:size])})
		remaining -= size + 2
	}
	if remaining != 0 {
		t.Fatalf("fixture needs %d more bytes", remaining)
	}
	treeApply(t, proxy, workspace, updates...)
	summary, err = summaryForTest(t, ctx, store, workspace, "tree")
	if err != nil || len(summary.Text) != maxJournalSummaryBytes || !strings.HasSuffix(summary.Text, guidance) {
		t.Fatalf("full packet at capacity: bytes=%d err=%v", len(summary.Text), err)
	}
	// One ordinary body byte crosses the full-packet limit, while the packet
	// without final guidance still fits. Rejection must expose no partial text.
	last := updates[len(updates)-1]
	treeApply(t, proxy, workspace, journalMutation{Op: "set", P: last.P, Body: new(*last.Body + ".")})
	summary, err = summaryForTest(t, ctx, store, workspace, "tree")
	if err == nil || summary.Text != "" || err.Error() != "journal evidence exceeds summary capacity" {
		t.Fatalf("oversized full packet was exposed: bytes=%d err=%v", len(summary.Text), err)
	}
}
