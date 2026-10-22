package launch

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeShellReservationSurvivesHostAcknowledgement(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected=%t", rejected), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.startSession("main", "/workspace")
			n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
			defer n.close()
			if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
				t.Fatal(refusal)
			}
			view := n.views["view"]
			u.attached, u.shell, u.picker = view.attached, view.shell, view.picker
			u.attached.restoreShell = n.restoreShell
			reservation := view.effects.reserve(1)
			view.effects.current = reservation
			u.Editor = &composer.Editor{Text: "!pwd"}
			if err := submitShell(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, u.clock, u.restoring != nil, u.skillEnvironment, u.ctx, u.review, &u.shellEdits, u.retained, u.retainedSession, u.changeSource, u.snapshots, u.recovered, u.publishChanges, u.execWindows, u.audience, u.captureNotice, u.reviewSource, u.preparePane); err != nil {
				t.Fatal(err)
			}
			view.effects.current = nil
			reservation.release()
			n.reconcileEffects(view)
			if u.attached.conversation.Shell.Delivery == 0 || view.effects.reserved != 1 {
				t.Fatal("input admission released restoration before host settlement")
			}
			for range nativeSemanticEffects - 1 {
				view.effects.add(sessionapi.NativeEffect{Clipboard: new("earlier consequence")})
			}
			for range 200 {
				view.effects.deliver("main", []sessionpresentation.ActivityDelivery{{Command: &sessionpresentation.CommandDelivery{Delta: "progress"}}})
				view.effects.activityPublication()
			}
			if view.effects.reserve(1) != nil {
				t.Fatal("full retained consequences admitted another operation")
			}
			if rejected {
				appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"rejected"}}`)
			} else {
				appServerTestMessage(t, u, `{"id":1,"result":{}}`)
			}
			n.reconcileEffects(view)
			if view.effects.reserved != 0 || len(view.effects.inputs) != 0 || view.ctx.Err() != nil {
				t.Fatal("settled host outcome retained admission capacity or closed view")
			}
			if rejected {
				last := view.effects.pending[len(view.effects.pending)-1]
				if len(view.effects.pending) != nativeSemanticEffects || last.ShellRestore == nil || last.ShellRestore.Text != "!pwd" {
					t.Fatal("rejected shell lost its reserved originating draft")
				}
			} else if len(view.effects.pending) != nativeSemanticEffects-1 || view.effects.reserve(1) == nil {
				t.Fatal("accepted input did not release restoration capacity")
			}
		})
	}
}

func TestNativeNotificationCoalescesWithoutSemanticCapacity(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, "main"); refusal != "" {
		t.Fatal(refusal)
	}
	view := n.views["view"]
	for range nativeSemanticEffects {
		view.effects.add(sessionapi.NativeEffect{Restore: &composer.Draft{Text: "retained"}})
	}
	for i := range 200 {
		n.notify(u.attached.executionPresentation, "agent-turn-complete", fmt.Sprintf("completion %d", i))
	}
	latest := view.notification
	if latest == nil || latest.Notification.Message != "completion 199" || len(view.effects.pending) != nativeSemanticEffects {
		t.Fatal("desktop attention consumed semantic retention or lost latest signal")
	}
	c := nativeTestPromptClient(u)
	var out bytes.Buffer
	c.notifications.out, c.notifications.ready = &out, true
	c.notifications.settings.Method, c.notifications.settings.Condition = "bel", "always"
	frame := sessionapi.NativeEvent{Attachment: "client", Frame: sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Effects: view.effects.publication(), Notification: latest}}
	frame.Frame.Facts.Notifications.Ready = true
	frame.Frame.Facts.Notifications.Method, frame.Frame.Facts.Notifications.Condition = "bel", "always"
	c.accept(frame)
	c.accept(frame)
	if out.String() != "\a" {
		t.Fatalf("repeated frame emitted desktop notification more than once: %q", out.String())
	}
	if refusal := view.applyView(sessionapi.NativeOperation{Receipt: &sessionapi.NativeReceipt{Effect: c.effect}}); refusal != "" {
		t.Fatal(refusal)
	}
	if view.notification != nil {
		t.Fatal("desktop receipt retained an already-emitted signal")
	}
}
