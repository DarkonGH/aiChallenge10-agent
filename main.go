package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/DarkonGH/aiChallenge10-agent/internal/agent"
	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/invariant"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/mcp"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
	"github.com/DarkonGH/aiChallenge10-agent/internal/readline"
	"github.com/DarkonGH/aiChallenge10-agent/internal/task"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// app wires the CLI, the agent and the session together.
type app struct {
	client       *llm.ChatClient
	tools        *tools.ToolRegistry
	dataDir      string
	system       string
	budget       compact.Budget
	maxSteps     int
	temperature  float64
	maxToolChars int
	noCompact    bool
	noMemory     bool
	compactEvery int
	strategy     compact.Strategy
	window       int
	noReasoning  bool
	profile      string        // active named profile; "" is the shared default
	skipProfile  bool          // no onboarding dialogue at startup (scripts, runs)
	scriptDelay  time.Duration // pause between dialogue turns in -script (rate limits)

	summary *llm.ChatClient // client for compaction; nil means the chat client

	// MCP is an optional runtime layer: one manager for every session. The
	// approval map is per session, because /new and /session start a new trust
	// scope even though the MCP server process stays alive.
	mcpClient   *mcp.Manager
	mcpApproval string
	mcpAlways   map[string]bool
	stdin       *bufio.Reader

	hist  *history.History
	agent *agent.Agent
	mem   *memory.Store
	task  *task.Machine // day-13 task state machine of the current session
	// Day-14 project invariants: one set per workspace, outside every session.
	inv        *invariant.Set
	invPath    string
	invChecker *invariant.Checker // local-model prose check; nil = keywords only
	ui         *ui

	// The cancellable context of the turn currently being answered. Ctrl-C
	// during a request reaches the signal handler (the line editor is not in
	// raw mode then) and cancels this context; Ctrl-D remains the exit command.
	turnMu     sync.Mutex
	turnCancel context.CancelFunc
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

	// The reasoning block: the model's hidden thinking is printed line by line
	// in grey under the prompt and erased before anything else is printed, so
	// the thinking neither stays on screen nor scrolls the console.
	reasonOpen bool
	reasonUp   int // screen lines the cursor moved down since the block start
	reasonCol  int // cells used on the last printed screen line
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
	u.closeReasoningLocked()
	u.spinning = true
	u.mu.Unlock()
}

// reasoning prints a chunk of the model's hidden thinking in grey under the
// prompt, wrapping at the terminal width. The block is erased by
// closeReasoningLocked as soon as anything else must be printed.
func (u *ui) reasoning(s string) {
	if s == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.tty {
		return
	}
	if u.spinning {
		fmt.Print("\r" + strings.Repeat(" ", len(spinLine)) + "\r")
		u.spinning = false
	}
	width := readline.TerminalWidth()
	if width < 24 {
		width = 24
	}
	u.reasonOpen = true
	text, col, down := wrapReason(u.reasonCol, width, s)
	u.reasonCol = col
	u.reasonUp += down
	fmt.Print("\x1b[90m" + text + "\x1b[0m")
}

// wrapReason inserts hard line breaks at the terminal width and reports the
// resulting column and how many screen lines the chunk moved the cursor down.
// The console must never wrap on its own: the erase logic counts exactly these
// breaks and nothing else.
func wrapReason(col, width int, s string) (string, int, int) {
	var b strings.Builder
	down := 0
	for _, r := range s {
		switch r {
		case '\r':
			continue
		case '\n':
			b.WriteByte('\n')
			down++
			col = 0
			continue
		case '\t':
			r = ' '
		}
		if col >= width {
			b.WriteByte('\n')
			down++
			col = 0
		}
		b.WriteRune(r)
		col++
	}
	return b.String(), col, down
}

// closeReasoningLocked erases the reasoning block and returns the cursor to
// the line under the prompt. The caller holds u.mu.
func (u *ui) closeReasoningLocked() {
	if !u.reasonOpen {
		return
	}
	if u.reasonUp > 0 {
		fmt.Printf("\x1b[%dA", u.reasonUp)
	}
	fmt.Print("\r\x1b[J")
	u.reasonOpen = false
	u.reasonUp = 0
	u.reasonCol = 0
}

// beginTurn marks the start of one ask() turn: nothing streamed yet.
func (u *ui) beginTurn() {
	u.mu.Lock()
	u.closeReasoningLocked()
	u.streamed = false
	u.sawStream = false
	u.mu.Unlock()
}

// content prints a streamed piece of the assistant answer, stopping the
// spinner on the first visible token and wiping the reasoning block.
func (u *ui) content(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.spinning {
		fmt.Print("\r" + strings.Repeat(" ", len(spinLine)) + "\r")
		u.spinning = false
	}
	u.closeReasoningLocked()
	u.streamed = true
	u.sawStream = true
	fmt.Print(s)
}

// endLine closes a streamed line if one is open and reports whether it did.
func (u *ui) endLine() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closeReasoningLocked()
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
	u.closeReasoningLocked()
	if u.streamed {
		fmt.Println()
		u.streamed = false
	}
	fmt.Println("  ↳ " + s)
	u.mu.Unlock()
}

// pauseSpinner stops the spinner and closes a streamed line before an
// interactive prompt. The approval question must not be overwritten by the
// spinner goroutine or glued to the end of a streamed answer.
func (u *ui) pauseSpinner() {
	u.mu.Lock()
	if u.spinning {
		fmt.Print("\r" + strings.Repeat(" ", len(spinLine)) + "\r")
		u.spinning = false
	}
	u.closeReasoningLocked()
	if u.streamed {
		fmt.Println()
		u.streamed = false
	}
	u.mu.Unlock()
}

