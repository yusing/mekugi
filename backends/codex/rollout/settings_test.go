package rollout

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func resumeSettingsRollout(t *testing.T, records ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	content := `{"type":"session_meta","payload":{"id":"saved"}}` + "\n" + strings.Join(records, "\n")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResumeSettings(t *testing.T) {
	initial := `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"first","reasoning_effort":"low","service_tier":"priority"}}}`
	for _, tc := range []struct {
		name    string
		records []string
		want    map[string]any
	}{
		{"idle changes win", []string{initial, `{"type":"turn_context","payload":{"model":"turn","effort":"high"}}`, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"idle","reasoning_effort":"medium","service_tier":"flex"}}}`, ""}, map[string]any{"model": "idle", "model_reasoning_effort": "medium", "service_tier": "flex"}},
		{"turn preserves last tier", []string{initial, `{"type":"turn_context","payload":{"model":"turn","effort":"high"}}`, ""}, map[string]any{"model": "turn", "model_reasoning_effort": "high", "service_tier": "priority"}},
		{"cleared defaults", []string{initial, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"idle"}}}`, ""}, map[string]any{"model": "idle", "model_reasoning_effort": nil, "service_tier": nil}},
		{"older history", []string{`{"type":"turn_context","payload":{"model":"legacy","effort":"low"}}`, ""}, map[string]any{"model": "legacy", "model_reasoning_effort": "low"}},
		{"partial and invalid records ignored", []string{initial, `{broken}`, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"partial","service_tier":"flex"}}}`}, map[string]any{"model": "first", "model_reasoning_effort": "low", "service_tier": "priority"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResumeSettings("saved", resumeSettingsRollout(t, tc.records...), time.Time{}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("saved settings: got %#v, want %#v", got, tc.want)
			}
		})
	}
	path := resumeSettingsRollout(t, initial, "")
	if got := ResumeSettings("other", path, time.Time{}); len(got) != 0 {
		t.Fatalf("borrowed another thread's settings: %#v", got)
	}
}

func TestResumeSettingsBound(t *testing.T) {
	path := resumeSettingsRollout(t, `{"type":"turn_context","payload":{"model":"too-old","effort":"high"}}`, "")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Seek(9<<20, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSettings("saved", path, time.Time{}); len(got) != 0 {
		t.Fatalf("read outside retention bound: %#v", got)
	}
	if _, err := f.WriteString(`{"type":"turn_context","payload":{"model":"retained","effort":"low"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSettings("saved", path, time.Time{}); got["model"] != "retained" {
		t.Fatalf("lost retained settings: %#v", got)
	}
}

func compressSettingsRollout(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	if err := os.WriteFile(path+".zst", encoder.EncodeAll(data, nil), 0600); err != nil {
		t.Fatal(err)
	}
	return path + ".zst"
}

func TestResumeSettingsCompressed(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint("direct=", direct), func(t *testing.T) {
			path := resumeSettingsRollout(t,
				`{"timestamp":"2026-01-01T00:00:00Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"old","reasoning_effort":"high","service_tier":"priority"}}}`,
				`{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"saved","reasoning_effort":null,"service_tier":null}}}`,
				`{"timestamp":"2026-01-01T00:00:02Z","type":"turn_context","payload":{"model":"latest","effort":"low"}}`,
				`{"type":"turn_context","payload":{"model":"unfinished"}}`)
			compressed := compressSettingsRollout(t, path)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			selected := path
			if direct {
				selected = compressed
			}
			cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			want := map[string]any{"model": "latest", "model_reasoning_effort": "low", "service_tier": nil}
			if got := ResumeSettings("saved", selected, cutoff); !reflect.DeepEqual(got, want) {
				t.Fatalf("compressed settings: got %#v, want %#v", got, want)
			}
			if got := ResumeSettings("other", selected, time.Time{}); len(got) != 0 {
				t.Fatalf("borrowed compressed identity: %#v", got)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("read materialized plain rollout: %v", err)
			}
			if _, err := os.Stat(compressed); err != nil {
				t.Fatalf("read removed compressed rollout: %v", err)
			}
		})
	}
}

func TestResumeSettingsCompressedAdmission(t *testing.T) {
	if got := ResumeSettings("saved", "relative.jsonl.zst", time.Time{}); len(got) != 0 {
		t.Fatalf("relative compressed path admitted: %#v", got)
	}
	if got := ResumeSettings("saved", t.TempDir()+".zst", time.Time{}); len(got) != 0 {
		t.Fatalf("missing compressed path admitted: %#v", got)
	}
	directory := filepath.Join(t.TempDir(), "directory.zst")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSettings("saved", directory, time.Time{}); len(got) != 0 {
		t.Fatalf("nonregular compressed path admitted: %#v", got)
	}
	path := resumeSettingsRollout(t, `{"type":"turn_context","payload":{"model":"compressed"}}`, "")
	compressed := compressSettingsRollout(t, path)
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"saved"}}`+"\n"+`{"type":"turn_context","payload":{"model":"plain"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSettings("saved", path, time.Time{}); got["model"] != "plain" {
		t.Fatalf("plain rollout did not take precedence: %#v", got)
	}
	if err := os.WriteFile(path, []byte("invalid plain"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ResumeSettings("saved", path, time.Time{}); len(got) != 0 {
		t.Fatalf("invalid plain borrowed compressed sibling: %#v", got)
	}
	data, err := os.ReadFile(compressed)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range [][]byte{[]byte("invalid frame"), data[:len(data)-1]} {
		if err := os.WriteFile(compressed, content, 0600); err != nil {
			t.Fatal(err)
		}
		if got := ResumeSettings("saved", compressed, time.Time{}); len(got) != 0 {
			t.Fatalf("corrupt compressed settings accepted: %#v", got)
		}
	}
}

func TestResumeSettingsCompressedBound(t *testing.T) {
	for _, padding := range []int{(8 << 20) - 64, 8 << 20, 2 * (8 << 20)} {
		t.Run(fmt.Sprint(padding), func(t *testing.T) {
			path := resumeSettingsRollout(t, `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"old","service_tier":"priority"}}}`, strings.Repeat("x", padding), `{"type":"turn_context","payload":{"model":"retained","effort":"low"}}`, "")
			plain := ResumeSettings("saved", path, time.Time{})
			compressed := compressSettingsRollout(t, path)
			if got := ResumeSettings("saved", compressed, time.Time{}); !reflect.DeepEqual(got, plain) || got["model"] != "retained" || got["service_tier"] != nil {
				t.Fatalf("decompressed tail differs: got %#v, plain %#v", got, plain)
			}
		})
	}
}

func TestResumeSettingsCompressedCutFragment(t *testing.T) {
	fragment := `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"fragment","service_tier":"priority"}}}`
	retained := `{"type":"turn_context","payload":{"model":"retained"}}`
	// The tail begins exactly at a valid-looking suffix of an invalid record.
	// It must not contribute a tier after the newer turn supplies the model.
	tail := fragment + "\n" + retained + "\n" + strings.Repeat("x", (8<<20)-len(fragment)-len(retained)-3) + "\n"
	path := resumeSettingsRollout(t, "x"+tail)
	compressed := compressSettingsRollout(t, path)
	want := map[string]any{"model": "retained", "model_reasoning_effort": nil}
	if got := ResumeSettings("saved", compressed, time.Time{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("cut fragment supplied settings: %#v", got)
	}
}
