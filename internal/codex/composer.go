package codex

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/provider"
)

type permissionPreset struct {
	id, desktopMode, label, description, profile, policy, reviewer, sandbox string
}

var permissionPresets = []permissionPreset{
	{"read-only", "read-only", "只读", "读取工作区文件，修改前请求批准", ":read-only", "on-request", "user", "read-only"},
	{"ask", "auto", "请求批准", "在工作区外编辑文件或访问网络时请求批准", ":workspace", "on-request", "user", "workspace-write"},
	{"auto", "guardian-approvals", "帮我批准", "由 Codex 审查需要批准的操作", ":workspace", "on-request", "auto_review", "workspace-write"},
	{"full", "full-access", "完全访问", "允许访问网络和此电脑上的文件", ":danger-full-access", "never", "user", "danger-full-access"},
}

type composerSandbox struct {
	Type          string
	NetworkAccess json.RawMessage
}

type codexSettings struct {
	Model                   string
	Effort                  string
	ReasoningEffort         string
	ApprovalPolicy          json.RawMessage
	ApprovalsReviewer       string
	ActivePermissionProfile struct{ ID string }
	Sandbox                 composerSandbox
	SandboxPolicy           composerSandbox
}

// A named profile is authoritative. Older snapshots can describe the same
// preset with the effective sandbox, approval policy and reviewer instead.
func (s codexSettings) permission() (id, label string) {
	profile := s.ActivePermissionProfile.ID
	sandbox := s.SandboxPolicy
	if sandbox.Type == "" {
		sandbox = s.Sandbox
	}
	var policy string
	_ = json.Unmarshal(s.ApprovalPolicy, &policy)
	reviewer := s.ApprovalsReviewer
	switch reviewer {
	case "":
		reviewer = "user"
	case "guardian_subagent":
		reviewer = "auto_review"
	}
	if profile != "" && !slices.ContainsFunc(permissionPresets, func(p permissionPreset) bool { return p.profile == profile }) {
		return "", profile
	}
	effectiveProfile := profile
	if effectiveProfile == "" {
		networkRestricted := len(sandbox.NetworkAccess) == 0 || string(sandbox.NetworkAccess) == "false"
		switch sandbox.Type {
		case "dangerFullAccess":
			effectiveProfile = ":danger-full-access"
		case "readOnly":
			if networkRestricted {
				effectiveProfile = ":read-only"
			}
		case "workspaceWrite":
			if networkRestricted {
				effectiveProfile = ":workspace"
			}
		}
	}
	if effectiveProfile != "" && (len(s.ApprovalPolicy) == 0 || string(s.ApprovalPolicy) == "null") {
		return "", ""
	}
	for _, preset := range permissionPresets {
		if effectiveProfile == preset.profile && policy == preset.policy && reviewer == preset.reviewer {
			return preset.id, preset.label
		}
	}
	if profile != "" || sandbox.Type != "" {
		return "", "自定义权限"
	}
	return "", ""
}

// rememberComposerSettingsLocked keeps the latest native settings for loaded threads.
func (p *Provider) rememberComposerSettingsLocked(id string, s codexSettings) {
	if s.Effort == "" {
		s.Effort = s.ReasoningEffort
	}
	if p.composerSettings == nil {
		p.composerSettings = make(map[string]codexSettings)
	}
	if _, ok := p.composerSettings[id]; !ok && len(p.composerSettings) >= 256 {
		for k := range p.composerSettings {
			delete(p.composerSettings, k)
			break
		}
	}
	p.composerSettings[id] = s
}

func (p *Provider) permissionChoices(ctx context.Context) ([]provider.ComposerChoice, error) {
	var page struct {
		Data []struct {
			ID      string
			Allowed bool
		}
		NextCursor string
	}
	allowed := map[string]bool{}
	params := map[string]any{"limit": 100}
	seen := map[string]bool{}
	for {
		page.Data = nil
		page.NextCursor = ""
		if err := p.call(ctx, "permissionProfile/list", params, &page); err != nil {
			return nil, err
		}
		for _, r := range page.Data {
			allowed[r.ID] = r.Allowed
		}
		if page.NextCursor == "" {
			break
		}
		if seen[page.NextCursor] {
			return nil, errors.New("permissionProfile/list returned a repeated cursor")
		}
		seen[page.NextCursor] = true
		params["cursor"] = page.NextCursor
	}
	var limits struct {
		Requirements struct {
			AllowedApprovalPolicies                        []json.RawMessage
			AllowedApprovalsReviewers, AllowedSandboxModes []string
		}
	}
	if err := p.call(ctx, "configRequirements/read", map[string]any{}, &limits); err != nil {
		return nil, err
	}
	r := limits.Requirements
	var out []provider.ComposerChoice
	for _, v := range permissionPresets {
		policyAllowed := r.AllowedApprovalPolicies == nil || slices.ContainsFunc(r.AllowedApprovalPolicies, func(raw json.RawMessage) bool {
			var id string
			return json.Unmarshal(raw, &id) == nil && id == v.policy
		})
		if allowed[v.profile] && policyAllowed && (r.AllowedApprovalsReviewers == nil || slices.Contains(r.AllowedApprovalsReviewers, v.reviewer)) && (r.AllowedSandboxModes == nil || slices.Contains(r.AllowedSandboxModes, v.sandbox)) {
			out = append(out, provider.ComposerChoice{ID: v.id, Label: v.label, Description: v.description, FullAccess: v.id == "full"})
		}
	}
	return out, nil
}

