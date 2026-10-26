package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	"slices"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

// NativeFrame carries the existing renderer's detached owner publications. It
// contains no renderer, source store, host connection or execution capability.
// Draft and prompt-editor contents are removed at the publication boundary.
type NativeFrame struct {
	Processes         []sessionpresentation.Process `json:"processes"`
	CompletionCatalog uint64                        `json:"completionCatalog"`
	CompletionTarget  conversation.EditorTarget     `json:"completionTarget"`
	Questions         []prompt.CallView             `json:"questions"`
	Approvals         []prompt.ApprovalView         `json:"approvals"`
	View              sessionpresentation.View      `json:"view"`
	Main              *activity.Publication         `json:"main"`
	Agents            *activity.Publication         `json:"agents"`
	ChildThreads      []string                      `json:"childThreads,omitzero"`
	Review            *changes.ReviewView           `json:"review"`
	Live              []preview.Preview             `json:"live"`
	LiveScope         preview.Scope                 `json:"liveScope"`
	Details           *NativeDetails                `json:"details"`
	MainOutput        []activity.OutputResult       `json:"mainOutput"`
	AgentOutput       []activity.OutputResult       `json:"agentOutput"`
	// ReacquireOutput marks compacted transient hydration. Consumers retain
	// unresolved demands and reacquire them through identity-addressed reads.
	ReacquireOutput bool                 `json:"reacquireOutput,omitzero"`
	FileLinks       map[string]bool      `json:"fileLinks"`
	Switch          *NativeSwitch        `json:"switch"`
	Facts           NativeSessionFacts   `json:"facts"`
	Effects         []NativeEffect       `json:"effects"`
	Activity        *NativeActivityFrame `json:"activity,omitempty"`
	Notification    *NativeEffect        `json:"notification,omitempty"`
	PendingEffects  bool                 `json:"pendingEffects"`
	Notice          string               `json:"notice,omitempty"`
}

// NativeActivityFrame retains a bounded window of renderer pacing deliveries.
// Reset identifies the last compacted delivery: a client behind that watermark
// clears only pacing overlays and catches up from Main/Agents and their retained
// output identities. Semantic Effects are never compacted with this window.
type NativeActivityFrame struct {
	Reset   uint64
	Effects []NativeEffect
}

type NativeDetails struct {
	Source   sessionpresentation.Thread        `json:"source"`
	Selected string                            `json:"selected,omitempty"`
	View     sessionpresentation.DetailView    `json:"view"`
	Output   *sessionpresentation.DetailOutput `json:"output"`
	File     *activity.DetailFile              `json:"file"`
	Agent    bool                              `json:"agent"`
}

// NativeSwitch explicitly identifies a successful replacement. Other viewers
// are never implicitly authorized to address the replacement thread.
type NativeSwitch struct {
	From, To, Origin string
}

type NativeEvent struct {
	Attachment string      `json:"attachment"`
	Startup    string      `json:"startup,omitzero"`
	Frame      NativeFrame `json:"frame"`
}

// NativeTarget captures the publication that produced an input action. A later
// stream update cannot retarget an already selected command or dialog action.
type NativeTarget struct {
	Attachment, Thread, Startup string
	Version                     sessionpresentation.Version
}

func (e NativeEvent) Target() NativeTarget {
	return NativeTarget{Attachment: e.Attachment, Thread: e.Frame.View.Main.ID, Startup: e.Startup, Version: e.Frame.View.Version}
}

// NativeOperation uses existing semantic intents, not terminal keys. Exactly
// one operation is admitted by transport; its owner applies ordinary policy.
type NativeOperation struct {
	StopProcess  *sessionpresentation.Process             `json:"stopProcess,omitzero"`
	SaveDraft    *string                                  `json:"saveDraft,omitzero"`
	Completion   *NativeEditorInput                       `json:"completion,omitzero"`
	Preferences  *NativePreferenceUpdate                  `json:"preferences,omitzero"`
	Focused      *bool                                    `json:"focused,omitzero"`
	Editor       *NativeEditorInput                       `json:"editor,omitzero"`
	Command      *string                                  `json:"command,omitzero"`
	Composer     *NativeComposerInput                     `json:"composer,omitzero"`
	Menu         *sessionpresentation.MenuAction          `json:"menu,omitzero"`
	Resume       *sessionpresentation.ResumeAction        `json:"resume,omitzero"`
	Panel        *sessionpresentation.PanelAction         `json:"panel,omitzero"`
	Display      *sessionpresentation.DisplayIntent       `json:"display,omitzero"`
	Interrupt    *sessionpresentation.InterruptIntent     `json:"interrupt,omitzero"`
	Side         *sessionpresentation.CloseSideIntent     `json:"side,omitzero"`
	BTW          *sessionpresentation.CloseSideIntent     `json:"btw,omitzero"`
	Answers      *NativeAnswers                           `json:"answers,omitzero"`
	Approval     *sessionpresentation.ApprovalIntent      `json:"approval,omitzero"`
	Output       *NativeOutputDemand                      `json:"output,omitzero"`
	DetailOutput *sessionpresentation.DetailOutputRequest `json:"detailOutput,omitzero"`
	Details      *NativeDetailRequest                     `json:"details,omitzero"`
	CloseDetails *sessionpresentation.DetailID            `json:"closeDetails,omitzero"`
	Notice       *NativeNoticeAction                      `json:"notice,omitzero"`
	Receipt      *NativeReceipt                           `json:"receipt,omitzero"`
	Recovery     *NativeRecoveryInput                     `json:"recovery,omitzero"`
	Reference    *sessionpresentation.ReferenceIntent     `json:"reference,omitzero"`
}

