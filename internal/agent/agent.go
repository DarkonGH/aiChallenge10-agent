package agent

import (
	"context"
	"fmt"
	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/invariant"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
	"strings"
)

// ApprovalRequest is one MCP tool call waiting for the user's decision.
type ApprovalRequest struct {
	Name   string
	Server string
	Source string
	Args   string
}

// Approver asks the user whether an MCP tool call may run. Returning false
// rejects the call; the model then sees a tool result explaining the refusal.
type Approver func(ApprovalRequest) (bool, error)

// ActionGuard is the optional model check of an intended MCP call. Returning
// false rejects the call before the user approval prompt.
type ActionGuard func(ctx context.Context, userText string, req ApprovalRequest) (bool, string, error)

// Agent owns the whole loop: request → tool call → tool result → answer.
// main only feeds it user text and renders the result.
type Agent struct {
	client             *llm.ChatClient
	tools              *tools.ToolRegistry
	hist               *history.History
	Comp               *compact.Compactor
	Calib              *tokens.Calibrator
	maxSteps           int
	temperature        float64
	maxToolPromptChars int
	trace              func(string)
	// approve is the MCP approval gate. nil means "allow" (tests and
	// non-interactive code paths that do not set it).
	approve Approver
	// actionGuard is the optional model check of an MCP call. nil means off.
	actionGuard ActionGuard

	// Streaming callbacks (nil = plain non-stream requests): onMessageStart
	// before each model request, onContent per streamed delta of the answer,
	// onReasoning per streamed delta of the hidden reasoning, onMessageDone
	// after each assistant message is complete.
	onMessageStart func()
	onContent      func(string)
	onReasoning    func(string)
	onMessageDone  func()

	noCompact        bool // demo mode: skip MaybeCompact, let the prompt overflow
	strategy         compact.Strategy
	window           int // prompt window for the window/facts strategies
	lastAnswerTokens int // completion tokens of the most recent model turn

	// mem is the explicit memory model (day 11). Nil = memory disabled: the
	// prompt carries only the short-term strategy block.
	mem *memory.Store

	// inv is the project invariants (day 14). checkedOK opens the gate for
	// the current turn: only a successful check_solution sets it, and it stays
	// open while the prose layer rejects drafts for wording.
	inv        func() *invariant.Set
	invChecker *invariant.Checker
	checkedOK  bool
}

// maxInvariantRejects bounds how many drafts the gate may reject in one turn
// before the agent gives up with an honest error instead of looping.
const maxInvariantRejects = 3

// NewAgent wires a session, a tool set and a context compactor together.
func NewAgent(client *llm.ChatClient, tools *tools.ToolRegistry, hist *history.History, budget compact.Budget, maxSteps int, temperature float64, maxToolChars int) *Agent {
	calib := tokens.NewCalibrator()
	a := &Agent{
		client:             client,
		tools:              tools,
		hist:               hist,
		Calib:              calib,
		maxSteps:           maxSteps,
		temperature:        temperature,
		maxToolPromptChars: maxToolChars,
	}
	a.Comp = compact.NewCompactor(hist, client, budget, calib, maxToolChars)
	a.Comp.SetTrace(func(s string) { a.tracef("%s", s) })
	return a
}

// SetTrace installs a callback for tool calls and compaction notices.
func (a *Agent) SetTrace(fn func(string)) {
	a.trace = fn
	a.Comp.SetTrace(fn)
}

// SetApprover installs the MCP approval gate. Local tools never call it.
func (a *Agent) SetApprover(fn Approver) { a.approve = fn }

// SetActionGuard installs the optional model check of an intended MCP call.
func (a *Agent) SetActionGuard(fn ActionGuard) { a.actionGuard = fn }

// SetStreamUI installs the streaming callbacks. Until it is called the agent
// uses plain non-stream requests (tests, benchmarks).
func (a *Agent) SetStreamUI(onStart func(), onContent func(string), onDone func()) {
	a.onMessageStart = onStart
	a.onContent = onContent
	a.onMessageDone = onDone
}

// SetReasoningUI installs the callback for streamed reasoning deltas. It is
// separate from SetStreamUI on purpose: reasoning is display-only, and the
// agent must still use streaming when only the answer is printed.
func (a *Agent) SetReasoningUI(fn func(string)) { a.onReasoning = fn }

// SetNoCompact disables automatic context compaction. Demo mode for day 8:
// the prompt is allowed to grow past the budget so the overflow behavior
// (and the eventual API failure) can be observed instead of hidden.
func (a *Agent) SetNoCompact(v bool) { a.noCompact = v }

