package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
)

const callTimeout = 60 * time.Second

// Server — подключённый MCP-сервер.
//
// Команда и stderr хранятся рядом с сессией: если сервер завершится уже
// после успешного tools/list, по ним можно понять причину и перезапустить
// процесс, а не показывать модели невнятное «client is closing: EOF».
type Server struct {
	Name    string
	Config  ServerConfig
	Session *sdk.ClientSession
	Tools   []*sdk.Tool

	cmd    *exec.Cmd
	stderr *lockedBuffer
	mu     sync.Mutex
}

// lockedBuffer — stderr дочернего MCP-процесса. Писать в него может
// дочерний процесс в любой момент, а читать мы будем при ошибке; обычный
// bytes.Buffer здесь дал бы гонку.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// Status — итог подключения одного сервера для строки старта.
type Status struct {
	Name  string
	Tools int
	Names []string
	Err   error
}

// Manager держит подключения и регистрирует инструменты в реестре агента.
type Manager struct {
	cfg      Config
	servers  []*Server
	statuses []Status
	warnings []string
	guard    *Guard
	mu       sync.Mutex
	closed   atomic.Bool
}

// SetGuard installs the output/action guard used by Call. It is separate from
// Start because the guard needs the session's summary model, which is known
// only after the first session is opened.
func (m *Manager) SetGuard(g *Guard) {
	m.mu.Lock()
	m.guard = g
	m.mu.Unlock()
}

func (m *Manager) currentGuard() *Guard {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.guard
}

// Start подключается ко всем включённым серверам. Обязательный сервер
// роняет запуск, необязательный — только добавляет предупреждение.
func Start(ctx context.Context, cfg Config) (*Manager, error) {
	m := &Manager{cfg: cfg}
	for _, sc := range cfg.Servers {
		if !sc.IsEnabled() {
			continue
		}
		srv, err := startServer(ctx, sc)
		if err != nil {
			m.statuses = append(m.statuses, Status{Name: sc.Name, Err: err})
			if sc.Required {
				m.Close()
				return nil, fmt.Errorf("MCP %s: %w", sc.Name, err)
			}
			m.warnings = append(m.warnings, fmt.Sprintf("%s: %v", sc.Name, err))
			continue
		}
		st := Status{Name: srv.Name, Tools: len(srv.Tools)}
		for _, tool := range srv.Tools {
			st.Names = append(st.Names, tool.Name)
		}
		m.servers = append(m.servers, srv)
		m.statuses = append(m.statuses, st)
	}
	return m, nil
}

func startServer(ctx context.Context, sc ServerConfig) (*Server, error) {
	cmd := exec.Command(sc.Command, sc.Args...)
	// Не даём Ctrl-C в терминале убить MCP-серверы вместе с прерванным ходом
	// модели: агент переживает SIGINT, а дочерние серверы должны тоже.
	isolateProcess(cmd)
	cmd.Env = os.Environ()
	for k, v := range sc.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	client := sdk.NewClient(&sdk.Implementation{
		Name:    "goagent",
		Version: "1.0",
	}, nil)

	connCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	session, err := client.Connect(connCtx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("подключение: %w%s", err, stderrTail(stderr.String()))
	}

	toolsList, err := listTools(connCtx, session)
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("tools/list: %w%s", err, stderrTail(stderr.String()))
	}

	allowed := map[string]bool{}
	for _, name := range sc.Allow {
		allowed[name] = true
	}
	srv := &Server{Name: sc.Name, Config: sc, Session: session, cmd: cmd, stderr: stderr}
	for _, tool := range toolsList {
		if len(allowed) > 0 && !allowed[tool.Name] {
			continue
		}
		srv.Tools = append(srv.Tools, tool)
	}
	return srv, nil
}

func listTools(ctx context.Context, session *sdk.ClientSession) ([]*sdk.Tool, error) {
	var out []*sdk.Tool
	cursor := ""
	for {
		res, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, res.Tools...)
		if res.NextCursor == "" || res.NextCursor == cursor {
			return out, nil
		}
		cursor = res.NextCursor
	}
}

