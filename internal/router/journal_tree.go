package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxJournalEvents = 4096
const maxJournalEventBytes = 8 << 20

type journalStamp struct {
	Seq uint64 `json:"seq"`
	At  string `json:"at,omitempty"`
}

type journalNode struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
	// SupersededBy names the node that replaced this one; the node itself stays
	// as history, but reads and recovery no longer present it as current.
	SupersededBy string        `json:"superseded_by,omitempty"`
	Question     string        `json:"question,omitempty"`
	Agent        string        `json:"agent,omitempty"`
	Turns        int           `json:"turns,omitzero"`
	Author       string        `json:"author"`
	Created      journalStamp  `json:"created"`
	Updated      journalStamp  `json:"updated"`
	Started      *journalStamp `json:"started,omitempty"`
	Finished     *journalStamp `json:"finished,omitempty"`
	Children     []journalNode `json:"children"`
}

type journalEvent struct {
	ResetTurn      string      `json:"reset_turn,omitempty"`
	RootRetraction bool        `json:"root_retraction,omitzero"`
	Legacy         bool        `json:"legacy,omitzero"`
	Seq            uint64      `json:"seq"`
	At             string      `json:"at,omitempty"`
	Author         string      `json:"author"`
	Op             string      `json:"op"`
	Path           string      `json:"path"`
	Fields         journalNode `json:"fields"`
	Transition     bool        `json:"transition,omitzero"`
}

func journalParent(path string) string {
	parent, _, _ := strings.CutLast(path, "/")
	return parent
}

func (j *threadJournal) ensureTree() {
	if j.NextOrdinal == nil {
		j.NextOrdinal = make(map[string]uint64)
	}
	for i := range j.Items {
		item := &j.Items[i]
		if item.Path != "" {
			continue
		}
		j.NextOrdinal[""]++
		item.Path = "/" + strconv.FormatUint(j.NextOrdinal[""], 10)
		setLegacyJournalContent(item)
	}
	if j.Version == 1 {
		j.Version = 2
		for _, item := range j.Items {
			j.Events = append(j.Events, journalEvent{Legacy: true, Seq: item.Updated, Author: item.Author, Op: "add", Path: item.Path, Fields: item.node()})
		}
		slices.SortFunc(j.Events, func(a, b journalEvent) int {
			if a.Seq < b.Seq {
				return -1
			}
			if a.Seq > b.Seq {
				return 1
			}
			return 0
		})
		for _, item := range j.Items {
			if item.Reported || item.Flushed {
				j.acknowledgeLegacyPath(item.Path, item.Updated, item.Flushed)
			}
		}
		j.advanceLegacyCursors()

	}
}

func (item journalItem) node() journalNode {
	return journalNode{Path: item.Path, Kind: item.Kind, Title: item.Title, Body: item.Body, State: item.State, Reason: item.Reason, SupersededBy: item.SupersededBy, Question: item.Question, Agent: item.Agent, Turns: item.Turns, Author: item.Author,
		Created: journalStamp{Seq: item.Created, At: item.CreatedAt}, Updated: journalStamp{Seq: item.Updated, At: item.UpdatedAt}, Started: item.Started, Finished: item.Finished, Children: []journalNode{}}
}

// item restores a retained event's node; delivery fields start unset.
func (node journalNode) item() journalItem {
	return journalItem{ID: node.Path, Path: node.Path, Kind: node.Kind, Title: node.Title, Body: node.Body, State: node.State, Reason: node.Reason,
		SupersededBy: node.SupersededBy, Question: node.Question, Agent: node.Agent, Author: node.Author, Created: node.Created.Seq, CreatedAt: node.Created.At,
		Updated: node.Updated.Seq, UpdatedAt: node.Updated.At, Started: node.Started, Finished: node.Finished}
}

func (j *threadJournal) treeIndex(path string) int {
	return slices.IndexFunc(j.Items, func(item journalItem) bool { return item.Path == path })
}

func (j *threadJournal) treeParent(path string) error {
	if path == "" {
		return nil
	}
	i := j.treeIndex(path)
	if i < 0 {
		return fmt.Errorf("journal path not found: %s", path)
	}
	if j.Items[i].Kind != "task" {
		// Ordinals are shared by every kind, so a guessed path often lands on a note.
		hint := "omit under for the root"
		if task := journalParent(path); task != "" {
			hint = "use its task " + task + " or " + hint
		}
		return fmt.Errorf("journal parent must be a task: %s is a %s (%q); %s", path, j.Items[i].Kind, j.Items[i].Title, hint)
	}
	return nil
}

