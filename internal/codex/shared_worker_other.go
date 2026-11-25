//go:build (!darwin && !windows) || ios

package codex

import "os/exec"

func configureWorkerProcess(*exec.Cmd) {}
