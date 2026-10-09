package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestratePrepareInput struct {
	TaskName string `json:"task_name"`
}

type orchestrateInterruptInput struct {
	Target string `json:"target"`
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
		return nil, result.batch, result.err
	case <-command.ctx.Done():
		return nil, nil, command.ctx.Err()
	}
}
