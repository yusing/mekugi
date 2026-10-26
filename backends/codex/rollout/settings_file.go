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
	if !strings.HasSuffix(path, ".zst") {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return reverseRecords(threadID, path)
		}
		path += ".zst"
	}
	return reverseLines(compressedSettingsTail(threadID, path))
}

func compressedSettingsTail(threadID, path string) []byte {
	f, err := sourceparse.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil
	}
	decoded, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil
	}
	defer decoded.Close()
	reader := bufio.NewReaderSize(decoded, 64<<10)
	header, _ := reader.Peek(64 << 10)
	if !matchesIdentity(threadID, bytes.NewReader(header)) {
		return nil
	}

	// Zstandard streams require a forward decode. Keep only a circular tail,
	// independent of transcript size, and never decompress into the host store.
	data := make([]byte, 8<<20)
	offset, wrapped := 0, false
	var total int64
	for {
		n, err := reader.Read(data[offset:])
		total += int64(n)
		offset += n
		if offset == len(data) {
			offset, wrapped = 0, true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
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
