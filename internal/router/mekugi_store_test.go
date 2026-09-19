package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yusing/mekugi"
)

func TestMekugiReplayStoreRestartAndConflict(t *testing.T) {
	dir := t.TempDir()
	s, err := openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{Script: "original", CarrierPayload: "delivered", CarrierName: "exec", ReplayCarrier: true, bytes: 123, confirmed: true, sequence: 9}
	if err := s.put(t.Context(), "/workspace", map[string]mekugiHistory{"call": h}); err != nil {
		t.Fatal(err)
	}
	s, err = openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.lookup(t.Context(), "/workspace", "call")
	if err != nil || !ok || got.Script != h.Script || got.bytes != 0 || got.confirmed || got.sequence != 0 {
		t.Fatalf("lookup: %#v %v %v", got, ok, err)
	}
	if _, ok, err := s.lookup(t.Context(), "/other", "call"); err != nil || ok {
		t.Fatalf("workspace leak: %v %v", ok, err)
	}
	h.bytes, h.confirmed, h.sequence = 456, false, 10
	if err := s.put(t.Context(), "/workspace", map[string]mekugiHistory{"call": h}); err != nil {
		t.Fatalf("request-local state created a durable conflict: %v", err)
	}
	h.CarrierPayload = "changed"
	if err := s.put(t.Context(), "/workspace", map[string]mekugiHistory{"call": h}); err == nil {
		t.Fatal("accepted conflicting carrier")
	}
}

func TestMekugiReplayStoreConcurrencyAndCorruption(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			other, err := openMekugiReplayStore(dir)
			if err == nil {
				err = other.put(t.Context(), "/w", map[string]mekugiHistory{"c": {Script: "x"}})
			}
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := os.WriteFile(dir+"/"+replayRecordName("/w", "c", false), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.lookup(t.Context(), "/w", "c"); err == nil {
		t.Fatal("accepted corrupt record")
	}
}

func TestMekugiReplayStoreQuotaAndCommentary(t *testing.T) {
	s, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.putCommentary(t.Context(), "/w", []string{"id"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.hasCommentary(t.Context(), "/w", "id"); err != nil || !ok {
		t.Fatalf("membership %v %v", ok, err)
	}
	s.maxBytes = 1
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": {Script: "x"}}); err == nil {
		t.Fatal("accepted quota overflow")
	}
	if _, ok, err := s.lookup(t.Context(), "/w", "c"); err != nil || ok {
		t.Fatalf("partial record %v %v", ok, err)
	}
}

func TestMekugiReplayStoreProgressiveCompletion(t *testing.T) {
	s, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{Script: "x", UpstreamItem: map[string]json.RawMessage{"status": json.RawMessage(`"in_progress"`), "input": json.RawMessage(`"x"`)}, CommentaryMessageIDs: []string{"first"}}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err != nil {
		t.Fatal(err)
	}
	h.UpstreamItem["status"] = json.RawMessage(`"completed"`)
	h.UpstreamItem["id"] = json.RawMessage(`"item"`)
	h.CommentaryMessageIDs = []string{"second"}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.lookup(t.Context(), "/w", "c")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.UpstreamItem["status"]) != `"completed"` || len(got.CommentaryMessageIDs) != 2 {
		t.Fatalf("incomplete merge: %#v", got)
	}
	h.UpstreamItem["input"] = json.RawMessage(`"other"`)
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err == nil {
		t.Fatal("accepted changed model input")
	}
}

func TestMekugiReplayStoreRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, dir+"/linked"); err != nil {
		t.Fatal(err)
	}
	if _, err := openMekugiReplayStore(dir + "/linked"); err == nil {
		t.Fatal("accepted symlink directory")
	}
	s, err := openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target+"/outside", dir+"/"+replayRecordName("/w", "c", false)); err != nil {
		t.Fatal(err)
	}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": {Script: "x"}}); err == nil {
		t.Fatal("accepted symlink record")
	}
}

func TestMekugiReplayStorePreservesProtocolEncoding(t *testing.T) {
	dir := t.TempDir()
	s, err := openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{UpstreamItem: map[string]json.RawMessage{
		"input": json.RawMessage(`"<>& \u003c \\n \\u003e"`),
	}}
	before, err := marshalProtocolJSON(h.UpstreamItem)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err != nil {
		t.Fatal(err)
	}
	s, err = openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.lookup(t.Context(), "/w", "c")
	if err != nil || !ok {
		t.Fatalf("lookup: %v %v", ok, err)
	}
	after, err := marshalProtocolJSON(got.UpstreamItem)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("protocol spelling changed: %s -> %s", before, after)
	}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err != nil {
		t.Fatalf("identical retry conflicts: %v", err)
	}
}

