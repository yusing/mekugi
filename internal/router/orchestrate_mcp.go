package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestratePrepareInput struct {
	TaskName string `json:"task_name"`
}

type orchestrateInterruptInput struct {
	Target string `json:"target"`
}

type orchestrateWaitInput struct {
	TimeoutMS int `json:"timeout_ms"`
}

type orchestrateFollowupInput struct {
	Target  string `json:"target"`
	Message string `json:"message"`
}

// Preparation is coordinator-only. Cross-checkout child authority is granted
// separately when a run has a confirmed host child identity.
func orchestrateMain(ctx context.Context, proxy *mekugiProxy, workspace, thread, name string) error {
	return proxy.journals.transaction(ctx, proxy.replayStore, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || !j.IdentityKnown || j.IdentityConflicted || j.Parent != "" || j.Author != "/root" {
			return errors.New("orchestration preparation requires a proven main thread")
		}
		if name != "" {
			journals, failures, err := proxy.journals.workspaceJournals(proxy.replayStore, workspace)
			if err != nil {
				return err
			}
			for id, child := range journals {
				if child.Parent == thread && child.Author == "/root/"+name {
					if failures[id] != nil {
						return failures[id]
					}
					return fmt.Errorf("task_name %q already belongs to a native child", name)
				}
			}
		}
		return errJournalUnchanged
	})
}

func newOrchestrateMCPServer(proxy *mekugiProxy, store *orchestrate.Store) *mcp.Server {
	proxy.orchestration = &orchestrateRuntime{store: store, commands: make(chan *orchestrateCommand)}
	server := mcp.NewServer(&mcp.Implementation{Name: "mekugi-orchestrate", Version: "1"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	mcp.AddTool(server, &mcp.Tool{Name: "followup_task", Description: "Deliver input to a confirmed batch thread: start an idle turn or steer its running turn. Main and confirmed children can target sibling task names or /root/task paths. Repeats return retained delivery progress.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target", "message"}, "properties": map[string]any{"target": map[string]any{"type": "string", "minLength": 1}, "message": map[string]any{"type": "string", "minLength": 1}}}}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateFollowupInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
			return nil, nil, err
		}
		var turn struct {
			ID string `json:"turn_id"`
		}
		encoded, err := json.Marshal(request.Params.GetMeta()[codexTurnMetadataHeader])
		if err != nil || json.Unmarshal(encoded, &turn) != nil {
			return nil, nil, errors.New("missing host turn identity")
		}
		key := fmt.Sprintf("%q/%q/%q", thread, turn.ID, request.Params.GetMeta()["callId"])
		command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, target: input.Target, followup: true, callID: key, input: orchestrateSpawnInput{Message: input.Message}, reply: make(chan orchestrateResult, 1)}
		return proxy.orchestration.call(command)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "wait_agent", Description: "Wait for the next observed batch completion, failure, question or approval. Timeout ends only the wait. Prompt events identify the host request or question item.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"timeout_ms": map[string]any{"type": "integer", "minimum": 1, "maximum": 1500000}}}}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateWaitInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
			return nil, nil, err
		}
		if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
			return nil, nil, err
		}
		if input.TimeoutMS == 0 {
			input.TimeoutMS = 10000
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(input.TimeoutMS)*time.Millisecond)
		defer cancel()
		command := &orchestrateCommand{ctx: waitCtx, workspace: workspace, main: thread, wait: true, reply: make(chan orchestrateResult, 1)}
		result, value, err := proxy.orchestration.call(command)
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, map[string]any{"timed_out": true}, nil
		}
		return result, value, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "interrupt_agent", Description: "Request interruption of a live batch turn. The host's turn completion confirms the outcome.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target"}, "properties": map[string]any{"target": map[string]any{"type": "string", "minLength": 1}}}}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateInterruptInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
			return nil, nil, err
		}
		if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
			return nil, nil, err
		}
		command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, target: input.Target, reply: make(chan orchestrateResult, 1)}
		return proxy.orchestration.call(command)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "spawn_agent", Description: "Start a fresh Codex thread in a prepared batch checkout. The message is the complete assignment. Repeats return retained launch progress without starting another thread.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"task_name", "message"}, "properties": map[string]any{
			"task_name": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"}, "message": map[string]any{"type": "string", "minLength": 1},
			"model": map[string]any{"type": "string", "minLength": 1}, "reasoning_effort": map[string]any{"type": "string", "minLength": 1}, "service_tier": map[string]any{"type": "string"},
		}},
	}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateSpawnInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
			return nil, nil, err
		}
		if err := orchestrateMain(ctx, proxy, workspace, thread, input.TaskName); err != nil {
			return nil, nil, err
		}
		command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, input: input, reply: make(chan orchestrateResult, 1)}
		return proxy.orchestration.call(command)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "prepare", Description: "Prepare an isolated Git checkout at committed HEAD for a batch; copy required inputs before spawning. Repeating a prepared task returns its record without resetting files.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"task_name"}, "properties": map[string]any{
			"task_name": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
		}},
	}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestratePrepareInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
			return nil, nil, err
		}
		if err := orchestrateMain(ctx, proxy, workspace, thread, input.TaskName); err != nil {
			return nil, nil, err
		}
		batch, err := store.Prepare(ctx, workspace, thread, input.TaskName)
		return nil, batch, err
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_agents", Description: "List this coordinator's retained batches, including prepared checkouts; does not start or resume threads",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
	}, func(ctx context.Context, request *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
			return nil, nil, err
		}
		batches, err := store.List(ctx, workspace, thread)
		return nil, map[string]any{"agents": batches}, err
	})
	return server
}

func orchestrateCallIdentity(meta mcp.Meta, thread string) error {
	call, _ := meta["callId"].(string)
	var turn struct {
		Thread string `json:"thread_id"`
		ID     string `json:"turn_id"`
	}
	encoded, err := json.Marshal(meta[codexTurnMetadataHeader])
	if err != nil || json.Unmarshal(encoded, &turn) != nil || turn.Thread != thread || turn.ID == "" || call == "" {
		return errors.New("orchestration requires host call and turn identity")
	}
	return nil
}

func (runtime *orchestrateRuntime) call(command *orchestrateCommand) (*mcp.CallToolResult, any, error) {
	if !runtime.active.Load() {
		return nil, nil, errors.New("orchestration requires a running native UI")
	}
	select {
	case runtime.commands <- command:
	case <-command.ctx.Done():
		return nil, nil, command.ctx.Err()
	}
	select {
	case result := <-command.reply:
		if result.delivery != nil {
			d := result.delivery
			value := map[string]any{"id": d.ID, "target": d.Target, "state": d.State}
			if d.TurnID != "" {
				value["turn_id"] = d.TurnID
			}
			if d.Error != "" {
				value["error"] = d.Error
			}
			return nil, value, result.err
		}
		if result.event != nil {
			return nil, result.event, result.err
		}
		return nil, result.batch, result.err
	case <-command.ctx.Done():
		return nil, nil, command.ctx.Err()
	}
}
