package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// app wires the CLI, the agent and the session together.
type app struct {
	client       *ChatClient
	tools        *ToolRegistry
	dataDir      string
	system       string
	budget       Budget
	maxSteps     int
	temperature  float64
	maxToolChars int
	noCompact    bool
	compactEvery int
	strategy     Strategy
	window       int

	hist  *History
	agent *Agent
	ui    *ui
}

// ui owns the single "status line" under the prompt: the one-character
// spinner and the trace lines (tool calls, compaction notices) share it, so
// every write is serialized through one mutex. Streamed answer deltas also go
// through it: the first token stops the spinner, and every consumer that
// prints a full line closes the streamed line first.
type ui struct {
	mu        sync.Mutex
	tty       bool
	spinning  bool
	streamed  bool // a streamed answer fragment is on screen, line not closed
	sawStream bool // any answer text was streamed during the current ask turn
}

const spinLine = "  / "

// spinFrames is the one-character spinner: / - \ |, one cell, in place.
const spinFrames = "/-\\|"

// begin starts the spinner. It returns a stop channel (nil when the spinner
// is off); waiting on it guarantees the line is cleared before the next print.
func (u *ui) begin(done <-chan struct{}) <-chan struct{} {
	if !u.tty {
		return nil
	}
	stop := make(chan struct{})
	u.mu.Lock()
	u.spinning = true
	u.mu.Unlock()
	go func() {
		defer close(stop)
		for i := 0; ; i++ {
			select {
			case <-done:
				u.mu.Lock()
				if u.spinning {
					fmt.Print("\r" + strings.Repeat(" ", len(spinLine)) + "\r")
					u.spinning = false
				}
				u.mu.Unlock()
				return
			case <-time.After(130 * time.Millisecond):
				u.mu.Lock()
				if u.spinning {
					fmt.Printf("\r  %s ", string(spinFrames[i%len(spinFrames)]))
				}
				u.mu.Unlock()
			}
		}
	}()
	return stop
}

// rearm turns the spinner back on before the next model request (the painting
// goroutine from begin() keeps running until done is closed).
func (u *ui) rearm() {
	if !u.tty {
		return
	}
	u.mu.Lock()
	u.spinning = true
	u.mu.Unlock()
}

// beginTurn marks the start of one ask() turn: nothing streamed yet.
func (u *ui) beginTurn() {
	u.mu.Lock()
	u.streamed = false
	u.sawStream = false
	u.mu.Unlock()
}

// content prints a streamed piece of the assistant answer, stopping the
// spinner on the first visible token.
func (u *ui) content(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.spinning {
		fmt.Print("\r" + strings.Repeat(" ", len(spinLine)) + "\r")
		u.spinning = false
	}
	u.streamed = true
	u.sawStream = true
	fmt.Print(s)
}

// endLine closes a streamed line if one is open and reports whether it did.
func (u *ui) endLine() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.streamed {
		fmt.Println()
		u.streamed = false
		return true
	}
	return false
}

// didStream reports whether answer text was streamed in the current turn.
func (u *ui) didStream() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sawStream
}

// trace prints a tool/compaction line, wiping the spinner frame first and
// closing an unterminated streamed line.
func (u *ui) trace(s string) {
	u.mu.Lock()
	if u.spinning {
		fmt.Print("\r" + strings.Repeat(" ", len(spinLine)) + "\r")
	}
	if u.streamed {
		fmt.Println()
		u.streamed = false
	}
	fmt.Println("  ↳ " + s)
	u.mu.Unlock()
}