// NativeEditorInput is ephemeral request metadata for an operation that may
// insert into the requesting editor. It is never retained as a server editor.
type NativeEditorInput struct {
	Target conversation.EditorTarget
	Draft  composer.Draft
	// Explicit reopens a dismissed completion; other editor operations ignore it.
	Explicit bool `json:",omitzero"`
}

type NativePreferenceUpdate struct {
	Presentation jsontext.Value
	Flush        bool
}

// NativeSessionFacts are detached values from the invocation's existing history,
// preference, notification and accounting owners. Clients retain no new ledger.
type NativeSessionFacts struct {
	History                          []composer.HistoryEntry
	Preferences                      jsontext.Value `json:"preferences,omitzero"`
	Usage                            NativeUsage
	DebugDirectory                   string
	Diagnostics                      string
	Archived                         bool
	Notifications                    NativeNotificationSettings
	PredictionThread, PredictionTurn string
	ActionRequired                   bool
	Busy                             bool
}

type NativeUsage struct {
	TotalTokens, InputTokens, OutputTokens, CachedInputTokens, ReasoningOutputTokens uint64
}

type NativeNotificationSettings struct {
	Ready             bool
	Events            jsontext.Value
	Method, Condition string
}

// NativeComposerInput is a detached client editor value, never a server editor.
type NativeComposerInput struct {
	Intent sessionpresentation.ComposerIntent
	Draft  composer.Draft
	Input  *InputRequest `json:"input,omitzero"`
	// Consumed means the originating client has already taken its local draft.
	Consumed bool
	// Detached input never occupied the ordinary editor, so command completion
	// must not remove text from that editor.
	Detached bool `json:",omitzero"`
}

// NativeAnswers is one final answer admission. Secret text exists only in this
// authenticated local request, never in a publication or retained prompt editor.
type NativeAnswers struct {
	public     *Answer
	Call       string
	Answers    []NativeQuestionAnswer
	Incomplete bool
}

// PublicAnswer returns the public route value for resolution by the pending prompt owner.
func (a NativeAnswers) PublicAnswer() *Answer { return a.public }

type NativeQuestionAnswer struct {
	prompt.AnswerInput
	Secret string
}

type NativeDetailKind string

const (
	NativeEntryDetails NativeDetailKind = "entry"
	NativeFileDetails  NativeDetailKind = "file"
	NativeErrorDetails NativeDetailKind = "error"
	NativeSkillDetails NativeDetailKind = "skills"
)

type NativeDetailRequest struct {
	Displayed    sessionpresentation.Thread `json:",omitzero"`
	Kind         NativeDetailKind
	Source       uint64
	Output       int
	Sources      []uint64
	Agent        bool
	Path, Caller string
}

type NativeNoticeAction struct {
	Version  sessionpresentation.Version
	Feedback uint64
	Dismiss  bool
	Text     string
}

// NativeEffect belongs to one view and is retained until its ID is acknowledged.
// Restore prepends to the current local draft through composer.Editor.Restore;
// Action uses the existing editor action owner. Clipboard is raw text, not OSC.
type NativeEffect struct {
	ID           uint64
	Thread       string
	Target       *conversation.EditorTarget
	Skills       *[]composer.Token
	Restore      *composer.Draft
	Recall       *string
	ShellRestore *composer.Draft
	Recovery     *NativeRecoveryView
	Action       *composer.Action
	Clipboard    *string
	Selection    *NativeSelection
	Presentation *NativePresentationEffect
	Deliveries   []sessionpresentation.ActivityDelivery
	Notification *NativeDesktopNotification
	Reattach     *string
}

