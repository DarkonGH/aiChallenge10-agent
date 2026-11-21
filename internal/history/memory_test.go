package history

import (
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

// TestMemoryChangesPersistAndPrint covers the day-15 journal: the event keeps
// both the rebuildable diff and the old → new trace, and reopening the session
// restores the trace for /memory history and -print-session.
func TestMemoryChangesPersistAndPrint(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir, "s-mem", "chat", "sum", "sys")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	upd := memory.Update{Working: []memory.Entry{{Key: "срок", Value: "20 декабря"}}}
	changes := []memory.Change{
		{Layer: memory.LayerWorking, Key: "срок", Kind: "update",
			Old: "15 декабря", New: "20 декабря", At: "2026-11-21T18:00:00+03:00"},
	}
	if err := h.AppendMemoryChanges(upd, changes); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	re, err := OpenHistory(dir, "s-mem", "", "", "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := re.MemoryChanges()
	if len(got) != 1 || got[0].Old != "15 декабря" || got[0].New != "20 декабря" {
		t.Fatalf("журнал после перезагрузки: %+v", got)
	}
	re.Close()

	var b strings.Builder
	if err := PrintSessionLog(dir, "s-mem", &b); err != nil {
		t.Fatalf("print: %v", err)
	}
	if !strings.Contains(b.String(), "15 декабря → 20 декабря") {
		t.Errorf("в -print-session нет замены:\n%s", b.String())
	}
}
