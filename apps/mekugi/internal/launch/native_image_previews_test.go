package launch

import (
	"bytes"
	"encoding/base64"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/rollout"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/sessionreplay/playback"
)

func TestUISnapshotNativeImagePreviews(t *testing.T) {
	for _, protocol := range []terminalui.ImageProtocol{terminalui.NoImages, terminalui.KittyImages, terminalui.SixelImages, terminalui.KittyFileImages} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			for _, key := range []string{"TMUX", "TMUX_PANE", "ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_VERSION", "KITTY_WINDOW_ID", "WEZTERM_EXECUTABLE", "WEZTERM_VERSION", "WT_SESSION", "TERM_PROGRAM"} {
				t.Setenv(key, "")
			}
			t.Setenv("TERM", map[terminalui.ImageProtocol]string{terminalui.NoImages: "xterm", terminalui.KittyImages: "xterm-kitty", terminalui.SixelImages: "foot", terminalui.KittyFileImages: "xterm-256color"}[protocol])
			if protocol == terminalui.KittyFileImages {
				t.Setenv("TERM_PROGRAM", "iTerm.app")
				t.Setenv("TERM_PROGRAM_VERSION", "3.6.1")
			}
			workspace := t.TempDir()
			path := filepath.Join(workspace, "preview.png")
			if err := os.WriteFile(path, previewFixturePNG(t), 0600); err != nil {
				t.Fatal(err)
			}
			u := newAppServerSessionTestUI(t, workspace)
			now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			u.clock = func() time.Time { return now }
			for i, kind := range []string{"imageView", "imageGeneration"} {
				item := binding.ThreadItem{ID: fmt.Sprint(i), Type: kind, Path: path, SavedPath: path, Status: "completed"}
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
			}
			u.testMainFrame(80, 24, 0)
			frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source)}
			wire, err := json.Marshal(frame, jsonv1.FormatDurationAsNano(true))
			if err != nil {
				t.Fatal(err)
			}
			var received sessionapi.NativeFrame
			if err := json.Unmarshal(wire, &received, jsonv1.FormatDurationAsNano(true)); err != nil {
				t.Fatal(err)
			}
			if len(received.Main.Entries) != 2 || len(received.Main.Entries[1].Images) != 1 {
				t.Fatalf("host/native image binding lost: %+v", received.Main.Entries)
			}
			terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
			previews := imagePreviews{}
			t.Cleanup(previews.close)
			c := nativeClient{ctx: t.Context(), renderer: nativeClientRenderer{terminal: terminal, views: u.terminalViews}}
			c.renderer.update(received, conversation.DraftView{}, conversation.PromptDraftView{}, now)
			terminal.Width, terminal.Height = 80, 24
			terminal.Transcript.Expansion = 2
			terminal.Frame.Images = new(terminalui.ImageFrame)
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24, X: 640, Y: 384}); err != nil {
				t.Fatal(err)
			}
			source := terminal.FrameInput(now, "test").SourceContext()
			previews.prepare(slave, terminal, source)
			var output bytes.Buffer
			painted, err := c.renderer.render(t.Context(), &output, now, "test")
			if err != nil {
				t.Fatal(err)
			}
			if protocol != terminalui.NoImages {
				for range 2 {
					previews.request(t.Context(), source, painted.ImageDemands)
					select {
					case result := <-previews.results:
						previews.finish(result)
					case <-time.After(3 * time.Second):
						t.Fatal("visible image was not acquired")
					}
				}

				previews.prepare(slave, terminal, source)
				output.Reset()
				painted, err = c.renderer.render(t.Context(), &output, now, "test")
				if err != nil {
					t.Fatal(err)
				}
				if len(painted.Images.Placements) != 2 {
					t.Fatalf("missing placements: %+v; demands %v", painted.Images, painted.ImageDemands)
				}
				for _, p := range painted.Images.Placements {
					if p.Y+p.Rows >= 24 || p.X+p.Columns > 80 {
						t.Fatalf("preview outside transcript: %+v", p)
					}
				}
				marker := "\x1b_G"
				if protocol == terminalui.SixelImages {
					marker = "\x1bP"
				}
				if !strings.Contains(output.String(), marker) {
					t.Fatal("no graphics emitted")
				}
				output.Reset()
				if _, err := c.renderer.render(t.Context(), &output, now, "test"); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(output.String(), marker) {
					t.Fatal("unchanged frame retransmits graphics")
				}
				terminal.Main.Keybindings = true
				output.Reset()
				hidden, err := c.renderer.render(t.Context(), &output, now, "test")
				if err != nil {
					t.Fatal(err)
				}
				if len(hidden.Images.Placements) != 0 || len(terminal.Frame.Images.Placements) != 0 {
					t.Fatal("overlay retained images")
				}
				if protocol != terminalui.SixelImages && !strings.Contains(output.String(), "a=d") {
					t.Fatal("overlay did not delete Kitty graphics")
				}
				terminal.Main.Keybindings = false
				terminal.Transcript.Expansion = 0
				collapsed, err := c.renderer.render(t.Context(), &output, now, "test")
				if err != nil {
					t.Fatal(err)
				}
				if len(collapsed.Images.Placements) != 1 {
					t.Fatal("default disclosure must keep generated and hide viewed previews")
				}
				terminal.Transcript.Expansion = 2
				terminal.Width, terminal.Height = 36, 12
				terminal.Transcript.Following, terminal.Transcript.Offset = false, 3
				resized, err := c.renderer.render(t.Context(), &output, now, "test")
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range resized.Images.Placements {
					if p.X < 0 || p.Y < 0 || p.X+p.Columns > 36 || p.Y+p.Rows >= 12 {
						t.Fatalf("resized image crosses viewport: %+v", p)
					}
				}
			} else if len(painted.ImageDemands) != 0 || strings.Contains(output.String(), "\x1b_G") || strings.Contains(output.String(), "\x1bP") {
				t.Fatal("unsupported terminal acquired or emitted images")
			}
			snapshotProtocol := protocol
			if protocol != terminalui.NoImages {
				snapshotProtocol = terminalui.KittyImages
			}
			assertNativeUISnapshot(t, fmt.Sprintf("native-images-%d", snapshotProtocol), painted.Rows)
		})
	}
}

