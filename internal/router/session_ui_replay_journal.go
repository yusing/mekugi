package router

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Read published evidence only. Identities authorize composition; retained latest
// items and lifecycle never become historical presentation state.
func (r *sessionUIReplay) readJournals(store *mekugiReplayStore, loaded map[string]bool) error {
	r.Journals = make(map[string]map[string]threadJournal)
	slices.SortStableFunc(r.Events, func(a, b uiReplayEvent) int { return a.At.Compare(b.At) })
	start, end := r.Events[0].At, r.Events[len(r.Events)-1].At
	turns := make(map[string][]journalReplayTurn)
	first := make(map[string]time.Time)
	for _, event := range r.Events {
		thread := event.Params.ThreadID
		if first[thread].IsZero() {
			first[thread] = event.At
		}
		own := turns[thread]
		switch event.Method {
		case "turn/started":
			own = append(own, journalReplayTurn{id: event.Params.Turn.ID, start: event.At, end: end})
		case "turn/completed":
			if i := slices.IndexFunc(own, func(turn journalReplayTurn) bool { return turn.id == event.Params.Turn.ID }); i >= 0 {
				own[i].end, own[i].status = event.At, event.Params.Turn.Status
			}
		case "item/completed":
			if i := slices.IndexFunc(own, func(turn journalReplayTurn) bool { return turn.id == event.Params.TurnID }); i >= 0 {
				own[i].items = append(own[i].items, event.Params.Item)
			}
		}
		turns[thread] = own
	}
	var threads []string
	for thread := range loaded {
		threads = append(threads, thread)
	}
	slices.Sort(threads)
	for _, workspace := range slices.Compact([]string{r.Cwd, ""}) {
		records := make(map[string]threadJournal)
		seen := make(map[string]bool)
		queue := slices.Clone(threads)
		for len(queue) > 0 {
			thread := queue[0]
			queue = queue[1:]
			if seen[thread] {
				continue
			}
			seen[thread] = true
			if len(seen) > 256 {
				return errors.New("journal replay exceeds 256 ancestry records")
			}
			j, exists, err := readThreadJournal(store, workspace, thread)
			if err != nil {
				r.JournalUnavailable = append(r.JournalUnavailable, fmt.Sprintf("%s thread %s: %v", workspace, thread, err))
				continue
			}
			if !exists {
				continue // A thread need not have authored either journal namespace.
			}
			records[thread] = j
			if j.Parent != "" {
				queue = append(queue, j.Parent)
			}
		}
		root, exists := records[r.Thread]
		if !exists {
			continue
		}
		identities := make(map[string]threadJournal)
		// Retain ancestors only for proof, never their authored contents.
		for _, thread := range journalAncestry(records, r.Thread) {
			identities[thread] = replayJournalIdentity(records[thread])
		}
		identities[r.Thread] = replayJournalIdentity(root)
		for _, thread := range threads {
			j, exists := records[thread]
			if !exists {
				if thread != r.Thread {
					r.JournalUnavailable = append(r.JournalUnavailable, fmt.Sprintf("%s child %s: no retained journal", workspace, thread))
				}
				continue
			}
			ancestry := journalAncestry(records, thread)
			if thread != r.Thread && !slices.Contains(ancestry, r.Thread) {
				r.JournalUnavailable = append(r.JournalUnavailable, fmt.Sprintf("%s child %s: ancestry unavailable", workspace, thread))
				continue
			}
			for _, ancestor := range ancestry {
				identities[ancestor] = replayJournalIdentity(records[ancestor])
			}
			identities[thread] = replayJournalIdentity(j)
			own := turns[thread]
			if len(own) == 0 {
				begin := first[thread]
				if begin.IsZero() {
					begin = start
				}
				own = []journalReplayTurn{{start: begin, end: end}}
			}
			for _, update := range journalReplayTimeline(j, journalReplayInlineTurns(store, workspace, own)) {
				r.Events = append(r.Events, uiReplayEvent{At: update.at, Method: "replay/journal", Params: appServerEvent{ThreadID: thread}, Journal: &update})
				if len(r.Events) > 500000 {
					return errors.New("replay exceeds 500000 events")
				}
			}
		}
		r.Journals[workspace] = identities
	}
	return nil
}

