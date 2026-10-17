package terminalsession

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDecoderPasteAndTerminalReplies(t *testing.T) {
	var decoder Decoder
	now := time.Now()
	var events []Input
	for _, key := range []byte("\x1b]11;rgb:ffff/ffff/ffff\x1b\\\x1b[O\x1b[200~\x02\x1b]x\x1b[201~z") {
		if event := decoder.Feed(key, now); event.Kind != NoInput {
			events = append(events, event)
		}
	}
	if len(events) < 5 || events[0].Kind != ColorInput || events[1] != (Input{FocusInput, "\x1b[O"}) || events[2].Kind != PasteStart {
		t.Fatalf("decoded prefix: %v", events)
	}
	var pasted strings.Builder
	for _, event := range events[3 : len(events)-2] {
		if event.Kind != PasteInput {
			t.Fatalf("paste became terminal input: %+v", event)
		}
		pasted.WriteString(event.Text)
	}
	if pasted.String() != "\x02\x1b]x" || events[len(events)-2] != (Input{Kind: PasteEnd}) || decoder.Pasting() || events[len(events)-1] != (Input{KeyInput, "z"}) {
		t.Fatalf("paste boundary: %q, %v", pasted.String(), events)
	}
	decoder.Feed(27, now)
	if decoder.Escape(now.Add(39*time.Millisecond)) || !decoder.Escape(now.Add(40*time.Millisecond)) || decoder.Pending() {
		t.Fatal("standalone Escape deadline changed")
	}
}

func TestReplayInterruptedReportsReset(t *testing.T) {
	for _, report := range []string{"\x1b[", "\x1b[<64;", "\x1b[Mq"} {
		var input ReplayInput
		for _, key := range []byte(report) {
			input.Consume(key)
		}
		input.Consume(27)
		if input.mouse.Active || input.x10 != 0 || input.EscapeReady() == nil {
			t.Fatalf("interrupted report %q retained state", report)
		}
		input.ExpireEscape()
		if input.Consume('+') != '+' {
			t.Fatalf("input after timeout lost for %q", report)
		}
		input.Close()
	}
}

type failedPaint struct{}

func (failedPaint) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestFrameCommitsClipboardAndRowsTogether(t *testing.T) {
	frame := Frame{Rows: []string{"old"}, Width: 8, Notices: NoticeReceipt{Attachment: 9, Composer: true}}
	frame.CopyText("source")
	clipboard := frame.Clipboard
	rows := []string{"new"}
	rendered := RenderedFrame{Rows: rows, Notices: NoticeReceipt{Attachment: 3, Feedback: 7, Composer: true, Content: true}}
	if err := frame.Write(failedPaint{}, rendered, 8, true); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write: %v", err)
	}
	if frame.Clipboard != clipboard || frame.Rows[0] != "old" || frame.Notices != (NoticeReceipt{}) {
		t.Fatal("failed paint committed pending output")
	}
	var out bytes.Buffer
	if err := frame.Write(&out, rendered, 8, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), clipboard) || !strings.Contains(out.String(), "new") || frame.Clipboard != "" || frame.Notices != rendered.Notices {
		t.Fatal("successful paint lost clipboard or frame")
	}
	out.Reset()
	if err := frame.Write(&out, rendered, 8, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "new") || strings.Contains(out.String(), "]52;") {
		t.Fatal("unchanged frame replayed output")
	}
}

func TestPaneGeometryAndDividerIntent(t *testing.T) {
	panes := Panes{Focus: 1, DiffOpen: true, Split: 67}
	layout, _, _, framed, roster := panes.Geometry(140, 30, 4)
	if !framed || roster != 4 || layout.Vertical != 67 || layout.Main.W != 65 || layout.Diff.W != 71 {
		t.Fatalf("wide layout: %+v", layout)
	}
	if handled, _ := panes.Drag(Mouse{X: 67, Y: 3}, layout, 25); !handled {
		t.Fatal("divider press escaped")
	}
	panes.Drag(Mouse{Button: 32, X: 72, Y: 3}, layout, 25)
	panes.Drag(Mouse{X: 72, Y: 3, Release: true}, layout, 25)
	if panes.Split != 72 || panes.Dragging() {
		t.Fatal("divider drag did not settle")
	}
	panes.Select(1)
	layout, _, _, _, _ = panes.Geometry(140, 30, 4)
	if panes.Focus != 0 || layout.Main.W != 138 || layout.Diff != (Rect{}) {
		t.Fatalf("closing Diff: %+v", layout)
	}
	layout, _, _, _, _ = panes.Geometry(1, 1, 0)
	if layout.Main != (Rect{}) {
		t.Fatalf("unpaintable geometry became a hit target: %+v", layout)
	}
}
