package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnforceWindowKeepsToolGroups(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir, "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}

	// 4 turns; the second one carries a tool group that must stay intact.
	seedTurns(t, h, 1)
	h.AppendUser("вопрос с инструментом")
	h.AppendAssistant("", []ToolCall{{ID: "c1", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "calc", Arguments: "1+1"}}}, "m")
	h.AppendTool("c1", "calc", "2")
	seedTurns(t, h, 2) // всего 8 сообщений

	comp := NewCompactor(h, nil, DefaultBudget(), NewCalibrator(), 800)
	ok, err := comp.EnforceWindow(4)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ожидали вытеснение: сообщений больше окна")
	}

	active := h.Active()
	if len(active) > 4 {
		t.Errorf("active = %d, хотим <= 4", len(active))
	}
	if active[0].Role != "user" {
		t.Errorf("окно начинается с %q, хотим user (не рвать пары assistant+tool)", active[0].Role)
	}
	assertNoOrphanTools(t, active)
	if h.HasSummary() {
		t.Error("вытеснение окна не должно создавать конспект")
	}
	archived := len(h.Archived())

	// Replay from disk must reproduce the same split.
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	re, err := OpenHistory(dir, "s", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if len(re.Active()) != len(active) {
		t.Errorf("после replay active = %d, было %d", len(re.Active()), len(active))
	}
	if len(re.Archived()) != archived {
		t.Errorf("после replay archived = %d, было %d", len(re.Archived()), archived)
	}

	// Окно больше, чем сообщений: вытеснять нечего.
	if ok, err := comp.EnforceWindow(100); err != nil || ok {
		t.Errorf("EnforceWindow(100) = %v, %v; хотели false", ok, err)
	}
}

func TestPromptWindowCut(t *testing.T) {
	var msgs []*Message
	for i := 0; i < 3; i++ {
		msgs = append(msgs,
			&Message{Role: "user", Seq: int64(i*2 + 1)},
			&Message{Role: "assistant", Seq: int64(i*2 + 2)})
	}
	// Окно 4: срез по user-границе — остаются ходы 2 и 3.
	if cut := windowCut(msgs, 4); cut != 2 {
		t.Errorf("windowCut(4) = %d, хотим 2", cut)
	}
	if cut := windowCut(msgs, 6); cut != 0 {
		t.Errorf("windowCut(6) = %d, хотим 0 (нечего резать)", cut)
	}
	if cut := windowCut(msgs, 1); cut != 4 {
		t.Errorf("windowCut(1) = %d, хотим 4 (последний целый ход)", cut)
	}
}

func TestForkSession(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir, "main", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	seedTurns(t, h, 3)
	lastSeq := h.LastSeq()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	id, err := ForkSession(dir, "main", "b1")
	if err != nil || id != "b1" {
		t.Fatalf("ForkSession = %q, %v", id, err)
	}
	if _, err := ForkSession(dir, "main", "b1"); err == nil {
		t.Error("ожидали ошибку: сессия с таким именем уже есть")
	}
	if _, err := ForkSession(dir, "main", "../escape"); err == nil {
		t.Error("ожидали ошибку: недопустимое имя ветки")
	}

	fh, err := OpenHistory(dir, "b1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if fh.ForkOf() != "main" || fh.ForkSeq() != lastSeq {
		t.Errorf("fork = %q@%d, хотим main@%d", fh.ForkOf(), fh.ForkSeq(), lastSeq)
	}
	if len(fh.Active()) != 6 || len(fh.Archived()) != 0 {
		t.Errorf("ветка: active %d archived %d, хотим 6/0", len(fh.Active()), len(fh.Archived()))
	}

	// The branch grows independently; the parent stays at the checkpoint.
	fh.AppendUser("вопрос в ветке")
	fh.AppendAssistant("ответ в ветке", nil, "m")
	fh.Close()

	ph, err := OpenHistory(dir, "main", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	if ph.LastSeq() != lastSeq {
		t.Errorf("родитель вырос до seq %d, хотим %d", ph.LastSeq(), lastSeq)
	}

	kids, err := ListForks(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 1 || kids[0] != "b1" {
		t.Errorf("ListForks = %v, хотим [b1]", kids)
	}
}

func TestFactsReplayFromLog(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir, "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	facts := []Fact{{Key: "срок", Value: "15 декабря"}, {Key: "бюджет", Value: "480 000 ₽"}}
	if err := h.SetFacts(facts); err != nil {
		t.Fatal(err)
	}
	// Обновление: снапшот заменяет список целиком.
	if err := h.SetFacts([]Fact{{Key: "срок", Value: "20 ноября"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	re, err := OpenHistory(dir, "s", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	got := re.Facts()
	if len(got) != 1 || got[0].Key != "срок" || got[0].Value != "20 ноября" {
		t.Errorf("после replay facts = %+v", got)
	}
}

// TestAgentFactsFlow runs one full turn in the facts strategy against a stub
// server: request 1 is the extractor, request 2 is the chat answer whose
// system prompt must already carry the fresh facts.
func TestAgentFactsFlow(t *testing.T) {
	var calls int
	var chatSystem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req chatRequest
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
	h, err := OpenHistory(dir, "s", "chat-model", "sum-model", "sys-текст")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	client := &ChatClient{baseURL: srv.URL, apiKey: "test", http: srv.Client(), stats: ClientStats{ByModel: map[string]int{}}}
	a := NewAgent(client, NewToolRegistry(nil), h, DefaultBudget(), 4, 0.2, 800)
	a.SetStrategy(StrategyFacts)
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
	h, err := OpenHistory(t.TempDir(), "s", "chat-model", "sum-model", "sys-текст")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	seedTurns(t, h, 3) // 6 сообщений
	h.SetFacts([]Fact{{Key: "срок", Value: "15 декабря"}})
	client := &ChatClient{baseURL: "http://unused", apiKey: "test", http: http.DefaultClient, stats: ClientStats{ByModel: map[string]int{}}}
	a := NewAgent(client, NewToolRegistry(nil), h, DefaultBudget(), 4, 0.2, 800)

	a.SetStrategy(StrategyWindow)
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
	a.SetStrategy(StrategyFacts)
	a.SetWindow(3)
	msgs = a.buildMessages()
	// 6 сообщений, окно 3: срез сдвигается к user-границе, остаётся 2.
	if len(msgs) != 3 {
		t.Fatalf("facts: сообщений %d, хотим system+2 (срез по user-границе)", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Факты диалога") || !strings.Contains(msgs[0].Content, "- срок: 15 декабря") {
		t.Errorf("facts: блок фактов не попал в system: %q", msgs[0].Content)
	}
	if msgs[1].Content != "вопрос номер 2" {
		t.Errorf("facts: хвост = %q, хотим последний целый ход", msgs[1].Content)
	}
}