func main() {
	modelFlag := flag.String("model", "", "чат-модель (по умолчанию LLM_MODEL или deepseek-v4.1-flash)")
	summaryFlag := flag.String("summary-model", "", "модель для сжатия контекста (по умолчанию LLM_SUMMARY_MODEL или самая дешёвая в списке)")
	sessionFlag := flag.String("session", "", "id сессии")
	continueFlag := flag.Bool("continue", false, "продолжить последнюю сессию")
	dataFlag := flag.String("data", "", "каталог сессий")
	tasksFlag := flag.String("tasks", "tasks.txt", "список задач для find_task")
	maxSteps := flag.Int("max-steps", 8, "максимум шагов с инструментами на один запрос")
	maxPrompt := flag.Int("max-prompt-tokens", 6000, "бюджет токенов промпта")
	reserve := flag.Int("reserve-output", 1024, "токены, зарезервированные под ответ")
	minTail := flag.Int("min-tail-tokens", 1200, "сколько свежей истории держать дословно")
	temp := flag.Float64("temperature", 0.2, "температура сэмплирования")
	maxTokens := flag.Int("max-tokens", 4096, "лимит токенов ответа (включая рассуждения reasoning-моделей)")
	maxToolChars := flag.Int("max-tool-chars", 800, "сколько символов результата инструмента оставлять в промпте")
	noCompact := flag.Bool("no-compact", false, "отключить авто-сжатие контекста (демо переполнения)")
	compactEvery := flag.Int("compact-every", 0, "сжимать историю после каждых N новых сообщений с прошлого сжатия (0 — только по бюджету)")
	strategyFlag := flag.String("strategy", string(StrategySummary), "стратегия контекста: summary (конспект), window (последние N сообщений), facts (ключ-значение + окно)")
	windowFlag := flag.Int("window", 0, "размер окна для стратегий window и facts (обязателен для них)")
	systemFlag := flag.String("system", "", "файл с системным промптом")
	scriptFlag := flag.String("script", "", "проиграть запросы из файла и выйти")
	listModels := flag.Bool("list-models", false, "показать список моделей и выйти")
	listSessions := flag.Bool("list-sessions", false, "показать сохранённые сессии и выйти")
	printSession := flag.String("print-session", "", "показать весь лог сессии и выйти (офлайн, без модели)")
	flag.Parse()

	strategy, err := ParseStrategy(*strategyFlag)
	if err != nil {
		fatalf("%v", err)
	}
	window := *windowFlag
	if strategy.needsWindow() && window <= 0 {
		fatalf("стратегии %s нужен размер окна: -window N (сколько последних сообщений отправлять модели)", strategy)
	}

	dataDir := *dataFlag
	if dataDir == "" {
		dataDir = defaultDataDir()
	}

	if *listModels {
		printModels()
		return
	}
	if *listSessions {
		printSessions(dataDir)
		return
	}
	if *printSession != "" {
		id := *printSession
		if id == "last" {
			var err error
			if id, err = LatestSession(dataDir); err != nil {
				fatalf("%v", err)
			}
		}
		if err := PrintSessionLog(dataDir, id, os.Stdout); err != nil {
			fatalf("%v", err)
		}
		return
	}

	system := DefaultSystemPrompt
	if *systemFlag != "" {
		b, err := os.ReadFile(*systemFlag)
		if err != nil {
			fatalf("system prompt: %v", err)
		}
		system = strings.TrimSpace(string(b))
	}

	tasks, err := loadTasks(*tasksFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: список задач не загружен (%v); find_task будет пустым\n", err)
	}

	client := NewChatClient()
	if client.baseURL == "" || client.apiKey == "" {
		fatalf("нужны переменные окружения LLM_BASE_URL и OPENAI_API_KEY")
	}
	client.SetMaxTokens(*maxTokens)

	a := &app{
		client:       client,
		tools:        NewToolRegistry(tasks),
		dataDir:      dataDir,
		system:       system,
		budget:       Budget{MaxPromptTokens: *maxPrompt, ReserveOutput: *reserve, MinTailTokens: *minTail},
		maxSteps:     *maxSteps,
		temperature:  *temp,
		maxToolChars: *maxToolChars,
		noCompact:    *noCompact,
		compactEvery: *compactEvery,
		strategy:     strategy,
		window:       window,
		ui:           &ui{tty: isTerminal(os.Stdout)},
	}

	sessionID := *sessionFlag
	if *continueFlag {
		id, err := LatestSession(dataDir)
		if err != nil {
			fatalf("%v", err)
		}
		sessionID = id
	}
	if err := a.newSession(sessionID, *modelFlag, *summaryFlag); err != nil {
		fatalf("%v", err)
	}
	defer a.hist.Close()

	if *scriptFlag != "" {
		if err := a.runScript(*scriptFlag); err != nil {
			fatalf("%v", err)
		}
		return
	}
	a.repl()
}

