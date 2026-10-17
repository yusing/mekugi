package history

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const (
	maxCacheCalls        = 128
	maxCacheSessions     = 256
	maxCacheSessionBytes = 32 << 20
	maxCacheBytes        = 128 << 20
)

// Call supplies a neutral host fact in request emission order.
type Call struct {
	ID      string
	History legacycall.History
}
type cachedCall struct {
	legacycall.History
	bytes    int
	sequence uint64
}
type cachedSession struct {
	calls                  map[string]cachedCall
	bytes                  int
	nextSequence, lastUsed uint64
}

// CallCache owns bounded invocation-local host facts, not request reconciliation.
// Source: apps/mekugi/internal/router/mekugi_history.go:106:301@d002a0dc.
type CallCache struct {
	mu              sync.RWMutex
	sessions        map[string]*cachedSession
	activeSessions  map[string]int
	historyBytes    int
	sessionSequence uint64
}

func (p *CallCache) Pin(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeSessions == nil {
		p.activeSessions = make(map[string]int)
	}
	p.activeSessions[sessionID]++
	if session := p.sessions[sessionID]; session != nil {
		p.touch(session)
	}
}
func (p *CallCache) Unpin(sessionID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeSessions[sessionID] <= 1 {
		delete(p.activeSessions, sessionID)
	} else {
		p.activeSessions[sessionID]--
	}
}
func (p *CallCache) PinnedSessions() []string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Collect(maps.Keys(p.activeSessions))
}
func (p *CallCache) Clear() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.sessions)
	clear(p.activeSessions)
	p.historyBytes = 0
}
func (p *CallCache) touch(session *cachedSession) {
	p.sessionSequence++
	session.lastUsed = p.sessionSequence
}
func (p *CallCache) CommentaryIDs(sessionID string) map[string]struct{} {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]struct{})
	if session := p.sessions[sessionID]; session != nil {
		for _, call := range session.calls {
			for _, id := range call.CommentaryMessageIDs {
				result[id] = struct{}{}
			}
		}
	}
	return result
}

func (p *CallCache) Remember(sessionID string, histories []Call) error {
	if len(histories) == 0 {
		return nil
	}
	if p == nil {
		return errors.New("session history cache is unavailable")
	}
	prepared := make(map[string]cachedCall, len(histories))
	for _, call := range histories {
		callID, history := call.ID, cachedCall{History: call.History}
		encodedItem, err := responses.MarshalJSON(history.UpstreamItem)
		if err != nil {
			return fmt.Errorf("encode mekugi history item: %w", err)
		}
		history.bytes = len(sessionID) + len(callID) + len(history.ToolName) + len(history.PluginID) + len(history.Script) + len(history.Root) + len(history.CarrierKind) + len(history.CarrierName) + len(history.CarrierPayload) + len(history.Report) + len(history.OutputWarning) + len(history.TranslationError) + len(history.CorrelationID) + len(encodedItem)
		for _, patch := range history.NativePatches {
			history.bytes += len(patch.Input)
			for _, file := range patch.Files {
				history.bytes += len(file.BeforePath) + len(file.AfterPath) + len(file.Before) + len(file.Error)
			}
		}
		if history.ResolvedBaseline != nil {
			history.bytes += len(responses.MustMarshalJSON(history.ResolvedBaseline))
		}
		history.bytes += len(history.ChangeID) + len(history.ExecutingThread) + len(history.Caller) + len(history.Source)
		for _, file := range history.ReviewFiles {
			history.bytes += len(file.BeforePath) + len(file.AfterPath) + len(file.Diff)
		}
		for _, messageID := range history.CommentaryMessageIDs {
			history.bytes += len(messageID)
		}
		prepared[callID] = history
	}
	if len(prepared) > maxCacheCalls {
		return errors.New("mekugi history batch exceeds call capacity")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sessions == nil {
		p.sessions = make(map[string]*cachedSession)
	}
	existing := p.sessions[sessionID]
	calls := make(map[string]cachedCall, len(prepared))
	nextSequence := uint64(0)
	oldSessionBytes := 0
	sessionBytes := 0
	if existing != nil {
		calls = maps.Clone(existing.calls)
		nextSequence = existing.nextSequence
		oldSessionBytes = existing.bytes
		sessionBytes = existing.bytes
	}
	callIDs := make([]string, 0, len(histories))
	for _, call := range histories {
		callIDs = append(callIDs, call.ID)
	}
	protected := make(map[string]bool, len(prepared))
	for _, callID := range callIDs {
		history := prepared[callID]
		if previous, ok := calls[callID]; ok {
			sessionBytes -= previous.bytes
			history.sequence = previous.sequence
		} else {
			nextSequence++
			history.sequence = nextSequence
		}
		sessionBytes += history.bytes
		calls[callID] = history
		protected[callID] = true
	}

	for len(calls) > maxCacheCalls || sessionBytes > maxCacheSessionBytes {
		oldest, ok := oldestCall(calls, protected)
		if !ok {
			if len(calls) > maxCacheCalls {
				return errors.New("mekugi history call capacity reached")
			}
			return errors.New("mekugi history byte capacity reached")
		}
		sessionBytes -= calls[oldest].bytes
		delete(calls, oldest)
	}

	totalBytes := p.historyBytes - oldSessionBytes + sessionBytes
	sessionCount := len(p.sessions)
	if existing == nil {
		sessionCount++
	}
	type sessionCandidate struct {
		id       string
		lastUsed uint64
	}
	candidates := make([]sessionCandidate, 0, len(p.sessions))
	for id, session := range p.sessions {
		if id == sessionID || p.activeSessions[id] != 0 {
			continue
		}
		candidates = append(candidates, sessionCandidate{id: id, lastUsed: session.lastUsed})
	}
	slices.SortFunc(candidates, func(first, second sessionCandidate) int {
		if order := cmp.Compare(first.lastUsed, second.lastUsed); order != 0 {
			return order
		}
		return strings.Compare(first.id, second.id)
	})

	evicted := make([]string, 0)
	for sessionCount > maxCacheSessions || totalBytes > maxCacheBytes {
		if len(evicted) == len(candidates) {
			if sessionCount > maxCacheSessions {
				return errors.New("mekugi history session capacity reached")
			}
			return errors.New("mekugi history byte capacity reached")
		}
		id := candidates[len(evicted)].id
		evicted = append(evicted, id)
		sessionCount--
		totalBytes -= p.sessions[id].bytes
	}
	for _, id := range evicted {
		delete(p.sessions, id)
	}

	if existing == nil {
		existing = &cachedSession{}
		p.sessions[sessionID] = existing
	}
	existing.calls = calls
	existing.bytes = sessionBytes
	existing.nextSequence = nextSequence
	p.touch(existing)
	p.historyBytes = totalBytes
	return nil
}

func oldestCall(histories map[string]cachedCall, protected map[string]bool) (string, bool) {
	oldestID := ""
	var oldest cachedCall
	found := false
	for callID, history := range histories {
		if protected[callID] {
			continue
		}
		if !found || history.sequence < oldest.sequence || history.sequence == oldest.sequence && callID < oldestID {
			oldestID = callID
			oldest = history
			found = true
		}
	}
	return oldestID, found
}

func (p *CallCache) Lookup(sessionID, callID string) (legacycall.History, bool) {
	if p == nil {
		return legacycall.History{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	session := p.sessions[sessionID]
	if session == nil {
		return legacycall.History{}, false
	}
	call, ok := session.calls[callID]
	return call.History, ok
}
