package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// mcp constructs the sole collaboration catalog through stock execution.
func (d *Driver) mcp() binding.MCPServer {
	server := binding.MCPServer{Name: "threadcontrol", Server: mcp.NewServer(&mcp.Implementation{Name: "threadcontrol", Version: "1"}, nil)}
	for _, entry := range []struct{ name, description, schema string }{
		{"spawn_agent", "Create a native agent with role and optional cwd. Full-history forks inherit model and effort; fork_turns=none permits authorized overrides.", `{"type":"object","properties":{"task_name":{"type":"string"},"message":{"type":"string"},"agent_type":{"type":"string"},"fork_turns":{"type":"string","enum":["all","none"]},"model":{"type":"string"},"reasoning_effort":{"type":"string"},"cwd":{"type":"string"}},"required":["task_name","message"],"additionalProperties":false}`},
		{"list_agents", "List admitted agents and retained operations, optionally below path_prefix.", `{"type":"object","properties":{"path_prefix":{"type":"string"}},"additionalProperties":false}`},
		{"wait_agent", "Wait for selected descendants without polling. target defaults to all; omit timeout_ms for no deadline. Returns a queued event or registers a local stop after successful enclosing execution. cancel_id cancels only the subscription and excludes other fields.", `{"type":"object","properties":{"target":{"oneOf":[{"type":"string","enum":["all"]},{"type":"array","items":{"type":"string"},"minItems":1}]},"timeout_ms":{"type":"integer"},"cancel_id":{"type":"string"}},"additionalProperties":false}`},
		{"send_message", "Queue attributed input without starting idle work.", `{"type":"object","properties":{"target":{"type":"string"},"message":{"type":"string"}},"required":["target","message"],"additionalProperties":false}`},
		{"followup_task", "Start or steer an admitted non-root agent with a new assignment.", `{"type":"object","properties":{"target":{"type":"string"},"message":{"type":"string"}},"required":["target","message"],"additionalProperties":false}`},
		{"interrupt_agent", "Interrupt a non-root agent, retaining it for later work.", `{"type":"object","properties":{"target":{"type":"string"}},"required":["target"],"additionalProperties":false}`},
	} {
		server.Tools = append(server.Tools, entry.name)
		mcp.AddTool(server.Server, &mcp.Tool{Name: entry.name, Description: entry.description, InputSchema: jsontext.Value(entry.schema)}, func(ctx context.Context, request *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
			rawMetadata, err := json.Marshal(request.Params.Meta)
			if err != nil {
				return nil, nil, err
			}
			metadata, err := binding.DecodeMCPIdentity(rawMetadata)
			if err != nil {
				return nil, nil, err
			}
			caller := control.Identity{Thread: metadata.Thread, Session: metadata.Session, Turn: metadata.Turn, Call: metadata.Call, Item: metadata.Item}
			var result any
			switch entry.name {
			case "spawn_agent":
				result, err = d.spawnAgent(ctx, metadata, caller, jsontext.Value(request.Params.Arguments))
			case "list_agents":
				result, err = d.listAgents(caller, jsontext.Value(request.Params.Arguments))
			case "wait_agent":
				result, err = d.waitAgent(ctx, caller, jsontext.Value(request.Params.Arguments))
			default:
				result, err = d.controlAction(ctx, caller, jsontext.Value(request.Params.Arguments), entry.name)
			}
			if err != nil {
				return nil, nil, err
			}
			value, err := json.Marshal(result)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(value)}}}, nil, err
		})
	}
	return server
}

