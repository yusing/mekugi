package commandsegments_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/commandsegments"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

const workspace = "/retained/workspace"

func composedReports(t *testing.T, extra ...recordkind.Spec) (*reports.Service, *history.Service) {
	t.Helper()
	kinds := append([]recordkind.Spec{}, commandsegments.Module.Contributes.RecordKinds...)
	kinds = append(kinds, extra...)
	kinds = append(kinds,
		recordkind.Spec{ID: "call", Match: func(name string) bool { return strings.HasPrefix(name, "call-") && strings.HasSuffix(name, ".json") }, Dependencies: func(r recordkind.Record) ([]string, error) { return legacycall.Dependencies(r.Name, r.Data) }},
		recordkind.Spec{ID: "snapshot", Match: legacycall.ValidSnapshotName})
	owner := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: kinds}, nil)
	service := moduletest.New(t, commandsegments.Module, commandsegments.Deps{History: owner}, module.Slots{}, nil)
	return service, owner
}

func selected(t *testing.T, owner *history.Service) reports.Scope {
	t.Helper()
	return reports.Scope{Store: owner.Attach(t.TempDir()), Workspace: workspace, Thread: "viewer"}
}

func evidence() reports.Report {
	output := "ok"
	return reports.Report{Command: "echo ok", Output: [32]byte{7, 8}, Parts: []reports.Part{{Source: "echo ok", Output: &output}}}
}

func completed(id string, r reports.Report) reports.Completed {
	c := reports.Completed{ID: id, Command: r.Command, Exit: r.Exit, Output: r.Output}
	for _, p := range r.Parts {
		c.Sources = append(c.Sources, p.Source)
	}
	return c
}

func writeFile(t *testing.T, directory, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// Independently spelled historical mixed record, including unrelated filesystem
// facts. Neither the report encoder nor the legacy encoder constructs this wire.
func historical(t *testing.T, scope reports.Scope, id string) (string, []byte) {
	t.Helper()
	manifest := []byte(`{"Exec":[{"Path":"a","Content":"retained baseline"}]}`)
	snapshot := fmt.Sprintf("snapshot-%x.json.gz", sha256.Sum256(manifest))
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, scope.Store.Directory(), snapshot, compressed.Bytes())
	wire := []byte(fmt.Sprintf(`{"Version":2,"Workspace":%q,"CallID":%q,
 "History":{"ToolName":"exec","Applied":true,"Report":"unrelated history",
 "ExecObservation":{"Sweep":true},
 "CommandSegments":{"Command":"echo ok","Output":[7,8],"Parts":[{"Source":"echo ok","Output":"ok"}]}},
 "Snapshots":{"ExecFiles":%q}}`, scope.Workspace, id, snapshot))
	writeFile(t, scope.Store.Directory(), legacycall.Name(scope.Workspace, id, false), wire)
	return snapshot, wire
}

func catalogHas(t *testing.T, owner *history.Service, scope reports.Scope, names ...string) {
	t.Helper()
	catalog, err := owner.Attach(scope.Store.Directory()).ReadCatalog(history.SessionName(scope.Thread))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Files) != len(names) {
		t.Fatalf("catalog=%v, want %v", catalog.Files, names)
	}
	for _, name := range names {
		if !catalog.Files[name] {
			t.Fatalf("catalog missing %s: %v", name, catalog.Files)
		}
	}
}

