package hubui

import (
	"cmp"
	"reflect"
	"slices"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

// The landing page occupies the conversation pane; the aggregate sidebar and
// its catalogue lifetimes remain mounted while users inspect local readiness.
func (s *State) home(cx *core.Scope) core.Node {
	if !s.api.CanHost() {
		return s.welcome(cx)
	}
	return core.View{TestID: "workspace-home", Style: fill, StyleFunc: func() core.Style { return s.workspaceSurface(cx) }, Children: []core.Node{
		core.Scroll{TestID: "home-scroll", Style: fill, Children: []core.Node{
			core.View{Style: core.Style{Width: core.Pct(100), MaxWidth: core.Pt(820), AlignSelf: core.AlignCenter, Padding: core.All(32), Gap: 28}, Children: []core.Node{
				label(cx, locales.For(cx).LocalManagement(), 22, false),
				s.localOverview(cx, "home-"),
			}},
		}},
	}}
}

func (s *State) local(cx *core.Scope) core.Node {
	return core.Scroll{TestID: "local-dashboard", Style: fill, Children: []core.Node{
		core.View{Style: core.Style{Padding: core.Edges{Top: 4, Right: 4, Bottom: 12}}, Children: []core.Node{s.localOverview(cx, "")}},
	}}
}

func actionRow(nodes ...core.Node) core.Node {
	return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 8}, Children: nodes}
}

func (s *State) localOverview(cx *core.Scope, prefix string) core.Node {
	msgs := locales.For(cx)
	hasID := func() bool { s.read(); return str(s.host, "connectionId") != "" }
	copyID := s.copyButton(cx, prefix+"copy-device-id", msgs.CopyDeviceID(), "outline", func() string { return str(s.host, "connectionId") }, func() bool { return !hasID() })
	qr := button(cx, prefix+"show-qr", msgs.DeviceQRCode(), lucide.QrCode, "outline", func() { s.dialog.Set("qr") }).(core.View)
	qr.Disabled = func() bool { s.read(); return !hasID() || len(object(s.host["qr"])) == 0 }
	return core.View{Style: core.Style{Gap: 28, MinWidth: core.Pt(0)}, Children: []core.Node{
		contentSection(cx, "", "",
			row(identityGlyph(cx, platformIconKey(platformClientInfo().Platform), 22), singleLine(cx, func() string {
				s.read()
				if name := str(s.host, "name"); name != "" {
					return name
				}
				return msgs.LocalDevice()
			}, 16, false), spacer(), dynamicText(cx, func() string { s.read(); return str(s.host, "status") }, 12, true), core.Show{When: hasID, Then: func(cx *core.Scope) core.Node { return s.connectionIndicator(cx, func() string { return "" }, prefix) }}),
			label(cx, msgs.YourDeviceID(), 12, true),
			core.Text{TestID: prefix + "device-code", ContentFunc: func() string {
				s.read()
				v := str(s.host, "connectionId")
				if len(v) == 6 {
					return v[:3] + " " + v[3:]
				}
				if v != "" {
					return v
				}
				return msgs.DeviceNotRegistered()
			}, Selectable: core.SelectableOn, Style: core.Style{FontSize: 36, FontWeight: core.FontWeightSemibold, LetterSpacing: 2}},
			singleLine(cx, func() string { s.read(); return str(s.host, "serviceUrl") }, 12, true),
			actionRow(copyID, qr, row(
				labelledIconButton(cx, prefix+"configure-service", msgs.ConfigureService(), lucide.Settings2, func() { s.openSettings("connection") }),
				labelledIconButton(cx, prefix+"manage-access", msgs.AccessManagement(), lucide.ShieldCheck, func() { s.settingsReturn = ""; s.dialog.Set("local-access") }))),
		),
		sectionDivider(cx),
		contentSection(cx, "", "",
			row(label(cx, msgs.LocalAgents(), 16, false), spacer(), labelledIconButton(cx, prefix+"manage-agents", msgs.ManageAgents(), lucide.Settings2, func() { s.openSettings("agents") })),
			core.Show{When: func() bool { s.read(); return len(s.localProviders) == 0 }, Then: func(cx *core.Scope) core.Node {
				return dynamicText(cx, func() string {
					s.read()
					if !s.ready || s.localPolling {
						return msgs.DetectingAgents()
					}
					return msgs.NoLocalAgents()
				}, 13, true)
			}},
			core.Each(func() []Object { s.read(); return s.localProviders }, func(p Object) string { return str(p, "id") }, func(cx *core.Scope, p func() Object, _ func() int) core.Node {
				return core.View{TestID: prefix + "agent-" + str(p(), "id"), Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12, Padding: core.Symmetric(10, 0), MinWidth: core.Pt(0)}, Children: []core.Node{
					agentGlyph(cx, str(p(), "id"), 20),
					agentLabel(cx, p),
					s.agentControls(cx, prefix, p, func(kind string) { s.settingsReturn = ""; s.dialog.Set(kind) }),
				}}
			}),
		),
	}}
}

