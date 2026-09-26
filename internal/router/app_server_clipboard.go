package router

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

type composerImage struct {
	start, end int
	path       string
}

func (u *appServerUI) renumberImages() {
	slices.SortFunc(u.images, func(a, b composerImage) int { return a.start - b.start })
	at := u.cursor()
	for i := range u.images {
		attachment := &u.images[i]
		label := fmt.Sprintf("[Image %d]", i+1)
		delta := len(label) - (attachment.end - attachment.start)
		u.draft = u.draft[:attachment.start] + label + u.draft[attachment.end:]
		if at >= attachment.end {
			at += delta
		}
		attachment.end += delta
		for j := i + 1; j < len(u.images); j++ {
			u.images[j].start += delta
			u.images[j].end += delta
		}
	}
	u.cursorBack = len(u.draft) - at
}

// attachImage takes ownership of a clipboard file for cleanup.
func (u *appServerUI) attachImage(path string) {
	u.insertImage(path)
	if u.ownedImages == nil {
		u.ownedImages = make(map[string]bool)
	}
	u.ownedImages[path] = true
}

// insertImage is its own undoable edit.
func (u *appServerUI) insertImage(path string) {
	u.run = runNone
	at := u.cursor()
	label := fmt.Sprintf("[Image %d]", len(u.images)+1)
	u.insertDraft(label)
	u.images = append(u.images, composerImage{start: at, end: at + len(label), path: path})
	u.renumberImages()
	u.run = runNone
	u.pruneDraftImages()
}

// finishPaste inserts a bracketed paste as one edit. A lone path to an image
// file, as terminals paste for copied or dropped files, attaches that file
// instead, followed by a space as in Codex. The user's file is never removed.
func (u *appServerUI) finishPaste() {
	text := string(u.pasted)
	u.pasted = nil
	path, ok := pastedImagePath(text)
	if !ok {
		if text != "" {
			u.insertDraft(text)
			u.run = runNone
		}
		return
	}
	u.insertImage(path)
	u.run = runInsert // The separating space belongs to the attachment's edit.
	u.insertDraft(" ")
	u.run = runNone
}

// Source: codex-rs/tui/src/clipboard_paste.rs:251:287 and
// bottom_pane/chat_composer.rs:1187:1208@86be5320b068ef67b56348b02aa8c33706955da6.
// Accept a file:// URL, a literal path, or one shell word naming an absolute
// path to a decodable image; anything else is pasted as text.
func pastedImagePath(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 4096 {
		return "", false
	}
	if location, err := url.Parse(text); err == nil && location.Scheme == "file" {
		if location.Host != "" && location.Host != "localhost" {
			return "", false
		}
		return location.Path, imageFile(location.Path)
	}
	if imageFile(text) {
		return text, true
	}
	path, ok := shellWord(text)
	return path, ok && imageFile(path)
}

func imageFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	// Check the type before opening, so a pasted FIFO or device cannot block.
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	_, _, err = image.DecodeConfig(file)
	return err == nil
}

// shellWord unquotes text that is exactly one POSIX shell word, as terminals
// escape pasted file paths.
func shellWord(text string) (string, bool) {
	var word strings.Builder
	quote := byte(0)
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case c == '\\' && i+1 < len(text) && (quote == 0 || strings.IndexByte("$`\"\\\n", text[i+1]) >= 0):
			i++
			word.WriteByte(text[i])
		case quote == '"':
			if c == '"' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t' || c == '\n':
			return "", false
		default:
			word.WriteByte(c)
		}
	}
	return word.String(), quote == 0 && word.Len() > 0
}

func (u *appServerUI) composerInput() []map[string]any {
	var input []map[string]any
	at := 0
	for _, attachment := range u.images {
		if attachment.start > at {
			input = append(input, appServerInput(u.draft[at:attachment.start])...)
		}
		input = append(input, map[string]any{"type": "localImage", "path": attachment.path})
		at = attachment.end
	}
	if at < len(u.draft) || len(input) == 0 {
		input = append(input, appServerInput(u.draft[at:])...)
	}
	return input
}

func (u *appServerUI) pasteImage() {
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	path, err := clipboardImage(ctx)
	if err != nil {
		u.notice, u.noticeAlert = "Paste image: "+err.Error(), true
		return
	}
	// The highlighted placeholder is the feedback; it also clears any earlier notice.
	u.attachImage(path)
}

// The retained local file lets Codex own image processing and XML framing, and
// remains available to resumed turns just like Codex's own pasted-image files.
func clipboardImage(ctx context.Context) (string, error) {
	var data []byte
	var err error
	switch runtime.GOOS {
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			data, err = exec.CommandContext(ctx, "wl-paste", "--no-newline", "--type", "image/png").Output()
		}
		if len(data) == 0 || err != nil {
			data, err = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
		}
	case "darwin":
		// AppleScript returns PNG bytes as hex without allocating another image file.
		data, err = exec.CommandContext(ctx, "osascript", "-e", `get the clipboard as «class PNGf»`).Output()
		if err == nil {
			encoded := strings.TrimSpace(string(data))
			encoded = strings.TrimPrefix(encoded, "«data PNGf")
			encoded = strings.TrimSuffix(encoded, "»")
			data, err = hex.DecodeString(encoded)
		}
	default:
		return "", errors.New("image clipboard is supported on Linux (wl-paste or xclip) and macOS")
	}
	if err != nil {
		return "", fmt.Errorf("cannot read PNG clipboard (Linux requires wl-paste or xclip): %w", err)
	}
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return "", errors.New("clipboard does not contain a PNG image")
	}
	file, err := os.CreateTemp("", "mekugi-clipboard-*.png")
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(data)
	err = errors.Join(writeErr, file.Close())
	if err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}
