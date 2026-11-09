package launch

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func (v *nativeSessionView) reserveFileCopy() *nativeEffectReservation {
	reservation := v.effects.current
	if reservation == nil {
		reservation = v.effects.reserve(1)
	}
	if reservation == nil || reservation.remaining == 0 {
		return nil
	}
	// Transfer one admitted consequence to asynchronous acquisition.
	reservation.remaining--
	return &nativeEffectReservation{owner: &v.effects, remaining: 1}
}

func (v *nativeSessionView) openNoticeDetails(text string) string {
	v.files.openDetailSelection(&v.attached.session, v.attached.presentation.View().Version, v.attached.source, activity.DetailPages([]activity.Value{{Kind: "error", Body: text}}), false)
	v.detailDemand = nil
	return ""
}

func (v *nativeSessionView) openDetails(request sessionapi.NativeDetailRequest) string {
	view := v.attached.presentation.View()
	thread := request.Displayed
	if thread == (sessionpresentation.Thread{}) {
		thread = view.Displayed
	}
	if request.Kind == sessionapi.NativeSkillDetails && request.Agent {
		found := false
		for _, agent := range v.attached.session.Roster() {
			if agent.Name == request.Caller {
				thread = sessionpresentation.Thread{ID: agent.Thread, Caller: agent.Name}
				found = true
				break
			}
		}
		if !found {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		if thread.ID == v.thread {
			request.Agent, request.Caller = false, "Main"
		}
	}
	source := v.attached.source
	if request.Agent {
		source = v.attached.childSource
	}
	v.files.openDiff = nil
	if !v.remoteHuman && time.Now().UnixMilli() < v.viewerLease.DiffOpenExpires {
		v.files.openDiff = func(page activity.DetailPage) {
			id := uint64(1)
			if v.diffOpen != nil {
				id = v.diffOpen.ID + 1
			}
			v.diffOpen = &sessionapi.NativeDiffOpen{ID: id, Path: page.Path, Title: page.Verb, Source: page.Code}
			v.files.closeOutput()
		}
	}
	switch request.Kind {
	case sessionapi.NativeFileDetails:
		v.files.textOnly = v.remoteHuman
		v.files.externalFile = !v.remoteHuman && time.Now().UnixMilli() < v.viewerLease.FileOpenExpires
		copyReservation := v.reserveFileCopy()
		if copyReservation == nil {
			return "Clipboard delivery capacity is exhausted."
		}
		if !v.files.openMarkdownFile(&v.attached.session, v.attached.presentation, v.attached.cwd, source, request.Path) {
			copyReservation.release()
			return "The local file target is unavailable."
		}
		if v.files.fileReads == nil {
			copyReservation.release()
			break
		}
		cancel := v.files.cancelRead
		v.files.cancelRead = func() { cancel(); copyReservation.release() }
		v.files.copyFilePath = func(path string) {
			copyReservation.add(sessionapi.NativeEffect{Thread: v.thread, Clipboard: new(path)})
		}
		if v.files.externalFile {
			v.files.openFile = func(path string, first, last int) {
				id := uint64(1)
				if v.fileOpen != nil {
					id = v.fileOpen.ID + 1
				}
				v.fileOpen = &sessionapi.NativeFileOpen{ID: id, Path: path, First: first, Last: last}
			}
		}
	case sessionapi.NativeErrorDetails:
		v.files.openErrorDetails(&v.attached, source)
	case sessionapi.NativeSkillDetails:
		v.files.openSkills(&v.attached, source, request.Caller)
	case sessionapi.NativeEntryDetails:
		if len(request.Sources) == 0 && request.Path == "" {
			for _, entry := range source.Entries {
				n := entry.Source.Native
				if entry.Source.Seq == request.Source && entry.Source.Kind == "compaction" && n != nil && n.SummaryPending {
					read := v.attached.hooks.compaction(n.Thread, n.Item)
					v.files.openCompactionSummary(&v.attached.session, v.attached.presentation, v.attached.cwd, source, read)
					v.detailDemand = nil
					return ""
				}
			}
		}
		if request.Edit || request.Path != "" {
			if !v.files.openActivityEdit(&v.attached, source, v.changes, request.Source, request.Path) {
				return sessionpresentation.ErrStaleIntent.Error()
			}
			break
		}
		if !request.Agent && len(request.Sources) == 0 && v.files.openActivityReply(&v.attached, request.Source) {
			break
		}
		sources := request.Sources
		if len(sources) == 0 {
			sources = []uint64{request.Source}
		}
		var pages []activity.DetailPage
		for _, seq := range sources {
			next := entryDetailPages(source, seq)
			if len(next) == 0 {
				return sessionpresentation.ErrStaleIntent.Error()
			}
			pages = append(pages, next...)
			// Source-only row details also expose retained edits, without replacing
			// the Activity pages needed by source and output disclosures.
			if request.Source == seq && request.Operation == 0 && request.Output == 0 && len(request.Sources) == 0 {
				if edits, _ := activityEditPages(&v.attached, source, v.changes, seq, ""); len(edits) > 0 {
					pages = append(pages, edits...)
				}
			}
		}
		if len(pages) == 0 {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		selected := -1
		for i, page := range pages {
			if page.Source == request.Source && page.Output == request.Output && (request.Operation == 0 || page.Operation == request.Operation) {
				selected = i
				break
			}
		}
		if selected < 0 && request.Operation != 0 {
			return sessionpresentation.ErrStaleIntent.Error()
		}
		v.files.openDetailSelection(&v.attached.session, view.Version, source, pages, false)
		if selected >= 0 {
			v.files.selected = pages[selected].ID
		}
	default:
		return "The detail action is unavailable."
	}
	v.detailDemand = nil
	return ""
}

func (v *nativeSessionView) publishDetails(frame *sessionapi.NativeFrame) {
	defer func() {
		frame.FileOpen = v.fileOpen
		frame.DiffOpen = v.diffOpen
	}()
	select {
	case result := <-v.files.fileReads:
		v.files.loadedMarkdownFile(&v.attached.session, v.attached.presentation, v.attached.cwd, result)
	default:
	}
	if v.files.id.Selection == 0 || v.files.pendingFile {
		return
	}
	if v.files.id.Attachment != frame.View.Version.Attachment {
		v.files.closeOutput()
		v.detailDemand = nil
		return
	}
	view, ok := v.files.detailView(&v.attached.session)
	if !ok {
		return
	}
	frame.Details = &sessionapi.NativeDetails{Source: frame.View.Main, View: view, File: v.files.file, Agent: v.files.agent, Selected: v.files.selected}
	for _, page := range view.Pages {
		if page.ID == v.files.selected && page.Retained {
			request := sessionpresentation.DetailOutputRequest{ID: view.ID, Page: page.ID, Version: page.OutputVersion}
			if output, ok := v.files.details.DetailOutput(request); ok {
				frame.Details.Output = &output
			}
			break
		}
	}
	if v.detailDemand != nil {
		frame.Details.Output = nil
		if output, ok := v.files.details.DetailOutput(*v.detailDemand); ok {
			frame.Details.Output = &output
		}
	}
}
