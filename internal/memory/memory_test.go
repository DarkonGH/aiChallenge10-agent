package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "memory"), filepath.Join(dir, "shared"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// TestApplyMergesByKey checks the core update rule: an existing key is
// replaced in place (order kept), a new one is appended.
func TestApplyMergesByKey(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{Working: []Entry{{Key: "срок", Value: "15 декабря"}, {Key: "бюджет", Value: "480 000"}}})
	s.Apply(Update{Working: []Entry{{Key: "Срок", Value: "20 декабря"}, {Key: "мест", Value: "42"}}})

	got := s.Section(LayerWorking)
	want := []Entry{{Key: "срок", Value: "20 декабря"}, {Key: "бюджет", Value: "480 000"}, {Key: "мест", Value: "42"}}
	if len(got) != len(want) {
		t.Fatalf("записей %d, ожидалось %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("запись %d = %v, ожидалось %v", i, got[i], want[i])
		}
	}
}

// TestCapsKeepNewest checks that a layer never grows past its cap and that the
// oldest entries are the ones dropped.
func TestCapsKeepNewest(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < Caps[LayerWorking]+5; i++ {
		s.Add(LayerWorking, string(rune('a'+i)), "x")
	}
	got := s.Section(LayerWorking)
	if len(got) != Caps[LayerWorking] {
		t.Fatalf("записей %d, ожидался потолок %d", len(got), Caps[LayerWorking])
	}
	if got[0].Key != "f" {
		t.Errorf("первая запись %q, ожидалась «f» (старые вытеснены)", got[0].Key)
	}
}

// TestSaveLoadRoundTrip checks that the layers survive a reopen and that the
// profile is shared between two sessions of the same data dir.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared")
	s, err := Open(filepath.Join(dir, "s1", "memory"), shared)
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(Update{
		Working:   []Entry{{Key: "цель", Value: "переезд"}},
		Profile:   []Entry{{Key: "роль", Value: "офис-менеджер"}},
		Decisions: []Entry{{Key: "площадка", Value: "коворкинг", At: "2026-11-17"}},
		Knowledge: []Entry{{Key: "адрес", Value: "Пресня, 1"}},
	})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	re, err := Open(filepath.Join(dir, "s1", "memory"), shared)
	if err != nil {
		t.Fatal(err)
	}
	if re.Total() != 4 {
		t.Fatalf("после перезагрузки %d записей, ожидалось 4", re.Total())
	}
	if got := re.Section(LayerDecisions)[0].At; got != "2026-11-17" {
		t.Errorf("дата решения %q, ожидалась 2026-11-17", got)
	}

	// A second session sees the shared profile but not the first session's
	// working layer.
	other, err := Open(filepath.Join(dir, "s2", "memory"), shared)
	if err != nil {
		t.Fatal(err)
	}
	if other.Len(LayerProfile) != 1 {
		t.Errorf("общий профиль не подхвачен: %d записей", other.Len(LayerProfile))
	}
	if other.Len(LayerWorking) != 0 {
		t.Errorf("рабочая память протекла в другую сессию: %v", other.Section(LayerWorking))
	}

	if _, err := os.Stat(filepath.Join(dir, "s1", "memory", "working.json")); err != nil {
		t.Errorf("working.json не создан: %v", err)
	}
	if _, err := os.Stat(filepath.Join(shared, "profile.json")); err != nil {
		t.Errorf("shared/profile.json не создан: %v", err)
	}
}

// TestPromptOrderAndKnowledgeFilter checks the per-layer prompt rules:
// working, profile and decisions always, knowledge only by relevance.
func TestPromptOrderAndKnowledgeFilter(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{
		Working:   []Entry{{Key: "срок", Value: "15 декабря"}},
		Profile:   []Entry{{Key: "роль", Value: "офис-менеджер"}},
		Decisions: []Entry{{Key: "площадка", Value: "коворкинг Пресня", At: "2026-11-17"}},
		Knowledge: []Entry{{Key: "адрес", Value: "Пресня, 1"}, {Key: "телефон", Value: "+7 495 000"}},
	})

	block := s.Prompt("какой адрес у коворкинга?")
	for _, want := range []string{"Рабочая память задачи:", "срок", "Профиль пользователя:", "Принятые решения:", "2026-11-17 · площадка", "Знания по теме вопроса:", "адрес"} {
		if !strings.Contains(block, want) {
			t.Errorf("в блоке нет %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "телефон") {
		t.Errorf("нерелевантное знание попало в промпт:\n%s", block)
	}

	// Order: working → profile → decisions → knowledge.
	iWork := strings.Index(block, "Рабочая память")
	iProf := strings.Index(block, "Профиль пользователя")
	iDec := strings.Index(block, "Принятые решения")
	iKnow := strings.Index(block, "Знания по теме")
	if !(iWork < iProf && iProf < iDec && iDec < iKnow) {
		t.Errorf("порядок слоёв нарушен: %d %d %d %d", iWork, iProf, iDec, iKnow)
	}

	tok := s.LayerTokens("какой адрес у коворкинга?")
	if tok[LayerKnowledge] == 0 {
		t.Error("слой знаний не посчитан в токенах")
	}
	if tok[LayerProfile] == 0 || tok[LayerDecisions] == 0 {
		t.Error("постоянные слои не посчитаны в токенах")
	}
}

// TestClearAndAddErrors checks the command-level API: clearing one layer
// leaves the others alone, and unknown layers are rejected.
func TestClearAndAddErrors(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{
		Working: []Entry{{Key: "цель", Value: "переезд"}},
		Profile: []Entry{{Key: "роль", Value: "офис-менеджер"}},
	})
	if err := s.Clear(LayerWorking); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if s.Len(LayerWorking) != 0 || s.Len(LayerProfile) != 1 {
		t.Errorf("после clear: working %d, profile %d", s.Len(LayerWorking), s.Len(LayerProfile))
	}
	if err := s.Add("короткая", "a", "b"); err == nil {
		t.Error("неизвестный слой принят без ошибки")
	}
	if err := s.Clear("long"); err == nil {
		t.Error("неизвестный слой очищен без ошибки")
	}
}

