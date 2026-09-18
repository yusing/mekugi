package router

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
		handled, status := RunToolPluginWorker(t.Context(), os.Args[0], []string{"bash", "printf pinned"}, os.Stdin, os.Stdout, os.Stderr)
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
		if err := os.WriteFile(replacement, []byte("#!/bin/sh\nprintf 'shell: decode tool worker manifest: json: unknown field replay_directory\\n' >&2\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, location); err != nil {
			t.Fatal(err)
		}
	}
	if stage == "before-registry" {
		replace()
	}
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	registry, err := buildToolRegistry(t.Context(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if stage == "after-registry" {
		replace()
	}
	command := exec.CommandContext(t.Context(), registry.shellRuntime, "-test.run=^TestPinnedToolWorkerProcess$")
	command.Env = append(os.Environ(), "MEKUGI_PIN_WORKER_TEST=worker")
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "pinned" {
		t.Fatalf("pinned worker: %q, %v", output, err)
	}
}