// logTarget resolves where a log note attaches. A non-task p attaches to its
// nearest task, and several working leaves share their deepest common task,
// so an under-specified fact is never attributed to an unrelated task.
func (j *threadJournal) logTarget(p string) (string, error) {
	if p != "" {
		i := j.treeIndex(p)
		if i < 0 {
			return "", journalMutationPathError(p)
		}
		if j.Items[i].Kind != "task" {
			return journalParent(p), nil // Only tasks have children.
		}
		return p, nil
	}
	var leaves []string
	for _, item := range j.Items {
		if item.Kind != "task" || item.State != "working" || item.Agent != "" {
			continue
		}
		if !slices.ContainsFunc(j.Items, func(child journalItem) bool {
			return child.Kind == "task" && strings.HasPrefix(child.Path, item.Path+"/")
		}) {
			leaves = append(leaves, item.Path)
		}
	}
	if len(leaves) == 0 {
		return "", nil
	}
	common := leaves[0]
	for _, leaf := range leaves[1:] {
		for common != "" && leaf != common && !strings.HasPrefix(leaf, common+"/") {
			common = journalParent(common)
		}
	}
	return common, nil
}

func journalMutationPathError(p string) error {
	if strings.Contains(p, "/@") {
		return fmt.Errorf("journal path %s is a read-only mounted view; record a note or update your owned task", p)
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("journal path not found: %s; use the returned path starting with /, not a plan position; recover task paths with read view tasks", p)
	}
	return fmt.Errorf("journal path not found: %s; recover task paths with read view tasks, or every node's path with read view outline", p)
}

func journalReadOnlyError(item journalItem) error {
	return fmt.Errorf("%s: router-owned journal node is read-only (%s %.80q); record a note or update your owned task", item.Path, item.Kind, item.Title)
}

func validJournalState(state string) bool {
	return slices.Contains([]string{"pending", "working", "done", "blocked", "dropped"}, state)
}

func (j *threadJournal) treeEvent(op string, index int, transition bool) error {
	if j.Sequence == ^uint64(0) {
		return errors.New("journal sequence exhausted")
	}
	j.Sequence++
	stamp := journalStamp{Seq: j.Sequence, At: time.Now().UTC().Format(time.RFC3339Nano)}
	item := &j.Items[index]
	item.Updated, item.UpdatedAt = stamp.Seq, stamp.At
	if item.Created == 0 {
		item.Created, item.CreatedAt = stamp.Seq, stamp.At
	}
	if item.State == "working" && item.Started == nil {
		item.Started = &stamp
	}
	if transition {
		item.Finished = nil
		if item.State == "done" || item.State == "dropped" {
			item.Finished = &stamp
		}
	}
	item.Reported, item.Flushed, item.ReportNow = false, false, !item.TerminalOnly
	item.Text = item.Title
	if item.Body != "" {
		item.Text += "\n\n" + item.Body
	}
	event := journalEvent{Seq: stamp.Seq, At: stamp.At, Author: j.Author, Op: op, Path: item.Path, Fields: item.node(), Transition: transition}
	return j.appendEvent(event)

}

