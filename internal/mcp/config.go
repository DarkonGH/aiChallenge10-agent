// Package mcp подключает внешние MCP-серверы и превращает их инструменты
// в обычные инструменты агента.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config — общий конфиг MCP. Один и тот же файл используется и в обычной
// работе, и в демо; путь к данным задаётся через ${MCP_FS_ROOT}.
type Config struct {
	Version  int            `json:"version"`
	Approval string         `json:"approval,omitempty"`
	Servers  []ServerConfig `json:"servers"`
}

// ServerConfig описывает один MCP-сервер.
type ServerConfig struct {
	Name         string            `json:"name"`
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	Required     bool              `json:"required,omitempty"`
	Enabled      *bool             `json:"enabled,omitempty"`
	Allow        []string          `json:"allow,omitempty"`
	Approval     map[string]string `json:"approval,omitempty"`
	Rules        map[string]Rule   `json:"rules,omitempty"`
	Descriptions map[string]string `json:"descriptions,omitempty"`
}

// Rule — проверка аргументов конкретного инструмента до вызова MCP.
type Rule struct {
	// PathUnder разрешает только пути внутри указанного каталога.
	PathUnder string `json:"path_under,omitempty"`
	// URLHosts разрешает только эти домены (точное совпадение или поддомен).
	URLHosts []string `json:"url_hosts,omitempty"`
}

// FindConfig ищет mcp.json в порядке приоритета. Пустая строка и nil
// означают, что конфига нет и MCP надо просто не включать.
func FindConfig(explicit, dataDir string) (string, error) {
	candidates := []string{}
	if explicit != "" {
		candidates = append(candidates, explicit)
	} else if env := os.Getenv("MCP_CONFIG"); env != "" {
		candidates = append(candidates, env)
	} else {
		candidates = append(candidates, "mcp.json")
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, ".config", "goagent", "mcp.json"))
		}
		if dataDir != "" {
			candidates = append(candidates, filepath.Join(dataDir, "mcp.json"))
		}
	}

	for _, path := range candidates {
		path = expand(path)
		if path == "" {
			continue
		}
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path, nil
		}
		if explicit != "" || os.Getenv("MCP_CONFIG") != "" {
			return "", fmt.Errorf("конфиг MCP не найден: %s", path)
		}
	}
	return "", nil
}

// Load читает и проверяет конфиг.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// Старый формат: просто массив серверов.
		if err := json.Unmarshal(trimmed, &cfg.Servers); err != nil {
			return Config{}, fmt.Errorf("разбор %s: %w", path, err)
		}
	} else if err := json.Unmarshal(trimmed, &cfg); err != nil {
		return Config{}, fmt.Errorf("разбор %s: %w", path, err)
	}

	if cfg.Approval == "" {
		cfg.Approval = "ask"
	}
	normalized, err := NormalizeApproval(cfg.Approval)
	if err != nil {
		return Config{}, fmt.Errorf("%s: approval: %w", path, err)
	}
	cfg.Approval = normalized

	seen := map[string]bool{}
	for i := range cfg.Servers {
		sc := &cfg.Servers[i]
		sc.Name = strings.TrimSpace(sc.Name)
		sc.Command = expand(sc.Command)
		if sc.Name == "" {
			return Config{}, fmt.Errorf("%s: у сервера %d нет name", path, i+1)
		}
		if seen[sc.Name] {
			return Config{}, fmt.Errorf("%s: сервер %q указан дважды", path, sc.Name)
		}
		seen[sc.Name] = true
		if sc.Command == "" {
			return Config{}, fmt.Errorf("%s: у сервера %q нет command", path, sc.Name)
		}
		for j := range sc.Args {
			sc.Args[j] = expand(sc.Args[j])
		}
		for k, v := range sc.Env {
			sc.Env[k] = expand(v)
		}
		for tool, decision := range sc.Approval {
			norm, err := NormalizeApproval(decision)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %s/%s approval: %w", path, sc.Name, tool, err)
			}
			sc.Approval[tool] = norm
		}
		for tool, rule := range sc.Rules {
			rule.PathUnder = expand(rule.PathUnder)
			for j := range rule.URLHosts {
				rule.URLHosts[j] = strings.ToLower(strings.TrimSpace(rule.URLHosts[j]))
			}
			sc.Rules[tool] = rule
		}
	}
	return cfg, nil
}

// Enabled сообщает, надо ли запускать сервер.
func (sc ServerConfig) IsEnabled() bool {
	return sc.Enabled == nil || *sc.Enabled
}

// ApprovalFor возвращает политику подтверждения для инструмента.
func (c Config) ApprovalFor(sc ServerConfig, tool string) string {
	if v, ok := sc.Approval[tool]; ok && v != "" {
		return v
	}
	if v, ok := sc.Approval["*"]; ok && v != "" {
		return v
	}
	if c.Approval != "" {
		return c.Approval
	}
	return "ask"
}

// NormalizeApproval приводит политику к allow/ask/deny.
func NormalizeApproval(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "ask":
		return "ask", nil
	case "allow", "yes", "always":
		return "allow", nil
	case "deny", "no", "never":
		return "deny", nil
	default:
		return "", fmt.Errorf("неизвестная политика %q (ожидается allow, ask или deny)", s)
	}
}

// expand подставляет переменные окружения. Для MCP_FS_ROOT, CWD и WORKSPACE
// при отсутствии переменной используется текущий каталог.
func expand(s string) string {
	if s == "" {
		return ""
	}
	cwd, _ := os.Getwd()
	return os.Expand(s, func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		switch key {
		case "MCP_FS_ROOT", "CWD", "WORKSPACE":
			return cwd
		}
		return ""
	})
}
