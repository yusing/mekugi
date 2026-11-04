package executionobservation

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/outputdeduplication"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestAttachmentContextRefreshesCurrentSkillsAndPreservesSelections(t *testing.T) {
	for _, retained := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "summary", false: "reset"}[retained], map[bool]string{true: "explicit", false: "automatic"}[explicit]}, "/"), func(t *testing.T) {
				p := newManagedMekugiProxy(t)
				attachTestReplayStore(t, p)
				selected, err := p.Retained.Store.SelectSession(t.Context(), nil, history.Limits{Total: history.DefaultStorageBytes}, "root")
				if err != nil {
					t.Fatal(err)
				}
				defer selected.Close()
				service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
				if err != nil {
					t.Fatal(err)
				}
				p.InputProjections = []binding.InputProjection{outputdeduplication.Module.Bindings.InputProjection(service)}
				dir := t.TempDir()
				file, skill := filepath.Join(dir, "code.txt"), filepath.Join(dir, "SKILL.md")
				if err := os.WriteFile(file, []byte("FILE_ORIGINAL"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(skill, []byte("SKILL_ORIGINAL"), 0600); err != nil {
					t.Fatal(err)
				}
				draft := composer.Draft{Text: "@code $guide [Selected 1]"}
				draft.Files = []composer.Token{{Start: 0, End: 5, Path: file}}
				draft.Skills = []composer.Token{{Start: 6, End: 12, Name: "guide", Path: skill}}
				draft.Selections = []composer.Token{{Start: 13, End: 25, Text: "SELECTED_BYTES\n", Source: "screen"}}
				// The selection label ends at the submitted text's actual byte boundary.
				draft.Selections[0].End = len(draft.Text)
				draft.SnapshotFiles(dir)
				draft.SnapshotSkills(func(_, path string) ([]byte, error) { return os.ReadFile(path) })
				if err := draft.IdentifyAttachments("submitted"); err != nil {
					t.Fatal(err)
				}
				native := binding.ThreadItem{ID: "user", Type: "userMessage", Content: mustTestJSON(t, conversationbinding.Input(draft))}
				values := conversationbinding.CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{native, {ID: "boundary", Type: "contextCompaction", Status: "completed"}}}})
				if len(values) != 1 || strings.Join(values[0].Placeholders, " ") != draft.Text {
					t.Fatalf("submitted bindings lost: %+v", values)
				}
				p.ContextAttachments = func(context.Context, string, string) ([]composer.ContextAttachment, error) { return values, nil }
				if err := os.WriteFile(skill, []byte("CURRENT_SKILL"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("FILE_CHANGED"), 0600); err != nil {
					t.Fatal(err)
				}
				meta := codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", ContextWindowID: "new-window", WindowNumber: 1}
				var items []map[string]jsontext.Value
				if retained {
					items = append(items, map[string]jsontext.Value{"type": mustTestJSON(t, "message"), "role": mustTestJSON(t, "user"), "content": mustTestJSON(t, []map[string]string{{"type": "input_text", "text": draft.Text}, {"type": "input_text", "text": draft.Attachments[0]}})})
					items = append(items, map[string]jsontext.Value{"type": mustTestJSON(t, "message"), "role": mustTestJSON(t, "user"), "content": mustTestJSON(t, attachments.ManagedSkillReference("guide"))})
				}
				sources := []compact.Source{{Skill: "guide", Path: skill}}
				if explicit {
					sources = append(sources, sources[0])
				}
				selection := string(mustTestJSON(t, struct {
					Sources []compact.Source `json:"sources"`
				}{sources}))
				if explicit {
					wake := control.NativeInput{ID: "checkpoint", Wake: &control.WaitResult{State: "completed", Compaction: &control.CompactionResult{State: "completed", Reload: selection}}}
					items = append(items, map[string]jsontext.Value{"type": mustTestJSON(t, "function_call_output"), "namespace": mustTestJSON(t, "threadcontrol"), "name": mustTestJSON(t, "compact"), "output": mustTestJSON(t, wake.RetainedWaitOutput())})
				}
				parse := func(input []map[string]jsontext.Value) codexresponses.Request {
					r, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": input, "tools": testExecResponsesTools()}))
					if err != nil {
						t.Fatal(err)
					}
					return r
				}
				request := parse(items)
				transform, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", meta, true, false)
				if err != nil {
					t.Fatal(err)
				}
				var reader map[string]jsontext.Value
				if err := json.Unmarshal(transform.CompactReload(), &reader); err != nil {
					t.Fatal(err)
				}
				transform.Close()
				var program string
				_ = json.Unmarshal(reader["input"], &program)
				if !codexresponses.MatchesCompactReadProgram(program, compact.CaptureCommand(selection)) {
					t.Fatal("refresh did not use the selected current source")
				}
				snapshot, err := compact.ReadSources(t.Context(), selection, dir, nil, compact.ChangeReader{})
				if err != nil {
					t.Fatal(err)
				}
				items = append(items, reader, map[string]jsontext.Value{"type": mustTestJSON(t, "custom_tool_call_output"), "call_id": reader["call_id"], "output": mustTestJSON(t, compactTestSnapshot(t, p, snapshot))})
				if err := os.WriteFile(skill, []byte("LATER_SKILL"), 0600); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					request = parse(items)
					transform, err = p.prepareModelRequest(t.Context(), &request, "routing", "root", meta, true, false)
					if err != nil {
						t.Fatal(err)
					}
					if len(transform.CompactReload()) != 0 {
						t.Fatal("completed current-window refresh reacquired sources")
					}
					transform.Close()
					visible := request.Fields["input"]
					for _, body := range []string{"CURRENT_SKILL", "SELECTED_BYTES"} {
						if bytes.Count(visible, []byte(body)) != 1 {
							t.Fatalf("%s missing or repeated: %s", body, visible)
						}
					}
					for _, stale := range []string{"FILE_ORIGINAL", "FILE_CHANGED", "SKILL_ORIGINAL", "LATER_SKILL", "<skill name="} {
						if bytes.Contains(visible, []byte(stale)) {
							t.Fatalf("stale or unselected context %s", stale)
						}
					}
					if !bytes.Contains(visible, []byte(draft.Text)) {
						t.Fatal("submitted placeholders lost")
					}
				}
				for _, thread := range []string{"root", "fork"} {
					fresh := newManagedMekugiProxy(t)
					store, err := openMekugiReplayStore(p.Retained.Store.Directory())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(store.snapshots.Close)
					setTestProxyStore(fresh, store)
					fresh.InputProjections, fresh.ContextAttachments = p.InputProjections, p.ContextAttachments
					forkMeta := meta
					forkMeta.ThreadID = thread
					if thread == "fork" {
						forkMeta.ForkedFromThreadID = "root"
						if err := fresh.Retained.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
							return tx.InitializeScope(history.Scope{Thread: thread, Namespace: thread, Fork: "root"}, nil)
						}); err != nil {
							t.Fatal(err)
						}
					}
					request = parse(items)
					transform, err = fresh.prepareModelRequest(t.Context(), &request, "fresh-routing", thread, forkMeta, true, false)
					if err != nil {
						t.Fatal(err)
					}
					if len(transform.CompactReload()) != 0 || bytes.Count(request.Fields["input"], []byte("CURRENT_SKILL")) != 1 || bytes.Contains(request.Fields["input"], []byte("LATER_SKILL")) {
						t.Fatal("fresh resume/fork lost or reacquired completed skill content")
					}
					transform.Close()
				}
				meta.ContextWindowID, meta.WindowNumber = "later-window", 2
				request = parse(nil)
				transform, err = p.prepareModelRequest(t.Context(), &request, "routing", "root", meta, true, false)
				if err != nil {
					t.Fatal(err)
				}
				if len(transform.CompactReload()) == 0 || bytes.Equal(transform.CompactReload(), mustTestJSON(t, reader)) {
					t.Fatal("later reset reused an obsolete skill acquisition")
				}
				transform.Close()
			})
		}
	}
}

