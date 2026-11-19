package memory

import (
	"sort"
	"strings"
	"unicode"

	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
)

// KnowledgeLimit is how many knowledge records may enter one prompt. Knowledge
// is the only layer filtered by relevance: it is the largest and the least
// certain, so only what the question is about is worth the tokens.
const KnowledgeLimit = 6

// Headers open each block in the system prompt. They are stable so the token
// report can attribute a block to its layer.
const (
	headerWorking   = "Рабочая память задачи:"
	headerProfile   = "Профиль пользователя (соблюдай в каждом ответе):"
	headerDecisions = "Принятые решения:"
	headerKnowledge = "Знания по теме вопроса:"
)

// Header returns the block header of a layer.
func Header(l Layer) string {
	switch l {
	case LayerWorking:
		return headerWorking
	case LayerProfile:
		return headerProfile
	case LayerDecisions:
		return headerDecisions
	case LayerKnowledge:
		return headerKnowledge
	}
	return ""
}

// Render prints entries as "- ключ: значение" lines. Decisions carry their date.
func Render(entries []Entry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("- ")
		if e.At != "" {
			b.WriteString(e.At)
			b.WriteString(" · ")
		}
		b.WriteString(e.Key)
		b.WriteString(": ")
		b.WriteString(e.Value)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// Block renders one layer with its header; an empty layer renders as "".
func (s *Store) Block(l Layer, question string) string {
	entries := s.Section(l)
	if l == LayerKnowledge {
		entries = Relevant(entries, question, KnowledgeLimit)
	}
	if len(entries) == 0 {
		return ""
	}
	return Header(l) + "\n" + Render(entries)
}

// promptOrder is the order of the memory blocks in the system prompt. The
// profile goes last on purpose: preferences are instructions, and the block
// closest to the dialogue wins when a fact and a preference disagree. Storage
// order (Layers) stays separate: the files and the /memory listing keep theirs.
var promptOrder = []Layer{LayerWorking, LayerDecisions, LayerKnowledge, LayerProfile}

// Prompt assembles the memory block for the system prompt: working, decisions
// and profile always, knowledge only when it matches the question. The profile
// closes the block, so the last thing the model reads before the dialogue is
// how this user wants to be answered.
func (s *Store) Prompt(question string) string {
	var parts []string
	for _, l := range promptOrder {
		if b := s.Block(l, question); b != "" {
			parts = append(parts, b)
		}
	}
	return strings.Join(parts, "\n\n")
}

// LayerTokens estimates the prompt cost of every layer. The knowledge figure
// is what the question would actually pull in, not the whole layer.
func (s *Store) LayerTokens(question string) map[Layer]int {
	out := make(map[Layer]int, len(Layers))
	for _, l := range promptOrder {
		out[l] = tokens.EstimateTokens(s.Block(l, question))
	}
	return out
}

// Relevant picks the knowledge records that share words with the question,
// best match first. It is deliberately simple: no embeddings, no model call —
// a deterministic rule that can be explained and tested.
func Relevant(entries []Entry, question string, limit int) []Entry {
	words := questionWords(question)
	if len(words) == 0 || limit <= 0 {
		return nil
	}
	type scored struct {
		e     Entry
		score int
		order int
	}
	var hits []scored
	for i, e := range entries {
		text := strings.ToLower(e.Key + " " + e.Value)
		score := 0
		for w := range words {
			if wordHit(text, w) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{e: e, score: score, order: i})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].order < hits[j].order
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// wordHit reports whether the question word occurs in the record text. Russian
// words change their endings («коворкинга» in the question, «коворкинг» in the
// record), so a shared prefix of four runes counts as a hit. A plain substring
// check is not enough: in the cascade run the record «коворкинг_Пресня» never
// reached the prompt because the question asked about «коворкинга».
func wordHit(text, w string) bool {
	if strings.Contains(text, w) {
		return true
	}
	for _, t := range splitWords(text) {
		if commonPrefix(w, t) >= prefixLen {
			return true
		}
	}
	return false
}

// prefixLen is the shortest shared prefix that still means "the same word" for
// Russian nouns and adjectives.
const prefixLen = 4

func splitWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func commonPrefix(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && ra[n] == rb[n] {
		n++
	}
	return n
}

// stopWords are ignored when matching knowledge to a question.
var stopWords = map[string]bool{
	"что": true, "какой": true, "какая": true, "какие": true, "каком": true,
	"какого": true, "кто": true, "где": true, "когда": true, "сколько": true,
	"чем": true, "это": true, "если": true, "чтобы": true, "также": true,
	"надо": true, "нужно": true, "можно": true, "есть": true, "было": true,
	"были": true, "будет": true, "наши": true, "наш": true, "ваши": true,
	"про": true, "для": true, "при": true, "над": true, "под": true,
	"или": true, "как": true, "все": true, "ещё": true, "уже": true,
	"этом": true, "этой": true, "этого": true, "меня": true, "нам": true,
	"тебе": true, "себе": true, "они": true, "она": true, "оно": true,
	"мы": true, "вы": true, "он": true, "то": true,
}

// questionWords returns the significant words of the question: lowercase,
// letters and digits only, at least four characters, no stop words.
func questionWords(q string) map[string]bool {
	out := make(map[string]bool)
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) < 4 || stopWords[w] {
			continue
		}
		out[w] = true
	}
	return out
}