// TestRelevantScoresAndLimits checks the deterministic knowledge selector.
func TestRelevantScoresAndLimits(t *testing.T) {
	entries := []Entry{
		{Key: "адрес офиса", Value: "Пресня, 1"},
		{Key: "телефон", Value: "+7 495 000"},
		{Key: "адрес склада", Value: "Пресня, 2"},
		{Key: "график", Value: "будни"},
	}
	got := Relevant(entries, "какой адрес у офиса на Пресне?", 2)
	if len(got) != 2 {
		t.Fatalf("выбрано %d, ожидалось 2", len(got))
	}
	if got[0].Key != "адрес офиса" {
		t.Errorf("первым должен идти «адрес офиса», получено %q", got[0].Key)
	}
	if empty := Relevant(entries, "сколько стоит кофе?", 2); len(empty) != 0 {
		t.Errorf("на нерелевантный вопрос выбрано %d записей", len(empty))
	}
}

// TestEvictionIsCounted checks that a bulk update over the cap reports how many
// records the limit dropped — the day-10 import prints this instead of claiming
// everything was stored.
func TestEvictionIsCounted(t *testing.T) {
	s := openTemp(t)
	var many []Entry
	for i := 0; i < Caps[LayerWorking]+4; i++ {
		many = append(many, Entry{Key: string(rune('a' + i)), Value: "x"})
	}
	s.Apply(Update{Working: many})

	if got := s.Len(LayerWorking); got != Caps[LayerWorking] {
		t.Fatalf("записей %d, ожидался потолок %d", got, Caps[LayerWorking])
	}
	if got := s.Evicted(LayerWorking); got != 4 {
		t.Errorf("вытеснено %d, ожидалось 4", got)
	}
	if got := s.EvictedTotal(); got != 4 {
		t.Errorf("всего вытеснено %d, ожидалось 4", got)
	}
	if got := s.Evicted(LayerProfile); got != 0 {
		t.Errorf("в profile вытеснено %d, ожидалось 0", got)
	}
}

// TestApplyRemovesKeys checks the deletion half of the diff: the extractor can
// drop a record that moved to another layer or stopped being true.
func TestApplyRemovesKeys(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{Working: []Entry{
		{Key: "открытый вопрос", Value: "вариант A или B"},
		{Key: "срок", Value: "15 декабря"},
	}})

	// Case-insensitive key match, like mergeEntry.
	s.Apply(Update{
		Working:   []Entry{{Key: "срок", Value: "20 декабря"}},
		Decisions: []Entry{{Key: "выбор", Value: "вариант B"}},
		Remove:    map[Layer][]string{LayerWorking: {"Открытый Вопрос"}},
	})

	working := s.Section(LayerWorking)
	if len(working) != 1 || working[0].Key != "срок" || working[0].Value != "20 декабря" {
		t.Fatalf("working после удаления: %+v", working)
	}
	if got := s.Section(LayerDecisions); len(got) != 1 || got[0].Key != "выбор" {
		t.Fatalf("decisions: %+v", got)
	}
	if s.EvictedTotal() != 0 {
		t.Errorf("удаление не должно считаться вытеснением: %d", s.EvictedTotal())
	}
}

// TestUpdateWithOnlyRemovalsIsNotEmpty keeps the journal honest: a diff that
// only deletes records still has to be applied and written to the log.
func TestUpdateWithOnlyRemovalsIsNotEmpty(t *testing.T) {
	u := Update{Remove: map[Layer][]string{LayerWorking: {"ключ"}}}
	if u.Empty() {
		t.Error("дифф только с удалением считается пустым")
	}
	if got := u.Removed(LayerWorking); len(got) != 1 || got[0] != "ключ" {
		t.Errorf("Removed: %v", got)
	}
}
