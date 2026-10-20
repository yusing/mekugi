package composer

import "testing"

func TestEditorRevisionSurvivesRestoration(t *testing.T) {
	var e Editor
	check := func(change func()) {
		t.Helper()
		before := e.Revision()
		change()
		if e.Revision() <= before {
			t.Fatal("editor mutation reused a revision")
		}
	}
	check(func() { e.Insert("a") })
	check(func() { e.Insert("b") }) // One undo group still has distinct revisions.
	parked := e.Park()
	check(func() { e.Undo(false) })
	check(func() { e.Undo(true) })
	check(func() { e.Take() })
	check(func() { e.Resume(parked) })
	check(func() { e.Restore(Draft{Text: "before"}) })
	check(func() { e.Replace(e.Draft) })
	check(func() {
		if err := e.ApplyExternal("changed"); err != nil {
			t.Fatal(err)
		}
	})
	check(func() {
		if _, err := e.Apply(Action{Kind: DeleteAction, End: len(e.Text)}); err != nil {
			t.Fatal(err)
		}
	})
}
