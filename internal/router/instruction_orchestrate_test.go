package router

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/responses"
)

func TestOrchestrateInstructionMaskBoundaries(t *testing.T) {
	for _, pair := range orchestrateInstructionConflicts {
		t.Run(strings.Split(pair[0], "\n")[0], func(t *testing.T) {
			for _, test := range []struct{ input, want string }{
				{pair[0], pair[1]},
				{strings.ReplaceAll(pair[0], "\n", "\r\n"), strings.ReplaceAll(pair[1], "\n", "\r\n")},
				{"Keep authorization.\n" + pair[0] + "\nKeep policy.", "Keep authorization.\n" + pair[1] + "\nKeep policy."},
				{pair[0] + " Only after approval.", pair[0] + " Only after approval."},
				{"Quoted: " + pair[0], "Quoted: " + pair[0]},
				{"```text\n" + pair[0] + "\n```", "```text\n" + pair[0] + "\n```"},
				{"~~~\n" + pair[0] + "\n~~~", "~~~\n" + pair[0] + "\n~~~"},
				{pair[0] + "\n" + pair[0], pair[1] + "\n" + pair[1]},
			} {
				got := maskOrchestrateInstructions(test.input)
				if got != test.want {
					t.Fatalf("got %q, want %q", got, test.want)
				}
				if maskOrchestrateInstructions(got) != got {
					t.Fatal("not idempotent")
				}
			}
		})
	}
}

func TestOrchestrateInstructionConsumers(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"initial"}}`)
	// The fresh-child producer carries current role ownership before any reads.
	if !strings.Contains(launch.Params.Input[0].Text, orchestrateChildInstructions) {
		t.Fatal("fresh child lacks workflow guidance")
	}
	childCwd := u.orchestrateThreads["child"].batch.Cwd
	const conflict = "Use native role capabilities within authorized scope. Main owns integration/completion and edits\ninstructions, skills, workflow/task guidance, and delivery hooks; delegates return findings/proposals."
	const reads = "- `MAIN.md`: main: planning, delegation, and coordination.\n- `REVIEW.md`: main: after an authorized change is implemented and focused-validated,\n  before completion or a slice commit; read it then even if read earlier.\n- `DOCS.md`: writing, integrating, or reviewing reader documents, not every worker task."
	const retained = "Use native role capabilities within authorized scope. Main owns integration/completion."
	global := filepath.Join(t.TempDir(), "AGENTS.md")
	writeTestFile(t, global, conflict)
	wrapper := "# AGENTS.md instructions for " + global + "\n<INSTRUCTIONS>" + conflict + "\nKeep safety.</INSTRUCTIONS>"
	for _, test := range []struct {
		thread, cwd, parent string
		active              bool
	}{
		{"main", u.session.cwd, "", true}, {"child", childCwd, "", true},
		{"native", childCwd, "child", true}, {"unrelated", childCwd, "", false},
	} {
		t.Run(test.thread, func(t *testing.T) {
			// New proxy and journal owners have no live run/parent state.
			proxy := newToolPluginTestProxy(t)
			proxy.replayStore = replay
			input := []any{
				map[string]any{"role": "system", "content": conflict},
				map[string]any{"role": "developer", "content": []any{map[string]string{"type": "input_text", "text": conflict}, map[string]string{"type": "input_image", "image_url": conflict}}},
				map[string]string{"role": "user", "content": wrapper},
				map[string]string{"role": "user", "content": orchestrateCoordinatorInstructions + "\n" + conflict},
				map[string]string{"role": "assistant", "content": conflict},
				map[string]string{"type": "function_call_output", "call_id": "read", "output": conflict},
			}
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{"instructions": mustMarshalJSON(reads + "\n" + conflict), "input": mustMarshalJSON(input)}}
			metadata := codexTurnMetadata{RequestKind: "prewarm", Directories: map[string]json.RawMessage{test.cwd: nil}}
			if test.parent != "" {
				metadata.SubagentKind, metadata.ParentThreadID = "thread_spawn", test.parent
			}
			for range 2 {
				before := bytes.Clone(request.fields["input"])
				if _, err := proxy.prepareModelRequest(t.Context(), &request, "", test.thread, metadata, true, true); err != nil {
					t.Fatal(err)
				}
				text := jsonString(request.fields, "instructions")
				if test.active {
					if strings.Contains(text, conflict) || strings.Count(text, orchestrateOwnershipInstructions) != 1 || strings.Contains(text, "`MAIN.md`") || strings.Contains(text, "`REVIEW.md`") || !strings.Contains(text, "`DOCS.md`") {
						t.Fatalf("wrong policy: %s", text)
					}
					var items []map[string]json.RawMessage
					if err := json.Unmarshal(request.fields["input"], &items); err != nil {
						t.Fatal(err)
					}
					if jsonString(items[0], "content") != retained || strings.Contains(jsonString(items[2], "content"), conflict) {
						t.Fatal("instruction carriers not masked")
					}
					if jsonString(items[3], "content") != orchestrateCoordinatorInstructions+"\n"+conflict || jsonString(items[4], "content") != conflict || jsonString(items[5], "output") != conflict {
						t.Fatal("history or tool result changed")
					}
					if !strings.Contains(string(items[1]["content"]), "input_image") {
						t.Fatal("nontext part lost")
					}
				} else if !bytes.Equal(before, request.fields["input"]) || text != reads+"\n"+conflict {
					t.Fatal("unrelated thread acquired orchestration from quoted text")
				}
			}
			if test.active && test.parent == "" {
				compact := parsedResponsesRequest{fields: map[string]json.RawMessage{"instructions": mustMarshalJSON(conflict)}}
				if _, err := proxy.projectOrchestrateInstructions(t.Context(), &compact, test.thread, codexTurnMetadata{RequestKind: "compaction"}, true); err != nil || strings.Contains(jsonString(compact.fields, "instructions"), conflict) || !strings.Contains(jsonString(compact.fields, "instructions"), orchestrateOwnershipInstructions) {
					t.Fatal("compaction lost retained workspace policy", err)
				}
			}
		})
	}
	got, err := os.ReadFile(global)
	if err != nil || string(got) != conflict {
		t.Fatal("global file changed", err)
	}
}

func TestOrchestrateInstructionFencedMarkers(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"initial"}}`)
	const start, end = "<!-- mekugi-orchestration:start -->", "<!-- mekugi-orchestration:end -->"
	for _, test := range []struct{ name, example, section string }{
		{"paired", "```text\n" + start + "\nexample\n" + end + "\n```", ""},
		{"single", "~~~\r\n" + start + "\r\n~~~", ""},
		{"open fence", "```text\n" + start, ""},
		{"refresh", "```\n" + start + "\nexample\n" + end + "\n```", "\n" + start + "\nstale ownership\n" + end},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newToolPluginTestProxy(t)
			proxy.replayStore = replay
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{"instructions": mustMarshalJSON("Keep safety.\n" + test.example + test.section)}}
			metadata := codexTurnMetadata{Directories: map[string]json.RawMessage{u.session.cwd: nil}}
			var previous string
			for pass := range 2 {
				if _, err := proxy.prepareModelRequest(t.Context(), &request, "", "main", metadata, true, true); err != nil {
					t.Fatal(err)
				}
				text := jsonString(request.fields, "instructions")
				if !strings.Contains(text, test.example) || !strings.Contains(text, "Keep safety.") || strings.Contains(text, "stale ownership") {
					t.Fatal("projection changed the example or retained stale ownership", text)
				}
				active := 0
				rewriteUnfencedInstructionSpans(text, func(span string) string {
					active += strings.Count(span, orchestrateOwnershipInstructions)
					return span
				})
				if active != 1 || pass > 0 && text != previous {
					t.Fatal("runtime policy is fenced, duplicated or not idempotent", text)
				}
				previous = text
			}
		})
	}
}

