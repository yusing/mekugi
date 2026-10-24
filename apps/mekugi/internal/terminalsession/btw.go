package terminalsession

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
	conversationui "github.com/yusing/mekugi/modules/conversation/terminalview"
	"time"
)

// BTWDock retains only terminal interaction state for a published exchange.
type BTWDock struct {
	version      sessionpresentation.Version
	body         *conversationmodel.SideView
	running      bool
	Scroll, Rows int
	Rect         Rect
	CopyAnswer   []string
}

func (s *BTWDock) Update(v sessionpresentation.View) {
	if v.BTW == nil {
		*s = BTWDock{}
		return
	}
	if s.body == nil || s.version.Attachment != v.Version.Attachment || s.body.Key != v.BTW.Key || s.body.ID != v.BTW.ID {
		*s = BTWDock{}
	}
	s.version, s.body, s.running = v.Version, v.BTW, v.BTWRunning
}
func (s *BTWDock) Open() bool { return s.body != nil }

// Source: apps/mekugi/internal/router/app_server_btw.go:100:117@630ce384.
func (s *BTWDock) Key(key string) (handled bool, close *sessionpresentation.CloseSideIntent) {
	if s.body == nil {
		return false, nil
	}
	switch key {
	case "\x1b":
		return true, &sessionpresentation.CloseSideIntent{Version: s.version, Key: s.body.Key, Exchange: s.body.ID}
	case "\x1b[5~":
		s.Scroll += max(1, s.Rows-1)
		return true, nil
	case "\x1b[6~":
		s.Scroll = max(0, s.Scroll-max(1, s.Rows-1))
		return true, nil
	}
	return false, nil
}

// Source: apps/mekugi/internal/router/app_server_btw.go:349:362@[ce8700a8].
func (s *BTWDock) Render(painter *activityui.Painter, now time.Time, width, height int) conversationui.SideFrame {
	if s.body == nil {
		return conversationui.SideFrame{}
	}
	status := textsanitize.Safe(s.body.Status, false)
	if s.running && s.body.Status == "Answering" && !s.body.Alert && !s.body.Started.IsZero() {
		elapsed := max(time.Duration(0), now.Sub(s.body.Started))
		status = activityui.ReasoningShimmer("Answering", elapsed, painter.Colors)
		if duration := activityui.FormatLiveDuration(elapsed); duration != "" {
			status += terminalui.Dim + " · " + duration + terminalui.Reset
		}
	}
	frame := conversationui.Side(*s.body, &painter.Painter, status, width, height, s.Scroll)
	s.CopyAnswer, s.Rows, s.Scroll = frame.CopyAnswer, frame.AnswerRows, frame.Scroll
	return frame
}
