package router

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/capturer"
	codexinstructions "github.com/yusing/mekugi/contrib/codex"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/shellsyntax"
)

const (
	mekugiToolName     = "hpatch"
	applyPatchToolName = "apply_patch"

	maxMekugiScriptBytes = 1 << 20
	maxMekugiPatchBytes  = 16 << 20

	maxMekugiPendingCalls = 128

	shellArtifactPrefix = "@shell/"
)

var (
	errMekugiCapacity = errors.New("mekugi proxy capacity exceeded")
	shellArtifactTTL  = time.Hour
)

type mekugiTranslationResult struct {
	reviewFiles []mekugi.ReviewFile
	patch       []byte
	report      string
	diagnostic  string
	rejections  []mekugi.HostRejection
	failures    []mekugi.HostFailure
	change      mekugi.HostChange
	aliases     []mekugi.TargetAlias
}

type mekugiTranslator interface {
	Translate(ctx context.Context, directory, script string) (mekugiTranslationResult, error)
	ToolDescription() string
}

type mekugiApplier interface {
	Apply(ctx context.Context, root *os.Root, script string) (mekugiTranslationResult, error)
}

type inProcessMekugiTranslator struct {
	dataDirectory string
}

func mekugiDataDirectory() (string, error) {
	configDirectory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("determine mekugi data directory: %w", err)
	}
	return filepath.Join(configDirectory, "mekugi"), nil
}

func newInProcessMekugiTranslator(dataDirectory string) mekugiTranslator {
	return inProcessMekugiTranslator{dataDirectory: dataDirectory}
}

func (inProcessMekugiTranslator) ToolDescription() string {
	return mekugi.ToolDescription()
}

func (t inProcessMekugiTranslator) Translate(ctx context.Context, directory, script string) (mekugiTranslationResult, error) {
	translated, err := mekugi.TranslateForHostAt(ctx, directory, script, t.dataDirectory)
	if contextErr := ctx.Err(); contextErr != nil {
		return mekugiTranslationResult{}, contextErr
	}
	if len(translated.Patch) > maxMekugiPatchBytes {
		return mekugiTranslationResult{}, fmt.Errorf("%w: mekugi translation output exceeds its configured bound", errMekugiCapacity)
	}
	return mekugiTranslationResultOf(translated), err
}

func (t inProcessMekugiTranslator) Apply(ctx context.Context, root *os.Root, script string) (mekugiTranslationResult, error) {
	applied, err := mekugi.ApplyForHostRoot(ctx, root, script, t.dataDirectory)
	if contextErr := ctx.Err(); contextErr != nil {
		return mekugiTranslationResult{}, contextErr
	}
	return mekugiTranslationResultOf(applied), err
}

func (t inProcessMekugiTranslator) ReportOutcome(ctx context.Context, stage, outcome string) error {
	return mekugi.ReportHostOutcome(ctx, t.dataDirectory, stage, outcome)
}

func mekugiTranslationResultOf(translated mekugi.HostTranslation) mekugiTranslationResult {
	return mekugiTranslationResult{
		reviewFiles: translated.ReviewFiles,
		patch:       translated.Patch,
		report:      translated.Report,
		diagnostic:  translated.Diagnostic,
		rejections:  slices.Clone(translated.Rejections),
		failures:    slices.Clone(translated.Failures),
		change:      translated.Change,
		aliases:     slices.Clone(translated.TargetAliases),
	}
}

type mekugiProxy struct {
	translator             mekugiTranslator
	registry               *toolRegistry
	customizedInstructions bool
	compactModelProtocol   bool
	shellDirectory         string
	titles                 *sessionTitleCache
	shellSessions          map[string]*shellSession
	shellParent            *os.Root
	shellLeases            sync.WaitGroup
	memoryCommentary       map[string]map[string]struct{}
	commentary             *commentaryBroker
	commentaryEndpoint     string
	journals               *journalStore
	usage                  *threadUsage
	activity               *subagentActivity

	mu              sync.RWMutex
	replayStore     *mekugiReplayStore
	sessions        map[string]*mekugiHistorySession
	activeSessions  map[string]int
	historyBytes    int
	sessionSequence uint64
	closed          bool
}

