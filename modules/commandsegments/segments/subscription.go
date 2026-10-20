package segments

// Source: apps/mekugi/internal/router/exec_track.go:340:476@b27b4c66.

import (
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/modules/commandsegments/transport"
)

// Subscription joins one application-owned observation window to future host
// commands. It retains report facts independently of Activity retirement.
type Subscription struct {
	observer     *Observer
	thread, turn string
	matches      map[string]*match
	order        []string
	after        uint64
}

type match struct {
	key                      [3]string
	report                   *report
	ambiguous, hostCompleted bool
}

// Match is a detached report identity. Ambiguity supplies no usable evidence.
type Match struct {
	Source                   string
	Key                      [3]string
	Report                   *Report
	Ambiguous, HostCompleted bool
}

type State struct {
	Matches []Match
	Changed <-chan struct{}
}

func (h *Observer) Subscribe(thread, turn string, scripts []string) *Subscription {
	if h == nil || thread == "" || turn == "" || len(scripts) == 0 {
		return nil
	}
	p := &Subscription{observer: h, thread: thread, turn: turn, matches: make(map[string]*match)}
	for _, script := range scripts {
		source := p.Source(script, "")
		if previous := p.matches[source]; previous != nil {
			previous.ambiguous = true
		} else {
			p.matches[source] = &match{}
			p.order = append(p.order, source)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscriptions == nil {
		h.subscriptions = make(map[*Subscription]struct{})
	}
	for other := range h.subscriptions {
		if other.thread != thread || other.turn != turn {
			continue
		}
		for script, match := range p.matches {
			if sibling := other.matches[script]; sibling != nil {
				match.ambiguous, sibling.ambiguous = true, true
			}
		}
	}
	h.subscriptions[p] = struct{}{}
	p.after = h.sequence
	return p
}

func (p *Subscription) retain(key [3]string, report *report) {
	if key[0] != p.thread || key[1] != p.turn || report.serial <= p.after {
		return
	}
	if match := p.matches[p.Source(report.Script, key[2])]; match != nil {
		if match.report != nil && match.key != key {
			match.ambiguous = true
		} else {
			match.key, match.report = key, report
		}
	}
}

// Source removes only this invocation's exact, item-bound guard instrumentation.
func (p *Subscription) Source(script, item string) string {
	// Reapply only observed guards: sudo also runs with the VCS guard disabled.
	var guard, sudo string
	source := shellinstrumentation.DisplayScript(script, func(directory string) bool {
		if directory == "" {
			return false
		}
		switch directory {
		case p.observer.instrumentation.VCS:
			guard = directory
		case p.observer.instrumentation.Sudo:
			sudo = directory
		default:
			return false
		}
		return true
	})
	if source == script {
		return script
	}
	// This owner has no authenticated helper executable. An inverse rewrite
	// with no helper must match exactly, so only injected PATH prefixes qualify.
	rewritten, err := shellinstrumentation.RewriteCommands(source, "", guard, sudo, "")
	if err != nil || rewritten != script {
		if item == "" {
			return script
		}
		// The host hook also binds the guard to this exact command item.
		rewritten, err = shellinstrumentation.RewriteCommands(source, "", guard, sudo, item)
		if err != nil || rewritten != script {
			return script
		}
	}
	return source
}

func (p *Subscription) Close() {
	if p == nil {
		return
	}
	p.observer.mu.Lock()
	delete(p.observer.subscriptions, p)
	p.observer.mu.Unlock()
}

func (p *Subscription) CreatedPaths() []transport.Message {
	if p == nil {
		return nil
	}
	p.observer.mu.Lock()
	defer p.observer.mu.Unlock()
	var paths []transport.Message
	for _, match := range p.matches {
		if !match.ambiguous && match.report != nil {
			paths = append(paths, match.report.Created...)
		}
	}
	return paths
}

func (p *Subscription) State() State {
	if p == nil {
		return State{}
	}
	h := p.observer
	h.mu.Lock()
	defer h.mu.Unlock()
	state := State{Changed: h.changed}
	for _, script := range p.order {
		retained := p.matches[script]
		view := Match{Source: script, Key: retained.key, Ambiguous: retained.ambiguous, HostCompleted: retained.hostCompleted}
		if retained.report != nil {
			view.Report = new(retained.report.snapshot())
		}
		state.Matches = append(state.Matches, view)
	}
	return state
}
