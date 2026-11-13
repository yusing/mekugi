package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestMCPToolsValidateArgumentsBeforeAdmission(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	contribution := Codex.MCP(driver)
	left, right := net.Pipe()
	server, err := contribution.Server.Connect(t.Context(), &mcp.IOTransport{Reader: left, Writer: left}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: right, Writer: right}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := make(map[string]string)
	for _, tool := range ToolCatalog() {
		descriptions[tool.Name] = tool.Description
	}
	var names []string
	for _, tool := range catalog.Tools {
		names = append(names, tool.Name)
		if tool.Description != descriptions[tool.Name] {
			t.Errorf("effective %s description differs from its owning catalog", tool.Name)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for name, field := range schema.Properties {
			if field.Description == "" {
				t.Errorf("%s.%s omitted field guidance", tool.Name, name)
			}
		}
		if tool.Name == "inspect" && (!strings.Contains(tool.Description, "idle or completed agents") || !strings.Contains(tool.Description, "retained observations without starting work") || !strings.Contains(tool.Description, "unavailable observations are null")) {
			t.Fatalf("inspect description: %q", tool.Description)
		}
		if tool.Name == "wait_agent" && (!strings.Contains(tool.Description, "ends the host turn") || !strings.Contains(tool.Description, "later wait_agent output")) {
			t.Fatalf("wait description: %q", tool.Description)
		}
		if tool.Name == "wait_agent" && (!strings.Contains(tool.Description, "unsettled work or unread outcomes") || !strings.Contains(schema.Properties["yield_time_ms"].Description, "changed-agent summary collection")) {
			t.Fatalf("wait catalog lost selection or quiet-interval semantics: %q %q", tool.Description, schema.Properties["yield_time_ms"].Description)
		}
	}
	slices.Sort(names)
	expected := []string{"followup_task", "inspect", "interrupt_agent", "list_agents", "send_message", "spawn_agent", "wait_agent"}
	if !slices.Equal(names, expected) {
		t.Fatalf("catalog=%v", names)
	}
	for _, test := range []struct{ name, arguments string }{
		{"spawn_agent", `{"task_name":"child"}`},
		{"spawn_agent", `{"task_name":"child","message":"task","preload":{"sources":[{"unknown":"file"}]}}`},
		{"spawn_agent", `{"task_name":"child","message":"task","preload":{"sources":[{"path":"file","ranges":[[1]]}]}}`},
		{"list_agents", `{"unknown":true}`},
		{"wait_agent", `{"compact":"yes"}`},
		{"inspect", `{}`},
		{"inspect", `{"target":"/root","unknown":true}`},
		{"wait_agent", `{"target":[]}`},
		{"wait_agent", `{"yield_time_ms":299999}`},
		{"wait_agent", `{"yield_time_ms":true}`},
		{"wait_agent", `{"target":"all"}`},
		{"wait_agent", `{"target":["child"]}`},
		{"wait_agent", `{"timeout_ms":1.5}`},
		{"wait_agent", `{"cancel_id":"wait"}`},
		{"wait_agent", `{"wait_mode":"invalid"}`},
		{"wait_agent", `{"wait_mode":""}`},
		{"send_message", `{"message":"hello"}`},
		{"send_message", `{"target":"child","message":"question","btw":"true"}`},
		{"followup_task", `{"target":"child","message":"task","btw":false}`},
		{"followup_task", `{"target":"child","message":3}`},
		{"interrupt_agent", `{"target":"child","message":"extra"}`},
	} {
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: test.name, Arguments: jsontext.Value(test.arguments)})
		if err != nil || !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, `validating "arguments"`) {
			t.Fatalf("%s(%s): result=%+v err=%v", test.name, test.arguments, result, err)
		}
	}
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_agents", Arguments: map[string]any{}})
	if err != nil || !result.IsError || result.Content[0].(*mcp.TextContent).Text != "incomplete host MCP identity" {
		t.Fatalf("missing identity: result=%+v err=%v", result, err)
	}
	meta := mcp.Meta{"threadId": caller.Thread, "sessionId": caller.Session, "callId": "wait", "itemId": caller.Item, "windowId": "window",
		"x-codex-turn-metadata": map[string]any{"thread_id": caller.Thread, "session_id": caller.Session, "turn_id": caller.Turn, "codex_version": "0.163.0", "model": "model", "reasoning_effort": "low"}}
	host.ask = func(_ context.Context, target *control.Member, text string) (string, error) {
		if target.Thread != "child" || text != "question" {
			t.Fatal("MCP question lost its admitted target or text")
		}
		return "side answer", nil
	}
	question, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "send_message", Meta: meta, Arguments: map[string]any{"target": "agent:main/child", "message": "question", "btw": true}})
	if err != nil || question.IsError || len(question.Content) != 1 || !strings.Contains(question.Content[0].(*mcp.TextContent).Text, `"answer":"side answer"`) {
		t.Fatalf("MCP question result=%+v error=%v", question, err)
	}
	for _, tool := range []string{"wait_agent", "list_agents"} {
		arguments := map[string]any{}
		if tool == "list_agents" {
			arguments = map[string]any{"path_prefix": "agent:main"}
		}
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Meta: meta, Arguments: arguments})
		if tool == "wait_agent" {
			if err != nil || result.IsError || len(result.Content) != 0 || result.StructuredContent != nil {
				t.Fatalf("waiting acknowledgement is not empty: %+v, %v", result, err)
			}
			continue
		}
		if err != nil || result.IsError || len(result.Content) != 1 {
			t.Fatalf("%s: result=%+v err=%v", tool, result, err)
		}
		payload := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(payload, "agent:main/child") || strings.Contains(payload, "/root") {
			t.Fatalf("%s visible identities: %s", tool, payload)
		}
		if !strings.Contains(payload, `"state":"waiting"`) || strings.Contains(payload, `"registered"`) {
			t.Fatalf("%s public wait state: %s", tool, payload)
		}
	}
	value, err := driver.Call(t.Context(), caller, "list_agents", jsontext.Value(`{"path_prefix":"agent:main"}`))
	if err != nil || !strings.Contains(string(value), "agent:main/child") || strings.Contains(string(value), "/root") {
		t.Fatalf("native dispatch: %s, %v", value, err)
	}
	foreign := caller
	foreign.Session = "foreign"
	if _, err := driver.Call(t.Context(), foreign, "list_agents", jsontext.Value(`{}`)); err == nil {
		t.Fatal("native dispatch admitted a foreign session")
	}
	if _, err := driver.Call(t.Context(), caller, "unknown", jsontext.Value(`{}`)); err == nil {
		t.Fatal("native dispatch admitted an unknown tool")
	}
}

