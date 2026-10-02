package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestNativeInputOrderedContentAndMentions(t *testing.T) {
	c := startMockBridge(t, t.Context(), `require('node:readline').createInterface({input:process.stdin}).on('line', text => console.log(JSON.stringify({kind:'notice', text})));`, Config{Cwd: t.TempDir()})
	path := filepath.Join(t.TempDir(), "native image.png")
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	text := "@\"path with spaces.txt\" /native-skill 日本語\n"
	if err := c.SendInput(t.Context(), []session.InputPart{{Text: text}, {ImagePath: path}, {Text: "describe"}}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(nextEvent(t, c).Text), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"kind": "input", "content": []any{
		map[string]any{"type": "text", "text": text},
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(data.Bytes())}},
		map[string]any{"type": "text", "text": "describe"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("native content altered, reordered, or given client_composed semantics")
	}
}

func TestNativeInputImageTypes(t *testing.T) {
	for _, media := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		t.Run(media, func(t *testing.T) {
			var data bytes.Buffer
			im := image.NewRGBA(image.Rect(0, 0, 1, 1))
			var err error
			switch media {
			case "image/png":
				err = png.Encode(&data, im)
			case "image/jpeg":
				err = jpeg.Encode(&data, im, nil)
			case "image/gif":
				err = gif.Encode(&data, im, nil)
			case "image/webp":
				// DetectContentType requires the RIFF length and WEBP signature.
				data.WriteString("RIFF\x04\x00\x00\x00WEBPVP8 ")
			}
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "image")
			if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			encoded, kind, err := readInputImage(path)
			if err != nil || kind != media || encoded != base64.StdEncoding.EncodeToString(data.Bytes()) {
				t.Fatalf("media=%s error=%v", kind, err)
			}
		})
	}
}

func TestNativeInputRejectedBeforeTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad image")
	if err := os.WriteFile(path, []byte("plain text"), 0600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(t.TempDir(), "large image")
	if err := os.WriteFile(large, bytes.Repeat([]byte{0}, (5<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Client{} // Any transport attempt would panic: rejection is atomic.
	for name, parts := range map[string][]session.InputPart{
		"empty":       nil,
		"relative":    {{ImagePath: "relative.png"}},
		"missing":     {{ImagePath: path + ".missing"}},
		"directory":   {{ImagePath: filepath.Dir(path)}},
		"unsupported": {{Text: "must not send first"}, {ImagePath: path}},
		"image-limit": {{ImagePath: large}},
		"frame-limit": {{Text: strings.Repeat("x", frameLimit)}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := c.SendInput(t.Context(), parts); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.SendInput(ctx, []session.InputPart{{ImagePath: path}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled input: %v", err)
	}
}

func TestNativeCommandMetadataAndReplacement(t *testing.T) {
	var a adapter
	commands := []session.Command{{Name: "skill", Description: "Native skill", Arguments: "<file>", Aliases: []string{"alias"}, Builtin: true}}
	assertDecode(t, &a, `{"kind":"ready","commandInfo":[{"name":"skill","description":"Native skill","argumentHint":"<file>","aliases":["alias"],"builtin":true}]}`, []session.Event{{Kind: "ready", CommandInfo: commands}})
	assertDecode(t, &a, `{"kind":"event","event":{"type":"system","subtype":"commands_changed","commands":[]}}`, []session.Event{{Kind: "commands", CommandInfo: []session.Command{}}})
}
