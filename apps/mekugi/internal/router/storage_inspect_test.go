package router

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	inspectioncli "github.com/yusing/mekugi/apps/mekugi/internal/inspectioncli"

	"github.com/yusing/mekugi/libraries/legacycall"
)

type storageInspection struct {
	Schema       string `json:"schema"`
	Target       string `json:"target"`
	Bytes        int    `json:"bytes"`
	SHA256       string `json:"sha256"`
	Dependencies int    `json:"dependencies"`
	Field        string `json:"field,omitempty"`
	Offset       int    `json:"offset,omitzero"`
	NextOffset   *int   `json:"next_offset,omitempty"`
	Text         string `json:"text,omitempty"`
}

func TestStorageInspectionBoundedPagesAndNoMutation(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := snapshotTestHistory()
	h.Script = "private unrequested command"
	h.ResolvedBaseline.Files[0].Content = strings.Repeat("π private source\n", 256)
	snapshotTestPut(t, store, t.Context(), "selected", h)
	snapshotTestPut(t, store, t.Context(), "unrelated", mekugiHistory{Script: "unrelated private command"})
	before := storageInspectionFiles(t, store.directory)
	base := []string{"--replay-dir", store.directory, "--workspace", "/w", "--call-id", "selected"}
	run := func(args []string) storageInspection {
		var out, stderr bytes.Buffer
		if status := inspectioncli.RunStorageInspection(testHistory(), testReports(), t.Context(), args, &out, &stderr); status != 0 || stderr.Len() != 0 {
			t.Fatalf("inspection: status=%d stderr=%s", status, &stderr)
		}
		var result storageInspection
		if err := json.Unmarshal(out.Bytes(), &result, json.RejectUnknownMembers(true)); err != nil {
			t.Fatal(err)
		}
		return result
	}
	checkOffsets := func(args []string, target, text string) {
		t.Helper()
		offsets := []int{len(text) + 1}
		if at := strings.Index(text, "π"); at >= 0 {
			offsets = append(offsets, at+1)
		}
		for _, offset := range offsets {
			var out, stderr bytes.Buffer
			args := append(slices.Clone(args), "--offset", strconv.Itoa(offset))
			status := inspectioncli.RunStorageInspection(testHistory(), testReports(), t.Context(), args, &out, &stderr)
			if status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), target) || !strings.Contains(stderr.String(), "UTF-8 boundary") {
				t.Fatalf("offset %d for %s: status=%d stdout=%s stderr=%s", offset, target, status, &out, &stderr)
			}
		}
	}
	metadata := run(base)
	expanded, found, err := store.read("/w", "selected", false)
	if err != nil || !found {
		t.Fatalf("read written evidence: found=%t err=%v", found, err)
	}
	want, err := json.Marshal(expanded, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	complete := run(append(slices.Clone(base), "--field", "all", "--text-bytes", "65536"))
	if complete.Text != string(want) || complete.NextOffset != nil {
		t.Fatal("offline inspection changed the expanded retained envelope")
	}
	if metadata.Text != "" || metadata.Dependencies != 4 || metadata.Bytes == 0 {
		t.Fatalf("unexpected default disclosure: %+v", metadata)
	}
	var text strings.Builder
	for offset := 0; ; {
		args := append(append([]string{}, base...), "--field", "baseline", "--text-bytes", "31", "--offset", strconv.Itoa(offset))
		page := run(args)
		if len(page.Text) > 31 || page.Offset != offset || strings.Contains(page.Text, h.Script) || strings.Contains(page.Text, "unrelated private command") {
			t.Fatalf("invalid bounded page: %+v", page)
		}
		text.WriteString(page.Text)
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset {
			t.Fatal("inspection pagination made no progress")
		}
		offset = *page.NextOffset
	}
	var got resolvedStockBaseline
	if err := json.Unmarshal([]byte(text.String()), &got); err != nil || !reflect.DeepEqual(&got, durableHistory(h).ResolvedBaseline) {
		t.Fatalf("paged evidence differs: %v", err)
	}
	checkOffsets(append(slices.Clone(base), "--field", "baseline"), metadata.Target, text.String())
	for _, path := range snapshotTestBlobs(t, store) {
		args := []string{"--replay-dir", store.directory, "--object", filepath.Base(path), "--field", "all", "--text-bytes", "65536"}
		result := run(args)
		if result.Text == "" || result.NextOffset != nil {
			t.Fatal("could not inspect compressed object")
		}
		checkOffsets(args, result.Target, result.Text)
	}
	if after := storageInspectionFiles(t, store.directory); !reflect.DeepEqual(before, after) {
		t.Fatal("read-only inspection changed store contents, modes or timestamps")
	}
}

func TestStorageInspectionPreciseErrorsAndCancellation(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "not-created")
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--replay-dir", dir, "--workspace", "/w", "--call-id", "absent"}, "call absent in workspace /w is unavailable"},
		{[]string{"--replay-dir", dir, "--object", "../outside"}, "invalid snapshot reference"},
	} {
		var out, stderr bytes.Buffer
		if status := inspectioncli.RunStorageInspection(testHistory(), testReports(), t.Context(), test.args, &out, &stderr); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), test.want) {
			t.Fatalf("error diagnosis: status=%d stdout=%s stderr=%s", status, &out, &stderr)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("inspection created an absent store")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, stderr bytes.Buffer
	if status := inspectioncli.RunStorageInspection(testHistory(), testReports(), ctx, []string{"--replay-dir", dir, "--workspace", "/w", "--call-id", "call"}, &out, &stderr); status != 1 || !strings.Contains(stderr.String(), "context canceled") || out.Len() != 0 {
		t.Fatalf("cancellation: status=%d stderr=%s", status, &stderr)
	}
}

func TestStorageInspectionMissingContentNamesExactObject(t *testing.T) {
	t.Parallel()
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := snapshotTestHistory()
	h.ResolvedBaseline.Files[0].Content = strings.Repeat("source", 256)
	snapshotTestPut(t, store, t.Context(), "selected", h)
	data, err := os.ReadFile(filepath.Join(store.directory, legacycall.Name("/w", "selected", false)))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct{ Snapshots *legacycall.Snapshots }
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	name := envelope.Snapshots.Contents[0]
	if err := os.Remove(filepath.Join(store.directory, name)); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	status := inspectioncli.RunStorageInspection(testHistory(), testReports(), t.Context(), []string{"--replay-dir", store.directory, "--workspace", "/w", "--call-id", "selected"}, &out, &stderr)
	if status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "call selected") || !strings.Contains(stderr.String(), name) || !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("missing content diagnosis: status=%d stderr=%s", status, &stderr)
	}
}
