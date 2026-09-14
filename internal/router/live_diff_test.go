package router

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/yusing/mekugi"
)

func TestLiveDiffRedirectedWithoutExternalRenderer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	out, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	status := RunLiveDiff(t.Context(), []string{"--workspace", t.TempDir(), "--replay-dir", t.TempDir(), "--session-file", "connection.json"}, out, out, out)
	data, err := os.ReadFile(out.Name())
	if err != nil || status != 1 || !strings.Contains(string(data), "live view needs a terminal") {
		t.Fatalf("redirected invocation: %d %q %v", status, data, err)
	}
}

func TestLiveDiffSafeText(t *testing.T) {
	got := liveDiffSafe("\x1b]52;c;clipboard\a\x1b[2J\x1b[31mred\x1b[0m\t界\r\n", true)
	if got != "\x1b[31mred\x1b[0m    界\n" {
		t.Fatalf("unsafe rendering: %q", got)
	}
	if ansi.StringWidth(ansi.Truncate(got, 7, "")) > 7 {
		t.Fatal("wide text escaped viewport")
	}
}

func TestLiveDiffRelativeDisplayKeepsSourceAndCapture(t *testing.T) {
	workspace := t.TempDir()
	before := filepath.Join(workspace, "old name.txt")
	after := filepath.Join(workspace, "sub", "new.txt")
	source := "const path = " + strconv.Quote(before)
	diff := "--- " + strconv.Quote(before) + "\n+++ " + strconv.Quote(after) + "\n@@ -1 +1 @@\n-old\n+" + source + "\n"
	chunk := liveDiffChunk{
		review: mekugi.ReviewFile{BeforePath: before, AfterPath: after, Diff: diff},
	}
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{{path: after, chunks: []liveDiffChunk{chunk}}}, workspace, 240, 0, chunk)
	if err != nil {
		t.Fatal(err)
	}
	text := ansi.Strip(strings.Join(render.lines, "\n"))
	if !strings.Contains(text, "Rename: old name.txt → sub/new.txt") || !strings.Contains(text, source) || chunk.review.Diff != diff {
		t.Fatalf("incorrect display or mutated capture: %q", text)
	}
}

func TestLiveDiffDisplayPathWorkspaceBoundary(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	for _, path := range []string{
		filepath.Join(filepath.Dir(workspace), "outside.go"),
		workspace + "-other/file.go",
		filepath.Dir(workspace),
		"/tmp/outside.go",
	} {
		t.Run(path, func(t *testing.T) {
			if got := liveDiffDisplayPath(workspace, path); got != path {
				t.Fatalf("external path = %q, want %q", got, path)
			}
		})
	}
	for _, path := range []string{"", "relative.go"} {
		if got := liveDiffDisplayPath(workspace, path); got != path {
			t.Fatalf("nonabsolute path = %q, want %q", got, path)
		}
	}
}

func TestLiveDiffFollowPauseAndResume(t *testing.T) {
	view := liveDiffView{following: true}
	first := liveDiffFile{path: "a", chunks: []liveDiffChunk{{key: "first"}}}
	second := liveDiffFile{path: "b", chunks: []liveDiffChunk{{key: "second"}}}
	view.merge([]liveDiffFile{first})
	view.merge([]liveDiffFile{first, second})
	if view.files[view.selected].path != "b" {
		t.Fatal("following did not select new edit")
	}
	view.following = false
	view.selected = 0
	third := liveDiffFile{path: "c", chunks: []liveDiffChunk{{key: "third"}}}
	view.merge([]liveDiffFile{first, second, third})
	if view.files[view.selected].path != "a" {
		t.Fatal("paused update changed selection")
	}
	view.followLatest()
	if !view.following || view.files[view.selected].path != "c" {
		t.Fatal("resume did not follow the edit received while paused")
	}
	first.chunks[0].applied = true
	view.merge([]liveDiffFile{first, second, third})
	if view.files[view.selected].path != "c" {
		t.Fatal("old receipt stole follow selection")
	}
}

