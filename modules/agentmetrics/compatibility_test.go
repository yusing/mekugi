package agentmetrics_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentmetrics"
	"github.com/yusing/mekugi/modules/agentmetrics/evidence"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestModuleRetainsHistoricalReadLog(t *testing.T) {
	// Frozen version-2 records remain independent of the current event encoder.
	const historical = `{"schema":"mekugi.ax.read.v2","id":"historical-read","thread_id":"historical-thread","tool":"mcat","phase":"start","at":"2026-09-11T00:00:00Z","call_id":"historical-call","shell_id":"historical-shell"}
{"schema":"mekugi.ax.read.v2","id":"historical-read","thread_id":"historical-thread","tool":"mcat","phase":"finish","at":"2026-09-11T00:00:00.01Z","duration_ns":10000000,"succeeded":false,"failure_class":"not_found","exit_code":1,"call_id":"historical-call","shell_id":"historical-shell"}
`
	path := filepath.Join(t.TempDir(), "reads.jsonl")
	if err := os.WriteFile(path, []byte(historical), 0600); err != nil {
		t.Fatal(err)
	}
	service := moduletest.New(t, agentmetrics.Module, agentmetrics.Deps{}, module.Slots{}, nil)
	before, err := service.ReadReads(t.Context(), path, "historical-thread")
	if err != nil || before.Started != 1 || before.Completed != 1 || before.Failed != 1 || before.DurationNS != 10000000 || len(before.Failures) != 1 {
		t.Fatalf("historical metrics=%+v error=%v", before, err)
	}
	failure := before.Failures[0]
	if failure.ID != "historical-read" || failure.CallID != "historical-call" || failure.ShellID != "historical-shell" || failure.Tool != "mcat" || failure.Class != "not_found" || failure.ExitCode == nil || *failure.ExitCode != 1 {
		t.Fatalf("historical identity=%+v", failure)
	}
	observation, err := service.StartRead(path, "historical-thread", "inspect_file", evidence.AXReadContext{CallID: "new-call"})
	if err != nil {
		t.Fatal(err)
	}
	if err := observation.FinishResult(true, "", new(0)); err != nil {
		t.Fatal(err)
	}
	// A fresh module instance consumes both the retained and newly emitted pairs.
	reopened := moduletest.New(t, agentmetrics.Module, agentmetrics.Deps{}, module.Slots{}, nil)
	log, err := reopened.ReadLog(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	after := log.ForThread("historical-thread")
	if len(log.Threads) != 1 || after.State != "observed" || after.Started != 2 || after.Completed != 2 || after.Succeeded != 1 || after.Failed != 1 || after.Incomplete != 0 || after.ByTool["mcat"] != 1 || after.ByTool["inspect_file"] != 1 || len(after.Failures) != 1 || !reflect.DeepEqual(after.Failures[0], failure) || after.FailuresByClass["not_found"] != 1 {
		t.Fatalf("reopened metrics=%+v", after)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(data, []byte(historical)) || bytes.Count(data, []byte{'\n'}) != 4 {
		t.Fatalf("retained pair changed or appended pair missing: %q, %v", data, err)
	}
}
