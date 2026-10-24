//go:build live

package tests_test

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// TestLiveMultiProtocolRouting checks per-request protocol selection against
// the real gateway: DeepSeek serves each client protocol natively, and the
// Gemini model is reached through GenerateContent from other clients.
func TestLiveMultiProtocolRouting(t *testing.T) {
	type format struct {
		name    string
		build   func(*testing.T, string, bool) (*http.Request, error)
		regular func(*testing.T, *http.Response)
		stream  func(*testing.T, *http.Response)
	}
	chat := format{"Chat", buildChatRequest, verifyChatNonStream, verifyChatStream}
	messages := format{"Messages", buildMessagesRequest, verifyMessagesNonStream, verifyMessagesStream}
	responses := format{"Responses", buildResponsesRequest, verifyResponsesNonStream, verifyResponsesStream}
	cases := []struct {
		model   string
		formats []format
	}{
		{"deepseek-v4-pro", []format{chat, messages, responses}},
		{"gemini-4-argon-contributor", []format{chat, messages, responses}},
	}
	client := &http.Client{Timeout: 60 * time.Second}
	for _, c := range cases {
		for _, f := range c.formats {
			for _, stream := range []bool{false, true} {
				name := c.model + "/" + f.name + map[bool]string{false: "/NonStream", true: "/Stream"}[stream]
				t.Run(name, func(t *testing.T) {
					time.Sleep(500 * time.Millisecond) // rate pacing
					req, err := f.build(t, modelID(c.model), stream)
					if err != nil {
						t.Fatalf("build request: %v", err)
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatalf("HTTP request failed: %v", err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						body, _ := io.ReadAll(resp.Body)
						t.Fatalf("expected HTTP 200, got %d: %s", resp.StatusCode, body)
					}
					if stream {
						f.stream(t, resp)
					} else {
						f.regular(t, resp)
					}
				})
			}
		}
	}
}
