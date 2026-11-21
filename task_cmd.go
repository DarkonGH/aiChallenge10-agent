package main

import (
	"fmt"
	"strings"

	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
)

// taskCommand handles /task: the user-side control surface of the day-13
// machine. The model can only request stage changes through the
// task_transition tool (validated in internal/task); approval, pause, resume
// and reset belong to the user.
//
//	/task                    — current state and transition history
//	/task start [шаг]        — create the task in planning
//	/task approve            — approve the plan (opens planning → execution)
//	/task pause              — freeze at the current stage
//	/task resume             — continue from the same stage and step
//	/task state <этап> [шаг] — manual transition, validated like the tool's
//	/task reset              — clear the task
func (a *app) taskCommand(arg string) error {
	fields := strings.Fields(arg)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}
	rest := ""
	if len(fields) > 1 {
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(arg), fields[0]))
	}

	m := a.task
	if m == nil {
		return fmt.Errorf("автомат задачи недоступен")
	}

	switch sub {
	case "":
		a.printTask()
		return nil

	case "start":
		if err := m.Start(rest, "предложить план", "user"); err != nil {
			return err
		}
		fmt.Println("задача создана: этап planning")
		a.printTask()
		return nil

	case "approve":
		if err := m.Approve(); err != nil {
			return err
		}
		fmt.Println("план утверждён; переход planning → execution открыт")
		return nil

	case "pause":
		if err := m.Pause(); err != nil {
			return err
		}
		fmt.Printf("пауза: задача заморожена на этапе %s; /task resume продолжит с того же места\n", m.State().Stage)
		return nil

	case "resume":
		if err := m.Resume(); err != nil {
			return err
		}
		fmt.Printf("продолжаем: этап %s, шаг «%s»\n", m.State().Stage, m.State().Step)
		return nil

	case "state":
		parts := strings.Fields(rest)
		if len(parts) == 0 {
			return fmt.Errorf("нужно: /task state <этап> [шаг]; этапы: %s", taskStages())
		}
		to, err := task.ParseStage(parts[0])
		if err != nil {
			return err
		}
		step := strings.TrimSpace(strings.TrimPrefix(rest, parts[0]))
		before := m.State().Stage
		if err := m.Transition(to, step, "", "user"); err != nil {
			return err
		}
		// The tool path calls the same hook from ToolRegistry; a manual
		// /task state done must not leave the finished task's working layer
		// behind.
		if a.agent != nil && m.State().Stage == task.StageDone && before != task.StageDone {
			a.agent.TaskDone()
		}
		fmt.Printf("переход применён: %s → %s\n", before, to)
		a.printTask()
		return nil

	case "reset":
		if err := m.Reset(); err != nil {
			return err
		}
		fmt.Println("задача сброшена; автомата нет")
		return nil

	default:
		return fmt.Errorf("не знаю подкоманду /task %s; допустимо: start, approve, pause, resume, state, reset", sub)
	}
}

// printTask renders the current machine state and the transition history.
func (a *app) printTask() {
	m := a.task
	if !m.Started() {
		fmt.Println("задачи нет; /task start <шаг> — создать в planning")
		return
	}
	st := m.State()
	flags := []string{}
	if st.Approved {
		flags = append(flags, "план утверждён")
	}
	if st.Paused {
		flags = append(flags, "НА ПАУЗЕ")
	}
	flagStr := ""
	if len(flags) > 0 {
		flagStr = " · " + strings.Join(flags, ", ")
	}
	fmt.Printf("задача: этап %s · шаг «%s» · ожидается «%s»%s\n",
		st.Stage, orDashS(st.Step), orDashS(st.Expected), flagStr)
	fmt.Printf("допустимые переходы: %s\n", orNoneS(m.Next()))
	if len(st.History) > 0 {
		fmt.Println("история:")
		for _, tr := range st.History {
			from := string(tr.From)
			if from == "" {
				from = "—"
			}
			fmt.Printf("  %s  %s → %s (%s)\n", tr.At, from, tr.To, tr.Who)
		}
	}
}

// taskBlockInfo is the /context line for the task block: what the model sees
// and how big it is. Empty when no task is started.
func (a *app) taskBlockInfo() (string, int) {
	if a.task == nil || !a.task.Started() {
		return "", 0
	}
	block := a.task.Block()
	return string(a.task.State().Stage), tokens.EstimateTokens(block)
}

func taskStages() string {
	names := make([]string, 0, len(task.Stages))
	for _, s := range task.Stages {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}

func orDashS(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func orNoneS(stages []task.Stage) string {
	if len(stages) == 0 {
		return "нет"
	}
	names := make([]string, 0, len(stages))
	for _, s := range stages {
		names = append(names, string(s))
	}
	return strings.Join(names, ", ")
}