func newMekugiProxy(translator mekugiTranslator, registry *toolRegistry, customizedInstructions, compactModelProtocol bool, titleCaches ...*sessionTitleCache) *mekugiProxy {
	if translator == nil || registry == nil {
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
		translator:             translator,
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
	broker.journalPublisher = func(ctx context.Context, session, thread, receipt string, mutations []journalMutation) ([]string, error) {
		workspace, _, ok := strings.Cut(session, "\x00")
		if !ok {
			return nil, errors.New("journal workspace is unavailable")
		}
		return proxy.journals.apply(ctx, proxy.replayStore, workspace, thread, "runtime:"+receipt, mutations)
	}
	return proxy
}

func (p *mekugiProxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	// New leases are rejected after closed is set. Existing operations finish
	// before shutdown removes retained files or closes their shared anchor.
	p.shellLeases.Wait()
	p.mu.Lock()
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

type mekugiResponseTransform struct {
	featureTrace           featureUsageTrace
	ctx                    context.Context
	proxy                  *mekugiProxy
	sessionID              string
	shellThreadID          string // Runtime identity remains available when activity attribution is invalid.
	shellDirectory         string
	model                  string
	visible                map[string]mekugiHistory
	historySessionID       string
	sessionActive          bool
	threadID               string
	activityStarted        time.Time
	activityBytes          int
	activityMessages       []map[string]json.RawMessage
	activityShellSessions  map[string]string
	activityCellOperations map[string]string

	originalTools             json.RawMessage
	originalToolsPresent      bool
	originalToolChoice        json.RawMessage
	originalToolChoicePresent bool
	pending                   map[string]mekugiPendingCall
	nativeExecCalls           map[string]map[string]json.RawMessage
	local                     map[string]mekugiHistory
	directory                 string
	carriers                  codeModeCarrierCatalog
	commentaryAuthor          string
	commentaryTools           commentaryToolCatalog
	commentarySubscriptions   []commentarySubscription
	deferredCommentary        []publishedCommentary
	commentaryEmitted         map[string]struct{}
	subagentDeferred          []map[string]json.RawMessage
	subagentResponses         []map[string]json.RawMessage
	subagentTurn              bool
	usageTracker              *threadUsageObservation
	journalDeliveries         map[string]journalDelivery
	journalUsageID            string
	journalQuietFile          os.FileInfo
	journalLiveBytes          int
	journalNewCount           int
	journalFlushedCount       int
	journalDeliveryRelease    func()
	journalQuestion           string // Request-local user text for answer-marked journal mutations.
	journalAvailable          bool
	journalActive             bool
	journalPending            map[string]bool
	journalCalls              map[string]map[string]json.RawMessage
	journalResults            []map[string]json.RawMessage
	journalClientOutput       []map[string]json.RawMessage
	journalProviderOutput     []map[string]json.RawMessage
	journalClientCalls        bool
	journalTerminal           bool
	journalContinue           bool
	journalFinishRequested    bool
	finalAnswer               finalAnswerStream
	usageObserved             bool

	codeModeToolName string
	nativeTools      bool

	// localSequence orders the calls translated during this turn, so a
	// recovery resolves against the newest rejection rather than an arbitrary
	// map entry. Retained order is reassigned when the turn commits.
	localSequence    uint64
	historyCommitted bool
}

func (t *mekugiResponseTransform) Close() {
	if t == nil {
		return
	}
	t.ReleaseDelivery()
	t.releaseCommentarySubscriptions()
	if t.sessionActive {
		t.proxy.deactivateSession(t.historySessionID)
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
	if p != nil {
		request.filterInput(p.activity.stripInput)
	}
	if metadataValid && metadata.RequestKind == "compaction" {
		if err := validateMekugiCompactionRequest(request, metadata); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if p == nil {
		return nil, errors.New("mekugi response proxy is unavailable")
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("mekugi rewrite requires a valid session ID")
	}
	if !metadataValid || metadata.RequestKind != "turn" {
		return nil, errors.New("mekugi rewrite requires valid turn metadata")
	}
	// Execution-free requests retain their native instructions, tools, and schema.
	if request.isExecutionFreeRequest() {
		if strings.TrimSpace(threadID) == "" {
			return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
		}
		return nil, nil
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
		p.activity.collect(activityThreadID, "subagent-start\x00"+activityThreadID, "start", subagentStartCommentary(request))
	}
	deferredCommentary := p.drainCommentarySession(historySessionID, threadID)
	transform := &mekugiResponseTransform{
		ctx:              ctx,
		proxy:            p,
		sessionID:        sessionID,
		shellThreadID:    threadID,
		shellDirectory:   shellDirectory,
		model:            request.modelDescription(),
		historySessionID: historySessionID,
		visible:          visible,
		sessionActive:    true,
		usageTracker:     p.usage.observation(threadID, metadata.ThreadID, request.model()),
		threadID:         activityThreadID,
		activityStarted:  time.Now(),

		originalTools:             originalTools,
		originalToolsPresent:      originalToolsPresent,
		originalToolChoice:        originalToolChoice,
		originalToolChoicePresent: originalToolChoicePresent,
		pending:                   make(map[string]mekugiPendingCall),
		nativeExecCalls:           make(map[string]map[string]json.RawMessage),
		local:                     make(map[string]mekugiHistory),
		directory:                 directory,
		carriers:                  carriers,
		subagentDeferred:          subagentDeferred,
		subagentResponses:         subagentDeferred,
		subagentTurn:              metadata.SubagentKind != "",
		commentaryAuthor:          metadata.commentaryAuthor(),
		commentaryTools:           commentaryTools,
		deferredCommentary:        deferredCommentary,
		commentaryEmitted:         make(map[string]struct{}),

		codeModeToolName: codeModeToolName,
		nativeTools:      nativeTools,
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
		if !errors.Is(err, errJournalThreadCapacity) {
			transform.Close()
			return nil, err
		}
	} else {
		if err := p.journals.bindIdentity(ctx, p.replayStore, directory, threadID, metadata.ParentThreadID, author, activityThreadID != ""); err != nil {
			transform.Close()
			return nil, err
		}
		transform.journalAvailable = true
	}
	transform.journalQuestion = journalQuestionFromInput(request.fields["input"])
	transform.journalActive = true
	transform.journalPending = make(map[string]bool)
	transform.journalCalls = make(map[string]map[string]json.RawMessage)
	projectExecutionContinuations(request, tools, codeModeToolName, visible)
	if transform.subagentTurn {
		transform.prepareShellActivity(request.fields["input"])
	}
	return transform, nil
}

type codeModeApplyPatchOwner struct {
	group     *responsesAdditionalTools
	section   *responsesToolSection
	toolIndex int
	name      string

	strippedDescription          string
	execCommandParamsDescription string
}

func installedToolNames(tools []*responsesToolDefinition) map[string]struct{} {
	names := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		names[tool.Name] = struct{}{}
	}
	return names
}

// replaceCodeModeTools rewrites the authoritative Code Mode exec tool, whether
// top-level or in additional_tools, and exposes the router's standalone tools.
func replaceCodeModeTools(fields map[string]json.RawMessage, catalog *responsesToolCatalog, installedTools []*responsesToolDefinition) (string, bool, error) {
	if catalog.top.present {
		if err := catalog.top.err; err != nil {
			return "", false, fmt.Errorf("decode responses tools: %w", err)
		}
	}
	installedNames := installedToolNames(installedTools)
	owner, err := findCodeModeApplyPatch(catalog, installedNames)
	if err != nil || owner == nil {
		return "", false, err
	}
	for index, tool := range catalog.top.tools {
		if owner.group == nil && index == owner.toolIndex {
			continue
		}
		name := tool.Name
		if _, exists := installedNames[name]; exists {
			return "", false, fmt.Errorf("responses request already defines %s", name)
		}
		if name == applyPatchToolName || name == "exec" || name == "functions.exec" {
			return "", false, fmt.Errorf("responses request exposes unsupported top-level %s", name)
		}
	}
	if codeModeToolChoiceRestricted(fields, owner.name) {
		return "", false, incompatibleRequest("restricted_tool_choice", "The forced Code Mode tool choice prevents Mekugi replacement. Use automatic tool choice.")
	}
	if err := exposeStandaloneMekugi(fields, catalog, owner, installedTools); err != nil {
		return "", false, err
	}
	return owner.name, true, nil
}

// replaceNativeTools replaces the native apply_patch definition while retaining
// exec_command as the executor-owned carrier for translated results.
func replaceNativeTools(fields map[string]json.RawMessage, catalog *responsesToolCatalog, installedTools []*responsesToolDefinition) (string, bool, error) {
	if !catalog.top.present || catalog.top.err != nil {
		return "", false, nil //nolint:nilerr // An absent native tool array belongs to another request shape.
	}
	tools := catalog.top.tools
	installedNames := installedToolNames(installedTools)
	applyPatchIndex := -1
	execCommandIndex := -1
	for index, tool := range tools {
		name := tool.Name
		if _, exists := installedNames[name]; exists {
			return "", false, incompatibleRequest("invalid_tool_catalog", fmt.Sprintf("The request already defines %s. Remove the conflicting HPATCH tool definition.", name))
		}
		switch name {
		case applyPatchToolName:
			if applyPatchIndex >= 0 {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native apply_patch is defined more than once. Use one custom apply_patch tool.")
			}
			if tool.Type != "custom" {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native apply_patch must be a custom tool. Check the Codex tool catalog.")
			}
			applyPatchIndex = index
		case nativeExecCommandToolName:
			if execCommandIndex >= 0 {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native exec_command is defined more than once. Use one function exec_command tool.")
			}
			if tool.Type != "function" {
				return "", false, incompatibleRequest("invalid_tool_catalog", "Native exec_command must be a function tool. Check the Codex tool catalog.")
			}
			execCommandIndex = index
		case "exec", "functions.exec":
			return "", false, incompatibleRequest("invalid_tool_catalog", fmt.Sprintf("Unsupported top-level %s tool. Use a supported Codex tool catalog.", name))
		}
	}
	if applyPatchIndex < 0 && execCommandIndex >= 0 {
		return "", false, incompatibleRequest("missing_apply_patch", "The request has exec_command but no apply_patch tool. Enable editing tools for this Codex session.")
	}
	if execCommandIndex < 0 && applyPatchIndex >= 0 {
		return "", false, incompatibleRequest("missing_exec_command", "The request has apply_patch but no exec_command carrier. Enable execution tools for this Codex session.")
	}
	if applyPatchIndex < 0 || execCommandIndex < 0 {
		return "", false, nil
	}
	var choice struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(fields["tool_choice"], &choice) == nil {
		selected := choice.Name
		if selected == applyPatchToolName || selected == nativeExecCommandToolName {
			return "", false, incompatibleRequest("restricted_tool_choice", "The forced native tool choice prevents Mekugi replacement. Use automatic tool choice.")
		}
	}
	catalog.removeTop(applyPatchIndex)
	catalog.appendTop(installedTools)
	if err := catalog.encodeTop(fields); err != nil {
		return "", false, fmt.Errorf("encode native Responses tools: %w", err)
	}
	return nativeExecCommandToolName, true, nil
}

// findCodeModeApplyPatch locates exactly one authoritative Code Mode exec tool.
func findCodeModeApplyPatch(catalog *responsesToolCatalog, installedNames map[string]struct{}) (*codeModeApplyPatchOwner, error) {
	var owner *codeModeApplyPatchOwner
	claim := func(
		group *responsesAdditionalTools,
		section *responsesToolSection,
		toolIndex int,
		nested bool,
	) error {
		tool := section.tools[toolIndex]
		name := tool.Name
		if _, exists := installedNames[name]; exists {
			if nested {
				return fmt.Errorf("responses functions namespace defines %s", name)
			}
			return fmt.Errorf("responses additional_tools item defines direct %s", name)
		}
		if name == applyPatchToolName {
			if nested {
				return errors.New("responses functions namespace exposes direct apply_patch")
			}
			return errors.New("responses additional_tools item exposes unsupported flat apply_patch")
		}
		if name != "exec" {
			return nil
		}
		if owner != nil {
			return errors.New("responses request defines Code Mode exec more than once")
		}
		stripped, found, err := stripCodeModeApplyPatchSection(tool.Description)
		if err != nil {
			return err
		}
		if !found || tool.Type != "custom" {
			return nil
		}
		var execCommandParamsDescription string
		stripped, execCommandParamsDescription, _, err = stripCodeModeExecCommandContract(stripped)
		if err != nil {
			return err
		}
		owner = &codeModeApplyPatchOwner{
			group:                        group,
			section:                      section,
			toolIndex:                    toolIndex,
			name:                         name,
			strippedDescription:          stripped,
			execCommandParamsDescription: execCommandParamsDescription,
		}
		return nil
	}
	if catalog.top.err == nil {
		for index, tool := range catalog.top.tools {
			if tool != nil && tool.Name == "exec" {
				if err := claim(nil, catalog.top, index, false); err != nil {
					return nil, err
				}
			}
		}
	}
	if catalog.inputObjectsErr != nil && catalog.inputItems == nil {
		return owner, nil
	}
	for _, group := range catalog.additional {
		if group.tools.err != nil {
			continue
		}
		for additionalToolIndex, additionalTool := range group.tools.tools {
			name := additionalTool.Name
			if additionalTool.Type != "namespace" {
				if name == "functions.exec" {
					return nil, errors.New("responses additional_tools item exposes unsupported flat functions.exec")
				}
				if err := claim(group, group.tools, additionalToolIndex, false); err != nil {
					return nil, err
				}
				continue
			}
			if name == "exec" || name == "functions.exec" || name == applyPatchToolName {
				return nil, fmt.Errorf("responses additional_tools item exposes unsupported flat %s", name)
			}
			if _, exists := installedNames[name]; exists {
				return nil, fmt.Errorf("responses additional_tools item defines direct %s", name)
			}
			if name != "functions" {
				continue
			}

			node := group.tools.nodes[additionalToolIndex]
			if node == nil || node.nested == nil || node.nested.err != nil {
				continue
			}
			for toolIndex := range node.nested.tools {
				if err := claim(group, node.nested, toolIndex, true); err != nil {
					return nil, err
				}
			}
		}
	}
	return owner, nil
}

func codeModeToolChoiceRestricted(fields map[string]json.RawMessage, codeToolName string) bool {
	var choice struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(fields["tool_choice"], &choice) != nil {
		return false
	}
	return choice.Type == "custom" && choice.Name == codeToolName
}

// exposeStandaloneMekugi exposes standalone mekugi tools in the tool catalog.
func exposeStandaloneMekugi(fields map[string]json.RawMessage, catalog *responsesToolCatalog, owner *codeModeApplyPatchOwner, installedTools []*responsesToolDefinition) error {
	owner.section.tools[owner.toolIndex].setDescription(owner.strippedDescription)
	shellIndex := slices.IndexFunc(installedTools, func(tool *responsesToolDefinition) bool {
		return tool.Name == "shell"
	})
	if shellIndex < 0 {
		return errors.New("built-in shell tool is unavailable")
	}
	if owner.execCommandParamsDescription != "" {
		description := strings.TrimRight(installedTools[shellIndex].Description, "\r\n")
		description += "\n\n" + owner.execCommandParamsDescription
		installedTools[shellIndex].setDescription(description)
	}
	if owner.group != nil {
		if err := catalog.encodeAdditional(fields, owner.group, owner.section); err != nil {
			return fmt.Errorf("encode Responses input: %w", err)
		}
	}
	catalog.appendTop(installedTools)
	if err := catalog.encodeTop(fields); err != nil {
		return fmt.Errorf("encode Responses tools: %w", err)
	}
	return nil
}

func (t *mekugiResponseTransform) routesTool(name string) bool {
	if t == nil || t.proxy == nil {
		return false
	}
	contribution, ok := t.proxy.registry.contribution(name)
	return ok && contribution.ModelVisible
}

func customGrammarTool(name, description, grammar string) map[string]json.RawMessage {
	tool := responses.ToolParamOfCustom(name)
	tool.OfCustom.Description = param.NewOpt(description)
	tool.OfCustom.Format = shared.CustomToolInputFormatParamOfGrammar(grammar, "lark")
	return mustToolDefinitionFields(tool)
}

func customFreeformTool(name, description string) map[string]json.RawMessage {
	tool := responses.ToolParamOfCustom(name)
	tool.OfCustom.Description = param.NewOpt(description)
	return mustToolDefinitionFields(tool)
}

// mustToolDefinitionFields converts a tool parameter to its JSON field map.
func mustToolDefinitionFields(tool responses.ToolUnionParam) map[string]json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshalJSON(tool), &fields); err != nil {
		panic(err)
	}
	return fields
}

const (
	codeModeApplyPatchHeading       = "### `apply_patch`"
	codeModeExecCommandHeading      = "### `exec_command`"
	codeModeExecCommandPlainHeading = "### exec_command"
)

type codeModeSectionMatcher func(string) (int, string)

func stripCodeModeSection(description string, findHeading codeModeSectionMatcher, valid func(string) bool, duplicateError string) (string, string, bool, error) {
	start, heading := findHeading(description)
	if start < 0 {
		return description, "", false, nil
	}
	sectionEnd := len(description)
	remaining := description[start+len(heading):]
	for _, nextHeading := range []string{"\n### ", "\n## "} {
		if offset := strings.Index(remaining, nextHeading); offset >= 0 {
			sectionEnd = min(sectionEnd, start+len(heading)+offset+1)
		}
	}
	section := description[start:sectionEnd]
	if valid != nil && !valid(section) {
		return description, "", false, nil
	}
	lineEnding := "\n"
	if strings.Contains(description, "\r\n") {
		lineEnding = "\r\n"
	}
	stripped := strings.TrimRight(description[:start], "\r\n")
	suffix := strings.TrimLeft(description[sectionEnd:], "\r\n")
	if stripped != "" && suffix != "" {
		stripped += lineEnding + lineEnding
	}
	stripped += suffix
	if duplicateStart, _ := findHeading(stripped); duplicateStart >= 0 {
		return "", "", false, errors.New(duplicateError)
	}
	return stripped, section, true, nil
}

// stripCodeModeApplyPatchSection removes the Code Mode apply_patch section from a
// tool description. It also returns that removed section, which is the native
// patch tool definition mekugi displaces: the host pays for one or the other as
// request input, so measuring mekugi's definition cost requires the text it replaced.
func stripCodeModeApplyPatchSection(description string) (string, bool, error) {
	findHeading := func(text string) (int, string) {
		start := strings.Index(text, codeModeApplyPatchHeading)
		if start < 0 || start > 0 && text[start-1] != '\n' {
			return -1, ""
		}
		return start, codeModeApplyPatchHeading
	}
	const declaration = "declare const tools: { apply_patch(input: string): Promise<unknown>; };"
	valid := func(section string) bool {
		return strings.Contains(section, "exec tool declaration:") && strings.Contains(section, declaration)
	}
	stripped, _, found, err := stripCodeModeSection(
		description,
		findHeading,
		valid,
		"responses Code Mode tool defines nested apply_patch more than once",
	)
	return stripped, found, err
}

// stripCodeModeExecCommandSection removes only the nested command-execution
// section. It recognizes app and CLI description bodies without parsing either
// parameter schema. The apply_patch extractor remains an independent contract.
func stripCodeModeExecCommandSection(description string) (string, string, bool, error) {
	findHeading := func(text string) (int, string) {
		best := -1
		matched := ""
		for _, heading := range []string{codeModeExecCommandHeading, codeModeExecCommandPlainHeading} {
			searchFrom := 0
			for searchFrom < len(text) {
				offset := strings.Index(text[searchFrom:], heading)
				if offset < 0 {
					break
				}
				index := searchFrom + offset
				end := index + len(heading)
				for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
					end++
				}
				lineStart := index == 0 || text[index-1] == '\n'
				lineEnd := end == len(text) || text[end] == '\n' || text[end] == '\r'
				if lineStart && lineEnd {
					if best < 0 || index < best {
						best = index
						matched = heading
					}
					break
				}
				searchFrom = index + len(heading)
			}
		}
		return best, matched
	}
	return stripCodeModeSection(
		description,
		findHeading,
		nil,
		"responses Code Mode tool defines nested exec_command more than once",
	)
}

