package sessionapi

import (
	"strings"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestReadConflictsDistinguishSupersededSources(t *testing.T) {
	s := NewLive()
	view := changes.ReviewView{Revision: 1, Workspace: "/project"}
	frame := Frame{Session: Session{Thread: "thread"}, Review: &view,
		Main:   &activity.Publication{Entries: []activity.Entry{{Seq: 1, Agent: "Main", Kind: "text", Text: strings.Repeat("body ", 1000), Revision: 1}}},
		Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/worker"}}}}},
	}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind     string
		revision uint64
	}{
		{"main", s.main.history}, {"agents", s.agents.history}, {"roster", s.rosterRevision}, {"review", view.Revision}, {"live", s.contentVersions["live"]},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			cursor := PageCursor{Attachment: s.readAttachment, Kind: tc.kind, Revision: tc.revision}
			if _, failure := s.page(testJSON(t, cursor)); failure != nil {
				t.Fatalf("current page: %v", failure)
			}
			cursor.Before = ^uint64(0)
			if _, failure := s.page(testJSON(t, cursor)); failure == nil || failure.Code == RPCReadSuperseded {
				t.Fatalf("invalid current boundary suppressed: %+v", failure)
			}
			cursor.Before = 0
			cursor.Attachment = "replaced"
			if _, failure := s.page(testJSON(t, cursor)); failure == nil || failure.Code != RPCReadSuperseded || failure.Data.Status != 409 {
				t.Fatalf("source replacement not classified: %+v", failure)
			}
		})
	}
	for _, ref := range []ContentReference{
		{Attachment: s.readAttachment, Kind: "main", Seq: 999, Revision: s.main.entries[1].revision},
		{Attachment: s.readAttachment, Kind: "roster", Seq: 999, Revision: s.rosterRevision},
		{Attachment: s.readAttachment, Kind: "review", Key: "missing", Revision: view.Revision},
		{Attachment: s.readAttachment, Kind: "live", Key: "missing", Revision: s.contentVersions["live"]},
		{Attachment: "replaced", Kind: "unsupported", Seq: 1, Revision: 1},
	} {
		if _, failure := s.content(testJSON(t, ref)); failure == nil || failure.Code == RPCReadSuperseded {
			t.Fatalf("invalid current reference suppressed: kind=%s failure=%+v", ref.Kind, failure)
		}
	}
	if _, failure := s.page(testJSON(t, PageCursor{Kind: "unsupported", Attachment: "replaced"})); failure == nil || failure.Code == RPCReadSuperseded {
		t.Fatalf("unknown history kind suppressed: %+v", failure)
	}
	ref := ContentReference{Attachment: s.readAttachment, Kind: "main", Seq: 1, Revision: s.main.entries[1].revision}
	frame.Main = &activity.Publication{Entries: []activity.Entry{{Seq: 1, Agent: "Main", Kind: "text", Text: "changed", Revision: 2}}}
	if err := s.Publish(frame); err != nil {
		t.Fatal(err)
	}
	if _, failure := s.content(testJSON(t, ref)); failure == nil || failure.Code != RPCReadSuperseded {
		t.Fatalf("entry replacement not classified: %+v", failure)
	}
}