// SetCompactEvery enables the message-count compaction trigger (0 = off).
func (a *Agent) SetCompactEvery(n int) { a.Comp.SetCompactEvery(n) }

// SetStrategy selects the context strategy (summary is the default).
func (a *Agent) SetStrategy(s compact.Strategy) {
	a.strategy = s
	a.Comp.SetStrategy(s)
}

// SetWindow sets the prompt window size for the window/facts strategies.
func (a *Agent) SetWindow(n int) {
	a.window = n
	a.Comp.SetWindow(n)
}

// SetMemory attaches the layered memory. Every turn then folds the new user
// message into the layers before the request is built, and the prompt carries
// the layers that the model's rules allow.
func (a *Agent) SetMemory(m *memory.Store) {
	a.mem = m
	a.Comp.SetMemory(m)
}

// TaskDone is the day-15 cleanup hook: when the task machine reaches done, the
// working layer is a card of the finished task and must not leak into the next
// one. Long-term layers (profile, decisions, knowledge) stay untouched. The
// removals are journaled with their old values, exactly like a model update.
func (a *Agent) TaskDone() {
	if a.mem == nil {
		return
	}
	changes := a.mem.ClearWithChanges(memory.LayerWorking)
	if len(changes) == 0 {
		return
	}
	if err := a.mem.Save(); err != nil {
		a.tracef("память: задачу завершили, но сохранить очистку не удалось: %v", err)
		return
	}
	upd := memory.Update{Remove: map[memory.Layer][]string{}}
	for _, c := range changes {
		upd.Remove[c.Layer] = append(upd.Remove[c.Layer], c.Key)
	}
	if err := a.hist.AppendMemoryChanges(upd, changes); err != nil {
		a.tracef("память: журнал очистки: %v", err)
	}
	a.tracef("память: задача завершена — рабочая память очищена (%s); "+
		"долговременная сохранена", memory.DescribeChanges(changes))
}

// SetTask attaches the day-13 task machine provider: the state block joins the
// system prompt after the memory layers.
func (a *Agent) SetTask(p func() *task.Machine) { a.Comp.SetTask(p) }

// SetInvariants attaches the day-14 project invariants. The block joins the
// system prompt between memory and the task state; the gate below enforces it.
func (a *Agent) SetInvariants(p func() *invariant.Set) {
	a.inv = p
	a.Comp.SetInvariants(p)
}

// SetInvariantChecker attaches the local-model prose checker. Nil keeps only
// the deterministic keyword fallback.
func (a *Agent) SetInvariantChecker(c *invariant.Checker) { a.invChecker = c }

// MarkChecked opens or closes the gate for the current turn. The tool registry
// calls it from check_solution: success opens, failure closes.
func (a *Agent) MarkChecked(ok bool) { a.checkedOK = ok }

// SetSummaryClient routes compaction (summary, facts, memory extraction) to a
// separate endpoint; nil keeps it on the chat client.
func (a *Agent) SetSummaryClient(cl *llm.ChatClient) { a.Comp.SetSummaryClient(cl) }

// Memory returns the attached memory model (nil when memory is off).
func (a *Agent) Memory() *memory.Store { return a.mem }

// mode returns the active strategy, defaulting to summary.
func (a *Agent) mode() compact.Strategy { return a.Comp.Mode() }

// LastAnswerTokens reports the completion-token usage of the most recent
// model turn (0 before the first turn).
func (a *Agent) LastAnswerTokens() int { return a.lastAnswerTokens }

func (a *Agent) tracef(format string, args ...any) {
	if a.trace != nil {
		a.trace(fmt.Sprintf(format, args...))
	}
}

