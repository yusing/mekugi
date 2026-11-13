package launch

import (
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
)

func TestNativeDetailUnknownSkillCallerIsStale(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.attached.session.RegisterThread(observation.Thread{ID: "child", Path: "/root/child"})
	sessionpresentation.ApplyActivity(u.attached.source, u.attached.childSource, nil, u.attached.session.Roster())
	for _, roster := range []bool{false, true} {
		c := &nativeClient{event: sessionapi.NativeEvent{Frame: sessionapi.NativeFrame{View: u.attached.presentation.View()}}}
		if roster {
			terminal, _ := u.terminalViews.NewAttachment(u.shell.Transcript.Painter.Theme)
			terminal.Agents.UpdateRoster(u.attached.childSource.Publication(u.attached.presentation, u.attached.source))
			c.renderer.terminal = terminal
		}
		c.details(c.event.Frame.View.Version, sessionapi.NativeDetailRequest{Kind: sessionapi.NativeSkillDetails, Agent: true, Caller: "/root/gone"})
		if len(c.calls) != 0 || c.reattach != "" || c.attachOperation != nil || c.notice != sessionpresentation.ErrStaleIntent.Error() {
			t.Fatalf("missing skill caller became a local detail, roster=%v", roster)
		}
	}
}
