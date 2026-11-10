package sessioncontrol

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestNativeMessageSourcePermissionsAndBounds(t *testing.T) {
	for _, phase := range []string{"new-child", "active-recipient", "idle-updated-permissions"} {
		t.Run(phase, func(t *testing.T) {
			active := phase != "new-child"
			workspace := t.TempDir()
			member := &control.Member{Thread: "child", Session: "child-session", Settings: control.Settings{Workspace: workspace, PermissionProfile: jsontext.Value(`{"type":"disabled"}`)}}
			if active {
				member.HostRunning = phase == "active-recipient"
				member.Turn = "child-turn"
				member.Path = filepath.Join(workspace, "rollout.jsonl")
				body := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n{\"type\":\"turn_context\",\"payload\":{\"turn_id\":\"child-turn\",\"model\":\"fixture\",\"cwd\":%q,\"permission_profile\":{\"type\":\"restricted\"},\"active_permission_profile\":{\"id\":\":read-only\"},\"approval_policy\":\"never\"}}\n", workspace)
				if !member.HostRunning {
					body = strings.ReplaceAll(body, `"permission_profile":{"type":"restricted"},"active_permission_profile":{"id":":read-only"}`, `"permission_profile":{"type":"disabled"}`)
					body += fmt.Sprintf("{\"type\":\"event_msg\",\"payload\":{\"type\":\"thread_settings_applied\",\"thread_id\":\"child\",\"thread_settings\":{\"model\":\"fixture\",\"model_provider_id\":\"provider\",\"cwd\":%q,\"permission_profile\":{\"type\":\"restricted\"},\"active_permission_profile\":{\"id\":\":read-only\"},\"approval_policy\":\"never\"}}}\n", workspace)
				}
				if err := os.WriteFile(member.Path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				member.Settings.Workspace = "/not-the-current-workspace"
			}
			requests := NewRequests[string](&requestTransport{id: "source-host-call"})
			t.Cleanup(requests.Close)
			result := make(chan struct {
				text string
				err  error
			}, 1)
			go func() {
				text, err := requests.ReadMessageSources(t.Context(), member, `{"sources":["@source","@denied"]}`)
				result <- struct {
					text string
					err  error
				}{text, err}
			}()
			previous := history.MaxRecordBytes + 2
			for i := range 3 { // Root, its nested reference, then the denied root.
				call := <-requests.Calls()
				var params struct {
					Command           []string          `json:"command"`
					Cwd               string            `json:"cwd"`
					PermissionProfile string            `json:"permissionProfile"`
					SandboxPolicy     map[string]string `json:"sandboxPolicy"`
					Cap               int               `json:"outputBytesCap"`
				}
				if err := json.Unmarshal(call.params, &params); err != nil {
					t.Fatal(err)
				}
				if call.method != "command/exec" || call.scope != "child" || params.Cwd != workspace || params.Cap >= previous || len(params.Command) != 3 || params.Command[0] != "bash" || !strings.HasPrefix(params.Command[2], "'mcat'") {
					t.Fatalf("native read escaped its recipient or shared bounds: %s %+v", call.method, params)
				}
				if active && (params.PermissionProfile != ":read-only" || params.SandboxPolicy != nil) || !active && (params.PermissionProfile != "" || params.SandboxPolicy["type"] != "dangerFullAccess") {
					t.Fatal("recipient permissions were not selected", params)
				}
				previous = params.Cap
				requests.Dispatch(call)
				body := map[string]any{"stdout": "NESTED_BYTES", "stderr": "", "exitCode": 0}
				if i == 0 {
					body["stdout"] = "ROOT_BYTES @nested"
				}
				if i == 2 {
					body["stdout"], body["stderr"], body["exitCode"] = "", "recipient denied read", 1
				}
				raw, _ := json.Marshal(body)
				if !requests.CompleteCall(appserver.Message{ID: jsontext.Value(`source-host-call`), Result: raw}) {
					t.Fatal("native reader bypassed the shared request ledger")
				}
			}
			got := <-result
			if got.err != nil || !strings.Contains(got.text, "ROOT_BYTES") || !strings.Contains(got.text, "NESTED_BYTES") || !strings.Contains(got.text, "recipient denied read") || !strings.Contains(got.text, "exit_code=1") {
				t.Fatalf("source output/status lost: %s %v", got.text, got.err)
			}
		})
	}
}

func TestNativeMessageSourcesDoNotWidenUnknownPermissions(t *testing.T) {
	requests := NewRequests[string](&requestTransport{id: "source-host-call"})
	defer requests.Close()
	member := &control.Member{Thread: "child", Settings: control.Settings{Workspace: t.TempDir(), PermissionProfile: jsontext.Value(`{"type":"restricted"}`)}}
	if _, err := requests.ReadMessageSources(context.Background(), member, `{"sources":["@private"]}`); err == nil {
		t.Fatal("unselectable permissions silently widened")
	}
}

func TestMessageSourceSelectionPreservesCanonicalAdmission(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "child.jsonl")
	body := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"thread_settings_applied\",\"thread_id\":\"child\",\"thread_settings\":{\"model\":\"fixture\",\"model_provider_id\":\"provider\",\"cwd\":%q,\"permission_profile\":{\"type\":\"disabled\"},\"active_permission_profile\":{\"id\":\":danger-full-access\"},\"approval_policy\":\"never\"}}}\n", workspace)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	// Older preparations did not need the selection identity to confirm an
	// identical effective policy. Adding its reader must not reject them.
	settings := control.Settings{Model: "fixture", Provider: "provider", Workspace: workspace, PermissionProfile: jsontext.Value(`{"type":"disabled"}`), ApprovalPolicy: jsontext.Value(`"never"`)}
	if err := confirmNativeSettings("child", path, settings); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeMessageSourcesUseAdmittedReader(t *testing.T) {
	runtime, _, _ := claudeFixture(t, nil)
	if _, err := runtime.ReadMessageSources(t.Context(), &control.Member{Thread: "foreign"}, "selection"); err == nil {
		t.Fatal("foreign runtime identity accepted")
	}
	var selected string
	runtime.SetMessageSourceReader(func(_ context.Context, selection string) (string, error) {
		selected = selection
		return "captured", nil
	})
	text, err := runtime.ReadMessageSources(t.Context(), &control.Member{Thread: "child"}, "selection")
	if err != nil || text != "captured" || selected != "selection" {
		t.Fatalf("Claude reader not used: %q %q %v", text, selected, err)
	}
}
