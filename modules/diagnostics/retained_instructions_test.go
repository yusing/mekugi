package diagnostics_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const (
	retainedBase  = `"base guidance\n<mekugi_environment_facts>\nworkspace facts\n</mekugi_environment_facts>"`
	retainedTool  = `{"type":"function","name":"exec","description":"run commands"}`
	retainedWait  = `{"type":"function","name":"wait","description":"wait for agents"}`
	retainedRules = `{"type":"message","role":"developer","content":"permissions"}`
	retainedNote  = `{"type":"message","role":"developer","content":"context notice"}`
)

// staticContent is a fixed request projection.
type staticContent struct {
	instructions string
	developers   []string
	tools        []string
}

func (c *staticContent) visit(visitor diagnostic.InstructionVisitor) error {
	if c.instructions != "" {
		visitor.Visit(diagnostic.InstructionText, jsontext.Value(c.instructions))
	}
	for _, developer := range c.developers {
		visitor.Visit(diagnostic.DeveloperMessage, jsontext.Value(developer))
	}
	for _, tool := range c.tools {
		visitor.Visit(diagnostic.ToolDescription, jsontext.Value(tool))
	}
	return nil
}

func contentRequest(thread, turn string, content *staticContent) diagnostic.InstructionRequest {
	return diagnostic.InstructionRequest{Thread: thread, Model: "model-a", TurnID: turn,
		Time: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Content: content.visit}
}

func instructionRequest(thread, turn string, developers ...string) diagnostic.InstructionRequest {
	return contentRequest(thread, turn, &staticContent{instructions: retainedBase, developers: developers, tools: []string{retainedTool, retainedWait}})
}

func contentName(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "instruction-content-" + hex.EncodeToString(digest[:]) + ".json"
}

func contentDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func retainedNames(t *testing.T, directory, prefix string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, prefix+"*.json"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, filepath.Base(match))
	}
	return names
}

