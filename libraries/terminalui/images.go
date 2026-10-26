package terminalui

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/charmbracelet/x/ansi/sixel"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// ImageProtocol is a renderer capability, never a session preference.
type ImageProtocol uint8

const (
	NoImages ImageProtocol = iota
	KittyImages
	SixelImages
	KittyFileImages
)

// DetectImages follows stock terminal support and multiplexer fallback.
// Source: codex-rs/tui/src/pets/image_protocol.rs:112:198@0df4aa677.
func DetectImages(getenv func(string) string) ImageProtocol {
	for _, key := range []string{"TMUX", "TMUX_PANE", "ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_VERSION"} {
		if getenv(key) != "" {
			return NoImages
		}
	}
	if getenv("KITTY_WINDOW_ID") != "" || getenv("WEZTERM_EXECUTABLE") != "" || getenv("WEZTERM_VERSION") != "" {
		return KittyImages
	}
	term := strings.ToLower(getenv("TERM") + " " + getenv("TERM_PROGRAM"))
	if strings.Contains(strings.ToLower(getenv("TERM_PROGRAM")), "iterm") {
		parts := strings.Split(getenv("TERM_PROGRAM_VERSION"), ".")
		if len(parts) >= 2 {
			major, e1 := strconv.Atoi(parts[0])
			minor, e2 := strconv.Atoi(parts[1])
			if e1 == nil && e2 == nil && (major > 3 || major == 3 && minor >= 6) {
				return KittyFileImages
			}
		}
		return NoImages
	}
	for _, name := range []string{"kitty", "ghostty", "wezterm"} {
		if strings.Contains(term, name) {
			return KittyImages
		}
	}
	for _, name := range []string{"sixel", "mlterm", "foot"} {
		if strings.Contains(term, name) {
			return SixelImages
		}
	}
	if getenv("WT_SESSION") != "" {
		return SixelImages
	}
	return NoImages
}

// ImagePreview retains only a bounded thumbnail. Loading belongs to acquisition,
// not painting; unchanged placements need no repeated encoding.
type ImagePreview struct {
	Pixels *image.RGBA
	file   string
}

// PrepareFile acquires iTerm's local PNG transport outside painting. The cache
// retains this file while its placements are usable and closes it on retirement.
func (p *ImagePreview) PrepareFile() error {
	file, err := os.CreateTemp("", "tty-graphics-protocol-*.png")
	if err != nil {
		return err
	}
	err = png.Encode(file, p.Pixels)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(file.Name())
		return err
	}
	p.file, err = filepath.Abs(file.Name())
	if err != nil {
		_ = os.Remove(file.Name())
	}
	return err
}

func (p *ImagePreview) Close() {
	if p != nil && p.file != "" {
		_ = os.Remove(p.file)
		p.file = ""
	}
}

// ImageSource binds a preview to an immutable published entry, so a later view
// of a replaced file cannot borrow an earlier item's cached pixels.
type ImageSource struct {
	Path            string
	Entry, Revision uint64
	Dialog          uint64 // Local file selection, distinct from transcript entries.
}

const ImageInputLimit = 32 << 20

func DecodePreview(r io.Reader) *ImagePreview {
	data, err := io.ReadAll(io.LimitReader(r, ImageInputLimit+1))
	if err != nil || len(data) > ImageInputLimit {
		return nil
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > ImageInputLimit/8 {
		return nil
	}
	pixels, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	w, h := imageFit(config.Width, config.Height, 384, 160)
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), pixels, pixels.Bounds(), draw.Src, nil)
	return &ImagePreview{Pixels: scaled}
}

// imageFit preserves aspect ratio without upscaling.
func imageFit(w, h, maxW, maxH int) (int, int) {
	if w > maxW {
		h, w = max(1, h*maxW/w), maxW
	}
	if h > maxH {
		w, h = max(1, w*maxH/h), maxH
	}
	return max(1, w), max(1, h)
}

// ImagePlacement uses terminal-cell coordinates, with First naming clipped rows.
type ImagePlacement struct {
	Image                                 *ImagePreview
	X, Y, Columns, Rows, First, TotalRows int
}

func (p *ImagePreview) Size(width int, protocol ImageProtocol, cellWidth, cellHeight int) (int, int) {
	if p == nil || width <= 2 || protocol == NoImages {
		return 0, 0
	}
	if protocol == KittyImages || protocol == KittyFileImages {
		cellWidth, cellHeight = 8, 16
	}
	if cellWidth <= 0 || cellHeight <= 0 {
		return 0, 0
	}
	maxW, maxH := min(48, width-2)*cellWidth, 6*cellHeight
	if protocol == SixelImages {
		maxW, maxH = min(maxW, 160), min(maxH, 80)
	}
	w, h := imageFit(p.Pixels.Bounds().Dx(), p.Pixels.Bounds().Dy(), maxW, maxH)
	return (w + cellWidth - 1) / cellWidth, (h + cellHeight - 1) / cellHeight
}

func (p ImagePlacement) Encode(protocol ImageProtocol, cellWidth, cellHeight, id int) string {
	if p.Image == nil || p.Rows <= 0 || p.Columns <= 0 || p.TotalRows <= 0 {
		return ""
	}
	pixels := p.Image.Pixels
	if protocol == KittyFileImages {
		if p.Image.file == "" {
			return ""
		}
		top := p.First * pixels.Bounds().Dy() / p.TotalRows
		bottom := (p.First + p.Rows) * pixels.Bounds().Dy() / p.TotalRows
		options := kitty.Options{Action: kitty.TransmitAndPut, Transmission: kitty.File, Format: kitty.PNG, ID: id, Columns: p.Columns, Rows: p.Rows, Y: top, Width: pixels.Bounds().Dx(), Height: bottom - top, Quiet: 2, DoNotMoveCursor: true}
		return ansi.KittyGraphics([]byte(base64.StdEncoding.EncodeToString([]byte(p.Image.file))), options.Options()...)
	}
	if protocol == KittyImages {
		cellWidth, cellHeight = 8, 16
	}
	if cellWidth <= 0 || cellHeight <= 0 {
		return ""
	}
	w, h := imageFit(pixels.Bounds().Dx(), pixels.Bounds().Dy(), p.Columns*cellWidth, p.TotalRows*cellHeight)
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), pixels, pixels.Bounds(), draw.Src, nil)
	top, bottom := min(h, p.First*cellHeight), min(h, (p.First+p.Rows)*cellHeight)
	if protocol == KittyImages {
		top, bottom = p.First*h/p.TotalRows, (p.First+p.Rows)*h/p.TotalRows
	}
	if top >= bottom {
		return ""
	}
	cropped := scaled.SubImage(image.Rect(0, top, w, bottom))
	var b bytes.Buffer
	if protocol == KittyImages {
		err := kitty.EncodeGraphics(&b, cropped, &kitty.Options{Action: kitty.TransmitAndPut, Transmission: kitty.Direct, Format: kitty.PNG, ID: id, Columns: p.Columns, Rows: p.Rows, Quiet: 2, Chunk: true, DoNotMoveCursor: true})
		if err != nil {
			return ""
		}
		return b.String()
	}
	if err := new(sixel.Encoder).Encode(&b, cropped); err != nil {
		return ""
	}
	return ansi.SixelGraphics(0, 1, 0, b.Bytes())
}

func DeleteImage(id int) string {
	options := kitty.Options{Action: kitty.Delete, Delete: kitty.DeleteID, DeleteResources: true, ID: id, Quiet: 2}
	return ansi.KittyGraphics(nil, options.Options()...)
}
