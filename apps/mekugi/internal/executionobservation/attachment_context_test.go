package executionobservation

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/outputdeduplication"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestAttachmentContextRefreshesCurrentSkillsAndPreservesSelections(t *testing.T) {
	usePreloadTestFrontend(t)
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{true: "summary", false: "reset"}[retained], func(t *testing.T) {
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
			p.ContextAttachments = attachmentTestContext(values)
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
			sources := []compact.SourceIdentity{{Skill: "guide", Path: skill}}
			selection := string(mustTestJSON(t, struct {
				Sources []compact.SourceIdentity `json:"sources"`
			}{sources}))
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
			snapshot, err := (compact.Readers{}).Read(t.Context(), selection, dir, nil)
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
				hint := attachmentTestHint(t, visible)
				if !regexp.MustCompile(`^Optional attachment recall: selections: mread [a-z]+[0-9]*; skills: mread [a-z]+[0-9]*; files: mread [a-z]+[0-9]*$`).MatchString(hint) {
					t.Fatalf("unexpected attachment hint: %q", hint)
				}
				if bytes.Contains(visible, []byte("SELECTED_BYTES")) || bytes.Count(visible, []byte("CURRENT_SKILL")) != 0 {
					t.Fatalf("attachment bodies were forced: %s", visible)
				}
				recalled := attachmentTestReadHints(t, p, "root", visible)
				if strings.Count(recalled, "SELECTED_BYTES") != 1 || !strings.Contains(recalled, "@code") || strings.Count(recalled, "$guide") != 1 || strings.Count(recalled, "CURRENT_SKILL") != 1 {
					t.Fatalf("optional recall lost or repeated attachment evidence: %s", recalled)
				}
				for _, stale := range []string{"FILE_ORIGINAL", "FILE_CHANGED", "SKILL_ORIGINAL", "LATER_SKILL", "<skill name="} {
					if bytes.Contains(visible, []byte(stale)) || strings.Contains(recalled, stale) {
						t.Fatalf("stale or unselected context %s", stale)
					}
				}
				if retained && !bytes.Contains(visible, []byte(draft.Text)) {
					t.Fatal("native summary placeholders lost")
				}
			}
			originalHints := request.Fields["input"]
			for _, thread := range []string{"root", "fork"} {
				fresh := newManagedMekugiProxy(t)
				store, err := openMekugiReplayStore(p.Retained.Store.Directory())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(store.snapshots.Close)
				setTestProxyStore(fresh, store)
				fresh.InputProjections, fresh.ContextAttachments = p.InputProjections, attachmentTestContext(values)
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
				if len(transform.CompactReload()) != 0 || bytes.Count(request.Fields["input"], []byte("CURRENT_SKILL")) != 0 || bytes.Contains(request.Fields["input"], []byte("LATER_SKILL")) {
					t.Fatal("fresh resume/fork lost or reacquired completed skill content")
				}
				if old := attachmentTestReadHints(t, fresh, thread, originalHints); !strings.Contains(old, "SELECTED_BYTES") {
					t.Fatalf("inherited recovery handles lost after reopen: %s", old)
				}
				if recalled := attachmentTestReadHints(t, fresh, thread, request.Fields["input"]); !strings.Contains(recalled, "SELECTED_BYTES") || !strings.Contains(recalled, "CURRENT_SKILL") {
					t.Fatalf("reopened recall unavailable: %s", recalled)
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

func attachmentTestContext(values []composer.ContextAttachment) func(context.Context, string, string) (composer.AttachmentSnapshot, error) {
	var cache composer.AttachmentContext
	return func(_ context.Context, _, window string) (composer.AttachmentSnapshot, error) {
		if snapshot, ok := cache.Get(window); ok {
			return snapshot, nil
		}
		cache.Set(window, values)
		snapshot, _ := cache.Get(window)
		return snapshot, nil
	}
}

func attachmentTestReadHints(t *testing.T, p *Preparer, thread string, input []byte) string {
	t.Helper()
	matches := regexp.MustCompile(`mread ([a-z]+[0-9]*(?: [a-z]+[0-9]*)*)`).FindAllStringSubmatch(attachmentTestHint(t, input), -1)
	namespace, err := p.Retained.Store.NamespaceForThread(thread)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	for _, match := range matches {
		args = append(args, strings.Fields(match[1])...)
	}
	if len(args) == 0 {
		return ""
	}
	out, err := p.Recovery.Execute(t.Context(), command.Request{Args: args, Scope: scope.Retained{Directory: p.Retained.Store.Directory(), Namespace: namespace, Thread: thread}})
	if err != nil || out.ExitCode != 0 {
		t.Fatalf("mread: %+v, %v", out, err)
	}
	return out.Stdout
}

func TestAttachmentContextPreservesSubmittedImagePath(t *testing.T) {
	for _, kind := range []string{"", "message", "diff"} {
		mixed := kind != ""
		for _, origin := range []string{"image", "answer"} {
			t.Run(fmt.Sprintf("%s/selection=%s", origin, kind), func(t *testing.T) {
				p := newManagedMekugiProxy(t)
				attachTestReplayStore(t, p)
				imagePath := filepath.Join(t.TempDir(), origin+"-image.png")
				var editor composer.Editor
				if mixed {
					editor.InsertText("[Selected " + kind + "]")
					body := "ORIGINAL_SELECTION\r\n日本語\n"
					if kind == "diff" {
						body = "+ORIGINAL_SELECTION\r\n-日本語\n"
					}
					editor.Selections = []composer.Token{{Start: 0, End: len(editor.Text), Text: body, Source: "screen"}}
				}
				if origin == "answer" {
					editor.InsertText(binding.EncodeQuestionReplies([]binding.QuestionReply{{Answer: "image answer", QuestionItemID: binding.AsyncQuestionIdentity("call", 0)}}))
					editor.AnswerImages = []string{imagePath}
				} else {
					editor.InsertImage(imagePath)
				}
				editor.SnapshotFiles("")
				if err := editor.IdentifyAttachments("submitted"); err != nil {
					t.Fatal(err)
				}
				native := binding.ThreadItem{ID: "image-user", Type: "userMessage", Content: mustTestJSON(t, conversationbinding.Input(editor.Draft))}
				values := conversationbinding.CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{native, {ID: "reset", Type: "contextCompaction", Status: "completed"}}}})
				p.ContextAttachments = attachmentTestContext(values)
				request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{}, "tools": testExecResponsesTools()}))
				if err != nil {
					t.Fatal(err)
				}
				transform, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", ContextWindowID: "new-window", WindowNumber: 1}, true, false)
				if err != nil {
					t.Fatal(err)
				}
				defer transform.Close()
				if len(transform.CompactReload()) != 0 || bytes.Contains(request.Fields["input"], []byte(imagePath)) || bytes.Contains(request.Fields["input"], []byte("ORIGINAL_SELECTION")) {
					t.Fatalf("attachment content forced into provider input: %s", request.Fields["input"])
				}
				pattern := `^Optional attachment recall: images: mread [a-z]+[0-9]*$`
				if mixed {
					pattern = `^Optional attachment recall: selections: mread [a-z]+[0-9]*; images: mread [a-z]+[0-9]*$`
				}
				if hint := attachmentTestHint(t, request.Fields["input"]); !regexp.MustCompile(pattern).MatchString(hint) {
					t.Fatalf("unexpected attachment hint: %q", hint)
				}
				recalled := attachmentTestReadHints(t, p, "root", request.Fields["input"])
				if !strings.Contains(recalled, imagePath) || !strings.Contains(recalled, "[Image 1]") || mixed && !strings.Contains(recalled, strings.Join(values[0].Selection.Frames, "\n")) {
					t.Fatalf("reset dropped submitted evidence: %s", recalled)
				}
				for _, noise := range []struct{ name, text string }{
					{"JSON", `"frames":`}, {"v2", "<mekugi-file-attachments-v2>"},
					{"id", `"id":`}, {"byte coordinates", "UTF-8 bytes"},
					{"file-attachments label", "file-attachments"},
				} {
					if strings.Contains(recalled, noise.text) {
						t.Fatalf("selection recall contains %s: %s", noise.name, recalled)
					}
				}
				if mixed {
					quote := strings.Join(values[0].Selection.Frames, "\n")
					selection, ok := attachments.ParseSelection(quote)
					if !ok || selection.Label != "[Selected "+kind+"]" || !strings.HasPrefix(selection.Header, "<selected-text ") || !strings.HasSuffix(selection.Footer, "</selected-text>") || (kind == "diff") != strings.HasSuffix(selection.Header, "```diff\n") {
						t.Fatalf("recall lost selection or diff framing: %q", quote)
					}
					at := strings.Index(recalled, quote)
					t.Logf("Recalled model-visible selection:\n%s", recalled[at:at+len(quote)])
				}
			})
		}
	}
}

