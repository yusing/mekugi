package inferencegateway

import "testing"

func TestIsProviderLimitRejection(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		rejected   bool
	}{
		{"usage_limit", `{"error":{"type":"usage_limit_reached"}}`, true},
		{"failed_response", `{"response":{"error":{"code":"usage_limit_reached"}}}`, true},
		{"wrapped_provider", `{"error":{"provider_error":"{\"response\":{\"error\":{\"code\":\"server_is_overloaded\"}}}"}}`, true},
		{"overloaded", `{"error":{"type":"service_unavailable_error","message":"The servers are overloaded. Please try again later."}}`, true},
		{"capacity", `{"error":{"type":"service_unavailable_error","message":"Selected model is at capacity"}}`, true},
		{"stream_overloaded", `{"type":"response.failed","response":{"error":{"code":"overloaded_error"}}}`, true},
		{"canonical_overload", `{"error":{"code":"server_is_overloaded"}}`, true},
		{"ordinary_unavailable", `{"error":{"type":"service_unavailable_error","message":"upstream unavailable"}}`, false},
		{"quoted_capacity", `{"output":[{"text":"Selected model is at capacity"}]}`, false},
		{"ordinary_rate_limit", `{"error":{"type":"rate_limit_exceeded"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsProviderLimitRejection([]byte(tc.body)); got != tc.rejected {
				t.Fatalf("provider rejection = %t, want %t", got, tc.rejected)
			}
		})
	}
}