// Controller identities and revocation are intentionally one level deeper.
func (s *State) localAccess(cx *core.Scope) core.Node {
	grants := core.NewMemoFunc(cx, func() []Object {
		s.read()
		rows := slices.Clone(objects(s.host["grants"]))
		slices.SortFunc(rows, func(a, b Object) int {
			if order := cmp.Compare(number(b, "authorizedAt"), number(a, "authorizedAt")); order != 0 {
				return order
			}
			return cmp.Compare(str(a, "client"), str(b, "client"))
		})
		return rows
	}, func(a, b []Object) bool { return reflect.DeepEqual(a, b) })
	return core.View{Style: core.Style{Gap: 20}, Children: []core.Node{
		label(cx, "首次连接需本机确认；配置密码后，输入正确密码可自动授权。", 13, true),
		actionRow(button(cx, "set-password", "设置访问密码", nil, "outline", func() { s.settingsReturn = "local-access"; s.dialog.Set("password") }), core.Show{When: func() bool { s.read(); return flag(s.host, "passwordEnabled") }, Then: func(cx *core.Scope) core.Node {
			return s.actionButton(cx, "clear-password", "关闭密码访问", nil, "ghost", func() { s.call("clear-password", "host.password", Object{"clearPassword": true}, nil) })
		}}),
		sectionDivider(cx),
		label(cx, locales.For(cx).AuthorizedControllers(), 15, false),
		core.Show{When: func() bool { return len(grants.Get()) == 0 }, Then: func(cx *core.Scope) core.Node { return label(cx, locales.For(cx).NoAuthorizedControllers(), 13, true) }},
		core.Scroll{TestID: "authorized-controllers", StyleFunc: func() core.Style {
			return core.Style{MaxHeight: core.Pt(min(360, max(120, cx.Env().Window().Height-340))), MinHeight: core.Pt(0)}
		}, Children: []core.Node{core.Each(grants.Get, func(g Object) string { return str(g, "client") }, func(cx *core.Scope, g func() Object, _ func() int) core.Node {
			client := str(core.Untracked(cx, g), "client")
			action := "revoke-" + client
			authorizedAt := core.Show{When: func() bool { return number(g(), "authorizedAt") > 0 }, Then: func(cx *core.Scope) core.Node {
				return singleLine(cx, func() string {
					at := s.localTime(time.UnixMilli(number(g(), "authorizedAt")))
					return locales.For(cx).ControllerAuthorizedAt(at.Format("2006-01-02 15:04"))
				}, 11, true)
			}}
			return row(controllerIdentity(cx, g, authorizedAt), spacer(), s.actionButton(cx, action, "撤销授权", nil, "ghost", func() {
				s.call(action, "host.revoke", Object{"client": client}, nil)
			}))
		})}},
	}}
}