func TestLiveDiffFollowUsesCaptureOrderNotFileGrouping(t *testing.T) {
	view := liveDiffView{following: true}
	view.merge([]liveDiffFile{
		{path: "a", chunks: []liveDiffChunk{{key: "a1", snapshotOrder: 1}, {key: "a2", snapshotOrder: 3}}},
		{path: "b", chunks: []liveDiffChunk{{key: "b1", snapshotOrder: 2}}},
	})
	if view.latest != "a2" || view.files[view.selected].path != "a" {
		t.Fatal("file grouping overrode latest capture order")
	}
}

func TestLiveDiffMergePreservesFileAndPosition(t *testing.T) {
	v := liveDiffView{scroll: map[string]int{"old": 40}}
	v.merge([]liveDiffFile{
		{path: "b", chunks: []liveDiffChunk{{key: "old", status: "prepared"}}},
		{path: "c"},
	})
	v.merge([]liveDiffFile{
		{path: "c"},
		{path: "a"},
		{path: "b", chunks: []liveDiffChunk{{key: "new"}, {key: "old", status: "applied"}}},
	})
	if v.files[v.selected].path != "b" || v.scroll["old"] != 40 ||
		v.files[0].chunks[0].key != "old" || v.files[0].chunks[0].status != "applied" ||
		v.files[0].chunks[1].key != "new" {
		t.Fatalf("viewport shifted: %#v", v)
	}
	v.selected = 1
	v.merge(append(slices.Clone(v.files), liveDiffFile{path: "d"}))
	if v.files[v.selected].path != "c" || v.scroll["old"] != 40 {
		t.Fatal("other-file update stole navigation")
	}
}

func liveDiffTestChange(t *testing.T, store *mekugiReplayStore, workspace, call, path string, applied bool) {
	t.Helper()
	id, err := store.reserveChange(t.Context(), workspace, "thread", call)
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{
		ChangeID: id, CorrelationID: call, Applied: applied,
		ReviewFiles: []mekugi.ReviewFile{{
			BeforePath: path, AfterPath: path,
			Diff: "update\n--- \"" + path + "\"\n+++ \"" + path + "\"\n@@ -1,80 +1,80 @@\n" + strings.Repeat("-old\n+new\n", 80),
		}},
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: h}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDiffStoreRestartAndMissingEvidence(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffTestChange(t, store, workspace, "one", "x.go", false)
	reopened := &mekugiReplayStore{directory: store.directory}
	files, err := reopened.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil || len(files) != 1 || !strings.Contains(files[0].chunks[0].status, "unconfirmed") {
		t.Fatalf("files: %#v, %v", files, err)
	}
	if err := store.confirmChanges(t.Context(), workspace, map[string]mekugiHistory{
		"one": {ChangeID: "hp_a1", CorrelationID: "one", confirmed: true},
	}); err != nil {
		t.Fatal(err)
	}
	files, err = reopened.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil || files[0].chunks[0].status != "hp_a1 applied" {
		t.Fatalf("receipt: %#v, %v", files, err)
	}
	if err := os.Rename(filepath.Join(store.directory, replayRecordName(workspace, "one", false)), filepath.Join(store.directory, "removed-record")); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}}); err == nil {
		t.Fatal("missing evidence treated as an empty view")
	}
}

func TestLiveDiffNativeRenderer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	workspace := t.TempDir()
	path := filepath.Join(workspace, "x.go")
	file := liveDiffFile{path: path, chunks: []liveDiffChunk{{
		status: "hp_a1 applied",
		review: mekugi.ReviewFile{BeforePath: path, AfterPath: path, Diff: "--- " + strconv.Quote(path) + "\n+++ " + strconv.Quote(path) + "\n@@ -1 +1 @@\n-old\n+new\n"},
	}}}
	render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{file}, workspace, 80, 0, liveDiffChunk{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(render.lines, "\n")
	if strings.Contains(text, workspace) || !strings.Contains(ansi.Strip(text), "x.go") {
		t.Fatalf("native renderer did not render relative paths: %q", text)
	}
	if !strings.Contains(ansi.Strip(text), "new") || !strings.Contains(text, "\x1b[") {
		t.Fatalf("native renderer did not render: %q", text)
	}
}

