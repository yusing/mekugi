package presentation

import (
	"strings"
	"testing"
)

func TestToolGenerationLabels(t *testing.T) {
	for _, test := range []struct{ name, want string }{
		{"functions.request_user_input_async", "Asking user question"},
		{"request_user_input", "Asking user question"},
		{"apply_patch", "Emitting a patch"},
		{"exec_command", "Executing a command"},
		{"mcp__threadcontrol__spawn_agent", "Spawning subagent"},
		{"mcp__other__lookup", "Calling `mcp__other__lookup`"},
	} {
		if got := NewToolGeneration(test.name, false).Label(); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
}

// Keep this inventory aligned with the stock execution builtins in
// backends/codex/binding/execution_source.go and threadcontrol's ToolCatalog.
// new_context is deliberately omitted: PrepareStockExecution removes it.
func TestToolGenerationCatalog(t *testing.T) {
	for _, test := range []struct{ name, want string }{
		{"exec", "Emitting JavaScript"},
		{"wait", "Waiting for JavaScript"},
		{"write_stdin", "Interacting with a command"},
		{"view_image", "Viewing an image"},
		{"list_mcp_resources", "Listing resources"},
		{"list_mcp_resource_templates", "Listing resource templates"},
		{"read_mcp_resource", "Reading resource"},
		{"get_context_remaining", "Checking remaining context"},
		{"clock.curr_time", "Checking time"},
		{"clock.sleep", "Sleeping"},
		{"curr_time", "Checking time"},
		{"sleep", "Sleeping"},
		{"web.run", "Browsing the web"},
		{"image_gen.imagegen", "Generating an image"},
		{"mcp__threadcontrol.spawn_agent", "Spawning subagent"},
		{"mcp__threadcontrol.followup_task", "Assigning follow-up to"},
		{"mcp__threadcontrol.send_message", "Sending message to"},
		{"mcp__threadcontrol.interrupt_agent", "Interrupting agent"},
		{"mcp__threadcontrol.inspect", "Inspecting agent"},
		{"mcp__threadcontrol.list_agents", "Listing agents"},
		{"mcp__threadcontrol.wait_agent", "Waiting for agents"},
	} {
		for _, name := range []string{test.name, strings.ReplaceAll(test.name, ".", "__")} {
			for _, prefix := range []string{"", "functions.", "tools."} {
				g := NewToolGeneration(prefix+name, false)
				g.Append(`{`)
				if got := g.Label(); got != test.want {
					t.Errorf("%s%s: %q, want %q", prefix, name, got, test.want)
				}
			}
		}
	}
}

func TestToolGenerationIdentityRefinement(t *testing.T) {
	for _, test := range []struct{ name, input, want string }{
		{"mcp__threadcontrol__followup_task", `{"target":"worker","message":"long`, "Assigning follow-up to `worker`"},
		{"mcp__threadcontrol__send_message", `{"message":"hello","target":"agent:main/reviewer"}`, "Sending message to `agent:main/reviewer`"},
		{"mcp__threadcontrol__interrupt_agent", `{"target":"worker"`, "Interrupting agent `worker`"},
		{"mcp__threadcontrol__inspect", `{"target":"worker"}`, "Inspecting agent `worker`"},
		{"mcp__threadcontrol__list_agents", `{"path_prefix":"agent:main/team"}`, "Listing agents `agent:main/team`"},
		{"view_image", `{"path":"/tmp/picture.png"}`, "Viewing an image `/tmp/picture.png`"},
		{"view_image", `{"path":"/tmp/\ud83d\ude00.png"}`, "Viewing an image `/tmp/😀.png`"},
		{"view_image", `{"path":"/tmp/😀.png"}`, "Viewing an image `/tmp/😀.png`"},
		{"list_mcp_resources", `{"server":"docs"}`, "Listing resources `docs`"},
		{"list_mcp_resource_templates", `{"server":"docs"}`, "Listing resource templates `docs`"},
		{"read_mcp_resource", `{"uri":"resource://docs/intro"}`, "Reading resource `resource://docs/intro`"},
		{"wait", `{"cell_id":"cell-1"}`, "Waiting for JavaScript `cell-1`"},
		{"mcp__other__spawn_agent", `{"task_name":"decoy"}`, "Calling `mcp__other__spawn_agent`"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, nested := range []bool{false, true} {
				for _, split := range []bool{false, true} {
					g := NewToolGeneration(test.name, false)
					input := test.input
					if nested {
						g = NewToolGeneration("", true)
						input = "await tools." + test.name + "(" + input
					}
					if split {
						for _, c := range input {
							g.Append(string(c))
						}
					} else {
						g.Append(input)
					}
					if got := g.Label(); got != test.want {
						t.Errorf("nested=%v split=%v: %q, want %q", nested, split, got, test.want)
					}
				}
			}
		})
	}
}

