package capture

// Source: apps/mekugi/internal/router/exec_observation.go:38:1250@1e6d58c6.

import (
	"cmp"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	changes "github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

const (
	// execCaptureHold bounds how long a call item waits for its pre-call
	// capture. Unread paths become incomplete instead of delaying the host.
	execCaptureHold = 500 * time.Millisecond
	// MaxContentBytes bounds the text read on each side of a call.
	MaxContentBytes = 8 << 20
	// maxExecObservationBytes bounds the encoded pre-call capture that the call
	// record carries until its result arrives.
	maxExecObservationBytes = 8 << 20
	// maxExecRecordBytes keeps the encoded reviews of a derived record below
	// the store's record limit.
	maxExecRecordBytes    = 24 << 20
	maxExecListingEntries = execscope.ListingLimit
)

type FileKind string

const (
	Absent    FileKind = ""
	Text      FileKind = "text"
	Binary    FileKind = "binary"
	Symlink   FileKind = "symlink"
	Directory FileKind = "dir"
	Other     FileKind = "other"
)

// File is the bounded state of one path. Binary content keeps only
// size and hash; symlinks keep their target, never the target's content.
type File struct {
	Origin      string `json:",omitempty"`
	AlsoManaged string `json:",omitempty"`
	Path        string
	Kind        FileKind `json:",omitempty"`
	Content     string   `json:",omitempty"`
	Size        int64    `json:",omitzero"`
	Hash        string   `json:",omitempty"`
	Link        string   `json:",omitempty"`
	// Stamp identifies an uncaptured regular file, so an unchanged file is not
	// reported just because its content was over a bound.
	Stamp string `json:",omitempty"`
	Error string `json:",omitempty"`
	// WatchStamp is the live preview's capture-time metadata baseline. It is
	// not durable evidence and must not replace content comparison.
	WatchStamp string `json:"-"`
	// Move-only operands have retained evidence, but no arriving source to stream.
	WatchMoveOnly bool `json:"-"`
	// CopyOf names the source a copy command writes into this path.
	CopyOf string `json:",omitempty"`
}

// Omission records scope that was not captured at all.
type Omission struct {
	Origin string `json:",omitempty"`
	Path   string
	Reason string
	// Existing explicitly scoped fixer or formatter paths were not baselined. Their
	// after-state is checked against the observation clock before reporting.
	Deferred bool `json:",omitzero"`
}

// Observation is the durable pre-call capture of one observed call: a
// native exec_command, or all literal commands of one exec cell.
type Observation struct {
	Commands []commandanalysis.Command
	// CommandClasses retain pre-call classification for repeated literal
	// occurrences, without reclassifying providers after execution.
	CommandClasses []string `json:",omitempty"`
	Class          string
	Labels         []string `json:",omitempty"`
	Reason         string   `json:",omitempty"`
	Files          []File
	Omitted        []Omission          `json:",omitempty"`
	Listings       []Listing           `json:",omitempty"`
	CodeMode       bool                `json:",omitzero"`
	Roots          []string            `json:",omitempty"`
	WindowStart    time.Time           `json:",omitzero"`
	Programs       []execscope.Program `json:",omitempty"`
	// Group identifies the response that emitted the call, so parallel
	// siblings finalized together share one record.
	Group string `json:",omitempty"`
	// Excluded paths belong to patch records of the same call.
	Excluded []string `json:",omitempty"`
	// RepeatedPaths names multiple potential writes in one call window. A
	// final equality cannot prove those intermediate attempts had no effects.
	RepeatedPaths bool `json:",omitzero"`
	// LiteralClass preserves pre-captured scope independently of uncertainty
	// about additional exec calls whose arguments are not yet resolved.
	LiteralClass string `json:",omitempty"`
	// Inventory is the selected workspace before an opaque call. It is
	// window evidence, separate from the explicit scope above.
	Inventory *Inventory `json:",omitempty"`
	// Tree is the private workspace snapshot taken before a writer call. Its
	// comparison covers effects outside the source-named scope.
	Tree string `json:",omitempty"`
}

// A successful mktemp report supplies the ordinary absent endpoint and empty
// destination listing. Reconciliation reads only that path and its contents,
// which are authored like any named operand.
func (o *Observation) AddCreatedPath(path string, tree bool) {
	// Observation copies may share backing arrays with a running preview.
	o.Files, o.Listings = slices.Clip(o.Files), slices.Clip(o.Listings)
	if !slices.ContainsFunc(o.Files, func(file File) bool { return file.Path == path }) {
		o.Files = append(o.Files, File{Path: path})
	}
	if tree && !slices.ContainsFunc(o.Listings, func(listing Listing) bool { return listing.Root == path }) {
		o.Listings = append(o.Listings, Listing{Root: path})
	}
}

// Member carries the original admission sequence independently of display order.
type Member struct {
	Observation
	Sequence uint64
}

// Merge combines a nonempty set of sibling captures without mutating their storage.
// Original admission sequence selects the checkpoint bounding sibling effects;
// capture clocks and supplied member order cannot establish that boundary.
// Source: apps/mekugi/internal/router/exec_observation.go:163:177@1bcbdd5a.
func Merge(members []Member) Observation {
	merged := members[0].Observation
	if len(members) == 1 {
		return merged
	}
	var tree string
	var earliest uint64
	for _, member := range members {
		if member.Tree != "" && (tree == "" || member.Sequence < earliest) {
			tree, earliest = member.Tree, member.Sequence
		}
	}
	merged.Commands = slices.Clone(merged.Commands)
	merged.CommandClasses = merged.CommandClassesForCalls()
	merged.Labels = slices.Clone(merged.Labels)
	merged.Programs = slices.Clone(merged.Programs)
	merged.Roots = slices.Clone(merged.Roots)
	merged.Files = slices.Clone(merged.Files)
	merged.Omitted = slices.Clone(merged.Omitted)
	merged.Listings = slices.Clone(merged.Listings)
	merged.Excluded = slices.Clone(merged.Excluded)
	for _, member := range members[1:] {
		other := member.Observation
		merged.CodeMode = merged.CodeMode || other.CodeMode
		merged.RepeatedPaths = merged.RepeatedPaths || other.RepeatedPaths
		merged.Commands = append(merged.Commands, other.Commands...)
		merged.CommandClasses = append(merged.CommandClasses, other.CommandClassesForCalls()...)
		if classRank(other.Class) > classRank(merged.Class) {
			merged.Class, merged.Reason = other.Class, other.Reason
		}
		for _, label := range other.Labels {
			if !slices.Contains(merged.Labels, label) {
				merged.Labels = append(merged.Labels, label)
			}
		}
		for _, program := range other.Programs {
			if !slices.Contains(merged.Programs, program) {
				merged.Programs = append(merged.Programs, program)
			}
		}
		for _, root := range other.Roots {
			merged.Roots = AddRoot(merged.Roots, root)
		}
		merged.Files = append(merged.Files, other.Files...)
		merged.Omitted = append(merged.Omitted, other.Omitted...)
		merged.Listings = append(merged.Listings, other.Listings...)
		merged.Excluded = append(merged.Excluded, other.Excluded...)
		if other.WindowStart.IsZero() || !merged.WindowStart.IsZero() && other.WindowStart.Before(merged.WindowStart) {
			merged.WindowStart = other.WindowStart
		}
	}
	merged.Tree = tree
	return merged
}

func classRank(class string) int {
	for rank := execscope.Neutral; rank <= execscope.Opaque; rank++ {
		if rank.String() == class {
			return int(rank)
		}
	}
	return int(execscope.Opaque)
}

// Outcome is the finalized status of an observed exec record.
type Outcome struct {
	DeletedDirectories []string `json:",omitempty"` // Captured directory roots absent after the call.
	OutputRef          string   `json:",omitempty"` // Router-retained failed host result, readable with mread.
	Status             string
	Exit               *int `json:",omitempty"`
	Class              string
	Labels             []string `json:",omitempty"`
	Coverage           string
	CodeMode           bool `json:",omitzero"`
	// Scope lists the captured paths, the only paths the record compares
	// exactly.
	Scope       []string `json:",omitempty"`
	ScopeReason string   `json:",omitempty"`
	// Unswept retains command-level coverage diagnostics, not changed files.
	Unswept string `json:",omitempty"`
	// Overlaps are calls whose windows overlapped this one on the same root.
	Overlaps []string `json:",omitempty"`
	// Background names still-running sessions for command-history diagnostics.
	Background []string `json:",omitempty"`
	// SharedWith names the record of a parallel sibling that holds this
	// call's effects.
	SharedWith string `json:",omitempty"`
}

func (o Observation) CommandClassesForCalls() []string {
	classes := slices.Clone(o.CommandClasses)
	// Older or timed-out captures without per-command facts can establish
	// neutrality only for their aggregate original scope.
	for len(classes) < len(o.Commands) {
		classes = append(classes, cmp.Or(o.LiteralClass, o.Class))
	}
	return classes
}

const (
	StatusCompleted = "completed"
	StatusFailed    = "failed"

	CoverageExact   = "exact"
	CoveragePartial = "partial"
)

// Options is the router state a capture consults.
type Options struct {
	// Workspaces supplies the invocation-owned fallback for unnamed effects.
	Workspaces *Workspaces
	// PreviewOnly keeps source-derived display baselines out of durable edits.
	PreviewOnly bool
	// Directory is the selected workspace for resolving explicit edit operands.
	Directory string
	// Excluded paths belong to patch records of the same call.
	Excluded []string
	// Changes scopes mchanges revert and apply by their recorded paths.
	Changes execscope.ChangeResolver
}

// Capture captures named scope and, for writers, a selected-workspace checkpoint.
// Neutral readers and calls with no available baseline stay unobserved.
func Capture(ctx context.Context, commands []commandanalysis.Command, dynamic, codeMode bool, env Options) (*Observation, bool) {
	observation, observed := observeCommands(commands, dynamic, codeMode, env)
	if !env.PreviewOnly && observation != nil && observation.Class != execscope.Neutral.String() {
		if tree := env.Workspaces.Checkpoint(ctx, env.Directory); tree != "" {
			observation.Tree = tree
			observation.Roots = AddRoot(observation.Roots, env.Directory)
			return observation, true
		}
	}
	if !observed {
		return nil, false
	}
	return observation, true
}

// observeCommands classifies commands and captures their scope before
// the host runs them. An unobserved call keeps its classification, unless it
// is neutral, so a writer without a derived scope can still be snapshotted.
func observeCommands(commands []commandanalysis.Command, dynamic, codeMode bool, env Options) (*Observation, bool) {
	if len(commands) == 0 && !dynamic {
		return nil, false
	}
	deadline := time.Now().Add(execCaptureHold)
	type result struct {
		observation *Observation
		observed    bool
	}
	done := make(chan result, 1)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case execCaptureSlots <- struct{}{}:
		go func() {
			defer func() { <-execCaptureSlots }()
			observation, observed := captureExecObservationWithin(commands, dynamic, codeMode, env)
			done <- result{observation, observed}
		}()
	case <-timer.C:
		return incompleteExecCapture(commands, codeMode), true
	}
	select {
	case result := <-done:
		if time.Now().Before(deadline) {
			return result.observation, result.observed
		}
	case <-timer.C:
	}
	// Late reads never enter evidence: the host may already be writing by then.
	return incompleteExecCapture(commands, codeMode), true
}

