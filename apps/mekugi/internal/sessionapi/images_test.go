package sessionapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
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
	for _, tc := range []struct {
		source, agent, kind string
		child               bool
		caller              string
	}{
		{"image.png", "You", "text", false, ""},
		{"data:image/png;base64," + base64.StdEncoding.EncodeToString(data), "You", "text", false, ""},
		{"image.png", "Main", "tool", false, ""},
		{"image.png", "Main", "tool", false, "/root/worker"},
		{filepath.Join(workspace, "image.png"), "/root/images", "tool", true, ""},
	} {
		source := tc.source
		s := New()
		sourceEntry := activity.Entry{Seq: 1, Agent: tc.agent, Kind: tc.kind, Text: "[Image 1]", Images: []string{source}}
		frame := Frame{Session: Session{Thread: "main", Caller: tc.caller, Workspace: workspace}, Main: &activity.Publication{Entries: []activity.Entry{sourceEntry}}}
		if tc.child {
			frame.Main, frame.Agents = nil, frame.Main
		}
		if err := s.Publish(frame); err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(snapshot.Data), source) {
			t.Fatal("image source leaked into publication")
		}
		var publication message
		if err := json.Unmarshal(snapshot.Data, &publication); err != nil {
			t.Fatal(err)
		}
		feed := publication.Main
		if tc.child {
			feed = publication.Agents
		}
		var published entry
		if err := json.Unmarshal(feed.Entries[0], &published); err != nil {
			t.Fatal(err)
		}
		request := map[string]any{"seq": 1, "agent": published.Agent, "index": 0, "image": imageIdentity(source)}
		body, _ := json.Marshal(request)
		got, failure := s.readImage(t.Context(), requestTarget{"main", workspace}, body)
		if failure != nil {
			t.Fatal(failure)
		}
		image := got.(*imageResource)
		if image.mime != "image/png" || !bytes.Equal(image.data, data) {
			t.Fatal("unexpected image bytes or type")
		}
		for _, field := range []string{"seq", "agent", "index", "image"} {
			bad := map[string]any{"seq": 1, "agent": published.Agent, "index": 0, "image": imageIdentity(source)}
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
		entry := activity.Entry{Seq: 1, Agent: "You", Kind: "text", Images: []string{source}}
		if err := s.Publish(Frame{Session: Session{Thread: "main"}, Main: &activity.Publication{Entries: []activity.Entry{entry}}}); err != nil {
			t.Fatal(err)
		}
		body := jsontext.Value(`{"seq":1,"agent":"You","index":0,"image":"` + imageIdentity(source) + `"}`)
		if _, failure := s.readImage(t.Context(), requestTarget{"main", t.TempDir()}, body); failure == nil {
			t.Fatalf("accepted %q", source)
		}
	}
}

func TestDraftPathImageAdmission(t *testing.T) {
	workspace := t.TempDir()
	data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=")
	for name, body := range map[string][]byte{"image": data, "text": []byte("ordinary text")} {
		if err := os.WriteFile(filepath.Join(workspace, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	hub := NewHub(t.Context(), &managedLauncher{})
	defer hub.Close()
	registerTestSession(hub, "selected", NewLive(), Summary{Thread: "selected", Workspace: workspace})
	for _, tc := range []struct {
		name, thread, path, directory string
		status                        int
	}{
		{"initial", "", "image", workspace, 0},
		{"live", "selected", "image", "", 0},
		{"absolute", "selected", filepath.Join(workspace, "image"), "", 0},
		{"stale thread", "gone", "image", "", 404},
		{"override", "selected", "image", workspace, 400},
		{"missing workspace", "", "image", "", 400},
		{"missing file", "selected", "missing", "", 404},
		{"not regular", "selected", workspace, "", 404},
		{"text", "selected", "text", "", 415},
		{"remote", "selected", "https://example.com/image.png", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"path": tc.path, "workspace": tc.directory})
			params, _ := json.Marshal(RPCParams{Thread: tc.thread, Body: body})
			request := httptest.NewRequest(http.MethodPost, ResourcePath+"attachment-image", bytes.NewReader(params))
			reply := httptest.NewRecorder()
			hub.Handler().ServeHTTP(reply, request)
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			if reply.Code != status {
				t.Fatalf("preview status %d: %s", reply.Code, reply.Body)
			}
			if tc.status == 0 && (reply.Header().Get("Content-Type") != "image/png" || !bytes.Equal(reply.Body.Bytes(), data)) {
				t.Fatal("preview lost selected image")
			}

		})
	}
}

func TestNativeImageSourcesExcludeUserPayloads(t *testing.T) {
	entries := []activity.Entry{{Seq: 1, Kind: "text", Images: []string{"data:image/png;base64,user-private"}}, {Seq: 2, Kind: "tool", Images: []string{"/work/artifact.png"}}}
	wire, err := json.Marshal(&activity.Publication{Entries: entries}, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	var got activity.Publication
	if err := json.Unmarshal(wire, &got, nativeEncoding); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries[0].Images) != 0 || len(got.Entries[1].Images) != 1 || got.Entries[1].Images[0] != "/work/artifact.png" {
		t.Fatalf("native source projection: %s", wire)
	}
	if len(entries[0].Images) != 1 {
		t.Fatal("projection mutated semantic source")
	}
}
