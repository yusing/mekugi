package router

import (
	"bytes"
	"crypto/sha256"
	jsonv1 "encoding/json"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	inspectioncli "github.com/yusing/mekugi/apps/mekugi/internal/inspectioncli"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
)

func TestCommandSegmentsStorageInspection(t *testing.T) {
	t.Parallel()
	for _, shape := range []string{"old", "new", "both", "corrupt-old", "corrupt-new", "conflicting"} {
		t.Run(shape, func(t *testing.T) {
			directory, workspace := t.TempDir(), t.TempDir()
			id := reports.ID("inspection-turn", "inspection-item")
			oldName, newName := legacycall.Name(workspace, id, false), reports.Name(workspace, id)
			wire := commandSegmentsCompatEnvelope(workspace, id)
			var fixture struct {
				History struct{ CommandSegments reports.Report }
			}
			if err := json.Unmarshal([]byte(wire), &fixture, jsonv1.DefaultOptionsV1()); err != nil {
				t.Fatal(err)
			}
			service := testReports()
			write := func(name, data string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Publish before adding the historical neighbor so a genuine new
			// envelope exists even when historical evidence would suppress a write.
			if shape != "old" {
				if err := service.Publish(t.Context(), reports.Scope{Store: testHistory().Attach(directory), Workspace: workspace}, id, fixture.History.CommandSegments); err != nil {
					t.Fatal(err)
				}
			}
			if shape != "new" {
				write(oldName, wire)
			}
			switch shape {
			case "corrupt-old":
				write(oldName, "{")
			case "corrupt-new":
				write(newName, "{")
			case "conflicting":
				write(oldName, strings.Replace(wire, `"Exit":1`, `"Exit":2`, 1))
			}
			before := storageInspectionFiles(t, directory)
			base := []string{"--replay-dir", directory, "--workspace", workspace, "--call-id", id}
			if shape == "corrupt-old" || shape == "corrupt-new" || shape == "conflicting" {
				var out, stderr bytes.Buffer
				status := inspectioncli.RunStorageInspection(testHistory(), service, t.Context(), base, &out, &stderr)
				if status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "call "+id+":") {
					t.Fatalf("invalid neighboring evidence: status=%d stdout=%s stderr=%s", status, &out, &stderr)
				}
			} else {
				target := newName
				var expected []byte
				var err error
				if shape == "old" {
					target = oldName
					var historical replayRecord
					if err := json.Unmarshal([]byte(wire), &historical, jsonv1.DefaultOptionsV1()); err != nil {
						t.Fatal(err)
					}
					expected, err = json.Marshal(historical, json.Deterministic(true))
				} else {
					expected, err = json.Marshal(struct {
						Version   int
						Workspace string
						CallID    string
						Report    reports.Report
					}{1, workspace, id, fixture.History.CommandSegments}, json.Deterministic(true))
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"", "all", "baseline", "exec", "patches", "review", "refs"} {
					args := append([]string{}, base...)
					selected := expected
					if field != "" {
						args = append(args, "--field", field, "--text-bytes", "65536")
					}
					switch field {
					case "baseline", "exec":
						selected = []byte("null")
					case "patches", "review", "refs":
						selected = []byte("[]")
					}
					var out, stderr bytes.Buffer
					if status := inspectioncli.RunStorageInspection(testHistory(), service, t.Context(), args, &out, &stderr); status != 0 || stderr.Len() != 0 {
						t.Fatalf("field %q: status=%d stderr=%s", field, status, &stderr)
					}
					var got storageInspection
					if err := json.Unmarshal(out.Bytes(), &got, json.RejectUnknownMembers(true)); err != nil {
						t.Fatal(err)
					}
					want := storageInspection{Schema: "mekugi.storage.v1", Target: target, Bytes: len(selected), SHA256: fmt.Sprintf("%x", sha256.Sum256(selected)), Field: field}
					if field != "" {
						want.Text = string(selected)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("field %q: got %+v, want %+v", field, got, want)
					}
				}
			}
			if after := storageInspectionFiles(t, directory); !reflect.DeepEqual(before, after) {
				t.Fatal("inspection changed storage contents, modes, timestamps, or coordination files")
			}
		})
	}
}

// Include directories so creating then removing a lock is still observable.
func storageInspectionFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if !entry.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		files[path] = fmt.Sprintf("%s %s %x", info.Mode(), info.ModTime(), data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCommandSegmentsInspectionRelativeReplayDirectory(t *testing.T) {
	directory, workspace := t.TempDir(), t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, directory)
	if err != nil {
		t.Fatal(err)
	}
	service := testReports()
	oldID, newID := reports.ID("turn", "old"), reports.ID("turn", "new")
	if err := os.WriteFile(filepath.Join(directory, legacycall.Name(workspace, oldID, false)), []byte(commandSegmentsCompatEnvelope(workspace, oldID)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.Publish(t.Context(), reports.Scope{Store: testHistory().Attach(directory), Workspace: workspace}, newID, reports.Report{Command: "true", Parts: []reports.Part{{Source: "true"}}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldID, newID} {
		var absolute bytes.Buffer
		for _, path := range []string{directory, relative} {
			var out, stderr bytes.Buffer
			args := []string{"--replay-dir", path, "--workspace", workspace, "--call-id", id, "--field", "all", "--text-bytes", "65536"}
			if status := inspectioncli.RunStorageInspection(testHistory(), service, t.Context(), args, &out, &stderr); status != 0 {
				t.Fatalf("%s: status=%d stderr=%s", path, status, &stderr)
			}
			if path == directory {
				absolute.Write(out.Bytes())
			} else if !bytes.Equal(absolute.Bytes(), out.Bytes()) {
				t.Fatal("relative directory changed selected evidence")
			}
		}
	}
}
