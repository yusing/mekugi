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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/charmbracelet/x/ansi"
	"github.com/coder/websocket"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	agentevidence "github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

type nativeThreadsProvider struct {
	reviewSources                           bool
	authorChange                            string
	spawnSettings                           *spawnSettingsFixture
	mu                                      sync.Mutex
	childDirectory, parent, child, waitTurn string
	fullHistory                             bool
	frozenPrefix                            bool
	initialFacts                            string
	shellWait                               bool
	compactWait                             bool
	agentCompact                            bool
	earlyCompletion                         bool
	preload                                 bool
	messageSources                          bool
	deniedMessageSource                     bool
	attachChanges                           bool
	attachments, attachmentReload           bool
	recoverFailure                          bool
	recoveryRetries                         int
	multipleAnswers                         bool
	deduplication                           bool
	usageLimit                              bool
	temporaryUsage                          bool
	concurrentChildren                      bool
	readerEvidence                          bool
	childReset                              bool
	childReady                              chan string
	compactLatency                          bool
	compactIssued, compactContinued         time.Time
	limited                                 map[string]bool
	calls                                   map[string]int
	users                                   int
	waiting, mailbox                        chan struct{}
	stop                                    context.CancelFunc
}

func latestChangeReceipt(items []map[string]jsontext.Value) string {
	changeID := ""
	for _, item := range items {
		if string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` {
			for part, text := range binding.ExecutionOutputTexts(item["output"]) {
				if part == 0 {
					continue // Original host output is not a capture receipt.
				}
				ids, _, _ := strings.Cut(text, "\n")
				for _, id := range strings.Fields(ids) {
					if _, _, err := changes.ParseID(id); err == nil {
						changeID = id
					}
				}
			}
		}
	}
	return changeID
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
			if response.Code == http.StatusTooManyRequests || p.recoverFailure && response.Code == http.StatusInternalServerError {
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
	for _, item := range request.Input {
		if codexresponses.StandaloneFunctionOutput(item) {
			t.Error("provider received an uncorrelated native tool output")
			http.Error(w, "function_call_output requires call_id", http.StatusBadRequest)
			return
		}
	}
	if request.Generate != nil && !*request.Generate {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"native-prewarm\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"native-prewarm\",\"status\":\"completed\",\"output\":[]}}\n\n")
		return
	}
	if p.childReady != nil && bytes.Contains(body, []byte("Answer the question below from the supplied context.")) && bytes.Contains(body, []byte("Are the command results ready?")) {
		if len(request.Tools) != 0 {
			t.Error("side question offered tools")
		}
		writeChildActivityResponse(w, "activity-side", "", "", "The command results are ready.")
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
	if p.reviewSources {
		p.serveReviewSources(t, w, r, metadata.ThreadID, request.Input)
		return
	}
	if p.childReset {
		p.serveChildReset(t, w, r, metadata, request.PreviousResponse)
		return
	}
	if p.childReady != nil {
		p.serveChildActivity(t, w, r, metadata.ThreadID, body)
		return
	}
	if request.PreviousResponse == "" {
		facts := environmentFacts(t, body)
		if p.frozenPrefix {
			p.mu.Lock()
			if p.initialFacts == "" {
				p.initialFacts = facts
			} else if p.parent != "" && metadata.ThreadID != p.parent && p.calls[metadata.ThreadID] == 0 && p.initialFacts != facts {
				t.Error("same-workspace child or grandchild changed the frozen initial context")
			}
			p.mu.Unlock()
		}
	}
	if p.concurrentChildren {
		p.serveConcurrentChildren(t, w, r, metadata.ThreadID, body)
		return
	}
	if p.spawnSettings != nil {
		p.spawnSettings.serve(t, w, r, metadata, body)
		return
	}
	if p.temporaryUsage {
		p.serveTemporaryUsage(t, w, metadata.ThreadID, body)
		return
	}
	p.mu.Lock()
	resumeStep := 5
	if p.attachments {
		resumeStep++
	}
	resumedParent := !p.earlyCompletion && metadata.ThreadID == p.parent && p.calls[p.parent]-p.recoveryRetries >= resumeStep
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
	if parent && p.recoverFailure {
		// Keep rejecting retries until the consumer observes the recovery wait
		// and releases the child. Recovery retains the original native turn.
		if step-p.recoveryRetries > 4 {
			select {
			case <-p.waiting:
			default:
				p.recoveryRetries++
			}
		}
		step -= p.recoveryRetries
	}
	grandchild := !parent && p.child != "" && metadata.ThreadID != p.child
	if !parent && !grandchild {
		p.child = metadata.ThreadID
	}
	var program, answer, failure string
	reset := false
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
		for _, name := range []string{"spawn_agent", "list_agents", "inspect_agent", "wait_agent", "send_message", "followup_task", "interrupt_agent"} {
			if !strings.Contains(catalog, "collaboration__"+name+"(args:") {
				failure = "initial execution catalog omitted native collaboration declaration: " + name
			}
		}
		if !strings.Contains(catalog, "task_name: string;") || !strings.Contains(catalog, "message: string;") {
			failure = "initial execution catalog omitted required spawn arguments"
		}
		for _, field := range []string{"fork_turns?", "preload?", "cwd?", "timeout_ms?", "yield_time_ms?", "btw?"} {
			if !strings.Contains(catalog, field) {
				failure = "initial native catalog omitted parameter: " + field
			}
		}
		if !bytes.Contains(body, []byte(`"name":"new_context"`)) || strings.Contains(catalog, "collaboration__compact(args:") {
			failure = "native catalog lost direct reset or advertised the retired calling convention"
		}
		if bytes.Contains(body, []byte("collaboration tools cannot be called from inside")) || bytes.Contains(body, []byte("All agents use the same current working directory.")) {
			failure = "native agent guidance contradicts JavaScript tool calls or child cwd"
		}
		modeAt := strings.LastIndex(string(body), "<multi_agent_mode>")
		wantMode := "Do not spawn sub-agents unless the user or applicable AGENTS.md/skill instructions explicitly ask"
		if parent {
			wantMode = "Proactive multi-agent delegation is active."
		}
		if modeAt < 0 || !strings.Contains(string(body)[modeAt:], wantMode) {
			mode := "missing"
			if modeAt >= 0 {
				mode, _, _ = strings.Cut(string(body)[modeAt:], "</multi_agent_mode>")
			}
			failure = fmt.Sprintf("inherited ultra did not select the expected effective delegation mode: parent=%t mode=%q", parent, mode)
		}
		if strings.Contains(string(body), "an explicit delegation request in your authenticated parent assignment carries the parent's authority within that assignment, subject to higher-priority limits") != !parent {
			failure = "parent-assignment delegation authority is not child-only"
		}
		if strings.Contains(string(body), "Ask the user directly for clarification with functions.request_user_input_async.") != !parent {
			failure = "direct user clarification guidance is not child-only"
		}
		identity := "Agent name: /root"
		if !parent {
			identity = "Agent name: /root/worker"
		}
		if grandchild {
			identity = "Agent name: /root/worker/reviewer"
		}
		if strings.Count(string(body), identity+`\nFirst context window id:`) != 1 {
			failure = "provider context omitted or duplicated the canonical agent identity"
		}

	}
	if !p.fullHistory && step == 1 {
		rootCount := strings.Count(string(body), "You are `/root`, the primary agent in a team")
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
	case p.compactLatency && parent && step == 1:
		p.compactIssued = time.Now()
		reset = true
	case p.compactLatency && parent && step == 2:
		p.compactContinued = time.Now()
		if request.PreviousResponse != "" || !completedStandaloneCompaction(request.Input) {
			failure = "standalone compaction did not deliver its retained continuation"
		}
		answer = "MANAGED_PARENT_RESUMED"
	case grandchild:
		inherited, assigned := false, false
		for _, item := range request.Input {
			if string(item["type"]) != `"agent_message"` {
				continue
			}
			var content []struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(item["content"], &content) != nil {
				continue
			}
			for _, part := range content {
				inherited = inherited || part.Text == "Message Type: NEW_TASK\nTask name: /root/worker\nSender: /root\nPayload:\nCHILD_ATTRIBUTED_ASSIGNMENT"
				assigned = assigned || part.Text == "Message Type: NEW_TASK\nTask name: /root/worker/reviewer\nSender: /root/worker\nPayload:\nGRANDCHILD_ATTRIBUTED_ASSIGNMENT"
			}
		}
		if step != 1 || !inherited || !assigned || !strings.Contains(string(body), "PARENT_PRIVATE_SENTINEL") {
			failure = "delegate full-history fork lost inherited assignment, own assignment or initial user context"
		}
		answer = "GRANDCHILD_CONTEXT_OK"
	case parent && p.earlyCompletion && step == 4:
		answer = "PARENT_PREMATURE_FINAL"
	case parent && p.earlyCompletion && step == 5:
		if !strings.Contains(string(body), "completion_guard") || !strings.Contains(string(body), "Active descendants: /root/worker. Call wait_agent before completing.") {
			failure = "early completion did not receive retained descendant hint"
		}
		p.waitTurn = metadata.TurnID
		program = `text(await tools.collaboration__wait_agent({}));`
		if p.agentCompact {
			program = `text(await tools.collaboration__wait_agent({compact:true}));`
		}
	case parent && p.earlyCompletion && step == 6:
		if metadata.TurnID != p.waitTurn || !strings.Contains(string(body), "MANAGED_CHILD_FINAL") {
			failure = "guarded completion lost native child wake"
		}
		answer = "MANAGED_PARENT_RESUMED"
	case parent && step == 1:
		p.users = users
		program = `text(Object.keys(tools).filter(name => name.includes("collaboration"))); text(await tools.collaboration__list_agents({path_prefix:"/root"})); text(await tools.collaboration__inspect_agent({target:"/root"}));`
	case parent && step == 2:
		if !strings.Contains(string(body), "completedCompactions") || !strings.Contains(string(body), "contextWindow") || !strings.Contains(string(body), "reasoningEffort") {
			failure = "root inspection omitted applied settings or metrics"
		}
		if strings.Contains(string(body), "context_known") || strings.Contains(string(body), "compactions_known") {
			failure = "root inspection exposed availability flags"
		}
		if !strings.Contains(string(body), "collaboration__spawn_agent") || !strings.Contains(string(body), "/root") {
			failure = "native discovery/list_agents omitted root or spawn tool"
		}
		fork := `,fork_turns:"none"`
		if p.fullHistory {
			fork = "" // Omission exercises the public default, full history.
		}
		if p.frozenPrefix {
			if err := os.WriteFile(filepath.Join(p.childDirectory, "changed-after-initial-context"), []byte("new state"), 0600); err != nil {
				failure = err.Error()
			}
		}
		if p.preload {
			fork += `,preload:{sources:["@preload.txt#L2","@missing.txt","@AGENTS.md"]}`
		}
		assignment := "CHILD_ATTRIBUTED_ASSIGNMENT"
		if p.messageSources {
			assignment += ` @message-source.txt @missing-source.txt \@literal`
			if p.deniedMessageSource {
				assignment += " @private-source.txt"
			}
		}
		program = `text(await tools.collaboration__spawn_agent({task_name:"worker",message:` + strconv.Quote(assignment) + fork + `,cwd:` + strconv.Quote(p.childDirectory) + `}));`
	case parent && step == 3:
		if !strings.Contains(string(body), "/root/worker") {
			failure = "spawn omitted confirmed logical child"
			for _, item := range request.Input {
				if string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` {
					failure += "\n" + string(item["output"])
				}
			}
		}
		program = `text("PARENT_ACCEPTED_MAILBOX");`
		if p.readerEvidence {
			program += `text((await tools.exec_command({cmd:"mcat native-reader.txt",shell:"/bin/bash",login:false})).output);`
		}
	case parent && step == 4:
		if !strings.Contains(string(body), "CHILD_PROGRESS_ALREADY_READ") {
			failure = "parent input omitted injected mailbox message"
		}
		if p.messageSources && !strings.Contains(string(body), "PARENT_MESSAGE_SOURCE") {
			failure = "message reference did not acquire the parent recipient's source"
		}
		p.waitTurn = metadata.TurnID
		program = `text(await tools.collaboration__wait_agent({}));`
		if p.fullHistory {
			program = `text(await tools.collaboration__wait_agent({wait_mode:"all"}));`
		}
		if p.agentCompact {
			program = `text(await tools.collaboration__wait_agent({compact:true}));`
		}
		if p.attachments {
			if err := os.WriteFile(filepath.Join(p.childDirectory, "SKILL.md"), []byte("CURRENT_ATTACHED_SKILL"), 0600); err != nil {
				failure = err.Error()
			}
			if err := os.WriteFile(filepath.Join(p.childDirectory, "attachment.txt"), []byte("CHANGED_ATTACHED_FILE"), 0600); err != nil {
				failure = err.Error()
			}
			if p.attachmentReload {
				source := strconv.Quote("@" + filepath.Join(p.childDirectory, "SKILL.md"))
				program = `text(await tools.collaboration__wait_agent({compact:true,reload:{sources:[` + source + `,` + source + `]}}));`
			}
		}
	case parent && step == 5:
		if p.recoverFailure {
			completed := false
			for _, item := range request.Input {
				var parts []struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(item["content"], &parts) != nil {
					continue
				}
				for _, part := range parts {
					var wake struct {
						Reason   string `json:"reason"`
						Outcomes []struct {
							Task   string                    `json:"task_name"`
							Status map[string]jsontext.Value `json:"status"`
						} `json:"outcomes"`
					}
					if json.Unmarshal([]byte(strings.TrimPrefix(part.Text, "Native logical continuation:\n")), &wake) != nil {
						continue
					}
					for _, outcome := range wake.Outcomes {
						completed = completed || wake.Reason == "descendant_recovery" && outcome.Task == "/root/worker" && outcome.Status["completed"] != nil
					}
				}
			}
			if !completed {
				failure = "recovery wake did not identify the completed child"
			}
		}

		if p.attachments {
			if strings.Count(string(body), "CURRENT_ATTACHED_SKILL") != map[bool]int{true: 1, false: 0}[p.attachmentReload] || strings.Contains(string(body), "SELECTED_SCREEN_BYTES") {
				failure = "reset forced attachment bodies or lost explicit reload"
			}
			for _, excluded := range []string{"ORIGINAL_ATTACHED_SKILL", "ORIGINAL_ATTACHED_FILE", "CHANGED_ATTACHED_FILE"} {
				if strings.Contains(string(body), excluded) {
					failure = "reset supplied stale or unselected attachment content: " + excluded
				}
			}
		}

		expectedUsers := p.users
		if p.compactWait || p.agentCompact {
			expectedUsers = 1 // Reset replaces the prior conversation with the current environment.
			if strings.Contains(string(body), "PARENT_PRIVATE_SENTINEL") {
				failure = "reset retained the original task"
			}
		}
		if request.PreviousResponse != "" {
			expectedUsers = 0 // Linked requests send only new input items.
		}
		waitOutputs := 0
		for _, item := range request.Input {
			if string(item["type"]) == `"custom_tool_call_output"` && string(item["call_id"]) == strconv.Quote("managed-"+p.parent+"-4-call") {
				waitOutputs++
			}
		}
		if p.attachChanges && (!strings.Contains(string(body), "+managed-child-write") || !strings.Contains(string(body), "managed-child.txt")) {
			failure = "parent completion omitted the delegate's explicitly selected retained diff"
		}
		if p.shellWait {
			if metadata.TurnID != p.waitTurn || users != expectedUsers+1 || waitOutputs != 1 || strings.Count(string(body), "<user_shell_command>") != 1 || !strings.Contains(string(body), "SHELL_WAIT_OUTPUT") {
				failure = fmt.Sprintf("shell continuation: same_turn=%t user_items=%d want=%d original_outputs=%d shell_records=%d has_output=%t", metadata.TurnID == p.waitTurn, users, expectedUsers+1, waitOutputs, strings.Count(string(body), "<user_shell_command>"), strings.Contains(string(body), "SHELL_WAIT_OUTPUT"))
			}
		} else if metadata.TurnID != p.waitTurn || !p.attachments && users != expectedUsers || !strings.Contains(string(body), "MANAGED_CHILD_FINAL") || !strings.Contains(string(body), "/root/worker") || !p.recoverFailure && waitOutputs != 1 {
			failure = fmt.Sprintf("native wait continuation: same_turn=%t recovery=%t linked=%t users=%d want=%d final=%t task=%t original_outputs=%d", metadata.TurnID == p.waitTurn, p.recoverFailure, request.PreviousResponse != "", users, expectedUsers, strings.Contains(string(body), "MANAGED_CHILD_FINAL"), strings.Contains(string(body), "/root/worker"), waitOutputs)
		}
		answer = "MANAGED_PARENT_RESUMED"
		if p.multipleAnswers && (strings.Count(string(body), "MANAGED_CHILD_FINAL") != 1 || strings.Count(string(body), "LATER_CHILD_FINAL") != 1) {
			failure = "first awakened request omitted or repeated completed child answers"
		}
		if p.attachments {
			var paths []string
			for _, item := range request.Input {
				if string(item["role"]) != `"user"` {
					continue
				}
				var parts []struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(item["content"], &parts)
				for _, part := range parts {
					rest, ok := strings.CutPrefix(part.Text, "Optional attachment recall (read with mcat): ")
					if !ok {
						continue
					}
					for entry := range strings.SplitSeq(rest, "; ") {
						if _, path, ok := strings.Cut(entry, ": "); ok && path != "" {
							paths = append(paths, path)
						}
					}
				}
			}
			if len(paths) != 3 {
				failure = fmt.Sprintf("reset did not expose kind-separated recall: %v", paths)
			}
			answer = ""
			program = `const r = await tools.exec_command({cmd:` + strconv.Quote("mcat "+strings.Join(paths, " ")) + `}); text(r.output);`
		}
	case parent && step == 6 && p.attachments:
		for _, expected := range []string{"CURRENT_ATTACHED_SKILL", "SELECTED_SCREEN_BYTES", "@attachment.txt", "$guide"} {
			count := 1
			if expected == "CURRENT_ATTACHED_SKILL" && p.attachmentReload && request.PreviousResponse != "" {
				count = 0
			} // Explicit source is already cached upstream.
			if strings.Count(string(body), expected) != count {
				failure = "mcat did not recover attachment context exactly once: " + expected
			}
		}
		for _, excluded := range []string{"ORIGINAL_ATTACHED_SKILL", "ORIGINAL_ATTACHED_FILE", "CHANGED_ATTACHED_FILE"} {
			if strings.Contains(string(body), excluded) {
				failure = "mcat recovered stale or unselected file content: " + excluded
			}
		}
		answer = "MANAGED_PARENT_RESUMED"
	case parent && step == 6:
		program = `text(await tools.collaboration__list_agents({path_prefix:"/root"})); text(await tools.collaboration__followup_task({target:"/root/worker",message:"COLD_CHILD_FOLLOWUP"}));`
		if p.messageSources {
			program = `text(await tools.collaboration__followup_task({target:"/root/worker",message:"COLD_CHILD_FOLLOWUP @message-source.txt"}));`
		}
	case parent && step == 7:
		program = `text(await tools.collaboration__wait_agent({}));`
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
				if part.Text == "Message Type: NEW_TASK\nTask name: /root/worker\nSender: /root\nPayload:\nCHILD_ATTRIBUTED_ASSIGNMENT" && string(item["type"]) == `"agent_message"` && part.Type == "input_text" {
					assignment = true
				}
				if p.messageSources && strings.HasPrefix(part.Text, "Message Type: NEW_TASK\nTask name: /root/worker\nSender: /root\nPayload:\nCHILD_ATTRIBUTED_ASSIGNMENT @message-source.txt") && string(item["type"]) == `"agent_message"` && part.Type == "input_text" {
					assignment = true
				}
				if string(item["role"]) == `"user"` && part.Type == "input_text" && strings.HasPrefix(part.Text, "<environment_context>") && strings.Contains(part.Text, "<cwd>"+p.childDirectory+"</cwd>") && !strings.Contains(part.Text, "CHILD_ATTRIBUTED_ASSIGNMENT") {
					environment = true
				}
			}
		}
		wantUsers := 1
		if !strings.Contains(developerText.String(), "To attach retained changes to your parent, include explicit mchanges source references in your ordinary final answer.") {
			failure = "initial child inference omitted completion selection guidance"
		}
		inherited := strings.Contains(string(body), "PARENT_PRIVATE_SENTINEL")
		if inherited != p.fullHistory || !assignment || !environment || !p.fullHistory && users != wantUsers {
			failure = fmt.Sprintf("child attribution: full_history=%v parent_input=%v assistant_assignment=%v environment=%v user_items=%d", p.fullHistory, inherited, assignment, environment, users)
		}
		if p.preload && (!strings.Contains(string(body), "CHILD_PRELOAD_CONTENT") || !strings.Contains(string(body), `Source \"@missing.txt\": CONTENT NOT ATTACHED:`) || strings.Contains(string(body), "UNSELECTED_PRELOAD_CONTENT") || strings.Contains(string(body), "PARENT_PRELOAD_CONTENT")) {
			failure = "initial child inference did not receive selected child-cwd preload and explicit read failure"
		}
		if p.messageSources {
			if !strings.Contains(string(body), "CHILD_MESSAGE_SOURCE") || !strings.Contains(string(body), "NESTED_MESSAGE_SOURCE") || !strings.Contains(string(body), `Source \"@missing-source.txt\": CONTENT NOT ATTACHED:`) || strings.Contains(string(body), "PARENT_MESSAGE_SOURCE") || strings.Contains(string(body), "ESCAPED_SOURCE_MUST_STAY_LITERAL") {
				failure = "spawn reference did not deliver bounded child-cwd sources and visible failures"
			}
			if p.deniedMessageSource && (strings.Contains(string(body), "FORBIDDEN_MESSAGE_SOURCE") || !strings.Contains(string(body), "private-source.txt") || !strings.Contains(strings.ToLower(string(body)), "permission denied")) {
				failure = fmt.Sprintf("message source named-profile denial: forbidden_bytes=%t source_path=%t permission_denied=%t", strings.Contains(string(body), "FORBIDDEN_MESSAGE_SOURCE"), strings.Contains(string(body), "private-source.txt"), strings.Contains(strings.ToLower(string(body)), "permission denied"))
			}
			if err := os.WriteFile(filepath.Join(p.childDirectory, "message-source.txt"), []byte("CHANGED_CHILD_MESSAGE_SOURCE"), 0600); err != nil {
				failure = err.Error()
			}
		}
		if p.preload {
			if err := os.WriteFile(filepath.Join(p.childDirectory, "preload.txt"), []byte("UNSELECTED_PRELOAD_CONTENT\nCHANGED_PRELOAD_CONTENT\n"), 0600); err != nil {
				failure = err.Error()
			}
		}
		if p.fullHistory {
			guidance := developerText.String()
			if strings.Count(guidance, "NATIVE_FIXTURE_CHILD_ROLE") != 1 || strings.Count(guidance, "Agent name: /root/worker") != 1 || strings.Count(guidance, "NATIVE_FIXTURE_ROOT_ROLE") != 1 || strings.LastIndex(guidance, "NATIVE_FIXTURE_CHILD_ROLE") < strings.LastIndex(guidance, "NATIVE_FIXTURE_ROOT_ROLE") {
				failure = "full-history child lost the calling prefix or its effective child role"
			}
			call, output := false, false
			for _, item := range request.Input {
				call = call || string(item["type"]) == `"custom_tool_call"` && strings.Contains(string(item["input"]), "Object.keys(tools)")
				output = output || string(item["type"]) == `"custom_tool_call_output"` && strings.Contains(string(item["output"]), "collaboration__spawn_agent")
			}
			if !call || !output {
				failure = "full-history child lost the parent's completed tool call or result"
			}
		}
		program = `text(await tools.collaboration__send_message({target:"/root",message:"CHILD_PROGRESS_ALREADY_READ"})); text(await tools.exec_command({cmd:"pwd; printf managed-child-write > managed-child.txt",shell:"/bin/bash",login:false}));`
		if p.messageSources {
			program = `text(await tools.collaboration__send_message({target:"/root",message:"CHILD_PROGRESS_ALREADY_READ @message-source.txt"})); text(await tools.exec_command({cmd:"pwd; printf managed-child-write > managed-child.txt",shell:"/bin/bash",login:false}));`
		}
		if p.fullHistory {
			program += `text(await tools.collaboration__spawn_agent({task_name:"reviewer",message:"GRANDCHILD_ATTRIBUTED_ASSIGNMENT",fork_turns:"all"})); text(await tools.collaboration__wait_agent({}));`
		}
		if p.readerEvidence {
			program += `text((await tools.exec_command({cmd:"mcat managed-child.txt",shell:"/bin/bash",login:false})).output);`
		}
	case !parent && step == 2:
		changeID := latestChangeReceipt(request.Input)
		if changeID == "" {
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
			reset = true
		}
		if p.attachChanges {
			if changeID == "" {
				failure = "child capture omitted an explicit selectable ID"
			}
			answer += " Captured changes: mchanges:" + changeID + "."
		}
	case !parent && step == 3 && p.agentCompact:
		if request.PreviousResponse != "" || !completedStandaloneCompaction(request.Input) {
			failure = "child compaction lost standalone continuation"
		}
		if strings.Contains(string(body), "CHILD_ATTRIBUTED_ASSIGNMENT") || strings.Contains(string(body), "managed-child-write") {
			failure = "child compaction retained pre-reset task or tool history"
		}
		answer = "MANAGED_CHILD_FINAL"
	case !parent && step == 3:
		if !strings.Contains(string(body), "COLD_CHILD_FOLLOWUP") || !strings.Contains(string(body), "CHILD_ATTRIBUTED_ASSIGNMENT") {
			failure = "cold recovery omitted retained history or the explicit follow-up"
		}
		if p.messageSources && (!strings.Contains(string(body), "CHANGED_CHILD_MESSAGE_SOURCE") || !strings.Contains(string(body), "NESTED_MESSAGE_SOURCE")) {
			failure = "cold follow-up did not preserve the old capture and acquire the new source snapshot"
		}
		answer = "## Summary\nMANAGED_CHILD_RECOVERED.\n\n## Details\nBoth actual compaction and a new host turn recover."
	default:
		failure = fmt.Sprintf("unexpected inference: parent=%v step=%d", parent, step)
	}
	p.mu.Unlock()
	if failure != "" {
		if export := os.Getenv("MEKUGI_TEST_LIFECYCLE_EXPORT"); export != "" {
			directory := filepath.Join(export, t.Name())
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Log("provider failure export:", err)
			} else if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("%s-%d.json", metadata.ThreadID, step)), body, 0600); err != nil {
				t.Log("provider failure export:", err)
			}
		}
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
		// Capacity rejections have a timed provider wait, not descendant recovery.
		// A structured 500 reaches the host's internal-server failure mapping.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"type":"server_error","message":"MANAGED_PARENT_SERVER_FAILED"}}`)
		return
	}
	item := map[string]any{"type": "message", "id": id, "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id, "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	if reset {
		item = map[string]any{"type": "function_call", "id": id, "call_id": id + "-call", "name": "new_context", "namespace": "functions", "arguments": "{}", "status": "completed"}
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
			if export := os.Getenv("MEKUGI_TEST_LIFECYCLE_EXPORT"); export != "" {
				directory := filepath.Join(export, t.Name())
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Log("provider failure export:", err)
				} else if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("%s-%d.json", thread, step)), body, 0600); err != nil {
					t.Log("provider failure export:", err)
				}
			}
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
	for _, command := range []string{"/btw initial question", "/oneoff initial question"} {
		t.Run(strings.Fields(command)[0][1:], func(t *testing.T) {
			testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{temporaryUsageCommand: command})
		})
	}
}

