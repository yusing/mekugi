package launch

import (
	"cmp"
	"context"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"

	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/observation"

	json "encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
	"time"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"

	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/BurntSushi/toml"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func openSession(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time, resumePicker **appServerResumePicker, approvalMode bool) error {
	thread := cmp.Or(control.Replacement.Target, control.Startup.Thread)
	if attached.reserveThread != nil && thread != "" && thread != "--last" && thread != sessioncontrol.ResumePicker {
		if err := attached.reserveThread(thread); err != nil {
			return err
		}
	}
	opening, err := control.Requests.OpenSession(&control.Startup, &control.Live, approvalMode, appServerRequest{label: "resume/settings"})
	attached.presentation.BeginOpening(opening)
	if err != nil {
		if attached.reserveThread != nil {
			_ = attached.reserveThread("")
		}
		return err
	}
	if opening == sessioncontrol.OpeningPicker {
		return openResumePicker(attached, control, picker, clock, resumePicker, true)
	}
	return nil
}

func resumeSettingsResponse(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, approvalMode bool, dispatch func(appserver.Message) error, resumePending *[]appserver.Message, panes *nativePreferences, m appserver.Message) error {
	fail := func(message string) error {
		if control.Replacement.Target != "" {
			return resumeSessionFailed(attached, terminal, control, picker, questions, approvals, clock, dispatch, resumePending, message)
		}
		return fmt.Errorf("read resume settings: %s", message)
	}
	if m.Error != nil {
		return fail(m.Error.Message)
	}
	thread := cmp.Or(control.Replacement.Target, control.Startup.Thread)
	info, err := binding.DecodeHistoryThread(m.Result, thread)
	if err != nil {
		return fail(err.Error())
	}

	// Codex skips persisted model/effort when the routing provider is explicit.
	// Read settings before resume can write its new defaults into the rollout.
	var retained preferences.Snapshot
	if panes != nil {
		state, err := panes.read(info.Cwd, thread)
		if err != nil {
			paneError(attached.source, err)
		} else {
			retained = state.Snapshot
		}
	}
	evidence, newerThan := retained.ResumeBaseline()
	evidence = evidence.WithNewer(resumeSettingsEvidence(rollout.ResumeSettings(info.ID, info.Path, newerThan)))
	err = control.Requests.Resume(&control.Startup, &control.Live, evidence, approvalMode)
	if control.Startup.MissingSettings {
		setNotice(&attached.feedback, terminal, presentationTime(clock), sessionpresentation.ResumeSettingsUnavailable, true)
	}
	return err
}

