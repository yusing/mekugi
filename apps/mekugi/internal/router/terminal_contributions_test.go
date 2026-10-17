package router

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

func TestNativeAuxiliaryAbsencePreservesMainAndReviewSource(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.terminalViews = terminalsession.Views{}
	u.ensureShell()
	u.shell.Panes.Restore(terminalsession.Preferences{Focus: 1, DiffOpen: true})
	u.shell.saved.workspace, u.shell.saved.coverage = "/workspace", "READY"
	u.shell.publishReview()
	if err := u.paint(io.Discard, 160, 32); err != nil {
		t.Fatal(err)
	}
	shell := u.shell
	if shell.Auxiliary != nil || shell.Focus != 0 || shell.DiffOpen || shell.Layout.Diff.W != 0 || shell.Tabs[1].W != 0 {
		t.Fatalf("absent contribution retained interaction: %+v", shell.Shell)
	}
	if strings.Contains(shell.nativeStatus(), "Diff") {
		t.Fatal("absent pane retained a selector")
	}
	for _, key := range []byte("\x022hello") {
		if err := shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.Text != "hello" || wire.Len() != 0 {
		t.Fatalf("Main input changed: %q %q", u.Text, wire.String())
	}
	if view := u.reviewSource.ReviewView(u.session.Scope()); view.Workspace != "/workspace" || view.Coverage != "READY" {
		t.Fatal("removing a view removed its independent source publication")
	}
	if state := shell.paneState(); state.Focus != 0 || state.DiffOpen || state.NavigatorColumns != 0 {
		t.Fatalf("absent preference: %+v", state)
	}
}

func TestReplayAuxiliaryAbsenceSurvivesReset(t *testing.T) {
	owner, _ := newAppServerTestUI()
	start := time.Unix(1, 0)
	p := newUIReplayPlayback(terminalsession.Views{}, owner.reviewSource, owner.presentationState, owner.conversationState, owner.session.observations, owner.session.activity, t.Context(), &playback.Source{Thread: "main", Start: start, End: start.Add(time.Second)}, 1)
	defer p.close()
	p.ui.shell.Focus, p.ui.shell.DiffOpen = 1, true
	p.reset()
	if p.ui.shell.Auxiliary != nil || p.ui.shell.Focus != 0 || p.ui.shell.DiffOpen {
		t.Fatal("replay recreated an absent contribution")
	}
	if err := p.ui.paint(io.Discard, 120, 24); err != nil {
		t.Fatal(err)
	}
}
