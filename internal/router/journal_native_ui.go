package router

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// A native sink is scoped to one launched Main thread and workspace. Durable
// journal records remain the owner; this holds only pending presentation copies.
type nativeJournalSink struct {
	mounted             *threadJournal
	publicationSequence uint64
	tree                *threadJournal
	mu                  sync.Mutex
	workspace, thread   string
	pending             map[string]nativeJournalPublication
	answers             map[string]bool // Exact empty-Outcome item IDs replaced by work reports.
	sequence            uint64
	current             map[string]uint64
}

type nativeJournalPublication struct {
	order               uint64
	event               *journalEvent
	card                *nativeJournalCard
	item                journalItem
	terminal, retracted bool
	batch               uint64
}

func (s *journalStore) attachNative(workspace, thread string) *nativeJournalSink {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	if s.native == nil {
		s.native = make(map[string]*nativeJournalSink)
	}
	sink := &nativeJournalSink{workspace: workspace, thread: thread, pending: make(map[string]nativeJournalPublication), answers: make(map[string]bool)}
	s.native[journalKey(workspace, thread)] = sink
	return sink
}

func (s *journalStore) detachNative(sink *nativeJournalSink) {
	if sink == nil {
		return
	}
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	key := journalKey(sink.workspace, sink.thread)
	if s.native[key] == sink {
		delete(s.native, key)
	}
}

func (s *journalStore) nativeSink(workspace, thread string) *nativeJournalSink {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	return s.native[journalKey(workspace, thread)]
}

func (t *mekugiResponseTransform) nativeJournal() *nativeJournalSink {
	if t.subagentTurn || t.proxy == nil || t.proxy.journals == nil {
		return nil
	}
	if t.journalNativeSink == nil {
		t.journalNativeSink = t.proxy.journals.nativeSink(t.directory, t.shellThreadID)
	}
	return t.journalNativeSink
}

func (s *nativeJournalSink) publish(journal threadJournal, terminal bool, responseID ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if journal.TreeAuthored {
		if journal.Sequence >= s.sequence {
			snapshot := journal.clone()
			s.tree = &snapshot
			s.sequence = journal.Sequence
		}
		for _, event := range journal.Events {
			if event.Seq <= journal.LiveSeq || journal.LegacyLive[event.Seq] || event.Fields.Kind == "answer" {
				continue
			}
			id := fmt.Sprintf("event:%d", event.Seq)
			e := event
			if _, exists := s.pending[id]; exists {
				continue
			}
			s.publicationSequence++
			publication := nativeJournalPublication{order: s.publicationSequence, event: &e, item: journalItem{ID: id, Path: event.Path, Kind: event.Fields.Kind, State: event.Fields.State, Text: journalRowText(event), Updated: event.Seq, Author: event.Author}}
			if event.Op == "set" && !event.Transition {
				// Title and body edits are not transitions. They stay pending
				// only for acknowledgement and render no transcript row.
				publication.item.Text = ""
			}
			s.pending[id] = publication
		}
		if terminal && journalHasReport(journal, journal.FlushSeq) {
			id := fmt.Sprintf("card:%d", journal.Sequence)
			if len(responseID) > 0 && responseID[0] != "" {
				id = "card:" + responseID[0]
			}
			snapshot := journal.clone()
			s.publicationSequence++
			s.pending[id] = nativeJournalPublication{order: s.publicationSequence, card: &nativeJournalCard{Journal: snapshot, Since: journal.FlushSeq}, terminal: true, item: journalItem{ID: id, Updated: journal.Sequence, Author: journal.Author}}
		}
		return
	}
	if journal.Sequence >= s.sequence {
		s.sequence = journal.Sequence
		s.current = make(map[string]uint64, len(journal.Items)+len(journal.Retractions))
		for _, item := range journal.Items {
			s.current[item.ID] = item.Updated
		}
		for _, item := range journal.Retractions {
			s.current[item.ID] = item.Sequence
		}
		for id, pending := range s.pending {
			if s.current[id] != pending.item.Updated {
				delete(s.pending, id)
			}
		}
	}
	for _, item := range journal.Items {
		if item.TerminalOnly {
			continue // Main answers remain on the host's ordinary message path.
		}
		if s.current[item.ID] != item.Updated {
			continue
		}
		if terminal && item.Flushed || !terminal && (item.TerminalOnly || item.Question != "" || item.Reported) {
			continue
		}
		previous, exists := s.pending[item.ID]
		if exists && (previous.item.Updated > item.Updated || previous.item.Updated == item.Updated && previous.terminal) {
			continue
		}
		publication := nativeJournalPublication{item: item, terminal: terminal}
		if terminal {
			publication.batch = journal.Sequence
		}
		s.pending[item.ID] = publication
	}
	for _, retraction := range journal.Retractions {
		if s.current[retraction.ID] != retraction.Sequence {
			continue
		}
		s.pending[retraction.ID] = nativeJournalPublication{item: journalItem{ID: retraction.ID, Updated: retraction.Sequence, Author: journal.Author}, terminal: terminal, retracted: true}
	}
}