// Chat handles one user turn. It may perform several model/tool round-trips
// and returns the final text once the model stops requesting tools.
func (a *Agent) Chat(ctx context.Context, userText string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.hist.AppendUser(userText)
	// The question drives knowledge relevance, so it is recorded before any
	// prompt is assembled.
	a.Comp.SetQuestion(userText)

	// Every turn starts with the invariant gate closed: the model must call
	// check_solution again, even if the previous turn passed.
	a.checkedOK = false
	inv := a.activeInvariants()
	rejects := 0

	// Explicit memory model: one extractor call per turn routes the durable
	// part of the message into working/long-term layers. On failure the layers
	// stay as they were and the turn proceeds.
	if a.mem != nil {
		a.updateMemory(ctx, userText)
	}

	// Sticky facts: fold the new user message into the key-value store before
	// anything else. One cheap extractor call per user turn; on failure the
	// previous store stays (the turn proceeds with slightly older facts).
	if a.mode() == compact.StrategyFacts {
		out, err := a.Comp.ExtractFacts(ctx, a.hist.Facts(), userText)
		if err != nil {
			if ctx.Err() == nil {
				a.tracef("факты: не удалось обновить (%s) — остаются прежние", config.OneLine(err.Error()))
			}
		} else if facts, perr := compact.ParseFactsJSON(out); perr != nil {
			a.tracef("факты: %s — остаются прежние", perr)
		} else if len(facts) == 0 && len(a.hist.Facts()) > 0 {
			a.tracef("факты: экстрактор вернул пустой список — остаются прежние")
		} else if serr := a.hist.SetFacts(facts); serr != nil {
			a.tracef("факты: %v", serr)
		} else {
			a.tracef("факты: %d ключей в памяти", len(facts))
		}
	}

	for step := 1; step <= a.maxSteps; step++ {
		// A cancelled turn must stop before any more model calls. This also
		// covers cancellation while memory extraction or compaction was
		// running: those failures are non-fatal, but the turn is not.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		switch a.mode() {
		case compact.StrategySummary:
			if !a.noCompact {
				if _, err := a.Comp.MaybeCompact(ctx, a.hist.SystemPrompt()); err != nil {
					if ctx.Err() == nil {
						a.tracef("сжатие контекста: %v", err)
					}
				}
			}
		case compact.StrategyWindow, compact.StrategyFacts:
			if _, err := a.Comp.EnforceWindow(a.window); err != nil {
				a.tracef("окно: %v", err)
			}
		}

		messages := a.buildMessages()
		estimated := 0
		for _, m := range messages {
			estimated += tokens.EstimateMessage(m)
		}

		if a.onMessageStart != nil {
			a.onMessageStart()
		}
		var resp *llm.ChatResponse
		var err error
		if a.onContent != nil {
			// With invariants the answer is buffered: the deltas are swallowed
			// and the text is printed only after the gate lets it through.
			// Reasoning still streams: it is not part of the answer.
			onDelta := a.onContent
			if inv != nil {
				onDelta = func(string) {}
			}
			resp, err = a.client.ChatStream(ctx, a.hist.ChatModel(), messages, a.tools.Schemas(), a.temperature, onDelta, a.onReasoning)
		} else {
			resp, err = a.client.Chat(ctx, a.hist.ChatModel(), messages, a.tools.Schemas(), a.temperature)
		}
		if err != nil {
			return "", err
		}
		a.Calib.Update(estimated, resp.Usage.PromptTokens)
		a.lastAnswerTokens = resp.Usage.CompletionTokens

		a.hist.AppendAssistant(resp.Content, resp.ToolCalls, a.hist.ChatModel())
		if a.onMessageDone != nil {
			a.onMessageDone()
		}

		if len(resp.ToolCalls) == 0 {
			if inv != nil {
				problem := a.invariantProblem(ctx, inv, resp.Content)
				// The local checker uses the same context. If Ctrl-C cancelled
				// it, the keyword fallback may have let the draft through;
				// never release an answer from a cancelled turn.
				if err := ctx.Err(); err != nil {
					return "", err
				}
				if problem != "" {
					rejects++
					if rejects > maxInvariantRejects {
						return "", fmt.Errorf("инварианты: ответ не прошёл проверку кода %d раз; последний отказ: %s", maxInvariantRejects, problem)
					}
					// The draft stays in the log (audit), the notice tells the
					// model what to fix. The violating text never reaches
					// stdout: with invariants the stream was swallowed.
					a.hist.AppendNotice(problem +
						" Черновик выше не выпущен. Перепиши ответ: убери нарушение, " +
						"вызови check_solution и только после успешной проверки отвечай.")
					a.tracef("инварианты: черновик отклонён кодом — %s", config.OneLine(problem))
					continue
				}
			}
			return resp.Content, nil
		}

		for _, call := range resp.ToolCalls {
			actionOK, actionReason, err := a.checkAction(ctx, userText, call)
			if err != nil {
				return "", err
			}
			if !actionOK {
				msg := "guard счёл действие небезопасным"
				if strings.TrimSpace(actionReason) != "" {
					msg = strings.TrimSpace(actionReason)
				}
				out := fmt.Sprintf("⛔ действие %s отклонено guard: %s; не повторяй его без новой просьбы", call.Function.Name, msg)
				a.tracef("%s — действие отклонено guard: %s", call.Function.Name, config.OneLine(msg))
				a.hist.AppendTool(call.ID, call.Function.Name, out)
				continue
			}
			allowed, err := a.approveCall(call)
			if err != nil {
				return "", err
			}
			if !allowed {
				out := fmt.Sprintf("⛔ вызов %s отклонён пользователем; не повторяй его без новой просьбы", call.Function.Name)
				a.tracef("%s — вызов отклонён пользователем", call.Function.Name)
				a.hist.AppendTool(call.ID, call.Function.Name, out)
				continue
			}
			out := a.tools.Execute(call)
			a.tracef("%s(%s) → %s", call.Function.Name, call.Function.Arguments, config.OneLine(out))
			a.hist.AppendTool(call.ID, call.Function.Name, out)
		}
	}

	return "", fmt.Errorf("агент: превышен лимит шагов (%d), модель продолжает вызывать инструменты", a.maxSteps)
}

