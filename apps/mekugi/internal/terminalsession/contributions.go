package terminalsession

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/libraries/terminalui"
	activityview "github.com/yusing/mekugi/modules/activity/terminalview"
)

// Views is the validated, statically selected terminal contribution set.
type Views struct {
	auxiliary *terminalui.PaneFactory[*scope.Store]
	roster    *terminalui.RosterDefinition[*activityview.View]
}

func NewViews(auxiliary []terminalui.PaneFactory[*scope.Store], roster []terminalui.RosterDefinition[*activityview.View]) (Views, error) {
	var views Views
	if len(auxiliary) > 1 {
		return Views{}, fmt.Errorf("terminal auxiliary placement has %d contributions; expected at most one", len(auxiliary))
	}
	if len(auxiliary) == 1 {
		factory := auxiliary[0]
		if strings.TrimSpace(factory.Name) == "" || strings.TrimSpace(factory.Label) == "" || factory.Attach == nil {
			return Views{}, fmt.Errorf("terminal auxiliary contribution %q requires a name, label and constructor", factory.Name)
		}
		views.auxiliary = &factory
	}
	if len(roster) > 1 {
		return Views{}, fmt.Errorf("terminal roster placement has %d contributions; expected at most one", len(roster))
	}
	if len(roster) == 1 {
		factory := roster[0]
		if strings.TrimSpace(factory.Name) == "" || strings.TrimSpace(factory.Label) == "" || factory.New == nil {
			return Views{}, fmt.Errorf("terminal roster contribution %q requires a name, label and constructor", factory.Name)
		}
		views.roster = &factory
	}
	return views, nil
}

func (v Views) RosterLabel() string {
	if v.roster == nil {
		return ""
	}
	return v.roster.Label
}

// NewThreadView keeps the shared child Conversation painter available when no
// Activity roster is selected. This creates renderer state, never a source service.
func (v Views) NewThreadView() *activityview.View {
	if v.roster == nil {
		return activityview.NewView()
	}
	return v.roster.New()
}

// Attach returns separate application preparation and renderer interaction.
func (v Views) Attach(theme diffrender.Theme) (label string, attachment terminalui.PaneAttachment[*scope.Store]) {
	if v.auxiliary == nil {
		return "", terminalui.PaneAttachment[*scope.Store]{}
	}
	return v.auxiliary.Label, v.auxiliary.Attach(theme)
}

// NewAttachment constructs the actual terminal independently of source scopes.
// The returned preparation belongs to the application, never the renderer.
func (v Views) NewAttachment(theme diffrender.Theme) (*Attachment, func(*scope.Store)) {
	u := &Attachment{
		Main: new(Main), Prompts: new(Prompts),
		Transcript: activityview.NewView(), Agents: v.NewThreadView(), Diff: new(DiffScreen),
	}
	u.Transcript.Conversation = true
	u.Agents.ChildrenOnly, u.Agents.Bare = true, true
	u.RosterLabel, u.Theme = v.RosterLabel(), theme
	label, attachment := v.Attach(theme)
	u.AuxiliaryLabel, u.Auxiliary = label, attachment.Pane
	return u, attachment.Prepare
}
