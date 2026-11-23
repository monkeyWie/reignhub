package hubui

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"github.com/liyue201/goqr"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
	qrcode "github.com/skip2/go-qrcode"
)

func (s *State) dialogView(cx *core.Scope, kind string) core.Node {
	if strings.HasPrefix(kind, "agent-help:") {
		return s.agentHelp(cx, strings.TrimPrefix(kind, "agent-help:"))
	}
	if kind == "remove-session" {
		return s.removeSessionDialog(cx)
	}
	if kind == "attachment-download" {
		return s.downloadView(cx)
	}
	if kind == "settings" || strings.HasPrefix(kind, "settings:") {
		return s.preferences(cx, strings.TrimPrefix(kind, "settings:"))
	}
	if kind == "connection-status" {
		mode := core.NewMemo(cx, func() string {
			s.read()
			id := s.connectionDetailTarget
			a := s.connection(id).attempt
			active := a != nil && a.active
			return fmt.Sprintf("%s|%p|%t|%s", id, a, active, s.connectionDetails(id))
		})
		return core.Dynamic(func(cx *core.Scope) core.Node {
			_ = mode.Get()
			id := s.connectionDetailTarget
			return s.connectionPanel(cx, id, s.connection(id).attempt, false)
		})
	}
	if kind == "connecting" {
		mode := core.NewMemo(cx, func() string {
			s.read()
			if s.connectAttempt == nil {
				return ""
			}
			return fmt.Sprintf("%p|%t", s.connectAttempt, s.connectAttempt.active)
		})
		return core.Dynamic(func(cx *core.Scope) core.Node { _ = mode.Get(); return s.connectAttemptView(cx) })
	}
	if kind == "" {
		return nil
	}
	mounted := true
	dialogTarget := s.target
	if (kind == "device" || kind == "forget") && s.deviceMenuTarget != "" {
		dialogTarget = s.deviceMenuTarget
	}
	cx.Cleanup(func() { mounted = false })
	close := func() {
		if mounted {
			s.closeDialog()
		}
	}
	pending := func() bool {
		key := ""
		switch {
		case kind == "agent-config" || strings.HasPrefix(kind, "agent-config:"):
			key = "save-agent"
		case kind == "account":
			return s.actionPending("account-start") || s.actionPending("account-refresh")
		case kind == "password" || kind == "service-settings":
			key = "dialog-save"
		case strings.HasPrefix(kind, "activate:"):
			key = "confirm-activate"
		case kind == "create":
			key = "create-session-confirm:" + dialogTarget
		case kind == "clear-cache":
			key = "confirm-clear-cache:" + dialogTarget
		case kind == "forget":
			key = "confirm-forget:" + dialogTarget
		case strings.HasPrefix(kind, "authorize:"):
			key = "host-respond:" + strings.TrimPrefix(kind, "authorize:")
		}
		return key != "" && s.actionPending(key)
	}
	title := ""
	children := []core.Node{}
	cancelButton := button(cx, "dialog-cancel", "取消", nil, "outline", close).(core.View)
	cancelButton.Label = ""
	cancelButton.LabelFunc = func() string { return choose(pending(), "关闭", "取消") }
	cancelButton.Children = []core.Node{dynamicText(cx, func() string { return choose(pending(), "关闭", "取消") }, 13, false)}
	footer := []core.Node{cancelButton}
	field := func(id, placeholder string, value *core.Signal[string], masked bool) core.Node {
		return ui.Input{TestID: id, Placeholder: placeholder, Label: placeholder, Value: value.Get, OnChange: value.Set, Disabled: pending, Masked: masked, Style: core.Style{MinHeight: core.Pt(40), FontSize: 13}}
	}
	d := ui.Dialog{TestID: "dialog-" + strings.Split(kind, ":")[0], Open: func() bool { return true }, OnOpenChange: func(v bool, _ base.Reason) {
		if !v {
			close()
		}
	}, Style: core.Style{Width: core.Pt(448), MaxWidth: core.Pct(96)}}
	switch {
	case kind == "agent-config" || strings.HasPrefix(kind, "agent-config:"):
		title = "Agent 接入配置"
		idValue := ""
		if strings.HasPrefix(kind, "agent-config:") {
			idValue = strings.TrimPrefix(kind, "agent-config:")
		}
		def := object(s.agentDefinitions[idValue])
		if def == nil {
			def = Object{"name": "My ACP Agent", "protocol": "acp", "command": "", "args": []string{}, "env": Object{}}
		}
		initial, _ := json.MarshalIndent(def, "", "  ")
		id := core.NewSignal(cx, idValue)
		settings := core.NewSignal(cx, string(initial))
		children = append(children, label(cx, "Agent 在这台电脑上运行。先安装并登录对应 CLI；参数为 JSON 数组，每个元素是一个原样参数。", 13, true), field("agent-id", "Agent ID（如 my-agent）", id, false), core.Input{TestID: "agent-settings", Disabled: pending, Label: "Agent 配置 JSON", Value: settings.Get, OnChange: settings.Set, Multiline: true, Style: core.Style{Height: core.Pt(230), FontSize: 12}, StyleFunc: func() core.Style { return textInputStyle(cx) }}, label(cx, "支持 acp 和 pi。保存后重启 ReignHub 生效。环境变量只保存在本机，不会同步给控制端。", 12, true))
		footer = append(footer, s.actionButton(cx, "save-agent", "保存配置", nil, "primary", func() {
			var value Object
			if err := json.Unmarshal([]byte(settings.Get()), &value); err != nil {
				s.error = "配置 JSON 无效：" + err.Error()
				s.changed()
				return
			}
			s.call("save-agent", "host.agent.save", Object{"id": id.Get(), "agent": value}, func(any) { close(); s.showNotice("Agent 配置已保存，重启 ReignHub 后生效") })
		}))
	case kind == "account":
		title = "账号与设备同步"
		footer = nil
		prefix := "client.account."
		origin := serviceOrigin()
		if s.api.CanHost() && str(s.host, "serviceUrl") != "" {
			origin = str(s.host, "serviceUrl")
		}
		service := core.NewSignal(cx, origin)
		params := Object{"service": service.Get(), "name": s.clientName}
		if !s.api.CanHost() {
			children = append(children, field("account-service", "服务地址", service, false))
		}
		if s.api.CanHost() {
			prefix = "host.account."
		}
		children = append(children, label(cx, "登录后可同步设备。首次连接仍需主机确认或访问密码；自建开源服务无需登录。", 13, true))
		if s.accountLogin != nil {
			children = append(children, label(cx, "授权码："+str(s.accountLogin, "code"), 17, false), s.copyButton(cx, "copy-login-code", "复制授权码", "outline", func() string { return str(s.accountLogin, "code") }), button(cx, "open-login-page", "在浏览器中打开登录页面", nil, "outline", func() { cx.OpenURL(str(s.accountLogin, "url"), nil) }))
		}
		if s.accountStatus != nil {
			children = append(children, label(cx, choose(flag(s.accountStatus, "linked"), "账号已绑定 · "+str(s.accountStatus, "plan"), "尚未绑定账号"), 13, false))
		}
		for _, entry := range s.accountDevices {
			d := entry
			children = append(children, button(cx, "account-device-"+str(d, "id"), str(d, "name")+" · "+str(d, "connectionId"), nil, "outline", func() {
				close()
				s.connect(Object{"service": service.Get(), "code": str(d, "connectionId"), "public": str(d, "public"), "remember": true, "name": s.clientName})
			}))
		}
		footer = append(footer, s.actionButton(cx, "account-start", "登录与绑定", nil, "primary", func() {
			params["service"] = service.Get()
			s.call("account-start", prefix+"start", params, func(v any) { s.accountLogin = object(v); cx.OpenURL(str(s.accountLogin, "url"), nil) })
		}), s.actionButton(cx, "account-refresh", "刷新账号设备", nil, "outline", func() {
			params := Object{"service": service.Get(), "name": s.clientName}
			loginID := str(s.accountLogin, "id")
			s.runAction("account-refresh", func(ctx context.Context) (any, error) {
				if loginID != "" {
					if _, err := s.api.Call(ctx, prefix+"poll", Object{"service": params["service"], "name": params["name"], "id": loginID}, "", nil); err != nil {
						return nil, err
					}
				}
				status, err := s.api.Call(ctx, prefix+"status", params, "", nil)
				if err != nil {
					return nil, err
				}
				devices, err := s.api.Call(ctx, prefix+"devices", params, "", nil)
				return Object{"status": status, "devices": object(devices)["devices"]}, err
			}, func(v any) {
				result := object(v)
				s.accountStatus = object(result["status"])
				s.accountDevices = objects(result["devices"])
			})
		}))
	case kind == "session-access-help":
		help := sessionAccessHelp(s.error, s.failure)
		if help.Title == "" {
			return nil
		}
		title = "接入 Codex 后继续会话"
		for i, step := range help.Steps {
			children = append(children, label(cx, fmt.Sprintf("%d. %s", i+1, step), 13, false))
		}
		children = append(children, label(cx, "关闭这份指引不会执行重启，也不会自动重发消息。", 12, true))
		footer = []core.Node{button(cx, "session-help-done", "知道了", nil, "primary", close)}
	case kind == "connect":
		title = "连接设备"
		service := core.NewSignal(cx, serviceOrigin())
		code := core.NewSignal(cx, "")
		password := core.NewSignal(cx, "")
		remember := core.NewSignal(cx, true)
		relay := core.NewSignal(cx, false)
		if s.scanned != nil {
			service.Set(str(s.scanned, "service"))
			initialCode := str(s.scanned, "connectionId")
			if initialCode == "" {
				initialCode = str(s.scanned, "code")
			}
			code.Set(initialCode)
		}
		if !isWeb() {
			children = append(children, field("connection-service", "服务地址（HTTP / HTTPS）", service, false))
		}
		children = append(children, field("connection-code", "设备 ID，例如 123 456", code, false), field("connection-password", "访问密码（留空则请求主机确认）", password, true), ui.Checkbox{TestID: "remember-device", Text: "记住此设备授权", Checked: remember.Get, OnChange: remember.Set}, ui.Checkbox{TestID: "relay-only", Text: "仅用中继（用于诊断）", Checked: relay.Get, OnChange: relay.Set}, label(cx, "当前设备会保存配对身份与会话缓存，请仅在可信设备上使用。", 12, true))
		footer = append(footer, button(cx, "connect-submit", "连接", nil, "primary", func() {
			p := Object{"service": strings.TrimRight(strings.TrimSpace(service.Get()), "/"), "code": strings.NewReplacer(" ", "", "-", "").Replace(code.Get()), "password": password.Get(), "remember": remember.Get(), "relayOnly": relay.Get(), "name": s.clientName}
			if s.scanned != nil {
				p["public"] = s.scanned["public"]
			}
			s.scanned = nil
			close()
			s.connect(p)
		}))
	case kind == "service-settings" || kind == "password":
		url := str(s.host, "serviceUrl")
		if url == "" {
			url = serviceOrigin()
		}
		service := core.NewSignal(cx, url)
		name := core.NewSignal(cx, str(s.host, "name"))
		password := core.NewSignal(cx, "")
		if kind == "password" {
			title = "设置访问密码"
			children = append(children, field("access-password", "8–72 字节的访问密码", password, true))
		} else {
			title = "本机服务设置"
			children = append(children, button(cx, "account-settings", "账号与设备同步", nil, "outline", func() { s.dialog.Set("account") }))
			children = append(children, field("server-url", "服务地址", service, false), field("device-name", "设备名称", name, false), label(cx, "优先尝试 P2P 直连；允许中继时自动回退到所选服务的 WSS 中继。", 12, true))
		}
		footer = append(footer, s.actionButton(cx, "dialog-save", "保存", nil, "primary", func() {
			if kind == "password" {
				s.call("dialog-save", "host.password", Object{"password": password.Get()}, func(any) { close() })
			} else {
				s.call("dialog-save", "host.configure", Object{"serviceUrl": strings.TrimSpace(service.Get()), "name": strings.TrimSpace(name.Get())}, func(any) { close() })
			}
		}))
	case kind == "qr":
		title = "扫描连接这台电脑"
		code := pairingCode(object(s.host["qr"]))
		png, e := qrcode.Encode(code, qrcode.Medium, 250)
		if e != nil {
			children = append(children, label(cx, e.Error(), 12, true))
		} else {
			children = append(children, core.Image{Source: core.ImageSource{Data: png}, ResizeMode: core.Contain, Style: core.Style{Width: core.Pt(250), Height: core.Pt(250), AlignSelf: core.AlignCenter, Background: core.RGB(0xffffff)}})
		}
		children = append(children, label(cx, "扫码后仍需主机确认或密码授权。", 12, true), s.copyButton(cx, "copy-pairing", "复制连接信息", "outline", func() string { return code }))
		footer = []core.Node{button(cx, "dialog-done", "完成", nil, "primary", close)}
	case strings.HasPrefix(kind, "authorize:"):
		return s.hostAuthorizationDialog(cx, strings.TrimPrefix(kind, "authorize:"), close)
	case strings.HasPrefix(kind, "activate:"):
		provider := strings.TrimPrefix(kind, "activate:")
		var p Object
		for _, v := range s.localProviders {
			if str(v, "id") == provider {
				p = v
				break
			}
		}
		l := locales.For(cx)
		title = l.RestartCodex()
		children = append(children, label(cx, l.RestartCodexConfirm(), 13, false))
		footer = append(footer, s.actionButton(cx, "confirm-activate", title, nil, "primary", func() {
			s.activateCodex("confirm-activate", p, true, close)
		}))
	case kind == "local-access":
		title = locales.For(cx).AccessManagement()
		children = append(children, s.localAccess(cx))
		footer = nil
	case kind == "local":
		title = locales.For(s.cx).LocalManagement()
		d.Style.Width = core.Pt(740)
		children = append(children, core.View{Style: core.Style{MinHeight: core.Pt(0)}, StyleFunc: func() core.Style {
			return core.Style{Height: core.Pt(min(600, max(160, cx.Env().Window().Height-160)))}
		}, Children: []core.Node{s.local(cx)}})
		footer = nil
	case kind == "device":
		return s.deviceDialog(cx, dialogTarget, d, close)
	case kind == "create":
		title = "新建会话"
		locations := s.createProject
		cwd := core.NewSignal(cx, "")
		target := core.NewSignal(cx, dialogTarget)
		agent := core.NewSignal(cx, "")
		environments := []ui.Option[string]{}
		for _, device := range s.devices {
			id := str(device, "id")
			if s.connectionReady(id) && (locations == nil || projectLocationFor(locations, id).Target != "") {
				environments = append(environments, ui.Option[string]{Value: id, Label: s.environmentName(id)})
			}
		}
		valid := false
		for _, option := range environments {
			if option.Value == target.GetUntracked() {
				valid = true
			}
		}
		if !valid && len(environments) > 0 {
			target.Set(environments[0].Value)
		} else if !valid {
			target.Set("")
		}
		seed := func(id string) {
			location := projectLocationFor(locations, id)
			cwd.Set(location.Path)
			agent.Set(location.Provider)
		}
		seed(target.GetUntracked())
		children = append(children, ui.Select[string]{TestID: "new-session-device", Disabled: pending, Label: locales.For(s.cx).ExecutionDevice(), Options: environments, Value: target.Get, OnChange: func(id string) { target.Set(id); seed(id) }},
			core.Dynamic(func(cx *core.Scope) core.Node {
				id := target.Get()
				s.read()
				options := []ui.Option[string]{}
				for _, p := range s.workspaces[id].providers {
					if providerReady(p) && flag(object(p["capabilities"]), "create") {
						options = append(options, ui.Option[string]{Value: str(p, "id"), Label: str(p, "name")})
					}
				}
				selected := agent.GetUntracked()
				found := false
				for _, p := range options {
					if p.Value == selected {
						found = true
					}
				}
				if !found {
					selected = ""
					if len(options) > 0 {
						selected = options[0].Value
					}
					agent.Set(selected)
				}
				if len(options) == 0 {
					return label(cx, locales.For(s.cx).NoCreateAgent(), 13, true)
				}
				return ui.Select[string]{TestID: "new-session-provider", Disabled: pending, Label: "Agent", Options: options, Value: agent.Get, OnChange: agent.Set}
			}), field("new-session-cwd", locales.For(s.cx).ExecutionDirectory(), cwd, false),
		)
		footer = append(footer, busyButton(cx, "create-session-confirm", "创建", nil, "primary", pending, func() {
			id, p, directory := target.Get(), agent.Get(), cwd.Get()
			if p == "" || id == "" || !s.connectionReady(id) {
				s.error = locales.For(s.cx).ConnectAndSelectAgent()
				s.changed()
				return
			}
			s.runAction("create-session-confirm:"+dialogTarget, func(ctx context.Context) (any, error) {
				return rpc(ctx, s.api, id, p, "", "create", Object{"options": Object{"cwd": directory}})
			}, func(v any) {
				session := copyObject(object(v))
				session["deviceId"] = id
				s.cancelTargetCatalogue(id)
				s.applyEnvironmentPage(id, p, []Object{session}, false)
				if mounted {
					close()
					s.selectSession(session)
				}
				s.poll()
			})
		}))

	case kind == "clear-cache":
		title = "清除这台设备的附件缓存？"
		children = append(children, label(cx, "仅清除控制端已下载的附件，下次查看时会从被控端重新读取。", 13, true))
		target := dialogTarget
		footer = append(footer, busyButton(cx, "confirm-clear-cache", "清除缓存", nil, "primary", pending, func() {
			s.runAction("confirm-clear-cache:"+dialogTarget, func(ctx context.Context) (any, error) {
				return s.api.Call(ctx, "client.assetCache.clear", Object{}, target, nil)
			}, func(any) { close(); s.resetImages(); s.showNotice("附件缓存已清除") })
		}))
	case kind == "forget":
		title = "移除设备？"
		children = append(children, label(cx, "删除本机保存的配对身份与该设备的消息、附件缓存和未发送草稿。", 13, false))
		id := dialogTarget
		footer = append(footer, busyButton(cx, "confirm-forget", "移除", nil, "primary", pending, func() {
			s.persistDraft()
			if s.draftWriter != nil {
				s.draftWriter.block(id, true)
			}
			s.runAction("confirm-forget:"+dialogTarget, func(ctx context.Context) (any, error) {
				if s.draftWriter != nil {
					_ = s.draftWriter.flush(ctx)
				}
				_, err := s.api.Call(ctx, "client.forget", Object{"id": id}, "", nil)
				return removalResult{err}, nil
			}, func(v any) {
				if err := v.(removalResult).err; err != nil {
					if s.draftWriter != nil {
						s.draftWriter.block(id, false)
					}
					s.error = err.Error()
					return
				}
				close()
				if s.target == id {
					s.selectDevice("")
				}
				for key, d := range s.drafts {
					if strings.HasPrefix(key, id+"|") {
						for _, u := range d.Uploads {
							u.cancelled.Store(true)
							if u.cancel != nil {
								u.cancel()
							}
						}
						delete(s.drafts, key)
						delete(s.draftLoads, key)
					}
				}
				for key := range s.draftLoads {
					if strings.HasPrefix(key, id+"|") {
						delete(s.draftLoads, key)
					}
				}
				s.clearLastSession(id, "", "")
				if s.draftWriter != nil {
					s.draftWriter.block(id, false)
				}
				s.poll()
			})
		}))
	case kind == "scan":
		return s.scanView(cx, close)
	case kind == "preview":
		return s.imagePreview(cx, close)
	default:
		return nil
	}
	d.Title = title
	children = append(children, core.Show{When: pending, Then: func(cx *core.Scope) core.Node {
		return label(cx, "请求处理中，关闭窗口不会取消已提交的操作。", 12, true)
	}}, core.Show{When: func() bool { s.read(); return s.error != "" }, Then: func(cx *core.Scope) core.Node {
		return dynamicText(cx, func() string { s.read(); return s.error }, 12, false)
	}})
	d.Children = children
	d.Footer = footer
	return modalDialog(cx, d, close)
}

