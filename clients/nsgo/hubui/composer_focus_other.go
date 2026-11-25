//go:build !ios && !android && !(js && wasm)

package hubui

import "github.com/nsgo-dev/nsgo/core"

func focusComposerOnEntry(_ *core.Scope, input *core.Ref, _ string) { input.Focus() }
