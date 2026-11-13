package compact

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestPublicSelectionsRejectObjectsButHistoryRemainsReadable(t *testing.T) {
	for _, selection := range []string{
		`{"sources":[{"path":"guide.md","ranges":[[2,4]]}]}`,
		`{"sources":[{"skill":"guide/references/file.md","path":"/native/SKILL.md"}]}`,
		`{"sources":[{"changes":["amber1..3"]}]}`,
	} {
		if _, err := Sources(selection); err == nil {
			t.Fatalf("public object source accepted: %s", selection)
		}
		if _, err := (Readers{}).Capture(t.Context(), command.Request{Args: []string{selection}}); err == nil {
			t.Fatalf("mreload accepted object source: %s", selection)
		}
		if sources, err := StoredSources(selection); err != nil || len(sources) != 1 {
			t.Fatalf("historical identity lost: %+v, %v", sources, err)
		}
	}
	for _, selection := range []string{`{"sources":null}`, `{"sources":[12]}`, `{"sources":["@file",{"path":"file"}]}`, `{"sources":[],"path":"file"}`} {
		if _, err := Sources(selection); err == nil {
			t.Fatalf("invalid envelope accepted: %s", selection)
		}
	}
	if err := CompletionSources(`{"sources":["mchanges:amber1..3,birch2"]}`); err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{`{"sources":["@file"]}`, `{"sources":["$skill"]}`, `{"sources":[{"changes":["amber1"]}]}`} {
		if err := CompletionSources(selection); err == nil {
			t.Fatalf("completion admitted a non-change reference string: %s", selection)
		}
	}
}

func TestReferenceSelectionRangesAndIndependentMalformedSource(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	name := "file $(touch unwanted) 'quoted'.md"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("unselected\nselected\nalso unselected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selection := Selection([]SourceIdentity{{Path: name, Ranges: [][2]int{{2, 2}}}})
	selection = strings.TrimSuffix(selection, "]}") + `,"@bad#L4-L2","$bad/../private"]}`
	snapshot, err := (Readers{}).Read(t.Context(), selection, dir, os.Environ())
	if err != nil || len(snapshot.Sources) != 3 {
		t.Fatalf("capture: %+v %v", snapshot, err)
	}
	if got := snapshot.Sources[0]; got.Stdout != "selected\n" || got.ExitCode != 0 {
		t.Fatalf("selected range: %+v", got)
	}
	for _, result := range snapshot.Sources[1:] {
		if result.ExitCode == 0 || !strings.Contains(result.Text(), "invalid source reference") {
			t.Fatalf("malformed reference lost individual diagnostic: %+v", result)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "unwanted")); !os.IsNotExist(err) {
		t.Fatal("reference executed as shell source")
	}
}

func TestNativeReferenceCatalogIsLazySharedAndFrozen(t *testing.T) {
	testMcat(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(dir, "SKILL.md")
	for path, body := range map[string]string{native: "native body\n", filepath.Join(dir, "references", "guide.md"): "skip\nreference body\n", filepath.Join(dir, "file"): "file body\n"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	reader := Readers{Skills: func(context.Context) (map[string][]string, error) {
		calls.Add(1)
		return map[string][]string{"native": {native}, "ambiguous": {native, filepath.Join(dir, "other")}}, errors.New("unrelated catalog entry failed")
	}}
	files, err := reader.Read(t.Context(), `{"sources":["@file"]}`, dir, os.Environ())
	if err != nil || files.Sources[0].Stdout != "file body\n" || calls.Load() != 0 {
		t.Fatalf("file-only read queried native skills: %+v %v %d", files, err, calls.Load())
	}
	snapshot, err := reader.Read(t.Context(), `{"sources":["$native","$native/references/guide.md#L2-L2","$ambiguous","$unavailable","@file"]}`, dir, os.Environ())
	if err != nil || calls.Load() != 1 {
		t.Fatalf("catalog reuse: %v %d", err, calls.Load())
	}
	if snapshot.Sources[0].Stdout != "native body\n" || snapshot.Sources[1].Stdout != "reference body\n" || snapshot.Sources[4].Stdout != "file body\n" {
		t.Fatalf("native references or independent file lost: %+v", snapshot)
	}
	for i, want := range map[int]string{2: "ambiguous native skill", 3: "native skill identity unavailable"} {
		if snapshot.Sources[i].ExitCode == 0 || !strings.Contains(snapshot.Sources[i].Text(), want) {
			t.Fatalf("native resolution failure: %+v", snapshot.Sources[i])
		}
	}
	if err := os.WriteFile(native, []byte("changed after capture"), 0600); err != nil {
		t.Fatal(err)
	}
	identities, err := snapshot.SelectedSources()
	if err != nil || identities[0].Path != native || identities[1].Path != native || snapshot.Sources[0].Stdout != "native body\n" {
		t.Fatalf("native snapshot identity/bytes changed: %+v %v", identities, err)
	}
}

func TestNativeReferenceReadsUseReceivingRunner(t *testing.T) {
	var mu sync.Mutex
	var commands []string
	reader := Readers{
		Skills: func(context.Context) (map[string][]string, error) {
			return map[string][]string{"native": {"/native/SKILL.md"}}, nil
		},
		Run: func(_ context.Context, script string, _ int) (CapturedSource, error) {
			mu.Lock()
			commands = append(commands, script)
			mu.Unlock()
			if strings.Contains(script, "/native/SKILL.md") {
				return CapturedSource{ExitCode: 1, Stderr: "permission denied"}, nil
			}
			return CapturedSource{Stdout: "allowed file"}, nil
		},
	}
	snapshot, err := reader.Read(t.Context(), `{"sources":["$native","@allowed"]}`, t.TempDir(), nil)
	if err != nil || len(commands) != 2 || snapshot.Sources[0].ExitCode != 1 || snapshot.Sources[1].Stdout != "allowed file" {
		t.Fatalf("recipient admission changed: %+v %q %v", snapshot, commands, err)
	}
}
