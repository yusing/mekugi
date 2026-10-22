package legacycall_test

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/legacycall"
)

const (
	workspace = "/retained/workspace"
	callID    = "legacy-call"
	readLimit = 1 << 20
)

// Literal historical input, deliberately independent of the current encoder.
// Sparse numeric arrays, folded names and repeated members all use v1 rules.
const historicalWire = `{
	"Version":1,"Workspace":"/retained/workspace","CallID":"legacy-call",
	"TaskPath":"old/task","Replacement":{"Thread":"old-thread","Turn":"old-turn","Items":["old-answer"]},
	"History":{"ToolName":"old","Report":"keep merged history"},
	"history":{
		"toolname":"exec","Applied":true,
		"JournalIDs":["legacy-item"],"JournalFinishTurnID":"legacy-turn",
		"ReviewFiles":[{"AfterPath":"a","CreatedPath":"/old/workspace/a","Diff":"saved diff"}],
		"ExecObservation":{"Sweep":true,"Class":"write","Files":[{"Path":"a","Content":"before"}]},
		"CommandSegments":{"Command":"old","command":"echo ok","Output":[7,8],
			"Parts":[{"Source":"echo ok","Output":"ok"}]},
		"UpstreamItem":{"extension":{"opaque":[1,"two"]}}
	}
}`

func writeRecord(t *testing.T, directory, wire string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, legacycall.Name(workspace, callID, false)), []byte(wire), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadHistoricalV1Semantics(t *testing.T) {
	directory := t.TempDir()
	writeRecord(t, directory, historicalWire)
	r, found, err := legacycall.Read(directory, workspace, callID, false, readLimit)
	if err != nil || !found {
		t.Fatalf("read historical record: found %v, error %v", found, err)
	}
	h := r.History
	if r.Version != 1 || r.Snapshots != nil || h.ToolName != "exec" || h.Report != "keep merged history" {
		t.Fatalf("lost v1 duplicate-member merge or folded names: %#v", r)
	}
	if h.CommandSegments == nil || h.CommandSegments.Command != "echo ok" || h.CommandSegments.Output != [32]byte{7, 8} {
		t.Fatalf("lost sparse numeric array or last scalar value: %#v", h.CommandSegments)
	}
	if len(h.CommandSegments.Parts) != 1 || h.CommandSegments.Parts[0].Output == nil || *h.CommandSegments.Parts[0].Output != "ok" {
		t.Fatalf("lost segment output: %#v", h.CommandSegments.Parts)
	}
	if h.ExecObservation == nil || h.ExecObservation.Class != "write" || len(h.ExecObservation.Files) != 1 || h.ExecObservation.Files[0].Content != "before" {
		t.Fatalf("legacy Sweep obscured observation: %#v", h.ExecObservation)
	}
	if !reflect.DeepEqual(h.ReviewFiles, []legacycall.ReviewFile{{AfterPath: "a", Diff: "saved diff"}}) {
		t.Fatalf("retired creation identity obscured review: %#v", h.ReviewFiles)
	}
	if string(h.UpstreamItem["extension"]) != `{"opaque":[1,"two"]}` {
		t.Fatalf("lost opaque provider extension: %s", h.UpstreamItem["extension"])
	}
}

func TestReadRejectsInvalidMixedRecord(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"unknown-observation", `"Sweep":true`, `"Sweep":true,"Future":null`},
		{"unknown-unrelated-file", `"Path":"a"`, `"Path":"a","Future":true`},
		{"malformed-applied", `"Applied":true`, `"Applied":"true"`},
		{"malformed-sweep", `"Sweep":true`, `"Sweep":[]`},
		{"malformed-created-path", `"CreatedPath":"/old/workspace/a"`, `"CreatedPath":42`},
		{"unknown-review", `"CreatedPath":"/old/workspace/a"`, `"Future":true`},
		{"malformed-journal-ids", `"JournalIDs":["legacy-item"]`, `"JournalIDs":"legacy-item"`},
		{"malformed-journal-turn", `"JournalFinishTurnID":"legacy-turn"`, `"JournalFinishTurnID":42`},
		{"malformed-task-path", `"TaskPath":"old/task"`, `"TaskPath":42`},
		{"malformed-replacement", `"Items":["old-answer"]`, `"Items":42`},
		{"unknown-replacement", `"Items":["old-answer"]`, `"Future":true`},
		{"malformed-unrelated-file", `"Content":"before"`, `"Content":42`},
		{"trailing-value", "\n}", "\n} {}"},
		{"trailing-malformed", "\n}", "\n} {"},
		{"workspace-identity", `"Workspace":"/retained/workspace"`, `"Workspace":"/other"`},
		{"call-identity", `"CallID":"legacy-call"`, `"CallID":"other"`},
		{"commentary-identity", `"Version":1`, `"Version":1,"Commentary":true`},
		{"version", `"Version":1`, `"Version":3`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			writeRecord(t, directory, strings.Replace(historicalWire, tc.old, tc.replacement, 1))
			if _, found, err := legacycall.Read(directory, workspace, callID, false, readLimit); err == nil || found {
				t.Fatalf("accepted invalid mixed record: found %v, error %v", found, err)
			}
		})
	}
}

