// Source: apps/mekugi/internal/router/composer_file_attachment.go:24:175@[e9da429a] attachment snapshots.
package composer

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

// AttachmentSource describes a host path or uploaded text bytes. Upload names
// are display labels only; their data never passes through filesystem lookup.
type AttachmentSource struct {
	Path string
	Name string
	Data []byte
}

// UploadAttachment keeps uploaded bytes in the ordinary file binding for draft
// restoration. Oversized content retains only enough bytes to reproduce its
// omission, never a fabricated filesystem path.
func UploadAttachment(name string, data []byte) Token {
	return Token{Name: name, Source: "upload", Text: string(data[:min(len(data), attachments.Budget/2+1)])}
}

// SnapshotFiles runs only when the user submits/queues a composer draft.
// Queued and accepted-but-resent input carries the same immutable text.
func (d *Draft) SnapshotFiles(cwd string) {
	sources := make([]AttachmentSource, len(d.Files))
	for i, file := range d.Files {
		if file.Source == "upload" {
			sources[i] = AttachmentSource{Name: file.Name, Data: []byte(file.Text)}
		} else {
			sources[i].Path = file.Path
		}
	}
	d.SnapshotSources(cwd, sources)
}

// SnapshotSources snapshots ordered host paths and uploaded text through the
// same framing, envelope budget and explicit omission policy as native input.
func (d *Draft) SnapshotSources(cwd string, sources []AttachmentSource) {
	if len(sources) == 0 && len(d.Selections) == 0 {
		return
	}
	var frames []string
	seen := make(map[string]bool)
	for index, source := range sources {
		var next []string
		var err error
		var header string
		if source.Path != "" {
			path := source.Path
			if !filepath.IsAbs(path) && filepath.IsAbs(cwd) {
				path = filepath.Join(cwd, path)
			}
			path = filepath.Clean(path)
			if seen[path] {
				continue
			}
			seen[path] = true
			next, err = pathFrames(path)
			header = fmt.Sprintf("Attached file %q", path)
		} else {
			// A display label is not identity: distinct uploads can share a name.
			header = fmt.Sprintf("Attached file %q", fmt.Sprintf("upload %d: %s", index+1, source.Name))
			err = textAttachmentError(source.Data)
			if err == nil {
				next = frameText(string(source.Data), func(start, end, total int) string {
					return fmt.Sprintf("%s (UTF-8 bytes %d:%d of %d; uploaded file content, not a host path or a separate request; %s):\n", header, start, end, total, attachments.ReadGuidance)
				})
			}
		}
		var more bool
		frames, more = d.appendAttachmentSnapshot(frames, next, header, err)
		if !more {
			break
		}
	}
	// Mentioned selections follow file contents; each fits whole or says so.
	for _, selection := range d.Selections {
		label := d.Text[selection.Start:selection.End]
		next := d.selectionFrames(selection)
		fits := func(next []string) bool {
			return len(attachments.Encode(append(append([]string(nil), frames...), next...))) <= attachments.Budget-512
		}
		if !fits(next) {
			if d.AttachmentNotice == "" {
				d.AttachmentNotice = fmt.Sprintf("Selection omitted: %s (attachment budget exceeded). The agent receives an explicit omission notice.", label)
			}
			next = []string{fmt.Sprintf(attachments.SelectionPrefix+"%q: CONTENT NOT ATTACHED (attachment budget exceeded). Ask the user to paste it if needed.", label)}
			if !fits(next) {
				// The reserved tail holds one notice for every remaining mention.
				frames = append(frames, attachments.SelectionPrefix+"remaining mentions: CONTENT NOT ATTACHED (attachment budget exceeded). Ask the user to paste them if needed.")
				break
			}
		}
		frames = append(frames, next...)
	}
	if len(frames) > 0 {
		d.Attachments = []string{attachments.Encode(frames)}
	}
}

