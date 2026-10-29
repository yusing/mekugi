package codexbinding

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// ToolCatalog is shared by admitted backend transports. Calling conventions
// and authenticated caller identity remain with each transport.
func ToolCatalog(claudeCaller bool) []*mcp.Tool {
	modelGuidance := "For a Claude delegate, use list_models and pass a returned claude:MODEL unchanged."
	modelList := "List Claude's native advertised model IDs for spawn_agent without submitting model input. Account access is validated during execution. Use a returned claude: ID unchanged as spawn_agent.model with fork_turns=none."
	if claudeCaller {
		modelGuidance = "Use list_models for Codex choices. An explicit claude:MODEL keeps Claude even when the role pins a Codex model."
		modelList = "List the selected Codex catalog's model IDs for spawn_agent without submitting model input. Use a returned ID unchanged as spawn_agent.model with fork_turns=none."
	}
	entries := []struct{ name, description, schema string }{
		{"spawn_agent", "Create an agent with an attributed assignment. Returns the confirmed thread identity and logical agent name.", `{"type":"object","properties":{
			"task_name":{"type":"string","description":"Task-name segment under this agent. Use lowercase ASCII letters, digits, underscores and hyphens; for example, trace_roster_metrics. Must be nonempty and unique under this parent."},
			"message":{"type":"string","description":"Nonempty assignment for the new agent. Include the task, constraints and expected result, especially when fork_turns=none supplies no surrounding conversation."},
			"agent_type":{"type":"string","description":"Role from the available agent_type roles. Omit for the default role on a fresh child or inherited role on a full-history fork."},
			"fork_turns":{"type":"string","enum":["all","none"],"description":"Conversation context to inherit: all (default) copies the full history and inherits model and effort; none starts fresh and permits authorized model/effort overrides and preload."},
			"model":{"type":"string","description":"Model override for fork_turns=none. Set only when explicitly requested by the user, applicable AGENTS.md instructions, or skill instructions. ` + modelGuidance + `"},
			"reasoning_effort":{"type":"string","description":"Reasoning-effort override for fork_turns=none. Set only when explicitly requested by the user, applicable AGENTS.md instructions, or skill instructions."},
			"cwd":{"type":"string","description":"Existing authorized working directory for the child. Omit to inherit the caller's cwd; relative paths resolve there. The parent's cwd is unchanged."},
			"preload":{"description":"Startup sources for fork_turns=none. Supply known files, skills or mchanges IDs/ranges the child needs to begin; complete content arrives before first inference without unrelated parent history. Select only assignment-relevant sources. Omit for full-history forks or when no startup sources are known. Each source uses path with optional inclusive line ranges, skill with optional SKILL.md path, or changes with an array of explicit IDs/ranges supplying their composed diff. Relative paths use child cwd; AGENTS.md is host-loaded. Reads use child permissions; failures remain visible.","allOf":[` + compact.SelectionSchema + `]}
		},"required":["task_name","message"],"additionalProperties":false}`},
		{"list_models", modelList, `{"type":"object","properties":{},"additionalProperties":false}`},
		{"list_agents", "List admitted agents and retained operations.", `{"type":"object","properties":{"path_prefix":{"type":"string","description":"Logical agent-name prefix, such as agent:main/worker. Omit to list the whole admitted tree."}},"additionalProperties":false}`},
		{"inspect", "Inspect an admitted agent: applied model, reasoning effort, service tier, context usage/window and completed compactions. Includes idle or completed agents; uses retained observations without starting work; unavailable observations are null.", `{"type":"object","properties":{"target":{"type":"string","description":"Agent identifier, such as agent:main, a returned agent:main/worker name, an unambiguous relative task name, or a thread ID."}},"required":["target"],"additionalProperties":false}`},
		{"compact", "Compact your own context at a safe checkpoint, then automatically continue unfinished work. Call alone and let the enclosing execution finish. Use inspect to judge context usage.", `{"type":"object","properties":{},"additionalProperties":false}`},
		{"wait_agent", "Wait for a descendant outcome or addressed message. Call alone and let the enclosing tool execution finish. A waiting acknowledgement ends the host turn; the outcome arrives as a later wait_agent output. New input supersedes the wait.", `{"type":"object","properties":{"target":{"description":"Descendant agent identifiers to wait for. Omit or use all to select all current descendants; otherwise provide a nonempty array of logical names or thread IDs.","oneOf":[{"type":"string","enum":["all"]},{"type":"array","items":{"type":"string"},"minItems":1}]},"wait_mode":{"type":"string","enum":["any","all"],"description":"Omit or use any to return on the first selected completion or failure. Use all to wait until every selected agent settles. Addressed messages wake either mode."},"compact":{"type":"boolean","description":"Compact your context before delivering this wait's outcome, including an already-ready outcome. The timeout starts at wait registration. Compaction failure is included in the eventual wait result; pending waits remain waiting."},"timeout_ms":{"type":"integer","description":"Wait timeout in milliseconds. Omit for completion waits; set only for a real deadline. Omission waits indefinitely. Explicit values below the host minimum are clamped; values above its maximum are rejected."}},"additionalProperties":false}`},
		{"send_message", "Queue attributed input without starting idle work.", `{"type":"object","properties":{"target":{"type":"string","description":"Recipient's returned logical agent name, unambiguous relative task name, or thread ID within the admitted tree."},"message":{"type":"string","description":"Nonempty message to deliver with the caller's attribution."}},"required":["target","message"],"additionalProperties":false}`},
		{"followup_task", "Start or steer an admitted non-root agent with a new assignment.", `{"type":"object","properties":{"target":{"type":"string","description":"Non-root agent's returned logical name, unambiguous relative task name, or thread ID."},"message":{"type":"string","description":"Nonempty follow-up assignment. Starts idle work, steers a running turn, or supersedes a pending wait."}},"required":["target","message"],"additionalProperties":false}`},
		{"interrupt_agent", "Interrupt a non-root agent, retaining it for later work.", `{"type":"object","properties":{"target":{"type":"string","description":"Non-root agent's returned logical name, unambiguous relative task name, or thread ID."}},"required":["target"],"additionalProperties":false}`},
	}
	result := make([]*mcp.Tool, 0, len(entries))
	for _, entry := range entries {
		result = append(result, &mcp.Tool{Name: entry.name, Description: entry.description, InputSchema: jsontext.Value(entry.schema)})
	}
	return result
}

