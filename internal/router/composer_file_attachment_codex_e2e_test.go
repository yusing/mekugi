//go:build journal_e2e

package router

import (
	"context"
	json "encoding/json/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestComposerFileAttachmentsNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	const name = "attachment-unique-4827.txt"
	content := strings.Repeat("line 世界\n", 3000)
	path := filepath.Join(workspace, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &appQueueProvider{requests: make(chan []byte, 8)}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil))
	t.Cleanup(func() { server.Close() })
	newCommand := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server",
			"-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`,
			"-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}
	check := func(terminal *appResumeTerminal) {
		t.Helper()
		select {
		case body := <-provider.requests:
			var request struct {
				Input []struct {
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			var attached strings.Builder
			promptIndex, frames := -1, 0
			for index, item := range request.Input {
				for _, part := range item.Content {
					if part.Text == "Review @"+name+" " {
						promptIndex = index
					}
					if strings.HasPrefix(part.Text, "Attached file "+strconv.Quote(path)+" (") {
						if item.Role != "user" || len(item.Content) != 1 || promptIndex < 0 || index <= promptIndex {
							t.Fatal("file content was not a separate user message after the inline prompt")
						}
						_, data, _ := strings.Cut(part.Text, ":\n")
						attached.WriteString(data)
						frames++
					}
				}
			}
			if attached.String() != content || frames < 2 {
				t.Fatalf("native host lost snapshot bytes or chunking: %d bytes in %d frames", attached.Len(), frames)
			}
		case <-terminal.ctx.Done():
			t.Fatal("no provider request for file attachment")
		}
	}
	first := startAppResumeTerminal(t, newCommand, "")
	first.await("Ready")
	first.send("Review @attachment-unique")
	first.await(name)
	first.send("\t\r")
	first.await("completed")
	check(first)
	first.await("Attached")
	if strings.Contains(first.screen.String(), "mekugi-file-attachments-v1") {
		t.Fatal("attachment envelope leaked into terminal")
	}
	first.stopCanceled()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Fresh router handler and Codex process must recover only the submitted snapshot.
	server.Close()
	server = httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil, nil))
	second := startAppResumeTerminal(t, newCommand, "--last")
	second.await("Ready")
	second.await("Attached")
	second.send("Continue from the attached snapshot\r")
	second.await("completed")
	check(second)
	second.quit()
}
