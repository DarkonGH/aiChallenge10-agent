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
	"regexp"
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
	// UpdatedAt is when this value was last written (RFC3339). The code stamps
	// it; the extractor never supplies it. It is the tie-breaker when two
	// records for the same key race: the later write wins.
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Now returns the timestamp written into Entry.UpdatedAt and Change.At.
// RFC3339 keeps the time zone: a comparison between two records must not
// depend on the machine's local time.
func Now() string { return time.Now().Format(time.RFC3339) }

// Change is one entry of the memory journal: what a single update did to one
// key. Kind is "add", "update", "remove" or "stale"; Old and New carry the
// values, so the log answers both «что было» and «что стало».
type Change struct {
	Layer Layer  `json:"layer"`
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Old   string `json:"old,omitempty"`
	New   string `json:"new,omitempty"`
	At    string `json:"at"`
}

// DescribeChange renders one change for /memory history and -print-session.
func DescribeChange(c Change) string {
	switch c.Kind {
	case "add":
		return fmt.Sprintf("%s + %s: %s", c.Layer, c.Key, c.New)
	case "update":
		return fmt.Sprintf("%s %s: %s → %s", c.Layer, c.Key, c.Old, c.New)
	case "remove":
		return fmt.Sprintf("%s − %s (было: %s)", c.Layer, c.Key, c.Old)
	case "stale":
		return fmt.Sprintf("%s %s: отклонено устаревшее значение %q (актуально %q)", c.Layer, c.Key, c.New, c.Old)
	}
	return fmt.Sprintf("%s %s: %s", c.Layer, c.Key, c.Kind)
}

// DescribeChanges joins changes for one trace/journal line.
func DescribeChanges(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, DescribeChange(c))
	}
	return strings.Join(parts, "; ")
}

// UpdatedLabel is the compact local time shown next to an entry.
func (e Entry) UpdatedLabel() string { return FormatTime(e.UpdatedAt) }

// FormatTime turns an RFC3339 timestamp into the compact local form used in
// listings and the journal. An empty or broken value renders as "".
func FormatTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return t.Local().Format("02.01 15:04")
}

// Update is the extractor's answer: the entries to merge into the store. Only
// new or changed records are expected — the store is never replaced wholesale.
type Update struct {
	Working   []Entry `json:"working,omitempty"`
	Profile   []Entry `json:"profile,omitempty"`
	Decisions []Entry `json:"decisions,omitempty"`
	Knowledge []Entry `json:"knowledge,omitempty"`

	// Remove lists keys to delete from a layer. The extractor uses it to move
	// a fact between layers (add to the new one, delete from the old one) and
	// to drop what stopped being true.
	Remove map[Layer][]string `json:"remove,omitempty"`
}

// Empty reports whether the update carries nothing.
func (u Update) Empty() bool {
	if len(u.Working) > 0 || len(u.Profile) > 0 || len(u.Decisions) > 0 || len(u.Knowledge) > 0 {
		return false
	}
	for _, keys := range u.Remove {
		if len(keys) > 0 {
			return false
		}
	}
	return true
}

