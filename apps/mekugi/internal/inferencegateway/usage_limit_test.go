package inferencegateway

import (
	"net/http"
	"testing"
	"time"
)

func TestUsageLimitProviderTiming(t *testing.T) {
	now := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name, body, retry string
		limited           bool
		deadline          *time.Time
	}{
		{"reset", `{"error":{"type":"usage_limit_reached","resets_at":1700000060}}`, "90", true, new(now.Add(time.Minute))},
		{"retry_seconds", `{"error":{"type":"usage_limit_reached"}}`, "90", true, new(now.Add(90 * time.Second))},
		{"retry_date", `{"error":{"type":"usage_limit_reached"}}`, now.Add(time.Hour).UTC().Format(http.TimeFormat), true, new(now.Add(time.Hour))},
		{"wrapped_socket", `{"type":"error","status":429,"error":{"type":"usage_limit_reached"},"headers":{"retry-after":"60","x-codex-primary-window-minutes":15}}`, "", true, new(now.Add(time.Minute))},
		{"nested_retry", `{"type":"response.failed","response":{"error":{"code":"usage_limit_reached","headers":{"retry-after":"60"}}}}`, "", true, new(now.Add(time.Minute))},
		{"failed_response", `{"response":{"error":{"code":"usage_limit_reached","resets_at":1700000060}}}`, "", true, new(now.Add(time.Minute))},
		{"no_timing", `{"error":{"type":"usage_limit_reached"}}`, "", true, nil},
		{"invalid_retry", `{"error":{"type":"usage_limit_reached"}}`, "later", true, nil},
		{"overloaded", `{"error":{"type":"service_unavailable_error","message":"The servers are overloaded. Please try again later."}}`, "", true, nil},
		{"capacity", `{"error":{"type":"service_unavailable_error","message":"Selected model is at capacity"}}`, "90", true, new(now.Add(90 * time.Second))},
		{"stream_overloaded", `{"type":"response.failed","response":{"error":{"code":"overloaded_error"}}}`, "", true, nil},
		{"ordinary_unavailable", `{"error":{"type":"service_unavailable_error","message":"upstream unavailable"}}`, "60", false, nil},
		{"quoted_capacity", `{"output":[{"text":"Selected model is at capacity"}]}`, "", false, nil},
		{"canonical_overload", `{"error":{"code":"server_is_overloaded"}}`, "", true, nil},
		{"numeric_retry", `{"type":"error","error":{"code":"server_is_overloaded"},"headers":{"retry-after":60}}`, "", true, new(now.Add(time.Minute))},
		{"padded_retry", `{"error":{"code":"server_is_overloaded"}}`, "\t60\t", true, new(now.Add(time.Minute))},
		{"ordinary_rate_limit", `{"error":{"type":"rate_limit_exceeded"}}`, "60", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deadline, limited, _ := ProviderWait([]byte(tc.body), http.Header{"Retry-After": {tc.retry}}, now)
			if limited != tc.limited || (deadline == nil) != (tc.deadline == nil) || deadline != nil && !deadline.Equal(*tc.deadline) {
				t.Fatalf("limit=%t deadline=%v", limited, deadline)
			}
		})
	}
}
