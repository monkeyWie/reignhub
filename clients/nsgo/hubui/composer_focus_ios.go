package hubui

import (
	"github.com/monkeyWie/reignhub/clients/nsgo/nativecap"
	"github.com/nsgo-dev/nsgo/core"
)

func focusComposerOnEntry(_ *core.Scope, _ *core.Ref, entryID string) {
	nativecap.FocusComposer(entryID)
}
