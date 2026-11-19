package main

import (
	"math"
	"testing"
)

func TestSummarizeLogsKeepsCacheRatePrecision(t *testing.T) {
	summary := summarizeLogs([]requestLog{
		{Status: 200, PromptTokens: 1000, CachedTokens: 913},
		{Status: 200, PromptTokens: 2000, CachedTokens: 1800},
	})
	rate, ok := summary["cache_hit_rate"].(float64)
	if !ok {
		t.Fatalf("cache_hit_rate missing: %#v", summary)
	}
	if math.Abs(rate-0.9043) > 0.00001 {
		t.Fatalf("cache_hit_rate = %v, want 0.9043", rate)
	}
}

// joinLines ran in quadratic time because it grew the string one concatenation
// at a time, and every append then paid for a full rewrite of the log.
func TestJoinLinesKeepsInput(t *testing.T) {
	lines := []string{"a", "bb", "", "ccc"}
	if got := joinLines(lines); got != "a\nbb\n\nccc\n" {
		t.Fatalf("joinLines = %q", got)
	}
	if got := joinLines(nil); got != "" {
		t.Fatalf("joinLines(nil) = %q", got)
	}
}

func TestSplitLinesRoundTrips(t *testing.T) {
	lines := []string{"one", "two", "three"}
	if got := splitLines(joinLines(lines)); len(got) != len(lines) {
		t.Fatalf("splitLines(joinLines(x)) = %#v, want %#v", got, lines)
	}
}
