package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func args(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func TestCheckArgsPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "data", "expenses.csv")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sc := ServerConfig{Rules: map[string]Rule{"read_file": {PathUnder: root}}}
	if err := CheckArgs(sc, "read_file", args(map[string]any{"path": inside})); err != nil {
		t.Errorf("inside path rejected: %v", err)
	}
	if err := CheckArgs(sc, "read_file", args(map[string]any{"path": outside})); err == nil {
		t.Error("outside path allowed")
	}
	if err := CheckArgs(sc, "read_file", args(map[string]any{"path": filepath.Join(root, "..", "escape")})); err == nil {
		t.Error("traversal path allowed")
	}
	if err := CheckArgs(sc, "read_file", args(map[string]any{"path": inside + "/../../etc/passwd"})); err == nil {
		t.Error("mixed traversal path allowed")
	}
}

func TestCheckArgsPathSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	sc := ServerConfig{Rules: map[string]Rule{"read_file": {PathUnder: root}}}
	if err := CheckArgs(sc, "read_file", args(map[string]any{"path": filepath.Join(link, "secret.txt")})); err == nil {
		t.Error("symlink escape allowed")
	}
}

func TestCheckArgsURL(t *testing.T) {
	sc := ServerConfig{Rules: map[string]Rule{"fetch": {URLHosts: []string{"avito.ru", "*.example.com"}}}}
	good := []string{
		"https://avito.ru/item/1",
		"https://www.avito.ru/item/1",
		"https://api.example.com/x",
	}
	for _, raw := range good {
		if err := CheckArgs(sc, "fetch", args(map[string]any{"url": raw})); err != nil {
			t.Errorf("%s rejected: %v", raw, err)
		}
	}
	bad := []string{
		"https://evil.ru/",
		"http://localhost:11434/v1",
		"http://127.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/",
		"file:///etc/passwd",
	}
	for _, raw := range bad {
		if err := CheckArgs(sc, "fetch", args(map[string]any{"url": raw})); err == nil {
			t.Errorf("%s allowed", raw)
		}
	}
}

func TestCheckArgsURLAnyPublicHost(t *testing.T) {
	sc := ServerConfig{Rules: map[string]Rule{"fetch": {URLHosts: []string{"*"}}}}
	if err := CheckArgs(sc, "fetch", args(map[string]any{"url": "https://example.org/x"})); err != nil {
		t.Errorf("public host rejected: %v", err)
	}
	if err := CheckArgs(sc, "fetch", args(map[string]any{"url": "http://127.0.0.1:11434/"})); err == nil {
		t.Error("loopback allowed with wildcard host rule")
	}
}

func TestCheckArgsNoRule(t *testing.T) {
	if err := CheckArgs(ServerConfig{}, "anything", args(map[string]any{"x": 1})); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