// Isolate potentially stalled filesystem syscalls from forwarding while keeping
// outstanding auxiliary workers bounded across requests and threads.
var execCaptureSlots = make(chan struct{}, 16)

func incompleteExecCapture(commands []commandanalysis.Command, codeMode bool) *Observation {
	return &Observation{Commands: commands, CodeMode: codeMode, Class: execscope.Opaque.String(), Reason: "capture deadline"}
}

func captureExecObservationWithin(commands []commandanalysis.Command, dynamic, codeMode bool, env Options) (*Observation, bool) {
	started := time.Now()
	if len(commands) == 0 && !dynamic {
		return nil, false
	}
	observation := &Observation{Commands: commands, CodeMode: codeMode}
	class := execscope.Neutral
	var scope []execscope.Entry
	for _, command := range commands {
		plan := execscope.Classify(execscope.Input{Command: command.Command, Directory: command.Workdir, Shell: command.Shell, Deadline: started.Add(execscope.Budget), Changes: env.Changes, AuthoredOnly: !env.PreviewOnly})
		observation.CommandClasses = append(observation.CommandClasses, plan.Class.String())
		if plan.Class > class {
			class = plan.Class
		}
		if plan.Reason != "" && !strings.Contains(observation.Reason, plan.Reason) {
			if observation.Reason != "" {
				observation.Reason += "; "
			}
			observation.Reason += plan.Reason
		}
		for _, label := range plan.Labels {
			if !slices.Contains(observation.Labels, label) {
				observation.Labels = append(observation.Labels, label)
			}
		}
		for _, program := range plan.Programs {
			if !slices.Contains(observation.Programs, program) {
				observation.Programs = append(observation.Programs, program)
			}
		}
		scope = append(scope, plan.Scope...)
		observation.Roots = AddRoot(observation.Roots, execCaptureRoot(command.Workdir, env.Directory))
	}
	if dynamic {
		observation.LiteralClass = class.String()
		class, observation.Reason = execscope.Opaque, "exec cell has a non-literal command"
		observation.Programs = append(observation.Programs, execscope.Program{Label: "exec command"})
		if filepath.IsAbs(env.Directory) {
			observation.Roots = AddRoot(observation.Roots, filepath.Clean(env.Directory))
		}
	}
	observation.Class = class.String()
	observation.Excluded = env.Excluded
	if len(scope) == 0 {
		if class == execscope.Neutral {
			return nil, false
		}
		return observation, false
	}
	seenWrites := make(map[string]bool)
	for _, entry := range scope {
		for _, operand := range entry.Operands {
			if !operand.Glob {
				observation.RepeatedPaths = observation.RepeatedPaths || seenWrites[operand.Path]
				seenWrites[operand.Path] = true
			}
		}
	}
	capture := newExecCapture(started.Add(execCaptureHold))
	capture.scopeRoots = observation.Roots
	for _, managed := range []bool{false, true} {
		for _, entry := range scope {
			tool, _, _ := strings.Cut(entry.Origin, " ")
			if (entry.Origin != "") == managed && (env.PreviewOnly || entry.Origin == "" || commandanalysis.IsVCSTool(tool)) {
				capture.entry(entry)
			}
		}
	}
	observation.Files, observation.Omitted, observation.Listings = capture.files, capture.omitted, capture.listings
	boundExecObservation(observation)
	if len(observation.Files) == 0 && len(observation.Omitted) == 0 && len(observation.Listings) == 0 {
		return observation, false
	}
	return observation, true
}

