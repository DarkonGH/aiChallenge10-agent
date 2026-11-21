package main

import (
	"path/filepath"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/agent"
	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
)

// TestTaskStateDoneClearsWorking covers the command path: /task state done
// must run the same day-15 cleanup hook as the model's task_transition tool.
func TestTaskStateDoneClearsWorking(t *testing.T) {
	dir := t.TempDir()
	h, err := history.OpenHistory(dir, "test", "chat", "summary", "")
	if err != nil {
		t.Fatalf("OpenHistory: %v", err)
	}
	defer h.Close()

	mem, err := memory.Open(filepath.Join(dir, "test", "memory"), filepath.Join(dir, "shared"))
	if err != nil {
		t.Fatalf("memory.Open: %v", err)
	}
	if err := mem.Add(memory.LayerWorking, "срок", "15 декабря"); err != nil {
		t.Fatalf("memory.Add: %v", err)
	}
	if err := mem.Save(); err != nil {
		t.Fatalf("memory.Save: %v", err)
	}

	m, err := task.Load(filepath.Join(dir, "test"))
	if err != nil {
		t.Fatalf("task.Load: %v", err)
	}
	if err := m.Start("предложить план", "", "user"); err != nil {
		t.Fatalf("task.Start: %v", err)
	}
	if err := m.Approve(); err != nil {
		t.Fatalf("task.Approve: %v", err)
	}
	for _, stage := range []task.Stage{task.StageExecution, task.StageValidation} {
		if err := m.Transition(stage, "", "", "user"); err != nil {
			t.Fatalf("task.Transition(%s): %v", stage, err)
		}
	}

	ag := agent.NewAgent(nil, nil, h, compact.Budget{}, 0, 0, 0)
	ag.SetMemory(mem)
	a := &app{hist: h, mem: mem, task: m, agent: ag}

	if err := a.taskCommand("state done"); err != nil {
		t.Fatalf("taskCommand: %v", err)
	}
	if got := mem.Len(memory.LayerWorking); got != 0 {
		t.Fatalf("после /task state done в working осталось %d записей", got)
	}
	changes := h.MemoryChanges()
	if len(changes) != 1 || changes[0].Kind != "remove" || changes[0].Old != "15 декабря" {
		t.Fatalf("журнал очистки: %+v", changes)
	}
}
