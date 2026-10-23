package codexbinding

import (
	"encoding/json/jsontext"
	"fmt"
	"testing"
)

func TestUserImagesMatchMarkers(t *testing.T) {
	content := jsontext.Value(`[{"type":"text","text":"literal [Image 1] "},{"type":"localImage","path":"/tmp/first.png"},{"type":"image","url":"data:image/png;base64,cG5n"}]`)
	text, spans, ok := UserText(content)
	images := UserImages(content)
	if !ok || len(spans) != 2 || len(images) != 2 || images[0] != "/tmp/first.png" || images[1] != "data:image/png;base64,cG5n" {
		t.Fatalf("images %v, spans %v", images, spans)
	}
	for i, span := range spans {
		if text[span.Start:span.End] != fmt.Sprintf("[Image %d]", i+1) {
			t.Fatalf("marker %v in %q", span, text)
		}
	}
}