// The public server owns the native tree; this client uses native
// semantic controls without a terminal renderer or live provider credentials.
func TestNativeThreadsServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{multipleAnswers: true})
}

func TestNativeChildActivityServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{childActivity: true})
}

func (p *nativeThreadsProvider) serveChildReset(t *testing.T, w http.ResponseWriter, r *http.Request, metadata codexresponses.TurnMetadata, previousResponse string) {
	p.mu.Lock()
	thread := metadata.ThreadID
	if p.parent == "" {
		p.parent = thread
	}
	parent := thread == p.parent
	p.calls[thread]++
	step := p.calls[thread]
	p.mu.Unlock()
	id := fmt.Sprintf("reset-%s-%d", thread, step)
	switch {
	case parent && step == 1:
		writeChildActivityResponse(w, id, `text(await tools.collaboration__spawn_agent({task_name:"worker",message:"Reset this context once, then finish.",fork_turns:"none"}));`, "", "")
	case parent && step == 2:
		writeChildActivityResponse(w, id, `text(await tools.collaboration__wait_agent({wait_mode:"all"}));`, "", "")
	case parent && step == 3:
		writeChildActivityResponse(w, id, "", "", "MANAGED_PARENT_RESUMED")
	case !parent && step == 1 && metadata.WindowNumber == 0:
		p.childReady <- thread
		select {
		case <-p.waiting:
		case <-r.Context().Done():
			return
		}
		writeNativeThreadResponse(w, id, []any{map[string]any{
			"type": "function_call", "id": id + "-call", "call_id": id + "-call",
			"name": "new_context", "namespace": "functions", "arguments": "{}", "status": "completed",
		}})
	case !parent && step == 2 && metadata.WindowNumber == 1 && metadata.ContextWindowID != "" && previousResponse == "":
		writeChildActivityResponse(w, id, "", "", "MANAGED_CHILD_RESET_CONTINUED")
	default:
		t.Errorf("unexpected reset inference: parent=%t step=%d window=%d linked=%t", parent, step, metadata.WindowNumber, previousResponse != "")
		http.Error(w, "unexpected reset request", http.StatusInternalServerError)
		p.stop()
	}
}

