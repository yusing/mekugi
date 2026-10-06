package router

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func TestMarkdownFileLinkClickAndDrag(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "src", "source #1?%.go")
	content := strings.Repeat("// previous line\n", 25) + "package target\n\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	fileURL := (&url.URL{Scheme: "file", Path: path}).String()
	for _, target := range []string{"src/source #1?%.go", path + ":26", fileURL + ":26"} {
		t.Run(target, func(t *testing.T) {
			row := (&activityui.Painter{}).Inline("[source](<" + target + ">)")
			u := selectionTestUI(row)
			u.main.session.cwd = workspace
			u.selectionMouse(0, 1, 0, false)
			u.selectionMouse(0, 1, 0, true)
			if u.output == nil || u.clipboard != "" || u.selection != nil {
				t.Fatal("file click did not open a clean dialog")
			}
			u.width, u.height = 80, 18
			frame := drawOutputDialog(u)
			if u.output.filePath != "src/source #1?%.go" || strings.HasSuffix(target, ":26") && (u.output.top == 0 || !strings.Contains(frame, "package target")) {
				t.Fatalf("path or line destination lost: %q", frame)
			}
			if strings.HasSuffix(target, ":26") {
				return // Copy and drag share one path after destination resolution.
			}
			u.outputKey("y")
			if u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
				t.Fatal("whole-file copy changed source or trailing newlines")
			}
			u = selectionTestUI(row)
			u.main.session.cwd = workspace
			selectionTestDrag(t, u, 0, 0, 5, 0)
			if u.output != nil || u.clipboard != "" || u.selection.text() != "source" {
				t.Fatal("drag activated the file link")
			}
		})
	}
}

func TestMarkdownFileDialogSanitizesDisplayKeepsSourceCopy(t *testing.T) {
	for _, size := range []int{0, 256 << 10} {
		path := filepath.Join(t.TempDir(), "data.txt")
		control := "\x1b]52;c;ZXZpbA==\x07"
		content := control + "visible\n" + strings.Repeat("a", size)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		main, _ := newAppServerTestUI()
		u := &terminalUI{main: main, width: 80, height: 18}
		if !u.openMarkdownFile(main.view, path) {
			t.Fatal("existing file did not open")
		}
		rows := make([]string, u.height)
		u.paintOutput(rows, u.width, u.height)
		if strings.Contains(strings.Join(rows, "\n"), control) {
			t.Fatal("file emitted a clipboard control in the rendered frame")
		}
		u.outputKey("y")
		if u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(content))+"\x07" {
			t.Fatal("explicit source copy changed original bytes")
		}
	}
}

func TestMarkdownFileLinkFromDialog(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "sample.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	main, _ := newAppServerTestUI()
	main.session.cwd = workspace
	u := &terminalUI{main: main, width: 80, height: 18}
	u.openBlocks(main.view, []activityui.Block{{Body: "Open [source](sample.go:1) or [remote](https://example.com)."}})
	drawOutputDialog(u)
	before := u.output
	x, y := outputSelectionPoint(t, u, "remote")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	if u.output != before || u.clipboard != "" {
		t.Fatal("non-file dialog link behavior changed")
	}
	x, y = outputSelectionPoint(t, u, "source")
	u.outputMouse(0, x, y, false)
	u.outputMouse(0, x, y, true)
	if u.output == before || u.output.filePath != "sample.go" {
		t.Fatal("dialog link did not open the file")
	}
	drawOutputDialog(u)
	x, y = outputSelectionPoint(t, u, "sample.go")
	outputSelectionDrag(u, x, y, x+8, y)
	if u.selection == nil || u.selection.text() != "sample.go" {
		t.Fatal("displayed path is not selectable")
	}
	for _, target := range []string{"missing.go", workspace, "file://remote" + workspace + "/sample.go", "https://example.com", "#section"} {
		if u.openMarkdownFile(main.view, target) {
			t.Fatalf("opened non-file destination %q", target)
		}
	}
	main.session.cwd = ""
	if u.openMarkdownFile(main.view, "sample.go") {
		t.Fatal("relative file resolved without workspace metadata")
	}
	path := filepath.Join(workspace, "sample.go")
	if !u.openMarkdownFile(main.view, path) || u.output.filePath != path {
		t.Fatal("external absolute path was shortened")
	}
}

func TestUISnapshotMarkdownFileDialog(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "sample.go")
			content := []byte("package sample\n\n// Copy this source.\n")
			name := "native-markdown-file-dialog"
			if failure {
				content, name = []byte{0xff}, "native-markdown-file-error"
			}
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			main, _ := newAppServerTestUI()
			main.session.cwd = workspace
			u := &terminalUI{main: main, width: 80, height: 18}
			if !u.openMarkdownFile(main.view, path) {
				t.Fatal("existing file did not open")
			}
			rows := make([]string, u.height)
			u.paintOutput(rows, u.width, u.height)
			screen := vt.NewEmulator(u.width, u.height)
			defer screen.Close()
			for y, row := range rows {
				fmt.Fprintf(screen, "\x1b[%d;1H%s", y+1, row)
			}
			assertNativeUISnapshot(t, name, strings.Split(screen.String(), "\n"))
			if failure {
				x, y := outputSelectionPoint(t, u, "file is not")
				if screen.CellAt(x, y).Style.Fg != ansi.IndexedColor(203) || ansi.Strip(u.output.laid.Lines[2].Text) != "file is not UTF-8 text" {
					t.Fatalf("read error is not red literal content: color=%v", screen.CellAt(x, y).Style.Fg)
				}
				u.outputKey("y")
				if u.clipboard != "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("file is not UTF-8 text"))+"\x07" {
					t.Fatal("read error is not copyable")
				}
			}
		})
	}
}
