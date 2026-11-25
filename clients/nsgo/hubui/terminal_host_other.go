//go:build ios || android || js || (!darwin && !windows && !linux)

package hubui

import "github.com/nsgo-dev/nsgo/core"

func initializeTerminalHost()                           {}
func (s *State) terminalSettings(*core.Scope) core.Node { return nil }
