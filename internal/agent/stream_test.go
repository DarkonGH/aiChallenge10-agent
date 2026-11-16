package agent

import (
	"context"
	"encoding/json"
	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
)

// sseBody builds an SSE stream body from chunk objects.
func sseBody(chunks ...any) string {
	var b strings.Builder
	for _, c := range chunks {
		data, err := json.Marshal(c)
		if err != nil {
			panic(err)
		}
		b.WriteString("data: " + string(data) + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func TestAgentStreamsFinalAnswer(t *testing.T) {
	var calls, starts, dones int
	var deltas []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")

		if calls == 1 {
			if !req.Stream {
				t.Error("agent should stream when a delta consumer is installed")
			}
			_, _ = w.Write([]byte(sseBody(
				map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": 0, "id": "call_1", "type": "function",
						"function": map[string]any{"name": "calc", "arguments": `{"expr":"17*23"}`},
					}},
				}, "finish_reason": "tool_calls"}}},
				map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}},
			)))
			return
		}

		found := false
		for _, m := range req.Messages {
			if m.Role == "tool" && m.ToolCallID == "call_1" && m.Content != nil && *m.Content == "391" {
				found = true
			}
		}
		if !found {
			t.Errorf("tool result was not sent back to the model: %+v", req.Messages)
		}
		_, _ = w.Write([]byte(sseBody(
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "17*23 = "}}}},
			map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "391"}, "finish_reason": "stop"}}},
			map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 8, "total_tokens": 128}},
		)))
	}))
	defer srv.Close()

	h, err := history.OpenHistory(t.TempDir(), "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	agent := NewAgent(newTestClient(srv), tools.NewToolRegistry(nil), h, compact.DefaultBudget(), 4, 0, 800)
	agent.SetStreamUI(
		func() { starts++ },
		func(s string) { deltas = append(deltas, s) },
		func() { dones++ },
	)

	got, err := agent.Chat(context.Background(), "сколько будет 17*23?")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got != "17*23 = 391" {
		t.Errorf("answer = %q", got)
	}
	if strings.Join(deltas, "") != "17*23 = 391" {
		t.Errorf("deltas = %q", deltas)
	}
	if starts != 2 || dones != 2 {
		t.Errorf("starts = %d, dones = %d, want 2 and 2", starts, dones)
	}
	if agent.LastAnswerTokens() != 8 {
		t.Errorf("last answer tokens = %d, want 8 (usage.completion_tokens of the final turn)", agent.LastAnswerTokens())
	}
	if len(h.Active()) != 4 {
		t.Fatalf("history = %d messages, want 4", len(h.Active()))
	}
}

func TestAgentNoCompactKeepsFullHistory(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "ок"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 5000, "completion_tokens": 2, "total_tokens": 5002},
		})
	}))
	defer srv.Close()

	h, err := history.OpenHistory(t.TempDir(), "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	// 16 messages of ~1800 estimated tokens: far beyond DefaultBudget.
	big := strings.Repeat("слово ", 800)
	for i := 0; i < 8; i++ {
		h.AppendUser(big)
		h.AppendAssistant(big, nil, "m")
	}
	before := len(h.Active())

	agent := NewAgent(newTestClient(srv), tools.NewToolRegistry(nil), h, compact.DefaultBudget(), 4, 0, 800)
	agent.SetNoCompact(true)
	if _, err := agent.Chat(context.Background(), "вопрос"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if calls != 1 {
		t.Errorf("API calls = %d, want 1 (no-compact must not call the summarizer)", calls)
	}
	if got := len(h.Active()); got != before+2 {
		t.Errorf("active = %d, want %d: no-compact must keep the whole history", got, before+2)
	}
}