func (j *threadJournal) applyTree(m journalMutation) ([]string, error) {
	j.ensureTree()
	j.TreeAuthored = true
	if m.ID != "" || m.Answer != nil || m.ReportNow {
		return nil, errors.New("v2 journal operations do not accept legacy fields")
	}
	if err := validateTreeMutation(m); err != nil {
		return nil, err
	}
	switch m.Op {
	case "plan":
		return j.applyPlan(m)
	case "log":
		if m.Text == nil || strings.TrimSpace(*m.Text) == "" {
			return nil, errors.New("journal log requires text")
		}
		under, err := j.logTarget(m.P)
		if err != nil {
			return nil, err
		}
		title, body, _ := strings.Cut(strings.TrimSpace(*m.Text), "\n")
		title = strings.TrimSuffix(title, "\r") // CRLF text keeps a one-line title.
		return j.applyTree(journalMutation{Op: "add", Under: under, Kind: "note", Title: &title, Body: new(strings.TrimSpace(body))})
	case "add":
		if err := j.treeParent(m.Under); err != nil {
			return nil, err
		}
		if len(j.Items) >= maxJournalItems {
			return nil, errJournalItemLimit
		}
		kind := m.Kind
		if kind == "" {
			kind = "note"
		}
		if kind != "task" && kind != "note" && kind != "context" {
			return nil, errors.New("kind must be task, note, or context")
		}
		if m.Title == nil {
			return nil, errors.New("journal add requires title")
		}
		if j.NextOrdinal[m.Under] == ^uint64(0) {
			return nil, errors.New("journal ordinal exhausted")
		}
		j.NextOrdinal[m.Under]++
		path := m.Under + "/" + strconv.FormatUint(j.NextOrdinal[m.Under], 10)
		item := journalItem{ID: path, Path: path, Kind: kind, Title: *m.Title, Author: j.Author}
		if m.Body != nil {
			item.Body = *m.Body
		}
		if m.State != nil {
			item.State = *m.State
		} else if kind == "task" {
			item.State = "pending"
		}
		if m.Reason != nil {
			item.Reason = *m.Reason
		}
		if err := j.bindAgent(&item, m.Agent); err != nil {
			return nil, err
		}
		index := len(j.Items)
		if m.Before != "" {
			index = j.treeIndex(m.Before)
			if index < 0 || journalParent(m.Before) != m.Under {
				return nil, errors.New("before must name a sibling")
			}
		}
		j.Items = slices.Insert(j.Items, index, item)
		if err := j.treeEvent("add", index, item.Kind == "task"); err != nil {
			return nil, err
		}
		return []string{path}, nil
	case "set":
		index := j.treeIndex(m.P)
		if index < 0 {
			return nil, journalMutationPathError(m.P)
		}
		item := &j.Items[index]
		if item.Kind == "answer" {
			return nil, journalReadOnlyError(*item)
		}
		if err := j.bindAgent(item, m.Agent); err != nil {
			return nil, err
		}
		transition := m.State != nil && *m.State != item.State
		if transition && (item.State == "done" || item.State == "dropped") && *m.State != "working" {
			return nil, errors.New("reopen a final task with state working")
		}
		if m.Title != nil {
			item.Title = *m.Title
		}
		if m.Body != nil {
			item.Body = *m.Body
		}
		if m.Reason != nil {
			item.Reason = *m.Reason
		}
		if m.SupersededBy != nil {
			item.SupersededBy = *m.SupersededBy
		}
		if m.State != nil {
			item.State = *m.State
			if item.State != "blocked" && item.State != "dropped" {
				item.Reason = ""
			}
		}
		if err := j.treeEvent("set", index, transition); err != nil {
			return nil, err
		}
		// Only an explicit task reactivation in a later user-driven turn releases
		// a user stop. Status notes and unrelated new work cannot revive it.
		if m.State != nil && *m.State == "working" && j.TurnID != "" {
			for path, turn := range j.StoppedTasks {
				if turn != j.TurnID && (path == m.P || strings.HasPrefix(path, m.P+"/")) {
					delete(j.StoppedTasks, path)
				}
			}
		}
		return []string{m.P}, nil
	case "remove":
		index := j.treeIndex(m.P)
		if index < 0 {
			return nil, journalMutationPathError(m.P)
		}
		if j.Items[index].Kind == "answer" {
			return nil, journalReadOnlyError(j.Items[index])
		}
		if slices.ContainsFunc(j.Items, func(item journalItem) bool {
			return item.Agent != "" && (item.Path == m.P || strings.HasPrefix(item.Path, m.P+"/"))
		}) {
			return nil, fmt.Errorf("%s: journal subtree contains an immutable agent binding; update the owned task instead of removing it", m.P)
		}
		if err := j.treeEvent("remove", index, false); err != nil {
			return nil, err
		}
		for _, item := range j.Items {
			if item.Path == m.P || strings.HasPrefix(item.Path, m.P+"/") {
				delete(j.StoppedTasks, item.Path)
				j.Retractions = append(j.Retractions, journalRetraction{ID: item.ID, Sequence: j.Sequence})
			}
		}
		j.Items = slices.DeleteFunc(j.Items, func(item journalItem) bool { return item.Path == m.P || strings.HasPrefix(item.Path, m.P+"/") })
		return []string{m.P}, nil
	default:
		return nil, errors.New("journal op must be plan, add, set, log, remove, or read")
	}
}