// Keep this script independent of the larger coordination scenario's prompt
// and reply fixtures. Neither side receives its final response until the child
// has run the command and the client can observe the active family roster.
func (p *nativeThreadsProvider) serveChildActivity(t *testing.T, w http.ResponseWriter, r *http.Request, thread string, body []byte) {
	p.mu.Lock()
	if p.parent == "" {
		p.parent = thread
	}
	parent := thread == p.parent
	if !parent {
		p.child = thread
	}
	p.calls[thread]++
	step := p.calls[thread]
	p.mu.Unlock()
	var program, answer, summary string
	switch {
	case parent && step == 1:
		program = `text(await tools.collaboration__spawn_agent({task_name:"worker",message:"Run the command and finish.",fork_turns:"none"}));
text(await tools.collaboration__send_message({target:"/root/worker",message:"MANAGED_ROOT_OUTBOX"}));
text(await tools.collaboration__send_message({target:"/root/absent",message:"MANAGED_REJECTED_MAIL"}));`
	case parent && step == 2:
		program = `text(await tools.collaboration__wait_agent({wait_mode:"all"}));`
	case parent && step == 3:
		answer = "MANAGED_PARENT_RESUMED"
	case parent && step == 4:
		program = `text(await tools.collaboration__followup_task({target:"/root/worker",message:"Continue in the open child transcript."}));
text(await tools.collaboration__send_message({target:"/root/worker",message:"Are the command results ready?",btw:true}));`
	case parent && step == 5:
		if !bytes.Contains(body, []byte("The command results are ready.")) {
			t.Error("side answer missing from tool result")
		}
		answer = "MANAGED_PARENT_FOLLOWUP"
	case parent && step == 6:
		answer = "MANAGED_PARENT_ACKNOWLEDGED"
	case !parent && step == 1:
		summary = "Child reasoning before command"
		program = `text(await tools.exec_command({cmd:"cat activity-skill/SKILL.md",shell:"/bin/bash",login:false}));
text(await tools.exec_command({cmd:"printf managed-child-write > managed-child.txt",shell:"/bin/bash",login:false}));
text(await tools.collaboration__send_message({target:"/root",message:"MANAGED_MAIN_INBOX"}));`
	case !parent && step == 2:
		if !bytes.Contains(body, []byte("Confirmed child skill read")) {
			t.Error("native child never received the skill-read result")
		}
		p.childReady <- thread
		select {
		case <-p.waiting:
		case <-r.Context().Done():
			return
		}
		summary = "Child reasoning after command"
		answer = "MANAGED_CHILD_FINAL"
	case !parent && step == 3:
		if !bytes.Contains(body, []byte("Continue in the open child transcript.")) || bytes.Contains(body, []byte("Are the command results ready?")) {
			t.Error("side question leaked into primary child input or follow-up missing")
		}
		answer = "MANAGED_CHILD_FOLLOWUP"
	default:
		t.Errorf("unexpected child activity request: parent=%t step=%d", parent, step)
		http.Error(w, "unexpected child activity request", http.StatusInternalServerError)
		p.stop()
		return
	}
	writeChildActivityResponse(w, fmt.Sprintf("activity-%s-%d", thread, step), program, summary, answer)
}
func writeChildActivityResponse(w http.ResponseWriter, id, program, summary, answer string) {
	item := map[string]any{"type": "message", "id": id + "-msg", "role": "assistant", "phase": "final_answer", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}
	if program != "" {
		item = map[string]any{"type": "custom_tool_call", "id": id + "-call", "call_id": id + "-call", "name": "exec", "namespace": "functions", "input": program, "status": "completed"}
	}
	items := []any{item}
	if summary != "" {
		items = append([]any{map[string]any{"type": "reasoning", "id": id + "-reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": summary}}}}, items...)
	}
	writeNativeThreadResponse(w, id, items)
}

