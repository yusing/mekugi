package router

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

const (
	maxExecSweepEntries  = 50_000
	maxExecSweepDuration = 100 * time.Millisecond
)

// execSweepFinding is a path whose change time is inside the call window.
type execSweepFinding struct {
	Path string
	Dir  bool
	// New means the inode was born inside the window. It proves a new inode,
	// not a new path: an atomic replacement also creates one.
	New bool
	// Born reports whether the filesystem records birth times at all.
	Born bool
}

type execSweepResult struct {
	Findings []execSweepFinding
	// Quiet names directories with a changed entry that the sweep prunes,
	// such as an ignored cache, which explains the directory's own change.
	Quiet map[string]bool
	// Unswept explains why paths may have changed unseen.
	Unswept string
}

var execSweepVCSDirectories = []string{".git", ".hg", ".svn", ".jj"}

// execWindowStart reads the file clock by creating a marker in dir, so the
// window compares file times with file times rather than with the router's
// clock.
func execWindowStart(dir string) time.Time {
	marker, err := os.CreateTemp(dir, ".exec-window-*")
	if err != nil {
		return time.Time{}
	}
	name := marker.Name()
	defer marker.Close()
	defer os.Remove(name)
	initial, _, _, ok := execFileTimes(name)
	if !ok {
		return time.Time{}
	}
	// Inode timestamps may share a coarse kernel tick. Advance the marker
	// beyond its first tick so unchanged files immediately preceding capture
	// cannot be mistaken for command effects by the inclusive comparison.
	deadline := time.Now().Add(20 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := marker.WriteAt([]byte{0}, 0); err != nil {
			return time.Time{}
		}
		change, _, _, ok := execFileTimes(name)
		if !ok {
			return time.Time{}
		}
		if change.After(initial) {
			return change
		}
		time.Sleep(100 * time.Microsecond)
	}
	return time.Time{}
}

// sweepExecRoot walks root once and reports entries changed at or after
// since, including root itself. It keeps nothing afterwards. skip names
// directories that belong to Mekugi, such as the replay store.
func sweepExecRoot(root string, since time.Time, skip []string) execSweepResult {
	if since.IsZero() {
		return execSweepResult{Unswept: "the window start is unavailable"}
	}
	if execRemoteFilesystem(root) {
		return execSweepResult{Unswept: "network or FUSE filesystems use another clock"}
	}
	deadline := time.Now().Add(maxExecSweepDuration)
	result := execSweepResult{Quiet: make(map[string]bool)}
	changed := func(path string) (execSweepFinding, bool) {
		change, birth, born, ok := execFileTimes(path)
		if !ok {
			result.Unswept = "file timestamps unavailable: " + path
			return execSweepFinding{}, false
		}
		if change.Before(since) {
			return execSweepFinding{}, false
		}
		return execSweepFinding{Path: path, Born: born, New: born && !birth.Before(since)}, true
	}
	if finding, ok := changed(root); ok {
		finding.Dir = true
		result.Findings = append(result.Findings, finding)
	}
	type pending struct {
		dir    string
		frames []execIgnoreFrame
	}
	entries := 0
	stack := []pending{{dir: root}}
	for len(stack) != 0 {
		if time.Now().After(deadline) {
			result.Unswept = "the sweep exceeded " + maxExecSweepDuration.String()
			return result
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		frames := current.frames
		if frame, found := readExecIgnoreFrame(current.dir); found {
			frames = append(slices.Clip(frames), frame)
		}
		directory, err := os.Open(current.dir)
		if err != nil {
			result.Unswept = "directory unreadable: " + err.Error()
			continue
		}
		children, err := directory.ReadDir(maxExecSweepEntries - entries + 1)
		directory.Close()
		if err != nil && err != io.EOF {
			result.Unswept = "directory unreadable: " + err.Error()
		}
		for _, child := range children {
			entries++
			if entries > maxExecSweepEntries {
				result.Unswept = "the sweep exceeded " + strconv.Itoa(maxExecSweepEntries) + " entries"
				return result
			}
			if entries%256 == 0 && time.Now().After(deadline) {
				result.Unswept = "the sweep exceeded " + maxExecSweepDuration.String()
				return result
			}
			path := filepath.Join(current.dir, child.Name())
			dir := child.IsDir()
			finding, ok := changed(path)
			if dir && (slices.Contains(execSweepVCSDirectories, child.Name()) || slices.Contains(skip, path)) ||
				execIgnored(frames, path, dir) || dir && len(frames) == 0 && execBuiltinPruned(path, child.Name()) {
				if ok {
					result.Quiet[current.dir] = true
				}
				continue
			}
			if ok {
				finding.Dir = dir
				result.Findings = append(result.Findings, finding)
			}
			if dir {
				stack = append(stack, pending{dir: path, frames: frames})
			}
		}
	}
	return result
}