func execCommandParamsDescription(section string) string {
	const heading = "### `#!params`"
	const appMarker = "exec_command(args:"

	if _, after, ok := strings.Cut(section, appMarker); ok {
		rest := strings.TrimLeft(after, " \t")
		end := strings.Index(rest, "}): Promise")
		if !strings.HasPrefix(rest, "{") || end < 0 {
			return ""
		}
		shape := rest[:end+1]
		inside := shape[1 : len(shape)-1]
		cursor := 0
		for {
			cursor += len(inside[cursor:]) - len(strings.TrimLeft(inside[cursor:], " \t\r\n"))
			if !strings.HasPrefix(inside[cursor:], "//") {
				break
			}
			newline := strings.IndexByte(inside[cursor:], '\n')
			if newline < 0 {
				return ""
			}
			cursor += newline + 1
		}
		field := inside[cursor:]
		colon := strings.IndexByte(field, ':')
		semicolon := strings.IndexByte(field, ';')
		if colon < 0 || semicolon < colon || strings.TrimSpace(field[:colon]) != "cmd" {
			return ""
		}
		shape = "{" + inside[cursor+semicolon+1:] + "}"
		if strings.Contains(shape, "exec_command") {
			return ""
		}
		return heading + "\nThe leading `#!params={...}` directive accepts this request-specific JSON object shape. The script body supplies `cmd`, so omit it.\n\n```ts\n" + shape + "\n```"
	}

	normalized := strings.ReplaceAll(section, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	parameters := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) == "Parameters:"
	})
	if parameters < 0 {
		return ""
	}
	kept := make([]string, 0, len(lines)-parameters)
	for _, line := range lines[parameters+1:] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if strings.HasPrefix(name, "`cmd`") || strings.HasPrefix(name, "cmd:") {
			continue
		}
		kept = append(kept, trimmed)
	}
	if len(kept) == 0 {
		return ""
	}
	fields := strings.Join(kept, "\n")
	if strings.Contains(fields, "exec_command") {
		return ""
	}
	return heading + "\nThe leading `#!params={...}` directive accepts a JSON object with these request-specific fields. The script body supplies `cmd`, so omit it.\n\n" + fields
}

