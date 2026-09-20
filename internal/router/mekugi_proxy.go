package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	codexinstructions "github.com/yusing/mekugi/contrib/codex"
	responseevents "github.com/yusing/mekugi/internal/responses"
)

const (
	mekugiToolName     = "hpatch"
	applyPatchToolName = "apply_patch"

	maxMekugiScriptBytes = 1 << 20

	maxMekugiPendingCalls = 128
)

func nativeSpawnRoles(raw json.RawMessage, parentAuthor string, ignoredCallIDs ...map[string]bool) map[string]journalSpawnRole {
	var input []responsesItem
	if json.Unmarshal(raw, &input) != nil {
		return nil
	}
	type spawnCall struct {
		role       string
		conflicted bool
	}
	calls := make(map[string]spawnCall)
	ignored := map[string]bool(nil)
	if len(ignoredCallIDs) != 0 {
		ignored = ignoredCallIDs[0]
	}
	for _, item := range input {
		if item.Type != "function_call" || item.Namespace != subagentBridgeNamespace && item.Namespace != "collaboration" || item.Name != "spawn_agent" || item.CallID == "" || ignored[item.CallID] || item.Arguments == nil {
			continue
		}
		var arguments struct {
			AgentType string `json:"agent_type"`
		}
		if json.Unmarshal([]byte(*item.Arguments), &arguments) != nil || strings.TrimSpace(arguments.AgentType) == "" {
			continue
		}
		call, found := calls[item.CallID]
		if found && call.role != arguments.AgentType {
			call.conflicted = true
		} else if !found {
			call.role = arguments.AgentType
		}
		calls[item.CallID] = call
	}
	roles := make(map[string]journalSpawnRole)
	for _, item := range input {
		if item.Type != "function_call_output" || item.CallID == "" || item.Output == nil {
			continue
		}
		call, ok := calls[item.CallID]
		if !ok {
			continue
		}
		output, ok := decodeJSONString(item.Output)
		if !ok {
			continue
		}
		var result struct {
			TaskName string `json:"task_name"`
		}
		if json.Unmarshal([]byte(output), &result) != nil || !directChildAuthor(parentAuthor, result.TaskName) {
			continue
		}
		evidence := journalSpawnRole{Role: call.role, Conflicted: call.conflicted}
		if current, found := roles[result.TaskName]; found && (current.Conflicted || current.Role != evidence.Role) {
			evidence = journalSpawnRole{Conflicted: true}
		}
		roles[result.TaskName] = evidence
	}
	return roles
}

func nativeSpawnCallIDs(raw json.RawMessage) map[string]bool {
	var input []responsesItem
	if json.Unmarshal(raw, &input) != nil {
		return nil
	}
	callIDs := make(map[string]bool)
	for _, item := range input {
		if item.Type == "function_call" && (item.Namespace == subagentBridgeNamespace || item.Namespace == "collaboration") && item.Name == "spawn_agent" && item.CallID != "" {
			callIDs[item.CallID] = true
		}
	}
	return callIDs
}

func directChildAuthor(parent, child string) bool {
	prefix := strings.TrimSuffix(parent, "/") + "/"
	remainder, ok := strings.CutPrefix(child, prefix)
	return ok && remainder != "" && !strings.Contains(remainder, "/")
}

func mekugiDataDirectory() (string, error) {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determine mekugi data directory: %w", err)
	}
	return filepath.Join(configDirectory, "mekugi"), nil
}

type mekugiProxy struct {
	registry               *toolRegistry
	customizedInstructions bool
	compactModelProtocol   bool
	shellDirectory         string
	titles                 *sessionTitleCache
	shellSessions          map[string]*shellSession
	shellParent            *os.Root
	memoryCommentary       map[string]map[string]struct{}
	commentary             *commentaryBroker
	commentaryEndpoint     string
	journals               *journalStore
	usage                  *threadUsage
	autoLiveDiff           *autoLiveDiff
	activity               *subagentActivity

	mu              sync.RWMutex
	replayStore     *mekugiReplayStore
	sessions        map[string]*mekugiHistorySession
	noticeSink      func(string, string, string)
	storageTurns    map[string]uint64
	storageSequence uint64
	storageLeases   map[string]func()
	activeSessions  map[string]int
	historyBytes    int
	sessionSequence uint64
	closed          bool
}

