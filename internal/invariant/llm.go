package invariant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
)

// Verdict is the local model's answer about one assistant draft.
type Verdict struct {
	Violates bool   `json:"violates"`
	ID       string `json:"id"`
	Quote    string `json:"quote"`
	Reason   string `json:"reason"`
}

// Checker reviews free text against the invariants with a local model. It is
// the semantic layer: a regex cannot tell "возьмите MongoDB" from "MongoDB
// использовать нельзя", while a small local model can. The deterministic
// check_solution call and CheckText stay in place as the hard guarantee and
// the fallback for when the endpoint is down.
type Checker struct {
	client  *llm.ChatClient
	model   func() string
	timeout time.Duration
}

// NewChecker builds a text checker. model is a function because /summary can
// switch the model mid-session.
func NewChecker(client *llm.ChatClient, model func() string) *Checker {
	return &Checker{client: client, model: model, timeout: 90 * time.Second}
}

// Model returns the current checker model id (empty when unset).
func (c *Checker) Model() string {
	if c == nil || c.model == nil {
		return ""
	}
	return c.model()
}

// Check asks the model whether text proposes a solution that breaks any of the
// invariants. It returns an error when the endpoint is unavailable or the
// answer is not valid JSON; the caller then falls back to CheckText.
func (c *Checker) Check(ctx context.Context, text string, items []Invariant) (Verdict, error) {
	if c == nil || c.client == nil {
		return Verdict{}, fmt.Errorf("проверка инвариантов: клиент не задан")
	}
	model := c.Model()
	if strings.TrimSpace(model) == "" {
		return Verdict{}, fmt.Errorf("проверка инвариантов: модель не задана")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Verdict{}, nil
	}

	var b strings.Builder
	b.WriteString("Инварианты проекта:\n")
	for _, it := range items {
		fmt.Fprintf(&b, "- [%s] %s: %s", it.ID, it.Area, it.Text)
		if len(it.Rule.Deny) > 0 {
			fmt.Fprintf(&b, " (запрещено: %s)", strings.Join(it.Rule.Deny, ", "))
		}
		if len(it.Rule.Allow) > 0 {
			fmt.Fprintf(&b, " (разрешено только: %s)", strings.Join(it.Rule.Allow, ", "))
		}
		if it.Rule.Max != nil {
			fmt.Fprintf(&b, " (не более %s)", formatAmount(*it.Rule.Max))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nОтвет ассистента:\n<<<\n")
	b.WriteString(text)
	b.WriteString("\n>>>")

	messages := []llm.Message{
		{Role: "system", Content: checkerSystemPrompt},
		{Role: "user", Content: b.String()},
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.client.Chat(callCtx, model, messages, nil, 0)
	if err != nil {
		return Verdict{}, err
	}
	return parseVerdict(resp.Content)
}

const checkerSystemPrompt = `Ты — проверяющий инвариантов проекта.
Верни ТОЛЬКО JSON без пояснений и без markdown:
{"violates": true|false, "id": "...", "quote": "...", "reason": "..."}

Нарушение — если ответ предлагает, рекомендует, планирует или описывает как допустимое решение, которое нарушает инвариант.
Не нарушение: отказ, предупреждение, сравнение, объяснение, почему так нельзя, и предложение допустимой альтернативы.
Упоминание запрещённого названия или суммы в отрицании — не нарушение.
Примеры:
- «MongoDB использовать нельзя, берите PostgreSQL» → violates=false.
- «Предлагаю MongoDB» → violates=true.
- «350 000 ₽/мес не проходит: лимит 300 000» → violates=false, это отказ.
- «Стек за 350 000 ₽/мес: PostgreSQL, Kafka» → violates=true.
Если нарушений нет: {"violates": false, "id": "", "quote": "", "reason": ""}.
Если нарушение есть: id — id нарушенного инварианта, quote — предложение целиком, в котором есть нарушение, reason — одна фраза.`

// parseVerdict tolerates markdown fences and surrounding prose: small local
// models add them even when asked for bare JSON.
func parseVerdict(content string) (Verdict, error) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return Verdict{}, fmt.Errorf("проверка инвариантов: в ответе модели нет JSON: %q", oneLine(content))
	}
	var v Verdict
	if err := json.Unmarshal([]byte(content[start:end+1]), &v); err != nil {
		return Verdict{}, fmt.Errorf("проверка инвариантов: JSON модели: %w", err)
	}
	v.ID = strings.ToLower(strings.TrimSpace(v.ID))
	v.Quote = strings.TrimSpace(v.Quote)
	v.Reason = strings.TrimSpace(v.Reason)
	return v, nil
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 200 {
		r := []rune(s)
		return string(r[:200]) + "…"
	}
	return s
}