// Removed returns the keys the update deletes from one layer.
func (u Update) Removed(l Layer) []string { return u.Remove[l] }

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
// all sessions of the agent (a person is the same person in every task, and a
// named profile is one person or one test persona), the rest is scoped to the
// session directory.
type Store struct {
	sessionDir string // <data>/<session>/memory
	sharedDir  string // <data>/shared or <data>/profiles/<name>

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

// LoadProfile switches the shared profile to the one stored in dir and reloads
// it. An empty dir disables the profile block. Nothing is written here: a
// switch never modifies the profile that was active before, so /profile use is
// safe to try and undo.
func (s *Store) LoadProfile(dir string) error {
	var profile []Entry
	if dir != "" {
		if err := readJSON(filepath.Join(dir, "profile.json"), &profile); err != nil {
			return fmt.Errorf("память: profile.json: %w", err)
		}
	}
	// A hand-edited profile may exceed the cap; trim on load like the others.
	profile, _ = capEntries(profile, Caps[LayerProfile])
	s.sharedDir = dir
	s.profile = profile
	return nil
}

// ProfileDir is the directory profile.json is read from and written to.
func (s *Store) ProfileDir() string { return s.sharedDir }

// ReadProfile loads a profile file without opening a store: the /profile list
// view needs the record count of profiles that are not active right now.
func ReadProfile(path string) ([]Entry, error) {
	var entries []Entry
	if err := readJSON(path, &entries); err != nil {
		return nil, err
	}
	return entries, nil
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

	if err := s.LoadProfile(s.sharedDir); err != nil {
		return err
	}

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
		// An emptied profile is an empty list, not null: the file is read by
		// people and by /profile list, and "null" looks like a broken file.
		profile := s.profile
		if profile == nil {
			profile = []Entry{}
		}
		if err := writeJSON(filepath.Join(s.sharedDir, "profile.json"), profile); err != nil {
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

// decisionDatePrefix matches the date that Render puts in front of a decision
// ("2026-11-21 · площадка: …"). The extractor sometimes copies it back into the
// key; the date already lives in Entry.At, so keeping it in the key would create
// a second record instead of replacing the original.
var decisionDatePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\s*[·:\-–—]\s*`)

func canonicalKey(l Layer, key string) string {
	key = strings.TrimSpace(key)
	if l == LayerDecisions {
		key = strings.TrimSpace(decisionDatePrefix.ReplaceAllString(key, ""))
	}
	return key
}

// Apply merges the extractor's update: removals go first, then an existing key
// is updated in place and a new key is appended. Layer caps evict the oldest
// records first.
//
// The returned changes are the day-15 journal: add, update (old → new),
// remove and stale. A stale entry is one whose UpdatedAt is older than the
// stored value: the later write wins and the old one is not applied.
func (s *Store) Apply(u Update) []Change {
	var changes []Change
	at := Now()
	for _, l := range Layers {
		entries := u.Layer(l)
		removed := u.Removed(l)
		if len(entries) == 0 && len(removed) == 0 {
			continue
		}
		cur := s.Section(l)
		var removedChanges []Change
		cur, removedChanges = removeEntriesChanged(cur, removed, l, at)
		changes = append(changes, removedChanges...)
		for _, e := range entries {
			e.Key = canonicalKey(l, e.Key)
			if e.Key == "" {
				continue
			}
			// The code owns the write time: the extractor cannot backdate a
			// fact to make it win a conflict.
			if e.UpdatedAt == "" {
				e.UpdatedAt = at
			}
			// Decisions are dated when they are first stored; a later
			// re-statement keeps the original date (mergeEntryChanged does that).
			if l == LayerDecisions && e.At == "" {
				e.At = Date()
			}
			var ch Change
			cur, ch = mergeEntryChanged(cur, e, l, at)
			if ch.Kind != "" {
				changes = append(changes, ch)
			}
		}
		// SetSection enforces the cap. Capture what it will drop so the log
		// says explicitly that old records left the prompt, not just that the
		// layer became smaller.
		willDrop := 0
		if n := Caps[l]; n > 0 && len(cur) > n {
			willDrop = len(cur) - n
		}
		dropped := append([]Entry(nil), cur[:willDrop]...)
		s.SetSection(l, cur)
		for _, e := range dropped {
			changes = append(changes, Change{Layer: l, Key: e.Key, Kind: "evict", Old: e.Value, At: at})
		}
	}
	return changes
}

// removeEntriesChanged drops the records whose key matches one of keys,
// ignoring case, and journals each removal with its old value.
func removeEntriesChanged(entries []Entry, keys []string, l Layer, at string) ([]Entry, []Change) {
	if len(keys) == 0 {
		return entries, nil
	}
	out := make([]Entry, 0, len(entries))
	var changes []Change
	for _, e := range entries {
		drop := false
		for _, k := range keys {
			if strings.EqualFold(e.Key, strings.TrimSpace(k)) {
				drop = true
				break
			}
		}
		if drop {
			changes = append(changes, Change{Layer: l, Key: e.Key, Kind: "remove", Old: e.Value, At: at})
			continue
		}
		out = append(out, e)
	}
	return out, changes
}

// AddWithChange inserts or updates one record and returns the journal entry
// (the /memory add command). Add keeps the old signature for callers that do
// not journal.
func (s *Store) AddWithChange(l Layer, key, value string) (Change, error) {
	if _, ok := Caps[l]; !ok {
		return Change{}, fmt.Errorf("неизвестный слой %q; доступны: working, profile, decisions, knowledge", l)
	}
	key = canonicalKey(l, key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return Change{}, fmt.Errorf("нужны ключ и значение: /memory add <слой> ключ=значение")
	}
	at := Now()
	ent := Entry{Key: key, Value: value, UpdatedAt: at}
	if l == LayerDecisions {
		ent.At = Date()
	}
	cur, ch := mergeEntryChanged(s.Section(l), ent, l, at)
	s.SetSection(l, cur)
	return ch, nil
}

// Add inserts or updates one record (the /memory add command).
func (s *Store) Add(l Layer, key, value string) error {
	_, err := s.AddWithChange(l, key, value)
	return err
}

// ClearWithChanges empties one layer and returns the removed records, so a
// manual clear and the automatic working-memory cleanup after a finished task
// leave the same audit trail.
func (s *Store) ClearWithChanges(l Layer) []Change {
	if _, ok := Caps[l]; !ok {
		return nil
	}
	entries := s.Section(l)
	if len(entries) == 0 {
		return nil
	}
	at := Now()
	changes := make([]Change, 0, len(entries))
	for _, e := range entries {
		changes = append(changes, Change{Layer: l, Key: e.Key, Kind: "remove", Old: e.Value, At: at})
	}
	s.SetSection(l, nil)
	return changes
}

// Clear empties one layer.
func (s *Store) Clear(l Layer) error {
	if _, ok := Caps[l]; !ok {
		return fmt.Errorf("неизвестный слой %q; доступны: working, profile, decisions, knowledge", l)
	}
	s.SetSection(l, nil)
	return nil
}

// mergeEntryChanged replaces the value of an existing key or appends a new
// record. An entry whose write time is older than the stored one is rejected
// as stale: the later fact wins, as the assignment requires.
func mergeEntryChanged(entries []Entry, e Entry, l Layer, at string) ([]Entry, Change) {
	for i := range entries {
		if strings.EqualFold(entries[i].Key, e.Key) {
			if olderWrite(e.UpdatedAt, entries[i].UpdatedAt) {
				return entries, Change{Layer: l, Key: entries[i].Key, Kind: "stale", Old: entries[i].Value, New: e.Value, At: at}
			}
			old := entries[i].Value
			entries[i].Value = e.Value
			entries[i].UpdatedAt = e.UpdatedAt
			// A decision keeps the date it was taken; the replacement gets a
			// new UpdatedAt but not a new decision date.
			if e.At != "" && entries[i].At == "" {
				entries[i].At = e.At
			}
			if old == e.Value {
				// Repeating the same value is not a change; keeping it out of
				// the journal makes the log readable.
				return entries, Change{}
			}
			return entries, Change{Layer: l, Key: entries[i].Key, Kind: "update", Old: old, New: e.Value, At: at}
		}
	}
	return append(entries, e), Change{Layer: l, Key: e.Key, Kind: "add", New: e.Value, At: at}
}

// olderWrite reports whether a is strictly older than b. Missing timestamps
// mean "unknown", and an unknown write never wins a conflict.
func olderWrite(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	if errA != nil || errB != nil {
		return false
	}
	return ta.Before(tb)
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
