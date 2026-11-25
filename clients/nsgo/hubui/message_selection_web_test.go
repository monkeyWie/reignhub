//go:build js && wasm

package hubui

import (
	"strings"
	"syscall/js"
	"testing"

	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestBrowserSelectAllStaysInOneMessage(t *testing.T) {
	if nsgotest.Target() != "web" {
		t.Skip("requires browser selection")
	}
	app := startSelectionFixture(t,
		Object{"id": "user", "role": "user", "text": "User first\n\nUser second"},
		Object{"id": "agent", "role": "assistant", "text": "Agent first\n\nAgent second"},
	)
	doc := js.Global().Get("document")
	sel := doc.Call("getSelection")
	for _, sample := range []struct{ id, prefix string }{{"user", "User"}, {"agent", "Agent"}} {
		body := doc.Call("querySelector", `[data-testid="message-body-turn|`+sample.id+`"]`)
		for _, action := range []string{"keyboard", "browser command"} {
			// Blur the composer as a press on the message would, then select
			// a word in the first paragraph of this actual rendered message.
			doc.Get("activeElement").Call("blur")
			walker := doc.Call("createTreeWalker", body, 4)
			node := walker.Call("nextNode")
			r := doc.Call("createRange")
			r.Call("setStart", node, 0)
			r.Call("setEnd", node, len(sample.prefix))
			sel.Call("removeAllRanges")
			sel.Call("addRange", r)
			if action == "keyboard" {
				app.Key("mod+a")
			} else {
				doc.Call("execCommand", "selectAll")
			}
			expanded := sel.Call("getRangeAt", 0)
			text := strings.Join(strings.Fields(expanded.Call("toString").String()), "")
			if !body.Call("contains", expanded.Get("commonAncestorContainer")).Bool() || text != sample.prefix+"first"+sample.prefix+"second" {
				t.Fatalf("%s in %s selected %q", action, sample.id, text)
			}
		}
	}
	app.Type("id:chat-input", "draft")
	input := doc.Get("activeElement")
	doc.Call("execCommand", "selectAll")
	if input.Get("selectionStart").Int() != 0 || input.Get("selectionEnd").Int() != 5 {
		t.Fatal("message Select All captured the composer's selection")
	}
}
