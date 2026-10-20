package history

import "errors"

var errCallsClosed = errors.New("session history request admission is closed")

type callPin struct {
	thread string
	count  int
}

type callLease struct {
	current *CallRequest
	release func()
}

// CallRequest pins retained facts before storage acquisition. Its identity also
// distinguishes a delivered terminal from a newer host handoff for the thread.
// Source: apps/mekugi/internal/router/mekugi_history.go:101:111 and
// storage_retention.go:77:124@73fdbcf2.
type CallRequest struct {
	cache           *CallCache
	session, thread string
	finished        bool // Protected by cache.mu with the pin and lease.
}

func (p *CallCache) Begin(session, thread string) (*CallRequest, error) {
	if p == nil {
		return nil, errors.New("session history cache is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.draining {
		return nil, errCallsClosed
	}
	pin := p.activeSessions[session]
	if pin.count != 0 && pin.thread != thread {
		return nil, errors.New("retained request cache identity belongs to another thread")
	}
	if p.activeSessions == nil {
		p.activeSessions = make(map[string]callPin)
	}
	p.activeSessions[session] = callPin{thread: thread, count: pin.count + 1}
	if cached := p.sessions[session]; cached != nil {
		p.touch(cached)
	}
	return &CallRequest{cache: p, session: session, thread: thread}, nil
}

// BindLease adopts an acquired process lease. A shared handoff retains its first
// lease and disposes only the redundant acquisition, including after shutdown.
func (r *CallRequest) BindLease(release func()) error {
	p := r.cache
	p.mu.Lock()
	if p.closed || p.draining || r.finished {
		p.mu.Unlock()
		release()
		return errCallsClosed
	}
	if p.leases == nil {
		p.leases = make(map[string]callLease)
	}
	lease := p.leases[r.thread]
	discard := release
	if lease.release == nil {
		lease.release, discard = release, nil
	}
	lease.current = r
	p.leases[r.thread] = lease
	p.mu.Unlock()
	if discard != nil {
		discard()
	}
	return nil
}

// Finish releases the request pin once. Only a delivered idle terminal may end
// the newest handoff, after all preparations and requests for its thread finish.
func (r *CallRequest) Finish(idle bool) {
	if r == nil {
		return
	}
	p := r.cache
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	pin := p.activeSessions[r.session]
	if pin.count <= 1 {
		delete(p.activeSessions, r.session)
	} else {
		pin.count--
		p.activeSessions[r.session] = pin
	}
	lease := p.leases[r.thread]
	if !idle || p.closed || p.draining || lease.current != r {
		return
	}
	for _, active := range p.activeSessions {
		if active.thread == r.thread {
			return
		}
	}
	delete(p.leases, r.thread)
	lease.release()
}

// StopAdmission preserves leases while application observers drain. Close then
// releases the retained handoffs, independently of unfinished host operations.
func (p *CallCache) StopAdmission() {
	if p != nil {
		p.mu.Lock()
		p.draining = true
		p.mu.Unlock()
	}
}

// Source: apps/mekugi/internal/router/mekugi_proxy.go:88:107@73fdbcf2.
func (p *CallCache) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, lease := range p.leases {
		lease.release()
	}
	clear(p.leases)
	clear(p.sessions)
	clear(p.activeSessions)
	p.historyBytes = 0
}
