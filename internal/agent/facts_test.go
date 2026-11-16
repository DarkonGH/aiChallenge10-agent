package agent

import (
	"context"
	"encoding/json"
	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAgentFactsFlow runs one full turn in the facts strategy against a stub
// server: request 1 is the extractor, request 2 is the chat answer whose
// system prompt must already carry the fresh facts.
func TestAgentFactsFlow(t *testing.T) {
	var calls int
	var chatSystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if calls == 1 {
			// The extractor call: same system instruction, no tools.
			if len(req.Messages) == 0 || !strings.Contains(*req.Messages[0].Content, "фактов") {
				t.Errorf("extractor system prompt = %v", req.Messages)
			}
			if len(req.Tools) != 0 {
				t.Error("extractor должен идти без схем инструментов")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{"role": "assistant", "content": `{"срок": "15 декабря"}`},
				}},
				"usage": map[string]any{"prompt_tokens": 30, "completion_tokens": 8, "total_tokens": 38},
			})
			return
		}
		// The chat request: system must carry the facts block.
		if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
			chatSystem = *req.Messages[0].Content
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "Ответ"},
			}},
			"usage": map[string]any{"prompt_tokens": 80, "completion_tokens": 5, "total_tokens": 85},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	h, err := history.OpenHistory(dir, "s", "chat-model", "sum-model", "sys-текст")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	client := llm.NewChatClientAt(srv.URL, "test")
	a := NewAgent(client, tools.NewToolRegistry(nil), h, compact.DefaultBudget(), 4, 0.2, 800)
	a.SetStrategy(compact.StrategyFacts)
	a.SetWindow(6)

	ans, err := a.Chat(testCtx(), "Планируем переезд: срок — 15 декабря")
	if err != nil {
		t.Fatal(err)
	}
	if ans != "Ответ" {
		t.Errorf("answer = %q", ans)
	}
	if calls != 2 {
		t.Fatalf("вызовов API = %d, хотим 2 (экстрактор + чат)", calls)
	}
	facts := h.Facts()
	if len(facts) != 1 || facts[0].Key != "срок" || facts[0].Value != "15 декабря" {
		t.Errorf("facts = %+v", facts)
	}
	if !strings.Contains(chatSystem, "Факты диалога") || !strings.Contains(chatSystem, "- срок: 15 декабря") {
		t.Errorf("system промпт без свежих фактов: %q", chatSystem)
	}
}

func testCtx() context.Context { return context.Background() }

// TestBuildMessagesWindow keeps the memory blocks out of the window strategy's
// system prompt and trims the history to the window.
func TestBuildMessagesWindow(t *testing.T) {
	h, err := history.OpenHistory(t.TempDir(), "s", "chat-model", "sum-model", "sys-текст")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	seedTurns(t, h, 3) // 6 сообщений
	h.SetFacts([]history.Fact{{Key: "срок", Value: "15 декабря"}})
	client := llm.NewChatClientAt("http://unused", "test")
	a := NewAgent(client, tools.NewToolRegistry(nil), h, compact.DefaultBudget(), 4, 0.2, 800)

	a.SetStrategy(compact.StrategyWindow)
	a.SetWindow(2)
	msgs := a.buildMessages()
	if len(msgs) != 3 {
		t.Fatalf("window: сообщений %d, хотим system+2", len(msgs))
	}
	if strings.Contains(msgs[0].Content, "Конспект") || strings.Contains(msgs[0].Content, "Факты") {
		t.Errorf("window: лишний блок памяти в system: %q", msgs[0].Content)
	}
	if msgs[1].Content != "вопрос номер 2" {
		t.Errorf("window: хвост = %q, хотим последние 2 сообщения", msgs[1].Content)
	}

	// The facts strategy, on the other hand, includes the block.
	a.SetStrategy(compact.StrategyFacts)
	a.SetWindow(3)
	msgs = a.buildMessages()
	// 6 сообщений, окно 3: ровно три последних — ответ хода 1, вопрос и ответ хода 2.
	if len(msgs) != 4 {
		t.Fatalf("facts: сообщений %d, хотим system+3", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Факты диалога") || !strings.Contains(msgs[0].Content, "- срок: 15 декабря") {
		t.Errorf("facts: блок фактов не попал в system: %q", msgs[0].Content)
	}
	if msgs[1].Content != "ответ номер 1" {
		t.Errorf("facts: хвост = %q, хотим ровно 3 последних сообщения", msgs[1].Content)
	}
}
