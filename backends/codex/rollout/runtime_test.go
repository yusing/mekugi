package rollout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRequiresExactTurnAndCompleteAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"thread"}}
{"type":"turn_context","payload":{"turn_id":"selected","cwd":"/work","model":"actual","effort":"high","approval_policy":"never","approvals_reviewer":"user","permission_profile":{"type":"managed","file_system":{"type":"restricted","entries":[{"path":{"type":"path","path":"/denied"},"access":"deny"}]},"network":"restricted"}}}
{"type":"turn_context","payload":{"turn_id":"later","cwd":"/other","model":"later","permission_profile":{"type":"disabled"},"approval_policy":"never"}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := ReadRuntime("thread", path, "selected")
	if err != nil || runtime.Model != "actual" || runtime.Workspace != "/work" || runtime.Effort == nil || *runtime.Effort != "high" || len(runtime.PermissionProfile) == 0 {
		t.Fatalf("wrong runtime: %+v, %v", runtime, err)
	}
	for _, identity := range [][2]string{{"foreign", "selected"}, {"thread", "missing"}, {"thread", ""}} {
		if _, err := ReadRuntime(identity[0], path, identity[1]); err == nil {
			t.Fatalf("accepted unavailable identity %v", identity)
		}
	}
	if err := os.WriteFile(path, []byte(data+`{"type":"turn_context","payload":{"turn_id":"selected","cwd":"/work","model":"actual"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRuntime("thread", path, "selected"); err == nil {
		t.Fatal("incomplete latest evidence fell back to older authority")
	}
}

func TestAdmissionRequiresThreadOwnedCanonicalSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	valid := `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"child","thread_settings":{"model":"actual","model_provider_id":"provider","cwd":"/child","permission_profile":{"type":"disabled"},"approval_policy":"never"}}}`
	for _, test := range []struct {
		name, record string
		valid        bool
	}{
		{"owned", valid, true},
		{"inherited", strings.Replace(valid, `"thread_id":"child"`, `"thread_id":"parent"`, 1), false},
		{"unowned", strings.Replace(valid, `"thread_id":"child",`, "", 1), false},
		{"null authority", strings.Replace(valid, `{"type":"disabled"}`, "null", 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"+test.record+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := ReadAdmission("child", path)
			if (err == nil) != test.valid {
				t.Fatalf("admission valid=%t error=%v", test.valid, err)
			}
			if test.valid && (snapshot.Model != "actual" || string(snapshot.PermissionProfile) != `{"type":"disabled"}`) {
				t.Fatal("canonical settings changed")
			}
		})
	}
}
