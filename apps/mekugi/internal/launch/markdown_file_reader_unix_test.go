//go:build unix

package launch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadMarkdownFileRejectsReplacedDevice(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selected, ok := markdownFileTarget(workspace, "source.txt")
	if !ok {
		t.Fatal("regular source was not admitted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(os.DevNull, path); err != nil {
		t.Fatal(err)
	}
	data, err := readMarkdownFile(selected)
	if err == nil || !strings.Contains(err.Error(), "regular file") || len(data) != 0 {
		t.Fatalf("replacement device was not rejected: data=%q err=%v", data, err)
	}
}

func TestReadMarkdownFileRejectsFIFOWithoutWriter(t *testing.T) {
	if path := os.Getenv("MEKUGI_TEST_MARKDOWN_FIFO"); path != "" {
		data, err := readMarkdownFile(path)
		if err == nil || !strings.Contains(err.Error(), "regular file") || len(data) != 0 {
			t.Fatalf("FIFO was not rejected: data=%q err=%v", data, err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestReadMarkdownFileRejectsFIFOWithoutWriter$")
	cmd.Env = append(os.Environ(), "MEKUGI_TEST_MARKDOWN_FIFO="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO admission blocked or failed: %v: %s", err, output)
	}
}
