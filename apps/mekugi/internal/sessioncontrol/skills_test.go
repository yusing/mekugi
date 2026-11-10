package sessioncontrol

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestNativeSkillCatalogUsesAdmittedWorkspaceAndHostLedger(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "rollout.jsonl")
	body := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"thread_settings_applied\",\"thread_id\":\"child\",\"thread_settings\":{\"model\":\"fixture\",\"model_provider_id\":\"provider\",\"cwd\":%q,\"permission_profile\":{\"type\":\"disabled\"},\"approval_policy\":\"never\"}}}\n", workspace)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	requests := NewRequests[string](&requestTransport{id: "catalog-call"})
	t.Cleanup(requests.Close)
	type result struct {
		paths map[string][]string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		paths, err := requests.NativeSkillCatalog(t.Context(), "child")
		done <- result{paths, err}
	}()
	call := <-requests.Calls()
	if call.method != "thread/read" || call.scope != "child" {
		t.Fatalf("identity lookup escaped recipient: %+v", call)
	}
	requests.Dispatch(call)
	raw, _ := json.Marshal(map[string]any{"thread": map[string]string{"id": "child", "sessionId": "session", "path": path}})
	if !requests.CompleteCall(appserver.Message{ID: jsontext.Value(`catalog-call`), Result: raw}) {
		t.Fatal("identity lookup bypassed request ledger")
	}
	select {
	case call = <-requests.Calls():
	case got := <-done:
		t.Fatalf("catalog returned before host lookup: %v", got.err)
	}
	var params struct {
		Cwds []string `json:"cwds"`
	}
	if err := json.Unmarshal(call.params, &params); err != nil {
		t.Fatal(err)
	}
	if call.method != "skills/list" || call.scope != "child" || !reflect.DeepEqual(params.Cwds, []string{workspace}) {
		t.Fatalf("catalog not scoped to admitted workspace: %s %+v", call.method, params)
	}
	requests.Dispatch(call)
	raw, _ = json.Marshal(map[string]any{"data": []any{
		map[string]any{"cwd": "/other", "skills": []any{map[string]any{"name": "foreign", "path": "/foreign/SKILL.md", "enabled": true}}},
		map[string]any{"cwd": workspace, "skills": []any{
			map[string]any{"name": "native", "path": "/native/SKILL.md", "enabled": true},
			map[string]any{"name": "native", "path": "/native/SKILL.md", "enabled": true},
			map[string]any{"name": "native", "path": "/second/SKILL.md", "enabled": true},
			map[string]any{"name": "disabled", "path": "/disabled/SKILL.md", "enabled": false},
			map[string]any{"name": "invalid", "path": "relative/SKILL.md", "enabled": true},
		}, "errors": []any{map[string]any{"message": "one catalog entry failed"}}},
	}})
	if !requests.CompleteCall(appserver.Message{ID: jsontext.Value(`catalog-call`), Result: raw}) {
		t.Fatal("catalog bypassed request ledger")
	}
	got := <-done
	if !reflect.DeepEqual(got.paths, map[string][]string{"native": {"/native/SKILL.md", "/second/SKILL.md"}}) || got.err == nil || !strings.Contains(got.err.Error(), "one catalog entry failed") || !strings.Contains(got.err.Error(), "invalid identity") {
		t.Fatalf("catalog filtering lost identities or partial errors: %+v %v", got.paths, got.err)
	}
}