// mcp constructs the sole collaboration catalog through stock execution.
func (d *Driver) mcp() binding.MCPServer {
	server := binding.MCPServer{Name: "threadcontrol", Server: mcp.NewServer(&mcp.Implementation{Name: "threadcontrol", Version: "1"}, nil)}
	for _, entry := range ToolCatalog(false) {
		server.Tools = append(server.Tools, entry.Name)
		mcp.AddTool(server.Server, entry, func(ctx context.Context, request *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
			rawMetadata, err := json.Marshal(request.Params.Meta)
			if err != nil {
				return nil, nil, err
			}
			metadata, err := binding.DecodeMCPIdentity(rawMetadata)
			if err != nil {
				return nil, nil, err
			}
			caller := control.Identity{Thread: metadata.Thread, Session: metadata.Session, Turn: metadata.Turn, Call: metadata.Call, Item: metadata.Item}
			value, err := d.invoke(ctx, metadata, caller, entry.Name, jsontext.Value(request.Params.Arguments))
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(value)}}}, nil, err
		})
	}
	return server
}

// Call accepts identity established by a backend's authenticated runtime. Model
// arguments never select the caller or its authority.
func (d *Driver) Call(ctx context.Context, caller control.Identity, name string, raw jsontext.Value) (jsontext.Value, error) {
	member, err := d.control.Caller(caller)
	if err != nil {
		return nil, err
	}
	metadata := binding.MCPIdentity{Thread: caller.Thread, Session: caller.Session, Turn: caller.Turn, Call: caller.Call, Item: caller.Item, Model: member.Settings.Model}
	return d.invoke(ctx, metadata, caller, name, raw)
}

