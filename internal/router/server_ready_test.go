package router

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/capturer"
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
		done <- RunSession(ctx, []string{"--mode", "passthrough"}, nil, func(session Session) {
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
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(strings.TrimSuffix(baseURL, "/v1") + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", response.StatusCode)
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
	for _, args := range [][]string{{"--mode", "unknown"}, {"--mode", "passthrough", "--main-mentor-handoff=true"}, {"--mode", "passthrough", "--mentor-handoff=true"}, {"--stream-idle-timeout", "0"}, {"--listen", "127.0.0.1:0"}, {"--provider-base-url", "https://example.com"}} {
		if err := RunSession(t.Context(), args, nil, func(Session) { t.Error("ready called despite startup failure") }, nil); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestRunSessionServesMetricsWithoutLogging(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := RunSession(ctx, []string{"--mode", "passthrough", "--mentor-handoff=false"}, NewCriticalErrors(), func(session Session) {
		if session.FrontendDirectory != "" {
			t.Error("passthrough installed frontends")
		}
		response, err := http.Get(strings.TrimSuffix(session.BaseURL, "/v1") + "/api/metrics")
		if err != nil {
			t.Error(err)
		} else {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil || !bytes.Contains(body, []byte(`"schema":"mekugi.capture.metrics.v6"`)) {
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
	t.Setenv(capturer.AXReadOutputEnvironment, capture)
	err := RunSession(t.Context(), []string{"--mode", "passthrough", "--capture-output", capture}, nil, func(Session) { t.Error("aliased exports reached readiness") }, nil)
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
	err := RunSession(t.Context(), []string{"--mode", "mekugi", "--mentor-handoff=false"}, nil, func(Session) { t.Error("unusable replay storage reached readiness") }, nil)
	if err == nil || !strings.Contains(err.Error(), "initialize replay storage") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestRunSessionPassthroughIgnoresInvalidReplayStorage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative-invalid-state")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	notified := false
	err := RunSession(ctx, []string{"--mode", "passthrough"}, nil, func(Session) { notified = true; cancel() }, nil)
	if err != nil || !notified {
		t.Fatalf("passthrough ready=%v error=%v", notified, err)
	}
}
