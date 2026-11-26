package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPHelperProcess is not a real test: when the test binary is started
// with GO_WANT_MCP_HELPER=1, it acts as a tiny stdio MCP server. This lets the
// reconnect test kill a real child process and check that Call starts it again.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "echo", Version: "1"}, nil)
	type args struct {
		Text string `json:"text"`
	}
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "echo text"},
		func(_ context.Context, _ *sdk.CallToolRequest, in args) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{
				Content: []sdk.Content{&sdk.TextContent{Text: in.Text}},
			}, nil, nil
		})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestCallRestartsDeadServer(t *testing.T) {
	cfg := Config{Servers: []ServerConfig{{
		Name:     "echo",
		Command:  os.Args[0],
		Args:     []string{"-test.run=TestMCPHelperProcess"},
		Env:      map[string]string{"GO_WANT_MCP_HELPER": "1"},
		Allow:    []string{"echo"},
		Approval: map[string]string{"echo": "allow"},
	}}}
	m, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close()

	srv := m.server("echo")
	if srv == nil || srv.cmd == nil || srv.cmd.Process == nil {
		t.Fatal("echo server not started")
	}
	if err := srv.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	out, err := m.Call(context.Background(), "echo", "echo", json.RawMessage(`{"text":"after restart"}`))
	if err != nil {
		t.Fatalf("Call after kill: %v", err)
	}
	if out != "after restart" {
		t.Fatalf("out = %q", out)
	}
	if srv.cmd.Process == nil || srv.cmd.Process.Pid == 0 {
		t.Fatal("server was not restarted")
	}
}
