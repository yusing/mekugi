package router

import (
	"cmp"
	"strings"
)

// Host metadata supplies ancestry and cwd. Unknown ancestry stays distinct until
// metadata arrives; a native path alone does not identify an independent tree.
func (s *appServerSession) threadRoot(thread string) string {
	for range len(s.threads) + 1 {
		if _, ok := s.roots[thread]; ok {
			return thread
		}
		info, ok := s.threads[thread]
		if !ok || info.ParentThreadID == "" {
			return ""
		}
		thread = info.ParentThreadID
	}
	return ""
}

func (s *appServerSession) scopedAgentPath(thread, path string) string {
	if path == "" {
		return ""
	}
	root := s.threadRoot(thread)
	if prefix := s.roots[root]; prefix != "" {
		return prefix + strings.TrimPrefix(path, "/root")
	}
	if info, known := s.threads[thread]; !known || info.ParentThreadID != "" {
		return "/thread/" + thread
	}
	return path
}

func (s *appServerSession) threadWorkspace(thread string) string {
	for range len(s.threads) + 1 {
		info, ok := s.threads[thread]
		if !ok {
			return ""
		}
		if info.Cwd != "" {
			return info.Cwd
		}
		thread = info.ParentThreadID
	}
	return ""
}

func (u *appServerUI) registerSessionThread(info appServerThreadInfo) {
	old := u.session.path(info.ID)
	u.session.registerThread(info)
	u.renameThreadActivity(old, u.session.paths[info.ID])
	// Metadata can arrive after descendant events. Reconcile their display names
	// from the same host records without changing the host's canonical paths.
	for thread, child := range u.session.threads {
		if thread == info.ID || child.ParentThreadID == "" {
			continue
		}
		for parent, remaining := child.ParentThreadID, len(u.session.threads); parent != "" && remaining > 0; remaining-- {
			if parent == info.ID {
				old := u.session.paths[thread]
				path := u.session.threadPresentationPath(child)
				if old != path {
					u.session.agent(old).Name = path
					u.session.paths[thread] = path
					u.renameThreadActivity(old, path)
				}
				break
			}
			parent = u.session.threads[parent].ParentThreadID
		}
	}
}

func (s *appServerSession) threadPresentationPath(info appServerThreadInfo) string {
	path := info.agentPath
	if path == "" && info.AgentNickname != "" {
		path = "/root/" + info.AgentNickname
	}
	if path != "" {
		return s.scopedAgentPath(info.ID, path)
	}
	return cmp.Or(s.paths[info.ID], appServerPlaceholder(info.ID))
}

// Durable native ancestry can precede both host metadata and a child's own
// retention lease. A path or the coordinator cwd alone cannot prove ownership.
func (u *appServerUI) journalDescendantWorkspace(thread string) (string, error) {
	workspace := u.session.cwd
	found := false
	err := u.proxy.journals.transaction(u.ctx, u.proxy.replayStore, workspace, u.thread, func(root *threadJournal, exists bool) error {
		if !exists || !root.IdentityKnown || root.IdentityConflicted || root.Parent != "" {
			return errJournalUnchanged
		}
		seen := map[string]bool{}
		for current := thread; current != "" && !seen[current]; {
			seen[current] = true
			j, exists, err := readThreadJournal(u.proxy.replayStore, workspace, current)
			if err != nil {
				return err
			}
			if !exists || !j.IdentityKnown || j.IdentityConflicted {
				break
			}
			if current == u.thread {
				found = true
				break
			}
			current = j.Parent
		}
		return errJournalUnchanged
	})
	if err != nil {
		return "", err
	}
	if !found {
		return "", errNoRetainedJournalWorkspace
	}
	return workspace, nil
}