// newSession opens (or creates) a session and rebuilds the agent around it.
func (a *app) newSession(id, requestedChat, requestedSummary string) error {
	if a.hist != nil {
		_ = a.hist.Close()
	}
	h, err := OpenHistory(a.dataDir, id, requestedChat, requestedSummary, a.system)
	if err != nil {
		return err
	}
	a.hist = h
	a.agent = NewAgent(a.client, a.tools, h, a.budget, a.maxSteps, a.temperature, a.maxToolChars)
	a.agent.SetTrace(a.ui.trace)
	a.agent.SetStreamUI(a.ui.rearm, a.ui.content, func() { a.ui.endLine() })
	a.agent.SetNoCompact(a.noCompact)
	a.agent.SetCompactEvery(a.compactEvery)
	a.agent.SetStrategy(a.strategy)
	a.agent.SetWindow(a.window)
	return nil
}

func (a *app) repl() {
	mode := fmt.Sprintf(" · стратегия %s", a.strategy)
	if a.strategy.needsWindow() {
		mode += fmt.Sprintf(" (%d сообщ.)", a.window)
	}
	fmt.Printf("сессия %s · модель %s · сжатие %s%s\n/help — команды: /model — модель, /session — сессия, /branch — ветки, /exit — выход; статистика — после каждого ответа\n",
		a.hist.ID(), a.hist.ChatModel(), a.hist.SummaryModel(), mode)
	if envModel := strings.TrimSpace(os.Getenv("LLM_MODEL")); envModel != "" && envModel != a.hist.ChatModel() {
		fmt.Printf("внимание: сессия сохранена с моделью %s, а LLM_MODEL=%s; переключить: /model %s\n",
			a.hist.ChatModel(), envModel, envModel)
	}

	ed := newLineEditor(isTerminal(os.Stdin), filepath.Join(a.dataDir, "history"))
	for {
		line, err := ed.read("agent> ")
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			exit, err := a.command(line)
			if err != nil {
				fmt.Println("ошибка:", err)
			}
			if exit {
				break
			}
			continue
		}
		a.ask(line)
	}
	fmt.Println()
}

func (a *app) ask(text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	done := make(chan struct{})
	a.ui.beginTurn()

	if a.ui.tty {
		// One-character spinner on the line under the prompt; the streamed
		// answer replaces it as soon as the first token arrives.
		stop := a.ui.begin(done)
		answer, err := a.agent.Chat(ctx, text)
		close(done)
		if stop != nil {
			<-stop
		}
		streamed := a.ui.didStream()
		a.ui.endLine()
		if err != nil {
			fmt.Println("ошибка:", err)
			return
		}
		if !streamed {
			fmt.Println()
			fmt.Println(answer)
		}
		a.footer()
		return
	}

	// Non-TTY (script mode): no spinner, deltas still stream to stdout; the
	// ticker only speaks while nothing has arrived yet.
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		start := time.Now()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if a.ui.didStream() {
					continue
				}
				fmt.Printf("  … ждём ответа (%d с)\n", int(time.Since(start).Seconds()))
			}
		}
	}()

	answer, err := a.agent.Chat(ctx, text)
	close(done)
	streamed := a.ui.didStream()
	a.ui.endLine()
	if err != nil {
		fmt.Println("ошибка:", err)
		return
	}
	if !streamed {
		fmt.Println()
		fmt.Println(answer)
	}
	a.footer()
}

