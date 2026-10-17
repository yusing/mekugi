package router

import (
	"context"
	"io"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// A Main excerpt carries an Activity identity, not a nearby question or run.
func (u *terminalUI) openActivityReply(seq uint64) bool {
	v := u.main.source
	for _, entry := range v.Entries {
		if entry.Source.Seq != seq || entry.Source.ActivitySeq == 0 {
			continue
		}
		for _, target := range u.main.childSource.Entries {
			if target.Source.Seq != entry.Source.ActivitySeq {
				continue
			}
			return u.openEntry(u.agents, target.Source.Seq)
		}
	}
	return false
}

func (u *appServerUI) transcriptCaller() string {
	return u.sessionPresentation().View().Displayed.Caller
}

func (u *appServerUI) composerReadOnly() bool {
	return u.sessionPresentation().View().ReadOnly != sessionpresentation.Editable
}

// transcriptView selects display state without changing the input or execution owner.
func (u *appServerUI) transcriptView() *activityui.View {
	view := u.sessionPresentation().View()
	if u.agents != nil && view.Displayed.ID != view.Main.ID {
		u.agents.Conversation = true
		return u.agents
	}
	return u.view
}

// frameInput binds actual terminal state and the playback caption without
// exposing application records or callbacks to the compositor.
func (u *terminalUI) frameInput() terminalsession.FrameInput {
	in := terminalsession.FrameInput{
		Main: &u.main.mainView, Prompts: &u.main.prompts, Transcript: u.main.view, Agents: u.agents,
		Shell: &u.Shell, Dialog: u.output, Diff: u.diffScreen, Live: &u.liveDock,
		Width: u.width, Height: u.height, Now: u.main.now(), Version: mekugiVersion(),
		LiveHidden: u.liveHidden, DiffUnseen: u.diffUnseen, Prefix: u.prefix,
	}
	if u.selection != nil {
		in.Selection, in.SelectionPane = u.selection.Selection, u.selection.Pane
		in.SelectionSource = terminalsession.SourceContext{Attachment: u.selection.Attachment, Thread: u.selection.Thread, Workspace: u.selection.Workspace}
	}
	if u.main.replay != nil {
		in.Playback, in.PlaybackBar = true, u.main.replay.bar(u.width)
	}
	return in
}

func (u *terminalUI) paintNative(ctx context.Context, out io.Writer) error {
	in := u.frameInput()
	for {
		frame, err := u.frame.Render(ctx, in)
		if err != nil {
			return err
		}
		if len(frame.Demands) != 0 {
			transcript := u.main.transcriptView()
			transcript.Hydrate(u.main.activitySource(transcript).ResolveOutputDemands(frame.Demands))
			continue
		}
		if frame.DiffPainted {
			u.diffUnseen = false
		}
		if !frame.SelectionRetained {
			u.selection = nil
		}
		if err := u.frame.Write(out, frame, u.width, u.faint); err != nil {
			return err
		}
		if frame.PromptVisible {
			u.main.prompts.Delivered(frame.Prompt)
		}
		return nil
	}
}

// selectNativePane gives status-bar clicks and Ctrl-B digits the same action.
func (u *terminalUI) selectNativePane(pane int) {
	if pane == 0 {
		view := u.main.sessionPresentation().View()
		u.main.displaySession(sessionpresentation.DisplayIntent{Version: view.Version, Thread: view.Main})
	}
	u.Shell.Select(pane)
	u.selection = nil
}

// showRosterPick switches presentation only; Main retains its composer and host identity.
func (u *terminalUI) showRosterPick(only bool, selected string) {
	if intent := u.main.takeDisplayIntent(); intent != nil && u.main.displaySession(*intent) && intent.Thread.Caller != "/root" {
		u.agents.Selected = intent.Thread.Caller
	}
	if u.main != nil && (u.main.sessionPresentation().DisplayingChild() != only || u.agents.Selected != selected) {
		u.selection = nil
		u.agents.Runs = nil
	}
}

// flushEscape sends a lone Esc once no sequence followed it.
func (u *terminalUI) flushEscape() error {
	if !u.input.Escape(time.Now()) {
		return nil
	}
	return u.send("\x1b")
}
