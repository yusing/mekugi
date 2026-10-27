package rollout

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

func settingsRecords(threadID, path string) iter.Seq[[]byte] {
	if !filepath.IsAbs(path) {
		return reverseLines(nil)
	}
	path = retainedPath(path)
	if !strings.HasSuffix(path, ".zst") {
		return reverseRecords(threadID, path)
	}
	return reverseLines(compressedSettingsTail(threadID, path))
}

func retainedPath(path string) string {
	if !strings.HasSuffix(path, ".zst") {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path + ".zst"
		}
	}
	return path
}

// withRetainedSource validates ownership before exposing plain or compressed
// observations. Reads never materialize or modify the retained host source.
func withRetainedSource(threadID, path string, read func(io.Reader) error) error {
	path = retainedPath(path)
	f, err := sourceparse.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() {
		return errors.New("retained source must be a regular file")
	}
	var source io.Reader = f
	if strings.HasSuffix(path, ".zst") {
		decoded, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return err
		}
		defer decoded.Close()
		source = decoded
	}
	reader := bufio.NewReaderSize(source, 64<<10)
	header, _ := reader.Peek(64 << 10)
	if !matchesIdentity(threadID, bytes.NewReader(header)) {
		return errors.New("retained source identity is unavailable")
	}
	return read(reader)
}

func compressedSettingsTail(threadID, path string) []byte {
	// Zstandard streams require a forward decode. Keep only a circular tail,
	// independent of transcript size, and never decompress into the host store.
	data := make([]byte, 8<<20)
	offset, wrapped := 0, false
	var total int64
	err := withRetainedSource(threadID, path, func(reader io.Reader) error {
		for {
			n, err := reader.Read(data[offset:])
			total += int64(n)
			offset += n
			if offset == len(data) {
				offset, wrapped = 0, true
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	})
	if err != nil {
		return nil
	}
	if !wrapped {
		return data[:offset]
	}
	data = append(data[offset:], data[:offset]...)
	if total > int64(len(data)) {
		_, data, _ = bytes.Cut(data, []byte{'\n'})
	}
	return data
}
