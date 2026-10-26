package launch

import (
	"bytes"
	"encoding/base64"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func awaitNativeFileDialog(t *testing.T, view *nativeSessionView) {
	t.Helper()
	for view.files.fileReads != nil {
		select {
		case result := <-view.files.fileReads:
			view.files.loadedMarkdownFile(&view.attached.session, view.attached.presentation, view.attached.cwd, view.shell, result)
		case <-time.After(3 * time.Second):
			t.Fatal("file admission stalled")
		}
	}
}

func TestUISnapshotNativeImageFileDialog(t *testing.T) {
	for _, protocol := range []terminalui.ImageProtocol{terminalui.NoImages, terminalui.KittyImages, terminalui.SixelImages, terminalui.KittyFileImages} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			for _, key := range []string{"TMUX", "TMUX_PANE", "ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_VERSION", "KITTY_WINDOW_ID", "WEZTERM_EXECUTABLE", "WEZTERM_VERSION", "WT_SESSION", "TERM_PROGRAM", "TERM_PROGRAM_VERSION"} {
				t.Setenv(key, "")
			}
			t.Setenv("TERM", map[terminalui.ImageProtocol]string{terminalui.NoImages: "xterm", terminalui.KittyImages: "xterm-kitty", terminalui.SixelImages: "foot", terminalui.KittyFileImages: "xterm-256color"}[protocol])
			if protocol == terminalui.KittyFileImages {
				t.Setenv("TERM_PROGRAM", "iTerm.app")
				t.Setenv("TERM_PROGRAM_VERSION", "3.6.1")
			}
			workspace := t.TempDir()
			path := filepath.Join(workspace, "preview.png")
			if err := os.WriteFile(filepath.Join(workspace, "raster"), previewFixturePNG(t), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("raster", path); err != nil {
				t.Fatal(err)
			}
			main := newAppServerSessionTestUI(t, workspace)
			now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
			main.testActivity(main.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "final", Text: "[preview.png](preview.png)", Observed: now}}})
			_, view, _ := nativeDetailTestViews(t, main)
			terminal, _ := main.terminalViews.NewAttachment(diffrender.DarkTheme)
			terminal.Width, terminal.Height = 80, 24
			terminal.Frame.Images = new(terminalui.ImageFrame)
			client := nativeClient{ctx: t.Context(), renderer: nativeClientRenderer{terminal: terminal, views: main.terminalViews}}
			previews := imagePreviews{}
			t.Cleanup(previews.close)
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(master, &pty.Winsize{Cols: 80, Rows: 24, X: 640, Y: 384}); err != nil {
				t.Fatal(err)
			}
			publish := func() {
				t.Helper()
				frame := nativeDetailFrame(view)
				frame.View.Info.Workspace, frame.View.DisplayedWorkspace = "/workspace", "/workspace"
				frame.Main = view.attached.source.Publication(view.attached.presentation, nil)
				frame.FileLinks = map[string]bool{"preview.png": true}
				wire, err := json.Marshal(&frame, jsonv1.FormatDurationAsNano(true))
				if err != nil {
					t.Fatal(err)
				}
				var received sessionapi.NativeFrame
				if err := json.Unmarshal(wire, &received, jsonv1.FormatDurationAsNano(true)); err != nil {
					t.Fatal(err)
				}
				client.event = sessionapi.NativeEvent{Attachment: view.attached.view, Frame: received}
				client.renderer.update(received, conversation.DraftView{}, conversation.PromptDraftView{}, now)
			}
			var output bytes.Buffer
			render := func() terminalsession.RenderedFrame {
				t.Helper()
				previews.prepare(slave, terminal, terminal.FrameInput(now, "test").SourceContext())
				output.Reset()
				frame, err := client.renderer.render(t.Context(), &output, now, "test")
				if err != nil {
					t.Fatal(err)
				}
				return frame
			}
			publish()
			painted := render()
			rows, _ := terminalsession.Screen(80, 24, painted.Rows, -1, -1)
			clicked := false
			for y, row := range rows {
				if x := strings.Index(ansi.Strip(row), "preview.png"); x >= 0 {
					client.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1)})
					client.dispatch(terminalsession.Input{Kind: terminalsession.MouseInput, Text: fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+1)})
					clicked = true
					break
				}
			}
			if !clicked || len(client.calls) != 1 || client.calls[0].operation.Details == nil {
				t.Fatalf("message link did not request file details: clicked=%v calls=%d", clicked, len(client.calls))
			}
			if refusal := view.openDetails(*client.calls[0].operation.Details); refusal != "" {
				t.Fatal(refusal)
			}
			awaitNativeFileDialog(t, view)
			publish()
			if terminal.Dialog.File.ImagePath != path || terminal.Dialog.File.Source != "" {
				t.Fatal("native publication lost the admitted image source")
			}
			painted = render()
			if protocol == terminalui.NoImages {
				if len(painted.ImageDemands) != 0 || !strings.Contains(terminal.Dialog.Document.Text, "terminal graphics are unsupported") {
					t.Fatal("unsupported terminal did not explain the fallback")
				}
			} else {
				if !strings.Contains(terminal.Dialog.Document.Text, "Loading image") || len(painted.ImageDemands) != 1 {
					t.Fatal("native image dialog did not request asynchronous acquisition")
				}
				previews.request(t.Context(), terminal.FrameInput(now, "test").SourceContext(), painted.ImageDemands)
				select {
				case result := <-previews.results:
					previews.finish(result)
				case <-time.After(3 * time.Second):
					t.Fatal("image acquisition stalled")
				}
				painted = render()
				if len(painted.Images.Placements) != 1 || terminal.Dialog.Document.Text != "preview.png" {
					t.Fatal("acquired native image did not reach the dialog")
				}
				placement := painted.Images.Placements[0]
				if got := placement.Image.Pixels.RGBAAt(90, 40); got.R != 225 || got.G != 145 || got.B != 65 {
					t.Fatalf("dialog decoded wrong pixels: %v", got)
				}
				marker := "\x1b_G"
				if protocol == terminalui.SixelImages {
					marker = "\x1bP"
				}
				if !strings.Contains(output.String(), marker) {
					t.Fatal("native frame omitted image graphics")
				}
				render()
				if strings.Contains(output.String(), marker) {
					t.Fatal("unchanged dialog retransmitted graphics")
				}
			}
			if protocol == terminalui.NoImages || protocol == terminalui.KittyImages {
				rows, _ := terminalsession.Screen(80, 24, painted.Rows, -1, -1)
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/native-image-file-%d.txt", protocol), append(rows, "plain after image dialog"), 80)
			}
			if protocol == terminalui.NoImages {
				return
			}
			original := painted.Images.Placements[0]
			var thumbnail string
			if protocol == terminalui.KittyFileImages {
				_, payload, _ := strings.Cut(original.Encode(protocol, 8, 16, 1), ";")
				payload, _, _ = strings.Cut(payload, "\x1b\\")
				decoded, err := base64.StdEncoding.DecodeString(payload)
				if err != nil {
					t.Fatal(err)
				}
				thumbnail = string(decoded)
				if _, err := os.Stat(thumbnail); err != nil {
					t.Fatal(err)
				}
			}
			terminal.Width, terminal.Height = 36, 10
			clipped := render()
			if len(clipped.Images.Placements) != 1 {
				t.Fatal("resize lost the image instead of clipping it")
			}
			for _, p := range clipped.Images.Placements {
				if p.X < 0 || p.Y < 0 || p.X+p.Columns >= 36 || p.Y+p.Rows >= 9 || p.Rows >= original.Rows {
					t.Fatalf("image escaped the resized dialog body: %+v", p)
				}
			}
			view.files.closeOutput(view.shell)
			publish()
			closed := render()
			if len(closed.Images.Placements) != 0 || len(previews.images) != 0 {
				t.Fatal("closed dialog retained pixels")
			}
			if thumbnail != "" {
				if _, err := os.Stat(thumbnail); !os.IsNotExist(err) {
					t.Fatal("closed dialog retained its thumbnail file")
				}
			}
			if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: path}); refusal != "" {
				t.Fatal(refusal)
			}
			awaitNativeFileDialog(t, view)
			publish()
			painted = render()
			if terminal.Dialog.Image != nil || len(painted.ImageDemands) != 1 {
				t.Fatal("reopened dialog reused retired pixels")
			}
			// Replacement after admission is revalidated by the frontend acquirer.
			if err := os.WriteFile(path, []byte("not an image"), 0600); err != nil {
				t.Fatal(err)
			}
			previews.request(t.Context(), terminal.FrameInput(now, "test").SourceContext(), painted.ImageDemands)
			select {
			case result := <-previews.results:
				previews.finish(result)
			case <-time.After(3 * time.Second):
				t.Fatal("replacement image acquisition stalled")
			}
			render()
			if !strings.Contains(terminal.Dialog.Document.Text, "Image preview unavailable") || terminal.Dialog.Image != nil {
				t.Fatal("unreadable replacement did not show a copyable failure")
			}
			if err := os.WriteFile(path, previewFixturePNG(t), 0600); err != nil {
				t.Fatal(err)
			}
			if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: path}); refusal != "" {
				t.Fatal(refusal)
			}
			awaitNativeFileDialog(t, view)
			publish()
			painted = render()
			previews.request(t.Context(), terminal.FrameInput(now, "test").SourceContext(), painted.ImageDemands)
			select {
			case result := <-previews.results:
				if result.preview == nil {
					t.Fatal("pending result has no decoded image")
				}
				view.files.closeOutput(view.shell)
				publish()
				render()
				previews.finish(result)
				if len(previews.images) != 0 || result.preview != nil && protocol == terminalui.KittyFileImages && (terminalui.ImagePlacement{Image: result.preview, Columns: 20, Rows: 5, TotalRows: 5}).Encode(protocol, 8, 16, 1) != "" {
					t.Fatal("late completion retained a dismissed image or transport file")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("pending acquisition did not settle")
			}
		})
	}
}

