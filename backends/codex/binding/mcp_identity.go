package binding

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
)

// MCPIdentity contains host-supplied identity for a stock nested MCP call. Call
// identifies the nested invocation; Item identifies its enclosing execution.
type MCPIdentity struct {
	Thread  string
	Session string
	Turn    string
	Call    string
	Item    string
	Window  string
	Version string
	Model   string
	Effort  string
}

// DecodeMCPIdentity authenticates consistency, not tree membership. The owner
// must additionally admit this identity against its live invocation records.
// Source: codex-rs/core/src/mcp_tool_call.rs:1307,1407@0df4aa677.
func DecodeMCPIdentity(raw jsontext.Value) (MCPIdentity, error) {
	var meta struct {
		Call    string `json:"callId"`
		Thread  string `json:"threadId"`
		Session string `json:"sessionId"`
		Item    string `json:"itemId"`
		Window  string `json:"windowId"`
		Turn    struct {
			Thread  string `json:"thread_id"`
			Session string `json:"session_id"`
			Turn    string `json:"turn_id"`
			Version string `json:"codex_version"`
			Model   string `json:"model"`
			Effort  string `json:"reasoning_effort"`
		} `json:"x-codex-turn-metadata"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return MCPIdentity{}, errors.New("missing or malformed host MCP identity")
	}
	if meta.Call == "" || meta.Thread == "" || meta.Session == "" || meta.Item == "" || meta.Window == "" || meta.Turn.Turn == "" || meta.Turn.Version == "" || meta.Turn.Model == "" {
		return MCPIdentity{}, errors.New("incomplete host MCP identity")
	}
	if meta.Thread != meta.Turn.Thread || meta.Session != meta.Turn.Session {
		return MCPIdentity{}, errors.New("conflicting host MCP identity")
	}
	return MCPIdentity{Thread: meta.Thread, Session: meta.Session, Turn: meta.Turn.Turn,
		Call: meta.Call, Item: meta.Item, Window: meta.Window, Version: meta.Turn.Version,
		Model: meta.Turn.Model, Effort: meta.Turn.Effort}, nil
}
