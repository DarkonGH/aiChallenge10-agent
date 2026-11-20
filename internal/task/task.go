// Package task implements the day-13 assignment: the state of the current
// task as a finite state machine (planning → execution → validation → done).
//
// The design rule of the day: transitions are checked by code before they are
// applied, not just described in the prompt. The model requests a stage change
// through the task_transition tool; this package validates the request against
// the transition table and either applies it or returns an error that reaches
// the model as a tool failure. Two gates are enforced in code:
//
//   - no implementation before an approved plan: planning → execution requires
//     the plan_approved flag, which only the user can set (/task approve — the
//     tool interface has no approve action on purpose);
//   - a paused task admits no transitions; /task resume returns exactly to the
//     saved stage and step, so continuation needs no re-explaining.
package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Stage is one stage of the task lifecycle.
type Stage string

const (
	StagePlanning   Stage = "planning"
	StageExecution  Stage = "execution"
	StageValidation Stage = "validation"
	StageDone       Stage = "done"
)

// Stages lists all stages in lifecycle order: for help texts and validation
// messages.
var Stages = []Stage{StagePlanning, StageExecution, StageValidation, StageDone}

// transitions is the machine's transition table — the single source of truth
// for what is allowed. Everything not listed here is rejected in code.
var transitions = map[Stage][]Stage{
	StagePlanning:   {StageExecution},
	StageExecution:  {StageValidation},
	StageValidation: {StageExecution, StageDone}, // rework after a failed check
	StageDone:       {},                          // terminal stage: no exits
}

