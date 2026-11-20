package compact

import (
	"context"
	"fmt"
	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
	"strings"
	"time"
)

// summaryAttemptTimeout bounds one summarizer request. Without it a hung
// summary model eats the caller's whole deadline (180 s in ask), and the
// fallback model never gets a chance to run.
var summaryAttemptTimeout = 75 * time.Second

// Budget is the token budget of the prompt window.
type Budget struct {
	MaxPromptTokens int // hard ceiling for system + summary + history
	ReserveOutput   int // room kept for the model's answer
	MinTailTokens   int // keep at least this much recent history verbatim
}

// DefaultBudget is tuned for small demo backends (8k–32k context).
func DefaultBudget() Budget {
	return Budget{
		MaxPromptTokens: 6000,
		ReserveOutput:   1024,
		MinTailTokens:   1200,
	}
}

// Compactor implements the context strategy: sliding window + rolling summary
// + archive. It never deletes data: messages leave the prompt, not the log.
type Compactor struct {
	hist         *history.History
	client       *llm.ChatClient
	budget       Budget
	calib        *tokens.Calibrator
	maxToolChars int
	trace        func(string)
	compactEvery int      // compact when the window holds this many messages (0 = off)
	strategy     Strategy // summary (default) | window | facts
	window       int      // prompt window size for the window/facts strategies

	// mem is the explicit memory model (day 11): working + long-term layers,
	// independent of the short-term strategy. question is the user text of the
	// current turn — it decides which knowledge records enter the prompt.
	mem      *memory.Store
	question string

	// task is the day-13 task machine provider; its state block joins the
	// system prompt after the memory layers.
	task func() *task.Machine
}

// SetCompactEvery enables the message-count trigger: the history is compacted
// once the active window grows to n messages, even if the prompt still fits
// the budget. n <= 0 restores the budget-only behavior.
func (c *Compactor) SetCompactEvery(n int) { c.compactEvery = n }

// SetTrace installs the callback for compaction notices.
func (c *Compactor) SetTrace(fn func(string)) { c.trace = fn }

// SetStrategy selects the context strategy (summary when never set).
func (c *Compactor) SetStrategy(s Strategy) { c.strategy = s }

// SetWindow sets the prompt window size for the window/facts strategies.
func (c *Compactor) SetWindow(n int) { c.window = n }

// SetMemory attaches the explicit memory model. Nil disables the memory block.
func (c *Compactor) SetMemory(m *memory.Store) { c.mem = m }

// SetTask attaches the day-13 task machine provider. The provider (not a
// pointer) is deliberate: /new and /session swap the machine per session. Nil
// provider or no started task removes the block from the prompt.
func (c *Compactor) SetTask(p func() *task.Machine) { c.task = p }

// SetQuestion records the user text of the current turn. Knowledge records are
// filtered by relevance to it, so the prompt must be built after this call.
func (c *Compactor) SetQuestion(q string) { c.question = q }

// Memory returns the attached memory model (nil when memory is off).
func (c *Compactor) Memory() *memory.Store { return c.mem }

// MemoryBlock renders the layers that belong in the prompt for the current
// question: working, profile and decisions always, knowledge by relevance.
func (c *Compactor) MemoryBlock() string {
	if c.mem == nil {
		return ""
	}
	return c.mem.Prompt(c.question)
}

// MemoryTokens estimates each layer's contribution to the current prompt.
func (c *Compactor) MemoryTokens() map[memory.Layer]int {
	if c.mem == nil {
		return nil
	}
	return c.mem.LayerTokens(c.question)
}

// ShortSystem returns the system prompt with only the short-term block (the
// summary or the day-10 facts). /context prints the memory layers separately,
// so it must not count them twice.
func (c *Compactor) ShortSystem(system string) string { return c.shortBlock(system) }

