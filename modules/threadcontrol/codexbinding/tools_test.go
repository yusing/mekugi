package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestMCPToolsValidateArgumentsBeforeAdmission(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	discovery := &modelDiscoveryHost{Native: host}
	driver.host = discovery
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
	var names []string
	for _, tool := range catalog.Tools {
		names = append(names, tool.Name)
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
	}
	slices.Sort(names)
	expected := []string{"followup_task", "inspect", "interrupt_agent", "list_agents", "list_models", "send_message", "spawn_agent", "wait_agent"}
	if !slices.Equal(names, expected) {
		t.Fatalf("catalog=%v", names)
	}
	for _, test := range []struct{ name, arguments string }{
		{"spawn_agent", `{"task_name":"child"}`},
		{"spawn_agent", `{"task_name":"child","message":"task","preload":{"sources":[{"unknown":"file"}]}}`},
		{"spawn_agent", `{"task_name":"child","message":"task","preload":{"sources":[{"path":"file","ranges":[[1]]}]}}`},
		{"list_agents", `{"unknown":true}`},
		{"list_models", `{"backend":"codex"}`},
		{"inspect", `{}`},
		{"inspect", `{"target":"/root","unknown":true}`},
		{"wait_agent", `{"target":[]}`},
		{"wait_agent", `{"timeout_ms":1.5}`},
		{"wait_agent", `{"cancel_id":"wait"}`},
		{"send_message", `{"message":"hello"}`},
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
	modelResult, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_models", Meta: meta, Arguments: map[string]any{}})
	if err != nil || modelResult.IsError || len(modelResult.Content) != 1 {
		t.Fatalf("model discovery: %+v, %v", modelResult, err)
	}
	assertDiscoveredModels(t, []byte(modelResult.Content[0].(*mcp.TextContent).Text))
	for _, tool := range []string{"wait_agent", "list_agents"} {
		arguments := map[string]any{"target": []string{"agent:main/child"}}
		if tool == "list_agents" {
			arguments = map[string]any{"path_prefix": "agent:main"}
		}
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Meta: meta, Arguments: arguments})
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

type modelDiscoveryHost struct {
	control.Native
	calls int
	err   error
}

func (h *modelDiscoveryHost) DiscoverModels(ctx context.Context, _ control.Identity) ([]control.ModelOption, error) {
	h.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []control.ModelOption{{ID: "claude:fixture", Name: "Fixture", Description: "Native catalog choice", ResolvedModel: "claude:fixture-version"}}, h.err
}

func assertDiscoveredModels(t *testing.T, body []byte) {
	t.Helper()
	var result struct {
		Models []control.ModelOption `json:"models"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Models) != 1 || result.Models[0].ID != "claude:fixture" || result.Models[0].Name != "Fixture" || result.Models[0].ResolvedModel != "claude:fixture-version" {
		t.Fatalf("model identifiers or native metadata lost: %s, %v", body, err)
	}
}

func TestNativeModelDiscoveryRequiresAdmittedCaller(t *testing.T) {
	driver, host, _, caller := reconciliationFixture(t)
	before, _ := driver.control.Member(caller.Thread)
	inputs, resumes := len(host.inputs), host.resumes
	if _, err := driver.Call(t.Context(), caller, "list_models", jsontext.Value(`{}`)); err == nil {
		t.Fatal("missing native discovery silently succeeded")
	}
	discovery := &modelDiscoveryHost{Native: host}
	driver.host = discovery
	// Claude discovery is independent of the selected Codex provider catalog.
	driver.CaptureCatalog([]byte(`{"models":[]}`))
	value, err := driver.Call(t.Context(), caller, "list_models", jsontext.Value(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	assertDiscoveredModels(t, value)
	foreign := caller
	foreign.Session = "other-session"
	if _, err := driver.listModels(t.Context(), foreign, jsontext.Value(`{}`)); err == nil || discovery.calls != 1 {
		t.Fatal("unadmitted caller reached native discovery")
	}
	discovery.err = errors.New("native catalog unavailable")
	if _, err := driver.Call(t.Context(), caller, "list_models", jsontext.Value(`{}`)); !errors.Is(err, discovery.err) {
		t.Fatalf("native catalog failure hidden: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := driver.Call(ctx, caller, "list_models", jsontext.Value(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery cancellation lost: %v", err)
	}
	after, _ := driver.control.Member(caller.Thread)
	if len(host.inputs) != inputs || host.resumes != resumes || after.Generation != before.Generation || after.Turn != before.Turn || after.Status != before.Status {
		t.Fatal("model discovery started work or changed logical admission")
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
	result, err := driver.waitAgent(t.Context(), caller, []byte(`{}`))
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
	if !strings.Contains(string(fields["instructions"]), "later wait_agent output") {
		t.Fatalf("wait acknowledgement omitted delivery instructions: %s", payload)
	}
	member, _ := driver.control.Member(caller.Thread)
	if member.Wait == nil || member.Wait.ID != caller.Call || !member.Wait.All || member.Wait.State != "registered" {
		t.Fatal("lost internal wait correlation")
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