func TestAttachmentContextReusesHistoricalExplicitSkill(t *testing.T) {
	for _, kind := range []string{"compaction", "compaction_summary"} {
		t.Run(kind, func(t *testing.T) {
			p := newManagedMekugiProxy(t)
			attachTestReplayStore(t, p)
			selection := `{"sources":[{"skill":"guide","path":"/SKILL.md"}]}`
			ref, err := moduletest.StoreCompact(t.Context(), p.Retained, "root", "summary", selection)
			if err != nil {
				t.Fatal(err)
			}
			p.ContextAttachments = attachmentTestContext([]composer.ContextAttachment{{ID: "submitted", Placeholders: []string{"$guide"}, Skills: []composer.Token{{Name: "guide", Path: "/SKILL.md"}}}})
			items := []map[string]jsontext.Value{{"type": mustTestJSON(t, kind), "encrypted_content": mustTestJSON(t, ref)}}
			raw, _, err := compactReaderCall(compact.Record{Reference: ref, Selection: selection}, items, "root")
			if err != nil {
				t.Fatal(err)
			}
			var call map[string]jsontext.Value
			if err := json.Unmarshal(raw, &call); err != nil {
				t.Fatal(err)
			}
			captured := compact.Snapshot{Selection: selection, Sources: []compact.CapturedSource{{Command: "skill reader", Stdout: "CURRENT_SKILL"}}}
			items = append(items, call, map[string]jsontext.Value{"type": mustTestJSON(t, "custom_tool_call_output"), "call_id": call["call_id"], "output": mustTestJSON(t, compactTestSnapshot(t, p, captured))})
			request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": items, "tools": testExecResponsesTools()}))
			if err != nil {
				t.Fatal(err)
			}
			transform, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", ContextWindowID: "summary-window", WindowNumber: 1}, true, false)
			if err != nil {
				t.Fatal(err)
			}
			defer transform.Close()
			if len(transform.CompactReload()) != 0 || bytes.Count(request.Fields["input"], []byte("CURRENT_SKILL")) != 1 {
				t.Fatalf("historical explicit source reacquired or duplicated: %s", transform.CompactReload())
			}
			recalled := attachmentTestReadHints(t, p, "root", request.Fields["input"])
			if !strings.Contains(recalled, "$guide") || strings.Contains(recalled, "CURRENT_SKILL") {
				t.Fatalf("historical skill marker lost or redundant body recall exposed: %s", recalled)
			}
		})
	}
}