func newMekugiProxy(registry *toolRegistry, customizedInstructions, compactModelProtocol bool, titleCaches ...*sessionTitleCache) *mekugiProxy {
	if registry == nil {
		return nil
	}
	titles := newSessionTitleCache()
	if len(titleCaches) != 0 && titleCaches[0] != nil {
		titles = titleCaches[0]
	}
	directory := registry.runtimeDirectory
	activity := newSubagentActivity()
	broker := newCommentaryBroker()
	broker.activity = activity
	proxy := &mekugiProxy{
		registry:               registry,
		customizedInstructions: customizedInstructions,
		compactModelProtocol:   compactModelProtocol,
		shellDirectory:         directory,
		titles:                 titles,
		shellSessions:          make(map[string]*shellSession),
		commentary:             broker,
		journals:               newJournalStore(),
		usage:                  newThreadUsage(),
		activity:               activity,
		sessions:               make(map[string]*mekugiHistorySession),
		activeSessions:         make(map[string]int),
	}
	broker.editPublisher = func(ctx context.Context, workspace, thread, callID string) error {
		if proxy.replayStore == nil {
			return errors.New("edit storage unavailable")
		}
		return proxy.replayStore.publishEditReceipt(ctx, workspace, thread, callID, activity)
	}
	broker.previewPublisher = func(workspace, thread, author string, preview liveDiffPreview) {
		auto := proxy.autoLiveDiff
		if auto == nil || !auto.enabled.Load() {
			return
		}
		auto.requestLaunch(workspace, thread)
		if author == "" {
			author = "/root"
		}
		preview.ID = "prewrite:" + thread + ":" + preview.ID
		preview.Workspace, preview.Thread, preview.Caller = workspace, thread, author
		auto.events.publishPreview(preview, false)
	}
	broker.notice = func(category, message string) { proxy.notice("", category, message) }
	activity.notice = broker.notice
	broker.journalPublisher = func(ctx context.Context, session, thread, receipt string, mutations []journalMutation) ([]string, error) {
		workspace, _, ok := strings.Cut(session, "\x00")
		if !ok {
			return nil, errors.New("journal workspace is unavailable")
		}
		return proxy.journals.apply(ctx, proxy.replayStore, workspace, thread, "runtime:"+receipt, mutations)
	}
	broker.journalLister = func(ctx context.Context, session, thread, agent string) ([]journalItem, error) {
		workspace, _, ok := strings.Cut(session, "\x00")
		if !ok {
			return nil, errors.New("journal workspace is unavailable")
		}
		if agent != "" {
			return proxy.journals.listAgent(ctx, proxy.replayStore, workspace, thread, agent)
		}
		return proxy.journals.list(ctx, proxy.replayStore, workspace, thread)
	}
	return proxy
}

func (p *mekugiProxy) notice(session, category, message string) {
	if p != nil && p.noticeSink != nil {
		p.noticeSink(session, category, message)
	}
}

func (p *mekugiProxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closed = true
	defer p.mu.Unlock()
	var cleanupErr error
	for _, session := range p.shellSessions {
		cleanupErr = errors.Join(cleanupErr, session.close())
	}
	clear(p.shellSessions)
	if p.shellParent != nil {
		cleanupErr = errors.Join(cleanupErr, p.shellParent.Close())
		p.shellParent = nil
	}
	for _, release := range p.storageLeases {
		release()
	}
	clear(p.storageLeases)
	clear(p.sessions)
	clear(p.activeSessions)
	p.historyBytes = 0
	if p.commentary != nil {
		p.commentary.close()
	}
	p.usage.close()
	p.activity.close()
	return cleanupErr
}

type mekugiPendingCall struct {
	callID     string
	toolName   string
	structured bool

	added         []byte
	argumentsDone []byte
}

type mekugiActivityState struct {
	activityStarted        time.Time
	activityBytes          int
	activityMessages       []map[string]json.RawMessage
	activityShellSessions  map[string]string
	activityCellOperations map[string]string
}

type mekugiTranslationState struct {
	pending         map[string]mekugiPendingCall
	previews        map[string]*liveDiffPreviewWorker
	nativeExecCalls map[string]map[string]json.RawMessage
	local           map[string]mekugiHistory

	// localSequence orders the calls translated during this turn, so a
	// recovery resolves against the newest rejection rather than an arbitrary
	// map entry. Retained order is reassigned when the turn commits.
	localSequence    uint64
	storageIdle      bool
	historyCommitted bool
}

type mekugiCommentaryState struct {
	commentarySubscriptions []commentarySubscription
	deferredCommentary      []publishedCommentary
	commentaryEmitted       map[string]struct{}
	subagentDeferred        []map[string]json.RawMessage
	subagentResponses       []map[string]json.RawMessage
	subagentTurn            bool
}

