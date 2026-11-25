package hubui

import "github.com/nsgo-dev/nsgo/core"

func focusComposerOnEntry(cx *core.Scope, _ *core.Ref, entryID string) {
	cx.CallHost("ras.focus-composer", entryID, nil)
}