// Register добавляет MCP-инструменты в общий реестр агента.
func (m *Manager) Register(reg *tools.ToolRegistry) {
	for _, srv := range m.servers {
		for _, remote := range srv.Tools {
			name := remote.Name
			description := remote.Description
			if override := srv.Config.Descriptions[name]; override != "" {
				// Сервер может отдавать описание на другом языке или с
				// рекламным текстом; конфиг перекрывает его для /tools и модели.
				description = override
			}
			reg.AddMCP(srv.Name, &tools.Tool{
				Name:        "mcp_" + srv.Name + "_" + name,
				Description: "MCP " + srv.Name + ": " + description,
				Parameters:  schemaMap(remote.InputSchema),
				Approval:    m.cfg.ApprovalFor(srv.Config, name),
				Run: func(raw json.RawMessage) (string, error) {
					return m.Call(context.Background(), srv.Name, name, raw)
				},
			})
		}
	}
}

// Call вызывает инструмент MCP после проверки аргументов и прогоняет
// результат через guard: это путь для модели и обычных инструментов агента.
func (m *Manager) Call(ctx context.Context, server, tool string, raw json.RawMessage) (string, error) {
	return m.call(ctx, server, tool, raw, true)
}

// CallTrusted — путь для внутреннего кода, который сам знает, что делает:
// планировщик вызывает им MCP-инструменты по расписанию. Проверка аргументов
// остаётся, guard результата не применяется, иначе обёртка сломала бы JSON,
// который планировщик разбирает.
func (m *Manager) CallTrusted(ctx context.Context, server, tool string, raw json.RawMessage) (string, error) {
	return m.call(ctx, server, tool, raw, false)
}

func (m *Manager) call(ctx context.Context, server, tool string, raw json.RawMessage, useGuard bool) (string, error) {
	srv := m.server(server)
	if srv == nil {
		return "", fmt.Errorf("MCP-сервер %q не подключён", server)
	}
	if err := CheckArgs(srv.Config, tool, raw); err != nil {
		return "", err
	}

	var args any = map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("аргументы %s: %w", tool, err)
		}
	}

	text, err := m.callSession(ctx, srv, tool, args, useGuard)
	if err == nil {
		return text, nil
	}
	// MCP-сервер — отдельный процесс: он мог умереть уже после tools/list.
	// В этом случае SDK отдаёт «client is closing: EOF», и раньше планировщик
	// повторял этот же мёртвый вызов 24 раза. Перезапускаем один раз и
	// повторяем вызов; stderr сервера добавляем к ошибке, если и это не помогло.
	if !isTransportError(err) {
		return "", err
	}
	if rerr := m.restartServer(ctx, srv); rerr != nil {
		return "", fmt.Errorf("%w; MCP-сервер %s не перезапустился: %v%s", err, server, rerr, m.stderrTail(srv))
	}
	text, err = m.callSession(ctx, srv, tool, args, useGuard)
	if err != nil {
		return "", fmt.Errorf("%w%s", err, m.stderrTail(srv))
	}
	return text, nil
}

// callSession выполняет один вызов на текущей сессии сервера.
func (m *Manager) callSession(ctx context.Context, srv *Server, tool string, args any, useGuard bool) (string, error) {
	srv.mu.Lock()
	session := srv.Session
	srv.mu.Unlock()
	if session == nil {
		return "", fmt.Errorf("MCP-сервер %s закрыт", srv.Name)
	}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := session.CallTool(callCtx, &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("вызов %s: %w", tool, err)
	}

	text := contentText(res.Content)
	if text == "" && res.StructuredContent != nil {
		if data, err := json.MarshalIndent(res.StructuredContent, "", "  "); err == nil {
			text = string(data)
		}
	}
	if res.IsError {
		if text == "" {
			text = "инструмент вернул ошибку без описания"
		}
		if useGuard {
			if g := m.currentGuard(); g != nil {
				guarded, err := g.Check(ctx, srv.Name, tool, text)
				if err != nil {
					return "", err
				}
				text = guarded
			}
		}
		return "", fmt.Errorf("%s", text)
	}
	if text == "" {
		text = "инструмент выполнен, но не вернул содержимого"
	}
	if useGuard {
		if g := m.currentGuard(); g != nil {
			guarded, err := g.Check(ctx, srv.Name, tool, text)
			if err != nil {
				return "", err
			}
			text = guarded
		}
	}
	return text, nil
}

