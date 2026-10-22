package compact

import (
	json "encoding/json/v2"
	"github.com/yusing/mekugi/contracts/command"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReloadSelectionHasNoSourceCountLimit(t *testing.T) {
	sources := make([]Source, 40)
	for i := range sources {
		sources[i] = Source{Path: strings.Repeat("a/", 250) + "guide.md"}
	}
	data, err := json.Marshal(map[string]any{"sources": sources})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := ReadCommands(string(data))
	if err != nil || len(commands) != len(sources) {
		t.Fatalf("selected %d sources: %d commands, %v", len(sources), len(commands), err)
	}
	for _, command := range commands {
		if strings.Contains(command, "--number") || !strings.HasPrefix(command, "'cat'") {
			t.Fatalf("reload must retain raw source rows: %s", command)
		}
	}
}

func TestReloadReadsFullSelectedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "full.txt")
	body := strings.Repeat("full source content beyond the former reader token budget\n", 3000)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	selection, _ := json.Marshal(map[string]any{"sources": []Source{{Path: path}, {Path: path + ".missing"}}})
	output, err := Capture(t.Context(), command.Request{Args: []string{string(selection)}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := output.Delivery.Prepare([]string{"amber"})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Referenced.Stdout != SnapshotPrefix+"amber\n" || strings.Contains(prepared.Referenced.Stdout, body) {
		t.Fatal("source body entered capped tool output")
	}
	var captured Snapshot
	if err := json.Unmarshal([]byte(prepared.Recovery[0].Stdout), &captured); err != nil {
		t.Fatal(err)
	}
	sources := captured.Sources
	if len(sources) != 2 || sources[0].Stdout != body || sources[0].ExitCode != 0 || sources[1].ExitCode == 0 || sources[1].Stderr == "" {
		t.Fatal("capture lost complete source or independent failure")
	}
	if prepared.Unreferenced.ExitCode == 0 || strings.Contains(prepared.Unreferenced.Stdout, SnapshotPrefix) {
		t.Fatal("failed publication exposed a reference")
	}
}

func TestReloadReadsDashFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "-"), []byte("first\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		ranges [][2]int
		want   string
	}{{"whole", nil, "first\nsecond\n"}, {"range", [][2]int{{2, 2}}, "second\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			selection, _ := json.Marshal(map[string]any{"sources": []Source{{Path: "-", Ranges: tc.ranges}}})
			commands, err := ReadCommands(string(selection))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-c", commands[0])
			cmd.Dir = directory
			got, err := cmd.Output()
			if err != nil || string(got) != tc.want {
				t.Fatalf("dash file: got %q, want %q: %v", got, tc.want, err)
			}
		})
	}
}
