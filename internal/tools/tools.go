package tools

import (
	"encoding/json"
	"fmt"
	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
	"strconv"
	"strings"
	"time"
)

// Tool is one callable function exposed to the model.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Run         func(args json.RawMessage) (string, error)
	// Visible, when set, gates the schema: the tool is offered to the model
	// only while it returns true. Execute stays available regardless — a
	// call for a hidden tool is still answered with an error text instead
	// of a provider-side "unknown tool".
	Visible func() bool
}

// ToolRegistry maps names to tools and builds their JSON schemas.
type ToolRegistry struct {
	tools     map[string]*Tool
	order     []string
	maxResult int
	task      func() *task.Machine // current session's task machine (day 13)
}

// NewToolRegistry builds the agent's tool set. tasks is the in-memory list from
// tasks.txt, exposed through find_riddle.
func NewToolRegistry(tasks []config.Task) *ToolRegistry {
	r := &ToolRegistry{tools: map[string]*Tool{}, maxResult: 8000}

	r.add(&Tool{
		Name:        "now",
		Description: "Возвращает текущую дату и время (локальное и UTC). Аргументов не требует.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		Run: runNow,
	})

	r.add(&Tool{
		Name:        "calc",
		Description: "Вычисляет арифметическое выражение с + - * / и скобками. Используй для любых вычислений, не считай в уме.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expr": map[string]any{
					"type":        "string",
					"description": "Выражение, например (17*23)+5",
				},
			},
			"required":             []string{"expr"},
			"additionalProperties": false,
		},
		Run: runCalc,
	})

	r.add(&Tool{
		Name:        "find_riddle",
		Description: "Ищет загадку в списке загадок по ключевым словам. В списке только загадки и головоломки — это не задачи пользователя, не дела и не поручения. Возвращает номер, название и ответ. Пустой query возвращает весь список загадок.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Ключевые слова, например улитка или поезд; пусто — показать все загадки",
				},
			},
			"additionalProperties": false,
		},
		Run: runFindTask(tasks),
	})

	// The day-13 tool: the model requests a stage change, the code validates
	// the transition before applying it. The machine is per-session, so the
	// registry keeps a provider, not a pointer: /new and /session swap the
	// machine under the same closure.
	r.add(&Tool{
		Name: "task_transition",
		Description: "Переводит задачу на следующий этап конечного автомата " +
			"(planning → execution → validation → done). Переход проверяется кодом: " +
			"запрещённый, из паузы или без утверждённого плана будет отклонён. " +
			"Переходи на следующий этап только когда текущий фактически выполнен. " +
			"В validation не переходи, пока результаты шага не зафиксированы в ответе: " +
			"если данных не хватает, перечисли их и дождись ответа пользователя.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "string",
					"enum":        []string{"planning", "execution", "validation", "done"},
					"description": "Целевой этап",
				},
				"step": map[string]any{
					"type":        "string",
					"description": "Новый текущий шаг (что именно делается на новом этапе)",
				},
				"expected": map[string]any{
					"type":        "string",
					"description": "Ожидаемое действие на новом этапе",
				},
			},
			"required":             []string{"to"},
			"additionalProperties": false,
		},
		Run: r.runTaskTransition,
		// Ленивый инструмент: пока задача не начата (/task start), схема не
		// уходит в запрос — экономим ~260 токенов на каждом ходу обычного
		// диалога. После старта схема появляется, и модель ведёт задачу.
		Visible: func() bool {
			return r.task != nil && r.task() != nil && r.task().Started()
		},
	})

	return r
}

// SetTaskProvider attaches the source of the current session's task machine.
func (r *ToolRegistry) SetTaskProvider(p func() *task.Machine) { r.task = p }

// runTaskTransition applies the requested stage change through the machine;
// a forbidden transition comes back as an error, so the model sees the
// rejection and the state stays untouched.
func (r *ToolRegistry) runTaskTransition(args json.RawMessage) (string, error) {
	if r.task == nil || r.task() == nil {
		return "", fmt.Errorf("задача не начата; попросите пользователя запустить её: /task start")
	}
	var a struct {
		To       string `json:"to"`
		Step     string `json:"step"`
		Expected string `json:"expected"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("аргументы: %w", err)
	}
	to, err := task.ParseStage(a.To)
	if err != nil {
		return "", err
	}
	m := r.task()
	before := m.State().Stage
	if err := m.Transition(to, a.Step, a.Expected, "assistant"); err != nil {
		return "", err
	}
	st := m.State()
	return fmt.Sprintf("переход применён: %s → %s; шаг: %s; ожидаемое действие: %s",
		before, st.Stage, orDash(st.Step), orDash(st.Expected)), nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func (r *ToolRegistry) add(t *Tool) {
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
}

// Schemas returns the tool schemas for the API request. Tools with a Visible
// gate that currently evaluates false are left out: the model pays tokens
// only for the tools it can actually use right now.
func (r *ToolRegistry) Schemas() []llm.ToolSchema {
	out := make([]llm.ToolSchema, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		if t.Visible != nil && !t.Visible() {
			continue
		}
		out = append(out, llm.ToolSchema{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

// Names returns tool names in registration order.
func (r *ToolRegistry) Names() []string {
	return append([]string(nil), r.order...)
}

// Describe returns "name — description" lines for /tools.
func (r *ToolRegistry) Describe() []string {
	out := make([]string, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		out = append(out, fmt.Sprintf("%s — %s", t.Name, t.Description))
	}
	return out
}

// Execute runs a tool call and always returns a string for the model: errors
// and panics become text so the model can correct itself instead of crashing
// the loop.
func (r *ToolRegistry) Execute(call llm.ToolCall) (out string) {
	name := call.Function.Name
	t, ok := r.tools[name]
	if !ok {
		return fmt.Sprintf("ошибка: неизвестный инструмент %q; доступны: %s", name, strings.Join(r.order, ", "))
	}

	args := json.RawMessage(call.Function.Arguments)
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage("{}")
	}

	defer func() {
		if rec := recover(); rec != nil {
			out = fmt.Sprintf("ошибка инструмента %s: %v", name, rec)
		}
	}()

	res, err := t.Run(args)
	if err != nil {
		return fmt.Sprintf("ошибка инструмента %s: %v", name, err)
	}
	return config.Truncate(res, r.maxResult)
}

func runNow(json.RawMessage) (string, error) {
	n := time.Now()
	return fmt.Sprintf("локальное: %s\nUTC: %s",
		n.Format("2006-01-02 15:04:05 -07:00"),
		n.UTC().Format(time.RFC3339)), nil
}

func runCalc(raw json.RawMessage) (string, error) {
	var a struct {
		Expr string `json:"expr"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("аргумент expr: %v", err)
	}
	if strings.TrimSpace(a.Expr) == "" {
		return "", fmt.Errorf("пустое выражение")
	}
	v, err := evalExpr(a.Expr)
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(v, 'f', -1, 64), nil
}