// execCaptureRoot bounds deferred formatter checks to the selected workspace
// or the command directory when no workspace is available.
func execCaptureRoot(workdir, directory string) string {
	if filepath.IsAbs(directory) {
		return filepath.Clean(directory)
	}
	return workdir
}

// AddRoot adds root unless an existing root contains it, and drops
// roots it contains.
func AddRoot(roots []string, root string) []string {
	for _, existing := range roots {
		if changes.PathWithin(root, existing) {
			return roots
		}
	}
	roots = slices.DeleteFunc(roots, func(existing string) bool { return changes.PathWithin(existing, root) })
	return append(roots, root)
}

// scopePaths are the paths a capture claims: files it compares and roots it
// lists. Concurrent sweeps leave them to this record.
func (o Observation) ScopePaths() []string {
	var paths []string
	for _, file := range o.Files {
		paths = append(paths, file.Path)
	}
	for _, listing := range o.Listings {
		paths = append(paths, listing.Root)
	}
	for _, omission := range o.Omitted {
		if !omission.Deferred {
			paths = append(paths, omission.Path)
		}
	}
	return paths
}

// boundExecObservation keeps the encoded capture within its record share by
// reducing the largest text snapshots to stamps.
func boundExecObservation(observation *Observation) {
	total := len(responses.MustMarshalJSON(observation))
	for total > maxExecObservationBytes {
		largest := -1
		for index, file := range observation.Files {
			if file.Content != "" && (largest < 0 || len(file.Content) > len(observation.Files[largest].Content)) {
				largest = index
			}
		}
		if largest < 0 {
			return
		}
		file := &observation.Files[largest]
		before := len(responses.MustMarshalJSON(file))
		file.Content, file.Error = "", "capture exceeds the record bound"
		if info, err := os.Lstat(file.Path); err == nil {
			file.Stamp = execFileStamp(info)
		}
		total -= before - len(responses.MustMarshalJSON(file))
	}
}

