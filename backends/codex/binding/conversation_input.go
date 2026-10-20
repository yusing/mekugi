// Source: apps/mekugi/internal/router/{app_server_clipboard,app_server_text_spans}.go@[e9da429a] submitted input encoding.
package binding

import "github.com/yusing/mekugi/backends/codex/appserver"

// TextElement uses part-relative byte ranges in the app-server input protocol.
type InputByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type InputTextElement struct {
	ByteRange   InputByteRange `json:"byteRange"`
	Placeholder string         `json:"placeholder"`
}

func TextInput(text string, elements []InputTextElement) map[string]any {
	part := appserver.Input(text)[0]
	part["text_elements"] = elements
	return part
}
func ImageInput(path string) map[string]any {
	return map[string]any{"type": "localImage", "path": path}
}

func AttachmentInput(text string) map[string]any { return appserver.Input(text)[0] }