// approveMCP is the code-level gate for external tools. The model proposes a
// call, this method decides whether it may run. In auto mode a real terminal
// gets asked; scripts and pipes deny by default unless -mcp-approval=allow.
func (a *app) approveMCP(req agent.ApprovalRequest) (bool, error) {
	switch a.mcpApproval {
	case "allow":
		return true, nil
	case "deny":
		return false, nil
	case "auto", "ask":
		if !isTerminal(os.Stdin) {
			return false, nil
		}
	default:
		return false, fmt.Errorf("неизвестная политика MCP %q", a.mcpApproval)
	}

	key := req.Server + "/" + req.Name
	if a.mcpAlways[key] {
		return true, nil
	}

	pretty := req.Args
	if pretty == "" {
		pretty = "{}"
	}
	var parsed any
	if json.Unmarshal([]byte(pretty), &parsed) == nil {
		if b, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			pretty = string(b)
		}
	}
	pretty = strings.ReplaceAll(pretty, "\n", "\n     ")
	short := strings.TrimPrefix(req.Name, "mcp_"+req.Server+"_")

	a.ui.pauseSpinner()
	fmt.Printf("\n  ⛓ MCP %s · %s\n     %s\n     разрешить? [y] да / [n] нет / [a] всегда в этой сессии: ", req.Server, short, pretty)
	line, err := a.stdin.ReadString('\n')
	if err != nil {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "д", "да":
		return true, nil
	case "a", "always", "в", "всегда":
		a.mcpAlways[key] = true
		return true, nil
	default:
		return false, nil
	}
}

