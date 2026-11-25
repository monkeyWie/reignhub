//go:build unix

package system

import (
	"os/exec"
	"syscall"
)

// IndependentAgentProcess isolates a desktop-owned backend from the GUI session.
func IndependentAgentProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
