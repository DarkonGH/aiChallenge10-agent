//go:build !unix

package mcp

import "os/exec"

func isolateProcess(cmd *exec.Cmd) {}
