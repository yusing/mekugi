package rollout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestReadObservationsRetainsOwnedSettingsContextAndCompactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	records := []string{
		`{"type":"session_meta","payload":{"id":"child"}}`,
		`{"type":"session_meta","payload":{"id":"parent"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":999}}}}`,
		`{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"child","thread_settings":{"model":"applied","reasoning_effort":"high","service_tier":"fast"}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":0},"model_context_window":10000}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"child","item":{"id":"one","type":"ContextCompaction"}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"child","item":{"id":"one","type":"ContextCompaction"}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"parent","item":{"id":"foreign","type":"ContextCompaction"}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"child","item":{"id":"failed","type":"ContextCompaction","status":"failed"}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := ReadObservations(t.Context(), "child", path)
	if got.Model == nil || *got.Model != "applied" || got.Effort == nil || *got.Effort != "high" || got.Tier == nil || *got.Tier != "fast" || got.ContextTokens == nil || *got.ContextTokens != 0 || got.Window == nil || *got.Window != 10000 || got.Compactions == nil || *got.Compactions != 1 {
		t.Fatalf("retained observations = %+v", got)
	}
	if got := ReadObservations(t.Context(), "foreign", path); got.Model != nil || got.ContextTokens != nil || got.Compactions != nil {
		t.Fatalf("foreign observations = %+v", got)
	}
	if err := os.WriteFile(path, []byte(records[0]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got = ReadObservations(t.Context(), "child", path)
	if got.Model != nil || got.ContextTokens != nil || got.Compactions == nil || *got.Compactions != 0 {
		t.Fatalf("empty history observations = %+v", got)
	}
	if err := os.WriteFile(path, []byte(records[0]+"\n{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ReadObservations(t.Context(), "child", path); got.Compactions != nil {
		t.Fatalf("partial history has complete count = %+v", got)
	}
}

func TestReadObservationsCompressedHistory(t *testing.T) {
	path := resumeSettingsRollout(t,
		`{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"saved","thread_settings":{"model":"actual","reasoning_effort":"high","service_tier":"flex"}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":456},"model_context_window":1000}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","thread_id":"saved","item":{"id":"compact","type":"ContextCompaction"}}}`)
	compressed := compressSettingsRollout(t, path)
	check := func(selected string) {
		t.Helper()
		got := ReadObservations(t.Context(), "saved", selected)
		if got.Model == nil || *got.Model != "actual" || got.ContextTokens == nil || *got.ContextTokens != 456 || got.Compactions == nil || *got.Compactions != 1 {
			t.Fatalf("compressed observations = %+v", got)
		}
	}
	check(compressed)
	if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"saved"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := ReadObservations(t.Context(), "saved", path); got.Model != nil {
		t.Fatal("compressed history overrode existing plain source")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check(path)
	if got := ReadObservations(t.Context(), "foreign", compressed); got.Model != nil || got.Compactions != nil {
		t.Fatal("borrowed foreign compressed history")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read materialized plain source: %v", err)
	}
}

func TestReadObservationsBoundsDecompressedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl.zst")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(file, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.Write([]byte(`{"type":"session_meta","payload":{"id":"saved"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	padding := []byte(`{"padding":"` + strings.Repeat("x", 1<<20) + "\"}\n")
	for range 65 {
		if _, err := encoder.Write(padding); err != nil {
			t.Fatal(err)
		}
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if got := ReadObservations(t.Context(), "saved", path); got.Compactions != nil {
		t.Fatalf("oversized source supplied complete count: %+v", got)
	}
}