// Listing names the files below one destination root before the call, so
// files a recursive copy or move creates there are found afterwards.
type Listing struct {
	Origin string `json:",omitempty"`
	Root   string
	// Entries maps each file's relative path to its stamp.
	Entries map[string]string `json:",omitempty"`
}

type execCapture struct {
	origin       string
	moveOnly     bool
	deadline     time.Time
	seen         map[string]bool
	files        []File
	omitted      []Omission
	omittedPaths map[string]bool
	listings     []Listing
	scopeRoots   []string
	listed       int
	// removals are trees an earlier statement deletes or moves away.
	removals []string
	budget   int
}

func newExecCapture(deadline time.Time) *execCapture {
	return &execCapture{deadline: deadline, seen: make(map[string]bool), omittedPaths: make(map[string]bool), budget: MaxContentBytes}
}

func (c *execCapture) omit(path, reason string) {
	if c.omittedPaths[path] {
		return
	}
	c.omittedPaths[path] = true
	c.omitted = append(c.omitted, Omission{Path: path, Reason: reason, Origin: c.origin})
}

// exhausted names the bound that stops further capture.
func (c *execCapture) exhausted() string {
	if time.Now().After(c.deadline) {
		return "capture deadline"
	}
	return ""
}

// add snapshots one path once. Past the capture deadline the path is
// recorded as omitted, never silently dropped.
func (c *execCapture) add(path string) {
	c.addCopy(path, "")
}

