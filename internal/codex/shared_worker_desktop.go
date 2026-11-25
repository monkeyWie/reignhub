//go:build (darwin && !ios) || windows

package codex

import (
	"os/exec"

	"github.com/monkeyWie/reignhub/internal/system"
)

func configureWorkerProcess(cmd *exec.Cmd) {
	system.IndependentAgentProcess(cmd)
}
