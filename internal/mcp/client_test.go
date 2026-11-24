package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DarkonGH/aiChallenge10-agent/internal/tools"
)

func TestContentText(t *testing.T) {
	content := []sdk.Content{
		&sdk.TextContent{Text: "hello"},
		&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{Text: "resource"}},
		&sdk.ResourceLink{URI: "file:///tmp/a"},
		&sdk.ImageContent{MIMEType: "image/png"},
		&sdk.AudioContent{MIMEType: "audio/wav"},
	}
	got := contentText(content)
	want := "hello\nresource\nfile:///tmp/a\n[изображение image/png]\n[аудио audio/wav]"
	if got != want {
		t.Errorf("contentText = %q, want %q", got, want)
	}
}

func TestSchemaMap(t *testing.T) {
	in := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}
	out := schemaMap(in)
	if out["type"] != "object" {
		t.Errorf("schemaMap = %#v", out)
	}
	if schemaMap(nil)["type"] != "object" {
		t.Errorf("nil schema fallback = %#v", schemaMap(nil))
	}
}

func TestRegisterMetadata(t *testing.T) {
	cfg := Config{Approval: "ask", Servers: []ServerConfig{{
		Name:     "fs",
		Allow:    []string{"read_file"},
		Approval: map[string]string{"read_file": "allow"},
	}}}
	m := &Manager{cfg: cfg, servers: []*Server{{
		Name:   "fs",
		Config: cfg.Servers[0],
		Tools: []*sdk.Tool{{
			Name:        "read_file",
			Description: "Read a file",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
			},
		}},
	}}}
	reg := tools.NewToolRegistry(nil)
	m.Register(reg)

	meta, ok := reg.Meta("mcp_fs_read_file")
	if !ok {
		t.Fatal("mcp tool not registered")
	}
	if meta.Source != "mcp" || meta.Server != "fs" || meta.Approval != "allow" {
		t.Errorf("meta = %+v", meta)
	}
	var described string
	for _, line := range reg.Describe() {
		if strings.Contains(line, "mcp_fs_read_file") {
			described = line
		}
	}
	if !strings.Contains(described, "[MCP fs]") {
		t.Errorf("Describe = %q, want MCP marker", described)
	}
}

func TestStatusLines(t *testing.T) {
	m := &Manager{statuses: []Status{
		{Name: "fs", Tools: 1, Names: []string{"read_file"}},
		{Name: "web", Tools: 2, Names: []string{"fetch", "other"}},
		{Name: "empty", Tools: 0},
	}}
	lines := m.StatusLines()
	if len(lines) != 3 {
		t.Fatalf("lines = %v", lines)
	}
	if !strings.Contains(lines[0], "1 инструмент") {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.Contains(lines[1], "2 инструмента") {
		t.Errorf("line 1 = %q", lines[1])
	}
}

func TestStderrTail(t *testing.T) {
	if got := stderrTail("a\nb\nc\nd\n"); got != ": b | c | d" {
		t.Errorf("stderrTail = %q", got)
	}
	if got := stderrTail("  "); got != "" {
		t.Errorf("empty stderrTail = %q", got)
	}
}

func TestCallUnknownServer(t *testing.T) {
	m := &Manager{}
	if _, err := m.Call(t.Context(), "nope", "x", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for unknown server")
	}
}
