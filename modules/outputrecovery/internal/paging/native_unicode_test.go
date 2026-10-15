package paging

import (
	"strings"
	"testing"
)

func TestRetainedJSONPreservesLoneSurrogates(t *testing.T) {
	page, err := Page(t.Context(), ReadRequest{Stdout: `[{"name":"\ud800"}]`, StdoutKind: "json"}, 1000)
	if err != nil || !strings.Contains(page.Text, `\ud800`) {
		t.Fatalf("retained surrogate: %+v, %v", page, err)
	}
}
