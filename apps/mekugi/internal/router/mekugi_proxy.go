package router

import (
	"bytes"
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	"github.com/yusing/mekugi/contracts/inference"
	segments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	jsonv1 "encoding/json"

	json "encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const applyPatchToolName = "apply_patch"

type mekugiProxy struct {
	registry         *frontends.Registry
	inputProjections []binding.InputProjection
	usage            *accounting.Service
	review           *savedReview
	activity         *subagentActivity
	guard            *shellApprovalServer
	execTrack        *segments.Observer // Set for the UI when command shells are tracked.
	skillsManager    bool
	execWindows      *execWindowRegistry
	nativeTrace      *nativetrace.Reader
	control          sessioncontrol.Observations
	calls            *history.CallCache
	replayStore      *mekugiReplayStore
	noticeSink       func(string, string, string, string)
}

func newMekugiProxy(registry *frontends.Registry, projections []binding.InputProjection, usage *accounting.Service, service *history.Service) *mekugiProxy {
	if registry == nil {
		return nil
	}
	activity := newSubagentActivity()
	proxy := &mekugiProxy{
		registry:         registry,
		calls:            service.Calls(),
		review:           newSavedReview(context.Background(), nil),
		inputProjections: slices.Clone(projections),
		usage:            usage,
		activity:         activity,
		execWindows:      &execWindowRegistry{},
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
	p.calls.StopAdmission()
	if p.execTrack != nil {
		p.execTrack.Close()
	}
	p.guard.close()
	p.calls.Close()
	p.activity.close()
	return nil
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
	generation       sessioncontrol.Generation
	toolTrace        requestexecution.Trace
	ctx              context.Context
	proxy            *mekugiProxy
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
	if t.proxy != nil {
		t.proxy.control.EndGeneration(&t.generation)
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
func (p *mekugiProxy) prepareModelRequest(ctx context.Context, request *codexresponses.Request, sessionID, threadID string, metadata codexresponses.TurnMetadata, metadataValid, prewarm bool) (*mekugiResponseTransform, error) {
	if err := validateProviderContext(request.Fields["input"]); err != nil {
		return nil, err
	}
	if metadataValid && metadata.RequestKind == responseevents.Compaction {
		if err := codexresponses.ValidateCompaction(request, metadata); err != nil {
			return nil, err
		}
		if err := p.observeCompactionInput(ctx, request, sessionID, threadID, metadata); err != nil {
			if _, incompatible := errors.AsType[*inferencegateway.CompatibilityError](err); incompatible {
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
	if codexresponses.ExecutionFree(request) {
		if !prewarm && strings.TrimSpace(threadID) == "" {
			return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
		}
		return nil, nil
	}
	var projections []binding.PreparedInputProjection
	if !prewarm {
		for _, registration := range p.inputProjections {
			projections = append(projections, registration.Prepare(request.Fields["input"]))
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
			return nil, nil
		}
	}
	if p.registry.Guidance != nil {
		if err := requestexecution.ProjectInstructions(request, p.registry.Guidance.ForExecution); err != nil {
			return nil, err
		}
	}

	recipient := metadata.AgentName
	if metadata.SubagentKind == "" {
		recipient = "/root"
	}
	if metadata.SubagentKind != "" && metadata.ActivityIdentityInvalid {
		recipient = ""
	}

	// Projection deduplication needs the original visible messages. Keep this
	// request-local result until replay validation succeeds and strips known copies.
	envelopes := prepareSubagentInputEnvelopes(request.Fields, recipient)

	tools := codexresponses.DecodeToolCatalog(request.Fields)
	directory, _ := usableRoutingDirectory(metadata.Directories)
	responseContract := codexresponses.CaptureResponseContract(request.Fields)
	codeModeToolName, err := binding.PrepareStockExecution(request.Fields, tools, p.registry.FrontendGuidance, p.registry.Guidance)
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
	retained, err := p.calls.Begin(historySessionID, threadID)
	if err != nil {
		return nil, err
	}
	ctx, release, err := p.replayStore.beginSession(ctx, threadID, sessionID)
	if err == nil {
		err = retained.BindLease(release)
	}
	if err != nil {
		retained.Finish(false)
		return nil, storageIOError(err)
	}
	ctx, err = p.replayStore.prepareHandleScope(ctx, metadata)
	if err != nil {
		retained.Finish(false)
		return nil, err
	}
	visible, err := p.reconcileVisibleInput(ctx, request, directory, historySessionID)
	if err != nil {
		retained.Finish(false)
		return nil, err
	}
	// Only accepted requests may change retained ancestry or collect activity.
	activityThreadID := ""
	if metadata.ActivityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != threadID {
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
	if activityThreadID != "" {
		model := request.Model()
		p.activity.beginResponse(activityThreadID, grok.IsModel(model) || opencode.IsModel(model), time.Now())
	}
	transform := &mekugiResponseTransform{
		ctx:              ctx,
		proxy:            p,
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
	// Continuation advice is appended to the output the model will see.
	retainedExecution := make(map[string]binding.RetainedExecutionItem, len(visible))
	for id, call := range visible {
		retainedExecution[id] = binding.RetainedExecutionItem{Evidence: call.executionEvidence(true), UpstreamItem: call.UpstreamItem}
	}
	binding.ProjectExecutionContinuations(request, tools, codeModeToolName, retainedExecution)
	if err := request.AppendApprovalFeedback(p.control.Feedback(threadID, metadata.TurnID)); err != nil {
		transform.Close()
		return nil, err
	}
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
			p.activity.collectEvent(activityEvent{thread: activityThreadID, source: "output-dedupe\x00" + call, kind: "output_dedupe", callID: target, text: label})
		}
	}
	return transform, nil
}

// Publish the native denial and its feedback together. A provider continuation
// cannot observe the denial without the user reply already being available.