// footer is the one-line stats line printed after every turn: model, prompt
// estimate vs budget, last answer size, active/archive split and accumulated
// API cost. An overflowing prompt is flagged explicitly.
func (a *app) footer() {
	activeMsgs, _, archivedMsgs, _ := a.hist.Stats()
	extra := ""
	if a.hist.HasSummary() {
		extra = fmt.Sprintf(" · конспект ~%d ток", EstimateTokens(a.hist.SummaryText()))
	}
	if a.strategy.needsWindow() {
		extra += fmt.Sprintf(" · %s(%d)", a.strategy, a.window)
	}
	prompt := a.agent.comp.PromptTokens(a.hist.SystemPrompt())
	over := ""
	if prompt > a.budget.MaxPromptTokens {
		over = " · ПЕРЕПОЛНЕНИЕ"
	}
	s := a.client.Stats()
	fmt.Printf("· %s · промпт ~%d/%d ток · ответ ~%d ток · актив %d, архив %d сообщ%s · API %d выз., $%.4f%s\n",
		a.hist.ChatModel(),
		prompt, a.budget.MaxPromptTokens,
		a.agent.LastAnswerTokens(),
		activeMsgs, archivedMsgs, extra, s.Calls, s.Cost, over)
}

// describeStrategy prints the active context strategy in one line.
func describeStrategy(s Strategy, window int) {
	switch s {
	case StrategySummary:
		fmt.Println("стратегия контекста: summary — старые ходы сворачиваются в конспект моделью сжатия")
	case StrategyWindow:
		fmt.Printf("стратегия контекста: window — в промпте только последние %d сообщений, остальное вытесняется в архив без конспекта\n", window)
	case StrategyFacts:
		fmt.Printf("стратегия контекста: facts — блок «ключ: значение» (обновляется после каждого сообщения) + последние %d сообщений\n", window)
	}
}

