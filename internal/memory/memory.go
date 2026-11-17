// Package memory implements the explicit memory model of the agent: three
// layers with their own storage, their own lifetime and their own rule for
// entering the prompt.
//
//	short    — the active dialogue window (lives in session.jsonl, see
//	           internal/history and internal/compact)
//	working  — the card of the current task: goal, constraints, numbers,
//	           deadlines, open questions, next step
//	long     — everything that outlives the task: profile, decisions, knowledge
//
// Working and long-term memory are stored apart from the dialogue log, in
// readable JSON files, so the layers can be inspected, cleared and counted
// independently. Every change is also journaled into session.jsonl as a
// "memory" event; the files stay the source of truth.
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Layer names one memory layer. Short-term memory has no layer value here: it
// is the dialogue window, owned by the history.
type Layer string

const (
	LayerWorking   Layer = "working"
	LayerProfile   Layer = "profile"
	LayerDecisions Layer = "decisions"
	LayerKnowledge Layer = "knowledge"
)

// Layers lists the layers this package owns, in prompt order.
var Layers = []Layer{LayerWorking, LayerProfile, LayerDecisions, LayerKnowledge}

// Caps are the hard limits of each layer. They live in code, not in the
// extractor prompt: a model cannot be trusted to keep a budget.
var Caps = map[Layer]int{
	LayerWorking:   12,
	LayerProfile:   8,
	LayerDecisions: 12,
	LayerKnowledge: 20,
}

// Entry is one record of a layer.
type Entry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// At is the date a decision was taken (YYYY-MM-DD). Empty for other layers.
	At string `json:"at,omitempty"`
}

// Update is the extractor's answer: the entries to merge into the store. Only
// new or changed records are expected — the store is never replaced wholesale.
type Update struct {
	Working   []Entry `json:"working,omitempty"`
	Profile   []Entry `json:"profile,omitempty"`
	Decisions []Entry `json:"decisions,omitempty"`
	Knowledge []Entry `json:"knowledge,omitempty"`
}

// Empty reports whether the update carries nothing.
func (u Update) Empty() bool {
	return len(u.Working) == 0 && len(u.Profile) == 0 &&
		len(u.Decisions) == 0 && len(u.Knowledge) == 0
}

// Layer returns the entries of one layer inside the update.
func (u Update) Layer(l Layer) []Entry {
	switch l {
	case LayerWorking:
		return u.Working
	case LayerProfile:
		return u.Profile
	case LayerDecisions:
		return u.Decisions
	case LayerKnowledge:
		return u.Knowledge
	}
	return nil
}

// Store is the working + long-term memory of one session. Profile is shared by
// all sessions of the agent (a person is the same person in every task), the
// rest is scoped to the session directory.
type Store struct {
	sessionDir string // <data>/<session>/memory
	sharedDir  string // <data>/shared

	working   []Entry
	profile   []Entry
	decisions []Entry
	knowledge []Entry

	// evicted counts the records the layer caps dropped since Open. The import
	// report uses it to show what the limits cost on a big store.
	evicted map[Layer]int

	warnf func(format string, args ...any)
}

// longFile is the on-disk shape of long.json.
type longFile struct {
	Profile   []Entry `json:"profile"`
	Decisions []Entry `json:"decisions"`
	Knowledge []Entry `json:"knowledge"`
}

