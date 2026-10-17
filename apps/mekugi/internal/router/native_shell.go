package router

import (
	"context"
	"io"

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
	if u.shell.Agents != nil && view.Displayed.ID != view.Main.ID {
		u.shell.Agents.Conversation = true
		return u.shell.Agents
	}
	return u.shell.Transcript
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
