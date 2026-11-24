package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
)

const callTimeout = 60 * time.Second

// Server — подключённый MCP-сервер.
type Server struct {
	Name    string
	Config  ServerConfig
	Session *sdk.ClientSession
	Tools   []*sdk.Tool
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
	cmd.Env = os.Environ()
	for k, v := range sc.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

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
	srv := &Server{Name: sc.Name, Config: sc, Session: session}
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

// Call вызывает инструмент MCP после проверки аргументов.
func (m *Manager) Call(ctx context.Context, server, tool string, raw json.RawMessage) (string, error) {
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

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := srv.Session.CallTool(callCtx, &sdk.CallToolParams{Name: tool, Arguments: args})
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
		if g := m.currentGuard(); g != nil {
			guarded, err := g.Check(ctx, server, tool, text)
			if err != nil {
				return "", err
			}
			text = guarded
		}
		return "", fmt.Errorf("%s", text)
	}
	if text == "" {
		text = "инструмент выполнен, но не вернул содержимого"
	}
	if g := m.currentGuard(); g != nil {
		guarded, err := g.Check(ctx, server, tool, text)
		if err != nil {
			return "", err
		}
		text = guarded
	}
	return text, nil
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
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, srv := range m.servers {
		if srv.Session != nil {
			_ = srv.Session.Close()
			srv.Session = nil
		}
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
