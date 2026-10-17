package router

import (
	"cmp"

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

func (u *appServerUI) openSession() error {
	opening, err := u.Requests.OpenSession(&u.Startup, &u.Live, u.approvalMode, appServerRequest{label: "resume/settings"})
	u.attached.presentation.BeginOpening(opening)
	if err != nil {
		return err
	}
	if opening == sessioncontrol.OpeningPicker {
		return u.openResumePicker(true)
	}
	return nil
}

func (u *appServerUI) resumeSettingsResponse(m appserver.Message) error {
	fail := func(message string) error {
		if u.Replacement.Target != "" {
			return u.resumeSessionFailed(message)
		}
		return fmt.Errorf("read resume settings: %s", message)
	}
	if m.Error != nil {
		return fail(m.Error.Message)
	}
	thread := cmp.Or(u.Replacement.Target, u.Startup.Thread)
	info, err := binding.DecodeHistoryThread(m.Result, thread)
	if err != nil {
		return fail(err.Error())
	}

	// Codex skips persisted model/effort when the routing provider is explicit.
	// Read settings before resume can write its new defaults into the rollout.
	var retained preferences.Snapshot
	if u.panes != nil {
		state, err := u.panes.read(info.Cwd, thread)
		if err != nil {
			u.paneError(err)
		} else {
			retained = state.Snapshot
		}
	}
	evidence, newerThan := retained.ResumeBaseline()
	evidence = evidence.WithNewer(resumeSettingsEvidence(rollout.ResumeSettings(info.ID, info.Path, newerThan)))
	err = u.Requests.Resume(&u.Startup, &u.Live, evidence, u.approvalMode)
	if u.Startup.MissingSettings {
		u.setNotice(sessionpresentation.ResumeSettingsUnavailable, true)
	}
	return err
}

// holdResumeEvent decides whether an event waits for a resuming thread's
// history. During an in-session switch the current session stays live, so
// only events for other threads wait. Streaming deltas from the resumed
// thread's descendants are dropped rather than kept: their completed items
// carry the full content, and busy children must not exhaust the bounded
// buffer.
func (u *appServerUI) holdResumeEvent(m appserver.Message, resuming bool) (hold, keep bool) {
	var p struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID             string `json:"id"`
			ParentThreadID string `json:"parentThreadId"`
		} `json:"thread"`
	}
	_ = json.Unmarshal(m.Params, &p)
	thread := cmp.Or(p.ThreadID, p.Thread.ID)
	if !resuming && (thread == "" || u.attached.session.KnownCaller(thread) != "" || u.attached.session.KnownCaller(p.Thread.ParentThreadID) != "") {
		return false, false
	}
	root := cmp.Or(u.Replacement.Target, u.Startup.Thread)
	delta := strings.HasSuffix(m.Method, "/delta") || strings.HasSuffix(m.Method, "Delta")
	return true, !delta || thread == "" || thread == root
}

// replayResumePending delivers events buffered while a thread was pending.
func (u *appServerUI) replayResumePending() error {
	pending := u.resumePending
	u.resumePending = nil
	for _, event := range pending {
		if err := u.message(event); err != nil {
			return err
		}
	}
	return nil
}

func (u *appServerUI) resumeLastResponse(m appserver.Message) error {
	if m.Error != nil {
		return fmt.Errorf("find latest thread: %s", m.Error.Message)
	}
	if err := u.Startup.Latest(m.Result); err != nil {
		return err
	}
	return u.openSession()
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
func (u *appServerUI) restoreMainHistory(turns []binding.HistoryTurn, placements []*restoredPlacement, itemAt map[string]time.Time) {
	previousSegments := u.restoredSegments
	defer func() { u.restoredSegments = previousSegments }()
	positions := historyPositions(turns, itemAt)
	byTurn := make(map[int][]*restoredPlacement)
	for _, p := range placements {
		index := p.position().TurnFor(positions)
		if index >= 0 {
			byTurn[index] = append(byTurn[index], p)
		} else if p.entry.Kind == "hook" {
			u.applyRestoredMain([]*restoredPlacement{p})
		}
	}
	for index, turn := range turns {
		u.restoredSegments = u.prepareCommandSegments(u.Thread, u.attached.cwd, turn)
		placements := byTurn[index]
		slots := positions[index].Slots(restoredPositions(placements))
		place := func(indices []int) {
			for _, index := range indices {
				u.applyRestoredMain(placements[index : index+1])
			}
		}
		place(slots[0])
		for i, item := range turn.Items {
			before := u.attached.source.LastSeq
			u.restoreHistoryItem(turn, item)
			at := cmp.Or(itemAt[item.ID], historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt)))
			if !at.IsZero() {
				for j := len(u.attached.source.Entries) - 1; j >= 0 && u.attached.source.Entries[j].Source.Seq > before; j-- {
					u.attached.source.Model.Mutate(j, func(record *sessionpresentation.ActivityRecord) { record.Source.Observed = at })
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
			u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: u.attached.source.LastSeq + 1, Agent: "Main", Kind: "error", Text: text,
				Observed: historyTime(cmp.Or(turn.CompletedAt, turn.StartedAt))}}})
		}
		if turn.Status == "inProgress" {
			u.Turn = turn.ID
			u.attached.presentation.Status.Started = u.now()
			u.attached.presentation.Status.SetText("Working")
		}
	}
	u.attached.session.Lifecycle(u.Thread).Restore(restoredAgentIntervals(turns), uint64(len(turns)), u.Turn != "")
	// Keep future activity sequence numbers ahead of the hydrated transcript.
	u.attached.session.Activity().AdvanceSequence(u.attached.source.LastSeq)
	u.applyActivity(nil, u.attached.session.Roster())
}

