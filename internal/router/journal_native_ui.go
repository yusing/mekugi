package router

import (
	"context"
	"slices"
	"sync"
)

// A native sink is scoped to one launched Main thread and workspace. Durable
// journal records remain the owner; this holds only pending presentation copies.
type nativeJournalSink struct {
	mu                sync.Mutex
	workspace, thread string
	pending           map[string]nativeJournalPublication
	answers           map[string]string // Proven provider item ID -> captured journal item ID.
	sequence          uint64
	current           map[string]uint64
}

type nativeJournalPublication struct {
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
	sink := &nativeJournalSink{workspace: workspace, thread: thread, pending: make(map[string]nativeJournalPublication), answers: make(map[string]string)}
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

func (s *nativeJournalSink) publish(journal threadJournal, terminal bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		for id, journalID := range s.answers {
			if s.current[journalID] == 0 {
				delete(s.answers, id)
			}
		}
	}
	for _, item := range journal.Items {
		if s.current[item.ID] != item.Updated {
			continue
		}
		if terminal && item.Flushed || !terminal && (!item.ReportNow || item.Reported) {
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

func (s *nativeJournalSink) bindAnswer(ids []string, journalID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		s.answers[id] = journalID
	}
}

func (s *nativeJournalSink) hides(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answers[id] != ""
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
		return 0
	})
	return items
}

// Receipts belong to the journal owner and follow the successful terminal write.
// Failure retains the pending revision; enqueueing or reading is not delivery.
func (s *nativeJournalSink) acknowledge(ctx context.Context, p *mekugiProxy, items []nativeJournalPublication) error {
	for _, item := range items {
		if err := p.journals.acknowledge(ctx, p.replayStore, s.workspace, s.thread, map[string]uint64{item.item.ID: item.item.Updated}, item.terminal); err != nil {
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
