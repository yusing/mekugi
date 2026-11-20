package nativetrace

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/yusing/mekugi/libraries/commandanalysis"
)

func TestV3LiveShardConfirmsNestedCalls(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "trace-native")
	if err := os.MkdirAll(filepath.Join(bundle, "payloads"), 0700); err != nil {
		t.Fatal(err)
	}
	events, err := os.Create(filepath.Join(bundle, "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	shard, err := os.Create(filepath.Join(bundle, "payloads", "000001.zst"))
	if err != nil {
		t.Fatal(err)
	}
	defer shard.Close()
	writer, err := zstd.NewWriter(shard, zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	var seq uint64
	appendEvent := func(payload string) {
		t.Helper()
		seq++
		if _, err := fmt.Fprintf(events, "{\"schema_version\":3,\"seq\":%d,\"thread_id\":\"thread\",\"codex_turn_id\":\"turn\",\"payload\":%s}\n", seq, payload); err != nil {
			t.Fatal(err)
		}
	}
	appendPayload := func(id, body string) string {
		t.Helper()
		record := binary.LittleEndian.AppendUint16(nil, uint16(len(id)))
		record = binary.LittleEndian.AppendUint64(record, uint64(len(body)))
		record = append(record, id...)
		record = append(record, body...)
		if _, err := writer.Write(record); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf(`{"raw_payload_id":%q,"kind":{"type":"tool_invocation"},"shard":1}`, id)
	}

	appendEvent(`{"type":"code_cell_started","runtime_cell_id":"cell","model_visible_call_id":"call","source_js":"source"}`)
	invocation := appendPayload("raw_payload:1", `{"tool_name":"exec_command","tool_namespace":"functions","payload":{"type":"function","arguments":"{\"cmd\":\"exit 3\",\"timeout_ms\":1234}"}}`)
	appendEvent(`{"type":"tool_call_started","tool_call_id":"shell","requester":{"type":"code_cell","runtime_cell_id":"cell"},"invocation_payload":` + invocation + `}`)
	reader := NewWithNotice(root, func(message string) { t.Error(message) })
	if call, ok := reader.ReadTool("thread", "turn", "shell", "call", "source"); !ok || call.Command.Command != "exit 3" {
		t.Fatalf("command must be readable before completion: %+v, %t", call, ok)
	}
	if timeout, ok := reader.CommandTimeout("thread", "shell"); !ok || timeout != 1234 {
		t.Fatalf("authored timeout = %d, %t", timeout, ok)
	}

	yielded := appendPayload("raw_payload:2", `{"type":"code_mode_response","value":{"session_id":123}}`)
	appendEvent(`{"type":"tool_call_ended","tool_call_id":"shell","status":"completed","result_payload":` + yielded + `}`)
	if cell := reader.ReadCell("thread", "call", "source"); !cell.Pending() {
		t.Fatal("yielded command is not pending")
	}
	runtimeResult := appendPayload("raw_payload:3", `{"exit_code":3}`)
	appendEvent(`{"type":"tool_call_runtime_ended","tool_call_id":"shell","status":"failed","runtime_payload":` + runtimeResult + `}`)
	const patchInvocation = `{"tool_name":"apply_patch","tool_namespace":"functions","payload":{"type":"custom","input":"patch body"}}`
	patch := appendPayload("raw_payload:4", patchInvocation)
	appendEvent(`{"type":"tool_call_started","tool_call_id":"patch","requester":{"type":"code_cell","runtime_cell_id":"cell"},"invocation_payload":` + patch + `}`)
	appendEvent(`{"type":"tool_call_ended","tool_call_id":"patch","status":"completed"}`)
	abortedPatch := appendPayload("raw_payload:5", patchInvocation)
	appendEvent(`{"type":"tool_call_started","tool_call_id":"aborted-patch","requester":{"type":"code_cell","runtime_cell_id":"cell"},"invocation_payload":` + abortedPatch + `}`)
	appendEvent(`{"type":"tool_call_ended","tool_call_id":"aborted-patch","status":"aborted"}`)
	appendEvent(`{"type":"code_cell_ended","runtime_cell_id":"cell"}`)

	// The frame is still open: both observations must work during the session.
	cell := reader.ReadCell("thread", "call", "source")
	if !cell.Ended() || cell.Pending() {
		t.Fatal("aborted patch kept the ended cell pending")
	}
	if patch, ok := cell.Patch("patch body", 0); !ok || patch.CallID != "patch" || patch.Status != "completed" {
		t.Fatalf("nested patch not confirmed: %+v, %t", patch, ok)
	}
	if patch, ok := cell.Patch("patch body", 1); !ok || patch.CallID != "aborted-patch" || patch.Status != "aborted" || patch.ExitCode != nil {
		t.Fatalf("aborted patch status not retained: %+v, %t", patch, ok)
	}
	commands, ok := cell.Commands([]commandanalysis.Command{{Command: "exit 3"}}, "/work")
	if !ok || len(commands) != 1 || commands[0].ExitCode == nil || *commands[0].ExitCode != 3 {
		t.Fatalf("nested command exit not confirmed: %+v, %t", commands, ok)
	}

	// An omitted input could be an earlier occurrence of any call in that
	// cell. Do not guess which occurrence ran, but keep the first cell.
	appendEvent(`{"type":"code_cell_started","runtime_cell_id":"other","model_visible_call_id":"other-call","source_js":"other source"}`)
	appendEvent(`{"type":"tool_call_started","tool_call_id":"unknown","requester":{"type":"code_cell","runtime_cell_id":"other"},"invocation_payload":{"raw_payload_id":"raw_payload:6","shard":null,"capture":{"status":"omitted","original_bytes":100}}}`)
	otherPatch := appendPayload("raw_payload:7", patchInvocation)
	appendEvent(`{"type":"tool_call_started","tool_call_id":"other-patch","requester":{"type":"code_cell","runtime_cell_id":"other"},"invocation_payload":` + otherPatch + `}`)
	appendEvent(`{"type":"tool_call_ended","tool_call_id":"other-patch","status":"completed"}`)
	appendEvent(`{"type":"code_cell_ended","runtime_cell_id":"other"}`)
	if _, ok := reader.ReadCell("thread", "other-call", "other source").Patch("patch body", 0); ok {
		t.Fatal("a cell with an omitted invocation must not confirm a patch occurrence")
	}
	if patch, ok := reader.ReadCell("thread", "call", "source").Patch("patch body", 0); !ok || patch.CallID != "patch" {
		t.Fatalf("an omitted invocation in another cell hid valid evidence: %+v, %t", patch, ok)
	}

	// Once sealed, a corrupt checksum invalidates the shard even if earlier
	// blocks decoded to otherwise usable tool records.
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := shard.Stat()
	if err != nil {
		t.Fatal(err)
	}
	var checksumByte [1]byte
	if _, err := shard.ReadAt(checksumByte[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	checksumByte[0] ^= 1
	if _, err := shard.WriteAt(checksumByte[:], info.Size()-1); err != nil {
		t.Fatal(err)
	}
	if cell := NewWithNotice(root, nil).ReadCell("thread", "call", "source"); cell != nil {
		t.Fatal("a corrupt sealed shard must never confirm evidence")
	}
}