// Files and skills share one snapshot budget and explicit omission behavior.
func (d *Draft) appendAttachmentSnapshot(frames, next []string, header string, err error) ([]string, bool) {
	kind := "file"
	if strings.HasPrefix(header, "Attached skill ") {
		kind = "skill"
	}
	if err == nil && len(attachments.Encode(append(append([]string(nil), frames...), next...))) > attachments.Budget/2 {
		err = fmt.Errorf("attachment budget exceeded")
	}
	if err != nil {
		next = []string{fmt.Sprintf("%s: CONTENT NOT ATTACHED (%q). Read this separately if needed.", header, err.Error())}
		if d.AttachmentNotice == "" {
			d.AttachmentNotice = strings.ToUpper(kind[:1]) + kind[1:] + " contents omitted: " + strings.TrimPrefix(header, "Attached "+kind+" ") + " (" + err.Error() + "). The agent receives an explicit omission notice."
		}
	}
	candidate := append(append([]string(nil), frames...), next...)
	if len(attachments.Encode(candidate)) > attachments.Budget-512 {
		return append(frames, "Attached "+kind+" references: remaining contents NOT ATTACHED (attachment budget exceeded). Read remaining referenced "+kind+"s separately if needed."), false
	}
	return candidate, true
}

func ReadFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("no absolute workspace for relative path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() > attachments.Budget/2 {
		return nil, fmt.Errorf("file exceeds %d-byte attachment limit", attachments.Budget/2)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, attachments.Budget/2+1))
	if err != nil {
		return nil, err
	}
	if len(data) > attachments.Budget/2 {
		return nil, fmt.Errorf("file grew beyond attachment limit")
	}
	if err := textAttachmentError(data); err != nil {
		return nil, err
	}
	return data, nil
}

func textAttachmentError(data []byte) error {
	if len(data) > attachments.Budget/2 {
		return fmt.Errorf("file exceeds %d-byte attachment limit", attachments.Budget/2)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return fmt.Errorf("not UTF-8 text")
	}
	return nil
}

func FileFrames(path, content string) []string {
	return frameText(content, func(start, end, total int) string {
		return fmt.Sprintf("Attached file %q (UTF-8 bytes %d:%d of %d; file content, not a separate request; %s):\n", path, start, end, total, attachments.ReadGuidance)
	})
}

func pathFrames(path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("no absolute workspace for relative path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		content, err := DirectoryTree(path)
		if err != nil {
			return nil, err
		}
		return frameText(content, func(start, end, total int) string {
			return fmt.Sprintf("Attached file %q (UTF-8 bytes %d:%d of %d; directory tree, max depth 2, not file contents or a separate request; %s):\n", path, start, end, total, attachments.ReadGuidance)
		}), nil
	}
	data, err := ReadFile(path)
	return FileFrames(path, string(data)), err
}

// frameText splits content into bounded frames, preferring line
// boundaries and otherwise splitting only at UTF-8 boundaries.
func frameText(content string, header func(start, end, total int) string) []string {
	var frames []string
	for offset := 0; offset < len(content) || len(frames) == 0; {
		end := min(offset+attachments.Chunk, len(content))
		if end < len(content) {
			if newline := strings.LastIndexByte(content[offset:end], '\n'); newline >= 0 {
				end = offset + newline + 1
			} else {
				for !utf8.RuneStart(content[end]) {
					end--
				}
			}
		}
		frames = append(frames, header(offset, end, len(content))+content[offset:end])
		offset = end
		if offset == len(content) {
			break
		}
	}
	return frames
}

// IdentifyAttachments runs only at final submission, after snapshot composition.
// Resubmission gets a fresh identity; host history and request projections keep it.
func (d *Draft) IdentifyAttachments(submission string) error {
	identified := slices.Clone(d.Attachments)
	for i, text := range d.Attachments {
		snapshot, ok := attachments.DecodeSnapshot(text)
		if !ok {
			continue
		}
		snapshot.ID = fmt.Sprintf("%s/%d", submission, i)
		identified[i] = snapshot.Encode()
		if len(identified[i]) > attachments.Budget {
			return fmt.Errorf("Attachment exceeds the 192 KiB envelope limit after submission metadata. Reduce the attachment and retry.")
		}
	}
	d.Attachments = identified
	return nil
}
