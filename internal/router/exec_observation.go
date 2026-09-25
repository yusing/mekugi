package router

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/yusing/mekugi"
)

const (
	maxExecCaptureFiles = 256
	// execCaptureHold bounds how long a call item waits for its pre-call
	// capture. Unread paths become incomplete instead of delaying the host.
	execCaptureHold = 500 * time.Millisecond
	// maxExecReportBytes bounds the retained host result, which is context for
	// the record rather than the command's full output.
	maxExecReportBytes = 8 << 10
	// maxExecContentBytes bounds the text read on each side of a call.
	maxExecContentBytes = 8 << 20
	// maxExecObservationBytes bounds the encoded pre-call capture that the call
	// record carries until its result arrives.
	maxExecObservationBytes = 8 << 20
	// maxExecRecordBytes keeps the encoded reviews of a derived record below
	// the store's record limit.
	maxExecRecordBytes    = 24 << 20
	maxExecGlobMatches    = 256
	maxExecListingEntries = 4096
)

// execCommandInput is one stock command as the host will run it.
type execCommandInput struct {
	Command string
	Workdir string
	Shell   string `json:",omitempty"`
}

type execFileKind string

const (
	execFileAbsent  execFileKind = ""
	execFileText    execFileKind = "text"
	execFileBinary  execFileKind = "binary"
	execFileSymlink execFileKind = "symlink"
	execFileDir     execFileKind = "dir"
	execFileOther   execFileKind = "other"
)

// execFileSnapshot is the bounded state of one path. Binary content keeps only
// size and hash; symlinks keep their target, never the target's content.
type execFileSnapshot struct {
	Origin      string `json:",omitempty"`
	AlsoManaged string `json:",omitempty"`
	Path        string
	Kind        execFileKind `json:",omitempty"`
	Content     string       `json:",omitempty"`
	Size        int64        `json:",omitzero"`
	Hash        string       `json:",omitempty"`
	Link        string       `json:",omitempty"`
	// Stamp identifies an uncaptured regular file, so an unchanged file is not
	// reported just because its content was over a bound.
	Stamp string `json:",omitempty"`
	Error string `json:",omitempty"`
	// watchStamp is the live preview's capture-time metadata baseline. It is
	// not durable evidence and must not replace content comparison.
	watchStamp string
	// CopyOf names the source a copy command writes into this path.
	CopyOf string `json:",omitempty"`
}

// execOmission records scope that was not captured at all.
type execOmission struct {
	Origin string `json:",omitempty"`
	Path   string
	Reason string
	// Existing explicitly scoped fixer or formatter paths were not baselined. Their
	// after-state is checked against the observation clock before reporting.
	Deferred bool `json:",omitzero"`
}

// execObservation is the durable pre-call capture of one observed call: a
// native exec_command, or all literal commands of one Code Mode cell.
type execObservation struct {
	Commands    []execCommandInput
	Class       string
	Labels      []string `json:",omitempty"`
	Reason      string   `json:",omitempty"`
	Files       []execFileSnapshot
	Omitted     []execOmission `json:",omitempty"`
	Listings    []execListing  `json:",omitempty"`
	CodeMode    bool           `json:",omitzero"`
	Roots       []string       `json:",omitempty"`
	WindowStart time.Time      `json:",omitzero"`
	Programs    []execProgram  `json:",omitempty"`
	// Group identifies the response that emitted the call, so parallel
	// siblings finalized together share one record.
	Group string `json:",omitempty"`
	// Excluded paths belong to patch records of the same call.
	Excluded []string `json:",omitempty"`
}

// execOutcome is the finalized status of an observed exec record.
type execOutcome struct {
	Status   string
	Exit     *int `json:",omitempty"`
	Class    string
	Labels   []string `json:",omitempty"`
	Coverage string
	CodeMode bool `json:",omitzero"`
	// Scope lists the captured paths, the only paths the record compares
	// exactly.
	Scope       []string `json:",omitempty"`
	ScopeReason string   `json:",omitempty"`
	// Unswept retains diagnostics from older workspace-sweep records.
	Unswept string `json:",omitempty"`
	// Overlaps are calls whose windows overlapped this one on the same root.
	Overlaps []string `json:",omitempty"`
	// Background names still-running sessions for command-history diagnostics.
	Background []string `json:",omitempty"`
	// SharedWith names the record of a parallel sibling that holds this
	// call's effects.
	SharedWith string `json:",omitempty"`
}

const (
	execStatusCompleted = "completed"
	execStatusFailed    = "failed"

	execCoverageExact   = "exact"
	execCoveragePartial = "partial"
	execCoverageUnswept = "unswept"
)

func (o execOutcome) text() string {
	label := "exec"
	if len(o.Labels) != 0 {
		label += " " + strings.Join(o.Labels, ", ")
	}
	parts := []string{label}
	if o.Status == execStatusCompleted || o.Status == execStatusFailed {
		parts = append(parts, "command "+o.Status)
	}
	if o.Exit != nil {
		parts = append(parts, "exit "+strconv.Itoa(*o.Exit))
	}
	parts = append(parts, o.Class+", "+o.Coverage+" coverage")
	if o.Unswept != "" {
		parts = append(parts, o.Unswept)
	}
	return strings.Join(parts, " · ")
}

