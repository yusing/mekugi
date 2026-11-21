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

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/inference"
	responseevents "github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

const applyPatchToolName = "apply_patch"

// Config supplies borrowed invocation owners. Startup owns their lifetimes.
type Config struct {
	ProgressSnapshot   func(string) *changepresentation.ReviewView
	ContextAttachments func(context.Context, string, string) (composer.AttachmentSnapshot, error)

	Usage          *accounting.Service
	Review         *sessionreview.Review
	Audience       *sessioncontrol.Audience
	Windows        *Windows
	NativeTrace    *nativetrace.Reader
	Control        *sessioncontrol.Observations
	PreviewTiming  previewbinding.Timing
	Calls          *history.CallCache
	Retained       changes.Scope
	Changes        changes.Service
	Checkpoints    *changecapture.Workspaces
	PublishChanges func([]changes.Update)
	Notice         func(string, string, string, string)
}

// New binds request preparation to the composed owners without acquiring resources.
func New(config Config) *Preparer {
	return &Preparer{Config: config}
}

type Preparer struct {
	Config
}

func (p *Preparer) notice(session, thread, category, message string) {
	if p != nil && p.Notice != nil {
		p.Notice(session, thread, category, message)
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
	owners           *Preparer
	compactReload    jsonv1.RawMessage
	generation       sessioncontrol.Generation
	generationOnly   bool
	toolTrace        requestexecution.Trace
	ctx              context.Context
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
	if t.owners.Control != nil {
		t.owners.Control.EndGeneration(&t.generation)
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
	var attachments attachmentPreparation
	if bytes.Contains(request.Fields["input"], []byte(compact.Prefix)) {
		if !metadataValid {
			return nil, errors.New("compaction recovery requires valid metadata")
		}
		prepared, err := p.prepareCompactInput(ctx, request, sessionID, threadID, metadata, prewarm)
		if err != nil {
			return nil, err
		}
		reload, reloadCalls = prepared.reader, prepared.calls
	}
	if metadataValid && !metadata.ActivityIdentityInvalid && (metadata.ThreadID == "" || metadata.ThreadID == threadID) {
		prepared, err := p.prepareAttachmentContext(ctx, request, threadID, metadata, prewarm)
		if err != nil {
			return nil, err
		}
		attachments = prepared
	}
	if reloadCalls == nil && bytes.Contains(request.Fields["input"], []byte(compact.SnapshotPrefix)) {
		reloadCalls = make(map[string]bool)
	}
	reloadOutputs, err := p.compactSnapshots(ctx, request.Fields["input"], reloadCalls, threadID, metadata, !prewarm)
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
	p.Windows.markBackground(threadID, metadata.TurnID)
	// Execution-free requests do not initialize capture.
	if codexresponses.ExecutionFree(request) {
		if !prewarm && strings.TrimSpace(threadID) == "" {
			return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
		}
		if !prewarm {
			input, err := hydrateCompactSnapshots(request.Fields["input"], reloadOutputs)
			if err != nil {
				return nil, err
			}
			request.Fields["input"] = input
			reloadOutputs = nil
		}
		return nil, projectReload()
	}
	// Recognize the native execution tool for observation, without changing its
	// catalog or assuming that a prewarm handshake has tools at all.
	codeModeToolName, err := binding.StockExecutionTool(codexresponses.DecodeToolCatalog(request.Fields))
	if err != nil {
		return nil, err
	}
	if prewarm {
		return nil, projectReload()
	}
	directory := p.observationDirectory(metadata.Directories, threadID)
	responseContract := codexresponses.CaptureResponseContract(request.Fields)
	if strings.TrimSpace(threadID) == "" {
		return nil, errors.New("mekugi rewrite requires a valid Codex thread ID")
	}
	historySessionID := directory + "\x00" + threadID
	retained, err := p.Calls.Begin(historySessionID, threadID)
	if err != nil {
		return nil, err
	}
	release := func() {}
	if store := p.Retained; store.Store != nil {
		ctx, release, err = beginRetainedSession(ctx, store.Store, store.Writes, store.Limits, threadID, sessionID)
	}
	if err == nil {
		err = retained.BindLease(release)
	}
	if err != nil {
		retained.Finish(false)
		return nil, requestexecution.StorageIOError(err)
	}
	if store := p.Retained; store.Store != nil {
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
	if len(reload) == 0 {
		if err := p.prepareAttachmentHints(ctx, request, attachments); err != nil {
			retained.Finish(false)
			return nil, err
		}
	}
	activityThreadID := p.admitActivityThread(threadID, metadata)
	if activityThreadID != "" {
		p.Audience.BeginResponse(activityThreadID, false, time.Now())
	}
	transform := &mekugiResponseTransform{
		compactReload:    reload,
		owners:           p,
		ctx:              ctx,
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
		usageTracker:     p.Usage.Observation(threadID, metadata.ThreadID, request.Model(), codexresponses.UsageServiceTier(request.Fields["service_tier"])),
		codeModeToolName: codeModeToolName,
		sessionShell:     codexresponses.SessionShell(request.Fields["input"]),
	}
	if transform.usageTracker != nil {
		transform.usageTracker.Reasoning = request.ReasoningEffort()
	}
	p.observeCodeModeFailures(activityThreadID, codeModeToolName, request, visible)
	if err := request.AppendApprovalFeedback(p.Control.Feedback(threadID, metadata.TurnID)); err != nil {
		transform.Close()
		return nil, err
	}
	request.Fields["input"], err = hydrateCompactSnapshots(request.Fields["input"], reloadOutputs)
	if err != nil {
		transform.Close()
		return nil, err
	}
	reloadOutputs = nil
	if err := projectReload(); err != nil {
		transform.Close()
		return nil, err
	}
	return transform, nil
}

// Publish the native denial and its feedback together. A provider continuation
// cannot observe the denial without the user reply already being available.

// Only accepted requests may change retained ancestry or collect activity.
func (p *Preparer) admitActivityThread(threadID string, metadata codexresponses.TurnMetadata) string {
	activityThreadID := ""
	if metadata.ActivityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != threadID {
		p.Audience.Invalidate(threadID)
	} else {
		name := metadata.AgentName
		if name == "" && metadata.SubagentKind == "" {
			name = "/root"
		}
		if p.Audience.Observe(threadID, metadata.ParentThreadID, name, metadata.SubagentKind != "") {
			activityThreadID = threadID
		}
	}
	return activityThreadID
}
