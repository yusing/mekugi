package executionobservation

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
	"time"

	"github.com/yusing/mekugi/modules/agentguidance/compact"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"
)

const applyPatchToolName = "apply_patch"

// Config supplies borrowed invocation owners. Startup owns their lifetimes.
type Config struct {
	ThreadControl    *threadbinding.Driver
	Registry         *frontends.Registry
	InputProjections []binding.InputProjection
	Usage            *accounting.Service
	Review           *sessionreview.Review
	Audience         *sessioncontrol.Audience
	Windows          *Windows
	NativeTrace      *nativetrace.Reader
	Control          *sessioncontrol.Observations
	PreviewTiming    previewbinding.Timing
	Calls            *history.CallCache
	Retained         changes.Scope
	Changes          changes.Service
	Checkpoints      *changecapture.Workspaces
	Recovery         *recovery.Service
	PublishChanges   func([]changes.Update)
	Notice           func(string, string, string, string)
}

// New binds request preparation to the composed owners without acquiring resources.
func New(config Config) *Preparer {
	return &Preparer{
		threadControl:    config.ThreadControl,
		registry:         config.Registry,
		inputProjections: config.InputProjections,
		usage:            config.Usage,
		review:           config.Review,
		activity:         config.Audience,
		execWindows:      config.Windows,
		nativeTrace:      config.NativeTrace,
		control:          config.Control,
		previewTiming:    config.PreviewTiming,
		calls:            config.Calls,
		retained:         config.Retained,
		changeSource:     config.Changes,
		snapshots:        config.Checkpoints,
		recovered:        config.Recovery,
		publishChanges:   config.PublishChanges,
		noticeSink:       config.Notice,
	}
}

type Preparer struct {
	threadControl    *threadbinding.Driver
	registry         *frontends.Registry
	inputProjections []binding.InputProjection
	usage            *accounting.Service
	review           *sessionreview.Review
	activity         *sessioncontrol.Audience
	execWindows      *Windows
	nativeTrace      *nativetrace.Reader
	control          *sessioncontrol.Observations
	previewTiming    previewbinding.Timing
	calls            *history.CallCache
	retained         changes.Scope
	changeSource     changes.Service
	snapshots        *changecapture.Workspaces
	recovered        *recovery.Service
	publishChanges   func([]changes.Update)
	noticeSink       func(string, string, string, string)
}

func (p *Preparer) notice(session, thread, category, message string) {
	if p != nil && p.noticeSink != nil {
		p.noticeSink(session, thread, category, message)
	}
}

type mekugiTranslationState struct {
	previews        map[string]*previewbinding.Stream
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
	localWaitItem    string
	compactReload    jsonv1.RawMessage
	activity         *sessioncontrol.Audience
	nativeTrace      *nativetrace.Reader
	generation       sessioncontrol.Generation
	toolTrace        requestexecution.Trace
	ctx              context.Context
	control          *sessioncontrol.Observations
	review           *sessionreview.Review
	execWindows      *Windows
	calls            *history.CallCache
	retained         changes.Scope
	changeSource     changes.Service
	snapshots        *changecapture.Workspaces
	recovered        *recovery.Service
	publishChanges   func([]changes.Update)
	previewTiming    previewbinding.Timing
	sessionID        string
	shellTurnID      string
	execGroup        string
	shellThreadID    string // Runtime identity remains available when activity attribution is invalid.
	model            string
	visible          map[string]mekugiHistory
	historySessionID string
	retainedRequest  *history.CallRequest
	threadID         string

	responseContract codexresponses.ResponseContract
	directory        string
	mekugiTranslationState

	commentaryAuthor string

	subagentTurn            bool
	liveDiffCompletionReady bool
	storageCompletionReady  bool
	storageToolHandoff      bool
	usageTracker            *accounting.Observation
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
	if t.control != nil {
		t.control.EndGeneration(&t.generation)
	}
	for itemID := range t.previews {
		t.endPreview(itemID)
	}
	t.retainedRequest.Finish(t.storageIdle)
}