func (c *execCapture) addCopy(path, source string) {
	path = filepath.Clean(path)
	if c.seen[path] {
		for i := range c.files {
			file := &c.files[i]
			if file.Path != path {
				continue
			}
			file.WatchMoveOnly = file.WatchMoveOnly && c.moveOnly
			// A direct path may fall in several managed scopes.
			if c.origin != "" && file.Origin == "" && !slices.Contains(strings.Split(file.AlsoManaged, ", "), c.origin) {
				file.AlsoManaged = strings.TrimPrefix(file.AlsoManaged+", "+c.origin, ", ")
			}
			break
		}
		return
	}
	c.seen[path] = true
	if reason := c.exhausted(); reason != "" {
		// Explicit fixer and formatter paths were enumerated but not baselined.
		// Check their after-state against the window clock instead of
		// fabricating a changed-file review for every unread path.
		if slices.Contains([]string{"go fix", "gofmt", "goimports", "prettier", "eslint", "ruff format", "ruff check", "black", "rustfmt", "cargo fmt"}, c.origin) &&
			slices.ContainsFunc(c.scopeRoots, func(root string) bool { return changes.PathWithin(path, root) }) {
			if !c.omittedPaths[path] {
				c.omittedPaths[path] = true
				c.omitted = append(c.omitted, Omission{Path: path, Reason: reason, Origin: c.origin, Deferred: true})
			}
			return
		}
		c.omit(path, reason)
		return
	}
	snapshot := ReadFile(path, &c.budget)
	snapshot.CopyOf = source
	snapshot.Origin = c.origin
	snapshot.WatchMoveOnly = c.moveOnly
	c.files = append(c.files, snapshot)
}

