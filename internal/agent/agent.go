package agent

import (
	"context"
	"fmt"
	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
	"strings"
)

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
}

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

// SetTask attaches the day-13 task machine provider: the state block joins the
// system prompt after the memory layers.
func (a *Agent) SetTask(p func() *task.Machine) { a.Comp.SetTask(p) }

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
	a.hist.AppendUser(userText)
	// The question drives knowledge relevance, so it is recorded before any
	// prompt is assembled.
	a.Comp.SetQuestion(userText)

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
			a.tracef("факты: не удалось обновить (%s) — остаются прежние", config.OneLine(err.Error()))
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
		switch a.mode() {
		case compact.StrategySummary:
			if !a.noCompact {
				if _, err := a.Comp.MaybeCompact(ctx, a.hist.SystemPrompt()); err != nil {
					a.tracef("сжатие контекста: %v", err)
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
			resp, err = a.client.ChatStream(ctx, a.hist.ChatModel(), messages, a.tools.Schemas(), a.temperature, a.onContent, a.onReasoning)
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
			return resp.Content, nil
		}

		for _, call := range resp.ToolCalls {
			out := a.tools.Execute(call)
			a.tracef("%s(%s) → %s", call.Function.Name, call.Function.Arguments, config.OneLine(out))
			a.hist.AppendTool(call.ID, call.Function.Name, out)
		}
	}

	return "", fmt.Errorf("агент: превышен лимит шагов (%d), модель продолжает вызывать инструменты", a.maxSteps)
}

// updateMemory folds the new user message into the layered memory: extract a
// diff, merge it, persist the files and journal the diff into the session log.
func (a *Agent) updateMemory(ctx context.Context, userText string) {
	out, err := a.Comp.ExtractMemory(ctx, a.mem, userText)
	if err != nil {
		a.tracef("память: не удалось обновить (%s) — остаётся прежняя", config.OneLine(err.Error()))
		return
	}
	upd, perr := compact.ParseMemoryJSON(out)
	if perr != nil {
		a.tracef("память: %s — остаётся прежняя", perr)
		return
	}
	if upd.Empty() {
		return // nothing durable in this message
	}
	a.mem.Apply(upd)
	if err := a.mem.Save(); err != nil {
		a.tracef("память: сохранить не удалось: %v", err)
	}
	if err := a.hist.AppendMemory(upd); err != nil {
		a.tracef("память: журнал: %v", err)
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