type mekugiJournalState struct {
	journalDeliveries      map[string]journalDelivery
	liveDiffUsageID        string
	journalUsageID         string
	journalQuietFile       os.FileInfo
	journalLiveBytes       int
	journalNewCount        int
	journalChildResult     string
	journalFlushedCount    int
	journalDeliveryRelease func()
	journalQuestion        string // Request-local user text for answer-marked journal mutations.
	journalAvailable       bool
	journalActive          bool
	journalPending         map[string]bool
	journalCalls           map[string]map[string]json.RawMessage
	journalResults         []map[string]json.RawMessage
	journalClientOutput    []map[string]json.RawMessage
	journalProviderOutput  []map[string]json.RawMessage
	journalClientCalls     bool
	journalTerminal        bool
	journalContinue        bool
	shellFinishRequested   bool
	journalFinishRequested bool
}

type mekugiDeliveryState struct {
	usageTracker  *threadUsageObservation
	finalAnswer   finalAnswerStream
	usageObserved bool
}

type mekugiResponseTransform struct {
	featureTrace     featureUsageTrace
	ctx              context.Context
	proxy            *mekugiProxy
	sessionID        string
	shellTurnID      string
	shellThreadID    string // Runtime identity remains available when activity attribution is invalid.
	shellDirectory   string
	model            string
	visible          map[string]mekugiHistory
	historySessionID string
	sessionActive    bool
	threadID         string

	mekugiActivityState

	originalTools             json.RawMessage
	originalToolsPresent      bool
	originalToolChoice        json.RawMessage
	originalToolChoicePresent bool
	directory                 string
	carriers                  codeModeCarrierCatalog

	mekugiTranslationState

	waitPolicies waitPolicies

	commentaryAuthor string
	commentaryTools  commentaryToolCatalog

	mekugiCommentaryState
	mekugiJournalState
	mekugiDeliveryState

	codeModeToolName string
	nativeTools      bool
}

func (t *mekugiResponseTransform) Close() {
	if t == nil {
		return
	}
	for itemID := range t.previews {
		t.endPreview(itemID)
	}
	t.ReleaseDelivery()
	t.releaseCommentarySubscriptions()
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
	t.usageTracker.observe(counts)
	t.usageObserved = true
}

func validateMekugiCompactionRequest(request *parsedResponsesRequest, metadata codexTurnMetadata) error {
	var compaction struct {
		Trigger        string `json:"trigger"`
		Reason         string `json:"reason"`
		Implementation string `json:"implementation"`
		Phase          string `json:"phase"`
		Strategy       string `json:"strategy"`
	}
	if err := json.Unmarshal(metadata.Compaction, &compaction); err != nil || slices.ContainsFunc(
		[]string{compaction.Trigger, compaction.Reason, compaction.Implementation, compaction.Phase, compaction.Strategy},
		func(value string) bool { return strings.TrimSpace(value) == "" },
	) {
		return errors.New("mekugi rewrite requires valid compaction metadata")
	}
	if !request.streamResponse {
		return errors.New("mekugi compaction bypass requires a streaming request")
	}

	var tools []json.RawMessage
	if rawTools, exists := request.fields["tools"]; exists {
		if err := json.Unmarshal(rawTools, &tools); err != nil {
			return fmt.Errorf("decode compaction tools: %w", err)
		}
	}
	if len(tools) != 0 {
		return errors.New("mekugi compaction request cannot expose tools")
	}

	var items []json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		return fmt.Errorf("decode compaction input: %w", err)
	}
	if len(items) == 0 {
		return errors.New("mekugi compaction request requires nonempty input")
	}
	for _, rawItem := range items {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(rawItem, &item); err != nil || item == nil {
			return errors.New("mekugi compaction request contains a malformed input item")
		}
		if jsonString(item, "type") != "additional_tools" {
			continue
		}
		var additionalTools []json.RawMessage
		if err := json.Unmarshal(item["tools"], &additionalTools); err != nil {
			return fmt.Errorf("decode compaction additional tools: %w", err)
		}
		if len(additionalTools) != 0 {
			return errors.New("mekugi compaction request cannot expose tools")
		}
	}

	var toolChoice string
	if json.Unmarshal(request.fields["tool_choice"], &toolChoice) != nil || toolChoice != "auto" {
		return errors.New("mekugi compaction request requires automatic tool choice")
	}
	var parallelToolCalls bool
	if err := json.Unmarshal(request.fields["parallel_tool_calls"], &parallelToolCalls); err != nil || parallelToolCalls {
		return errors.New("mekugi compaction request requires disabled parallel tool calls")
	}
	return nil
}

