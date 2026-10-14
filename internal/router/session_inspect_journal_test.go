package router

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runJournalInspection(t *testing.T, replay string, metadata map[string]any, args ...string) (int, sessionJournalInspection, string) {
	t.Helper()
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeStockRollout(t, rollout,
		map[string]any{"type": "session_meta", "payload": metadata},
		map[string]any{"type": "turn_context", "payload": map[string]any{"cwd": t.TempDir()}},
	)
	var stdout, stderr bytes.Buffer
	code := RunSessionInspection(t.Context(), append([]string{"--session", rollout, "--replay-dir", replay, "--journal"}, args...), &stdout, &stderr)
	var result sessionJournalInspection
	if code == 0 {
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return code, result, stderr.String()
}

func TestSessionInspectionJournalIdentityBoundsAndReadOnly(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	workspace := t.TempDir()
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "tree", "/root", ""); err != nil {
		t.Fatal(err)
	}
	treeApply(t, proxy, workspace,
		journalMutation{Op: "add", Kind: "task", Title: new("Work"), State: new("blocked"), Reason: new("Need input")},
		journalMutation{Op: "add", Under: "/1", Kind: "context", Title: new("Private title"), Body: new("ééé")},
		journalMutation{Op: "add", Kind: "note", Title: new("Last")},
	)
	// A fork can retain the source session_id while id names its own journal.
	if err := proxy.journals.initialize(t.Context(), proxy.replayStore, workspace, "fork", "/root", "tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, "fork", "", []journalMutation{{Op: "set", P: "/1", Title: new("Fork only")}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proxy.replayStore.directory, journalFilename(workspace, "tree"))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"id": "tree", "cwd": workspace}
	code, result, diagnostic := runJournalInspection(t, proxy.replayStore.directory, metadata)
	if code != 0 || result.Schema != "mekugi.session.journal.v1" || result.Availability != "matched" || result.Workspace != workspace || result.TotalNodes == nil || *result.TotalNodes != 3 || len(result.Nodes) != 3 {
		t.Fatalf("result: %d %+v %s", code, result, diagnostic)
	}
	if result.Nodes[0].State != "blocked" || result.Nodes[1].Path != "/1/1" || result.Nodes[1].Text["body"].Text != "" || result.Nodes[1].Text["body"].OmittedBytes != 6 {
		t.Fatalf("default nodes: %+v", result.Nodes)
	}
	code, result, diagnostic = runJournalInspection(t, proxy.replayStore.directory, metadata, "--offset", "1", "--limit", "1", "--field", "body", "--text-bytes", "3")
	if code != 0 || len(result.Nodes) != 1 || result.NextOffset == nil || *result.NextOffset != 2 || result.Offset != 1 || *result.TotalNodes != 3 {
		t.Fatalf("page: %d %+v %s", code, result, diagnostic)
	}
	if body := result.Nodes[0].Text["body"]; body.Text != "é" || body.Bytes != 6 || body.OmittedBytes != 4 || result.Nodes[0].Text["title"].Text != "" {
		t.Fatalf("bounded fields: %+v", result.Nodes[0])
	}
	code, result, diagnostic = runJournalInspection(t, proxy.replayStore.directory, metadata, "--offset", "3")
	if code != 0 || len(result.Nodes) != 0 || result.NextOffset != nil || *result.TotalNodes != 3 {
		t.Fatalf("end page: %d %+v %s", code, result, diagnostic)
	}
	code, result, diagnostic = runJournalInspection(t, proxy.replayStore.directory, map[string]any{"id": "fork", "session_id": "tree", "cwd": workspace}, "--field", "all")
	if code != 0 || result.ThreadID != "fork" || len(result.Nodes) != 3 || result.Nodes[0].Text["title"].Text != "Fork only" {
		t.Fatalf("fork: %d %+v %s", code, result, diagnostic)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || info.Mode() != afterInfo.Mode() || info.ModTime() != afterInfo.ModTime() {
		t.Fatal("inspection changed journal state or permissions")
	}
}

func TestSessionInspectionJournalAvailabilityAndValidation(t *testing.T) {
	for _, name := range []string{"missing", "empty", "workspace unavailable", "thread unavailable", "relative workspace", "corrupt", "identity mismatch", "override"} {
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			replay := filepath.Join(t.TempDir(), "absent")
			metadata := map[string]any{"id": "thread", "cwd": workspace}
			var args []string
			want, wantCode := "missing", 0
			switch name {
			case "workspace unavailable":
				delete(metadata, "cwd")
				want = "workspace_unavailable"
			case "thread unavailable":
				delete(metadata, "id")
				wantCode = 1
			case "relative workspace":
				metadata["cwd"] = "relative"
				wantCode = 1
			case "override":
				metadata["cwd"] = "relative"
				args = []string{"--workspace", workspace}
				want = "matched"
			case "empty":
				want = "matched"
			case "corrupt", "identity mismatch":
				wantCode = 1
			}
			stored := name == "empty" || name == "corrupt" || name == "identity mismatch" || name == "override"
			if stored {
				store, err := openMekugiReplayStore(replay)
				if err != nil {
					t.Fatal(err)
				}
				journal := threadJournal{Version: 2, Workspace: workspace, Thread: "thread", Author: "/root", Receipts: map[string]journalReceipt{}}
				if err := writeThreadJournal(store, journal); err != nil {
					t.Fatal(err)
				}
				if name == "corrupt" || name == "identity mismatch" {
					data := []byte("{")
					if name == "identity mismatch" {
						journal.Thread = "other"
						data, err = json.Marshal(&journal)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := os.WriteFile(filepath.Join(replay, journalFilename(workspace, "thread")), data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			code, result, diagnostic := runJournalInspection(t, replay, metadata, args...)
			if code != wantCode {
				t.Fatalf("status: %d want %d: %s", code, wantCode, diagnostic)
			}
			if wantCode == 0 {
				if result.Availability != want || len(result.Nodes) != 0 {
					t.Fatalf("availability: %+v", result)
				}
				if (result.TotalNodes != nil) != (want == "matched") {
					t.Fatalf("missing state counted as empty: %+v", result)
				}
			} else if diagnostic == "" {
				t.Fatal("validation failure has no diagnostic")
			}
			if !stored {
				if _, err := os.Stat(replay); !os.IsNotExist(err) {
					t.Fatal("inspection created a replay store")
				}
			}
		})
	}
}

func TestSessionInspectionJournalInvalidOptions(t *testing.T) {
	for _, flags := range [][]string{{"--ax"}, {"--read-log", "x"}, {"--defects", "x"}, {"--call-id", "x"}, {"--debug-dir", "x"}, {"--failures"}, {"--field", "script"}} {
		var stdout, stderr bytes.Buffer
		args := []string{"--journal"}
		if flags[0] != "--debug-dir" && flags[0] != "--failures" {
			args = append(args, "--session", "unused")
		}
		code := RunSessionInspection(t.Context(), append(args, flags...), &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("flags %v: %d %s", flags, code, stderr.String())
		}
	}
}
