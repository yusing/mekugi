package router

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSubagentObservedUsage(t *testing.T) {
	data, err := os.ReadFile("testdata/subagent_activity_usage.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Sources map[string]string `json:"sources"`
		Cases   []struct {
			Tool        string `json:"tool"`
			DebugSource string `json:"debug_source"`
			DebugLine   int    `json:"debug_line"`
			Name        string `json:"name"`
			Source      string `json:"source"`
			CallID      string `json:"call_id"`
			Selection   string `json:"selection"`
			Input       string `json:"input"`
			Want        string `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Cases) == 0 {
		t.Fatal("empty observed-usage corpus")
	}
	for _, tc := range document.Cases {
		// Keep captured inputs unchanged on disk; exercise their equivalent
		// journal authoring form after removal of the commentary builtin.
		tc.Input = strings.ReplaceAll(tc.Input, "commentary '", "journal add '")
		t.Run(tc.Name, func(t *testing.T) {
			if document.Sources[tc.Source] == "" || tc.CallID == "" || tc.Selection == "" {
				t.Fatal("missing observed-source provenance")
			}
			if tc.DebugSource != "" && (document.Sources[tc.DebugSource] == "" || tc.DebugLine < 1) {
				t.Fatal("missing debug-source provenance")
			}
			// Exercise original shell calls and transparent Code Mode carriers.
			names := []string{"shell", "exec_command", "exec"}
			if tc.Tool != "" {
				names = []string{tc.Tool}
			}
			for _, name := range names {
				item := map[string]json.RawMessage{"name": mustMarshalJSON(name)}
				switch name {
				case "shell":
					item["input"] = mustMarshalJSON(tc.Input)
				case "exec_command":
					item["arguments"] = mustMarshalJSON(string(mustMarshalJSON(map[string]string{"cmd": tc.Input})))
				case "exec":
					if tc.Tool == "exec" {
						item["input"] = mustMarshalJSON(tc.Input)
					} else {
						item["input"] = mustMarshalJSON("text(await tools.exec_command({cmd:" + string(mustMarshalJSON(tc.Input)) + "}));")
					}
				}
				if got := subagentToolActivityText(item, name); got != tc.Want {
					t.Errorf("%s: got %q, want %q", name, got, tc.Want)
				}
			}
			for _, stream := range []bool{false, true} {
				t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
					proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
					root, _ := prepareActivityTest(t, proxy, "root", "r", "", "/root", nil)
					child, _ := prepareActivityTest(t, proxy, "child", "c", "r", "/root/usage", nil)
					root.drainActivity() // The corpus asserts tool activity, not the start notice.
					name := tc.Tool
					if name == "" {
						name = "shell"
					}
					call := map[string]any{"type": "custom_tool_call", "name": name, "id": "observed", "call_id": "observed", "input": tc.Input}
					payload := mustMarshalJSON(map[string]any{"status": "completed", "output": []any{call}})
					if stream {
						if _, err := child.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.output_item.done", "item": call})); err != nil {
							t.Fatal(err)
						}
						if _, err := child.TransformSSE(mustMarshalJSON(map[string]any{"type": "response.completed", "response": json.RawMessage(payload)})); err != nil {
							t.Fatal(err)
						}
					} else if _, err := child.TransformJSON(payload); err != nil {
						t.Fatal(err)
					}
					// Repeated completed-call observations must not duplicate activity.
					child.collectSubagentToolCall(map[string]json.RawMessage{
						"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(name),
						"id": mustMarshalJSON("observed"), "call_id": mustMarshalJSON("observed"), "input": mustMarshalJSON(tc.Input),
					})
					events := root.drainActivity()
					if tc.Want == "" {
						if len(events) != 0 {
							t.Fatalf("commentary-only activity: %v", events)
						}
					} else {
						if len(events) != 1 {
							t.Fatalf("want one activity, got %v", events)
						}
						want := toolActivityGroup("[`/root/usage`] ", tc.Want)
						if got := commentaryText(t, events[0]); got != want {
							t.Fatalf("got %q, want %q", got, want)
						}
					}
				})
			}
		})
	}
}

func TestSubagentSearchPatternAndPipelineBoundaries(t *testing.T) {
	for _, source := range []string{
		"rg needle $(touch marker)",
		"rg needle \"$path\"",
		"rg needle <(cat file)",
		"rg needle *.go | head -n \"$limit\"",
		"rg needle *.go | head 40",
		"rg needle *.go | tail 40",
		"rg needle *.go | head -n 0",
		"rg needle *.go | head -n 10 extra-file",
		"rg needle *.go | sort -o output",
		"rg needle *.go | sort extra-file",
		"rg needle *.go | sed -i 's/a/b/' file",
		"rg needle *.go > output",
		"rg needle *.go | head -10 &",
		"find /tmp -name '*.go' -delete | head -10",
		"ls \"$path\"",
	} {
		t.Run(source, func(t *testing.T) {
			want := "Run\n```bash\n" + source + "\n```"
			if got := toolActivityShell(source); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
	for _, source := range []string{
		"rg needle *.go | tail -60",
		"rg needle *.go | sort -nr -u | head -n 40",
		"rg needle *.go | grep 'match' | head -20",
		"rg needle *.go |\n  head -n 40",
	} {
		t.Run(source, func(t *testing.T) {
			got := toolActivityShell(source)
			if !strings.HasPrefix(got, "Search ") || !strings.Contains(got, source) {
				t.Fatalf("lost search pipeline: %q", got)
			}
		})
	}
}
