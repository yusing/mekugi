package sessionapi

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestRemoteDetailContentRetainsPublicSelection(t *testing.T) {
	for _, authority := range []viewAuthority{remoteHumanView, localNativeView} {
		native := NewLive().Native()
		subscription, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", authority)
		if failure != nil {
			t.Fatal(failure)
		}
		frame := nativeTestFrame("thread", 1)
		frame.Details = &NativeDetails{Source: frame.View.Main, View: sessionpresentation.DetailView{ID: sessionpresentation.DetailID{Attachment: 1, Selection: 1}, Revision: 1, Pages: []sessionpresentation.DetailItem{
			{DetailPage: activity.DetailPage{ID: "large", Value: activity.Value{Code: strings.Repeat("retained full code\n", 4000)}}},
			{DetailPage: activity.DetailPage{ID: "secret", Value: activity.Value{Code: "PRIVATE", Questions: []activity.Question{{Text: "PRIVATE", Secret: true}}}}},
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
