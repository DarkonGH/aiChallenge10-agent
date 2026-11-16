package history

import (
	"fmt"
	"testing"
)

func TestForkSession(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir, "main", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	seedTurns(t, h, 3)
	lastSeq := h.LastSeq()
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	id, err := ForkSession(dir, "main", "b1")
	if err != nil || id != "b1" {
		t.Fatalf("ForkSession = %q, %v", id, err)
	}
	if _, err := ForkSession(dir, "main", "b1"); err == nil {
		t.Error("ожидали ошибку: сессия с таким именем уже есть")
	}
	if _, err := ForkSession(dir, "main", "../escape"); err == nil {
		t.Error("ожидали ошибку: недопустимое имя ветки")
	}

	fh, err := OpenHistory(dir, "b1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if fh.ForkOf() != "main" || fh.ForkSeq() != lastSeq {
		t.Errorf("fork = %q@%d, хотим main@%d", fh.ForkOf(), fh.ForkSeq(), lastSeq)
	}
	if len(fh.Active()) != 6 || len(fh.Archived()) != 0 {
		t.Errorf("ветка: active %d archived %d, хотим 6/0", len(fh.Active()), len(fh.Archived()))
	}

	// The branch grows independently; the parent stays at the checkpoint.
	fh.AppendUser("вопрос в ветке")
	fh.AppendAssistant("ответ в ветке", nil, "m")
	fh.Close()

	ph, err := OpenHistory(dir, "main", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	if ph.LastSeq() != lastSeq {
		t.Errorf("родитель вырос до seq %d, хотим %d", ph.LastSeq(), lastSeq)
	}

	kids, err := ListForks(dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 1 || kids[0] != "b1" {
		t.Errorf("ListForks = %v, хотим [b1]", kids)
	}
}

func TestFactsReplayFromLog(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir, "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}
	facts := []Fact{{Key: "срок", Value: "15 декабря"}, {Key: "бюджет", Value: "480 000 ₽"}}
	if err := h.SetFacts(facts); err != nil {
		t.Fatal(err)
	}
	// Обновление: снапшот заменяет список целиком.
	if err := h.SetFacts([]Fact{{Key: "срок", Value: "20 ноября"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	re, err := OpenHistory(dir, "s", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	got := re.Facts()
	if len(got) != 1 || got[0].Key != "срок" || got[0].Value != "20 ноября" {
		t.Errorf("после replay facts = %+v", got)
	}
}

// seedTurns appends n user/assistant turns.
func seedTurns(t *testing.T, h *History, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		h.AppendUser(fmt.Sprintf("вопрос номер %d", i))
		h.AppendAssistant(fmt.Sprintf("ответ номер %d", i), nil, "m")
	}
}
