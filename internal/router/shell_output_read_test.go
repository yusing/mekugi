package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/tokenizer"
)

func (s *mekugiReplayStore) putShellOutput(ctx context.Context, stdout, stderr string, exitCode int) (string, error) {
	return s.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: stdout, Stderr: stderr}, exitCode)
}

func TestShellOutputReadPagesAndRestart(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	out, diagnostic := strings.Repeat("out π🙂\n", 36), strings.Repeat("err 引用\n", 30)
	id, err := store.putShellOutput(t.Context(), out, diagnostic, 7)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	record, err := reopened.readShellOutput(t.Context(), id)
	if err != nil || record.Stdout != out || record.Stderr != diagnostic || record.ExitCode != 7 {
		t.Fatalf("restart record: %#v, %v", record, err)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, selection := range []string{"", "stdout", "stderr"} {
		t.Run(selection, func(t *testing.T) {
			position := [2]int{}
			var gotOut, gotErr strings.Builder
			for pageIndex := range 100 {
				request := struct {
					Stdout     string `json:"stdout"`
					Stderr     string `json:"stderr"`
					StdoutKind string `json:"stdoutKind"`
					StderrKind string `json:"stderrKind"`
					Position   [2]int `json:"position"`
					Stream     string `json:"stream"`
				}{out, diagnostic, "", "", position, selection}

				data, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				formatted, err := toolplugin.FormatOutput(ctx, manifest.NodeExecutable, filepath.Join(registry.SnapshotDir, manifest.RuntimeRoot),
					[]string{"48", "read", string(data), ""})
				if err != nil || formatted.ExitCode != 0 {
					t.Fatalf("page selection: %#v, %v", formatted, err)
				}
				var page struct {
					Text     string `json:"text"`
					Position [2]int `json:"position"`
					Complete bool   `json:"complete"`
				}
				if err := json.Unmarshal([]byte(formatted.Stdout), &page); err != nil {
					t.Fatal(err)
				}
				if count, err := codec.Count(page.Text); err != nil || count > 48 {
					t.Fatalf("page budget = %d, %v", count, err)
				}

				frame := page.Text
				if after, ok := strings.CutPrefix(frame, "[stdout bytes]\n"); ok {
					part, rest, found := strings.Cut(after, "\n[/stdout]\n")
					if !found {
						t.Fatalf("missing stdout frame: %q", frame)
					}
					gotOut.WriteString(part)
					frame = rest
				}
				if after, ok := strings.CutPrefix(frame, "[stderr bytes]\n"); ok {
					part, found := strings.CutSuffix(after, "\n[/stderr]\n")
					if !found {
						t.Fatalf("missing stderr frame: %q", frame)
					}
					gotErr.WriteString(part)
				} else {
					gotOut.WriteString(frame)
				}

				if page.Complete {
					wantOut, wantErr := out, diagnostic
					if selection == "stdout" {
						wantErr = ""
					} else if selection == "stderr" {
						wantOut = ""
					}
					if pageIndex < 2 || gotOut.String() != wantOut || gotErr.String() != wantErr {
						t.Fatalf("incomplete or duplicated pages: %d, %q, %q", pageIndex, gotOut.String(), gotErr.String())
					}
					return
				}
				ref, err := reopened.putReadCursor(ctx, record, page.Position, selection)
				if err != nil {
					t.Fatal(err)
				}
				cursor, err := reopened.readShellOutput(ctx, ref)
				if err != nil || cursor.Position != page.Position || cursor.Stream != selection {
					t.Fatalf("cursor round trip: %#v, %v", cursor, err)
				}
				position = page.Position
			}
			t.Fatal("pagination did not finish")
		})
	}

	// Keep sh represented and exercise the executable frontend's
	// restart/continuation behavior under a changed cwd, thread ID and state directory.
	stdout, stderr, status := runShellWorkerTest(t, registry, "sh", nil, "mread "+id+" --stdout --max-tokens 48", nil)
	if status != 1 || stdout == "" || !strings.Contains(stderr, "next_call: mread ") {
		t.Fatalf("sh boundary: %d %q %q", status, stdout, stderr)
	}
	invocation := newShellWorkerTestInvocation(t.TempDir(), "XDG_STATE_HOME="+t.TempDir())
	first, firstErr, firstStatus := runShellWorkerTest(t, registry, "bash", nil,
		"mread "+id+" --stderr --max-tokens 48", nil, invocation)
	const notice = "read: incomplete; next_call: mread "
	if firstStatus != 1 || !strings.Contains(first, "err 引用") || strings.Contains(first, "out π") || !strings.HasPrefix(firstErr, notice) {
		t.Fatalf("restart boundary: %d %q %q", firstStatus, first, firstErr)
	}
	cursor := strings.TrimSpace(strings.TrimPrefix(firstErr, notice))
	command := "mread " + cursor + " --max-tokens 48"
	next, nextErr, nextStatus := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
	again, againErr, againStatus := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
	if next != again || nextErr != againErr || nextStatus != againStatus {
		t.Fatalf("cursor read consumed or changed output: first=%q/%q/%d again=%q/%q/%d",
			next, nextErr, nextStatus, again, againErr, againStatus)
	}
	if !strings.Contains(next, "err 引用") || strings.Contains(next, "out π") || nextStatus != 1 || !strings.HasPrefix(nextErr, notice) {
		t.Fatalf("continued stderr binding failed: %d %q %q", nextStatus, next, nextErr)
	}
}

func TestShellOutputReadRejectsInvalidState(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.putShellOutput(t.Context(), "same", "same", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, flags := range []string{
		"", "../outside", id + " " + id, id + " --stdout --stderr",
		id + " --max-tokens 0", id + " --max-tokens 01", id + " --max-tokens 15501",
		id + " --cursor", id + " --tail", id + " --stdout --stdout",
	} {
		if _, err := parseOutputRead(strings.Fields(flags)); err == nil {
			t.Errorf("accepted %q", flags)
		}
	}
	if _, err := store.readShellOutput(t.Context(), "r_"+strings.Repeat("A", 22)); err == nil {
		t.Fatal("missing record accepted")
	}
	name := filepath.Join(store.directory, scopedOutputName("", id))
	if err := os.WriteFile(name, []byte(`{"version":2,"id":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readShellOutput(t.Context(), id); err == nil {
		t.Fatal("corrupt record accepted")
	}
	if _, err := store.putShellOutput(t.Context(), strings.Repeat("x", maxShellOutputBytes+1), "", 0); err == nil {
		t.Fatal("oversize output accepted")
	}
	if _, err := store.putShellOutput(t.Context(), "\xff", "", 0); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := store.putReadCursor(t.Context(), shellOutputRecord{Version: 1, ID: id}, [2]int{-1, 0}, ""); err == nil {
		t.Fatal("negative cursor position accepted")
	}
}

func TestParseOutputReadDefaultBudgetAndOverride(t *testing.T) {
	defaults, err := parseOutputRead([]string{"amber"})
	if err != nil || defaults.maxTokens != 8000 {
		t.Fatalf("default output-read budget: options=%+v err=%v", defaults, err)
	}
	override, err := parseOutputRead([]string{"amber", "--max-tokens", "12000"})
	if err != nil || override.maxTokens != 12000 {
		t.Fatalf("explicit output-read budget: options=%+v err=%v", override, err)
	}
}

func TestShellOutputStoreQuotaAndPermissions(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.putShellOutput(t.Context(), "omitted", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.directory, scopedOutputName("", id)))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record permissions: %v, %v", info, err)
	}
	// A sparse fixture exercises the separate quota without large allocations.
	file, err := os.Create(filepath.Join(store.directory, scopedOutputName("", "maple")))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxShellOutputStoreBytes); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.putShellOutput(t.Context(), "next", "", 0); err == nil {
		t.Fatal("quota overflow accepted")
	}
	if record, err := store.readShellOutput(t.Context(), id); err != nil || record.Stdout != "omitted" {
		t.Fatalf("quota evicted prior record: %#v, %v", record, err)
	}
	for _, budget := range []int{1, 64, 15500} {
		if _, err := parseOutputRead([]string{id, "--max-tokens", strconv.Itoa(budget)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShellOutputReadRejectsMissingAndNullFields(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.putShellOutput(t.Context(), "out", "err", 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range []string{
		"",
		`,"stderr":"","exit_code":0`,
		`,"stdout":"","exit_code":0`,
		`,"stdout":"","stderr":""`,
		`,"stdout":null,"stderr":"","exit_code":0`,
		`,"stdout":"","stderr":null,"exit_code":0`,
		`,"stdout":"","stderr":"","exit_code":null`,
	} {
		data := `{"version":1,"id":"` + id + `"` + fields + `}`
		if err := os.WriteFile(filepath.Join(store.directory, scopedOutputName("", id)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.readShellOutput(t.Context(), id); err == nil || !strings.Contains(err.Error(), "missing required fields") {
			t.Fatalf("corrupt evidence accepted: %q, %v", fields, err)
		}
	}
}

func TestReadCursorRejectsAlteredPosition(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := store.putShellOutput(t.Context(), "first\nsecond\nthird\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.readShellOutput(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.putReadCursor(t.Context(), source, [2]int{6, 0}, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(store.directory, scopedOutputName("", ref))
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range []string{`"position":[0,0]`, `"position":[13,0]`, `"unused_position":[6,0]`} {
		altered := strings.Replace(string(data), `"position":[6,0]`, position, 1)
		if err := os.WriteFile(name, []byte(altered), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.readShellOutput(t.Context(), ref); err == nil {
			t.Fatalf("altered cursor accepted: %s", position)
		}
	}
}

func TestFileAndOutlineReadRecoveryAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, command := range []string{"mcat", "inspect_file"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			source := filepath.Join(directory, "sample.go")
			var content strings.Builder
			content.WriteString("package p\n")
			for i := range 60 {
				fmt.Fprintf(&content, "func Item%d() {}\n", i)
			}
			if err := os.WriteFile(source, []byte(content.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			invocation := newShellWorkerTestInvocation(directory)
			full, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
				command+" "+source+" --max-tokens 15500", nil, invocation)
			if status != 0 || diagnostic != "" {
				t.Fatalf("full read: %d %q", status, diagnostic)
			}
			budget := "256"
			if command == "mcat" {
				budget = "64"
			}
			first, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
				command+" "+source+" --max-tokens "+budget, nil, invocation)
			if status == 0 || first == "" {
				t.Fatalf("expected partial output: %d %q %q", status, first, diagnostic)
			}
			reference := func(diagnostic string) string {
				t.Helper()
				_, ref, found := strings.Cut(diagnostic, "read: incomplete; next_call: mread ")
				if !found {
					t.Fatalf("missing continuation: %q", diagnostic)
				}
				return strings.TrimSpace(ref)
			}
			ref := reference(diagnostic)
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			// A stream-only read must not consume the other stream or reopen the source.
			empty, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
				"mread "+ref+" --stderr", nil, invocation)
			if status != 0 || empty != "" || diagnostic != "" {
				t.Fatalf("stderr selection: %d %q %q", status, empty, diagnostic)
			}
			rows := first
			var entries []json.RawMessage
			if command == "inspect_file" {
				var initial struct {
					Data struct{ Outline []json.RawMessage }
				}
				if err := json.Unmarshal([]byte(first), &initial); err != nil {
					t.Fatal(err)
				}
				entries = initial.Data.Outline
			}
			complete := false
			for pageIndex := range 100 {
				page, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
					"mread "+ref+" --max-tokens "+budget, nil, invocation)
				payload := page
				if command == "mcat" {
					if !strings.HasSuffix(payload, "\n") {
						t.Fatal("partial source row")
					}
					rows += payload
				} else {
					var next []json.RawMessage
					if err := json.Unmarshal([]byte(payload), &next); err != nil {
						t.Fatal(err)
					}
					entries = append(entries, next...)
				}
				if status == 0 {
					if pageIndex < 1 {
						t.Fatal("fixture did not exercise continuation pagination")
					}
					complete = true
					break
				}
				ref = reference(diagnostic)
			}
			if !complete {
				t.Fatal("read did not complete")
			}
			if command == "mcat" {
				if rows != full {
					t.Fatal("recovery lost or duplicated source rows")
				}
			} else {
				var expected struct {
					Data struct{ Outline []json.RawMessage }
				}
				if err := json.Unmarshal([]byte(full), &expected); err != nil {
					t.Fatal(err)
				}
				actualJSON, _ := json.Marshal(entries)
				expectedJSON, _ := json.Marshal(expected.Data.Outline)
				if string(actualJSON) != string(expectedJSON) {
					t.Fatal("recovery lost, changed, or duplicated outline entries")
				}
			}
		})
	}
}

func TestMSymbolFrontendRecoveryAfterSourceRemoval(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "sample.go")
	var content, references strings.Builder
	content.WriteString("package p\nfunc Target() {}\n")
	for index := range 80 {
		fmt.Fprintf(&content, "func Use%d() { Target() }\n", index)
		fmt.Fprintf(&references, "%s:%d:14-20\n", source, index+3)
	}
	if err := os.WriteFile(source, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	resolverDirectory := t.TempDir()
	resolver := "#!/bin/sh\ncat <<'EOF'\n" + references.String() + "EOF\n"
	if err := os.WriteFile(filepath.Join(resolverDirectory, "gopls"), []byte(resolver), 0o700); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory,
		"PATH="+resolverDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	full, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
		"msymbol --max-tokens 15500 refs sample.go 2 Target", nil, invocation)
	if status != 0 || !strings.Contains(diagnostic, "(current snapshot)") ||
		!strings.Contains(full, `"sample.go":3 func Use0() { Target() }`) {
		t.Fatalf("full symbol read: %d %q %q", status, full, diagnostic)
	}
	first, diagnostic, status := runShellWorkerTest(t, registry, "sh", nil,
		"msymbol --max-tokens 16 refs sample.go 2 Target", nil, invocation)
	if status == 0 || !strings.Contains(diagnostic, "msymbol: output incomplete") {
		t.Fatalf("partial symbol read: %d %q %q", status, first, diagnostic)
	}
	_, reference, found := strings.Cut(diagnostic, "read: incomplete; next_call: mread ")
	if !found {
		t.Fatalf("missing symbol continuation: %q", diagnostic)
	}
	reference = strings.TrimSpace(reference)
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	recovered := first
	for range 100 {
		page, next, pageStatus := runShellWorkerTest(t, registry, "sh", nil,
			"mread "+reference+" --max-tokens 256", nil, invocation)
		if page != "" && !strings.HasSuffix(page, "\n") {
			t.Fatal("partial symbol row")
		}
		recovered += page
		if pageStatus == 0 {
			if recovered != full {
				t.Fatal("symbol recovery lost or duplicated rows")
			}
			return
		}
		_, reference, found = strings.Cut(next, "read: incomplete; next_call: mread ")
		if !found {
			t.Fatalf("missing next symbol continuation: %q", next)
		}
		reference = strings.TrimSpace(reference)
	}
	t.Fatal("symbol recovery did not complete")
}

func TestOutlineRecoveryCapacityPreservesCurrentOutput(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	source := filepath.Join(directory, "large.md")
	if err := os.WriteFile(source, []byte(strings.Repeat("# "+strings.Repeat("x", 70_000)+"\n", 250)), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"inspect_file "+source+" --max-tokens 256", nil, newShellWorkerTestInvocation(directory))
	if status != 1 || !json.Valid([]byte(stdout)) || !strings.Contains(stdout, `"truncated":true`) ||
		!strings.Contains(stderr, "recovery unavailable") || strings.Contains(stderr, "next_call") {
		t.Fatalf("capacity discarded valid output or advertised recovery: %d %q %q", status, stdout, stderr)
	}
}
