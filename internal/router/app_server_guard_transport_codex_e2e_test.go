//go:build codex_e2e

package router

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/vcsguard"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// The fake executable records an effect without invoking Git or a remote.
// Faults occur after effective-hook validation, before the host receives exec.
func TestAppServerGuardTransportFailureNativeCodex(t *testing.T) {
	for _, failure := range []string{"disconnected-mcp", "missing-helper", "failed-helper"} {
		t.Run(failure, func(t *testing.T) {
			shell := newVCSGuardShell(t)
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			proxy.execTrack = shell.hub
			proxy.guard = shell.guard
			helper, err := execTrackHelper()
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			marker := filepath.Join(directory, "executed")
			fakeGit := filepath.Join(directory, "git")
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + quoteShellWord(marker) + "\nprintf '%s\\n' FAKE_ABSOLUTE_GIT_EXECUTED\n"
			if err := os.WriteFile(fakeGit, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if failure != "disconnected-mcp" {
				hookHelper := filepath.Join(directory, "hook helper")
				if err := os.Symlink(helper, hookHelper); err != nil {
					t.Fatal(err)
				}
				helper = hookHelper
			}
			guard, _ := vcsguard.Paths(filepath.Join(shell.root, "bin"))
			hook, state, hash, err := vcsguard.HookConfig(helper, guard, "", "")
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"-c", hook, "-c", "hooks.state={" + state + "}", "-c", `sandbox_mode="danger-full-access"`, "-c", `approval_policy="never"`}
			var inject func() error
			if failure == "disconnected-mcp" {
				server := mcp.NewServer(&mcp.Implementation{Name: "reserved", Version: "1"}, nil)
				socket, stop, err := startMCP(t.Context(), server)
				if err != nil {
					t.Fatal(err)
				}
				closeEndpoint := sync.OnceValue(stop)
				t.Cleanup(func() {
					if err := closeEndpoint(); err != nil {
						t.Error(err)
					}
				})
				inject = func() error {
					if err := closeEndpoint(); err != nil {
						return err
					}
					if _, err := os.Stat(socket); !os.IsNotExist(err) {
						return fmt.Errorf("endpoint still available: %v", err)
					}
					return nil
				}
				args = append(args, "-c", `mcp_servers.mekugi={command=`+strconv.Quote(os.Args[0])+`,args=["mcp-bridge",`+strconv.Quote(socket)+`]}`)
			} else {
				inject = func() error {
					if err := os.Remove(helper); err != nil {
						return err
					}
					if failure == "failed-helper" {
						return os.WriteFile(helper, []byte("#!/bin/sh\nexit 7\n"), 0700)
					}
					return nil
				}
			}
			injectOnce := sync.OnceValue(inject)
			provider := &toolFrontendCodexProvider{
				program:        `try { const r = await tools.exec_command({cmd:` + string(responses.MustMarshalJSON(quoteShellWord(fakeGit)+" push origin main")) + `,shell:` + string(responses.MustMarshalJSON(execTrackShellExecutable(t, "bash"))) + `,login:false}); text(r.output); } catch(e) { text(String(e)); } text("GUARD_FAULT_COMPLETE");`,
				expected:       []string{"GUARD_FAULT_COMPLETE"},
				finalText:      "Recovered after a retry.",
				observeRequest: func(_ []byte, _ http.Header) error { return injectOnce() },
			}
			prompted := false
			runAppServerPreviewWith(t, provider, proxy, appServerPreview{
				environment: []string{"PATH=" + execTrackPath(), vcsguard.HookEnvironment + "=" + hash},
				codexArgs:   args,
				duringTurn: func(t *testing.T, outer io.Writer, _ func(string), awaitFrame func(func(string) bool), screen *vt.Emulator) {
					awaitFrame(func(frame string) bool {
						return strings.Contains(frame, "Allow this remote write?") || strings.Contains(frame, "Recovered after a retry.")
					})
					if strings.Contains(screen.String(), "Allow this remote write?") {
						prompted = true
						if _, err := io.WriteString(outer, "3\r"); err != nil {
							t.Fatal(err)
						}
					}
				},
			})
			if data, err := os.ReadFile(marker); err == nil {
				t.Fatalf("guard bypass: fake absolute git executed %q; output: %s", data, provider.output)
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if failure == "disconnected-mcp" && !prompted {
				t.Fatalf("MCP outage prevented ordinary guard approval: %s", provider.output)
			}
			if failure != "disconnected-mcp" && !strings.Contains(provider.output, "approval guard hook failed; command blocked") {
				t.Fatalf("helper failure did not produce native blocking reason: %s", provider.output)
			}
		})
	}
}
