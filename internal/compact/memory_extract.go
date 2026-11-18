package compact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

// memorySystemPrompt teaches the extractor the routing rules: which fact goes
// to which layer and what is not stored at all. The layer caps are enforced in
// code (memory.Caps), never by the model.
const memorySystemPrompt = "Ты раскладываешь сведения из диалога по слоям памяти агента. " +
	"Слои и правила: " +
	"working — данные текущей задачи: цель, ограничения, требования, числа, сроки, бюджет, открытые вопросы, следующий шаг. " +
	"profile — кто пользователь: имя, роль, город, устойчивые предпочтения и стиль работы. " +
	"decisions — то, что уже решили, выбрали или утвердили (суть решения, без хода обсуждения). " +
	"knowledge — внешние проверяемые сведения: адреса, контакты, цены, характеристики площадок и услуг. " +
	"НЕ сохраняй никуда: вопросы пользователя, эмоции и сиюминутные состояния, стиль общения, ход обсуждения, " +
	"то, что уже неактуально. " +
	"Если сведение уже есть в памяти и не изменилось — не возвращай его. " +
	"Если новое сообщение дополняет существующую запись (тот же человек, объект или тема) — " +
	"верни её под тем же ключом с объединённым значением, а не создавай вторую запись с другим ключом. " +
	"Смена статуса — это изменение: открытый вопрос, который стал решением, верни в decisions, даже если формулировка совпадает. " +
	"Переноси сведение между слоями явно: добавь его в новый слой и удали из старого. " +
	"Удаление — это null вместо значения: {\"working\": {\"ключ\": null}}. Удаляй только то, что переехало или стало неверным; " +
	"сведения, которые всё ещё верны, не удаляй. " +
	"Ключ — короткий (1-4 слова), значение — конкретное, до 200 символов. " +
	"Ответ — только JSON без пояснений и markdown."

// memoryPrompt builds the extractor input: the current layers plus the new
// user message. Only a diff is expected back, so the store is shown for
// deduplication, not for rewriting.
func memoryPrompt(store *memory.Store, userText string) string {
	var b strings.Builder
	b.WriteString("Текущая память:\n")
	for _, l := range memory.Layers {
		b.WriteString(string(l))
		b.WriteString(":\n")
		entries := store.Section(l)
		if len(entries) == 0 {
			b.WriteString("(пусто)\n")
			continue
		}
		b.WriteString(memory.Render(entries))
		b.WriteString("\n")
	}
	b.WriteString("\nНовое сообщение пользователя:\n")
	b.WriteString(config.Truncate(userText, 2000))
	b.WriteString("\n\nВерни только новые или изменённые записи, разложенные по слоям, одним JSON-объектом: " +
		"{\"working\": {\"ключ\": \"значение\"}, \"profile\": {...}, \"decisions\": {...}, \"knowledge\": {...}}. " +
		"Чтобы удалить запись, верни null вместо значения: {\"working\": {\"ключ\": null}}. " +
		"Если сохранять нечего — верни {}.")
	return b.String()
}

// ParseMemoryJSON reads the extractor's answer into a layered update. Objects
// are decoded in insertion order, so new records keep the order the model
// produced. A flat object without layer keys is accepted as working memory.
func ParseMemoryJSON(s string) (memory.Update, error) {
	var upd memory.Update
	s = strings.TrimSpace(s)
	if s == "" {
		return upd, fmt.Errorf("память: пустой ответ")
	}
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
		return upd, fmt.Errorf("память: ответ не JSON: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return upd, fmt.Errorf("память: ожидался JSON-объект")
	}

	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return upd, fmt.Errorf("память: %w", err)
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return upd, fmt.Errorf("память: %w", err)
		}
		layer := memory.Layer(strings.ToLower(strings.TrimSpace(key)))
		if !knownLayer(layer) {
			// Flat answer: the whole object is one working-memory record.
			if string(bytes.TrimSpace(raw)) == "null" {
				addRemoved(&upd, memory.LayerWorking, []string{key})
				continue
			}
			val := strings.TrimSpace(strings.Trim(string(raw), `"`))
			if e := newEntry(key, val); e.Key != "" {
				upd.Working = append(upd.Working, e)
			}
			continue
		}
		entries, removed, perr := orderedEntries(raw)
		if perr != nil {
			return upd, fmt.Errorf("память: слой %s: %w", layer, perr)
		}
		addRemoved(&upd, layer, removed)
		switch layer {
		case memory.LayerWorking:
			upd.Working = append(upd.Working, entries...)
		case memory.LayerProfile:
			upd.Profile = append(upd.Profile, entries...)
		case memory.LayerDecisions:
			upd.Decisions = append(upd.Decisions, entries...)
		case memory.LayerKnowledge:
			upd.Knowledge = append(upd.Knowledge, entries...)
		}
	}
	return upd, nil
}

// knownLayer reports whether s names a layer this package stores.
func knownLayer(l memory.Layer) bool {
	for _, x := range memory.Layers {
		if x == l {
			return true
		}
	}
	return false
}

// orderedEntries decodes one JSON object into entries, keeping key order. A
// null value is a deletion: the key goes to removed instead of out.
func orderedEntries(raw json.RawMessage) ([]memory.Entry, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, nil, fmt.Errorf("ожидался объект «ключ: значение»")
	}
	var out []memory.Entry
	var removed []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, _ := kt.(string)
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if v == nil {
			if k := strings.TrimSpace(key); k != "" {
				removed = append(removed, k)
			}
			continue
		}
		if e := newEntry(key, fmt.Sprintf("%v", v)); e.Key != "" {
			out = append(out, e)
		}
	}
	return out, removed, nil
}