// approveCall applies the MCP approval policy before the tool runs. Local
// tools and tools explicitly marked allow skip the gate. A nil approver means
// allow: only main installs the interactive gate.
func (a *Agent) approveCall(call llm.ToolCall) (bool, error) {
	meta, ok := a.tools.Meta(call.Function.Name)
	if !ok || meta.Source != "mcp" || meta.Approval == "allow" || a.approve == nil {
		return true, nil
	}
	if meta.Approval == "deny" {
		return false, nil
	}
	return a.approve(ApprovalRequest{
		Name:   call.Function.Name,
		Server: meta.Server,
		Source: meta.Source,
		Args:   call.Function.Arguments,
	})
}

// checkAction runs the optional model check before the approval gate. It only
// covers MCP tools: local tools are already bounded by their implementation.
func (a *Agent) checkAction(ctx context.Context, userText string, call llm.ToolCall) (bool, string, error) {
	if a.actionGuard == nil {
		return true, "", nil
	}
	meta, ok := a.tools.Meta(call.Function.Name)
	if !ok || meta.Source != "mcp" {
		return true, "", nil
	}
	return a.actionGuard(ctx, userText, ApprovalRequest{
		Name:   call.Function.Name,
		Server: meta.Server,
		Source: meta.Source,
		Args:   call.Function.Arguments,
	})
}

// activeInvariants returns the set only when it has rules to enforce.
func (a *Agent) activeInvariants() *invariant.Set {
	if a.inv == nil {
		return nil
	}
	set := a.inv()
	if set == nil || !set.Active() {
		return nil
	}
	return set
}

// invariantProblem is the gate's verdict on a final draft. Empty means the
// answer may be released. The order is deliberate: the structural check (the
// model's own check_solution declaration) is the hard guarantee, then the
// keyword scan, then the local model for meaning. If the local endpoint is
// down, the first two still hold.
func (a *Agent) invariantProblem(ctx context.Context, set *invariant.Set, text string) string {
	if !a.checkedOK {
		return "check_solution не пройден: перед ответом вызови инструмент и перечисли все затронутые инварианты и выборы"
	}
	// The semantic layer is primary: it understands refusals and comparisons,
	// while the keyword scan is deliberately conservative and would reject a
	// correct "MongoDB can only be changed by the user" explanation. Keywords
	// remain the fallback when the local endpoint is unavailable.
	if a.invChecker != nil {
		a.tracef("инварианты: проверяю текст локальной моделью %s", a.invChecker.Model())
		v, err := a.invChecker.Check(ctx, text, set.All())
		if err != nil {
			a.tracef("инварианты: локальная проверка недоступна (%s) — остаются ключевые слова", config.OneLine(err.Error()))
			return keywordProblem(set, text)
		}
		if v.Violates {
			// A small local model can invent a plausible quote. Blocking on a
			// sentence that is not in the answer would reject a correct
			// refusal on fabricated evidence; require the quote to be present.
			if !invariant.QuotePresent(text, v.Quote) {
				a.tracef("инварианты: локальная модель сослалась на фразу, которой нет в ответе — вердикт отброшен")
				return ""
			}
			// A refusal may quote the forbidden number or name while
			// explaining why it is not allowed. The semantic layer sometimes
			// flags exactly that; do not let it reject a correct refusal.
			if invariant.NegatedQuote(text, v.Quote) {
				a.tracef("инварианты: локальная модель ошиблась — цитата в отрицающем предложении")
				return ""
			}
			// A numeric rule has a deterministic answer: if no number in the
			// text is above max, the model's "budget exceeded" verdict is
			// wrong. This catches the valid "350 000 при лимите 400 000" case.
			if it := set.Find(v.ID); it != nil && it.Rule.Max != nil && !invariant.ExceedsMax(text, *it.Rule.Max) {
				a.tracef("инварианты: локальная модель ошиблась — числа не превышают предел %g", *it.Rule.Max)
				return ""
			}
			quote := config.Truncate(config.OneLine(v.Quote), 140)
			id := v.ID
			if id == "" {
				id = "?"
			}
			reason := config.OneLine(v.Reason)
			if reason == "" {
				reason = "предлагает запрещённое решение"
			}
			return fmt.Sprintf("локальная модель увидела нарушение инварианта %s: %q — %s", id, quote, reason)
		}
		a.tracef("инварианты: нарушений нет")
		return ""
	}
	return keywordProblem(set, text)
}

