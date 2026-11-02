//go:build codex_e2e

package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
)

func TestCaptureDynamicPatchNativeCodex(t *testing.T) {
	for _, mode := range []string{"patch-only", "with-shell", "sequential-python", "shell-before-patch", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv(routerTestWorkerEnvironment, "1")
			codex, err := exec.LookPath("codex")
			if err != nil {
				t.Fatal(err)
			}
			registry, err := buildToolRegistryForTest(t, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registry.Close(); err != nil {
					t.Error(err)
				}
			})
			store, err := openMekugiReplayStore(registry.ReplayDirectory)
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			writeTestFile(t, filepath.Join(workspace, ".gitignore"), "*.txt\n")
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			setCodexFrontendLoginEnvironment(t, registry.FrontendDirectory)
			patch := "*** Begin Patch\n*** Add File: variable.txt\n+actual variable\n*** End Patch\n"
			program := "const patch = [" + string(responses.MustMarshalJSON(patch)) + "].join(''); await tools.apply_patch(patch);"
			if mode == "with-shell" {
				program += "text((await tools.exec_command({cmd:'printf shell > shell.txt', workdir:" + strconv.Quote(workspace) + "})).output);"
			} else if mode == "sequential-python" {
				command := `python3 -c 'from pathlib import Path; Path("sequential.go").write_text("package fixture\nconst Sequential=1\n")'; gofmt -w sequential.go`
				program += "await tools.exec_command({cmd:" + string(responses.MustMarshalJSON(command)) + "});"
			} else if mode == "shell-before-patch" {
				writeTestFile(t, filepath.Join(workspace, "variable.txt"), "initial\n")
				patch = "*** Begin Patch\n*** Update File: variable.txt\n@@\n-middle\n+actual variable\n*** End Patch\n"
				program = "await tools.exec_command({cmd:" + string(responses.MustMarshalJSON("printf 'middle\\n' > variable.txt")) + "}); const patch=[" + string(responses.MustMarshalJSON(patch)) + "].join(''); await tools.apply_patch(patch);"
			}
			provider := &mchangesNestedCodexProvider{program: program, callID: "capture-probe", finalMessage: "probe complete"}
			if mode == "unavailable" {
				provider.codexArgs = []string{"-c", "hooks.PreToolUse=[]"}
			}
			thread := runMChangesNestedCodexCell(t, codex, registry, store, workspace, provider)
			want := 1
			if mode == "with-shell" || mode == "sequential-python" {
				want = 2
			}
			patchIndex := 0
			if mode == "shell-before-patch" {
				patchIndex = 1
			}
			if len(provider.observedCalls) != want+patchIndex || provider.observedCalls[patchIndex].Input != patch {
				t.Fatalf("native operation count/input changed: %+v", provider.observedCalls)
			}
			if ids := mchangesThreadIDs(t, store, workspace, thread); len(ids) != want {
				t.Fatalf("independent edit receipts=%v want %d", ids, want)
			}
			list := runMChangesNestedShell(t, registry, workspace, thread, "mchanges --list")
			if list.status != 0 || list.stdout == "" {
				t.Fatalf("missing captures: %+v", list)
			}
			result := runMChangesNestedShell(t, registry, workspace, thread, "mchanges")
			if mode == "unavailable" {
				if result.status == 0 || !strings.Contains(result.stderr, "native patch baseline unavailable") {
					t.Fatalf("missing boundary was not partial: %+v", result)
				}
				return
			}
			if result.status != 0 || !strings.Contains(result.stdout, "+actual variable") || mode == "with-shell" && !strings.Contains(result.stdout, "+shell") {
				t.Fatalf("dynamic patch evidence: %+v", result)
			}
			if mode == "sequential-python" && !strings.Contains(result.stdout, "+const Sequential = 1") {
				t.Fatalf("sequential writer lost formatted effect: %+v", result)
			}
			if mode == "shell-before-patch" && !strings.Contains(result.stdout, "-initial") {
				t.Fatalf("earliest shell baseline lost: %+v", result)
			}
		})
	}
}