type journalPlanTask struct {
	P      string           `json:"p,omitempty"`
	Title  string           `json:"title"`
	State  *string          `json:"state,omitempty"`
	Body   *string          `json:"body,omitempty"`
	Reason *string          `json:"reason,omitempty"`
	Tasks  []jsontext.Value `json:"tasks,omitempty"`
}

func (j *threadJournal) applyPlan(m journalMutation) ([]string, error) {
	if err := j.treeParent(m.Under); err != nil {
		return nil, err
	}
	if m.Tasks == nil {
		return nil, errors.New("journal plan requires tasks")
	}
	if m.Reset != "" && m.Reset != "slice" {
		return nil, errors.New("journal reset must be slice")
	}
	if m.Reset == "slice" {
		if j.SliceParents == nil {
			j.SliceParents = make(map[string]bool)
		}
		j.SliceParents[m.Under] = true
	}
	previous := slices.Clone(j.Items)
	listed := make(map[string]bool)
	var paths []string
	for _, raw := range m.Tasks {
		var task journalPlanTask
		var title string
		if err := json.Unmarshal(raw, &title); err == nil {
			task.Title = title
		} else if err := json.Unmarshal(raw, &task, json.RejectUnknownMembers(true)); err != nil {
			return nil, fmt.Errorf("invalid planned task: %w", err)
		}
		path := task.P
		var created []string
		var err error
		if path == "" {
			created, err = j.applyTree(journalMutation{Op: "add", Under: m.Under, Kind: "task", Title: &task.Title, Body: task.Body, State: task.State, Reason: task.Reason})
			if err == nil {
				path = created[0]
				paths = append(paths, path)
			}
		} else {
			index := j.treeIndex(path)
			if index < 0 || j.Items[index].Kind != "task" || journalParent(path) != m.Under || listed[path] {
				return nil, fmt.Errorf("plan p must name a distinct direct task child: %s", path)
			}
			_, err = j.applyTree(journalMutation{Op: "set", P: path, Title: &task.Title, Body: task.Body, State: task.State, Reason: task.Reason})
		}
		if err != nil {
			return nil, err
		}
		listed[path] = true
		if task.Tasks != nil {
			nested, err := j.applyPlan(journalMutation{Op: "plan", Under: path, Tasks: task.Tasks})
			if err != nil {
				return nil, err
			}
			paths = append(paths, nested...)
		}
	}
	for _, item := range previous {
		if item.Kind == "task" && item.State == "pending" && journalParent(item.Path) == m.Under && !listed[item.Path] {
			if _, err := j.applyTree(journalMutation{Op: "set", P: item.Path, State: new("dropped"), Reason: new("Removed from the plan")}); err != nil {
				return nil, err
			}
		}
	}
	return paths, nil
}

func (j *threadJournal) validateTree() error {
	var open []string
	for _, item := range j.Items {
		if err := j.validateSupersession(item); err != nil {
			return err
		}
		contentBytes := len(item.Title) + len(item.Body) + len(item.Reason) + len(item.Question)
		if item.ID != item.Path {
			contentBytes = len(item.Text) + len(item.Question) + len(item.Reason)
		}
		if strings.TrimSpace(item.Title) == "" || strings.ContainsAny(item.Title, "\r\n") || !utf8.ValidString(item.Title+item.Body+item.Reason+item.Question) || contentBytes > maxJournalItemBytes {
			return fmt.Errorf("%s: title must be one nonblank UTF-8 line and node content at most 16 KiB", item.Path)
		}
		if item.Kind != "task" {
			if item.State != "" || item.Reason != "" {
				direction := "create kind task for work with a state, or edit this node's title/body"
				if parent := journalParent(item.Path); parent != "" {
					direction = "set the containing task " + parent + " instead, or edit this node's title/body"
				}
				return fmt.Errorf("%s: only tasks have state or reason (%s %.80q); %s", item.Path, item.Kind, item.Title, direction)
			}
			continue
		}
		if !validJournalState(item.State) {
			return fmt.Errorf("%s: invalid task state", item.Path)
		}
		if (item.State == "blocked" || item.State == "dropped") && strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("%s: %s requires reason", item.Path, item.State)
		}
		if item.State == "done" {
			for _, child := range j.Items {
				if child.Kind == "task" && strings.HasPrefix(child.Path, item.Path+"/") && child.State != "done" && child.State != "dropped" {
					open = append(open, child.Path)
				}
			}
		}
	}
	if len(open) != 0 {
		slices.Sort(open)
		return fmt.Errorf("done tasks have open descendants: %s", strings.Join(slices.Compact(open), ", "))
	}
	return nil
}

