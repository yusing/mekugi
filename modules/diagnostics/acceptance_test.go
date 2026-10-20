package diagnostics_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/agentmetrics"
	"github.com/yusing/mekugi/modules/diagnostics"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection"
	"github.com/yusing/mekugi/modules/transportdiagnostics"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func newDiagnostics(t *testing.T, sections ...diagnosticsection.Contribution) *diagnostic.Service {
	t.Helper()
	script := new(moduletest.Scripted)
	history := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{RecordKinds: diagnostics.Module.Contributes.RecordKinds}, script)
	inspection := moduletest.New(t, sessioninspection.Module, sessioninspection.Deps{}, module.Slots{}, script)
	return moduletest.New(t, diagnostics.Module, diagnostics.Deps{History: history, Inspection: inspection}, module.Slots{DiagnosticSections: sections}, script)
}

func TestDiagnosticsBundleWithoutContributors(t *testing.T) {
	service := newDiagnostics(t)
	root := t.TempDir()
	bundle, err := service.OpenBundle(diagnostic.BundleOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	paths := bundle.Paths()
	if len(paths) != 2 || bundle.CapturePath() != "" || bundle.MetricsPath() != "" || bundle.ReadLogPath() != "" {
		t.Fatalf("zero-contribution bundle paths=%v capture=%q metrics=%q reads=%q", paths, bundle.CapturePath(), bundle.MetricsPath(), bundle.ReadLogPath())
	}
	bundle.Event(map[string]any{"event": "request_complete", "request_id": "request-1", "outcome": "failed", "error": "private failure"})
	bundle.Instructions(map[string]any{"request_id": "request-1", "model": "fixture", "instructions": "private instructions"})
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if bundle.Writes().Snapshot().Bytes == 0 {
		t.Fatal("bundle writes were not accounted")
	}
	directory := filepath.Dir(paths[0])
	for _, path := range append(paths, directory) {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("private artifact %s: info=%v err=%v", path, info, err)
		}
	}
	// Absent owners leave even malformed optional artifacts untouched and unread.
	for _, name := range []string{"capture.jsonl", "metrics.json", "reads.jsonl", "ax.json"} {
		writeDiagnosticsFixture(t, filepath.Join(directory, name), "not json")
	}
	before := diagnosticsTree(t, root)
	restarted := newDiagnostics(t)
	report, failures := restarted.InspectBundle(t.Context(), diagnostic.InspectOptions{Directory: directory, RequestID: "request-1", Limit: 10, TextBytes: 4096})
	if len(failures) != 0 || report.Schema != "mekugi.session.debug.v1" || len(report.Artifacts) != 2 || report.TotalRequests != 1 || report.TotalEvidence != 2 {
		t.Fatalf("report=%+v failures=%v", report, failures)
	}
	for _, evidence := range report.Evidence {
		if len(evidence.Text) != 0 {
			t.Fatalf("unselected private text: %+v", evidence)
		}
	}
	report, failures = restarted.InspectBundle(t.Context(), diagnostic.InspectOptions{Directory: directory, RequestID: "request-1", Field: "all", Limit: 10, TextBytes: 4096})
	if len(failures) != 0 || len(report.Evidence) != 2 || report.Evidence[0].Text["error"].Text != "private failure" || report.Evidence[1].Text["instructions"].Text != "private instructions" {
		t.Fatalf("selected private evidence=%+v failures=%v", report.Evidence, failures)
	}
	if !reflect.DeepEqual(before, diagnosticsTree(t, root)) {
		t.Fatal("offline bundle inspection mutated storage")
	}
}

func TestDiagnosticsFailurePublicationAndRestart(t *testing.T) {
	service := newDiagnostics(t)
	root := t.TempDir()
	directory := filepath.Join(root, "replay")
	store, err := service.OpenFailures(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	writes := new(persistence.Counter)
	late := diagnostic.Failure{Version: 1, Time: time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC), Thread: "thread-1", Phase: "stream", Code: "incomplete", Reference: "failure-late", Error: "complete private error", Stream: jsontext.Value(`{"complete":false}`)}
	early := late
	early.Time, early.Reference = late.Time.Add(-time.Minute), "failure-early"
	for _, record := range []diagnostic.Failure{late, early} {
		if err := service.RetainFailure(t.Context(), store, writes, history.Limits{}, record); err != nil {
			t.Fatal(err)
		}
	}
	if writes.Snapshot().Bytes == 0 {
		t.Fatal("failure publication writes were not accounted")
	}
	before := diagnosticsTree(t, root)
	restarted := newDiagnostics(t)
	got, err := restarted.ReadFailures(t.Context(), directory, "")
	if err != nil || !reflect.DeepEqual(got, []diagnostic.Failure{early, late}) {
		t.Fatalf("restarted failures=%+v err=%v", got, err)
	}
	got, err = restarted.ReadFailures(t.Context(), directory, late.Reference)
	if err != nil || !reflect.DeepEqual(got, []diagnostic.Failure{late}) {
		t.Fatalf("exact reference=%+v err=%v", got, err)
	}
	if _, err := restarted.ReadFailures(t.Context(), directory, "expired"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("missing reference error=%v", err)
	}
	missing := filepath.Join(root, "missing-store")
	if got, err := restarted.ReadFailures(t.Context(), missing, ""); err != nil || len(got) != 0 {
		t.Fatalf("absent store=%v err=%v", got, err)
	}
	if !reflect.DeepEqual(before, diagnosticsTree(t, root)) {
		t.Fatal("offline failure lookup mutated storage")
	}
	rejected := late
	rejected.Reference = "not-published"
	err = service.RetainFailure(t.Context(), store, writes, history.Limits{Total: 1}, rejected)
	var capacity *history.CapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("quota publication error=%v", err)
	}
	if _, err := restarted.ReadFailures(t.Context(), directory, rejected.Reference); err == nil {
		t.Fatal("rejected publication became readable")
	}
	if got, err := restarted.ReadFailures(t.Context(), directory, ""); err != nil || !reflect.DeepEqual(got, []diagnostic.Failure{early, late}) {
		t.Fatalf("existing records after rejection=%+v err=%v", got, err)
	}
}

