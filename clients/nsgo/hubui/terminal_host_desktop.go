//go:build (darwin && !ios) || windows || (linux && !android)

package hubui

import (
	"sync"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/clients/nsgo/terminalengine"
	"github.com/monkeyWie/reignhub/internal/bridge"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

var terminalHostOnce sync.Once

func initializeTerminalHost() {
	terminalHostOnce.Do(func() { bridge.SetTerminalFactory(terminalengine.New) })
}

func (s *State) terminalSettings(cx *core.Scope) core.Node {
	checked := func() bool { s.read(); return flag(s.host, "remoteTerminalEnabled") }
	look := ui.NewSwitchLook(cx, checked, ui.SizeSmall)
	return row(core.View{Style: core.Style{FlexGrow: 1, Gap: 4}, Children: []core.Node{
		label(cx, locales.For(cx).RemoteTerminal(), 14, false), label(cx, locales.For(cx).RemoteTerminalHint(), 12, true),
	}}, base.Switch{TestID: "remote-terminal-enabled", Label: locales.For(cx).RemoteTerminal(), Checked: checked,
		Disabled: func() bool { return s.actionPending("remote-terminal-enabled") }, TrackStyleFunc: look.Track, ThumbStyleFunc: look.Thumb,
		OnChange: func(value bool) {
			s.call("remote-terminal-enabled", "host.terminal.enabled", Object{"enabled": value}, func(any) { s.host = copyObject(s.host); s.host["remoteTerminalEnabled"] = value })
		},
	})
}
