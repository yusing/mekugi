package composition

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/changehistory"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	changeview "github.com/yusing/mekugi/modules/changehistory/terminalview"
	"github.com/yusing/mekugi/modules/sessionhistory"
)

func TestCompositionTerminalPublicationAndScopeReplacement(t *testing.T) {
	var c Composition
	history := c.Add(sessionhistory.Module, func() sessionhistory.Deps { return sessionhistory.Deps{} })
	changes := c.Add(changehistory.Module, func() changehistory.Deps { return changehistory.Deps{History: history.Value()} })
	runtime, err := c.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	label, attachment := runtime.Terminal.Attach(diffrender.TerminalTheme)
	if label != "Diff" || attachment.Pane == nil || attachment.Prepare == nil {
		t.Fatalf("missing declared view: %q %+v", label, attachment)
	}
	first, second := new(scope.Store), new(scope.Store)
	source := changes.Value()
	source.PublishReview(first, new(presentation.Snapshot), "/first", "READY", "")
	attachment.Prepare(first)
	if err := attachment.Pane.Paint(t.Context(), io.Discard, 80, 20); err != nil {
		t.Fatal(err)
	}
	editor := attachment.Pane.(terminalui.PaneEditor)
	if editor.Workspace() != "/first" {
		t.Fatal("first publication missing")
	}
	// Publishing does not mutate a renderer until application preparation.
	source.PublishReview(second, new(presentation.Snapshot), "/second", "RECONNECTING", "")
	if editor.Workspace() != "/first" {
		t.Fatal("source mutated the renderer")
	}
	attachment.Prepare(second)
	_, status := attachment.Pane.Title()
	if editor.Workspace() != "/second" || !strings.Contains(status, "RECONNECTING") {
		t.Fatalf("replacement scope lost: %q %q", editor.Workspace(), status)
	}
	attachment.Pane.Reset()
	attachment.Prepare(second)
	if editor.Workspace() != "/second" {
		t.Fatal("reset did not republish the current scope")
	}
	source.PublishReview(second, new(presentation.Snapshot), "/second", "READY", "retained read failed")
	attachment.Prepare(second)
	var painted bytes.Buffer
	if err := attachment.Pane.Paint(t.Context(), &painted, 80, 20); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(painted.String(), "Diff unavailable") || !strings.Contains(painted.String(), "Use mchanges") {
		t.Fatalf("feature failure was not rendered: %q", painted.String())
	}
	if source := attachment.Pane.(terminalui.PaneSelection).Selection(1, 1); len(source.Painted) != 0 {
		t.Fatal("unavailable evidence remained selectable")
	}
	// The generated reference observes declarations without constructing another view.
	if !strings.Contains(strings.Join(changehistory.Module.Reference().Contributes, ","), "terminal-auxiliary") {
		t.Fatal("reference omitted the selected presentation family")
	}
}

func TestCompositionTerminalAcceptsAnotherTypedSource(t *testing.T) {
	var c Composition
	c.Add(module.Descriptor[struct{}, *testService, module.NoBindings, terminalui.Definitions[*testService, *scope.Store]]{
		ID: "alternate", Lifetime: module.Stateless,
		New: func(struct{}, module.Slots) (*testService, error) { return &testService{name: "/alternate"}, nil },
		Views: terminalui.Definitions[*testService, *scope.Store]{Auxiliary: []terminalui.PaneDefinition[*testService, *scope.Store]{{
			Name: "alternate", Label: "Review",
			Attach: terminalui.BindPane(func(source *testService, _ *scope.Store) string { return source.name },
				func(theme diffrender.Theme) *changeview.Pane { return changeview.NewPane("", theme) }, (*changeview.Pane).SetWorkspace),
		}}},
	}, emptyDeps)
	runtime, err := c.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	label, attachment := runtime.Terminal.Attach(diffrender.TerminalTheme)
	attachment.Prepare(new(scope.Store))
	if label != "Review" || attachment.Pane.(terminalui.PaneEditor).Workspace() != "/alternate" {
		t.Fatal("the shared placement failed to bind a different source type")
	}
}

func TestCompositionTerminalPlacementValidation(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		var c Composition
		d := module.Descriptor[struct{}, *testService, module.NoBindings, terminalui.Definitions[*testService, *scope.Store]]{
			ID: "views", Lifetime: module.Stateless,
			New: func(struct{}, module.Slots) (*testService, error) {
				if count != 0 {
					t.Fatal("invalid declaration reached construction")
				}
				return new(testService), nil
			},
		}
		for range count {
			d.Views.Auxiliary = append(d.Views.Auxiliary, terminalui.PaneDefinition[*testService, *scope.Store]{Name: "test", Label: "Test"})
		}
		c.Add(d, emptyDeps)
		runtime, err := c.Activate(t.Context(), new(binding.Registry))
		if count == 0 {
			if err != nil {
				t.Fatal(err)
			}
			label, attachment := runtime.Terminal.Attach(diffrender.TerminalTheme)
			if label != "" || attachment.Pane != nil || attachment.Prepare != nil {
				t.Fatal("empty composition invented a pane")
			}
			if err := runtime.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatalf("accepted %d invalid contributions", count)
		}
	}
}