func TestReadMixedV2RequiresSnapshotDespiteValidSegments(t *testing.T) {
	// This record is independently spelled, with a real content-addressed gzip
	// dependency. Valid command evidence cannot excuse missing filesystem facts.
	manifest := []byte(`{"Exec":[{"Path":"a","Content":"retained baseline"}]}`)
	name := fmt.Sprintf("snapshot-%x.json.gz", sha256.Sum256(manifest))
	wire := fmt.Sprintf(`{"Version":2,"Workspace":"/retained/workspace","CallID":"legacy-call",
		"History":{"Applied":true,"ExecObservation":{"Sweep":true},
		"CommandSegments":{"Command":"echo ok","Output":[7,8],"Parts":[{"Source":"echo ok","Output":"ok"}]}},
		"Snapshots":{"ExecFiles":%q}}`, name)
	deps, err := legacycall.Dependencies(legacycall.Name(workspace, callID, false), []byte(wire))
	if err != nil || !reflect.DeepEqual(deps, []string{name}) {
		t.Fatalf("mixed dependency discovery: %v, %v", deps, err)
	}
	for _, state := range []string{"missing", "corrupt", "valid"} {
		t.Run(state, func(t *testing.T) {
			directory := t.TempDir()
			writeRecord(t, directory, wire)
			if state != "missing" {
				var compressed bytes.Buffer
				z := gzip.NewWriter(&compressed)
				data := manifest
				if state == "corrupt" {
					// Well-formed gzip and JSON under the wrong content identity.
					data = []byte(`{"Exec":[{"Path":"a","Content":"substituted"}]}`)
				}
				if _, err := z.Write(data); err != nil {
					t.Fatal(err)
				}
				if err := z.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, name), compressed.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r, found, err := legacycall.Read(directory, workspace, callID, false, readLimit)
			if state != "valid" {
				if err == nil || found {
					t.Fatalf("segments masked %s snapshot: found %v, error %v", state, found, err)
				}
				return
			}
			if err != nil || !found {
				t.Fatalf("read valid mixed snapshot: found %v, error %v", found, err)
			}
			if r.Version != 1 || r.Snapshots != nil || r.History.ExecObservation == nil || !reflect.DeepEqual(r.History.ExecObservation.Files, []legacycall.File{{Path: "a", Content: "retained baseline"}}) {
				t.Fatalf("snapshot not expanded into normalized record: %#v", r)
			}
			if r.History.CommandSegments == nil || r.History.CommandSegments.Output != [32]byte{7, 8} || r.History.CommandSegments.Command != "echo ok" {
				t.Fatalf("mixed snapshot lost command evidence: %#v", r.History.CommandSegments)
			}
			data, exists, err := legacycall.SnapshotData(directory, name, readLimit)
			if err != nil || !exists || !bytes.Equal(data, manifest) {
				t.Fatalf("snapshot bytes: %q, exists %v, error %v", data, exists, err)
			}
		})
	}
}

func TestReadRetiredCreatedPathInReviewSnapshot(t *testing.T) {
	directory := t.TempDir()
	manifest := []byte(`{"Review":[{"AfterPath":"a","CreatedPath":"/unavailable/old/a","Diff":"saved diff"}]}`)
	name := legacycall.SnapshotName(manifest)
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, directory, fmt.Sprintf(`{"Version":2,"Workspace":%q,"CallID":%q,"History":{},"Snapshots":{"ReviewFiles":%q}}`, workspace, callID, name))
	r, found, err := legacycall.Read(directory, workspace, callID, false, readLimit)
	if err != nil || !found {
		t.Fatalf("read historical review snapshot: found %v, error %v", found, err)
	}
	if !reflect.DeepEqual(r.History.ReviewFiles, []legacycall.ReviewFile{{AfterPath: "a", Diff: "saved diff"}}) {
		t.Fatalf("retired acquisition metadata changed review: %#v", r.History.ReviewFiles)
	}
	retained, exists, err := legacycall.SnapshotData(directory, name, readLimit)
	if err != nil || !exists || !bytes.Equal(retained, manifest) {
		t.Fatalf("read changed retained snapshot: exists %v, error %v", exists, err)
	}
}
