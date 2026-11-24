package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigAndApproval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	data := `{
	  "version": 1,
	  "approval": "ask",
	  "servers": [
	    {
	      "name": "fs",
	      "command": "uvx",
	      "args": ["--from", "filesystem-mcp", "filesystem-mcp", "${MCP_FS_ROOT}"],
	      "required": false,
	      "allow": ["read_file", "list_directory"],
	      "approval": {"list_allowed_directories": "allow", "read_file": "ask"},
	      "descriptions": {"read_file": "Прочитать файл"},
	      "rules": {"read_file": {"path_under": "${MCP_FS_ROOT}"}}
	    },
	    {
	      "name": "web",
	      "command": "uvx",
	      "args": ["mcp-server-fetch"],
	      "allow": ["fetch"],
	      "approval": {"fetch": "ask"}
	    }
	  ]
	}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_FS_ROOT", "/tmp/day16-fs/data")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(cfg.Servers))
	}
	fs := cfg.Servers[0]
	if got := fs.Args[len(fs.Args)-1]; got != "/tmp/day16-fs/data" {
		t.Errorf("expanded arg = %q", got)
	}
	if got := cfg.ApprovalFor(fs, "list_allowed_directories"); got != "allow" {
		t.Errorf("approval for list_allowed_directories = %q, want allow", got)
	}
	if got := cfg.ApprovalFor(fs, "read_file"); got != "ask" {
		t.Errorf("approval for read_file = %q, want ask", got)
	}
	if got := cfg.ApprovalFor(fs, "unknown"); got != "ask" {
		t.Errorf("approval for unknown = %q, want ask", got)
	}
	if fs.Rules["read_file"].PathUnder != "/tmp/day16-fs/data" {
		t.Errorf("path_under = %q", fs.Rules["read_file"].PathUnder)
	}
	if fs.Descriptions["read_file"] != "Прочитать файл" {
		t.Errorf("description = %q", fs.Descriptions["read_file"])
	}
}

func TestLoadLegacyArrayAndInvalidApproval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`[{"name":"fs","command":"echo","approval":{"read_file":"nope"}}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "approval") {
		t.Fatalf("expected approval error, got %v", err)
	}
}

func TestExpandFallsBackToCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_FS_ROOT", "")
	if got := expand("${MCP_FS_ROOT}"); got != wd {
		t.Errorf("expand = %q, want cwd %q", got, wd)
	}
	t.Setenv("MCP_TEST_VALUE", "x")
	if got := expand("a-${MCP_TEST_VALUE}"); got != "a-x" {
		t.Errorf("expand = %q, want a-x", got)
	}
}

func TestFindConfigExplicitMissing(t *testing.T) {
	_, err := FindConfig(filepath.Join(t.TempDir(), "nope.json"), t.TempDir())
	if err == nil {
		t.Fatal("expected error for explicit missing config")
	}
}

func TestNormalizeApproval(t *testing.T) {
	cases := map[string]string{
		"":        "ask",
		"ALLOW":   "allow",
		"always":  "allow",
		"ask":     "ask",
		"deny":    "deny",
		"never":   "deny",
		"unknown": "",
	}
	for in, want := range cases {
		got, err := NormalizeApproval(in)
		if want == "" {
			if err == nil {
				t.Errorf("NormalizeApproval(%q) expected error", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("NormalizeApproval(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
