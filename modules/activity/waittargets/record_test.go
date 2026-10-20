package waittargets

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRetainedWaitTargets(t *testing.T) {
	if got := Name("/workspace", "caller", "turn", "item"); got != "wait-88ce154e184f1c06b834178c7fcf9137c8de4ee82c9bd08b218de05f15cffab3.json" {
		t.Fatalf("retained identity changed: %s", got)
	}
	// Existing on-disk array spelling remains readable, including literal HTML.
	wire := `[{"thread":"child","name":"/root/a<&>"},{"thread":"nested","name":"/root/a/b"}]`
	targets := []Target{{Thread: "child", Name: "/root/a<&>"}, {Thread: "nested", Name: "/root/a/b"}}
	data, err := Encode(targets)
	if err != nil || string(data) != wire {
		t.Fatalf("retained encoding changed: %s, %v", data, err)
	}
	path := filepath.Join(t.TempDir(), Name("/workspace", "caller", "turn", "item"))
	if err := os.WriteFile(path, []byte(wire), 0600); err != nil {
		t.Fatal(err)
	}
	got, found, err := Read(path)
	if err != nil || !found || !slices.Equal(got, targets) {
		t.Fatalf("retained snapshot: %+v found=%v err=%v", got, found, err)
	}
	for _, empty := range []string{"[]", "null", "[]" + strings.Repeat(" ", maxBytes-2)} {
		if err := os.WriteFile(path, []byte(empty), 0600); err != nil {
			t.Fatal(err)
		}
		if got, found, err := Read(path); err != nil || !found || len(got) != 0 {
			t.Fatalf("empty snapshot (%d bytes): %+v found=%v err=%v", len(empty), got, found, err)
		}
	}
	data, err = Encode(nil)
	if err != nil || string(data) != "[]" {
		t.Fatalf("empty encoding changed: %s, %v", data, err)
	}
}

func TestWaitTargetsRejectInvalidSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wait.json")
	if _, found, err := Read(path); err != nil || found {
		t.Fatalf("missing snapshot: found=%v err=%v", found, err)
	}
	for _, wire := range []string{
		`[{"thread":"a","name":"b","unknown":true}]`, `[] []`, `not json`, strings.Repeat(" ", maxBytes+1),
	} {
		if err := os.WriteFile(path, []byte(wire), 0600); err != nil {
			t.Fatal(err)
		}
		if got, found, err := Read(path); err == nil || found || got != nil {
			t.Fatalf("invalid snapshot accepted: found=%v err=%v", found, err)
		}
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Dir(path)} {
		if _, found, err := Read(path); err == nil || found {
			t.Fatalf("nonregular snapshot accepted: %s, found=%v err=%v", path, found, err)
		}
	}
	if _, err := Encode([]Target{{Name: strings.Repeat("a", maxBytes)}}); err == nil {
		t.Fatal("oversized snapshot encoded")
	}
}