func TestControlActionReturnsCanonicalTargetForAlias(t *testing.T) {
	for _, tool := range []string{"send_message", "followup_task"} {
		t.Run(tool, func(t *testing.T) {
			driver, _, _, caller := reconciliationFixture(t)
			caller.Call = tool
			result, err := driver.controlAction(t.Context(), caller, []byte(`{"target":"child","message":"next assignment"}`), tool)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := result.(map[string]any)
			if !ok || got["id"] != caller.Call || got["state"] != "confirmed" || got["thread_id"] != "child" || got["task_name"] != "/root/child" {
				t.Fatalf("action acknowledgement = %#v; want confirmed action identifying child and /root/child", result)
			}
		})
	}
}

func TestWaitAgentKeepsWaitIdentityInternal(t *testing.T) {
	driver, _, _, caller := reconciliationFixture(t)
	caller.Call, caller.Item = "wait", "exec"
	result, err := driver.waitAgent(t.Context(), caller, []byte(`{"wait_mode":"all"}`))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["id"] != nil || fields["all"] != nil || string(fields["state"]) != `"waiting"` || string(fields["targets"]) != `["/root/child"]` {
		t.Fatalf("wait result: %s", payload)
	}
	if fields["instructions"] != nil {
		t.Fatalf("wait acknowledgement retained delivery instructions: %s", payload)
	}
	member, _ := driver.control.Member(caller.Thread)
	if member.Wait == nil || member.Wait.ID != caller.Call || member.Wait.State != "registered" || member.Wait.Mode != control.WaitAll {
		t.Fatal("lost internal wait correlation or mode")
	}
}