// stripCodeModeExecCommandContract removes the command tool section and the
// introductory example from the model-visible Code Mode description. It derives
// a shell-specific parameter description without retaining the nested tool surface.
func stripCodeModeExecCommandContract(description string) (string, string, bool, error) {
	stripped, section, found, err := stripCodeModeExecCommandSection(description)
	if err != nil {
		return "", "", false, err
	}
	if !found {
		if strings.Contains(description, "exec_command") {
			return "", "", false, errors.New("responses Code Mode tool exposes exec_command without an owned section")
		}
		return description, "", false, nil
	}
	const example = " for example `await tools.exec_command(...)`."
	if count := strings.Count(stripped, example); count > 1 {
		return "", "", false, errors.New("responses Code Mode tool references tools.exec_command more than once outside its section")
	} else if count == 1 {
		stripped = strings.Replace(stripped, example, "", 1)
	}
	if strings.Contains(stripped, "exec_command") {
		return "", "", false, errors.New("responses Code Mode tool exposes exec_command outside its owned contract")
	}
	return stripped, execCommandParamsDescription(section), true, nil
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

func (t *mekugiResponseTransform) translate(callID, input string, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	if history, ok := t.local[callID]; ok {
		if history.toolName != mekugiToolName || history.pluginID != "" || history.script != input {
			return mekugiHistory{}, fmt.Errorf("mekugi call %q changed input", callID)
		}
		if len(upstreamItem) != 0 {
			history.upstreamItem = maps.Clone(upstreamItem)
			t.local[callID] = history
		}
		return history, nil
	}
	if len(input) > maxMekugiScriptBytes {
		return mekugiHistory{}, fmt.Errorf("mekugi call %q script exceeds %d bytes", callID, maxMekugiScriptBytes)
	}

	if strings.HasPrefix(strings.TrimSpace(input), "resume ") {
		return t.translateMixedResume(callID, input, upstreamItem)
	}

	if parts, mixed, err := hpatchsyntax.SplitShell(input); mixed {
		return t.translateMixedScript(callID, input, parts, err, upstreamItem)
	}

	evaluated, err := mekugi.RewriteTargetAliases(input, t.targetAliases())
	if err != nil {
		// Preserve evaluator-owned syntax diagnostics for malformed scripts.
		evaluated = input
	}
	attemptMetadata := mekugi.AttemptMetadata{
		SessionID:       t.sessionID,
		Title:           t.proxy.titles.title(t.sessionID),
		CorrelationID:   callID,
		CallID:          callID,
		Attempt:         1,
		Correction:      false,
		Model:           t.model,
		ToolName:        mekugiToolName,
		EmittedPayload:  input,
		EvaluatedScript: evaluated,
	}

	return t.evaluateScript(callID, input, evaluated, attemptMetadata, upstreamItem)
}

// evaluateScript owns target dispatch and result projection for both ordinary and
// rebuilt recovery scripts. Recovery policy never selects a different storage root.
func (t *mekugiResponseTransform) evaluateScript(
	callID, input, evaluated string,
	attemptMetadata mekugi.AttemptMetadata,
	upstreamItem map[string]json.RawMessage,
) (mekugiHistory, error) {
	changeID, err := t.changeIDForAttempt(attemptMetadata)
	if err != nil {
		return mekugiHistory{}, err
	}
	applied := false
	var translated mekugiTranslationResult
	retainedStart := len(evaluated) - len(strings.TrimLeft(evaluated, "\r\n"))
	retainedScript := evaluated
	retainedBody, retained := strings.CutPrefix(evaluated[retainedStart:], "in "+shellArtifactPrefix)
	if retained {
		retainedScript = evaluated[:retainedStart] + "in " + retainedBody
	}
	retainedApply := t.proxy.shellDirectory != "" && retained
	if retainedApply {
		attemptMetadata.EvaluatedScript = retainedScript
	}
	attemptContext := mekugi.WithAttemptMetadata(t.ctx, attemptMetadata)
	if retainedApply {
		root, release, openErr := t.proxy.shellRoot(t.shellDirectory)
		if errors.Is(openErr, errRetainedShellUnavailable) {
			return t.rejectUnevaluated(attemptMetadata.ToolName, callID, input, openErr, attemptMetadata, "", nil, upstreamItem)
		}
		if openErr != nil {
			return mekugiHistory{}, fmt.Errorf("open retained shell directory: %w", openErr)
		}
		defer release()
		applier, ok := t.proxy.translator.(mekugiApplier)
		if !ok {
			return mekugiHistory{}, errors.New("mekugi translator cannot apply retained shell edits")
		}
		translated, err = applier.Apply(attemptContext, root, retainedScript)
		applied = err == nil
	} else {
		translated, err = t.proxy.translator.Translate(attemptContext, t.directory, evaluated)
	}
	if err != nil {
		if contextErr := t.ctx.Err(); contextErr != nil {
			return mekugiHistory{}, contextErr
		}
		if errors.Is(err, errMekugiCapacity) {
			return mekugiHistory{}, err
		}
		evaluatorRejected := len(translated.rejections) != 0
		diagnostic := translated.diagnostic
		if diagnostic == "" {
			diagnostic = err.Error()
		}
		if evaluatorRejected {
			diagnostic += mekugiRecoveryGuidance(evaluated, translated.rejections, attemptMetadata.Correction)
		}
		history := mekugiHistory{
			toolName: attemptMetadata.ToolName,
			script:   input,

			root:              t.directory,
			evaluated:         retainedEvaluated(input, evaluated),
			carrierName:       t.codeModeToolName,
			translationError:  changeNotice(changeID) + diagnostic,
			changeID:          changeID,
			evaluatorRejected: evaluatorRejected,
			rejections:        slices.Clone(translated.rejections),

			upstreamItem:  maps.Clone(upstreamItem),
			correlationID: attemptMetadata.CorrelationID,
			attempt:       attemptMetadata.Attempt,
		}
		t.recordLocal(callID, &history)
		return history, nil
	}
	patch := translated.patch
	if len(patch) > maxMekugiPatchBytes {
		return mekugiHistory{}, fmt.Errorf("mekugi call %q translation exceeds %d bytes", callID, maxMekugiPatchBytes)
	}
	patchText := string(patch)
	alreadySatisfied := translated.change.AlreadySatisfied
	history := mekugiHistory{
		toolName: attemptMetadata.ToolName,
		script:   input,

		root:             t.directory,
		evaluated:        retainedEvaluated(input, evaluated),
		patch:            patchText,
		applied:          applied,
		alreadySatisfied: alreadySatisfied,
		confirmed:        applied,
		aliases:          slices.Clone(translated.aliases),
		carrierName:      t.codeModeToolName,
		report:           changeNotice(changeID) + mekugiReport(translated.report, translated.diagnostic),
		changeID:         changeID,
		reviewFiles:      translated.reviewFiles,
		upstreamItem:     maps.Clone(upstreamItem),
		correlationID:    attemptMetadata.CorrelationID,
		attempt:          attemptMetadata.Attempt,
	}
	t.recordLocal(callID, &history)
	return history, nil
}

func (t *mekugiResponseTransform) translateTool(name, callID, input string, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	switch name {
	case mekugiToolName:
		return t.translate(callID, input, upstreamItem)
	case mekugiRecoveryToolName:
		return t.translateRecovery(callID, input, upstreamItem)
	case reportIssueToolName:
		return t.translateReportIssue(callID, input, upstreamItem)
	}
	contribution, ok := t.proxy.registry.contribution(name)
	if !ok || contribution.Builtin {
		return mekugiHistory{}, fmt.Errorf("registered tool %q is unavailable", name)
	}
	return t.translateRegisteredTool(contribution, callID, input, upstreamItem)
}

func (t *mekugiResponseTransform) translateReportIssue(callID, input string, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	if history, ok := t.local[callID]; ok {
		if history.toolName != reportIssueToolName || history.script != input {
			return mekugiHistory{}, fmt.Errorf("report_issue call %q changed input", callID)
		}
		if len(upstreamItem) != 0 {
			history.upstreamItem = maps.Clone(upstreamItem)
			t.local[callID] = history
		}
		return history, nil
	}
	attemptContext := mekugi.WithAttemptMetadata(t.ctx, mekugi.AttemptMetadata{
		SessionID:       t.sessionID,
		Title:           t.proxy.titles.title(t.sessionID),
		CorrelationID:   callID,
		CallID:          callID,
		Attempt:         1,
		Model:           t.model,
		ToolName:        reportIssueToolName,
		EmittedPayload:  input,
		EvaluatedScript: input,
	})
	report := "Issue reported."
	if err := t.proxy.registry.DiagnoseHooks.Report(attemptContext, input); err != nil {
		report = "Issue report was not delivered.\nmekugi: warning: " + strings.TrimSpace(err.Error()) + "\n"
	}
	history := mekugiHistory{
		toolName:     reportIssueToolName,
		script:       input,
		carrierName:  t.codeModeToolName,
		report:       report,
		applied:      true,
		upstreamItem: maps.Clone(upstreamItem),
	}
	t.recordLocal(callID, &history)
	return history, nil
}

func (t *mekugiResponseTransform) translateRegisteredTool(contribution toolContribution, callID, input string, upstreamItem map[string]json.RawMessage) (mekugiHistory, error) {
	if history, ok := t.local[callID]; ok {
		if history.toolName != contribution.Name || history.pluginID != contribution.PluginID || history.script != input {
			return mekugiHistory{}, fmt.Errorf("%s call %q changed input", contribution.Name, callID)
		}
		if len(upstreamItem) != 0 {
			history.upstreamItem = maps.Clone(upstreamItem)
			t.local[callID] = history
		}
		return history, nil
	}
	axCallID := ""
	if t.featureTrace.debug != nil || os.Getenv(capturer.AXReadOutputEnvironment) != "" {
		axCallID = callID
	}
	pathPrefix := t.shellDirectory + string(os.PathSeparator)
	recovered := !t.nativeTools && shellCodeModeRecovery(contribution, input)
	var stopBatchOnNonzero bool
	var batch []string
	var translation toolplugin.Translation
	var err error
	effectiveInput := input
	if !recovered && contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell" {
		effectiveInput, err = t.proxy.resolveShellInput(t.shellDirectory, input)
		if err == nil {
			var programs []string
			_, stopBatchOnNonzero, _ = shellsyntax.BatchHeader(effectiveInput)
			programs, err = shellsyntax.Split(effectiveInput)
			if err == nil && len(programs) > 1 {
				batch, translation, err = t.prepareShellBatch(contribution, programs, pathPrefix, axCallID)
			}
		}
		if err != nil {
			translation = toolplugin.Translation{Rejected: true, Diagnostic: err.Error()}
		}
	}
	if !recovered && !translation.Rejected && len(batch) == 0 {
		if contribution.PluginID == builtinToolsPluginID {
			translation, err = t.proxy.registry.builtinTranslator.Translate(t.ctx, contribution.ModuleIndex, effectiveInput, pathPrefix)
		} else {
			translation, err = toolplugin.Translate(
				t.ctx,
				t.proxy.registry.NodeExecutable,
				t.proxy.registry.RuntimeRoot,
				contribution.Module,
				contribution.ModuleIndex,
				effectiveInput,
				pathPrefix,
			)
		}
		if err != nil {
			return mekugiHistory{}, fmt.Errorf("translate registered tool %s: %w", contribution.Name, err)
		}
	}
	if !recovered && !translation.Rejected && len(batch) == 0 && shellTypeScriptMisuse(contribution, translation.Arguments) {
		translation = toolplugin.Translation{Rejected: true, Diagnostic: shellTypeScriptDiagnostic}
	}
	var resultMetadata map[string]json.RawMessage
	if translation.Carrier.RetainInput != nil {
		resultMetadata = map[string]json.RawMessage{"retained": mustMarshalJSON(false)}
		if *translation.Carrier.RetainInput {
			reference, expiresAt, retained := t.proxy.retainShell(t.shellDirectory, callID, effectiveInput)
			resultMetadata["retained"] = mustMarshalJSON(retained)
			if retained {
				resultMetadata["retention"] = mustMarshalJSON(map[string]any{
					"scope": "thread", "durable": false,
					"scheduled_expiry":               expiresAt.UTC().Format(time.RFC3339Nano),
					"ends_on_router_shutdown":        true,
					"reads_or_edits_extend_lifetime": false,
				})
				resultMetadata["script_ref"] = mustMarshalJSON(reference)
			}
		}
	}

	kind := codeModeCarrierCustom
	if t.nativeTools {
		kind = codeModeCarrierFunction
	}
	name := t.codeModeToolName
	payload := ""
	diagnostic := translation.Diagnostic
	splitShellCarrier := false
	var misuseWarnings []string
	if recovered {
		misuseWarnings = append(misuseWarnings, shellCodeModeRecoveryWarning)
		if inspectCodeModeRuntime(input).execCommand {
			misuseWarnings = append(misuseWarnings, nativeExecCommandWarning)
		}
		payload = input
		if err := t.carriers.require(name, kind); err != nil {
			return mekugiHistory{}, err
		}
	} else if translation.Rejected {
		if err := t.carriers.require(name, kind); err != nil {
			return mekugiHistory{}, fmt.Errorf("%s input rejection: %w", contribution.Name, err)
		}
		if diagnostic == "" {
			diagnostic = contribution.Name + " rejected the model input"
		}
		if t.nativeTools {
			command := "printf %s " + shellQuoteArgument(diagnostic)
			if diagnostic == shellTypeScriptDiagnostic {
				command = mekugiNativeDiagnosticMarker + strconv.Quote(diagnostic) + "\n" + command
			}
			payload = renderExecCarrier(
				kind,
				execCommandArguments(command, nil),
				false,
				nil,
			)
		} else {
			payload = "text(" + strconv.Quote(diagnostic) + ");"
		}
	} else {
		switch translation.Carrier.Kind {
		case "exec":
			if err := t.carriers.require(name, kind); err != nil {
				return mekugiHistory{}, fmt.Errorf("%s exec carrier: %w", contribution.Name, err)
			}
			if len(batch) != 0 {
				payload = renderShellBatch(batch, resultMetadata, stopBatchOnNonzero)
				splitShellCarrier = true
				break
			}
			arguments := translation.Arguments
			if splitPayload, ok := t.shellCatCarrier(contribution, kind, arguments, translation.Carrier.Template, translation.Carrier.Params, resultMetadata, axCallID); ok {
				payload = splitPayload
				splitShellCarrier = true
				break
			}
			payload, err = t.proxy.registry.execCarrierPayload(
				kind,
				contribution,
				input,
				arguments,
				translation.Carrier.Template,
				translation.Carrier.Params,
				resultMetadata,
				axCallID,
			)
			if err != nil {
				return mekugiHistory{}, fmt.Errorf("%s exec carrier: %w", contribution.Name, err)
			}
		case "custom":
			kind = codeModeCarrierCustom
			name = translation.Carrier.Name
			payload = translation.Carrier.Payload
			if err := t.carriers.require(name, kind); err != nil {
				return mekugiHistory{}, fmt.Errorf("%s custom carrier: %w", contribution.Name, err)
			}
		case "function":
			kind = codeModeCarrierFunction
			name = translation.Carrier.Name
			payload = translation.Carrier.Payload
			if err := t.carriers.require(name, kind); err != nil {
				return mekugiHistory{}, fmt.Errorf("%s function carrier: %w", contribution.Name, err)
			}
			var arguments map[string]json.RawMessage
			if json.Unmarshal([]byte(payload), &arguments) != nil || arguments == nil {
				return mekugiHistory{}, fmt.Errorf("%s function carrier returned invalid JSON object arguments", contribution.Name)
			}
		default:
			return mekugiHistory{}, fmt.Errorf(
				"%s translator returned unsupported carrier kind %q",
				contribution.Name,
				translation.Carrier.Kind,
			)
		}
	}
	if !translation.Rejected && !splitShellCarrier {
		for _, misuse := range shellInterpreterWrapperMisuses(contribution, input) {
			misuseWarnings = append(misuseWarnings, shellInterpreterWrapperWarning(misuse))
		}
	}
	misuseWarning := ""
	outputWarning := ""
	if recovered {
		usage := inspectCodeModeRuntime(payload)
		if usage.textShadowed {
			outputWarning = strings.Join(misuseWarnings, "\n") + "\n"
		} else {
			for _, warning := range misuseWarnings {
				misuseWarning += misuseWarningProjection(warning)
			}
		}
		offset := usage.warningOffset
		payload = payload[:offset] + misuseWarning + payload[offset:]
	} else if t.nativeTools && len(misuseWarnings) != 0 {
		var arguments map[string]json.RawMessage
		if json.Unmarshal([]byte(payload), &arguments) != nil || arguments == nil {
			return mekugiHistory{}, fmt.Errorf("%s native exec carrier returned invalid arguments", contribution.Name)
		}
		command := jsonString(arguments, "cmd")
		for _, warning := range misuseWarnings {
			misuseWarning += warning + "\n"
		}
		arguments["cmd"] = mustMarshalJSON("printf %s " + shellQuoteArgument(misuseWarning) + "\n" + command)
		payload = string(mustMarshalJSON(arguments))
	} else {
		for _, warning := range misuseWarnings {
			warnedPayload, warningInput, _, warningErr := insertExecCommandWarning(payload, warning)
			if warningErr != nil {
				return mekugiHistory{}, fmt.Errorf("%s interpreter-wrapper warning: %w", contribution.Name, warningErr)
			}
			misuseWarning += warningInput
			payload = warnedPayload
		}
	}

	if contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell" &&
		jsonString(upstreamItem, "name") == t.codeModeToolName && !translation.Rejected {
		payload = misuseWarningProjection(execShellRecoveryWarning) + payload
	}

	history := mekugiHistory{
		toolName:         contribution.Name,
		pluginID:         contribution.PluginID,
		script:           input,
		root:             t.directory,
		carrierKind:      kind,
		carrierName:      name,
		carrierPayload:   payload,
		translationError: diagnostic,
		outputWarning:    outputWarning,
		upstreamItem:     maps.Clone(upstreamItem),
		replayCarrier:    recovered,
	}
	t.recordLocal(callID, &history)
	return history, nil
}

