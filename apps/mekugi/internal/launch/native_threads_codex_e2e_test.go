//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/backends/codex/appserver"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type nativeThreadsProvider struct {
	mu                                      sync.Mutex
	childDirectory, parent, child, waitTurn string
	fullHistory                             bool
	shellWait                               bool
	compactWait                             bool
	agentCompact                            bool
	earlyCompletion                         bool
	preload                                 bool
	attachChanges                           bool
	attachments, attachmentReload           bool
	recoverFailure                          bool
	multipleAnswers                         bool
	deduplication                           bool
	usageLimit                              bool
	temporaryUsage                          bool
	concurrentChildren                      bool
	limited                                 map[string]bool
	calls                                   map[string]int
	users                                   int
	waiting, mailbox                        chan struct{}
	stop                                    context.CancelFunc
}

// Only fixture state and host identities are included. Provider request bodies,
// authorization headers and inherited process environment stay out of failures.
func (p *nativeThreadsProvider) diagnostic() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("provider inference counts=%v; parent=%q child=%q wait_turn=%q; want initial parent=5 child=2, recovered parent=8 child=3", p.calls, p.parent, p.child, p.waitTurn)
}

func (p *nativeThreadsProvider) serveSocket(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		t.Error("accept local provider WebSocket:", err)
		return
	}
	defer conn.CloseNow()
	for {
		fields, err := providerSocketRead(r.Context(), conn)
		if err != nil {
			return
		}
		if string(fields["generate"]) == "false" {
			if providerSocketWrite(r.Context(), conn, socketEvent("response.created", "managed-prewarm")) != nil ||
				providerSocketWrite(r.Context(), conn, socketEvent("response.completed", "managed-prewarm")) != nil {
				return
			}
			continue
		}
		body, err := json.Marshal(&fields)
		if err != nil {
			t.Error(err)
			return
		}
		request := r.Clone(r.Context())
		request.Body = io.NopCloser(bytes.NewReader(body))
		// A reused socket carries current turn metadata on response.create.
		var metadata map[string]string
		if err := json.Unmarshal(fields["client_metadata"], &metadata); err == nil {
			for key, value := range metadata {
				request.Header.Set(key, value)
			}
		}
		response := httptest.NewRecorder()
		p.serve(t, response, request)
		if response.Code != http.StatusOK {
			if response.Code == http.StatusTooManyRequests {
				var rejection map[string]any
				if json.Unmarshal(response.Body.Bytes(), &rejection) == nil {
					rejection["type"], rejection["status"] = "error", response.Code
					if providerSocketWrite(r.Context(), conn, rejection) == nil {
						continue
					}
				}
			}
			_ = providerSocketWrite(r.Context(), conn, map[string]any{"type": "error", "error": map[string]string{"type": "server_error", "message": "managed-thread fixture rejected request"}})
			return
		}
		for line := range strings.SplitSeq(response.Body.String(), "\n") {
			if payload, ok := strings.CutPrefix(line, "data: "); ok {
				if err := conn.Write(r.Context(), websocket.MessageText, []byte(payload)); err != nil {
					return
				}
			}
		}
	}
}