func TestMekugiReplayStoreCancelledWrite(t *testing.T) {
	s, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.put(ctx, "/w", map[string]mekugiHistory{"c": {Script: "x"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if _, ok, err := s.lookup(t.Context(), "/w", "c"); err != nil || ok {
		t.Fatalf("cancelled write retained record: %v %v", ok, err)
	}
}

func TestMekugiReplayStoreSymlinkAncestorCreatesNothing(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, dir+"/linked"); err != nil {
		t.Fatal(err)
	}
	if _, err := openMekugiReplayStore(dir + "/linked/new/replay"); err == nil {
		t.Fatal("accepted symlink ancestor")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("created directories through symlink before rejecting it")
	}
}

func TestMekugiReplayStoreCommentaryCannotConsumeCallQuota(t *testing.T) {
	s, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	call := replayRecord{Version: 1, Workspace: "/w", CallID: "c", History: durableHistory(mekugiHistory{Script: "x"})}
	encoded, err := marshalProtocolJSON(call)
	if err != nil {
		t.Fatal(err)
	}
	s.maxBytes = int64(len(encoded))
	if err := s.putCommentary(t.Context(), "/w", []string{"commentary"}); err != nil {
		t.Fatal(err)
	}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": {Script: "x"}}); err != nil {
		t.Fatalf("commentary displaced replay: %v", err)
	}
	s.maxCommentaryBytes = 1
	if err := s.putCommentary(t.Context(), "/w", []string{"another"}); err == nil {
		t.Fatal("accepted commentary capacity overflow")
	}
	if _, ok, err := s.lookup(t.Context(), "/w", "c"); err != nil || !ok {
		t.Fatalf("commentary overflow lost replay: %v %v", ok, err)
	}
}

func TestDefaultMekugiReplayDirectory(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	got, err := defaultMekugiReplayDirectory()
	if err != nil || got != filepath.Join(state, "mekugi", "replay") {
		t.Fatalf("explicit state path = %q, %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "relative-state")
	if _, err := defaultMekugiReplayDirectory(); err == nil {
		t.Fatal("accepted relative state home")
	}
	t.Setenv("XDG_STATE_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err = defaultMekugiReplayDirectory()
	if err != nil || got != filepath.Join(home, ".local", "state", "mekugi", "replay") {
		t.Fatalf("fallback state path = %q, %v", got, err)
	}
}

func TestMekugiReplayStoreStructuredFieldWhitespaceRetry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "state", "replay")
	s, err := openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{UpstreamItem: map[string]json.RawMessage{
		"extension": json.RawMessage(`{ "x": 1, "values": [ "<>&", "\u003c", 2 ] }`),
	}}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err != nil {
		t.Fatal(err)
	}
	s, err = openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err != nil {
		t.Fatalf("unchanged structured field conflicts on retry: %v", err)
	}
	got, ok, err := s.lookup(t.Context(), "/w", "c")
	if err != nil || !ok {
		t.Fatalf("lookup: %v %v", ok, err)
	}
	before, err := marshalProtocolJSON(h.UpstreamItem)
	if err != nil {
		t.Fatal(err)
	}
	after, err := marshalProtocolJSON(got.UpstreamItem)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("provider encoding changed: %s -> %s", before, after)
	}
	h.UpstreamItem["extension"] = json.RawMessage(`{"x":1,"values":["<>&","<",2]}`)
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"c": h}); err == nil {
		t.Fatal("accepted changed escape spelling")
	}
}

func TestMekugiReplayStoreProviderMetadataCompletion(t *testing.T) {
	dir := t.TempDir()
	s, err := openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{Script: "echo test", UpstreamItem: map[string]json.RawMessage{
		"id": json.RawMessage(`"item"`), "call_id": json.RawMessage(`"call"`),
		"name": json.RawMessage(`"shell"`), "type": json.RawMessage(`"custom_tool_call"`),
		"input": json.RawMessage(`"echo test"`), "status": json.RawMessage(`"in_progress"`),
		"internal_chat_message_metadata_passthrough": json.RawMessage(`{"phase":"started"}`),
	}}
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"call": h}); err != nil {
		t.Fatal(err)
	}
	metadata := json.RawMessage(`{"phase":"finished","opaque":"\u003c"}`)
	h.UpstreamItem["internal_chat_message_metadata_passthrough"] = metadata
	h.UpstreamItem["status"] = json.RawMessage(`"completed"`)
	if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"call": h}); err != nil {
		t.Fatalf("provider metadata completion: %v", err)
	}
	s, err = openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := s.lookup(t.Context(), "/w", "call")
	if err != nil || !found {
		t.Fatalf("restart lookup: %v, %v", found, err)
	}
	if !bytes.Equal(got.UpstreamItem["internal_chat_message_metadata_passthrough"], metadata) {
		t.Fatal("replay did not retain exact completed provider metadata")
	}
	for _, key := range []string{"id", "call_id", "name", "type", "input", "status"} {
		t.Run(key, func(t *testing.T) {
			original := h.UpstreamItem[key]
			h.UpstreamItem[key] = json.RawMessage(`"changed"`)
			defer func() { h.UpstreamItem[key] = original }()
			if err := s.put(t.Context(), "/w", map[string]mekugiHistory{"call": h}); err == nil {
				t.Fatalf("accepted changed %s", key)
			}
		})
	}
}

