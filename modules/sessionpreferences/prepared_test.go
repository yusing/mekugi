package sessionpreferences_test

import (
	"encoding/json/jsontext"
	"os"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestPreparedPreferenceWriteRetainsItsSnapshot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	service := newPreferences(t)
	scope := preferences.Scope{Workspace: t.TempDir(), Thread: "main"}
	owner, err := service.Attach(scope, "terminal", jsontext.Value(`{"focus":0}`))
	if err != nil {
		t.Fatal(err)
	}
	owner.StagePresentation(jsontext.Value(`{"focus":1}`))
	write, err := owner.PrepareSave(time.Now(), true)
	if err != nil || write == nil {
		t.Fatalf("prepare: %v, %v", write, err)
	}
	if _, err := os.Stat(preferencePath(t, scope)); !os.IsNotExist(err) {
		t.Fatalf("prepare performed storage I/O: %v", err)
	}
	owner.StagePresentation(jsontext.Value(`{"focus":2}`))
	snapshot := owner.Snapshot()
	snapshot.Presentation[9] = '3'
	if err := write.Publish(); err != nil {
		t.Fatal(err)
	}
	saved, err := service.Read(scope, "terminal")
	if err != nil || string(saved.Presentation) != `{"focus":1}` {
		t.Fatalf("prepared write changed: %s, %v", saved.Presentation, err)
	}
	if err := savePreferences(owner, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	saved, err = service.Read(scope, "terminal")
	if err != nil || string(saved.Presentation) != `{"focus":2}` {
		t.Fatalf("later stage or detached snapshot changed: %s, %v", saved.Presentation, err)
	}
	if write, err := owner.PrepareSave(time.Now(), true); err != nil || write != nil {
		t.Fatalf("unchanged state prepared again: %v, %v", write, err)
	}
}
