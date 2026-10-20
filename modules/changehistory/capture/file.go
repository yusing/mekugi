package capture

// Source: apps/mekugi/internal/router/exec_observation.go:38:1250@1e6d58c6.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"syscall"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"

	"github.com/yusing/mekugi/libraries/diffmodel"
)

func execFileStamp(info os.FileInfo) string {
	return strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + execFileIdentity(info)
}

func WatchFileStamp(path string, info os.FileInfo) string {
	stamp := execFileStamp(info)
	if change, ok := execFileTimes(path); ok {
		stamp += ":" + strconv.FormatInt(change.UnixNano(), 10)
	}
	return stamp
}

// ReadFile reads one path without following a final symlink or
// blocking on a FIFO.
func ReadFile(path string, budget *int) File {
	snapshot := File{Path: path}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist) || errors.Is(err, fs.ErrNotExist):
		return snapshot
	case err != nil:
		if errors.Is(err, syscall.ENOTDIR) {
			return snapshot
		}
		snapshot.Kind, snapshot.Error = Other, err.Error()
		return snapshot
	}
	snapshot.WatchStamp = WatchFileStamp(path, info)
	switch mode := info.Mode(); {
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		snapshot.Kind, snapshot.Link = Symlink, target
		if err != nil {
			snapshot.Error = err.Error()
		}
		return snapshot
	case mode.IsDir():
		snapshot.Kind = Directory
		return snapshot
	case !mode.IsRegular():
		snapshot.Kind = Other
		return snapshot
	}
	snapshot.Kind, snapshot.Size = Text, info.Size()
	if info.Size() > MaxFileBytes {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), "file exceeds the 8 MiB capture bound"
		return snapshot
	}
	if budget != nil && int(info.Size()) > *budget {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), "capture budget exhausted"
		return snapshot
	}
	file, err := sourceparse.Open(path)
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
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil || len(data) > MaxFileBytes {
		snapshot.Stamp, snapshot.Error = execFileStamp(info), "file could not be read within the capture bound"
		return snapshot
	}
	snapshot.Kind, snapshot.Size, snapshot.Content, snapshot.Hash = execDataKind(data, budget)
	return snapshot
}

// execDataSnapshot is the state of a regular file with known content.
func execDataSnapshot(path string, data []byte, budget *int) File {
	snapshot := File{Path: path}
	snapshot.Kind, snapshot.Size, snapshot.Content, snapshot.Hash = execDataKind(data, budget)
	return snapshot
}

func execDataKind(data []byte, budget *int) (kind FileKind, size int64, content, hash string) {
	// NUL marks binary content, as in Git.
	if utf8.Valid(data) && !slices.Contains(data, 0) {
		if budget != nil {
			*budget -= len(data)
		}
		return Text, int64(len(data)), string(data), ""
	}
	sum := sha256.Sum256(data)
	return Binary, int64(len(data)), "", hex.EncodeToString(sum[:])
}

// SameContent compares captured states, treating directories as absent
// because only files are reviewed.
func SameContent(before, after File) bool {
	if before.Kind != after.Kind {
		return false
	}
	switch before.Kind {
	case Text:
		return before.Content == after.Content
	case Binary:
		return before.Hash == after.Hash
	case Symlink:
		return before.Link == after.Link
	default:
		return true
	}
}

func FilePresent(snapshot File) bool {
	return snapshot.Kind != Absent && snapshot.Kind != Directory
}

func ReviewText(snapshot File) string {
	if snapshot.Kind == Symlink {
		return "-> " + snapshot.Link + "\n"
	}
	return snapshot.Content
}

func execSnapshotHash(snapshot File) (int64, string) {
	if snapshot.Kind == Binary {
		return snapshot.Size, snapshot.Hash
	}
	text := ReviewText(snapshot)
	sum := sha256.Sum256([]byte(text))
	return int64(len(text)), hex.EncodeToString(sum[:])
}

func RenderReview(beforePath, afterPath string, before, after File) diffmodel.ReviewFile {
	if before.Kind == Binary || after.Kind == Binary {
		// A text or link side against binary content has no row diff either.
		beforeSize, beforeHash := execSnapshotHash(before)
		afterSize, afterHash := execSnapshotHash(after)
		return diffmodel.RenderBinaryReviewFile(beforePath, afterPath, beforeSize, afterSize, beforeHash, afterHash)
	}
	review := diffmodel.RenderReviewFile(beforePath, afterPath, ReviewText(before), ReviewText(after))
	review.Link = before.Kind == Symlink || after.Kind == Symlink
	return review
}
