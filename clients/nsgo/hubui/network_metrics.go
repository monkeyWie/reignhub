package hubui

import (
	"fmt"
	"strings"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/i18n"
)

func (s *State) networkDevice(id string) Object {
	for _, d := range s.devices {
		if str(d, "id") == id {
			return d
		}
	}
	return nil
}
func (s *State) networkValue(cx *core.Scope, id, key string) string {
	d := s.networkDevice(id)
	m := object(d["network"])
	if !flag(d, "connected") || len(m) == 0 {
		return "—"
	}
	switch key {
	case "responseMs":
		if _, ok := m[key]; !ok {
			return "—"
		}
		return i18n.FormatInteger(i18n.Language(cx), float64(number(m, key))) + " ms"
	case "uploadBps", "downloadBps":
		return networkRate(number(m, key))
	case "connectedMs":
		if number(m, key) < 0 {
			return "—"
		}
		seconds := number(m, key) / 1000
		return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	case "lastProgressMs":
		if number(m, key) < 0 {
			return "—"
		}
		return locales.For(cx).SecondsAgo(int(number(m, key) / 1000))
	case "pendingRequests":
		return i18n.FormatInteger(i18n.Language(cx), float64(number(m, key)))
	case "path":
		return connectivity.PathLabel(str(d, "path"))
	}
	return "—"
}

// Bind values individually so presence updates do not rebuild dialogs or reset
// their focus and animations. This card reads existing controller-local state.
func (s *State) networkCard(cx *core.Scope, id string) core.Node {
	return core.Dynamic(func(cx *core.Scope) core.Node {
		msgs := locales.For(cx)
		fields := []struct{ key, title, hint string }{
			{"responseMs", msgs.RequestLatency(), msgs.RequestLatencyHint()},
			{"connectedMs", msgs.ConnectionTime(), ""},
			{"uploadBps", msgs.UploadRate(), msgs.RateHint()},
			{"downloadBps", msgs.DownloadRate(), msgs.RateHint()},
			{"lastProgressMs", msgs.LastData(), ""},
			{"pendingRequests", msgs.PendingRequests(), ""},
		}
		cards := make([]core.Node, 0, len(fields))
		for _, field := range fields {
			value := dynamicText(cx, func() string { s.read(); return s.networkValue(cx, id, field.key) }, 13, false)
			value.TestID = "network-" + field.key
			value.Style.FontWeight = core.FontWeightSemibold
			item := core.View{Style: core.Style{MinWidth: core.Pt(0), Gap: 2}, Children: []core.Node{label(cx, field.title, 10, true), value}}
			if field.hint != "" {
				item.Tooltip = tooltipCard(cx, label(cx, field.hint, 12, false))
			}
			cards = append(cards, item)
		}
		return core.View{TestID: "connection-network", Style: core.Style{Padding: core.All(12), Gap: 8, CornerRadius: 10}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).muted} }, Children: []core.Node{
			singleLine(cx, func() string { s.read(); return msgs.CurrentChannel() + " · " + s.networkValue(cx, id, "path") }, 11, true),
			cardGrid(cx, "network-grid", 130, 10, cards...),
		}}
	})
}
func networkRate(bytes int64) string {
	if bytes >= 1<<20 {
		return fmt.Sprintf("%.1f MiB/s", float64(bytes)/(1<<20))
	}
	if bytes >= 1<<10 {
		return fmt.Sprintf("%.1f KiB/s", float64(bytes)/(1<<10))
	}
	return fmt.Sprintf("%d B/s", bytes)
}
func (s *State) networkSummary(id string) string {
	d := s.networkDevice(id)
	m := object(d["network"])
	if len(m) == 0 {
		return ""
	}
	// Fixed field names make copied diagnostics useful across UI languages.
	lines := []string{}
	for _, key := range []string{"responseMs", "lastResponseMs", "uploadBps", "downloadBps", "sentBytes", "receivedBytes", "pendingRequests", "lastProgressMs", "connectedMs"} {
		if value, ok := m[key]; ok {
			lines = append(lines, fmt.Sprintf("%s=%v", key, value))
		}
	}
	return strings.Join(lines, " ")
}