// through also captures the file a write reaches when path is a symlink.
func (c *execCapture) through(path, source string) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// A write through a dangling link creates a target this capture
		// cannot name reliably.
		c.omit(path, "symlink target is unavailable")
		return
	}
	c.addCopy(resolved, source)
}

// tree snapshots a path and, for a real directory, every entry below it.
func (c *execCapture) tree(root string) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		c.add(root)
		return
	}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			c.omit(path, err.Error())
			return nil
		}
		if reason := c.exhausted(); reason != "" {
			c.omit(root, "more files below were not captured ("+reason+")")
			return filepath.SkipAll
		}
		if !entry.IsDir() {
			c.add(path)
		}
		return nil
	})
}

// list records the files below a destination root without reading them.
func (c *execCapture) list(root string) {
	root = filepath.Clean(root)
	for _, listing := range c.listings {
		if listing.Root == root {
			return
		}
	}
	listing := Listing{Root: root, Origin: c.origin}
	info, err := os.Lstat(root)
	if err == nil && info.IsDir() {
		listing.Entries = make(map[string]string)
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				c.omit(path, err.Error())
				return nil
			}
			if c.listed >= maxExecListingEntries || time.Now().After(c.deadline) {
				c.omit(root, "destination listing exceeds its bound")
				return filepath.SkipAll
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				c.omit(path, err.Error())
				return nil
			}
			relative, _ := filepath.Rel(root, path)
			listing.Entries[relative] = execFileStamp(info)
			c.listed++
			return nil
		})
	}
	c.listings = append(c.listings, listing)
}

