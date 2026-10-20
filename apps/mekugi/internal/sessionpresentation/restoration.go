package sessionpresentation

import "slices"

const RestoreThreadLimit = 128

// Restoration owns bounded discovery and the child-before-Main hydration order.
// Host payloads and feature content are retained by the consuming adapter.
// Source: apps/mekugi/internal/router/app_server_restore_content.go:20:290@eaad597b.
type Restoration struct {
	root             string
	threads          []string
	seen             map[string]bool
	cursors          map[string]bool
	archived         bool
	cursor           string
	pages, next      int
	listed, rendered bool
}

type RestoreView struct {
	Threads          []string
	Next             int
	Archived         bool
	Cursor           string
	Listed, Rendered bool
}

func NewRestoration(root string) *Restoration {
	return &Restoration{root: root, seen: make(map[string]bool), cursors: make(map[string]bool)}
}

func (r *Restoration) View() RestoreView {
	return RestoreView{Threads: slices.Clone(r.threads), Next: r.next, Archived: r.archived, Cursor: r.cursor, Listed: r.listed, Rendered: r.rendered}
}

// Discover returns positions admitted from one native thread-list page. A notice
// ends discovery with its successful prefix intact; child reads still continue.
func (r *Restoration) Discover(ids []string, cursor string) (accepted []int, notice string) {
	if r.listed {
		return nil, ""
	}
	r.pages++
	for i, id := range ids {
		if id == "" || id == r.root || r.seen[id] {
			continue
		}
		if len(r.threads) == RestoreThreadLimit {
			r.listed = true
			return accepted, "Activity restoration is limited to 128 child threads; additional history was not loaded."
		}
		r.seen[id] = true
		r.threads = append(r.threads, id)
		accepted = append(accepted, i)
	}
	if cursor != "" {
		if r.cursors[cursor] {
			r.listed = true
			return accepted, "Roster history returned a repeated pagination cursor."
		}
		r.cursors[cursor] = true
		r.cursor = cursor
	} else if !r.archived {
		r.archived, r.cursor = true, ""
		r.cursors = make(map[string]bool)
	} else {
		r.listed = true
	}
	if !r.listed && r.pages >= 8 {
		r.listed = true
		return accepted, "Roster pagination exceeded the restoration limit."
	}
	return accepted, ""
}

func (r *Restoration) EndDiscovery() { r.listed = true }

func (r *Restoration) ReadComplete() {
	if r.next < len(r.threads) {
		r.next++
	}
}

func (r *Restoration) ContentRendered() { r.rendered = true }

// Ready means discovery, child reads and Main hydration have settled. The
// consumer separately gates input on its admitted retained-evidence publication.
func (r *Restoration) Ready() bool {
	return r.listed && r.next >= len(r.threads) && r.rendered
}