func TestNativeRemoteHumanImageFileRemainsTextOnly(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "preview.png"), previewFixturePNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	main := newAppServerSessionTestUI(t, workspace)
	_, view, _ := nativeDetailTestViews(t, main)
	view.remoteHuman = true
	if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: "preview.png"}); refusal != "" {
		t.Fatal(refusal)
	}
	awaitNativeFileDialog(t, view)
	details := nativeDetailFrame(view).Details
	if details.File.ImagePath != "" || details.View.Pages[0].Kind != "error" || details.View.Pages[0].Body != "file is not UTF-8 text" {
		t.Fatal("remote-human file links changed their existing binary-text behavior")
	}
}

func TestImageFileAdmissionBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "raster")
	if err := os.WriteFile(path, previewFixturePNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{9 << 20, terminalui.ImageInputLimit + 1} {
		if err := os.Truncate(path, size); err != nil {
			t.Fatal(err)
		}
		_, image, err := readMarkdownFile(t.Context(), path, true)
		if size <= terminalui.ImageInputLimit {
			if err != nil || !image {
				t.Fatalf("image within shared bounds was rejected: %v", err)
			}
		} else if err == nil || image {
			t.Fatal("image exceeded the shared byte limit")
		}
		if _, _, err := readMarkdownFile(t.Context(), path, false); err == nil || !strings.Contains(err.Error(), "dialog limit") {
			t.Fatal("remote-human text limit changed")
		}
	}
}

func TestNativeImageHeaderTextStaysText(t *testing.T) {
	workspace := t.TempDir()
	text := "GIF89a\nThis document explains the GIF image header.\n"
	if err := os.WriteFile(filepath.Join(workspace, "header.txt"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	main := newAppServerSessionTestUI(t, workspace)
	_, view, _ := nativeDetailTestViews(t, main)
	if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeFileDetails, Path: "header.txt"}); refusal != "" {
		t.Fatal(refusal)
	}
	awaitNativeFileDialog(t, view)
	details := nativeDetailFrame(view).Details
	view.shell.Dialog.Layout(76)
	if details.File.ImagePath != "" || details.File.Source != text || view.shell.Dialog.Document.Text != text {
		t.Fatal("image header overrode the text dialog's source or copy content")
	}
}