func TestToolGenerationTargetIsLiteralAndLocal(t *testing.T) {
	for _, input := range []string{
		`{target:"unfinished`,
		`{target:computed}`,
		"{target:`agent:${name}`}",
		`{metadata:{target:"decoy"},message:"long`,
		`{message:"target: decoy"}`,
		`{target:"first", ...{target:"second"}}`,
		`{target:"first", ["target"]:"second"}`,
		`{target:"first", get target(){return "second"}}`,
		`{target:"first", target}`,
		`{target:"first", ...overrides}`,
	} {
		for _, nested := range []bool{false, true} {
			for _, split := range []bool{false, true} {
				g := NewToolGeneration("mcp__threadcontrol__send_message", false)
				source := input
				if nested {
					g = NewToolGeneration("", true)
					source = "tools.mcp__threadcontrol__send_message(" + input
				}
				if split {
					for _, c := range source {
						g.Append(string(c))
					}
				} else {
					g.Append(source)
				}
				if got := g.Label(); got != "Sending message to" {
					t.Errorf("%s nested=%v split=%v: %q", input, nested, split, got)
				}
			}
		}
	}
	g := NewToolGeneration("", true)
	g.Append(`await tools.mcp__threadcontrol__inspect({target:"first"}); await tools.view_image({path:"second`)
	if got := g.Label(); got != "Viewing an image" {
		t.Fatal(got)
	}
	g.Append(`.png"})`)
	if got := g.Label(); got != "Viewing an image `second.png`" {
		t.Fatal(got)
	}
}

func TestToolGenerationIncompleteJavaScript(t *testing.T) {
	for _, test := range []struct{ source, want string }{
		{`await tools.exec_command({cmd: "long`, "Executing a command"},
		{`await tools.exec_command /* reason */ ({cmd: "long`, "Executing a command"},
		{"const value = `${await tools.exec_command({cmd: \"long", "Executing a command"},
		{"const value = `${await tools.exec_command({cmd:\"ok\"})} more ${await tools.apply_patch(\"long", "Emitting a patch"},
		{"const value = `${await tools.mcp__threadcontrol__spawn_agent({task_name:\"worker\",message:\"long", "Spawning subagent `worker`"},
		{`const r = await tools.apply_patch("*** Begin Patch`, "Emitting a patch"},
		{`await tools.request_user_input_async({questions: [`, "Asking user question"},
		{`await tools.mcp__threadcontrol__spawn_agent({message: "long`, "Spawning subagent"},
		{`await tools.mcp__threadcontrol.spawn_agent({task_name: "worker", message: "long`, "Spawning subagent `worker`"},
		{`await tools.mcp__threadcontrol__spawn_agent({metadata: {task_name: "decoy"}, message: "long`, "Spawning subagent"},
		{`await tools.mcp__other__lookup({query: "long`, "Calling `mcp__other__lookup`"},
		{`const note = "tools.exec_command({cmd: bad";`, "Emitting JavaScript"},
		{"// tools.exec_command({cmd: bad", "Emitting JavaScript"},
		{`const re = /tools.exec_command\(/;`, "Emitting JavaScript"},
		{"// a quote ' in a comment\nawait tools.exec_command({cmd: \"long", "Executing a command"},
		{`const re = /'/; await tools.exec_command({cmd: "long`, "Executing a command"},
		{`fake.tools.exec_command({cmd: "long`, "Emitting JavaScript"},
		{`await tools.exec_command({cmd: "first"}); await tools.apply_patch("long`, "Emitting a patch"},
	} {
		t.Run(test.source, func(t *testing.T) {
			for _, split := range []bool{false, true} {
				g := NewToolGeneration("", true)
				if split {
					for _, c := range test.source {
						g.Append(string(c))
					}
				} else {
					g.Append(test.source)
				}
				if got := g.Label(); got != test.want {
					t.Errorf("split=%v: %q, want %q", split, got, test.want)
				}
			}
		})
	}
}

