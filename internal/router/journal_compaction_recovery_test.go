package router

import (
	"bytes"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Read the actual delivered terminal message, rather than regenerate the summary
// from today's journal or compare only a distinguishing substring.
func deliverRecoveryCompaction(t *testing.T, proxy *mekugiProxy, workspace, thread, turn string) (string, string) {
	t.Helper()
	request, headers := journalCompactionRequest(t, workspace, thread)
	metadata, _ := decodeCodexTurnMetadata(headers)
	metadata.TurnID = turn
	metadata.Compaction = mustTestJSON(t, map[string]any{
		"trigger": "manual", "reason": "user_requested", "implementation": "responses",
		"phase": "standalone_turn", "strategy": "memento",
	})
	headers.Set(codexTurnMetadataHeader, string(mustTestJSON(t, metadata)))
	provider := &serverFakeProvider{}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "recovery-"+turn, provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 0 {
		t.Fatal("manual compaction unexpectedly forwarded to provider")
	}
	for line := range strings.SplitSeq(output.String(), "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			Response struct {
				ID     string `json:"id"`
				Output []struct {
					Type    string `json:"type"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "response.completed" {
			continue
		}
		var text strings.Builder
		for _, item := range event.Response.Output {
			if item.Type == "message" {
				for _, part := range item.Content {
					if part.Type == "output_text" {
						text.WriteString(part.Text)
					}
				}
			}
		}
		if event.Response.ID == "" || text.Len() == 0 {
			t.Fatal("compaction completed without response ID or summary")
		}
		return event.Response.ID, text.String()
	}
	t.Fatal("compaction did not deliver a completed SSE response")
	return "", ""
}

func requireCompactionRecovery(t *testing.T, store *mekugiReplayStore, workspace, thread, turn, item, want string) {
	t.Helper()
	got, err := store.compactionRecovery(t.Context(), workspace, thread, turn, item)
	if err != nil || got != want {
		t.Fatalf("recovery for %s/%s = %q, %v; want exact %q", turn, item, got, err, want)
	}
}

func TestJournalCompactionRecoveryReadsCompletedHistoryOnDemand(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, thread := transform.ctx, transform.shellThreadID
	proxy.journalCompaction = "auto"
	body := strings.Repeat("Historical validation detail. ", 100)
	mutations := []journalMutation{
		{Op: "add", Kind: "context", Title: new("Current constraint"), Body: new("Keep wire compatibility")},
		{Op: "add", Kind: "task", Title: new("Completed checkpoint"), State: new("done"), Body: &body},
		{Op: "add", Kind: "task", Title: new("Pending acceptance"), State: new("pending")},
		{Op: "log", P: "/3", Text: new("Live acceptance still needs evidence")},
	}
	for range 30 {
		mutations = append(mutations, journalMutation{Op: "add", Under: "/2", Kind: "note", Title: new("Historical validation"), Body: &body})
	}
	if _, err := proxy.journals.apply(ctx, proxy.replayStore, workspace, thread, "", mutations); err != nil {
		t.Fatal(err)
	}
	_, delivered := deliverRecoveryCompaction(t, proxy, workspace, thread, "lean-reset")
	for _, want := range []string{"/1 Current constraint", `journal({op:"read",p:"PATH",depth:1})`, "Live acceptance still needs evidence", "Resume: continue /3"} {
		if !strings.Contains(delivered, want) {
			t.Errorf("delivered recovery omitted %q: %s", want, delivered)
		}
	}
	if strings.Contains(delivered, "Historical validation") || strings.Contains(delivered, "Completed checkpoint") || len(delivered) > 2048 {
		t.Fatalf("completed history inflated the delivered recovery: %d bytes", len(delivered))
	}
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	depth := 1
	nodes, err := newJournalStore().readTree(ctx, reopened, workspace, thread, "", "/2", &depth, "own")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Body != body || len(nodes[0].Children) != 30 || nodes[0].Children[29].Body != body {
		t.Fatalf("on-demand read lost completed evidence after restart: %+v", nodes)
	}
	hook, err := reopened.postCompactContext(ctx, workspace, thread)
	if err != nil || hook != delivered {
		t.Fatalf("hook and synthesized recovery differ: %v\n%s", err, hook)
	}
}

func TestJournalCompactionRecoveryExactDeliveredSummaryAndRestart(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Original recovery: 日本語, <details>, and `code`"), State: new("working")},
	}); err != nil {
		t.Fatal(err)
	}
	firstID, first := deliverRecoveryCompaction(t, proxy, workspace, thread, "first-reset")
	// A pending turn receipt alone must not disclose a summary to an arbitrary item.
	requireCompactionRecovery(t, proxy.replayStore, workspace, thread, "first-reset", "first-item", "")
	proxy.replayStore.bindStandaloneCompactionItem(t.Context(), workspace, thread, "first-reset", "first-item")
	requireCompactionRecovery(t, proxy.replayStore, workspace, thread, "first-reset", "first-item", first)
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "set", P: "/1", Title: new("Changed after reset")},
		{Op: "log", Text: new("New journal state must not rewrite prior recovery")},
	}); err != nil {
		t.Fatal(err)
	}
	requireCompactionRecovery(t, proxy.replayStore, workspace, thread, "first-reset", "first-item", first)
	secondID, second := deliverRecoveryCompaction(t, proxy, workspace, thread, "second-reset")
	if firstID == secondID || first == second {
		t.Fatal("distinct compactions did not produce distinct IDs and summaries")
	}
	// Restart before binding the second item: the original response identity must
	// survive independently of a live request or journal reset driver.
	reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	reopened.bindStandaloneCompactionItem(t.Context(), workspace, thread, "second-reset", "second-item")
	reopened, err = openMekugiReplayStore(proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	requireCompactionRecovery(t, reopened, workspace, thread, "first-reset", "first-item", first)
	requireCompactionRecovery(t, reopened, workspace, thread, "second-reset", "second-item", second)
	requireCompactionRecovery(t, reopened, workspace, thread, "first-reset", "", first)
	requireCompactionRecovery(t, reopened, workspace, thread, "second-reset", "", second)
	for _, key := range [][4]string{
		{t.TempDir(), thread, "first-reset", "first-item"},
		{workspace, "foreign-thread", "first-reset", "first-item"},
		{workspace, thread, "foreign-turn", "first-item"},
		{workspace, thread, "first-reset", "foreign-item"},
		{workspace, thread, "second-reset", "first-item"},
		{workspace, thread, "", "first-item"},
	} {
		requireCompactionRecovery(t, reopened, key[0], key[1], key[2], key[3], "")
	}
}

func TestJournalCompactionRecoveryTurnDisclosureRejectsAmbiguousReceipts(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Original recovery")},
	}); err != nil {
		t.Fatal(err)
	}
	_, first := deliverRecoveryCompaction(t, proxy, workspace, thread, "shared-turn")
	proxy.replayStore.bindStandaloneCompactionItem(t.Context(), workspace, thread, "shared-turn", "first-item")
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "set", P: "/1", Title: new("Later recovery")},
	}); err != nil {
		t.Fatal(err)
	}
	_, second := deliverRecoveryCompaction(t, proxy, workspace, thread, "shared-turn")
	proxy.replayStore.bindStandaloneCompactionItem(t.Context(), workspace, thread, "shared-turn", "second-item")
	requireCompactionRecovery(t, proxy.replayStore, workspace, thread, "shared-turn", "", "")
	requireCompactionRecovery(t, proxy.replayStore, workspace, thread, "shared-turn", "first-item", first)
	requireCompactionRecovery(t, proxy.replayStore, workspace, thread, "shared-turn", "second-item", second)
}

func TestJournalCompactionRecoveryUnavailableLegacyAndMissing(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing", "legacy-item", "unbound-legacy-turn"} {
		t.Run(scenario, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Available current journal must not regenerate historical text"), State: new("working")},
			}); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing" {
				item := "old-item"
				if scenario == "unbound-legacy-turn" {
					item = ""
				}
				data := mustTestJSON(t, map[string]any{
					"version": 1, "workspace": workspace, "thread": thread, "response_id": "legacy-response",
					"answered_items": []any{map[string]any{"turn": "old-turn", "item": item}},
				})
				if err := os.WriteFile(filepath.Join(proxy.replayStore.directory, journalCompactionName(workspace, thread)), data, 0600); err != nil {
					t.Fatal(err)
				}
				proxy.replayStore.bindStandaloneCompactionItem(t.Context(), workspace, thread, "old-turn", "old-item")
			}
			reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
			if err != nil {
				t.Fatal(err)
			}
			requireCompactionRecovery(t, reopened, workspace, thread, "old-turn", "old-item", "")
		})
	}
}

func TestJournalCompactionRecoveryRetainedContentUnavailableOrCorrupt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing", "corrupt-json", "foreign-identity"} {
		t.Run(scenario, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			proxy.journalCompaction = "auto"
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{
				{Op: "add", Kind: "task", Title: new("Live journal is not a replacement for the original summary"), State: new("working")},
			}); err != nil {
				t.Fatal(err)
			}
			responseID, _ := deliverRecoveryCompaction(t, proxy, workspace, thread, "reset-turn")
			proxy.replayStore.bindStandaloneCompactionItem(t.Context(), workspace, thread, "reset-turn", "reset-item")
			path := filepath.Join(proxy.replayStore.directory, journalCompactionRecoveryName(workspace, thread, responseID))
			if scenario == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				data := []byte(`{"text":`)
				if scenario == "foreign-identity" {
					data = mustTestJSON(t, map[string]any{
						"workspace": workspace, "thread": "foreign-thread", "response_id": responseID, "text": "Unrelated summary",
					})
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := openMekugiReplayStore(proxy.replayStore.directory)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.compactionRecovery(t.Context(), workspace, thread, "reset-turn", "reset-item")
			if got != "" {
				t.Fatalf("unavailable retained content returned regenerated text: %q", got)
			}
			if scenario == "missing" && err != nil {
				t.Fatalf("missing recovery should be unavailable without error: %v", err)
			}
			if scenario != "missing" && err == nil {
				t.Fatal("corrupt retained recovery did not report an error")
			}
		})
	}
}
