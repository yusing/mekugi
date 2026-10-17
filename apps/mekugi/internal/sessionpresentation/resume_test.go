package sessionpresentation_test

import (
	"errors"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"testing"
)

func TestResumePublicationRejectsStaleAndUnpublishedTargets(t *testing.T) {
	c := newStatusConsumer()
	m := c.model()
	source := &sessionpresentation.ResumeView{ID: 1, Rows: []sessionpresentation.ResumeRow{{ID: "saved"}}, CanPage: true, PageAfter: "saved"}
	publish := func() sessionpresentation.View {
		return m.Publish(sessionpresentation.MainInputs{Control: &c.control, Feedback: &c.feedback, Conversation: &c.conversation, Resume: source}, sessionpresentation.Caption{}, c.now)
	}
	v := publish()
	if publish().Version != v.Version {
		t.Fatal("unchanged listing advanced the publication")
	}
	v.Resume.Rows[0].ID = "reader mutation"
	source.Rows[0].Title = "owner mutation"
	if m.View().Resume.Rows[0] != (sessionpresentation.ResumeRow{ID: "saved"}) {
		t.Fatal("listing aliases mutable rows")
	}
	action := sessionpresentation.ResumeAction{Version: v.Version, Picker: 1, Kind: sessionpresentation.ResumeChoose, Thread: "saved"}
	if err := m.AdmitResume(action); err != nil {
		t.Fatal(err)
	}
	action.Thread = "unpublished"
	if !errors.Is(m.AdmitResume(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("unpublished target admitted")
	}
	action.Kind, action.After = sessionpresentation.ResumePage, "old"
	if !errors.Is(m.AdmitResume(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("unpublished page target admitted")
	}
	action.After = "saved"
	if err := m.AdmitResume(action); err != nil {
		t.Fatal(err)
	}
	source.ID++
	publish()
	if !errors.Is(m.AdmitResume(action), sessionpresentation.ErrStaleIntent) {
		t.Fatal("old chooser action admitted")
	}
	m.Attach(sessionpresentation.Thread{ID: "main", Caller: "/root"}, true)
	action.Version = publish().Version
	action.Picker, action.Kind, action.Thread = source.ID, sessionpresentation.ResumeChoose, "saved"
	if !errors.Is(m.AdmitResume(action), sessionpresentation.ErrReplayIntent) {
		t.Fatal("replay admitted a session switch")
	}
}
