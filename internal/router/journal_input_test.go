package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJournalNativeInputsAreOperationSpecific(t *testing.T) {
	fields := map[string]jsonv1.RawMessage{"tools": mustMarshalJSON([]any{map[string]any{
		"type": "function", "name": "exec_command",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}},
	}})}
	if _, err := prepareCommentaryTools(fields, decodeResponsesToolCatalog(fields)); err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Parameters struct {
			Properties struct {
				Journal struct {
					Defs  map[string]jsonv1.RawMessage `json:"$defs"`
					Items struct {
						AnyOf []struct {
							AdditionalProperties bool                         `json:"additionalProperties"`
							Properties           map[string]jsonv1.RawMessage `json:"properties"`
							Required             []string                     `json:"required"`
						} `json:"anyOf"`
					} `json:"items"`
				} `json:"journal"`
			} `json:"properties"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(fields["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	input := tools[0].Parameters.Properties.Journal
	want := []struct {
		op, fields, required string
	}{
		{"plan", "op under tasks reset", "op tasks"},
		{"add", "op under kind title body before state reason agent", "op kind title"},
		{"add", "op under kind title body before", "op title"},
		{"set", "op p title body state reason agent superseded_by", "op p"},
		{"log", "op p text", "op text"},
		{"remove", "op p", "op p"},
		{"finish", "op", "op"},
	}
	if len(input.Items.AnyOf) != len(want) {
		t.Fatalf("mutation variants = %d, want %d", len(input.Items.AnyOf), len(want))
	}
	for i, variant := range input.Items.AnyOf {
		var operation struct {
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(variant.Properties["op"], &operation); err != nil || !reflect.DeepEqual(operation.Enum, []string{want[i].op}) {
			t.Fatalf("variant %d op = %v, err=%v", i, operation.Enum, err)
		}
		var names []string
		for name := range variant.Properties {
			names = append(names, name)
		}
		expected := strings.Fields(want[i].fields)
		slices.Sort(names)
		slices.Sort(expected)
		if variant.AdditionalProperties || !reflect.DeepEqual(names, expected) || !reflect.DeepEqual(variant.Required, strings.Fields(want[i].required)) {
			t.Fatalf("variant %d is not a closed operation shape: %+v", i, variant)
		}
	}
	for i, kinds := range map[int][]string{1: {"task"}, 2: {"note", "context"}} {
		var kind struct {
			Enum []string `json:"enum"`
		}
		if err := json.Unmarshal(input.Items.AnyOf[i].Properties["kind"], &kind); err != nil || !reflect.DeepEqual(kind.Enum, kinds) {
			t.Fatalf("add variant %d kinds = %v, err=%v", i, kind.Enum, err)
		}
	}
	var plan struct {
		AdditionalProperties bool                         `json:"additionalProperties"`
		Properties           map[string]jsonv1.RawMessage `json:"properties"`
		Required             []string                     `json:"required"`
	}
	if err := json.Unmarshal(input.Defs["task"], &plan); err != nil {
		t.Fatal(err)
	}
	if plan.AdditionalProperties || !reflect.DeepEqual(plan.Required, []string{"title"}) || len(plan.Properties) != 6 || plan.Properties["agent"] != nil {
		t.Fatalf("planned task input is not closed: %+v", plan)
	}
	for _, tasks := range []jsonv1.RawMessage{input.Items.AnyOf[0].Properties["tasks"], plan.Properties["tasks"]} {
		var array struct {
			Items struct {
				AnyOf []struct {
					Ref string `json:"$ref"`
				} `json:"anyOf"`
			} `json:"items"`
		}
		if err := json.Unmarshal(tasks, &array); err != nil || len(array.Items.AnyOf) != 2 || array.Items.AnyOf[1].Ref != "#/properties/journal/$defs/task" {
			t.Fatalf("planned task reference is not rooted at the projected tool input: %s, err=%v", tasks, err)
		}
	}
}

func TestJournalProjectedInputsTypeCheck(t *testing.T) {
	tsc, err := exec.LookPath("tsc")
	if err != nil {
		t.Skip("TypeScript is required to check projected journal declarations")
	}
	fields := map[string]jsonv1.RawMessage{"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(testCodeModeDescription)})}
	if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), ""); err != nil {
		t.Fatal(err)
	}
	description := decodeResponsesToolCatalog(fields).additional[0].tools.tools[0].nested.tools[0].Description
	_, journalDescription, hasJournal := strings.Cut(description, "<journal>\n")
	_, after, ok := strings.Cut(journalDescription, "```ts\n")
	declarations, _, closed := strings.Cut(after, "\n```")
	if !hasJournal || !ok || !closed {
		t.Fatal("projected exec description has no journal declarations")
	}
	const checks = `
async function checks() {
  const task: string | null = await journal({op: "add", kind: "task", title: "Delegate", agent: "/root/child"});
  await journal({op: "set", p: "/1", agent: "/root/child"});
  await journal({op: "add", title: "Note"});
  await journal({op: "add", kind: "context", title: "Constraint"});
  const plan: string[] = await journal({op: "plan", tasks: ["One", {title: "Two", reason: "Waiting", state: "blocked", tasks: ["Child"]}]});
  const batch: string[] = await journal([{op: "log", text: "Checked"}, {op: "add", kind: "task", title: "Delegate", agent: "/root/child"}]);
  const nodes: JournalNode[] = await journal({op: "read", p: "/1", depth: 0, agent: "/root/child", view: "tasks"});
  await journal({op: "read", view: "own", depth: 0});
  // @ts-expect-error invalid read view
  await journal({op: "read", view: "unknown"});
  await journal({op: "remove", p: "/1"});
  const operation: JournalMutation = Math.random() > 0.5 ? {op: "plan", tasks: []} : {op: "log", text: "Checked"};
  await journal(operation);
  // @ts-expect-error agent requires explicit task creation
  await journal({op: "add", title: "Note", agent: "/root/child"});
  // @ts-expect-error notes cannot bind an agent
  await journal({op: "add", kind: "note", title: "Note", agent: "/root/child"});
  // @ts-expect-error context has no task state
  await journal({op: "add", kind: "context", title: "Constraint", state: "working"});
  // @ts-expect-error unknown field on task creation
  await journal({op: "add", kind: "task", title: "Task", path: "/1"});
  // @ts-expect-error set requires p
  await journal({op: "set", agent: "/root/child"});
  // @ts-expect-error under is not a set operand
  await journal({op: "set", p: "/1", under: "/2"});
  // @ts-expect-error logs cannot bind agents
  await journal({op: "log", text: "Checked", agent: "/root/child"});
  // @ts-expect-error array members retain operation-specific fields
  await journal([{op: "remove", p: "/1", title: "Ignored"}]);
  // @ts-expect-error plan task objects do not support agent
  await journal({op: "plan", tasks: [{title: "Task", agent: "/root/child"}]});
  // @ts-expect-error nested plan objects are also closed
  await journal({op: "plan", tasks: [{title: "Parent", tasks: [{title: "Task", extra: true}]}]});
  // @ts-expect-error read cannot mutate state
  await journal({op: "read", state: "done"});
  // @ts-expect-error invalid state
  await journal({op: "set", p: "/1", state: "unknown"});
}
`
	file := filepath.Join(t.TempDir(), "journal.ts")
	if err := os.WriteFile(file, []byte(declarations+checks), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), tsc, "--strict", "--noEmit", "--lib", "es2020", file)
	command.Dir = filepath.Dir(file)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("projected journal types do not check: %v\n%s", err, output)
	}
}
