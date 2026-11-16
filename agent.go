package main

import (
	"context"
	"fmt"
	"strings"
)

// Agent owns the whole loop: request → tool call → tool result → answer.
// main only feeds it user text and renders the result.
type Agent struct {
	client             *ChatClient
	tools              *ToolRegistry
	hist               *History
	comp               *Compactor
	calib              *Calibrator
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
	strategy         Strategy
	window           int // prompt window for the window/facts strategies
	lastAnswerTokens int // completion tokens of the most recent model turn
}

// NewAgent wires a session, a tool set and a context compactor together.
func NewAgent(client *ChatClient, tools *ToolRegistry, hist *History, budget Budget, maxSteps int, temperature float64, maxToolChars int) *Agent {
	calib := NewCalibrator()
	a := &Agent{
		client:             client,
		tools:              tools,
		hist:               hist,
		calib:              calib,
		maxSteps:           maxSteps,
		temperature:        temperature,
		maxToolPromptChars: maxToolChars,
	}
	a.comp = NewCompactor(hist, client, budget, calib, maxToolChars)
	a.comp.trace = func(s string) { a.tracef("%s", s) }
	return a
}

// SetTrace installs a callback for tool calls and compaction notices.
func (a *Agent) SetTrace(fn func(string)) {
	a.trace = fn
	a.comp.trace = fn
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
func (a *Agent) SetCompactEvery(n int) { a.comp.SetCompactEvery(n) }

// SetStrategy selects the context strategy (summary is the default).
func (a *Agent) SetStrategy(s Strategy) {
	a.strategy = s
	a.comp.SetStrategy(s)
}

// SetWindow sets the prompt window size for the window/facts strategies.
func (a *Agent) SetWindow(n int) {
	a.window = n
	a.comp.SetWindow(n)
}

// mode returns the active strategy, defaulting to summary.
func (a *Agent) mode() Strategy { return a.comp.mode() }

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

	// Sticky facts: fold the new user message into the key-value store before
	// anything else. One cheap extractor call per user turn; on failure the
	// previous store stays (the turn proceeds with slightly older facts).
	if a.mode() == StrategyFacts {
		out, err := a.comp.extractFacts(ctx, a.hist.Facts(), userText)
		if err != nil {
			a.tracef("факты: не удалось обновить (%s) — остаются прежние", oneLine(err.Error()))
		} else if facts, perr := parseFactsJSON(out); perr != nil {
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
		case StrategySummary:
			if !a.noCompact {
				if _, err := a.comp.MaybeCompact(ctx, a.hist.SystemPrompt()); err != nil {
					a.tracef("сжатие контекста: %v", err)
				}
			}
		case StrategyWindow, StrategyFacts:
			if _, err := a.comp.EnforceWindow(a.window); err != nil {
				a.tracef("окно: %v", err)
			}
		}

		messages := a.buildMessages()
		estimated := 0
		for _, m := range messages {
			estimated += EstimateMessage(m)
		}

		if a.onMessageStart != nil {
			a.onMessageStart()
		}
		var resp *ChatResponse
		var err error
		if a.onContent != nil {
			resp, err = a.client.ChatStream(ctx, a.hist.ChatModel(), messages, a.tools.Schemas(), a.temperature, a.onContent, a.onReasoning)
		} else {
			resp, err = a.client.Chat(ctx, a.hist.ChatModel(), messages, a.tools.Schemas(), a.temperature)
		}
		if err != nil {
			return "", err
		}
		a.calib.Update(estimated, resp.Usage.PromptTokens)
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
			a.tracef("%s(%s) → %s", call.Function.Name, call.Function.Arguments, oneLine(out))
			a.hist.AppendTool(call.ID, call.Function.Name, out)
		}
	}

	return "", fmt.Errorf("агент: превышен лимит шагов (%d), модель продолжает вызывать инструменты", a.maxSteps)
}

// buildMessages assembles the API request: system prompt with the strategy's
// memory block (summary or facts), then the strategy's window. Tool outputs
// are truncated here only — the log keeps the full result.
func (a *Agent) buildMessages() []Message {
	out := make([]Message, 0, len(a.hist.Active())+1)
	out = append(out, Message{Role: "system", Content: a.comp.promptSystem(a.hist.SystemPrompt())})
	for _, m := range a.comp.promptWindow() {
		c := *m
		if c.Role == "tool" {
			c.Content = truncate(c.Content, a.maxToolPromptChars)
		}
		out = append(out, c)
	}
	return out
}

// oneLine flattens a tool result for the trace line.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, 160)
}
