package errclass

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpstreamResponseError(t *testing.T) {
	const goLimit = `{"type":"error","error":{"type":"GoUsageLimitError","message":"Go usage limit exceeded"},"metadata":{"limitName":"5 hour"}}`
	retryAfter := http.Header{"Retry-After": []string{"3600"}}
	cases := []struct {
		name       string
		status     int
		headers    http.Header
		body       string
		scoped     bool
		retryAfter int
	}{
		{"go limit", 429, retryAfter, goLimit, true, 3600},
		{"go limit without retry-after", 429, nil, goLimit, true, 0},
		{"other 429 stays model-scoped", 429, retryAfter, `{"error":{"type":"rate_limit_error","message":"slow down"}}`, false, 0},
		{"go limit body on another status stays model-scoped", 503, retryAfter, goLimit, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := UpstreamResponseError(tc.status, tc.headers, []byte(tc.body))
			if e.CredentialScoped != tc.scoped || e.RetryAfterSeconds != tc.retryAfter {
				t.Fatalf("got scoped=%v retryAfter=%d, want %v %d", e.CredentialScoped, e.RetryAfterSeconds, tc.scoped, tc.retryAfter)
			}
			if tc.scoped && (e.Class != ClassQuota || !strings.Contains(e.Message, "5 hour window")) {
				t.Fatalf("got class=%s message=%q, want quota naming the window", e.Class, e.Message)
			}
		})
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]int{
		"120":                           120,
		"Mon, 23 Nov 2026 13:00:00 GMT": 3600,
		"Mon, 23 Nov 2026 11:00:00 GMT": 0,
		"soon":                          0,
	} {
		if got := retryAfterSeconds(in, now); got != want {
			t.Errorf("retryAfterSeconds(%q) = %d, want %d", in, got, want)
		}
	}
}
