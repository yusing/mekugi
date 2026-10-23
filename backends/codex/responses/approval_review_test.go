package responses

import (
	"net/http"
	"testing"
)

func TestApprovalReviewIdentityAndForwarding(t *testing.T) {
	for _, tc := range []struct {
		header, value, kind string
		review              bool
	}{
		{GuardianHeader, "reviewer", "", true},
		{SubagentHeader, "guardian", "", true},
		{"", "", "guardian", true},
		{GuardianHeader, "classifier", "", false},
		{SubagentHeader, "collab_spawn", "thread_spawn", false},
		{"", "", "review", false},
		{"", "", "", false},
	} {
		headers := make(http.Header)
		if tc.header != "" {
			headers.Set(tc.header, tc.value)
		}
		forwarded := make(http.Header)
		ForwardHeaders(forwarded, headers)
		if tc.header != "" && forwarded.Get(tc.header) != tc.value {
			t.Fatalf("lost host header: %v", headers)
		}
		if got := ApprovalReview(forwarded, TurnMetadata{SubagentKind: tc.kind}); got != tc.review {
			t.Errorf("%+v: review=%v", tc, got)
		}
	}
}