func main() {
	modelFlag := flag.String("model", "", "чат-модель (по умолчанию LLM_MODEL или opencode-go/deepseek-v4.1-flash)")
	summaryFlag := flag.String("summary-model", "", "модель для сжатия контекста (по умолчанию LLM_SUMMARY_MODEL или самая дешёвая в списке)")
	sessionFlag := flag.String("session", "", "id сессии")
	continueFlag := flag.Bool("continue", false, "продолжить последнюю сессию")
	dataFlag := flag.String("data", "", "каталог сессий")
	tasksFlag := flag.String("tasks", "tasks.txt", "список загадок для find_riddle")
	maxSteps := flag.Int("max-steps", 8, "максимум шагов с инструментами на один запрос")
	maxPrompt := flag.Int("max-prompt-tokens", 6000, "бюджет токенов промпта")
	reserve := flag.Int("reserve-output", 1024, "токены, зарезервированные под ответ")
	minTail := flag.Int("min-tail-tokens", 1200, "сколько свежей истории держать дословно")
	temp := flag.Float64("temperature", 0.2, "температура сэмплирования")
	maxTokens := flag.Int("max-tokens", 4096, "лимит токенов ответа (включая рассуждения reasoning-моделей)")
	maxToolChars := flag.Int("max-tool-chars", 800, "сколько символов результата инструмента оставлять в промпте")
	noCompact := flag.Bool("no-compact", false, "отключить авто-сжатие контекста (демо переполнения)")
	noMemory := flag.Bool("no-memory", false, "не обновлять слоистую память (история и /memory остаются)")
	compactEvery := flag.Int("compact-every", 0, "сжимать историю после каждых N новых сообщений с прошлого сжатия (0 — только по бюджету)")
	strategyFlag := flag.String("strategy", string(compact.StrategySummary), "стратегия контекста: summary (конспект), window (последние N сообщений), facts (ключ-значение + окно)")
	windowFlag := flag.Int("window", 0, "размер окна для стратегий window и facts (обязателен для них)")
	noReasoning := flag.Bool("no-reasoning", false, "не печатать блок рассуждений модели (для записи демо)")
	systemFlag := flag.String("system", "", "файл с системным промптом")
	scriptFlag := flag.String("script", "", "проиграть запросы и команды (/memory и др.) из файла и выйти")
	listModels := flag.Bool("list-models", false, "показать список моделей и выйти")
	listSessions := flag.Bool("list-sessions", false, "показать сохранённые сессии и выйти")
	printSession := flag.String("print-session", "", "показать весь лог сессии и выйти (офлайн, без модели)")
	memoryReport := flag.String("memory-report", "", "показать слои памяти сессии и токены по слоям (офлайн)")
	importMemory := flag.String("import-memory", "", "разложить факты дня 10 из сессии в слои памяти текущей -session")
	profileFlag := flag.String("profile", "", "профиль пользователя: имя каталога в <data>/profiles (пусто — общий <data>/shared)")
	skipProfile := flag.Bool("skip-profile", false, "не спрашивать профиль при старте (сценарии и прогоны)")
	scriptDelay := flag.Duration("script-delay", 0, "пауза между репликами в -script (для лимитов провайдера, например 30s)")
	invariantsFlag := flag.String("invariants", "", "файл инвариантов проекта (по умолчанию <data>/invariants.json)")
	invariantCheck := flag.String("invariant-check", "llm", "проверка текста ответа: llm (локальная модель + ключевые слова) или keywords")
	invariantModel := flag.String("invariant-model", "", "модель семантической проверки (по умолчанию LLM_INVARIANT_MODEL / LLM_SUMMARY_MODEL)")
	mcpConfig := flag.String("mcp-config", "", "путь к mcp.json (по умолчанию MCP_CONFIG, ./mcp.json, ~/.config/goagent/mcp.json, <data>/mcp.json)")
	noMCP := flag.Bool("no-mcp", false, "не подключать MCP-серверы")
	mcpApproval := flag.String("mcp-approval", "auto", "подтверждение MCP-инструментов: auto (ask в TTY, deny в script), allow или deny")
	mcpGuard := flag.String("mcp-guard", "heuristic", "проверка результата MCP: off, heuristic (эвристики) или model (модель сжатия)")
	mcpGuardModel := flag.String("mcp-guard-model", "", "модель проверки MCP (по умолчанию модель сжатия)")
	mcpActionGuard := flag.String("mcp-action-guard", "off", "проверка намерения MCP моделью: off или model")
	dumpRequest := flag.String("dump-request", "", "писать тела запросов к чат-модели в NDJSON-файл")
	flag.Parse()

	if *mcpApproval != "auto" && *mcpApproval != "allow" && *mcpApproval != "deny" && *mcpApproval != "ask" {
		fatalf("-mcp-approval: допустимо auto, allow, ask или deny, получено %q", *mcpApproval)
	}
	if _, err := mcp.ParseGuardMode(*mcpGuard); err != nil {
		fatalf("-mcp-guard: %v", err)
	}
	if *mcpActionGuard != "off" && *mcpActionGuard != "model" {
		fatalf("-mcp-action-guard: допустимо off или model, получено %q", *mcpActionGuard)
	}

	if *invariantCheck != "llm" && *invariantCheck != "keywords" {
		fatalf("-invariant-check: допустимо llm или keywords, получено %q", *invariantCheck)
	}

	strategy, err := compact.ParseStrategy(*strategyFlag)
	if err != nil {
		fatalf("%v", err)
	}
	window := *windowFlag
	if strategy.NeedsWindow() && window <= 0 {
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
			if id, err = history.LatestSession(dataDir); err != nil {
				fatalf("%v", err)
			}
		}
		if err := history.PrintSessionLog(dataDir, id, os.Stdout); err != nil {
			fatalf("%v", err)
		}
		return
	}
	if *memoryReport != "" {
		id := *memoryReport
		if id == "last" {
			var err error
			if id, err = history.LatestSession(dataDir); err != nil {
				fatalf("%v", err)
			}
		}
		if err := memoryReportCmd(dataDir, id, profileDirFor(dataDir, *profileFlag)); err != nil {
			fatalf("%v", err)
		}
		return
	}
	if *importMemory != "" {
		// The import is model-based, so this offline branch needs a client of
		// its own: it runs before the interactive app is built.
		model := *summaryFlag
		if model == "" {
			model = *modelFlag
		}
		if model == "" {
			model = config.DefaultSummaryModel()
		}
		client := llm.NewChatClient()
		client.SetMaxTokens(*maxTokens)
		if err := importMemoryCmd(context.Background(), client, model, dataDir, *importMemory, *sessionFlag, profileDirFor(dataDir, *profileFlag)); err != nil {
			fatalf("%v", err)
		}
		return
	}

	system := config.DefaultSystemPrompt
	if *systemFlag != "" {
		b, err := os.ReadFile(*systemFlag)
		if err != nil {
			fatalf("system prompt: %v", err)
		}
		system = strings.TrimSpace(string(b))
	}

	// The invariant set is a workspace file, not part of any session: /new,
	// /session and a fresh log leave it untouched. Loading it before the app
	// also fails fast on a malformed file instead of mid-dialogue.
	invariantsPath := *invariantsFlag
	if invariantsPath == "" {
		invariantsPath = filepath.Join(dataDir, "invariants.json")
	}
	inv, err := invariant.Load(invariantsPath)
	if err != nil {
		fatalf("%v", err)
	}
	if inv.Active() {
		fmt.Printf("инварианты: %d правил (%s)\n", inv.Len(), invariantsPath)
	}

	tasks, err := config.LoadTasks(*tasksFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "предупреждение: список загадок не загружен (%v); find_riddle будет пустым\n", err)
	}

	client := llm.NewChatClient()
	if !client.Configured() {
		fatalf("нужны переменные окружения LLM_BASE_URL и OPENAI_API_KEY")
	}
	client.SetMaxTokens(*maxTokens)

	// MCP is discovered and connected after the offline branches: listing
	// sessions or printing a log must not spawn external processes. A missing
	// config simply means "local tools only"; an explicit broken config is an
	// error, and -no-mcp always wins.
	var mcpManager *mcp.Manager
	if !*noMCP {
		configPath, err := mcp.FindConfig(*mcpConfig, dataDir)
		if err != nil {
			fatalf("%v (отключить MCP: -no-mcp)", err)
		}
		if configPath != "" {
			cfg, err := mcp.Load(configPath)
			if err != nil {
				fatalf("%v (отключить MCP: -no-mcp)", err)
			}
			mcpManager, err = mcp.Start(context.Background(), cfg)
			if err != nil {
				fatalf("%v (отключить MCP: -no-mcp)", err)
			}
			defer mcpManager.Close()
			for _, line := range mcpManager.StatusLines() {
				fmt.Println(line)
			}
			for _, warning := range mcpManager.Warnings() {
				fmt.Fprintf(os.Stderr, "MCP: %s\n", warning)
			}
		}
	}

	if *dumpRequest != "" {
		f, err := os.Create(*dumpRequest)
		if err != nil {
			fatalf("dump-request: %v", err)
		}
		defer f.Close()
		var dumpMu sync.Mutex
		client.SetRequestHook(func(model string, payload []byte) {
			dumpMu.Lock()
			defer dumpMu.Unlock()
			_, _ = f.Write(payload)
			_, _ = f.Write([]byte("\n"))
		})
	}

	// Compaction may live on another endpoint (local Ollama). Without
	// LLM_SUMMARY_BASE_URL this is the same client as chat.
	summaryClient := llm.NewSummaryClient()
	summaryClient.SetMaxTokens(*maxTokens)
	if summaryClient != client {
		fmt.Printf("сжатие: %s\n", llm.DescribeEndpoint(summaryClient))
	}

	a := &app{
		client:       client,
		tools:        tools.NewToolRegistry(tasks),
		dataDir:      dataDir,
		system:       system,
		budget:       compact.Budget{MaxPromptTokens: *maxPrompt, ReserveOutput: *reserve, MinTailTokens: *minTail},
		maxSteps:     *maxSteps,
		temperature:  *temp,
		maxToolChars: *maxToolChars,
		noCompact:    *noCompact,
		noMemory:     *noMemory,
		compactEvery: *compactEvery,
		noReasoning:  *noReasoning,
		strategy:     strategy,
		window:       window,
		profile:      *profileFlag,
		skipProfile:  *skipProfile,
		scriptDelay:  *scriptDelay,
		summary:      summaryClient,
		inv:          inv,
		invPath:      invariantsPath,
		mcpClient:    mcpManager,
		mcpApproval:  *mcpApproval,
		mcpAlways:    map[string]bool{},
		stdin:        bufio.NewReader(os.Stdin),
		ui:           &ui{tty: isTerminal(os.Stdout)},
	}
	if mcpManager != nil {
		mcpManager.Register(a.tools)
	}
	// The task machine is per-session, so the registry holds a provider, not
	// a pointer: the task_transition tool always acts on the current session.
	a.tools.SetTaskProvider(func() *task.Machine { return a.task })
	// Invariants are the opposite: one workspace set, shared by every session.
	a.tools.SetInvariantProvider(func() *invariant.Set { return a.inv })
	// Ctrl-C while the agent is answering cancels that turn; at the prompt the
	// line editor consumes it as "clear the line", and Ctrl-D still exits.
	readline.SetInterruptHandler(a.interruptTurn)

	if *invariantCheck == "llm" {
		model := *invariantModel
		if model == "" {
			model = os.Getenv("LLM_INVARIANT_MODEL")
		}
		if model == "" {
			model = os.Getenv("LLM_SUMMARY_MODEL")
		}
		// The checker reuses the summary endpoint: locally it is Ollama, so
		// the extra per-answer call costs no money. The fallback resolves the
		// session's summary model after newSession.
		a.invChecker = invariant.NewChecker(summaryClient, func() string {
			if model != "" {
				return model
			}
			if a.hist != nil {
				return a.hist.SummaryModel()
			}
			return config.DefaultSummaryModel()
		})
	}

	sessionID := *sessionFlag
	if *continueFlag {
		id, err := history.LatestSession(dataDir)
		if err != nil {
			fatalf("%v", err)
		}
		sessionID = id
	}
	if err := a.newSession(sessionID, *modelFlag, *summaryFlag); err != nil {
		fatalf("%v", err)
	}
	defer a.hist.Close()

	if mcpManager != nil {
		guardMode, _ := mcp.ParseGuardMode(*mcpGuard)
		if guardMode == mcp.GuardModel && !summaryClient.Configured() {
			fatalf("MCP guard: для -mcp-guard=model нужен настроенный эндпоинт модели сжатия")
		}
		guardModel := *mcpGuardModel
		if guardModel == "" {
			guardModel = a.hist.SummaryModel()
		}
		guard, err := mcp.NewGuard(*mcpGuard, summaryClient, guardModel)
		if err != nil {
			fatalf("%v", err)
		}
		guard.ActionGuard = *mcpActionGuard == "model"
		if guard.ActionGuard && !summaryClient.Configured() {
			fatalf("MCP action guard: нужен настроенный эндпоинт модели сжатия")
		}
		guard.OnResult = func(rec mcp.GuardRecord) {
			if a.hist == nil {
				return
			}
			_ = a.hist.AppendGuard(history.GuardRecord{
				Server:     rec.Server,
				Tool:       rec.Tool,
				Suspicious: rec.Suspicious,
				Blocked:    rec.Blocked,
				Reasons:    rec.Reasons,
				Model:      rec.Model,
			})
		}
		mcpManager.SetGuard(guard)
		if guard.ActionGuard {
			a.agent.SetActionGuard(func(ctx context.Context, userText string, req agent.ApprovalRequest) (bool, string, error) {
				return guard.CheckAction(ctx, userText, req.Server, req.Name, req.Args)
			})
		}
	}

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
	h, err := history.OpenHistory(a.dataDir, id, requestedChat, requestedSummary, a.system)
	if err != nil {
		return err
	}
	a.hist = h
	// Working and long-term memory live next to the session log but in their
	// own files: readable JSON, one file per layer group. The profile is shared
	// by every session that uses the same profile name, so a second task sees
	// the same user without any extra plumbing; -profile picks the file.
	mem, err := memory.Open(filepath.Join(a.dataDir, h.ID(), "memory"), a.profileDir())
	if err != nil {
		return err
	}
	a.mem = mem
	// The task state machine (day 13) lives next to the session log:
	// task.json in the session directory. Providers are closures over the app,
	// so /new and /session swap the machine and the tool and the prompt block
	// follow automatically.
	tsk, err := task.Load(filepath.Join(a.dataDir, h.ID()))
	if err != nil {
		return err
	}
	a.task = tsk
	a.agent = agent.NewAgent(a.client, a.tools, h, a.budget, a.maxSteps, a.temperature, a.maxToolChars)
	if !a.noMemory {
		a.agent.SetMemory(mem)
	}
	a.agent.SetTask(func() *task.Machine { return a.task })
	a.agent.SetInvariants(func() *invariant.Set { return a.inv })
	a.agent.SetInvariantChecker(a.invChecker)
	a.tools.SetInvariantCheckHook(a.agent.MarkChecked)
	// Day 15: the FSM reports the edge into done; the agent clears the working
	// card of the finished task and journals the removals.
	a.tools.SetTaskDoneHook(a.agent.TaskDone)
	a.agent.SetSummaryClient(a.summary)
	a.agent.SetTrace(a.ui.trace)
	a.agent.SetStreamUI(a.ui.rearm, a.ui.content, func() { a.ui.endLine() })
	// A new session is a new trust scope: "always" answers from the previous
	// session must not silently authorise MCP calls in this one.
	a.mcpAlways = map[string]bool{}
	a.agent.SetApprover(a.approveMCP)
	if !a.noReasoning {
		a.agent.SetReasoningUI(a.ui.reasoning)
	}
	a.agent.SetNoCompact(a.noCompact)
	a.agent.SetCompactEvery(a.compactEvery)
	a.agent.SetStrategy(a.strategy)
	a.agent.SetWindow(a.window)
	return nil
}