// A superseded node remains history. Its replacement must stay addressable,
// and an open task is replaced by dropping it, not by hiding it from recovery.
func (j *threadJournal) validateSupersession(item journalItem) error {
	by := item.SupersededBy
	if by == "" {
		return nil
	}
	if item.Kind == "task" && item.State != "done" && item.State != "dropped" {
		return fmt.Errorf("%s: only a done or dropped task can be superseded; drop it with a reason, or finish it first", item.Path)
	}
	if by == item.Path || strings.HasPrefix(by, item.Path+"/") {
		return fmt.Errorf("%s: superseded_by must name a node outside this subtree, not %s", item.Path, by)
	}
	if j.treeIndex(by) < 0 {
		return fmt.Errorf("%s: superseded_by %s does not exist; name the replacement's returned path, or clear it with \"\" in the same batch that removes the replacement", item.Path, by)
	}
	// Recovery hides a superseded subtree, so it must not hide open work.
	for _, child := range j.Items {
		if child.Kind == "task" && strings.HasPrefix(child.Path, item.Path+"/") && child.State != "done" && child.State != "dropped" {
			return fmt.Errorf("%s: superseded subtree has open task %s; finish or drop it in the same batch", item.Path, child.Path)
		}
	}
	// A reader follows pointers, including a superseded ancestor's, to the
	// current owner. A cycle would hide every node on it from recovery.
	for current, steps := by, 0; current != "" && steps <= len(j.Items); steps++ {
		if current == item.Path || strings.HasPrefix(current, item.Path+"/") {
			return fmt.Errorf("%s: superseded_by %s leads back to this node; point at the node that now owns the topic", item.Path, by)
		}
		current = j.supersededTarget(current)
	}
	return nil
}

// supersededTarget is where a reader of path is sent: its own replacement,
// otherwise its nearest superseded ancestor's.
func (j *threadJournal) supersededTarget(path string) string {
	for ; path != ""; path = journalParent(path) {
		if index := j.treeIndex(path); index >= 0 && j.Items[index].SupersededBy != "" {
			return j.Items[index].SupersededBy
		}
	}
	return ""
}

func journalTree(items []journalItem, path string, depth *int) ([]journalNode, error) {
	if depth != nil && *depth < 0 {
		return nil, errors.New("journal depth must be nonnegative")
	}
	var build func(string, int) []journalNode
	build = func(parent string, level int) []journalNode {
		nodes := []journalNode{}
		for _, item := range items {
			if journalParent(item.Path) != parent {
				continue
			}
			node := item.node()
			if depth == nil || level < *depth {
				node.Children = build(item.Path, level+1)
			}
			nodes = append(nodes, node)
		}
		return nodes
	}
	if path == "" {
		return build("", 0), nil
	}
	for _, item := range items {
		if item.Path == path {
			node := item.node()
			if depth == nil || *depth > 0 {
				node.Children = build(path, 1)
			}
			return []journalNode{node}, nil
		}
	}
	return nil, fmt.Errorf("journal path not found: %s; omit p to recover paths in the selected agent and view", path)
}

func validateTreeMutation(m journalMutation) error {
	if m.Agent != "" && m.Op != "set" && m.Op != "add" {
		return errors.New("agent binding is only supported by task add and set")
	}
	if m.SupersededBy != nil && m.Op != "set" {
		return errors.New("superseded_by is only supported by set")
	}
	extra := false
	switch m.Op {
	case "plan":
		extra = m.P != "" || m.Kind != "" || m.Title != nil || m.Body != nil || m.State != nil || m.Reason != nil || m.Before != "" || m.Text != nil
	case "add":
		extra = m.P != "" || m.Tasks != nil || m.Reset != "" || m.Text != nil
	case "set":
		extra = m.Under != "" || m.Kind != "" || m.Before != "" || m.Tasks != nil || m.Reset != "" || m.Text != nil
	case "log":
		extra = m.Under != "" || m.Kind != "" || m.Title != nil || m.Body != nil || m.State != nil || m.Reason != nil || m.Before != "" || m.Tasks != nil || m.Reset != ""
	case "remove":
		extra = m.Under != "" || m.Kind != "" || m.Title != nil || m.Body != nil || m.State != nil || m.Reason != nil || m.Before != "" || m.Tasks != nil || m.Reset != "" || m.Text != nil
	}
	if extra {
		return fmt.Errorf("journal %s has fields for another operation", m.Op)
	}
	return nil
}

