package task

import (
	"strings"
	"testing"
)

func newMachine(t *testing.T) *Machine {
	t.Helper()
	m, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m
}

// TestTransitionsMatrix checks the day-13 rule: only the table's transitions
// pass, every other pair is rejected in code with the stage unchanged.
func TestTransitionsMatrix(t *testing.T) {
	// The first leg of each chain needs the plan approved; approve up front so
	// the matrix tests the table, not the gate (the gate has its own test).
	approved := map[Stage]bool{StagePlanning: true}
	tests := []struct {
		path []Stage // stages to walk through before the final transition
		to   Stage
		ok   bool
	}{
		{[]Stage{StagePlanning}, StagePlanning, false}, // self-transition
		{[]Stage{StagePlanning}, StageValidation, false},
		{[]Stage{StagePlanning}, StageDone, false},
		{[]Stage{StagePlanning}, StageExecution, true},
		{[]Stage{StagePlanning, StageExecution}, StageDone, false},
		{[]Stage{StagePlanning, StageExecution}, StagePlanning, false},
		{[]Stage{StagePlanning, StageExecution}, StageValidation, true},
		{[]Stage{StagePlanning, StageExecution, StageValidation}, StageDone, true},
		{[]Stage{StagePlanning, StageExecution, StageValidation}, StageExecution, true},
		{[]Stage{StagePlanning, StageExecution, StageValidation}, StagePlanning, false},
		{[]Stage{StagePlanning, StageExecution, StageValidation, StageDone}, StageExecution, false},
		{[]Stage{StagePlanning, StageExecution, StageValidation, StageDone}, StageValidation, false},
	}
	for _, tt := range tests {
		m := newMachine(t)
		if err := m.Start("шаг", "ожидание", "user"); err != nil {
			t.Fatalf("Start: %v", err)
		}
		// The gate is tested separately; here the plan is approved so the
		// matrix exercises the table itself.
		m.state.Approved = approved[tt.path[0]]
		for _, st := range tt.path[1:] {
			if err := m.Transition(st, "", "", "user"); err != nil {
				t.Fatalf("переход на %s: %v", st, err)
			}
		}
		err := m.Transition(tt.to, "", "", "user")
		if tt.ok && err != nil {
			t.Errorf("%s → %s: ожидали успех, получили %v", tt.path[len(tt.path)-1], tt.to, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("%s → %s: ожидали отказ, переход применился", tt.path[len(tt.path)-1], tt.to)
		}
		if !tt.ok && err != nil && !strings.Contains(err.Error(), "запрещён") {
			t.Errorf("%s → %s: отказ без слова «запрещён»: %v", tt.path[len(tt.path)-1], tt.to, err)
		}
	}
}

// TestPlanningNeedsApproval checks the gate: no implementation before an
// approved plan, and the model cannot approve the plan itself.
func TestPlanningNeedsApproval(t *testing.T) {
	m := newMachine(t)
	if err := m.Start("собрать требования", "предложить план", "user"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := m.Transition(StageExecution, "", "", "assistant")
	if err == nil || !strings.Contains(err.Error(), "план не утверждён") {
		t.Fatalf("ожидали отказ «план не утверждён», получили: %v", err)
	}
	if m.State().Stage != StagePlanning {
		t.Fatalf("этап изменился без утверждённого плана: %s", m.State().Stage)
	}
	if err := m.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := m.Transition(StageExecution, "реализация", "писать код", "assistant"); err != nil {
		t.Fatalf("после approve переход отклонён: %v", err)
	}
	if m.State().Stage != StageExecution {
		t.Fatalf("этап не execution: %s", m.State().Stage)
	}
}

// TestPauseBlocksTransitions checks that a pause freezes the machine on any
// stage and resume returns exactly to the saved stage and step.
func TestPauseBlocksTransitions(t *testing.T) {
	m := newMachine(t)
	if err := m.Start("сбор требований", "план", "user"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := m.Transition(StageExecution, "писать код", "коммит", "user"); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := m.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := m.Transition(StageValidation, "", "", "assistant"); err == nil || !strings.Contains(err.Error(), "на паузе") {
		t.Fatalf("переход из паузы не заблокирован: %v", err)
	}
	if m.State().Stage != StageExecution || !m.State().Paused {
		t.Fatalf("состояние после паузы сломано: %+v", m.State())
	}
	if err := m.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	st := m.State()
	if st.Stage != StageExecution || st.Paused || st.Step != "писать код" {
		t.Fatalf("после resume не тот этап/шаг: %+v", st)
	}
	if err := m.Transition(StageValidation, "", "", "assistant"); err != nil {
		t.Fatalf("переход после resume отклонён: %v", err)
	}
}

// TestPauseOnEveryStage checks the assignment's "pause at any stage": the
// machine freezes and unfreezes on planning, execution and validation alike.
func TestPauseOnEveryStage(t *testing.T) {
	path := []Stage{StagePlanning, StageExecution, StageValidation}
	for i, target := range path {
		m := newMachine(t)
		if err := m.Start("", "", "user"); err != nil {
			t.Fatalf("Start: %v", err)
		}
		m.state.Approved = true
		for _, st := range path[1 : i+1] {
			if err := m.Transition(st, "", "", "user"); err != nil {
				t.Fatalf("переход на %s: %v", st, err)
			}
		}
		if err := m.Pause(); err != nil {
			t.Fatalf("пауза на %s: %v", target, err)
		}
		if err := m.Resume(); err != nil {
			t.Fatalf("resume на %s: %v", target, err)
		}
		if m.State().Stage != target {
			t.Fatalf("после паузы на %s этап %s", target, m.State().Stage)
		}
	}
}

// TestPersistence checks that the machine survives a restart: the stage, the
// step, the approval and the pause flag all reload from task.json — the
// "continue without re-explaining" requirement.
func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.Started() {
		t.Fatal("пустой каталог не должен содержать задачу")
	}
	if err := m.Start("шаг 1", "ожидание", "user"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := m.Transition(StageExecution, "шаг 2", "ожидание 2", "assistant"); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := m.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("перезагрузка: %v", err)
	}
	st := reloaded.State()
	if st.Stage != StageExecution || !st.Paused || !st.Approved || st.Step != "шаг 2" {
		t.Fatalf("состояние не пережило перезапуск: %+v", st)
	}
	if len(st.History) != 2 {
		t.Fatalf("история переходов потеряна: %+v", st.History)
	}
	if st.History[1].From != StagePlanning || st.History[1].To != StageExecution || st.History[1].Who != "assistant" {
		t.Fatalf("запись истории некорректна: %+v", st.History[1])
	}
}

// TestResetClears checks that reset leaves no task behind and the machine can
// start over.
func TestResetClears(t *testing.T) {
	m := newMachine(t)
	if err := m.Start("", "", "user"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if m.Started() {
		t.Fatalf("после reset задача осталась: %+v", m.State())
	}
	if err := m.Start("заново", "", "user"); err != nil {
		t.Fatalf("Start после reset: %v", err)
	}
	if m.State().Stage != StagePlanning {
		t.Fatalf("перезапуск не в planning: %s", m.State().Stage)
	}
}

// TestStartTwiceFails checks that a running task cannot be started again.
func TestStartTwiceFails(t *testing.T) {
	m := newMachine(t)
	if err := m.Start("", "", "user"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Start("", "", "user"); err == nil {
		t.Fatal("повторный start прошёл")
	}
}

// TestBlock tests the prompt block: absent without a task, and complete with
// one.
func TestBlock(t *testing.T) {
	m := newMachine(t)
	if b := m.Block(); b != "" {
		t.Fatalf("без задачи блок должен быть пуст, получили: %q", b)
	}
	if err := m.Start("шаг", "ждём", "user"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b := m.Block()
	for _, want := range []string{"planning", "шаг", "ждём", "task_transition", "execution"} {
		if !strings.Contains(b, want) {
			t.Errorf("в блоке нет %q:\n%s", want, b)
		}
	}
}

// TestParseStage checks stage-name normalization and rejection.
func TestParseStage(t *testing.T) {
	if st, err := ParseStage(" Execution "); err != nil || st != StageExecution {
		t.Fatalf("ParseStage: %v %v", st, err)
	}
	if _, err := ParseStage("deploy"); err == nil {
		t.Fatal("неизвестный этап принят")
	}
}