// mode returns the active strategy, defaulting to the day-9 summary behavior.
func (c *Compactor) Mode() Strategy {
	if c.strategy == "" {
		return StrategySummary
	}
	return c.strategy
}

// NewCompactor builds a compactor for one session.
func NewCompactor(hist *history.History, client *llm.ChatClient, budget Budget, calib *tokens.Calibrator, maxToolChars int) *Compactor {
	return &Compactor{
		hist:         hist,
		client:       client,
		budget:       budget,
		calib:        calib,
		maxToolChars: maxToolChars,
	}
}

// PromptTokens estimates the next request size: system (+ the strategy's
// memory block) and the window the strategy would send, with tool outputs
// truncated exactly as buildMessages does.
func (c *Compactor) PromptTokens(system string) int {
	n := tokens.EstimateTokens(c.PromptSystem(system))
	for _, m := range c.PromptWindow() {
		msg := *m
		if msg.Role == "tool" {
			msg.Content = config.Truncate(msg.Content, c.maxToolChars)
		}
		n += tokens.EstimateMessage(msg)
	}
	return c.calib.Scale(n)
}

// promptSystem assembles the system prompt with the strategy's memory block:
// the rolling summary for the summary strategy, the sticky facts for the
// facts strategy, nothing extra for the bare window. The day-13 task state
// block goes last, after the memory layers.
func (c *Compactor) PromptSystem(system string) string {
	base := c.shortBlock(system)
	if block := c.MemoryBlock(); block != "" {
		base = base + "\n\n" + block
	}
	if c.task != nil {
		if m := c.task(); m != nil {
			if block := m.Block(); block != "" {
				base = base + "\n\n" + block
			}
		}
	}
	return base
}

// shortBlock is the short-term part of the system prompt: the strategy's
// memory block (summary or sticky facts), nothing extra for the bare window.
func (c *Compactor) shortBlock(system string) string {
	switch c.Mode() {
	case StrategyFacts:
		if facts := c.hist.Facts(); len(facts) > 0 {
			return system + "\n\nФакты диалога (память о договорённостях):\n" + RenderFacts(facts)
		}
	case StrategySummary:
		if s := c.hist.SummaryText(); s != "" {
			return system + "\n\nКонспект предыдущего диалога:\n" + s
		}
	}
	return system
}

// promptWindow returns the messages the next request will carry: the whole
// active window for the summary strategy (compaction bounds it), the last
// windowCut messages for window/facts.
func (c *Compactor) PromptWindow() []*llm.Message {
	msgs := c.hist.Active()
	if c.Mode() == StrategySummary {
		return msgs
	}
	if cut := windowCut(msgs, c.window); cut > 0 {
		return msgs[cut:]
	}
	return msgs
}

// windowCut returns the index where the kept tail starts: exactly the last n
// messages, so the window slides one message at a time. The cut moves forward
// only when it lands on a tool result — a tool message cannot be sent without
// the assistant call that produced it. 0 means "nothing to cut".
func windowCut(msgs []*llm.Message, n int) int {
	if n <= 0 || len(msgs) <= n {
		return 0
	}
	cut := len(msgs) - n
	for cut < len(msgs) && msgs[cut].Role == "tool" {
		cut++
	}
	if cut >= len(msgs) {
		// The window is too small to hold even one complete turn: keep the
		// last turn anyway, so eviction still makes progress.
		for i := len(msgs) - 1; i > 0; i-- {
			if msgs[i].Role == "user" {
				return i
			}
		}
		return 0
	}
	return cut
}

// EnforceWindow archives the oldest messages until at most n remain in the
// prompt. Unlike ForceCompact nothing replaces the evicted text — that is the
// honest "drop it" behavior of the sliding window (the raw messages stay in
// the log and /recall).
func (c *Compactor) EnforceWindow(n int) (bool, error) {
	msgs := c.hist.Active()
	cut := windowCut(msgs, n)
	if cut <= 0 {
		return false, nil
	}
	from, to := msgs[0].Seq, msgs[cut-1].Seq
	if err := c.hist.Evict(from, to); err != nil {
		return false, err
	}
	if c.trace != nil {
		c.trace(fmt.Sprintf("окно: сообщения seq %d..%d → архив (стратегия %s)", from, to, c.Mode()))
	}
	return true, nil
}