func (d *Driver) invoke(ctx context.Context, metadata binding.MCPIdentity, caller control.Identity, name string, raw jsontext.Value) (jsontext.Value, error) {
	var result any
	var err error
	switch name {
	case "spawn_agent":
		result, err = d.spawnAgent(ctx, metadata, caller, raw)
	case "list_models":
		result, err = d.listModels(ctx, caller, raw)
	case "list_agents":
		result, err = d.listAgents(caller, raw)
	case "inspect":
		result, err = d.inspectAgent(ctx, caller, raw)
	case "compact":
		var args struct{}
		if err = json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err == nil {
			result, err = d.control.RegisterCompaction(ctx, caller, nil, false, "", nil, time.Now(), true)
		}
	case "wait_agent":
		result, err = d.waitAgent(ctx, caller, raw)
	case "send_message", "followup_task", "interrupt_agent":
		result, err = d.controlAction(ctx, caller, raw, name)
	default:
		return nil, errors.New("unknown coordination tool")
	}
	if err != nil {
		return nil, err
	}
	value, err := json.Marshal(result)
	if err == nil {
		value, err = visibleResultNames(value)
	}
	return value, err
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
	old, err := d.control.Effect(caller.Thread, caller.Call)
	if err != nil {
		return nil, err
	}
	var prepared PreparedChild
	if old != nil {
		if old.Kind != "create" || !sameJSON(arguments, old.Child.Arguments) {
			return nil, errors.New("conflicting repeated spawn arguments")
		}
		_, name, _ := strings.CutLast(old.Child.Task, "/")
		prepared = PreparedChild{Arguments: old.Child.Arguments, Settings: old.Child.Settings, Limits: old.Child.Limits, TaskName: name, Message: old.Message, FullHistory: old.Child.Import != "", Import: old.Child.Import, Preload: old.Child.Preload.Selection}
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
		Target  jsontext.Value   `json:"target"`
		Timeout *int64           `json:"timeout_ms"`
		Mode    control.WaitMode `json:"wait_mode"`
		Compact bool             `json:"compact"`
	}
	if err := json.Unmarshal(raw, &args, json.RejectUnknownMembers(true)); err != nil {
		return nil, err
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
	var result control.WaitResult
	var err error
	if args.Compact {
		result, err = d.control.RegisterCompaction(ctx, caller, targets, all, args.Mode, args.Timeout, time.Now(), false)
	} else {
		result, err = d.control.RegisterWait(ctx, caller, targets, all, args.Mode, args.Timeout, time.Now())
	}
	if err == nil {
		if result.State == "waiting" {
			result.Instructions = "Let this enclosing execution finish now. Mekugi ends the host turn and delivers the outcome as a later wait_agent output. New input supersedes this wait."
		}
		d.waitMetrics(ctx, caller.Thread, &result)
	}
	return result, err
}

func (d *Driver) controlAction(ctx context.Context, caller control.Identity, raw jsontext.Value, name string) (result any, err error) {
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
	if kind != "message" {
		// Live backends can hold retirement across logical admission and native
		// acknowledgement. Paused targets retain their ordinary resume path.
		if owner, runtimeErr := d.native().Runtime(target.Thread); runtimeErr == nil {
			if admission, ok := owner.(interface {
				AdmitControl(context.Context) (func() error, error)
			}); ok {
				release, admissionErr := admission.AdmitControl(ctx)
				if admissionErr != nil {
					return nil, admissionErr
				}
				defer func() { err = errors.Join(err, release()) }()
			}
		}
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
	retained, err := d.control.Effect(caller.Thread, action.ID)
	if err != nil {
		return nil, err
	}
	action = *retained
	response := map[string]any{"id": action.ID, "state": action.State, "thread_id": target.Thread, "task_name": target.Task}
	if kind == "followup" {
		response["metrics"] = d.sessionMetrics(ctx, target.Thread)
	}
	if kind == "interrupt" {
		response["previous_status"] = action.PreviousStatus
	}
	return response, nil
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
	prefix := strings.TrimRight(control.TaskPath(args.Prefix), "/")
	// Status never exposes frozen settings, credentials or message payloads.
	return d.control.Agents(caller.Thread, prefix)
}
