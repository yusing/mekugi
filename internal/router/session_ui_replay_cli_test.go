package router

import (
	"context"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionUIReplayCLI(t *testing.T) {
	dir := t.TempDir()
	path := replayTestWrite(t, dir, "root.jsonl", replayTestMeta("root"),
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+1, replayTestEpoch+2, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "Offline replay."}}}),
		replayTestRecord("event_msg", replayTestEpoch+50, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	output := func(name string) *os.File {
		t.Helper()
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	stdin, stdout, stderr := output("stdin"), output("stdout"), output("stderr")
	cpu, heap := filepath.Join(dir, "cpu.pprof"), filepath.Join(dir, "heap.pprof")
	args := []string{"--session", path, "--headless", "--cpu-profile", cpu, "--heap-profile", heap}
	if code := RunSessionUIReplay(t.Context(), args, stdin, stdout, stderr); code != 0 {
		b, _ := os.ReadFile(stderr.Name())
		t.Fatalf("exit=%d stderr=%s", code, b)
	}
	var summary struct {
		Speed     float64 `json:"speed"`
		Simulated bool    `json:"simulated_streaming"`
		Frames    int     `json:"frames"`
		Events    int     `json:"events_applied"`
		Position  float64 `json:"recorded_seconds"`
	}
	data, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Speed != 1 || !summary.Simulated || summary.Frames == 0 || summary.Events != 5 || summary.Position != .05 {
		t.Fatalf("summary: %s", data)
	}
	if strings.Contains(string(data), "\x1b") {
		t.Fatal("headless stdout contains terminal control sequences")
	}
	for _, profile := range []string{cpu, heap} {
		info, err := os.Stat(profile)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() == 0 || info.Mode().Perm() != 0600 {
			t.Fatalf("profile %s: size=%d mode=%v", profile, info.Size(), info.Mode())
		}
	}
	before, _ := os.ReadFile(cpu)
	if code := RunSessionUIReplay(t.Context(), args, stdin, stdout, stderr); code != 1 {
		t.Fatalf("existing profile accepted: %d", code)
	}
	after, _ := os.ReadFile(cpu)
	if string(before) != string(after) {
		t.Fatal("existing profile overwritten")
	}
	for _, speed := range []string{"0", "-1", "NaN", "Inf", "101"} {
		if code := RunSessionUIReplay(t.Context(), []string{"--session", path, "--headless", "--speed", speed}, stdin, stdout, stderr); code != 2 {
			t.Errorf("invalid speed %s exit=%d", speed, code)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := RunSessionUIReplay(ctx, []string{"--session", path, "--headless"}, stdin, stdout, stderr); code != 1 {
		t.Fatalf("canceled replay exit=%d", code)
	}
}

func TestSessionUIReplayPauseKeepsFrameStable(t *testing.T) {
	p := replayPlaybackTestNew(t)
	if err := p.advance(5500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	p.paused = true
	first := replayPlaybackTestPaint(t, p, 120, 28)
	second := replayPlaybackTestPaint(t, p, 120, 28)
	if first != second {
		t.Fatal("paused repaint advanced output presentation")
	}
}
