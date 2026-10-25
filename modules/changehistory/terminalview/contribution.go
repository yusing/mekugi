package terminalview

import (
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

var Views = terminalui.Definitions[*capture.Service, *scope.Store]{
	Auxiliary: []terminalui.PaneDefinition[*capture.Service, *scope.Store]{{
		Name: "saved-review", Label: "Diff",
		Attach: terminalui.BindPane((*capture.Service).DiffReviewView,
			func(theme diffrender.Theme) *Pane { return NewPane("", theme) }, (*Pane).PublishReview),
	}},
}

// PublishReview applies the detached owner publication without opening its store.
func (c *Pane) PublishReview(view presentation.ReviewView) {
	if view.Revision == c.publicationRevision && view.Failure == c.failure && view.Workspace == c.workspace {
		return
	}
	if view.Workspace != c.workspace {
		c.Reset()
		c.SetWorkspace(view.Workspace)
	}
	c.publicationRevision, c.failure = view.Revision, view.Failure
	if view.Failure != "" {
		// A transport-capacity projection hides the pane, not its selection.
		c.dirty = true
		return
	}
	c.Update(view.Publication, view.Coverage)
}
