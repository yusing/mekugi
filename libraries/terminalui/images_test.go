package terminalui

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi/sixel"
)

func TestImagePreviewProtocolsAndClipping(t *testing.T) {
	pixels := image.NewRGBA(image.Rect(0, 0, 320, 160))
	for y := range 160 {
		for x := range 320 {
			pixels.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, pixels); err != nil {
		t.Fatal(err)
	}
	preview := DecodePreview(&input)
	if preview == nil {
		t.Fatal("valid PNG refused")
	}
	for _, protocol := range []ImageProtocol{KittyImages, SixelImages} {
		columns, rows := preview.Size(40, protocol, 8, 16)
		p := ImagePlacement{Image: preview, Columns: columns, Rows: rows - 1, TotalRows: rows, First: 1}
		encoded := p.Encode(protocol, 8, 16, 42)
		var decoded image.Image
		var err error
		if protocol == KittyImages {
			var data strings.Builder
			for _, chunk := range strings.Split(encoded, "\x1b_G")[1:] {
				_, payload, _ := strings.Cut(chunk, ";")
				payload, _, _ = strings.Cut(payload, "\x1b\\")
				data.WriteString(payload)
			}
			raw, e := base64.StdEncoding.DecodeString(data.String())
			if e != nil {
				t.Fatal(e)
			}
			decoded, err = png.Decode(bytes.NewReader(raw))
		} else {
			_, payload, _ := strings.Cut(encoded, "q")
			decoded, err = new(sixel.Decoder).Decode(strings.NewReader(payload))
		}
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Bounds().Dx() > columns*8 || decoded.Bounds().Dy() > p.Rows*16 {
			t.Fatalf("protocol %d overflows cells: %v", protocol, decoded.Bounds())
		}
		if decoded.Bounds().Dy() == 0 {
			t.Fatal("clipped preview is empty")
		}
	}
	if columns, rows := preview.Size(40, SixelImages, 0, 0); columns != 0 || rows != 0 {
		t.Fatal("Sixel guessed cell size")
	}
	if DecodePreview(strings.NewReader("not an image")) != nil {
		t.Fatal("invalid image accepted")
	}
}

func TestImageTerminalCapabilityFallback(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want ImageProtocol
	}{
		{map[string]string{"TERM": "xterm-kitty"}, KittyImages},
		{map[string]string{"TERM_PROGRAM": "ghostty"}, KittyImages},
		{map[string]string{"TERM": "foot"}, SixelImages},
		{map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.6.1"}, KittyFileImages},
		{map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.5.9"}, NoImages},
		{map[string]string{"KITTY_WINDOW_ID": "1", "TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.5.9"}, KittyImages},
		{map[string]string{"WEZTERM_VERSION": "1", "TERM_PROGRAM": "iTerm.app", "TERM_PROGRAM_VERSION": "3.6.1"}, KittyImages},
		{map[string]string{"WT_SESSION": "session"}, SixelImages},
		{map[string]string{"TERM": "xterm-256color"}, NoImages},
		{map[string]string{"TERM": "xterm-kitty", "TMUX": "socket"}, NoImages},
		{map[string]string{"TERM": "foot", "ZELLIJ": "0"}, NoImages},
	} {
		if got := DetectImages(func(key string) string { return tc.env[key] }); got != tc.want {
			t.Fatalf("%v: %d", tc.env, got)
		}
	}
}

func TestImagePreviewRejectsOversizedRaster(t *testing.T) {
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := input.Bytes()
	binary.BigEndian.PutUint32(data[16:20], 8192)
	binary.BigEndian.PutUint32(data[20:24], 8192)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	if DecodePreview(bytes.NewReader(data)) != nil {
		t.Fatal("oversized raster decoded")
	}
}

func TestImagePreviewKittyFileLifetime(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TMPDIR", ".")
	preview := &ImagePreview{Pixels: image.NewRGBA(image.Rect(0, 0, 160, 80))}
	if err := preview.PrepareFile(); err != nil {
		t.Fatal(err)
	}
	path := preview.file
	defer preview.Close()
	if !filepath.IsAbs(path) {
		t.Fatal("local-file transport is relative to the terminal process")
	}
	encoded := (ImagePlacement{Image: preview, Columns: 20, Rows: 3, First: 1, TotalRows: 5}).Encode(KittyFileImages, 0, 0, 42)
	if !strings.Contains(encoded, "t=f") || !strings.Contains(encoded, "y=16") || !strings.Contains(encoded, "h=48") {
		t.Fatalf("invalid clipped file transport: %q", encoded)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	preview.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("transport file retained: %v", err)
	}
}
