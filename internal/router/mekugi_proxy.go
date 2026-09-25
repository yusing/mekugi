package router

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

const (
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
	registry           *toolRegistry
	titles             *sessionTitleCache
	memoryCommentary   map[string]map[string]struct{}
	commentary         *commentaryBroker
	commentaryEndpoint string
	journals           *journalStore
	usage              *threadUsage
	metricMu           sync.Mutex
	metricPaths        map[string]string
	autoLiveDiff       *autoLiveDiff
	activity           *subagentActivity
	skillsManager      bool
	execWindows        *execWindowRegistry
	execLastSeen       *execLastSeen
	nativeTrace        *nativeToolTrace
	exploreFilter      *exploreFilter

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

// Token metrics are a best-effort local artifact, never a response dependency.
// The transport thread is the stable Codex session identity across router restarts.
func (p *mekugiProxy) writeTokenMetrics(thread string, report tokenUsageReport) {
	if p == nil || thread == "" {
		return
	}
	digest := sha256.Sum256([]byte(thread))
	path := filepath.Join(os.TempDir(), "mekugi-token-metrics-"+hex.EncodeToString(digest[:])+".md")
	p.metricMu.Lock()
	defer p.metricMu.Unlock()
	file, err := os.CreateTemp(os.TempDir(), ".mekugi-token-metrics-*.md")
	if err != nil {
		p.notice(thread, "token_metrics", "Token metrics could not be saved: "+err.Error())
		return
	}
	defer os.Remove(file.Name())
	content := formatTokenUsageReport(report) + "\n"
	_, writeErr := file.WriteString(content)
	err = errors.Join(writeErr, file.Chmod(0o600), file.Close())
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		p.notice(thread, "token_metrics", "Token metrics could not be saved: "+err.Error())
		return
	}
	if p.metricPaths == nil {
		p.metricPaths = make(map[string]string)
	}
	p.metricPaths[thread] = path
}