func replayJournalIdentity(j threadJournal) threadJournal {
	return threadJournal{Version: 2, TreeAuthored: j.TreeAuthored, Workspace: j.Workspace, Thread: j.Thread,
		Author: j.Author, Parent: j.Parent, IdentityKnown: j.IdentityKnown, IdentityConflicted: j.IdentityConflicted}
}

func (p *uiReplayPlayback) replayJournal(workspace, thread string) (threadJournal, bool) {
	if j, ok := p.journals[workspace][thread]; ok {
		return j, true
	}
	identity, ok := p.source.Journals[workspace][thread]
	if !ok {
		return threadJournal{}, false
	}
	if p.journals[workspace] == nil {
		p.journals[workspace] = make(map[string]threadJournal)
	}
	// Ancestors must exist for shared composition's proof, but descendants are
	// admitted only when their recorded events arrive.
	for _, ancestor := range journalAncestry(p.source.Journals[workspace], thread) {
		if _, exists := p.journals[workspace][ancestor]; !exists {
			p.journals[workspace][ancestor] = p.source.Journals[workspace][ancestor]
		}
	}
	p.journals[workspace][thread] = identity
	return identity, true
}

func (p *uiReplayPlayback) composeReplayJournal(workspace string) *nativeJournalSink {
	journals := p.journals[workspace]
	root := journals[p.source.Thread]
	items, err := mountedJournalItems(journals, nil, p.source.Thread, p.source.Thread)
	if err != nil {
		items = append(slices.Clone(root.Items), journalItem{Path: "/@mount-error", Kind: "context", Title: "Mounted journals unavailable: " + err.Error()})
	}
	root.Items = items
	sink := p.ui.journal
	if workspace == "" {
		sink = p.ui.unscopedJournal
	}
	if sink == nil {
		sink = &nativeJournalSink{workspace: workspace, thread: p.source.Thread}
		if workspace == "" {
			p.ui.unscopedJournal = sink
		} else {
			p.ui.journal = sink
		}
	}
	sink.tree = &root
	return sink
}

func (p *uiReplayPlayback) applyReplayJournal(e uiReplayEvent) {
	update, thread := e.Journal, e.Params.ThreadID
	if thread == "" {
		thread = p.source.Thread
	}
	j, ok := p.replayJournal(update.workspace, thread)
	if !ok {
		return
	}
	if update.tree != nil {
		j.Items, j.Events, j.Sequence = slices.Clone(update.tree.Items), slices.Clone(update.tree.Events), update.tree.Sequence
	}
	if event := update.publication.event; event != nil {
		applyJournalReplayEvent(&j, *event)
	}
	p.journals[update.workspace][thread] = j
	sink := p.composeReplayJournal(update.workspace)
	if thread == p.source.Thread && update.tree == nil {
		p.ui.applyJournalPublication(sink, update.publication)
	}
	p.ui.session.seq = max(p.ui.session.seq, p.ui.view.lastSeq)
}

func (p *uiReplayPlayback) observeReplayJournalTurn(e uiReplayEvent) {
	if e.Method != "turn/started" && e.Method != "turn/completed" {
		return
	}
	for workspace := range p.source.Journals {
		j, ok := p.replayJournal(workspace, e.Params.ThreadID)
		if !ok {
			continue
		}
		j.LifecycleState, j.LifecycleReason = "working", ""
		if e.Method == "turn/completed" {
			j.LifecycleState = "done"
			if e.Params.Turn.Status != "completed" {
				j.LifecycleState, j.LifecycleReason = "blocked", "Host turn "+e.Params.Turn.Status
			}
		}
		j.LifecycleAt = e.At.Format(time.RFC3339Nano)
		p.journals[workspace][e.Params.ThreadID] = j
		p.composeReplayJournal(workspace)
	}
}
