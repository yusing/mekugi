package router

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/chroma/v2"
)

func TestLiveDiffProducerRetainsSyntaxBoundaries(t *testing.T) {
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
			select {
			case <-sub.previewReady:
			case <-time.After(5 * time.Second):
				t.Fatal("no streamed preview")
			}
			updates := broker.takePreviews(sub)
			var preview liveDiffPreview
			for _, update := range updates {
				if update.Preview != nil {
					preview = *update.Preview
				}
			}
			if preview.ID == "" {
				t.Fatalf("missing preview: %+v", updates)
			}
			if preview.Truncated != tc.clipped || len(mustMarshalJSON(preview)) > 48<<10 {
				t.Fatalf("unexpected clipping: %+v", preview.Syntax)
			}
			if tc.clipped && strings.Contains(preview.Input, "#!python") {
				t.Fatal("fixture did not clip the interpreter selector")
			}
			var pane liveDiffPreviewPane
			pane.update(preview, time.Now())
			lines, err := pane.render(t.Context(), workspace, liveDiffDarkTheme, 100, 20)
			if err != nil || !strings.Contains(strings.Join(lines, "\n"), liveDiffDarkTheme.foreground(tc.kind)+tc.token) {
				t.Fatalf("producer lost syntax for %s: %v %q", tc.token, err, lines)
			}
		})
	}
}