func (a *app) command(line string) (bool, error) {
	fields := strings.Fields(line)
	cmd := strings.ToLower(fields[0])
	arg := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))

	switch cmd {
	case "/exit", "/quit", "/q":
		return true, nil

	case "/help":
		printHelp()

	case "/new":
		chat, summary := a.hist.ChatModel(), a.hist.SummaryModel()
		if err := a.newSession("", chat, summary); err != nil {
			return false, err
		}
		fmt.Println("новая сессия:", a.hist.ID())

	case "/session":
		if arg == "" {
			a.printSessionList()
			return false, nil
		}
		id := arg
		infos, err := SummarizeSessions(a.dataDir)
		if err != nil {
			return false, err
		}
		exists := false
		for _, inf := range infos {
			if inf.ID == id {
				exists = true
				break
			}
		}
		if !exists {
			return false, fmt.Errorf("нет сессии %s; /session без аргумента — список сессий", id)
		}
		if id == a.hist.ID() {
			fmt.Println("уже в этой сессии:", id)
			return false, nil
		}
		if err := a.newSession(id, "", ""); err != nil {
			return false, err
		}
		activeMsgs, _, archivedMsgs, _ := a.hist.Stats()
		fmt.Printf("сессия: %s · модель %s · сжатие %s · сообщений %d (актив %d, архив %d)\n",
			a.hist.ID(), a.hist.ChatModel(), a.hist.SummaryModel(), activeMsgs+archivedMsgs, activeMsgs, archivedMsgs)

	case "/model":
		if arg == "" {
			printModels()
			fmt.Println("текущая чат-модель:", a.hist.ChatModel())
			fmt.Printf("переключить: /model <id>, например /model %s\n", ModelSet[0].ID)
			return false, nil
		}
		if _, ok := FindModel(arg); !ok {
			return false, fmt.Errorf("неизвестная модель %q; доступны: %s", arg, strings.Join(ModelIDs(), ", "))
		}
		a.hist.SetChatModel(arg)
		fmt.Println("чат-модель:", arg)

	case "/summary":
		if arg == "" {
			printModels()
			fmt.Println("текущая модель сжатия:", a.hist.SummaryModel())
			fmt.Println("переключить: /summary <id>; вступит в силу со следующего сжатия, /compact — сжать сейчас")
			return false, nil
		}
		if _, ok := FindModel(arg); !ok {
			return false, fmt.Errorf("неизвестная модель %q; доступны: %s", arg, strings.Join(ModelIDs(), ", "))
		}
		if arg == a.hist.SummaryModel() {
			fmt.Println("модель сжатия уже:", arg)
			return false, nil
		}
		a.hist.SetSummaryModel(arg)
		fmt.Println("модель сжатия:", arg, "— вступит в силу со следующего сжатия (/compact — сжать сейчас)")

	case "/strategy":
		if arg == "" {
			describeStrategy(a.strategy, a.window)
			fmt.Println("переключить: /strategy summary | window | facts [N]")
			return false, nil
		}
		fields := strings.Fields(arg)
		st, err := ParseStrategy(fields[0])
		if err != nil {
			return false, err
		}
		win := a.window
		if len(fields) > 1 {
			n, perr := strconv.Atoi(fields[1])
			if perr != nil || n <= 0 {
				return false, fmt.Errorf("размер окна — целое число > 0: /strategy %s N", st)
			}
			win = n
		}
		if st.needsWindow() && win <= 0 {
			return false, fmt.Errorf("стратегии %s нужен размер окна: /strategy %s N", st, st)
		}
		a.strategy = st
		a.window = win
		a.agent.SetStrategy(st)
		a.agent.SetWindow(win)
		describeStrategy(a.strategy, a.window)

	case "/facts":
		facts := a.hist.Facts()
		if len(facts) == 0 {
			fmt.Println("фактов нет; стратегия facts наполняет их после каждого сообщения")
			return false, nil
		}
		fmt.Printf("факты (%d ключей, ~%d ток.):\n", len(facts), EstimateTokens(renderFacts(facts)))
		fmt.Println(renderFacts(facts))

	case "/branch":
		if arg == "" {
			if p := a.hist.ForkOf(); p != "" {
				fmt.Printf("родитель: %s (чекпоинт seq %d)\n", p, a.hist.ForkSeq())
			}
			kids, err := ListForks(a.dataDir, a.hist.ID())
			if err != nil {
				return false, err
			}
			if len(kids) == 0 {
				fmt.Println("веток от этой сессии пока нет")
			} else {
				fmt.Println("ветки от текущей сессии:")
				for _, k := range kids {
					marker := ""
					if k == a.hist.ID() {
						marker = " (вы здесь)"
					}
					fmt.Println("  ·", k+marker)
				}
			}
			fmt.Println("создать ветку от текущего места: /branch <имя>; переключиться: /branch <имя существующей>")
			return false, nil
		}
		name := strings.Fields(arg)[0]
		if name == a.hist.ID() {
			fmt.Println("уже в этой сессии:", name)
			return false, nil
		}
		exists := false
		if infos, err := SummarizeSessions(a.dataDir); err == nil {
			for _, inf := range infos {
				if inf.ID == name {
					exists = true
					break
				}
			}
		}
		if !exists {
			id, err := ForkSession(a.dataDir, a.hist.ID(), name)
			if err != nil {
				return false, err
			}
			fmt.Printf("чекпоинт: ветка %s создана от %s (seq %d); продолжить в ней: /branch %s\n",
				id, a.hist.ID(), a.hist.LastSeq(), id)
			return false, nil
		}
		if err := a.newSession(name, "", ""); err != nil {
			return false, err
		}
		line := fmt.Sprintf("сессия: %s · модель %s · сжатие %s", a.hist.ID(), a.hist.ChatModel(), a.hist.SummaryModel())
		if p := a.hist.ForkOf(); p != "" {
			line += fmt.Sprintf(" · ветка от %s (seq %d)", p, a.hist.ForkSeq())
		}
		activeMsgs, _, archivedMsgs, _ := a.hist.Stats()
		fmt.Printf("%s · сообщений %d (актив %d, архив %d)\n", line, activeMsgs+archivedMsgs, activeMsgs, archivedMsgs)

	case "/system":
		fmt.Println(a.hist.SystemPrompt())

	case "/tools":
		for _, s := range a.tools.Describe() {
			fmt.Println("•", s)
		}

	case "/history":
		a.printHistory()

	case "/context":
		a.printContext()

	case "/tokens":
		a.printTokens()

	case "/compact":
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		ok, err := a.agent.comp.ForceCompact(ctx, a.hist.SystemPrompt())
		if err != nil {
			return false, err
		}
		if !ok {
			fmt.Println("сжимать нечего: история слишком короткая")
		}
		a.printTokens()

	case "/recall":
		if arg == "" {
			return false, fmt.Errorf("использование: /recall <текст>")
		}
		hits := a.hist.SearchArchived(arg)
		if len(hits) == 0 {
			fmt.Println("в архиве ничего не найдено")
			return false, nil
		}
		for i, m := range hits {
			if i == 10 {
				fmt.Println("…")
				break
			}
			fmt.Printf("[%d] %s: %s\n", m.Seq, m.Role, oneLine(m.Content))
		}

	default:
		fmt.Println("неизвестная команда; /help — список команд")
	}
	return false, nil
}

