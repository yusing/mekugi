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
func (main *appServerUI) openActivityReply(u *terminalsession.Attachment, seq uint64) bool {
	v := main.source
	for _, entry := range v.Entries {
		if entry.Source.Seq != seq || entry.Source.ActivitySeq == 0 {
			continue
		}
		for _, target := range main.childSource.Entries {
			if target.Source.Seq != entry.Source.ActivitySeq {
				continue
			}
			return main.openEntry(u, u.Agents, target.Source.Seq)
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
func (main *appServerUI) frameInput(u *terminalsession.Attachment) terminalsession.FrameInput {
	var now time.Time
	if main != nil {
		u.Main, u.Prompts, u.Transcript = &main.mainView, &main.prompts, main.view
		now = main.now()
	}
	in := u.FrameInput(now, mekugiVersion())
	return in
}

func paintNative(u *terminalsession.Attachment, in terminalsession.FrameInput, transcript *activityui.View, source *sessionpresentation.ActivitySource, ctx context.Context, out io.Writer) error {
	for {
		frame, err := u.Frame.Render(ctx, in)
		if err != nil {
			return err
		}
		if len(frame.Demands) != 0 {
			transcript.Hydrate(source.ResolveOutputDemands(frame.Demands))
			continue
		}
		if frame.DiffPainted {
			u.DiffUnseen = false
		}
		if !frame.SelectionRetained {
			u.Selection = nil
		}
		if err := u.Frame.Write(out, frame, u.Width, u.Faint); err != nil {
			return err
		}
		if frame.PromptVisible {
			u.Prompts.Delivered(frame.Prompt)
		}
		return nil
	}
}
