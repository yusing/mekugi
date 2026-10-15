package router

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/yusing/mekugi/capturer"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const (
	applyPatchToolName = "apply_patch"

	maxMekugiScriptBytes = 1 << 20
)

type mekugiProxy struct {
	registry         *toolRegistry
	duplicateOutput  bool
	usage            *threadUsage
	autoLiveDiff     *autoLiveDiff
	activity         *subagentActivity
	guard            *shellApprovalServer
	execTrack        *execTrackHub // Set for the UI when command shells are tracked.
	skillsManager    bool
	execWindows      *execWindowRegistry
	nativeTrace      *nativeToolTrace
	approvalMu       sync.Mutex
	approvalFeedback map[[2]string][]string // Live thread/turn feedback; no host steering.

	mu                 sync.RWMutex
	toolInputEmissions map[*mekugiResponseTransform]map[string]bool // Live generation only, never command execution or replay.
	contextUsage       map[string]hostContextUsage                  // Latest host context observation, never inherited.
	btwThreads         map[string]bool                              // Live ephemeral UI threads; never inherited or replayed.
	replayStore        *mekugiReplayStore
	sessions           map[string]*mekugiHistorySession
	noticeSink         func(string, string, string, string)
	storageTurns       map[string]uint64
	storageSequence    uint64
	storageLeases      map[string]func()
	activeSessions     map[string]int
	historyBytes       int
	sessionSequence    uint64
	closed             bool
}

func newMekugiProxy(registry *toolRegistry) *mekugiProxy {
	if registry == nil {
		return nil
	}
	activity := newSubagentActivity()
	activity.usage = newThreadUsage()
	proxy := &mekugiProxy{
		registry:       registry,
		usage:          activity.usage,
		activity:       activity,
		execWindows:    &execWindowRegistry{},
		sessions:       make(map[string]*mekugiHistorySession),
		activeSessions: make(map[string]int),
	}
	activity.notice = func(category, message string) { proxy.notice("", "", category, message) }
	return proxy
}

func (p *mekugiProxy) notice(session, thread, category, message string) {
	if p != nil && p.noticeSink != nil {
		p.noticeSink(session, thread, category, message)
	}
}

func (p *mekugiProxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closed = true
	defer p.mu.Unlock()
	if p.execTrack != nil {
		p.execTrack.close()
	}
	p.guard.close()
	var cleanupErr error
	for _, release := range p.storageLeases {
		release()
	}
	clear(p.storageLeases)
	clear(p.sessions)
	clear(p.activeSessions)
	p.historyBytes = 0
	p.usage.close()
	p.activity.close()
	return cleanupErr
}

type mekugiTranslationState struct {
	previews        map[string]*liveDiffPreviewWorker
	nativeExecCalls map[string]map[string]jsonv1.RawMessage
	local           map[string]mekugiHistory

	// localSequence orders the calls translated during this turn, so a
	// recovery resolves against the newest rejection rather than an arbitrary
	// map entry. Retained order is reassigned when the turn commits.
	localSequence    uint64
	storageIdle      bool
	historyCommitted bool
}

type mekugiResponseTransform struct {
	toolTrace        toolObservationTrace
	ctx              context.Context
	proxy            *mekugiProxy
	sessionID        string
	shellTurnID      string
	execGroup        string
	shellThreadID    string // Runtime identity remains available when activity attribution is invalid.
	model            string
	visible          map[string]mekugiHistory
	historySessionID string
	sessionActive    bool
	threadID         string

	originalTools             jsonv1.RawMessage
	originalToolsPresent      bool
	originalToolChoice        jsonv1.RawMessage
	originalToolChoicePresent bool
	directory                 string
	mekugiTranslationState

	commentaryAuthor string

	subagentTurn            bool
	activityResponding      bool // Counted in the agents-pane roster until Close.
	liveDiffCompletionReady bool
	storageCompletionReady  bool
	storageToolHandoff      bool
	usageTracker            *threadUsageObservation
	finalAnswer             finalAnswerStream
	usageObserved           bool

	codeModeToolName string
	// sessionShell runs stock commands that name no shell of their own.
	sessionShell string
}

func (t *mekugiResponseTransform) Close() {
	if t == nil {
		return
	}
	t.clearToolInputEmissions()
	for itemID := range t.previews {
		t.endPreview(itemID)
	}
	if t.activityResponding {
		t.activityResponding = false
		t.proxy.activity.endResponse(t.threadID)
	}
	if t.sessionActive {
		t.proxy.deactivateSession(t.historySessionID)
		if t.storageIdle {
			t.proxy.releaseIdleStorageSession(t.ctx, t.shellThreadID)
		}
		t.sessionActive = false
	}
}

