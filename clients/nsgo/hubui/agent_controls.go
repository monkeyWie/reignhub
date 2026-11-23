package hubui

import (
	"context"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func agentSetup(p Object) string {
	if str(p, "state") == "unsupported" {
		return "unsupported"
	}
	if !flag(p, "installed") {
		if str(p, "lastError") != "" {
			return "configure"
		}
		return "install"
	}
	if str(p, "state") == "disabled" || p["enabled"] == false {
		return "disabled"
	}
	if str(p, "state") == "launch-required" {
		return "launch-codex"
	}
	if providerReady(p) {
		return ""
	}
	if str(p, "lastError") != "" {
		return "configure"
	}
	if str(p, "id") == "codex-app" && !flag(p, "running") {
		return "open-app"
	}
	return "connecting"
}

func agentGuideURL(id string) string {
	switch id {
	case "codex-app":
		return "https://reignhub.app/docs/agents/codex"
	case "pi":
		return "https://reignhub.app/docs/agents/" + id
	default:
		return "https://reignhub.app/docs/agents/acp"
	}
}

func agentLabel(cx *core.Scope, p func() Object) core.Node {
	setup := core.NewMemo(cx, func() string { return agentSetup(p()) })
	return core.View{Style: fill.Merge(core.Style{Gap: 3}), Children: []core.Node{
		singleLine(cx, func() string { return str(p(), "name") }, 14, false),
		core.Dynamic(func(cx *core.Scope) core.Node {
			l := locales.For(cx)
			var text string
			switch setup.Get() {
			case "open-app":
				text = l.OpenAgentApp()
			case "unsupported":
				text = l.AgentUnsupported()
			case "connecting":
				text = l.AgentConnecting()
			}
			if text == "" {
				return nil
			}
			return label(cx, text, 12, true)
		}),
	}}
}

// Home and settings share the same switch, readiness guidance and action state.
// Technical diagnostics stay behind an explicit details action.
func (s *State) agentControls(cx *core.Scope, prefix string, p func() Object, open func(string)) core.Node {
	id := str(core.Untracked(cx, p), "id")
	key := "agent-switch-" + id
	busy := core.NewMemo(cx, func() bool { return s.actionPending(key) })
	desired := core.NewSignal(cx, true)
	checked := func() bool {
		if busy.Get() {
			return desired.Get()
		}
		return p()["enabled"] != false
	}
	setup := core.NewMemo(cx, func() string { return agentSetup(p()) })
	// Periodic status updates only replace a button when its action changes.
	action := core.NewMemo(cx, func() string {
		kind := setup.Get()
		if kind != "" && kind != "launch-codex" {
			return kind
		}
		if id != "codex-app" {
			return ""
		}
		status := p()
		if kind == "" && str(status, "endpoint") != "ipc-recovery" {
			return ""
		}
		if len(objects(status["instances"])) > 0 {
			return "restart-codex"
		}
		return "launch-codex"
	})
	look := ui.NewSwitchLook(cx, checked, ui.SizeSmall)
	return core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter, Gap: 8, FlexShrink: 0}, Children: []core.Node{
		core.Dynamic(func(cx *core.Scope) core.Node {
			l := locales.For(cx)
			switch action.Get() {
			case "restart-codex":
				return button(cx, prefix+"launch-codex", l.RestartCodex(), nil, "ghost", func() { open("activate:" + id) })
			case "launch-codex":
				return s.actionButton(cx, prefix+"launch-codex", l.LaunchCodex(), nil, "ghost", func() {
					s.activateCodex(prefix+"launch-codex", p(), false, nil)
				})
			case "install":
				return button(cx, prefix+"install-"+id, l.InstallAgent(), lucide.ArrowUpRight, "ghost", func() { cx.OpenURL(agentGuideURL(id), nil) })
			case "configure":
				return button(cx, prefix+"configure-"+id, l.ConfigureAgent(), nil, "ghost", func() { open("agent-help:" + id) })
			default:
				return nil
			}
		}),
		core.Show{When: busy.Get, Then: func(cx *core.Scope) core.Node {
			return s.loadingRing(prefix+key+"-loading", locales.For(cx).SavingAgent(), 12)
		}},
		base.Switch{TestID: prefix + key, Label: str(p(), "name"), Checked: checked,
			Disabled: func() bool { v := setup.Get(); return busy.Get() || v != "" && v != "disabled" },
			Style:    core.Style{Padding: core.All(8), CornerRadius: 8},
			StyleFunc: func() core.Style {
				return pressStyle(cx).Merge(core.Style{Disabled: &core.Style{Opacity: core.Alpha(.4)}})
			},
			TrackStyleFunc: look.Track, ThumbStyleFunc: look.Thumb,
			Tooltip: tooltipCard(cx, label(cx, locales.For(cx).AgentSwitchHint(), 12, false)),
			OnChange: func(enabled bool) {
				desired.Set(enabled)
				s.localProviderGeneration++
				s.call(key, "host.agent.enabled", Object{"provider": id, "enabled": enabled}, func(any) {
					s.localProviderGeneration++
					for i, v := range s.localProviders {
						if str(v, "id") == id {
							next := copyObject(v)
							next["enabled"] = enabled
							if !enabled {
								next["state"] = "disabled"
							} else {
								next["state"] = "detected"
							}
							s.localProviders[i] = next
						}
					}
					s.cancelTargetCatalogue(control.LocalTarget)
				})
			}},
	}}
}

func (s *State) activateCodex(action string, p Object, restart bool, done func()) {
	s.runAction(action, func(ctx context.Context) (any, error) {
		return s.api.Call(ctx, "host.activate", Object{"provider": "codex-app"}, "", Object{"expected": p["instances"], "confirmRestart": restart})
	}, func(any) {
		if done != nil {
			done()
		}
		s.poll()
	})
}

func (s *State) agentHelp(cx *core.Scope, id string) core.Node {
	return modalDialog(cx, ui.Dialog{TestID: "dialog-agent-help", Title: locales.For(cx).ConfigureAgent(), Open: func() bool { return true }, OnOpenChange: func(open bool, _ base.Reason) {
		if !open {
			s.closeDialog()
		}
	}, Children: []core.Node{
		core.View{TestID: "agent-help", Style: core.Style{Gap: 12}, Children: []core.Node{
			label(cx, locales.For(cx).AgentSetupHint(), 13, true),
			button(cx, "agent-setup-guide", locales.For(cx).AgentSetupGuide(), lucide.ArrowUpRight, "outline", func() { cx.OpenURL(agentGuideURL(id), nil) }),
			core.Dynamic(func(cx *core.Scope) core.Node {
				s.read()
				for _, p := range s.localProviders {
					if str(p, "id") == id && str(p, "lastError") != "" {
						return label(cx, str(p, "lastError"), 12, true)
					}
				}
				return nil
			}),
			button(cx, "agent-help-settings", locales.For(cx).ManageAgents(), lucide.Settings2, "ghost", func() { s.openSettings("agents") }),
		}},
	}, Footer: []core.Node{button(cx, "agent-help-close", "完成", nil, "ghost", s.closeDialog)}}, s.closeDialog)
}
