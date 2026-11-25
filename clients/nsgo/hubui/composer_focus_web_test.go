//go:build js && wasm

package hubui

import (
	"syscall/js"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestMobileBrowserComposerFocusAndTap(t *testing.T) {
	if nsgotest.Target() != "web" {
		t.Skip("requires a browser textarea")
	}
	nav := js.Global().Get("navigator")
	js.Global().Get("Object").Call("defineProperty", nav, "userAgent", map[string]any{"configurable": true, "value": "Mozilla/5.0 (Linux; Android 15) Mobile"})
	defer nav.Delete("userAgent")
	doc := js.Global().Get("document")
	initialMode := ""
	onFocus := js.FuncOf(func(_ js.Value, args []js.Value) any {
		target := args[0].Get("target")
		if target.Call("getAttribute", "data-testid").String() == "chat-input" {
			initialMode = target.Get("inputMode").String()
		}
		return nil
	})
	doc.Call("addEventListener", "focusin", onFocus)
	defer func() { doc.Call("removeEventListener", "focusin", onFocus); onFocus.Release() }()
	app, _ := conversationFixture(t, newDemoAPI(), 390, outgoingTranscript(), true)
	app.WaitUntil(time.Second, func() bool { return app.Find("id:chat-input").Focused })
	if initialMode != "none" {
		t.Fatalf("entry requested the keyboard with inputmode %q", initialMode)
	}
	app.Key("x")
	if app.Find("id:chat-input").Text != "x" {
		t.Fatal("the focused composer did not receive keyboard input")
	}
	app.Press("id:chat-input")
	field := doc.Get("activeElement")
	if field.Call("getAttribute", "data-testid").String() != "chat-input" || field.Get("inputMode").String() == "none" {
		t.Fatal("tap did not restore normal keyboard behavior")
	}
}