// ParseStage normalizes model or user input into a Stage.
func ParseStage(s string) (Stage, error) {
	st := Stage(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range Stages {
		if st == known {
			return st, nil
		}
	}
	return "", fmt.Errorf("неизвестный этап %q; допустимы: %s", s, stageNames())
}

func stageNames() string {
	names := make([]string, 0, len(Stages))
	for _, s := range Stages {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}

// Transition records one applied stage change (or the task start) in history.
type Transition struct {
	From Stage  `json:"from"` // empty for the start
	To   Stage  `json:"to"`
	Who  string `json:"who"` // "user" or "assistant"
	At   string `json:"at"`  // RFC3339
}

// State is the persisted machine state: task.json in the session directory,
// one file for the whole lifecycle (as with working.json / long.json).
type State struct {
	Stage    Stage        `json:"stage,omitempty"`
	Step     string       `json:"step,omitempty"`     // current step
	Expected string       `json:"expected,omitempty"` // expected action
	Approved bool         `json:"plan_approved,omitempty"`
	Paused   bool         `json:"paused,omitempty"`
	History  []Transition `json:"history,omitempty"`
}

// Machine is the task state machine of the current session.
type Machine struct {
	path  string
	state State
}

// Load reads task.json from the session directory. A missing or empty file is
// a valid starting point: no task yet, like in memory.Open.
func Load(sessionDir string) (*Machine, error) {
	m := &Machine{path: filepath.Join(sessionDir, "task.json")}
	b, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("задача: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m.state); err != nil {
		return nil, fmt.Errorf("задача: task.json: %w", err)
	}
	// A hand-edited file may name an unknown stage; refuse rather than guess.
	if m.state.Stage != "" {
		if _, err := ParseStage(string(m.state.Stage)); err != nil {
			return nil, fmt.Errorf("задача: task.json: %v", err)
		}
	}
	return m, nil
}

// Started reports whether a task exists.
func (m *Machine) Started() bool { return m.state.Stage != "" }

// State returns a copy of the current machine state.
func (m *Machine) State() State { return m.state }

// Start creates the task in the planning stage.
func (m *Machine) Start(step, expected, who string) error {
	if m.Started() {
		return fmt.Errorf("задача уже идёт (этап %s); сбросьте её через /task reset", m.state.Stage)
	}
	m.state = State{Stage: StagePlanning, Step: step, Expected: expected}
	m.state.History = append(m.state.History, Transition{To: StagePlanning, Who: who, At: now()})
	return m.save()
}

// Transition moves the machine from the current stage to `to`. The check
// happens here, in code, before anything is applied: a forbidden transition
// returns an error and the state does not change.
func (m *Machine) Transition(to Stage, step, expected, who string) error {
	if !m.Started() {
		return fmt.Errorf("задача не начата; сначала запустите её: /task start")
	}
	if m.state.Paused {
		return fmt.Errorf("задача на паузе; переходы запрещены до /task resume")
	}
	from := m.state.Stage
	if !allowed(from, to) {
		if from == StageDone {
			return fmt.Errorf("переход запрещён: задача завершена (этап done), обратных переходов нет")
		}
		return fmt.Errorf("переход %s → %s запрещён; допустимо из %s: %s",
			from, to, from, orNone(transitions[from]))
	}
	// The plan gate: implementation is only possible after the user approved
	// the plan. The model cannot approve it for itself.
	if from == StagePlanning && to == StageExecution && !m.state.Approved {
		return fmt.Errorf("план не утверждён; переход planning → execution возможен только после /task approve (утверждает пользователь, не ассистент)")
	}
	m.state.Stage = to
	if step != "" {
		m.state.Step = step
	}
	if expected != "" {
		m.state.Expected = expected
	}
	m.state.History = append(m.state.History, Transition{From: from, To: to, Who: who, At: now()})
	return m.save()
}

// SetStep updates the current step or the expected action without changing the
// stage. Allowed in any stage, including a pause: it is a note, not a
// transition.
func (m *Machine) SetStep(step, expected string) error {
	if !m.Started() {
		return fmt.Errorf("задача не начата; сначала запустите её: /task start")
	}
	if step != "" {
		m.state.Step = step
	}
	if expected != "" {
		m.state.Expected = expected
	}
	return m.save()
}

// Pause freezes the task at the current stage. Transitions are blocked while
// paused; resume returns exactly to the saved stage and step.
func (m *Machine) Pause() error {
	if !m.Started() {
		return fmt.Errorf("задача не начата; пауза не к чему")
	}
	if m.state.Paused {
		return fmt.Errorf("задача уже на паузе")
	}
	m.state.Paused = true
	return m.save()
}

// Resume removes the pause flag: the task continues from the same stage and
// step, without repeating any explanation.
func (m *Machine) Resume() error {
	if !m.Started() {
		return fmt.Errorf("задача не начата; продолжать нечего")
	}
	if !m.state.Paused {
		return fmt.Errorf("задача не на паузе")
	}
	m.state.Paused = false
	return m.save()
}

// Approve records the user's plan approval. Only the user can do this — the
// tool interface intentionally exposes no approve action to the model.
func (m *Machine) Approve() error {
	if !m.Started() {
		return fmt.Errorf("задача не начата; утверждать нечего")
	}
	m.state.Approved = true
	return m.save()
}

// Reset clears the machine: no task. The file is kept with an empty state so a
// follow-up Start rewrites it in place.
func (m *Machine) Reset() error {
	m.state = State{}
	return m.save()
}

// Next lists the stages the machine may legally move to from the current one.
func (m *Machine) Next() []Stage {
	if !m.Started() || m.state.Paused {
		return nil
	}
	return transitions[m.state.Stage]
}

// Block renders the system-prompt section about the task state. Empty when no
// task is started: without a task the model sees no state machinery at all.
// The table here is informational — enforcement lives in Transition.
func (m *Machine) Block() string {
	if !m.Started() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Состояние задачи (конечный автомат; этап меняется только через инструмент task_transition, переходы проверяет код):\n")
	fmt.Fprintf(&b, "- этап: %s\n", m.state.Stage)
	if m.state.Step != "" {
		fmt.Fprintf(&b, "- текущий шаг: %s\n", m.state.Step)
	}
	if m.state.Expected != "" {
		fmt.Fprintf(&b, "- ожидаемое действие: %s\n", m.state.Expected)
	}
	fmt.Fprintf(&b, "- план утверждён: %s\n", yesNo(m.state.Approved))
	// Статус паузы печатается всегда: после /task resume модель должна видеть
	// явное «нет», а не тащить из своего прошлого ответа устаревшую причину
	// «пауза активна» и отказываться от перехода.
	if m.state.Paused {
		b.WriteString("- пауза: да (переходы запрещены до /task resume от пользователя)\n")
	} else {
		b.WriteString("- пауза: нет\n")
	}
	fmt.Fprintf(&b, "- допустимые переходы: %s\n", orNone(m.Next()))
	// The check gate the code cannot verify: whether the work of the stage was
	// actually done. The rule is explicit so the model does not rubber-stamp a
	// transition the user asked for while the data is still missing.
	b.WriteString("- правило проверки: в validation переходи только когда результаты шага зафиксированы " +
		"в ответе (что сделано, с какими данными и числами); если данных не хватает — не переходи, " +
		"перечисли, чего не хватает, и дождись ответа пользователя\n")
	return strings.TrimRight(b.String(), "\n")
}

// allowed consults the transition table.
func allowed(from, to Stage) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// save writes the state atomically: a crash never leaves a half-written file.
func (m *Machine) save() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("задача: %w", err)
	}
	b, err := json.MarshalIndent(&m.state, "", "  ")
	if err != nil {
		return fmt.Errorf("задача: %w", err)
	}
	b = append(b, '\n')
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("задача: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("задача: %w", err)
	}
	return nil
}

func now() string { return time.Now().Format(time.RFC3339) }

func yesNo(v bool) string {
	if v {
		return "да"
	}
	return "нет"
}

func orNone(stages []Stage) string {
	if len(stages) == 0 {
		return "нет"
	}
	names := make([]string, 0, len(stages))
	for _, s := range stages {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}
