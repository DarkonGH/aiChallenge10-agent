package llm

import (
	"encoding/json"
	"testing"
)

// TestParseExtraHeaders covers the LLM_EXTRA_HEADERS format: "Name: value"
// pairs, separated by ";" or a newline. It exists because the opencode gateway
// rejects a request without x-opencode-session, and the header must stay
// configuration, not code.
func TestParseExtraHeaders(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"empty", "", map[string]string{}},
		{"one", "x-opencode-session: goagent", map[string]string{"x-opencode-session": "goagent"}},
		{"semicolon", "a: 1; b: 2", map[string]string{"a": "1", "b": "2"}},
		{"newline", "a: 1\nb: 2", map[string]string{"a": "1", "b": "2"}},
		{"colon in value", "x-key: a:b", map[string]string{"x-key": "a:b"}},
		{"no pair", "мусор; b: 2", map[string]string{"b": "2"}},
		{"empty name", ": value", map[string]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseExtraHeaders(c.raw)
			if len(got) != len(c.want) {
				t.Fatalf("ParseExtraHeaders(%q) = %v, want %v", c.raw, got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Fatalf("ParseExtraHeaders(%q)[%q] = %q, want %q", c.raw, k, got[k], v)
				}
			}
		})
	}
}

// TestUsageCachedTokens checks both halves of implicit caching: the provider
// reports it in prompt_tokens_details.cached_tokens, and those tokens are a
// part of prompt_tokens, not an addition to them.
func TestUsageCachedTokens(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"ок"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":14438,"completion_tokens":7,"total_tokens":14445,
		         "prompt_tokens_details":{"cached_tokens":14336}}}`
	var resp chatResponseBody
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := resp.Usage.PromptTokensDetails.CachedTokens; got != 14336 {
		t.Fatalf("cached_tokens = %d, want 14336", got)
	}
	if got := resp.Usage.PromptTokens; got != 14438 {
		t.Fatalf("prompt_tokens = %d, want 14438", got)
	}
}

// TestRecordChargesCacheRate checks the stats side: cached prompt tokens are
// billed at the cache-read rate, so a fully cached request is ~50x cheaper
// than the same request without a cache.
func TestRecordChargesCacheRate(t *testing.T) {
	c := newClientAt("http://example.invalid", "", nil)

	u := usage{PromptTokens: 1_000_000, CompletionTokens: 0, TotalTokens: 1_000_000}
	u.PromptTokensDetails.CachedTokens = 1_000_000
	c.record("opencode-go/deepseek-v4.1-flash", u)

	st := c.Stats()
	if st.CachedTokens != 1_000_000 {
		t.Fatalf("CachedTokens = %d, want 1000000", st.CachedTokens)
	}
	if got, want := st.Cost, 0.006; got != want {
		t.Fatalf("Cost = %v, want %v (cache read rate)", got, want)
	}

	// The same million tokens without a cache costs the full input rate.
	fresh := newClientAt("http://example.invalid", "", nil)
	fresh.record("opencode-go/deepseek-v4.1-flash", usage{PromptTokens: 1_000_000})
	if got, want := fresh.Stats().Cost, 0.30; got != want {
		t.Fatalf("no-cache Cost = %v, want %v", got, want)
	}
}