// holdResumeEvent decides whether an event waits for a resuming thread's
// history. During an in-session switch the current session stays live, so
// only events for other threads wait. Streaming deltas from the resumed
// thread's descendants are dropped rather than kept: their completed items
// carry the full content, and busy children must not exhaust the bounded
// buffer.
func holdResumeEvent(session *sessionpresentation.Attachment, control *sessioncontrol.Live, startup sessioncontrol.Startup, m appserver.Message, resuming bool) (hold, keep bool) {
	var p struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID             string `json:"id"`
			ParentThreadID string `json:"parentThreadId"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(m.Params, &p)
	thread := cmp.Or(p.ThreadID, p.Thread.ID)
	if !resuming && (thread == "" || session.KnownCaller(thread) != "" || session.KnownCaller(p.Thread.ParentThreadID) != "") {
		return false, false
	}
	root := cmp.Or(control.Replacement.Target, startup.Thread)
	delta := strings.HasSuffix(m.Method, "/delta") || strings.HasSuffix(m.Method, "Delta")
	return true, !delta || thread == "" || thread == root
}

// replayResumePending delivers events buffered while a thread was pending.
func replayResumePending(dispatch func(appserver.Message) error, resumePending *[]appserver.Message) error {
	pending := (*resumePending)
	(*resumePending) = nil
	for _, event := range pending {
		if err := dispatch(event); err != nil {
			return err
		}
	}
	return nil
}

func resumeLastResponse(attached *attachedPresentation, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, clock func() time.Time, resumePicker **appServerResumePicker, approvalMode bool, m appserver.Message) error {
	if m.Error != nil {
		return fmt.Errorf("find latest thread: %s", m.Error.Message)
	}
	if cursor, err := control.Startup.Latest(m.Result); err != nil {
		return err
	} else if cursor != "" {
		params := sessioncontrol.ResumableThreads(25)
		params["cwd"], params["cursor"] = control.Startup.Workspace, cursor
		_, err := control.Requests.Send("thread/list", params, "main", appServerRequest{})
		return err
	}
	if reattachSession(attached, control.Startup.Thread) {
		// The lookup-only startup stays unconfirmed until its originating view
		// closes after destination attachment, or the user cancels it.
		control.Startup.Thread = "--last"
		return nil
	}
	return openSession(attached, control, picker, clock, resumePicker, approvalMode)
}

// Codex merges saved model metadata after loading process CLI configuration.
// Forward explicit model settings on resume as well so that invocation-local
// overrides retain their meaning. Other config keeps its app-server owner.
func appServerResumeConfig(args, resumeArgv []string) map[string]any {
	config := make(map[string]any)
	for i := 0; i < len(args); i++ {
		if args[i] != "-c" || i+1 == len(args) {
			continue
		}
		i++
		key, value, ok := strings.Cut(args[i], "=")
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		if !ok || (key != "model" && key != "model_provider" && key != "model_reasoning_effort" && key != "service_tier") {
			continue
		}
		var parsed map[string]any
		if _, err := toml.Decode("value = "+value, &parsed); err == nil {
			config[key] = parsed["value"]
		} else {
			// Codex accepts bare strings when a CLI value is not valid TOML.
			config[key] = strings.TrimSpace(value)
		}
	}
	if len(resumeArgv) > 0 {
		_, userArgs, err := launchoptions.SplitCommand(resumeArgv[1:])
		if err == nil && !launchoptions.HasModelOverride(userArgs) {
			// Startup defaults are not explicit resume overrides. Retain the
			// original invocation's selection, not launcher-generated config.
			delete(config, "model")
		}
	}
	return config
}

// History is presentation evidence, not a stream of live lifecycle events.
// In particular, old edits and collaboration calls must not revive previews,
// processes or child agents. restoreMainHistory
// also places restored evidence the host's history omits among Main's items: at its rollout anchor, or by time for evidence from
// another thread. itemAt holds the root's retained item completion times.
func restoreMainHistory(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, reviewOwner *sessionreview.Review, receipts *[]*changepresentation.Receipt, notifications *nativeNotifications, retained changes.Scope, retainedSession history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, turns []binding.HistoryTurn, placements []*restoredPlacement, itemAt map[string]time.Time) {
	previousSegments := (*restoredSegments)
	defer func() { (*restoredSegments) = previousSegments }()
	positions := historyPositions(turns, itemAt)
	byTurn := make(map[int][]*restoredPlacement)
	for _, p := range placements {
		index := p.position().TurnFor(positions)
		if index >= 0 {
			byTurn[index] = append(byTurn[index], p)
		} else if p.entry.Kind == "hook" {
			applyRestoredMain(attached.source, []*restoredPlacement{p})
		}
	}
	for index, turn := range turns {
		(*restoredSegments) = prepareCommandSegments(parent, retained, segments, control.Thread, attached.cwd, turn)
		placements := byTurn[index]
		slots := positions[index].Slots(restoredPositions(placements))
		place := func(indices []int) {
			for _, index := range indices {
				applyRestoredMain(attached.source, placements[index:index+1])
			}
		}
		place(slots[0])
		for i, item := range turn.Items {
			before := attached.source.LastSeq
			restoreHistoryItem(attached, terminal, control, picker, editor, questions, approvals, clock, panel, resumePicker, parent, notifications, retained, retainedSession, activityService, segments, restoredSegments, turn, item)
			at := cmp.Or(itemAt[item.ID], historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt)))
			if !at.IsZero() {
				for j := len(attached.source.Entries) - 1; j >= 0 && attached.source.Entries[j].Source.Seq > before; j-- {
					attached.source.Model.Mutate(j, func(record *sessionpresentation.ActivityRecord) { record.Source.Observed = at })
				}
			}
			place(slots[i+1])
		}
		if turn.Status == "failed" {
			// Live, the failure only reaches the status line, which resuming replaces.
			text := "Turn failed"
			if turn.Error != nil && strings.TrimSpace(turn.Error.Message) != "" {
				text += ": " + turn.Error.Message
			}
			attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: attached.source.LastSeq + 1, Agent: "Main", Kind: "error", Text: text,
				Observed: historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt))}}})
		}
		if turn.Status == "inProgress" {
			control.Turn = turn.ID
			attached.presentation.Status.Started = presentationTime(clock)
			attached.presentation.Status.SetText("Working")
		}
	}
	attached.session.Lifecycle(control.Thread).Restore(restoredAgentIntervals(turns), uint64(len(turns)), control.Turn != "")
	// Keep future activity sequence numbers ahead of the hydrated transcript.
	attached.session.Activity().AdvanceSequence(attached.source.LastSeq)
	applyActivity(attached, approvals, terminal, reviewOwner, receipts, control.Thread, clock, nil, attached.session.Roster())
}

func applyRestoredMain(source *sessionpresentation.ActivitySource, placements []*restoredPlacement) {
	for _, p := range placements {
		entry := p.entry
		if p.link != nil && sessionpresentation.MainActivityLinked(entry) {
			entry.ActivitySeq = *p.link
		}
		if entry.Observed.IsZero() {
			entry.Observed = p.at
		}
		source.ApplyMain(entry)
	}
}

func restoreHistoryItem(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, editor **composer.Editor, questions *nativeQuestionDock, approvals *nativeApprovalDock, clock func() time.Time, panel **appServerStatusReport, resumePicker **appServerResumePicker, parent context.Context, notifications *nativeNotifications, retained changes.Scope, retainedSession history.Session, activityService *activitymodel.Service, segments *reports.Service, restoredSegments *map[string]*reports.Report, turn binding.HistoryTurn, item binding.ThreadItem) {
	if item.Type == "userMessage" {
		control.HasUserTurn = true
	}
	facts := item.Observation()
	text := observation.TextEvent{Kind: observation.ItemCompleted, Item: transcriptItem(item, control.Thread, turn.ID, item.ID)}
	if observeMessageQuestions(attached, terminal, control, approvals, clock, questions, editor, picker, (*panel) != nil || (*resumePicker) != nil, notifications, text, facts, true) {
		return
	}
	if item.Type == "userMessage" {
		var content []conversationbinding.UserContent
		if json.Unmarshal(item.Content, &content) == nil {
			var text strings.Builder
			var attachmentFrames []string
			var spans []composer.Span
			for _, part := range content {
				if part.Type == "text" {
					if frames, ok := attachments.Decode(part.Text); ok {
						attachmentFrames = append(attachmentFrames, frames...)
						continue
					}
					for _, span := range conversationbinding.ElementSpans(part.Text, part.Elements) {
						span.Start += text.Len()
						span.End += text.Len()
						spans = append(spans, span)
					}
					text.WriteString(part.Text)
				}
			}
			draft := conversationmodel.Input{Text: text.String()}
			draft.Selections = composer.RestoreSelections(draft.Text, attachmentFrames)
			draft.Skills = conversationbinding.SkillBindings(draft.Text, conversationbinding.ContentSkills(content), spans)
			if attached.inputHistory != nil && len(binding.QuestionReplies(draft.Text)) == 0 {
				attached.conversation.Editor.RestoreHistory(control.Thread, draft.Draft)
			} else {
				rememberInput(attached.conversation, draft)
			}
		}
	}
	method := "item/completed"
	if turn.Status == "inProgress" && (item.Type == "agentMessage" || item.Status == "inProgress") {
		method = "item/started"
	}
	progressPhase := appServerHistoryProgressPhase(facts, turn.Status)
	if text, wait, progress := itemProgress(attached, terminal, retainedSession, clock, facts, control.Thread, turn.ID, item.ID, progressPhase, false); progress {
		if text != "" {
			attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{
				Seq: attached.source.LastSeq + 1, Agent: "Main", Kind: "progress", Text: text, Observed: time.Now(),
				Native: &sessionpresentation.NativeItem{Thread: control.Thread, Turn: turn.ID, Item: item.ID, Phase: progressPhase, Wait: wait},
			}}})
		}
		return
	}
	switch item.Type {
	case "imageView":
		entry := sessionpresentation.ActivityEntry{Seq: attached.source.LastSeq + 1, Agent: "Main", Kind: "tool", Text: "View " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(attached.cwd, item.Path)), CallID: item.ID, Observed: time.Now(),
			Native: &sessionpresentation.NativeItem{Thread: control.Thread, Turn: turn.ID, Item: item.ID, Phase: method, SearchResults: sessionpresentation.SearchResults(facts)}}
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
	case "commandExecution", "fileChange", "webSearch":
		text := sessionpresentation.ToolText(facts, attached.cwd)
		if item.Type == "fileChange" {
			text = activitypresentation.HostEditText(facts, attached.cwd)
		}
		entry := sessionpresentation.ActivityEntry{Seq: attached.source.LastSeq + 1, Agent: "Main", Kind: "tool", Text: text, CallID: item.ID, Observed: time.Now(),
			Native: &sessionpresentation.NativeItem{Thread: control.Thread, Turn: turn.ID, Item: item.ID, Phase: method, Command: item.Command, CommandCwd: sessionpresentation.CommandDirectory(facts, attached.cwd), Status: item.Status, Duration: sessionpresentation.ItemDuration(facts), SearchResults: sessionpresentation.SearchResults(facts), Workdir: sessionpresentation.CommandWorkdir(facts, attached.cwd)}}
		if item.Type == "fileChange" {
			entry.Native.EditPages = activitypresentation.HostEditPages(facts, attached.cwd, method)
		}
		attached.session.CompleteCommand(entry.Native, facts, activityService.Outputs(attached.session.Scope()))
		sessionpresentation.CommandOutput(&entry, facts, time.Time{})
		restoreCommandSegments(parent, retained, segments, attached, activityService, (*restoredSegments), &entry, item, attached.cwd)
		attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{entry}})
		if len(entry.Native.Segments) == 0 && item.ExitCode != nil && *item.ExitCode != 0 {
			exit := sessionpresentation.ActivityEntry{Seq: attached.source.LastSeq + 1, Agent: "Main", Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID}
			exit.OutputTail, exit.OutputOmit = sessionpresentation.OutputTail(item.AggregatedOutput)
			attached.source.Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{exit}})
		}
	default:
		if update, ok := historicalTranscript([]binding.ThreadItem{item}, control.Thread, turn.ID, true, false, presentationTime(clock))[item.ID]; ok {
			update.Phase = method
			attached.source.ApplyTranscript(update, item.Content, attached.cwd, control.Thread, item.Type == "userMessage")
		}
	}
}

func resumeSettingsEvidence(config map[string]any) preferences.Evidence {
	var evidence preferences.Evidence
	evidence.Model, _ = config["model"].(string)
	evidence.Effort, _ = config["model_reasoning_effort"].(string)
	evidence.Tier, _ = config["service_tier"].(string)
	_, evidence.TierKnown = config["service_tier"]
	return evidence
}
