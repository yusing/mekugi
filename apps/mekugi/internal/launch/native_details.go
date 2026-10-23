package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Native child dialogs keep the source view alive independently of navigation.
// The parent's existing selection owner supplies IDs for all of its dialogs.
type nativeChildDetails struct {
	observation *sessionapi.Observation
	id          sessionpresentation.DetailID
}

func (v *nativeSessionView) closeChildDetails() {
	if details := v.childDetails; details != nil {
		details.observation.Close()
		v.files.details.CloseDetails(details.id)
		v.childDetails = nil
	}
}

func (v *nativeSessionView) openNoticeDetails(text string) string {
	v.closeChildDetails()
	openBlocks(&v.attached, v.shell, &v.files, v.shell, v.shell.Transcript, []activityui.Block{{Kind: "error", Body: text}})
	v.detailDemand = nil
	return ""
}

func (v *nativeSessionView) openDetails(request sessionapi.NativeDetailRequest) string {
	view := v.attached.presentation.View()
	thread := request.Displayed
	if thread == (sessionpresentation.Thread{}) {
		thread = view.Displayed
	}
	if v.children != nil {
		if _, live := v.children.Descendants(v.thread)[thread.ID]; live {
			observer, err := v.children.Observe(v.ctx, v.thread, thread.ID)
			if err != nil {
				return err.Error()
			}
			request.Displayed, request.Agent = thread, false
			if err := observer.Read(sessionapi.NativeOperation{Details: &request}); err != nil {
				observer.Close()
				return err.Error()
			}
			v.closeChildDetails()
			v.files.closeOutput(v.shell)
			id := v.files.details.OpenDetails(view.Version, nil, nil, false, nil)
			v.childDetails = &nativeChildDetails{observation: observer, id: id}
			return ""
		}
	}
	v.closeChildDetails()
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
	if v.shell.Dialog != nil {
		v.closeChildDetails()
	}
	if child := v.childDetails; child != nil {
		if child.id.Attachment != frame.View.Version.Attachment {
			v.closeChildDetails()
			return
		}
		event, err := child.observation.Snapshot()
		if err != nil {
			frame.View.Loading = err.Error()
		}
		if event.Frame.Details != nil {
			details := *event.Frame.Details
			details.View.ID, details.Agent = child.id, true
			if details.Output != nil {
				output := *details.Output
				output.ID = child.id
				details.Output = &output
			}
			frame.Details = &details
		}
		return
	}
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
	frame.Details = &sessionapi.NativeDetails{Source: frame.View.Main, View: view, Output: dialog.Output, File: dialog.File, Agent: dialog.View == v.shell.Agents}
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