// addRemoved records the keys an update deletes from a layer.
func addRemoved(upd *memory.Update, l memory.Layer, keys []string) {
	if len(keys) == 0 {
		return
	}
	if upd.Remove == nil {
		upd.Remove = make(map[memory.Layer][]string)
	}
	upd.Remove[l] = append(upd.Remove[l], keys...)
}

// newEntry normalizes one record and drops empty pairs.
func newEntry(key, value string) memory.Entry {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" || value == "<nil>" {
		return memory.Entry{}
	}
	return memory.Entry{
		Key:   config.Truncate(key, 60),
		Value: config.Truncate(value, 200),
	}
}

// ExtractMemory is the glue used by the agent: one extractor request with the
// summary-model-first / chat-model-fallback pattern of the summarizer.
func (c *Compactor) ExtractMemory(ctx context.Context, store *memory.Store, userText string) (string, error) {
	prompt := memoryPrompt(store, userText)
	primary := c.hist.SummaryModel()

	out, err := c.jsonRequest(ctx, primary, memorySystemPrompt, prompt)
	if err != nil {
		backup := c.hist.ChatModel()
		if backup == "" || backup == primary || ctx.Err() != nil {
			return "", err
		}
		if c.trace != nil {
			c.trace(fmt.Sprintf("память: %s не справился (%s) — пробую %s", primary, config.OneLine(err.Error()), backup))
		}
		out2, err2 := c.jsonRequest(ctx, backup, memorySystemPrompt, prompt)
		if err2 != nil {
			return "", fmt.Errorf("%s: %v; запасная %s: %v", primary, err, backup, err2)
		}
		return out2, nil
	}
	return out, nil
}

// jsonRequest runs one extractor request under the summarizer's per-attempt
// timeout and returns the raw answer.
func (c *Compactor) jsonRequest(ctx context.Context, model, system, prompt string) (string, error) {
	return jsonChat(ctx, c.client, model, system, prompt, summaryAttemptTimeout)
}

// jsonChat is the transport of both extractor paths: one system+user request
// without tools, bounded by its own timeout, answer trimmed and non-empty.
func jsonChat(ctx context.Context, client *llm.ChatClient, model, system, prompt string, timeout time.Duration) (string, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := client.Chat(actx, model, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: prompt},
	}, nil, 0)
	if err != nil {
		if actx.Err() != nil && ctx.Err() == nil {
			return "", fmt.Errorf("%s: таймаут %s", model, timeout)
		}
		return "", err
	}
	out := strings.TrimSpace(resp.Content)
	if out == "" {
		return "", fmt.Errorf("%s: пустой ответ", model)
	}
	return out, nil
}

// importAttemptTimeout bounds the one-shot import of a day-10 facts store: the
// list is bigger than one dialogue turn, so it gets more room than the
// per-turn extractor.
const importAttemptTimeout = 3 * time.Minute

// importPrompt renders the whole day-10 facts store as extractor input. Unlike
// memoryPrompt it shows no current layers — an import starts from a flat list
// with no tags at all — and asks explicitly not to drop anything.
func importPrompt(facts []history.Fact) string {
	var b strings.Builder
	b.WriteString("Ниже — все факты, накопленные за прошлый диалог, в виде «ключ: значение».\n")
	b.WriteString("Разложи их по слоям памяти: каждый факт должен попасть ровно в один слой, ничего не пропускай.\n")
	b.WriteString("Факты, которые не подходят ни одному слою, тоже не выбрасывай — положи в working.\n\n")
	b.WriteString("Факты:\n")
	for _, f := range facts {
		b.WriteString("- ")
		b.WriteString(f.Key)
		b.WriteString(": ")
		b.WriteString(config.Truncate(f.Value, 400))
		b.WriteByte('\n')
	}
	// The caps are enforced in code anyway; telling the model about them lets it
	// choose what matters instead of losing whatever happens to be oldest.
	fmt.Fprintf(&b, "\nЛимиты слоёв: working — %d записей, profile — %d, decisions — %d, knowledge — %d.\n",
		memory.Caps[memory.LayerWorking], memory.Caps[memory.LayerProfile],
		memory.Caps[memory.LayerDecisions], memory.Caps[memory.LayerKnowledge])
	b.WriteString("Если сведений больше лимита, оставь самые важные для дальнейшей работы.\n")
	b.WriteString("\nВерни одним JSON-объектом только записи, разложенные по слоям: " +
		"{\"working\": {\"ключ\": \"значение\"}, \"profile\": {...}, \"decisions\": {...}, \"knowledge\": {...}}.")
	return b.String()
}

// ImportFacts routes a flat day-10 facts store into the memory layers with one
// model call: the same system prompt and JSON contract as the per-turn
// extractor, only the input is the whole store instead of one message. The
// day-10 facts carry no layer tags, so the layer descriptions — not a keyword
// list — decide where each one goes.
func ImportFacts(ctx context.Context, client *llm.ChatClient, model string, facts []history.Fact) (memory.Update, error) {
	var upd memory.Update
	if len(facts) == 0 {
		return upd, fmt.Errorf("импорт: нечего раскладывать")
	}
	out, err := jsonChat(ctx, client, model, memorySystemPrompt, importPrompt(facts), importAttemptTimeout)
	if err != nil {
		return upd, err
	}
	return ParseMemoryJSON(out)
}
