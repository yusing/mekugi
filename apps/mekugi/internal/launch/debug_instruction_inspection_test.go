package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDebugInstructionCompactInspectionBounds(t *testing.T) {
	dir := debugInspectionFixture(t)
	d := diagnosticDebugOutput(t)
	shared := strings.Repeat("指令", 100)
	d.Instructions(map[string]any{"request_id": "earlier", "instructions": shared, "developer_messages": strings.Repeat("private unrelated", 100)})
	d.Instructions(map[string]any{"request_id": "selected", "instructions": shared, "developer_messages": strings.Repeat("selected developer", 100)})
	data, err := os.ReadFile(d.Paths()[3])
	if err != nil {
		t.Fatal(err)
	}
	writeDebugInspectionFixture(t, filepath.Join(dir, "instructions.jsonl"), string(data))
	writeDebugInspectionFixture(t, filepath.Join(dir, "metrics.json"), `{"storage_writes":{"bytes":1234,"scope":"fixture"},"secret":"hidden metrics"}`)
	result, code, output, stderr := runDebugInspection(t, dir, "--request-id", "selected", "--field", "all", "--text-bytes", "17")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("evidence=%+v", result.Evidence)
	}
	text := result.Evidence[0].Text["instructions"]
	if text.Text == "" || len(text.Text) > 17 || !utf8.ValidString(text.Text) || text.Bytes != len(shared) || text.OmittedBytes != len(shared)-len(text.Text) {
		t.Fatalf("invalid text bounds: %+v", text)
	}
	if !strings.HasPrefix(shared, text.Text) {
		t.Fatal("resolved wrong content")
	}
	if strings.Contains(output, "private unrelated") || strings.Contains(output, "hidden metrics") {
		t.Fatal("unrelated text disclosed")
	}
	if string(result.Metrics["storage_writes"]) != `{"bytes":1234,"scope":"fixture"}` {
		t.Fatal("storage writes not exposed")
	}
	for _, artifact := range result.Artifacts {
		info, err := os.Stat(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		if artifact.Bytes == nil || *artifact.Bytes != info.Size() {
			t.Fatalf("artifact bytes: %+v", artifact)
		}
	}
}

func TestDebugInstructionCompactInvalidReferencesIsolated(t *testing.T) {
	for name, row := range map[string]string{
		"unknown schema": `{"schema":"future","request_id":"req-a","content_refs":{"instructions":"missing"}}`,
		"unknown field":  `{"schema":"mekugi.instructions.v2","request_id":"req-a","content_refs":{"wire_request":"missing"}}`,
		"dangling":       `{"schema":"mekugi.instructions.v2","request_id":"req-a","content_refs":{"instructions":"missing"}}`,
		"conflicting":    `{"schema":"mekugi.instructions.v2","request_id":"req-a","instructions":"inline","content_refs":{"instructions":"missing"}}`,
		"malformed":      `{"schema":"mekugi.instructions.v2","request_id":"req-a","content_refs":{"instructions":123}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := debugInspectionFixture(t)
			writeDebugInspectionFixture(t, filepath.Join(dir, "instructions.jsonl"), row+"\n"+`{"request_id":"req-a","instructions":"legacy still readable"}`+"\n")
			result, code, _, stderr := runDebugInspection(t, dir, "--request-id", "req-a", "--field", "all")
			if code != 1 || !strings.Contains(stderr, "instructions.jsonl") {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			for _, artifact := range result.Artifacts {
				want := "observed"
				if artifact.Name == "instructions.jsonl" {
					want = "invalid_or_unavailable"
				}
				if artifact.State != want {
					t.Fatalf("artifact failure leaked: %+v", artifact)
				}
			}
			found := false
			for _, entry := range result.Evidence {
				if entry.Text["instructions"].Text == "legacy still readable" {
					found = true
				}
			}
			if !found || result.Metrics == nil || result.TotalRequests != 1 {
				t.Fatal("valid independent evidence lost")
			}
		})
	}
}
