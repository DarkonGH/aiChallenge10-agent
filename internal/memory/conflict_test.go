package memory

import (
	"strings"
	"testing"
	"time"
)

// TestApplyJournalsReplacement is the day-15 core: a new value for the same
// key replaces the old one in place, stamps a write time, and leaves an
// old → new record for /memory history.
func TestApplyJournalsReplacement(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{Working: []Entry{{Key: "срок", Value: "15 декабря"}}})

	changes := s.Apply(Update{Working: []Entry{{Key: "Срок", Value: "20 декабря"}}})
	if len(changes) != 1 {
		t.Fatalf("изменений %d, ожидалось 1: %+v", len(changes), changes)
	}
	c := changes[0]
	if c.Layer != LayerWorking || c.Kind != "update" || c.Old != "15 декабря" || c.New != "20 декабря" {
		t.Fatalf("журнал замены: %+v", c)
	}
	entries := s.Section(LayerWorking)
	if len(entries) != 1 || entries[0].Value != "20 декабря" {
		t.Fatalf("после замены: %+v", entries)
	}
	if entries[0].UpdatedAt == "" {
		t.Error("у записи нет времени последней записи")
	}
	if c.At == "" {
		t.Error("в журнале нет времени замены")
	}

	prompt := s.Prompt("")
	if !strings.Contains(prompt, "20 декабря") {
		t.Errorf("в промпте нет нового значения:\n%s", prompt)
	}
	if strings.Contains(prompt, "15 декабря") {
		t.Errorf("в промпте осталось старое значение:\n%s", prompt)
	}
	if !strings.Contains(prompt, entries[0].UpdatedLabel()) {
		t.Errorf("в промпте нет времени записи [%s]:\n%s", entries[0].UpdatedLabel(), prompt)
	}
}

// TestLaterWriteWins covers the explicit priority rule. The extractor gets its
// timestamp from the code, but an import or a replayed journal can carry one;
// an older write must not overwrite a newer fact.
func TestLaterWriteWins(t *testing.T) {
	s := openTemp(t)
	now := time.Now()
	newer := now.Add(2 * time.Minute).Format(time.RFC3339)
	older := now.Add(-2 * time.Minute).Format(time.RFC3339)

	s.Apply(Update{Working: []Entry{{Key: "статус", Value: "согласован", UpdatedAt: newer}}})
	changes := s.Apply(Update{Working: []Entry{{Key: "статус", Value: "отменён", UpdatedAt: older}}})
	if len(changes) != 1 || changes[0].Kind != "stale" {
		t.Fatalf("устаревшая запись не отклонена: %+v", changes)
	}
	if got := s.Section(LayerWorking)[0].Value; got != "согласован" {
		t.Fatalf("значение после устаревшей записи = %q, ожидалось «согласован»", got)
	}

	latest := now.Add(4 * time.Minute).Format(time.RFC3339)
	changes = s.Apply(Update{Working: []Entry{{Key: "статус", Value: "закрыт", UpdatedAt: latest}}})
	if len(changes) != 1 || changes[0].Kind != "update" || changes[0].New != "закрыт" {
		t.Fatalf("более поздняя запись не применилась: %+v", changes)
	}
}

// TestAddAndClearJournal checks the command path: manual add and clear leave
// the same old → new trail as the extractor.
func TestAddAndClearJournal(t *testing.T) {
	s := openTemp(t)
	ch, err := s.AddWithChange(LayerWorking, "бюджет", "300 000")
	if err != nil {
		t.Fatalf("AddWithChange: %v", err)
	}
	if ch.Kind != "add" || ch.New != "300 000" || ch.At == "" {
		t.Fatalf("журнал добавления: %+v", ch)
	}
	ch, err = s.AddWithChange(LayerWorking, "бюджет", "350 000")
	if err != nil {
		t.Fatalf("AddWithChange: %v", err)
	}
	if ch.Kind != "update" || ch.Old != "300 000" || ch.New != "350 000" {
		t.Fatalf("журнал обновления: %+v", ch)
	}

	changes := s.ClearWithChanges(LayerWorking)
	if len(changes) != 1 || changes[0].Kind != "remove" || changes[0].Old != "350 000" {
		t.Fatalf("журнал очистки: %+v", changes)
	}
	if s.Len(LayerWorking) != 0 {
		t.Errorf("после ClearWithChanges осталось %d записей", s.Len(LayerWorking))
	}
}

