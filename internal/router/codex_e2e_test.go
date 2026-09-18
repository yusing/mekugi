//go:build e2e

package router

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const codexE2EPrompt = `Use functions.shell for all operations. For every file edit, run hpatch as a standalone shell command. Use other shell commands for inspection and verification.

Work through these requests in order:

1. Inspect whole.go with fresh hcat output. Replace the complete logical lines of the status function with:
func status() string {
	return "new"
}
The replacement must have exactly one trailing newline and no blank line after the closing brace. Use a range target directly in type.

2. In anchor.go, change only return saveArtifactPayload(path, b) to return saveArtifactPayloadAtomically(path, b). Preserve its existing indentation exactly; do not put the indentation in a content anchor.

3. In partial.go, replace the multiline block beginning at oldCall( and ending at finalArgument) with newCall(firstArgument, finalArgument). Preserve the prefix and suffix on the boundary lines.

Do not merely describe the edits. Make them and verify the resulting files.`

func TestCodexMekugiGrammarE2E(t *testing.T) {
	codexPath := requireExecutable(t, "codex")
	gitPath := requireExecutable(t, "git")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var requestSequence atomic.Uint64
	server := httptest.NewServer(responsesHandler(
		t.Context(),
		10*time.Minute,
		newProviderClient(codexBaseURL, nil),
		nil,
		newManagedMekugiProxy(t),
		nil, nil,
		&requestSequence,
	))
	defer server.Close()

	workspace := t.TempDir()
	runCommand(t, workspace, gitPath, "init", "--quiet")
	writeFixture(t, workspace, "whole.go", "package sample\n\nfunc status() string {\n\treturn \"old\"\n}\n")
	writeFixture(t, workspace, "anchor.go", "package sample\n\nfunc save(path string, b []byte) error {\n\t\treturn saveArtifactPayload(path, b)\n}\n")
	writeFixture(t, workspace, "partial.go", "package sample\n\nvar expression = prefix + oldCall(\n\tfirstArgument,\n\tfinalArgument) + suffix\n")

	model := environmentOrDefault("MEKUGI_E2E_MODEL", "gpt-5.6-luna")
	providerName := "mekugi-e2e"
	baseURL := server.URL + "/v1"
	providerConfig := "model_providers." + providerName + "={ name = " + strconv.Quote(providerName) +
		", base_url = " + strconv.Quote(baseURL) + ", wire_api = \"responses\", requires_openai_auth = true }"
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	args := []string{
		"-c", providerConfig,
		"--local-provider", providerName,
		"--oss",
		"--model", model,
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
		"exec",
		"--ignore-user-config",
		"--json",
		"--color", "never",
		"-C", workspace,
		codexE2EPrompt,
	}
	var stdout, stderr bytes.Buffer
	codex := exec.CommandContext(ctx, codexPath, args...)
	codex.Stdout = &stdout
	codex.Stderr = &stderr
	if err := codex.Run(); err != nil {
		t.Fatalf(
			"run Codex E2E: %v\nstdout tail:\n%s\nstderr tail:\n%s",
			err,
			tailBytes(stdout.Bytes(), 64<<10),
			tailBytes(stderr.Bytes(), 64<<10),
		)
	}

	assertFileBytes(t, workspace, "whole.go", "package sample\n\nfunc status() string {\n\treturn \"new\"\n}\n")
	assertFileBytes(t, workspace, "anchor.go", "package sample\n\nfunc save(path string, b []byte) error {\n\t\treturn saveArtifactPayloadAtomically(path, b)\n}\n")
	assertFileBytes(t, workspace, "partial.go", "package sample\n\nvar expression = prefix + newCall(firstArgument, finalArgument) + suffix\n")

}

func requireExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s is required for the E2E test: %v", name, err)
	}
	return path
}

func environmentOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func runCommand(t *testing.T, directory, path string, args ...string) {
	t.Helper()
	command := exec.Command(path, args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run %s: %v\n%s", filepath.Base(path), err, output)
	}
}

func writeFixture(t *testing.T, workspace, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFileBytes(t *testing.T, workspace, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(workspace, name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("%s bytes differ:\n got %q\nwant %q", name, got, want)
	}
}

func tailBytes(value []byte, limit int) []byte {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