// keywordProblem is the deterministic fallback for free text. It is
// intentionally simple and over-blocks some refusals; it runs only when no
// semantic checker is configured or the local endpoint failed.
func keywordProblem(set *invariant.Set, text string) string {
	if vs := set.CheckText(text); len(vs) > 0 {
		v := vs[0]
		return fmt.Sprintf("в ответе запрещённое значение %q (инвариант %s)", v.Term, v.ID)
	}
	return ""
}

// updateMemory folds the new user message into the layered memory: extract a
// diff, merge it, persist the files and journal the diff into the session log.
func (a *Agent) updateMemory(ctx context.Context, userText string) {
	out, err := a.Comp.ExtractMemory(ctx, a.mem, userText)
	if err != nil {
		// Cancellation is not a memory failure: the user stopped the turn and
		// the "прервано" line already explains it.
		if ctx.Err() != nil {
			return
		}
		a.tracef("память: не удалось обновить (%s) — остаётся прежняя", config.OneLine(err.Error()))
		return
	}
	upd, perr := compact.ParseMemoryJSON(out)
	if perr != nil {
		a.tracef("память: %s — остаётся прежняя", perr)
		return
	}
	upd, isolated := compact.IsolateMemoryUpdate(a.mem, userText, upd)
	if len(isolated) > 0 {
		a.tracef("память: пропущено, чтобы не смешать объекты: %s", strings.Join(isolated, "; "))
	}
	if upd.Empty() {
		return // nothing durable in this message
	}
	changes := a.mem.Apply(upd)
	if err := a.mem.Save(); err != nil {
		a.tracef("память: сохранить не удалось: %v", err)
	}
	if err := a.hist.AppendMemoryChanges(upd, changes); err != nil {
		a.tracef("память: журнал: %v", err)
	}
	if len(changes) > 0 {
		a.tracef("память: %s; всего %d", memory.DescribeChanges(changes), a.mem.Total())
		return
	}
	a.tracef("память: %s; всего %d", describeUpdate(upd), a.mem.Total())
}

// describeUpdate summarizes a memory diff for the trace line.
func describeUpdate(u memory.Update) string {
	var parts []string
	for _, l := range memory.Layers {
		added, removed := len(u.Layer(l)), len(u.Removed(l))
		switch {
		case added > 0 && removed > 0:
			parts = append(parts, fmt.Sprintf("%s +%d −%d", l, added, removed))
		case added > 0:
			parts = append(parts, fmt.Sprintf("%s +%d", l, added))
		case removed > 0:
			parts = append(parts, fmt.Sprintf("%s −%d", l, removed))
		}
	}
	if len(parts) == 0 {
		return "без изменений"
	}
	return strings.Join(parts, ", ")
}

// buildMessages assembles the API request: system prompt with the strategy's
// memory block (summary or facts), then the strategy's window. Tool outputs
// are truncated here only — the log keeps the full result.
func (a *Agent) buildMessages() []llm.Message {
	out := make([]llm.Message, 0, len(a.hist.Active())+1)
	out = append(out, llm.Message{Role: "system", Content: a.Comp.PromptSystem(a.hist.SystemPrompt())})
	for _, m := range a.Comp.PromptWindow() {
		c := *m
		if c.Role == "tool" {
			c.Content = config.Truncate(c.Content, a.maxToolPromptChars)
		}
		out = append(out, c)
	}
	return out
}
