package router

import (
	"context"
	"io"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// A Main excerpt carries an Activity identity, not a nearby question or run.
func openActivityReply(attached *attachedPresentation, terminal *terminalsession.Attachment, files *detailFiles, surface *terminalsession.Attachment, seq uint64) bool {
	v := attached.source
	for _, entry := range v.Entries {
		if entry.Source.Seq != seq || entry.Source.ActivitySeq == 0 {
			continue
		}
		for _, target := range attached.childSource.Entries {
			if target.Source.Seq != entry.Source.ActivitySeq {
				continue
			}
			return openEntry(attached, terminal, files, surface, surface.Agents, target.Source.Seq)
		}
	}
	return false
}

// transcriptView selects display state without changing the input or execution owner.
func transcriptView(presentation *sessionpresentation.Model, terminal *terminalsession.Attachment) *activityui.View {
	view := presentation.View()
	if terminal.Agents != nil && view.Displayed.ID != view.Main.ID {
		terminal.Agents.Conversation = true
		return terminal.Agents
	}
	return terminal.Transcript
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