// Open loads the store. Missing files are not an error: an empty memory is a
// valid starting point. sessionDir holds working/decisions/knowledge,
// sharedDir holds the cross-session profile.
func Open(sessionDir, sharedDir string) (*Store, error) {
	s := &Store{sessionDir: sessionDir, sharedDir: sharedDir, evicted: make(map[Layer]int)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	var working []Entry
	if err := readJSON(filepath.Join(s.sessionDir, "working.json"), &working); err != nil {
		return fmt.Errorf("память: working.json: %w", err)
	}
	s.working = working

	var long longFile
	if err := readJSON(filepath.Join(s.sessionDir, "long.json"), &long); err != nil {
		return fmt.Errorf("память: long.json: %w", err)
	}
	s.decisions = long.Decisions
	s.knowledge = long.Knowledge

	var profile []Entry
	if s.sharedDir != "" {
		if err := readJSON(filepath.Join(s.sharedDir, "profile.json"), &profile); err != nil {
			return fmt.Errorf("память: profile.json: %w", err)
		}
	}
	s.profile = profile

	// A hand-edited file may exceed the caps; trim on load so the prompt is
	// never larger than the declared budget.
	s.working, _ = capEntries(s.working, Caps[LayerWorking])
	s.profile, _ = capEntries(s.profile, Caps[LayerProfile])
	s.decisions, _ = capEntries(s.decisions, Caps[LayerDecisions])
	s.knowledge, _ = capEntries(s.knowledge, Caps[LayerKnowledge])
	return nil
}

// readJSON decodes path into v; a missing file leaves v untouched.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// Save writes every layer to disk. Files are replaced atomically so a crash
// never leaves a half-written memory.
func (s *Store) Save() error {
	if err := os.MkdirAll(s.sessionDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(s.sessionDir, "working.json"), s.working); err != nil {
		return err
	}
	long := longFile{Profile: s.profile, Decisions: s.decisions, Knowledge: s.knowledge}
	// profile lives in the shared file, not in long.json: keep the field out
	// of the per-session copy to avoid two sources of truth.
	long.Profile = nil
	if err := writeJSON(filepath.Join(s.sessionDir, "long.json"), long); err != nil {
		return err
	}
	if s.sharedDir != "" {
		if err := os.MkdirAll(s.sharedDir, 0o755); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(s.sharedDir, "profile.json"), s.profile); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Section returns a copy of the layer's entries.
func (s *Store) Section(l Layer) []Entry {
	switch l {
	case LayerWorking:
		return append([]Entry(nil), s.working...)
	case LayerProfile:
		return append([]Entry(nil), s.profile...)
	case LayerDecisions:
		return append([]Entry(nil), s.decisions...)
	case LayerKnowledge:
		return append([]Entry(nil), s.knowledge...)
	}
	return nil
}

// SetSection replaces one layer wholesale (used by the day-10 importer and by
// /memory clear).
func (s *Store) SetSection(l Layer, entries []Entry) {
	var dropped int
	entries, dropped = capEntries(entries, Caps[l])
	if dropped > 0 {
		if s.evicted == nil {
			s.evicted = make(map[Layer]int)
		}
		s.evicted[l] += dropped
	}
	switch l {
	case LayerWorking:
		s.working = entries
	case LayerProfile:
		s.profile = entries
	case LayerDecisions:
		s.decisions = entries
	case LayerKnowledge:
		s.knowledge = entries
	}
}

// Evicted reports how many records the layer cap dropped since the store was
// opened. The day-10 import prints it so a big store does not look complete
// when half of it was cut by the limit.
func (s *Store) Evicted(l Layer) int { return s.evicted[l] }

// EvictedTotal sums the evictions across all layers.
func (s *Store) EvictedTotal() int {
	n := 0
	for _, l := range Layers {
		n += s.evicted[l]
	}
	return n
}

// Apply merges the extractor's update: an existing key is updated in place, a
// new key is appended. Layer caps evict the oldest records first.
func (s *Store) Apply(u Update) {
	for _, l := range Layers {
		entries := u.Layer(l)
		if len(entries) == 0 {
			continue
		}
		cur := s.Section(l)
		for _, e := range entries {
			// Decisions are dated when they are first stored; a later
			// re-statement keeps the original date (mergeEntry does that).
			if l == LayerDecisions && e.At == "" {
				e.At = Date()
			}
			cur = mergeEntry(cur, e)
		}
		s.SetSection(l, cur)
	}
}

// Add inserts or updates one record (the /memory add command).
func (s *Store) Add(l Layer, key, value string) error {
	if _, ok := Caps[l]; !ok {
		return fmt.Errorf("неизвестный слой %q; доступны: working, profile, decisions, knowledge", l)
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return fmt.Errorf("нужны ключ и значение: /memory add <слой> ключ=значение")
	}
	ent := Entry{Key: key, Value: value}
	if l == LayerDecisions {
		ent.At = Date()
	}
	cur := mergeEntry(s.Section(l), ent)
	s.SetSection(l, cur)
	return nil
}

// Clear empties one layer.
func (s *Store) Clear(l Layer) error {
	if _, ok := Caps[l]; !ok {
		return fmt.Errorf("неизвестный слой %q; доступны: working, profile, decisions, knowledge", l)
	}
	s.SetSection(l, nil)
	return nil
}

// mergeEntry replaces the value of an existing key or appends a new record.
func mergeEntry(entries []Entry, e Entry) []Entry {
	for i := range entries {
		if strings.EqualFold(entries[i].Key, e.Key) {
			entries[i].Value = e.Value
			if e.At != "" {
				entries[i].At = e.At
			}
			return entries
		}
	}
	return append(entries, e)
}

// capEntries keeps the newest n records: the front of the slice is the oldest.
// It also reports how many records were dropped, so the caller can count the
// evictions.
func capEntries(entries []Entry, n int) ([]Entry, int) {
	if n <= 0 || len(entries) <= n {
		return entries, 0
	}
	return entries[len(entries)-n:], len(entries) - n
}

// Date returns today's date in the form used for decisions.
func Date() string { return time.Now().Format("2006-01-02") }

// Len reports how many records a layer holds.
func (s *Store) Len(l Layer) int { return len(s.Section(l)) }

// Total reports the number of records across all layers.
func (s *Store) Total() int {
	return len(s.working) + len(s.profile) + len(s.decisions) + len(s.knowledge)
}
