package launch

import (
	"context"
	"io"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (files *detailFiles) openActivityReply(attached *attachedPresentation, seq uint64) bool {
	for _, entry := range attached.source.Entries {
		if entry.Source.Seq != seq || entry.Source.ActivitySeq == 0 {
			continue
		}
		pages := entryDetailPages(attached.childSource, entry.Source.ActivitySeq)
		if len(pages) == 0 {
			continue
		}
		files.openDetailSelection(&attached.session, attached.presentation.View().Version, attached.childSource, pages, false)
		return true
	}
	return false
}

func paintNative(files *detailFiles, u *terminalsession.Attachment, in terminalsession.FrameInput, transcript *activityui.View, source *sessionpresentation.ActivitySource, ctx context.Context, out io.Writer) error {
	scope := in.SourceContext()
	if in.Selection != nil && in.Dialog == nil && in.SelectionPane == u.Shell.Layout.Main {
		scope = in.SelectionSource
	}
	files.links.selectAttachment(scope.Attachment)
	in.FileLinks = files.links.known(scope)
	for {
		frame, err := u.Frame.Render(ctx, in)
		if err != nil {
			return err
		}
		if len(frame.Demands) != 0 {
			transcript.Hydrate(source.ResolveOutputDemands(frame.Demands))
			continue
		}
		if !frame.SelectionRetained && scope != in.SourceContext() {
			scope = in.SourceContext()
			in.FileLinks = files.links.known(scope)
			in.Selection, u.Selection = nil, nil
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
		files.links.request(ctx, scope, frame.FileLinkDemands)
		if frame.PromptVisible {
			u.Prompts.Delivered(frame.Prompt)
		}
		return nil
	}
}