func TestAttachmentPathReloadDoesNotSuppressManagedSkillRefresh(t *testing.T) {
	p := newManagedMekugiProxy(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	for name, body := range map[string]string{
		"SKILL.md":                "metadata placeholder",
		".skills-mgr-placeholder": "",
		"skills-mgr":              "#!/bin/sh\nprintf CURRENT_INSTRUCTIONS\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	p.ContextAttachments = func(context.Context, string, string) ([]composer.ContextAttachment, error) {
		return []composer.ContextAttachment{{ID: "attached", Skills: []composer.Token{{Name: "guide", Path: path}}}}, nil
	}
	explicit := string(mustTestJSON(t, map[string]any{"sources": []compact.Source{{Path: path}}}))
	wake := control.NativeInput{ID: "checkpoint", Wake: &control.WaitResult{State: "completed", Compaction: &control.CompactionResult{State: "completed", Reload: explicit}}}
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []map[string]any{{"type": "function_call_output", "namespace": "threadcontrol", "name": "compact", "output": wake.RetainedWaitOutput()}}, "tools": testExecResponsesTools()}))
	if err != nil {
		t.Fatal(err)
	}
	call, _, err := p.prepareAttachmentContext(t.Context(), &request, "root", codexresponses.TurnMetadata{RequestKind: "turn", WindowNumber: 1, ContextWindowID: "reset"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var reader struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(call, &reader); err != nil {
		t.Fatal(err)
	}
	selection := string(mustTestJSON(t, map[string]any{"sources": []compact.Source{{Skill: "guide", Path: path}}}))
	if !codexresponses.MatchesCompactReadProgram(reader.Input, compact.CaptureCommand(selection)) {
		t.Fatal("path-only reload suppressed the managed refresh")
	}
	snapshot, err := compact.ReadSources(t.Context(), selection, dir, append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH")), compact.ChangeReader{})
	if err != nil || len(snapshot.Sources) != 1 || snapshot.Sources[0].Stdout != "CURRENT_INSTRUCTIONS" {
		t.Fatalf("managed refresh = %+v, %v", snapshot, err)
	}
}
