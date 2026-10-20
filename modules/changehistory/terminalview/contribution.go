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
		Attach: terminalui.BindPane((*capture.Service).ReviewView,
			func(theme diffrender.Theme) *Pane { return NewPane("", theme) }, (*Pane).publishReview),
	}},
}

func (c *Pane) publishReview(view presentation.ReviewView) {
	if view.Revision == c.publicationRevision {
		return
	}
	c.publicationRevision, c.failure = view.Revision, view.Failure
	c.SetWorkspace(view.Workspace)
	c.Update(view.Publication, view.Coverage)
}
