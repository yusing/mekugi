package router

import (
	"context"
	"io"

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
		Agents: u.agents,
		Shell:  &u.Shell, Dialog: u.output, Diff: u.diffScreen, Live: &u.liveDock,
		Width: u.width, Height: u.height, Version: mekugiVersion(),
		LiveHidden: u.liveHidden, DiffUnseen: u.diffUnseen, Prefix: u.Prefix,
	}
	if u.main != nil {
		in.Main, in.Prompts, in.Transcript, in.Now = &u.main.mainView, &u.main.prompts, u.main.view, u.main.now()
	}
	if u.Selection != nil {
		in.Selection, in.SelectionPane = u.Selection.Selection, u.Selection.Pane
		in.SelectionSource = terminalsession.SourceContext{Attachment: u.Selection.Attachment, Thread: u.Selection.Thread, Workspace: u.Selection.Workspace}
	}
	if u.main != nil && u.main.replay != nil {
		in.Playback, in.PlaybackBar = true, u.main.replay.bar(u.width)
	}
	return in
}

func (u *terminalUI) paintNative(ctx context.Context, out io.Writer) error {
	in := u.frameInput()
	for {
		frame, err := u.Frame.Render(ctx, in)
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
			u.Selection = nil
		}
		if err := u.Frame.Write(out, frame, u.width, u.faint); err != nil {
			return err
		}
		if frame.PromptVisible {
			u.main.prompts.Delivered(frame.Prompt)
		}
		return nil
	}
}
