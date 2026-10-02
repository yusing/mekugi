package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/persistence"
)

func compactInstructionWriter(t *testing.T) *debugOutput {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "instructions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return &debugOutput{dump: file, writes: new(persistence.Counter)}
}

func compactInstructionRows(t *testing.T, d *debugOutput) ([]map[string]jsontext.Value, [][]byte) {
	t.Helper()
	data, err := os.ReadFile(d.dump.Name())
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]jsontext.Value
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	for _, line := range lines {
		var row map[string]jsontext.Value
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows, lines
}

func TestDebugInstructionCompactRoundTrip(t *testing.T) {
	d := compactInstructionWriter(t)
	keys := []string{"instructions", "developer_messages", "tools", "additional_tools", "wire_developer_messages", "wire_additional_tools"}
	original := map[string]jsontext.Value{}
	for _, key := range keys {
		raw, err := json.Marshal(strings.Repeat(key+" ", 100))
		if err != nil {
			t.Fatal(err)
		}
		original[key] = raw
	}
	for i := range 3 {
		fields := map[string]any{"request_id": fmt.Sprintf("req-%d", i), "model": "test"}
		for key, value := range original {
			fields[key] = value
		}
		if i == 2 {
			fields["tools"] = strings.Repeat("changed tool ", 100)
		}
		d.writeInstructions(fields)
	}
	if d.err != nil {
		t.Fatal(d.err)
	}
	rows, lines := compactInstructionRows(t, d)
	if len(rows) != 3 {
		t.Fatalf("got %d request rows", len(rows))
	}
	if len(lines[1])*2 >= len(lines[0]) {
		t.Fatalf("repetition not substantially reduced: %d -> %d", len(lines[0]), len(lines[1]))
	}
	var refs map[string]string
	if err := json.Unmarshal(rows[1]["content_refs"], &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs) != len(keys) {
		t.Fatalf("references: %v", refs)
	}
	for _, key := range keys {
		digest := sha256.Sum256(original[key])
		if refs[key] != hex.EncodeToString(digest[:]) || rows[1][key] != nil {
			t.Fatalf("wrong reference for %s", key)
		}
	}
	if rows[2]["tools"] == nil {
		t.Fatal("changed tools must remain inline")
	}
	var decoder debugInstructionDecoder
	for i, row := range rows {
		if debugInspectionString(row, "schema") != "mekugi.instructions.v2" || debugInspectionString(row, "request_id") != fmt.Sprintf("req-%d", i) {
			t.Fatalf("request identity/schema lost: %v", row)
		}
		if err := decoder.resolve(row); err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			if i == 2 && key == "tools" {
				continue
			}
			if !bytes.Equal(row[key], original[key]) {
				t.Fatalf("row %d changed %s", i, key)
			}
		}
		if bytes.Equal(row["developer_messages"], row["wire_developer_messages"]) {
			t.Fatal("projection and wire values collapsed")
		}
	}
	if bytes.Equal(rows[2]["tools"], original["tools"]) {
		t.Fatal("changed tools lost during resolution")
	}
}

func TestDebugInstructionCompactSameRowAndThreshold(t *testing.T) {
	for _, size := range []int{126, 127} { // JSON string encoding adds two bytes.
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d := compactInstructionWriter(t)
			value := strings.Repeat("a", size)
			d.writeInstructions(map[string]any{"request_id": "same-row", "instructions": value, "tools": value})
			rows, _ := compactInstructionRows(t, d)
			var refs map[string]string
			if raw := rows[0]["content_refs"]; raw != nil {
				if err := json.Unmarshal(raw, &refs); err != nil {
					t.Fatal(err)
				}
			}
			if (refs["tools"] != "") != (size == 127) {
				t.Fatalf("encoded-size threshold: %v", refs)
			}
			var decoder debugInstructionDecoder
			if err := decoder.resolve(rows[0]); err != nil {
				t.Fatal(err)
			}
			if debugInspectionString(rows[0], "tools") != value {
				t.Fatal("same-row content was not resolved")
			}
		})
	}
}