func mekugiReport(report, diagnostic string) string {
	if diagnostic == "" {
		return report
	}
	if report != "" && !strings.HasSuffix(report, "\n") {
		report += "\n"
	}
	return report + diagnostic
}

func retainedEvaluated(emitted, evaluated string) string {
	if emitted == evaluated {
		return ""
	}
	return evaluated
}

func (t *mekugiResponseTransform) TransformJSON(payload []byte) ([]byte, error) {
	transformed, _, err := t.transformResponse(payload, "")
	if err == nil && t.journalActive {
		transformed, err = t.decorateJournalJSON(transformed)
	}
	return transformed, criticalDiagnostic(err, "mekugi_json", "Mekugi response translation failed while processing a JSON response", true)
}

func (t *mekugiResponseTransform) Finish(streamEvent bool) error {
	if streamEvent && len(t.pending) != 0 {
		return staticCriticalDiagnostic("stream_ended_incomplete_mekugi_call", "the upstream stream ended with an incomplete HPATCH call")
	}
	return nil
}

func (t *mekugiResponseTransform) TransformSSE(payload []byte) ([][]byte, error) {
	// Match the failed terminal projected to the host before journal interception
	// can prepare a successful flush or continue a failed response.
	var terminal struct {
		Type     string `json:"type"`
		Response struct {
			Status string `json:"status"`
		} `json:"response"`
	}
	if t.journalActive && json.Unmarshal(payload, &terminal) == nil && terminal.Type == "response.completed" && terminal.Response.Status == "failed" {
		var err error
		payload, err = replaceRawField(payload, "type", mustMarshalJSON("response.failed"))
		if err != nil {
			return nil, err
		}
	}
	visible, err := t.transformSSE(payload)
	if err == nil && t.journalActive {
		visible, err = t.decorateJournalSSE(payload, visible)
	}
	return visible, criticalDiagnostic(err, "mekugi_sse", "Mekugi response translation failed while processing an upstream streaming event", true)
}