// Keep dismissal in the header; the footer belongs to the dialog's decisions.
func modalDialog(cx *core.Scope, d ui.Dialog, close func()) ui.Dialog {
	if d.Label == "" {
		d.Label = d.Title
	}
	return modalWithHeader(cx, d, dialogHeader(cx, d.TestID+"-close", d.Title, close))
}

func modalWithHeader(cx *core.Scope, d ui.Dialog, header core.Node) ui.Dialog {
	children := []core.Node{header}
	if d.Description != "" {
		children = append(children, label(cx, d.Description, 13, true))
	}
	d.Title, d.Description = "", ""
	if ui.Touch(cx) {
		// The sheet's max height alone clips a long form's footer. Let only
		// its body shrink/scroll, keeping the close and decision actions visible.
		d.Children = []core.Node{header, core.Scroll{TestID: d.TestID + "-body", Style: core.Style{FlexShrink: 1, MinHeight: core.Pt(0)}, Children: []core.Node{
			core.View{Style: core.Style{Gap: 12}, Children: append(children[1:], d.Children...)},
		}}}
		for i, node := range d.Footer {
			if action, ok := node.(core.View); ok {
				action.Style.MinHeight = core.Pt(44)
				d.Footer[i] = action
			}
		}
		style := d.StyleFunc
		d.StyleFunc = func() core.Style {
			var st core.Style
			if style != nil {
				st = style()
			}
			keyboard := cx.Env().KeyboardHeight()
			return st.Merge(core.Style{Width: core.Pct(100), MaxWidth: core.Pct(100), Bottom: core.Pt(keyboard),
				MaxHeight: core.Pt(max(0, cx.Env().Window().Height-keyboard-max(16, cx.Env().SafeArea().Top)))})
		}
	} else {
		d.Children = append(children, d.Children...)
	}
	return d
}