func TestWaitAgentImmediateOutcomesKeepContent(t *testing.T) {
	for _, state := range []string{"timeout", "completed"} {
		t.Run(state, func(t *testing.T) {
			driver, _, _, caller := reconciliationFixture(t)
			caller.Call = "wait"
			args := []byte(`{"timeout_ms":0}`)
			if state == "completed" {
				if err := driver.control.HostCompleted(t.Context(), "child", "child-turn", control.Completed, control.HostFailure{}); err != nil {
					t.Fatal(err)
				}
				if err := driver.DispatchPending(t.Context(), "child"); err != nil {
					t.Fatal(err)
				}
				args = []byte(`{}`)
			}
			output, err := driver.Call(t.Context(), caller, "wait_agent", args)
			if err != nil {
				t.Fatal(err)
			}
			var result control.WaitResult
			if err := json.Unmarshal(output, &result); err != nil {
				t.Fatal(err)
			}
			if result.State != state || result.Metrics == nil || state == "completed" && (result.Event == nil || result.Event.Source != "child") {
				t.Fatalf("immediate outcome lost content: %s", output)
			}
		})
	}
}

func TestWaitAgentWaitsAfterImmediateTimeout(t *testing.T) {
	driver, _, _, caller := reconciliationFixture(t)
	for _, test := range []struct{ call, arguments, state, retainedState, id string }{
		{"poll", `{"timeout_ms":0}`, "timeout", "timeout", "poll"},
		{"poll", `{}`, "timeout", "timeout", "poll"},
		{"wait", `{}`, "waiting", "registered", "wait"},
		{"another", `{"timeout_ms":0}`, "waiting", "registered", "wait"},
	} {
		caller.Call = test.call
		result, err := driver.waitAgent(t.Context(), caller, []byte(test.arguments))
		if err != nil {
			t.Fatal(err)
		}
		wait := result.(control.WaitResult)
		if wait.State != test.state || wait.ID != test.id {
			t.Fatalf("%s(%s) = %+v; want %s id=%s", test.call, test.arguments, wait, test.state, test.id)
		}
		if _, found := wait.Metrics["/root/child"]; found != (test.state == "timeout") {
			t.Fatalf("metrics belong to the timeout outcome, not the waiting acknowledgement: %+v", wait)
		}
		member, _ := driver.control.Member(caller.Thread)
		if member.Wait == nil || member.Wait.ID != test.id || member.Wait.State != test.retainedState {
			t.Fatalf("retained wait = %+v", member.Wait)
		}
		if test.state == "waiting" && (member.Wait.Deadline != nil || member.Wait.RequestedMS != nil || member.Wait.EffectiveMS != nil) {
			t.Fatalf("new indefinite wait retained the expired deadline: %+v", member.Wait)
		}
	}
	if err := driver.control.Execution(t.Context(), caller.Thread, caller.Turn, caller.Item, "", true); err != nil {
		t.Fatal(err)
	}
	stop, err := driver.control.ClaimLocalStop(t.Context(), caller.Thread, caller.Turn, caller.Item)
	if err != nil || stop == nil || stop.Wait != "wait" {
		t.Fatalf("replacement wait local stop = %+v, %v", stop, err)
	}
}

func TestResetReloadAdmission(t *testing.T) {
	for _, test := range []struct{ tool, args string }{
		{"wait_agent", `{"compact":true,"reload":{"sources":[{"path":"task.json","ranges":[[2,4]]}]}}`},
		{"wait_agent", `{"compact":true,"reload":{"sources":[{"skill":"native","path":"/native/SKILL.md"}]}}`},
		{"wait_agent", `{"compact":true,"reload":{"sources":[{"changes":["amber1"]}]}}`},
		{"wait_agent", `{"compact":true,"reload":{"sources":[{"path":"task.json"}]}}`},
		{"wait_agent", `{"compact":true,"reload":{"sources":[{"path":"task.json","ranges":[[2]]}]}}`},
		{"wait_agent", `{"compact":true,"reload":{"sources":[{"unknown":"task.json"}]}}`},
		{"wait_agent", `{"reload":{"sources":[]}}`},
		{"wait_agent", `{"compact":true,"reload":null}`},
	} {
		t.Run(test.tool+test.args, func(t *testing.T) {
			driver, _, _, caller := reconciliationFixture(t)
			caller.Cell, caller.Window = "cell", "window"
			if _, err := driver.Call(t.Context(), caller, test.tool, []byte(test.args)); err == nil {
				t.Fatal("invalid reload admitted")
			}
			member, _ := driver.control.Member(caller.Thread)
			if member.Wait != nil {
				t.Fatal("rejection changed wait")
			}
			if _, err := driver.Call(t.Context(), caller, "wait_agent", []byte(`{"compact":true,"reload":{"sources":["@task.json#L2-L4"]}}`)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