// pendingCount counts the messages added since the last compaction. The
// verbatim tail kept after a compaction is not part of the summary, so the
// count cannot be derived from SummaryCovers: it starts at the compaction
// point instead. Counting the whole window would keep the trigger true on
// every turn whenever the tail is at least N messages long.
func (c *Compactor) pendingCount() int {
	if !c.hist.HasSummary() {
		return len(c.hist.Active())
	}
	n := c.hist.LastSeq() - c.hist.CompactMark()
	if n < 0 {
		return 0
	}
	return int(n)
}

// MaybeCompact compacts the window when either trigger fires — whichever
// comes first: the prompt no longer fits the budget, or compactEvery new
// messages have accumulated since the last compaction. The budget trigger may
// need several rounds; the message-count trigger runs at most one, so a small
// N cannot fold the whole dialogue into the summary in a single turn.
func (c *Compactor) MaybeCompact(ctx context.Context, system string) (bool, error) {
	limit := c.budget.MaxPromptTokens - c.budget.ReserveOutput
	if limit < 500 {
		limit = 500
	}
	changed := false
	for i := 0; i < 3; i++ {
		byTokens := c.PromptTokens(system) > limit*85/100
		byCount := c.compactEvery > 0 && c.pendingCount() >= c.compactEvery
		if !byTokens && !byCount {
			break
		}
		ok, err := c.ForceCompact(ctx, system)
		if err != nil {
			return changed, err
		}
		if !ok {
			break
		}
		changed = true
		if !byTokens {
			break
		}
	}
	return changed, nil
}

// ForceCompact archives the oldest turns and folds them into the rolling
// summary. It returns false when there is nothing safe to compact.
func (c *Compactor) ForceCompact(ctx context.Context, system string) (bool, error) {
	msgs := c.hist.Active()
	split := c.splitIndex(msgs)
	if split <= 0 {
		return false, nil
	}
	prefix := msgs[:split]
	from, to := prefix[0].Seq, prefix[len(prefix)-1].Seq
	// The new summary folds in the previous one, so its coverage starts
	// earlier. Compact() only touches active messages, so widening the range
	// is safe: already archived seqs are simply not in the window.
	coversFrom := from
	if c.hist.HasSummary() {
		if prev := c.hist.SummaryCovers(); prev[0] > 0 && prev[0] < coversFrom {
			coversFrom = prev[0]
		}
	}

	summary, err := c.summarize(ctx, prefix)
	if err != nil {
		// Never lose the dialogue because the summarizer is down: keep the old
		// summary, archive the raw messages and leave a pointer instead.
		var b strings.Builder
		if c.hist.HasSummary() {
			b.WriteString(c.hist.SummaryText())
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "(сжатие недоступно: %v; ещё %d сообщений перенесено в архив, доступны через /recall)",
			err, len(prefix))
		summary = b.String()
	}
	if err := c.hist.Compact(coversFrom, to, summary); err != nil {
		return false, err
	}
	if c.trace != nil {
		c.trace(fmt.Sprintf("сжатие контекста: сообщения seq %d..%d → архив, конспект ~%d ток., промпт теперь ~%d ток.",
			from, to, tokens.EstimateTokens(summary), c.PromptTokens(system)))
	}
	return true, nil
}

