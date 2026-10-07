package router

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"
)

// An ancestry chain is usable only when every record reaches an unambiguous root.
func journalAncestry(journals map[string]threadJournal, thread string) []string {
	var chain []string
	for range len(journals) {
		j, ok := journals[thread]
		if !ok || !j.IdentityKnown || j.IdentityConflicted {
			return nil
		}
		chain = append(chain, thread)
		if j.Parent == "" {
			return chain
		}
		thread = j.Parent
	}
	return nil
}

func journalRelative(journals map[string]threadJournal, caller, target string) bool {
	a, b := journalAncestry(journals, caller), journalAncestry(journals, target)
	return len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] && (slices.Contains(a, target) || slices.Contains(b, caller))
}

func journalReadTarget(journals map[string]threadJournal, recordErrors map[string]error, caller, agent string) (string, error) {
	if agent != "" && !strings.HasPrefix(agent, "/") {
		agent = "/root/" + agent
	}
	if err := recordErrors[caller]; err != nil {
		return "", fmt.Errorf("journal agent %q cannot be resolved: caller record unavailable: %w", agent, err)
	}
	if len(journalAncestry(journals, caller)) == 0 {
		return "", fmt.Errorf("journal agent %q cannot be resolved: caller ancestry is unavailable; omit agent to read your own journal with view own or tasks", agent)
	}
	target := ""
	for thread, j := range journals {
		if j.Author != agent || !journalRelative(journals, caller, thread) {
			continue
		}
		if target != "" {
			return "", fmt.Errorf("journal agent %q is ambiguous in durable ancestry; use an unambiguous agent path or omit agent with view own or tasks", agent)
		}
		target = thread
	}
	if target == "" {
		return "", fmt.Errorf("journal agent %q is unavailable: no proven ancestor or descendant matches; verify its canonical /root/... path (or omit /root/), not a leaf name for a nested agent; omit agent with view own or tasks to read your own journal", agent)
	}
	for _, thread := range append(journalAncestry(journals, caller), journalAncestry(journals, target)...) {
		if err := recordErrors[thread]; err != nil {
			return "", err
		}
	}
	return target, nil
}