func (a *app) printHistory() {
	msgs := a.hist.Active()
	start := 0
	if len(msgs) > 20 {
		start = len(msgs) - 20
	}
	for _, m := range msgs[start:] {
		tok := m.Tokens
		if tok == 0 {
			tok = EstimateMessage(*m)
		}
		fmt.Printf("[%d] %-9s ~%-5d ток. %s\n", m.Seq, m.Role, tok, oneLine(m.Content))
	}
	if a.hist.HasSummary() {
		fmt.Println("(плюс конспект предыдущего диалога)")
	}
}

// printContext shows what the next request will actually carry: the system
// prompt, the rolling summary, the verbatim tail and the tool schemas, with a
// token estimate per block. It is the "what did compression do" view.
func (a *app) printContext() {
	promptMsgs := a.agent.comp.promptWindow()
	base := EstimateTokens(a.agent.comp.promptSystem(a.hist.SystemPrompt()))
	factsTok := 0
	if facts := a.hist.Facts(); len(facts) > 0 {
		factsTok = EstimateTokens("Факты диалога (память о договорённостях):\n" + renderFacts(facts))
	}
	histTok := 0
	for _, m := range promptMsgs {
		msg := *m
		if msg.Role == "tool" {
			msg.Content = truncate(msg.Content, a.maxToolChars)
		}
		histTok += EstimateMessage(msg)
	}
	schemas := a.tools.Schemas()
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		names = append(names, s.Function.Name)
	}
	toolsTok := 0
	if len(schemas) > 0 {
		if b, err := json.Marshal(schemas); err == nil {
			toolsTok = EstimateTokens(string(b))
		}
	}

	fmt.Printf("что уходит в модель (сессия %s, модель %s, стратегия %s):\n", a.hist.ID(), a.hist.ChatModel(), a.strategy)
	fmt.Printf("  system prompt        ~%d ток.\n", base)
	switch a.strategy {
	case StrategyFacts:
		if factsTok > 0 {
			fmt.Printf("  факты                ~%d ток. (%d ключей)\n", factsTok, len(a.hist.Facts()))
		} else {
			fmt.Println("  факты                нет")
		}
		fmt.Printf("  окно                 последние %d сообщ.\n", a.window)
	case StrategyWindow:
		fmt.Printf("  окно                 последние %d сообщ. (без конспекта — стратегия window)\n", a.window)
	default:
		if a.hist.HasSummary() {
			cov := a.hist.SummaryCovers()
			sumTok := EstimateTokens("Конспект предыдущего диалога:\n"+a.hist.SummaryText()) + 4
			fmt.Printf("  конспект seq %d..%d   ~%d ток.\n", cov[0], cov[1], sumTok)
		} else {
			fmt.Println("  конспект             нет")
		}
	}
	if len(promptMsgs) > 0 {
		fmt.Printf("  история дословно     ~%d ток. (%d сообщ., seq %d..%d)\n",
			histTok, len(promptMsgs), promptMsgs[0].Seq, promptMsgs[len(promptMsgs)-1].Seq)
	} else {
		fmt.Println("  история дословно     пусто")
	}
	fmt.Printf("  инструменты          ~%d ток. (%s)\n", toolsTok, strings.Join(names, ", "))
	fmt.Printf("  ─────────────────────────────────────────\n")
	fmt.Printf("  итого                ~%d ток. из %d (калибровка ×%.2f)\n",
		a.agent.comp.PromptTokens(a.hist.SystemPrompt()), a.budget.MaxPromptTokens, a.agent.calib.Ratio())

	switch a.strategy {
	case StrategyWindow, StrategyFacts:
		fmt.Printf("  вытеснение           всё старше %d сообщений → архив (без конспекта)\n", a.window)
	case StrategySummary:
		if a.noCompact {
			fmt.Println("  авто-сжатие отключено (-no-compact)")
		} else {
			limit := a.budget.MaxPromptTokens - a.budget.ReserveOutput
			if limit < 500 {
				limit = 500
			}
			fmt.Printf("  сжатие начнётся при  %d ток.", limit*85/100)
			if a.compactEvery > 0 {
				fmt.Printf(" или %d сообщ.", a.compactEvery)
			}
			fmt.Println()
		}
	}

	if a.strategy == StrategySummary && a.hist.HasSummary() {
		fmt.Println("\nконспект вместо старой истории:")
		for _, line := range strings.Split(strings.TrimSpace(a.hist.SummaryText()), "\n") {
			fmt.Println("  " + line)
		}
	}
	if a.strategy == StrategyFacts && len(a.hist.Facts()) > 0 {
		fmt.Println("\nфакты вместо старой истории:")
		fmt.Println(indentBlock(renderFacts(a.hist.Facts()), "  "))
	}
}

