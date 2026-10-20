package sessionpresentation_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

func TestNonTerminalPanelPublicationAndAdmission(t *testing.T) {
	c := newStatusConsumer()
	m := c.model()
	source := m.OpenPanel("")
	source.Fields = []sessionpresentation.StatusField{{Group: "Usage", Remaining: new(50.0)}}
	source.Metrics = new(transportcapture.MetricsSnapshot)
	publish := func() sessionpresentation.View {
		return m.Publish(sessionpresentation.MainInputs{Control: &c.control, Feedback: &c.feedback, Conversation: &c.conversation}, sessionpresentation.Caption{}, c.now)
	}
	v := publish()
	want := m.View()
	if publish().Version != v.Version {
		t.Fatal("unchanged panel advanced the publication")
	}
	v.Panel.Fields[0].Group = "mutated"
	*v.Panel.Fields[0].Remaining = 0
	v.Panel.Metrics.Schema = "changed by reader"
	source.Fields[0].Group = "owner changed"
	source.Metrics.Schema = "changed by owner"
	if !reflect.DeepEqual(m.View(), want) {
		t.Fatal("panel publication aliases an owner or reader")
	}
	action := sessionpresentation.PanelAction{Version: want.Version, Panel: 1, Kind: sessionpresentation.PanelClose}
	if err := m.AdmitPanel(action); err != nil {
		t.Fatal(err)
	}
	m.OpenPanel("")
	v = publish()
	if !errors.Is(m.AdmitPanel(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("replacement admitted an old close")
	}
	action.Version = v.Version
	if !errors.Is(m.AdmitPanel(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("wrong panel ID admitted")
	}
	m.ClosePanel()
	publish()
	if m.View().Panel != nil {
		t.Fatal("closed panel remains published")
	}
}
