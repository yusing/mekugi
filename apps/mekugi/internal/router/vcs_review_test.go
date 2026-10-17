package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/commandsegments/segments"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestVCSUnsupportedFormatsKeepLiteralOutput(t *testing.T) {
	g := newGitFixture(t)
	g.write("a.go", "one\n")
	g.run("add", ".")
	g.run("commit", "-qm", "subject")
	output := g.run("show", "--format=oneline", "--name-only", "HEAD")
	for _, command := range []string{
		"git show --format=oneline --name-only HEAD",
		"git show --pretty=format:%H --name-only HEAD",
		"git diff --src-prefix=old/ --dst-prefix=new/",
		"git diff --word-diff",
		"git diff --numstat --name-only",
		"git diff -C --numstat",
		"git diff --find-copies-harder --stat",
	} {
		text := toolActivityShell(command)
		if !strings.HasPrefix(text, "Run") {
			t.Fatalf("unsupported format classified: %s: %s", command, text)
		}
		rows, _ := commandOutputRows(binding.ThreadItem{Command: command, Cwd: g.dir, AggregatedOutput: &output})
		if len(rows) != 0 {
			t.Fatalf("metadata became file rows for %s: %+v", command, rows)
		}
	}
}

func TestVCSCumulativeDirectoriesPreserveSymlinkResolution(t *testing.T) {
	g, output := vcsCommitFixture(t, "directory")
	outer := t.TempDir()
	if err := os.Symlink(g.dir, filepath.Join(outer, "link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ cwd, command string }{
		{filepath.Dir(filepath.Dir(g.dir)), "git -C " + quoteShellWord(filepath.Base(filepath.Dir(g.dir))) + " -C " + quoteShellWord(filepath.Base(g.dir)) + " commit -m x"},
		{outer, "git -C link -C " + quoteShellWord("../"+filepath.Base(g.dir)) + " commit -m x"},
		{outer, "git -C link -C " + quoteShellWord(g.dir) + " commit -m x"},
	} {
		_, key := activitypresentation.VCSOutputRows(tc.command, tc.cwd, output)
		// The final absolute -C supersedes a prior relative path for resolution;
		// actual output remains required before a key can be requested.
		got, err := filepath.EvalSymlinks(key.Directory)
		if err != nil || got != g.dir {
			t.Fatalf("directory %q: %+v, %v", tc.command, key, err)
		}
	}
}

func TestVCSCommitEnrichmentRequiresProvenSegmentDirectory(t *testing.T) {
	g, output := vcsCommitFixture(t, "directory")
	for _, prior := range []string{"git add a.go", "cd other", "eval 'cd other'"} {
		t.Run(prior, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, g.dir)
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			u.proxy = &mekugiProxy{replayStore: store}
			key := [3]string{"main", "t", "commit"}
			command := "git commit -m directory"
			hub := newExecTrackFixture(t, t.Context(), segments.Instrumentation{})
			hub.ui, u.execTrack = u, hub.Observer
			u.session.commands[key] = &appServerCommandRun{}
			report := hub.begin(t, key, prior+"; "+command)
			report.send(
				segmenttransport.Message{Type: segmenttransport.Begin, Index: 0}, segmenttransport.Message{Type: segmenttransport.End, Index: 0, Code: new(0)},
				segmenttransport.Message{Type: segmenttransport.Begin, Index: 1}, segmenttransport.Message{Type: segmenttransport.Output, Index: 1, Data: output}, segmenttransport.Message{Type: segmenttransport.End, Index: 1, Code: new(0)},
				segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed},
			)
			hub.await(t, key, func(view execTrackView) bool { return view.complete })
			item := binding.ThreadItem{ID: key[2], Type: "commandExecution", Command: "bash -lc " + quoteShellWord(prior+"; "+command), Cwd: g.dir, ExitCode: new(0), AggregatedOutput: &output}
			entry := activityPaneEntry{Kind: "tool", native: &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2], command: item.Command}}
			live := u.trackedCommandDone(key, entry, item)[0]
			awaitCommandSegments(t, u)
			restored := entry
			restored.native = &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2]}
			u.restoreCommandSegments(&restored, item, g.dir)
			for _, candidate := range []activityPaneEntry{live, restored} {
				if len(candidate.native.segments) != 2 {
					t.Fatalf("segments missing: %+v", candidate.native.segments)
				}
				commit := candidate.native.segments[1].commit
				if prior == "git add a.go" {
					if commit.Directory != g.dir || commit.Hash == "" {
						t.Fatalf("known directory lost: %+v", commit)
					}
				} else if commit != (activitypresentation.CommitKey{}) {
					t.Fatalf("unproven cwd used: %+v", commit)
				}
			}
		})
	}
}

