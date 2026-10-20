package scope

// Store owns memory for one application scope. Its zero value is ready for use.
// The application serializes access and replaces the store when the scope ends.
// Do not copy a store after use.
type Store struct {
	entries map[any]any
}

// Key grants access to one module-owned typed entry. Keep keys private to their
// module and do not copy them after use. The byte gives even zero-sized T keys
// distinct addresses. State is initialized to T's zero value on first access.
type Key[T any] struct{ _ byte }

func (k *Key[T]) Value(store *Store) *T {
	if value, ok := store.entries[k]; ok {
		return value.(*T)
	}
	if store.entries == nil {
		store.entries = make(map[any]any)
	}
	value := new(T)
	store.entries[k] = value
	return value
}
