package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		rows, _ := commandOutputRows(appServerItem{Command: command, Cwd: g.dir, AggregatedOutput: &output})
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
		_, key := vcsOutputRows(tc.command, tc.cwd, output)
		// The final absolute -C supersedes a prior relative path for resolution;
		// actual output remains required before a key can be requested.
		got, err := filepath.EvalSymlinks(key.dir)
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
			u.execTrack = &execTrackHub{tracks: map[[3]string]*execTrack{key: {done: true, ended: true, segments: []execTrackSegment{
				{source: prior, began: true, ended: true},
				{source: command, began: true, ended: true, vcs: true, raw: []byte(output)},
			}}}}
			item := appServerItem{ID: key[2], Type: "commandExecution", Command: "bash -lc " + quoteShellWord(prior+"; "+command), Cwd: g.dir, ExitCode: new(0), AggregatedOutput: &output}
			entry := activityPaneEntry{Kind: "tool", native: &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2], command: item.Command}}
			live := u.trackedCommandDone(key, entry, item)[0]
			restored := entry
			restored.native = &liveActivityNativeItem{thread: key[0], turn: key[1], item: key[2]}
			u.restoreCommandSegments(&restored, item, g.dir)
			for _, candidate := range []activityPaneEntry{live, restored} {
				if len(candidate.native.segments) != 2 {
					t.Fatalf("segments missing: %+v", candidate.native.segments)
				}
				commit := candidate.native.segments[1].commit
				if prior == "git add a.go" {
					if commit.dir != g.dir || commit.hash == "" {
						t.Fatalf("known directory lost: %+v", commit)
					}
				} else if commit != (gitCommitKey{}) {
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
		rows, _ := vcsOutputRows(command, g.dir, output)
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
				if row.Verb != "Created" || row.From != "" || row.Note != "copy of source.txt" || row.Added != added || row.Removed != 0 {
					t.Fatalf("copy misrepresented: %+v", row)
				}
			case "other.txt":
				if row.Verb != "Edited" || row.Added != 1 || row.Removed != 1 {
					t.Fatalf("neighbor lost: %+v", row)
				}
			default:
				t.Fatalf("unexpected file: %+v", row)
			}
		}
	}
}
