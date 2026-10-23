package sessionapi

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestPublishedImageRead(t *testing.T) {
	workspace := t.TempDir()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "image.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"image.png", "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)} {
		s := New()
		entry := activity.Entry{Facts: activity.Facts{Seq: 1, Agent: "You", Kind: "text", Text: "[Image 1]", Images: []string{source}}}
		if err := s.Publish(Frame{Session: Session{Thread: "main", Workspace: workspace}, Main: &activity.Publication{Entries: []activity.Entry{entry}}}); err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(snapshot.Data), source) {
			t.Fatal("image source leaked into publication")
		}
		request := map[string]any{"seq": 1, "agent": "You", "index": 0, "image": imageIdentity(source)}
		body, _ := json.Marshal(request)
		got, failure := s.readImage(t.Context(), requestTarget{"main", workspace}, body)
		if failure != nil {
			t.Fatal(failure)
		}
		encoded, _ := json.Marshal(got)
		if !strings.Contains(string(encoded), `"mime":"image/png"`) {
			t.Fatalf("unexpected image: %s", encoded)
		}
		for _, field := range []string{"seq", "agent", "index", "image"} {
			bad := map[string]any{"seq": 1, "agent": "You", "index": 0, "image": imageIdentity(source)}
			switch field {
			case "seq":
				bad[field] = 2
			case "agent":
				bad[field] = "/root/other"
			case "index":
				bad[field] = -1
			case "image":
				bad[field] = workspace
			}
			value, _ := json.Marshal(bad)
			if _, failure := s.readImage(t.Context(), requestTarget{"main", workspace}, value); failure == nil {
				t.Fatalf("accepted invalid %s", field)
			}
		}
		if _, failure := s.readImage(t.Context(), requestTarget{"other", workspace}, body); failure == nil {
			t.Fatal("accepted foreign thread")
		}
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		if _, failure := s.readImage(cancelled, requestTarget{"main", workspace}, body); failure == nil {
			t.Fatal("accepted cancelled request")
		}
	}
}

func TestPublishedImageFailures(t *testing.T) {
	for _, source := range []string{"missing.png", "https://example.com/image.png", "data:image/png;base64,bm90IGFuIGltYWdl", "data:image/png,plain"} {
		s := New()
		entry := activity.Entry{Facts: activity.Facts{Seq: 1, Agent: "You", Kind: "text", Images: []string{source}}}
		if err := s.Publish(Frame{Session: Session{Thread: "main"}, Main: &activity.Publication{Entries: []activity.Entry{entry}}}); err != nil {
			t.Fatal(err)
		}
		body := jsontext.Value(`{"seq":1,"agent":"You","index":0,"image":"` + imageIdentity(source) + `"}`)
		if _, failure := s.readImage(t.Context(), requestTarget{"main", t.TempDir()}, body); failure == nil {
			t.Fatalf("accepted %q", source)
		}
	}
}
