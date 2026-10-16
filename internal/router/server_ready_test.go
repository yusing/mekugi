package router

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	"github.com/yusing/mekugi/modules/agentmetrics/evidence"

	"github.com/yusing/mekugi/modules/usage/accounting"

	"github.com/yusing/mekugi/contracts/command"
	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestRunSessionUsesBoundPortAndClosesListener(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "skills-mgr"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan Session, 1)
	done := make(chan error, 1)
	go func() {
		done <- testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"--mode", "passthrough"}, nil, func(session Session) {
			ready <- session
		}, nil)
	}()
	var session Session
	select {
	case session = <-ready:
	case err := <-done:
		t.Fatalf("router stopped before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("router did not become ready")
	}
	if session.SkillsManagerAvailable {
		t.Fatal("passthrough session enabled skills-mgr integration")
	}
	baseURL := session.BaseURL
	address := strings.TrimSuffix(strings.TrimPrefix(baseURL, "http://"), "/v1")
	if _, port, err := net.SplitHostPort(address); err != nil || port == "0" {
		t.Fatalf("ready URL = %q", baseURL)
	}
	// This fixture owns its requests; do not leave connections in the shared pool.
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Get(strings.TrimSuffix(baseURL, "/v1") + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", response.StatusCode)
	}
	response, err = client.Get(strings.TrimSuffix(baseURL, "/v1") + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("superseded HTML dashboard is still served: %d", response.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("listener survived router exit")
	}
}

func TestRunSessionDoesNotNotifyOnStartupFailure(t *testing.T) {
	for _, args := range [][]string{{"--mode", "unknown"}, {"--stream-idle-timeout", "0"}, {"--listen", "127.0.0.1:0"}, {"--provider-base-url", "https://example.com"}} {
		if err := testRunSession(testHistory(), testRecovery(), t.Context(), testCommands(), args, nil, func(Session) { t.Error("ready called despite startup failure") }, nil); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestRunSessionServesMetricsWithoutLogging(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"--mode", "passthrough"}, NewCriticalErrors(), func(session Session) {
		if session.FrontendDirectory != "" {
			t.Error("passthrough installed frontends")
		}
		response, err := http.Get(strings.TrimSuffix(session.BaseURL, "/v1") + "/api/metrics")
		if err != nil {
			t.Error(err)
		} else {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || !bytes.Contains(body, []byte(`"schema":"mekugi.capture.metrics.v7"`)) {
				t.Errorf("metrics = %s, %v", body, readErr)
			}
		}
		cancel()
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionRejectsAXCaptureAlias(t *testing.T) {
	directory := t.TempDir()
	capture := filepath.Join(directory, "capture.jsonl")
	if err := os.WriteFile(capture, []byte("retained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(evidence.AXReadOutputEnvironment, capture)
	err := testRunSession(testHistory(), testRecovery(), t.Context(), testCommands(), []string{"--mode", "passthrough", "--capture-output", capture}, nil, func(Session) { t.Error("aliased exports reached readiness") }, nil)
	if err == nil {
		t.Fatal("aliased outputs accepted")
	}
	data, err := os.ReadFile(capture)
	if err != nil || string(data) != "retained\n" {
		t.Fatalf("capture truncated: %q %v", data, err)
	}
}

func TestRunSessionRejectsUnusableReplayStorageBeforeReady(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(state, "mekugi"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	err := testRunSession(testHistory(), testRecovery(), t.Context(), testCommands(), []string{"--mode", "mekugi"}, nil, func(Session) { t.Error("unusable replay storage reached readiness") }, nil)
	if err == nil || !strings.Contains(err.Error(), "initialize replay storage") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestRunSessionPassthroughIgnoresInvalidReplayStorage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative-invalid-state")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notified := false
	err := testRunSession(testHistory(), testRecovery(), ctx, testCommands(), []string{"--mode", "passthrough"}, nil, func(Session) { notified = true; cancel() }, nil)
	if err != nil || !notified {
		t.Fatalf("passthrough ready=%v error=%v", notified, err)
	}
}

func TestRunSessionPropagatesShellGuard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  bool
	}{
		{name: "default", want: true},
		{name: "disabled", flags: []string{"--shell-guard=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			notified := false
			err := testRunSession(testHistory(), testRecovery(), ctx, testCommands(), append([]string{"--mode", "passthrough"}, tc.flags...), nil, func(session Session) {
				notified = true
				if (len(session.Guards) != 0) != tc.want {
					t.Errorf("session guard = %t, want %t", len(session.Guards) != 0, tc.want)
				}
				cancel()
			}, nil)
			if err != nil || !notified {
				t.Fatalf("ready=%t error=%v", notified, err)
			}
		})
	}
}

// Existing router fixtures select raw arguments; production passes parsed values.
func testRunSession(service *history.Service, recovered *recovery.Service, ctx context.Context, commands []command.Contribution, args []string, issues *CriticalErrors, ready func(Session), artifacts func([]string)) error {
	options, err := ParseSessionConfig(args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	usage := accounting.New(service)
	if err := usage.Start(ctx); err != nil {
		return err
	}
	defer usage.Close()
	return RunSession(service, recovered, &segmentservice.Service{Reports: testReports()}, testChanges(), usage, new(evidence.Service), new(transportcapture.Service), testInspector(new(evidence.Service), new(transportcapture.Service)), ctx, commands, testInputProjections(), testGuidance(), testNaming(), testGuards(), options, issues, ready, artifacts)
}
