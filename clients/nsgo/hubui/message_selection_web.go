//go:build js && wasm

package hubui

import (
	"syscall/js"

	"github.com/nsgo-dev/nsgo/core"
)

// NSGo scopes Ctrl/Cmd+A. The browser menu can invoke Select All directly,
// starting a new selection on the document body without sending a key event.
func bindBrowserMessageSelectAll(cx *core.Scope) {
	doc := js.Global().Get("document")
	if !doc.Truthy() {
		return
	}
	onStart := js.FuncOf(func(_ js.Value, args []js.Value) any {
		event := args[0]
		target := event.Get("target")
		if !target.Equal(doc.Get("body")) && !target.Equal(doc.Get("documentElement")) && !target.Equal(doc) {
			return nil
		}
		sel := doc.Call("getSelection")
		if !sel.Truthy() || sel.Get("rangeCount").Int() != 1 || sel.Get("isCollapsed").Bool() {
			return nil
		}
		node := sel.Call("getRangeAt", 0).Get("commonAncestorContainer")
		if node.Get("nodeType").Int() != 1 {
			node = node.Get("parentElement")
		}
		if node.Truthy() {
			body := node.Call("closest", `[data-testid^="message-body-"]`)
			if body.Truthy() {
				event.Call("preventDefault")
				sel.Call("selectAllChildren", body)
			}
		}
		return nil
	})
	doc.Call("addEventListener", "selectstart", onStart)
	cx.Cleanup(func() {
		doc.Call("removeEventListener", "selectstart", onStart)
		onStart.Release()
	})
}
