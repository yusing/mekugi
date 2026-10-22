package sessionapi

// registerTestSession supplies a publication to transport tests without launching
// a runtime. Runtime ownership is exercised by the managed-session tests.
func registerTestSession(h *Hub, id string, server *Server, summary Summary) {
	entry := projectSummary(summary)
	entry.ID = id
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[id] = &listedSession{entry: entry, server: server}
	h.publishList()
}