func TestAppServerGeneratedImageRestored(t *testing.T) {
	workspace := t.TempDir()
	u := newAppServerSessionTestUI(t, workspace)
	u.restoreHistory([]binding.HistoryTurn{{ID: "turn", Status: "completed", Items: []binding.ThreadItem{{ID: "generated", Type: "imageGeneration", SavedPath: filepath.Join(workspace, "image.png"), Status: "completed"}}}})
	entries := u.attached.source.Publication(u.attached.presentation, nil).Entries
	if len(entries) != 1 || len(entries[0].Images) != 1 || len(entries[0].Values) != 1 || entries[0].Values[0].Verb != "Generated" {
		t.Fatalf("restored generated image missing: %+v", entries)
	}
}

func previewFixturePNG(t *testing.T) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 160, 80))
	for y := range 80 {
		for x := range 160 {
			shade := color.RGBA{30, 90, 140, 255}
			if x > 15 && x < 145 && y > 12 && y < 68 {
				shade = color.RGBA{180, 220, 235, 255}
			}
			if x > 30 && x < 62 && y > 25 && y < 55 {
				shade = color.RGBA{25, 100, 130, 255}
			}
			if x > 78 && x < 130 && y > 25 && y < 55 {
				shade = color.RGBA{225, 145, 65, 255}
			}
			pixels.Set(x, y, shade)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, pixels); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestNativeImagePreviewScopeAndReplacement(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "image.png")
	if err := os.WriteFile(path, previewFixturePNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	scope := terminalsession.SourceContext{Attachment: 1, Thread: "main", Workspace: workspace}
	previews := imagePreviews{scope: scope, images: make(map[terminalui.ImageSource]*terminalui.ImagePreview)}
	t.Cleanup(previews.close)
	first := terminalui.ImageSource{Path: path, Entry: 1, Revision: 1}
	load := func(source terminalui.ImageSource) {
		t.Helper()
		previews.request(t.Context(), scope, []terminalui.ImageSource{source})
		select {
		case result := <-previews.results:
			previews.finish(result)
		case <-time.After(time.Second):
			t.Fatal("preview load stalled")
		}

	}
	load(first)
	if previews.images[first] == nil {
		t.Fatal("first image missing")
	}
	if err := os.WriteFile(path, []byte("replaced with invalid data"), 0600); err != nil {
		t.Fatal(err)
	}
	second := terminalui.ImageSource{Path: path, Entry: 2, Revision: 1}
	load(second)
	if previews.images[second] != nil {
		t.Fatal("new entry borrowed old cached pixels")
	}
	previews.scope.Attachment++
	previews.finish(imagePreviewResult{scope: scope, source: terminalui.ImageSource{Path: "late"}, preview: previews.images[first]})
	if _, ok := previews.images[terminalui.ImageSource{Path: "late"}]; ok {
		t.Fatal("stale result adopted")
	}
}

func TestReplayImagePreviewsLifecycle(t *testing.T) {
	for _, key := range []string{"TMUX", "TMUX_PANE", "ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_VERSION", "KITTY_WINDOW_ID", "WEZTERM_EXECUTABLE", "WEZTERM_VERSION"} {
		t.Setenv(key, "")
	}
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("TERM_PROGRAM_VERSION", "3.6.1")
	workspace := t.TempDir()
	path := filepath.Join(workspace, "artwork.png")
	if err := os.WriteFile(path, previewFixturePNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	artifact, _ := json.Marshal(path)
	record := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"main","cwd":%q}}
{"timestamp":"2026-10-01T12:00:01Z","type":"event_msg","payload":{"type":"item_completed","thread_id":"main","turn_id":"turn","started_at_ms":1,"completed_at_ms":1001,"item":{"type":"Extension","kind":"image_gen.generation","id":"generated","status":"completed","fileId":"artifact","savedPath":%s}}}
`, workspace, artifact)
	rollpath := filepath.Join(workspace, "rollout.jsonl")
	if err := os.WriteFile(rollpath, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	decoded, err := rollout.Replay(t.Context(), rollpath, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.UnixMilli(1)
	source := &playback.Source{Thread: decoded.Thread, Cwd: workspace, Start: start, End: start.Add(2 * time.Second), Threads: map[string]string{"main": "/root"}}
	for _, event := range decoded.Events {
		source.Events = append(source.Events, playback.Event{Event: event})
	}
	p := newTestUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	t.Cleanup(p.previews.close)
	if err := p.seek(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := p.paint(&output, 80, 24); err != nil {
		t.Fatal(err)
	}
	if p.previews.results != nil || strings.Contains(output.String(), "\x1b_G") {
		t.Fatal("headless replay acquired images")
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	p.imageTerminal = slave
	if err := p.paint(&output, 80, 24); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-p.previews.results:
		p.previews.finish(result)
	case <-time.After(time.Second):
		t.Fatal("replay image did not load")
	}
	output.Reset()
	if err := p.paint(&output, 80, 24); err != nil {
		t.Fatal(err)
	}
	if len(p.graphics.Placements) != 1 {
		t.Fatal("replay did not render generated artifact")
	}
	u := p.ui
	if !u.openMarkdownFile(&u.session, u.presentation, u.presentation.View().SourceWorkspace(), u.activitySource(u.view), u.shell, u.view, path) {
		t.Fatal("replay image file link was not admitted")
	}
	for u.fileReads != nil {
		select {
		case result := <-u.fileReads:
			u.loadedMarkdownFile(&u.session, u.presentation, u.presentation.View().SourceWorkspace(), u.shell, result)
		case <-time.After(time.Second):
			t.Fatal("replay image file admission stalled")
		}
	}
	if err := p.paint(&output, 80, 24); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-p.previews.results:
		p.previews.finish(result)
	case <-time.After(time.Second):
		t.Fatal("replay dialog did not acquire its own preview")
	}
	output.Reset()
	if err := p.paint(&output, 80, 24); err != nil {
		t.Fatal(err)
	}
	if len(p.graphics.Placements) != 1 || p.previews.dialog.Dialog == 0 || u.shell.Dialog.File.ImagePath != path {
		t.Fatal("replay image dialog did not replace transcript graphics")
	}
	_, graphic, _ := strings.Cut(output.String(), "\x1b_G")
	_, payload, _ := strings.Cut(graphic, ";")
	payload, _, _ = strings.Cut(payload, "\x1b\\")
	filename, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(filename)); err != nil {
		t.Fatal("local-file transport missing", err)
	}
	if err := p.seek(0); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := p.paint(&output, 80, 24); err != nil {
		t.Fatal(err)
	}
	if len(p.graphics.Placements) != 0 || !strings.Contains(output.String(), "a=d") {
		t.Fatal("backward seek retained graphics")
	}
	if _, err := os.Stat(string(filename)); !os.IsNotExist(err) {
		t.Fatal("backward seek retained local transport file", err)
	}
}

func TestNativeImagePreviewsLoadAllVisibleDemands(t *testing.T) {
	for _, key := range []string{"TMUX", "TMUX_PANE", "ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_VERSION", "TERM_PROGRAM"} {
		t.Setenv(key, "")
	}
	t.Setenv("TERM", "xterm-kitty")
	workspace := t.TempDir()
	path := filepath.Join(workspace, "image.png")
	if err := os.WriteFile(path, previewFixturePNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	u := newAppServerSessionTestUI(t, workspace)
	for i := range 17 {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": binding.ThreadItem{ID: fmt.Sprint(i), Type: "imageGeneration", SavedPath: path, Status: "completed"}})
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	u.testMainFrame(80, 200, 0)
	frame := sessionapi.NativeFrame{View: u.attached.presentation.View(), Main: u.attached.source.Publication(u.attached.presentation, nil), Agents: u.attached.childSource.Publication(u.attached.presentation, u.attached.source)}
	terminal, _ := u.terminalViews.NewAttachment(diffrender.DarkTheme)
	renderer := nativeClientRenderer{terminal: terminal, views: u.terminalViews}
	renderer.update(frame, conversation.DraftView{}, conversation.PromptDraftView{}, now)
	terminal.Width, terminal.Height = 80, 200
	terminal.Frame.Images = new(terminalui.ImageFrame)
	previews := imagePreviews{}
	defer previews.close()
	source := terminal.FrameInput(now, "test").SourceContext()
	previews.prepare(os.Stdout, terminal, source)
	var output bytes.Buffer
	painted, err := renderer.render(t.Context(), &output, now, "test")
	if err != nil {
		t.Fatal(err)
	}
	for range 17 {
		previews.request(t.Context(), source, painted.ImageDemands)
		select {
		case result := <-previews.results:
			previews.finish(result)
		case <-time.After(time.Second):
			t.Fatal("visible demand starved behind cached images")
		}
		previews.prepare(os.Stdout, terminal, source)
		painted, err = renderer.render(t.Context(), &output, now, "test")
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(painted.Images.Placements) != 17 {
		t.Fatalf("rendered %d of 17 visible images", len(painted.Images.Placements))
	}
}
