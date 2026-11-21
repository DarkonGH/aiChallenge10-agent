package agent

import (
	"context"
	"encoding/json"
	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/invariant"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentToolLoop(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}

		if calls == 1 {
			if len(req.Tools) == 0 {
				t.Error("tools missing from the first request")
			}
			if req.ToolChoice != "auto" {
				t.Errorf("tool_choice = %q, want auto", req.ToolChoice)
			}
			if len(req.Messages) == 0 || req.Messages[0].Role != "system" {
				t.Errorf("first message is not system: %+v", req.Messages)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []any{map[string]any{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      "calc",
								"arguments": `{"expr":"17*23"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110},
			})
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "17*23 = 391"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 8, "total_tokens": 128},
		})
	}))
	defer srv.Close()

	client := llm.NewChatClientAt(srv.URL, "test")
	h, err := history.OpenHistory(t.TempDir(), "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	agent := NewAgent(client, tools.NewToolRegistry(nil), h, compact.DefaultBudget(), 4, 0, 800)
	got, err := agent.Chat(context.Background(), "сколько будет 17*23?")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got != "17*23 = 391" {
		t.Errorf("answer = %q", got)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}

	// user, assistant(tool_calls), tool, assistant(final)
	if len(h.Active()) != 4 {
		t.Fatalf("history = %d messages, want 4", len(h.Active()))
	}
	if h.Active()[1].Role != "assistant" || len(h.Active()[1].ToolCalls) != 1 {
		t.Errorf("assistant tool call not stored: %+v", h.Active()[1])
	}
	if h.Active()[2].Role != "tool" || h.Active()[2].Content != "391" {
		t.Errorf("tool result not stored: %+v", h.Active()[2])
	}
}

func TestAgentNoTools(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Tools) == 0 {
			t.Error("tools should always be advertised")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "Рекурсия — это вызов функцией самой себя."},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 12, "total_tokens": 52},
		})
	}))
	defer srv.Close()

	client := llm.NewChatClientAt(srv.URL, "test")
	h, err := history.OpenHistory(t.TempDir(), "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	agent := NewAgent(client, tools.NewToolRegistry(nil), h, compact.DefaultBudget(), 4, 0, 800)
	got, err := agent.Chat(context.Background(), "что такое рекурсия?")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(got, "Рекурсия") {
		t.Errorf("answer = %q", got)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestAgentToolErrorIsReturnedToModel(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{
						"role": "assistant", "content": nil,
						"tool_calls": []any{map[string]any{
							"id": "call_1", "type": "function",
							"function": map[string]any{"name": "calc", "arguments": `{"expr":"1/0"}`},
						}},
					},
				}},
			})
			return
		}
		found := false
		for _, m := range req.Messages {
			if m.Role == "tool" && m.Content != nil && strings.Contains(*m.Content, "деление на ноль") {
				found = true
			}
		}
		if !found {
			t.Errorf("tool error not sent back: %+v", req.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "На ноль делить нельзя."},
			}},
		})
	}))
	defer srv.Close()

	client := llm.NewChatClientAt(srv.URL, "test")
	h, err := history.OpenHistory(t.TempDir(), "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	agent := NewAgent(client, tools.NewToolRegistry(nil), h, compact.DefaultBudget(), 4, 0, 800)
	if _, err := agent.Chat(context.Background(), "сколько будет 1/0?"); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

// TestAgentInvariantGate covers the day-14 gate end to end: the model must call
// check_solution, a draft that names a forbidden database is never returned,
// and a refusal that mentions it in a negation is released.
func TestAgentInvariantGate(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		// The lazy schema must be visible as soon as the set is active.
		if calls == 1 {
			found := false
			for _, tl := range req.Tools {
				if tl.Function.Name == "check_solution" {
					found = true
				}
			}
			if !found {
				t.Error("check_solution schema missing from the request")
			}
		}
		switch calls {
		case 1:
			// Structural check first: PostgreSQL is allowed and declared.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []any{map[string]any{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      "check_solution",
								"arguments": `{"summary":"стек","choices":[{"area":"db","value":"PostgreSQL 15"}],"invariants":["db"]}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110},
			})
		case 2:
			// A violating draft: the keyword layer must reject it after the
			// successful check_solution, and the draft stays in the log.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": "Для хранения возьмите MongoDB."},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 8, "total_tokens": 128},
			})
		default:
			// A refusal is not a violation: "не" makes the sentence a rejection.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": "MongoDB использовать нельзя: инвариант db требует PostgreSQL."},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 140, "completion_tokens": 12, "total_tokens": 152},
			})
		}
	}))
	defer srv.Close()

	set, err := invariant.Load(filepath.Join(t.TempDir(), "invariants.json"))
	if err != nil {
		t.Fatal(err)
	}
	rule, err := invariant.ParseRule("deny=mysql,mongodb")
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Add("db", "database", "Только PostgreSQL", rule, "user"); err != nil {
		t.Fatal(err)
	}

	client := llm.NewChatClientAt(srv.URL, "test")
	h, err := history.OpenHistory(t.TempDir(), "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	reg := tools.NewToolRegistry(nil)
	reg.SetInvariantProvider(func() *invariant.Set { return set })
	ag := NewAgent(client, reg, h, compact.DefaultBudget(), 6, 0, 800)
	ag.SetTrace(func(s string) { t.Log(s) })
	ag.SetInvariants(func() *invariant.Set { return set })
	reg.SetInvariantCheckHook(ag.MarkChecked)

	got, err := ag.Chat(context.Background(), "На чём хранить данные?")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(got, "нельзя") {
		t.Errorf("answer = %q, want the refusal draft", got)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (check_solution, violating draft, refusal)", calls)
	}

	// The rejected draft and the notice must both be in the journal: the audit
	// trail is the point of keeping drafts that never reached stdout.
	msgs := h.Active()
	var notices int
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, history.NoticePrefix) {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("notices = %d, want 1; messages = %+v", notices, msgs)
	}
}

// TestAgentInvariantGateRequiresCheck shows the other half of the gate: even a
// harmless answer is not released until check_solution has run this turn.
func TestAgentInvariantGateRequiresCheck(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch calls {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": "PostgreSQL 15 — хороший выбор."},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 80, "completion_tokens": 8, "total_tokens": 88},
			})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []any{map[string]any{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      "check_solution",
								"arguments": `{"summary":"стек","choices":[{"area":"db","value":"PostgreSQL 15"}],"invariants":["db"]}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": "PostgreSQL 15 — хороший выбор."},
					"finish_reason": "stop",
				}},
				"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 8, "total_tokens": 128},
			})
		}
	}))
	defer srv.Close()

	set, err := invariant.Load(filepath.Join(t.TempDir(), "invariants.json"))
	if err != nil {
		t.Fatal(err)
	}
	rule, _ := invariant.ParseRule("deny=mysql,mongodb")
	if err := set.Add("db", "database", "Только PostgreSQL", rule, "user"); err != nil {
		t.Fatal(err)
	}

	client := llm.NewChatClientAt(srv.URL, "test")
	h, err := history.OpenHistory(t.TempDir(), "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	reg := tools.NewToolRegistry(nil)
	reg.SetInvariantProvider(func() *invariant.Set { return set })
	ag := NewAgent(client, reg, h, compact.DefaultBudget(), 6, 0, 800)
	ag.SetInvariants(func() *invariant.Set { return set })
	reg.SetInvariantCheckHook(ag.MarkChecked)

	got, err := ag.Chat(context.Background(), "Что выберем для хранения?")
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(got, "PostgreSQL") {
		t.Errorf("answer = %q", got)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (draft, check_solution, answer)", calls)
	}
}
