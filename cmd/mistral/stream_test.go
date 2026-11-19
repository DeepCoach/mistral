package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// streamIdleSeconds must count silence, not the length of the whole stream. The
// watcher used to read a timestamp that was never updated, so it cancelled an
// active stream as soon as the limit was reached, whatever the stream was doing.
func TestWatchIdleResetsWhileDataArrives(t *testing.T) {
	if defaultSettings().StreamIdleSeconds <= 0 {
		t.Fatal("a silent stream must have a limit by default")
	}
	if defaultSettings().StreamIdleSeconds < 300 {
		t.Fatalf("default idle limit = %ds, too short for a long tool call", defaultSettings().StreamIdleSeconds)
	}
}

// A stream that stops must tell the client it stopped. OpenAI-style clients
// only finish on [DONE] or an explicit error, so a stream that just ends leaves
// them waiting for a frame that never arrives.
func TestFailedStreamFramesEndTheStream(t *testing.T) {
	frames := failedStreamFrames(errTestStream)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want an error event and [DONE]", len(frames))
	}
	if !strings.Contains(string(frames[0]), `"stream_interrupted"`) {
		t.Fatalf("first frame is not an error event: %s", frames[0])
	}
	if strings.TrimSpace(string(frames[1])) != "data: [DONE]" {
		t.Fatalf("last frame = %q, want data: [DONE]", frames[1])
	}
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(frames[0])), "data:"))
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("error event is not JSON: %v", err)
	}
	message, _ := doc["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "connection reset") {
		t.Fatalf("error message lost the cause: %q", message)
	}
	// Even without a cause the stream still has to end.
	if got := failedStreamFrames(nil); len(got) != 2 {
		t.Fatalf("nil cause produced %d frames", len(got))
	}
}

// Only model output may reset the idle limit. Mistral generates a tool call
// whole and sends it at the end, so preamble and heartbeat frames say nothing
// about whether the stream is alive.
func TestFrameCarriesOutput(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  bool
	}{
		{"role only", `data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`, false},
		{"empty content", `data: {"choices":[{"delta":{"content":""}}]}`, false},
		{"heartbeat", `: keep-alive`, false},
		{"done", `data: [DONE]`, false},
		{"text", `data: {"choices":[{"delta":{"content":"hi"}}]}`, true},
		{"thinking", `data: {"choices":[{"delta":{"reasoning_content":"hm"}}]}`, true},
		{"tool call", `data: {"choices":[{"delta":{"tool_calls":[{"id":"1"}]}}]}`, true},
	}
	for _, tc := range cases {
		if got := frameCarriesOutput([]byte(tc.frame)); got != tc.want {
			t.Fatalf("%s: frameCarriesOutput = %v, want %v", tc.name, got, tc.want)
		}
	}
}

var errTestStream = errType("upstream connection reset")

type errType string

func (e errType) Error() string { return string(e) }