func (p *mekugiProxy) prepareRequest(ctx context.Context, request *parsedResponsesRequest, sessionID, threadID string, metadata codexTurnMetadata, metadataValid bool) (*mekugiResponseTransform, error) {
	return p.prepareModelRequest(ctx, request, sessionID, threadID, metadata, metadataValid, false)
}

// Prewarm shares model projection but cannot initialize execution, replay, or
// agent lifecycle state. Only the non-generating WebSocket path selects it.
func (p *mekugiProxy) prepareModelRequest(ctx context.Context, request *parsedResponsesRequest, sessionID, threadID string, metadata codexTurnMetadata, metadataValid, prewarm bool) (*mekugiResponseTransform, error) {
	if p != nil {
		request.filterInput(p.activity.stripInput)
	}
	if metadataValid && metadata.RequestKind == responseevents.Compaction {
		if err := validateMekugiCompactionRequest(request, metadata); err != nil {
			return nil, err
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
	// Execution-free requests retain their native instructions, tools, and schema.
	if request.isExecutionFreeRequest() {
		if !prewarm && strings.TrimSpace(threadID) == "" {
			return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
		}
		return nil, nil
	}
	// Discover an execution owner before rewriting instructions: a native
	// handshake may not yet carry the instruction or tool catalog of a turn.
	if prewarm {
		tools := request.responseTools()
		if tools.top.err != nil {
			return nil, incompatibleRequest("invalid_tool_catalog", tools.top.err.Error())
		}
		owner, err := findCodeModeApplyPatch(tools, nil)
		if err != nil {
			return nil, err
		}
		native := slices.ContainsFunc(tools.top.tools, func(tool *responsesToolDefinition) bool {
			return tool.Name == applyPatchToolName || tool.Name == nativeExecCommandToolName
		})
		if owner == nil && !native {
			return nil, nil
		}
	}

	modelInstructions := codexinstructions.InstructionsForModel(request.model(), p.compactModelProtocol)
	if err := rewriteReceivedModelInstructions(ctx, request, p.customizedInstructions, modelInstructions); err != nil {
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
	subagentDeferred := prepareSubagentInputCommentary(request.fields, recipient)

	tools := request.responseTools()
	directory, _ := usableRoutingDirectory(metadata.Directories)
	originalTools, originalToolsPresent := request.fields["tools"]
	originalTools = bytes.Clone(originalTools)
	originalToolChoice, originalToolChoicePresent := request.fields["tool_choice"]
	originalToolChoice = bytes.Clone(originalToolChoice)
	if err := stripStockPlanTools(request.fields, tools); err != nil {
		return nil, err
	}
	carriers, err := buildCodeModeCarrierCatalog(tools, p.registry)
	if err != nil {
		return nil, incompatibleRequest("invalid_tool_catalog", err.Error()+". Check the Codex tool catalog.")
	}
	installedTools, err := p.registry.specifications()
	if err != nil {
		return nil, err
	}
	codeModeToolName, replaced, err := replaceCodeModeTools(request.fields, tools, installedTools)
	if err != nil {
		return nil, err
	}
	nativeTools := false
	if !replaced {
		codeModeToolName, replaced, err = replaceNativeTools(request.fields, tools, installedTools)
		if err != nil {
			return nil, err
		}
		nativeTools = replaced
	}
	if !replaced {
		return nil, incompatibleRequest("unsupported_tool_catalog", "This request exposes no supported editing and execution tools. Use a Codex session with apply_patch and exec_command, or the supported Code Mode exec tool.")
	}
	if err := exposeJournalTool(request.fields, tools); err != nil {
		return nil, err
	}
	var commentaryTools commentaryToolCatalog
	commentaryTools, err = prepareCommentaryTools(request.fields, tools)
	if err != nil {
		return nil, err
	}

	if prewarm {
		return nil, nil
	}
	if strings.TrimSpace(threadID) == "" {
		return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
	}
	shellDirectory, err := p.storeShellRuntime(threadID)
	if err != nil {
		return nil, fmt.Errorf("store shell runtime: %w", err)
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
	p.prepareShellCommentary(threadID, historySessionID, metadata.commentaryAuthor())
	visible, err := p.reconcileVisibleInput(ctx, request, directory, historySessionID)
	if err != nil {
		p.deactivateSession(historySessionID)
		return nil, err
	}
	if err := restoreJournalCalls(request, visible); err != nil {
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
	for _, message := range subagentDeferred {
		var content []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(message["content"], &content) == nil && len(content) == 1 {
			p.activity.collect(activityThreadID, jsonString(message, "id"), "reply", content[0].Text)
		}
	}
	if metadata.SubagentKind == "thread_spawn" {
		// The collector deduplicates this source by stable child thread, including
		// across routing-session changes, and forwards it only to the observed root.
		p.activity.collect(activityThreadID, "subagent-start\x00"+activityThreadID, "start", subagentStartCommentary(request, metadata.AgentName))
	}
	deferredCommentary := p.drainCommentarySession(historySessionID, threadID)
	transform := &mekugiResponseTransform{
		ctx:              ctx,
		proxy:            p,
		sessionID:        sessionID,
		shellTurnID:      metadata.TurnID,
		shellThreadID:    threadID,
		shellDirectory:   shellDirectory,
		model:            request.modelDescription(),
		historySessionID: historySessionID,
		visible:          visible,
		sessionActive:    true,
		threadID:         activityThreadID,

		activityStarted:           time.Now(),
		originalTools:             originalTools,
		originalToolsPresent:      originalToolsPresent,
		originalToolChoice:        originalToolChoice,
		originalToolChoicePresent: originalToolChoicePresent,
		directory:                 directory,
		carriers:                  carriers,
		pending:                   make(map[string]mekugiPendingCall),
		nativeExecCalls:           make(map[string]map[string]json.RawMessage),
		local:                     make(map[string]mekugiHistory),
		commentaryAuthor:          metadata.commentaryAuthor(),
		commentaryTools:           commentaryTools,
		waitPolicies:              collectWaitPolicies(tools, codeModeToolName),
		subagentDeferred:          subagentDeferred,
		subagentResponses:         subagentDeferred,
		subagentTurn:              metadata.SubagentKind != "",
		deferredCommentary:        deferredCommentary,
		commentaryEmitted:         make(map[string]struct{}),
		usageTracker:              p.usage.observation(threadID, metadata.ThreadID, request.model(), usageServiceTier(request.fields["service_tier"])),
		codeModeToolName:          codeModeToolName,
		nativeTools:               nativeTools,
	}
	author := metadata.AgentName
	if metadata.SubagentKind == "" {
		author = "/root"
	}
	fork := metadata.ForkedFromThreadID
	if metadata.SubagentKind != "" {
		fork = ""
	}
	if err := p.journals.initialize(ctx, p.replayStore, directory, threadID, author, fork); err != nil {
		transform.Close()
		return nil, fmt.Errorf("initialize journal: %w", err)
	}
	if err := p.journals.bindIdentity(ctx, p.replayStore, directory, threadID, metadata.ParentThreadID, author, activityThreadID != ""); err != nil {
		transform.Close()
		return nil, err
	}
	spawnBaseline, err := p.journals.forkSpawnBaseline(ctx, p.replayStore, directory, threadID, fork != "", nativeSpawnCallIDs(request.fields["input"]))
	if err != nil {
		transform.Close()
		return nil, err
	}
	if err := p.journals.bindSpawnRoles(ctx, p.replayStore, directory, threadID, nativeSpawnRoles(request.fields["input"], author, spawnBaseline)); err != nil {
		transform.Close()
		return nil, err
	}
	transform.journalAvailable = true
	transform.journalQuestion = journalQuestionFromInput(request.fields["input"], metadata.commentaryAuthor())
	transform.journalActive = true
	transform.journalPending = make(map[string]bool)
	transform.journalCalls = make(map[string]map[string]json.RawMessage)
	if transform.journalAvailable {
		transform.shellFinishRequested, err = transform.shellJournalFinished(request.fields["input"])
		if err != nil {
			transform.Close()
			return nil, err
		}
		transform.journalFinishRequested = transform.shellFinishRequested
	}
	if err := p.replayStore.cleanupSessions(ctx); err != nil {
		transform.Close()
		return nil, err
	}
	projectExecutionContinuations(request, tools, codeModeToolName, visible)
	if transform.subagentTurn {
		transform.prepareShellActivity(request.fields["input"])
	}
	return transform, nil
}

func mustMarshalJSON(value any) json.RawMessage {
	encoded, err := marshalProtocolJSON(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func jsonString(object map[string]json.RawMessage, name string) string {
	var value string
	_ = json.Unmarshal(object[name], &value)
	return value
}
