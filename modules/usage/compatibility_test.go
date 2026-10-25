package usage_test

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

// Source: internal/router/thread_usage_store.go, request_observation.go,
// output_throughput.go and capturer/throughput.go@47bcdd8e.
// This wire fixture is independent of the current record encoder and types.
const historicalUsageV1 = `{
 "Version":1,"Thread":"historical-thread",
 "Counts":{"TotalsKnown":false,"InputTokens":100,"UncachedInputTokens":70,
 "CacheWriteTokens":5,"OutputTokens":20,"ReasoningTokens":8,
 "Inconsistent":false,"Incomplete":false,"ServiceTier":""},
 "UncachedCost":1.25,"CachedCost":0.5,"OutputCost":2.75,
 "CostKnown":true,"Complete":true,"PriorUnknown":false,
 "MissingUsage":1,"Roundtrips":3,"Models":["historical-model"],
 "RoundOutput":{"StartedUnixNano":123456789,
 "Throughput":{"output_tokens":20,"duration_ns":2000000000,"measured_requests":1}}
}`

func usageRecordName(thread string) string {
	return fmt.Sprintf("usage-%x.json", sha256.Sum256([]byte(thread)))
}

func composedUsage(t *testing.T, directory string) (*accounting.Service, *history.Service) {
	t.Helper()
	owner := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: usage.Module.Contributes.RecordKinds}, nil)
	service := moduletest.New(t, usage.Module, usage.Deps{}, module.Slots{}, nil)
	service.Configure(owner.Attach(directory), nil, history.Limits{}, func(thread string, err error) {
		t.Errorf("retaining %q: %v", thread, err)
	})
	return service, owner
}

func TestModuleHistoricalUsageV1Roundtrip(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, usageRecordName("historical-thread"))
	if err := os.WriteFile(path, []byte(historicalUsageV1), 0600); err != nil {
		t.Fatal(err)
	}
	want := accounting.Report{
		InputTokens: 100, UncachedInputTokens: 70, CacheWriteTokens: 5, OutputTokens: 20, ReasoningTokens: 8,
		Cost:  accounting.Cost{UncachedInput: 1.25, CachedInput: 0.5, Output: 2.75, Known: true},
		Model: "historical-model", MissingUsage: 1, Roundtrips: 3, PriorUnknown: true,
		RoundOutput: accounting.RoundOutput{StartedUnixNano: 123456789, Throughput: inference.OutputThroughput{OutputTokens: 20, DurationNanos: 2000000000, MeasuredRequests: 1}},
	}
	t.Run("read-and-append", func(t *testing.T) {
		service, owner := composedUsage(t, directory)
		kind, found, err := owner.Kind(filepath.Base(path))
		if err != nil || !found || kind.ID != "usage" {
			t.Fatalf("historical record registration: kind=%+v found=%v err=%v", kind, found, err)
		}
		assertUsageReport(t, service, "historical-thread", want)
		observation := service.Observation("historical-thread", "historical-thread", "new-model", "priority")
		observation.Price = func(tier string, counts inference.Usage) accounting.Cost {
			if tier != "default" || counts.InputTokens != 10 {
				t.Errorf("completed exchange pricing: tier=%q counts=%+v", tier, counts)
			}
			return accounting.Cost{UncachedInput: 0.25, CachedInput: 0.125, Output: 0.5, Known: true}
		}
		observation.Observe(inference.Usage{TotalsKnown: true, InputTokens: 10, UncachedInputTokens: 6, CacheWriteTokens: 2, OutputTokens: 4, ReasoningTokens: 1, ServiceTier: "default"})
		want.InputTokens, want.UncachedInputTokens, want.CacheWriteTokens = 110, 76, 7
		want.OutputTokens, want.ReasoningTokens = 24, 9
		want.Cost = accounting.Cost{UncachedInput: 1.5, CachedInput: 0.625, Output: 3.25, Known: true}
		want.Model, want.Roundtrips = "historical-model, new-model", 4
		assertUsageReport(t, service, "historical-thread", want)
		// Descriptor shutdown retains the pending observation before the wire check.
	})
	data, err := history.ReadRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	// Compare exact member spelling at each historical object boundary. Values
	// are checked through the public reader, without depending on member order.
	var old, current map[string]jsontext.Value
	if err := json.Unmarshal([]byte(historicalUsageV1), &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatal(err)
	}
	assertUsageWireKeys(t, old, current)
	if string(current["Version"]) != "1" || string(current["Thread"]) != `"historical-thread"` {
		t.Fatalf("changed record identity: %s", data)
	}
	service, _ := composedUsage(t, directory)
	assertUsageReport(t, service, "historical-thread", want)
}

func assertUsageWireKeys(t *testing.T, old, current map[string]jsontext.Value) {
	t.Helper()
	if len(old) != len(current) {
		t.Fatalf("record fields differ: old=%v current=%v", old, current)
	}
	for key, value := range old {
		actual, ok := current[key]
		if !ok {
			t.Fatalf("missing historical field %q", key)
		}
		if value.Kind() == '{' {
			var a, b map[string]jsontext.Value
			if err := json.Unmarshal(value, &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(actual, &b); err != nil {
				t.Fatal(err)
			}
			assertUsageWireKeys(t, a, b)
		}
	}
}

func assertUsageReport(t *testing.T, service *accounting.Service, thread string, want accounting.Report) {
	t.Helper()
	got, observed := service.Snapshot(thread)
	if !observed || got != want {
		t.Fatalf("%q: observed=%v got=%+v want=%+v", thread, observed, got, want)
	}
}