func TestReportsRoundTripAndImmutableRetries(t *testing.T) {
	service, owner := composedReports(t)
	scope := selected(t, owner)
	id := reports.ID("turn", "item")
	report := evidence()
	empty := ""
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	report.Parts = append(report.Parts,
		reports.Part{Source: "empty", Output: &empty, Timing: reports.Timing{Started: started, Ended: started.Add(time.Second), ElapsedNS: int64(time.Second)}},
		reports.Part{Source: "unavailable"}, reports.Part{Source: "skipped", Skipped: true})
	if err := service.Publish(t.Context(), scope, id, report); err != nil {
		t.Fatal(err)
	}
	name := reports.Name(scope.Workspace, id)
	before := tree(t, scope.Store.Directory())
	retry := report
	retry.Parts = append([]reports.Part{}, report.Parts...)
	retry.Parts[1].Timing.Started = started.In(time.FixedZone("equivalent", 3600))
	if err := service.Publish(t.Context(), scope, id, retry); err != nil {
		t.Fatalf("equal retry: %v", err)
	}
	if !reflect.DeepEqual(before, tree(t, scope.Store.Directory())) {
		t.Fatal("equal retry rewrote retained files")
	}
	for _, mutate := range []func(*reports.Report){
		func(r *reports.Report) { r.Command += "!" }, func(r *reports.Report) { r.Exit++ },
		func(r *reports.Report) { r.Output[0]++ }, func(r *reports.Report) { r.Parts[0].Source += "!" },
		func(r *reports.Report) { r.Parts[0].Output = nil }, func(r *reports.Report) { r.Parts[1].Timing.ElapsedNS++ },
	} {
		changed := report
		changed.Parts = append([]reports.Part{}, report.Parts...)
		mutate(&changed)
		if err := service.Publish(t.Context(), scope, id, changed); err == nil {
			t.Fatal("conflicting retry accepted")
		}
	}
	restarted, _ := composedReports(t)
	restored, err := restarted.Restore(t.Context(), scope, []reports.Completed{completed(id, report)})
	if err != nil || !reflect.DeepEqual(restored[id], &report) {
		t.Fatalf("roundtrip=%#v err=%v", restored, err)
	}
	catalogHas(t, owner, scope, name)
	before = tree(t, scope.Store.Directory())
	inspection, found, err := restarted.Inspect(scope.Store.Directory(), scope.Workspace, id)
	if err != nil || !found || inspection.Name != name || len(inspection.Data) == 0 || inspection.Legacy != nil || len(inspection.Dependencies) != 0 {
		t.Fatalf("inspection=%+v found=%v err=%v", inspection, found, err)
	}
	if !reflect.DeepEqual(before, tree(t, scope.Store.Directory())) {
		t.Fatal("current inspection mutated storage")
	}
}

