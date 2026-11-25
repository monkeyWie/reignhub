package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/monkeyWie/reignhub/internal/provider"
)

func (p *Provider) ComposerControls(ctx context.Context, id, model string) ([]provider.ComposerControl, error) {
	rt, err := p.ensureRuntime(ctx, id)
	if err != nil {
		return nil, err
	}
	return rt.controls(ctx, model)
}
func (p *Provider) ConfigureComposer(ctx context.Context, id string, change provider.ComposerChange) error {
	rt, err := p.ensureRuntime(ctx, id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	s := p.sessions[id]
	if s == nil || s.runtime != rt || s.sending || s.removing || s.configuring {
		p.mu.Unlock()
		return errors.New("请等待当前操作完成后再修改设置")
	}
	s.configuring = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); s.configuring = false; p.mu.Unlock() }()
	if err := rt.configure(ctx, change); err != nil {
		return err
	}
	p.log.Publish(provider.StreamEvent{Kind: "composerSettings", SessionID: id})
	if change.Control == "model" {
		p.mu.Lock()
		s.Session.Model = change.Value
		p.mu.Unlock()
		return p.persist(id)
	}
	return nil
}

// ACP exposes native mode IDs. Only recognized permission-bypass modes need
// the full-access prompt. Model IDs and reasoning levels can be arbitrary.
func acpFullAccess(category, value string) bool {
	switch category {
	case "mode", "permission", "permission_mode":
		switch value {
		case "bypassPermissions", "yolo", "full-access", "danger-full-access":
			return true
		}
	}
	return false
}

func selectChoices(v *acp.SessionConfigOptionSelect, category string) []provider.ComposerChoice {
	var choices []provider.ComposerChoice
	add := func(rows []acp.SessionConfigSelectOption) {
		for _, row := range rows {
			c := provider.ComposerChoice{ID: string(row.Value), Label: row.Name, FullAccess: acpFullAccess(category, string(row.Value))}
			if row.Description != nil {
				c.Description = *row.Description
			}
			choices = append(choices, c)
		}
	}
	if v.Options.Ungrouped != nil {
		add(*v.Options.Ungrouped)
	}
	if v.Options.Grouped != nil {
		for _, g := range *v.Options.Grouped {
			add(g.Options)
		}
	}
	return choices
}

func (r *acpRuntime) controls(_ context.Context, _ string) ([]provider.ComposerControl, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []provider.ComposerControl
	hasMode := false
	for _, o := range r.config {
		v := o.Select
		if v == nil {
			continue
		}
		category := string(v.Id)
		if v.Category != nil {
			category = string(*v.Category)
		}
		switch category {
		case "model", "mode", "permission", "permission_mode", "thought_level", "reasoning_effort":
		default:
			continue
		}
		choices := selectChoices(v, category)
		if len(choices) == 0 {
			continue
		}
		id := "acp:" + string(v.Id)
		if category == "model" {
			id = "model"
		}
		if category == "mode" {
			hasMode = true
		}
		control := provider.ComposerControl{ID: id, Label: v.Name, Selected: string(v.CurrentValue), Choices: choices}
		if category == "thought_level" || category == "reasoning_effort" {
			control.Kind = "effort"
		}
		out = append(out, control)
	}
	if !hasMode && r.modes != nil && len(r.modes.AvailableModes) > 0 {
		mode := provider.ComposerControl{ID: "mode", Label: "会话模式", Selected: string(r.modes.CurrentModeId)}
		for _, v := range r.modes.AvailableModes {
			c := provider.ComposerChoice{ID: string(v.Id), Label: v.Name, FullAccess: acpFullAccess("mode", string(v.Id))}
			if v.Description != nil {
				c.Description = *v.Description
			}
			mode.Choices = append(mode.Choices, c)
		}
		out = append(out, mode)
	}
	return out, nil
}

func (r *acpRuntime) configure(ctx context.Context, c provider.ComposerChange) error {
	controls, _ := r.controls(ctx, "")
	if err := provider.ValidateComposerChoice(controls, c); err != nil {
		return err
	}
	for _, control := range controls {
		if control.ID == "model" && c.Control != "model" && c.Model != "" && c.Model != control.Selected {
			return errors.New("当前模型已改变，请重新选择设置")
		}
	}
	r.mu.Lock()
	native := r.native
	configID := acp.SessionConfigId(strings.TrimPrefix(c.Control, "acp:"))
	if c.Control == "model" {
		for _, o := range r.config {
			if o.Select != nil && (o.Select.Id == "model" || o.Select.Category != nil && *o.Select.Category == "model") {
				configID = o.Select.Id
				break
			}
		}
	}
	r.mu.Unlock()
	if c.Control == "mode" {
		_, err := r.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: native, ModeId: acp.SessionModeId(c.Value)})
		if err == nil {
			r.mu.Lock()
			if r.modes != nil {
				r.modes.CurrentModeId = acp.SessionModeId(c.Value)
			}
			r.mu.Unlock()
		}
		return err
	}
	resp, err := r.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: native, ConfigId: configID, Value: acp.SessionConfigValueId(c.Value)}})
	if err == nil {
		r.mu.Lock()
		r.config = resp.ConfigOptions
		r.mu.Unlock()
	}
	return err
}

type piModel struct{ Provider, ID string }
type piState struct {
	Model         piModel
	ThinkingLevel string
}

func (r *piRuntime) state(ctx context.Context) (piState, error) {
	var state piState
	data, err := r.request(ctx, map[string]any{"type": "get_state"})
	if err == nil {
		err = json.Unmarshal(data, &state)
	}
	return state, err
}

func (r *piRuntime) controls(ctx context.Context, _ string) ([]provider.ComposerControl, error) {
	state, err := r.state(ctx)
	if err != nil {
		return nil, err
	}
	model := provider.ComposerControl{ID: "model", Label: "模型"}
	for _, m := range r.Models() {
		model.Choices = append(model.Choices, provider.ComposerChoice{ID: m.ID, Label: m.Name})
		var v piModel
		_ = json.Unmarshal([]byte(m.ID), &v)
		if state.Model == v {
			model.Selected = m.ID
		}
	}
	out := []provider.ComposerControl{model}
	data, err := r.request(ctx, map[string]any{"type": "get_available_thinking_levels"})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unknown command") {
			return out, nil
		}
		return nil, err
	}
	var available struct{ Levels []string }
	if err = json.Unmarshal(data, &available); err != nil {
		return nil, err
	}
	if len(available.Levels) > 1 {
		control := provider.ComposerControl{ID: "effort", Label: "推理级别", Selected: state.ThinkingLevel}
		for _, level := range available.Levels {
			control.Choices = append(control.Choices, provider.ComposerChoice{ID: level, Label: level})
		}
		out = append(out, control)
	}
	return out, nil
}

func (r *piRuntime) configure(ctx context.Context, c provider.ComposerChange) error {
	controls, err := r.controls(ctx, "")
	if err != nil {
		return err
	}
	if err = provider.ValidateComposerChoice(controls, c); err != nil {
		return err
	}
	if c.Control == "model" {
		var m struct{ Provider, ID string }
		if err = json.Unmarshal([]byte(c.Value), &m); err != nil {
			return err
		}
		_, err = r.request(ctx, map[string]any{"type": "set_model", "provider": m.Provider, "modelId": m.ID})
	} else {
		if c.Model != "" && controls[0].Selected != c.Model {
			return errors.New("当前模型已改变，请重新选择推理级别")
		}
		_, err = r.request(ctx, map[string]any{"type": "set_thinking_level", "level": c.Value})
	}
	return err
}
