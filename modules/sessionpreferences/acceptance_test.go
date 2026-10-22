package sessionpreferences_test

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/sessionhistory"
	"github.com/yusing/mekugi/modules/sessionpreferences"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func newPreferences(t *testing.T) *preferences.Service {
	t.Helper()
	script := new(moduletest.Scripted)
	history := moduletest.New(t, sessionhistory.Module, sessionhistory.Deps{}, module.Slots{}, script)
	return moduletest.New(t, sessionpreferences.Module, sessionpreferences.Deps{History: history}, module.Slots{}, script)
}

func preferencePath(t *testing.T, scope preferences.Scope) string {
	t.Helper()
	key := sha256.Sum256([]byte(scope.Workspace + "\x00" + scope.Thread))
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "mekugi", "ui", fmt.Sprintf("panes-%x.json", key))
}

func writePreference(t *testing.T, scope preferences.Scope, data string) {
	t.Helper()
	path := preferencePath(t, scope)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPreferencesEffectFreeReadsKeepAttachedScope(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	service := newPreferences(t)
	scope := preferences.Scope{Workspace: t.TempDir(), Thread: "main"}
	initial := jsontext.Value(`{"focus":0}`)
	owner, err := service.Attach(scope, "terminal", initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePreferences(owner, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	got, err := service.Read(scope, "terminal")
	if err != nil || !reflect.DeepEqual(got, preferences.Snapshot{}) {
		t.Fatalf("missing scope read=%+v err=%v", got, err)
	}
	if _, err := service.Read(scope, "web"); err == nil {
		t.Fatal("unsupported renderer read accepted")
	}
	if _, err := service.Attach(scope, "web", initial); err == nil {
		t.Fatal("unsupported renderer writer accepted")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("construction/read/unchanged save created state: %v %v", entries, err)
	}
	layout := jsontext.Value(`{"focus":1,"futureTerminalField":{"value":3}}`)
	expected := decodePresentation(t, layout)
	owner.StagePresentation(layout)
	now := time.Now()
	if err := savePreferences(owner, now, false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Read(preferences.Scope{Workspace: scope.Workspace, Thread: "other"}, "terminal"); err != nil {
		t.Fatal(err)
	}
	layout[9] = '2' // Caller-owned bytes must not mutate the pending snapshot.
	if err := savePreferences(owner, now.Add(time.Second), false); err != nil {
		t.Fatal(err)
	}
	restarted := newPreferences(t)
	got, err = restarted.Read(scope, "terminal")
	if err != nil || !reflect.DeepEqual(decodePresentation(t, got.Presentation), expected) || owner.Scope() != scope {
		t.Fatalf("attached target changed: scope=%+v read=%+v err=%v", owner.Scope(), got, err)
	}

}

func decodePresentation(t *testing.T, data jsontext.Value) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPreferencesFlatV1RecoveryAndConfirmation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	scope := preferences.Scope{Workspace: t.TempDir(), Thread: "retained"}
	writePreference(t, scope, `{"version":1,"focus":1,"split":63,"diffOpen":true,"navigatorColumns":22,"futureTerminalField":{"value":3},"settings":{"model":"retained","effort":"","tier":"fast","observed":"2026-10-01T00:00:00Z"}}`)
	service := newPreferences(t)
	state, err := service.Read(scope, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	presentation := decodePresentation(t, state.Presentation)
	if len(presentation) != 5 || presentation["focus"] != float64(1) || presentation["settings"] != nil || presentation["version"] != nil {
		t.Fatalf("flat presentation=%v", presentation)
	}
	baseline, cutoff := state.ResumeBaseline()
	if baseline != (preferences.Evidence{Model: "retained", Tier: "fast", TierKnown: true}) || !cutoff.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("applied baseline=%+v cutoff=%v", baseline, cutoff)
	}
	partial := baseline.WithNewer(preferences.Evidence{Model: "newer", Effort: "high"})
	if partial != (preferences.Evidence{Model: "newer", Effort: "high", Tier: "fast", TierKnown: true}) {
		t.Fatalf("partial history erased tier: %+v", partial)
	}
	explicitDefault := partial.WithNewer(preferences.Evidence{Model: "latest", TierKnown: true})
	if explicitDefault != (preferences.Evidence{Model: "latest", TierKnown: true}) {
		t.Fatalf("explicit host defaults lost: %+v", explicitDefault)
	}
	owner, err := service.Attach(scope, "terminal", state.Presentation)
	if err != nil {
		t.Fatal(err)
	}
	owner.Restore(state)
	before, err := os.Stat(preferencePath(t, scope))
	if err != nil {
		t.Fatal(err)
	}
	if err := savePreferences(owner, cutoff, true); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(preferencePath(t, scope))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("restored baseline rewritten: %v", err)
	}
	checkpoint := cutoff.Add(time.Minute)
	owner.Recover(partial, checkpoint)
	if err := savePreferences(owner, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	restored, err := newPreferences(t).Read(scope, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	selected, watermark := restored.ResumeBaseline()
	if selected != partial || !watermark.Equal(checkpoint) {
		t.Fatalf("fresh recovery=%+v watermark=%v", selected, watermark)
	}
	if !reflect.DeepEqual(presentation, decodePresentation(t, restored.Presentation)) {
		t.Fatal("recovery changed terminal fields")
	}
	confirmed := preferences.Applied{Model: "confirmed", Observed: checkpoint.Add(time.Minute)}
	owner.Apply(confirmed)
	if err := savePreferences(owner, confirmed.Observed, true); err != nil {
		t.Fatal(err)
	}
	restored, err = newPreferences(t).Read(scope, "terminal")
	if err != nil || restored.Settings != confirmed || !restored.ResumeObserved.IsZero() || restored.ResumeEvidence != (preferences.Evidence{}) {
		t.Fatalf("confirmation=%+v err=%v", restored, err)
	}
	selected, watermark = restored.ResumeBaseline()
	if selected != (preferences.Evidence{Model: "confirmed", TierKnown: true}) || watermark != confirmed.Observed {
		t.Fatalf("confirmed baseline=%+v cutoff=%v", selected, watermark)
	}
}

func TestPreferencesAtomicReplacementPreservesLinkReferent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	scope := preferences.Scope{Workspace: t.TempDir(), Thread: "main"}
	service := newPreferences(t)
	path := preferencePath(t, scope)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	referent := filepath.Join(t.TempDir(), "external.json")
	original := `{"version":1,"focus":0}`
	if err := os.WriteFile(referent, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(referent, path); err != nil {
		t.Fatal(err)
	}
	owner, err := service.Attach(scope, "terminal", jsontext.Value(`{"focus":0}`))
	if err != nil {
		t.Fatal(err)
	}
	owner.StagePresentation(jsontext.Value(`{"focus":1}`))
	if err := savePreferences(owner, time.Now(), true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("published record=%v err=%v", info, err)
	}
	data, err := os.ReadFile(referent)
	if err != nil || string(data) != original {
		t.Fatalf("link referent changed: %q %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("publication left artifacts: %v %v", entries, err)
	}
	state, err := newPreferences(t).Read(scope, "terminal")
	if err != nil || decodePresentation(t, state.Presentation)["focus"] != float64(1) {
		t.Fatalf("replacement unreadable: %+v %v", state, err)
	}
}

func TestPreferencesRejectUnavailableRecords(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	service := newPreferences(t)
	scope := preferences.Scope{Workspace: t.TempDir(), Thread: "main"}
	for _, body := range []string{`{"version":1,"settings":{"model":"missing-time"}}`, `{"version":1,"settings":{"observed":"2026-10-01T00:00:00Z"}}`} {
		writePreference(t, scope, body)
		if got, err := service.Read(scope, "terminal"); err == nil || !reflect.DeepEqual(got, preferences.Snapshot{}) {
			t.Fatalf("invalid record accepted: %+v err=%v", got, err)
		}
	}
	directoryScope := preferences.Scope{Workspace: scope.Workspace, Thread: "directory"}
	if err := os.Mkdir(preferencePath(t, directoryScope), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Read(directoryScope, "terminal"); err == nil {
		t.Fatal("directory record accepted")
	}
	for _, bad := range []preferences.Scope{{Workspace: "relative", Thread: "main"}, {Workspace: scope.Workspace}} {
		if _, err := service.Read(bad, "terminal"); err == nil {
			t.Fatalf("invalid scope accepted: %+v", bad)
		}
	}
}

func savePreferences(a *preferences.Attachment, now time.Time, flush bool) error {
	write, err := a.PrepareSave(now, flush)
	if err != nil || write == nil {
		return err
	}
	return write.Publish()
}
