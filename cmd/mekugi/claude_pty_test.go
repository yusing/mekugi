//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/yusing/mekugi/internal/claude"
)

type claudeLaunchReport struct {
	Config            claude.Config `json:"config"`
	Socket            string        `json:"socket"`
	Plugin            string        `json:"plugin"`
	FrontendDirectory string        `json:"frontendDirectory"`
}

// The test binary supplies both Node's transport and the terminal process. No
// installed Claude runtime, SDK, inference, or package installation is used.
func TestClaudeLaunchPTYProcess(t *testing.T) {
	switch os.Getenv("MEKUGI_TEST_CLAUDE_LAUNCH_PROCESS") {
	case "ui":
		separator := slices.Index(os.Args, "--")
		if separator < 0 {
			t.Fatal("missing subprocess arguments")
		}
		os.Exit(runClaude(t.Context(), os.Args[separator+1:], os.Stdin, os.Stdout, os.Stderr))
	case "bridge":
		var launch struct {
			claude.Config
			CompanionFD int `json:"companionFD"`
		}
		if err := json.Unmarshal([]byte(os.Args[len(os.Args)-1]), &launch); err != nil {
			t.Fatal("invalid launch configuration")
		}
		if launch.Companion != nil || launch.CompanionFD != 3 {
			t.Fatal("default launch did not use the private capability pipe")
		}
		pipe := os.NewFile(uintptr(launch.CompanionFD), "companion")
		var endpoint claude.ObservationEndpoint
		err := json.UnmarshalRead(pipe, &endpoint)
		pipe.Close()
		if err != nil || endpoint.Token == "" || endpoint.Socket == "" {
			t.Fatal("missing private observation capability")
		}
		// Never print these values, including in assertion failures.
		for _, value := range append(slices.Clone(os.Args), os.Environ()...) {
			if strings.Contains(value, endpoint.Token) || strings.Contains(value, endpoint.Socket) {
				t.Fatal("observation capability leaked through argv or environment")
			}
		}
		for _, path := range []string{filepath.Join(endpoint.Plugin, ".claude-plugin", "plugin.json"), filepath.Join(endpoint.Plugin, "skills", "mekugi", "SKILL.md"), filepath.Join(endpoint.Plugin, "skills", "mekugi", "frontends.md")} {
			if endpoint.Plugin == "" {
				t.Fatal("default plugin missing")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("default plugin artifact missing")
			}
		}
		for _, name := range []string{"inspect_file", "mcat", "mchanges", "mread", "mrun", "msymbol"} {
			if endpoint.FrontendDirectory == "" {
				t.Fatal("default frontends missing")
			}
			info, err := os.Stat(filepath.Join(endpoint.FrontendDirectory, name))
			if err != nil || info.Mode().Perm()&0111 == 0 {
				t.Fatalf("default frontend %s unavailable", name)
			}
		}
		var schema struct {
			Type  string `json:"type"`
			Items struct {
				AnyOf []struct {
					Properties struct {
						Op struct {
							Enum []string `json:"enum"`
						} `json:"op"`
					} `json:"properties"`
				} `json:"anyOf"`
			} `json:"items"`
		}
		if err := json.Unmarshal(endpoint.JournalSchema, &schema); err != nil || schema.Type != "array" {
			t.Fatal("default journal schema unavailable")
		}
		var operations []string
		for _, variant := range schema.Items.AnyOf {
			operations = append(operations, variant.Properties.Op.Enum...)
		}
		for _, op := range []string{"plan", "add", "set", "log", "remove"} {
			if !slices.Contains(operations, op) {
				t.Fatalf("journal operation %s missing", op)
			}
		}
		if slices.Contains(operations, "finish") {
			t.Fatal("Codex-only finish exposed to Claude")
		}
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.Socket)
		}}
		defer transport.CloseIdleConnections()
		binding, err := json.Marshal(map[string]any{"operation": "bind", "binding": map[string]string{"runtime": "claude", "session": "offline-launch", "workspace": launch.Cwd}})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://companion/observe", bytes.NewReader(binding))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+endpoint.Token)
		response, err := (&http.Client{Transport: transport, Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			t.Fatal("default observation endpoint unavailable")
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatal("default observation endpoint rejected native binding")
		}
		report, err := json.Marshal(claudeLaunchReport{Config: launch.Config, Socket: endpoint.Socket, Plugin: endpoint.Plugin, FrontendDirectory: endpoint.FrontendDirectory})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("MEKUGI_TEST_CLAUDE_LAUNCH_REPORT"), report, 0600); err != nil {
			t.Fatal(err)
		}
		encoder := jsontext.NewEncoder(os.Stdout)
		for _, frame := range []map[string]string{{"kind": "session", "sessionID": "offline-launch"}, {"kind": "ready"}} {
			if err := json.MarshalEncode(encoder, frame); err != nil {
				t.Fatal(err)
			}
		}
		// Clean native shutdown occurs when the UI closes the input pipe.
		for scanner := bufio.NewScanner(os.Stdin); scanner.Scan(); {
		}
		os.Exit(0)
	default:
		t.Skip("private offline Claude launch subprocess")
	}
}