func writeNativeThreadResponse(w http.ResponseWriter, id string, items []any) {
	events := []any{map[string]any{"type": "response.created", "response": map[string]any{"id": id, "status": "in_progress", "output": []any{}}}}
	for index, output := range items {
		events = append(events, map[string]any{"type": "response.output_item.added", "output_index": index, "item": output}, map[string]any{"type": "response.output_item.done", "output_index": index, "item": output})
	}
	events = append(events, map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "status": "completed", "output": items,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "input_tokens_details": map[string]int{"cached_tokens": 0}, "output_tokens_details": map[string]int{"reasoning_tokens": 0}}}})
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, routerFaultSSE(events...))
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
			program = `const results = await Promise.allSettled(["worker", "helper1", "helper2"].map(task_name => tools.collaboration__spawn_agent({task_name,message:"parallel shared-history work",fork_turns:"none"}))); for (const result of results) { if (result.status !== "fulfilled" || result.value.isError) throw new Error("concurrent spawn failed: " + JSON.stringify(result)); text(result); }`
		case 2:
			program = `text(await tools.collaboration__wait_agent({wait_mode:"all"}));`
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
	for _, sameWorkspace := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-workspace-%t", sameWorkspace), func(t *testing.T) {
			testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{fullHistory: true, sameWorkspace: sameWorkspace})
		})
	}
}

func TestNativeThreadsShellWaitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{shellWait: true})
}

// Native reset rebuilds the context-window fragment rather than fabricating a
// completed MCP compact output. No prior call/output pair survives the reset.
func completedStandaloneCompaction(input []map[string]jsontext.Value) bool {
	count := 0
	for _, item := range input {
		if string(item["role"]) != `"developer"` {
			continue
		}
		var content []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item["content"], &content) != nil {
			continue
		}
		var text strings.Builder
		for _, part := range content {
			text.WriteString(part.Text)
		}
		if !strings.Contains(text.String(), "<context_window>") {
			continue
		}
		fields := make(map[string]string)
		for line := range strings.SplitSeq(text.String(), "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if ok {
				fields[key] = value
			}
		}
		current, previous := fields["Current context window id"], fields["Previous context window id"]
		if current == "" || previous == "" || current == previous {
			return false
		}
		count++
	}
	return count == 1
}

func TestNativeThreadsAgentCompactWaitServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{compactWait: true, agentCompact: true})
}

func TestNativeChildResetHistoryReadServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{childReset: true})
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

func TestNativeReviewerReplySkipsAuthorChangesServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{reviewSources: true})
}

func (p *nativeThreadsProvider) serveReviewSources(t *testing.T, w http.ResponseWriter, r *http.Request, thread string, items []map[string]jsontext.Value) {
	p.mu.Lock()
	if p.parent == "" {
		p.parent = thread
	}
	parent := p.parent == thread
	p.calls[thread]++
	step := p.calls[thread]
	if parent && step == 2 {
		p.authorChange = latestChangeReceipt(items)
	}
	own := p.authorChange
	p.mu.Unlock()
	var program, answer string
	switch {
	case parent && step == 1:
		program = `text(await tools.exec_command({cmd:"echo AUTHOR_EDIT > author.txt"}));`
	case parent && step == 2:
		if own == "" {
			t.Error("author edit missing receipt")
		}
		program = `text(await tools.collaboration__spawn_agent({task_name:"reviewer",fork_turns:"none",message:"Review mchanges:` + own + `"}));`
	case !parent && step == 1:
		assertReviewMessage(t, items, "Review mchanges:"+own, []string{"+AUTHOR_EDIT"}, "")
		program = `text(await tools.exec_command({cmd:"echo REVIEWER_EDIT > reviewer.txt"}));`
	case !parent && step == 2:
		foreign := latestChangeReceipt(items)
		if foreign == "" || foreign == own {
			t.Error("reviewer edit missing independent receipt")
		}
		program = `text(await tools.collaboration__send_message({target:"/root",message:"Review complete mchanges:` + own + `,` + foreign + ` @review-note.txt"}));`
	case !parent && step == 3:
		close(p.mailbox) // Source acquisition and queue admission have completed.
		answer = "REVIEW_FINISHED"
	case parent && step == 3:
		select {
		case <-p.mailbox:
		case <-r.Context().Done():
			return
		}
		program = `text("Accept queued review");`
	case parent && step == 4:
		assertReviewMessage(t, items, "Review complete mchanges:"+own+",", []string{"+REVIEWER_EDIT", "REVIEW_FILE_BYTES"}, "+AUTHOR_EDIT")
		close(p.waiting)
		answer = "REVIEW_SOURCES_VERIFIED"
	default:
		t.Errorf("unexpected review inference: parent=%t step=%d", parent, step)
		p.stop()
		return
	}
	writeChildActivityResponse(w, fmt.Sprintf("review-%s-%d", thread, step), program, "", answer)
}

func assertReviewMessage(t *testing.T, items []map[string]jsontext.Value, marker string, required []string, excluded string) {
	t.Helper()
	for _, item := range items {
		if string(item["type"]) != `"agent_message"` {
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item["content"], &parts); err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			if !strings.Contains(part.Text, "Payload:\n"+marker) {
				continue
			}
			for _, body := range required {
				if !strings.Contains(part.Text, body) {
					t.Errorf("review delivery missing %q", body)
				}
			}
			if excluded != "" && strings.Contains(part.Text, excluded) {
				t.Error("reviewer reattached recipient's own diff")
			}
			return
		}
	}
	t.Errorf("missing attributed review message %q", marker)
}

func TestNativeThreadsMessageSourcesServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{messageSources: true})
}

func TestNativeThreadsMessageSourcePermissionsServerOwnedCodex(t *testing.T) {
	testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{messageSources: true, deniedMessageSource: true})
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
	reviewSources                                                                                                                                                     bool
	childActivity                                                                                                                                                     bool
	childReset                                                                                                                                                        bool
	spawnSettings                                                                                                                                                     string
	sameWorkspace                                                                                                                                                     bool
	deniedMessageSource                                                                                                                                               bool
	messageSources                                                                                                                                                    bool
	compactionRecords                                                                                                                                                 int
	concurrentChildren                                                                                                                                                bool
	attachments, attachmentReload                                                                                                                                     bool
	deduplication                                                                                                                                                     bool
	fullHistory, shellWait, compactWait, agentCompact, earlyCompletion, nonGit, preload, recoverFailure, multipleAnswers, rewind, headless, attachChanges, usageLimit bool
	temporaryUsageCommand                                                                                                                                             string
}

