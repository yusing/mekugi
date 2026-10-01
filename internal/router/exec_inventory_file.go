package router

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Empty stamps never establish an unchanged file on an unsupported filesystem.
func execInventoryStamp(info os.FileInfo) string {
	changed, ok := execInfoChangeTime(info)
	if !ok {
		return ""
	}
	return execFileStamp(info) + ":" + strconv.FormatInt(changed.UnixNano(), 10) + ":" + strconv.FormatUint(uint64(info.Mode()), 10)
}

func snapshotInventoryFile(root, relative string, budget *int, unchangedStamp string) (execFileSnapshot, bool) {
	return readInventoryFile(root, relative, budget, unchangedStamp, false)
}

func snapshotInventoryBaseline(root, relative, stamp string) (execFileSnapshot, bool) {
	return readInventoryFile(root, relative, nil, stamp, true)
}

func readInventoryFile(root, relative string, budget *int, unchangedStamp string, baseline bool) (execFileSnapshot, bool) {
	snapshot := execFileSnapshot{Path: filepath.Join(root, relative)}
	file, link, err := openInventoryFile(root, relative)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, false
	}
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot, false
	}
	if file == nil {
		snapshot.Kind, snapshot.Link = execFileSymlink, link
		return snapshot, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		snapshot.Error = err.Error()
		return snapshot, false
	}
	stamp := execInventoryStamp(info)
	if baseline && (stamp == "" || stamp != unchangedStamp) {
		snapshot.Error = "before content not captured"
		return snapshot, false
	}
	if !baseline && unchangedStamp != "" && stamp == unchangedStamp {
		return snapshot, true
	}
	switch {
	case info.IsDir():
		snapshot.Kind = execFileDir
		return snapshot, false
	case !info.Mode().IsRegular():
		snapshot.Kind = execFileOther
		return snapshot, false
	}
	snapshot.Kind, snapshot.Size = execFileText, info.Size()
	if info.Size() > maxNativePatchFileBytes {
		snapshot.Error = "file exceeds the 8 MiB capture bound"
		return snapshot, false
	}
	if budget != nil && info.Size() > int64(*budget) {
		snapshot.Error = "capture budget exhausted"
		return snapshot, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxNativePatchFileBytes+1))
	if err != nil || len(data) > maxNativePatchFileBytes {
		snapshot.Error = "file could not be read within the capture bound"
		return snapshot, false
	}
	after, err := file.Stat()
	if err != nil || stamp != "" && execInventoryStamp(after) != stamp {
		snapshot.Error = "file changed while it was captured"
		return snapshot, false
	}
	snapshot.Kind, snapshot.Size, snapshot.Content, snapshot.Hash = execDataKind(data, budget)
	return snapshot, baseline
}
