package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestMekugiReplayStoreIgnoresLivePreviewStampOnRetry(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{
		ToolName: "exec", Script: "write file", CarrierPayload: "write file",
		ExecObservation: &execObservation{Files: []execFileSnapshot{{
			Path: "/workspace/file", Kind: execFileText, Content: "old", watchStamp: "live-only-stamp",
		}}},
	}
	put := func() error {
		return store.put(t.Context(), "/workspace", map[string]mekugiHistory{"call": history})
	}
	if err := put(); err != nil {
		t.Fatal(err)
	}
	if err := put(); err != nil {
		t.Fatalf("unchanged completed call conflicted after serialization: %v", err)
	}
	if history.ExecObservation.Files[0].watchStamp != "live-only-stamp" {
		t.Fatal("durable projection changed the active live-preview baseline")
	}
	history.ExecObservation.Files[0].Content = "changed"
	if err := put(); err == nil {
		t.Fatal("accepted changed captured content")
	}
}

func TestMekugiReplayStoreNormalizesDurableObservationOnRetry(t *testing.T) {
	for _, test := range []struct {
		name        string
		observation execObservation
	}{
		{name: "empty directory entries", observation: execObservation{
			Listings: []execListing{{Root: "/workspace/empty", Entries: map[string]string{}}},
		}},
		{name: "file-clock location", observation: execObservation{
			WindowStart: time.Unix(1_700_000_000, 0).In(time.FixedZone("zero-offset-local", 0)),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			history := mekugiHistory{Script: "unchanged", ExecObservation: &test.observation}
			for range 2 {
				if err := store.put(t.Context(), "/workspace", map[string]mekugiHistory{"call": history}); err != nil {
					t.Fatalf("unchanged observation conflicted after serialization: %v", err)
				}
			}
		})
	}
}

func TestMekugiReplayStoreAcceptsRetainedOffsetTimestamp(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1_700_000_000, 0).In(time.FixedZone("captured-offset", 8*60*60))
	history := mekugiHistory{Script: "same", ExecObservation: &execObservation{WindowStart: started}}
	if err := store.locked(t.Context(), func() error {
		return store.write(replayRecord{Version: 1, Workspace: "/workspace", CallID: "call", History: history})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), "/workspace", map[string]mekugiHistory{"call": history}); err != nil {
		t.Fatalf("retained equivalent timestamp conflicted: %v", err)
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

func TestTerminalResponseReplayPersistenceFailureIsClassified(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	transform, _, _, _ := newMekugiTestTransformWithProxy(t, proxy)
	transform.local["completed-call"] = mekugiHistory{Script: "completed"}
	if err := os.Symlink(filepath.Join(t.TempDir(), "absent"),
		filepath.Join(proxy.replayStore.directory, replayRecordName(transform.directory, "completed-call", false))); err != nil {
		t.Fatal(err)
	}
	_, err := transform.TransformSSE([]byte(`{"type":"response.completed","response":{"status":"completed","output":[]}}`))
	if err == nil {
		t.Fatal("terminal response claimed successful persistence")
	}
	diagnostic, ok := errors.AsType[*criticalDiagnosticError](err)
	if !ok || diagnostic.code != "replay_history_commit" || diagnostic.summary != "Mekugi could not persist completed response history" {
		t.Fatalf("terminal persistence diagnostic = %#v, wrapped = %v", diagnostic, err)
	}
	if transform.historyCommitted {
		t.Fatal("failed history was marked committed")
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

func TestReplayItemConflictReportsSafeCause(t *testing.T) {
	old := mekugiHistory{UpstreamItem: map[string]json.RawMessage{"input": mustMarshalJSON("private old script")}}
	next := mekugiHistory{UpstreamItem: map[string]json.RawMessage{"input": mustMarshalJSON("private new script")}}
	_, err := mergeReplayHistory(old, next)
	if err == nil {
		t.Fatal("accepted changed tool input")
	}
	issues := NewCriticalErrors()
	finalization := &requestFinalization{failurePhase: requestFailureTransform,
		observation: requestObservation{outcome: requestOutcomeFailed}}
	issues.record(finalization, err)
	notice := strings.Join(issues.Pending(), "\n")
	if finalization.diagnosticCode != "replay_item_conflict" ||
		!strings.Contains(notice, "changed a retained replay item field") ||
		strings.Contains(notice, "private") || strings.Contains(notice, "not safe for display") {
		t.Fatalf("unsafe or missing replay conflict diagnostic: %s", notice)
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