func (u *appServerUI) applyRestoredMain(placements []*restoredPlacement) {
	for _, p := range placements {
		entry := p.entry
		if p.link != nil && sessionpresentation.MainActivityLinked(entry) {
			entry.ActivitySeq = *p.link
		}
		if entry.Observed.IsZero() {
			entry.Observed = p.at
		}
		u.attached.source.ApplyMain(entry)
	}
}

func (u *appServerUI) restoreHistoryItem(turn binding.HistoryTurn, item binding.ThreadItem) {
	facts := item.Observation()
	text := observation.TextEvent{Kind: observation.ItemCompleted, Item: transcriptItem(item, u.Thread, turn.ID, item.ID)}
	if u.observeMessageQuestions(text, facts, true) {
		return
	}
	if item.Type == "userMessage" {
		var content []conversationbinding.UserContent
		if json.Unmarshal(item.Content, &content) == nil {
			var text strings.Builder
			var attached []string
			var spans []composer.Span
			for _, part := range content {
				if part.Type == "text" {
					if frames, ok := attachments.Decode(part.Text); ok {
						attached = append(attached, frames...)
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
			draft.Selections = composer.RestoreSelections(draft.Text, attached)
			draft.Skills = conversationbinding.SkillBindings(draft.Text, conversationbinding.ContentSkills(content), spans)
			u.rememberInput(draft)
		}
	}
	method := "item/completed"
	if turn.Status == "inProgress" && (item.Type == "agentMessage" || item.Status == "inProgress") {
		method = "item/started"
	}
	progressPhase := appServerHistoryProgressPhase(facts, turn.Status)
	if text, wait, progress := u.itemProgress(facts, u.Thread, turn.ID, item.ID, progressPhase, false); progress {
		if text != "" {
			u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{
				Seq: u.attached.source.LastSeq + 1, Agent: "Main", Kind: "progress", Text: text, Observed: time.Now(),
				Native: &sessionpresentation.NativeItem{Thread: u.Thread, Turn: turn.ID, Item: item.ID, Phase: progressPhase, Wait: wait},
			}}})
		}
		return
	}
	switch item.Type {
	case "imageView":
		entry := sessionpresentation.ActivityEntry{Seq: u.attached.source.LastSeq + 1, Agent: "Main", Kind: "tool", Text: "View " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(u.attached.cwd, item.Path)), CallID: item.ID, Observed: time.Now(),
			Native: &sessionpresentation.NativeItem{Thread: u.Thread, Turn: turn.ID, Item: item.ID, Phase: method, SearchResults: sessionpresentation.SearchResults(facts)}}
		u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
	case "commandExecution", "fileChange", "webSearch":
		text := sessionpresentation.ToolText(facts, u.attached.cwd)
		if item.Type == "fileChange" {
			text = activitypresentation.HostEditText(facts, u.attached.cwd)
		}
		entry := sessionpresentation.ActivityEntry{Seq: u.attached.source.LastSeq + 1, Agent: "Main", Kind: "tool", Text: text, CallID: item.ID, Observed: time.Now(),
			Native: &sessionpresentation.NativeItem{Thread: u.Thread, Turn: turn.ID, Item: item.ID, Phase: method, Command: item.Command, CommandCwd: sessionpresentation.CommandDirectory(facts, u.attached.cwd), Status: item.Status, Duration: sessionpresentation.ItemDuration(facts), SearchResults: sessionpresentation.SearchResults(facts), Workdir: sessionpresentation.CommandWorkdir(facts, u.attached.cwd)}}
		if item.Type == "fileChange" {
			entry.Native.EditPages = activitypresentation.HostEditPages(facts, u.attached.cwd, method)
		}
		u.attached.session.CompleteCommand(entry.Native, facts, u.activity.Outputs(u.attached.session.Scope()))
		sessionpresentation.CommandOutput(&entry, facts, time.Time{})
		u.restoreCommandSegments(&entry, item, u.attached.cwd)
		u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{entry}})
		if len(entry.Native.Segments) == 0 && item.ExitCode != nil && *item.ExitCode != 0 {
			exit := sessionpresentation.ActivityEntry{Seq: u.attached.source.LastSeq + 1, Agent: "Main", Kind: "exit", Text: strconv.Itoa(*item.ExitCode), CallID: item.ID}
			exit.OutputTail, exit.OutputOmit = sessionpresentation.OutputTail(item.AggregatedOutput)
			u.attached.source.Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{exit}})
		}
	default:
		if update, ok := historicalTranscript([]binding.ThreadItem{item}, u.Thread, turn.ID, true, false, u.now())[item.ID]; ok {
			update.Phase = method
			u.attached.source.ApplyTranscript(update, item.Content, u.attached.cwd, u.Thread, item.Type == "userMessage")
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
