package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	noticepresentation "github.com/yusing/mekugi/modules/notifications/presentation"
	"golang.org/x/term"
)

// runNativeClient attaches a renderer to server-owned execution. Closing this
// client releases its view without canceling the admitted runtime.
func runNativeClient(ctx context.Context, socket string, routerArgs, args []string, stdin, stdout *os.File) error {
	_, resume, _, err := appServerArgs(expandReasoningShortcuts(args))
	if err != nil {
		return err
	}
	options, err := launchoptions.Parse(routerArgs)
	if err != nil {
		return err
	}
	settings, err := launchoptions.LoadSettings(options.ThirdParty)
	if err != nil {
		return err
	}
	options = options.ResolveFeatures(settings.Features)
	views, err := composition.NativeRenderer(options.ChildReplySummary)
	if err != nil {
		return err
	}
	faint, err := terminalui.SupportsFaint(ctx, options.ANSIFaint)
	if err != nil {
		return err
	}
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	if socket == "" {
		socket, err = sessionapi.ControlSocketPath()
		if err != nil {
			return err
		}
	}
	if err := ensureNativeServer(ctx, socket); err != nil {
		return err
	}
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var connection *sessionapi.NativeConnection
	if resume != "" && resume != "--last" && resume != "--pick" {
		connection, err = sessionapi.OpenNative(ctx, client, "http://mekugi", resume)
		if err != nil {
			refusal, ok := errors.AsType[*sessionapi.NativeFailure](err)
			if !ok || refusal.Status != http.StatusNotFound {
				return err
			}
		}
	}
	if connection == nil {
		connection, err = sessionapi.StartNative(ctx, client, "http://mekugi", sessionapi.NativeStart{Workspace: workspace, RouterArgs: routerArgs, Args: args})
		if err != nil {
			return err
		}
	}
	defer connection.Close()
	terminal, _ := views.NewAttachment(diffrender.EnvironmentTheme(os.Getenv("COLORFGBG")))
	terminal.Faint = faint
	c := &nativeClient{connection: connection, renderer: nativeClientRenderer{terminal: terminal, views: views}, editors: make(map[string]*composer.Editor)}
	c, err = c.run(ctx, stdin, stdout)
	if diagnostic := c.event.Frame.Facts.Diagnostics; diagnostic != "" {
		fmt.Fprintln(stdout, textsanitize.Safe(diagnostic, false))
	}
	if c.editor("main").Text != "" {
		fmt.Fprintln(stdout, "Unsent draft:\n"+textsanitize.Safe(c.editor("main").Text, false))
	}
	var retained []string
	for identity, editor := range c.runtime.editors {
		if editor != c.editor("main") && editor.Text != "" {
			retained = append(retained, "Unsent draft (session "+identity[1]+"):\n"+editor.Text)
		}
	}
	slices.Sort(retained)
	for _, draft := range retained {
		fmt.Fprintln(stdout, textsanitize.Safe(draft, false))
	}
	if c.notice != "" {
		fmt.Fprintln(stdout, textsanitize.Safe(c.notice, false))
	}
	if c.feedback.Text != "" {
		fmt.Fprintln(stdout, textsanitize.Safe(c.feedback.Text, false))
	}
	for _, draft := range c.unknown {
		origin := "session " + draft.Thread
		if draft.Prompt != "" {
			origin += ", prompt " + draft.Prompt
		}
		fmt.Fprintln(stdout, "Submission outcome unknown ("+textsanitize.Safe(origin, false)+"); not automatically resent:\n"+textsanitize.Safe(draft.Text, false))
	}
	facts := c.event.Frame.Facts
	return errors.Join(err, writeExitSummary(binding.TokenUsage(facts.Usage), facts.Archived, c.event.Frame.View.Main.ID, appServerResumeArgv(os.Args[0], routerArgs, args), facts.DebugDirectory, stdout, term.IsTerminal(int(stdout.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"))
}

type nativeUnknownInput struct {
	composer.Draft
	Thread string
	Prompt string
}

type nativeClientCall struct {
	owner      *nativeClient
	target     sessionapi.NativeTarget
	operation  sessionapi.NativeOperation
	next       *terminalsession.KeyContinuation
	nextInput  sessionpresentation.Thread
	nextView   sessionpresentation.Thread
	nextEditor conversation.EditorTarget
	after      func(error)
	result     sessionapi.NativeResult
	complete   bool
	hydrated   bool
	unresolved []activity.OutputDemand
	reading    *activity.OutputDemand
	reacquire  bool
	outputLost bool
	err        error
	cancel     context.CancelFunc
	discard    bool
}

type nativeClient struct {
	ctx                    context.Context
	workers                sync.WaitGroup
	connection             *sessionapi.NativeConnection
	renderer               nativeClientRenderer
	event                  sessionapi.NativeEvent
	editors                map[string]*composer.Editor
	prompts                nativeClientPrompts
	handoff                *nativeEditorHandoff
	secretKey              string
	secrets                map[string]composer.State
	calls                  []*nativeClientCall
	runtime                *nativeClientRuntime
	stop                   context.CancelFunc
	closed                 bool
	ackEffect              uint64
	input                  []nativeClientInput
	selection              *nativeSelectionCapture
	pasteEditor            *composer.Editor
	pasteTarget            conversation.EditorTarget
	pasteSecret            bool
	unknown                []nativeUnknownInput
	notice                 string
	feedback               noticepresentation.Model
	streams                terminalsession.Streams
	notifications          nativeNotifications
	preferences            terminalsession.PreferenceEncoder
	preferenceState        terminalsession.Preferences
	preferenceReady        bool
	completionTarget       conversation.EditorTarget
	completionInput        string
	completionReady        bool
	completionCatalog      uint64
	prediction             [3]string
	predictionRevision     uint64
	detailHydrating        bool
	localDetails           sessionpresentation.Details
	localDetailID          sessionpresentation.DetailID
	effect                 uint64
	receipt                sessionapi.NativeNoticeReceipt
	receiptEffect          uint64
	links                  markdownLinkChecks
	ownedImages            map[string]bool
	clipboardPending       bool
	restoration            *nativeRestoredEffect
	reattach               string
	attachOperation        *sessionapi.NativeOperation
	attaching              bool
	quit, dirty, hydrating bool
}

func (c *nativeClient) editor(key string) *composer.Editor {
	if c.editors == nil {
		c.editors = make(map[string]*composer.Editor)
	}
	if c.editors[key] == nil {
		thread := ""
		if key == "main" {
			thread = c.event.Frame.View.Main.ID
		} else if len(key) > 5 && key[:5] == "side:" {
			thread = key[5:]
		}
		if c.runtime != nil && thread != "" {
			identity := [2]string{c.event.Frame.View.Info.Workspace, thread}
			if c.runtime.editors[identity] == nil {
				c.runtime.editors[identity] = new(composer.Editor)
			}
			c.editors[key] = c.runtime.editors[identity]
		} else {
			c.editors[key] = new(composer.Editor)
		}
	}
	return c.editors[key]
}

func (c *nativeClient) selectedEditor() (*composer.Editor, conversation.EditorTarget) {
	v := c.event.Frame.View
	key := "main"
	target := conversation.EditorTarget{}
	if v.Input.ID != "" && v.Input.ID != v.Main.ID {
		key = "side:" + v.Input.ID
	}
	if p := v.PromptDraft; p.Call != "" || p.Request != "" {
		key = "prompt:" + p.Call + ":" + p.Question + ":" + p.Request
		target.Call, target.Question, target.Request = p.Call, p.Question, p.Request
	}
	editor := c.editor(key)
	if c.secretKey != "" {
		editor = &c.renderer.terminal.Prompts.Secret
	}
	target.Revision = editor.Revision()
	return editor, target
}

func (c *nativeClient) refresh(now time.Time) {
	c.autoOpenPrompts()
	c.refreshView(now)
}

// refreshView reconciles local editors without opening a dock during input.
func (c *nativeClient) refreshView(now time.Time) {
	c.prompts.overlay(&c.event.Frame.View)
	view := c.event.Frame.View
	key := ""
	if q := view.Questions; q.Call != nil && q.Index < len(q.Call.Questions) && q.Call.Questions[q.Index].Secret {
		key = q.Call.ID + ":" + q.Call.Questions[q.Index].ID
	}
	if key != c.secretKey {
		if c.secrets == nil {
			c.secrets = make(map[string]composer.State)
		}
		if c.secretKey != "" {
			c.secrets[c.secretKey] = c.renderer.terminal.Prompts.Secret.Park()
		}
		c.renderer.terminal.Prompts.Secret.Resume(c.secrets[key])
		c.secretKey = key
	}
	editor, target := c.selectedEditor()
	ordinary := c.editor("main")
	if view.Input.ID != "" && view.Input.ID != view.Main.ID {
		ordinary = c.editor("side:" + view.Input.ID)
	}
	draft := conversation.DraftView{Draft: ordinary.Draft.Editable(), Revision: ordinary.Revision()}
	prompt := conversation.PromptDraftView{Call: target.Call, Question: target.Question, Request: target.Request, Revision: target.Revision}
	if c.secretKey == "" {
		prompt.Draft = editor.Draft.Editable()
	}
	frame := c.event.Frame
	if details, ok := c.localDetails.DetailView(c.localDetailID); ok {
		frame.Details = &sessionapi.NativeDetails{View: details}
	}
	prediction := [3]string{frame.Facts.PredictionThread, frame.Facts.PredictionTurn, frame.View.Draft.Prediction}
	if prediction[2] != "" && prediction != c.prediction {
		c.prediction, c.predictionRevision = prediction, ordinary.Revision()
	}
	if ordinary.Text != "" || ordinary.Revision() != c.predictionRevision || c.renderer.terminal.Main.PredictionsDisabled {
		frame.View.Draft.Prediction = ""
	}
	frame.View.Status.ShellMode = conversation.ShellDraftMode(ordinary.Text, view.Questions.Call != nil || view.Approvals.Open)
	if menu := frame.View.Menu; menu != nil && menu.Kind == sessionpresentation.MenuCompletion && frame.CompletionTarget != target {
		frame.View.Menu = nil
	}
	if c.notice != "" {
		c.feedback.Set(c.notice, false, now)
		c.notice = ""
	}
	c.feedback.Expire(now)
	if c.feedback.Text != "" {
		frame.View.Status.SetFeedback(sessionpresentation.Label{Text: c.feedback.Text, Alert: c.feedback.Alert}, 0)
	}
	c.renderer.update(frame, draft, prompt, now)
	c.dirty = true
}

func (c *nativeClient) enqueue(operation sessionapi.NativeOperation, next *terminalsession.KeyContinuation, after func(error)) {
	if operation.Menu != nil && operation.Menu.Thread == "" {
		operation.Menu.Thread = c.event.Frame.View.Input.ID
	}
	var nextEditor conversation.EditorTarget
	if next != nil {
		_, nextEditor = c.selectedEditor()
	}
	if operation.Menu != nil || operation.Reference != nil || operation.Command != nil {
		editor, target := c.selectedEditor()
		operation.Editor = &sessionapi.NativeEditorInput{Target: target}
		if c.secretKey == "" {
			operation.Editor.Draft = editor.Draft.Clone()
		}
	}
	call := &nativeClientCall{owner: c, target: c.event.Target(), operation: operation, next: next, nextInput: c.event.Frame.View.Input, nextView: c.event.Frame.View.Displayed, nextEditor: nextEditor, after: after}
	if operation.Output != nil {
		call.unresolved = slices.Clone(operation.Output.Demands)
	}
	c.calls = append(c.calls, call)
}

func (c *nativeClient) run(ctx context.Context, stdin, stdout *os.File) (*nativeClient, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runtime := &nativeClientRuntime{
		ctx: ctx, active: c, messages: make(chan nativeClientMessage),
		results: make(chan *nativeClientCall, nativeCallLanes), reattached: make(chan nativeClientReattached, 1),
		editors: make(map[[2]string]*composer.Editor), images: make(map[string]bool),
	}
	runtime.start(c, stdout)
	c.renderer.terminal.Frame.Images = new(terminalui.ImageFrame)
	defer runtime.close()
	defer runtime.previews.close()
	// Source: apps/mekugi/internal/launch/app_server_ui.go:468:468@42f43ded.
	for {
		err := terminalui.WithRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l\x1b[?1003;1004;1006;2004h\x1b]10;?\x1b\\\x1b]11;?\x1b\\", "\x1b[?2026l\x1b[?1003;1004;1006;2004l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) (runErr error) {
			defer func() {
				if images := runtime.active.renderer.terminal.Frame.Images; images != nil {
					_, clearErr := io.WriteString(stdout, images.Clear())
					runErr = errors.Join(runErr, clearErr)
					*images = terminalui.ImageFrame{}
				}
			}()
			tick := time.NewTicker(33 * time.Millisecond)
			defer tick.Stop()
			for !runtime.active.quit && runtime.active.handoff == nil {
				runtime.schedule()
				c := runtime.active
				select {
				case <-ctx.Done():
					return ctx.Err()
				case message := <-runtime.messages:
					if err := runtime.deliver(message); err != nil {
						return err
					}
				case result := <-runtime.reattached:
					runtime.reattach(result, stdout)
				case call := <-runtime.results:
					call.complete = true
				case result := <-c.links.results:
					c.links.finish(result)
					c.dirty = true
				case result := <-runtime.previews.results:
					runtime.previews.finish(result)
					c.dirty = true
				case key, ok := <-keys:
					if !ok {
						return nil
					}
					runtime.decode(key, time.Now())
				case now := <-tick.C:
					runtime.drainKeys(keys, now)
					if c.advancePresentation(now) {
						c.dirty = true
					}
					if c.feedback.Expire(now) {
						c.refresh(now)
					}
					c.stagePreferences(false)
					c.notifications.writeTitle(&c.feedback, c.renderer.terminal, nil, c.event.Frame.View.Info.Workspace, c.event.Frame.Facts.ActionRequired, c.event.Frame.Facts.Busy, now)
					width, height, err := term.GetSize(int(stdout.Fd()))
					if err != nil {
						return err
					}
					if width != c.renderer.terminal.Width || height != c.renderer.terminal.Height {
						c.renderer.terminal.Width, c.renderer.terminal.Height, c.dirty = width, height, true
					}
					if c.event.Attachment != "" && (c.dirty || c.renderer.terminal.Live.Tick(now)) {
						source := c.renderer.terminal.FrameInput(now, mekugiVersion()).SourceContext()
						c.links.selectAttachment(source.Attachment)
						runtime.previews.prepare(stdout, c.renderer.terminal, source)
						c.renderer.frame.FileLinks = c.links.known(source)
						c.hydrateDetails()
						painted, err := c.renderer.render(ctx, stdout, now, mekugiVersion())
						if err != nil {
							return err
						}
						if len(painted.Demands) > 0 && !c.hydrating {
							c.hydrating = true
							c.enqueue(sessionapi.NativeOperation{Output: &sessionapi.NativeOutputDemand{Agent: c.event.Frame.View.Displayed.ID != c.event.Frame.View.Main.ID, Demands: painted.Demands}}, nil, func(error) { c.hydrating = false })
						} else if len(painted.Demands) == 0 {
							c.links.request(c.ctx, painted.Source, painted.FileLinkDemands)
							runtime.previews.request(runtime.ctx, source, painted.ImageDemands)
							c.dirty = false
							receipt := sessionapi.NativeNoticeReceipt{Claim: c.renderer.frame.Notice, Attachment: painted.Notices.Attachment, Feedback: painted.Notices.Feedback, Composer: painted.Notices.Composer, Content: painted.Notices.Content}
							c.stageReceipt(&receipt)
						}
					}
				}
				runtime.settle()
				for _, owner := range runtime.owners {
					owner.drainInput()
				}
				runtime.retire()
			}
			return nil
		})
		c := runtime.active
		c.notifications.clearTerminalTitle(&c.feedback, c.renderer.terminal, nil)
		if c.handoff == nil {
			c.flushNativeView()
			return c, err
		}
		handoff := c.handoff
		c.handoff = nil
		if err := c.external(ctx, stdin, stdout, handoff, runtime); err != nil {
			if outcome, ok := errors.AsType[*sessionapi.NativeExit](err); ok && outcome.Code == 0 {
				return runtime.active, nil
			}
			return runtime.active, err
		}
		runtime.active.refresh(time.Now())
	}
}