// observeResponseUsage records provider-authoritative token usage for this response.
func (t *mekugiResponseTransform) observeResponseUsage(counts tokenCounts) {
	if t.usageObserved {
		return
	}
	t.usageTracker.observe(counts)
	t.usageObserved = true
	if t.proxy != nil && t.threadID != "" {
		t.proxy.activity.syncUsage(t.threadID)
	}
}

func validateMekugiCompactionRequest(request *parsedResponsesRequest, metadata codexTurnMetadata) error {
	var compaction struct {
		Trigger        string `json:"trigger"`
		Reason         string `json:"reason"`
		Implementation string `json:"implementation"`
		Phase          string `json:"phase"`
		Strategy       string `json:"strategy"`
	}
	if err := jsonv1.Unmarshal(metadata.Compaction, &compaction); err != nil || slices.ContainsFunc(
		[]string{compaction.Trigger, compaction.Reason, compaction.Implementation, compaction.Phase, compaction.Strategy},
		func(value string) bool { return strings.TrimSpace(value) == "" },
	) {
		return errors.New("mekugi rewrite requires valid compaction metadata")
	}
	if !request.streamResponse {
		return errors.New("mekugi compaction bypass requires a streaming request")
	}
	v2 := compaction.Implementation == "responses_compaction_v2"

	var tools []jsonv1.RawMessage
	if rawTools, exists := request.fields["tools"]; exists {
		if err := jsonv1.Unmarshal(rawTools, &tools); err != nil {
			return fmt.Errorf("decode compaction tools: %w", err)
		}
	}
	if !v2 && len(tools) != 0 {
		return errors.New("mekugi compaction request cannot expose tools")
	}

	var items []jsonv1.RawMessage
	if err := jsonv1.Unmarshal(request.fields["input"], &items); err != nil {
		return fmt.Errorf("decode compaction input: %w", err)
	}
	if len(items) == 0 {
		return errors.New("mekugi compaction request requires nonempty input")
	}
	triggers := 0
	for index, rawItem := range items {
		var item map[string]jsonv1.RawMessage
		if err := jsonv1.Unmarshal(rawItem, &item); err != nil || item == nil {
			return errors.New("mekugi compaction request contains a malformed input item")
		}
		if responseevents.StringField(item, "type") == "compaction_trigger" {
			triggers++
			if !v2 || index != len(items)-1 {
				return errors.New("mekugi compaction trigger must end a V2 request")
			}
		}
		if responseevents.StringField(item, "type") != "additional_tools" {
			continue
		}
		var additionalTools []jsonv1.RawMessage
		if err := jsonv1.Unmarshal(item["tools"], &additionalTools); err != nil {
			return fmt.Errorf("decode compaction additional tools: %w", err)
		}
		if !v2 && len(additionalTools) != 0 {
			return errors.New("mekugi compaction request cannot expose tools")
		}
	}
	if v2 && triggers != 1 {
		return errors.New("mekugi V2 compaction requires one final trigger")
	}

	var toolChoice string
	if jsonv1.Unmarshal(request.fields["tool_choice"], &toolChoice) != nil || toolChoice != "auto" {
		return errors.New("mekugi compaction request requires automatic tool choice")
	}
	var parallelToolCalls bool
	if err := jsonv1.Unmarshal(request.fields["parallel_tool_calls"], &parallelToolCalls); err != nil || !v2 && parallelToolCalls {
		return errors.New("mekugi compaction request has incompatible parallel tool calls")
	}
	return nil
}

