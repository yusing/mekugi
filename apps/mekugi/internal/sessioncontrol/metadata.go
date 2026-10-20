package sessioncontrol

// ReadThreadMetadata admits one observational read per attached scope. Already
// supplied metadata settles admission without issuing a host request.
// Source: apps/mekugi/internal/router/app_server_thread_metadata.go:13:27@29a3a244.
func (r *Requests[T]) ReadThreadMetadata(scope, thread string, known bool, owner T) error {
	key := [2]string{scope, thread}
	if r.metadata[key] {
		return nil
	}
	if !known {
		if _, err := r.Send("thread/read", map[string]any{"threadId": thread, "includeTurns": false}, scope, owner); err != nil {
			return err
		}
	}
	if r.metadata == nil {
		r.metadata = make(map[[2]string]bool)
	}
	r.metadata[key] = true
	return nil
}