func TestVCSPatchCopiesKeepTheirSource(t *testing.T) {
	g := newGitFixture(t)
	source := "one\ntwo\nthree\nfour\nfive\nsix\n"
	g.write("source.txt", source)
	g.write("other.txt", "before\n")
	g.run("add", ".")
	g.run("commit", "-qm", "base")
	g.write("exact.txt", source)
	g.write("modified.txt", source+"seven\n")
	g.write("other.txt", "after\n")
	g.run("add", ".")
	output := g.run("diff", "--cached", "-C", "--find-copies-harder")
	if !strings.Contains(output, "copy to exact.txt") || !strings.Contains(output, "copy to modified.txt") {
		t.Fatalf("fixture did not produce copy patches: %s", output)
	}
	for _, format := range []string{"", "--numstat", "--stat"} {
		command := "git diff --cached -C --find-copies-harder"
		if format != "" {
			output = g.run("diff", "--cached", "-C", "--find-copies-harder", format, "--summary")
			command += " " + format + " --summary"
		}
		rows, _ := activitypresentation.VCSOutputRows(command, g.dir, output)
		if len(rows) > 0 && rows[len(rows)-1].Footer {
			rows = rows[:len(rows)-1]
		}
		if len(rows) != 3 {
			t.Fatalf("copy omitted from mixed output: %+v", rows)
		}
		for _, row := range rows {
			switch row.Label {
			case "exact.txt", "modified.txt":
				added := 0
				if row.Label == "modified.txt" {
					added = 1
				}
				if row.Code != "A" || row.From != "" || row.Note != "copy of source.txt" || row.Added != added || row.Removed != 0 {
					t.Fatalf("copy misrepresented: %+v", row)
				}
			case "other.txt":
				if row.Code != "M" || row.Added != 1 || row.Removed != 1 {
					t.Fatalf("neighbor lost: %+v", row)
				}
			default:
				t.Fatalf("unexpected file: %+v", row)
			}
		}
	}
}

func TestSessionUIReplayVCSKeepsRetainedEvidenceAcrossReset(t *testing.T) {
	g, output := vcsCommitFixture(t, "recorded commit")
	live := vcsCommandUI(t, g.dir)
	item := map[string]any{"id": "commit", "type": "commandExecution", "command": "git commit -m 'recorded commit'", "cwd": g.dir,
		"status": "completed", "exitCode": 0, "aggregatedOutput": output}
	appServerTestNotify(t, live, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	awaitMain(t, live, "M  a.go")
	// An offline replay neither borrows the live cache nor starts another Git read.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n: > \"$2/replayed-git\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	meta := replayTestMeta("root")
	meta["payload"].(map[string]any)["cwd"] = g.dir
	path := replayTestWrite(t, t.TempDir(), "root.jsonl", meta,
		replayTestRecord("event_msg", replayTestEpoch, map[string]any{"type": "task_started", "turn_id": "turn"}),
		replayTestItem("root", "turn", replayTestEpoch+1000, replayTestEpoch+2000, map[string]any{
			"type": "CommandExecution", "id": "commit", "command": []string{"git", "commit", "-m", "recorded commit"},
			"cwd": g.dir, "aggregated_output": output, "exit_code": 0, "status": "completed"}),
		replayTestRecord("event_msg", replayTestEpoch+3000, map[string]any{"type": "task_complete", "turn_id": "turn"}))
	source, err := replayReadTest(t, nil, t.Context(), path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	p := newUIReplayPlayback(new(activitymodel.Service), t.Context(), source, 1)
	t.Cleanup(p.close)
	for range 2 {
		if err := p.seek(3 * time.Second); err != nil {
			t.Fatal(err)
		}
		replayPlaybackTestPaint(t, p, 90, 28)
		if got := mainFeed(p.ui, 90); !strings.Contains(got, "A  b.go") || strings.Contains(got, "M  a.go") {
			t.Fatalf("replayed evidence:\n%s", got)
		}
		if p.ui.commits != nil {
			t.Fatal("replay acquired a Git reader")
		}
		previous := p.ui.view.Model
		if err := p.seek(0); err != nil {
			t.Fatal(err)
		}
		if p.ui.view.Model == previous || strings.Contains(mainFeed(p.ui, 90), "b.go") {
			t.Fatal("backward reset retained future VCS rows")
		}
	}
	if _, err := os.Stat(filepath.Join(g.dir, "replayed-git")); !os.IsNotExist(err) {
		t.Fatalf("replay started Git: %v", err)
	}
}
