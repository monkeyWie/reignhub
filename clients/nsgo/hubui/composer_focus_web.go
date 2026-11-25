//go:build js && wasm

package hubui

import (
	"syscall/js"

	"github.com/nsgo-dev/nsgo/core"
)

func focusComposerOnEntry(cx *core.Scope, input *core.Ref, entryID string) {
	platform := platformClientInfo().Platform
	if !isMobile(cx) && platform != "ios" && platform != "android" {
		input.Focus()
		return
	}

	doc := js.Global().Get("document")
	field := doc.Call("querySelector", `[data-testid="`+entryID+`"] textarea[data-testid="chat-input"]`)
	if !field.Truthy() || field.Get("disabled").Bool() || field.Equal(doc.Get("activeElement")) {
		return
	}
	mode := field.Call("getAttribute", "inputmode")
	field.Call("setAttribute", "inputmode", "none")
	var restore js.Func
	restored := false
	restoreMode := func() {
		if restored {
			return
		}
		restored = true
		if mode.IsNull() {
			field.Call("removeAttribute", "inputmode")
		} else {
			field.Call("setAttribute", "inputmode", mode)
		}
		field.Call("removeEventListener", "pointerdown", restore)
		field.Call("removeEventListener", "blur", restore)
		restore.Release()
	}
	restore = js.FuncOf(func(js.Value, []js.Value) any { restoreMode(); return nil })
	field.Call("addEventListener", "pointerdown", restore)
	field.Call("addEventListener", "blur", restore)
	cx.Cleanup(restoreMode)
	field.Call("focus", map[string]any{"preventScroll": true})
}
