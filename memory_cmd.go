package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

// memoryCommand handles /memory and its subcommands. The layer listing is the
// main teaching view of day 11: what is stored where, how big each block is,
// and which knowledge entries the current question actually pulls in.
func (a *app) memoryCommand(arg string) error {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		a.printMemory()
		return nil
	}
	switch strings.ToLower(fields[0]) {
	case "clear":
		if len(fields) != 2 {
			return fmt.Errorf("нужен слой: /memory clear working|profile|decisions|knowledge")
		}
		layer, err := parseLayer(fields[1])
		if err != nil {
			return err
		}
		if err := a.mem.Clear(layer); err != nil {
			return err
		}
		if err := a.mem.Save(); err != nil {
			return err
		}
		fmt.Printf("слой %s очищен\n", layer)
		a.printMemory()
	case "add":
		if len(fields) < 3 {
			return fmt.Errorf("нужно: /memory add <слой> ключ=значение")
		}
		layer, err := parseLayer(fields[1])
		if err != nil {
			return err
		}
		rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
		rest = strings.TrimSpace(strings.TrimPrefix(rest, fields[1]))
		key, value, ok := strings.Cut(rest, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("нужно: /memory add %s ключ=значение", layer)
		}
		if err := a.mem.Add(layer, strings.TrimSpace(key), strings.TrimSpace(value)); err != nil {
			return err
		}
		if err := a.mem.Save(); err != nil {
			return err
		}
		fmt.Printf("%s += %s\n", layer, strings.TrimSpace(key))
		a.printMemory()
	default:
		return fmt.Errorf("неизвестная подкоманда %q; /memory | /memory clear <слой> | /memory add <слой> ключ=значение", fields[0])
	}
	return nil
}

// printMemory renders every layer with its size, its token cost and whether it
// goes into the prompt. Knowledge is shown with the entries the last question
// selected, because that layer is relevance-filtered.
func (a *app) printMemory() {
	q := a.lastUserText()
	toks := a.mem.LayerTokens(q)
	total := 0
	for _, n := range toks {
		total += n
	}
	fmt.Printf("память сессии %s: %d записей, ~%d ток. в промпте\n", a.hist.ID(), a.mem.Total(), total)
	for _, layer := range memory.Layers {
		entries := a.mem.Section(layer)
		note := "всегда в промпте"
		switch layer {
		case memory.LayerProfile:
			note = "всегда; общий профиль для всех сессий"
		case memory.LayerKnowledge:
			note = fmt.Sprintf("в промпте %d из %d — только релевантные вопросу", len(memory.Relevant(entries, q, memory.KnowledgeLimit)), len(entries))
		}
		fmt.Printf("  %-10s %2d/%-2d  ~%4d ток.  %s\n", layer, len(entries), memory.Caps[layer], toks[layer], note)
		if len(entries) == 0 {
			continue
		}
		chosen := map[string]bool{}
		if layer == memory.LayerKnowledge {
			for _, e := range memory.Relevant(entries, q, memory.KnowledgeLimit) {
				chosen[e.Key] = true
			}
		}
		for _, e := range entries {
			line := e.Key + ": " + e.Value
			if e.At != "" {
				line = e.At + " · " + line
			}
			if layer == memory.LayerKnowledge {
				mark := " × "
				if chosen[e.Key] {
					mark = " → "
				}
				fmt.Printf("%s%s\n", mark, line)
				continue
			}
			fmt.Printf("   - %s\n", line)
		}
	}
	if q != "" {
		fmt.Printf("вопрос для отбора знаний: %q\n", config.OneLine(q))
	}
}

// lastUserText returns the newest user message, which is the relevance query
// for the knowledge layer.
func (a *app) lastUserText() string {
	msgs := a.hist.Active()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

// parseLayer accepts both the bare layer names and the long.profile style used
// in the docs.
func parseLayer(s string) (memory.Layer, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "long.")
	s = strings.TrimPrefix(s, "short.")
	for _, l := range memory.Layers {
		if string(l) == s {
			return l, nil
		}
	}
	return "", fmt.Errorf("неизвестный слой %q; доступны: working, profile, decisions, knowledge", s)
}

