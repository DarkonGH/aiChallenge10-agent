package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// CheckArgs проверяет аргументы инструмента до вызова MCP. Это вторая линия
// защиты после ограничений самого сервера: путь не должен выходить за
// разрешённый каталог, а URL — за разрешённые домены и публичную сеть.
func CheckArgs(sc ServerConfig, tool string, raw json.RawMessage) error {
	rule, ok := sc.Rules[tool]
	if !ok {
		return nil
	}
	var args map[string]any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return fmt.Errorf("аргументы %s: %w", tool, err)
		}
	}

	if rule.PathUnder != "" {
		found := false
		for _, key := range []string{"path", "file", "directory"} {
			value, ok := args[key].(string)
			if !ok || value == "" {
				continue
			}
			found = true
			if err := checkPath(rule.PathUnder, value); err != nil {
				return err
			}
		}
		if !found {
			return fmt.Errorf("инструмент %s: нет пути для проверки", tool)
		}
	}

	if len(rule.URLHosts) > 0 {
		value, _ := args["url"].(string)
		if value == "" {
			return fmt.Errorf("инструмент %s: нет url для проверки", tool)
		}
		if err := checkURL(rule.URLHosts, value); err != nil {
			return err
		}
	}
	return nil
}

func checkPath(root, target string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("корень %q: %w", root, err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("путь %q: %w", target, err)
	}

	rootEval := evalPath(rootAbs)
	targetEval := evalPath(targetAbs)
	rel, err := filepath.Rel(rootEval, targetEval)
	if err != nil || filepath.IsAbs(rel) || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("путь %q вне разрешённого каталога %q", target, root)
	}
	return nil
}

// evalPath разрешает симлинки у существующего пути или хотя бы у его
// родителя: иначе ссылка внутри разрешённого каталога могла бы увести наружу.
func evalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(resolved, filepath.Base(path))
	}
	return filepath.Clean(path)
}

func checkURL(allowed []string, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q: разрешены только http и https", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("url %q: пустой host", raw)
	}
	if isBlockedHost(host) {
		return fmt.Errorf("url %q: локальные и приватные адреса запрещены", raw)
	}
	if !hostAllowed(host, allowed) {
		return fmt.Errorf("домен %q не разрешён (разрешены: %s)", host, strings.Join(allowed, ", "))
	}
	return nil
}

func isBlockedHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

func hostAllowed(host string, allowed []string) bool {
	for _, item := range allowed {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if item == "*" {
			// Разрешён любой публичный хост; приватные адреса всё равно
			// отсекает isBlockedHost.
			return true
		}
		if strings.HasPrefix(item, "*.") {
			if strings.HasSuffix(host, item[1:]) {
				return true
			}
			continue
		}
		if host == item || strings.HasSuffix(host, "."+item) {
			return true
		}
	}
	return false
}