func (c *execCapture) mayRemove(path string) bool {
	for _, removed := range c.removals {
		if path == removed || strings.HasPrefix(path, removed+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// expand returns a glob's current matches and earlier captured paths it may
// match once earlier statements have run. Without nullglob, bash passes an
// unmatched pattern through.
func (c *execCapture) expand(operand execscope.Operand) ([]string, bool) {
	if !operand.Glob {
		return []string{operand.Path}, true
	}
	matches, err := filepath.Glob(operand.Path)
	if err != nil {
		return nil, false
	}
	matchedPaths := make(map[string]bool, len(matches))
	for _, path := range matches {
		matchedPaths[path] = true
	}
	for path := range c.seen {
		if matched, _ := filepath.Match(operand.Path, path); matched && !matchedPaths[path] {
			matches = append(matches, path)
		}
	}
	if len(matches) == 0 {
		return []string{operand.Path}, true
	}
	slices.Sort(matches)
	return matches, true
}

func (c *execCapture) entry(entry execscope.Entry) {
	c.origin = entry.Origin
	c.moveOnly = entry.Kind == execscope.Into && entry.Sources
	var sources []string
	for _, operand := range entry.Operands {
		matches, ok := c.expand(operand)
		if !ok {
			c.omit(operand.Path, "invalid glob pattern")
			continue
		}
		sources = append(sources, matches...)
	}
	switch entry.Kind {
	case execscope.File:
		for _, path := range sources {
			c.add(path)
			if entry.Through {
				c.through(path, "")
			}
		}
	case execscope.Tree:
		for _, path := range sources {
			c.tree(path)
			c.list(path)
		}
		c.removals = append(c.removals, sources...)
	case execscope.Into:
		for _, source := range sources {
			for _, target := range c.targets(entry, source) {
				c.into(entry, source, target)
			}
		}
		if entry.Sources {
			c.removals = append(c.removals, sources...)
		}
	}
}

// targets resolves coreutils destination rules. Whether an unforced
// destination names the target or a directory to write into depends on the
// state when the command reaches it, which an earlier statement may change,
// so both readings are captured when both are possible.
func (c *execCapture) targets(entry execscope.Entry, source string) []string {
	inside := filepath.Join(entry.Dest, filepath.Base(source))
	switch {
	case entry.DestDir:
		return []string{inside}
	case entry.NoTarget:
		return []string{entry.Dest}
	}
	info, err := os.Stat(entry.Dest)
	if entry.NoDereference {
		info, err = os.Lstat(entry.Dest)
		if err == nil && info.Mode()&os.ModeSymlink != 0 && !c.mayRemove(entry.Dest) {
			return []string{entry.Dest}
		}
	}
	if err == nil && info.IsDir() && !c.mayRemove(entry.Dest) {
		return []string{inside}
	}
	return []string{entry.Dest, inside}
}

func (c *execCapture) into(entry execscope.Entry, source, target string) {
	if entry.Recursive {
		// A recursive source can be created or grow before the command
		// runs, so the destination is listed for files that appear there.
		c.list(target)
	}
	info, err := os.Lstat(source)
	if entry.Recursive && err == nil && info.IsDir() {
		_ = filepath.WalkDir(source, func(path string, dirEntry fs.DirEntry, err error) error {
			if err != nil {
				c.omit(path, err.Error())
				return nil
			}
			if reason := c.exhausted(); reason != "" {
				c.omit(source, "more files below were not captured ("+reason+")")
				return filepath.SkipAll
			}
			if dirEntry.IsDir() {
				return nil
			}
			relative, relErr := filepath.Rel(source, path)
			if relErr != nil {
				return nil
			}
			if entry.Sources {
				c.add(path)
			}
			destination := filepath.Join(target, relative)
			c.addCopy(destination, execCopySource(entry, path))
			if entry.Through {
				c.through(destination, execCopySource(entry, path))
			}
			return nil
		})
		return
	}
	if entry.Sources {
		c.add(source)
	}
	c.addCopy(target, execCopySource(entry, source))
	if entry.Through {
		c.through(target, execCopySource(entry, source))
	}
	if entry.Backup != "" {
		c.add(target + entry.Backup)
	}
}

func execCopySource(entry execscope.Entry, source string) string {
	if entry.Copies {
		return filepath.Clean(source)
	}
	return ""
}

// sourceLabel names the programs that made an exec record's edits, such as
// sed or python, so a review can tell them from stock patches.
func (o Observation) SourceLabel() string {
	var labels []string
	add := func(label string) {
		if label != "" && !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
	}
	for _, label := range o.Labels {
		add(label)
	}
	for _, program := range o.Programs {
		if program.Direct {
			add(program.Label)
		}
	}
	if len(labels) == 0 {
		return "exec_command"
	}
	if len(labels) > 2 {
		labels = append(labels[:2], "…")
	}
	return strings.Join(labels, "+")
}

// Durable drops provisional metadata and normalizes omitted collections for
// immutable-record comparison. The live observation keeps its own state.
func (o *Observation) Durable() *Observation {
	if o == nil {
		return nil
	}
	observation := *o
	observation.WindowStart = observation.WindowStart.UTC()
	observation.Files = slices.Clone(observation.Files)
	for i := range observation.Files {
		observation.Files[i].WatchStamp = ""
		observation.Files[i].WatchMoveOnly = false
	}
	observation.Listings = slices.Clone(observation.Listings)
	for i := range observation.Listings {
		if len(observation.Listings[i].Entries) == 0 {
			observation.Listings[i].Entries = nil
		}
	}
	observation.Inventory = observation.Inventory.Durable()
	return &observation
}
