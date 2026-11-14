package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Strategy selects how the prompt remembers the dialogue beyond the raw
// window. The strategies are exclusive: only one memory block is assembled
// into the system prompt.
type Strategy string

const (
	// StrategySummary is the day-9 behavior: evicted turns are folded into a
	// rolling natural-language summary (a model call per compaction).
	StrategySummary Strategy = "summary"
	// StrategyWindow keeps only the last N messages; everything older is
	// evicted to the archive with nothing replacing it. No model calls.
	StrategyWindow Strategy = "window"
	// StrategyFacts keeps a key-value store of the important details (updated
	// by a cheap model call after every user message) plus the last N messages.
	StrategyFacts Strategy = "facts"
)

// ParseStrategy validates a strategy name from the flag or /strategy command.
func ParseStrategy(s string) (Strategy, error) {
	switch Strategy(s) {
	case StrategySummary, StrategyWindow, StrategyFacts:
		return Strategy(s), nil
	}
	return "", fmt.Errorf("неизвестная стратегия %q; доступны: summary, window, facts", s)
}

// needsWindow reports whether the strategy requires the -window N parameter.
func (s Strategy) needsWindow() bool {
	return s == StrategyWindow || s == StrategyFacts
}

// renderFacts prints the store as "ключ: значение" lines for the prompt and
// the /facts command.
func renderFacts(facts []Fact) string {
	var b strings.Builder
	for _, f := range facts {
		fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Value)
	}
	return strings.TrimRight(b.String(), "\n")
}

// factsSystemPrompt instructs the extractor model. The model returns the FULL
// updated store every time — the caller replaces its copy wholesale, which is
// what the facts event in the log stores too.
const factsSystemPrompt = "Ты ведёшь память диалога в виде списка фактов «ключ: значение» " +
	"(цель, ограничения, решения, договорённости, имена, числа, сроки). " +
	"Тебе даны текущие факты и новое сообщение пользователя. Верни обновлённый ПОЛНЫЙ список " +
	"фактов одним JSON-объектом {\"ключ\": \"значение\"}: ключ короткий (1-4 слова), значение конкретное. " +
	"Исправляй устаревшие значения и добавляй новые, ничего важного не теряй. " +
	"Ответ — только JSON, без пояснений и без markdown."

// factsPrompt builds the extractor input: the current store plus the message
// being folded in.
func factsPrompt(prev []Fact, userText string) string {
	var b strings.Builder
	b.WriteString("Текущие факты:\n")
	if len(prev) == 0 {
		b.WriteString("(пока пусто)\n")
	} else {
		b.WriteString(renderFacts(prev))
		b.WriteString("\n")
	}
	b.WriteString("\nНовое сообщение пользователя:\n")
	b.WriteString(truncate(userText, 2000))
	return b.String()
}

// parseFactsJSON reads the extractor's answer: a flat JSON object in key
// insertion order. Code fences and stray prose around the object are tolerated.
func parseFactsJSON(s string) ([]Fact, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}

	dec := json.NewDecoder(strings.NewReader(s))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("факты: ответ не JSON: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("факты: ожидался JSON-объект")
	}

	var facts []Fact
	seen := make(map[string]int)
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("факты: %w", err)
		}
		key, _ := kt.(string)
		var val any
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("факты: %w", err)
		}
		key = strings.TrimSpace(key)
		value := strings.TrimSpace(truncate(fmt.Sprintf("%v", val), 500))
		if key == "" || value == "" {
			continue
		}
		if i, ok := seen[key]; ok {
			facts[i].Value = value // duplicate key: the later value wins
			continue
		}
		seen[key] = len(facts)
		facts = append(facts, Fact{Key: key, Value: value})
	}
	return facts, nil
}

// extractFacts is the glue used by the Compactor: one extractor request with
// the summary-model-first / chat-model-fallback pattern of summarize. The
// raw model answer is returned; parsing happens at the call site.
func (c *Compactor) extractFacts(ctx context.Context, prev []Fact, userText string) (string, error) {
	prompt := factsPrompt(prev, userText)
	primary := c.hist.SummaryModel()

	out, err := c.factsRequest(ctx, primary, prompt)
	if err != nil {
		backup := c.hist.ChatModel()
		if backup == "" || backup == primary || ctx.Err() != nil {
			return "", err
		}
		if c.trace != nil {
			c.trace(fmt.Sprintf("факты: %s не справился (%s) — пробую %s", primary, oneLine(err.Error()), backup))
		}
		out, err2 := c.factsRequest(ctx, backup, prompt)
		if err2 != nil {
			return "", fmt.Errorf("%s: %v; запасная %s: %v", primary, err, backup, err2)
		}
		return out, nil
	}
	return out, nil
}

// factsRequest runs one extractor request under the same per-attempt timeout
// as the summarizer.
func (c *Compactor) factsRequest(ctx context.Context, model, prompt string) (string, error) {
	actx, cancel := context.WithTimeout(ctx, summaryAttemptTimeout)
	defer cancel()
	resp, err := c.client.Chat(actx, model, []Message{
		{Role: "system", Content: factsSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil, 0)
	if err != nil {
		if actx.Err() != nil && ctx.Err() == nil {
			return "", fmt.Errorf("%s: таймаут %s", model, summaryAttemptTimeout)
		}
		return "", err
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return "", fmt.Errorf("%s: пустой ответ", model)
	}
	return out, nil
}
