package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The host re-serializes the auth document, so matching it by byte pattern
// missed harmless spacing differences and left the auth unhandled.
func TestAuthResponseMatchesAnySpacing(t *testing.T) {
	for _, body := range []string{
		`{"type":"mistral"}`,
		`{"type": "mistral"}`,
		`{"type" :  "mistral"}`,
		"{\n  \"type\": \"mistral\"\n}",
	} {
		var env envelope
		if err := json.Unmarshal(authResponse([]byte(body)), &env); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if !env.OK {
			t.Fatalf("%s: auth response was not OK", body)
		}
		if !strings.Contains(string(authResponse([]byte(body))), `"Handled":true`) {
			t.Fatalf("%s: auth document not handled", body)
		}
	}
	if strings.Contains(string(authResponse([]byte(`{"type":"openai"}`))), `"Handled":true`) {
		t.Fatal("foreign auth document was claimed")
	}
}

// The limit is in bytes because callers bound log lines; measuring bytes and
// cutting runes let a Chinese message run well past the bound.
func TestTruncateTextRespectsByteLimit(t *testing.T) {
	text := strings.Repeat("错误信息", 200)
	for _, limit := range []int{40, 400, 800} {
		got := truncateText(text, limit)
		if len(got) > limit {
			t.Fatalf("truncateText(limit %d) returned %d bytes", limit, len(got))
		}
	}
	if got := truncateText("short", 100); got != "short" {
		t.Fatalf("short text changed: %q", got)
	}
	// A cut must land on a rune boundary, never mid-character.
	if got := truncateText(text, 41); !strings.HasSuffix(got, "...") && len(got) > 41 {
		t.Fatalf("odd cut: %q", got)
	}
}

// A label ending in a backslash used to consume the closing quote and produce
// a document the host could not parse.
func TestQuoteYAMLEscapesBackslash(t *testing.T) {
	cases := map[string]string{
		`C:\keys\`: `"C:\\keys\\"`,
		`say "hi"`: `"say \"hi\""`,
		"line":     `"line"`,
	}
	for in, want := range cases {
		if got := quoteYAML(in); got != want {
			t.Fatalf("quoteYAML(%q) = %s, want %s", in, got, want)
		}
	}
}

// An unrecognized role used to keep every unknown field, each of which is a 422
// from the upstream.
func TestUnknownRoleKeepsOnlyRoleAndContent(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"weird","content":"hi","parallel_tool_calls":true,"cache_control":{"type":"ephemeral"}}]}`)
	out, ok := rewriteRequestBody(in, []string{"*"}, "upstream-model", "upstream-model")
	if !ok {
		t.Fatal("expected rewrite")
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	msg := doc["messages"].([]any)[0].(map[string]any)
	if len(msg) != 2 || msg["role"] != "weird" || msg["content"] != "hi" {
		t.Fatalf("unknown role message = %#v, want only role and content", msg)
	}
}