func (d *Driver) spawnAgent(ctx context.Context, metadata binding.MCPIdentity, caller control.Identity, raw jsontext.Value) (any, error) {
	var options SpawnOptions
	if err := json.Unmarshal(raw, &options, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if _, err := d.control.Caller(caller); err != nil {
		return nil, err
	}
	arguments, err := json.Marshal(&options, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	tree, err := d.control.Snapshot(caller.Thread)
	if err != nil {
		return nil, err
	}
	var prepared PreparedChild
	if old := tree.Effect(caller.Thread, caller.Call); old != nil {
		if old.Kind != "create" || !sameJSON(arguments, old.Child.Arguments) {
			return nil, errors.New("conflicting repeated spawn arguments")
		}
		_, name, _ := strings.CutLast(old.Child.Task, "/")
		prepared = PreparedChild{Arguments: old.Child.Arguments, Settings: old.Child.Settings, Limits: old.Child.Limits, TaskName: name, Message: old.Message, FullHistory: old.Child.Import != "", Import: old.Child.Import}
	} else {
		d.mu.RLock()
		models, catalogError := d.models, d.catalogError
		d.mu.RUnlock()
		if catalogError != nil {
			return nil, catalogError
		}
		prepared, err = d.PrepareSpawn(ctx, metadata, options, models)
		if err != nil {
			return nil, err
		}
	}
	return d.SpawnPrepared(ctx, caller, prepared)
}

func (d *Driver) waitAgent(ctx context.Context, caller control.Identity, raw jsontext.Value) (any, error) {
	var args struct {
		Target  jsontext.Value `json:"target"`
		Timeout *int64         `json:"timeout_ms"`
		Cancel  *string        `json:"cancel_id"`
	}
	if err := json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if args.Cancel != nil {
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		if len(fields) != 1 {
			return nil, errors.New("cancel_id excludes target and timeout_ms")
		}
		if _, err := d.control.Caller(caller); err != nil {
			return nil, err
		}
		if err := d.Reconcile(ctx, caller.Thread); err != nil {
			return nil, err
		}
		return d.control.CancelWait(ctx, caller, *args.Cancel)
	}
	all := len(args.Target) == 0
	var targets []string
	if args.Target.Kind() == '"' {
		var target string
		if err := json.Unmarshal(args.Target, &target); err != nil {
			return nil, err
		}
		if target != "all" {
			return nil, errors.New("target must be all or a nonempty array")
		}
		all = true
	} else if !all {
		if err := json.Unmarshal(args.Target, &targets); err != nil {
			return nil, err
		}
	}
	if _, err := d.control.Caller(caller); err != nil {
		return nil, err
	}
	if err := d.Reconcile(ctx, caller.Thread); err != nil {
		return nil, err
	}
	return d.control.RegisterWait(ctx, caller, targets, all, args.Timeout, time.Now())
}

func (d *Driver) controlAction(ctx context.Context, caller control.Identity, raw jsontext.Value, name string) (any, error) {
	kind := map[string]string{"send_message": "message", "followup_task": "followup", "interrupt_agent": "interrupt"}[name]
	var args struct {
		Target  string  `json:"target"`
		Message *string `json:"message"`
	}
	if err := json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if kind == "interrupt" && args.Message != nil {
		return nil, errors.New("interrupt_agent accepts only target")
	}
	if kind != "interrupt" && (args.Message == nil || strings.TrimSpace(*args.Message) == "") {
		return nil, errors.New("empty message cannot be sent to an agent")
	}
	message := ""
	if args.Message != nil {
		message = *args.Message
	}
	target, err := d.control.Target(caller, args.Target)
	if err != nil {
		return nil, err
	}
	if err := d.Reconcile(ctx, target.Thread); err != nil {
		return nil, err
	}
	action, err := d.control.PrepareAction(ctx, caller, args.Target, kind, message)
	if err != nil {
		return nil, err
	}
	err = d.DispatchPending(ctx, caller.Thread)
	if err == nil {
		err = d.DispatchPending(ctx, target.Thread)
	}
	err = errors.Join(err, d.Wake(ctx, target.Thread, time.Now()))
	if target.Parent != "" {
		err = errors.Join(err, d.Wake(ctx, target.Parent, time.Now()))
	}
	if err != nil {
		return nil, err
	}
	tree, err := d.control.Snapshot(caller.Thread)
	if err != nil {
		return nil, err
	}
	action = *tree.Effect(caller.Thread, action.ID)
	result := map[string]any{"id": action.ID, "state": action.State}
	if kind == "interrupt" {
		result["previous_status"] = action.PreviousStatus
	}
	return result, nil
}

func (d *Driver) listAgents(caller control.Identity, raw jsontext.Value) (any, error) {
	var args struct {
		Prefix string `json:"path_prefix"`
	}
	if err := json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if _, err := d.control.Caller(caller); err != nil {
		return nil, err
	}
	tree, err := d.control.Snapshot(caller.Thread)
	if err != nil {
		return nil, err
	}
	var agents, effects []map[string]any
	prefix := strings.TrimRight(args.Prefix, "/")
	matches := func(task string) bool {
		return prefix == "" || task == prefix || strings.HasPrefix(task, prefix+"/")
	}
	for _, m := range tree.Members {
		if matches(m.Task) {
			agents = append(agents, map[string]any{"agent_name": m.Task, "agent_status": m.Status, "thread_id": m.Thread, "generation": m.Generation, "wait": m.Wait.View(&tree)})
		}
	}
	slices.SortFunc(agents, func(a, b map[string]any) int {
		return strings.Compare(a["agent_name"].(string), b["agent_name"].(string))
	})
	for _, effect := range tree.Actions() {
		if effect.State == "confirmed" {
			continue
		}
		if !matches(tree.Members[effect.Caller].Task) && (tree.Members[effect.Target] == nil || !matches(tree.Members[effect.Target].Task)) && (effect.Child == nil || !matches(effect.Child.Task)) {
			continue
		}
		row := map[string]any{"id": effect.ID, "state": effect.State, "thread_id": effect.Target}
		if effect.Child != nil {
			row["task"] = effect.Child.Task
		}
		effects = append(effects, row)
	}
	// Status never exposes frozen settings, credentials or message payloads.
	return map[string]any{"agents": agents, "effects": effects}, nil
}