func TestToolGenerationSpawnRefinement(t *testing.T) {
	g := NewToolGeneration("mcp__threadcontrol__spawn_agent", false)
	g.Append(`{"message":"`)
	for range 1024 {
		g.Append(strings.Repeat("payload ", 128))
	}
	if got := g.Label(); got != "Spawning subagent" {
		t.Fatal(got)
	}
	g.Append(`", "task_name":"work`)
	if got := g.Label(); got != "Spawning subagent" {
		t.Fatalf("invented an unfinished name: %q", got)
	}
	g.Append(`er", "other":`)
	if got := g.Label(); got != "Spawning subagent `worker`" {
		t.Fatal(got)
	}
	g.Append(`null}`)
	if got := g.Label(); got != "Spawning subagent `worker`" {
		t.Fatalf("completed arguments lost the identified name: %q", got)
	}
}

func TestToolGenerationPayloadChunksDoNotReparse(t *testing.T) {
	for _, test := range []struct{ name, prefix, want string }{
		{"", `await tools.exec_command({cmd:`, "Executing a command"},
		{"", `await tools.mcp__threadcontrol.send_message({message:`, "Sending message to"},
		{"mcp__threadcontrol.send_message", `{message:`, "Sending message to"},
	} {
		for _, quote := range []string{"\"", "`"} {
			g := NewToolGeneration(test.name, test.name == "")
			g.Append(test.prefix + quote)
			g.input.Grow(1 << 20)
			payload := strings.Repeat("x", 1024)
			if allocations := testing.AllocsPerRun(100, func() { g.Append(payload) }); allocations != 0 {
				t.Fatalf("%s quoted payload chunks allocated parser work: %v", test.name, allocations)
			}
			if got := g.Label(); got != test.want {
				t.Fatal(got)
			}
		}
	}
}

func TestToolGenerationRosterPublication(t *testing.T) {
	model := Model[modelSource, modelAgent]{Agents: []modelAgent{
		{AgentFacts: AgentFacts{Name: "/root", Responding: true, Generating: "Emitting a patch"}},
		{AgentFacts: AgentFacts{Name: "/root/child", Responding: true, Generating: "Executing a command"}},
	}}
	model.Append(modelSource{Facts: Facts{Seq: 1, Agent: "/root"}}, []Value{{Kind: "summary", Label: "Older reasoning"}})
	publication := model.Publish()
	for i, want := range []string{"Emitting a patch", "Executing a command"} {
		values := CompactRoster(publication.Roster[i].Agent.Summary, "/root")
		if len(values) != 1 || values[0].Body != want || !values[0].Live {
			t.Fatalf("roster %d: %+v", i, values)
		}
	}
	if len(publication.Entries) != 1 || publication.Entries[0].Values[0].Label != "Older reasoning" {
		t.Fatal("generation changed the transcript")
	}
}