func (p *mekugiProxy) tokenMetricPaths() []string {
	p.metricMu.Lock()
	defer p.metricMu.Unlock()
	paths := make([]string, 0, len(p.metricPaths))
	for _, path := range p.metricPaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func newMekugiProxy(registry *toolRegistry, titleCaches ...*sessionTitleCache) *mekugiProxy {
	if registry == nil {
		return nil
	}
	titles := newSessionTitleCache()
	if len(titleCaches) != 0 && titleCaches[0] != nil {
		titles = titleCaches[0]
	}
	activity := newSubagentActivity()
	broker := newCommentaryBroker()
	broker.activity = activity
	proxy := &mekugiProxy{
		registry:       registry,
		titles:         titles,
		commentary:     broker,
		journals:       newJournalStore(),
		usage:          newThreadUsage(),
		activity:       activity,
		execWindows:    &execWindowRegistry{},
		execLastSeen:   &execLastSeen{},
		sessions:       make(map[string]*mekugiHistorySession),
		activeSessions: make(map[string]int),
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
	activityResponding      bool // Counted in the agents-pane roster until Close.
}

type mekugiJournalState struct {
	journalDeliveries       map[string]journalDelivery
	liveDiffCompletionReady bool
	journalQuietFile        os.FileInfo
	journalLiveBytes        int
	journalNewCount         int
	journalChildResult      string
	journalFlushedCount     int
	journalDeliveryRelease  func()
	journalQuestion         string // Request-local user text for the natural final answer.
	journalAvailable        bool
	journalActive           bool
	journalPending          map[string]bool
	journalCalls            map[string]map[string]json.RawMessage
	journalResults          []map[string]json.RawMessage
	journalClientOutput     []map[string]json.RawMessage
	journalProviderOutput   []map[string]json.RawMessage
	journalClientCalls      bool
	journalDeferredFinish   map[string]bool
	journalTerminal         bool
	journalContinue         bool
	journalFinishRequested  bool
	journalNaturalAnswerIDs map[string]bool
	journalNaturalFinalSeen bool
}

type mekugiDeliveryState struct {
	usageTracker         *threadUsageObservation
	usageMentorRevisions map[string]uint64
	finalAnswer          finalAnswerStream
	usageObserved        bool
}

type mekugiResponseTransform struct {
	featureTrace     featureUsageTrace
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

	mekugiActivityState

	originalTools             json.RawMessage
	originalToolsPresent      bool
	originalToolChoice        json.RawMessage
	originalToolChoicePresent bool
	directory                 string
	mekugiTranslationState

	commentaryAuthor string
	commentaryTools  commentaryToolCatalog

	mekugiCommentaryState
	mekugiJournalState
	mekugiDeliveryState

	codeModeToolName string
	nativeTools      bool
	// sessionShell runs stock commands that name no shell of their own.
	sessionShell string
}

func (t *mekugiResponseTransform) Close() {
	if t == nil {
		return
	}
	for itemID := range t.previews {
		t.endPreview(itemID)
	}
	if t.activityResponding {
		t.activityResponding = false
		if !t.usageObserved {
			t.proxy.activity.markUsageGap(t.threadID)
		}
		t.proxy.activity.endResponse(t.threadID)
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
	if t.usageObserved {
		return
	}
	t.usageTracker.observe(counts)
	t.usageObserved = true
	if t.proxy != nil && t.threadID != "" {
		report, ok := t.proxy.usage.snapshot(t.threadID)
		tier := cmp.Or(counts.ServiceTier, t.usageTracker.serviceTier)
		fallback := rosterTokenCost(t.usageTracker.model, tier, counts, t.usageTracker.openCodePrice)
		t.proxy.activity.syncUsage(t.threadID, counts, report, ok, fallback)
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
	p.execWindows.markBackground(threadID, metadata.TurnID)
	// Execution-free requests retain their native instructions, tools, and schema.
	if request.isExecutionFreeRequest() {
		if !prewarm && strings.TrimSpace(threadID) == "" {
			return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
		}
		return nil, nil
	}
	// Discover an execution owner before projecting tools: a native
	// handshake may not yet carry the instruction or tool catalog of a turn.
	if prewarm {
		tools := request.responseTools()
		execution, err := prepareStockExecution(request.fields, tools, p.registry.frontendGuidance)
		if err != nil {
			return nil, err
		}
		if execution.codeMode == nil && !execution.native {
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
	subagentDeferred := envelopes.commentary

	tools := request.responseTools()
	directory, _ := usableRoutingDirectory(metadata.Directories)
	originalTools, originalToolsPresent := request.fields["tools"]
	originalTools = bytes.Clone(originalTools)
	originalToolChoice, originalToolChoicePresent := request.fields["tool_choice"]
	originalToolChoice = bytes.Clone(originalToolChoice)
	if err := stripStockPlanTools(request.fields, tools); err != nil {
		return nil, err
	}
	execution, err := prepareStockExecution(request.fields, tools, p.registry.frontendGuidance)
	if err != nil {
		return nil, err
	}
	// Collaboration-only turns still need journal and commentary projection;
	// absence of execution tools is not an incompatible Codex catalog.
	codeModeToolName := ""
	if execution.codeMode != nil {
		codeModeToolName = execution.codeMode.name
	}
	if err := exposeJournalTool(request.fields, tools, codeModeToolName != ""); err != nil {
		return nil, err
	}
	if p.registry.diagnoseEnabled {
		if err := exposeReportIssueTool(request.fields, tools); err != nil {
			return nil, err
		}
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
	for _, final := range envelopes.finals {
		p.activity.markFinal(activityThreadID, final)
	}
	if recipient == "/root" && activityThreadID != "" {
		subagentDeferred = p.activity.divertRootReplies(activityThreadID, subagentDeferred, envelopes.senders)
	}
	if activityThreadID != "" {
		p.activity.beginResponse(activityThreadID)
	}
	deferredCommentary := p.drainCommentarySession(historySessionID, threadID)
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

		activityStarted:           time.Now(),
		originalTools:             originalTools,
		originalToolsPresent:      originalToolsPresent,
		originalToolChoice:        originalToolChoice,
		originalToolChoicePresent: originalToolChoicePresent,
		directory:                 directory,
		pending:                   make(map[string]mekugiPendingCall),
		nativeExecCalls:           make(map[string]map[string]json.RawMessage),
		local:                     make(map[string]mekugiHistory),
		commentaryAuthor:          metadata.commentaryAuthor(),
		commentaryTools:           commentaryTools,
		subagentDeferred:          subagentDeferred,
		subagentResponses:         subagentDeferred,
		subagentTurn:              metadata.SubagentKind != "",
		activityResponding:        activityThreadID != "",
		deferredCommentary:        deferredCommentary,
		commentaryEmitted:         make(map[string]struct{}),
		usageTracker:              p.usage.observationForTurn(threadID, metadata.ThreadID, metadata.TurnID, request.model(), usageServiceTier(request.fields["service_tier"])),
		codeModeToolName:          codeModeToolName,
		nativeTools:               execution.native,
		sessionShell:              requestSessionShell(request.fields["input"]),
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
	if activityThreadID != "" {
		for _, owner := range []string{threadID, metadata.ParentThreadID} {
			if owner == "" {
				continue
			}
			// Read durable role evidence so resumed children do not depend on a live parent.
			if err := p.journals.transaction(ctx, p.replayStore, directory, owner, func(j *threadJournal, exists bool) error {
				if exists {
					p.activity.syncPaneRoles(owner, j.SpawnRoles)
				}
				return errJournalUnchanged
			}); err != nil {
				log.Printf("read roster roles: %v", err)
			}
		}
	}
	transform.usageTracker.reasoning = request.reasoningEffort()
	transform.journalAvailable = true
	transform.journalQuestion = journalQuestionFromInput(request.fields["input"], metadata.commentaryAuthor())
	transform.journalActive = true
	transform.journalPending = make(map[string]bool)
	transform.journalCalls = make(map[string]map[string]json.RawMessage)
	// Continuation advice is appended to the output the model will see.
	p.exploreFilter.project(p.exploreContext(ctx, threadID, activityThreadID), request, visible, directory, transform.sessionShell, recipient, p.replayStore)
	projectExecutionContinuations(request, tools, codeModeToolName, visible)
	if transform.subagentTurn {
		transform.prepareShellActivity(request.fields["input"])
		transform.collectShellExits(request.fields["input"])
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
