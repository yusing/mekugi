package router

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestCommentaryReplayFilteringRequiresExactRetainedID(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	known := subagentCommentaryMessageID("known")
	unknown := subagentCommentaryMessageID("unknown")
	if err := store.putCommentary(t.Context(), "workspace", []string{known}); err != nil {
		t.Fatal(err)
	}

	request := &parsedResponsesRequest{fields: map[string]json.RawMessage{
		"input": mustTestJSON(t, []any{
			assistantCommentaryMessage(known, "router-authored"),
			assistantCommentaryMessage(unknown, "model-authored"),
		}),
	}}
	proxy := &mekugiProxy{replayStore: store}
	if _, err := proxy.reconcileVisibleInput(t.Context(), request, "workspace", "session"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(request.fields["input"], []byte(known)) {
		t.Fatalf("known router commentary survived replay: %s", request.fields["input"])
	}
	if !bytes.Contains(request.fields["input"], []byte(unknown)) {
		t.Fatalf("unretained prefix-matching message was removed: %s", request.fields["input"])
	}
}

func TestCommentaryReplacementProvenanceIsScopedAndRetained(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "memory", true: "durable"}[durable], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			if durable {
				attachTestReplayStore(t, proxy)
			}
			child, _ := prepareActivityTest(t, proxy, "child", "child", "root", "/root/child", nil)
			defer child.Close()
			id := commentaryMessageID("summary")
			message := assistantCommentaryMessage(id, "Complete enriched result")
			replacement := &commentaryReplacement{Thread: "child", Turn: "turn", Items: []string{"original"}}
			if len(child.retainCommentaryReplacing(replacement, message)) != 1 {
				t.Fatal("cannot retain enriched result")
			}
			// A duplicate generic retention must not erase the relationship.
			if len(child.retainCommentary(message)) != 1 {
				t.Fatal("cannot retain duplicate message")
			}
			if durable {
				var err error
				proxy.replayStore, err = openMekugiReplayStore(proxy.replayStore.directory)
				if err != nil {
					t.Fatal(err)
				}
			}
			items := proxy.commentaryReplacementItems(t.Context(), child.directory, "child", "turn", id)
			if !reflect.DeepEqual(items, []string{"original"}) {
				t.Fatalf("replacement relationship lost: %v", items)
			}
			items[0] = "tampered"
			if got := proxy.commentaryReplacementItems(t.Context(), child.directory, "child", "turn", id); !reflect.DeepEqual(got, []string{"original"}) {
				t.Fatalf("reader mutated retained provenance: %v", got)
			}
			for _, scope := range []struct{ workspace, thread, turn, item string }{
				{"other workspace", "child", "turn", id},
				{child.directory, "sibling", "turn", id},
				{child.directory, "child", "later-turn", id},
				{child.directory, "child", "turn", commentaryMessageID("unretained")},
			} {
				if got := proxy.commentaryReplacementItems(t.Context(), scope.workspace, scope.thread, scope.turn, scope.item); len(got) != 0 {
					t.Fatalf("replacement crossed identity boundary: %+v: %v", scope, got)
				}
			}
			wrong := &commentaryReplacement{Thread: "sibling", Turn: "turn", Items: []string{"original"}}
			if len(child.retainCommentaryReplacing(wrong, message)) != 0 {
				t.Fatal("accepted conflicting replacement provenance")
			}
		})
	}
}
