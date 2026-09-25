package router

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestPaneOperationRegressionMCatReadLabels(t *testing.T) {
	cases := []struct {
		name, command, want string
	}{
		{"line limit", "mcat -n 40 a.go", "Read `a.go`"},
		{"token limit", "mcat --max-tokens 400 a.go", "Read `a.go`"},
		{"tail", "mcat --tail --max-tokens 400 a.go", "Read `a.go`"},
		{"multiple files", "mcat --max-tokens 500 a.go 1:3 b.go", "Read `a.go 1:3`\n\nRead `b.go`"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/native", func(t *testing.T) {
			got := toolActivityShell(tc.command)
			if got != tc.want {
				t.Fatalf("display = %q, want %q", got, tc.want)
			}
		})
		t.Run(tc.name+"/code-mode", func(t *testing.T) {
			source := "text(await tools.exec_command({cmd:" + string(mustMarshalJSON(tc.command)) + "}));"
			item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
			if got := subagentToolPreview(item, "functions.exec", nil); got != tc.want {
				t.Fatalf("display = %q, want %q", got, tc.want)
			}
		})
	}

	const batch = `const r=await Promise.allSettled([
	tools.exec_command({cmd:"mcat --max-tokens 500 a.go 1:3 b.go"}),
	]); for(let i=0;i<r.length;i++)text(JSON.stringify({i,...r[i]}));`
	item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(batch)}
	if got, want := subagentToolPreview(item, "functions.exec", nil), "Read `a.go 1:3`\n\nRead `b.go`"; got != want {
		t.Fatalf("Code Mode batch display = %q, want %q", got, want)
	}
}

func TestPaneOperationRegressionMReadIsOmitted(t *testing.T) {
	cases := []struct {
		name, command, want string
	}{
		{"only", "mread amber", ""},
		{"before read", "mread amber\nmcat source.go", "Read `source.go`"},
		{"after read", "mcat source.go\nmread amber", "Read `source.go`"},
		{"between runs", "mread amber\necho first\nmread maple\necho second", "Run `echo first`\n\nRun `echo second`"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/native", func(t *testing.T) {
			got := toolActivityShell(tc.command)
			if got != tc.want {
				t.Fatalf("display = %q, want %q", got, tc.want)
			}
		})
	}

	for _, test := range []struct{ source, want string }{
		{`text(await tools.exec_command({cmd:"mread amber"}));`, ""},
		{`const r=await Promise.allSettled([tools.exec_command({cmd:"mread amber"}),tools.exec_command({cmd:"mcat source.go"})]);for(let i=0;i<r.length;i++)text(JSON.stringify({i,...r[i]}));`, "Read `source.go`"},
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(test.source)}
		originalInput := bytes.Clone(item["input"])
		got := subagentToolPreview(item, "functions.exec", nil)
		if !bytes.Equal(item["input"], originalInput) {
			t.Fatal("mread activity projection changed the original Code Mode input")
		}
		if got != test.want {
			t.Fatalf("Code Mode display = %q, want %q", got, test.want)
		}
	}
}

func TestPaneOperationRegressionDecorativePrintf(t *testing.T) {
	const decorative = `printf '\n--- source context ---\n'`
	for _, source := range []string{
		"mcat first.go 1:4\n" + decorative + "\nmcat second.go",
		decorative + "\nmcat source.go",
		"mcat source.go\n" + decorative,
	} {
		got := toolActivityShell(source)
		if strings.Contains(got, "printf") || strings.Contains(got, "source context") || !strings.Contains(got, "Read") {
			t.Errorf("decorative printf was not omitted beside a read: %q", got)
		}
	}

	for _, source := range []string{
		"mcat source.go\nprintf 'loaded\\n'",
		"mcat source.go\nprintf '\\n--- %s ---\\n' heading",
		"mcat source.go\nprintf '\\n--- source context ---\\n' > /tmp/activity.log",
	} {
		got := toolActivityShell(source)
		if !strings.Contains(got, "printf") || !strings.Contains(got, "Run") {
			t.Errorf("non-decorative printf was omitted: %q", got)
		}
	}
}

func TestPaneOperationRegressionApplyPatchDisplay(t *testing.T) {
	const patch = "*** Begin Patch\n*** Add File: pane.txt\n+first\n*** End Patch\n"
	codeModes := []string{
		"const patch = " + string(mustMarshalJSON(patch)) + "; text(await tools.apply_patch(patch));",
		"const patch = `" + patch + "`; text(await tools.apply_patch(patch));",
		"text(await tools.apply_patch(`" + patch + "`));",
		"const patch = `*** Begin Patch\n*** Add File: pane.txt\n+Use \\`mcat\\` and \\${literal}\n*** End Patch\n`; text(await tools.apply_patch(patch));",
	}
	for _, input := range append([]string{patch}, codeModes...) {
		name := "apply_patch"
		if input != patch {
			name = "exec"
		}
		item := map[string]json.RawMessage{"name": mustMarshalJSON(name), "input": mustMarshalJSON(input)}
		originalInput := bytes.Clone(item["input"])
		if got := subagentToolPreview(item, "functions."+name, nil); got != "" {
			t.Errorf("patch operation display = %q, want no bare label", got)
		}
		if !bytes.Equal(item["input"], originalInput) {
			t.Fatal("patch activity projection changed the original tool input")
		}
	}
	for _, source := range []string{
		"const patch = `*** Begin Patch\n${dynamic}`; text(await tools.apply_patch(patch));",
		"text(await tools.apply_patch(`*** Begin Patch\n${dynamic}`));",
	} {
		item := map[string]json.RawMessage{"name": mustMarshalJSON("exec"), "input": mustMarshalJSON(source)}
		if got := subagentToolPreview(item, "functions.exec", nil); !strings.HasPrefix(got, "Run JavaScript") {
			t.Fatalf("dynamic template was treated as a static patch: %q", got)
		}
		if patches := stockLiteralPatchInputs(source); len(patches) != 0 {
			t.Fatalf("dynamic template entered patch capture: %q", patches)
		}
	}
}

