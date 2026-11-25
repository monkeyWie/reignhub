//go:build !windows

package codex

import "os"

func sharedWorkerExecutable(_ string) (string, func(), error) {
	exe, err := os.Executable()
	return exe, func() {}, err
}
