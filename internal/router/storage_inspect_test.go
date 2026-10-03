package router

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

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
	fingerprint := func() map[string]string {
		entries, err := os.ReadDir(store.directory)
		if err != nil {
			t.Fatal(err)
		}
		files := make(map[string]string)
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(store.directory, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			files[entry.Name()] = fmt.Sprintf("%s %s %s", info.Mode(), info.ModTime(), data)
		}
		return files
	}
	before := fingerprint()
	base := []string{"--replay-dir", store.directory, "--workspace", "/w", "--call-id", "selected"}
	run := func(args []string) storageInspection {
		var out, stderr bytes.Buffer
		if status := RunStorageInspection(t.Context(), args, &out, &stderr); status != 0 || stderr.Len() != 0 {
			t.Fatalf("inspection: status=%d stderr=%s", status, &stderr)
		}
		var result storageInspection
		if err := json.Unmarshal(out.Bytes(), &result, json.RejectUnknownMembers(true)); err != nil {
			t.Fatal(err)
		}
		return result
	}
	metadata := run(base)
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
	for _, path := range snapshotTestBlobs(t, store) {
		result := run([]string{"--replay-dir", store.directory, "--object", filepath.Base(path), "--field", "all", "--text-bytes", "65536"})
		if result.Text == "" || result.NextOffset != nil {
			t.Fatal("could not inspect compressed object")
		}
	}
	if after := fingerprint(); !reflect.DeepEqual(before, after) {
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
		if status := RunStorageInspection(t.Context(), test.args, &out, &stderr); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), test.want) {
			t.Fatalf("error diagnosis: status=%d stdout=%s stderr=%s", status, &out, &stderr)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("inspection created an absent store")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, stderr bytes.Buffer
	if status := RunStorageInspection(ctx, []string{"--replay-dir", dir, "--workspace", "/w", "--call-id", "call"}, &out, &stderr); status != 1 || !strings.Contains(stderr.String(), "context canceled") || out.Len() != 0 {
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
	data, err := os.ReadFile(filepath.Join(store.directory, replayRecordName("/w", "selected", false)))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct{ Snapshots *replaySnapshots }
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	name := envelope.Snapshots.Contents[0]
	if err := os.Remove(filepath.Join(store.directory, name)); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	status := RunStorageInspection(t.Context(), []string{"--replay-dir", store.directory, "--workspace", "/w", "--call-id", "selected"}, &out, &stderr)
	if status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "call selected") || !strings.Contains(stderr.String(), name) || !strings.Contains(stderr.String(), "unavailable") {
		t.Fatalf("missing content diagnosis: status=%d stderr=%s", status, &stderr)
	}
}
