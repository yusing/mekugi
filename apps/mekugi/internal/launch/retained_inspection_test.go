package launch

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestThreadInspectionWithoutRuntime(t *testing.T) {
	retained, err := history.NewService(control.RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	store, err := retained.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"root"}}
{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"root","thread_settings":{"model":"retained","reasoning_effort":"high","service_tier":"fast"}}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":123},"model_context_window":1000}}}
{"type":"event_msg","payload":{"type":"item_completed","thread_id":"root","item":{"id":"compact","type":"ContextCompaction"}}}
`), 0600); err != nil {
		t.Fatal(err)
	}
	service := control.New()
	if err := service.AdmitRoot(t.Context(), session, &control.Member{Thread: "root", Session: "session", Task: "/root", Path: path, Status: control.Completed, AudienceAdmitted: true, Settings: control.Settings{Model: "stale-creation-model", Effort: new("low"), Tier: new("default")}}); err != nil {
		t.Fatal(err)
	}
	execution := &threadExecution{hub: sessionapi.NewHub(t.Context(), nil), shared: service}
	if _, err := execution.Runtime("root"); err == nil {
		t.Fatal("fixture has a running runtime")
	}
	got, err := execution.Inspect(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	want := `{"model":"retained","reasoning_effort":"high","service_tier":"priority","context_tokens":123,"context_window":1000,"compactions":1}`
	if err != nil || string(data) != want {
		t.Fatalf("retained inspection = %s, %v", data, err)
	}
	if _, err := execution.Inspect(t.Context(), "foreign"); err != control.ErrNotAdmitted {
		t.Fatalf("foreign inspection = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got, err = execution.Inspect(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(got)
	want = `{"model":null,"reasoning_effort":null,"service_tier":null,"context_tokens":null,"context_window":null,"compactions":null}`
	if err != nil || string(data) != want {
		t.Fatalf("unavailable inspection = %s, %v", data, err)
	}
	if _, err := execution.Runtime("root"); err == nil {
		t.Fatal("inspection started a runtime")
	}
}