func TestLiveDiffRenderFollowsLatestHunk(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	header := "--- " + strconv.Quote(path) + "\n+++ " + strconv.Quote(path) + "\n"
	top := header + "@@ -1 +1 @@\n-top\n+TOP\n"
	bottom := header + "@@ -99 +100 @@\n-bottom\n+BOTTOM\n"
	file := liveDiffFile{chunks: []liveDiffChunk{
		{status: "Applied", review: mekugi.ReviewFile{BeforePath: path, AfterPath: path, Diff: top}},
		{status: "Applied", review: mekugi.ReviewFile{BeforePath: path, AfterPath: path, Diff: bottom}},
	}}
	for i, diff := range []string{top, bottom} {
		focus := liveDiffChunk{review: mekugi.ReviewFile{Diff: diff}}
		render, err := renderLiveDiff(t.Context(), liveDiffTerminalTheme, []liveDiffFile{file}, workspace, 80, 0, focus)
		if err != nil {
			t.Fatal(err)
		}
		text := ansi.Strip(strings.Join(render.lines[render.focusOffset:], "\n"))
		if !strings.Contains(text, []string{"TOP", "BOTTOM"}[i]) {
			t.Fatalf("focus missed changed hunk: offset=%d %q", render.focusOffset, text)
		}
		if i == 1 && strings.Contains(text, "TOP") {
			t.Fatalf("bottom hunk focused the top: %q", text)
		}
		if strings.Contains(strings.Join(render.lines, "\n"), "mekugi-live-diff-") {
			t.Fatal("renderer markers leaked into viewport")
		}
	}
}

func assertLiveDiffNoBackground(t *testing.T, text string) {
	t.Helper()
	var state byte
	for len(text) > 0 {
		seq, _, n, next := ansi.DecodeSequence(text, state, nil)
		if n == 0 {
			break
		}
		state, text = next, text[n:]
		if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			code, _, _ := strings.Cut(seq[2:len(seq)-1], ";")
			value, _ := strconv.Atoi(code)
			if value >= 40 && value <= 48 || value >= 100 && value <= 107 {
				t.Fatalf("renderer added a background: %q", seq)
			}
		}
	}
}

