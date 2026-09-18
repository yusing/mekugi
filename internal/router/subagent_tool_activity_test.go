package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSubagentToolActivityJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
			child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", []any{
				map[string]any{"type": "custom_tool_call", "call_id": "origin", "name": "exec", "input": `text(await tools.exec_command({cmd:"go test ./internal/router"}));`},
				map[string]any{"type": "custom_tool_call_output", "call_id": "origin", "output": "Script running with cell ID 7\nWall time 30 seconds\nOutput:\n"},
			})
			calls := []map[string]any{
				{"type": "function_call", "id": "cell-wait", "call_id": "cell-wait", "name": "wait", "arguments": `{"cell_id":"7","yield_time_ms":30000,"max_tokens":5000}`},
				{"type": "function_call", "id": "cell-stop", "call_id": "cell-stop", "namespace": "functions", "name": "wait", "arguments": `{"cell_id":"7","terminate":true}`},
				{"type": "function_call", "id": "first", "call_id": "first", "namespace": "functions", "name": "lookup", "arguments": "{\"query\":\"hello\"}"},
				{"type": "custom_tool_call", "id": "second", "call_id": "second", "name": "external", "input": "first line\n" + strings.Repeat("界", 300)},
				{"type": "function_call", "id": "third", "call_id": "third", "namespace": "collaboration", "name": "send_message", "arguments": "opaque message"},
				{"type": "function_call", "id": "native-message", "call_id": "native-message", "namespace": "mekugi_collaboration", "name": "send_message", "arguments": "opaque message"},
				{"type": "shell_call", "id": "shell", "status": "completed", "action": map[string]any{"commands": []string{"echo a", "  echo b"}}},
				{"type": "local_shell_call", "id": "exec", "status": "completed", "action": map[string]any{"command": []string{"bash", "-lc", "cat a"}}},
				{"type": "web_search_call", "id": "web", "status": "completed", "action": map[string]any{"type": "search", "query": "Go parser"}},
				{"type": "custom_tool_call", "id": "mcp", "call_id": "mcp", "name": "exec", "input": `const r = await tools.mcp__openaiDeveloperDocs__fetch_openai_doc({url:"https://learn.chatgpt.com/docs/developer-commands",anchor:"#built-in-slash-commands"}); text(r);`},
				{"type": "function_call", "id": "namespaced-mcp", "call_id": "namespaced-mcp", "namespace": "mcp__docs", "name": "lookup", "arguments": "{}"},
				{"type": "custom_tool_call", "id": "batch", "call_id": "batch", "name": "exec", "input": `text(await tools.list_mcp_resources({})); text(await tools.clock__curr_time({}));`},
			}
			payload := mustTestJSON(t, map[string]any{"status": "completed", "output": calls})
			if stream {
				for _, call := range calls {
					for _, eventType := range []string{"response.output_item.added", "response.output_item.done"} {
						event := mustTestJSON(t, map[string]any{"type": eventType, "item": call})
						events, err := child.TransformSSE(event)
						if err != nil || len(events) != 1 || !bytes.Equal(events[0], event) {
							t.Fatalf("child call changed: %s, %v", events, err)
						}
					}
				}
				event := mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(payload)})
				if _, err := child.TransformSSE(event); err != nil {
					t.Fatal(err)
				}
			} else if output, err := child.TransformJSON(payload); err != nil || !bytes.Equal(output, payload) {
				t.Fatalf("child output changed: %s, %v", output, err)
			}
			visible, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			var response struct{ Output []map[string]json.RawMessage }
			if err := json.Unmarshal(visible, &response); err != nil || len(response.Output) != 2 {
				t.Fatalf("distinct calls or terminal deduplication: %s, %v", visible, err)
			}
			// Start metadata precedes the child's distinct tool calls.
			response.Output = response.Output[1:]
			got := commentaryText(t, response.Output[0])
			want := "In `/root/worker`\n\n- Still Running\n  ```bash\n  go test ./internal/router\n  ```\n\n- Stop\n  ```bash\n  go test ./internal/router\n  ```\n\n- Tool call: `functions.lookup`\n  `{\"query\":\"hello\"}`" +
				"\n\n- Tool call: `external`\n  ```\n  first line\n  " + strings.Repeat("界", 300) + "\n  ```" +
				"\n\n- Run\n  ```bash\n  echo a\n    echo b\n  ```" +
				"\n\n- Read `a`\n\n- Search web\n  `Go parser`" +
				"\n\n- MCP `openaiDeveloperDocs.fetch_openai_doc`\n  `{\"anchor\":\"#built-in-slash-commands\",\"url\":\"https://learn.chatgpt.com/docs/developer-commands\"}`" +
				"\n\n- MCP `docs.lookup`\n  `{}`" +
				"\n\n- List MCP resources\n  `{}`\n\n- Read current time\n  `{}`"
			if got != want {
				t.Fatalf("grouped display: got %q, want %q", got, want)
			}
			_, replay := prepareActivityTest(t, proxy, "replay", "c", "r", "/root/worker", []any{response.Output[0], calls[0]})
			if bytes.Contains(replay.fields["input"], response.Output[0]["id"]) || !bytes.Contains(replay.fields["input"], mustTestJSON(t, calls[0])) {
				t.Fatalf("call or user-only replay changed: %s", replay.fields["input"])
			}
		})
	}
}