func TestCaptureTerminalWriterBoundaryNativeCodex(t *testing.T) {
	for _, mode := range []string{"no-op", "restore", "patch", "dynamic-patch", "shell-then-dynamic-patch", "read-only-then-dynamic-patch"} {
		t.Run(mode, func(t *testing.T) {
			changesBytes := mode != "no-op"
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv(routerTestWorkerEnvironment, "1")
			codex, err := exec.LookPath("codex")
			if err != nil {
				t.Fatal(err)
			}
			registry, err := buildToolRegistryForTest(t, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := registry.Close(); err != nil {
					t.Error(err)
				}
			})
			store, err := openMekugiReplayStore(registry.ReplayDirectory)
			if err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			setCodexFrontendLoginEnvironment(t, registry.FrontendDirectory)
			writeTestFile(t, filepath.Join(workspace, ".gitignore"), "value.go\n")
			writeTestFile(t, filepath.Join(workspace, "value.go"), "package fixture\n\nconst Value = 1\n")
			command := "gofmt -w value.go; while ! grep -q 'Value = 2' value.go; do sleep .05; done; sleep 2"
			if changesBytes {
				command += "; printf 'package fixture\\nconst Value=2\\n' > value.go"
			}
			program := "text(await tools.exec_command({cmd:" + string(responses.MustMarshalJSON(command)) + ",yield_time_ms:1000}));"
			patch := "*** Begin Patch\n*** Update File: value.go\n@@\n-const Value = 1\n+const Value = 2\n*** End Patch\n"
			later := "// @exec: {\"yield_time_ms\":4000}\nconst ended = await tools.write_stdin({session_id:SESSION,chars:'',yield_time_ms:10000}); if(ended.exit_code!==0) throw new Error('first writer failed'); const next = await tools.exec_command({cmd:'gofmt -w value.go; sleep 3',yield_time_ms:1000}); text(await tools.write_stdin({session_id:next.session_id,chars:'',yield_time_ms:10000}));"
			if mode == "restore" {
				subdir := filepath.Join(workspace, "nested")
				if err := os.Mkdir(subdir, 0700); err != nil {
					t.Fatal(err)
				}
				later = strings.Replace(later, "cmd:'gofmt -w value.go; sleep 3'", "cmd:'gofmt -w ../value.go; sleep 3',workdir:"+strconv.Quote(subdir), 1)
			} else if mode == "patch" || strings.HasSuffix(mode, "dynamic-patch") {
				restore := "*** Begin Patch\n*** Update File: value.go\n@@\n package fixture\n-const Value=2\n+\n+const Value = 2\n*** End Patch\n"
				later = "await tools.write_stdin({session_id:SESSION,chars:'',yield_time_ms:10000}); await tools.apply_patch(" + string(responses.MustMarshalJSON(restore)) + ");"
				if mode == "dynamic-patch" || mode == "read-only-then-dynamic-patch" {
					later = "await tools.write_stdin({session_id:SESSION,chars:'',yield_time_ms:10000}); const patch=[" + string(responses.MustMarshalJSON(restore)) + "].join(''); await tools.apply_patch(patch);"
					if mode == "read-only-then-dynamic-patch" {
						later = strings.Replace(later, "const patch=", "await tools.exec_command({cmd:'true'}); const patch=", 1)
					}
				} else if mode == "shell-then-dynamic-patch" {
					restore = "*** Begin Patch\n*** Update File: value.go\n@@\n-const Value = 2\n+const Value = 3\n*** End Patch\n"
					later = "await tools.write_stdin({session_id:SESSION,chars:'',yield_time_ms:10000}); await tools.exec_command({cmd:'gofmt -w value.go'}); const patch=[" + string(responses.MustMarshalJSON(restore)) + "].join(''); await tools.apply_patch(patch);"
				}
			}
			provider := &mchangesNestedCodexProvider{program: program, callID: "terminal-writer", finalMessage: "terminal boundary complete", expectProcess: true, store: store, workspace: workspace,
				overlapProgram: "await tools.apply_patch(" + string(responses.MustMarshalJSON(patch)) + ");", laterProgram: later}
			if mode == "dynamic-patch" {
				provider.overlapProgram = "const patch=[" + string(responses.MustMarshalJSON(patch)) + "].join(''); await tools.apply_patch(patch);"
			}
			thread := runMChangesNestedCodexCell(t, codex, registry, store, workspace, provider)
			ctx, release, err := store.beginSession(t.Context(), thread, "boundary-read")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			for _, cell := range []string{provider.callID, provider.callID + "-later"} {
				id := legacycall.ExecCallID(cell, true)
				patchWriter := (mode == "patch" || strings.HasSuffix(mode, "dynamic-patch")) && cell != provider.callID
				if patchWriter {
					id = legacycall.PatchCallID(cell, 0)
				}
				writer, found, err := store.scoped(ctx).lookup(ctx, workspace, id)
				if err != nil || !found || !patchWriter && (writer.ExecOutcome == nil || writer.ExecOutcome.Coverage != changecapture.CoverageExact) {
					t.Fatalf("writer %s: found=%t err=%v history=%+v", cell, found, err, writer)
				}
				if changesBytes {
					if writer.ChangeID == "" {
						t.Fatalf("writer %s lost intermediate effect: %+v", cell, writer)
					}
					review := runMChangesNestedShell(t, registry, workspace, thread, "mchanges "+writer.ChangeID)
					formatted := "const Value = 2"
					if mode == "shell-then-dynamic-patch" && patchWriter {
						formatted = "const Value = 3"
					}
					if review.status != 0 || !strings.Contains(review.stdout, "const Value=2") || !strings.Contains(review.stdout, formatted) {
						t.Fatalf("writer %s attribution: %+v", cell, review)
					}
				} else if writer.ChangeID != "" || len(writer.ReviewFiles) != 0 {
					t.Fatalf("no-op writer %s retained changes: %+v", cell, writer)
				}
			}
			edited, found, err := store.scoped(ctx).lookup(ctx, workspace, legacycall.PatchCallID(provider.callID+"-overlap", 0))
			if err != nil || !found || edited.ChangeID == "" {
				t.Fatalf("patch missing: found=%t err=%v history=%+v", found, err, edited)
			}
			review := runMChangesNestedShell(t, registry, workspace, thread, "mchanges "+edited.ChangeID)
			if review.status != 0 || !strings.Contains(review.stdout, "-const Value = 1\n+const Value = 2") {
				t.Fatalf("patch lost evidence: %+v", review)
			}
		})
	}
}
