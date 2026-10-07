package router

import (
	"fmt"

	"github.com/yusing/mekugi/internal/session"
)

func (u *appServerUI) flushRuntimeBTW(b *appServerBTW) error {
	client := u.runtime.client.(session.SideClient)
	if b.nativeID == "" {
		u.runtime.serial++
		b.nativeID = fmt.Sprintf("btw/%d", u.runtime.serial)
	}
	input := append([]session.InputPart{{Text: btwInstruction}}, runtimeInputParts(b.pending)...)
	if err := client.SendSide(u.ctx, session.SideInput{ID: b.nativeID, Source: u.thread, Input: input}); err != nil {
		u.failBTW(b, "Side input not sent: "+err.Error())
		return nil
	}
	b.starting, b.status, b.started = true, "Answering", u.now()
	return nil
}

// Native side events update only the original dock, never Main or its owners.
func (u *appServerUI) runtimeBTWEvent(e session.Event) {
	b := u.btw
	if b == nil || b.nativeID != e.SideID {
		return
	}
	switch e.Kind {
	case "session":
		if e.SessionID == "" || e.SessionID == u.thread {
			u.failBTW(b, "Native runtime did not return a separate side session")
			_ = u.closeBTW()
			return
		}
		b.thread, b.starting = e.SessionID, false
	case "message":
		if e.Role != "Claude" {
			return
		}
		index := b.answerIndex(e.ID)
		if index < 0 {
			return
		}
		b.answer[index].text = ""
		b.appendAnswer(index, e.Text)
	case "done":
		b.busy, b.starting, b.alert = false, false, e.Failed
		b.pending = composerDraft{}
		b.status = "completed"
		if e.Failed {
			b.status = "Side turn ended: " + e.Text
		}
		u.pruneDraftImages()
	case "error":
		u.failBTW(b, "Side query failed: "+e.Text)
	case "side_closed":
		b.nativeID, b.thread, b.busy, b.starting = "", "", false, false
	}
	u.dirty = true
}