func journalPointerKey(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// Mount keys occupy a reserved view-only namespace. Owned ordinal paths never
// change, and read accepts these same combined paths. No composed item is saved.
func mountedJournalItems(journals map[string]threadJournal, recordErrors map[string]error, caller, target string) ([]journalItem, error) {
	const maxViewItems = maxJournalItems * 16
	var items []journalItem
	var build func(string, string) error
	build = func(thread, prefix string) error {
		if err := recordErrors[thread]; err != nil {
			return err
		}
		j := journals[thread]
		j.ensureTree()
		start := len(items)
		for _, item := range j.Items {
			item.Path = prefix + item.Path
			if item.SupersededBy != "" {
				item.SupersededBy = prefix + item.SupersededBy
			}
			items = append(items, item)
		}
		var children []threadJournal
		for childThread, child := range journals {
			if child.Parent == thread && childThread != thread && journalRelative(journals, caller, childThread) {
				children = append(children, child)
			}
		}
		slices.SortFunc(children, func(a, b threadJournal) int { return strings.Compare(a.Thread, b.Thread) })
		group := false
		for _, child := range children {
			// Ambiguous canonical paths cannot establish a mount binding.
			if slices.ContainsFunc(children, func(other threadJournal) bool { return other.Thread != child.Thread && other.Author == child.Author }) {
				return errors.New("journal mount agent path is ambiguous")
			}
			under := prefix + "/@agents"
			linked := -1
			for i := start; i < start+len(j.Items); i++ {
				if items[i].Agent == child.Author {
					linked, under = i, items[i].Path
					break
				}
			}
			if linked < 0 && !group {
				items = append(items, journalItem{Path: under, Kind: "context", Title: "Agents", Author: j.Author})
				group = true
			}
			path := under + "/@" + journalPointerKey(child.Thread)
			mount := journalItem{Path: path, Kind: "task", Title: child.Author, Agent: child.Author, Author: child.Author,
				State: child.LifecycleState, Reason: child.LifecycleReason, UpdatedAt: child.LifecycleAt, Turns: child.Turns}
			items = append(items, mount)
			if len(items) > maxViewItems {
				return errors.New("combined journal exceeds 8192 nodes; read a narrower agent subtree")
			}
			if err := build(child.Thread, path); err != nil {
				return err
			}
		}
		for _, item := range j.Items {
			if item.Agent != "" && !slices.ContainsFunc(children, func(child threadJournal) bool { return child.Author == item.Agent }) {
				items = append(items, journalItem{Path: prefix + item.Path + "/@pending", Kind: "task", Title: item.Agent, Agent: item.Agent, Author: item.Agent})
			}
		}
		if len(items) > maxViewItems {
			return errors.New("combined journal exceeds 8192 nodes; read a narrower agent subtree")
		}
		return nil
	}
	if err := build(target, ""); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *journalStore) readTree(ctx context.Context, store *mekugiReplayStore, workspace, caller, agent, path string, depth *int, view string) ([]journalNode, error) {
	if !validJournalView(view) {
		return nil, errJournalView
	}
	// Own reads need no mounted records or ancestry discovery. In particular,
	// recovering task IDs must still work when a descendant record is unavailable.
	if agent == "" && view != "" && view != "combined" {
		items, err := s.list(ctx, store, workspace, caller)
		if err != nil {
			return nil, err
		}
		return journalReadView(items, path, depth, view)
	}
	release, err := s.lockState(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var nodes []journalNode
	read := func() error {
		journals, recordErrors, err := s.workspaceJournals(store, workspace)
		if err != nil {
			return err
		}
		target := caller
		if agent != "" {
			target, err = journalReadTarget(journals, recordErrors, caller, agent)
			if err != nil {
				return err
			}
		} else if _, ok := journals[caller]; !ok {
			return errors.New("journal state is missing")
		}
		var items []journalItem
		if view != "" && view != "combined" {
			if err := recordErrors[target]; err != nil {
				return err
			}
			items = slices.Clone(journals[target].Items)
		} else {
			items, err = mountedJournalItems(journals, recordErrors, caller, target)
			if err != nil {
				return err
			}
		}
		nodes, err = journalReadView(items, path, depth, view)
		return err
	}
	if store != nil {
		err = store.locked(ctx, read)
	} else {
		err = read()
	}
	return nodes, err
}

var errJournalView = errors.New("journal view must be combined, own, tasks, or outline")

// Every view except combined reads only the selected journal. Outline keeps
// all node kinds but no bodies, so any path can be found without full content.
func validJournalView(view string) bool {
	return view == "" || view == "combined" || view == "own" || view == "tasks" || view == "outline"
}

func journalReadView(items []journalItem, path string, depth *int, view string) ([]journalNode, error) {
	if !validJournalView(view) {
		return nil, errJournalView
	}
	// Normalize retained v1 aliases without mutating their durable record.
	j := threadJournal{Items: slices.Clone(items)}
	j.ensureTree()
	if view == "tasks" {
		j.Items = slices.DeleteFunc(j.Items, func(item journalItem) bool { return item.Kind != "task" })
	}
	if view == "tasks" || view == "outline" {
		for i := range j.Items {
			j.Items[i].Body = ""
			j.Items[i].Question = ""
		}
	}
	return journalTree(j.Items, path, depth)
}

// Mounted views read a record's items, identity, spawn roles and host lifecycle.
// Delivery cursors, receipts and counters leave every view unchanged.
func mountedViewChanged(before, after threadJournal) bool {
	return before.Parent != after.Parent || before.Author != after.Author || before.IdentityKnown != after.IdentityKnown ||
		before.IdentityConflicted != after.IdentityConflicted || before.LifecycleState != after.LifecycleState ||
		before.LifecycleReason != after.LifecycleReason || before.LifecycleAt != after.LifecycleAt || before.Turns != after.Turns ||
		!reflect.DeepEqual(before.SpawnRoles, after.SpawnRoles) || !reflect.DeepEqual(before.Items, after.Items)
}

// Called under the state and replay locks after persistence. Child mutations
// refresh only ancestor views, never their records, events or delivery cursors.
// Only sinks on the changed thread's parent chain mount it, so other writes do
// not decode the workspace's journals.
func (s *journalStore) publishMountedViews(store *mekugiReplayStore, workspace, thread string) {
	s.nativeMu.Lock()
	var sinks []*nativeJournalSink
	for _, sink := range s.native {
		if sink.workspace == workspace {
			sinks = append(sinks, sink)
		}
	}
	s.nativeMu.Unlock()
	related := false
	seen := make(map[string]bool)
	for len(sinks) > 0 && thread != "" && !seen[thread] && !related {
		seen[thread] = true
		related = slices.ContainsFunc(sinks, func(sink *nativeJournalSink) bool { return sink.thread == thread })
		j, ok := s.memory[journalKey(workspace, thread)]
		if store != nil {
			var err error
			if j, ok, err = readThreadJournal(store, workspace, thread); err != nil {
				related = true // Unreadable ancestry cannot rule a view out.
				break
			}
		}
		if !ok {
			break
		}
		thread = j.Parent
	}
	if !related {
		return
	}
	journals, recordErrors, err := s.workspaceJournals(store, workspace)
	if err != nil {
		// The pane prefers the mounted view, so never leave an older one in
		// front of the sink's newer own tree.
		for _, sink := range sinks {
			sink.mu.Lock()
			sink.mounted = nil
			if sink.tree != nil {
				root := sink.tree.clone()
				root.Items = append(root.Items, journalItem{Path: "/@mount-error", Kind: "context", Title: "Mounted journals unavailable: " + err.Error()})
				sink.mounted = &root
			}
			sink.mu.Unlock()
		}
		return
	}
	for _, sink := range sinks {
		root, ok := journals[sink.thread]
		if !ok {
			continue
		}
		// Role evidence follows the same proven descendant boundary as mounts.
		// This is a presentation copy, not another durable role owner.
		root.SpawnRoles = maps.Clone(root.SpawnRoles)
		if root.SpawnRoles == nil {
			root.SpawnRoles = make(map[string]journalSpawnRole)
		}
		for thread, child := range journals {
			if thread != sink.thread && recordErrors[thread] == nil && slices.Contains(journalAncestry(journals, thread), sink.thread) {
				maps.Copy(root.SpawnRoles, child.SpawnRoles)
			}
		}
		items, err := mountedJournalItems(journals, recordErrors, sink.thread, sink.thread)
		sink.mu.Lock()
		if err == nil {
			root.Items = items
		} else {
			root.Items = slices.Clone(root.Items)
			root.Items = append(root.Items, journalItem{Path: "/@mount-error", Kind: "context", Title: "Mounted journals unavailable: " + err.Error()})
		}
		sink.mounted = &root
		sink.mu.Unlock()
	}
}

func (s *journalStore) observeLifecycle(ctx context.Context, store *mekugiReplayStore, workspace, thread, state, reason string) error {
	return s.observeLifecycleReceipt(ctx, store, workspace, thread, state, reason, "")
}

func (s *journalStore) observeLifecycleReceipt(ctx context.Context, store *mekugiReplayStore, workspace, thread, state, reason, receipt string) error {
	if state != "working" && state != "done" && state != "blocked" {
		return fmt.Errorf("invalid journal lifecycle state: %s", state)
	}
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists || !j.IdentityKnown || j.IdentityConflicted {
			return errJournalUnchanged
		}
		if receipt != "" {
			digest := state + "\x00" + reason
			if prior, ok := j.Receipts[receipt]; ok {
				if prior.Digest != digest {
					return errors.New("native lifecycle receipt changed")
				}
				return errJournalUnchanged
			}
			j.Receipts[receipt] = journalReceipt{Digest: digest}
		}
		if j.LifecycleState == state && j.LifecycleReason == reason && j.WorkPaused == (state != "working") && (state != "working" || !j.hasPausedWorkTimer()) {
			if receipt == "" {
				return errJournalUnchanged
			}
			return nil
		}
		now := time.Now().UTC()
		j.setWorkTimers(state == "working", now)
		j.LifecycleState, j.LifecycleReason, j.LifecycleAt = state, reason, now.Format(time.RFC3339Nano)
		return nil
	})
}

