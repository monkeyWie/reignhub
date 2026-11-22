package hubui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestPassiveNetworkCardLight(t *testing.T) { testPassiveNetworkCard(t, false) }
func TestPassiveNetworkCardDark(t *testing.T)  { testPassiveNetworkCard(t, true) }
func testPassiveNetworkCard(t *testing.T, dark bool) {
	var s *State
	route := "P2P"
	if dark {
		route = "Relay"
	}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		cx.SetTheme(core.DefaultTheme())
		s = newState(cx, newDemoAPI())
		s.applyColors()
		s.devices = []Object{{"id": "fixture", "connected": true, "path": route + " · QUIC", "network": Object{"responseMs": 51, "connectedMs": 62000, "uploadBps": 2048, "downloadBps": 4096, "pendingRequests": 1, "lastProgressMs": 2000}}}
		return core.View{Style: fill, Children: []core.Node{s.networkCard(cx, "fixture")}}
	}, nsgotest.Size(320, 450), nsgotest.Environment(core.Environment{Locale: "en-US", Dark: dark}))
	app.WaitFor("id:connection-network", time.Second)
	app.Find("text:Active channel · " + route + " · UDP")
	if app.Find("id:network-responseMs").Text != "51 ms" || app.Find("id:network-connectedMs").Text != "00:01:02" {
		t.Fatal("incorrect passive measurements")
	}
	if app.Exists("id:network-packetLoss") || app.Exists("text:Packet loss") {
		t.Fatal("unsupported metric displayed")
	}
	if app.Exists("id:network-requestFailurePercent") || app.Exists("text:Request failures") || len(app.Query("text:Response latency")) != 1 {
		t.Fatal("network card must have one latency metric and no request failure rate")
	}
	identity := app.Find("id:network-responseMs").ID
	app.Post(func() { s.cx.Post(func() { delete(object(s.devices[0]["network"]), "responseMs"); s.changed() }) })
	app.Settle()
	if app.Find("id:network-responseMs").Text != "—" || app.Find("id:network-responseMs").ID != identity {
		t.Fatal("missing sample remounted card or fabricated latency")
	}
	app.Post(func() {
		s.cx.Post(func() {
			if deviceStatus(s.devices[0]) != route+" · UDP" || !strings.Contains(s.connectionLog("fixture", nil), "path="+route+" · QUIC") {
				t.Error("device status must show UDP and diagnostics must identify QUIC")
			}
			if summary := s.networkSummary("fixture"); strings.Contains(summary, "requestFailure") || strings.Contains(summary, "requestSamples") {
				t.Error("removed failure metrics remain in copied diagnostics", summary)
			}
		})
	})
	app.Settle()
	app.Screenshot(fmt.Sprintf("passive-network-dark-%t", dark))
}