func TestOrchestrateInstructionWorkspaceShapes(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := gitTestWorkspace(t)
	workspace := filepath.Join(repository, "subdir")
	writeTestFile(t, filepath.Join(workspace, "file"), "base")
	gitTestCommit(t, repository)
	u, launch := orchestrateIdentityPendingTurnInWorkspace(t, replay, workspace)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"initial"}}`)
	child := u.orchestrateThreads["child"].batch
	for _, test := range []struct {
		name, thread, parent, directory, kind string
		active                                bool
	}{
		{"main empty", "main", "", "", "", true},
		{"main root", "main", "", repository, "prewarm", true},
		{"child empty", "child", "", "", "prewarm", true},
		{"child root", "child", "", child.Checkout, "", true},
		{"descendant empty", "native", "child", "", "", true},
		{"descendant root", "fresh-native", "child", child.Checkout, "prewarm", true},
		{"foreign workspace", "main", "", t.TempDir(), "prewarm", false},
		{"unrelated", "unrelated", "", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			proxy := newToolPluginTestProxy(t)
			proxy.replayStore = replay
			metadata := codexTurnMetadata{RequestKind: responses.Turn, ParentThreadID: test.parent, ThreadID: test.thread, TurnID: "turn"}
			if test.parent != "" {
				metadata.SubagentKind, metadata.AgentName = "thread_spawn", "/root/native"
			}
			if test.directory != "" {
				metadata.Directories = map[string]json.RawMessage{test.directory: nil}
			}
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(testCodeModeDescription)})}}
			for range 2 {
				transform, err := proxy.prepareModelRequest(t.Context(), &request, "session-"+test.thread, test.thread, metadata, true, test.kind == "prewarm")
				if err != nil {
					t.Fatal(err)
				}
				if transform != nil {
					transform.Close()
				}
			}
			if got := strings.Contains(jsonString(request.fields, "instructions"), orchestrateOwnershipInstructions); got != test.active {
				t.Fatalf("runtime ownership=%v, want %v", got, test.active)
			}
		})
	}
	// A corrupt or conflicting retained identity cannot be replaced by parent metadata.
	if err := u.proxy.journals.bindIdentity(u.ctx, replay, workspace, "main", "foreign", "/root", true); err != nil {
		t.Fatal(err)
	}
	proxy := newToolPluginTestProxy(t)
	proxy.replayStore = replay
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{}}
	if _, err := proxy.projectOrchestrateInstructions(t.Context(), &request, "main", codexTurnMetadata{SubagentKind: "thread_spawn", ParentThreadID: "child"}, true); err == nil {
		t.Fatal("conflicting retained identity was accepted")
	}
}
