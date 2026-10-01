package router

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi"
)

// Only these aggregate facts survive capture. Dependency descendants and their
// contents are never retained, even in --history or explicit-path reads.
type execDirectoryStamp struct {
	Exists   bool
	Stamp    string
	Digest   string
	Complete bool
}

func captureExecDirectory(root, relative string, deadline time.Time) execDirectoryStamp {
	var result execDirectoryStamp
	file, link, err := openInventoryFile(root, relative)
	if errors.Is(err, os.ErrNotExist) {
		result.Complete = true
		return result
	}
	if err != nil || file == nil {
		return result
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.IsDir() || link != "" {
		return result
	}
	result.Exists, result.Stamp = true, execInventoryStamp(info)
	digest := sha256.New()
	visited := 0
	var walk func(*os.File, string) bool
	walk = func(directory *os.File, prefix string) bool {
		if time.Now().After(deadline) {
			return false
		}
		entries, err := directory.ReadDir(maxExecListingEntries - visited + 1)
		visited += len(entries)
		if err != nil && !errors.Is(err, io.EOF) || visited > maxExecListingEntries {
			return false
		}
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, entry := range entries {
			if time.Now().After(deadline) {
				return false
			}
			if slices.Contains(execVCSMetadata, entry.Name()) {
				continue
			}
			name := filepath.Join(prefix, entry.Name())
			child, target, err := openInventoryFile(root, filepath.Join(relative, name))
			if err != nil {
				return false
			}
			if child == nil {
				fmt.Fprintf(digest, "%q link %q\n", name, target)
				continue
			}
			stat, err := child.Stat()
			if err != nil {
				child.Close()
				return false
			}
			stamp := execInventoryStamp(stat)
			if stamp == "" {
				child.Close()
				return false
			}
			fmt.Fprintf(digest, "%q %s\n", name, stamp)
			ok := !stat.IsDir() || walk(child, name)
			child.Close()
			if !ok {
				return false
			}
		}
		return true
	}
	result.Complete = walk(file, "")
	if result.Complete {
		result.Digest = hex.EncodeToString(digest.Sum(nil))
	}
	return result
}

func execDirectoryReview(path string, before, after execDirectoryStamp) (mekugi.ReviewFile, bool) {
	changed := !before.Exists && before.Complete && after.Exists || before.Exists && !after.Exists && after.Complete || before.Exists && after.Exists &&
		(before.Stamp != "" && after.Stamp != "" && before.Stamp != after.Stamp || before.Complete && after.Complete && before.Digest != after.Digest)
	if !changed {
		// A scan limit is coverage information, not evidence of a changed path.
		return mekugi.ReviewFile{}, false
	}
	a, b := path, path
	if !before.Exists {
		a = ""
	}
	if !after.Exists {
		b = ""
	}
	file := mekugi.ReviewFile{BeforePath: a, AfterPath: b, Directory: true, Incomplete: "directory contents intentionally not captured", OriginNote: execInventoryNote}
	return file, true
}

// Older captures stored these observation gaps as changed files. Keep the
// immutable records readable in --history, but do not project them as edits.
func dependencyObservationGap(file mekugi.ReviewFile) bool {
	if file.Directory || file.Origin != "" || file.OriginNote != execInventoryNote || file.BeforePath == "" || file.BeforePath != file.AfterPath {
		return false
	}
	switch file.Incomplete {
	case "dependency directory metadata capture incomplete", "dependency directory has no pre-call metadata":
		return file.Diff == mekugi.RenderIncompleteReviewFile(file.BeforePath, file.AfterPath, file.Incomplete).Diff
	}
	return false
}
