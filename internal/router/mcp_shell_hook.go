package router

import (
	"context"
	json "encoding/json/v2"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func registerMCPShellHook(server *mcp.Server) {
	// Native hooks use the existing bridge, but this internal tool is hidden
	// from the model catalog. Return a native denial on instrumentation errors;
	// an MCP transport error is only a failed observer in Codex's hook engine.
	server.AddTool(&mcp.Tool{Name: vcsguard.HookTool, InputSchema: map[string]any{"type": "object"},
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input vcsguard.HookInput
		var err error
		if len(request.Params.Arguments) > 4<<20 {
			err = errors.New("hook input exceeds 4 MiB")
		} else {
			err = json.Unmarshal(request.Params.Arguments, &input)
		}
		var output any
		if err == nil {
			output, err = vcsguard.RewriteHook(input)
		}
		if err != nil {
			output = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse",
				"permissionDecision": "deny", "permissionDecisionReason": "mekugi: VCS guard instrumentation failed: " + err.Error()}}
		}
		data, err := json.Marshal(output)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
	})
}
