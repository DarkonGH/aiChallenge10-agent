package compact

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

// entityURL matches a link in the user's message. A link is the clearest sign
// that the turn is about a new external object: a listing, a page, a file.
// When one is present, numbers in the extractor's answer must come from this
// message (or from the same key's previous value), not from an older object.
var entityURL = regexp.MustCompile(`(?i)https?://\S+`)

// numericFact matches a number that can identify a concrete fact: a price,
// mileage, listing ID, budget. Groups of three digits separated by a space or
// underscore are one number, so "78 050" and "2 497 925" are not split.
// A short number followed by a currency or scale ("3 млн") is also a fact.
var numericFact = regexp.MustCompile(`(?i)(\d{1,3}(?:[ _]\d{3})+|\d+)(?:\s*(₽|руб(?:\.|лей)?|млн|тыс(?:\.|яч)?))?`)

// IsolateMemoryUpdate stops the extractor from carrying numeric facts from one
// object to another. The bug it fixes: a new Avito listing arrived as a URL
// without a price, while the old listing's price was still in working memory;
// the extractor copied that price into the new listing's "следующий шаг".
//
// The guard is deliberately narrow. It runs only when the user's message has a
// URL, and only for working/knowledge records. An entry is dropped when it
// contains a number that is absent both from the message and from the previous
// value of the same key. A legitimate update of the same key still passes.
func IsolateMemoryUpdate(store *memory.Store, userText string, upd memory.Update) (memory.Update, []string) {
	if store == nil || !entityURL.MatchString(userText) {
		return upd, nil
	}
	fromMessage := numericFacts(userText)
	var notes []string

	filter := func(layer memory.Layer, entries []memory.Entry) []memory.Entry {
		if len(entries) == 0 {
			return entries
		}
		kept := make([]memory.Entry, 0, len(entries))
		for _, e := range entries {
			allowed := cloneSet(fromMessage)
			if old, ok := findEntry(store.Section(layer), e.Key); ok {
				for n := range numericFacts(old.Value) {
					allowed[n] = true
				}
			}
			if n := firstForeignNumber(e.Value, allowed); n != "" {
				notes = append(notes, fmt.Sprintf("%s %s: число %s не из нового сообщения", layer, e.Key, n))
				continue
			}
			kept = append(kept, e)
		}
		return kept
	}

	upd.Working = filter(memory.LayerWorking, upd.Working)
	upd.Knowledge = filter(memory.LayerKnowledge, upd.Knowledge)
	return upd, notes
}

// numericFacts returns the normalized numbers that can name a concrete fact.
// Short numbers without a currency/scale suffix are ignored: "2.4 AT" must not
// turn into a pair of facts 2 and 4.
func numericFacts(s string) map[string]bool {
	s = strings.NewReplacer("\u00a0", " ", "\u202f", " ").Replace(s)
	out := make(map[string]bool)
	for _, m := range numericFact.FindAllStringSubmatch(s, -1) {
		number := strings.NewReplacer(" ", "", "_", "").Replace(m[1])
		if number == "" {
			continue
		}
		if len(number) >= 4 || m[2] != "" {
			out[number] = true
		}
	}
	return out
}

// firstForeignNumber returns a number that is not in the allowed set. The keys
// are sorted so a trace line is stable when several numbers are foreign.
func firstForeignNumber(value string, allowed map[string]bool) string {
	numbers := numericFacts(value)
	keys := make([]string, 0, len(numbers))
	for n := range numbers {
		if !allowed[n] {
			keys = append(keys, n)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

func findEntry(entries []memory.Entry, key string) (memory.Entry, bool) {
	for _, e := range entries {
		if strings.EqualFold(strings.TrimSpace(e.Key), strings.TrimSpace(key)) {
			return e, true
		}
	}
	return memory.Entry{}, false
}

func cloneSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
