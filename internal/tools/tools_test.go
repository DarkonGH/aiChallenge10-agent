package tools

import (
	"encoding/json"
	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
	"strings"
	"testing"
)

// call builds a tool call for tests.
func call(name, args string) llm.ToolCall {
	var tc llm.ToolCall
	tc.ID = "call_1"
	tc.Type = "function"
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}

func TestEvalExpr(t *testing.T) {
	ok := []struct {
		in   string
		want float64
	}{
		{"17*23", 391},
		{"(2+3)*4", 20},
		{"-3+5", 2},
		{"10/4", 2.5},
		{"2*(3+4)-1", 13},
		{"7,5*2", 15},
	}
	for _, c := range ok {
		got, err := evalExpr(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "1/0", "2+", "(1+2", "abc", "2**3"} {
		if _, err := evalExpr(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestRegistryExecute(t *testing.T) {
	r := NewToolRegistry([]config.Task{
		{ID: 8, Label: "улитка на столбе", Condition: "Улитка ползёт вверх по столбу", Answer: 28},
	})

	if out := r.Execute(call("calc", `{"expr":"17*23"}`)); out != "391" {
		t.Errorf("calc = %q, want 391", out)
	}
	if out := r.Execute(call("calc", `{"expr":"1/0"}`)); !strings.Contains(out, "деление на ноль") {
		t.Errorf("calc error = %q", out)
	}
	if out := r.Execute(call("find_riddle", `{"query":"улитка"}`)); !strings.Contains(out, "улитка на столбе") || !strings.Contains(out, "28") {
		t.Errorf("find_riddle = %q", out)
	}
	if out := r.Execute(call("find_riddle", `{"query":"дракон"}`)); !strings.Contains(out, "ничего не найдено") {
		t.Errorf("find_riddle miss = %q", out)
	}
	if out := r.Execute(call("find_riddle", `{}`)); !strings.Contains(out, "8. улитка на столбе") {
		t.Errorf("find_riddle empty query should list all = %q", out)
	}
	if out := r.Execute(call("nope", `{}`)); !strings.Contains(out, "неизвестный инструмент") {
		t.Errorf("unknown tool = %q", out)
	}
	if out := r.Execute(call("calc", `not json`)); !strings.Contains(out, "ошибка") {
		t.Errorf("bad args = %q", out)
	}
	if out := r.Execute(call("now", `{}`)); !strings.Contains(out, "UTC") {
		t.Errorf("now = %q", out)
	}
}

func TestToolSchemas(t *testing.T) {
	r := NewToolRegistry(nil)
	// task_transition is lazy: without a started task its schema is not
	// offered, so a plain chat pays tokens only for the three base tools.
	if got := len(r.Schemas()); got != 3 {
		t.Fatalf("got %d schemas without a task, want 3", got)
	}
	// Once a task is started the schema joins the request.
	var machine *task.Machine
	r.SetTaskProvider(func() *task.Machine { return machine })
	var err error
	machine, err = task.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(r.Schemas()); got != 3 {
		t.Fatalf("got %d schemas with an unloaded task, want 3", got)
	}
	if err := machine.Start("проверка", "схема видна", "test"); err != nil {
		t.Fatal(err)
	}
	schemas := r.Schemas()
	if len(schemas) != 4 {
		t.Fatalf("got %d schemas with a started task, want 4", len(schemas))
	}
	names := map[string]bool{}
	for _, s := range schemas {
		names[s.Function.Name] = true
		if s.Type != "function" || s.Function.Name == "" {
			t.Errorf("bad schema: %+v", s)
		}
		b, err := json.Marshal(s.Function.Parameters)
		if err != nil {
			t.Fatalf("schema parameters: %v", err)
		}
		if !json.Valid(b) {
			t.Errorf("invalid parameters JSON: %s", b)
		}
	}
	if !names["task_transition"] {
		t.Errorf("started task must expose task_transition, got %v", names)
	}
}

// TestTaskTransitionTool checks the day-13 tool through Execute: a legal
// transition applies, a forbidden one comes back as an error text and the
// state stays untouched, the plan gate rejects implementation without the
// user's approval, and without a machine the tool asks for /task start.
func TestTaskTransitionTool(t *testing.T) {
	r := NewToolRegistry(nil)

	// No machine attached: actionable error, not a crash.
	out := r.Execute(call("task_transition", `{"to":"execution"}`))
	if !strings.Contains(out, "/task start") {
		t.Errorf("без автомата: %q", out)
	}

	var machine *task.Machine
	r.SetTaskProvider(func() *task.Machine { return machine })

	var err error
	machine, err = task.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := machine.Start("сбор требований", "план", "user"); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Forbidden jump: planning → done.
	out = r.Execute(call("task_transition", `{"to":"done"}`))
	if !strings.Contains(out, "запрещён") {
		t.Errorf("planning → done должен быть отклонён: %q", out)
	}
	if machine.State().Stage != task.StagePlanning {
		t.Fatalf("этап изменился при запрещённом переходе: %s", machine.State().Stage)
	}

	// The gate: no implementation before an approved plan.
	out = r.Execute(call("task_transition", `{"to":"execution","step":"писать код"}`))
	if !strings.Contains(out, "план не утверждён") {
		t.Errorf("гейт плана не сработал: %q", out)
	}

	// After the user approves, the same request applies.
	if err := machine.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	out = r.Execute(call("task_transition", `{"to":"execution","step":"писать код","expected":"коммит"}`))
	if !strings.Contains(out, "переход применён: planning → execution") {
		t.Errorf("легальный переход отклонён: %q", out)
	}
	if machine.State().Stage != task.StageExecution || machine.State().Step != "писать код" {
		t.Fatalf("состояние после перехода: %+v", machine.State())
	}

	// Unknown stage names are rejected with the list of valid ones.
	out = r.Execute(call("task_transition", `{"to":"deploy"}`))
	if !strings.Contains(out, "неизвестный этап") || !strings.Contains(out, "validation") {
		t.Errorf("неизвестный этап: %q", out)
	}
}
