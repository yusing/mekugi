package router

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"strconv"
	"strings"
	"testing"
)

func TestJournalEmptyOutcomeKeepsAnswerWithoutTurnCard(t *testing.T) {
	for _, final := range []string{"Done.", "   "} {
		t.Run(strconv.Quote(final), func(t *testing.T) {
			transform, proxy, _, _ := newDurableTreeTransform(t)
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, "", []journalMutation{
				{Op: "add", Text: new("Legacy milestone")},
			}); err != nil {
				t.Fatal(err)
			}
			journal, _, err := readThreadJournal(proxy.replayStore, transform.directory, transform.shellThreadID)
			if err != nil || journal.TreeAuthored {
				t.Fatalf("legacy setup: tree=%v err=%v", journal.TreeAuthored, err)
			}
			revisions := make(map[string]uint64)
			for _, item := range journal.Items {
				revisions[item.ID] = item.Updated
			}
			if err := proxy.journals.acknowledge(t.Context(), proxy.replayStore, transform.directory, transform.shellThreadID, revisions, true); err != nil {
				t.Fatal(err)
			}
			answer := map[string]any{"type": "message", "id": "raw-legacy-outcome", "role": "assistant", "phase": "final_answer", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": final}}}
			visible, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"id": "legacy-outcome", "status": "completed", "output": []any{answer}}))
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []map[string]jsonv1.RawMessage `json:"output"`
			}
			if err := jsonv2.Unmarshal(visible, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) == 0 {
				t.Fatalf("legacy journal turn ended without a final message: %s", visible)
			}
			if strings.TrimSpace(final) != "" && !strings.Contains(commentaryMessageText(response.Output[len(response.Output)-1]), final) {
				t.Fatalf("legacy final answer lost: %s", visible)
			}
		})
	}
}
