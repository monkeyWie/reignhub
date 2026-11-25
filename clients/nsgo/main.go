package main

import (
	"github.com/monkeyWie/reignhub/clients/nsgo/hubui"
	"github.com/monkeyWie/reignhub/clients/nsgo/nativecap"
	"github.com/nsgo-dev/nsgo"
	"github.com/nsgo-dev/nsgo/core"
)

func main() {
	if runSharedWorker() {
		return
	}
	// App mode gives every desktop a managed window that can hide to the
	// tray. Phones and browsers still use the same single main view.
	nsgo.Run(func(app *core.Scope) {
		nativecap.ConfigureTextEditing()
		nsgo.SetMainView(app, hubui.Root)
	}, append([]nsgo.Option{nsgo.WindowSize(1320, 880)}, nativecap.Options()...)...)
}