// Validate the final batch against proven mounted tasks, without copying their
// fields into the parent's durable record. Only tasks this batch completes or
// rebinds are checked: a child resumed under an earlier done task must not block
// unrelated writes.
func (s *journalStore) validateMountedCompletion(store *mekugiReplayStore, j threadJournal, before []journalItem) error {
	completing := func(item journalItem) bool {
		if item.Kind != "task" || item.State != "done" {
			return false
		}
		previous := slices.IndexFunc(before, func(old journalItem) bool { return old.Path == item.Path })
		return previous < 0 || before[previous].State != "done" || before[previous].Agent != item.Agent
	}
	if !slices.ContainsFunc(j.Items, completing) || !slices.ContainsFunc(j.Items, func(item journalItem) bool { return item.Agent != "" }) {
		return nil
	}
	journals, recordErrors, err := s.workspaceJournals(store, j.Workspace)
	if err != nil {
		return err
	}
	journals[j.Thread] = j
	items, err := mountedJournalItems(journals, recordErrors, j.Thread, j.Thread)
	if err != nil {
		return err
	}
	var open []string
	for _, parent := range j.Items {
		if !completing(parent) {
			continue
		}
		for _, child := range items {
			// Foreign task states belong to the child. Only the synthetic mount
			// roots gate parent integration on observed host completion. Keep
			// checking nested mounts even when their ancestor host has finished.
			mounted := strings.HasPrefix(child.Path[strings.LastIndex(child.Path, "/")+1:], "@")
			if child.Kind == "task" && mounted && strings.HasPrefix(child.Path, parent.Path+"/") && child.State != "done" && child.State != "dropped" {
				open = append(open, child.Path)
			}
		}
	}
	if len(open) > 0 {
		return fmt.Errorf("done tasks have open mounted descendants: %s", strings.Join(open, ", "))
	}
	return nil
}