// indentBlock prefixes every line of s with prefix; used for the facts block
// in /context.
func indentBlock(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func (a *app) printTokens() {
	activeMsgs, activeTokens, archivedMsgs, archivedTokens := a.hist.Stats()
	fmt.Printf("активное окно: %d сообщений, ~%d ток.\n", activeMsgs, activeTokens)
	fmt.Printf("архив: %d сообщений, ~%d ток.\n", archivedMsgs, archivedTokens)
	if a.hist.HasSummary() {
		covers := a.hist.SummaryCovers()
		fmt.Printf("конспект: ~%d ток., покрывает seq %d..%d\n",
			EstimateTokens(a.hist.SummaryText()), covers[0], covers[1])
	}
	if facts := a.hist.Facts(); len(facts) > 0 {
		fmt.Printf("факты: %d ключей, ~%d ток. (стратегия facts)\n", len(facts), EstimateTokens(renderFacts(facts)))
	}
	if a.strategy.needsWindow() {
		fmt.Printf("стратегия %s: в промпт идут последние %d сообщений, старое вытесняется в архив без конспекта\n", a.strategy, a.window)
	}
	fmt.Printf("оценка промпта: ~%d ток. (бюджет %d, калибровка ×%.2f)\n",
		a.agent.comp.PromptTokens(a.hist.SystemPrompt()), a.budget.MaxPromptTokens, a.agent.calib.Ratio())
	fmt.Printf("модель сжатия: %s\n", a.hist.SummaryModel())

	s := a.client.Stats()
	fmt.Printf("API: %d вызовов, промпт %d, ответ %d, всего %d ток., $%.4f\n",
		s.Calls, s.PromptTokens, s.CompletionTokens, s.TotalTokens, s.Cost)
}

func (a *app) runScript(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fmt.Printf("agent> %s\n", line)
		a.ask(line)
	}
	return sc.Err()
}