func TestHistoricalOnlyRetryReusesMixedRecordAndDependencies(t *testing.T) {
	service, owner := composedReports(t)
	scope := selected(t, owner)
	id := reports.ID("old-turn", "old-item")
	snapshot, wire := historical(t, scope, id)
	name := legacycall.Name(scope.Workspace, id, false)
	if err := service.Publish(t.Context(), scope, id, evidence()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(scope.Store.Directory(), name))
	if err != nil || !bytes.Equal(data, wire) {
		t.Fatalf("historical mixed record rewritten: %s, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(scope.Store.Directory(), reports.Name(scope.Workspace, id))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created duplicate report: %v", err)
	}
	catalogHas(t, owner, scope, name, snapshot)
	before := tree(t, scope.Store.Directory())
	inspection, found, err := service.Inspect(scope.Store.Directory(), scope.Workspace, id)
	if err != nil || !found || inspection.Legacy == nil || len(inspection.Data) != 0 || !reflect.DeepEqual(inspection.Dependencies, []string{snapshot}) {
		t.Fatalf("inspection=%+v found=%v err=%v", inspection, found, err)
	}
	if got := inspection.Legacy.History; got.Report != "unrelated history" || got.ExecObservation == nil || len(got.ExecObservation.Files) != 1 || got.ExecObservation.Files[0].Content != "retained baseline" {
		t.Fatalf("lost mixed facts: %+v", got)
	}
	if !reflect.DeepEqual(before, tree(t, scope.Store.Directory())) {
		t.Fatal("inspection mutated storage")
	}
	scope.Thread = "fork"
	restarted, _ := composedReports(t)
	restored, err := restarted.Restore(t.Context(), scope, []reports.Completed{completed(id, evidence())})
	if err != nil || !reflect.DeepEqual(restored[id], new(evidence())) {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	catalogHas(t, owner, scope, name, snapshot)
}

func TestReportsValidateBothPresentFormsAndWholeMixedRecord(t *testing.T) {
	for _, state := range []string{"agree", "conflict", "corrupt-current", "corrupt-mixed", "unknown-mixed-field", "missing-snapshot", "corrupt-snapshot"} {
		t.Run(state, func(t *testing.T) {
			service, owner := composedReports(t)
			scope := selected(t, owner)
			id := reports.ID("turn", "item")
			if err := service.Publish(t.Context(), scope, id, evidence()); err != nil {
				t.Fatal(err)
			}
			snapshot, wire := historical(t, scope, id)
			switch state {
			case "conflict":
				wire = bytes.Replace(wire, []byte(`"Output":"ok"`), []byte(`"Output":"different"`), 1)
			case "corrupt-current":
				writeFile(t, scope.Store.Directory(), reports.Name(scope.Workspace, id), []byte("{"))
			case "corrupt-mixed":
				wire = bytes.Replace(wire, []byte(`"Applied":true`), []byte(`"Applied":"true"`), 1)
			case "unknown-mixed-field":
				wire = bytes.Replace(wire, []byte(`"Sweep":true`), []byte(`"Sweep":true,"Future":null`), 1)
			case "missing-snapshot":
				if err := os.Remove(filepath.Join(scope.Store.Directory(), snapshot)); err != nil {
					t.Fatal(err)
				}
			case "corrupt-snapshot":
				writeFile(t, scope.Store.Directory(), snapshot, []byte("invalid gzip"))
			}
			writeFile(t, scope.Store.Directory(), legacycall.Name(scope.Workspace, id, false), wire)
			inspection, found, inspectErr := service.Inspect(scope.Store.Directory(), scope.Workspace, id)
			restored, restoreErr := service.Restore(t.Context(), scope, []reports.Completed{completed(id, evidence())})
			publishErr := service.Publish(t.Context(), scope, id, evidence())
			if state == "agree" {
				if inspectErr != nil || !found || inspection.Legacy != nil || len(inspection.Data) == 0 || len(inspection.Dependencies) != 0 || restored[id] == nil || restoreErr != nil || publishErr != nil {
					t.Fatalf("agree: inspection=%+v found=%v inspect=%v restore=%v publish=%v", inspection, found, inspectErr, restoreErr, publishErr)
				}
			} else if inspectErr == nil || found || len(restored) != 0 || restoreErr != nil || publishErr == nil {
				t.Fatalf("invalid %s: found=%v inspect=%v restored=%v restore=%v publish=%v", state, found, inspectErr, restored, restoreErr, publishErr)
			}
		})
	}
}

func TestRestoreIndependentCandidatesAndOwnershipAdmission(t *testing.T) {
	service, owner := composedReports(t)
	scope := selected(t, owner)
	scope.Thread = ""
	good, bad := reports.ID("turn", "good"), reports.ID("turn", "bad")
	for _, id := range []string{good, bad} {
		if err := service.Publish(t.Context(), scope, id, evidence()); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, scope.Store.Directory(), reports.Name(scope.Workspace, bad), []byte("{"))
	scope.Thread = "viewer"
	restored, err := service.Restore(t.Context(), scope, []reports.Completed{completed(bad, evidence()), completed(good, evidence())})
	if err != nil || len(restored) != 1 || restored[good] == nil {
		t.Fatalf("independent restore=%v err=%v", restored, err)
	}
	catalogHas(t, owner, scope, reports.Name(scope.Workspace, good))
	// A broken requesting catalog prevents exposure without changing the evidence.
	scope.Thread = "broken"
	writeFile(t, scope.Store.Directory(), history.SessionName(scope.Thread), []byte("{"))
	restored, err = service.Restore(t.Context(), scope, []reports.Completed{completed(good, evidence())})
	if err != nil || len(restored) != 0 {
		t.Fatalf("exposed unowned report: %v %v", restored, err)
	}
	if err := service.Publish(t.Context(), scope, good, evidence()); err == nil {
		t.Fatal("retry ignored ownership failure")
	}
}

func TestRestoreRetriesSmallerAdmissionsInSourceOrder(t *testing.T) {
	service, owner := composedReports(t)
	scope := selected(t, owner)
	scope.Thread = ""
	ids := []string{reports.ID("turn", "first"), reports.ID("turn", "second")}
	for _, id := range ids {
		if err := service.Publish(t.Context(), scope, id, evidence()); err != nil {
			t.Fatal(err)
		}
	}
	scope.Thread = "sizing"
	if _, err := service.Restore(t.Context(), scope, []reports.Completed{completed(ids[0], evidence())}); err != nil {
		t.Fatal(err)
	}
	sizing, err := os.Stat(filepath.Join(scope.Store.Directory(), history.SessionName(scope.Thread)))
	if err != nil {
		t.Fatal(err)
	}
	var used int64
	for name, data := range tree(t, scope.Store.Directory()) {
		if name != "store.lock" && name != "storage-revision" {
			used += int64(len(data))
		}
	}
	scope.Thread = "viewer"
	scope.Limits.Total = used + sizing.Size() + 10
	got, err := service.Restore(t.Context(), scope, []reports.Completed{completed(ids[0], evidence()), completed(ids[1], evidence())})
	if err != nil || len(got) != 1 || got[ids[0]] == nil {
		t.Fatalf("partial admission=%v err=%v", got, err)
	}
	catalogHas(t, owner, scope, reports.Name(scope.Workspace, ids[0]))
}

func TestExplicitInspectionDoesNotCreateStorageOrCoordination(t *testing.T) {
	service, owner := composedReports(t)
	scope := selected(t, owner)
	id := reports.ID("turn", "item")
	missing := filepath.Join(scope.Store.Directory(), "absent")
	if _, found, err := service.Inspect(missing, workspace, id); err != nil || found {
		t.Fatalf("missing inspect: %v %v", found, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created storage: %v", err)
	}
	historical(t, scope, id)
	before := tree(t, scope.Store.Directory())
	for _, request := range []struct{ workspace, id string }{{workspace, id}, {"/other", id}, {workspace, reports.ID("other", "item")}} {
		_, found, err := service.Inspect(scope.Store.Directory(), request.workspace, request.id)
		if err != nil || found != (request.workspace == workspace && request.id == id) {
			t.Fatalf("inspect %+v: found=%v err=%v", request, found, err)
		}
	}
	if !reflect.DeepEqual(before, tree(t, scope.Store.Directory())) {
		t.Fatal("inspection created locks/catalogs or changed evidence")
	}
}

func TestPublicationRejectsInvalidEvidenceAndCancellation(t *testing.T) {
	service, owner := composedReports(t)
	scope := selected(t, owner)
	id := reports.ID("turn", "item")
	for _, report := range []reports.Report{{}, {Parts: []reports.Part{{Skipped: true, Output: new("")}}}, {Parts: []reports.Part{{Timing: reports.Timing{ElapsedNS: 1}}}}} {
		if err := service.Publish(t.Context(), scope, id, report); err == nil {
			t.Fatalf("accepted invalid report: %+v", report)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.Publish(ctx, scope, id, evidence()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publish: %v", err)
	}
	if got, err := service.Restore(ctx, scope, []reports.Completed{completed(id, evidence())}); !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("cancelled restore=%v err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(scope.Store.Directory(), reports.Name(workspace, id))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled publication retained report: %v", err)
	}
}

func tree(t *testing.T, directory string) map[string]string {
	t.Helper()
	result := map[string]string{}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = string(data)
	}
	return result
}

func TestReportsReusePublicationSizeCache(t *testing.T) {
	scans := 0
	service, owner := composedReports(t, recordkind.Spec{ID: "scan-probe", Match: func(name string) bool {
		if name == "cache-probe" {
			scans++
		}
		return false
	}})
	scope := selected(t, owner)
	writeFile(t, scope.Store.Directory(), "cache-probe", nil)
	first, second := reports.ID("turn", "first"), reports.ID("turn", "second")
	if err := service.Publish(t.Context(), scope, first, evidence()); err != nil {
		t.Fatal(err)
	}
	if scans == 0 {
		t.Fatal("fixture did not exercise a directory scan")
	}
	scans = 0
	if err := service.Publish(t.Context(), scope, second, evidence()); err != nil {
		t.Fatal(err)
	}
	scope.Thread = "fork"
	got, err := service.Restore(t.Context(), scope, []reports.Completed{completed(first, evidence()), completed(second, evidence())})
	if err != nil || len(got) != 2 {
		t.Fatalf("restore=%v err=%v", got, err)
	}
	if scans != 0 {
		t.Fatalf("unchanged publication handle rescanned directory %d times", scans)
	}
}