func (t *mekugiResponseTransform) transformSSE(payload []byte) ([][]byte, error) {
	var prefix [][]byte
	if t.journalActive {
		events, handled, err := t.interceptJournalSSE(payload)
		if handled || err != nil {
			return events, err
		}
		prefix = events
	}
	visible, err := t.transformNonJournalSSE(payload)
	return append(prefix, visible...), err
}

func (t *mekugiResponseTransform) transformNonJournalSSE(payload []byte) ([][]byte, error) {
	if len(t.subagentDeferred) != 0 {
		t.subagentDeferred = t.retainCommentary(t.subagentDeferred...)
		if len(t.subagentDeferred) == 0 {
			t.subagentResponses = nil
		}
	}
	messages := t.retainCommentary(t.drainActivity()...)
	t.activityMessages = append(t.activityMessages, messages...)
	visible, err := t.transformActivitySSE(payload)
	if err != nil || len(messages) == 0 {
		return visible, err
	}
	var generated [][]byte
	for _, message := range messages {
		generated = append(generated, assistantCommentaryDoneEvent(message))
	}
	var event struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &event)
	if event.Type == "response.created" && len(visible) != 0 {
		return append(append(visible[:1:1], generated...), visible[1:]...), nil
	}
	return append(generated, visible...), nil
}

