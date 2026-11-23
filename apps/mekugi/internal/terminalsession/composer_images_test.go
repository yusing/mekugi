package terminalsession

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

func testComposerPreview(w, h int, top, bottom color.RGBA) *terminalui.ImagePreview {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		c := top
		if y >= h/2 {
			c = bottom
		}
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return &terminalui.ImagePreview{Pixels: img}
}

func TestComposerImagePreviewsNativeProtocols(t *testing.T) {
	path := "/tmp/screenshot.png"
	src := terminalui.ImageSource{Path: path, Composer: true}
	preview := testComposerPreview(160, 160, color.RGBA{200, 50, 40, 255}, color.RGBA{40, 90, 210, 255})

	for _, protocol := range []terminalui.ImageProtocol{terminalui.KittyImages, terminalui.KittyFileImages, terminalui.SixelImages} {
		var main Main
		var prompts Prompts
		thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
		draft := composer.Draft{
			Text:   "[Image 1] inspect this",
			Images: []composer.Token{{Start: 0, End: len("[Image 1]"), Path: path}},
		}
		main.Update(sessionpresentation.View{
			Main:      thread,
			Displayed: thread,
			Draft:     conversation.DraftView{Available: true, Draft: draft},
		})
		transcript := activityui.NewView()
		transcript.Painter.Theme = diffrender.DarkTheme
		transcript.ImageProtocol = protocol
		if protocol == terminalui.SixelImages {
			transcript.ImageCellWidth, transcript.ImageCellHeight = 8, 16
		}
		options := MainOptions{Now: time.Unix(100, 0), Focused: true, Version: "dev"}

		// Before the preview is decoded, Main requests it and renders a bordered loading card.
		loading := main.Render(transcript, &transcript.Painter, &prompts, options, 80, 20, 0)
		if len(loading.ImageDemands) != 1 || loading.ImageDemands[0] != src {
			t.Fatalf("protocol %d loading demands = %+v, want [%+v]", protocol, loading.ImageDemands, src)
		}
		if len(loading.ImagePlacements) != 0 {
			t.Fatalf("protocol %d emitted placement before decode: %+v", protocol, loading.ImagePlacements)
		}
		if !strings.Contains(strings.Join(loading.Rows, "\n"), "#1") {
			t.Fatalf("protocol %d loading card missing #1 label", protocol)
		}

		// Once decoded, Main emits a placement inside the bordered card below the draft row.
		transcript.ImagePreviews = map[terminalui.ImageSource]*terminalui.ImagePreview{src: preview}
		loaded := main.Render(transcript, &transcript.Painter, &prompts, options, 80, 20, 0)
		if len(loaded.ImageDemands) != 0 {
			t.Fatalf("protocol %d re-demanded cached preview: %+v", protocol, loaded.ImageDemands)
		}
		if len(loaded.ImagePlacements) != 1 {
			t.Fatalf("protocol %d placements = %d, want 1", protocol, len(loaded.ImagePlacements))
		}
		p := loaded.ImagePlacements[0]
		if p.Y <= main.ComposerRect.Y || p.Y+p.Rows >= len(loaded.Rows) {
			t.Fatalf("protocol %d placement Y=%d Rows=%d out of composer bounds (ComposerRect=%+v, total=%d)", protocol, p.Y, p.Rows, main.ComposerRect, len(loaded.Rows))
		}
		if p.Columns <= 0 || p.Rows <= 0 {
			t.Fatalf("protocol %d invalid placement size: %+v", protocol, p)
		}

		// Failed decode renders a fallback card with no placement and no repeated demand.
		transcript.ImagePreviews = map[terminalui.ImageSource]*terminalui.ImagePreview{src: nil}
		failed := main.Render(transcript, &transcript.Painter, &prompts, options, 80, 20, 0)
		if len(failed.ImageDemands) != 0 || len(failed.ImagePlacements) != 0 {
			t.Fatalf("protocol %d failed decode should not demand or place: demands=%+v placements=%+v", protocol, failed.ImageDemands, failed.ImagePlacements)
		}
		if !strings.Contains(strings.Join(failed.Rows, "\n"), "img") {
			t.Fatalf("protocol %d failed decode missing fallback label", protocol)
		}
	}
}

