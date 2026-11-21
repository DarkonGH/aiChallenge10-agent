package main

import (
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

// TestJournalMemoryChangesSkipsEmpty checks the /memory add path: repeating
// the same value returns an empty Change, and that no-op must not create a
// memory event or a blank line in /memory history.
func TestJournalMemoryChangesSkipsEmpty(t *testing.T) {
	h, err := history.OpenHistory(t.TempDir(), "test", "chat", "summary", "")
	if err != nil {
		t.Fatalf("OpenHistory: %v", err)
	}
	defer h.Close()

	a := &app{hist: h}
	a.journalMemoryChanges(memory.Update{}, []memory.Change{{}})
	if got := len(h.MemoryChanges()); got != 0 {
		t.Fatalf("пустое изменение попало в журнал: %d", got)
	}

	a.journalMemoryChanges(memory.Update{}, []memory.Change{{
		Layer: memory.LayerWorking, Key: "срок", Kind: "add", New: "15 декабря",
	}})
	if got := len(h.MemoryChanges()); got != 1 {
		t.Fatalf("настоящее изменение не записано: %d", got)
	}
}