func TestLiveDiffTerminalProcess(t *testing.T) {
	if os.Getenv("MEKUGI_LIVE_DIFF_TEST_CHILD") == "1" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		os.Exit(RunLiveDiff(ctx, []string{"--workspace", os.Getenv("MEKUGI_LIVE_DIFF_WORKSPACE"), "--replay-dir", os.Getenv("MEKUGI_LIVE_DIFF_REPLAY"), "--session-file", os.Getenv("MEKUGI_LIVE_DIFF_SESSION")}, os.Stdin, os.Stdout, os.Stderr))
	}
	t.Setenv("COLORFGBG", "")
	t.Setenv("PATH", t.TempDir())
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffTestChange(t, store, workspace, "one", "first.go", true)
	liveDiffTestChange(t, store, workspace, "two", "second.go", true)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	cmd.Env = append(os.Environ(),
		"MEKUGI_LIVE_DIFF_TEST_CHILD=1", "MEKUGI_LIVE_DIFF_WORKSPACE="+workspace,
		"MEKUGI_LIVE_DIFF_REPLAY="+store.directory, "MEKUGI_LIVE_DIFF_SESSION="+liveDiffTestSession(t, store, workspace), "GIT_PAGER=cat")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 20, Cols: 90})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	chunks := make(chan string, 128)
	go func() {
		defer close(chunks)
		buf := make([]byte, 8192)
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				select {
				case chunks <- string(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	waitFor := func(want ...string) string {
		t.Helper()
		var output strings.Builder
		for {
			select {
			case text, open := <-chunks:
				output.WriteString(text)
				if !slices.ContainsFunc(want, func(part string) bool { return !strings.Contains(output.String(), part) }) {
					return output.String()
				}
				if !open {
					t.Fatalf("viewer exited before %q: %q", want, output.String())
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %q: %q", want, output.String())
			}
		}
	}
	initial := waitFor("FOLLOW")
	if !strings.Contains(initial, "\x1b]11;?\x1b\\") {
		t.Fatalf("viewer did not query the terminal background: %q", initial)
	}
	for _, reply := range []struct {
		text  string
		theme liveDiffTheme
	}{
		{"\x1b]11;rgb:ffff/ffff/ffff\x1b\\", liveDiffLightTheme},
		{"\x1b]11;rgb:1111/1111/1111\a", liveDiffDarkTheme},
	} {
		// Exercise a reply fragmented at every byte, including ESC + ST.
		for _, key := range []byte(reply.text) {
			if _, err := terminal.Write([]byte{key}); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(reply.theme.foreground(chroma.GenericInserted), "FOLLOW")
	}
	// Unrelated and malformed OSC payloads must not flush, navigate, or quit,
	// even when they include escape-prefixed command keys or overflow the buffer.
	if _, err := terminal.Write([]byte("\x1b]0;qFnp\a\x1b]11;rgb:ff/ff/\x1bF\a\x1b]11;rgb:ff/ff/\x1bq\a" +
		"\x1b]0;" + strings.Repeat("F", 1024) + "\x1b\\")); err != nil {
		t.Fatal(err)
	}
	if _, err := terminal.Write([]byte("gnkjjj")); err != nil {
		t.Fatal(err)
	}
	// Following centers the added side of this 80-line replacement.
	// File navigation restores that offset rather than resetting to its heading.
	waitFor("second.go  | row 75/")
	liveDiffTestChange(t, store, workspace, "three", "first.go", true)
	liveDiffTestChange(t, store, workspace, "four", "third.go", true)
	waitFor("2/3  second.go  | row 75/")
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 25, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	waitFor("second.go  | row 75/")
	if _, err := terminal.Write([]byte("r")); err != nil {
		t.Fatal(err)
	}
	if output := waitFor("FOLLOW"); !strings.Contains(output, "third.go  | row ") {
		t.Fatalf("resume did not select latest edit: %s", output)
	}
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	select {
	case err := <-exit:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("viewer failed to quit and restore the terminal")
	}
}

func TestLiveDiffOutputBound(t *testing.T) {
	var out liveDiffOutput
	out.Grow(maxChangeReadBytes)
	out.WriteString(strings.Repeat("x", maxChangeReadBytes))
	if _, err := fmt.Fprint(&out, "overflow"); err == nil {
		t.Fatal("unbounded renderer output")
	}
}

func TestLiveDiffExcludesPrivateScripts(t *testing.T) {
	t.Setenv("GIT_PAGER", "cat")
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffTestChange(t, store, workspace, "workspace", "public.go", true)
	id, err := store.reserveChange(t.Context(), workspace, "thread", "private")
	if err != nil {
		t.Fatal(err)
	}
	h := mekugiHistory{ChangeID: id, CorrelationID: "private", Applied: true,
		Script:      "in @shell/private\n",
		ReviewFiles: []mekugi.ReviewFile{{AfterPath: "private-script", Diff: "private-content\n"}},
	}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"private": h}); err != nil {
		t.Fatal(err)
	}
	files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil || len(files) != 1 || files[0].path != filepath.Join(workspace, "public.go") {
		t.Fatalf("private scripts in live view: %#v %v", files, err)
	}
}

func TestLiveDiffHerdrMissingSplitIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("GIT_PAGER", "cat")
	probe := filepath.Join(dir, "herdr")
	script := `#!/bin/sh
case "$2" in
split) printf '%s\n' '{"result":{"pane":{}}}' ;;
run) echo 'must not run in caller' > "$MEKUGI_LIVE_DIFF_RUN_MARKER"; exit 1 ;;
esac
`
	if err := os.WriteFile(probe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "run-marker")
	t.Setenv("MEKUGI_LIVE_DIFF_RUN_MARKER", marker)
	err := splitLiveDiff(t.Context(), dir, dir, &liveDiffPane{sessionFile: filepath.Join(dir, "connection.json")})
	if err == nil || !strings.Contains(err.Error(), "no pane identity") {
		t.Fatalf("missing identity accepted: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("viewer command was sent to caller")
	}
}

func TestLiveDiffTerminalCancel(t *testing.T) {
	testLiveDiffTerminalCancel(t, "")
}

func TestLiveDiffTerminalInterruptDuringOSC(t *testing.T) {
	testLiveDiffTerminalCancel(t, "\x1b]11;rgb:ff/"+strings.Repeat("F", 1024)+"\x03")
}

func testLiveDiffTerminalCancel(t *testing.T, keys string) {
	t.Helper()
	workspace := t.TempDir()
	store := &mekugiReplayStore{directory: t.TempDir()}
	t.Setenv("PATH", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+workspace, "MEKUGI_LIVE_DIFF_REPLAY="+store.directory, "MEKUGI_LIVE_DIFF_SESSION="+liveDiffTestSession(t, store, workspace))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 20, Cols: 90})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	ready := make(chan struct{})
	output := make(chan string, 1)
	go func() {
		var text strings.Builder
		var buf [4096]byte
		notified := false
		for {
			n, err := terminal.Read(buf[:])
			text.Write(buf[:n])
			if !notified && strings.Contains(text.String(), "Waiting for captured") {
				close(ready)
				notified = true
			}
			if err != nil {
				output <- text.String()
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("viewer did not start")
	}
	if keys != "" {
		if _, err := terminal.Write([]byte(keys)); err != nil {
			t.Fatal(err)
		}
	} else if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	exit := make(chan error, 1)
	go func() { exit <- cmd.Wait() }()
	select {
	case err := <-exit:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("context cancellation left terminal reader running")
	}
	select {
	case text := <-output:
		if !strings.Contains(text, "\x1b[?25h\x1b[?1049l") {
			t.Fatalf("terminal was not restored: %q", text)
		}
	case <-ctx.Done():
		t.Fatal("terminal output remained open")
	}
}

func liveDiffFlushCapture(t *testing.T, store *mekugiReplayStore, workspace, call, before, after string, applied bool) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := mekugi.TranslateForHostAt(t.Context(), workspace,
		"in file.txt\ntype "+strconv.Quote(before)+" "+strconv.Quote(after)+"\n", "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.reserveChange(t.Context(), workspace, "flush-thread", call)
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ChangeID: id, CorrelationID: call, Applied: applied, ReviewFiles: result.ReviewFiles}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: history}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDiffFlushComposesAndPreservesSelection(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, "file.txt")
	view := liveDiffView{scroll: map[string]int{workspace + "\x00A/0": 4}}
	refresh := func() string {
		t.Helper()
		files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
		if err != nil {
			t.Fatal(err)
		}
		view.merge(files)
		view.refreshVisible()
		var text strings.Builder
		for _, chunk := range view.visible[workspace+"\x00A/0"].chunks {
			text.WriteString(chunk.review.UnifiedDiff())
		}
		return text.String()
	}
	base, a, b := "first\nold\nlast\n", "first\nA\nlast\n", "first\nB\nlast\n"
	liveDiffFlushCapture(t, store, workspace, "A", base, a, true)
	refresh()
	view.flush(false)
	if len(view.visible[workspace+"\x00A/0"].chunks) != 0 || view.files[view.selected].path != path || view.scroll[workspace+"\x00A/0"] != 4 {
		t.Fatal("flush changed file selection/scroll or left content visible")
	}
	liveDiffFlushCapture(t, store, workspace, "B", a, b, true)
	text := refresh()
	if !strings.Contains(text, "-old\n+B\n") || strings.Contains(text, "-A\n") {
		t.Fatalf("not original-to-latest: %q", text)
	}
	liveDiffFlushCapture(t, store, workspace, "revert", b, base, true)
	if text := refresh(); text != "" {
		t.Fatalf("reverted hunk remains: %q", text)
	}
}

