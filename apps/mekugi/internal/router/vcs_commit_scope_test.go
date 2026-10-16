package router

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestVCSCommitEnrichmentKeepsSegmentDirectory(t *testing.T) {
	u, hub := newTrackedAppServerUI(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec cat \"$2/enrichment\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	const hash = "abc1234"
	var commands, outputs []string
	for i, name := range []string{"first", "second"} {
		directory := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		files := fmt.Sprintf("%d\t0\t%s.go\n create mode 100644 %s.go\n", i+1, name, name)
		if err := os.WriteFile(filepath.Join(directory, "enrichment"), []byte(files), 0o600); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, "git -C "+quoteShellWord(directory)+" add .", "git -C "+quoteShellWord(directory)+" commit -m "+name)
		outputs = append(outputs, fmt.Sprintf("[main %s] %s\n 1 file changed, %d insertions(+)\n", hash, name, i+1))
	}
	script, err := shellinstrumentation.RewriteCommands(strings.Join(commands, " && "), displayGuardHelper, displayGuardDirectory, "", "")
	if err != nil {
		t.Fatal(err)
	}
	item := map[string]any{"id": "cmd", "type": "commandExecution", "command": "/usr/bin/bash -lc " + quoteShellWord(script), "cwd": u.session.cwd, "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	report := dialExecTrackReport(t, hub, script)
	for i := range commands {
		report.send(segmenttransport.Message{Type: segmenttransport.Begin, Index: i})
		if i%2 == 1 {
			report.send(segmenttransport.Message{Type: segmenttransport.Output, Index: i, Data: outputs[i/2]})
		}
		report.send(segmenttransport.Message{Type: segmenttransport.End, Index: i, Code: new(0)})
	}
	report.send(segmenttransport.Message{Type: segmenttransport.Done, Code: new(0)}, segmenttransport.Message{Type: segmenttransport.Closed})
	report.conn.Close()
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, strings.Join(outputs, "")
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "turn", "item": item})
	awaitMain(t, u, "Committed second")
	index := slices.IndexFunc(u.view.Entries, func(record liveActivityRecord) bool {
		return record.Source.native != nil && record.Source.native.item == "cmd"
	})
	if index < 0 || len(u.view.Entries[index].Source.native.segments) != 4 {
		if index < 0 {
			t.Fatal("tracked invocation lost its source record")
		}
		t.Fatalf("tracked invocation has %d segments, want four", len(u.view.Entries[index].Source.native.segments))
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		u.startCommitReads()
		if !slices.ContainsFunc(u.view.Entries[index].Source.native.segments, func(segment commandSegment) bool { return segment.commit.Hash != "" }) {
			break
		}
		select {
		case <-u.commits.Updates():
		case <-deadline.C:
			t.Fatal("commit reads did not complete")
		}
	}
	checkRows := func(scope, name string, added int, rows []activitypresentation.ChangeRow) {
		t.Helper()
		if len(rows) != 2 || rows[0].Label != name+".go" || rows[0].Added != added || !rows[1].Footer || rows[1].Label != hash || rows[1].Note != "on main · 1 file" || rows[1].Added != added {
			t.Fatalf("%s crossed segment directory or changed its footer: %+v", scope, rows)
		}
	}
	record := u.view.Entries[index]
	for i, name := range []string{"first", "second"} {
		operation := (i + 1) * 2
		checkRows("native "+name, name, i+1, record.Source.native.segments[operation-1].changes)
		value := slices.IndexFunc(record.Values, func(value activitypresentation.Value) bool { return value.Operation == operation })
		if value < 0 || len(record.Values[value].Members) != 2 {
			t.Fatal("staging and commit lost their canonical group")
		}
		checkRows("published "+name, name, i+1, record.Values[value].Changes)
		for _, member := range record.Values[value].Members {
			if member.Operation == operation {
				checkRows("group member "+name, name, i+1, member.Changes)
			}
		}
	}
	if feed := mainFeed(u, 100); strings.Count(feed, "first.go") != 1 || strings.Count(feed, "second.go") != 1 {
		t.Fatalf("rendered commits lost distinct files:\n%s", feed)
	}
	if !u.shell.openEntry(u.view, record.Source.Seq) || len(u.shell.output.pages) != 4 {
		t.Fatal("grouped invocation lost its original dialog pages")
	}
	for i, name := range []string{"first", "second"} {
		checkRows("dialog "+name, name, i+1, u.shell.output.pages[(i+1)*2-1].Changes)
	}
}
