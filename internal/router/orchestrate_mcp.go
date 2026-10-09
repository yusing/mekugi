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
	server := mcp.NewServer(&mcp.Implementation{Name: "mekugi-orchestrate", Version: "1"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
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
		meta := request.Params.GetMeta()
		call, _ := meta["callId"].(string)
		var turn struct {
			Thread string `json:"thread_id"`
			ID     string `json:"turn_id"`
		}
		encoded, err := json.Marshal(meta[codexTurnMetadataHeader])
		if err != nil || json.Unmarshal(encoded, &turn) != nil || turn.Thread != thread || turn.ID == "" || call == "" {
			return nil, nil, errors.New("orchestration preparation requires host call and turn identity")
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