func TestAttachmentContextUsesNativeSuccessfulWholeSources(t *testing.T) {
	for _, purpose := range []string{"preload", "reload"} {
		for _, test := range []struct {
			name, reference, kind, sourcePath, sourceName string
			success, supplied                             bool
		}{
			{"file", "@/SKILL.md", "file", "/SKILL.md", "", true, true},
			{"skill", "$guide", "skill", "", "guide", true, true},
			{"partial file", "@/SKILL.md#L1", "file", "/SKILL.md", "", true, false},
			{"partial skill", "$guide#L1", "skill", "", "guide", true, false},
			{"skill reference", "$guide/reference.md", "skill", "", "guide/reference.md", true, false},
			{"failed file", "@/SKILL.md", "file", "/SKILL.md", "", false, false},
			{"failed skill", "$guide", "skill", "", "guide", false, false},
			{"other file", "@/other.md", "file", "/other.md", "", true, false},
			{"invalid reference", "not a source", "file", "/SKILL.md", "", true, false},
		} {
			t.Run(purpose+"/"+test.name, func(t *testing.T) {
				p := newManagedMekugiProxy(t)
				attachTestReplayStore(t, p)
				p.ContextAttachments = attachmentTestContext([]composer.ContextAttachment{{ID: "submitted", Placeholders: []string{"$guide"}, Skills: []composer.Token{{Name: "guide", Path: "/SKILL.md"}}}})
				// Native reads supply the exact 22 file bytes. The optional mcat
				// reader would append a newline, so byte deduplication is not a
				// substitute for knowing this full source was already selected.
				body := "CURRENT_ATTACHED_SKILL"
				source := observation.SourceAcquisitionResult{Reference: test.reference, Kind: test.kind, Path: test.sourcePath, Name: test.sourceName, Success: test.success}
				receipt := observation.SourceAcquisition{ID: "native-acquisition", Purpose: purpose, Sources: []observation.SourceAcquisitionResult{source}}
				output := "Reloaded context (tool output).\n" + attachments.SourceRanges(body, []attachments.SourceRange{{Start: 0, End: len(body)}})
				item := map[string]any{"type": "function_call_output", "namespace": "context", "name": "reload", "output": output, "internal_chat_message_metadata_passthrough": map[string]any{"source_acquisition": receipt}}
				request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{item}, "tools": testExecResponsesTools()}))
				if err != nil {
					t.Fatal(err)
				}
				transform, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", ContextWindowID: "native-window", WindowNumber: 1}, true, false)
				if err != nil {
					t.Fatal(err)
				}
				defer transform.Close()
				if (len(transform.CompactReload()) == 0) != test.supplied {
					t.Fatalf("native whole-source selection ignored or partial/failure treated as complete: %s", transform.CompactReload())
				}
				if test.supplied {
					recalled := attachmentTestReadHints(t, p, "root", request.Fields["input"])
					if !strings.Contains(recalled, "$guide") || strings.Contains(recalled, body) || bytes.Count(request.Fields["input"], []byte(body)) != 1 {
						t.Fatalf("native source was reacquired or put in initial body recall: %s", recalled)
					}
				}
			})
		}
	}
}