func TestComposerImagePreviewsHalfBlockFallback(t *testing.T) {
	path := "/tmp/tmux-shot.png"
	src := terminalui.ImageSource{Path: path, Composer: true}
	preview := testComposerPreview(160, 160, color.RGBA{220, 40, 30, 255}, color.RGBA{30, 80, 220, 255})

	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	draft := composer.Draft{
		Text:   "[Image 1]",
		Images: []composer.Token{{Start: 0, End: len("[Image 1]"), Path: path}},
	}
	main.Update(sessionpresentation.View{
		Main:      thread,
		Displayed: thread,
		Draft:     conversation.DraftView{Available: true, Draft: draft},
	})
	transcript := activityui.NewView()
	transcript.Painter.Theme = diffrender.DarkTheme
	transcript.ImageProtocol = terminalui.NoImages
	transcript.ImagePreviews = map[terminalui.ImageSource]*terminalui.ImagePreview{src: preview}

	frame := main.Render(transcript, &transcript.Painter, &prompts, MainOptions{Now: time.Unix(100, 0), Focused: true, Version: "dev"}, 80, 20, 0)
	if len(frame.ImagePlacements) != 0 {
		t.Fatalf("NoImages fallback should not emit native placements: %+v", frame.ImagePlacements)
	}
	joined := strings.Join(frame.Rows, "\n")
	if !strings.Contains(joined, "▀") || !strings.Contains(joined, "#1") {
		t.Fatalf("half-block fallback thumbnail missing from rows:\n%s", joined)
	}
}

func TestComposerImagePreviewsMultipleAndOverflow(t *testing.T) {
	var tokens []composer.Token
	previews := make(map[terminalui.ImageSource]*terminalui.ImagePreview)
	preview := testComposerPreview(160, 160, color.RGBA{100, 180, 90, 255}, color.RGBA{60, 100, 180, 255})
	for i := range 8 {
		path := "/tmp/img-" + string(rune('a'+i)) + ".png"
		tokens = append(tokens, composer.Token{Start: 0, End: 9, Path: path})
		previews[terminalui.ImageSource{Path: path, Composer: true}] = preview
	}
	strip := renderComposerImages(tokens, previews, terminalui.NoImages, 0, 0, 2, 34, 6)
	if len(strip.rows) == 0 {
		t.Fatal("expected thumbnail strip rows")
	}
	joined := strings.Join(strip.rows, "\n")
	if !strings.Contains(joined, "+") {
		t.Fatalf("expected overflow badge in narrow strip:\n%s", joined)
	}
	for i, row := range strip.rows {
		if w := ansi.StringWidth(row); w > 36 {
			t.Fatalf("row %d width %d exceeds available width 36: %q", i, w, ansi.Strip(row))
		}
	}
}

func TestComposerImagePreviewsSuppressedWhenReadOnlyOrTiny(t *testing.T) {
	path := "/tmp/shot.png"
	var main Main
	var prompts Prompts
	thread := sessionpresentation.Thread{ID: "main", Caller: "/root"}
	draft := composer.Draft{
		Text:   "[Image 1]",
		Images: []composer.Token{{Start: 0, End: len("[Image 1]"), Path: path}},
	}
	transcript := activityui.NewView()
	transcript.ImageProtocol = terminalui.KittyImages

	// ReadOnly suppresses composer preview demands and strip.
	main.Update(sessionpresentation.View{
		Main:      thread,
		Displayed: thread,
		ReadOnly:  sessionpresentation.Replay,
		Draft:     conversation.DraftView{Available: true, Draft: draft},
	})
	ro := main.Render(transcript, &transcript.Painter, &prompts, MainOptions{Now: time.Unix(100, 0), Focused: true, Version: "dev"}, 80, 20, 0)
	if len(ro.ImageDemands) != 0 || len(ro.ImagePlacements) != 0 {
		t.Fatalf("read-only mode should suppress composer image previews: demands=%+v placements=%+v", ro.ImageDemands, ro.ImagePlacements)
	}

	// Tiny terminal height suppresses the preview strip gracefully.
	main.Update(sessionpresentation.View{
		Main:      thread,
		Displayed: thread,
		Draft:     conversation.DraftView{Available: true, Draft: draft},
	})
	tiny := main.Render(transcript, &transcript.Painter, &prompts, MainOptions{Now: time.Unix(100, 0), Focused: true, Version: "dev"}, 80, 6, 0)
	if len(tiny.ImageDemands) != 0 || len(tiny.ImagePlacements) != 0 {
		t.Fatalf("tiny viewport should suppress composer image previews: demands=%+v placements=%+v", tiny.ImageDemands, tiny.ImagePlacements)
	}
}