func TestRetainedInstructionsPublishOnlyChangedContent(t *testing.T) {
	service := newDiagnostics(t)
	root := t.TempDir()
	directory := filepath.Join(root, "replay")
	store, err := service.OpenFailures(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	writes := new(persistence.Counter)
	retention := service.RetainInstructions(store, writes, history.Limits{})
	observe := func(retention *diagnostic.InstructionRetention, request diagnostic.InstructionRequest) {
		t.Helper()
		if err := retention.Observe(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	observe(retention, instructionRequest("thread-a", "turn-1", retainedRules))
	if got := len(retainedNames(t, directory, "instruction-content-")); got != 4 {
		t.Fatalf("first revision content files = %d, want 4", got)
	}
	written := writes.Snapshot().Bytes
	observe(retention, instructionRequest("thread-a", "turn-2", retainedRules))
	if writes.Snapshot().Bytes != written {
		t.Fatal("unchanged request published storage")
	}
	observe(retention, instructionRequest("thread-a", "turn-3", retainedRules, retainedNote))
	if got := len(retainedNames(t, directory, "instruction-content-")); got != 5 {
		t.Fatalf("changed revision content files = %d, want only one added", got)
	}
	observe(retention, instructionRequest("thread-a", "turn-4", retainedRules))

	// A restarted invocation confirms the stored active revision without writing.
	written = writes.Snapshot().Bytes
	restarted := service.RetainInstructions(store, writes, history.Limits{})
	observe(restarted, instructionRequest("thread-a", "turn-5", retainedRules))
	if writes.Snapshot().Bytes != written {
		t.Fatal("restart republished the active revision")
	}
	// Another thread shares content records but owns its own index.
	observe(restarted, instructionRequest("thread-b", "turn-b", retainedRules))
	if got := len(retainedNames(t, directory, "instruction-content-")); got != 5 {
		t.Fatalf("shared content files = %d, want 5", got)
	}
	catalog, err := store.ReadCatalog(history.SessionName("thread-b"))
	if err != nil || !catalog.Files[contentName(retainedTool)] {
		t.Fatalf("thread-b catalog does not own shared content: %+v %v", catalog, err)
	}
	if got := len(retainedNames(t, directory, "instruction-revision-")); got != 2 {
		t.Fatalf("revision records = %d, want 2 shared across threads", got)
	}
	// The index grows by revision and use entries, not by repeated content digests.
	index, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("instructions-%x.json", sha256.Sum256([]byte("thread-a")))))
	if err != nil || strings.Contains(string(index), contentDigest(retainedTool)) || strings.Contains(string(index), "request_sequence") {
		t.Fatalf("index = %s, %v", index, err)
	}

	before := diagnosticsTree(t, root)
	reader := newDiagnostics(t)
	report, err := reader.ReadInstructions(t.Context(), directory, "thread-a", false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != diagnostic.InstructionsAvailable || len(report.Revisions) != 2 || len(report.Content) != 5 {
		t.Fatalf("report = %+v", report)
	}
	var uses []int
	var turns []string
	for _, use := range report.Uses {
		uses, turns = append(uses, use.Revision), append(turns, use.TurnID)
	}
	if !reflect.DeepEqual(uses, []int{1, 2, 1}) || !reflect.DeepEqual(turns, []string{"turn-1", "turn-3", "turn-4"}) || report.Uses[0].Model != "model-a" {
		t.Fatalf("uses = %+v", report.Uses)
	}
	second := report.Revisions[1]
	if second.Revision != 2 || second.Instructions != contentDigest(retainedBase) ||
		!reflect.DeepEqual(second.DeveloperMessages, []string{contentDigest(retainedRules), contentDigest(retainedNote)}) ||
		!reflect.DeepEqual(second.Tools, []string{contentDigest(retainedTool), contentDigest(retainedWait)}) {
		t.Fatalf("second revision = %+v", second)
	}
	var base string
	if err := json.Unmarshal([]byte(retainedBase), &base); err != nil {
		t.Fatal(err)
	}
	if text := report.Content[second.Instructions]; text.Text != "" || text.Bytes != len(base) || text.OmittedBytes != len(base) {
		t.Fatalf("unselected instruction text = %+v", text)
	}
	selected, err := reader.ReadInstructions(t.Context(), directory, "thread-a", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if text := selected.Content[second.Instructions]; text.Text != base[:10] || text.OmittedBytes != len(base)-10 {
		t.Fatalf("selected instruction text = %+v", text)
	}
	if text := selected.Content[contentDigest(retainedTool)]; text.Text != retainedTool[:10] {
		t.Fatalf("selected tool text = %+v", text)
	}
	missing, err := reader.ReadInstructions(t.Context(), directory, "unknown-thread", true, 4096)
	if err != nil || missing.Status != diagnostic.InstructionsUnavailable || missing.Reason == "" || missing.Revisions != nil {
		t.Fatalf("unknown thread = %+v %v", missing, err)
	}
	if !reflect.DeepEqual(before, diagnosticsTree(t, root)) {
		t.Fatal("instruction inspection mutated storage")
	}
}

func TestRetainedInstructionsRejectInvalidContent(t *testing.T) {
	service := newDiagnostics(t)
	directory := t.TempDir()
	store, err := service.OpenFailures(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RetainInstructions(store, nil, history.Limits{}).Observe(t.Context(), instructionRequest("thread", "turn", retainedRules)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, contentName(retainedTool))
	if err := os.WriteFile(path, []byte(`{"name":"substituted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadInstructions(t.Context(), directory, "thread", false, 4096); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("substituted content error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadInstructions(t.Context(), directory, "thread", false, 4096); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing content error = %v", err)
	}
}

func TestRetainedInstructionsReadNonCanonicalContent(t *testing.T) {
	for _, tc := range []struct {
		name, value, text string
		instruction       bool
	}{
		{name: "duplicate names", value: `{"role":"developer","content":"a","content":"b"}`, text: `{"role":"developer","content":"a","content":"b"}`},
		{name: "unpaired surrogate", value: `"bad\ud800text"`, text: "bad\ufffdtext", instruction: true},
		{name: "invalid UTF-8 string", value: "\"bad\xfftext\"", text: "bad\ufffdtext", instruction: true},
		{name: "invalid UTF-8 object", value: "{\"role\":\"developer\",\"content\":\"bad\xfftext\"}", text: "{\"role\":\"developer\",\"content\":\"bad\ufffdtext\"}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newDiagnostics(t)
			directory := t.TempDir()
			store, err := service.OpenFailures(t.Context(), directory)
			if err != nil {
				t.Fatal(err)
			}
			content := &staticContent{developers: []string{tc.value}}
			if tc.instruction {
				content = &staticContent{instructions: tc.value}
			}
			if err := service.RetainInstructions(store, nil, history.Limits{}).Observe(t.Context(), contentRequest("thread", "turn", content)); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(tc.value)))
			stored, err := os.ReadFile(filepath.Join(directory, "instruction-content-"+digest+".json"))
			if err != nil || string(stored) != tc.value {
				t.Fatalf("non-canonical content changed: %q, %v", stored, err)
			}
			for _, includeText := range []bool{false, true} {
				report, err := newDiagnostics(t).ReadInstructions(t.Context(), directory, "thread", includeText, 4096)
				if err != nil || report.Status != diagnostic.InstructionsAvailable {
					t.Fatalf("instruction report = %+v, %v", report, err)
				}
				if includeText && report.Content[digest].Text != tc.text {
					t.Fatalf("instruction text = %q, want %q", report.Content[digest].Text, tc.text)
				}
				if _, err := json.Marshal(&report); err != nil {
					t.Fatalf("encode instruction report: %v", err)
				}
			}
		})
	}
}

func TestRetainedInstructionsCapacityRetriesLaterRequest(t *testing.T) {
	service := newDiagnostics(t)
	directory := t.TempDir()
	store, err := service.OpenFailures(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	limited := service.RetainInstructions(store, nil, history.Limits{Total: 1})
	err = limited.Observe(t.Context(), instructionRequest("thread", "turn-1", retainedRules))
	if _, ok := errors.AsType[*history.CapacityError](err); !ok {
		t.Fatalf("capacity error = %v", err)
	}
	if report, err := service.ReadInstructions(t.Context(), directory, "thread", false, 4096); err != nil || report.Status != diagnostic.InstructionsUnavailable {
		t.Fatalf("rejected publication became readable: %+v %v", report, err)
	}
	ample := service.RetainInstructions(store, nil, history.Limits{})
	if err := ample.Observe(t.Context(), instructionRequest("thread", "turn-2", retainedRules)); err != nil {
		t.Fatal(err)
	}
	report, err := service.ReadInstructions(t.Context(), directory, "thread", false, 4096)
	if err != nil || report.Status != diagnostic.InstructionsAvailable || len(report.Uses) != 1 || report.Uses[0].TurnID != "turn-2" {
		t.Fatalf("retried publication = %+v %v", report, err)
	}
}

func TestRetainedInstructionsExpireWithTheirThread(t *testing.T) {
	service := newDiagnostics(t)
	directory := t.TempDir()
	store, err := service.OpenFailures(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	retention := service.RetainInstructions(store, nil, history.Limits{})
	expiredOnly := `"expired-only guidance"`
	expired := contentRequest("expired", "turn", &staticContent{instructions: expiredOnly, tools: []string{retainedTool, retainedWait}})
	for _, request := range []diagnostic.InstructionRequest{expired, instructionRequest("active", "turn", retainedRules)} {
		if err := retention.Observe(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		catalog, err := store.ReadCatalog(history.SessionName("expired"))
		if err != nil {
			return err
		}
		catalog.LastUsed = time.Now().Add(-history.SessionRetention - time.Hour)
		data, err := json.Marshal(&catalog)
		if err != nil {
			return err
		}
		return tx.Write(history.SessionName("expired"), "session-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(t.Context(), nil, history.Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	if report, err := service.ReadInstructions(t.Context(), directory, "expired", false, 4096); err != nil || report.Status != diagnostic.InstructionsUnavailable {
		t.Fatalf("expired thread = %+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(directory, contentName(expiredOnly))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired-only content remains: %v", err)
	}
	if got := len(retainedNames(t, directory, "instruction-revision-")); got != 1 {
		t.Fatalf("revision records after expiry = %d, want only the active thread's", got)
	}
	report, err := service.ReadInstructions(t.Context(), directory, "active", false, 4096)
	if err != nil || report.Status != diagnostic.InstructionsAvailable || len(report.Content) != 4 {
		t.Fatalf("active thread lost shared content: %+v %v", report, err)
	}
}

// An unchanged request republishes when its thread's index was reclaimed or
// replaced by another process after this invocation recorded it.
func TestRetainedInstructionsRepublishAfterReclamationOrPeerPublication(t *testing.T) {
	service := newDiagnostics(t)
	directory := t.TempDir()
	store, err := service.OpenFailures(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	retention := service.RetainInstructions(store, nil, history.Limits{})
	if err := retention.Observe(t.Context(), instructionRequest("idle", "turn-1", retainedRules)); err != nil {
		t.Fatal(err)
	}
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		catalog, err := store.ReadCatalog(history.SessionName("idle"))
		if err != nil {
			return err
		}
		catalog.LastUsed = time.Now().Add(-history.SessionRetention - time.Hour)
		data, err := json.Marshal(&catalog)
		if err != nil {
			return err
		}
		return tx.Write(history.SessionName("idle"), "session-pending-", data)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(t.Context(), nil, history.Limits{}, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := retention.Observe(t.Context(), instructionRequest("idle", "turn-2", retainedRules)); err != nil {
		t.Fatal(err)
	}
	report, err := service.ReadInstructions(t.Context(), directory, "idle", false, 4096)
	if err != nil || report.Status != diagnostic.InstructionsAvailable || len(report.Uses) != 1 || report.Uses[0].TurnID != "turn-2" {
		t.Fatalf("after reclamation = %+v %v", report, err)
	}
	peer := service.RetainInstructions(store, nil, history.Limits{})
	if err := peer.Observe(t.Context(), instructionRequest("idle", "peer", retainedRules, retainedNote)); err != nil {
		t.Fatal(err)
	}
	if err := retention.Observe(t.Context(), instructionRequest("idle", "turn-3", retainedRules)); err != nil {
		t.Fatal(err)
	}
	report, err = service.ReadInstructions(t.Context(), directory, "idle", false, 4096)
	if err != nil || len(report.Uses) != 3 || report.Uses[2].Revision != 1 || report.Uses[2].TurnID != "turn-3" {
		t.Fatalf("after peer publication = %+v %v", report.Uses, err)
	}
}
