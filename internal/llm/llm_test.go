package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRequestHookChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != "calc" {
			t.Errorf("tools = %+v", req.Tools)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer srv.Close()

	client := newTestClient(srv)
	var gotModel string
	var gotBody []byte
	client.SetRequestHook(func(model string, payload []byte) {
		gotModel = model
		gotBody = append([]byte(nil), payload...)
	})
	_, err := client.Chat(context.Background(), "test-model",
		[]Message{{Role: "user", Content: "2+2"}},
		[]ToolSchema{{Type: "function", Function: ToolFunction{Name: "calc", Parameters: map[string]any{"type": "object"}}}}, 0)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if gotModel != "test-model" {
		t.Errorf("hook model = %q", gotModel)
	}
	if !strings.Contains(string(gotBody), `"calc"`) {
		t.Errorf("hook body has no calc schema: %s", gotBody)
	}
}

func TestRequestHookStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody(
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "ok"}, "finish_reason": "stop"}}},
		)))
	}))
	defer srv.Close()

	client := newTestClient(srv)
	var calls int
	client.SetRequestHook(func(string, []byte) { calls++ })
	if _, err := client.ChatStream(context.Background(), "m", []Message{{Role: "user", Content: "x"}}, nil, 0, nil, nil); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if calls != 1 {
		t.Errorf("hook calls = %d, want 1", calls)
	}
}
