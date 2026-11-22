package hubui

import (
	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func (s *State) deviceDialog(cx *core.Scope, id string, d ui.Dialog, close func()) core.Node {
	msgs := locales.For(cx)
	name := func() string { s.read(); return s.environmentName(id) }
	title := core.Text{ContentFunc: name, Role: core.RoleHeader, MaxLines: 2, Style: ui.TypeTitle}
	status := dynamicText(cx, func() string {
		s.read()
		switch {
		case s.connectionReady(id):
			path := connectivity.PathLabel(str(s.networkDevice(id), "path"))
			if path != "" {
				return msgs.DeviceConnected() + " · " + path
			}
			return msgs.DeviceConnected()
		case s.deviceOffline(id):
			return msgs.DeviceOffline()
		case s.connectionRecovering(id):
			return msgs.AgentConnecting()
		default:
			return msgs.DeviceDisconnected()
		}
	}, 12, true)
	status.TestID = "device-summary-status"
	icon := core.View{Style: core.Style{Width: core.Pt(42), Height: core.Pt(42), FlexShrink: 0, CornerRadius: 12, AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter},
		StyleFunc: func() core.Style { return core.Style{Background: palette(cx).muted} }, Children: []core.Node{s.hostGlyph(cx, id, 22)}}
	header := core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12}, Children: []core.Node{
		icon, core.View{Style: core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0), Gap: 3}, Children: []core.Node{title, status}},
		dialogCloseButton(cx, d.TestID+"-close", close),
	}}

	row := func(v core.View) core.View {
		v.Style.JustifyContent = core.JustifyStart
		v.Style.Gap, v.Style.MinHeight = 12, core.Pt(44)
		return v
	}
	details := row(button(cx, "device-details", msgs.DeviceConnectionDetails(), lucide.Activity, "ghost", func() {
		s.connectionDetailTarget = id
		s.dialog.Set("connection-status")
	}).(core.View))
	details.Children = append(details.Children, spacer(), glyph(cx, lucide.ChevronRight, 15))
	key := "disconnect:" + id
	disconnecting := func() bool { return s.actionPending(key) }
	connected := core.NewMemo(cx, func() bool { s.read(); return s.connectionReady(id) })
	action := core.Show{When: connected.Get, Then: func(cx *core.Scope) core.Node {
		return row(busyButton(cx, "disconnect-device", msgs.DisconnectDevice(), lucide.Unplug, "ghost", disconnecting, func() {
			s.call(key, "client.disconnect", Object{"id": id}, func(any) { s.cancelTargetCatalogue(id); close() })
		}).(core.View))
	}, Else: func(cx *core.Scope) core.Node {
		v := row(button(cx, "reconnect-device", msgs.ConnectDevice(), lucide.Plug, "ghost", func() {
			s.connectSavedDevice(id)
			close()
		}).(core.View))
		v.Disabled = func() bool { s.read(); return s.connection(id).reconnecting || s.deviceOffline(id) || disconnecting() }
		return v
	}}
	remove := row(button(cx, "forget-device", msgs.RemoveDevice(), lucide.Trash, "ghost", func() { s.dialog.Set("forget") }).(core.View))
	remove.Disabled = disconnecting
	remove.StyleFunc = func() core.Style { return pressStyle(cx).Merge(core.Style{Color: ui.ColorsOf(cx).Now().Danger}) }
	line := core.View{Style: core.Style{Height: core.Pt(1), Margin: core.Symmetric(4, 12)}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).border} }}
	d.LabelFunc = name
	d.Style.Width = core.Pt(368)
	d.Children = []core.Node{core.View{Style: core.Style{Gap: 2}, Children: []core.Node{details, action, line, remove}},
		core.Show{When: func() bool { s.read(); return s.error != "" }, Then: func(cx *core.Scope) core.Node {
			return dynamicText(cx, func() string { s.read(); return s.error }, 12, false)
		}}}
	return modalWithHeader(cx, d, header)
}
