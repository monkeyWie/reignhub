//go:build android || (!darwin && !windows && !linux && !(js && wasm && !nsgominiprogram && !nsgoharmony))

package hubui

import "github.com/nsgo-dev/nsgo/core"

type terminalReceiver struct{}

func (s *State) terminalButton(*core.Scope) core.Node                 { return nil }
func (s *State) withTerminal(_ *core.Scope, chat core.Node) core.Node { return chat }

func (s *State) forgetDeviceTerminals(string) {}