func testNativeThreadsServerOwnedCodex(t *testing.T, scenario nativeThreadsScenario) {
	t.Helper()
	fullHistory, shellWait := scenario.fullHistory, scenario.shellWait
	codex, err := exec.LookPath("mekugi-app-server")
	if err != nil {
		t.Fatal("managed native app-server is required:", err)
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
	// Native world-state preparation reads account policy independently of the
	// inference gateway. Keep its fake account local too, rather than timing
	// repeated failed requests to the real backend on every fresh turn.
	var policyRequests atomic.Uint64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/codex/settings/user" || r.URL.Path == "/wham/settings/user" {
			policyRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"commit_attribution_enabled":false}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()
	if err := os.WriteFile(filepath.Join(configRoot, "config.toml"), []byte("chatgpt_base_url = "+strconv.Quote(backend.URL)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := bundledModelCatalog(ctx, codex)
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
		if scenario.spawnSettings == "default-effort" {
			model["default_reasoning_level"] = "high"
		}
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
	catalogPath := filepath.Join(configRoot, "fixture-models.json")
	if err := os.WriteFile(catalogPath, catalog, 0600); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if !scenario.nonGit {
		workspace = gitTestWorkspace(t)
	}
	childDirectory := workspace
	if fullHistory && !scenario.sameWorkspace {
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
	if scenario.reviewSources {
		if err := os.WriteFile(filepath.Join(workspace, "review-note.txt"), []byte("REVIEW_FILE_BYTES"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if scenario.messageSources {
		childDirectory = t.TempDir()
		for path, body := range map[string]string{
			filepath.Join(workspace, "message-source.txt"):      "PARENT_MESSAGE_SOURCE",
			filepath.Join(childDirectory, "message-source.txt"): "CHILD_MESSAGE_SOURCE @nested-message.txt",
			filepath.Join(childDirectory, "nested-message.txt"): "NESTED_MESSAGE_SOURCE",
			filepath.Join(childDirectory, "literal"):            "ESCAPED_SOURCE_MUST_STAY_LITERAL",
			filepath.Join(childDirectory, "private-source.txt"): "FORBIDDEN_MESSAGE_SOURCE",
		} {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture := &nativeThreadsProvider{usageLimit: scenario.usageLimit, limited: make(map[string]bool), attachChanges: scenario.attachChanges, multipleAnswers: scenario.multipleAnswers, recoverFailure: scenario.recoverFailure, preload: scenario.preload, childDirectory: childDirectory, fullHistory: fullHistory, shellWait: shellWait, compactWait: scenario.compactWait, agentCompact: scenario.agentCompact, earlyCompletion: scenario.earlyCompletion, calls: make(map[string]int), waiting: make(chan struct{}), mailbox: make(chan struct{}), stop: cancel}
	fixture.attachments, fixture.attachmentReload = scenario.attachments, scenario.attachmentReload
	fixture.reviewSources = scenario.reviewSources
	fixture.messageSources = scenario.messageSources
	fixture.deniedMessageSource = scenario.deniedMessageSource
	fixture.concurrentChildren = scenario.concurrentChildren
	fixture.compactLatency = scenario.compactionRecords > 0
	fixture.childReset = scenario.childReset
	if scenario.childReset || scenario.childActivity {
		fixture.childReady = make(chan string, 1)
	}
	if scenario.childActivity {
		skillDirectory := filepath.Join(childDirectory, "activity-skill")
		if err := os.Mkdir(skillDirectory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte("Confirmed child skill read\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.frozenPrefix = fullHistory && scenario.sameWorkspace
	fixture.temporaryUsage = scenario.temporaryUsageCommand != ""
	fixture.deduplication = scenario.deduplication
	fixture.readerEvidence = scenario.headless
	if fixture.readerEvidence {
		if err := os.WriteFile(filepath.Join(workspace, "native-reader.txt"), []byte("ROOT_READER_EVIDENCE"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if scenario.spawnSettings != "" {
		fixture.spawnSettings = &spawnSettingsFixture{change: scenario.spawnSettings, ready: make(chan struct{}), release: make(chan struct{})}
	}
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
	var compactionPeer func() func()
	if scenario.compactionRecords > 0 {
		compactionPeer = prepareCompactionLatencyStore(t, filepath.Join(stateDirectory, "mekugi", "replay"), scenario.compactionRecords)
	}
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
			capture := capturePath
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
			if health := snapshot.Capture; health.MissingProvider != 0 || health.CaptureErrors != 0 || health.Incomplete != 0 || health.AttemptGaps != 0 || health.WriteErrors != 0 {
				t.Fatalf("completed headless runtime lost provider evidence: %+v", snapshot.Capture)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			local := make(map[uint64]bool)
			capturedThreads := make(map[uint64]string)
			capturedAttempts := make(map[[2]uint64]bool)
			for row := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
				var record struct {
					Boundary         string `json:"boundary"`
					Thread           string `json:"thread_id"`
					Attempt          uint64 `json:"provider_attempt"`
					Sequence         uint64 `json:"request_sequence"`
					ProviderExpected *bool  `json:"provider_expected"`
				}
				if err := json.Unmarshal(row, &record); err != nil {
					t.Fatal(err)
				}
				if record.Boundary == "codex" {
					capturedThreads[record.Sequence] = record.Thread
				}
				if record.Boundary == "provider" {
					capturedAttempts[[2]uint64{record.Sequence, record.Attempt}] = true
				}
				if record.Boundary == "codex" && record.ProviderExpected != nil && !*record.ProviderExpected {
					local[record.Sequence] = true
				}
			}
			completedThreads := make(map[string]bool)
			for _, exchange := range snapshot.Exchanges {
				if exchange.ProviderAttempts == nil || exchange.Status == "completed" && len(exchange.ProviderAttempts) == 0 && !local[exchange.Sequence] {
					t.Fatalf("headless exchange has no provider decision: %+v", exchange)
				}
				if exchange.RequestKind == "turn" && exchange.Status == "completed" && exchange.ResponseComplete {
					if capturedThreads[exchange.Sequence] != exchange.ThreadID {
						t.Fatal("shared transport capture lost the metrics thread identity")
					}
					for _, attempt := range exchange.ProviderAttempts {
						if !capturedAttempts[[2]uint64{exchange.Sequence, attempt.Attempt}] {
							t.Fatal("shared capture lost a measured provider attempt")
						}
					}
					completedThreads[exchange.ThreadID] = true
				}
			}
			fixture.mu.Lock()
			threads := []string{fixture.parent, fixture.child}
			fixture.mu.Unlock()
			if threads[0] == "" || threads[1] == "" || threads[0] == threads[1] {
				t.Fatal("shared capture lacks distinct native actors")
			}
			for _, thread := range threads {
				if !completedThreads[thread] {
					t.Fatalf("no completed measured exchange for native actor %s", thread)
				}
				reads, err := new(agentevidence.Service).ReadReads(ctx, filepath.Join(filepath.Dir(capture), "reads.jsonl"), thread)
				if err != nil || reads.Succeeded == 0 || reads.Completed != reads.Started || reads.Incomplete != 0 {
					t.Fatalf("native actor reader evidence: %+v, %v", reads, err)
				}
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
	permissionArgs := []string{"--yolo"}
	if scenario.deniedMessageSource {
		permissionArgs = []string{"-c", `default_permissions="message-sources"`, "-c", `approval_policy="never"`, "-c", fmt.Sprintf(`permissions.message-sources.filesystem={":root"="read",%q="write",%q="deny"}`, os.TempDir(), filepath.Join(childDirectory, "private-source.txt"))}
	}
	args := append(permissionArgs, "-m", "gpt-6-sol", "-c", "features.plugins=false", "-c", "model_catalog_json="+strconv.Quote(catalogPath))
	if scenario.spawnSettings != "default-effort" {
		args = append(args, "-c", `model_reasoning_effort="ultra"`)
	}
	connection, err := sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{
		Workspace: workspace, RouterArgs: []string{"--shell-guard=false"},
		Args: args,
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
				next, err := connection.Next()
				if err != nil {
					t.Fatalf("temporary conversation publication: %v; status=%+v", err, event.Frame.View.Status)
				}
				event = next
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
	if scenario.reviewSources {
		select {
		case <-fixture.waiting:
		case <-ctx.Done():
			t.Fatal("reviewer reply did not reach the author's model:", ctx.Err())
		}
		return
	}
	if fixture.spawnSettings != nil {
		fixture.spawnSettings.verify(t, ctx, connection, event)
		return
	}
	if scenario.compactionRecords > 0 {
		stop := compactionPeer()
		defer stop()
		measureCompactionDelivery(t, connection, fixture)
		t.Logf("local native account-policy requests: %d", policyRequests.Load())
		return
	}
	if scenario.childReset {
		var child string
		select {
		case child = <-fixture.childReady:
		case <-ctx.Done():
			t.Fatal("child never requested its initial inference:", ctx.Err())
		}
		childView, err := connection.Attach(ctx, child)
		if err != nil {
			t.Fatal("attach child before reset:", err)
		}
		defer childView.Close()
		next, err := childView.Next()
		if err != nil {
			t.Fatal("child admission before reset:", err)
		}
		if next.Frame.View.Main.ID != child || !next.Frame.View.Admission.ReadOnly || next.Frame.View.Admission.Submit {
			t.Fatalf("pre-reset V2 child unexpectedly admits direct input: %+v", next.Frame.View.Admission)
		}
		close(fixture.waiting)
		for {
			next, err = connection.Next()
			if err != nil {
				t.Fatal("V2 child did not continue after new_context:", err)
			}
			if next.Frame.Main != nil && slices.ContainsFunc(next.Frame.Main.Entries, func(entry presentation.Entry) bool {
				return entry.Agent == "/root/worker" && entry.Kind == "final" && entry.Text == "MANAGED_CHILD_RESET_CONTINUED"
			}) {
				return
			}
		}
	}
	if scenario.childActivity {
		select {
		case child := <-fixture.childReady:
			assertNativeChildRosterCommand(t, ctx, connection, child)
		case <-ctx.Done():
			t.Fatal("spawned child command did not reach continuation:", ctx.Err())
		}
		close(fixture.waiting)
		waitingObserved = true
	}
	for {
		next, err := connection.Next()
		if err != nil {
			t.Fatalf("parent completion: %v; status=%+v", err, event.Frame.View.Status)
		}
		event = next
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
			if compactObserved && event.Frame.View.Interrupt.Turn == waitTurn && event.Frame.View.Status.Composer.Text == "Waiting for agents" {
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
		failureObserved = failureObserved || strings.Contains(string(body), "MANAGED_PARENT_SERVER_FAILED") || strings.Contains(string(body), "We’re currently experiencing high demand")
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
			childMessage = childMessage || entry.Agent == "/root/worker" && entry.Kind == "reply" && (entry.Text == "CHILD_PROGRESS_ALREADY_READ" || scenario.messageSources && strings.HasPrefix(entry.Text, "CHILD_PROGRESS_ALREADY_READ @message-source.txt"))
		}
	}
	if !scenario.childActivity && (!childReply || !childMessage) {
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
	recoveryRetries := fixture.recoveryRetries
	fixture.mu.Unlock()
	wantParentCalls := 5 + recoveryRetries
	if scenario.childActivity {
		wantParentCalls = 3
	}
	if scenario.attachments {
		wantParentCalls++
	}
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
	if scenario.childActivity {
		// The parent can finish on the ordinary reply before the child ends.
		// Get the child's committed lifecycle before exercising a cold restart.
		for childEvent.Frame.Agents == nil || !slices.ContainsFunc(childEvent.Frame.Agents.Roster, func(row presentation.AgentRow) bool {
			return row.Agent.Thread == child && row.Agent.Final && !row.Agent.Responding
		}) {
			childEvent, err = childView.Next()
			if err != nil {
				t.Fatal("child continuation:", err)
			}
		}
		assertNativeChildCommandOrder(t, childEvent.Frame.Main)
		assertNativeTaskTraffic(t, childEvent.Frame.Main, "/root/worker", false)
		assertNativeTaskTraffic(t, event.Frame.Main, "/root", false)
		assertNativeMainMail(t, event.Frame.Main)
		for _, entry := range event.Frame.Main.Entries {
			if nativeChildCommand(entry) {
				t.Fatal("child command leaked into parent's Thread pane")
			}
		}
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
			for event.Frame.View.Panel == nil || event.Frame.View.Panel.Features == nil || event.Frame.View.Panel.Features.Unavailable {
				*event, err = connection.Next()
				if err != nil {
					t.Fatal("Features publication:", err)
				}
			}
			return event.Frame.View.Panel.Features
		}
		parentFeatures, childFeatures := features(connection, &event), features(childView, &childEvent)
		if childFeatures.Self.OutputDeduplicationBytes == 0 || childFeatures.Self.OutputDeduplicationTokens == 0 || parentFeatures.Descendants.OutputDeduplicationBytes != childFeatures.Self.OutputDeduplicationBytes || parentFeatures.Descendants.OutputDeduplicationTokens != childFeatures.Self.OutputDeduplicationTokens || parentFeatures.Self.OutputDeduplicationBytes != 0 || childFeatures.Descendants.OutputDeduplicationBytes != 0 {
			t.Fatalf("completed admitted child's native savings lost or mixed in Features: parent=%+v child=%+v", parentFeatures, childFeatures)
		}
	}
	if scenario.preload {
		attached, failed, internal := false, false, false
		groupedFailure := false
		var operations []string
		if childEvent.Frame.Main != nil {
			for _, entry := range childEvent.Frame.Main.Entries {
				for _, group := range entry.Values {
					groupedFailure = groupedFailure || group.Kind == "attachments" && group.FailedOutcome()
					internal = internal || group.Kind == "attachments" && (group.Code != "" || len(group.Tail) > 0 || group.AutoReload)
					for _, value := range presentation.Originals(group) {
						if value.Kind == "op" {
							operations = append(operations, fmt.Sprintf("%s path=%q reload=%t exited=%t exit=%d code=%.180q", value.Verb, value.Path, value.AutoReload, value.Exited, value.ExitCode, value.Code))
						}
						attached = attached || value.Verb == "Attached" && value.Path == "preload.txt" && value.Exited && value.ExitCode == 0
						failed = failed || value.Verb == "Attached" && value.Path == "missing.txt" && value.Exited && value.ExitCode == 1
						internal = internal || value.Verb == "Run" && strings.Contains(value.Code, "mreload") || value.Verb == "Attached" && (value.Code != "" || len(value.Tail) > 0 || value.AutoReload)
					}
				}
			}
		}
		if !attached || !failed || !groupedFailure || internal {
			t.Fatalf("preload attachment presentation: attached=%t failed=%t groupedFailure=%t internal=%t operations=%v", attached, failed, groupedFailure, internal, operations)
		}
	}
	if fullHistory && childDirectory != workspace {
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
	reopen := func() {
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
			Args: append(permissionArgs, "-c", "features.plugins=false", "-c", `model_reasoning_effort="high"`, "resume", root),
		})
		if err != nil {
			t.Fatal("cold resume:", err)
		}
		var readiness string
		for {
			event, err = connection.Next()
			if err != nil {
				t.Fatal("cold root readiness:", err, readiness)
			}
			retainedChild := false
			if family := event.Frame.Family; family != nil && family.Agents != nil {
				for _, row := range family.Agents.Roster {
					if row.Agent.Thread == child && row.Agent.Name == "/root/worker" && (row.Agent.Final || scenario.childActivity) {
						retainedChild = true
					}
				}
			}
			if event.Frame.View.Main.ID == root && event.Frame.View.Admission.Submit && retainedChild {
				break
			}
			readiness = fmt.Sprintf("thread=%q submit=%t retained_child=%t", event.Frame.View.Main.ID, event.Frame.View.Admission.Submit, retainedChild)
		}
	}
	reopen()
	defer func() { _ = connection.Close() }()
	childReply, childMessage = false, false
	if event.Frame.Main != nil {
		for _, entry := range event.Frame.Main.Entries {
			childReply = childReply || entry.Agent == "/root/worker" && entry.Kind == "final" && entry.Text == wantChildReply
			childMessage = childMessage || entry.Agent == "/root/worker" && entry.Kind == "reply" && (entry.Text == "CHILD_PROGRESS_ALREADY_READ" || scenario.messageSources && strings.HasPrefix(entry.Text, "CHILD_PROGRESS_ALREADY_READ @message-source.txt"))
		}
	}
	if !scenario.childActivity && (!childReply || !childMessage) {
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
	if scenario.childActivity {
		defer attachedChild.Close()
		if coldChild.Frame.View.Main.ID != child || coldChild.Frame.View.Admission.Submit {
			t.Fatalf("retained child selection did not open read-only: thread=%q submit=%t", coldChild.Frame.View.Main.ID, coldChild.Frame.View.Admission.Submit)
		}
		assertNativeChildCommandOrder(t, coldChild.Frame.Main)
		assertNativeTaskTraffic(t, coldChild.Frame.Main, "/root/worker", false)
		assertNativeTaskTraffic(t, event.Frame.Main, "/root", false)
		assertNativeMainMail(t, event.Frame.Main)
		var retainedAgent presentation.Agent
		if agents := coldChild.Frame.Agents; agents != nil {
			for _, row := range agents.Roster {
				if row.Agent.Thread == child {
					retainedAgent = row.Agent
					break
				}
			}
		}
		if !retainedAgent.WorkKnown || !retainedAgent.WorkSince.IsZero() || !slices.Contains(retainedAgent.Skills, "activity-skill") ||
			!retainedAgent.TokensKnown || !retainedAgent.UsagePartial || !retainedAgent.CostKnown || !retainedAgent.CostPartial || retainedAgent.Cost <= 0 {
			t.Fatalf("retained child roster: skills=%v work=%t since=%v tokens=%t partial=%t cost=%f known=%t costPartial=%t",
				retainedAgent.Skills, retainedAgent.WorkKnown, retainedAgent.WorkSince, retainedAgent.TokensKnown, retainedAgent.UsagePartial,
				retainedAgent.Cost, retainedAgent.CostKnown, retainedAgent.CostPartial)
		}
		fixture.mu.Lock()
		coldParentCalls, coldChildCalls := fixture.calls[parent], fixture.calls[child]
		fixture.mu.Unlock()
		if coldParentCalls != counts[parent] || coldChildCalls != counts[child] {
			t.Fatalf("cold child selection started inference: Main=%d child=%d", coldParentCalls, coldChildCalls)
		}
		_, err := connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
			Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
			Draft:  composer.Draft{Text: "Resume the retained worker with new work."},
		}})
		if err != nil {
			t.Fatal("cold child follow-up:", err)
		}
		for {
			liveChild, err := attachedChild.Next()
			if err != nil {
				t.Fatal("open child transcript did not follow new native work:", err)
			}
			if transcript := liveChild.Frame.Main; transcript != nil && slices.ContainsFunc(transcript.Entries, func(entry presentation.Entry) bool {
				return entry.Kind == "text" && entry.Agent == "Main" && entry.Text == "MANAGED_CHILD_FOLLOWUP"
			}) {
				assertNativeChildCommandOrder(t, transcript)
				assertNativeTaskTraffic(t, transcript, "/root/worker", true)
				break
			}
		}
		for {
			event, err = connection.Next()
			if err != nil {
				t.Fatal("follow-up Main publication:", err)
			}
			if event.Frame.Main != nil && slices.ContainsFunc(event.Frame.Main.Entries, func(e presentation.Entry) bool { return e.Kind == "text" && e.Text == "MANAGED_PARENT_FOLLOWUP" }) {
				break
			}
		}
		// A completion is queued for the next sampling boundary, not inferred
		// from acceptance of either the assignment or the side answer.
		for !event.Frame.View.Admission.Submit {
			event, err = connection.Next()
			if err != nil {
				t.Fatal("follow-up settlement:", err)
			}
		}
		_, err = connection.Send(ctx, event.Target(), sessionapi.NativeOperation{Composer: &sessionapi.NativeComposerInput{
			Intent: sessionpresentation.ComposerIntent{Version: event.Frame.View.Version, Thread: root, Operation: sessionpresentation.SubmitComposer},
			Draft:  composer.Draft{Text: "Acknowledge the worker result."},
		}})
		if err != nil {
			t.Fatal(err)
		}
		for {
			event, err = connection.Next()
			if err != nil {
				t.Fatal("result acknowledgement:", err)
			}
			if event.Frame.Main != nil && slices.ContainsFunc(event.Frame.Main.Entries, func(e presentation.Entry) bool { return e.Text == "MANAGED_PARENT_ACKNOWLEDGED" }) {
				break
			}
		}
		assertNativeTaskTraffic(t, event.Frame.Main, "/root", true)
		attachedChild.Close()
		reopen()
		assertNativeTaskTraffic(t, event.Frame.Main, "/root", true)
		restored, err := connection.Attach(ctx, child)
		if err != nil {
			t.Fatal(err)
		}
		defer restored.Close()
		reopened, err := restored.Next()
		if err != nil {
			t.Fatal(err)
		}
		assertNativeTaskTraffic(t, reopened.Frame.Main, "/root/worker", true)
		return
	}
	if coldChild.Frame.View.Main.ID != child || coldChild.Frame.View.Caption.Model != "gpt-6-sol" || coldChild.Frame.View.Caption.Effort != "low" {
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
	var received []presentation.Entry
	for _, entry := range event.Frame.Main.Entries {
		if strings.Contains(entry.Text, "MANAGED_CHILD_RECOVERED") {
			if entry.Agent != "/root/worker" || entry.Kind != "final" || !entry.Delivered {
				t.Fatalf("follow-up received answer lost its author: %+v", entry.Facts)
			}
			received = append(received, entry)
		}
	}
	if len(received) != 1 {
		t.Fatalf("follow-up delivered %d answers, want one", len(received))
	}
	selected, err := connection.Attach(ctx, child)
	if err != nil {
		t.Fatal("follow-up child selection:", err)
	}
	defer selected.Close()
	childAnswer, err := selected.Next()
	if err != nil {
		t.Fatal("follow-up child publication:", err)
	}
	var own []presentation.Entry
	for _, entry := range childAnswer.Frame.Main.Entries {
		if strings.Contains(entry.Text, "MANAGED_CHILD_RECOVERED") {
			if entry.Agent != "Main" || entry.Kind != "text" || entry.Delivered {
				t.Fatalf("follow-up own answer became received traffic: %+v", entry.Facts)
			}
			own = append(own, entry)
		}
	}
	if len(own) != 1 || childAnswer.Frame.View.Info.Caller != "/root/worker" {
		t.Fatalf("follow-up own answer identity: count=%d caller=%q", len(own), childAnswer.Frame.View.Info.Caller)
	}
	for _, check := range []struct {
		entries         []presentation.Entry
		caller, heading string
	}{
		{received, event.Frame.View.Info.Caller, "worker → main finished"},
		{own, childAnswer.Frame.View.Info.Caller, "worker"},
	} {
		view := activityui.NewView()
		view.Conversation = true
		view.Update(&presentation.Publication{Entries: check.entries}, activityui.Display{MainCaller: check.caller})
		rows := view.RenderFeed(100, 100).Lines
		if len(rows) == 0 || !strings.Contains(ansi.Strip(rows[0]), check.heading) {
			t.Fatalf("follow-up rendered heading: %q, want %q", rows, check.heading)
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.calls) != wantThreads || fixture.calls[parent] != 8 || fixture.calls[child] != 3 || fixture.child != child {
		t.Fatalf("cold recovery changed identity or repeated old work: %v", fixture.calls)
	}
}

func assertNativeMainMail(t *testing.T, feed *presentation.Publication) {
	t.Helper()
	type route struct{ sender, recipient, body string }
	var got []route
	for _, entry := range feed.Entries {
		if entry.Kind != "reply" {
			continue
		}
		for _, value := range entry.Values {
			if value.From != "" && value.To != "" {
				got = append(got, route{value.From, value.To, entry.Text})
			}
		}
	}
	want := []route{{"Main", "/root/worker", "MANAGED_ROOT_OUTBOX"}, {"/root/worker", "Main", "MANAGED_MAIN_INBOX"}}
	if len(got) != len(want) || !slices.Contains(got, want[0]) || !slices.Contains(got, want[1]) {
		t.Fatalf("main native mail = %+v, want %+v", got, want)
	}
}

// Check the published routes and the actual transcript, not the JavaScript call text.
func assertNativeTaskTraffic(t *testing.T, feed *presentation.Publication, caller string, followup bool) {
	t.Helper()
	if feed == nil {
		t.Fatal("missing task transcript")
	}
	from, to, kind := "Main", "/root/worker", "start"
	if caller != "/root" {
		from, to, kind = "/root", "Main", "assignment"
	}
	check := func(kind, from, to, body string) {
		t.Helper()
		count := 0
		for _, e := range feed.Entries {
			if e.Kind != kind {
				continue
			}
			for _, v := range e.Values {
				if v.Body == body && v.From == from && v.To == to {
					count++
				}
			}
		}
		if count != 1 {
			t.Fatalf("got %d %s values from %s to %s with %q", count, kind, from, to, body)
		}
	}
	check(kind, from, to, "Run the command and finish.")
	if followup {
		check("assignment", from, to, "Continue in the open child transcript.")
		if caller == "/root" {
			check("progress", from, to, "Are the command results ready?")
			check("progress", to, from, "The command results are ready.")
			check("final", to, from, "MANAGED_CHILD_FOLLOWUP")
		}
	}
	view := activityui.NewView()
	view.Conversation = true
	view.Update(feed, activityui.Display{MainCaller: caller, Caller: caller})
	rendered := strings.Join(view.RenderFeed(110, 120).Lines, "\n")
	frame := ansi.Strip(rendered)
	if strings.Count(frame, "Run the command and finish.") != 1 || followup && strings.Count(frame, "Continue in the open child transcript.") != 1 {
		t.Fatalf("assignment rendering for %s:\n%s", caller, frame)
	}
	if followup {
		count := 0
		if caller == "/root" {
			count = 1
		}
		if strings.Count(frame, "Are the command results ready?") != count || strings.Count(frame, "The command results are ready.") != count {
			t.Fatalf("side exchange rendering for %s:\n%s", caller, frame)
		}
		t.Logf("Rendered %s transcript:\n%s", caller, rendered)
	}
}

func nativeChildCommand(entry presentation.Entry) bool {
	return entry.Kind == "tool" && strings.Contains(entry.Text, "printf managed-child-write > managed-child.txt")
}

func assertNativeChildCommandOrder(t *testing.T, transcript *presentation.Publication) {
	t.Helper()
	const first, last = "Child reasoning before command", "Child reasoning after command"
	if transcript == nil {
		t.Fatal("missing child Thread transcript")
	}
	before, command, after := -1, -1, -1
	for i, entry := range transcript.Entries {
		switch {
		case nativeChildCommand(entry) && entry.OutputComplete:
			if command >= 0 {
				t.Fatal("child transcript repeated the retained command")
			}
			command = i
		case entry.Kind == "reasoning" && strings.Contains(entry.Text, first):
			before = i
		case entry.Kind == "reasoning" && strings.Contains(entry.Text, last):
			after = i
		case strings.Contains(entry.Text, "PARENT_PRIVATE_SENTINEL") || entry.Text == "MANAGED_PARENT_RESUMED":
			t.Fatal("Main transcript leaked into selected child")
		}
	}
	if before < 0 || command <= before || after <= command {
		t.Fatalf("child Thread command, outcome or reasoning out of order: before=%d command=%d after=%d", before, command, after)
	}
	view := activityui.NewView()
	view.Conversation = true
	view.Update(transcript, activityui.Display{MainCaller: "/root/worker", Caller: "/root/worker"})
	visible := ansi.Strip(strings.Join(view.RenderFeed(100, 50).Lines, "\n"))
	before, command, after = strings.Index(visible, first), strings.Index(visible, "managed-child.txt"), strings.Index(visible, last)
	if before < 0 || command <= before || after <= command {
		t.Fatalf("rendered child Thread lost the command or reasoning order:\n%s", visible)
	}
}

func assertNativeChildRosterCommand(t *testing.T, ctx context.Context, root *sessionapi.NativeConnection, thread string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	child, err := root.Attach(ctx, thread)
	if err != nil {
		t.Fatal("attach active native child:", err)
	}
	defer child.Close()
	stop := context.AfterFunc(ctx, func() { _ = child.Close() })
	defer stop()
	var lastKind, roster string
	var activity []string
	for {
		event, err := child.Next()
		if err != nil {
			t.Fatalf("active child did not publish its transcript and roster: latest=%q %s activity=%v error=%v", lastKind, roster, activity, err)
		}
		if event.Frame.Family == nil || event.Frame.Family.Agents == nil {
			continue
		}
		for _, row := range event.Frame.Family.Agents.Roster {
			agent := row.Agent
			if agent.Thread != thread || agent.Name != "/root/worker" || !agent.Responding {
				continue
			}
			lastKind = agent.LatestKind
			roster = fmt.Sprintf("work=%t since=%v skills=%v tokens=%t partial=%t cost=%f known=%t", agent.WorkKnown, agent.WorkSince, agent.Skills, agent.TokensKnown, agent.UsagePartial, agent.Cost, agent.CostKnown)
			if event.Frame.Main == nil {
				continue
			}
			activity = activity[:0]
			for _, entry := range event.Frame.Main.Entries {
				activity = append(activity, fmt.Sprintf("%s/%s %q (running=%t unconfirmed=%t)", entry.Agent, entry.Kind, entry.Text, entry.Running, entry.Unconfirmed))
				for _, value := range entry.Values {
					if value.Kind == "reads" {
						activity = append(activity, fmt.Sprintf("%s:%v:exit=%d", value.Verb, value.Reads, value.ExitCode))
					}
				}
			}
			for _, entry := range event.Frame.Main.Entries {
				if nativeChildCommand(entry) && entry.OutputComplete {
					if agent.WorkKnown && !agent.WorkSince.IsZero() && slices.Contains(agent.Skills, "activity-skill") && agent.TokensKnown && !agent.UsagePartial && agent.CostKnown && agent.Cost > 0 {
						return
					}
				}
			}
		}
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

// Release the held child only after Main's native wait item was emitted.
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
		if err != nil || message.Method != "item/started" || o.released {
			continue
		}
		var event struct {
			Thread string `json:"threadId"`
			Turn   string `json:"turnId"`
			Item   struct {
				Type string `json:"type"`
				Tool string `json:"tool"`
			} `json:"item"`
		}
		if json.Unmarshal(message.Params, &event) != nil {
			continue
		}
		o.fixture.mu.Lock()
		release := event.Thread == o.fixture.parent && event.Turn == o.fixture.waitTurn && o.fixture.waitTurn != "" && event.Item.Type == "collabAgentToolCall" && event.Item.Tool == "wait"
		o.fixture.mu.Unlock()
		if release {
			o.released = true
			close(o.fixture.waiting)
		}
	}
	return len(data), nil
}
