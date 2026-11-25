package hubui

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
	"github.com/nsgo-dev/nsgo/ui/chat"
)

func permissionCachePrefix(target string) string {
	return "composer-permissions/" + url.QueryEscape(target) + "/"
}

func permissionCacheKey(target, agent, id string) string {
	return permissionCachePrefix(target) + url.QueryEscape(agent) + "/" + url.QueryEscape(id)
}

func sessionSetting(control Object) bool {
	return str(control, "id") != "model" && !effortControl(control)
}

// Persist only session-wide controls. Model choices and reasoning effort
// belong to the queried model and are loaded independently.
func (s *State) saveSessionPermissions(target, agent, id string, controls []Object) {
	var confirmed []Object
	for _, c := range controls {
		if sessionSetting(c) && (str(c, "selected") != "" || str(c, "selectedLabel") != "") {
			confirmed = append(confirmed, c)
		}
	}
	store := s.cx.Storage()
	key := permissionCacheKey(target, agent, id)
	if len(confirmed) == 0 {
		store.RemoveItem(key)
		return
	}
	if raw, err := json.Marshal(confirmed); err == nil {
		_ = store.SetItem(key, string(raw))
	}
}

func (s *State) loadSessionPermissions(target, agent, id string) []Object {
	raw, ok := s.cx.Storage().GetItem(permissionCacheKey(target, agent, id))
	var controls []Object
	if ok && json.Unmarshal([]byte(raw), &controls) == nil {
		return controls
	}
	return nil
}

func (s *State) forgetDevicePermissions(target string) {
	store := s.cx.Storage()
	prefix := permissionCachePrefix(target)
	for _, key := range store.Keys() {
		if strings.HasPrefix(key, prefix) {
			store.RemoveItem(key)
		}
	}
}

func (s *State) watchComposerControls(cx *core.Scope) {
	key, generation := s.key(), s.sessionGeneration
	var stop context.CancelFunc
	cx.Cleanup(func() {
		if stop != nil {
			stop()
		}
	})
	type query struct {
		enabled  bool
		model    string
		revision uint64
	}
	request := core.NewMemo(cx, func() query {
		s.read()
		return query{s.key() == key && s.connected() && s.agentCapability("composerSettings"), s.model, s.composerControlsRevision}
	})
	cx.Effect(func() {
		q := request.Get()
		if stop != nil {
			stop()
		}
		if !q.enabled || s.sessionGeneration != generation {
			return
		}
		target, agent, id := s.target, str(s.selected, "providerId"), str(s.selected, "id")
		ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		stop = cancel
		go func() {
			value, err := rpc(ctx, s.api, target, agent, id, "composer.settings", Object{"model": q.model})
			cx.Post(func() {
				if ctx.Err() == context.Canceled || generation != s.sessionGeneration || s.key() != key || q.model != s.model || q.revision != s.composerControlsRevision {
					return
				}
				cancel()
				if err != nil {
					s.setConnectionIssue(target, "composer.settings", err)
					s.changed()
					return
				}
				controls := objects(value)
				s.saveSessionPermissions(target, agent, id, controls)
				if !reflect.DeepEqual(s.composerControls, controls) {
					s.composerControls = controls
					s.composerContentRevision++
				}
				s.composerControlsModel = q.model
				if native := s.composerControl("model"); native != nil {
					if selected := str(native, "selected"); selected != "" && selected != s.model {
						s.model, s.modelBase = selected, selected
						s.persistDraft()
					}
				}
				s.setConnectionIssue(target, "composer.settings", nil)
				s.changed()
			})
		}()
	})
}

func (s *State) composerControl(id string) Object {
	for _, c := range s.composerControls {
		if str(c, "id") == id {
			return c
		}
	}
	return nil
}
func (s *State) composerChoice(control, value string) (choice Object, selected string) {
	c := s.composerControl(control)
	for _, v := range objects(c["choices"]) {
		if str(v, "id") == value {
			return v, str(c, "selected")
		}
	}
	return nil, ""
}

func isFullAccess(agent, control, value string, choice Object) bool {
	return flag(choice, "fullAccess") || agent == "codex-app" && control == "permission" && value == "full"
}

func (s *State) applyComposerChoice(control, value string) {
	choice, selected := s.composerChoice(control, value)
	if choice == nil || selected == value {
		return
	}
	agent := str(s.selected, "providerId")
	if !isFullAccess(agent, control, value, choice) {
		s.writeComposerChoice(control, value)
		return
	}
	key, generation, model := s.key(), s.sessionGeneration, s.model
	l := locales.For(s.cx)
	allow := l.AllowFullAccess()
	base.ShowAlertDialog(s.cx, base.AlertDialog{
		Title: l.FullAccessTitle(), Description: l.FullAccessWarning(s.providerName(s.target, agent), s.environmentName(s.target)),
		Actions:       []core.AlertAction{{Label: l.CancelFullAccess(), Role: core.AlertCancel}, {Label: allow, Role: core.AlertDestructive}},
		CloseOnEscape: core.ToggleOn, CloseOnBackdropPress: core.ToggleOn,
	}, func(answer string) {
		if answer != allow || s.ctx.Err() != nil || key != s.key() || generation != s.sessionGeneration || model != s.model {
			return
		}
		current, nowSelected := s.composerChoice(control, value)
		if current != nil && nowSelected == selected && s.agentCapability("composerSettings") {
			s.writeComposerChoice(control, value)
		}
	})
}