// ObserveUsage records provider-authoritative token usage for this response.
func (t *mekugiResponseTransform) ObserveUsage(counts inference.Usage) {
	if t.usageObserved {
		return
	}
	t.usageTracker.Observe(counts)
	t.usageObserved = true
}

// Prewarm shares model projection but cannot initialize execution, replay, or
// agent lifecycle state. Only the non-generating WebSocket path selects it.
func (p *Preparer) prepareModelRequest(ctx context.Context, request *codexresponses.Request, sessionID, threadID string, metadata codexresponses.TurnMetadata, metadataValid, prewarm bool) (*mekugiResponseTransform, error) {
	var reload jsonv1.RawMessage
	var reloadCalls map[string]bool
	if bytes.Contains(request.Fields["input"], []byte(compact.Prefix)) {
		if !metadataValid {
			return nil, errors.New("compaction recovery requires valid metadata")
		}
		var err error
		reload, reloadCalls, err = p.prepareCompactInput(ctx, request, sessionID, threadID, metadata, prewarm)
		if err != nil {
			return nil, err
		}
	}
	reloadOutputs, err := p.compactSnapshots(ctx, request.Fields["input"], reloadCalls, threadID, metadata, prewarm)
	if err != nil {
		return nil, err
	}
	projectReload := func() error {
		if len(reloadCalls) == 0 {
			return nil
		}
		var items []map[string]jsontext.Value
		input, err := hydrateCompactSnapshots(request.Fields["input"], reloadOutputs)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(input, &items); err != nil {
			return err
		}
		input, err = compactReaderInput(items, reloadCalls)
		if err == nil {
			request.Fields["input"] = input
		}
		return err
	}
	if err := validateProviderContext(request.Fields["input"]); err != nil {
		return nil, err
	}
	if metadataValid && metadata.RequestKind == responseevents.Compaction {
		if err := codexresponses.ValidateCompaction(request, metadata); err != nil {
			return nil, err
		}
		if !prewarm {
			if err := p.consumeThreadInput(ctx, threadID, sessionID, request); err != nil {
				return nil, err
			}
		}
		if err := p.observeCompactionInput(ctx, request, sessionID, threadID, metadata); err != nil {
			if _, incompatible := errors.AsType[*inferencegateway.CompatibilityError](err); incompatible {
				return nil, err
			}
			p.notice(sessionID, threadID, "compaction_observation", "Completed host results could not be retained during compaction. Provider compaction is unchanged; change evidence may be incomplete.")
		}
		return nil, projectReload()
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
	if p.threadControl != nil {
		if err := binding.ValidateIndependentThreadCatalog(request); err != nil {
			return nil, err
		}
	}
	p.execWindows.markBackground(threadID, metadata.TurnID)
	// Execution-free requests retain their native instructions, tools, and schema.
	if codexresponses.ExecutionFree(request) {
		if !prewarm && strings.TrimSpace(threadID) == "" {
			return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
		}
		if !prewarm {
			if err := p.consumeThreadInput(ctx, threadID, sessionID, request); err != nil {
				return nil, err
			}
		}
		return nil, projectReload()
	}
	projectionInput, err := hydrateCompactSnapshots(request.Fields["input"], reloadOutputs)
	if err != nil {
		return nil, err
	}
	var projections []binding.PreparedInputProjection
	if !prewarm {
		for _, registration := range p.inputProjections {
			projections = append(projections, registration.Prepare(projectionInput))
		}
	}
	// Discover an execution owner before projecting tools: a native
	// handshake may not yet carry the instruction or tool catalog of a turn.
	if prewarm {
		tools := codexresponses.DecodeToolCatalog(request.Fields)
		execution, err := binding.PrepareStockExecution(request.Fields, tools, p.registry.FrontendGuidance, p.registry.Guidance)
		if err != nil {
			return nil, err
		}
		if execution == "" {
			return nil, projectReload()
		}
	}
	if p.registry.Guidance != nil {
		if err := requestexecution.ProjectInstructions(request, p.registry.Guidance.ForExecution); err != nil {
			return nil, err
		}
	}

	tools := codexresponses.DecodeToolCatalog(request.Fields)
	directory, _ := usableRoutingDirectory(metadata.Directories)
	if directory == "" && len(metadata.Directories) == 0 {
		// Host workspace metadata describes Git roots, not every admitted cwd.
		if workspace := p.review.WorkspaceForThread(threadID); workspace != "" {
			directory, _ = usableRoutingDirectory(map[string]jsonv1.RawMessage{workspace: nil})
		}
	}
	responseContract := codexresponses.CaptureResponseContract(request.Fields)
	codeModeToolName, err := binding.PrepareStockExecution(request.Fields, tools, p.registry.FrontendGuidance, p.registry.Guidance)
	if err != nil {
		return nil, err
	}
	if prewarm {
		return nil, projectReload()
	}
	if strings.TrimSpace(threadID) == "" {
		return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
	}
	historySessionID := directory + "\x00" + threadID
	retained, err := p.calls.Begin(historySessionID, threadID)
	if err != nil {
		return nil, err
	}
	release := func() {}
	if store := p.retained; store.Store != nil {
		ctx, release, err = beginRetainedSession(ctx, store.Store, store.Writes, store.Limits, threadID, sessionID)
	}
	if err == nil {
		err = retained.BindLease(release)
	}
	if err != nil {
		retained.Finish(false)
		return nil, requestexecution.StorageIOError(err)
	}
	if store := p.retained; store.Store != nil {
		ctx, err = prepareHandleScope(ctx, store.Store, store.Writes, metadata)
	}
	if err != nil {
		retained.Finish(false)
		return nil, err
	}
	visible, err := p.reconcileVisibleInput(ctx, request, directory, historySessionID)
	if err != nil {
		retained.Finish(false)
		return nil, err
	}
	var localWaitItem string
	if p.threadControl != nil {
		selected := observationScope(ctx, p.retained)
		session := history.Session{Store: selected.Store, Thread: selected.Thread, Namespace: selected.Namespace, Limits: selected.Limits, Writes: selected.Writes, Context: ctx}
		if err := p.threadControl.AdmitRequest(ctx, session, threadcontrol.Identity{Thread: threadID, Session: sessionID, Turn: metadata.TurnID}, metadata.SubagentKind == ""); err != nil {
			retained.Finish(false)
			return nil, err
		}
		instructions, err := p.threadControl.Instructions(ctx, threadID, request.Model())
		if err == nil && (p.registry.Guidance == nil || p.registry.Guidance.Threads == nil) {
			err = errors.New("independent thread instruction projection is unavailable")
		}
		if err == nil {
			err = requestexecution.ProjectInstructions(request, func(input binding.InstructionInput) (binding.InstructionUpdate, error) {
				return p.registry.Guidance.Threads(input, instructions)
			})
		}
		if err != nil {
			retained.Finish(false)
			return nil, err
		}
		if err := p.consumeThreadInput(ctx, threadID, sessionID, request); err != nil {
			retained.Finish(false)
			return nil, err
		}
		localWaitItem, err = p.observeThreadWait(ctx, threadID, metadata.TurnID, codeModeToolName, request, visible)
		if err != nil {
			retained.Finish(false)
			return nil, err
		}
	}
	// Only accepted requests may change retained ancestry or collect activity.
	activityThreadID := ""
	if metadata.ActivityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != threadID {
		p.activity.Invalidate(threadID)
	} else {
		name := metadata.AgentName
		if name == "" && metadata.SubagentKind == "" {
			name = "/root"
		}
		if p.activity.Observe(threadID, metadata.ParentThreadID, name, metadata.SubagentKind != "") {
			activityThreadID = threadID
		}
	}
	if activityThreadID != "" {
		model := request.Model()
		p.activity.BeginResponse(activityThreadID, grok.IsModel(model) || opencode.IsModel(model), time.Now())
	}
	transform := &mekugiResponseTransform{
		localWaitItem: localWaitItem,
		compactReload: reload,
		activity:      p.activity, nativeTrace: p.nativeTrace,
		ctx:         ctx,
		control:     p.control,
		review:      p.review,
		execWindows: p.execWindows,
		calls:       p.calls,
		retained:    p.retained, changeSource: p.changeSource, snapshots: p.snapshots, recovered: p.recovered, publishChanges: p.publishChanges,
		previewTiming:    p.previewTiming,
		sessionID:        sessionID,
		shellTurnID:      metadata.TurnID,
		shellThreadID:    threadID,
		model:            strings.TrimSpace(request.Model() + " " + request.ReasoningEffort()),
		historySessionID: historySessionID,
		visible:          visible,
		retainedRequest:  retained,
		threadID:         activityThreadID,

		responseContract: responseContract,
		directory:        directory,
		nativeExecCalls:  make(map[string]map[string]jsonv1.RawMessage),
		local:            make(map[string]mekugiHistory),
		commentaryAuthor: metadata.CommentaryAuthor(),
		subagentTurn:     metadata.SubagentKind != "",
		usageTracker:     p.usage.Observation(threadID, metadata.ThreadID, request.Model(), codexresponses.UsageServiceTier(request.Fields["service_tier"])),
		codeModeToolName: codeModeToolName,
		sessionShell:     codexresponses.SessionShell(request.Fields["input"]),
	}
	if transform.usageTracker != nil {
		transform.usageTracker.Reasoning = request.ReasoningEffort()
	}
	p.observeCodeModeFailures(activityThreadID, codeModeToolName, request, visible)
	if err := request.AppendApprovalFeedback(p.control.Feedback(threadID, metadata.TurnID)); err != nil {
		transform.Close()
		return nil, err
	}
	request.Fields["input"], err = hydrateCompactSnapshots(request.Fields["input"], reloadOutputs)
	if err != nil {
		transform.Close()
		return nil, err
	}
	reloadOutputs = nil
	for _, project := range projections {
		before := request.Fields["input"]
		result := project(before)
		if !bytes.Equal(before, result.Input) {
			request.Fields["input"] = result.Input
		}
		summary := result.Receipt
		if summary == nil {
			continue
		}
		transportcapture.ObserveOutputDeduplication(ctx, summary.OutputRepetitions, summary.SavedTokens, summary.TokensMeasured)
		for id, label := range summary.RepeatedAttachments {
			p.activity.Collect(activitypresentation.Supplement{Thread: activityThreadID, Source: "output-dedupe\x00" + id, Kind: "output_dedupe", CallID: id, Text: label})
		}
		if len(summary.RepeatedCalls) == 0 {
			continue
		}
		var originalItems []map[string]jsonv1.RawMessage
		_ = json.Unmarshal(request.OriginalField("input"), &originalItems)
		sources := make(map[string]string)
		for _, item := range originalItems {
			if responseevents.StringField(item, "type") == "custom_tool_call" {
				sources[responseevents.StringField(item, "call_id")] = responseevents.StringField(item, "input")
			}
		}
		for call, label := range summary.RepeatedCalls {
			target := call
			source := sources[call]
			if history, ok := visible[call]; ok {
				source = history.CarrierPayload
				if source == "" {
					source = history.Script
				}
			}
			if commands := p.nativeTrace.ReadCell(activityThreadID, call, source).ExecCommands(); len(commands) > 0 {
				target = commands[len(commands)-1]
			}
			p.activity.Collect(activitypresentation.Supplement{Thread: activityThreadID, Source: "output-dedupe\x00" + call, Kind: "output_dedupe", CallID: target, Text: label})
		}
	}
	if err := projectReload(); err != nil {
		transform.Close()
		return nil, err
	}
	return transform, nil
}

// Publish the native denial and its feedback together. A provider continuation
// cannot observe the denial without the user reply already being available.
