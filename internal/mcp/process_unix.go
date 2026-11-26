//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// isolateProcess put an MCP server into its own process group. The terminal
// sends Ctrl-C (SIGINT) to the whole foreground group; without this the Go
// MCP servers die together with an interrupted model turn, while the agent
// itself keeps running. Then every later call sees a dead stdio session.
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