func TestReplayHistoryVersionOneWireCompatibility(t *testing.T) {
	// Freeze the pre-simplification wire schema, independent of the current type.
	const legacy = `{"Version":1,"Workspace":"/workspace","CallID":"call","Commentary":false,"History":{"ToolName":"hpatch","PluginID":"builtin","Script":"emitted","Root":"/workspace","Evaluated":"evaluated","ChangeID":"hp_1","ReviewFiles":[{"BeforePath":"old","AfterPath":"new","Diff":"diff"}],"Patch":"patch","Applied":true,"CarrierName":"exec","CarrierKind":"custom","CarrierPayload":"carrier","Report":"report","JournalIDs":["j1"],"OutputWarning":"warning","TranslationError":"rejected","EvaluatorRejected":true,"Rejections":[{"command":2,"source_line":3,"operation":"type","reason":"row-stale"}],"CorrelationID":"correlation","Attempt":2,"UpstreamItem":{"input":"<>& \\u003c","status":"completed"},"ReplayCarrier":true,"CommentaryMessageIDs":["notice"],"Unevaluated":true,"AlreadySatisfied":true,"Aliases":[{"Path":"new","Before":"1:abcd","After":"2:abcd"}]}}`
	want := mekugiHistory{
		ToolName: "hpatch", PluginID: "builtin", Script: "emitted", Root: "/workspace",
		Evaluated: "evaluated", ChangeID: "hp_1",
		ReviewFiles: []mekugi.ReviewFile{{BeforePath: "old", AfterPath: "new", Diff: "diff"}},
		Patch:       "patch", Applied: true, CarrierName: "exec", CarrierKind: codeModeCarrierCustom,
		CarrierPayload: "carrier", Report: "report", JournalIDs: []string{"j1"},
		OutputWarning: "warning", TranslationError: "rejected", EvaluatorRejected: true,
		Rejections:    []mekugi.HostRejection{{Command: 2, SourceLine: 3, Operation: "type", Reason: "row-stale"}},
		CorrelationID: "correlation", Attempt: 2,
		UpstreamItem: map[string]json.RawMessage{
			"input": json.RawMessage(`"<>& \\u003c"`), "status": json.RawMessage(`"completed"`),
		},
		ReplayCarrier: true, CommentaryMessageIDs: []string{"notice"},
		Unevaluated: true, AlreadySatisfied: true,
		Aliases: []mekugi.TargetAlias{{Path: "new", Before: "1:abcd", After: "2:abcd"}},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, replayRecordName("/workspace", "call", false))
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := store.lookup(t.Context(), "/workspace", "call")
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy lookup = %#v, %v, %v; want %#v", got, found, err, want)
	}

	got.bytes, got.confirmed, got.sequence = 123, true, 42
	persistent := durableHistory(got)
	if !reflect.DeepEqual(persistent, want) || got.bytes != 123 || !got.confirmed || got.sequence != 42 {
		t.Fatal("durable projection retained local state or mutated the request view")
	}
	record := replayRecord{Version: 1, Workspace: "/workspace", CallID: "call", History: persistent}
	encoded, err := marshalProtocolJSON(record)
	if err != nil || !sameJSONValue(encoded, []byte(legacy)) {
		t.Fatalf("wire schema changed: %s, %v", encoded, err)
	}
	if err := store.locked(t.Context(), func() error { return store.write(record) }); err != nil {
		t.Fatalf("local confirmation/order created a durable conflict: %v", err)
	}
	for _, field := range []string{"bytes", "confirmed", "sequence", "Bytes", "Confirmed", "Sequence", "FutureField"} {
		t.Run(field, func(t *testing.T) {
			corrupt := strings.Replace(legacy, `"ToolName":`, `"`+field+`":1,"ToolName":`, 1)
			if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.lookup(t.Context(), "/workspace", "call"); err == nil {
				t.Fatal("accepted an unknown or request-local durable field")
			}
		})
	}
}