func TestClaudeDefaultLaunchPTY(t *testing.T) {
	for _, controls := range []bool{false, true} {
		name := "default"
		if controls {
			name = "native-controls"
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			workspace := filepath.Join(directory, "workspace with spaces")
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			node := filepath.Join(directory, "node")
			wrapper := "#!/bin/sh\nMEKUGI_TEST_CLAUDE_LAUNCH_PROCESS=bridge exec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=^TestClaudeLaunchPTYProcess$ -- \"$@\"\n"
			if err := os.WriteFile(node, []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(directory, "claude")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
				t.Fatal(err)
			}
			bridge := filepath.Join(directory, "bridge.js")
			if err := os.WriteFile(bridge, nil, 0600); err != nil {
				t.Fatal(err)
			}
			reportPath := filepath.Join(directory, "launch.json")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestClaudeLaunchPTYProcess$", "--", "--cwd", workspace, "--bridge", bridge}
			if controls {
				args = append(args, "--resume", "native-session", "--fork-session", "--model", "native-model")
			}
			command := exec.CommandContext(ctx, os.Args[0], args...)
			command.Env = append(os.Environ(), "MEKUGI_TEST_CLAUDE_LAUNCH_PROCESS=ui", "MEKUGI_TEST_CLAUDE_LAUNCH_REPORT="+reportPath,
				"PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"), "XDG_STATE_HOME="+filepath.Join(directory, "state"),
				"XDG_CONFIG_HOME="+filepath.Join(directory, "config"), "MEKUGI_RUNTIME_DIR="+filepath.Join(directory, "runtime"), "TERM=xterm-256color")
			master, err := pty.StartWithSize(command, &pty.Winsize{Cols: 120, Rows: 30})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			processDone := make(chan struct{})
			go func() { defer close(processDone); done <- command.Wait() }()
			chunks := make(chan []byte, 64)
			readerDone := make(chan struct{})
			go func() {
				defer close(readerDone)
				buffer := make([]byte, 8192)
				for {
					n, err := master.Read(buffer)
					if n > 0 {
						select {
						case chunks <- bytes.Clone(buffer[:n]):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			t.Cleanup(func() { cancel(); master.Close(); <-readerDone; <-processDone })
			screen := vt.NewEmulator(120, 30)
			defer screen.Close()
			var pending []byte
			ready := false
			for !ready {
				select {
				case chunk := <-chunks:
					pending = append(pending, chunk...)
					for {
						at := bytes.Index(pending, []byte("\x1b[?2026l"))
						if at < 0 {
							break
						}
						at += len("\x1b[?2026l")
						frame := bytes.ReplaceAll(pending[:at], []byte("\x1b]10;?\x1b\\"), nil)
						frame = bytes.ReplaceAll(frame, []byte("\x1b]11;?\x1b\\"), nil)
						if _, err := screen.Write(frame); err != nil {
							t.Fatal(err)
						}
						pending = pending[at:]
						// Readiness is a launch/lifecycle assertion, not a layout
						// golden. Shared renderer snapshots live in internal/router.
						ready = ready || strings.Contains(screen.String(), "Ready")
					}
				case err := <-done:
					t.Fatalf("Claude launch exited before initial render: %v", err)
				case <-ctx.Done():
					t.Fatal("Claude launch did not render readiness")
				}
			}
			if _, err := master.Write([]byte("/quit\r")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Claude launch did not shut down cleanly: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("Claude shutdown timed out")
			}
			data, err := os.ReadFile(reportPath)
			if err != nil {
				t.Fatal("default bridge activation report missing")
			}
			var report claudeLaunchReport
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			want := claude.Config{Cwd: workspace, Executable: executable}
			if controls {
				want.Resume = "native-session"
				want.ForkSession = true
				want.Model = "native-model"
			}
			if report.Config != want {
				t.Fatal("native CLI controls or workspace changed at bridge boundary")
			}
			for _, path := range []string{report.Socket, report.Plugin, filepath.Join(report.FrontendDirectory, "mchanges")} {
				if path == "" {
					t.Fatal("default companion activation was not recorded")
				}
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("invocation-local companion resource survived shutdown")
				}
			}
		})
	}
}

func TestClaudeLaunchHasNoEnableFlags(t *testing.T) {
	for _, name := range []string{"--companion", "--companion-tools", "--companion-journal"} {
		var diagnostic bytes.Buffer
		if code := runClaude(t.Context(), []string{name}, os.Stdin, os.Stdout, &diagnostic); code != 2 {
			t.Fatalf("removed flag %s returned %d, want parse rejection", name, code)
		}
	}
}