func TestLiveDiffFlushAdjacentEditStaysHidden(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var view liveDiffView
	refresh := func() {
		t.Helper()
		files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
		if err != nil {
			t.Fatal(err)
		}
		view.merge(files)
		view.refreshVisible()
	}
	liveDiffFlushCapture(t, store, workspace, "first", "a\nb\n", "a\nB\n", true)
	refresh()
	view.flush(true)
	liveDiffFlushCapture(t, store, workspace, "adjacent", "a\nB\n", "A\nB\n", true)
	refresh()
	var text strings.Builder
	for _, chunk := range view.visible[view.files[0].key()].chunks {
		text.WriteString(chunk.review.UnifiedDiff())
	}
	if got := text.String(); strings.Contains(got, "+B\n") || !strings.Contains(got, "+A\n") {
		t.Fatalf("reviewed adjacent change revived: %q", got)
	}
}

func TestLiveDiffFlushPendingReceiptAndAllFiles(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view := liveDiffView{scroll: make(map[string]int)}
	refresh := func() {
		t.Helper()
		files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
		if err != nil {
			t.Fatal(err)
		}
		view.merge(files)
		view.refreshVisible()
	}
	liveDiffFlushCapture(t, store, workspace, "A", "old\n", "new\n", false)
	liveDiffTestChange(t, store, workspace, "other", "other.txt", true)
	refresh()
	view.flush(false)
	if len(view.visible[workspace+"\x00A/0"].chunks) != 0 || len(view.visible[workspace+"\x00other/0"].chunks) == 0 {
		t.Fatal("file flush scope is wrong")
	}
	view.flush(true)
	for _, file := range view.visible {
		if len(file.chunks) != 0 {
			t.Fatal("flush all left content")
		}
	}
	if err := store.confirmChanges(t.Context(), workspace, map[string]mekugiHistory{
		"A": {ChangeID: "hp_a1", CorrelationID: "A", confirmed: true},
	}); err != nil {
		t.Fatal(err)
	}
	refresh()
	if len(view.visible[workspace+"\x00A/0"].chunks) != 0 {
		t.Fatal("late receipt revived reviewed changes")
	}
	liveDiffFlushCapture(t, store, workspace, "B", "new\n", "fixed\n", true)
	refresh()
	if len(view.visible[workspace+"\x00A/0"].chunks) == 0 || !strings.Contains(view.visible[workspace+"\x00A/0"].chunks[0].review.Diff, "-old\n+fixed\n") {
		t.Fatalf("fix did not revive flushed pending baseline: %#v", view.visible[workspace+"\x00A/0"])
	}
}

