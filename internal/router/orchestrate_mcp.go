package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestratePrepareInput struct {
	TaskName string                      `json:"task_name"`
	Evidence []orchestrate.EvidenceInput `json:"evidence,omitempty"`
}

type orchestrateInterruptInput struct {
	Target string `json:"target"`
}

type orchestrateWaitInput struct {
	TimeoutMS *int           `json:"timeout_ms,omitempty"`
	CancelID  string         `json:"cancel_id,omitempty"`
	Targets   jsontext.Value `json:"targets,omitempty"`
}

type orchestrateListInput struct {
	Targets jsontext.Value `json:"targets,omitempty"`
	Detail  string         `json:"detail,omitempty"`
}

func orchestrateWaitTargets(raw jsontext.Value) ([]string, error) {
	var all string
	if len(raw) == 0 || json.Unmarshal(raw, &all) == nil && all == "all" {
		return nil, nil
	}
	var targets []string
	if json.Unmarshal(raw, &targets) != nil || len(targets) == 0 {
		return nil, errors.New(`targets must be "all" or a nonempty list of task names or /root/task_name paths`)
	}
	var names []string
	for _, target := range targets {
		name := strings.TrimPrefix(target, "/root/")
		if name == "" {
			return nil, errors.New("targets must contain nonempty task names")
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, nil
}

type orchestrateFollowupInput struct {
	Target       string `json:"target"`
	Message      string `json:"message"`
	ReviewWaiver string `json:"review_waiver,omitempty"`
	ReviewScope  string `json:"review_scope,omitempty"`
}

// Preparation is coordinator-only. Cross-checkout child authority is granted
// separately when a run has a confirmed host child identity.
func orchestrateMain(ctx context.Context, proxy *mekugiProxy, workspace, thread, name string) error {
	return proxy.journals.transaction(ctx, proxy.replayStore, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || !j.IdentityKnown || j.IdentityConflicted || j.Parent != "" || j.Author != "/root" {
			return errors.New("orchestration preparation requires a proven main thread")
		}
		if run := j.Orchestration; run != nil {
			if run.Main != thread || run.Workspace != workspace || run.Directory != proxy.orchestration.store.Directory {
				return errors.New("orchestration requires the retained run coordinator; nested runs are unavailable")
			}
			if _, err := proxy.orchestration.store.Snapshot(workspace, thread); err != nil {
				return err
			}
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
	store.ShadowSnapshot = orchestrateShadowSnapshot
	store.SVNBaseline = orchestrateSVNBaseline
	proxy.orchestration = &orchestrateRuntime{store: store, commands: make(chan *orchestrateCommand)}
	server := mcp.NewServer(&mcp.Implementation{Name: "mekugi-orchestrate", Version: "1"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}})
	for _, tool := range []struct{ name, description string }{
		{"followup_task", "Start an idle batch turn or steer a running batch. Targets are task names or /root/task paths. Child messages to main are attributed events for wait_agent, never user input. Report only a new blocker requiring action or one final handoff per assignment. Use request_user_input_async for human decisions. Repeats return retained progress."},
		{"send_message", "Deliver input to a running batch; queue without waking an idle batch. Targets are task names or /root/task paths. Child messages to main are attributed events for wait_agent, never user input. Report only a new blocker requiring action or one final handoff per assignment. Use request_user_input_async for human decisions. Repeats return retained progress."},
	} {
		properties := map[string]any{"target": map[string]any{"type": "string", "minLength": 1}, "message": map[string]any{"type": "string", "minLength": 1}}
		if tool.name == "followup_task" {
			properties["review_scope"] = map[string]any{"type": "string", "minLength": 1, "description": "Coordinator only: replace scope for an idle implementation batch with settled host work or a confirmed unsent first turn. Saved before delivery; delivery failure does not undo the amendment. Invalidates approvals and delta coverage. Kind, checks, permissions and review policy stay unchanged. Omit to retain scope."}
			properties["review_waiver"] = map[string]any{"type": "string", "minLength": 1, "description": "Coordinator only, after an explicit user request: reason to finish this stopped, idle batch without further reviews. Retained across resume. Required checks still run; prior findings remain. Omit to preserve current policy."}
		}
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: tool.description, InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target", "message"}, "properties": properties}}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateFollowupInput) (*mcp.CallToolResult, any, error) {
			ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
			if err != nil {
				return nil, nil, err
			}
			defer release()
			if input.ReviewWaiver != "" || input.ReviewScope != "" {
				if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
					return nil, nil, err
				}
			}
			key, err := orchestrateCallIdentity(request.Params.GetMeta(), thread)
			if err != nil {
				return nil, nil, err
			}
			command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, target: input.Target, tool: tool.name, callID: key, reviewWaiver: input.ReviewWaiver, reviewScope: input.ReviewScope, input: orchestrateSpawnInput{Message: input.Message}, reply: make(chan orchestrateResult, 1)}
			return proxy.orchestration.call(command)
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "wait_agent", Description: "Wait for a selected event. A pending wait ends Main's host turn after this exec succeeds and skips journal auto resume. Only a selected event or explicit timeout wakes Main with attributed tool output. Omit timeout to wait indefinitely; do not sleep or poll. cancel_id cancels only the subscription. Repeated host calls return retained state. Unconfirmed deliveries appear in list_agents; cancel one only if its result is absent. Final answers also reach Main as separate notifications.", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"cancel_id":  map[string]any{"type": "string", "minLength": 1, "description": "Cancel a wait by ID without interrupting children. Canceling an unconfirmed delivery requeues its event. Cannot be combined with targets or timeout_ms."},
		"timeout_ms": map[string]any{"type": "integer", "minimum": 1, "maximum": 1500000, "description": "Deadline in milliseconds, visible while waiting. Omit for no timeout wake."},
		"targets": map[string]any{"description": "Batches in this run: all (default), or a nonempty task-name or /root/task_name list. Returns the next event from any selected batch.", "oneOf": []any{
			map[string]any{"type": "string", "const": "all"},
			map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "pattern": "^(/root/)?[a-z][a-z0-9_]{0,63}$"}},
		}},
	}}}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateWaitInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		key, err := orchestrateCallIdentity(request.Params.GetMeta(), thread)
		if err != nil {
			return nil, nil, err
		}
		if input.CancelID != "" && (input.TimeoutMS != nil || len(input.Targets) != 0) {
			return nil, nil, errors.New("cancel_id cannot be combined with targets or timeout_ms")
		}

		if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
			return nil, nil, err
		}
		targets, err := orchestrateWaitTargets(input.Targets)
		if err != nil {
			return nil, nil, err
		}
		if len(targets) != 0 {
			batches, err := store.Snapshot(workspace, thread)
			if err != nil {
				return nil, nil, err
			}
			for _, target := range targets {
				if !slices.ContainsFunc(batches, func(b orchestrate.Batch) bool { return b.TaskName == target }) {
					return nil, nil, fmt.Errorf("wait target %q is not a retained batch in this coordinator run; use list_agents", target)
				}
			}
		}
		timeout := 0
		if input.TimeoutMS != nil {
			timeout = *input.TimeoutMS
			if timeout < 1 || timeout > 1500000 {
				return nil, nil, errors.New("timeout_ms must be 1..1500000")
			}
		}
		meta := request.Params.GetMeta()
		item, _ := meta["itemId"].(string)
		var host struct {
			Turn string `json:"turn_id"`
		}
		encoded, _ := json.Marshal(meta[codexTurnMetadataHeader])
		if json.Unmarshal(encoded, &host) != nil || host.Turn == "" || item == "" {
			return nil, nil, errors.New("wait requires host item and turn identity")
		}
		command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, tool: "wait_agent", callID: key, waitTurn: host.Turn, waitItem: item, waitTargets: targets, waitTimeout: timeout, waitCancel: input.CancelID, reply: make(chan orchestrateResult, 1)}
		return proxy.orchestration.call(command)
	})
	for _, tool := range []struct{ name, description string }{
		{"interrupt_agent", "Request interruption of a live batch turn. The host's turn completion confirms the outcome."},
		{"integrate", "Main merges an idle shadow batch with current source edits and writes only changed paths. Conflicts write nothing. Retains partial progress; repeats verify completed writes and reconcile confirmed effects before continuing pending paths. Uncertain or changed paths require inspection. Main marks its bound journal task accepted separately after verifying writeback."},
		{"cleanup", "Main removes an accepted idle Git or shadow batch checkout, safe run-owned submodule clones and unchanged evidence copies, then archives its child thread. Retains changed nested commits in source refs. Requires current journal acceptance and settled input. Keeps branches, private repositories, manifests, history and unsafe copies; reports partial cleanup. Repeats reconcile confirmed removal; uncertain archival requires inspection."},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: tool.description, InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target"}, "properties": map[string]any{"target": map[string]any{"type": "string", "minLength": 1}}}}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateInterruptInput) (*mcp.CallToolResult, any, error) {
			ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
			if err != nil {
				return nil, nil, err
			}
			defer release()
			if _, err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
				return nil, nil, err
			}
			if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
				return nil, nil, err
			}
			command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, target: input.Target, tool: tool.name, reply: make(chan orchestrateResult, 1)}
			result, value, err := proxy.orchestration.call(command)
			if batch, ok := value.(orchestrate.Batch); (tool.name == "cleanup" || tool.name == "integrate") && err != nil && ok && batch.TaskName != "" {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, batch, nil
			}
			return result, value, err
		})
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "spawn_agent", Description: "Similar to mekugi_collaboration.spawn_agent, but starts a fresh independent Codex thread in an isolated checkout from prepare. Native collaboration tools cannot reach these threads. The message carries the complete assignment, constraints and checks. Omitted model, reasoning effort and service tier inherit Main's effective settings. Bind the integration task to /root/task_name; child completion leaves it open. Git integration uses native Git; accept only after verifying reachable or patch-equivalent batch commits and merge-free child/source history after the baseline, including committed submodule gitlinks. SVN and unversioned shadow use integrate. Repeats return retained launch progress without starting another thread.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"task_name", "message"}, "properties": map[string]any{
			"task_name": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"}, "message": map[string]any{"type": "string", "minLength": 1},
			"kind":            map[string]any{"type": "string", "enum": []string{"implementation", "diagnosis"}, "default": "implementation"},
			"required_checks": map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}},
			"model":           map[string]any{"type": "string", "minLength": 1}, "reasoning_effort": map[string]any{"type": "string", "minLength": 1}, "service_tier": map[string]any{"type": "string"},
		}},
	}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateSpawnInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if _, err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
			return nil, nil, err
		}
		if err := orchestrateMain(ctx, proxy, workspace, thread, input.TaskName); err != nil {
			return nil, nil, err
		}
		command := &orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, tool: "spawn_agent", input: input, reply: make(chan orchestrateResult, 1)}
		return proxy.orchestration.call(command)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "prepare", Description: "Main prepares an isolated checkout at the committed Git or SVN baseline, or an unversioned source snapshot. Nested runs are unavailable. Git includes locally initialized submodules. Optional evidence copies named absolute source files into run state and returns handoff paths. Evidence paths accompany the first child input. Copy required checkout-relative ignored inputs before spawning; versioned source edits do not follow. Repeats preserve prepared files.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"task_name"}, "properties": map[string]any{
			"task_name": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9_]{0,63}$"},
			"evidence":  map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "source"}, "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1}, "source": map[string]any{"type": "string", "minLength": 1}}}},
		}},
	}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestratePrepareInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if _, err := orchestrateCallIdentity(request.Params.GetMeta(), thread); err != nil {
			return nil, nil, err
		}
		return proxy.orchestration.call(&orchestrateCommand{ctx: ctx, workspace: workspace, main: thread, tool: "prepare", prepare: input, reply: make(chan orchestrateResult, 1)})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_agents", Description: "Read retained batches in this coordinator's run without starting or resuming threads. Default summary shows identity, checkout state, last host status, workflow phase/candidate and blockers, without prompts/settings/history. Select targets and detail for exact checks, reviews or full records. Unconfirmed wait deliveries remain visible for the whole run; their event payloads require full detail. Retained facts do not prove current filesystem or live host state.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"targets": map[string]any{"description": "All retained batches (default), or selected task names or /root/task_name paths. Unknown targets reject the call.", "oneOf": []any{
				map[string]any{"type": "string", "const": "all"},
				map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "pattern": "^(/root/)?[a-z][a-z0-9_]{0,63}$"}},
			}},
			"detail": map[string]any{"type": "string", "enum": []string{"summary", "checks", "reviews", "full"}, "default": "summary", "description": "summary: compact retained status; checks: summary plus required commands and exact retained check results; reviews: summary plus exact retained verdicts/findings; full: complete batch records including launch settings, reports and dispatch history. Selected evidence is not truncated."},
		}},
	}, func(ctx context.Context, request *mcp.CallToolRequest, input orchestrateListInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
			return nil, nil, err
		}
		batches, err := store.List(ctx, workspace, thread)
		if err != nil {
			return nil, nil, err
		}
		agents, err := orchestrateListAgents(batches, input)
		if err != nil {
			return nil, nil, err
		}
		result := map[string]any{"agents": agents}
		// Unconfirmed wait deliveries stay visible until Main cancels them.
		if waits, err := store.Waits(workspace, thread, "uncertain", "rejected"); err != nil {
			return nil, nil, err
		} else if len(waits) != 0 {
			if input.Detail != "full" {
				for i := range waits {
					waits[i].Event = nil
				}
			}
			result["unsettled_waits"] = waits
		}
		return nil, result, nil
	})
	return server
}

func orchestrateCallIdentity(meta mcp.Meta, thread string) (string, error) {
	call, _ := meta["callId"].(string)
	var turn struct {
		Thread string `json:"thread_id"`
		ID     string `json:"turn_id"`
	}
	encoded, err := json.Marshal(meta[codexTurnMetadataHeader])
	if err != nil || json.Unmarshal(encoded, &turn) != nil || turn.Thread != thread || turn.ID == "" || call == "" {
		return "", errors.New("orchestration requires host call and turn identity")
	}
	return fmt.Sprintf("%q/%q/%q", thread, turn.ID, call), nil
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
		if result.wait != nil {
			return nil, result.wait, result.err
		}
		return nil, result.batch, result.err
	case <-command.ctx.Done():
		return nil, nil, command.ctx.Err()
	}
}