func (j *threadJournal) bindAgent(item *journalItem, agent string) error {
	if agent == "" || agent == item.Agent {
		return nil
	}
	if item.Agent != "" {
		return errors.New("journal agent binding is immutable")
	}
	if item.Kind != "task" || journalParent(agent) != j.Author || strings.HasSuffix(agent, "/") || strings.TrimSpace(agent) != agent || !utf8.ValidString(agent) {
		return errors.New("agent must name a direct child on a task")
	}
	if slices.ContainsFunc(j.Items, func(other journalItem) bool { return other.Agent == agent }) {
		return errors.New("child journal is already mounted")
	}
	item.Agent = agent
	return nil
}

// The display title is derived; the original Markdown remains the body.
func setLegacyJournalContent(item *journalItem) {
	item.Title = "Note"
	item.Kind = "note"
	if item.TerminalOnly {
		item.Kind = "answer"
		item.Title = "Outcome"
	}
	item.Body = item.Text
}

func (j *threadJournal) appendEvent(event journalEvent) error {
	size := journalEventSize(event)
	for _, e := range j.Events {
		size += journalEventSize(e)
	}
	if len(j.Events) >= maxJournalEvents || size > maxJournalEventBytes {
		compact := make([]journalEvent, 0, len(j.Events))
		for _, e := range j.Events {
			n := len(compact)
			if n > 0 && e.Op == "set" && !e.Transition && compact[n-1].Op == "set" && !compact[n-1].Transition && compact[n-1].Path == e.Path {
				compact[n-1] = e
			} else {
				compact = append(compact, e)
			}
		}
		j.Events = compact
		size = journalEventSize(event)
		for _, e := range j.Events {
			size += journalEventSize(e)
		}
	}
	if len(j.Events) >= maxJournalEvents || size > maxJournalEventBytes {
		return errJournalEventLimit
	}
	j.Events = append(j.Events, event)
	return nil
}

func journalEventSize(event journalEvent) int {
	node := event.Fields
	return len(node.Title) + len(node.Body) + len(node.Reason) + len(node.Question) + len(event.Path) + len(event.Author) + 256
}

// Sparse v1/native receipts bridge historical item delivery to ordered cursors.
// They disappear as soon as an event-window delivery covers the gap.
func (j *threadJournal) acknowledgeLegacyPath(path string, revision uint64, terminal bool) {
	for _, event := range j.Events {
		if event.Path != path || event.Seq > revision {
			continue
		}
		if event.Seq > j.LiveSeq {
			if j.LegacyLive == nil {
				j.LegacyLive = make(map[uint64]bool)
			}
			j.LegacyLive[event.Seq] = true
		}
		if terminal && event.Seq > j.FlushSeq {
			if j.LegacyFlush == nil {
				j.LegacyFlush = make(map[uint64]bool)
			}
			j.LegacyFlush[event.Seq] = true
		}
	}
}

func (j *threadJournal) advanceLegacyCursors() {
	for _, event := range j.Events {
		if event.Seq <= j.LiveSeq {
			continue
		}
		if !j.LegacyLive[event.Seq] {
			break
		}
		j.LiveSeq = event.Seq
	}
	for _, event := range j.Events {
		if event.Seq <= j.FlushSeq {
			continue
		}
		if !j.LegacyFlush[event.Seq] {
			break
		}
		j.FlushSeq = event.Seq
	}
	for seq := range j.LegacyLive {
		if seq <= j.LiveSeq {
			delete(j.LegacyLive, seq)
		}
	}
	for seq := range j.LegacyFlush {
		if seq <= j.FlushSeq {
			delete(j.LegacyFlush, seq)
		}
	}
}