func TestSubagentTranslatedEditActivityJSONAndSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			calls := 0
			proxy := newManagedMekugiProxy(t)
			root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
			child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", nil)
			call := testMekugiItem()
			original := mustTestJSON(t, call)
			child.directory = t.TempDir()

			if stream {
				if _, err := child.TransformSSE(mustTestJSON(t, map[string]any{
					"type": "response.output_item.done", "item": call,
				})); err != nil {
					t.Fatal(err)
				}
			} else {
				payload := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}})
				if _, err := child.TransformJSON(payload); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 0 {
				t.Fatalf("display translated or executed the edit again: %d translations", calls)
			}
			if got := mustTestJSON(t, call); !bytes.Equal(got, original) {
				t.Fatalf("activity changed the original call: %s", got)
			}

			visible, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			var response struct{ Output []map[string]json.RawMessage }
			if err := json.Unmarshal(visible, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Output) != 2 || !strings.Contains(commentaryText(t, response.Output[0]), "Started.") || !strings.Contains(commentaryText(t, response.Output[1]), "hpatch") {
				t.Fatalf("shell edit activity missing: %s", visible)
			}
			if calls != 0 {
				t.Fatalf("root delivery retranslated the edit: %d translations", calls)
			}
		})
	}
}

func TestSubagentPatchCommentarySuppressedJSONAndSSE(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: a\n+x\n*** Update File: b\n@@\n-old\n+new\n*** End Patch\n"
	for _, name := range []string{"apply_patch", "exec"} {
		for _, stream := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/json", true: "/sse"}[stream], func(t *testing.T) {
				proxy := newManagedMekugiProxy(t)
				root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
				child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", nil)
				input := patch
				if name == "exec" {
					input = "await tools.apply_patch(" + string(mustTestJSON(t, patch)) + ")"
				}
				call := map[string]any{"type": "custom_tool_call", "name": name, "id": "edit", "call_id": "edit", "input": input}
				payload := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}})
				if stream {
					event := mustTestJSON(t, map[string]any{"type": "response.output_item.done", "item": call})
					events, err := child.TransformSSE(event)
					if err != nil || len(events) != 1 || !bytes.Equal(events[0], event) {
						t.Fatalf("edit execution changed: %s, %v", events, err)
					}
				} else if output, err := child.TransformJSON(payload); err != nil || !bytes.Equal(output, payload) {
					t.Fatalf("edit execution changed: %s, %v", output, err)
				}
				visible, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
				if err != nil {
					t.Fatal(err)
				}
				var response struct{ Output []map[string]json.RawMessage }
				if err := json.Unmarshal(visible, &response); err != nil || len(response.Output) != 1 ||
					!strings.Contains(commentaryText(t, response.Output[0]), "Started.") {
					t.Fatalf("edit generated commentary: %s, %v", visible, err)
				}
			})
		}
	}
}

func TestSubagentToolActivityRejectsPartialCalls(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
	child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", nil)
	for _, status := range []string{"in_progress", "incomplete"} {
		call := map[string]json.RawMessage{"type": mustTestJSON(t, "function_call"), "id": mustTestJSON(t, status), "status": mustTestJSON(t, status)}
		child.collectSubagentToolCall(call)
	}
	if got := proxy.activity.drain("r", root.activityStarted, maxCommentaryPublicationBytes); len(got) != 1 || !strings.Contains(commentaryText(t, got[0]), "Started.") {
		t.Fatal("partial calls projected")
	}
}

func TestCommentaryCodeEscapesBackticks(t *testing.T) {
	if got := attributedCommentary("/root/a`b", "Working."); got != "[`` /root/a`b ``] Working." {
		t.Fatalf("code span: %s", got)
	}
	text := attributedCommentary("/root/a`b", "Working.")
	if attributedCommentary("/root/a`b", text) != text {
		t.Fatal("attribution duplicated")
	}
}

func TestSubagentBatchToolActivityJSONAndSSE(t *testing.T) {
	const source = "sed -n '1,360p' file.go\n#!bash\nrg pattern file.go\n#!bash\ngit status --short\ngit log -1 --oneline"
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
			child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/worker", nil)
			call := map[string]any{"type": "shell_call", "id": "batch", "status": "completed", "action": map[string]any{"commands": []string{source}}}
			payload := mustTestJSON(t, map[string]any{"status": "completed", "output": []any{call}})
			if stream {
				for _, eventType := range []string{"response.output_item.added", "response.output_item.done"} {
					event := mustTestJSON(t, map[string]any{"type": eventType, "item": call})
					events, err := child.TransformSSE(event)
					if err != nil || len(events) != 1 || !bytes.Equal(events[0], event) {
						t.Fatalf("native child call changed: %s, %v", events, err)
					}
				}
				if _, err := child.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.completed", "response": json.RawMessage(payload)})); err != nil {
					t.Fatal(err)
				}
			} else if output, err := child.TransformJSON(payload); err != nil || !bytes.Equal(output, payload) {
				t.Fatalf("native child call changed: %s, %v", output, err)
			}
			output, err := root.TransformJSON([]byte(`{"status":"completed","output":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			var response struct{ Output []map[string]json.RawMessage }
			if err := json.Unmarshal(output, &response); err != nil || len(response.Output) != 2 {
				t.Fatalf("batch activity deduplication: %s, %v", output, err)
			}
			text := commentaryText(t, response.Output[1])
			for _, want := range []string{"Read `file.go 1:360`", "Search", "pattern", "git status --short", "git log -1 --oneline"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %s", want, text)
				}
			}
			if strings.Contains(text, "#!batch") {
				t.Fatalf("transport framing leaked into activity: %s", text)
			}
		})
	}
}