// execCommandArguments decodes stock exec_command arguments and resolves the
// working directory against the selected metadata directory. A command without
// its own shell runs in the session shell. A command for another environment
// runs on a filesystem the router cannot read, so it is not observed.
func execCommandArguments(arguments, directory, sessionShell string) (execCommandInput, bool) {
	var args struct {
		Cmd           string          `json:"cmd"`
		Workdir       string          `json:"workdir"`
		Shell         string          `json:"shell"`
		EnvironmentID json.RawMessage `json:"environment_id"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil || args.Cmd == "" || len(args.EnvironmentID) != 0 && string(args.EnvironmentID) != "null" {
		return execCommandInput{}, false
	}
	workdir := args.Workdir
	if !filepath.IsAbs(workdir) {
		if !filepath.IsAbs(directory) {
			return execCommandInput{}, false
		}
		workdir = filepath.Join(directory, workdir)
	}
	return execCommandInput{Command: args.Cmd, Workdir: filepath.Clean(workdir), Shell: cmp.Or(args.Shell, sessionShell)}, true
}

// requestSessionShell reads the session shell from the latest environment
// context in a request. Several environments with different shells leave it
// unknown.
func requestSessionShell(raw json.RawMessage) string {
	var input []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return ""
	}
	shell := ""
	for _, item := range input {
		if item.Type != "message" && item.Type != "" {
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(item.Content, &parts) != nil {
			continue
		}
		for _, part := range parts {
			text := strings.TrimSpace(part.Text)
			if !strings.HasPrefix(text, "<environment_context>") || !strings.HasSuffix(text, "</environment_context>") {
				continue
			}
			var shells []string
			for rest := text; ; {
				_, after, found := strings.Cut(rest, "<shell>")
				if !found {
					break
				}
				value, remainder, closed := strings.Cut(after, "</shell>")
				if !closed {
					break
				}
				shells, rest = append(shells, html.UnescapeString(value)), remainder
			}
			if len(shells) == 0 {
				// An update that omits the shell keeps the previous one.
				continue
			}
			shell = shells[0]
			for _, other := range shells[1:] {
				if other != shell {
					shell = ""
				}
			}
		}
	}
	return shell
}

// execCaptureEnv is the router state a capture consults.
type execCaptureEnv struct {
	// directory is the selected workspace; commands inside it sweep it whole.
	directory string
	// clock is a directory where the window-start marker is written.
	clock string
	// excluded paths belong to patch records of the same call.
	excluded []string
	// changes scopes mchanges revert and apply by their recorded paths.
	changes execChangeResolver
}

// captureExecObservation classifies commands and captures their scope before
// the host runs them. Neutral commands are not observed.
func captureExecObservation(commands []execCommandInput, dynamic, codeMode bool, env execCaptureEnv) (*execObservation, bool) {
	if len(commands) == 0 && !dynamic {
		return nil, false
	}
	deadline := time.Now().Add(execCaptureHold)
	type result struct {
		observation *execObservation
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
		return incompleteExecCapture(commands, codeMode, env), true
	}
	select {
	case result := <-done:
		if time.Now().Before(deadline) {
			return result.observation, result.observed
		}
	case <-timer.C:
	}
	// Late reads never enter evidence: the host may already be writing by then.
	return incompleteExecCapture(commands, codeMode, env), true
}

// Isolate potentially stalled filesystem syscalls from forwarding while keeping
// outstanding auxiliary workers bounded across requests and threads.
var execCaptureSlots = make(chan struct{}, 16)

func incompleteExecCapture(commands []execCommandInput, codeMode bool, env execCaptureEnv) *execObservation {
	return &execObservation{Commands: commands, CodeMode: codeMode, Class: execOpaque.String(), Reason: "capture deadline"}
}

func captureExecObservationWithin(commands []execCommandInput, dynamic, codeMode bool, env execCaptureEnv) (*execObservation, bool) {
	started := time.Now()
	if len(commands) == 0 && !dynamic {
		return nil, false
	}
	observation := &execObservation{Commands: commands, CodeMode: codeMode}
	class := execNeutral
	var scope []execScopeEntry
	for _, command := range commands {
		plan := classifyExecShellWithin(command.Command, command.Workdir, command.Shell, started.Add(execProviderBudget), 0, env.changes)
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
		observation.Roots = execAddRoot(observation.Roots, execCaptureRoot(command.Workdir, env.directory))
	}
	if dynamic {
		class, observation.Reason = execOpaque, "Code Mode cell has a non-literal command"
		observation.Programs = append(observation.Programs, execProgram{Label: "Code Mode command"})
		if filepath.IsAbs(env.directory) {
			observation.Roots = execAddRoot(observation.Roots, filepath.Clean(env.directory))
		}
	}
	if class == execNeutral {
		return nil, false
	}
	observation.Class = class.String()
	if class != execDeclared {
		// The window starts before any capture read, so a write that races
		// the capture is still inside it.
		observation.WindowStart = execWindowStart(cmp.Or(env.clock, os.TempDir()))
	}
	observation.Excluded = env.excluded
	capture := newExecCapture(started.Add(execCaptureHold))
	capture.scopeRoots = observation.Roots
	for _, managed := range []bool{false, true} {
		for _, entry := range scope {
			if (entry.Origin != "") == managed {
				capture.entry(entry)
			}
		}
	}
	observation.Files, observation.Omitted, observation.Listings = capture.files, capture.omitted, capture.listings
	boundExecObservation(observation)
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

// execAddRoot adds root unless an existing root contains it, and drops
// roots it contains.
func execAddRoot(roots []string, root string) []string {
	for _, existing := range roots {
		if execPathWithin(root, existing) {
			return roots
		}
	}
	roots = slices.DeleteFunc(roots, func(existing string) bool { return execPathWithin(existing, root) })
	return append(roots, root)
}

// scopePaths are the paths a capture claims: files it compares and roots it
// lists. Concurrent sweeps leave them to this record.
func (o execObservation) scopePaths() []string {
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
func boundExecObservation(observation *execObservation) {
	total := len(mustMarshalJSON(observation))
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
		before := len(mustMarshalJSON(file))
		file.Content, file.Error = "", "capture exceeds the record bound"
		if info, err := os.Lstat(file.Path); err == nil {
			file.Stamp = execFileStamp(info)
		}
		total -= before - len(mustMarshalJSON(file))
	}
}

// execListing names the files below one destination root before the call, so
// files a recursive copy or move creates there are found afterwards.
type execListing struct {
	Origin string `json:",omitempty"`
	Root   string
	// Entries maps each file's relative path to its stamp.
	Entries map[string]string `json:",omitempty"`
}

type execCapture struct {
	origin     string
	deadline   time.Time
	seen       map[string]bool
	files      []execFileSnapshot
	omitted    []execOmission
	listings   []execListing
	scopeRoots []string
	listed     int
	// removals are trees an earlier statement deletes or moves away.
	removals []string
	budget   int
}

func newExecCapture(deadline time.Time) *execCapture {
	return &execCapture{deadline: deadline, seen: make(map[string]bool), budget: maxExecContentBytes}
}

func (c *execCapture) omit(path, reason string) {
	for _, omission := range c.omitted {
		if omission.Path == path {
			return
		}
	}
	c.omitted = append(c.omitted, execOmission{Path: path, Reason: reason, Origin: c.origin})
}

// exhausted names the bound that stops further capture.
func (c *execCapture) exhausted() string {
	switch {
	case len(c.files) >= maxExecCaptureFiles:
		return "capture limit of " + strconv.Itoa(maxExecCaptureFiles) + " files"
	case time.Now().After(c.deadline):
		return "capture deadline"
	}
	return ""
}

// add snapshots one path once. Past the file cap or deadline the path is
// recorded as omitted, never silently dropped.
func (c *execCapture) add(path string) {
	c.addCopy(path, "")
}

func (c *execCapture) addCopy(path, source string) {
	path = filepath.Clean(path)
	if c.seen[path] {
		if c.origin != "" {
			for i := range c.files {
				if c.files[i].Path == path && c.files[i].Origin == "" {
					c.files[i].AlsoManaged = c.origin
					break
				}
			}
		}
		return
	}
	c.seen[path] = true
	if reason := c.exhausted(); reason != "" {
		// Explicit fixer and formatter paths were enumerated but not baselined.
		// Check their after-state against the window clock instead of
		// fabricating a changed-file review for every unread path.
		if slices.Contains([]string{"go fix", "gofmt", "goimports", "prettier", "eslint", "ruff format", "ruff check", "black", "rustfmt", "cargo fmt"}, c.origin) &&
			slices.ContainsFunc(c.scopeRoots, func(root string) bool { return execPathWithin(path, root) }) {
			c.omitted = append(c.omitted, execOmission{Path: path, Reason: reason, Origin: c.origin, Deferred: true})
			return
		}
		c.omit(path, reason)
		return
	}
	snapshot := snapshotExecFile(path, &c.budget)
	snapshot.CopyOf = source
	snapshot.Origin = c.origin
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
	listing := execListing{Root: root, Origin: c.origin}
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
func (c *execCapture) expand(operand execOperand) ([]string, bool) {
	if !operand.Glob {
		return []string{operand.Path}, true
	}
	matches, err := filepath.Glob(operand.Path)
	if err != nil || len(matches) > maxExecGlobMatches {
		return nil, false
	}
	for path := range c.seen {
		if matched, _ := filepath.Match(operand.Path, path); matched && !slices.Contains(matches, path) {
			matches = append(matches, path)
		}
	}
	if len(matches) == 0 {
		return []string{operand.Path}, true
	}
	slices.Sort(matches)
	return matches, true
}

func (c *execCapture) entry(entry execScopeEntry) {
	c.origin = entry.Origin
	var sources []string
	for _, operand := range entry.Operands {
		matches, ok := c.expand(operand)
		if !ok {
			c.omit(operand.Path, "glob matches more than "+strconv.Itoa(maxExecGlobMatches)+" paths")
			continue
		}
		sources = append(sources, matches...)
	}
	switch entry.Kind {
	case execScopeFile:
		for _, path := range sources {
			c.add(path)
			if entry.Through {
				c.through(path, "")
			}
		}
	case execScopeTree:
		for _, path := range sources {
			c.tree(path)
			c.list(path)
		}
		c.removals = append(c.removals, sources...)
	case execScopeInto:
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
func (c *execCapture) targets(entry execScopeEntry, source string) []string {
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

func (c *execCapture) into(entry execScopeEntry, source, target string) {
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

func execCopySource(entry execScopeEntry, source string) string {
	if entry.Copies {
		return filepath.Clean(source)
	}
	return ""
}

func execFileStamp(info os.FileInfo) string {
	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + execFileIdentity(info)
}

func execWatchFileStamp(path string, info os.FileInfo) string {
	stamp := execFileStamp(info)
	if change, _, _, ok := execFileTimes(path); ok {
		stamp += ":" + strconv.FormatInt(change.UnixNano(), 10)
	}
	return stamp
}

// snapshotExecFile reads one path without following a final symlink or
// blocking on a FIFO.
func snapshotExecFile(path string, budget *int) execFileSnapshot {
	snapshot := execFileSnapshot{Path: path}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist):
		return snapshot
	case err != nil:
		if errors.Is(err, syscall.ENOTDIR) {
			return snapshot
		}
		snapshot.Kind, snapshot.Error = execFileOther, err.Error()
		return snapshot
	}
	snapshot.watchStamp = execWatchFileStamp(path, info)
	switch mode := info.Mode(); {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		snapshot.Kind, snapshot.Link = execFileSymlink, target
		if err != nil {
			snapshot.Error = err.Error()
		}
		return snapshot
	case mode.IsDir():
		snapshot.Kind = execFileDir
		return snapshot
	case !mode.IsRegular():
		snapshot.Kind = execFileOther
		return snapshot
	}
	snapshot.Kind, snapshot.Size = execFileText, info.Size()
	if info.Size() > maxNativePatchFileBytes {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), "file exceeds the 8 MiB capture bound"
		return snapshot
	}
	if budget != nil && int(info.Size()) > *budget {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), "capture budget exhausted"
		return snapshot
	}
	file, err := openNativePatchFile(path)
	if err != nil {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), err.Error()
		return snapshot
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), "file changed while it was captured"
		return snapshot
	}
	data, err := io.ReadAll(io.LimitReader(file, maxNativePatchFileBytes+1))
	if err != nil || len(data) > maxNativePatchFileBytes {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), "file could not be read within the capture bound"
		return snapshot
	}
	snapshot.Size = int64(len(data))
	// NUL marks binary content, as in Git.
	if utf8.Valid(data) && !slices.Contains(data, 0) {
		snapshot.Content = string(data)
		if budget != nil {
			*budget -= len(data)
		}
		return snapshot
	}
	sum := sha256.Sum256(data)
	snapshot.Kind, snapshot.Hash = execFileBinary, hex.EncodeToString(sum[:])
	return snapshot
}

// sameExecContent compares captured states, treating directories as absent
// because only files are reviewed.
func sameExecContent(before, after execFileSnapshot) bool {
	if before.Kind != after.Kind {
		return false
	}
	switch before.Kind {
	case execFileText:
		return before.Content == after.Content
	case execFileBinary:
		return before.Hash == after.Hash
	case execFileSymlink:
		return before.Link == after.Link
	default:
		return true
	}
}

func execFilePresent(snapshot execFileSnapshot) bool {
	return snapshot.Kind != execFileAbsent && snapshot.Kind != execFileDir
}

func execReviewText(snapshot execFileSnapshot) string {
	if snapshot.Kind == execFileSymlink {
		return "-> " + snapshot.Link + "\n"
	}
	return snapshot.Content
}

func execSnapshotHash(snapshot execFileSnapshot) (int64, string) {
	if snapshot.Kind == execFileBinary {
		return snapshot.Size, snapshot.Hash
	}
	text := execReviewText(snapshot)
	sum := sha256.Sum256([]byte(text))
	return int64(len(text)), hex.EncodeToString(sum[:])
}

func renderExecReview(beforePath, afterPath string, before, after execFileSnapshot) mekugi.ReviewFile {
	if before.Kind == execFileBinary || after.Kind == execFileBinary {
		// A text or link side against binary content has no row diff either.
		beforeSize, beforeHash := execSnapshotHash(before)
		afterSize, afterHash := execSnapshotHash(after)
		return mekugi.RenderBinaryReviewFile(beforePath, afterPath, beforeSize, afterSize, beforeHash, afterHash)
	}
	review := mekugi.RenderReviewFile(beforePath, afterPath, execReviewText(before), execReviewText(after))
	review.Link = before.Kind == execFileSymlink || after.Kind == execFileSymlink
	return review
}

// execReconcileEnv identifies paths claimed by overlapping calls.
type execReconcileEnv struct {
	excluded []string
}

// reconcileExecObservation compares only the command's captured write scope.
// Changes elsewhere cannot be attributed to this call.

func reconcileExecObservation(observation execObservation, env execReconcileEnv) (reviews []mekugi.ReviewFile, complete bool, coverage, unswept string) {
	type change struct {
		before, after execFileSnapshot
	}
	var changes []change
	complete = true
	coverage = execCoverageExact
	budget := maxExecContentBytes
	after := make(map[string]execFileSnapshot, len(observation.Files))
	metadata := make(map[string]execFileSnapshot, len(observation.Files))
	compare := func(before execFileSnapshot) {
		if _, seen := after[before.Path]; seen || slices.Contains(observation.Excluded, before.Path) {
			return
		}
		metadata[before.Path] = before
		if before.Stamp != "" {
			// An uncaptured file with the same identity, size, and time was
			// not written.
			if info, err := os.Lstat(before.Path); err == nil && execFileStamp(info) == before.Stamp {
				after[before.Path] = before
				return
			}
		}
		current := snapshotExecFile(before.Path, &budget)
		after[before.Path] = current
		if before.Error != "" || current.Error != "" {
			beforePath, afterPath := before.Path, before.Path
			if !execFilePresent(before) && before.Error == "" {
				beforePath = ""
			}
			if !execFilePresent(current) && current.Error == "" {
				afterPath = ""
			}
			reason := strings.Trim(strings.Join([]string{before.Error, current.Error}, "; "), "; ")
			reviews = append(reviews, mekugi.RenderIncompleteReviewFile(beforePath, afterPath, reason))
			complete = false
			return
		}
		if before.Kind == execFileOther || current.Kind == execFileOther {
			if before.Kind != current.Kind {
				reviews = append(reviews, mekugi.RenderIncompleteReviewFile(before.Path, before.Path, "not a regular file"))
				complete = false
			}
			return
		}
		if !execFilePresent(before) && !execFilePresent(current) || sameExecContent(before, current) {
			return
		}
		changes = append(changes, change{before: before, after: current})
	}
	for _, before := range observation.Files {
		compare(before)
	}
	omitted := func(path string) bool {
		for _, omission := range observation.Omitted {
			if path == omission.Path || strings.HasPrefix(path, omission.Path+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	found, walked := 0, 0
	for _, listing := range observation.Listings {
		// A cut listing cannot prove which files are new; its omission
		// already marks the record incomplete.
		if omitted(listing.Root) {
			continue
		}
		seen := make(map[string]bool)
		_ = filepath.WalkDir(listing.Root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || path == listing.Root {
				return nil
			}
			if walked++; walked > maxExecListingEntries+maxExecCaptureFiles {
				reviews = append(reviews, mekugi.RenderIncompleteReviewFile(listing.Root, listing.Root, "destination listing exceeds its bound"))
				complete = false
				return filepath.SkipAll
			}
			relative, _ := filepath.Rel(listing.Root, path)
			seen[relative] = true
			if slices.Contains(observation.Excluded, path) {
				return nil
			}
			if _, captured := after[path]; captured {
				return nil
			}
			if found >= maxExecCaptureFiles {
				reviews = append(reviews, mekugi.RenderIncompleteReviewFile(listing.Root, listing.Root, "more new files were not read"))
				complete = false
				return filepath.SkipAll
			}
			if stamp, listed := listing.Entries[relative]; listed {
				if info, err := entry.Info(); err != nil || execFileStamp(info) != stamp {
					reviews = append(reviews, mekugi.RenderIncompleteReviewFile(path, path, "modified; before content unavailable"))
					complete = false
				}
				return nil
			}
			found++
			compare(execFileSnapshot{Path: path, Origin: listing.Origin})
			return nil
		})
		for relative := range listing.Entries {
			path := filepath.Join(listing.Root, relative)
			if slices.Contains(observation.Excluded, path) {
				continue
			}
			if _, captured := after[path]; !captured && !seen[relative] {
				_, err := os.Lstat(path)
				if err == nil {
					continue
				}
				if errors.Is(err, os.ErrNotExist) {
					reviews = append(reviews, mekugi.RenderIncompleteReviewFile(path, "", "deleted; before content unavailable"))
				} else {
					reviews = append(reviews, mekugi.RenderIncompleteReviewFile(path, path, "destination unavailable: "+err.Error()))
				}
				complete = false
			}
		}
	}
	// A deletion and an addition with identical content form one move. Empty
	// files carry no evidence of which deletion an addition came from.
	paired := make(map[int]bool)
	for index, deleted := range changes {
		if !execFilePresent(deleted.before) || execFilePresent(deleted.after) || deleted.before.Kind == execFileText && deleted.before.Content == "" {
			continue
		}
		for other, added := range changes {
			if paired[other] || execFilePresent(added.before) || !execFilePresent(added.after) || !sameExecContent(deleted.before, added.after) {
				continue
			}
			paired[index], paired[other] = true, true
			review := renderExecReview(deleted.before.Path, added.after.Path, deleted.before, added.after)
			reviews = append(reviews, review)
			break
		}
	}
	for index, changed := range changes {
		if paired[index] {
			continue
		}
		beforePath, afterPath := changed.before.Path, changed.after.Path
		if !execFilePresent(changed.before) {
			beforePath = ""
		}
		if !execFilePresent(changed.after) {
			afterPath = ""
		}
		review := renderExecReview(beforePath, afterPath, changed.before, changed.after)
		if beforePath == "" && changed.before.CopyOf != "" {
			// A created copy names its source when the content matches.
			source, captured := after[changed.before.CopyOf]
			if !captured {
				source = snapshotExecFile(changed.before.CopyOf, &budget)
			}
			if source.Error == "" && execFilePresent(source) && sameExecContent(source, changed.after) {
				review.CopyFrom = changed.before.CopyOf
			}
		}
		reviews = append(reviews, review)
	}
	deferredChanged := false
	for _, omission := range observation.Omitted {
		if slices.Contains(observation.Excluded, omission.Path) || slices.Contains(env.excluded, omission.Path) {
			continue
		}
		if omission.Deferred {
			// A remote mount or missing clock cannot prove an unchanged path.
			// Keep the named gap instead of comparing timestamps from another host.
			if observation.WindowStart.IsZero() || execRemoteFilesystem(filepath.Dir(omission.Path)) {
				review := mekugi.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable; filesystem clock incomparable")
				review.Origin = omission.Origin
				reviews = append(reviews, review)
				complete = false
				deferredChanged = true
				continue
			}
			changed, _, _, ok := execFileTimes(omission.Path)
			if ok && changed.Before(observation.WindowStart) {
				continue
			}
			// These paths existed during provider enumeration. A missing
			// after-state is therefore a possible deletion, not a no-op.
			current := snapshotExecFile(omission.Path, &budget)
			var review mekugi.ReviewFile
			switch {
			case current.Error != "":
				review = mekugi.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable; "+current.Error)
			case current.Kind == execFileAbsent:
				review = mekugi.RenderIncompleteReviewFile(omission.Path, "", "deleted; before content unavailable")
			case current.Kind == execFileText || current.Kind == execFileSymlink:
				review = mekugi.RenderUnbasedReviewFile(omission.Path, execReviewText(current), "managed baseline unavailable")
			default:
				review = mekugi.RenderIncompleteReviewFile(omission.Path, omission.Path, "managed baseline unavailable")
			}
			review.Origin = omission.Origin
			reviews = append(reviews, review)
			deferredChanged = true
			continue
		}
		reviews = append(reviews, mekugi.RenderIncompleteReviewFile(omission.Path, omission.Path, omission.Reason))
		complete = false
	}
	reviews, complete = boundExecReviews(reviews, complete)
	for i := range reviews {
		file := &reviews[i]
		meta, ok := metadata[cmp.Or(file.AfterPath, file.BeforePath)]
		if !ok {
			meta, ok = metadata[file.BeforePath]
		}
		if ok {
			if before, found := metadata[file.BeforePath]; found && before.Origin == "" {
				meta = before
			}
			file.Origin = meta.Origin
			if meta.AlsoManaged != "" {
				file.OriginNote = "also changed by " + meta.AlsoManaged
			}
		} else {
			for _, omission := range observation.Omitted {
				if omission.Path == file.BeforePath || omission.Path == file.AfterPath {
					file.Origin = omission.Origin
					break
				}
			}
		}
	}
	if (!complete || deferredChanged) && coverage == execCoverageExact {
		coverage = execCoveragePartial
	}
	return reviews, complete, coverage, unswept
}

// sourceLabel names the programs that made an exec record's edits, such as
// sed or python, so a review can tell them from stock patches.
func (o execObservation) sourceLabel() string {
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
		return nativeExecCommandToolName
	}
	if len(labels) > 2 {
		labels = append(labels[:2], "…")
	}
	return strings.Join(labels, "+")
}

// boundExecReviews keeps the encoded reviews within the derived record's
// bound, replacing the largest diffs with incomplete entries.
func boundExecReviews(reviews []mekugi.ReviewFile, complete bool) ([]mekugi.ReviewFile, bool) {
	sizes := make([]int, len(reviews))
	total := 0
	for index, review := range reviews {
		sizes[index] = len(mustMarshalJSON(review))
		total += sizes[index]
	}
	for total > maxExecRecordBytes {
		largest := 0
		for index := range sizes {
			if sizes[index] > sizes[largest] {
				largest = index
			}
		}
		review := reviews[largest]
		reviews[largest] = mekugi.RenderIncompleteReviewFile(review.BeforePath, review.AfterPath, "review exceeds the record bound")
		size := len(mustMarshalJSON(reviews[largest]))
		total -= sizes[largest] - size
		sizes[largest] = size
		complete = false
	}
	return reviews, complete
}

// execResultState reads a stock command or Code Mode result. A yielded native
// session or running cell returns the continuation key that later completes it.
func execResultState(toolName string, raw json.RawMessage) (terminal bool, exit *int, completed bool, text, pending string) {
	text, _ = stockToolOutput(raw)
	if toolName != nativeExecCommandToolName && toolName != "write_stdin" {
		terminal, completed, text, cell := stockPatchResultState(toolName, raw)
		if cell != "" {
			pending = "cell:" + cell
		}
		return terminal, nil, completed, text, pending
	}
	state, _ := nativeExecutionHeader(text)
	if session := nativeExecutionSession(text); session != 0 {
		return false, nil, false, text, "session:" + strconv.FormatInt(session, 10)
	}
	if code, ok := strings.CutPrefix(state, "Process exited with code "); ok {
		if value, err := strconv.Atoi(code); err == nil {
			return true, &value, value == 0, text, ""
		}
	}
	if toolName == "write_stdin" && !strings.Contains(text, "Unknown process id") {
		// A failed write, such as one to closed stdin, leaves the process
		// running; only its exit or disappearance ends the call.
		return false, nil, false, text, ""
	}
	// An aborted, rejected, or unparsable command result has no live session.
	return true, nil, false, text, ""
}

func execDerivedCallID(callID string, codeMode bool) string {
	if codeMode {
		return callID + ":effects"
	}
	return callID + ":exec:1"
}

func execObservationScript(observation execObservation) string {
	if len(observation.Commands) == 0 {
		return "# Code Mode commands are not literal\n"
	}
	if !observation.CodeMode && len(observation.Commands) == 1 {
		return observation.Commands[0].Command
	}
	var script strings.Builder
	for index, command := range observation.Commands {
		if index != 0 {
			script.WriteByte('\n')
		}
		fmt.Fprintf(&script, "# tools.exec_command %d\n%s\n", index+1, strings.TrimSuffix(command.Command, "\n"))
	}
	return script.String()
}

// execCompletion is an observed call whose terminal result has arrived.
type execCompletion struct {
	callID  string
	history mekugiHistory
	output  json.RawMessage
}

// execSiblingKey groups calls of one response. Siblings finalized in
// the same request share one record, since their windows are the same.
func execSiblingKey(history mekugiHistory) string {
	if observation := history.ExecObservation; observation != nil && observation.Group != "" {
		return observation.Group
	}
	return ""
}

// mergeExecObservations joins sibling captures into the one observation that
// their shared record reconciles.
func mergeExecObservations(members []execCompletion) execObservation {
	merged := *members[0].history.ExecObservation
	if len(members) == 1 {
		return merged
	}
	merged.Commands = slices.Clone(merged.Commands)
	merged.Labels = slices.Clone(merged.Labels)
	merged.Programs = slices.Clone(merged.Programs)
	merged.Roots = slices.Clone(merged.Roots)
	merged.Files = slices.Clone(merged.Files)
	merged.Omitted = slices.Clone(merged.Omitted)
	merged.Listings = slices.Clone(merged.Listings)
	merged.Excluded = slices.Clone(merged.Excluded)
	for _, member := range members[1:] {
		other := member.history.ExecObservation
		merged.CodeMode = merged.CodeMode || other.CodeMode
		merged.Commands = append(merged.Commands, other.Commands...)
		if execClassRank(other.Class) > execClassRank(merged.Class) {
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
			merged.Roots = execAddRoot(merged.Roots, root)
		}
		merged.Files = append(merged.Files, other.Files...)
		merged.Omitted = append(merged.Omitted, other.Omitted...)
		merged.Listings = append(merged.Listings, other.Listings...)
		merged.Excluded = append(merged.Excluded, other.Excluded...)
		if other.WindowStart.IsZero() || !merged.WindowStart.IsZero() && other.WindowStart.Before(merged.WindowStart) {
			merged.WindowStart = other.WindowStart
		}
	}
	return merged
}

func execClassRank(class string) int {
	for rank := execNeutral; rank <= execOpaque; rank++ {
		if rank.String() == class {
			return int(rank)
		}
	}
	return int(execOpaque)
}

func execTruncateReport(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n[host result truncated]"
}

func execDerivedUpstreamItem(derivedCallID, arguments string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"type":      mustMarshalJSON("function_call"),
		"name":      mustMarshalJSON(nativeExecCommandToolName),
		"call_id":   mustMarshalJSON(derivedCallID),
		"arguments": mustMarshalJSON(arguments),
		"status":    mustMarshalJSON("completed"),
	}
}

// finalizeExecObservations persists the observed effect of terminal command
// results. Members are parallel siblings that share one record, keyed by the
// first; the others keep a record naming it. An empty effect is retained
// without a change ID, so replay neither reads the workspace again nor
// allocates an ID for read-only work.
func (p *mekugiProxy) finalizeExecObservations(ctx context.Context, workspace string, members []execCompletion) error {
	var pending []execCompletion
	for _, member := range members {
		terminal, _, _, _, _ := execResultState(member.history.ToolName, member.output)
		if member.history.ExecObservation == nil || !terminal {
			continue
		}
		derived := execDerivedCallID(member.callID, member.history.ExecObservation.CodeMode)
		retained, found, err := p.replayStore.lookup(ctx, workspace, derived)
		if err != nil {
			return err
		}
		if found {
			if retained.CorrelationID != member.callID+"\x00exec" {
				return errors.New("retained stock exec_command observation is inconsistent")
			}
			continue
		}
		pending = append(pending, member)
	}
	members = pending
	if len(members) == 0 {
		return nil
	}
	first := members[0]
	thread := first.history.ExecutingThread
	observation := mergeExecObservations(members)
	derivedCallID := execDerivedCallID(first.callID, first.history.ExecObservation.CodeMode)
	correlation := first.callID + "\x00exec"
	script := execObservationScript(observation)
	refs := make([]string, 0, len(members))
	for _, member := range members {
		refs = append(refs, member.callID)
	}
	view := p.execWindows.close(refs...)
	background := slices.Compact(slices.Sorted(slices.Values(view.background)))
	reviews, complete, coverage, unswept := reconcileExecObservation(observation, execReconcileEnv{excluded: view.excluded})
	outcome := &execOutcome{
		Class: observation.Class, Labels: observation.Labels, Coverage: coverage, Unswept: unswept,
		ScopeReason: observation.Reason,
		CodeMode:    observation.CodeMode, Overlaps: view.overlaps, Background: background,
	}
	outcome.Scope = observation.scopePaths()
	var reports []string
	var hostResults []nativeToolResult
	for index, member := range members {
		_, exit, completed, resultText, _ := execResultState(member.history.ToolName, member.output)
		status := execStatusCompleted
		switch {
		case member.history.ExecObservation.CodeMode && completed:
			// Outer script completion does not prove each nested command
			// succeeded; nested exit codes are not visible to the router.
			status = ""
		case !completed:
			status = execStatusFailed
		}
		if results, confirmed := member.history.nativeCell.commands(member.history.ExecObservation.Commands, workspace); confirmed {
			status = execStatusCompleted
			hostResults = append(hostResults, results...)
			for _, result := range results {
				if result.Status != "completed" || result.ExitCode == nil || *result.ExitCode != 0 {
					status = execStatusFailed
				}
			}
			if len(results) == 1 {
				exit = results[0].ExitCode
			}
		}
		if len(members) == 1 {
			outcome.Exit = exit
			reports = append(reports, execTruncateReport(resultText, maxExecReportBytes))
		} else {
			reports = append(reports, fmt.Sprintf("# call %d\n%s", index+1, execTruncateReport(resultText, maxExecReportBytes/len(members))))
		}
		if index == 0 || status == execStatusFailed || status == "" && outcome.Status != execStatusFailed {
			outcome.Status = status
		}
	}
	arguments := first.history.CarrierPayload
	if len(members) != 1 || observation.CodeMode || arguments == "" {
		arguments = string(mustMarshalJSON(map[string]string{"cmd": script}))
	}
	record := mekugiHistory{
		ToolName:        nativeExecCommandToolName,
		Script:          script,
		Root:            workspace,
		ExecutingThread: thread,
		Caller:          first.history.Caller,
		Source:          observation.sourceLabel(),
		CorrelationID:   correlation,
		Attempt:         1,
		ReviewFiles:     reviews,
		Report:          strings.Join(reports, "\n"),
		ExecOutcome:     outcome,
		HostResults:     hostResults,
		CarrierKind:     codeModeCarrierFunction,
		CarrierName:     nativeExecCommandToolName,
		CarrierPayload:  arguments,
		ReplayCarrier:   true,
		UpstreamItem:    execDerivedUpstreamItem(derivedCallID, arguments),
	}
	if len(reviews) != 0 {
		changeID, err := p.replayStore.reserveChange(ctx, workspace, thread, correlation)
		if err != nil {
			return err
		}
		record.ChangeID = changeID
	}
	if !complete && record.Report != "" {
		record.Report += "\nMekugi could not capture complete file evidence."
	}
	records := map[string]mekugiHistory{derivedCallID: record}
	for _, member := range members[1:] {
		memberObservation := *member.history.ExecObservation
		memberCallID := execDerivedCallID(member.callID, memberObservation.CodeMode)
		memberScript := execObservationScript(memberObservation)
		memberArguments := string(mustMarshalJSON(map[string]string{"cmd": memberScript}))
		records[memberCallID] = mekugiHistory{
			ToolName: nativeExecCommandToolName, Script: memberScript, Root: workspace,
			ExecutingThread: cmp.Or(member.history.ExecutingThread, thread), Caller: member.history.Caller,
			CorrelationID: member.callID + "\x00exec", Attempt: 1,
			ExecOutcome: &execOutcome{
				Status: outcome.Status, Class: memberObservation.Class, Labels: memberObservation.Labels,
				Coverage: outcome.Coverage, CodeMode: memberObservation.CodeMode, SharedWith: derivedCallID,
			},
			CarrierKind: codeModeCarrierFunction, CarrierName: nativeExecCommandToolName, CarrierPayload: memberArguments,
			ReplayCarrier: true, UpstreamItem: execDerivedUpstreamItem(memberCallID, memberArguments),
		}
	}
	if err := p.replayStore.put(context.WithoutCancel(ctx), workspace, records); err != nil {
		return err
	}
	if record.ChangeID != "" {
		_ = p.replayStore.publishEditReceipt(context.WithoutCancel(ctx), workspace, thread, derivedCallID, p.activity, jsonString(first.history.UpstreamItem, "id"))
	}
	return nil
}

// stockLiteralExecCommands extracts literal tools.exec_command calls from a
// Code Mode cell. Any other reference to the command tools, including
// write_stdin input that can drive a started process, makes the cell dynamic.
func stockLiteralExecCommands(source, directory, sessionShell string) (commands []execCommandInput, dynamic bool) {
	if len(source) > maxMekugiScriptBytes || !strings.Contains(source, "tools") {
		return nil, false
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if parser.SetLanguage(codeModeJavaScriptLanguage) != nil {
		return nil, true
	}
	bytes := []byte(source)
	tree := parser.Parse(bytes, nil)
	if tree == nil {
		return nil, true
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil, strings.Contains(source, nativeExecCommandToolName) || strings.Contains(source, "write_stdin")
	}
	handled := make(map[uintptr]bool)
	stack := []*sitter.Node{root}
	for len(stack) != 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if args, ok := toolActivityCallArguments(node, bytes, "tools", nativeExecCommandToolName); ok {
			function := node.ChildByFieldName("function")
			handled[function.Id()] = true
			command, literal := execLiteralCommand(args, bytes, directory, sessionShell)
			if !literal {
				dynamic = true
			} else {
				commands = append(commands, command)
			}
			if len(commands) > 32 {
				return nil, true
			}
		}
		if args, ok := toolActivityCallArguments(node, bytes, "tools", "write_stdin"); ok && execPollOnly(args, bytes) {
			handled[node.ChildByFieldName("function").Id()] = true
		}
		switch node.Kind() {
		case "member_expression":
			if !handled[node.Id()] && (toolActivityMemberPath(node, bytes, "tools", nativeExecCommandToolName) ||
				toolActivityMemberPath(node, bytes, "tools", "write_stdin")) {
				dynamic = true
			}
		case "subscript_expression":
			if object := node.ChildByFieldName("object"); object != nil && object.Kind() == "identifier" && object.Utf8Text(bytes) == "tools" {
				dynamic = true
			}
		case "optional_chain":
			// tools?.exec_command is a call the literal matcher does not read.
			if parent := node.Parent(); parent != nil && parent.Kind() == "member_expression" {
				if object := parent.ChildByFieldName("object"); object != nil && object.Utf8Text(bytes) == "tools" {
					dynamic = true
				}
			}
		case "identifier", "shorthand_property_identifier", "shorthand_property_identifier_pattern":
			// A bare tools value can be aliased or passed away, after which its
			// command calls are not literal. Global objects and evaluated code
			// reach tools without naming it.
			switch node.Utf8Text(bytes) {
			case "tools":
				parent := node.Parent()
				if parent == nil || parent.Kind() != "member_expression" || parent.ChildByFieldName("object").Id() != node.Id() {
					dynamic = true
				}
			case "globalThis", "self", "window", "global", "eval", "Function":
				dynamic = true
			}
		case "this":
			dynamic = true
		}
		for index := int(node.NamedChildCount()) - 1; index >= 0; index-- {
			stack = append(stack, node.NamedChild(uint(index)))
		}
	}
	return commands, dynamic
}

// Poll metadata may depend on a prior result; only input bytes determine whether
// this call can drive a writer. Spreads, prototypes and computed keys stay open.
func execPollOnly(args []*sitter.Node, source []byte) bool {
	if len(args) != 1 || args[0].Kind() != "object" {
		return false
	}
	seen := make(map[string]bool)
	for i := range args[0].NamedChildCount() {
		pair := args[0].NamedChild(uint(i))
		key := ""
		if pair.Kind() == "shorthand_property_identifier" {
			key = pair.Utf8Text(source)
		} else if pair.Kind() == "pair" {
			node := pair.ChildByFieldName("key")
			switch node.Kind() {
			case "property_identifier":
				key = node.Utf8Text(source)
			case "string":
				key, _ = toolActivityJavaScriptString(node.Utf8Text(source))
			}
		}
		if key == "" || key == "__proto__" || strings.ContainsRune(key, '\\') || seen[key] {
			return false
		}
		seen[key] = true
		if key == "chars" {
			value, ok := toolActivityStaticJavaScriptValue(pair.ChildByFieldName("value"), source)
			if !ok || value != "" {
				return false
			}
		}
	}
	return true
}

func execLiteralCommand(args []*sitter.Node, source []byte, directory, sessionShell string) (execCommandInput, bool) {
	if len(args) != 1 {
		return execCommandInput{}, false
	}
	value, ok := toolActivityStaticJavaScriptValue(args[0], source)
	object, isObject := value.(map[string]any)
	if !ok || !isObject {
		return execCommandInput{}, false
	}
	arguments := make(map[string]string, 3)
	if _, remote := object["environment_id"]; remote {
		return execCommandInput{}, false
	}
	for _, key := range []string{"cmd", "workdir", "shell"} {
		field, present := object[key]
		if !present {
			continue
		}
		text, isText := field.(string)
		if !isText {
			return execCommandInput{}, false
		}
		arguments[key] = text
	}
	return execCommandArguments(string(mustMarshalJSON(arguments)), directory, sessionShell)
}