// splitIndex returns the index of the user message that starts the verbatim
// tail. Splitting on a user boundary keeps every assistant+tool group intact.
func (c *Compactor) splitIndex(msgs []*llm.Message) int {
	if len(msgs) < 2 {
		return 0
	}
	tail := 0
	best := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		tail += tokens.EstimateMessage(*msgs[i])
		if msgs[i].Role == "user" && tail >= c.budget.MinTailTokens {
			best = i
			break
		}
	}
	if best == 0 {
		// Fewer turns than the tail budget: keep the last user turn.
		for i := len(msgs) - 1; i > 0; i-- {
			if msgs[i].Role == "user" {
				best = i
				break
			}
		}
	}
	return best
}

// summarizePrompt builds the summarizer input: the previous summary plus the
// messages that are leaving the prompt.
func (c *Compactor) summarizePrompt(prefix []*llm.Message) string {
	var b strings.Builder
	if c.hist.HasSummary() {
		b.WriteString("Предыдущий конспект:\n")
		b.WriteString(c.hist.SummaryText())
		b.WriteString("\n\n")
	}
	b.WriteString("Диалог:\n")
	for _, m := range prefix {
		switch m.Role {
		case "user":
			fmt.Fprintf(&b, "Пользователь: %s\n", config.Truncate(m.Content, 1000))
		case "assistant":
			if m.Content != "" {
				fmt.Fprintf(&b, "Ассистент: %s\n", config.Truncate(m.Content, 1000))
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "  вызов %s(%s)\n", tc.Function.Name, config.Truncate(tc.Function.Arguments, 300))
			}
		case "tool":
			fmt.Fprintf(&b, "  результат %s: %s\n", m.Name, config.Truncate(m.Content, 400))
		}
	}
	return b.String()
}

// summarize folds the previous summary and the archived prefix into one short
// note. If the summary model fails (503, timeout, empty answer), the chat model
// is tried once as a backup: a slightly more expensive note is much better than
// a stub that loses the dialogue.
func (c *Compactor) summarize(ctx context.Context, prefix []*llm.Message) (string, error) {
	prompt := c.summarizePrompt(prefix)
	primary := c.hist.SummaryModel()

	summary, err := c.summarizeWith(ctx, primary, prompt)
	if err == nil {
		return summary, nil
	}

	backup := c.hist.ChatModel()
	if backup == "" || backup == primary || ctx.Err() != nil {
		return "", err
	}
	if c.trace != nil {
		c.trace(fmt.Sprintf("сжатие: %s не справился (%s) — пробую %s", primary, config.OneLine(err.Error()), backup))
	}
	summary, err2 := c.summarizeWith(ctx, backup, prompt)
	if err2 != nil {
		return "", fmt.Errorf("%s: %v; запасная %s: %v", primary, err, backup, err2)
	}
	if c.trace != nil {
		c.trace(fmt.Sprintf("сжатие выполнено запасной моделью %s", backup))
	}
	return summary, nil
}

// summarizeWith runs one summarizer request on one model under its own timeout.
func (c *Compactor) summarizeWith(ctx context.Context, model, prompt string) (string, error) {
	sys := "Ты сжимаешь историю диалога в компактный конспект. Сохрани: что просил пользователь, " +
		"какие инструменты вызывались и с каким результатом, какие выводы и решения приняты. " +
		"Обязательно выписывай конкретные детали с их значениями — имена, числа, сроки, бюджеты, " +
		"ограничения, названия: по ним будут задавать вопросы. " +
		"Пиши по-русски, не более 250 слов, без вступлений и без обращения к пользователю."

	actx, cancel := context.WithTimeout(ctx, summaryAttemptTimeout)
	defer cancel()
	resp, err := c.client.Chat(actx, model, []llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: prompt},
	}, nil, 0)
	if err != nil {
		// Our own cap fired, not the caller's deadline: say so, otherwise the
		// error reads as if the whole turn ran out of time.
		if actx.Err() != nil && ctx.Err() == nil {
			return "", fmt.Errorf("%s: таймаут %s", model, summaryAttemptTimeout)
		}
		return "", err
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return "", fmt.Errorf("%s: пустой конспект", model)
	}
	return summary, nil
}