// memoryReport is the offline view for a saved session: layers, token cost per
// layer and the journal of memory events.
func memoryReportCmd(dataDir, id string) error {
	h, err := history.OpenHistory(dataDir, id, "", "", "")
	if err != nil {
		return err
	}
	defer h.Close()
	mem, err := memory.Open(filepath.Join(dataDir, h.ID(), "memory"), filepath.Join(dataDir, "shared"))
	if err != nil {
		return err
	}
	q := ""
	msgs := h.Active()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			q = msgs[i].Content
			break
		}
	}
	fmt.Printf("сессия %s · модель %s · память: %d записей, обновлений в журнале %d\n",
		h.ID(), h.ChatModel(), mem.Total(), len(h.MemoryUpdates()))
	toks := mem.LayerTokens(q)
	total := 0
	for _, l := range memory.Layers {
		total += toks[l]
		fmt.Printf("  %-10s %2d/%-2d  ~%4d ток.\n", l, mem.Len(l), memory.Caps[l], toks[l])
	}
	fmt.Printf("  %-10s       ~%4d ток.\n", "итого", total)
	for _, l := range memory.Layers {
		if b := mem.Block(l, q); b != "" {
			fmt.Printf("\n%s\n", b)
		}
	}
	return nil
}

// importMemory migrates a day-10 facts store into the layered memory of another
// session. The split is a documented heuristic, not a model call: the day-10
// facts have no layer tags, so the key and value wording decides.
func importMemoryCmd(dataDir, srcID, dstID string) error {
	if dstID == "" {
		return fmt.Errorf("-import-memory переносит факты в сессию: укажите -session <id> (новая или существующая)")
	}
	src, err := history.OpenHistory(dataDir, srcID, "", "", "")
	if err != nil {
		return err
	}
	facts := src.Facts()
	src.Close()
	if len(facts) == 0 {
		return fmt.Errorf("в сессии %s нет снимков facts — переносить нечего", srcID)
	}
	mem, err := memory.Open(filepath.Join(dataDir, dstID, "memory"), filepath.Join(dataDir, "shared"))
	if err != nil {
		return err
	}
	counts := map[memory.Layer]int{}
	for _, f := range facts {
		layer := classifyFact(f.Key, f.Value)
		if err := mem.Add(layer, f.Key, f.Value); err != nil {
			return err
		}
		counts[layer]++
	}
	if err := mem.Save(); err != nil {
		return err
	}
	fmt.Printf("перенос фактов %s → сессия %s: %d записей\n", srcID, dstID, len(facts))
	for _, l := range memory.Layers {
		if counts[l] > 0 {
			fmt.Printf("  %-10s %d\n", l, counts[l])
		}
	}
	return nil
}

// classifyFact routes one day-10 fact by wording. Constraints, numbers and
// task details stay in working memory; accepted choices go to decisions;
// external contact/price facts go to knowledge; the user's own identity and
// preferences go to the shared profile.
func classifyFact(key, value string) memory.Layer {
	s := strings.ToLower(key + " " + value)
	switch {
	case containsAny(s, "решили", "решение", "выбрали", "утвердили", "вариант ", "договорились"):
		return memory.LayerDecisions
	case containsAny(s, "адрес", "контакт", "телефон", "почта", "e-mail", "сайт", "цена", "стоимость", "график работы"):
		return memory.LayerKnowledge
	case containsAny(s, "пользовател", "меня зовут", "моё имя", "мое имя", "предпоч", "стиль общения"):
		return memory.LayerProfile
	}
	return memory.LayerWorking
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// selectedKnowledge counts the knowledge entries the current question pulls
// into the prompt (same rule the store uses when rendering the block).
func selectedKnowledge(m *memory.Store, question string) int {
	return len(memory.Relevant(m.Section(memory.LayerKnowledge), question, memory.KnowledgeLimit))
}