// Has сообщает, подключён ли сервер с таким именем. Регистрация инструмента
// и живая сессия — разные вещи, поэтому вызывающий код может проверить это
// до запуска длинной операции.
func (m *Manager) Has(name string) bool {
	return m.server(name) != nil
}

// restartServer закрывает старую сессию и поднимает процесс заново по тому
// же конфигу. Инструменты в реестре не перерегистрируются: они вызывают
// Manager.Call по имени и автоматически увидят новую сессию.
func (m *Manager) restartServer(ctx context.Context, srv *Server) error {
	if m.closed.Load() {
		return fmt.Errorf("MCP уже закрыт")
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.Session != nil {
		_ = srv.Session.Close()
		srv.Session = nil
	}
	fresh, err := startServer(ctx, srv.Config)
	if err != nil {
		return err
	}
	srv.Session = fresh.Session
	srv.Tools = fresh.Tools
	srv.cmd = fresh.cmd
	srv.stderr = fresh.stderr
	return nil
}

func (m *Manager) stderrTail(srv *Server) string {
	if srv == nil {
		return ""
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.stderr == nil {
		return ""
	}
	return stderrTail(srv.stderr.String())
}

// isTransportError отличает смерть процесса/сессии от обычной ошибки
// инструмента. Только в этом случае имеет смысл перезапускать сервер.
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection closed",
		"client is closing",
		"broken pipe",
		"eof",
		"transport",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (m *Manager) server(name string) *Server {
	for _, srv := range m.servers {
		if srv.Name == name {
			return srv
		}
	}
	return nil
}

// StatusLines возвращает строки для печати при старте.
func (m *Manager) StatusLines() []string {
	var out []string
	for _, st := range m.statuses {
		if st.Err != nil {
			continue
		}
		word := "инструментов"
		switch st.Tools % 10 {
		case 1:
			if st.Tools%100 != 11 {
				word = "инструмент"
			}
		case 2, 3, 4:
			if st.Tools%100 < 10 || st.Tools%100 >= 20 {
				word = "инструмента"
			}
		}
		line := fmt.Sprintf("MCP %s: %d %s", st.Name, st.Tools, word)
		if len(st.Names) > 0 {
			line += " (" + strings.Join(st.Names, ", ") + ")"
		}
		out = append(out, line)
	}
	return out
}

// Warnings возвращает предупреждения о необязательных серверах.
func (m *Manager) Warnings() []string {
	return append([]string(nil), m.warnings...)
}

// Close закрывает все сессии.
func (m *Manager) Close() {
	if m.closed.Swap(true) {
		return
	}
	for _, srv := range m.servers {
		srv.mu.Lock()
		if srv.Session != nil {
			_ = srv.Session.Close()
			srv.Session = nil
		}
		srv.mu.Unlock()
	}
}

func stderrTail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return ": " + strings.Join(lines, " | ")
}

func schemaMap(schema any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	data, err := json.Marshal(schema)
	if err == nil {
		var out map[string]any
		if json.Unmarshal(data, &out) == nil {
			return out
		}
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func contentText(content []sdk.Content) string {
	var parts []string
	for _, item := range content {
		switch c := item.(type) {
		case *sdk.TextContent:
			parts = append(parts, c.Text)
		case *sdk.EmbeddedResource:
			if c.Resource == nil {
				continue
			}
			if c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource.URI != "" {
				parts = append(parts, "["+c.Resource.URI+"]")
			}
		case *sdk.ResourceLink:
			if c.URI != "" {
				parts = append(parts, c.URI)
			}
		case *sdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[изображение %s]", c.MIMEType))
		case *sdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[аудио %s]", c.MIMEType))
		default:
			parts = append(parts, fmt.Sprintf("[содержимое %T]", item))
		}
	}
	return strings.Join(parts, "\n")
}
