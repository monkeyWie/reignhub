// Package desktop is the native host companion, not a conversation client.
package desktop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/monkeyWie/reignhub/internal/app"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/skip2/go-qrcode"
)

type window struct {
	win                                           *mygo.Window
	cfg                                           config.Config
	key                                           string
	local                                         control.Caller
	page                                          int
	providers                                     []provider.Status
	network                                       control.NetworkStatus
	providerID                                    string
	busy, refreshing, confirm, advanced, showCode bool
	error, notice                                 string
	relayURL, relayToken, stun, pairing           string
	qr                                            *ui.Bitmap
	qrSize                                        float32
}

func Run(c config.Config, key string, local control.Caller) error {
	if fresh, err := config.Load(c.DataDir); err == nil {
		c = fresh
	}
	a := &window{cfg: c, key: key, local: local, providerID: "codex", relayURL: c.RelayURL, relayToken: c.RelayToken, stun: strings.Join(c.STUN, ",")}
	a.updatePairing()
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{Title: "ReignHub · 主机", Width: 1040, Height: 800, Content: ui.View(a.view)})
		a.refresh()
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				a.win.Update(func() { a.refresh() })
			}
		}()
	})
	return mygo.App.Run()
}

func (a *window) view(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(18, 24).Gap(14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "AGENT HUB").Bold().FontSize(18)
			ui.Text(c, "主机连接服务").TextColor(t.TextMuted).Grow(1)
			ui.Text(c, networkLabel(a.network.State)).TextColor(t.Accent).FontSize(12)
			if ui.Button(c, "刷新").Disabled(a.refreshing).Clicked() {
				a.refresh()
			}
		})
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(190).Shrink(0).Padding(20, 14).Gap(12).Background(t.Surface).Children(func() {
				for i, label := range []string{"本机 Agent", "扫码配对", "服务设置"} {
					if a.page == i {
						if ui.PrimaryButton(c, label).Clicked() {
							a.page = i
						}
					} else {
						if ui.Button(c, label).Clicked() {
							a.page = i
						}
					}
				}
				ui.Column(c).Grow(1).Children(func() {})
				ui.Text(c, "Go 服务在应用内运行").TextColor(t.TextMuted).FontSize(11)
			})
			ui.Scroll(c).Grow(1).MinWidth(0).Padding(24, 28).Gap(18).Children(func() {
				switch a.page {
				case 1:
					a.pairView(c)
				case 2:
					a.settings(c)
				default:
					a.agents(c)
				}
			})
		})
		ui.Row(c).Padding(10, 24).Gap(12).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			if a.error != "" {
				ui.Text(c, a.error).TextColor(t.Danger).Grow(1)
			} else {
				ui.Text(c, a.notice).TextColor(t.TextMuted).Grow(1)
			}
			if a.busy {
				ui.Text(c, "正在处理…").TextColor(t.Accent)
			}
			ui.Text(c, "MyGo native").FontSize(11).TextColor(t.TextMuted)
		})
	})
}
func (a *window) agents(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "本机 Agent").Bold().FontSize(26)
	ui.Text(c, "在电脑上照常使用 Agent，通过手机或 Web 连接这台主机。").TextColor(t.TextMuted)
	if len(a.providers) == 0 {
		ui.Text(c, "正在检测已安装的 Agent…").TextColor(t.TextMuted)
	}
	for _, p := range a.providers {
		ui.Column(c).Padding(20).Gap(12).Radius(12).Background(t.Surface).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, p.Name).Bold().FontSize(19).Grow(1)
				ui.Text(c, agentLabel(p)).TextColor(t.Accent)
			})
			ui.Row(c).Gap(24).Children(func() {
				ui.Text(c, "安装："+yesno(p.Installed, "已安装", "未检测到")).FontSize(13)
				ui.Text(c, "运行："+yesno(p.Running, "运行中", "未运行")).FontSize(13)
				ui.Text(c, "接入："+yesno(p.DesktopClients > 0, "已连接", "未接入")).FontSize(13)
			})
			if p.InstallPath != "" {
				ui.Text(c, p.InstallPath).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Text(c, p.Detail).FontSize(13)
			if p.LastError != "" {
				ui.Text(c, p.LastError).TextColor(t.Danger).FontSize(12)
			}
			if p.Capabilities.Desktop && p.DesktopClients == 0 && p.State != "unsupported" {
				if a.confirm && a.providerID == p.ID {
					ui.Text(c, "接入会请求正在运行的 App 正常退出并重新打开。请先处理运行中的任务。").FontSize(13)
					ui.Row(c).Gap(10).Children(func() {
						if ui.PrimaryButton(c, "确认接入").Disabled(a.busy).Clicked() {
							a.confirm = false
							a.connectDesktop(p)
						}
						if ui.Button(c, "取消").Clicked() {
							a.confirm = false
						}
					})
				} else if ui.Button(c, "接入官方 App").Disabled(a.busy || !p.Installed || p.State == "unavailable").Clicked() {
					a.providerID = p.ID
					a.confirm = true
				}
			}
		})
	}
	ui.Text(c, "已连接的客户端").Bold().FontSize(18)
	if len(a.network.Peers) == 0 {
		ui.Text(c, "暂无在线客户端。在“扫码配对”中连接手机或 Web。").TextColor(t.TextMuted)
	}
	for i, p := range a.network.Peers {
		ui.Row(c).Padding(14).Gap(16).Radius(8).Background(t.Surface).Children(func() {
			ui.Text(c, fmt.Sprintf("客户端 %d", i+1)).Bold()
			ui.Text(c, p.ID).FontSize(11).TextColor(t.TextMuted).Grow(1)
			ui.Text(c, connectivity.PathLabel(p.Path)).FontSize(12).TextColor(t.Accent)
		})
	}
	ui.Text(c, "这里只显示已经通过配对握手的在线连接。").FontSize(12).TextColor(t.TextMuted)
}
func (a *window) pairView(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "扫码配对").Bold().FontSize(26)
	ui.Text(c, "用手机 App 扫描二维码，或在 Web 客户端导入配对码。").TextColor(t.TextMuted)
	if a.pairing == "" || a.qr == nil {
		ui.Text(c, "请先在“服务设置”中配置中继服务器。").FontSize(15)
		if ui.PrimaryButton(c, "配置服务器").Clicked() {
			a.page = 2
		}
		return
	}
	ui.Column(c).Padding(20).Gap(14).Radius(12).Background(t.Surface).AlignItems(ui.Center).Children(func() {
		ui.Image(c, a.qr).Width(a.qrSize).Height(a.qrSize).Label("主机配对二维码")
		ui.Text(c, "扫描后，客户端将在这台电脑上执行 Agent 操作。").FontSize(13)
		ui.Row(c).Gap(12).Children(func() {
			if ui.Button(c, "复制配对码").Clicked() {
				c.WriteClipboard(a.pairing)
				a.notice = "配对码已复制"
			}
			if ui.Button(c, yesno(a.showCode, "隐藏配对码", "显示配对码")).Clicked() {
				a.showCode = !a.showCode
			}
		})
		if a.showCode {
			ui.TextArea(c, &a.pairing).ReadOnly(true).Height(105).Label("主机配对码")
		}
	})
	ui.Text(c, "当前二维码包含长期配对凭据，请只交给自己的设备。一次性配对与单设备撤销尚未提供。").TextColor(t.TextMuted).FontSize(12)
	ui.Text(c, "连接方式：优先 P2P 直连，无法直连时通过服务器转发加密数据。").FontSize(13)
}
func (a *window) settings(c *ui.Context) {
	t := c.Theme()
	ui.Text(c, "服务设置").Bold().FontSize(26)
	ui.Text(c, "服务器只负责协调连接和中继，Agent 始终在这台电脑上运行。").TextColor(t.TextMuted)
	ui.Column(c).Padding(20).Gap(12).Radius(12).Background(t.Surface).Children(func() {
		ui.Text(c, "中继服务器").Bold().FontSize(18)
		field(c, "服务地址", &a.relayURL, "https://agenthub-relay.example.workers.dev", false)
		field(c, "接入凭据", &a.relayToken, "服务器接入凭据", true)
		ui.Text(c, networkLabel(a.network.State)).TextColor(t.Accent)
		if a.network.Detail != "" {
			ui.Text(c, a.network.Detail).FontSize(12).TextColor(t.TextMuted)
		}
		if ui.Button(c, yesno(a.advanced, "收起高级网络设置", "高级网络设置")).Clicked() {
			a.advanced = !a.advanced
		}
		if a.advanced {
			field(c, "STUN（逗号分隔）", &a.stun, "stun:stun.l.google.com:19302", false)
		}
		if ui.PrimaryButton(c, "保存服务设置").Clicked() {
			a.saveNetwork()
		}
	})
	ui.Text(c, "本机后台").Bold().FontSize(18)
	ui.Text(c, "配置目录："+a.cfg.DataDir).FontSize(12).TextColor(t.TextMuted)
	ui.Text(c, "关闭窗口后服务继续运行；接入 Agent 需要你主动确认。").FontSize(13)
}
func field(c *ui.Context, label string, value *string, placeholder string, password bool) {
	ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted)
	e := ui.TextInput(c, value).Placeholder(placeholder).Label(label)
	if password {
		e.Password()
	}
}
func yesno(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}
func agentLabel(p provider.Status) string {
	if p.DesktopClients > 0 {
		return "已接入"
	}
	if p.Running {
		return "运行中 · 待接入"
	}
	if p.Installed {
		return "已安装"
	}
	return "未安装"
}
func networkLabel(state string) string {
	switch state {
	case "connected":
		return "中继已连接"
	case "connecting":
		return "正在连接中继"
	case "error":
		return "中继连接失败"
	default:
		return "尚未连接中继"
	}
}
func (a *window) updatePairing() {
	a.pairing = ""
	a.qr = nil
	a.showCode = false
	if a.cfg.RelayURL == "" {
		return
	}
	p := remote.Pairing{Relay: a.cfg.RelayURL, Host: a.cfg.HostID, Key: a.key, RelayToken: a.cfg.RelayToken, ICEServers: app.ICE(a.cfg).ICEServers}
	if err := p.Validate(); err != nil {
		a.error = err.Error()
		return
	}
	a.pairing = p.Code()
	q, err := qrcode.New(a.pairing, qrcode.Medium)
	if err != nil {
		a.error = "无法生成二维码：" + err.Error()
		return
	}
	// Render at an integer module scale, including the quiet zone.
	size := len(q.Bitmap()) * 3
	a.qrSize = float32(size)
	a.qr = ui.NewBitmap(q.Image(size)).Pixelated()
}
func (a *window) saveNetwork() {
	c, err := config.Load(a.cfg.DataDir)
	if err != nil {
		a.error = err.Error()
		return
	}
	c.RelayURL = strings.TrimRight(strings.TrimSpace(a.relayURL), "/")
	if strings.HasPrefix(c.RelayURL, "https://") {
		c.RelayURL = "wss://" + strings.TrimPrefix(c.RelayURL, "https://")
	}
	if strings.HasPrefix(c.RelayURL, "http://") {
		c.RelayURL = "ws://" + strings.TrimPrefix(c.RelayURL, "http://")
	}
	c.RelayToken = strings.TrimSpace(a.relayToken)
	c.STUN = split(a.stun)
	if c.RelayURL != "" {
		if err = (remote.Pairing{Relay: c.RelayURL, Host: c.HostID, Key: a.key, RelayToken: c.RelayToken}).Validate(); err != nil {
			a.error = err.Error()
			return
		}
	}
	if err = c.Save(); err != nil {
		a.error = err.Error()
		return
	}
	a.cfg = c
	a.relayURL = c.RelayURL
	a.error = ""
	a.notice = "已保存，后台将在两秒内更新连接"
	a.updatePairing()
}
func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func (a *window) refresh() {
	if a.refreshing || a.busy {
		return
	}
	a.refreshing = true
	go func() {
		ctx, stop := context.WithTimeout(context.Background(), 12*time.Second)
		defer stop()
		var statuses []provider.Status
		var network control.NetworkStatus
		err := a.local.Call(ctx, control.Request{Method: "providers"}, &statuses)
		if err == nil {
			err = a.local.Call(ctx, control.Request{Method: "network"}, &network)
		}
		a.win.Update(func() {
			a.refreshing = false
			if err != nil {
				a.error = err.Error()
				return
			}
			a.providers = statuses
			a.network = network
		})
	}()
}
func (a *window) connectDesktop(p provider.Status) {
	a.busy = true
	a.error = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		err := a.local.Call(ctx, control.Request{Method: "connectDesktop", Provider: p.ID, Expected: p.Instances, ConfirmRestart: true}, nil)
		a.win.Update(func() {
			a.busy = false
			if err != nil {
				a.error = err.Error()
				return
			}
			a.notice = "Agent 接入已完成"
			a.refresh()
		})
	}()
}
