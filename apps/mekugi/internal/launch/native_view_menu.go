package launch

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
)

// Menu identity survives unrelated output and status publications. Recheck its
// current semantic destination before using the shared presentation admission.
func (v *nativeSessionView) menuAction(request sessionapi.NativeRequest) (sessionpresentation.MenuAction, error) {
	action := *request.Operation.Menu
	current := v.attached.presentation.View()
	if action.Version != request.Version || current.Menu == nil || !v.picker.Open || action.Menu != v.picker.ID {
		return action, sessionpresentation.ErrStaleIntent
	}
	if current.Menu.Kind == sessionpresentation.MenuCompletion {
		editor := request.Operation.Editor
		if editor == nil || editor.Target != v.completionTarget || v.picker.FindTarget(editor.Draft) != v.picker.Target {
			return action, sessionpresentation.ErrStaleIntent
		}
	}
	return action, v.attached.presentation.AdmitMenu(action)
}

func (v *nativeSessionView) panelAction(request sessionapi.NativeRequest) (sessionpresentation.PanelAction, error) {
	action := *request.Operation.Panel
	if action.Version != request.Version {
		return action, sessionpresentation.ErrStaleIntent
	}
	return action, v.attached.presentation.AdmitPanel(action)
}
