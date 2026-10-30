package sessionapi

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestRemoteDetailContentRetainsPublicSelection(t *testing.T) {
	for _, authority := range []viewAuthority{remoteHumanView, localNativeView} {
		server := NewLive()
		native := server.Native()
		hub := NewHub(t.Context(), nil)
		defer hub.Close()
		registerTestSession(hub, "thread", server, Summary{Thread: "thread", Workspace: "/workspace"})
		subscription, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", authority)
		if failure != nil {
			t.Fatal(failure)
		}
		frame := nativeTestFrame("thread", 1)
		frame.Details = &NativeDetails{Source: frame.View.Main, View: sessionpresentation.DetailView{ID: sessionpresentation.DetailID{Attachment: 1, Selection: 1}, Revision: 1, Pages: []sessionpresentation.DetailItem{
			{ID: "large", Code: strings.Repeat("retained full code\n", 4000)},
			{ID: "secret", Code: "PRIVATE", Questions: []activity.Question{{Text: "PRIVATE", Secret: true}}},
		}}}
		if err := native.Publish(request.Attachment, frame); err != nil {
			t.Fatal(err)
		}
		publication, ok := subscription.native.pop()
		var event NativeEvent
		if !ok || json.Unmarshal(publication.Data, &event, nativeEncoding) != nil {
			t.Fatal("missing detail publication")
		}
		if authority == localNativeView {
			if event.Frame.Details.Content != nil || event.Frame.Details.View.Pages[0].Code != frame.Details.View.Pages[0].Code {
				t.Fatal("local detail shape changed")
			}
			subscription.close()
			continue
		}
		ref := event.Frame.Details.Content
		if ref == nil || len(event.Frame.Details.View.Pages) != 0 || len(publication.Data) > 32<<10 {
			t.Fatal("remote detail content remained inline")
		}
		read, failure := native.readDetails(*ref)
		if failure != nil {
			t.Fatal(failure)
		}
		details := read.(ContentResult).Details
		if details.View.Pages[0].Code != frame.Details.View.Pages[0].Code || strings.Contains(string(testJSON(t, details)), "PRIVATE") {
			t.Fatal("detail content lost full code or exposed secret data")
		}
		response := httptest.NewRecorder()
		params := testJSON(t, RPCParams{Thread: "thread", Body: testJSON(t, ref)})
		hub.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, ResourcePath+"content", bytes.NewReader(params)))
		expected, err := json.Marshal(read, nativeEncoding)
		if err != nil || response.Code != 200 || !bytes.Equal(response.Body.Bytes(), expected) {
			t.Fatalf("HTTP detail content changed its wire shape: %s, %v", response.Body, err)
		}
		frame.Details.View.Revision++
		if err := native.Publish(request.Attachment, frame); err != nil {
			t.Fatal(err)
		}
		if _, failure := native.readDetails(*ref); failure == nil || failure.Data.Status != 409 {
			t.Fatal("stale details remained readable")
		}
		subscription.close()
		if _, failure := native.readDetails(*ref); failure == nil {
			t.Fatal("closed view retained readable details")
		}
	}
}