func runFindTask(tasks []config.Task) func(json.RawMessage) (string, error) {
	return func(raw json.RawMessage) (string, error) {
		var a struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("аргумент query: %v", err)
		}
		q := strings.ToLower(strings.TrimSpace(a.Query))
		if q == "" {
			return listTasks(tasks), nil
		}

		hits := matchTasks(tasks, q)
		if len(hits) == 0 {
			// Try individual words as a looser fallback.
			for _, w := range strings.Fields(q) {
				if len([]rune(w)) < 3 {
					continue
				}
				if found := matchTasks(tasks, w); len(found) > 0 {
					hits = append(hits, found...)
				}
			}
		}
		if len(hits) == 0 {
			return fmt.Sprintf("ничего не найдено по запросу «%s»", strings.TrimSpace(a.Query)), nil
		}

		var b strings.Builder
		seen := map[int]bool{}
		shown := 0
		for _, t := range hits {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			if shown == 5 {
				b.WriteString("…есть ещё совпадения, уточни запрос\n")
				break
			}
			fmt.Fprintf(&b, "%d. %s — ответ %s\n", t.ID, t.Label, config.FormatAnswer(t.Answer))
			shown++
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}
}

// listTasks renders the whole task list, one line per task.
func listTasks(tasks []config.Task) string {
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "%d. %s — ответ %s\n", t.ID, t.Label, config.FormatAnswer(t.Answer))
	}
	return strings.TrimRight(b.String(), "\n")
}

func matchTasks(tasks []config.Task, q string) []config.Task {
	var out []config.Task
	for _, t := range tasks {
		hay := strings.ToLower(t.Label + " " + t.Condition)
		if strings.Contains(hay, q) {
			out = append(out, t)
		}
	}
	return out
}

// calcParser is a tiny recursive-descent evaluator. It deliberately avoids
// eval-like constructs: tool input is untrusted model output.
type calcParser struct {
	s   string
	pos int
}

func evalExpr(s string) (float64, error) {
	p := &calcParser{s: strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), ",", ".")}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	if p.pos != len(p.s) {
		return 0, fmt.Errorf("неожиданный символ %q в позиции %d", string(p.s[p.pos]), p.pos)
	}
	return v, nil
}

func (p *calcParser) expr() (float64, error) {
	v, err := p.term()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
		op := p.s[p.pos]
		p.pos++
		rhs, err := p.term()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			v += rhs
		} else {
			v -= rhs
		}
	}
	return v, nil
}

func (p *calcParser) term() (float64, error) {
	v, err := p.factor()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.s) && (p.s[p.pos] == '*' || p.s[p.pos] == '/') {
		op := p.s[p.pos]
		p.pos++
		rhs, err := p.factor()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			v *= rhs
		} else {
			if rhs == 0 {
				return 0, fmt.Errorf("деление на ноль")
			}
			v /= rhs
		}
	}
	return v, nil
}

func (p *calcParser) factor() (float64, error) {
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("неожиданный конец выражения")
	}
	c := p.s[p.pos]
	switch {
	case c == '+':
		p.pos++
		return p.factor()
	case c == '-':
		p.pos++
		v, err := p.factor()
		return -v, err
	case c == '(':
		p.pos++
		v, err := p.expr()
		if err != nil {
			return 0, err
		}
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return 0, fmt.Errorf("нет закрывающей скобки")
		}
		p.pos++
		return v, nil
	case c == '.' || (c >= '0' && c <= '9'):
		start := p.pos
		for p.pos < len(p.s) && ((p.s[p.pos] >= '0' && p.s[p.pos] <= '9') || p.s[p.pos] == '.') {
			p.pos++
		}
		f, err := strconv.ParseFloat(p.s[start:p.pos], 64)
		if err != nil {
			return 0, fmt.Errorf("не число %q", p.s[start:p.pos])
		}
		return f, nil
	default:
		return 0, fmt.Errorf("неожиданный символ %q", string(c))
	}
}