// printSessionList shows all stored sessions with the current one marked.
func (a *app) printSessionList() {
	infos, err := SummarizeSessions(a.dataDir)
	if err != nil {
		fmt.Println("ошибка:", err)
		return
	}
	if len(infos) == 0 {
		fmt.Println("сессий пока нет")
		return
	}
	fmt.Println("сессии (сначала новые):")
	for _, inf := range infos {
		mark := " "
		if inf.ID == a.hist.ID() {
			mark = "*"
		}
		fmt.Printf(" %s %s · %s · модель %s · сжатие %s\n", mark, inf.ID, inf.Created.Format("02.01 15:04"), inf.ChatModel, inf.SummaryModel)
	}
	fmt.Println("* — текущая; переключиться: /session <id>")
}

func printModels() {
	fmt.Println("Модели — id это имя в первом столбце:")
	for _, m := range ModelSet {
		mark := " "
		if m.ID == DefaultModel() {
			mark = "*"
		}
		fmt.Printf(" %s %-22s %-10s $%.4f/1M\n", mark, m.ID, m.Tier, m.RatePerM)
	}
	fmt.Println("(* — по умолчанию)")
}

func printSessions(dataDir string) {
	infos, err := SummarizeSessions(dataDir)
	if err != nil {
		fatalf("%v", err)
	}
	if len(infos) == 0 {
		fmt.Println("сессий пока нет")
		return
	}
	for _, inf := range infos {
		fmt.Printf("%s · %s · модель %s · сжатие %s · сообщений %d\n",
			inf.ID, inf.Created.Format("02.01 15:04"), inf.ChatModel, inf.SummaryModel, inf.Msgs)
	}
}

const helpText = `Команды:
  /help            эта справка
  /exit            выход (также /quit)
  /new             новая сессия
  /session [id]    список сессий или переключение на сессию
  /model [id]      список моделей или смена чат-модели
  /summary [id]    модель сжатия: список или смена
  /strategy [имя [N]]
                   стратегия контекста: summary (конспект), window (последние
                   N сообщений), facts (ключ-значение + последние N); без
                   аргумента — текущая; N — размер окна для window/facts
  /facts           память «ключ-значение» стратегии facts
  /branch [имя]    ветки диалога: без аргумента — список, /branch <имя> —
                   чекпоинт (создать ветку от текущего места), повторный
                   /branch <существующее имя> — переключение
  /system          показать системный промпт
  /tools           список инструментов
  /history         последние сообщения активного окна
  /context         что реально уходит в модель: system, память, хвост, tools
  /tokens          токены, архив, память, расходы API
  /compact         принудительно сжать контекст (стратегия summary)
  /recall <текст>  поиск по архиву

Полный лог сессии (все сообщения, архив, сжатия) — из оболочки, офлайн:
  ./task2-06 -print-session <id|last>

Ввод: ↑/↓ — история запросов (хранится между запусками), ←/→ — курсор,
Backspace, Ctrl-A/E — начало/конец строки, Ctrl-U/W — стереть строку/слово,
Ctrl-C — отменить ввод, Ctrl-D — выход.

Всё остальное отправляется модели.
`

func printHelp() {
	fmt.Print(helpText)
}

func defaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "goagent", "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "data/sessions"
	}
	return filepath.Join(home, ".local", "share", "goagent", "sessions")
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ошибка: "+format+"\n", args...)
	os.Exit(1)
}