type NativeDesktopNotification struct{ Event, Message string }

type NativeRecoveryInput struct {
	Effect uint64         `json:"effect"`
	Action RecoveryAction `json:"action"`
	Text   *[]string      `json:"text,omitzero"`
}

type RecoveryAction string

const (
	RetryRecovery   RecoveryAction = "retry"
	EditRecovery    RecoveryAction = "edit"
	DiscardRecovery RecoveryAction = "discard"
)

type NativeRecoveryView struct {
	Queued      bool     `json:"queued,omitzero"`
	Editing     bool     `json:"editing,omitzero"`
	Parts       []string `json:"parts,omitzero"`
	Preview     string   `json:"preview"`
	Attachments bool     `json:"attachments"`
	Shell       bool     `json:"shell"`
}

// RecoveryDraft distinguishes unsent submissions from targeted editor changes.
func (e NativeEffect) RecoveryDraft() *composer.Draft {
	if e.Target != nil || e.Action != nil {
		return nil
	}
	if e.ShellRestore != nil {
		return e.ShellRestore
	}
	return e.Restore
}

type NativePresentationEffect struct {
	LiveHidden, InterruptLocked, PredictionsDisabled bool
}

type NativeSelection struct {
	Description, Text, Source string
	Pending                   []composer.Draft
}

type NativeNoticeReceipt struct {
	Claim                string
	Attachment, Feedback uint64
	Composer, Content    bool
}

type NativeReceipt struct {
	Effect uint64
	Notice NativeNoticeReceipt
}

type NativeOutputDemand struct {
	Agent   bool                    `json:"agent"`
	Demands []activity.OutputDemand `json:"demands"`
}

type NativeMutation struct {
	Attachment string                      `json:"attachment"`
	Version    sessionpresentation.Version `json:"version"`
	Operation  NativeOperation             `json:"operation"`
}

// NativeRequest arrives on the existing serialized execution-owner loop.
// An Attach request acquires only a presentation scope. ViewContext follows
// that stream and retires the scope independently of execution. Context bounds
// the individual admission request, including attachment acquisition.
// Reply is buffered so completing an admitted operation never waits for a client.
type NativeRequest struct {
	authority         viewAuthority
	Attach            bool
	Attachment        string
	Startup           string
	Thread, Workspace string
	Context           context.Context
	ViewContext       context.Context
	Version           sessionpresentation.Version
	Operation         NativeOperation
	Prepared          *PreparedInput
	Reply             chan string
}

// RemoteHuman reports authority established by the public route, never request JSON.
func (r NativeRequest) RemoteHuman() bool { return r.authority == remoteHumanView }

func (f NativeFrame) withoutEditors() NativeFrame {
	f.View.Draft = conversation.DraftView{Available: f.View.Draft.Available, Prediction: f.View.Draft.Prediction}
	f.View.PromptDraft = conversation.PromptDraftView{
		Call: f.View.PromptDraft.Call, Question: f.View.PromptDraft.Question, Request: f.View.PromptDraft.Request,
	}
	if call := f.View.Questions.Call; call != nil {
		call = new(*call)
		call.Questions = slices.Clone(call.Questions)
		for i := range call.Questions {
			if call.Questions[i].Secret {
				call.Questions[i].Answer = nil
			}
		}
		f.View.Questions.Call = call
	}
	f.Questions = slices.Clone(f.Questions)
	for i := range f.Questions {
		f.Questions[i].Questions = slices.Clone(f.Questions[i].Questions)
		for j := range f.Questions[i].Questions {
			if f.Questions[i].Questions[j].Secret {
				f.Questions[i].Questions[j].Answer = nil
			}
		}
	}
	return f
}

func (o NativeOperation) valid() bool {
	count := 0
	for _, present := range []bool{o.SaveDraft != nil, o.Completion != nil, o.Preferences != nil, o.Focused != nil, o.Command != nil, o.Composer != nil, o.Menu != nil, o.Resume != nil,
		o.Panel != nil, o.Display != nil, o.Interrupt != nil, o.StopProcess != nil,
		o.Side != nil, o.BTW != nil, o.Answers != nil,
		o.Approval != nil, o.Output != nil, o.DetailOutput != nil, o.Details != nil,
		o.CloseDetails != nil, o.Notice != nil, o.Receipt != nil, o.Reference != nil, o.Recovery != nil} {
		if present {
			count++
		}
	}
	return count == 1
}
