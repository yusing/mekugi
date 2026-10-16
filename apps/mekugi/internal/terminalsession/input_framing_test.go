package terminalsession

import (
	"strings"
	"testing"
	"time"
)

func TestDecoderPasteFraming(t *testing.T) {
	for _, payload := range []string{
		"",
		"opaque\x02\x03\x16\r\n你好",
		"\x1b[200~\x1b]11;rgb:ffff/ffff/ffff\x07\x1b[<0;1;1M",
		"\x1b[201x",
		"\x1b\x1b[201\x1b[20x",
		"trailing\x1b[201",
	} {
		t.Run(payload, func(t *testing.T) {
			var decoder Decoder
			now := time.Unix(1, 0)
			for _, b := range []byte("\x1b[200~") {
				event := decoder.Feed(b, now)
				if b == '~' {
					if event.Kind != PasteStart || !decoder.Pasting() {
						t.Fatalf("paste start: %+v, pasting=%v", event, decoder.Pasting())
					}
				} else if event.Kind != NoInput {
					t.Fatalf("partial paste start emitted %+v", event)
				}
			}
			var pasted strings.Builder
			for _, b := range []byte(payload) {
				event := decoder.Feed(b, now)
				switch event.Kind {
				case NoInput:
				case PasteInput:
					pasted.WriteString(event.Text)
				default:
					t.Fatalf("opaque payload emitted %+v", event)
				}
				if !decoder.Pasting() {
					t.Fatal("mismatched terminator ended paste")
				}
			}
			for i, b := range []byte("\x1b[201~") {
				event := decoder.Feed(b, now)
				if i == len("\x1b[201~")-1 {
					if event != (Input{Kind: PasteEnd}) || decoder.Pasting() {
						t.Fatalf("paste end: %+v, pasting=%v", event, decoder.Pasting())
					}
					continue
				}
				switch event.Kind {
				case NoInput:
				case PasteInput:
					pasted.WriteString(event.Text)
				default:
					t.Fatalf("partial terminator emitted %+v", event)
				}
				if !decoder.Pasting() {
					t.Fatal("partial terminator ended paste")
				}
			}
			if pasted.String() != payload || decoder.Pending() {
				t.Fatalf("payload=%q want=%q pending=%v", pasted.String(), payload, decoder.Pending())
			}
			if event := decoder.Feed('z', now); event != (Input{KeyInput, "z"}) {
				t.Fatalf("post-paste key: %+v", event)
			}
		})
	}
}

func TestDecoderCompleteKeySequence(t *testing.T) {
	for _, key := range []string{"\x1b[D", "\x1b[1;5D", "\x1bOH", "\x1bb"} {
		t.Run(key, func(t *testing.T) {
			var decoder Decoder
			for i, b := range []byte(key) {
				event := decoder.Feed(b, time.Unix(1, 0))
				if i == len(key)-1 {
					if event != (Input{KeyInput, key}) || decoder.Pending() {
						t.Fatalf("complete key: %+v, pending=%v", event, decoder.Pending())
					}
				} else if event.Kind != NoInput || !decoder.Pending() {
					t.Fatalf("partial key emitted %+v, pending=%v", event, decoder.Pending())
				}
			}
		})
	}
}