func (s *State) canEditComposerControl(control string) bool {
	return s.canWriteSession() && (activeTurn(s.transcript) == nil || control == "permission" && str(s.selected, "providerId") == "codex-app")
}

func (s *State) writeComposerChoice(control, value string) {
	if !s.canEditComposerControl(control) || s.pendingSend() != "" || s.composerWrites[s.key()] || !s.draftReady() {
		return
	}
	key, generation, model := s.key(), s.sessionGeneration, s.model
	target, agent, id := s.target, str(s.selected, "providerId"), str(s.selected, "id")
	params := Object{"control": control, "value": value, "model": s.model}
	if s.composerWrites == nil {
		s.composerWrites = map[string]bool{}
	}
	s.composerWrites[key] = true
	s.changed()
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		defer cancel()
		_, err := rpc(ctx, s.api, target, agent, id, "composer.configure", Object{"params": params})
		s.cx.Post(func() {
			delete(s.composerWrites, key)
			if s.key() != key || s.sessionGeneration != generation {
				s.changed()
				return
			}
			if err != nil {
				f := &sessionOperationError{target: target, device: str(s.device(), "name"), provider: agent, session: id, method: "composer.configure", cause: err}
				s.error, s.failure = err.Error(), f
			} else {
				if c := s.composerControl(control); effortControl(c) && s.model == model {
					c["selected"] = value
					s.composerContentRevision++
				}
				if control == "model" {
					s.model, s.modelBase = value, value
					s.persistDraft()
				}
				s.error = ""
				s.failure = nil
			}
			s.composerControlsRevision++
			s.changed()
		})
	}()
}

func (s *State) composerSettingsRow(cx *core.Scope) core.Node {
	content := core.NewMemo(cx, func() uint64 { s.read(); return s.composerContentRevision })
	editable := core.NewMemo(cx, func() bool {
		s.read()
		return s.agentCapability("composerSettings") && s.canEditComposerControl("permission")
	})
	idle := core.NewMemo(cx, func() bool { s.read(); return activeTurn(s.transcript) == nil })
	return core.Show{When: func() bool {
		s.read()
		if capabilities := s.agentCapabilities(); capabilities != nil && !flag(capabilities, "composerSettings") {
			return false
		}
		for _, control := range s.composerControls {
			if sessionSetting(control) && (len(objects(control["choices"])) > 0 || str(control, "selectedLabel") != "") {
				return true
			}
		}
		return false
	}, Then: func(cx *core.Scope) core.Node {
		useEditorTheme(cx)
		return core.View{Style: core.Style{Direction: core.Row, Wrap: core.Wrap, AlignItems: core.AlignCenter, Gap: 4, MinWidth: core.Pt(0)}, Children: []core.Node{
			core.Dynamic(func(cx *core.Scope) core.Node {
				content.Get()
				canEdit, idleTurn := editable.Get(), idle.Get()
				var pickers core.Group
				for _, control := range s.composerControls {
					id, selected, title := str(control, "id"), str(control, "selected"), str(control, "label")
					if !sessionSetting(control) {
						continue
					}
					if current := str(control, "selectedLabel"); current != "" {
						title = current
					} else if id == "permission" && selected == "" {
						title = locales.For(cx).PermissionUnsynced()
					}
					agent := str(s.selected, "providerId")
					fullSelected := isFullAccess(agent, id, selected, nil)
					var options []chat.Option
					fullIDs := map[string]bool{}
					for _, v := range objects(control["choices"]) {
						option := chat.Option{ID: str(v, "id"), Label: str(v, "label"), Description: str(v, "description"), Group: str(control, "label")}
						if isFullAccess(agent, id, option.ID, v) {
							fullIDs[option.ID] = true
							option.Label = locales.For(cx).FullAccessLabel()
						}
						options = append(options, option)
						if selected == option.ID {
							title = option.Label
						}
					}
					fullSelected = fullSelected || fullIDs[selected]
					if fullSelected {
						title = locales.For(cx).FullAccessLabel()
					}
					if len(options) == 0 || !canEdit || !idleTurn && id != "permission" {
						caption := singleLine(cx, func() string { return title }, 12, true)
						if fullSelected {
							caption.StyleFunc = ui.Colored(func() core.Color { return cx.Palette().Warning })
						}
						pickers = append(pickers, caption)
						continue
					}
					if id == "permission" || len(fullIDs) != 0 || fullSelected {
						pickers = append(pickers, permissionPicker(cx, id, title, selected, options, fullIDs, fullSelected, func(value string) { s.applyComposerChoice(id, value) }))
						continue
					}
					pickers = append(pickers, chat.Picker{TestID: "composer-setting-" + id, Label: func() string { return title }, Selected: func() string { return selected }, Options: options, Width: min(320, cx.Env().Window().Width-32), OnChange: func(v string) { s.applyComposerChoice(id, v) }})
				}
				return pickers
			}),
			core.Show{When: func() bool { s.read(); return s.composerWrites[s.key()] }, Then: func(cx *core.Scope) core.Node {
				return actionSpinner(cx, "composer-setting-saving", "正在更新设置", false, 14)
			}},
		}}
	}}
}
