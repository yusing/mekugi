package terminalsession

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

type pasteBuffer struct {
	intent         sessionpresentation.PasteIntent
	prompt         PromptReceipt
	secret         bool
	secretRevision uint64
	payload        []byte
}

// PasteResult is ephemeral input, never a published view or retained record.
type PasteResult struct {
	Intent sessionpresentation.PasteIntent
	Text   string
}

func (m *Main) Pasting() bool { return m.paste != nil }
func (m *Main) CancelPaste()  { m.paste = nil }

// PasteInput consumes decoder-framed bytes. Admission preserves the destination
// captured at the opening marker while allowing unrelated publication updates.
func (m *Main) PasteInput(event Input, prompts *Prompts) (*PasteResult, error) {
	switch event.Kind {
	case PasteStart:
		m.paste = nil
		if !m.PrepareInput(true).Editable {
			return nil, nil
		}
		m.Keybindings = false
		p := &pasteBuffer{intent: sessionpresentation.PasteIntent{Thread: m.session.Input.ID, Version: m.session.Version, Target: m.session.EditorTarget()}, prompt: prompts.receipt}
		if menu := m.session.Menu; menu != nil {
			p.intent.Menu = menu.ID
		}
		if q := m.session.Questions; q.Call != nil && q.Call.Questions[q.Index].Secret {
			p.secret, p.secretRevision = true, prompts.Secret.Revision()
		}
		m.paste = p
	case PasteInput:
		if m.paste != nil {
			m.paste.payload = append(m.paste.payload, event.Text...)
		}
	case PasteEnd:
		p := m.paste
		m.paste = nil
		if p == nil {
			return nil, nil
		}
		if !p.intent.Version.SameAttachment(m.session.Version) || p.secret && (p.prompt != prompts.receipt || p.secretRevision != prompts.Secret.Revision()) {
			return nil, sessionpresentation.ErrStaleIntent
		}
		return &PasteResult{Intent: p.intent, Text: string(pasteText(p.payload))}, nil
	}
	return nil, nil
}

// Sanitize only after delimiting the payload, so a partial escape cannot consume
// the closing marker. Source: apps/mekugi/internal/router/app_server_clipboard.go:219:239@ef878723.
func pasteText(payload []byte) []byte {
	var text []byte
	for len(payload) > 0 {
		b := payload[0]
		if b == 27 && len(payload) > 1 && (payload[1] == '[' || payload[1] == ']') {
			_, _, n, _ := ansi.DecodeSequence(payload, 0, nil)
			if n > 0 {
				payload = payload[n:]
				continue
			}
		}
		payload = payload[1:]
		if b == '\r' {
			b = '\n'
		}
		if b >= 32 || b == '\n' || b == '\t' {
			text = append(text, b)
		}
	}
	return text
}