func (p *nativeThreadsProvider) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	var request struct {
		Generate         *bool                       `json:"generate"`
		Instructions     string                      `json:"instructions"`
		Tools            []jsontext.Value            `json:"tools"`
		Input            []map[string]jsontext.Value `json:"input"`
		Model            string                      `json:"model"`
		PreviousResponse string                      `json:"previous_response_id"`
		Reasoning        struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Error(err)
		http.Error(w, "invalid request", 500)
		return
	}
	if request.Generate != nil && !*request.Generate {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"native-prewarm\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"native-prewarm\",\"status\":\"completed\",\"output\":[]}}\n\n")
		return
	}
	metadata, ok := codexresponses.DecodeTurnMetadata(r.Header)
	if !ok && request.Model == "gpt-6-luna" && len(request.Tools) == 0 && strings.HasPrefix(request.Instructions, "Generate a concise session title") && r.Header.Get("Authorization") == "Bearer fixture-access" && r.Header.Get("ChatGPT-Account-Id") == "fixture-account" {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"native-title\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Native thread fixture\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"native-title\",\"status\":\"completed\",\"output\":[]}}\n\n")
		return
	}
	if err != nil || !ok || metadata.ThreadID == "" {
		t.Errorf("upstream host identity/body: %v, valid=%v, generate=%v model=%q", err, ok, request.Generate, request.Model)
		http.Error(w, "missing host identity", 500)
		return
	}
	if metadata.RequestKind == "compaction" {
		t.Error("summary-free reset requested provider inference")
		http.Error(w, "unexpected summary inference", 500)
		return
	}
	if p.concurrentChildren {
		p.serveConcurrentChildren(t, w, r, metadata.ThreadID, body)
		return
	}
	if p.temporaryUsage {
		p.serveTemporaryUsage(t, w, metadata.ThreadID, body)
		return
	}
	p.mu.Lock()
	resumedParent := !p.earlyCompletion && metadata.ThreadID == p.parent && p.calls[p.parent] >= 5
	p.mu.Unlock()
	wantEffort := "low"
	if resumedParent {
		wantEffort = "high"
	}
	if request.Model != "gpt-6-sol" || request.Reasoning.Effort != wantEffort {
		t.Errorf("selected thread settings = %s/%s, want gpt-6-sol/%s", request.Model, request.Reasoning.Effort, wantEffort)
		http.Error(w, "wrong selected settings", 500)
		return
	}
	users := 0
	for _, item := range request.Input {
		if string(item["role"]) == `"user"` {
			users++
		}
	}
	p.mu.Lock()
	if p.parent == "" {
		p.parent = metadata.ThreadID
	}
	if p.usageLimit && !p.limited[metadata.ThreadID] {
		p.limited[metadata.ThreadID] = true
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintf(w, `{"error":{"type":"usage_limit_reached","resets_at":%d}}`, time.Now().Add(3*time.Second).Unix())
		return
	}
	p.calls[metadata.ThreadID]++
	step, parent := p.calls[metadata.ThreadID], metadata.ThreadID == p.parent
	grandchild := !parent && p.child != "" && metadata.ThreadID != p.child
	if !parent && !grandchild {
		p.child = metadata.ThreadID
	}
	var program, answer, failure string
	if step == 1 {
		if !strings.Contains(string(body), "Compaction does not end the task. Treat work spanning compactions as one logical chain of events.") ||
			strings.Contains(string(body), "the conversation is automatically compacted into a summary") ||
			strings.Contains(string(body), "Continue naturally from the summarized state") {
			failure = "first request contains stock summary-compaction assumptions or lost task continuity"
		}
		var descriptions strings.Builder
		for _, tool := range request.Tools {
			descriptions.Write(tool)
		}
		for _, item := range request.Input {
			if string(item["type"]) == `"additional_tools"` {
				descriptions.Write(item["tools"])
			}
		}
		catalog := descriptions.String()
		for _, name := range []string{"spawn_agent", "list_agents", "list_models", "inspect", "compact", "wait_agent", "send_message", "followup_task", "interrupt_agent"} {
			if !strings.Contains(catalog, "mcp__threadcontrol__"+name+"(args:") {
				failure = "initial execution catalog omitted threadcontrol declaration: " + name
			}
		}
		if !strings.Contains(catalog, "task_name: string;") || !strings.Contains(catalog, "message: string;") {
			failure = "initial execution catalog omitted required spawn arguments"
		}
		for _, comment := range []string{
			"// Task-name segment under this agent. Use lowercase ASCII letters, digits, underscores and hyphens; for example, trace_roster_metrics.",
			"// Model override for fork_turns=none. Set only when explicitly requested by the user, applicable AGENTS.md instructions, or skill instructions.",
			"// Required instruction files, skills, task files or mchanges IDs/ranges for fork_turns=none, acquired before first inference.",
			"// Wait timeout in milliseconds. Omit for completion waits; set only for a real deadline.",
		} {
			if !strings.Contains(catalog, comment) {
				failure = "initial execution catalog omitted field comment: " + comment
			}
		}
		modeAt := strings.LastIndex(string(body), "<multi_agent_mode>")
		wantMode := "Do not spawn sub-agents unless the user or applicable AGENTS.md/skill instructions explicitly ask"
		if parent {
			wantMode = "Proactive multi-agent delegation is active."
		}
		if modeAt < 0 || !strings.Contains(string(body)[modeAt:], wantMode) {
			failure = "inherited ultra did not select the expected effective delegation mode"
		}
		if strings.Contains(string(body), "an explicit delegation request in your authenticated parent assignment carries the parent's authority within that assignment, subject to higher-priority limits") != !parent {
			failure = "parent-assignment delegation authority is not child-only"
		}
		if strings.Contains(string(body), "Ask the user directly for clarification with functions.request_user_input_async.") != !parent {
			failure = "direct user clarification guidance is not child-only"
		}
		identity := "Your logical task is agent:main."
		if !parent {
			identity = "Your logical task is agent:main/worker. Your parent logical task is agent:main."
		}
		if grandchild {
			identity = "Your logical task is agent:main/worker/reviewer. Your parent logical task is agent:main/worker."
		}
		if strings.Count(string(body), identity+" Call threadcontrol tools inside functions.exec as tools.mcp__threadcontrol__<name>.") != 1 {
			failure = "provider prompt omitted or duplicated the logical identity and single calling convention"
		}
	}
	if !p.fullHistory && step == 1 {
		rootCount := strings.Count(string(body), "You are `agent:main`, the primary agent in a team")
		childCount := strings.Count(string(body), "You are an agent in a team of agents collaborating to complete a task.")
		wantRoot, wantChild := 0, 1
		if parent {
			wantRoot, wantChild = 1, 0
		}
		if rootCount != wantRoot || childCount != wantChild || strings.Count(string(body), "<multi_agent_mode>") != 1 {
			failure = fmt.Sprintf("stock guidance counts: root=%d child=%d parent=%t", rootCount, childCount, parent)
		}
	}
	switch {
	case grandchild:
		inherited, assigned := false, false
		for _, item := range request.Input {
			if string(item["role"]) != `"assistant"` || string(item["phase"]) != `"commentary"` {
				continue
			}
			var content []struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(item["content"], &content) != nil {
				continue
			}
			for _, part := range content {
				inherited = inherited || part.Text == "Message Type: NEW_TASK\nTask name: agent:main/worker\nSender: agent:main\nPayload:\nCHILD_ATTRIBUTED_ASSIGNMENT"
				assigned = assigned || part.Text == "Message Type: NEW_TASK\nTask name: agent:main/worker/reviewer\nSender: agent:main/worker\nPayload:\nGRANDCHILD_ATTRIBUTED_ASSIGNMENT"
			}
		}
		if step != 1 || !inherited || !assigned || !strings.Contains(string(body), "PARENT_PRIVATE_SENTINEL") {
			failure = "delegate full-history fork lost inherited assignment, own assignment or initial user context"
		}
		answer = "GRANDCHILD_CONTEXT_OK"
	case parent && p.earlyCompletion && step == 4:
		answer = "PARENT_PREMATURE_FINAL"
	case parent && p.earlyCompletion && step == 5:
		if !strings.Contains(string(body), "completion_guard") || !strings.Contains(string(body), "Active descendants: agent:main/worker. Call wait_agent before completing.") {
			failure = "early completion did not receive retained descendant hint"
		}
		p.waitTurn = metadata.TurnID
		program = `text(await tools.mcp__threadcontrol__wait_agent({target:["agent:main/worker"]}));`
		if p.agentCompact {
			program = `text(await tools.mcp__threadcontrol__wait_agent({target:["agent:main/worker"],compact:true}));`
		}
	case parent && p.earlyCompletion && step == 6:
		if metadata.TurnID == p.waitTurn || !strings.Contains(string(body), "MANAGED_CHILD_FINAL") {
			failure = "guarded completion lost native child wake"
		}
		answer = "MANAGED_PARENT_RESUMED"
	case parent && step == 1:
		p.users = users
		program = `text(Object.keys(tools).filter(name => name.includes("threadcontrol"))); text(await tools.mcp__threadcontrol__list_agents({path_prefix:"agent:main"})); text(await tools.mcp__threadcontrol__inspect({target:"agent:main"}));`
	case parent && step == 2:
		if !strings.Contains(string(body), "compactions") || !strings.Contains(string(body), "context_window") || !strings.Contains(string(body), "reasoning_effort") {
			failure = "root inspection omitted applied settings or metrics"
		}
		if strings.Contains(string(body), "context_known") || strings.Contains(string(body), "compactions_known") {
			failure = "root inspection exposed availability flags"
		}
		if !strings.Contains(string(body), "mcp__threadcontrol__spawn_agent") || !strings.Contains(string(body), "agent:main") {
			failure = "replacement MCP discovery/list_agents omitted root or spawn tool"
		}
		fork := `,fork_turns:"none"`
		if p.fullHistory {
			fork = "" // Omission exercises the public default, full history.
		}
		if p.preload {
			fork += `,preload:{sources:[{path:"preload.txt",ranges:[[2,2]]},{path:"missing.txt"},{path:"AGENTS.md"}]}`
		}
		program = `text(await tools.mcp__threadcontrol__spawn_agent({task_name:"worker",message:"CHILD_ATTRIBUTED_ASSIGNMENT"` + fork + `,cwd:` + strconv.Quote(p.childDirectory) + `}));`
	case parent && step == 3:
		if !strings.Contains(string(body), "agent:main/worker") {
			failure = "spawn omitted confirmed logical child"
			for _, item := range request.Input {
				if string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` {
					failure += "\n" + string(item["output"])
				}
			}
		}
		program = `text("PARENT_ACCEPTED_MAILBOX");`
	case parent && step == 4:
		if !strings.Contains(string(body), "CHILD_PROGRESS_ALREADY_READ") {
			failure = "parent input omitted injected mailbox message"
		}
		p.waitTurn = metadata.TurnID
		program = `text(await tools.mcp__threadcontrol__wait_agent({target:["agent:main/worker"]}));`
		if p.agentCompact {
			program = `text(await tools.mcp__threadcontrol__wait_agent({target:["agent:main/worker"],compact:true}));`
		}
		if p.attachments {
			if err := os.WriteFile(filepath.Join(p.childDirectory, "SKILL.md"), []byte("CURRENT_ATTACHED_SKILL"), 0600); err != nil {
				failure = err.Error()
			}
			if err := os.WriteFile(filepath.Join(p.childDirectory, "attachment.txt"), []byte("CHANGED_ATTACHED_FILE"), 0600); err != nil {
				failure = err.Error()
			}
			if p.attachmentReload {
				source := `{skill:"guide",path:` + strconv.Quote(filepath.Join(p.childDirectory, "SKILL.md")) + `}`
				program = `text(await tools.mcp__threadcontrol__wait_agent({target:["agent:main/worker"],compact:true,reload:{sources:[` + source + `,` + source + `]}}));`
			}
		}
	case parent && step == 5:
		if p.attachments {
			for _, expected := range []string{"CURRENT_ATTACHED_SKILL", "SELECTED_SCREEN_BYTES"} {
				if strings.Count(string(body), expected) != 1 {
					failure = "reset did not supply current attachment content exactly once: " + expected
				}
			}
			for _, excluded := range []string{"ORIGINAL_ATTACHED_SKILL", "ORIGINAL_ATTACHED_FILE", "CHANGED_ATTACHED_FILE"} {
				if strings.Contains(string(body), excluded) {
					failure = "reset supplied stale or unselected attachment content: " + excluded
				}
			}
			if !strings.Contains(string(body), "@attachment.txt $guide [Selected 1]") {
				failure = "reset lost submitted attachment placeholders"
			}
		}
		expectedUsers := p.users
		if p.compactWait || p.agentCompact {
			expectedUsers-- // Reset removes the original human prompt.
			if strings.Contains(string(body), "PARENT_PRIVATE_SENTINEL") {
				failure = "reset retained the original task"
			}
		}
		if p.attachChanges {
			expectedUsers++ // The shared reload consumer introduces captured context.
		}
		if p.attachChanges && (strings.Contains(string(body), compact.CompactMetadataStart) || strings.Contains(string(body), "mcp__threadcontrol__attach_changes") || !strings.Contains(string(body), "Reloaded context (tool output)") || !strings.Contains(string(body), "+managed-child-write") || !strings.Contains(string(body), "managed-child.txt")) {
			failure = "parent completion omitted the delegate's explicitly selected retained diff"
		}
		if p.shellWait {
			if metadata.TurnID == p.waitTurn || users != p.users+1 || strings.Count(string(body), "<user_shell_command>") != 1 || !strings.Contains(string(body), "SHELL_WAIT_OUTPUT") {
				failure = fmt.Sprintf("shell continuation: same_turn=%t user_items=%d want=%d shell_records=%d has_output=%t", metadata.TurnID == p.waitTurn, users, p.users+1, strings.Count(string(body), "<user_shell_command>"), strings.Contains(string(body), "SHELL_WAIT_OUTPUT"))
			}
		} else if metadata.TurnID == p.waitTurn || !p.attachments && users != expectedUsers || !strings.Contains(string(body), "MANAGED_CHILD_FINAL") || !strings.Contains(string(body), "agent:main/worker") || !strings.Contains(string(body), "wait_agent") {
			failure = fmt.Sprintf("local stop/wake: same_turn=%t users=%d want=%d final=%t task=%t wait=%t", metadata.TurnID == p.waitTurn, users, expectedUsers, strings.Contains(string(body), "MANAGED_CHILD_FINAL"), strings.Contains(string(body), "agent:main/worker"), strings.Contains(string(body), "wait_agent"))
		}
		answer = "MANAGED_PARENT_RESUMED"
		if p.multipleAnswers && (strings.Count(string(body), "MANAGED_CHILD_FINAL") != 1 || strings.Count(string(body), "LATER_CHILD_FINAL") != 1) {
			failure = "first awakened request omitted or repeated completed child answers"
		}
	case parent && step == 6:
		program = `text(await tools.mcp__threadcontrol__list_agents({path_prefix:"agent:main"})); text(await tools.mcp__threadcontrol__followup_task({target:"agent:main/worker",message:"COLD_CHILD_FOLLOWUP"}));`
	case parent && step == 7:
		program = `text(await tools.mcp__threadcontrol__wait_agent({target:["agent:main/worker"]}));`
	case parent && step == 8:
		if !strings.Contains(string(body), "MANAGED_CHILD_RECOVERED") {
			failure = "cold recovery omitted the retained child's attributed result"
		}
		answer = "MANAGED_PARENT_RECOVERED"
	case !parent && step == 1:
		assignment, environment := false, false
		var developerText strings.Builder
		for _, item := range request.Input {
			var content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(item["content"], &content) != nil {
				continue
			}
			for _, part := range content {
				if string(item["role"]) == `"developer"` {
					developerText.WriteString(part.Text)
				}
				if part.Text == "Message Type: NEW_TASK\nTask name: agent:main/worker\nSender: agent:main\nPayload:\nCHILD_ATTRIBUTED_ASSIGNMENT" && string(item["role"]) == `"assistant"` && string(item["phase"]) == `"commentary"` && part.Type == "output_text" {
					assignment = true
				}
				if string(item["role"]) == `"user"` && part.Type == "input_text" && strings.HasPrefix(part.Text, "<environment_context>") && strings.Contains(part.Text, "<cwd>"+p.childDirectory+"</cwd>") && !strings.Contains(part.Text, "CHILD_ATTRIBUTED_ASSIGNMENT") {
					environment = true
				}
			}
		}
		wantUsers := 1
		if !strings.Contains(developerText.String(), threadbinding.CompletionChangesGuidance) {
			failure = "initial child inference omitted completion selection guidance"
		}
		if p.preload {
			wantUsers += 2 // One shared reload context message per selected reader outcome.
		}
		inherited := strings.Contains(string(body), "PARENT_PRIVATE_SENTINEL")
		if inherited != p.fullHistory || !assignment || !environment || !p.fullHistory && users != wantUsers {
			failure = fmt.Sprintf("child attribution: full_history=%v parent_input=%v assistant_assignment=%v environment=%v user_items=%d", p.fullHistory, inherited, assignment, environment, users)
		}
		if p.preload && (!strings.Contains(string(body), "CHILD_PRELOAD_CONTENT") || !strings.Contains(string(body), "missing.txt") || !strings.Contains(string(body), "exit_code=1") || strings.Contains(string(body), "UNSELECTED_PRELOAD_CONTENT") || strings.Contains(string(body), "PARENT_PRELOAD_CONTENT")) {
			failure = "initial child inference did not receive selected child-cwd preload and explicit read failure"
		}
		if p.preload {
			if err := os.WriteFile(filepath.Join(p.childDirectory, "preload.txt"), []byte("UNSELECTED_PRELOAD_CONTENT\nCHANGED_PRELOAD_CONTENT\n"), 0600); err != nil {
				failure = err.Error()
			}
		}
		if p.fullHistory {
			guidance := developerText.String()
			if strings.Count(guidance, "NATIVE_FIXTURE_CHILD_ROLE") != 1 || strings.Count(guidance, "Your logical task is agent:main/worker.") != 1 || strings.Contains(guidance, "NATIVE_FIXTURE_ROOT_ROLE") {
				failure = "full-history child guidance omitted child role or retained duplicate/parent generated role"
			}
			call, output := false, false
			for _, item := range request.Input {
				call = call || string(item["type"]) == `"custom_tool_call"` && strings.Contains(string(item["input"]), "Object.keys(tools)")
				output = output || string(item["type"]) == `"custom_tool_call_output"` && strings.Contains(string(item["output"]), "mcp__threadcontrol__spawn_agent")
			}
			if !call || !output {
				failure = "full-history child lost the parent's completed tool call or result"
			}
		}
		program = `text(await tools.mcp__threadcontrol__send_message({target:"agent:main",message:"CHILD_PROGRESS_ALREADY_READ"})); text(await tools.exec_command({cmd:"pwd; printf managed-child-write > managed-child.txt",shell:"/bin/bash",login:false}));`
		if p.fullHistory {
			program += `text(await tools.mcp__threadcontrol__spawn_agent({task_name:"reviewer",message:"GRANDCHILD_ATTRIBUTED_ASSIGNMENT",fork_turns:"all"})); text(await tools.mcp__threadcontrol__wait_agent({target:["reviewer"]}));`
		}
	case !parent && step == 2:
		receipt := false
		changeID := ""
		for _, item := range request.Input {
			if string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` {
				receipt = receipt || strings.Contains(string(item["output"]), "mchanges ")
				if selected := regexp.MustCompile(`mchanges ([a-z]+[1-9][0-9]*)`).FindStringSubmatch(string(item["output"])); len(selected) == 2 {
					changeID = selected[1]
				}
			}
		}
		if !receipt {
			failure = "child shell tool output omitted its retained capture receipt"
		}
		if !strings.Contains(string(body), p.childDirectory) {
			failure = "stock shell did not return selected cwd"
		}
		if p.preload && ((!strings.Contains(string(body), "CHILD_PRELOAD_CONTENT") && request.PreviousResponse != "managed-"+metadata.ThreadID+"-1") || strings.Contains(string(body), "CHANGED_PRELOAD_CONTENT")) {
			failure = "child continuation reread or lost its completed preload"
		}
		answer = "MANAGED_CHILD_FINAL"
		if p.agentCompact {
			answer = ""
			program = `text(await tools.mcp__threadcontrol__compact({}));`
		}
		if p.attachChanges {
			if changeID == "" {
				failure = "child capture omitted an explicit selectable ID"
			}
			answer += "\n" + compact.CompactMetadataStart + "{\"sources\":[{\"changes\":[" + strconv.Quote(changeID) + "]}]}" + compact.CompactMetadataEnd
		}
	case !parent && step == 3 && p.agentCompact:
		if !strings.Contains(string(body), `"name":"compact"`) || !strings.Contains(string(body), "completed") {
			failure = "child compaction lost standalone continuation"
		}
		answer = "MANAGED_CHILD_FINAL"
	case !parent && step == 3:
		if !strings.Contains(string(body), "COLD_CHILD_FOLLOWUP") || !strings.Contains(string(body), "CHILD_ATTRIBUTED_ASSIGNMENT") {
			failure = "cold recovery omitted retained history or the explicit follow-up"
		}
		answer = "MANAGED_CHILD_RECOVERED"
	default:
		failure = fmt.Sprintf("unexpected inference: parent=%v step=%d", parent, step)
	}
	p.mu.Unlock()
	if failure != "" {
		t.Error(failure)
		http.Error(w, failure, 500)
		p.stop()
		return
	}
	if parent && step == 3 {
		select {
		case <-p.mailbox:
		case <-r.Context().Done():
			return
		}
	}
	if !parent && step == 2 {
		close(p.mailbox)
		select {
		case <-p.waiting:
		case <-r.Context().Done():
			return
		}
	}
	id := fmt.Sprintf("managed-%s-%d", metadata.ThreadID, step)
	if parent && step == 4 && p.recoverFailure {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"id\":%q,\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"MANAGED_PARENT_OVERLOADED\"}}}\n\n", id)
		return
	}
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	items := []any{item}
	if p.deduplication && !parent && step == 1 {
		print := "text(" + strconv.Quote(strings.Repeat("Measured output from an admitted child request. ", 32)+"\n") + ");"
		item["input"] = program + print
		items = append(items, map[string]any{"type": "custom_tool_call", "id": id + "-repeat", "call_id": id + "-repeat-call", "name": "exec", "namespace": "functions", "input": print, "status": "completed"})
	}
	if p.multipleAnswers && !parent && step == 2 {
		for index, text := range []string{"LATER_CHILD_FINAL", ""} {
			items = append(items, map[string]any{"type": "message", "id": fmt.Sprintf("%s-final-%d", id, index), "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}})
		}
	}
	events := []map[string]any{{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}}
	for index, output := range items {
		events = append(events, map[string]any{"type": "response.output_item.added", "output_index": index, "item": output}, map[string]any{"type": "response.output_item.done", "output_index": index, "item": output})
	}
	events = append(events, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": items}})
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
}

func (p *nativeThreadsProvider) serveTemporaryUsage(t *testing.T, w http.ResponseWriter, thread string, body []byte) {
	t.Helper()
	p.mu.Lock()
	if p.parent == "" {
		p.parent = thread
	}
	parent := thread == p.parent
	limited := !parent && !p.limited[thread]
	if limited {
		p.limited[thread] = true
	}
	p.calls[thread]++
	step := p.calls[thread]
	p.mu.Unlock()
	if limited {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"type":"usage_limit_reached"}}`)
		return
	}
	answer := "MAIN_SEEDED"
	if !parent {
		if !bytes.Contains(body, []byte("explicit follow-up")) {
			t.Error("temporary thread retried without explicit input")
		}
		answer = "TEMPORARY_INPUT_ACCEPTED"
	}
	id := fmt.Sprintf("temporary-%s-%d", thread, step)
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}}},
	} {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", encoded)
	}
}

func TestNativeTemporaryUsageLimitInputServerOwnedCodex(t *testing.T) {
	for _, command := range []string{"/btw initial question", "/side", "/oneoff initial question"} {
		t.Run(strings.Fields(command)[0][1:], func(t *testing.T) {
			testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{temporaryUsageCommand: command})
		})
	}
}

// The public server owns both installed host processes; this client uses native
// semantic controls without a terminal renderer or live provider credentials.
func TestNativeThreadsServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{multipleAnswers: true})
}

// Exercise ordered delivery across independent child reducers while their stock
// executions publish captures through the same retained history owner.
func (p *nativeThreadsProvider) serveConcurrentChildren(t *testing.T, w http.ResponseWriter, r *http.Request, thread string, body []byte) {
	t.Helper()
	p.mu.Lock()
	if p.parent == "" {
		p.parent = thread
	}
	parent := p.parent == thread
	p.calls[thread]++
	step := p.calls[thread]
	p.mu.Unlock()
	program, answer := "", ""
	if parent {
		switch step {
		case 1:
			program = `const results = await Promise.allSettled(["worker", "helper1", "helper2"].map(task_name => tools.mcp__threadcontrol__spawn_agent({task_name,message:"parallel shared-history work",fork_turns:"none"}))); for (const result of results) text(result);`
		case 2:
			program = `text(await tools.mcp__threadcontrol__wait_agent({target:"all",wait_mode:"all"}));`
		case 3:
			if strings.Count(string(body), "MANAGED_CHILD_FINAL:") != 3 {
				t.Error("awakened provider input omitted or repeated concurrent child results")
			}
			answer = "MANAGED_PARENT_RESUMED"
		default:
			t.Errorf("unexpected parent inference step %d", step)
		}
	} else {
		switch step {
		case 1:
			program = `const prefix = ` + strconv.Quote(thread) + `; const results = await Promise.allSettled([0,1,2,3].map(index => tools.exec_command({cmd:"printf shared-history-write >> capture-"+prefix+"-"+index+".txt",shell:"/bin/bash",login:false}))); for (const result of results) text(result);`
		case 2:
			select {
			case <-p.waiting:
			case <-r.Context().Done():
				return
			}
			answer = "MANAGED_CHILD_FINAL:" + thread
		default:
			t.Errorf("unexpected child inference step %d", step)
		}
	}
	id := fmt.Sprintf("concurrent-%s-%d", thread, step)
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}},
		{"type": "response.output_item.added", "output_index": 0, "item": item},
		{"type": "response.output_item.done", "output_index": 0, "item": item},
		{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": []any{item}}},
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Error(err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", raw)
	}
}

func TestNativeThreadsConcurrentCompletionServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{concurrentChildren: true})
}

func TestNativeThreadsOutputDeduplicationServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{deduplication: true})
}

func TestNativeThreadsUsageLimitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{usageLimit: true})
}

func TestNativeThreadsRecoverableParentServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{recoverFailure: true})
}

func TestNativeThreadsFullHistoryServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{fullHistory: true})
}

func TestNativeThreadsShellWaitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{shellWait: true})
}

func TestNativeThreadsAgentCompactWaitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{compactWait: true, agentCompact: true})
}

func TestNativeThreadsCompactWaitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{compactWait: true})
}

func TestNativeThreadsCompletionGuardServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{earlyCompletion: true})
}

func TestNativeThreadsNonGitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{nonGit: true})
}

func TestNativeThreadsPreloadServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{preload: true})
}

func TestHeadlessThreadsPreloadServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{headless: true, preload: true})
}

func TestHeadlessThreadsAttachedChangesServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{headless: true, attachChanges: true})
}

func TestHeadlessThreadsCompactWaitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{headless: true, agentCompact: true})
}

func TestHeadlessThreadsCompletionGuardServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{headless: true, earlyCompletion: true})
}

func TestNativeThreadsRewindServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{rewind: true})
}

func TestNativeThreadsAttachmentResetServerOwnedCodex(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{compactWait: true, agentCompact: true, attachments: true, attachmentReload: explicit})
		})
	}
}

type nativeThreadsScenario struct {
	concurrentChildren                                                                                                                                                bool
	attachments, attachmentReload                                                                                                                                     bool
	deduplication                                                                                                                                                     bool
	fullHistory, shellWait, compactWait, agentCompact, earlyCompletion, nonGit, preload, recoverFailure, multipleAnswers, rewind, headless, attachChanges, usageLimit bool
	temporaryUsageCommand                                                                                                                                             string
}

func testNativeThreadsServerOwnedCodex(t *testing.T, scenario nativeThreadsScenario) {
	t.Helper()
	fullHistory, shellWait := scenario.fullHistory, scenario.shellWait
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal("installed Codex is required:", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	environment := builtInOpenAICodexEnvironment(t)
	var configRoot string
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			configRoot = value
		}
	}
	claims := `{"email":"fixture@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account","chatgpt_plan_type":"pro"}}`
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".c2ln"
	auth, err := json.Marshal(map[string]any{
		"tokens":       map[string]string{"id_token": token, "access_token": "fixture-access", "refresh_token": "fixture-refresh", "account_id": "fixture-account"},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
	bundled := exec.CommandContext(ctx, codex, "debug", "models", "--bundled")
	bundled.Env = environment
	catalog, err := bundled.Output()
	if err != nil {
		t.Fatal("bundled account catalog:", err)
	}
	var modelCatalog map[string]any
	if err := json.Unmarshal(catalog, &modelCatalog); err != nil {
		t.Fatal(err)
	}
	for _, entry := range modelCatalog["models"].([]any) {
		model := entry.(map[string]any)
		if model["slug"] != "gpt-6-sol" {
			continue
		}
		// Deliberately differs from max: child configuration must use the raw
		// catalog's ordinary equivalent, not merely normalize provider requests.
		model["multi_agent_reasoning_effort"] = "low"
		model["supports_search_tool"] = true
		fields := model
		for _, key := range []string{"model_messages", "multi_agent", "role"} {
			child, ok := fields[key].(map[string]any)
			if !ok {
				child = make(map[string]any)
				fields[key] = child
			}
			fields = child
		}
		if fullHistory {
			fields["root"], fields["subagent"] = "NATIVE_FIXTURE_ROOT_ROLE", "NATIVE_FIXTURE_CHILD_ROLE"
		} else {
			delete(fields, "root")
			delete(fields, "subagent")
		}
	}
	catalog, err = json.Marshal(modelCatalog)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if !scenario.nonGit {
		workspace = gitTestWorkspace(t)
	}
	childDirectory := workspace
	if fullHistory {
		repository := newGitFixture(t)
		repository.run("commit", "--allow-empty", "-q", "-m", "base")
		childDirectory = filepath.Join(t.TempDir(), "child-worktree")
		repository.run("worktree", "add", "-q", "--detach", childDirectory, "HEAD")
	}
	if scenario.preload {
		childDirectory = t.TempDir()
		for path, body := range map[string]string{
			filepath.Join(workspace, "preload.txt"):      "PARENT_PRELOAD_CONTENT\n",
			filepath.Join(childDirectory, "preload.txt"): "UNSELECTED_PRELOAD_CONTENT\nCHILD_PRELOAD_CONTENT\n",
		} {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture := &nativeThreadsProvider{usageLimit: scenario.usageLimit, limited: make(map[string]bool), attachChanges: scenario.attachChanges, multipleAnswers: scenario.multipleAnswers, recoverFailure: scenario.recoverFailure, preload: scenario.preload, childDirectory: childDirectory, fullHistory: fullHistory, shellWait: shellWait, compactWait: scenario.compactWait, agentCompact: scenario.agentCompact, earlyCompletion: scenario.earlyCompletion, calls: make(map[string]int), waiting: make(chan struct{}), mailbox: make(chan struct{}), stop: cancel}
	fixture.attachments, fixture.attachmentReload = scenario.attachments, scenario.attachmentReload
	fixture.concurrentChildren = scenario.concurrentChildren
	fixture.temporaryUsage = scenario.temporaryUsageCommand != ""
	fixture.deduplication = scenario.deduplication
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(catalog)
		case strings.HasSuffix(r.URL.Path, "/responses") && strings.EqualFold(r.Header.Get("Upgrade"), "websocket"):
			fixture.serveSocket(t, w, r)
		case strings.HasSuffix(r.URL.Path, "/responses") && r.Method == http.MethodPost:
			fixture.serve(t, w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	socketDirectory, err := os.MkdirTemp("", "native-threads-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDirectory)
	socket := filepath.Join(socketDirectory, "s")
	stateDirectory := t.TempDir()
	capturePath := ""
	if scenario.headless {
		capturePath = filepath.Join(t.TempDir(), "capture.jsonl")
	}
	environment = append(environment, "XDG_STATE_HOME="+stateDirectory,
		"MEKUGI_NATIVE_THREADS_SOCKET="+socket,
		"MEKUGI_NATIVE_THREADS_ADDRESS="+server.Listener.Addr().String(),
		"MEKUGI_NATIVE_THREADS_CERT="+base64.StdEncoding.EncodeToString(server.Certificate().Raw),
		routerTestWorkerEnvironment+"=1")
	if scenario.headless {
		environment = append(environment, "MEKUGI_NATIVE_THREADS_HEADLESS=1", "MEKUGI_NATIVE_THREADS_CAPTURE="+capturePath)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeThreadsServerProcessHelper$")
	cmd.Env, cmd.Dir = environment, workspace
	var diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &diagnostics, &diagnostics
	var headlessOutput headlessThreadsOutput
	if scenario.headless {
		headlessOutput.fixture = fixture
		cmd.Stdin = strings.NewReader("PARENT_PRIVATE_SENTINEL delegate fresh work")
		cmd.Stdout = &headlessOutput
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct{})
	var processErr error
	go func() { processErr = cmd.Wait(); close(joined) }()
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-joined:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-joined
			t.Error("server did not join its native runtimes")
		}
		if processErr != nil || t.Failed() {
			t.Logf("server outcome: %v\n%s\n%s", processErr, &diagnostics, fixture.diagnostic())
		}
	}()

	if scenario.headless {
		select {
		case <-joined:
		case <-ctx.Done():
			t.Fatalf("headless did not join: %v\n%s", ctx.Err(), &diagnostics)
		}
		if processErr != nil {
			t.Fatalf("headless outcome: %v\n%s\n%s", processErr, &diagnostics, headlessOutput.output.String())
		}
		var methods []string
		for _, row := range bytes.Split(bytes.TrimSpace(headlessOutput.output.Bytes()), []byte("\n")) {
			var message appserver.Message
			if err := json.Unmarshal(row, &message); err != nil {
				t.Fatalf("non-JSON headless output: %v", err)
			}
			methods = append(methods, message.Method)
		}
		if len(methods) == 0 || methods[len(methods)-1] != "mekugi/headless/completed" || !headlessOutput.released || !strings.Contains(headlessOutput.output.String(), "MANAGED_PARENT_RESUMED") || !strings.Contains(headlessOutput.output.String(), "MANAGED_CHILD_FINAL") {
			t.Fatalf("headless logical completion lost family evidence: %s", headlessOutput.output.String())
		}
		if capturePath != "" {
			captures, err := filepath.Glob(capturePath + ".runtimes/*/capture.jsonl")
			if err != nil || len(captures) != 1 {
				t.Fatalf("independent child captures: %v, %v", captures, err)
			}
			captures = append(captures, capturePath)
			for _, capture := range captures {
				if _, err := os.Stat(filepath.Join(filepath.Dir(capture), "reads.jsonl")); err != nil {
					available, _ := filepath.Glob(filepath.Join(stateDirectory, "mekugi", "debug", "*", "reads.jsonl"))
					t.Fatalf("missing runtime-specific read log: %q, %v; bundle logs: %v", capture, err, available)
				}
				info, err := os.Stat(capture)
				if err != nil || info.Size() == 0 {
					t.Fatalf("missing runtime capture %q: %v", capture, err)
				}
				paired := false
				var metrics []byte
				err = filepath.WalkDir(stateDirectory, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.Type()&os.ModeSymlink == 0 || entry.Name() != "capture.jsonl" {
						return nil
					}
					target, err := filepath.EvalSymlinks(path)
					if err != nil {
						return err
					}
					if target == capture {
						metrics, err = os.ReadFile(filepath.Join(filepath.Dir(path), "metrics.json"))
						paired = err == nil
						return err
					}
					return nil
				})
				if err != nil || !paired {
					t.Fatalf("runtime capture has no joined metrics bundle: %q, %v", capture, err)
				}
				var snapshot transportcapture.MetricsSnapshot
				if err := json.Unmarshal(metrics, &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.Capture.MissingProvider != 0 {
					t.Fatalf("completed headless runtime lost provider evidence: %+v", snapshot.Capture)
				}
				raw, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				local := make(map[uint64]bool)
				for row := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
					var record struct {
						Boundary         string `json:"boundary"`
						Sequence         uint64 `json:"request_sequence"`
						ProviderExpected *bool  `json:"provider_expected"`
					}
					if err := json.Unmarshal(row, &record); err != nil {
						t.Fatal(err)
					}
					if record.Boundary == "codex" && record.ProviderExpected != nil && !*record.ProviderExpected {
						local[record.Sequence] = true
					}
				}
				localTurns := 0
				for _, exchange := range snapshot.Exchanges {
					if exchange.ProviderAttempts == nil || exchange.Status == "completed" && len(exchange.ProviderAttempts) == 0 && !local[exchange.Sequence] {
						t.Fatalf("headless exchange has no provider decision: %+v", exchange)
					}
					if exchange.RequestKind == "turn" && exchange.Status == "completed" && len(exchange.ProviderAttempts) == 0 && local[exchange.Sequence] {
						localTurns++
					}
				}
				if localTurns == 0 && (capture == capturePath || scenario.preload) {
					t.Fatalf("headless fixture did not exercise local completion: %s", capture)
				}
				if export := os.Getenv("MEKUGI_TEST_LIFECYCLE_EXPORT"); export != "" {
					relative, err := filepath.Rel(filepath.Dir(capturePath), capture)
					if err != nil {
						t.Fatal(err)
					}
					destination := filepath.Join(export, t.Name(), relative)
					if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(destination, raw, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(destination+".metrics.json", metrics, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		want := 5
		if scenario.earlyCompletion {
			want = 6
		}
		if len(fixture.calls) != 2 || fixture.calls[fixture.parent] != want || fixture.calls[fixture.child] != map[bool]int{false: 2, true: 3}[scenario.agentCompact] {
			t.Fatalf("headless repeated work or omitted children: %v", fixture.calls)
		}
		return
	}
	serverCLIAwait(t, func() bool {
		select {
		case <-joined:
			t.Fatalf("server stopped before readiness: %v\n%s", processErr, &diagnostics)
		default:
		}
		_, err := os.Stat(socket)
		return err == nil
	})
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	connection, err := sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{
		Workspace: workspace, RouterArgs: []string{"--shell-guard=false"},
		Args: []string{"--yolo", "-m", "gpt-6-sol", "-c", "features.plugins=false", "-c", `model_reasoning_effort="ultra"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var event sessionapi.NativeEvent
	for event.Frame.View.Main.ID == "" {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("root native readiness:", err)
		}
	}
	if command := scenario.temporaryUsageCommand; command != "" {
		await := func(ready func() bool) {
			t.Helper()
			for !ready() {
				event, err = connection.Next()
				if err != nil {
					t.Fatal("temporary conversation publication:", err)
				}
			}
		}
		submit := func(text string) {
			t.Helper()
			_, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
				Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: event.Frame.View.Input.ID, Operation: sessionpresentation.SubmitComposer},
				Draft:  composer.Draft{Text: text},
			}})
			if err != nil {
				t.Fatal("temporary conversation input:", err)
			}
		}
		contains := func(text string) bool {
			body, err := json.Marshal(event.Frame, jsonv1.FormatDurationAsNano(true))
			if err != nil {
				t.Fatal(err)
			}
			return bytes.Contains(body, []byte(text))
		}
		submit("seed main")
		await(func() bool { return !event.Frame.Facts.Busy && contains("MAIN_SEEDED") })
		if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Command: &command}); err != nil {
			t.Fatal("temporary conversation command:", err)
		}
		if command == "/side" {
			await(func() bool {
				return event.Frame.View.SideThread.ID != "" && event.Frame.View.Displayed == event.Frame.View.SideThread &&
					!event.Frame.View.Status.Composer.Running && event.Frame.View.Loading == ""
			})
			submit("initial question")
		}
		await(func() bool { return contains("Usage limit") })
		if strings.HasPrefix(command, "/btw") {
			command = "/btw explicit follow-up"
			if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Command: &command}); err != nil {
				t.Fatal("explicit BTW input:", err)
			}
		} else {
			submit("explicit follow-up")
		}
		// No synthetic flush or unrelated input wakes the pending admission.
		await(func() bool { return contains("TEMPORARY_INPUT_ACCEPTED") })
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if len(fixture.calls) != 2 || len(fixture.limited) != 1 {
			t.Fatalf("temporary usage request identities: calls=%v limited=%v", fixture.calls, fixture.limited)
		}
		for thread, count := range fixture.calls {
			want := 2
			if thread == fixture.parent {
				want = 1
			}
			if count != want {
				t.Fatalf("temporary usage inference count for %s: got %d, want %d", thread, count, want)
			}
		}
		return
	}
	waitingObserved, compactObserved, childReleased := false, false, false
	failureObserved, usageWaitingObserved := false, false
	root := event.Frame.View.Main.ID
	draft := composer.Draft{Text: "PARENT_PRIVATE_SENTINEL delegate fresh work"}
	if scenario.attachments {
		draft.Text += " @attachment.txt $guide [Selected 1]"
		file, skill := filepath.Join(workspace, "attachment.txt"), filepath.Join(workspace, "SKILL.md")
		if err := os.WriteFile(file, []byte("ORIGINAL_ATTACHED_FILE"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(skill, []byte("ORIGINAL_ATTACHED_SKILL"), 0600); err != nil {
			t.Fatal(err)
		}
		start := strings.Index(draft.Text, "@attachment.txt")
		draft.Files = []composer.Token{{Start: start, End: start + len("@attachment.txt"), Path: file}}
		start = strings.Index(draft.Text, "$guide")
		draft.Skills = []composer.Token{{Start: start, End: start + len("$guide"), Name: "guide", Path: skill}}
		start = strings.Index(draft.Text, "[Selected 1]")
		draft.Selections = []composer.Token{{Start: start, End: len(draft.Text), Text: "SELECTED_SCREEN_BYTES", Source: "screen"}}
	}
	_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
		Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
		Draft:  draft,
	}})
	if err != nil {
		t.Fatal("root input:", err)
	}
	for {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("parent completion:", err)
		}
		waiting := event.Frame.View.Status.Composer.Text == "Waiting for agents"
		if scenario.earlyCompletion && waiting {
			// The completion guard also commits a local stop. Release the child
			// only for the actual wait subscription, not that earlier guard stop.
			waiting = false
			if event.Frame.Agents != nil {
				for _, row := range event.Frame.Agents.Roster {
					if row.Agent.Thread == root && row.Agent.Wait != nil && row.Agent.Wait.Live {
						waiting = true
						break
					}
				}
			}
		}
		if waiting {
			if !event.Frame.Facts.Busy {
				t.Fatal("logical waiting lost the native terminal-title busy state")
			}
			if !waitingObserved {
				waitingObserved = true
				if shellWait || scenario.compactWait && !scenario.agentCompact {
					command := "!printf SHELL_WAIT_OUTPUT"
					if scenario.compactWait {
						command = "/compact"
					}
					_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
						Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
						Draft:  composer.Draft{Text: command},
					}})
					if err != nil {
						t.Fatal("auxiliary operation during logical waiting:", err)
					}
				} else if !scenario.agentCompact {
					close(fixture.waiting)
				}
			}
		}
		if scenario.usageLimit && strings.Contains(event.Frame.View.Status.Composer.Text, "usage limit") {
			usageWaitingObserved = true
			if !event.Frame.Facts.Busy || !event.Frame.View.InterruptAvailable || event.Frame.View.Status.Feedback.Alert {
				t.Fatal("usage wait lost cancellation or showed an error")
			}
		}
		if scenario.compactWait && waitingObserved && !childReleased {
			fixture.mu.Lock()
			waitTurn := fixture.waitTurn
			fixture.mu.Unlock()
			compactObserved = compactObserved || event.Frame.View.Interrupt.Turn != "" && event.Frame.View.Interrupt.Turn != waitTurn
			if event.Frame.Main != nil {
				for _, entry := range event.Frame.Main.Entries {
					compactObserved = compactObserved || entry.Kind == "compaction"
				}
			}
			if event.Frame.View.Status.Session.CompletionKnown {
				t.Fatal("compaction ended the logical wait")
			}
			if compactObserved && event.Frame.View.Interrupt.Turn == "" && event.Frame.View.Status.Composer.Text == "Waiting for agents" {
				close(fixture.waiting)
				childReleased = true
			}
		}
		body, err := json.Marshal(event.Frame, jsonv1.FormatDurationAsNano(true))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "MANAGED_PARENT_RESUMED") {
			break
		}
		failureObserved = failureObserved || strings.Contains(string(body), "MANAGED_PARENT_OVERLOADED") || strings.Contains(string(body), "out of capacity") || strings.Contains(string(body), "model is at capacity")
		if scenario.earlyCompletion && event.Frame.View.Status.Session.CompletionKnown {
			t.Fatal("early completion published a completed parent while its child was running")
		}
		if shellWait && waitingObserved && event.Frame.View.Status.Session.CompletionKnown {
			t.Fatal("standalone shell marked Main done before model continuation")
		}
	}
	if scenario.concurrentChildren {
		finals := make(map[string]bool)
		if event.Frame.Main != nil {
			for _, entry := range event.Frame.Main.Entries {
				if entry.Kind == "final" && strings.HasPrefix(entry.Text, "MANAGED_CHILD_FINAL:") {
					if finals[entry.Text] {
						t.Fatal("duplicate concurrent child result")
					}
					finals[entry.Text] = true
				}
			}
		}
		if len(finals) != 3 {
			t.Fatalf("concurrent child results=%v", finals)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if len(fixture.calls) != 4 {
			t.Fatalf("concurrent runtimes=%v", fixture.calls)
		}
		for thread, count := range fixture.calls {
			want := 2
			if thread == fixture.parent {
				want = 3
			}
			if count != want {
				t.Fatalf("concurrent inference count=%d, want %d", count, want)
			}
		}
		return
	}
	if scenario.recoverFailure && !failureObserved {
		t.Fatal("recovery hid the provider failure")
	}
	if scenario.usageLimit && !usageWaitingObserved {
		t.Fatal("native usage limit never published its virtual wait")
	}
	if shellWait {
		fixture.mu.Lock()
		calls := fixture.calls[root]
		fixture.mu.Unlock()
		if calls != 5 {
			t.Fatalf("shell resumed Main more than once: %d inference calls", calls)
		}
		close(fixture.waiting)
		return
	}
	childReply, childMessage := false, false
	wantChildReply := "MANAGED_CHILD_FINAL"
	if scenario.multipleAnswers {
		wantChildReply += "\n\nLATER_CHILD_FINAL"
	}
	if event.Frame.Main != nil {
		for _, entry := range event.Frame.Main.Entries {
			childReply = childReply || entry.Agent == "/root/worker" && entry.Kind == "final" && entry.Text == wantChildReply
			childMessage = childMessage || entry.Agent == "/root/worker" && entry.Kind == "reply" && entry.Text == "CHILD_PROGRESS_ALREADY_READ"
		}
	}
	if !childReply || !childMessage {
		t.Fatalf("delivered delegate traffic missing from Main transcript: final=%t message=%t", childReply, childMessage)
	}
	fixture.mu.Lock()
	if scenario.usageLimit && len(fixture.limited) != 2 {
		t.Errorf("usage limits did not cover Main and child: %v", fixture.limited)
	}
	parent, child := fixture.parent, fixture.child
	counts := make(map[string]int, len(fixture.calls))
	for id, n := range fixture.calls {
		counts[id] = n
	}
	fixture.mu.Unlock()
	wantParentCalls := 5
	wantThreads := 2
	if fullHistory {
		wantThreads = 3
	}
	if scenario.earlyCompletion {
		wantParentCalls = 6
	}
	wantChildCalls := 2
	if scenario.agentCompact {
		wantChildCalls = 3
	}
	if parent != root || child == "" || child == parent || len(counts) != wantThreads || counts[parent] != wantParentCalls || counts[child] != wantChildCalls {
		t.Fatalf("native host identities/inferences: root=%q parent=%q child=%q calls=%v", root, parent, child, counts)
	}
	childView, err := connection.Attach(ctx, child)
	if err != nil {
		t.Fatal("independent child runtime attachment:", err)
	}
	defer childView.Close()
	childEvent, err := childView.Next()
	if err != nil || childEvent.Frame.View.Main.ID != child || childEvent.Attachment == event.Attachment {
		t.Fatalf("independent child runtime publication: child=%q attachment=%q error=%v", childEvent.Frame.View.Main.ID, childEvent.Attachment, err)
	}
	if scenario.deduplication {
		features := func(connection *sessionapi.NativeConnection, event *sessionapi.NativeEvent) *sessionpresentation.FeatureMetrics {
			t.Helper()
			for !event.Frame.View.Admission.Submit {
				*event, err = connection.Next()
				if err != nil {
					t.Fatal("completed thread admission:", err)
				}
			}
			_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
				Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: event.Frame.View.Input.ID, Operation: sessionpresentation.SubmitComposer},
				Draft:  composer.Draft{Text: "/session"},
			}})
			if err != nil {
				t.Fatal("open Features consumer:", err)
			}
			for event.Frame.View.Panel == nil || event.Frame.View.Panel.Features == nil {
				*event, err = connection.Next()
				if err != nil {
					t.Fatal("Features publication:", err)
				}
			}
			return event.Frame.View.Panel.Features
		}
		parentFeatures, childFeatures := features(connection, &event), features(childView, &childEvent)
		self, descendants := parentFeatures.Self.OutputDeduplication, parentFeatures.Descendants.OutputDeduplication
		if childFeatures.Self.OutputDeduplication.Occurrences == 0 || descendants != childFeatures.Self.OutputDeduplication || self.Occurrences != 0 || childFeatures.Descendants.OutputDeduplication != (transportcapture.OutputDeduplicationMetrics{}) {
			t.Fatalf("completed admitted child's request receipts lost or mixed in Features: parent=%+v child=%+v", parentFeatures, childFeatures)
		}
	}
	if scenario.preload {
		attached, failed, internal := false, false, false
		var operations []string
		if childEvent.Frame.Main != nil {
			for _, entry := range childEvent.Frame.Main.Entries {
				for _, value := range entry.Values {
					if value.Kind == "op" {
						operations = append(operations, fmt.Sprintf("%s path=%q reload=%t exited=%t exit=%d code=%.180q", value.Verb, value.Path, value.AutoReload, value.Exited, value.ExitCode, value.Code))
					}
					attached = attached || value.Verb == "Attached" && value.Path == "preload.txt" && value.Exited && value.ExitCode == 0
					failed = failed || value.Verb == "Attached" && value.Path == "missing.txt" && value.Exited && value.ExitCode == 1
					internal = internal || value.Verb == "Run" && strings.Contains(value.Code, "mreload") || value.Verb == "Attached" && (len(value.Tail) > 0 || value.AutoReload)
				}
			}
		}
		if !attached || !failed || internal {
			t.Fatalf("preload attachment presentation: attached=%t failed=%t internal=%t operations=%v", attached, failed, internal, operations)
		}
	}
	if fullHistory {
		if _, err := os.Stat(filepath.Join(workspace, "managed-child.txt")); !os.IsNotExist(err) {
			t.Fatalf("child shell wrote in parent workspace: %v", err)
		}
	}
	data, err := os.ReadFile(filepath.Join(childDirectory, "managed-child.txt"))
	if err != nil || string(data) != "managed-child-write" {
		t.Fatalf("inherited child cwd effect: %q, %v", data, err)
	}
	for {
		review := childEvent.Frame.Review
		if review == nil || review.Workspace != childDirectory {
			t.Fatalf("child saved review lost its admitted workspace: %+v", review)
		}
		found := false
		for _, file := range review.Files {
			for _, chunk := range file.Chunks {
				found = found || chunk.Review.AfterPath == filepath.Join(childDirectory, "managed-child.txt")
			}
		}
		if found {
			break
		}
		// Review hydration publishes independently of task completion.
		childEvent, err = childView.Next()
		if err != nil {
			t.Fatalf("child workspace edit missing from saved Diff: %v; review=%+v", err, review)
		}
	}

	if scenario.rewind {
		for !event.Frame.View.Admission.Submit {
			event, err = connection.Next()
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
			Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
			Draft:  composer.Draft{Text: "/rewind"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		var acceptedMenu string
		var progress string
		for {
			event, err = connection.Next()
			if err != nil {
				t.Fatal("rewind publication:", err, progress)
			}
			menu := event.Frame.View.Menu
			progress = fmt.Sprintf("thread=%s submit=%t menu=%+v feedback=%s", event.Frame.View.Main.ID, event.Frame.View.Admission.Submit, menu, event.Frame.View.Status.Feedback.Text)
			menuKey := ""
			if menu != nil {
				menuKey = fmt.Sprintf("%s/%d", event.Frame.View.Main.ID, menu.ID)
			}
			if menu != nil && menuKey != acceptedMenu && len(menu.Choices) > 0 && !menu.Loading {
				acceptedMenu = menuKey
				action := sessionpresentation.MenuAction{Version: event.Frame.View.Version, Menu: menu.ID, Kind: sessionpresentation.MenuAccept, Choice: menu.Choices[0].Identity()}
				if _, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Menu: &action}); err != nil {
					t.Fatal("rewind choice:", err)
				}
			}
			if event.Frame.View.Main.ID == root && event.Frame.View.Admission.Submit && menu == nil {
				found := false
				if family := event.Frame.Family; family != nil && family.Agents != nil {
					for _, row := range family.Agents.Roster {
						found = found || row.Agent.Thread == child && row.Agent.Name == "/root/worker" && row.Agent.Final
					}
				}
				if !found {
					family, _ := json.Marshal(event.Frame.Family)
					progress += " family=" + string(family)
					continue
				}
				retained := false
				if review := event.Frame.Review; review != nil {
					for _, file := range review.Files {
						for _, chunk := range file.Chunks {
							retained = retained || chunk.Review.AfterPath == filepath.Join(childDirectory, "managed-child.txt")
						}
					}
				}
				if !retained {
					continue
				}
				break
			}
		}
		if _, err := childView.Send(ctx, childEvent.Target(), sessionapi.NativeOperation{SaveDraft: new("Retained child draft")}); err != nil {
			t.Fatal("rewind retired the existing descendant attachment:", err)
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		if len(fixture.calls) != wantThreads || fixture.calls[parent] != counts[parent] || fixture.calls[child] != counts[child] {
			t.Fatalf("rewind restarted native work: %v", fixture.calls)
		}
		return
	}
	if scenario.preload || scenario.compactWait || scenario.earlyCompletion || scenario.recoverFailure || scenario.usageLimit {
		return // Acquisition and prompt projection acceptance finish before cold recovery.
	}

	// A new server and host process recover only retained identities. Attaching a
	// resumed parent leaves its child cold until explicit selection or input.
	childView.Close()
	connection.Close()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined:
	case <-ctx.Done():
		t.Fatal("cold shutdown:", ctx.Err())
	}
	if processErr != nil {
		t.Fatal("cold shutdown:", processErr)
	}
	cmd = exec.Command(os.Args[0], "-test.run=^TestNativeThreadsServerProcessHelper$")
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = environment, workspace, &diagnostics, &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	joined = make(chan struct{})
	go func() { processErr = cmd.Wait(); close(joined) }()
	serverCLIAwait(t, func() bool { _, err := os.Stat(socket); return err == nil })
	connection, err = sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{
		Workspace: workspace, RouterArgs: []string{"--shell-guard=false"},
		Args: []string{"--yolo", "-c", "features.plugins=false", "-c", `model_reasoning_effort="high"`, "resume", root},
	})
	if err != nil {
		t.Fatal("cold root resume:", err)
	}
	defer connection.Close()
	var readiness string
	for {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("cold root readiness:", err, readiness)
		}
		retainedChild := false
		if family := event.Frame.Family; family != nil && family.Agents != nil {
			for _, row := range family.Agents.Roster {
				if row.Agent.Thread == child && row.Agent.Name == "/root/worker" && row.Agent.Final {
					retainedChild = true
				}
			}
		}
		if event.Frame.View.Main.ID == root && event.Frame.View.Admission.Submit && retainedChild {
			break
		}
		readiness = fmt.Sprintf("thread=%q submit=%t retained_child=%t", event.Frame.View.Main.ID, event.Frame.View.Admission.Submit, retainedChild)
	}
	childReply, childMessage = false, false
	if event.Frame.Main != nil {
		for _, entry := range event.Frame.Main.Entries {
			childReply = childReply || entry.Agent == "/root/worker" && entry.Kind == "final" && entry.Text == wantChildReply
			childMessage = childMessage || entry.Agent == "/root/worker" && entry.Kind == "reply" && entry.Text == "CHILD_PROGRESS_ALREADY_READ"
		}
	}
	if !childReply || !childMessage {
		t.Fatalf("cold Main lost delivered delegate traffic: final=%t message=%t", childReply, childMessage)
	}
	attachedChild, err := connection.Attach(ctx, child)
	if err != nil {
		t.Fatal("explicit cold child selection:", err)
	}
	coldChild, err := attachedChild.Next()
	if err != nil {
		t.Fatal("cold child publication:", err)
	}
	if coldChild.Frame.View.Main.ID != child || coldChild.Frame.View.Caption.Model != "gpt-6-sol" || coldChild.Frame.View.Caption.Effort != "low" || !fullHistory && coldChild.Frame.View.Caption.Role == "" {
		t.Fatalf("cold child identity=%q settings=%+v", coldChild.Frame.View.Main.ID, coldChild.Frame.View.Caption)
	}
	fixture.mu.Lock()
	if fixture.calls[parent] != counts[parent] || fixture.calls[child] != counts[child] {
		t.Errorf("child selection started inference: %v", fixture.calls)
	}
	fixture.mu.Unlock()
	attachedChild.Close()
	_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
		Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
		Draft:  composer.Draft{Text: "Resume the retained worker with new work."},
	}})
	if err != nil {
		t.Fatal("cold follow-up:", err)
	}
	for {
		event, err = connection.Next()
		if err != nil {
			t.Fatal("cold completion:", err)
		}
		body, err := json.Marshal(event.Frame, jsonv1.FormatDurationAsNano(true))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "MANAGED_PARENT_RECOVERED") {
			break
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.calls) != wantThreads || fixture.calls[parent] != 8 || fixture.calls[child] != 3 || fixture.child != child {
		t.Fatalf("cold recovery changed identity or repeated old work: %v", fixture.calls)
	}
}

