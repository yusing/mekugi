package claude

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/internal/session"
)

// User-selected attachments become supported native content blocks. No upload,
// image rewrite, native mention expansion or instruction injection occurs here.
func (c *Client) SendInput(ctx context.Context, parts []session.InputPart) error {
	if len(parts) == 0 {
		return fmt.Errorf("input is empty")
	}
	var blocks []map[string]any
	bytes := 0
	for _, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if part.ImagePath == "" {
			bytes += len(part.Text)
			blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
		} else {
			data, media, err := readInputImage(part.ImagePath)
			if err != nil {
				return err
			}
			bytes += len(data)
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": media, "data": data}})
		}
		if bytes > frameLimit {
			return fmt.Errorf("attachments exceed the 8 MiB bridge frame limit")
		}
	}
	return c.send(ctx, map[string]any{"kind": "input", "content": blocks})
}

func readInputImage(path string) (string, string, error) {
	if !filepath.IsAbs(path) {
		return "", "", fmt.Errorf("image path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", fmt.Errorf("attach image: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("image must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("attach image: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (5<<20)+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > 5<<20 {
		return "", "", fmt.Errorf("image exceeds the 5 MiB attachment limit")
	}
	media := http.DetectContentType(data)
	switch media {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", "", fmt.Errorf("native images require PNG, JPEG, GIF or WebP")
	}
	return base64.StdEncoding.EncodeToString(data), media, nil
}
