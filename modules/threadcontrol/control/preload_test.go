package control

import "testing"

func TestContextPreloadAdmissionAndRestore(t *testing.T) {
	s, session := fixture(t)
	caller := identity("root", "rt")
	caller.Call = "spawn"
	const selection = `{"sources":[{"path":"child.txt"},{"changes":["amber1..3","birch2"]}]}`
	effect, err := s.ReserveSpawn(t.Context(), caller, SpawnRequest{Name: "worker", Message: "task", Settings: Settings{Workspace: t.TempDir()}, Preload: selection})
	must(t, err)
	for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
		_, err = s.ClaimEffect(t.Context(), "root", effect.ID)
		must(t, err)
		must(t, s.EffectAcknowledged(t.Context(), "root", effect.ID, Receipt{Phase: phase, Thread: "worker", Session: "worker-session", Turn: "wt"}))
	}
	child(t, s, "worker", "nested", "/root/worker/nested", "nt")
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	for _, service := range []*Service{s, restored} {
		for _, thread := range []string{"worker", "nested"} {
			preloads, err := service.ContextPreloads(thread, thread+"-session")
			must(t, err)
			if len(preloads) != 1 || preloads[0].Selection != selection || preloads[0].Item != MessageItemID("root:spawn:assignment") {
				t.Fatalf("lost prepared selection: %+v", preloads)
			}
		}
		if _, err := service.ContextPreloads("worker", "foreign-session"); err == nil {
			t.Fatal("foreign runtime accepted")
		}
		preloads, err := service.ContextPreloads("root", "root-session")
		must(t, err)
		if len(preloads) != 0 {
			t.Fatal("child preload leaked into parent")
		}
	}
}
