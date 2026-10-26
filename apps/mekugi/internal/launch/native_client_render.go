package launch

import (
	"context"
	"io"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	changeui "github.com/yusing/mekugi/modules/changehistory/terminalview"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	previewui "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

// nativeClientRenderer binds socket publications to the existing native views.
// The caller owns draft/prompt editors; this binding owns no semantic source,
// history store, acquisition callback or host request ledger.
type nativeClientRenderer struct {
	terminal   *terminalsession.Attachment
	views      terminalsession.Views
	frame      sessionapi.NativeFrame
	attachment uint64
}

func (c *nativeClientRenderer) update(frame sessionapi.NativeFrame, draft conversation.DraftView, prompt conversation.PromptDraftView, now time.Time) {
	u := c.terminal
	if c.attachment != frame.View.Version.Attachment {
		c.replace(frame.View.Version.Attachment)
	}
	view := frame.View
	// Availability and prediction belong to the server. Text, caret, history and
	// editor revisions remain client-local and never arrive through publications.
	draft.Available, draft.Prediction = view.Draft.Available, view.Draft.Prediction
	view.Draft = draft
	view.ProjectComposerStatus(draft.Draft.Text != "")
	if prompt.Call == view.PromptDraft.Call && prompt.Question == view.PromptDraft.Question && prompt.Request == view.PromptDraft.Request {
		view.PromptDraft = prompt
	}
	u.Prompts.Update(view)
	u.Main.Update(view)
	display := activityui.Display{Caller: view.Displayed.Caller, SideCaller: view.SideThread.Caller, Attachment: view.Version.Attachment, Revision: view.Version.Revision}
	u.Transcript.StatusLabel = terminalsession.StatusText(view.Status.State)
	main, agents := frame.Main, frame.Agents
	if c.frame.Main != main || c.frame.Agents != agents {
		previousThreads, currentCallers := make(map[string]string), make(map[string]string)
		for _, previous := range []*activity.Publication{c.frame.Main, c.frame.Agents} {
			if previous == nil {
				continue
			}
			for _, row := range previous.Roster {
				previousThreads[row.Agent.Name] = row.Agent.Thread
			}
		}
		for _, current := range []*activity.Publication{main, agents} {
			if current != nil {
				for _, row := range current.Roster {
					currentCallers[row.Agent.Thread] = row.Agent.Name
				}
			}
		}
		for _, painter := range []*activityui.View{u.Transcript, u.Agents} {
			for _, selected := range []*string{&painter.Selected, &painter.RosterSelected} {
				if thread := previousThreads[*selected]; thread != "" {
					if caller := currentCallers[thread]; caller != "" && caller != *selected {
						*selected, painter.Runs = caller, nil
					}
				}
			}
		}
	}
	if main == nil {
		main = new(activity.Publication)
	}
	if agents == nil {
		agents = new(activity.Publication)
	}
	u.Transcript.Update(main, display)
	if frame.Agents != nil {
		u.Agents.MainSummary = u.Transcript.SummaryBlocks(frame.Agents.MainSummary)
	}
	u.Agents.MainStatus = u.Transcript.StatusLabel
	agentDisplay := display
	if view.Displayed.ID == view.Main.ID && view.Info.Caller != "" {
		agentDisplay.Caller = view.Info.Caller
	}
	u.Agents.Update(agents, agentDisplay)
	u.Transcript.Hydrate(frame.MainOutput)
	u.Agents.Hydrate(frame.AgentOutput)
	for _, painter := range []*activityui.View{u.Transcript, u.Agents} {
		painter.Painter.FileLink = markdownFileLinks(view.SourceWorkspace())
		painter.Painter.DeferFileLinks = true
	}
	if pane, ok := u.Auxiliary.(*changeui.Pane); ok && frame.Review != nil {
		pane.PublishReview(*frame.Review)
	}
	for _, live := range frame.Live {
		u.Live.Observe(live, false, now)
	}
	if frame.LiveScope.Workspaces != nil {
		ids := make([]string, 0, len(frame.Live))
		for _, live := range frame.Live {
			ids = append(ids, live.ID)
		}
		u.Live.Resync(frame.LiveScope, ids)
	}
	if detail := frame.Details; detail != nil {
		opened := u.Dialog == nil || u.Dialog.ID != detail.View.ID
		if opened {
			u.Dialog = &terminalsession.ContentDialog{ID: detail.View.ID}
		}
		u.Dialog.View = u.Transcript
		if detail.Agent {
			u.Dialog.View = u.Agents
		}
		u.Dialog.File, u.Dialog.Output = detail.File, detail.Output
		u.Dialog.Update(detail.View)
		if opened {
			for i, page := range u.Dialog.Pages {
				if page.ID == detail.Selected {
					u.Dialog.ShowPage(i)
					break
				}
			}
		}
	} else {
		u.Dialog = nil
	}
	c.frame = frame
	c.frame.View = view
}

func (c *nativeClientRenderer) replace(attachment uint64) {
	u := c.terminal
	c.views.ReplaceConversation(u)
	*u.Main = terminalsession.Main{InterruptLocked: u.Main.InterruptLocked, PredictionsDisabled: u.Main.PredictionsDisabled}
	u.Frame = terminalsession.Frame{Clipboard: u.Frame.Clipboard, Images: u.Frame.Images}
	u.Selection, u.Dialog = nil, nil
	u.Live = previewui.PreviewPane{}
	u.LiveHidden, u.DiffUnseen = false, false
	if u.Auxiliary != nil {
		u.Auxiliary.Reset()
	}
	c.attachment = attachment
}

// render returns immutable hydration demands to the transport caller. It never
// consults server-side reducers or marks undelivered prompts ready for input.
func (c *nativeClientRenderer) render(ctx context.Context, out io.Writer, now time.Time, version string) (terminalsession.RenderedFrame, error) {
	u := c.terminal
	u.Live.Tick(now)
	in := u.FrameInput(now, version)
	in.FileLinks = c.frame.FileLinks
	frame, err := u.Frame.Render(ctx, in)
	if err != nil || len(frame.Demands) > 0 {
		return frame, err
	}
	if err := u.Frame.Write(out, frame, u.Width, u.Faint); err != nil {
		return frame, err
	}
	if frame.PromptVisible {
		u.Prompts.Delivered(frame.Prompt)
	}
	if !frame.SelectionRetained {
		u.Selection = nil
	}
	if frame.DiffPainted {
		u.DiffUnseen = false
	}
	return frame, nil
}

// advancePresentation keeps all existing reveal cursors local to the physical
// renderer, including multi-operation pacing after retained source publication.
func (c *nativeClient) advancePresentation(now time.Time) bool {
	u := c.renderer.terminal
	reasoning := c.streams.AdvanceReasoning(now, u.Transcript, u.Agents)
	commands := c.streams.AdvanceCommands(now, nil, u.Transcript, u.Agents)
	main, agents := u.Transcript.Pace(now), u.Agents.Pace(now)
	return reasoning || commands || main || agents || c.event.Frame.View.Status.Animating || u.Agents.HasLiveStatus()
}