func (t *mekugiResponseTransform) transformActivitySSE(payload []byte) ([][]byte, error) {
	if visible, buffered := t.finalAnswer.observe(payload); buffered {
		return visible, nil
	}
	var envelope struct {
		Type     string          `json:"type"`
		ItemID   string          `json:"item_id"`
		CallID   string          `json:"call_id"`
		Name     string          `json:"name"`
		Input    string          `json:"input"`
		Item     json.RawMessage `json:"item"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		if len(t.pending) != 0 {
			return nil, staticCriticalDiagnostic("malformed_pending_mekugi_event", "the upstream sent a malformed event while an HPATCH call was pending")
		}
		return [][]byte{payload}, nil
	}
	switch envelope.Type {
	case "response.created":
		visible := [][]byte{payload}
		for _, message := range t.subagentDeferred {
			visible = append(visible, assistantCommentaryDoneEvent(message))
			t.commentaryEmitted[jsonString(message, "id")] = struct{}{}
		}
		t.subagentDeferred = nil
		for _, publication := range t.deferredCommentary {
			if message := t.runtimeCommentaryMessage(publication); message != nil {
				visible = append(visible, assistantCommentaryDoneEvent(message))
			}
		}
		t.deferredCommentary = nil
		return visible, nil

	case "response.output_item.added":
		item, ok := decodeResponsesItem(envelope.Item)
		if !ok {
			return [][]byte{payload}, nil //nolint:nilerr // Unrelated output items pass through unchanged.
		}
		name := item.Name
		if t.codeModeToolName != "" && name == t.codeModeToolName {
			if item.Type == "custom_tool_call" && item.ID != "" {
				t.nativeExecCalls[item.ID] = item.cloneFields()
			}
			return [][]byte{payload}, nil
		}
		if item.Type == "function_call" {
			key := functionToolKey(item.Namespace, name)
			if _, instrumented := t.commentaryTools[key]; instrumented {
				itemID, callID := item.ID, item.CallID
				if itemID == "" || callID == "" {
					return nil, staticCriticalDiagnostic("malformed_commentary_call", "the upstream emitted a malformed commentary function call")
				}
				if len(t.pending) >= maxMekugiPendingCalls {
					return nil, staticCriticalDiagnostic("commentary_call_capacity", "the upstream commentary call capacity was exceeded")
				}
				if _, exists := t.pending[itemID]; exists || t.pendingCallKnown(callID) {
					return nil, staticCriticalDiagnostic("reused_commentary_call", "the upstream reused a commentary call identity")
				}
				t.pending[itemID] = mekugiPendingCall{
					callID: callID, toolName: name, structured: true, added: bytes.Clone(payload),
				}
				return nil, nil
			}
		}
		if !t.routesTool(name) {
			return [][]byte{payload}, nil
		}
		itemID, callID := item.ID, item.CallID
		if item.Type != "custom_tool_call" || itemID == "" || callID == "" {
			return nil, staticCriticalDiagnostic("malformed_mekugi_call", "the upstream emitted a malformed HPATCH call")
		}
		if len(t.pending) >= maxMekugiPendingCalls {
			return nil, staticCriticalDiagnostic("mekugi_call_capacity", "the upstream HPATCH call capacity was exceeded")
		}
		if _, exists := t.pending[itemID]; exists {
			return nil, staticCriticalDiagnostic("reused_mekugi_item", "the upstream reused an HPATCH item identity")
		}
		t.pending[itemID] = mekugiPendingCall{callID: callID, toolName: name, added: bytes.Clone(payload)}
		return nil, nil

	case "response.custom_tool_call_input.delta":
		if pending, ok := t.pending[envelope.ItemID]; ok && !pending.structured {
			// Translation needs the complete input, but Codex's SSE idle timer only
			// observes dispatched events. Preserve liveness without exposing the
			// untranslated input fragment.
			return [][]byte{[]byte(`{"type":"response.in_progress"}`)}, nil
		}
		return [][]byte{payload}, nil

	case "response.function_call_arguments.delta":
		if pending, ok := t.pending[envelope.ItemID]; ok && pending.structured {
			return [][]byte{[]byte(`{"type":"response.in_progress"}`)}, nil
		}
		if _, pending := t.pending[envelope.ItemID]; pending {
			return nil, unsupportedMekugiStreamEvent(envelope.Type)
		}
		return [][]byte{payload}, nil

	case "response.custom_tool_call_input.done":
		pending, ok := t.pending[envelope.ItemID]
		if !ok || pending.structured {
			if addedFields, nativeExec := t.nativeExecCalls[envelope.ItemID]; nativeExec {
				addedCallID := jsonString(addedFields, "call_id")
				if addedCallID != "" && envelope.CallID != "" && addedCallID != envelope.CallID {
					return nil, staticCriticalDiagnostic("changed_code_mode_call", "the upstream changed a Code Mode call identity")
				}
				callID := cmp.Or(addedCallID, envelope.CallID)
				addedFields["call_id"] = mustMarshalJSON(callID)
				original := maps.Clone(addedFields)
				original["input"] = mustMarshalJSON(envelope.Input)
				item := newResponsesItem(original)
				changed, err := t.transformOutputItem(&item)
				if err != nil {
					return nil, err
				}
				event := payload
				if changed {
					event, err = replaceRawField(payload, "input", item.fields["input"])
					if err != nil {
						return nil, err
					}
				}
				if err := t.commitLocalCall(callID); err != nil {
					return nil, err
				}
				return [][]byte{event}, nil
			}
			return [][]byte{payload}, nil
		}
		var addedEnvelope struct {
			Item json.RawMessage `json:"item"`
		}
		if json.Unmarshal(pending.added, &addedEnvelope) != nil {
			return nil, staticCriticalDiagnostic("malformed_buffered_mekugi_item", "Mekugi could not decode a buffered upstream item")
		}
		addedItem, ok := decodeResponsesItem(addedEnvelope.Item)
		if !ok {
			return nil, staticCriticalDiagnostic("malformed_buffered_mekugi_call", "Mekugi could not decode a buffered upstream call")
		}
		// input.done is already an executable handoff boundary. Retain the
		// original item shape now; output_item.done may never arrive.
		addedItem.setInput(envelope.Input)
		history, err := t.translateTool(pending.toolName, pending.callID, envelope.Input, addedItem.cloneFields())
		if err != nil {
			return nil, err
		}
		kind := history.effectiveCarrierKind()
		addedItem.renderCarrier(kind, history.carrierName, "")
		itemPayload, err := marshalProtocolJSON(addedItem)
		if err != nil {
			return nil, err
		}
		addedEvent, err := replaceRawField(pending.added, "item", itemPayload)
		if err != nil {
			return nil, err
		}
		doneEvent, err := renderCarrierDoneEvent(payload, kind, history.carrierInput())
		if err != nil {
			return nil, err
		}
		if err := t.commitLocalCall(pending.callID); err != nil {
			return nil, err
		}
		return [][]byte{addedEvent, doneEvent}, nil

	case "response.function_call_arguments.done":
		pending, ok := t.pending[envelope.ItemID]
		if !ok {
			return [][]byte{payload}, nil
		}
		if !pending.structured {
			return nil, unsupportedMekugiStreamEvent(envelope.Type)
		}
		if len(pending.argumentsDone) != 0 {
			return nil, staticCriticalDiagnostic("repeated_commentary_arguments", "the upstream repeated commentary argument completion")
		}
		pending.argumentsDone = bytes.Clone(payload)
		t.pending[envelope.ItemID] = pending
		return [][]byte{[]byte(`{"type":"response.in_progress"}`)}, nil

	case "response.output_item.done":
		item, ok := decodeResponsesItem(envelope.Item)
		if !ok {
			return [][]byte{payload}, nil //nolint:nilerr // Malformed unrelated output remains the upstream's responsibility.
		}
		t.collectProviderCommentary(item.fields)
		activityFields := maps.Clone(item.fields)
		if _, delivered := t.local[item.CallID]; item.Status == "incomplete" && !delivered {
			// Item completion can report interrupted generation, not complete input.
			delete(t.pending, item.ID)
			delete(t.nativeExecCalls, item.ID)
			return [][]byte{payload}, nil
		}
		itemID := item.ID
		callID := item.CallID
		if addedFields, nativeExec := t.nativeExecCalls[itemID]; nativeExec {
			expectedCallID := jsonString(addedFields, "call_id")
			if item.Type != "custom_tool_call" || item.Name != t.codeModeToolName ||
				expectedCallID != callID {
				return nil, staticCriticalDiagnostic("inconsistent_code_mode_call", "the upstream completed an inconsistent Code Mode call")
			}
		}
		delete(t.nativeExecCalls, itemID)
		if pending, buffered := t.pending[itemID]; buffered && pending.structured {
			if pending.callID != callID || len(pending.argumentsDone) == 0 {
				return nil, staticCriticalDiagnostic("inconsistent_commentary_call", "the upstream completed an inconsistent commentary function call")
			}
			message, err := t.transformStructuredCommentary(item.fields)
			if err != nil {
				return nil, err
			}
			var addedEnvelope struct {
				Item json.RawMessage `json:"item"`
			}
			var addedItem map[string]json.RawMessage
			if json.Unmarshal(pending.added, &addedEnvelope) != nil || json.Unmarshal(addedEnvelope.Item, &addedItem) != nil {
				return nil, staticCriticalDiagnostic("malformed_buffered_commentary_call", "Mekugi could not decode a buffered commentary call")
			}
			addedItem["arguments"] = item.fields["arguments"]
			addedPayload, err := marshalProtocolJSON(addedItem)
			if err != nil {
				return nil, err
			}
			addedEvent, err := replaceRawField(pending.added, "item", addedPayload)
			if err != nil {
				return nil, err
			}
			argumentsDone, err := replaceRawField(pending.argumentsDone, "arguments", item.fields["arguments"])
			if err != nil {
				return nil, err
			}
			itemPayload, err := marshalProtocolJSON(item)
			if err != nil {
				return nil, err
			}
			itemDone, err := replaceRawField(payload, "item", itemPayload)
			if err != nil {
				return nil, err
			}
			delete(t.pending, itemID)
			if err := t.commitLocalCall(callID); err != nil {
				return nil, err
			}
			t.collectSubagentToolCall(activityFields)
			if message != nil {
				return [][]byte{assistantCommentaryDoneEvent(message), addedEvent, argumentsDone, itemDone}, nil
			}
			return [][]byte{addedEvent, argumentsDone, itemDone}, nil
		}
		originalArguments := string(item.fields["arguments"])
		message, err := t.transformStructuredCommentary(item.fields)
		if err != nil {
			return nil, err
		}
		item = newResponsesItem(item.fields)
		changed, err := t.transformOutputItem(&item)
		if err != nil {
			return nil, err
		}
		if err := t.commitLocalCall(callID); err != nil {
			return nil, err
		}
		t.collectSubagentToolCall(activityFields)
		if !changed && message == nil && string(item.fields["arguments"]) == originalArguments {
			return [][]byte{payload}, nil
		}
		delete(t.pending, itemID)
		transformed, err := marshalProtocolJSON(item)
		if err != nil {
			return nil, err
		}
		event, err := replaceRawField(payload, "item", transformed)
		if err != nil {
			return nil, err
		}
		if message != nil {
			return [][]byte{assistantCommentaryDoneEvent(message), event}, nil
		}
		return [][]byte{event}, nil

	case "response.completed", "response.failed", "response.incomplete":
		clear(t.nativeExecCalls)
		if envelope.Type == "response.completed" {
			if len(t.pending) != 0 {
				return nil, staticCriticalDiagnostic("terminal_incomplete_mekugi_call", "the upstream completed with an incomplete HPATCH call")
			}
		} else {
			clear(t.pending)
		}
		transformed, usageMessage, err := t.transformResponse(envelope.Response, strings.TrimPrefix(envelope.Type, "response."))
		if err != nil {
			return nil, err
		}
		event, err := replaceRawField(payload, "response", transformed)
		if err != nil {
			return nil, err
		}
		var terminal struct {
			Status string `json:"status"`
		}
		if envelope.Type == "response.completed" && json.Unmarshal(transformed, &terminal) == nil && terminal.Status == "failed" {
			event, err = replaceRawField(event, "type", mustMarshalJSON("response.failed"))
		}
		if err != nil {
			return nil, err
		}
		if err := t.Finish(true); err != nil {
			return nil, err
		}
		visible := make([][]byte, 0, len(t.commentarySubscriptions)+1)
		var threadMessages []map[string]json.RawMessage
		for _, subscription := range t.commentarySubscriptions {
			if !subscription.handedOff {
				continue
			}
			for _, publication := range t.proxy.commentary.drain(subscription.token) {
				if message := t.runtimeCommentaryMessage(publication); message != nil {
					if t.subagentTurn {
						threadMessages = append(threadMessages, message)
					} else {
						visible = append(visible, assistantCommentaryDoneEvent(message))
					}
				}
			}
		}
		for _, publication := range t.proxy.drainThreadCommentarySession(t.historySessionID, t.shellThreadID) {
			if message := t.runtimeCommentaryMessage(publication); message != nil {
				if t.subagentTurn {
					threadMessages = append(threadMessages, message)
				} else {
					visible = append(visible, assistantCommentaryDoneEvent(message))
				}
			}
		}
		if len(threadMessages) != 0 {
			var response map[string]json.RawMessage
			var output []map[string]json.RawMessage
			if err := json.Unmarshal(transformed, &response); err != nil {
				return nil, err
			}
			if raw, exists := response["output"]; exists {
				if err := json.Unmarshal(raw, &output); err != nil {
					return nil, err
				}
			}
			response["output"] = mustMarshalJSON(append(threadMessages, output...))
			event, err = replaceRawField(event, "response", mustMarshalJSON(response))
			if err != nil {
				return nil, err
			}
		}
		t.releaseCommentarySubscriptions()
		if usageMessage != nil {
			visible = append(visible, assistantCommentaryDoneEvent(usageMessage))
		}
		visible = append(visible, t.finalAnswer.flush()...)
		visible = append(visible, event)
		return visible, nil

	default:
		if _, pending := t.pending[envelope.ItemID]; pending || t.pendingCallKnown(envelope.CallID) || t.routesTool(envelope.Name) || envelope.Name == applyPatchToolName {
			return nil, unsupportedMekugiStreamEvent(envelope.Type)
		}
		return [][]byte{payload}, nil
	}
}

func unsupportedMekugiStreamEvent(eventType string) error {
	underlying := fmt.Errorf("unsupported mekugi-related stream event %q", eventType)
	switch eventType {
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		return criticalDiagnostic(
			underlying,
			"unsupported_mekugi_stream_event:"+eventType,
			fmt.Sprintf("the upstream emitted unsupported HPATCH-related streaming event %q", eventType),
			false,
		)
	default:
		// Unknown event names are provider-controlled payload. Their lexical
		// shape alone cannot establish that they are safe to display.
		return criticalDiagnostic(underlying, "unsupported_mekugi_stream_event", "the upstream emitted an unsupported HPATCH-related streaming event", true)
	}
}

func (t *mekugiResponseTransform) pendingCallKnown(callID string) bool {
	for _, pending := range t.pending {
		if callID != "" && pending.callID == callID {
			return true
		}
	}
	return false
}

func (t *mekugiResponseTransform) transformResponse(payload []byte, terminalStatus string) ([]byte, map[string]json.RawMessage, error) {
	counts, observed := t.threadUsageCounts()
	var object map[string]json.RawMessage
	var usageMessage map[string]json.RawMessage
	var err error
	if terminalStatus == "" {
		object, usageMessage, err = responseWithTokenUsageCommentary(payload, counts, observed && t.usageObserved, "")
	} else {
		err = json.Unmarshal(payload, &object)
		if err == nil && object == nil {
			err = errors.New("decode mekugi-enabled response")
		}
		// Codex consumes completed items, not the terminal output snapshot.
		usageMessage = formatTokenUsageCommentary(payload, counts, observed && t.usageObserved,
			terminalStatus, t.finalAnswer.substantive && !t.finalAnswer.blocked && !t.finalAnswer.disabled)
	}

	if err != nil {
		return nil, nil, err
	}
	if usageMessage != nil && len(t.retainCommentary(usageMessage)) == 0 {
		var output []map[string]json.RawMessage
		if terminalStatus == "" && json.Unmarshal(object["output"], &output) == nil {
			id := jsonString(usageMessage, "id")
			output = slices.DeleteFunc(output, func(item map[string]json.RawMessage) bool { return jsonString(item, "id") == id })
			object["output"] = mustMarshalJSON(output)
		}
		usageMessage = nil
	}
	t.subagentResponses = t.retainCommentary(t.subagentResponses...)
	// SSE terminal events own completion even when the embedded status is absent.
	// JSON responses have no event envelope and retain body-status semantics.
	status := cmp.Or(terminalStatus, jsonString(object, "status"))
	interrupted := status == "failed" || status == "incomplete"
	var journalPrefix journalContinuation
	if t.journalActive {
		journalPrefix, _ = t.ctx.Value(journalContinuationKey{}).(journalContinuation)
	}
	if t.journalActive && terminalStatus != "" && (!interrupted || len(t.journalResults) != 0 || len(journalPrefix.clientOutput) != 0) {
		var output []map[string]json.RawMessage
		if err := decodeJournalOutput(object["output"], &output); err != nil {
			return nil, nil, errors.New("decode mekugi-enabled response output")
		}
		if len(output) == 0 {
			// A provider may leave the terminal snapshot empty after streaming
			// completed items. Rebuild it before adding journal results/notices:
			// a journal-only snapshot would replace WebSocket history and orphan
			// the next client tool result. Nonempty provider snapshots still own
			// their exact output, and the ordinary projection below restores carriers.
			object["output"] = mustMarshalJSON(t.journalProviderOutput)
		}
	}
	if rawOutput, ok := object["output"]; ok {
		var output []map[string]json.RawMessage
		if err := json.Unmarshal(rawOutput, &output); err != nil {
			return nil, nil, errors.New("decode mekugi-enabled response output")
		}
		if t.journalActive && terminalStatus == "" {
			t.journalProviderOutput = output
			for _, item := range output {
				if !isJournalCall(item) && blocksTokenUsage(item) {
					t.journalClientCalls = true
				}
			}
		}
		activityMessages := t.retainCommentary(t.drainActivity()...)
		t.activityMessages = append(t.activityMessages, activityMessages...)
		transformedOutput := append([]map[string]json.RawMessage{}, t.activityMessages...)
		for _, publication := range t.deferredCommentary {
			if message := t.runtimeCommentaryMessage(publication); message != nil {
				transformedOutput = append(transformedOutput, message)
			}
		}
		t.deferredCommentary = nil
		for _, message := range t.subagentResponses {
			transformedOutput = append(transformedOutput, message)
		}
		t.subagentDeferred = nil
		for _, fields := range output {
			if t.journalActive && isJournalCall(fields) {
				// A completed stream event can omit status. Its already-executed
				// result must survive a later interruption without running a new call.
				if !interrupted || jsonString(fields, "status") == "completed" || t.journalCalls[jsonString(fields, "call_id")] != nil {
					result, err := t.executeJournalCall(fields)
					if err != nil {
						return nil, nil, err
					}
					transformedOutput = append(transformedOutput, journalClientResult(result))
				}
				continue
			}
			item := newResponsesItem(fields)
			// An interrupted response can contain partial calls. Only complete items
			// or calls whose complete input was already delivered may be projected.
			_, delivered := t.local[item.CallID]
			if !delivered && (interrupted && item.Status != "completed" || item.Status == "in_progress" || item.Status == "incomplete") {
				transformedOutput = append(transformedOutput, fields)
				continue
			}
			t.collectProviderCommentary(item.fields)
			activityFields := maps.Clone(item.fields)
			message, err := t.transformStructuredCommentary(item.fields)
			if err != nil {
				return nil, nil, err
			}
			if message != nil {
				transformedOutput = append(transformedOutput, message)
			}
			item = newResponsesItem(item.fields)
			if _, err := t.transformOutputItem(&item); err != nil {
				return nil, nil, err
			}
			t.collectSubagentToolCall(activityFields)
			transformedOutput = append(transformedOutput, item.fields)
		}
		encoded, err := marshalProtocolJSON(transformedOutput)
		if err != nil {
			return nil, nil, err
		}
		if t.journalActive {
			t.journalClientOutput = transformedOutput
		}
		object["output"] = encoded
	}
	if t.journalActive {
		t.journalContinue = status == "completed" && len(t.journalResults) != 0 && !t.journalClientCalls && !t.journalTerminalReady()
		if len(journalPrefix.clientOutput) != 0 {
			var output []map[string]json.RawMessage
			if err := json.Unmarshal(object["output"], &output); err != nil {
				return nil, nil, err
			}
			object["output"] = mustMarshalJSON(append(slices.Clone(journalPrefix.clientOutput), output...))
		}
	}
	t.restoreResponseContract(object)
	// Every translated carrier in a JSON body is about to become visible,
	// regardless of whether the provider supplied a terminal response status.
	if err := t.commitHistory(); err != nil {
		return nil, nil, err
	}
	transformed, err := marshalProtocolJSON(object)
	if err == nil && usageMessage != nil && !t.journalActive {
		// Codex forwards only the child's final answer, not its preceding usage.
		t.proxy.activity.collect(t.threadID, jsonString(usageMessage, "id"), "usage", formatTokenUsageReport(counts))
	}
	return transformed, usageMessage, err
}

func (t *mekugiResponseTransform) restoreResponseContract(object map[string]json.RawMessage) {
	if _, ok := object["tools"]; ok {
		if !t.originalToolsPresent {
			delete(object, "tools")
		} else {
			object["tools"] = bytes.Clone(t.originalTools)
		}
	}
	if _, ok := object["tool_choice"]; ok {
		if !t.originalToolChoicePresent {
			delete(object, "tool_choice")
		} else {
			object["tool_choice"] = bytes.Clone(t.originalToolChoice)
		}
	}
}

func (t *mekugiResponseTransform) transformOutputItem(item *responsesItem) (bool, error) {
	name := item.Name
	if t.codeModeToolName != "" && name == t.codeModeToolName &&
		item.Type == "custom_tool_call" {
		callID := item.CallID
		var originalInput string
		if item.Input != nil {
			originalInput = *item.Input
		}
		if contribution, ok := t.proxy.registry.contribution("shell"); ok &&
			contribution.PluginID == builtinToolsPluginID && !t.nativeTools {
			retained, exists := t.local[callID]
			if exists && retained.toolName == "shell" || execShellRecovery(originalInput) {
				if callID == "" {
					return false, errors.New("Code Mode call has no call ID")
				}
				history, err := t.translateRegisteredTool(contribution, callID, originalInput, item.cloneFields())
				if err != nil {
					return false, err
				}
				item.renderCarrier(history.effectiveCarrierKind(), history.carrierName, history.carrierInput())
				return true, nil
			}
		}
		if retained, exists := t.local[callID]; exists && retained.toolName == codeModeCommentaryHistoryTool {
			if retained.script != originalInput {
				return false, fmt.Errorf("Code Mode commentary call %q changed input", callID)
			}
			retained.upstreamItem = item.cloneFields()
			t.local[callID] = retained
			item.setInput(retained.carrierPayload)
			return retained.carrierPayload != originalInput, nil
		}
		input, warningInput, changed, detected := nativeExecCommandInput(originalInput)
		outputWarning := ""
		if detected && warningInput == "" {
			outputWarning = nativeExecCommandWarning + "\n"
		}
		input, commentaryChanged, err := t.lowerCodeModeCommentary(callID, input)
		if err != nil {
			return false, err
		}
		changed = changed || commentaryChanged
		if t.proxy.commentaryEndpoint == "" && outputWarning == "" {
			if changed {
				item.setInput(input)
			}
			return changed, nil
		}
		if callID == "" {
			return false, errors.New("Code Mode call has no call ID")
		}
		// Retain the provider input before applying warning or commentary rewrites.
		history := mekugiHistory{
			toolName: codeModeCommentaryHistoryTool,
			script:   originalInput, carrierKind: codeModeCarrierCustom,
			carrierName: name, carrierPayload: input, upstreamItem: item.cloneFields(),
			outputWarning: outputWarning,
		}
		if !commentaryChanged {
			history.replayCarrier = true
			history.commentaryMessageIDs = []string{commentaryMessageID(callID)}
		}
		t.recordLocal(callID, &history)
		if changed {
			item.setInput(input)
		}
		return changed, nil
	}
	if !t.routesTool(name) {
		return false, nil
	}
	callID := item.CallID
	if item.Type != "custom_tool_call" || callID == "" {
		return false, fmt.Errorf("upstream emitted malformed %s call", name)
	}
	var input string
	if item.Input != nil {
		input = *item.Input
	}
	history, err := t.translateTool(name, callID, input, item.cloneFields())
	if err != nil {
		return false, err
	}
	item.renderCarrier(history.effectiveCarrierKind(), history.carrierName, history.carrierInput())
	return true, nil
}

func replaceRawField(payload []byte, name string, value json.RawMessage) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return nil, errors.New("decode stream event")
	}
	object[name] = value
	return marshalProtocolJSON(object)
}