func attachmentTestHint(t *testing.T, input []byte) string {
	t.Helper()
	var items []struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(input, &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Role != "user" {
			continue
		}
		for _, part := range item.Content {
			if strings.HasPrefix(part.Text, "Optional attachment recall:") {
				return part.Text
			}
		}
	}
	return ""
}

func TestAttachmentRecallPreservesProviderPrefix(t *testing.T) {
	usePreloadTestFrontend(t)
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
	if err != nil {
		t.Fatal(err)
	}
	p.InputProjections = []binding.InputProjection{outputdeduplication.Module.Bindings.InputProjection(service)}
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("CURRENT_SKILL"), 0600); err != nil {
		t.Fatal(err)
	}
	draft := composer.Draft{Text: "$guide [Selected 1]", Skills: []composer.Token{{Start: 0, End: 6, Name: "guide", Path: path}}, Selections: []composer.Token{{Start: 7, End: 19, Text: "SELECTED_BYTES\n", Source: "screen"}}}
	draft.Selections[0].End = len(draft.Text)
	draft.SnapshotFiles(dir)
	draft.SnapshotSkills(func(_, path string) ([]byte, error) { return os.ReadFile(path) })
	if err := draft.IdentifyAttachments("submitted"); err != nil {
		t.Fatal(err)
	}
	values := conversationbinding.CompactedAttachments([]binding.HistoryTurn{{Items: []binding.ThreadItem{{ID: "submission", Type: "userMessage", Content: mustTestJSON(t, conversationbinding.Input(draft))}, {ID: "reset", Type: "contextCompaction", Status: "completed"}}}})
	p.ContextAttachments = attachmentTestContext(values)
	meta := codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", ContextWindowID: "cache-window", WindowNumber: 1}
	initial, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"input": []any{}, "tools": testExecResponsesTools()}))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := p.prepareModelRequest(t.Context(), &initial, "routing", "root", meta, true, false)
	if err != nil {
		t.Fatal(err)
	}
	reader := jsontext.Value(pending.CompactReload()).Clone()
	pending.Close()
	if len(reader) == 0 || attachmentTestHint(t, initial.Fields["input"]) != "" {
		t.Fatal("local skill reader froze incomplete provider hints")
	}
	selection := string(mustTestJSON(t, map[string]any{"sources": []compact.SourceIdentity{{Skill: "guide", Path: path}}}))
	captured, err := (compact.Readers{}).Read(t.Context(), selection, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var readerItem map[string]jsontext.Value
	if err := json.Unmarshal(reader, &readerItem); err != nil {
		t.Fatal(err)
	}
	fresh := []jsontext.Value{reader, mustTestJSON(t, map[string]any{"type": "custom_tool_call_output", "call_id": responses.StringField(readerItem, "call_id"), "output": compactTestSnapshot(t, p, captured)})}
	var history codexresponses.SessionHistory
	previous, hint := "", ""
	cached := 0
	for round := range 5 {
		command := map[string]any{"type": "response.create", "model": "gpt-test", "tools": testExecResponsesTools(), "input": fresh}
		if previous != "" {
			command["previous_response_id"] = previous
		}
		exchange, request, err := history.Prepare(mustTestJSON(t, command), nil)
		if err != nil {
			t.Fatal(err)
		}
		transform, err := p.prepareModelRequest(t.Context(), &request, "routing", "root", meta, true, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(transform.CompactReload()) != 0 {
			t.Fatal("completed skill reader was reacquired during provider continuation")
		}
		currentHint := attachmentTestHint(t, request.Fields["input"])
		var projected []jsontext.Value
		if err := json.Unmarshal(request.Fields["input"], &projected); err != nil {
			t.Fatal(err)
		}
		if round == 0 {
			hint = currentHint
		}
		if currentHint == "" || currentHint != hint || !bytes.Contains(projected[0], []byte("Optional attachment recall:")) {
			t.Fatalf("round %d moved or rewrote the frozen window hint: %s", round, request.Fields["input"])
		}
		reason, err := exchange.Reconcile(&request, true)
		if err != nil {
			t.Fatal(err)
		}
		if round > 0 && (reason != "matching_prefix" || request.RebaseInput || request.CachedInput != cached) {
			t.Fatalf("round %d lost confirmed provider prefix: reason=%s cached=%d want=%d rebase=%t", round, reason, request.CachedInput, cached, request.RebaseInput)
		}
		body, err := request.WireBody(request.Fields)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := request.IncrementalBody(body, true)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := codexresponses.ParseRequest(wire)
		if err != nil {
			t.Fatal(err)
		}
		var suffix []jsontext.Value
		if err := json.Unmarshal(sent.Fields["input"], &suffix); err != nil {
			t.Fatal(err)
		}
		if len(suffix) != len(projected)-cached || round > 0 && responses.StringField(sent.Fields, "previous_response_id") != previous {
			t.Fatalf("round %d did not send only the uncached suffix: %s", round, wire)
		}
		if err := exchange.BeginProvider(sent.Fields); err != nil {
			t.Fatal(err)
		}
		callID := fmt.Sprintf("ordinary-%d", round)
		script := fmt.Sprintf("text(%q);", callID)
		result := "Script completed\nOutput:\n" + callID
		if round == 0 {
			var refs []string
			for _, match := range regexp.MustCompile(`mread ([a-z]+[0-9]*(?: [a-z]+[0-9]*)*)`).FindAllStringSubmatch(hint, -1) {
				refs = append(refs, strings.Fields(match[1])...)
			}
			script = "text((await tools.exec_command({cmd:" + string(mustTestJSON(t, "mread "+strings.Join(refs, " "))) + "})).output);"
			result = "Script completed\nOutput:\n" + attachmentTestReadHints(t, p, "root", request.Fields["input"])
		}
		if round > 0 && (bytes.Count(request.Fields["input"], []byte("CURRENT_SKILL")) != 1 || bytes.Count(request.Fields["input"], []byte("SELECTED_BYTES")) != 1) {
			t.Fatalf("round %d duplicated consumed source bytes: %s", round, request.Fields["input"])
		}
		output := []map[string]any{{"type": "message", "id": fmt.Sprintf("assistant-%d", round), "role": "assistant", "phase": "commentary", "content": []map[string]string{{"type": "output_text", "text": fmt.Sprintf("Checking round %d", round)}}}, {"type": "custom_tool_call", "id": callID, "call_id": callID, "name": "exec", "input": script}}
		previous = fmt.Sprintf("provider-%d", round)
		terminal := mustTestJSON(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": previous, "output": output}})
		if err := exchange.ObserveProvider(terminal); err != nil {
			t.Fatal(err)
		}
		if err := exchange.ObserveOutput(terminal); err != nil {
			t.Fatal(err)
		}
		transform.Close()
		cached = len(projected) + len(output)
		fresh = []jsontext.Value{mustTestJSON(t, map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": result})}
	}
}