func TestDiagnosticsOptionalContributorsAndIndependentArtifacts(t *testing.T) {
	script := new(moduletest.Scripted)
	moduletest.New(t, transportdiagnostics.Module, transportdiagnostics.Deps{}, module.Slots{}, script)
	ax := moduletest.New(t, agentmetrics.Module, agentmetrics.Deps{}, module.Slots{}, script)
	transportDefinition := transportdiagnostics.Module.Contributes.DiagnosticSections[0]
	axDefinition := agentmetrics.Module.Contributes.DiagnosticSections[0]
	service := newDiagnostics(t,
		diagnosticsection.Contribution{Transport: transportDefinition.Transport},
		diagnosticsection.Contribution{
			PrepareReads: func(path string, outputs ...string) error { return axDefinition.PrepareReads(ax, path, outputs...) },
			ReadSnapshot: func(ctx context.Context, path string) (diagnosticsection.Reads, error) {
				return axDefinition.ReadSnapshot(ax, ctx, path)
			},
		})
	root := t.TempDir()
	capturePath := filepath.Join(root, "selected-capture.jsonl")
	readPath := filepath.Join(root, "selected-reads.jsonl")
	bundle, err := service.OpenBundle(diagnostic.BundleOptions{Root: filepath.Join(root, "bundles"), CapturePath: capturePath, ReadLogPath: readPath,
		Discover: func(context.Context, []string) (map[string][]string, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.CapturePath() != capturePath || bundle.ReadLogPath() != readPath {
		t.Fatalf("selected paths: capture=%q reads=%q", bundle.CapturePath(), bundle.ReadLogPath())
	}
	bundle.Event(map[string]any{"event": "request_complete", "request_id": "router-request", "outcome": "completed"})
	writeDiagnosticsFixture(t, capturePath, `{"boundary":"provider","capture_id":"capture-1","response_status":"incomplete","response_complete":false}`+"\n")
	writeDiagnosticsFixture(t, bundle.MetricsPath(), `{"schema":"fixture","requests":1}`)
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(bundle.Paths()[0])
	before := diagnosticsTree(t, root)
	report, failures := service.InspectBundle(t.Context(), diagnostic.InspectOptions{Directory: directory, Limit: 10, TextBytes: 4096})
	if len(failures) != 0 || len(report.Artifacts) != 6 || report.Metrics == nil || report.AX == nil || report.TotalRequests != 1 || report.TotalEvidence != 1 || report.Evidence[0].Source != "capture.jsonl" {
		t.Fatalf("contributed report=%+v failures=%v", report, failures)
	}
	if !reflect.DeepEqual(before, diagnosticsTree(t, root)) {
		t.Fatal("contributed inspection mutated storage")
	}
	writeDiagnosticsFixture(t, filepath.Join(directory, "instructions.jsonl"), "null\n")
	report, failures = service.InspectBundle(t.Context(), diagnostic.InspectOptions{Directory: directory, RequestID: "capture-1", Limit: 10, TextBytes: 4096})
	if len(failures) != 1 || failures[0].Path != filepath.Join(directory, "instructions.jsonl") || report.TotalEvidence != 1 || report.Metrics == nil || report.AX == nil {
		t.Fatalf("independent artifacts report=%+v failures=%v", report, failures)
	}
}

func writeDiagnosticsFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// Capture directory entries, modes, timestamps, links and bytes, excluding atime
// because reading evidence can update it without any application write.
func diagnosticsTree(t *testing.T, root string) map[string]diagnosticsEntry {
	t.Helper()
	result := make(map[string]diagnosticsEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := diagnosticsEntry{Mode: info.Mode(), Modified: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Content = string(data)
		} else if info.Mode()&os.ModeSymlink != 0 {
			item.Content, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		result[path] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type diagnosticsEntry struct {
	Mode     fs.FileMode
	Modified time.Time
	Content  string
}