func (p *Provider) ComposerControls(ctx context.Context, id, model string) ([]provider.ComposerControl, error) {
	p.mu.Lock()
	settings := p.composerSettings[id]
	p.mu.Unlock()
	var unloaded bool
	var state json.RawMessage
	if b := p.appBridge(); b != nil {
		b.mu.Lock()
		if f := b.followed[id]; f != nil && f.owner != "" && f.err == nil {
			state = f.state
		}
		b.mu.Unlock()
	}
	var snapshot struct {
		CurrentPermissions, LatestThreadSettings *codexSettings
		LatestModel, LatestReasoningEffort       string
	}
	if len(state) > 0 && json.Unmarshal(state, &snapshot) == nil {
		settings = codexSettings{}
		if snapshot.LatestThreadSettings != nil {
			settings = *snapshot.LatestThreadSettings
		}
		if current := snapshot.CurrentPermissions; current != nil && (current.ActivePermissionProfile.ID != "" || current.SandboxPolicy.Type != "" || current.Sandbox.Type != "") {
			settings = *current
		}
		settings.Model, settings.Effort = snapshot.LatestModel, snapshot.LatestReasoningEffort
	} else {
		var metadata struct {
			Thread struct {
				ID, Model, ReasoningEffort string
				Status                     struct{ Type string }
			}
		}
		if err := p.call(ctx, "thread/read", map[string]any{"threadId": id}, &metadata); err != nil {
			return nil, err
		}
		if metadata.Thread.ID != id {
			return nil, provider.ErrUnavailable
		}
		unloaded = metadata.Thread.Status.Type == "notLoaded"
		// The watch may have finished subscribing while thread/read was in flight.
		p.mu.Lock()
		settings = p.composerSettings[id]
		p.mu.Unlock()
		if settings.Model == "" {
			settings.Model, settings.Effort = metadata.Thread.Model, metadata.Thread.ReasoningEffort
		}
	}
	var controls []provider.ComposerControl
	choices, permErr := p.permissionChoices(ctx)
	selected, label := settings.permission()
	if label == "" && unloaded {
		selected, label = storedThreadPermission(ctx, codexProjectHome(), id)
	}
	if len(choices) > 0 || label != "" {
		controls = append(controls, provider.ComposerControl{ID: "permission", Label: "权限", Selected: selected, SelectedLabel: label, Choices: choices})
	}
	models, err := p.modelCatalogue(ctx)
	if err != nil && len(controls) == 0 {
		return nil, err
	}
	if model == "" {
		model = settings.Model
	}
	for _, m := range models {
		if !(m.ID == model || model == "" && m.Default) || len(m.Efforts) == 0 {
			continue
		}
		effort := m.DefaultEffort
		if settings.Model == m.ID && settings.Effort != "" {
			effort = settings.Effort
		}
		controls = append(controls, provider.ComposerControl{ID: "effort", Label: "推理级别", Selected: effort, Choices: m.Efforts})
		break
	}
	if len(controls) == 0 && permErr != nil {
		return nil, permErr
	}
	return controls, nil
}

func (p *Provider) ConfigureComposer(ctx context.Context, id string, change provider.ComposerChange) error {
	controls, err := p.ComposerControls(ctx, id, change.Model)
	if err != nil {
		return err
	}
	if err = provider.ValidateComposerChoice(controls, change); err != nil {
		return err
	}
	params := map[string]any{}
	switch change.Control {
	case "effort":
		params["effort"] = change.Value
		if change.Model != "" {
			params["model"] = change.Model
		}
	case "permission":
		for _, v := range permissionPresets {
			if v.id == change.Value {
				params["approvalPolicy"], params["approvalsReviewer"], params["permissions"] = v.policy, v.reviewer, v.profile
				break
			}
		}
	}
	c, owner, err := p.writeRoute(ctx, id)
	if err != nil {
		return err
	}
	b := p.appBridge()
	if owner != "" || b != nil && b.owns(id) {
		if b == nil {
			return provider.ErrUnavailable
		}
		const method = "thread-follower-update-thread-settings"
		args := map[string]any{"conversationId": id, "threadSettings": params}
		var response json.RawMessage
		if owner != "" {
			res, e := b.request(ctx, method, args, owner)
			if e != nil {
				return ipcWriteError(method, e)
			}
			response = res.Result
		} else {
			res, e := b.Handle(ctx, codexipc.Message{Method: method, Version: ipcVersions[method], Params: rawJSON(args)})
			if e != nil {
				return e
			}
			response = rawJSON(res)
		}
		var result struct {
			Applied bool
			Result  struct{ Applied bool }
		}
		if json.Unmarshal(response, &result) != nil || !(result.Applied || result.Result.Applied) {
			return errors.New("Codex 未确认会话设置")
		}
	} else {
		params["threadId"] = id
		if err = p.threadWrite(ctx, c, "thread/settings/update", params, nil); err != nil {
			return err
		}
	}
	return nil
}
