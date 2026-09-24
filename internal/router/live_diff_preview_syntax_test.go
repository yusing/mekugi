package router

import (
	"github.com/yusing/mekugi/internal/livediff"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
)

func TestLiveDiffProducerRetainsSyntaxBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, token string
		kind               chroma.TokenType
		clipped            bool
	}{
		{"clipped_python", "#!python3\n" + strings.Repeat("# context\n", 7000) + "return True\n",
			"return", chroma.Keyword, true},
		{"independent_shell", "printf 'hello'\n",
			"printf", chroma.NameBuiltin, false},
		{"clipped_batch", "echo before\n#!python3\n" + strings.Repeat("# context\n", 7000) + "return True\n",
			"return", chroma.Keyword, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			broker := newLiveDiffBroker(t.Context())
			broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
			sub := broker.subscribe()
			worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread")
			t.Cleanup(worker.stop)
			worker.appendDelta(tc.input)
			// Paced frames reveal the burst progressively; check the frame at its tip.
			var preview liveDiffPreview
			tip := tc.input[strings.LastIndex(strings.TrimSuffix(tc.input, "\n"), "\n")+1:]
			for !strings.HasSuffix(preview.Input, tip) {
				select {
				case <-sub.previewReady:
				case <-time.After(5 * time.Second):
					t.Fatalf("no streamed preview reached the tip: %q", preview.Input)
				}
				for _, update := range broker.takePreviews(sub) {
					if update.Preview != nil {
						preview = *update.Preview
					}
				}
			}
			if preview.Truncated != tc.clipped || len(mustMarshalJSON(preview)) > 48<<10 {
				t.Fatalf("unexpected clipping: %+v", preview.Syntax)
			}
			if tc.clipped && strings.Contains(preview.Input, "#!python") {
				t.Fatal("fixture did not clip the interpreter selector")
			}
			var pane liveDiffPreviewPane
			pane.update(preview)
			lines, err := pane.render(t.Context(), workspace, livediff.DarkTheme, 100, 20)
			if err != nil || !strings.Contains(strings.Join(lines, "\n"), livediff.DarkTheme.Foreground(tc.kind)+tc.token) {
				t.Fatalf("producer lost syntax for %s: %v %q", tc.token, err, lines)
			}
		})
	}
}

func TestLiveDiffBatchHeredocUsesInterpreterSyntax(t *testing.T) {
	input := "nl -ba semantic.ts; rg -n CHECK runner.ts; python3 - <<'PY'\nimport pathlib\nprint(pathlib.Path('semantic.ts'))\nPY\nprintf done\n"
	spans := liveDiffScriptSyntax(input)
	rows := liveDiffSourceRows(input, spans)
	for row, want := range []string{"stream.sh", "stream.py", "stream.py", "stream.sh", "stream.sh"} {
		if rows[row].Path != want {
			t.Fatalf("row %d syntax = %q, want %q; spans=%+v", row, rows[row].Path, want, spans)
		}
	}
	partial := "echo before; python3 - <<'PY'\nimport pathlib\n"
	partialRows := liveDiffSourceRows(partial, liveDiffScriptSyntax(partial))
	if partialRows[1].Path != "stream.py" {
		t.Fatalf("unfinished heredoc did not highlight as Python: %+v", partialRows)
	}
	var pane liveDiffPreviewPane
	pane.update(liveDiffPreview{ID: "batch", Workspace: "/workspace", Thread: "thread", Input: input, Syntax: spans})
	lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 110, 15)
	if err != nil || !strings.Contains(strings.Join(lines, "\n"), livediff.DarkTheme.Foreground(chroma.KeywordNamespace)+"import") {
		t.Fatalf("embedded Python remained plain: %v %q", err, lines)
	}
}

func TestLiveDiffInterpreterHeredocInCompoundAndShebang(t *testing.T) {
	for _, prefix := range []string{"cd /tmp && ", "#!/bin/bash\ncd /tmp && ", "#!/bin/sh\ncd /tmp; "} {
		input := prefix + "python3 - <<'PY'\nimport pathlib\nPY\necho after\n"
		spans := liveDiffScriptSyntax(input)
		rows := liveDiffSourceRows(input, spans)
		index := strings.Count(prefix, "\n") + 1
		if rows[index].Path != "stream.py" || rows[index+1].Path != "stream.sh" {
			t.Fatalf("prefix %q lost Python boundaries: %+v", prefix, rows)
		}
		var pane liveDiffPreviewPane
		pane.update(liveDiffPreview{ID: "compound", Workspace: "/workspace", Thread: "thread", Input: input, Syntax: spans})
		lines, err := pane.render(t.Context(), "/workspace", livediff.DarkTheme, 110, 15)
		if err != nil || !strings.Contains(strings.Join(lines, "\n"), livediff.DarkTheme.Foreground(chroma.KeywordNamespace)+"import") {
			t.Fatalf("compound interpreter remained plain: %v %q", err, lines)
		}
	}
}