// profileDirFor maps a profile name to its directory: the day-11 shared
// profile keeps its place, a named profile gets its own folder under profiles.
func profileDirFor(dataDir, profile string) string {
	if profile == "" {
		return filepath.Join(dataDir, "shared")
	}
	return filepath.Join(dataDir, "profiles", profile)
}

// profileDir is profileDirFor for the running app.
func (a *app) profileDir() string { return profileDirFor(a.dataDir, a.profile) }

func (a *app) repl() {
	mode := fmt.Sprintf(" · стратегия %s", a.strategy)
	if a.strategy.NeedsWindow() {
		mode += fmt.Sprintf(" (%d сообщ.)", a.window)
	}
	fmt.Printf("сессия %s · модель %s · сжатие %s%s\n/help — команды: /model — модель, /session — сессия, /branch — ветки, /exit — выход; статистика — после каждого ответа\n",
		a.hist.ID(), a.hist.ChatModel(), a.hist.SummaryModel(), mode)
	if envModel := strings.TrimSpace(os.Getenv("LLM_MODEL")); envModel != "" && envModel != a.hist.ChatModel() {
		fmt.Printf("внимание: сессия сохранена с моделью %s, а LLM_MODEL=%s; переключить: /model %s\n",
			a.hist.ChatModel(), envModel, envModel)
	}
	ed := readline.NewLineEditor(isTerminal(os.Stdin), filepath.Join(a.dataDir, "history"))
	a.onboardProfile(ed)

	for {
		line, err := ed.Read("agent> ")
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

// armInterrupt makes the current turn cancellable from the SIGINT handler.
// Only one turn runs at a time (the REPL is sequential), so a single slot is
// enough; disarmInterrupt must be called before the next prompt.
func (a *app) armInterrupt(cancel context.CancelFunc) {
	a.turnMu.Lock()
	a.turnCancel = cancel
	a.turnMu.Unlock()
}

func (a *app) disarmInterrupt() {
	a.turnMu.Lock()
	a.turnCancel = nil
	a.turnMu.Unlock()
}

// interruptTurn is called by readline when Ctrl-C arrives while a request is
// in flight. It reports whether there was a turn to cancel; without one the
// signal keeps its default meaning.
func (a *app) interruptTurn() bool {
	a.turnMu.Lock()
	cancel := a.turnCancel
	a.turnMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// turnError prints a cancelled turn as an action, not as a failure: Ctrl-C is
// a normal way to stop a long answer, unlike a provider or transport error.
func (a *app) turnError(err error) {
	if errors.Is(err, context.Canceled) {
		fmt.Println("прервано (Ctrl-C)")
		return
	}
	fmt.Println("ошибка:", err)
}

func (a *app) ask(text string) {
	// The day-14 gate adds a local-model check after the main answer; with a
	// 14B model on CPU that can take tens of seconds. The old 180 s cap was
	// enough for the chat call alone, not for chat + semantic re-check.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	// Ctrl-C during the request cancels this context instead of killing the
	// process. At the prompt the line editor handles Ctrl-C itself, and Ctrl-D
	// on an empty line remains the exit command.
	a.armInterrupt(cancel)
	defer a.disarmInterrupt()

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
			a.turnError(err)
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
		a.turnError(err)
		return
	}
	if !streamed {
		fmt.Println()
		fmt.Println(answer)
	}
	a.footer()
}

// summaryStatus renders the rolling summary as a status fragment, honestly
// marking it when the active strategy does not send it to the model (the
// summary stays in the log after a switch to window/facts).
func (a *app) summaryStatus() string {
	if !a.hist.HasSummary() {
		return ""
	}
	s := fmt.Sprintf("конспект ~%d ток", tokens.EstimateTokens(a.hist.SummaryText()))
	if a.strategy != compact.StrategySummary {
		s += " (не в промпте)"
	}
	return s
}

// footer is the one-line stats line printed after every turn: model, prompt
// estimate vs budget, last answer size, active/archive split and accumulated
// API cost. An overflowing prompt is flagged explicitly.
func (a *app) footer() {
	activeMsgs, _, archivedMsgs, _ := a.hist.Stats()
	extra := ""
	if s := a.summaryStatus(); s != "" {
		extra = " · " + s
	}
	if a.strategy.NeedsWindow() {
		extra += fmt.Sprintf(" · %s(%d)", a.strategy, a.window)
	}
	prompt := a.agent.Comp.PromptTokens(a.hist.SystemPrompt())
	over := ""
	if prompt > a.budget.MaxPromptTokens {
		over = " · ПЕРЕПОЛНЕНИЕ"
	}
	s := a.client.Stats()
	// Implicit caching is invisible in the estimate but halves the bill: show
	// the share of the prompt the provider served from its cache.
	cache := ""
	if s.CachedTokens > 0 {
		cache = fmt.Sprintf(" · кеш %d/%d", s.CachedTokens, s.PromptTokens)
	}
	fmt.Printf("· %s · промпт ~%d/%d ток · ответ ~%d ток%s · актив %d, архив %d сообщ%s · API %d выз., $%.4f%s\n",
		a.hist.ChatModel(),
		prompt, a.budget.MaxPromptTokens,
		a.agent.LastAnswerTokens(), cache,
		activeMsgs, archivedMsgs, extra, s.Calls, s.Cost, over)
}

// describeStrategy prints the active context strategy in one line.
func describeStrategy(s compact.Strategy, window int) {
	switch s {
	case compact.StrategySummary:
		fmt.Println("стратегия контекста: summary — старые ходы сворачиваются в конспект моделью сжатия")
	case compact.StrategyWindow:
		fmt.Printf("стратегия контекста: window — в промпте только последние %d сообщений, остальное вытесняется в архив без конспекта\n", window)
	case compact.StrategyFacts:
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
		infos, err := history.SummarizeSessions(a.dataDir)
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
			fmt.Printf("переключить: /model <id>, например /model %s\n", config.ModelSet[0].ID)
			return false, nil
		}
		if _, ok := config.FindModel(arg); !ok {
			return false, fmt.Errorf("неизвестная модель %q; доступны: %s", arg, strings.Join(config.ModelIDs(), ", "))
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
		if _, ok := config.FindModel(arg); !ok {
			return false, fmt.Errorf("неизвестная модель %q; доступны: %s", arg, strings.Join(config.ModelIDs(), ", "))
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
		st, err := compact.ParseStrategy(fields[0])
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
		if st.NeedsWindow() && win <= 0 {
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
		fmt.Printf("факты (%d ключей, ~%d ток.):\n", len(facts), tokens.EstimateTokens(compact.RenderFacts(facts)))
		fmt.Println(compact.RenderFacts(facts))

	case "/memory":
		return false, a.memoryCommand(arg)

	case "/profile":
		return false, a.profileCommand(arg)

	case "/task":
		return false, a.taskCommand(arg)

	case "/invariant":
		return false, a.invariantCommand(arg)

	case "/branch":
		if arg == "" {
			if p := a.hist.ForkOf(); p != "" {
				fmt.Printf("родитель: %s (чекпоинт seq %d)\n", p, a.hist.ForkSeq())
			}
			kids, err := history.ListForks(a.dataDir, a.hist.ID())
			if err != nil {
				return false, err
			}
			byID := map[string]history.SessionInfo{}
			if infos, err := history.SummarizeSessions(a.dataDir); err == nil {
				for _, inf := range infos {
					byID[inf.ID] = inf
				}
			}
			if len(kids) == 0 {
				fmt.Println("веток от этой сессии пока нет")
			} else {
				fmt.Println("ветки от текущей сессии:")
				for _, k := range kids {
					line := "  · " + k
					if inf, ok := byID[k]; ok {
						line += fmt.Sprintf(" · %d сообщ.", inf.Msgs)
						if inf.ForkSeq > 0 {
							line += fmt.Sprintf(" · seq %d", inf.ForkSeq)
						}
					}
					fmt.Println(line)
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
		if infos, err := history.SummarizeSessions(a.dataDir); err == nil {
			for _, inf := range infos {
				if inf.ID == name {
					exists = true
					break
				}
			}
		}
		if !exists {
			id, err := history.ForkSession(a.dataDir, a.hist.ID(), name)
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

	case "/mcp":
		sub := strings.ToLower(strings.TrimSpace(arg))
		switch sub {
		case "", "status":
			if a.mcpClient == nil {
				fmt.Println("MCP не подключён (нет mcp.json или включён -no-mcp)")
				return false, nil
			}
			for _, line := range a.mcpClient.StatusLines() {
				fmt.Println("•", line)
			}
			for _, warning := range a.mcpClient.Warnings() {
				fmt.Println("  предупреждение:", warning)
			}
			fmt.Printf("подтверждение MCP: %s; постоянные разрешения: /mcp approvals\n", a.mcpApproval)
		case "approvals":
			if len(a.mcpAlways) == 0 {
				fmt.Println("постоянных разрешений MCP в этой сессии нет")
				return false, nil
			}
			fmt.Println("постоянные разрешения MCP в этой сессии:")
			for key := range a.mcpAlways {
				fmt.Println("  •", key)
			}
		case "reset":
			a.mcpAlways = map[string]bool{}
			fmt.Println("постоянные разрешения MCP сброшены")
		default:
			return false, fmt.Errorf("использование: /mcp [status | approvals | reset]")
		}

	case "/history":
		a.printHistory()

	case "/context":
		a.printContext()

	case "/tokens":
		a.printTokens()

	case "/compact":
		if a.strategy.NeedsWindow() {
			fmt.Printf("стратегия %s не сворачивает историю в конспект: промпт ограничен последними %d сообщениями, старое уходит в архив.\n",
				a.strategy, a.window)
			fmt.Println("нужен конспект — переключитесь: /strategy summary, затем /compact")
			return false, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		ok, err := a.agent.Comp.ForceCompact(ctx, a.hist.SystemPrompt())
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
			fmt.Printf("[%d] %s: %s\n", m.Seq, m.Role, config.OneLine(m.Content))
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
			tok = tokens.EstimateMessage(*m)
		}
		fmt.Printf("[%d] %-9s ~%-5d ток. %s\n", m.Seq, m.Role, tok, config.OneLine(m.Content))
	}
	if a.hist.HasSummary() {
		if a.strategy == compact.StrategySummary {
			fmt.Println("(плюс конспект предыдущего диалога)")
		} else {
			fmt.Printf("(конспект есть, но в промпт не идёт — стратегия %s)\n", a.strategy)
		}
	}
}

// printContext shows what the next request will actually carry: the system
// prompt, the rolling summary, the verbatim tail and the tool schemas, with a
// token estimate per block. It is the "what did compression do" view.
func (a *app) printContext() {
	promptMsgs := a.agent.Comp.PromptWindow()
	base := tokens.EstimateTokens(a.agent.Comp.ShortSystem(a.hist.SystemPrompt()))
	memTok := a.agent.Comp.MemoryTokens()
	factsTok := 0
	if facts := a.hist.Facts(); len(facts) > 0 {
		factsTok = tokens.EstimateTokens("Факты диалога (память о договорённостях):\n" + compact.RenderFacts(facts))
	}
	histTok := 0
	for _, m := range promptMsgs {
		msg := *m
		if msg.Role == "tool" {
			msg.Content = config.Truncate(msg.Content, a.maxToolChars)
		}
		histTok += tokens.EstimateMessage(msg)
	}
	schemas := a.tools.Schemas()
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		names = append(names, s.Function.Name)
	}
	toolsTok := 0
	if len(schemas) > 0 {
		if b, err := json.Marshal(schemas); err == nil {
			toolsTok = tokens.EstimateTokens(string(b))
		}
	}

	fmt.Printf("что уходит в модель (сессия %s, модель %s, стратегия %s):\n", a.hist.ID(), a.hist.ChatModel(), a.strategy)
	if a.summary != nil && a.summary != a.client {
		fmt.Printf("  сжатие               %s, модель %s\n", llm.DescribeEndpoint(a.summary), a.hist.SummaryModel())
	}
	fmt.Printf("  system prompt        ~%d ток.\n", base)
	switch a.strategy {
	case compact.StrategyFacts:
		if factsTok > 0 {
			fmt.Printf("  факты                ~%d ток. (%d ключей)\n", factsTok, len(a.hist.Facts()))
		} else {
			fmt.Println("  факты                нет")
		}
		fmt.Printf("  окно                 последние %d сообщ.\n", a.window)
	case compact.StrategyWindow:
		fmt.Printf("  окно                 последние %d сообщ. (без конспекта — стратегия window)\n", a.window)
	default:
		if a.hist.HasSummary() {
			cov := a.hist.SummaryCovers()
			sumTok := tokens.EstimateTokens("Конспект предыдущего диалога:\n"+a.hist.SummaryText()) + 4
			fmt.Printf("  конспект seq %d..%d   ~%d ток.\n", cov[0], cov[1], sumTok)
		} else {
			fmt.Println("  конспект             нет")
		}
	}
	if a.strategy != compact.StrategySummary && a.hist.HasSummary() {
		fmt.Printf("  конспект (старый)    ~%d ток. — не в промпте (остался от summary)\n",
			tokens.EstimateTokens(a.hist.SummaryText()))
	}
	if a.mem != nil && a.mem.Total() > 0 {
		fmt.Println("  память по слоям:")
		for _, l := range memory.Layers {
			n := a.mem.Len(l)
			if n == 0 {
				fmt.Printf("    %-12s         нет\n", string(l))
				continue
			}
			note := "всегда"
			switch l {
			case memory.LayerProfile:
				note = fmt.Sprintf("всегда, профиль «%s»", a.profileLabel())
			case memory.LayerKnowledge:
				note = fmt.Sprintf("%d из %d по вопросу", selectedKnowledge(a.mem, a.lastUserText()), n)
			}
			fmt.Printf("    %-12s ~%4d ток.  %d/%d, %s\n", string(l), memTok[l], n, memory.Caps[l], note)
		}
	}
	if block := a.agent.Comp.InvariantBlock(); block != "" {
		fmt.Printf("  инварианты           ~%4d ток.  %d правил, всегда\n", tokens.EstimateTokens(block), a.inv.Len())
	}
	if stage, tok := a.taskBlockInfo(); stage != "" {
		fmt.Printf("  задача               ~%4d ток.  этап %s, всегда\n", tok, stage)
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
		a.agent.Comp.PromptTokens(a.hist.SystemPrompt()), a.budget.MaxPromptTokens, a.agent.Calib.Ratio())

	switch a.strategy {
	case compact.StrategyWindow, compact.StrategyFacts:
		fmt.Printf("  вытеснение           всё старше %d сообщений → архив (без конспекта)\n", a.window)
	case compact.StrategySummary:
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

	if a.strategy == compact.StrategySummary && a.hist.HasSummary() {
		fmt.Println("\nконспект вместо старой истории:")
		for _, line := range strings.Split(strings.TrimSpace(a.hist.SummaryText()), "\n") {
			fmt.Println("  " + line)
		}
	}
	if a.strategy == compact.StrategyFacts && len(a.hist.Facts()) > 0 {
		fmt.Println("\nфакты вместо старой истории:")
		fmt.Println(indentBlock(compact.RenderFacts(a.hist.Facts()), "  "))
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
	if s := a.summaryStatus(); s != "" {
		covers := a.hist.SummaryCovers()
		fmt.Printf("%s: покрывает seq %d..%d\n", s, covers[0], covers[1])
	}
	if facts := a.hist.Facts(); len(facts) > 0 {
		fmt.Printf("факты: %d ключей, ~%d ток. (стратегия facts)\n", len(facts), tokens.EstimateTokens(compact.RenderFacts(facts)))
	}
	if a.strategy.NeedsWindow() {
		fmt.Printf("стратегия %s: в промпт идут последние %d сообщений, старое вытесняется в архив без конспекта\n", a.strategy, a.window)
	}
	fmt.Printf("оценка промпта: ~%d ток. (бюджет %d, калибровка ×%.2f)\n",
		a.agent.Comp.PromptTokens(a.hist.SystemPrompt()), a.budget.MaxPromptTokens, a.agent.Calib.Ratio())
	fmt.Printf("модель сжатия: %s\n", a.hist.SummaryModel())

	s := a.client.Stats()
	fmt.Printf("API: %d вызовов, промпт %d (из кеша %d), ответ %d, всего %d ток., $%.4f\n",
		s.Calls, s.PromptTokens, s.CachedTokens, s.CompletionTokens, s.TotalTokens, s.Cost)
}

func (a *app) runScript(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sawTurn := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fmt.Printf("agent> %s\n", line)
		// Lines starting with "/" are chat commands, not prompts. Handling them
		// here is what makes a demo reproducible: /memory and /memory clear can
		// run between the dialogue turns instead of being typed by hand.
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
		// The provider limits requests per sliding window (a 60 s quota in
		// practice), and every turn costs two calls: the answer and the memory
		// extractor. Without a pause the second turn already gets 429, so the
		// script waits between turns instead of burning retries.
		if a.scriptDelay > 0 && sawTurn {
			time.Sleep(a.scriptDelay)
		}
		sawTurn = true
		a.ask(line)
	}
	return sc.Err()
}

// printSessionList shows all stored sessions with the current one marked.
func (a *app) printSessionList() {
	infos, err := history.SummarizeSessions(a.dataDir)
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
		fmt.Printf(" %s %s · %s · модель %s · сжатие %s · сообщений %d%s\n",
			mark, inf.ID, inf.Created.Format("02.01 15:04"), inf.ChatModel, inf.SummaryModel, inf.Msgs, forkMark(inf))
	}
	fmt.Println("* — текущая; переключиться: /session <id>")
}

// forkMark renders the branch suffix of a session line: parent id and the
// parent's seq at the fork point. Empty for ordinary (non-branch) sessions.
func forkMark(inf history.SessionInfo) string {
	if inf.ForkOf == "" {
		return ""
	}
	point := ""
	if inf.ForkSeq > 0 {
		point = fmt.Sprintf(" (seq %d)", inf.ForkSeq)
	}
	return fmt.Sprintf(" · ← ветка от %s%s", inf.ForkOf, point)
}

func printModels() {
	fmt.Println("Модели — id это имя в первом столбце:")
	for _, m := range config.ModelSet {
		mark := " "
		if m.ID == config.DefaultModel() {
			mark = "*"
		}
		// Кеш-чтение дешевле обычного входа, поэтому печатаем отдельной
		// колонкой: по ней видно, сколько даёт повторный промпт. Прочерк
		// означает, что провайдер кеш-ставку не сообщает.
		cache := "—"
		if m.RateCachePerM > 0 {
			cache = fmt.Sprintf("$%.3f", m.RateCachePerM)
		}
		fmt.Printf(" %s %-32s %-10s вход $%.2f / выход $%.2f / кеш %-6s за 1M\n",
			mark, m.ID, m.Tier, m.RateInPerM, m.RateOutPerM, cache)
	}
	fmt.Println("(* — по умолчанию)")
}

func printSessions(dataDir string) {
	infos, err := history.SummarizeSessions(dataDir)
	if err != nil {
		fatalf("%v", err)
	}
	if len(infos) == 0 {
		fmt.Println("сессий пока нет")
		return
	}
	for _, inf := range infos {
		fmt.Printf("%s · %s · модель %s · сжатие %s · сообщений %d%s\n",
			inf.ID, inf.Created.Format("02.01 15:04"), inf.ChatModel, inf.SummaryModel, inf.Msgs, forkMark(inf))
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
  /memory [clear <слой> | add <слой> ключ=значение | history [ключ] | prompt]
                   слои памяти: рабочая, профиль, решения, знания; без
                   аргумента — что в каждом слое, время записи и сколько
                   токенов он добавляет к запросу; clear — очистить слой;
                   add — добавить запись; history — журнал «было → стало»;
                   prompt — ровно тот блок памяти, который уходит в промпт
  /profile [list | use <имя> | set ключ=значение | clear]
                   профиль пользователя: стиль, формат, обращение, ограничения;
                   уходит в каждый запрос последним блоком и пополняется из
                   диалога сам; list — какие есть, use — переключить
  /task [start | approve | pause | resume | state <этап> | reset]
                   состояние задачи (день 13): конечный автомат planning →
                   execution → validation → done; переходы ассистента
                   проверяет код, approve/pause/resume — только здесь
  /invariant [list | add | change | remove | history]
                   инварианты проекта (день 14): архитектура, стек, бюджет;
                   хранятся в <data>/invariants.json отдельно от диалога,
                   меняются только этой командой, каждое изменение — в журнал
                   с парой «было → стало»; модель их не меняет
  /branch [имя]    ветки диалога: без аргумента — список, /branch <имя> —
                   чекпоинт (создать ветку от текущего места), повторный
                   /branch <существующее имя> — переключение
  /system          показать системный промпт
  /tools           список инструментов
  /mcp [status | approvals | reset]
                   MCP-серверы: какие подключены и какие инструменты дали;
                   approvals — постоянные разрешения этой сессии; reset — сбросить
  /history         последние сообщения активного окна
  /context         что реально уходит в модель: system, память, хвост, tools
  /tokens          токены, архив, память, расходы API
  /compact         принудительно сжать контекст (стратегия summary)
  /recall <текст>  поиск по архиву

Полный лог сессии (все сообщения, архив, сжатия) — из оболочки, офлайн:
  ./agent -print-session <id|last>

Ввод: ↑/↓ — история запросов (хранится между запусками), ←/→ — курсор,
Backspace, Ctrl-A/E — начало/конец строки, Ctrl-U/W — стереть строку/слово,
Ctrl-C — отменить ввод или текущий ответ, Ctrl-D — выход.

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