func TestNativeThreadsServerProcessHelper(t *testing.T) {
	socket := os.Getenv("MEKUGI_NATIVE_THREADS_SOCKET")
	if socket == "" {
		t.Skip("isolated native-server subprocess")
	}
	certificate, err := base64.StdEncoding.DecodeString(os.Getenv("MEKUGI_NATIVE_THREADS_CERT"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(certificate)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialTLSContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&tls.Dialer{Config: &tls.Config{RootCAs: roots, ServerName: "example.com"}}).DialContext(ctx, network, os.Getenv("MEKUGI_NATIVE_THREADS_ADDRESS"))
	}
	http.DefaultTransport = transport
	defer transport.CloseIdleConnections()
	ctx, cancel := signal.NotifyContext(t.Context(), os.Interrupt)
	defer cancel()
	if os.Getenv("MEKUGI_NATIVE_THREADS_HEADLESS") == "1" {
		routerArgs := []string{"--shell-guard=false"}
		if capture := os.Getenv("MEKUGI_NATIVE_THREADS_CAPTURE"); capture != "" {
			routerArgs = append(routerArgs, "--debug", "--capture-output", capture)
			// TestMain clears inherited AX destinations before fixture dispatch.
			t.Setenv("MEKUGI_AX_OUTPUT", filepath.Join(filepath.Dir(capture), "reads.jsonl"))
		}
		code, err := wrapCodex(ctx, routerArgs, []string{"headless", "--yolo", "-m", "gpt-6-sol", "-c", "features.plugins=false", "-c", `model_reasoning_effort="ultra"`, "-c", "bypass_hook_trust=true"})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}
	if code := RunServer(ctx, []string{"--socket", socket}, os.Stderr); code != 0 {
		t.Fatalf("server exit status: %d", code)
	}
}

// Release the held child only after Main's transient host completion was emitted.
// A client that mistakes that boundary for logical completion loses the final
// parent continuation and fails the provider-count and JSONL assertions.
type headlessThreadsOutput struct {
	output, pending bytes.Buffer
	fixture         *nativeThreadsProvider
	released        bool
}

func (o *headlessThreadsOutput) Write(data []byte) (int, error) {
	o.output.Write(data)
	o.pending.Write(data)
	for {
		rows := o.pending.Bytes()
		at := bytes.IndexByte(rows, '\n')
		if at < 0 {
			break
		}
		var message appserver.Message
		err := json.Unmarshal(rows[:at], &message)
		o.pending.Next(at + 1)
		if err != nil || message.Method != "turn/completed" || o.released {
			continue
		}
		var event struct {
			Thread string `json:"threadId"`
			Turn   struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Params, &event) != nil {
			continue
		}
		o.fixture.mu.Lock()
		release := event.Thread == o.fixture.parent && event.Turn.ID == o.fixture.waitTurn && o.fixture.waitTurn != ""
		o.fixture.mu.Unlock()
		if release {
			o.released = true
			close(o.fixture.waiting)
		}
	}
	return len(data), nil
}
