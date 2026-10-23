package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
)

// Late metadata updates canonical labels before detached terminal delivery.
func renameThreadActivity(attached *attachedPresentation, terminal *terminalsession.Attachment, old, name string) {
	attached.presentation.RenameCaller(old, name)
	for _, source := range []*sessionpresentation.ActivitySource{attached.source, attached.childSource} {
		source.RenameCaller(old, name)
	}
	terminalsession.RenameSelection(old, name, terminal.Transcript, terminal.Agents)
}
