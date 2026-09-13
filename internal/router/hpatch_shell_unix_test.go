//go:build unix

package router

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestHpatchTranslationRawStdin(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A large line and control/Unicode bytes exercise PTY line limits, echo,
	// byte counts, and input transformation, not merely a pipe substitute.
	transform, _ := mixedTestTransform(t)
	state, err := transform.retainMixedScript("", "", "shell true", nil)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", 600000) + "世界\t\x03\n"
	source := "new raw.txt\r\ntype <<PATCH\r\n" + content + "PATCH\n"
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestHpatchMixedProcess$", "--")
	command.Env = append(os.Environ(), "MEKUGI_HPATCH_WORKER_TEST=1",
		"MEKUGI_RUNTIME_DIR="+filepath.Dir(transform.shellDirectory),
		"CODEX_THREAD_ID="+strings.TrimPrefix(filepath.Base(transform.shellDirectory), "mekugi-scripts-"))
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer command.Wait()
	reader := bufio.NewReader(terminal)
	ready, err := reader.ReadString('\n')
	if err != nil || ready != hpatchTranslationReady {
		t.Fatalf("ready = %q, %v", ready, err)
	}
	encoder := json.NewEncoder(terminal)
	decoder := json.NewDecoder(reader)
	if err := encoder.Encode(hpatchControlRequest{Operation: "open", Handle: state.Handle}); err != nil {
		t.Fatal(err)
	}
	var opened struct {
		Opened bool `json:"opened"`
	}
	readHpatchControlReply(t, encoder, decoder, &opened)
	if !opened.Opened {
		t.Fatal("channel did not bind")
	}
	if err := encoder.Encode(hpatchControlRequest{Operation: "translate", Source: source}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Patch      string `json:"patch"`
		Diagnostic string `json:"diagnostic"`
	}
	readHpatchControlReply(t, encoder, decoder, &result)
	if err := encoder.Encode(hpatchControlRequest{Operation: "close"}); err != nil {
		t.Fatal(err)
	}
	if result.Diagnostic != "" || !strings.Contains(result.Patch, content) {
		t.Fatalf("raw transfer failed: diagnostic=%s patch bytes=%d", result.Diagnostic, len(result.Patch))
	}
}

func TestHpatchTranslationCancelledRawStdin(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	transform, _ := mixedTestTransform(t)
	if _, err := transform.retainMixedScript("", "", "shell true", nil); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestHpatchMixedProcess$", "--")
	command.Env = append(os.Environ(), "MEKUGI_HPATCH_WORKER_TEST=1", "MEKUGI_HPATCH_CANCEL_TRANSFER=1",
		"MEKUGI_RUNTIME_DIR="+filepath.Dir(transform.shellDirectory),
		"CODEX_THREAD_ID="+strings.TrimPrefix(filepath.Base(transform.shellDirectory), "mekugi-scripts-"))
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	reader := bufio.NewReader(terminal)
	ready, err := reader.ReadString('\n')
	if err != nil || ready != hpatchTranslationReady {
		t.Fatalf("ready = %q, %v", ready, err)
	}
	// Leave the transfer incomplete. Cancellation must unblock the inherited
	// PTY read, not rely on the outer test deadline to kill an orphan.
	if _, err := io.WriteString(terminal, "new "); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("incomplete transfer succeeded")
	}
	if ctx.Err() != nil {
		t.Fatal("receiver outlived its cancellation")
	}
}
