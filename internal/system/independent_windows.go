package system

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func IndependentAgentProcess(cmd *exec.Cmd) {
	// Keep the bridge in its own control-event group and keep redirected
	// standard handles available for its startup reply.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	HideConsole(cmd)
}
