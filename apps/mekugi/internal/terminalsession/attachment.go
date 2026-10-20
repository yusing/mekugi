package terminalsession

import (
	"time"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

// Attachment is the actual terminal lifetime. Its models are detached renderer
// objects; acquisition and application-session state are supplied by the caller.
type Attachment struct {
	Interaction
	Shell
	Main               *Main
	Prompts            *Prompts
	Transcript, Agents *activityui.View
	Dialog             *ContentDialog
	Diff               *DiffScreen
	Width, Height      int
	DiffUnseen         bool
	Live               previewview.PreviewPane
	LiveHidden, Faint  bool
}

func (a *Attachment) FrameInput(now time.Time, version string) FrameInput {
	in := FrameInput{
		Main: a.Main, Prompts: a.Prompts, Transcript: a.Transcript, Agents: a.Agents,
		Shell: &a.Shell, Dialog: a.Dialog, Diff: a.Diff, Live: &a.Live,
		Width: a.Width, Height: a.Height, Now: now, Version: version,
		LiveHidden: a.LiveHidden, DiffUnseen: a.DiffUnseen, Prefix: a.Prefix,
	}
	if s := a.Selection; s != nil {
		in.Selection, in.SelectionPane = s.Selection, s.Pane
		in.SelectionSource = SourceContext{Attachment: s.Attachment, Thread: s.Thread, Workspace: s.Workspace}
	}
	return in
}
