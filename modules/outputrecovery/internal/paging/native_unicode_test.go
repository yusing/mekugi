package paging

import (
	"strings"
	"testing"
)

func TestRetainedJSONPreservesLoneSurrogates(t *testing.T) {
	source := nativeSource(t, `[{"name":"\ud800"}]`, "", "json", "")
	page, err := source.Page(t.Context(), ReadRequest{}, 1000)
	if err != nil || !strings.Contains(page.Text, `\ud800`) {
		t.Fatalf("retained surrogate: %+v, %v", page, err)
	}
}