// Prewarm shares model projection but cannot initialize execution, replay, or
// agent lifecycle state. Only the non-generating WebSocket path selects it.
func (p *mekugiProxy) prepareModelRequest(ctx context.Context, request *parsedResponsesRequest, sessionID, threadID string, metadata codexTurnMetadata, metadataValid, prewarm bool) (*mekugiResponseTransform, error) {
	if err := validateProviderContext(request.fields["input"]); err != nil {
		return nil, err
	}
	if metadataValid && metadata.RequestKind == responseevents.Compaction {
		if err := validateMekugiCompactionRequest(request, metadata); err != nil {
			return nil, err
		}
		if err := p.observeCompactionInput(ctx, request, sessionID, threadID, metadata); err != nil {
			if _, incompatible := errors.AsType[*requestCompatibilityError](err); incompatible {
				return nil, err
			}
			p.notice(sessionID, threadID, "compaction_observation", "Completed host results could not be retained during compaction. Provider compaction is unchanged; change evidence may be incomplete.")
		}
		return nil, nil
	}
	if p == nil {
		return nil, errors.New("mekugi response proxy is unavailable")
	}
	if !prewarm && strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("mekugi rewrite requires a valid session ID")
	}
	if !metadataValid || (!prewarm && metadata.RequestKind != responseevents.Turn) {
		return nil, errors.New("mekugi rewrite requires valid turn metadata")
	}
	p.execWindows.markBackground(threadID, metadata.TurnID)
	// Execution-free requests retain their native instructions, tools, and schema.
	if request.isExecutionFreeRequest() {
		if !prewarm && strings.TrimSpace(threadID) == "" {
			return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
		}
		return nil, nil
	}
	var hostParts map[string][]int
	if p.duplicateOutput && !prewarm {
		hostParts = duplicateOutputHostParts(request.fields["input"])
	}
	// Discover an execution owner before projecting tools: a native
	// handshake may not yet carry the instruction or tool catalog of a turn.
	if prewarm {
		tools := request.responseTools()
		execution, err := prepareStockExecution(request.fields, tools, p.registry.frontendGuidance)
		if err != nil {
			return nil, err
		}
		if execution == nil {
			return nil, nil
		}
	}
	if err := rewriteRequestInstructionConflicts(request); err != nil {
		return nil, err
	}

	recipient := metadata.AgentName
	if metadata.SubagentKind == "" {
		recipient = "/root"
	}
	if metadata.SubagentKind != "" && metadata.activityIdentityInvalid {
		recipient = ""
	}

	// Projection deduplication needs the original visible messages. Keep this
	// request-local result until replay validation succeeds and strips known copies.
	envelopes := prepareSubagentInputEnvelopes(request.fields, recipient)

	tools := request.responseTools()
	directory, _ := usableRoutingDirectory(metadata.Directories)
	originalTools, originalToolsPresent := request.fields["tools"]
	originalTools = bytes.Clone(originalTools)
	originalToolChoice, originalToolChoicePresent := request.fields["tool_choice"]
	originalToolChoice = bytes.Clone(originalToolChoice)
	execution, err := prepareStockExecution(request.fields, tools, p.registry.frontendGuidance)
	if err != nil {
		return nil, err
	}
	codeModeToolName := ""
	if execution != nil {
		codeModeToolName = execution.name
	}
	if prewarm {
		return nil, nil
	}
	if strings.TrimSpace(threadID) == "" {
		return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
	}
	historySessionID := directory + "\x00" + threadID
	if err := p.activateSession(historySessionID); err != nil {
		return nil, err
	}
	ctx, err = p.beginStorageSession(ctx, threadID, sessionID)
	if err != nil {
		p.deactivateSession(historySessionID)
		return nil, storageIOError(err)
	}
	ctx, err = p.replayStore.prepareHandleScope(ctx, metadata)
	if err != nil {
		p.deactivateSession(historySessionID)
		return nil, err
	}
	visible, err := p.reconcileVisibleInput(ctx, request, directory, historySessionID)
	if err != nil {
		p.deactivateSession(historySessionID)
		return nil, err
	}
	// Only accepted requests may change retained ancestry or collect activity.
	activityThreadID := ""
	if metadata.activityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != threadID {
		p.activity.invalidate(threadID)
	} else {
		name := metadata.AgentName
		if name == "" && metadata.SubagentKind == "" {
			name = "/root"
		}
		if p.activity.observe(threadID, metadata.ParentThreadID, name, metadata.SubagentKind != "") {
			activityThreadID = threadID
		}
	}
	for _, reply := range envelopes.replies {
		thread := activityThreadID
		if recipient == "/root" {
			// Display directed replies under the proven sender when available.
			if child := p.activity.childThread(activityThreadID, reply.message.from); child != "" {
				thread = child
			}
		}
		p.activity.collectEvent(activityEvent{thread: thread, source: reply.source, kind: "reply", text: reply.message.text, message: &reply.message})
	}
	if metadata.SubagentKind == "thread_spawn" {
		// The collector deduplicates this source by stable child thread, including
		// across routing-session changes, and forwards it only to the observed root.
		p.activity.collectSubagentStart(activityThreadID, request, metadata.AgentName)
	}
	for _, final := range envelopes.finals {
		p.activity.markFinal(activityThreadID, final)
	}
	if activityThreadID != "" {
		model := request.model()
		p.activity.beginResponse(activityThreadID, isGrokModel(model) || isOpenCodeModel(model))
	}
	transform := &mekugiResponseTransform{
		ctx:              ctx,
		proxy:            p,
		sessionID:        sessionID,
		shellTurnID:      metadata.TurnID,
		shellThreadID:    threadID,
		model:            request.modelDescription(),
		historySessionID: historySessionID,
		visible:          visible,
		sessionActive:    true,
		threadID:         activityThreadID,

		originalTools:             originalTools,
		originalToolsPresent:      originalToolsPresent,
		originalToolChoice:        originalToolChoice,
		originalToolChoicePresent: originalToolChoicePresent,
		directory:                 directory,
		nativeExecCalls:           make(map[string]map[string]jsonv1.RawMessage),
		local:                     make(map[string]mekugiHistory),
		commentaryAuthor:          metadata.commentaryAuthor(),
		subagentTurn:              metadata.SubagentKind != "",
		activityResponding:        activityThreadID != "",
		usageTracker:              p.usage.observation(threadID, metadata.ThreadID, request.model(), usageServiceTier(request.fields["service_tier"])),
		codeModeToolName:          codeModeToolName,
		sessionShell:              requestSessionShell(request.fields["input"]),
	}
	transform.usageTracker.reasoning = request.reasoningEffort()
	p.observeCodeModeFailures(activityThreadID, codeModeToolName, request, visible)
	// Continuation advice is appended to the output the model will see.
	projectExecutionContinuations(request, tools, codeModeToolName, visible)
	if err := p.projectApprovalFeedback(request, threadID, metadata.TurnID); err != nil {
		transform.Close()
		return nil, err
	}
	if p.duplicateOutput {
		before := request.fields["input"]
		summary := projectDuplicateOutputs(request, hostParts)
		capturer.ObserveOutputDeduplication(ctx, before, request.fields["input"], summary.occurrences)
		var originalItems []map[string]jsonv1.RawMessage
		_ = json.Unmarshal(request.originalFields["input"], &originalItems)
		sources := make(map[string]string)
		for _, item := range originalItems {
			if responseevents.StringField(item, "type") == "custom_tool_call" {
				sources[responseevents.StringField(item, "call_id")] = responseevents.StringField(item, "input")
			}
		}
		for call, label := range summary.calls {
			target := call
			source := sources[call]
			if history, ok := visible[call]; ok {
				source = history.CarrierPayload
				if source == "" {
					source = history.Script
				}
			}
			if commands := p.nativeTrace.readCell(activityThreadID, call, source).execCommands(); len(commands) > 0 {
				target = commands[len(commands)-1]
			}
			p.activity.collectEvent(activityEvent{thread: activityThreadID, source: "output-dedupe\x00" + call, kind: "output_dedupe", callID: target, text: label})
		}
	}
	return transform, nil
}

