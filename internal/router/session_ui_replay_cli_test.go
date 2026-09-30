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
	t.Setenv("CODEX_HOME", dir)
	sessions := filepath.Join(dir, "sessions", "2023", "11", "14")
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	replayTestWrite(t, sessions, "rollout-2023-root.jsonl", replayTestMeta("root"),
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
	args := []string{"--session", "root", "--headless", "--cpu-profile", cpu, "--heap-profile", heap}
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
		if code := RunSessionUIReplay(t.Context(), []string{"--session", "root", "--headless", "--speed", speed}, stdin, stdout, stderr); code != 2 {
			t.Errorf("invalid speed %s exit=%d", speed, code)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := RunSessionUIReplay(ctx, []string{"--session", "root", "--headless"}, stdin, stdout, stderr); code != 1 {
		t.Fatalf("canceled replay exit=%d", code)
	}
}

func TestSessionUIReplayCLISessionLookup(t *testing.T) {
	const sessionID = "01900000-0000-7000-8000-000000000001"
	type rollout struct {
		directory string
		name      string
		metadata  []string
	}
	for _, tc := range []struct {
		name      string
		session   string
		rollouts  []rollout
		code      int
		error     string
		errorPath string
	}{
		{name: "active", session: sessionID, rollouts: []rollout{{"sessions/2023/11/14", sessionID, []string{sessionID}}}},
		{name: "archived", session: sessionID, rollouts: []rollout{{"archived_sessions", sessionID, []string{sessionID}}}},
		{name: "duplicate", session: sessionID, rollouts: []rollout{
			{"sessions/2023/11/14", sessionID, []string{sessionID}},
			{"archived_sessions", sessionID, []string{sessionID}},
		}, code: 1, error: "multiple rollouts"},
		{name: "missing", session: sessionID, code: 1, error: "not found"},
		{name: "metadata mismatch", session: sessionID, rollouts: []rollout{{"sessions", sessionID, []string{"other"}}}, code: 1, error: "not found"},
		{name: "copied ancestor", session: sessionID, rollouts: []rollout{{"sessions", sessionID, []string{"fork", sessionID}}}, code: 1, error: "not found"},
		{name: "suffix is not identity", session: sessionID, rollouts: []rollout{
			{"sessions", "other-" + sessionID, []string{"other-" + sessionID}},
			{"sessions", sessionID, []string{sessionID}},
		}},
		{name: "invalid metadata", session: sessionID, rollouts: []rollout{{"sessions", sessionID, nil}}, code: 1, error: "metadata is unavailable or invalid", errorPath: "sessions/rollout-2023-" + sessionID + ".jsonl"},
		{name: "absolute path", session: "/tmp/rollout.jsonl", code: 2},
		{name: "relative path", session: "sessions/rollout.jsonl", code: 2},
		{name: "glob", session: "*", code: 2},
		{name: "empty", code: 2},
		{name: "whitespace", session: " ", code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CODEX_HOME", dir)
			for _, rollout := range tc.rollouts {
				root := filepath.Join(dir, filepath.FromSlash(rollout.directory))
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
				var records []map[string]any
				for _, id := range rollout.metadata {
					records = append(records, replayTestMeta(id))
				}
				records = append(records,
					replayTestItem(sessionID, "turn", replayTestEpoch, replayTestEpoch+1, map[string]any{"type": "AgentMessage", "id": "answer", "content": []map[string]any{{"text": "Offline replay."}}}))
				replayTestWrite(t, root, "rollout-2023-"+rollout.name+".jsonl", records...)
			}
			files := make([]*os.File, 3)
			for i := range files {
				file, err := os.CreateTemp(dir, "cli-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { file.Close() })
				files[i] = file
			}
			args := []string{"--headless"}
			if tc.session != "" {
				args = append(args, "--session", tc.session)
			}
			code := RunSessionUIReplay(t.Context(), args, files[0], files[1], files[2])
			stderr, err := os.ReadFile(files[2].Name())
			if err != nil {
				t.Fatal(err)
			}
			if code != tc.code || tc.error != "" && !strings.Contains(string(stderr), tc.error) {
				t.Fatalf("exit=%d want=%d stderr=%s", code, tc.code, stderr)
			}
			if tc.errorPath != "" && !strings.Contains(string(stderr), filepath.Join(dir, filepath.FromSlash(tc.errorPath))) {
				t.Fatalf("error lacks source path: %s", stderr)
			}
			if tc.code == 0 {
				data, err := os.ReadFile(files[1].Name())
				if err != nil {
					t.Fatal(err)
				}
				var summary struct {
					Events int `json:"events_applied"`
				}
				if err := json.Unmarshal(data, &summary); err != nil || summary.Events == 0 {
					t.Fatalf("missing playback result: %s (%v)", data, err)
				}
			}
		})
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
