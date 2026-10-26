package rollout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplayGeneratedImageExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	source := `{"type":"session_meta","payload":{"id":"main","cwd":"/workspace"}}
{"timestamp":"2026-10-01T12:00:00Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"main","turn_id":"turn","started_at_ms":1,"completed_at_ms":1001,"item":{"type":"Extension","kind":"image_gen.generation","id":"generated","status":"completed","fileId":"artifact","savedPath":"/workspace/generated.png"}}}
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := Replay(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Events) != 1 || replay.Events[0].Item.Type != "imageGeneration" || replay.Events[0].Item.SavedPath != "/workspace/generated.png" || len(replay.Unsupported) != 0 {
		t.Fatalf("extension image lost: %+v", replay)
	}
}
