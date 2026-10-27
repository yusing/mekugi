package frontends

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestToolWorkerSurvivesExecutableReplacement(t *testing.T) {
	for _, stage := range []string{"before-registry", "after-registry"} {
		t.Run(stage, func(t *testing.T) {
			directory := t.TempDir()
			installed := filepath.Join(directory, "mekugi")
			source, err := openRunningExecutable()
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			file, err := os.OpenFile(installed, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.Copy(file, source)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatalf("copy: %v, %v", copyErr, closeErr)
			}
			// Finish writable executable setup before parallel subprocess tests
			// can fork and briefly inherit its writer descriptor before exec.
			t.Parallel()
			command := exec.CommandContext(t.Context(), installed, "-test.run=^TestPinnedToolWorkerProcess$")
			command.Env = append(os.Environ(), "MEKUGI_PIN_WORKER_TEST="+stage)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("replacement process: %v\n%s", err, output)
			}
		})
	}
}

func TestPinnedToolWorkerProcess(t *testing.T) {
	stage := os.Getenv("MEKUGI_PIN_WORKER_TEST")
	if stage == "" {
		return
	}
	if stage == "worker" {
		handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), os.Args[0], []string{"--max-tokens", "100", "--", "printf", "pinned"}, os.Stdin, os.Stdout, os.Stderr)
		if !handled {
			os.Exit(99)
		}
		os.Exit(status)
	}
	location, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	replace := func() {
		t.Helper()
		replacement := location + ".replacement"
		if err := os.WriteFile(replacement, []byte("#!/bin/sh\nprintf 'replacement executable ran\\n' >&2\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, location); err != nil {
			t.Fatal(err)
		}
	}
	if stage == "before-registry" {
		replace()
	}
	// The launcher opens replay storage before dispatching its workers.
	replayDirectory := t.TempDir()
	if _, err := testHistory().Open(t.Context(), replayDirectory); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	var image WorkerImage
	defer image.Close()
	config := Config{Commands: testCommands(), Guidance: testGuidance(), ReplayDirectory: replayDirectory, WorkerImage: &image}
	registry, err := New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if stage == "after-registry" {
		replace()
	}
	peer, err := New(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	firstInfo, err := os.Stat(filepath.Join(registry.SnapshotDir, workerExecutableFilename))
	if err != nil {
		t.Fatal(err)
	}
	peerInfo, err := os.Stat(filepath.Join(peer.SnapshotDir, workerExecutableFilename))
	if err != nil || !os.SameFile(firstInfo, peerInfo) {
		t.Fatalf("invocations did not share the pinned image: %v", err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), peer.Frontends["mrun"], "-test.run=^TestPinnedToolWorkerProcess$")
	command.Env = append(os.Environ(), "MEKUGI_PIN_WORKER_TEST=worker")
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "pinned" {
		t.Fatalf("pinned worker: %q, %v", output, err)
	}
}
