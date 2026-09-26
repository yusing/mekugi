package router

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
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

func (u *appServerUI) attachImage(path string) {
	u.recordDraft()
	u.typing = true
	if u.ownedImages == nil {
		u.ownedImages = make(map[string]bool)
	}
	u.ownedImages[path] = true
	at := u.cursor()
	label := fmt.Sprintf("[Image %d]", len(u.images)+1)
	u.insertDraft(label)
	u.images = append(u.images, composerImage{start: at, end: at + len(label), path: path})
	u.renumberImages()
	u.typing = false
	u.pruneDraftImages()
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
		u.status, u.alert = "Paste image: "+err.Error(), true
		return
	}
	u.attachImage(path)
	u.status, u.alert = "Image attached", false
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
