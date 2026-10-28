package main

import "testing"

// TestUsageInfoNormalizeFoldsProviderCacheShapes covers the three cache
// counter shapes upstream providers use. The statistics panel reports a
// cache hit rate, and that rate is only correct when every shape lands in
// the same CachedTokens field.
func TestUsageInfoNormalizeFoldsProviderCacheShapes(t *testing.T) {
	cases := []struct {
		name  string
		usage UsageInfo
		want  int
	}{
		{
			name:  "openai style details",
			usage: UsageInfo{PromptTokens: 1000, PromptTokensDetails: &UsageTokenDetails{CachedTokens: 250}},
			want:  250,
		},
		{
			name:  "anthropic style",
			usage: UsageInfo{PromptTokens: 1000, CacheReadInputTokens: 300},
			want:  300,
		},
		{
			name:  "deepseek style",
			usage: UsageInfo{PromptTokens: 1000, PromptCacheHitTokens: 400},
			want:  400,
		},
		{
			name:  "none reported",
			usage: UsageInfo{PromptTokens: 1000},
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.usage
			u.Normalize()
			if u.CachedTokens != tc.want {
				t.Fatalf("CachedTokens = %d, want %d", u.CachedTokens, tc.want)
			}
		})
	}
}

// TestUsageInfoNormalizeClampsCacheToPrompt guards the hit rate against
// exceeding 100%. A provider that reports a cache counter larger than the
// total prompt count would otherwise produce a nonsense rate.
func TestUsageInfoNormalizeClampsCacheToPrompt(t *testing.T) {
	u := UsageInfo{PromptTokens: 100, CacheReadInputTokens: 500}
	u.Normalize()
	if u.CachedTokens != 100 {
		t.Fatalf("CachedTokens = %d, want it clamped to 100", u.CachedTokens)
	}
	if rate := u.CacheHitRate(); rate != 1 {
		t.Fatalf("CacheHitRate = %f, want 1", rate)
	}
}

// TestUsageInfoNormalizeIsIdempotent makes sure calling Normalize twice does
// not double count.
func TestUsageInfoNormalizeIsIdempotent(t *testing.T) {
	u := UsageInfo{PromptTokens: 1000, PromptTokensDetails: &UsageTokenDetails{CachedTokens: 250}}
	u.Normalize()
	first := u.CachedTokens
	u.Normalize()
	if u.CachedTokens != first {
		t.Fatalf("Normalize is not idempotent: %d then %d", first, u.CachedTokens)
	}
}

// TestUsageInfoNormalizeHandlesNil makes sure a nil usage does not panic.
func TestUsageInfoNormalizeHandlesNil(t *testing.T) {
	var u *UsageInfo
	u.Normalize()
	if rate := u.CacheHitRate(); rate != 0 {
		t.Fatalf("CacheHitRate on nil = %f, want 0", rate)
	}
}

// TestUsageInfoNormalizeFoldsReasoningTokens covers the reasoning counter,
// which the statistics panel reports separately from the visible output.
func TestUsageInfoNormalizeFoldsReasoningTokens(t *testing.T) {
	u := UsageInfo{
		PromptTokens:           1000,
		CompletionTokens:       500,
		CompletionTokensDetails: &UsageTokenDetails{ReasoningTokens: 120},
	}
	u.Normalize()
	if u.ReasoningTokens != 120 {
		t.Fatalf("ReasoningTokens = %d, want 120", u.ReasoningTokens)
	}
}