func TestPaneOperationRegressionNativePatchStreamsProvisionalDiff(t *testing.T) {
	t.Parallel()
	proxy := newManagedMekugiProxy(t)
	workspace := t.TempDir()
	transform := prepareNativeStockTransform(t, proxy, workspace, "native-progress")
	broker := newLiveDiffBroker(t.Context())
	scope := liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"stock-thread": true}}}
	broker.setScope(scope)
	proxy.autoLiveDiff = &autoLiveDiff{events: broker, requested: true, scope: scope}
	proxy.autoLiveDiff.enabled.Store(true)
	sub := broker.subscribe()
	<-sub.events
	added := mustTestJSON(t, map[string]any{
		"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "custom_tool_call", "id": "patch-item", "call_id": "patch-call",
			"name": applyPatchToolName, "input": "", "status": "in_progress"},
	})
	if visible, err := transform.TransformSSE(added); err != nil || len(visible) != 1 || !bytes.Equal(visible[0], added) {
		t.Fatalf("stock patch item changed: visible=%q err=%v", visible, err)
	}

	for _, step := range []struct{ fragment, want string }{
		{"*** Begin Patch", ""},
		{"\n*** Add File: new.txt\n+first line", "+first line"},
		{"\n+second line", "+second line"},
	} {
		delta := mustTestJSON(t, map[string]any{
			"type": "response.custom_tool_call_input.delta", "item_id": "patch-item", "delta": step.fragment,
		})
		if visible, err := transform.TransformSSE(delta); err != nil || len(visible) != 1 || !bytes.Equal(visible[0], delta) {
			t.Fatalf("stock patch delta changed: visible=%q err=%v", visible, err)
		}
		preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
			if step.want == "" {
				return preview.Status == liveDiffPreviewEdit && len(preview.Files) == 0
			}
			return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, step.want)
		})
		assertProvisionalPatchPreview(t, preview)
	}
}

func TestPaneOperationRegressionCodeModePatchStreamsThroughPTY(t *testing.T) {
	t.Parallel()
	for _, quote := range []string{"double", "template"} {
		t.Run(quote, func(t *testing.T) {
			workspace := t.TempDir()
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			connection, broker, _ := liveDiffTestBroker(t, store, liveDiffScope{
				Workspaces: map[string]map[string]bool{workspace: {"thread": true}},
			})
			ui := startLiveDiffTerminal(t, workspace, store.directory, connection, 18)
			ui.frame(t, func(frame string) bool { return strings.Contains(frame, "STREAM · v diff") })
			worker := startLiveDiffPreview(t.Context(), broker, workspace, "thread", "exec")
			t.Cleanup(worker.stop)

			var opening, lineBreak string
			if quote == "double" {
				opening, lineBreak = `const patch = "`, `\n`
			} else {
				opening, lineBreak = "const patch = `", "\n"
			}
			worker.appendDelta(opening + "*** Begin Patch")
			assertCodeModePatchFrame(t, ui.frame(t, func(frame string) bool {
				return strings.Contains(ansi.Strip(frame), "· ◐ edit")
			}))

			worker.appendDelta(lineBreak + "*** Add File: new.txt" + lineBreak + "+first line")
			assertCodeModePatchFrame(t, ui.frame(t, func(frame string) bool {
				plain := ansi.Strip(frame)
				return strings.Contains(plain, "new.txt") && strings.Contains(plain, "+first line")
			}), "new.txt", "+first line")

			worker.appendDelta(lineBreak + "+second line")
			assertCodeModePatchFrame(t, ui.frame(t, func(frame string) bool {
				return strings.Contains(ansi.Strip(frame), "+second line")
			}), "new.txt", "+first line", "+second line")
			if quote == "template" {
				worker.appendDelta("\n+Use \\`mcat\\` and \\${literal}\n+after escapes")
				assertCodeModePatchFrame(t, ui.frame(t, func(frame string) bool {
					return strings.Contains(ansi.Strip(frame), "+after escapes")
				}), "+Use `mcat` and ${literal}", "+after escapes")
			}
			ui.quit(t)
		})
	}
}

func assertProvisionalPatchPreview(t *testing.T, preview liveDiffPreview) {
	t.Helper()
	if preview.Status != liveDiffPreviewEdit || preview.Complete || len(preview.Files) == 0 && !preview.DiffText {
		t.Fatalf("patch fragment was not displayed as a provisional diff: %+v", preview)
	}
}

func assertCodeModePatchFrame(t *testing.T, frame string, ordered ...string) {
	t.Helper()
	plain := ansi.Strip(frame)
	if !strings.Contains(plain, "· ◐ ") {
		t.Fatalf("progressive patch frame is not a provisional diff preview: %q", plain)
	}
	last := -1
	for _, want := range ordered {
		position := strings.Index(plain, want)
		if position <= last {
			t.Fatalf("progressive patch frame %q does not contain ordered %q after byte %d", plain, want, last)
		}
		last = position
	}
	for _, wrapper := range []string{"const patch", "tools.apply_patch", "*** Begin Patch", "*** Add File:"} {
		if strings.Contains(plain, wrapper) {
			t.Fatalf("terminal leaked Code Mode wrapper %q: %q", wrapper, plain)
		}
	}
}