// A transcript row is one line: a task transition, a removal, or a note's title.
// Bodies stay in the pane's detail view and the expanded turn card.
func journalRowText(event journalEvent) string {
	switch {
	case event.Legacy || event.Op == "remove":
		return journalEventText(event)
	case event.Fields.Kind == "task":
		return journalTaskText(event.Fields)
	}
	return event.Fields.Title + journalSupersededText(event.Fields)
}

func (s *nativeJournalSink) bindAnswer(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only the current native turn needs these transient provider IDs.
	clear(s.answers)
	for _, id := range ids {
		s.answers[id] = true
	}
}

func (s *nativeJournalSink) hides(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answers[id]
}

func (s *nativeJournalSink) snapshot() []nativeJournalPublication {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]nativeJournalPublication, 0, len(s.pending))
	for _, item := range s.pending {
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b nativeJournalPublication) int {
		if a.item.Updated < b.item.Updated {
			return -1
		}
		if a.item.Updated > b.item.Updated {
			return 1
		}
		if a.order < b.order {
			return -1
		}
		if a.order > b.order {
			return 1
		}
		return 0
	})
	return items
}

// Receipts belong to the journal owner and follow the successful terminal write.
// Failure retains the pending revision; enqueueing or reading is not delivery.
func (s *nativeJournalSink) acknowledge(ctx context.Context, p *mekugiProxy, items []nativeJournalPublication) error {
	for _, item := range items {
		if item.card != nil || item.event != nil {
			if err := p.journals.acknowledgeTree(ctx, p.replayStore, s.workspace, s.thread, item.item.Updated, item.terminal); err != nil {
				return err
			}
		} else if err := p.journals.acknowledge(ctx, p.replayStore, s.workspace, s.thread, map[string]uint64{item.item.ID: item.item.Updated}, item.terminal); err != nil {
			return err
		}
		s.mu.Lock()
		if pending, ok := s.pending[item.item.ID]; ok && pending.item.Updated == item.item.Updated && pending.terminal == item.terminal {
			delete(s.pending, item.item.ID)
		}
		s.mu.Unlock()
	}
	return nil
}

// Apply persisted milestones before later host events, not only at the next
// paint tick. Delivery receipts still belong to the successful paint path.
func (u *appServerUI) applyPendingJournal() {
	if u.historyPending() {
		return // Pending publications remain until Main shows resumed history.
	}
	for _, sink := range []*nativeJournalSink{u.journal, u.unscopedJournal} {
		if sink == nil {
			continue
		}
		items := sink.snapshot()
		for _, publication := range items {
			u.applyJournalPublication(sink, publication)
		}
		u.dirty = u.dirty || len(items) > 0
	}
	// Journal entries and app-server entries share the transcript ordering.
	u.session.seq = max(u.session.seq, u.view.lastSeq)
}

func (u *appServerUI) applyJournalPublication(sink *nativeJournalSink, publication nativeJournalPublication) {
	if publication.event != nil && publication.event.Op != "reset" && publication.event.Fields.Kind == "note" && u.journalPanePresents(sink) {
		return
	}
	if publication.event == nil || publication.item.Text != "" { // Non-transition edits render no row.
		u.view.applyJournal(journalKey(sink.workspace, sink.thread), publication)
	}
}

func (s *journalStore) restoreNative(ctx context.Context, store *mekugiReplayStore, sink *nativeJournalSink) error {
	var snapshot *threadJournal
	err := s.transaction(ctx, store, sink.workspace, sink.thread, func(j *threadJournal, exists bool) error {
		if exists {
			copy := j.clone()
			snapshot = &copy
		}
		return errJournalUnchanged
	})
	if err == nil && snapshot != nil {
		sink.publish(*snapshot, false)
		// Restore mounts under the same locked snapshot discipline as live updates.
		err = s.transaction(ctx, store, sink.workspace, sink.thread, func(_ *threadJournal, _ bool) error {
			s.publishMountedViews(store, sink.workspace, sink.thread)
			return errJournalUnchanged
		})
	}
	return err
}