func TestLiveDiffFlushTerminal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	liveDiffFlushCapture(t, store, workspace, "A", "old\n", "first edit\n", true)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLiveDiffTerminalProcess$")
	cmd.Env = append(os.Environ(), "MEKUGI_LIVE_DIFF_TEST_CHILD=1",
		"MEKUGI_LIVE_DIFF_WORKSPACE="+workspace, "MEKUGI_LIVE_DIFF_REPLAY="+store.directory, "MEKUGI_LIVE_DIFF_SESSION="+liveDiffTestSession(t, store, workspace))
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 20, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	chunks := make(chan string, 128)
	go func() {
		defer close(chunks)
		var buf [8192]byte
		for {
			n, err := terminal.Read(buf[:])
			if n > 0 {
				select {
				case chunks <- string(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	waitFor := func(want string) {
		t.Helper()
		var out strings.Builder
		for {
			select {
			case text, ok := <-chunks:
				out.WriteString(text)
				if strings.Contains(ansi.Strip(out.String()), want) {
					return
				}
				if !ok {
					t.Fatalf("viewer exited before %q: %q", want, out.String())
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %q: %q", want, out.String())
			}
		}
	}
	waitFor("+first edit")
	if _, err := terminal.Write([]byte("f")); err != nil {
		t.Fatal(err)
	}
	waitFor("No unreviewed changes")
	liveDiffFlushCapture(t, store, workspace, "B", "first edit\n", "fixed edit\n", true)
	waitFor("+fixed edit")
	liveDiffFlushCapture(t, store, workspace, "revert", "fixed edit\n", "old\n", true)
	waitFor("No unreviewed changes")
	if _, err := terminal.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func liveDiffIdentityCapture(t *testing.T, store *mekugiReplayStore, workspace, call, from, to, before, after string, applied bool) {
	t.Helper()
	id, err := store.reserveChange(t.Context(), workspace, "identity-thread", call)
	if err != nil {
		t.Fatal(err)
	}
	header := func(path string) string {
		if path == "" {
			return "/dev/null"
		}
		return strconv.Quote(path)
	}
	diff := "move " + strconv.Quote(from) + " -> " + strconv.Quote(to) + "\n"
	if before != after {
		oldRange, newRange := "1,1", "1,1"
		if before == "" {
			oldRange = "0,0"
		}
		if after == "" {
			newRange = "0,0"
		}
		diff = "--- " + header(from) + "\n+++ " + header(to) + "\n@@ -" + oldRange + " +" + newRange + " @@\n"
		if before != "" {
			diff += "-" + before
		}
		if after != "" {
			diff += "+" + after
		}
	}
	h := mekugiHistory{ChangeID: id, CorrelationID: call, Applied: applied, ReviewFiles: []mekugi.ReviewFile{{BeforePath: from, AfterPath: to, Diff: diff}}}
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{call: h}); err != nil {
		t.Fatal(err)
	}
}

func liveDiffRefreshTest(t *testing.T, store *mekugiReplayStore, workspace string, view *liveDiffView) {
	t.Helper()
	files, err := store.liveDiffSnapshotFiles(t.Context(), liveDiffScope{Workspaces: map[string]map[string]bool{workspace: nil}})
	if err != nil {
		t.Fatal(err)
	}
	view.merge(files)
	view.refreshVisible()
}

func liveDiffVisibleText(file liveDiffFile) string {
	var text strings.Builder
	for _, chunk := range file.chunks {
		text.WriteString(chunk.status + "\n" + chunk.review.UnifiedDiff())
	}
	return text.String()
}

func TestLiveDiffPreparedMoveKeepsAppliedBaseline(t *testing.T) {
	workspace := t.TempDir()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := liveDiffView{scroll: make(map[string]int)}
	liveDiffIdentityCapture(t, store, workspace, "A", "a.txt", "a.txt", "old\n", "first\n", true)
	liveDiffRefreshTest(t, store, workspace, &v)
	v.flush(true)
	liveDiffIdentityCapture(t, store, workspace, "move", "a.txt", "b.txt", "first\n", "first\n", false)
	liveDiffIdentityCapture(t, store, workspace, "fix", "a.txt", "a.txt", "first\n", "fixed\n", true)
	liveDiffRefreshTest(t, store, workspace, &v)
	text := liveDiffVisibleText(v.visible[workspace+"\x00A/0"])
	if len(v.files) != 1 || v.files[0].path != filepath.Join(workspace, "a.txt") ||
		!strings.Contains(text, "-old\n+fixed\n") || strings.Contains(text, "Uncomposed") {
		t.Fatalf("prepared move lost baseline: %#v %q", v.files, text)
	}
}

func TestLiveDiffMoveIntoDeletedPathKeepsChainsSeparate(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(strconv.FormatBool(reverse), func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			v := liveDiffView{scroll: map[string]int{workspace + "\x00A/0": 9}}
			order := []string{"A", "B"}
			if reverse {
				slices.Reverse(order)
			}
			for _, id := range order {
				path := strings.ToLower(id) + ".txt"
				liveDiffIdentityCapture(t, store, workspace, id, path, path, "old"+id+"\n", id+"\n", true)
			}
			liveDiffRefreshTest(t, store, workspace, &v)
			v.selected = slices.IndexFunc(v.files, func(f liveDiffFile) bool { return f.key() == workspace+"\x00A/0" })
			v.flush(true)
			liveDiffIdentityCapture(t, store, workspace, "deleteB", "b.txt", "", "B\n", "", true)
			liveDiffRefreshTest(t, store, workspace, &v)
			liveDiffIdentityCapture(t, store, workspace, "moveA", "a.txt", "b.txt", "A\n", "A\n", true)
			liveDiffRefreshTest(t, store, workspace, &v)
			liveDiffIdentityCapture(t, store, workspace, "fixA", "b.txt", "b.txt", "A\n", "fixed\n", true)
			liveDiffRefreshTest(t, store, workspace, &v)
			a, b := liveDiffVisibleText(v.visible[workspace+"\x00A/0"]), liveDiffVisibleText(v.visible[workspace+"\x00B/0"])
			if len(v.files) != 2 || v.files[v.selected].key() != workspace+"\x00A/0" || v.scroll[workspace+"\x00A/0"] != 9 ||
				!strings.Contains(a, "-oldA\n+fixed\n") || !strings.Contains(b, "-oldB\n") || strings.Contains(b, "+fixed\n") ||
				strings.Contains(a+b, "Uncomposed") {
				t.Fatalf("path reuse conflated chains: %#v a=%q b=%q", v.files, a, b)
			}
			v.flush(false)
			if len(v.visible[workspace+"\x00A/0"].chunks) != 0 || len(v.visible[workspace+"\x00B/0"].chunks) == 0 {
				t.Fatal("flush crossed file identities")
			}
		})
	}
}
