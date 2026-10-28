package claude

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Decision describes captured approval authority. Ask requires the host's
// ordinary approval flow to complete before the worker may allow the tool.
// A zero Decision defers to the SDK. Neither flag with a reason denies.
type Decision struct {
	Allow, Ask bool
	Reason     string
}

// Policy owns approval admission independently of application settings and any
// other backend. It deliberately supplies no filesystem or network isolation.
type Policy struct {
	mode        string
	nativeTools []string
	options     jsontext.Value
}

// CompilePolicy maps captured approval modes to the Claude worker. Unknown
// modes, categories and reviewers reject before startup rather than broadening
// permission. The SDK's native permission requests remain host-mediated.
func CompilePolicy(approval jsontext.Value, reviewer string) (*Policy, error) {
	p := &Policy{
		// Names are from the pinned SDK's BUILTIN_TOOL_NAMES. Keep the visible
		// native surface and its hook admission at the same owner.
		nativeTools: []string{"Bash", "PowerShell", "Read", "Write", "Edit", "NotebookEdit", "Glob", "Grep", "WebFetch", "WebSearch", "TaskStop", "TodoWrite"},
	}
	if err := json.Unmarshal(approval, &p.mode); err == nil {
		switch p.mode {
		case "never", "on-request", "untrusted":
		default:
			return nil, fmt.Errorf("unsupported Claude approval policy %q", p.mode)
		}
	} else {
		var captured struct {
			Granular *struct {
				SandboxApproval    *bool `json:"sandbox_approval"`
				Rules              *bool `json:"rules"`
				SkillApproval      bool  `json:"skill_approval"`
				RequestPermissions bool  `json:"request_permissions"`
				MCPElicitations    *bool `json:"mcp_elicitations"`
			} `json:"granular"`
		}
		if err := json.Unmarshal(approval, &captured, json.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("Claude approval policy: %w", err)
		}
		g := captured.Granular
		if g == nil || g.SandboxApproval == nil || g.Rules == nil || g.MCPElicitations == nil {
			return nil, errors.New("Claude granular approval requires sandbox_approval, rules and mcp_elicitations booleans")
		}
		p.mode = "granular"
	}
	// A captured automatic reviewer has no approval routing to own under never.
	if reviewer != "" && reviewer != "user" && !(p.mode == "never" && reviewer == "auto_review") {
		return nil, fmt.Errorf("Claude approval policy does not support reviewer %q", reviewer)
	}
	permissionMode := "default"
	if p.mode == "never" {
		permissionMode = "bypassPermissions"
	}
	var err error
	p.options, err = json.Marshal(map[string]any{
		"permissionMode":                  permissionMode,
		"allowDangerouslySkipPermissions": p.mode == "never",
		"sandbox":                         map[string]bool{"enabled": false},
		"tools":                           p.nativeTools,
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Options returns a detached SDK options fragment. The worker separately owns
// its authenticated coordination server, hooks and settingSources isolation.
func (p *Policy) Options() jsontext.Value { return bytes.Clone(p.options) }

// CheckTool runs before every native tool. Native tools with no special
// restriction defer to the SDK's permission checks, preserving canUseTool.
// Coordination is allowed only after the worker authenticates its configured
// SDK server and the specific invocation. Native delegation remains disabled.
func (p *Policy) CheckTool(name string, args jsontext.Value) (Decision, error) {
	if args.Kind() != '{' || !args.IsValid() {
		return Decision{}, errors.New("Claude tool input must be a valid JSON object")
	}
	if strings.HasPrefix(name, "mcp__threadcontrol__") {
		return Decision{Allow: true}, nil
	}
	if !slices.Contains(p.nativeTools, name) {
		return Decision{Reason: fmt.Sprintf("Native tool %q has no Claude approval mapping", name)}, nil
	}
	if p.mode == "untrusted" && (name == "Bash" || name == "PowerShell") {
		return Decision{Ask: true, Reason: "Untrusted Claude commands require approval; no captured command trust rule authorizes this command"}, nil
	}
	return Decision{}, nil
}

// CheckApproval admits an SDK permission prompt. Native prompts supply no
// captured category, so granular approval cannot authorize them.
func (p *Policy) CheckApproval() Decision {
	if p.mode == "on-request" || p.mode == "untrusted" {
		return Decision{Ask: true, Reason: "Claude native tool requires approval"}
	}
	if p.mode == "never" {
		return Decision{Reason: "Captured Claude approval policy never permits prompts"}
	}
	return Decision{Reason: "Claude cannot classify native approval requests under granular policy"}
}
