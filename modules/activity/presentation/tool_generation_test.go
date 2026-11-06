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
	for _, quote := range []string{"\"", "`"} {
		g := NewToolGeneration("", true)
		g.Append(`await tools.exec_command({cmd:` + quote)
		g.input.Grow(1 << 20)
		payload := strings.Repeat("x", 1024)
		if allocations := testing.AllocsPerRun(100, func() { g.Append(payload) }); allocations != 0 {
			t.Fatalf("quoted payload chunks allocated parser work: %v", allocations)
		}
		if got := g.Label(); got != "Executing a command" {
			t.Fatal(got)
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