// TestDecisionsKeepDateAndHistory: decisions accumulate, a changed decision
// keeps its original date but gets a new journal record.
func TestDecisionsKeepDateAndHistory(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{Decisions: []Entry{{Key: "площадка", Value: "коворкинг", At: "2026-11-17"}}})
	changes := s.Apply(Update{Decisions: []Entry{{Key: "площадка", Value: "свой офис"}}})
	if len(changes) != 1 || changes[0].Kind != "update" || changes[0].Old != "коворкинг" {
		t.Fatalf("журнал решения: %+v", changes)
	}
	d := s.Section(LayerDecisions)[0]
	if d.Value != "свой офис" || d.At != "2026-11-17" {
		t.Fatalf("решение после замены: %+v", d)
	}
}

// TestDecisionsStripDateFromExtractorKey: Render shows decisions with a date,
// and the extractor sometimes copies "2026-11-21 · итог" back as the key. The
// date belongs to Entry.At; in the key it would create a duplicate instead of
// replacing the decision.
func TestDecisionsStripDateFromExtractorKey(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{Decisions: []Entry{{Key: "итог", Value: "коворкинг"}}})
	changes := s.Apply(Update{Decisions: []Entry{{Key: "2026-11-21 · итог", Value: "офис"}}})
	if len(changes) != 1 || changes[0].Kind != "update" {
		t.Fatalf("ожидалась замена, а не вторая запись: %+v", changes)
	}
	d := s.Section(LayerDecisions)
	if len(d) != 1 || d[0].Key != "итог" || d[0].Value != "офис" {
		t.Fatalf("decisions после замены: %+v", d)
	}
}

// TestRemoveDecisionsWithDateKey: removals come from the same extractor as
// additions, so the date prefix must be stripped there too. Otherwise a
// decision that Render shows as "2026-11-21 · итог" cannot be deleted.
func TestRemoveDecisionsWithDateKey(t *testing.T) {
	s := openTemp(t)
	s.Apply(Update{Decisions: []Entry{{Key: "итог", Value: "коворкинг"}}})

	changes := s.Apply(Update{Remove: map[Layer][]string{
		LayerDecisions: {"2026-11-21 · итог"},
	}})
	if len(changes) != 1 || changes[0].Kind != "remove" || changes[0].Old != "коворкинг" {
		t.Fatalf("журнал удаления: %+v", changes)
	}
	if got := s.Section(LayerDecisions); len(got) != 0 {
		t.Fatalf("решение с датой в ключе не удалено: %+v", got)
	}
}

// TestOlderWriteSubsecond keeps two writes to the same key inside one second
// ordered: Now uses RFC3339Nano, and olderWrite must parse the fraction.
func TestOlderWriteSubsecond(t *testing.T) {
	early := "2026-11-21T21:35:15.100+03:00"
	late := "2026-11-21T21:35:15.900+03:00"
	if !olderWrite(early, late) {
		t.Errorf("olderWrite(%q, %q) = false, ожидалось true", early, late)
	}
	if olderWrite(late, early) {
		t.Errorf("olderWrite(%q, %q) = true, ожидалось false", late, early)
	}
	if _, err := time.Parse(time.RFC3339Nano, Now()); err != nil {
		t.Errorf("Now() не RFC3339Nano: %v", err)
	}
}

// TestDescribeChanges is the text used by traces and -print-session.
func TestDescribeChanges(t *testing.T) {
	got := DescribeChanges([]Change{
		{Layer: LayerWorking, Key: "срок", Kind: "update", Old: "15 декабря", New: "20 декабря"},
		{Layer: LayerWorking, Key: "статус", Kind: "stale", Old: "согласован", New: "отменён"},
		{Layer: LayerWorking, Key: "адрес", Kind: "evict", Old: "Пулковское шоссе 15"},
	})
	for _, want := range []string{
		"15 декабря → 20 декабря",
		"устаревшее значение",
		"вытеснено лимитом",
		"Пулковское шоссе 15",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в описании нет %q: %s", want, got)
		}
	}
}
