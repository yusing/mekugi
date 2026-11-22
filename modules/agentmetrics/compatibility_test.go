package agentmetrics_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentmetrics"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestModuleRetainsHistoricalReadLog(t *testing.T) {
	// Frozen version-2 records remain readable across module versions, including
	// historical inspect_file entries.
	const historical = `{"schema":"mekugi.ax.read.v2","id":"historical-read","thread_id":"historical-thread","tool":"mcat","phase":"start","at":"2026-09-11T00:00:00Z","call_id":"historical-call","shell_id":"historical-shell"}
{"schema":"mekugi.ax.read.v2","id":"historical-read","thread_id":"historical-thread","tool":"mcat","phase":"finish","at":"2026-09-11T00:00:00.01Z","duration_ns":10000000,"succeeded":false,"failure_class":"not_found","exit_code":1,"call_id":"historical-call","shell_id":"historical-shell"}
{"schema":"mekugi.ax.read.v2","id":"historical-inspect","thread_id":"historical-thread","tool":"inspect_file","phase":"start","at":"2026-09-11T00:00:01Z","call_id":"new-call"}
{"schema":"mekugi.ax.read.v2","id":"historical-inspect","thread_id":"historical-thread","tool":"inspect_file","phase":"finish","at":"2026-09-11T00:00:01.005Z","duration_ns":5000000,"succeeded":true,"exit_code":0,"call_id":"new-call"}
`
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	if err := os.WriteFile(path, []byte(historical), 0600); err != nil {
		t.Fatal(err)
	}
	service := moduletest.New(t, agentmetrics.Module, agentmetrics.Deps{}, module.Slots{}, nil)
	reads, err := service.ReadReads(t.Context(), path, "historical-thread")
	if err != nil || reads.State != "observed" || reads.Started != 2 || reads.Completed != 2 || reads.Succeeded != 1 || reads.Failed != 1 || reads.Incomplete != 0 || reads.DurationNS != 15000000 || reads.ByTool["mcat"] != 1 || reads.ByTool["inspect_file"] != 1 || reads.FailuresByClass["not_found"] != 1 || len(reads.Failures) != 1 {
		t.Fatalf("historical metrics=%+v error=%v", reads, err)
	}
	failure := reads.Failures[0]
	if failure.ID != "historical-read" || failure.CallID != "historical-call" || failure.ShellID != "historical-shell" || failure.Tool != "mcat" || failure.Class != "not_found" || failure.ExitCode == nil || *failure.ExitCode != 1 {
		t.Fatalf("historical identity=%+v", failure)
	}
}
