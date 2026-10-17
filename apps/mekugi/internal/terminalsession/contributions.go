package terminalsession

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
)

// Views is the validated, statically selected terminal contribution set.
type Views struct {
	auxiliary *terminalui.PaneFactory[*scope.Store]
}

func NewViews(auxiliary []terminalui.PaneFactory[*scope.Store]) (Views, error) {
	if len(auxiliary) == 0 {
		return Views{}, nil
	}
	if len(auxiliary) > 1 {
		return Views{}, fmt.Errorf("terminal auxiliary placement has %d contributions; expected at most one", len(auxiliary))
	}
	factory := auxiliary[0]
	if strings.TrimSpace(factory.Name) == "" || strings.TrimSpace(factory.Label) == "" || factory.Attach == nil {
		return Views{}, fmt.Errorf("terminal auxiliary contribution %q requires a name, label and constructor", factory.Name)
	}
	return Views{auxiliary: &factory}, nil
}

// Attach returns separate application preparation and renderer interaction.
func (v Views) Attach(theme diffrender.Theme) (label string, attachment terminalui.PaneAttachment[*scope.Store]) {
	if v.auxiliary == nil {
		return "", terminalui.PaneAttachment[*scope.Store]{}
	}
	return v.auxiliary.Label, v.auxiliary.Attach(theme)
}