func TestDebugInstructionCompactDictionaryLimit(t *testing.T) {
	d := compactInstructionWriter(t)
	for i := range 4097 {
		d.writeInstructions(map[string]any{"request_id": fmt.Sprint(i), "instructions": fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 130))})
	}
	d.writeInstructions(map[string]any{"request_id": "overflow-repeat", "instructions": "4096-" + strings.Repeat("x", 130)})
	d.writeInstructions(map[string]any{"request_id": "known-repeat", "instructions": "0000-" + strings.Repeat("x", 130)})
	if d.err != nil {
		t.Fatal(d.err)
	}
	if len(d.instructionContent) != 4096 {
		t.Fatalf("dictionary has %d entries", len(d.instructionContent))
	}
	rows, _ := compactInstructionRows(t, d)
	if rows[4097]["instructions"] == nil || rows[4097]["content_refs"] != nil {
		t.Fatal("unknown overflow value did not fall back inline")
	}
	if rows[4098]["instructions"] != nil || rows[4098]["content_refs"] == nil {
		t.Fatal("known content stopped deduplicating at capacity")
	}
	var decoder debugInstructionDecoder
	for _, row := range rows {
		if err := decoder.resolve(row); err != nil {
			t.Fatal(err)
		}
	}
	if len(decoder.content) != 4096 {
		t.Fatalf("decoder dictionary has %d entries", len(decoder.content))
	}
	if debugInspectionString(rows[4098], "instructions") != "0000-"+strings.Repeat("x", 130) {
		t.Fatal("known reference resolved incorrectly")
	}
}

func TestDebugInstructionCompactInspectionBounds(t *testing.T) {
	dir := debugInspectionFixture(t)
	d := compactInstructionWriter(t)
	shared := strings.Repeat("指令", 100)
	d.writeInstructions(map[string]any{"request_id": "earlier", "instructions": shared, "developer_messages": strings.Repeat("private unrelated", 100)})
	d.writeInstructions(map[string]any{"request_id": "selected", "instructions": shared, "developer_messages": strings.Repeat("selected developer", 100)})
	data, err := os.ReadFile(d.dump.Name())
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

func TestDebugInstructionCompactConcurrentRecords(t *testing.T) {
	d := compactInstructionWriter(t)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			d.writeInstructions(map[string]any{"request_id": fmt.Sprint(i), "instructions": strings.Repeat("shared", 100), "tools": fmt.Sprintf("tool-%d-%s", i, strings.Repeat("x", 200))})
		})
	}
	wg.Wait()
	if d.err != nil {
		t.Fatal(d.err)
	}
	rows, _ := compactInstructionRows(t, d)
	if len(rows) != 32 {
		t.Fatalf("got %d records", len(rows))
	}
	seen := map[string]bool{}
	var decoder debugInstructionDecoder
	for _, row := range rows {
		if err := decoder.resolve(row); err != nil {
			t.Fatal(err)
		}
		id := debugInspectionString(row, "request_id")
		if seen[id] || debugInspectionString(row, "tools") != "tool-"+id+"-"+strings.Repeat("x", 200) || debugInspectionString(row, "instructions") != strings.Repeat("shared", 100) {
			t.Fatalf("mixed/lost request %q", id)
		}
		seen[id] = true
	}
}

func TestDebugInstructionCompactWriteFailure(t *testing.T) {
	d := compactInstructionWriter(t)
	if err := d.dump.Close(); err != nil {
		t.Fatal(err)
	}
	d.writeInstructions(map[string]any{"request_id": "failed", "instructions": strings.Repeat("x", 200)})
	if d.err == nil {
		t.Fatal("write failure not retained")
	}
	firstErr := d.err
	d.writeInstructions(map[string]any{"request_id": "later", "instructions": strings.Repeat("y", 200)})
	if d.err != firstErr || d.writes.Snapshot().Bytes != 0 {
		t.Fatal("failed writer continued or claimed successful writes")
	}
	data, err := os.ReadFile(d.dump.Name())
	if err != nil || len(data) != 0 {
		t.Fatalf("failed write published records: %q %v", data, err)
	}
}