// Publish the native denial and its feedback together. A provider continuation
// cannot observe the denial without the user reply already being available.
func (p *mekugiProxy) answerWithApprovalFeedback(thread, turn, text string, answer func() error) error {
	if p == nil {
		return errors.New("native denial feedback requires the provider proxy")
	}
	p.approvalMu.Lock()
	defer p.approvalMu.Unlock()
	if err := answer(); err != nil {
		return err
	}
	if p.approvalFeedback == nil {
		p.approvalFeedback = make(map[[2]string][]string)
	}
	key := [2]string{thread, turn}
	p.approvalFeedback[key] = append(p.approvalFeedback[key], text)
	return nil
}

func (p *mekugiProxy) clearApprovalFeedback(thread, turn string) {
	if p == nil {
		return
	}
	p.approvalMu.Lock()
	defer p.approvalMu.Unlock()
	delete(p.approvalFeedback, [2]string{thread, turn})
}

func (p *mekugiProxy) projectApprovalFeedback(request *parsedResponsesRequest, thread, turn string) error {
	p.approvalMu.Lock()
	defer p.approvalMu.Unlock()
	feedback := p.approvalFeedback[[2]string{thread, turn}]
	if len(feedback) == 0 {
		return nil
	}
	var input []jsonv1.RawMessage
	if err := json.Unmarshal(request.fields["input"], &input); err != nil {
		return fmt.Errorf("read input for denial feedback: %w", err)
	}
	input = append(input, responseevents.MustMarshalJSON(map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": strings.Join(feedback, "\n\n")}}}))
	request.setInput(responseevents.MustMarshalJSON(input))
	return nil
}
