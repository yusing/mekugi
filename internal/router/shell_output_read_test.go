package router

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
	"github.com/yusing/mekugi/internal/router/toolplugin"
)

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
	out, diagnostic := strings.Repeat("out π🙂\n", 60), strings.Repeat("err 引用\n", 40)
	id, err := store.putShellOutput(t.Context(), out, diagnostic, 7)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh store object, cwd, environment and thread need only the visible ID.
	reopened, err := openMekugiReplayStore(manifest.ReplayDirectory)
	if err != nil {
		t.Fatal(err)
	}
	record, err := reopened.readShellOutput(t.Context(), id)
	if err != nil || record.Stdout != out || record.Stderr != diagnostic || record.ExitCode != 7 {
		t.Fatalf("restart record: %#v, %v", record, err)
	}
	if _, direct := registry.directBashExecCommand([]string{"bash", "houtput " + id}); direct {
		t.Fatal("houtput escaped the private runner")
	}
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		t.Fatal(err)
	}
	for _, interpreter := range []string{"bash", "sh"} {
		for _, selection := range []string{"", "--stdout", "--stderr"} {
			t.Run(interpreter+selection, func(t *testing.T) {
				var gotOut, gotErr strings.Builder
				cursor := ""
				invocation := newShellWorkerTestInvocation(t.TempDir(),
					"CODEX_THREAD_ID=resumed-fork", "XDG_STATE_HOME="+t.TempDir())
				for page := range 100 {
					command := "houtput " + id + " " + selection + " --max-tokens 96"
					if cursor != "" {
						command += " --cursor " + cursor
					}
					stdout, stderr, status := runShellWorkerTest(t, registry, interpreter, nil, command, nil, invocation)
					if count, err := codec.Count(stdout); err != nil || count > 96 {
						t.Fatalf("page budget = %d, %v", count, err)
					}
					if page == 1 {
						again, againErr, againStatus := runShellWorkerTest(t, registry, interpreter, nil, command, nil, invocation)
						if again != stdout || againErr != stderr || againStatus != status {
							t.Fatal("cursor read consumed or changed output")
						}
					}
					frame := stdout
					if selection != "--stderr" {
						var ok bool
						frame, ok = strings.CutPrefix(frame, "--- stdout ---\n")
						if !ok {
							t.Fatalf("missing stdout frame: %q", stdout)
						}
						if selection == "--stdout" {
							gotOut.WriteString(strings.TrimSuffix(frame, "\n"))
						} else {
							part, rest, found := strings.Cut(frame, "\n--- stderr ---\n")
							if !found {
								t.Fatalf("missing stderr frame: %q", stdout)
							}
							gotOut.WriteString(part)
							gotErr.WriteString(strings.TrimSuffix(rest, "\n"))
						}
					} else {
						frame, _ = strings.CutPrefix(frame, "--- stderr ---\n")
						gotErr.WriteString(strings.TrimSuffix(frame, "\n"))
					}
					if status == 0 {
						wantOut, wantErr := out, diagnostic
						if selection == "--stdout" {
							wantErr = ""
						} else if selection == "--stderr" {
							wantOut = ""
						}
						if page < 2 || stderr != "" || gotOut.String() != wantOut || gotErr.String() != wantErr {
							t.Fatalf("incomplete or duplicated pages: %d, %q, %q, %q", page, gotOut.String(), gotErr.String(), stderr)
						}
						return
					}
					const notice = "houtput: incomplete; repeat this read with --cursor "
					if !strings.HasPrefix(stderr, notice) {
						t.Fatalf("read failed: %q %q %d", stdout, stderr, status)
					}
					cursor = strings.TrimSpace(strings.TrimPrefix(stderr, notice))
				}
				t.Fatal("pagination did not finish")
			})
		}
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
	if _, err := store.readShellOutput(t.Context(), "ho_"+strings.Repeat("0", 32)); err == nil {
		t.Fatal("missing record accepted")
	}
	name := filepath.Join(store.directory, "output-"+id+".json")
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
	text := "same"
	digest, _, _ := readCursorOffset(text, "", id+":stdout:4:"+text)
	if _, _, err := readCursorOffset(text, digest+":1", id+":stderr:0:"+text); err == nil {
		t.Fatal("cursor crossed stream selection")
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
	info, err := os.Stat(filepath.Join(store.directory, "output-"+id+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record permissions: %v, %v", info, err)
	}
	// A sparse fixture exercises the separate quota without large allocations.
	file, err := os.Create(filepath.Join(store.directory, "output-quota-fixture"))
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
	display := newShellOutputDisplay(t.Context(), toolWorkerManifest{ReplayDirectory: store.directory}, "", 1, nil, nil)
	if _, err := display.streams[0].Write([]byte("omitted")); err != nil {
		t.Fatal(err)
	}
	execution, err := display.finish(toolplugin.ExecutionOutput{ExitCode: 7})
	if err == nil || strings.Contains(execution.Stderr, "houtput") {
		t.Fatalf("quota failure exposed recovery receipt: %#v, %v", execution, err)
	}
	for _, budget := range []int{1, 64, 15500} {
		if _, err := parseOutputRead([]string{id, "--max-tokens", strconv.Itoa(budget)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShellOutputPluginRemainderUsesManagedRecovery(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	directory := t.TempDir()
	source := strings.Repeat("needle row\n", 30)
	name := filepath.Join(directory, "matches")
	if err := os.WriteFile(name, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(directory)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		"hgrep --max-tokens 32 -F needle matches", nil, invocation)
	if status != 1 {
		t.Fatalf("expected reader limit: %q, %q, %d", stdout, stderr, status)
	}
	omitted, omittedErr := retainedShellTestOutput(t, stderr)
	if omitted == "" || omittedErr != "" || strings.Count(stdout+omitted, "needle row") != 30 {
		t.Fatalf("omitted prefix or missing results: %q %q", stdout, omitted)
	}
	// Reading the original result must not require the source or another search.
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(stderr, "houtput ")
	if start < 0 {
		t.Fatalf("missing houtput receipt: %q", stderr)
	}
	command := strings.TrimSpace(stderr[start:]) + " --stdout"
	rest, readErr, readStatus := runShellWorkerTest(t, registry, "sh", nil, command, nil, invocation)
	if readStatus != 0 || readErr != "" || rest != "--- stdout ---\n"+omitted+"\n" {
		t.Fatalf("managed reader failed: %q %q %d", rest, readErr, readStatus)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("output recovery polluted cwd: %v, %v", entries, err)
	}
}

func TestShellOutputReadRejectsMissingAndNullFields(t *testing.T) {
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
		if err := os.WriteFile(filepath.Join(store.directory, "output-"+id+".json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, "houtput "+id, nil)
		if status != 1 || stdout != "" || !strings.Contains(stderr, "missing required fields") {
			t.Fatalf("corrupt evidence accepted: %q, %q %q %d", fields, stdout, stderr, status)
		}
	}
}