func dialogHeader(cx *core.Scope, id, title string, close func()) core.Node {
	x := dialogCloseButton(cx, id, close)
	t := core.Text{Content: title, Role: core.RoleHeader, Style: ui.TypeTitle.Merge(core.Style{FlexGrow: 1, FlexShrink: 1, MinWidth: core.Pt(0)})}
	return core.View{TestID: id + "-header", Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 12, Margin: core.Edges{Top: -8, Right: -8}}, Children: []core.Node{t, x}}
}

func dialogCloseButton(cx *core.Scope, id string, close func()) core.View {
	x := labelledIconButton(cx, id, locales.For(cx).CloseDialog(), lucide.X, close).(core.View)
	x.Style.Width, x.Style.Height = core.Pt(36), core.Pt(36)
	x.Style.Padding = core.All(8)
	if ui.Touch(cx) {
		x.Style.Width, x.Style.Height = core.Pt(44), core.Pt(44)
	}
	return x
}

func (s *State) hostAuthorizationDialog(cx *core.Scope, id string, close func()) core.Node {
	mounted := true
	cx.Cleanup(func() { mounted = false })
	find := func() Object {
		s.read()
		for _, p := range objects(s.host["pending"]) {
			if str(p, "id") == id {
				return p
			}
		}
		return nil
	}
	request := core.Untracked(cx, find)
	expired := core.NewSignal(cx, request == nil)
	if deadline := number(request, "expires"); deadline > 0 {
		remaining := time.Until(time.Unix(deadline, 0))
		if remaining <= 0 {
			expired.Set(true)
		} else {
			cx.AfterFunc(remaining, func() { expired.Set(true) })
		}
	}
	ended := core.NewMemo(cx, func() bool { return expired.Get() || find() == nil })
	return core.Dynamic(func(cx *core.Scope) core.Node {
		terminal := ended.Get()
		title := choose(terminal, "连接请求已失效", "允许远程连接？")
		children := []core.Node{controllerIdentity(cx, func() Object { return request })}
		var footer []core.Node
		if terminal {
			children = append(children, label(cx, "此授权请求已过期或已被撤回。请在控制端重新发起连接。", 13, true))
		} else {
			children = append(children, label(cx, "请求访问这台电脑上的 Agent。", 13, false), label(cx, "设备指纹："+str(request, "fingerprint"), 11, true), label(cx, choose(flag(request, "remember"), "允许后记住此控制设备，可在授权列表撤销。", "临时授权 8 小时，期间可再次连接。"), 12, true))
			pending := func() bool { return s.actionPending("host-respond:" + id) }
			respond := func(allow bool) {
				s.runAction("host-respond:"+id, func(ctx context.Context) (any, error) {
					v, err := s.api.Call(ctx, "host.respond", Object{"id": id, "allow": allow}, "", nil)
					// The host can reject a stale local snapshot before its next poll.
					if err != nil && strings.Contains(err.Error(), "授权请求已过期") {
						return false, nil
					}
					return v, err
				}, func(v any) {
					if valid, ok := v.(bool); ok && !valid {
						if mounted {
							expired.Set(true)
						}
						return
					}
					close()
					s.pollLocal()
				})
			}
			footer = append(footer, busyButton(cx, "deny-host-request", "拒绝", nil, "outline", pending, func() { respond(false) }), busyButton(cx, "approve-host-request", "允许连接", nil, "primary", pending, func() { respond(true) }))
		}
		return modalDialog(cx, ui.Dialog{TestID: "dialog-authorize", Title: title, Open: func() bool { return true }, OnOpenChange: func(open bool, _ base.Reason) {
			if !open {
				close()
			}
		}, Style: core.Style{Width: core.Pt(448), MaxWidth: core.Pct(96)}, Children: children, Footer: footer}, close)
	})
}
func (s *State) imagePreview(cx *core.Scope, close func()) core.Node {
	index := core.NewMemo(cx, func() int { return s.previewIndex.Get() })
	move := func(delta int) {
		if len(s.preview) > 0 {
			s.previewIndex.Set((index.Get() + delta + len(s.preview)) % len(s.preview))
		}
	}
	cx.OnKey("left", func() { move(-1) })
	cx.OnKey("right", func() { move(1) })
	toolbar := row(dynamicText(cx, func() string { return fmt.Sprintf("%d / %d", index.Get()+1, len(s.preview)) }, 12, true), spacer(), contextualIconButton(cx, "preview-download", "下载图片", lucide.Download, func() {
		if len(s.preview) > 0 {
			s.download(cx, s.preview[index.Get()])
		}
	}), contextualIconButton(cx, "preview-close", "关闭图片预览", lucide.X, close))
	content := core.Dynamic(func(cx *core.Scope) core.Node {
		if len(s.preview) == 0 {
			return nil
		}
		a := s.preview[index.Get()]
		key := s.key() + "|" + str(a, "id")
		data, _ := a["localData"].([]byte)
		if len(data) == 0 {
			data = s.images[key+"|false"]
		}
		original := core.NewSignalFunc(cx, data, nil)
		failure := core.NewSignal(cx, "")
		ctx, cancel := context.WithCancel(cx.Context())
		cx.Cleanup(cancel)
		target, p, session := s.target, str(s.selected, "providerId"), str(s.selected, "id")
		load := func() {
			failure.Set("")
			go func() {
				b, err := s.attachmentBytes(ctx, target, p, session, a, false)
				cx.Post(func() {
					if ctx.Err() != nil {
						return
					}
					if err != nil {
						failure.Set(err.Error())
						return
					}
					original.Set(b)
					s.cacheImage(key+"|false", b)
				})
			}()
		}
		if len(data) == 0 {
			load()
		}
		img := core.Image{TestID: "preview-image", Label: str(a, "name"), SourceFunc: func() core.ImageSource {
			if b := original.Get(); len(b) > 0 {
				return core.ImageSource{Data: b}
			}
			s.read()
			return core.ImageSource{Data: s.images[key+"|true"]}
		}, ResizeMode: core.Contain, Style: fill.Merge(core.Style{AlignSelf: core.AlignStretch})}
		status := core.Dynamic(func(cx *core.Scope) core.Node {
			if msg := failure.Get(); msg != "" {
				return row(label(cx, msg, 12, true), button(cx, "preview-retry", locales.For(cx).PreviewRetry(), lucide.RotateCcw, "outline", load))
			}
			if len(original.Get()) == 0 {
				return label(cx, locales.For(cx).PreviewLoading(), 12, true)
			}
			return nil
		})
		center := core.View{Style: fill.Merge(core.Style{AlignSelf: core.AlignStretch}), Children: []core.Node{img, status}}
		return core.View{Style: fill.Merge(core.Style{Direction: core.Row, AlignItems: core.AlignCenter}), Children: []core.Node{button(cx, "preview-previous", "", lucide.ChevronLeft, "ghost", func() { move(-1) }), center, button(cx, "preview-next", "", lucide.ChevronRight, "ghost", func() { move(1) })}}
	})
	return base.Dialog{TestID: "image-preview", Label: "图片预览", Open: func() bool { return true }, OnOpenChange: func(open bool, _ base.Reason) {
		if !open {
			close()
		}
	}, BackdropStyle: core.Style{Background: core.RGB(0x000000).Faded(0.7)}, PopupStyleFunc: func() core.Style {
		window := cx.Env().Window()
		p := palette(cx)
		return core.Style{Width: core.Pt(min(1000, max(0, window.Width-24))), Height: core.Pt(window.Height * 0.86), Padding: core.All(16), Background: p.background, BorderColor: p.border, BorderWidth: 1, CornerRadius: 16, Overflow: core.Hidden}
	}, Children: []core.Node{toolbar, content}}
}

func (s *State) scanPhoto(cx *core.Scope, done func(string)) {
	accept := func(files []core.PickedFile) {
		if len(files) == 0 {
			done("")
			return
		}
		data := files[0].Data
		go func() {
			img, _, e := image.Decode(bytes.NewReader(data))
			if e != nil {
				cx.Post(func() { done("无法读取二维码图片") })
				return
			}
			codes, e := goqr.Recognize(img)
			cx.Post(func() {
				if e != nil || len(codes) == 0 {
					done("没有识别到设备二维码")
					return
				}
				for _, c := range codes {
					if s.acceptQR(string(c.Payload)) {
						return
					}
				}
				done("不是 ReignHub 设备二维码")
			})
		}()
	}
	cx.PickImages(core.PickOptions{Limit: 1}, accept)
}
func (s *State) acceptQR(code string) bool {
	if !strings.HasPrefix(code, "agenthub2:") {
		return false
	}
	b, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, "agenthub2:"))
	if e != nil {
		return false
	}
	v := Object{}
	if json.Unmarshal(b, &v) != nil || str(v, "service") == "" || str(v, "connectionId") == "" || str(v, "public") == "" {
		return false
	}
	s.scanned = v
	s.dialog.Set("connect")
	return true
}
