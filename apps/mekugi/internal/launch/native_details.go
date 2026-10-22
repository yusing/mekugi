package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func (v *nativeSessionView) openNoticeDetails(text string) string {
	openBlocks(&v.attached, v.shell, &v.files, v.shell, v.shell.Transcript, []activityui.Block{{Kind: "error", Body: text}})
	v.detailDemand = nil
	return ""
}

func (v *nativeSessionView) openDetails(request sessionapi.NativeDetailRequest) string {
	terminal := v.shell.Transcript
	if request.Agent {
		terminal = v.shell.Agents
	}
	// Legacy semantic adapters consume the same detached Activity publications as
	// native painting. The selected source and contributed detail owner stay real.
	publishActivity(&v.attached, v.shell)
	switch request.Kind {
	case sessionapi.NativeFileDetails:
		if !v.files.openMarkdownFile(&v.attached.session, v.attached.presentation, v.attached.cwd, activitySource(&v.attached, v.shell, terminal), v.shell, terminal, request.Path) {
			return "The local file target is unavailable."
		}
	case sessionapi.NativeErrorDetails:
		openErrorDetails(&v.attached, v.shell, &v.files, v.shell, terminal)
	case sessionapi.NativeSkillDetails:
		openSkills(&v.attached, v.shell, &v.files, v.shell, terminal, request.Caller)
	case sessionapi.NativeEntryDetails:
		if request.Path != "" {
			if !openActivityEdit(&v.attached, v.shell, &v.files, v.activity, v.shell, terminal, request.Source, request.Path) {
				return sessionpresentation.ErrStaleIntent.Error()
			}
			break
		}
		if !request.Agent && len(request.Sources) == 0 && openActivityReply(&v.attached, v.shell, &v.files, v.shell, request.Source) {
			break
		}
		sources := request.Sources
		if len(sources) == 0 {
			sources = []uint64{request.Source}
		}
		var pages []activity.DetailPage
		for _, seq := range sources {
			// A batch edit uses the admitted change owner's complete file pages.
			if edits, _ := activityEditPages(&v.attached, v.shell, v.activity, terminal, seq, ""); len(edits) > 0 {
				pages = append(pages, edits...)
				continue
			}
			next := entryDetailPages(activitySource(&v.attached, v.shell, terminal), seq)
			if len(next) == 0 {
				return sessionpresentation.ErrStaleIntent.Error()
			}
			pages = append(pages, next...)
		}
		if len(pages) == 0 {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		openDetails(&v.attached, v.shell, &v.files, v.shell, terminal, pages)
		for i, page := range v.shell.Dialog.Pages {
			if page.Source == request.Source && page.Output == request.Output {
				v.shell.Dialog.ShowPage(i)
				break
			}
		}
	default:
		return "The detail action is unavailable."
	}
	v.detailDemand = nil
	return ""
}

func (v *nativeSessionView) publishDetails(frame *sessionapi.NativeFrame) {
	select {
	case result := <-v.files.fileReads:
		v.files.loadedMarkdownFile(&v.attached.session, v.attached.presentation, v.attached.cwd, v.shell, result)
	default:
	}
	dialog := v.shell.Dialog
	if dialog == nil {
		return
	}
	if dialog.ID.Attachment != frame.View.Version.Attachment {
		v.files.closeOutput(v.shell)
		v.detailDemand = nil
		return
	}
	v.files.prepareOutput(&v.attached.session, v.shell)
	if v.shell.Dialog == nil {
		return
	}
	view, ok := v.files.details.DetailView(dialog.ID)
	if !ok {
		return
	}
	frame.Details = &sessionapi.NativeDetails{View: view, Output: dialog.Output, File: dialog.File, Agent: dialog.View == v.shell.Agents}
	if dialog.Page >= 0 && dialog.Page < len(dialog.Pages) {
		frame.Details.Selected = dialog.Pages[dialog.Page].ID
	}
	if v.detailDemand != nil {
		frame.Details.Output = nil
		if output, ok := v.files.details.DetailOutput(*v.detailDemand); ok {
			frame.Details.Output = &output
		}
	}
}
