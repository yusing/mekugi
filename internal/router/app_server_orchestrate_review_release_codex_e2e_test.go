//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

const reviewerReleaseProbe = "MEKUGI_TEST_REVIEWER_RELEASE"

// Observe only authenticated thread identity, never tool text or credentials.
// The installed host starts this real MCP bridge separately for every thread.
type reviewerIdentityWriter struct {
	bytes.Buffer
	directory string
}

func (w *reviewerIdentityWriter) Write(p []byte) (int, error) {
	w.Buffer.Write(p)
	for bytes.Contains(w.Bytes(), []byte{'\n'}) {
		line, _ := w.ReadBytes('\n')
		var request struct {
			Params struct {
				Meta struct {
					Thread string `json:"threadId"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &request) == nil {
			thread := request.Params.Meta.Thread
			if thread != "" && filepath.Base(thread) == thread && thread != "." && thread != ".." {
				if err := os.WriteFile(filepath.Join(w.directory, thread), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
					return 0, err
				}
			}
		}
	}
	return len(p), nil
}

func init() {
	if len(os.Args) == 4 && os.Args[1] == "review-release-mcp" {
		input, writer, err := os.Pipe()
		if err != nil {
			os.Exit(1)
		}
		go func() {
			_, _ = io.Copy(writer, io.TeeReader(os.Stdin, &reviewerIdentityWriter{directory: os.Args[3]}))
			_ = writer.Close()
		}()
		if err := RunJournalMCPBridge(context.Background(), os.Args[2], input, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func TestOrchestrateReviewerReleaseNativeCodex(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor proof requires Linux procfs")
	}
	t.Setenv(reviewerReleaseProbe, t.TempDir())
	testOrchestrateWorkflowNativeCodex(t, true)
}

type reviewerReleaseEvidence struct {
	pid   int
	pipes []string
}

func reviewerReleaseFDs(t *testing.T, pid int) map[string]bool {
	t.Helper()
	directory := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]bool{}
	for _, entry := range entries {
		if link, err := os.Readlink(filepath.Join(directory, entry.Name())); err == nil {
			links[link] = true
		}
	}
	return links
}

func observeReviewerRelease(t *testing.T, host int, directory string, event appserver.Message, evidence map[string]reviewerReleaseEvidence) {
	t.Helper()
	if directory == "" || event.Method != "turn/completed" {
		return
	}
	var terminal appServerEvent
	if json.Unmarshal(event.Params, &terminal) != nil {
		t.Fatal("invalid terminal event")
	}
	data, err := os.ReadFile(filepath.Join(directory, terminal.ThreadID))
	if os.IsNotExist(err) {
		return // The implementer does not use the journal MCP fixture.
	}
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 0 {
		t.Fatal("missing reviewer helper identity", err)
	}
	hostFDs := reviewerReleaseFDs(t, host)
	proof := reviewerReleaseEvidence{pid: pid}
	for link := range reviewerReleaseFDs(t, pid) {
		if strings.HasPrefix(link, "pipe:") && hostFDs[link] {
			proof.pipes = append(proof.pipes, link)
		}
	}
	if len(proof.pipes) < 2 {
		t.Fatal("reviewer helper lacks host-owned stdin/stdout descriptors")
	}
	evidence[terminal.ThreadID] = proof
}

func verifyReviewerRelease(t *testing.T, u *appServerUI, host int, evidence map[string]reviewerReleaseEvidence) {
	t.Helper()
	if len(evidence) != 2 {
		t.Fatal("expected both authenticated reviewer helper identities", len(evidence))
	}
	for thread, proof := range evidence {
		deadline := time.Now().Add(5 * time.Second)
		for {
			fds := reviewerReleaseFDs(t, host)
			_, err := os.Stat(fmt.Sprintf("/proc/%d", proof.pid))
			if os.IsNotExist(err) && !slices.ContainsFunc(proof.pipes, func(link string) bool { return fds[link] }) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("archived reviewer retained its helper or host pipe descriptors")
			}
			time.Sleep(10 * time.Millisecond)
		}
		id, err := u.client.Send("thread/read", map[string]any{"threadId": thread, "includeTurns": true}, true)
		if err != nil {
			t.Fatal(err)
		}
		result := awaitHistoryRPC(t, u.client, id)
		var history struct {
			Thread appServerThreadInfo `json:"thread"`
		}
		if json.Unmarshal(result.Result, &history) != nil || history.Thread.ID != thread || len(history.Thread.Turns) != 1 || history.Thread.Turns[0].Status != "completed" || len(history.Thread.Turns[0].Items) == 0 {
			t.Fatal("archive lost completed read-only reviewer history")
		}
		t.Logf("reviewer %s: helper %d exited; %d owned host pipe descriptors released; archived history readable", thread, proof.pid, len(proof.pipes))
	}
	id, err := u.client.Send("thread/loaded/list", map[string]any{}, true)
	if err != nil {
		t.Fatal(err)
	}
	result := awaitHistoryRPC(t, u.client, id)
	var loaded struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(result.Result, &loaded); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(loaded.Data, u.thread) {
		t.Fatal("release unloaded Main")
	}
	for thread, child := range u.orchestrateThreads {
		if !slices.Contains(loaded.Data, thread) {
			t.Fatal("release unloaded implementer")
		}
		for _, d := range child.batch.Workflow.Dispatches {
			if d.Kind != orchestrate.DispatchCorrectness && d.Kind != orchestrate.DispatchSimplification {
				continue
			}
			if d.Archive == nil || d.Archive.State != "archived" || d.HostStatus != "completed" || d.ResultItemID == "" || slices.Contains(loaded.Data, d.ThreadID) {
				t.Fatal("reviewer runtime or durable outcome was not released correctly")
			}
		}
	}
}
