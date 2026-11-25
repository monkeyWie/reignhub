package codex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

const desktopPermissionKey = "permission-selection-by-host-id:local"

// This is the desktop composer's selection for a new local thread. Existing
// threads keep their own effective settings, including when the account changes.
type desktopPermission struct {
	Kind, AgentMode, ProfileID string
}

func readDesktopPermission(ctx context.Context, home string) (desktopPermission, error) {
	var raw json.RawMessage
	var mode, derived string
	db, err := openDesktopStateDB(ctx, home)
	if err != nil {
		return desktopPermission{}, err
	}
	if db != nil {
		defer db.Close()
		// Resolve the signed-in account and its preference in one read snapshot.
		const query = `WITH active AS (
				SELECT COALESCE((SELECT 'electron-owner-atoms:' || json_extract(value, '$')
					FROM settings WHERE scope='global' AND key='electron-renderer-content-owner'), 'atom') AS scope)
			SELECT (SELECT CASE WHEN length(value) <= 4096 THEN value ELSE 'invalid' END FROM settings JOIN active USING(scope) WHERE key=?),
				(SELECT json_extract(value,'$.local') FROM settings JOIN active USING(scope) WHERE key='agent-mode-by-host-id'),
				(SELECT json_extract(value,'$.local') FROM settings JOIN active USING(scope) WHERE key='config-derived-agent-mode-by-host-id')`
		var value, storedMode, configMode sql.NullString
		if err = db.QueryRowContext(ctx, query, desktopPermissionKey).Scan(&value, &storedMode, &configMode); err != nil {
			return desktopPermission{}, err
		}
		raw, mode, derived = json.RawMessage(value.String), storedMode.String, configMode.String
	} else {
		state, err := readDesktopStateJSON(ctx, home)
		if err != nil {
			return desktopPermission{}, err
		}
		atomsKey := "electron-persisted-atom-state"
		if owner := state["electron-renderer-content-owner"]; len(owner) != 0 && string(owner) != "null" {
			var id string
			if err = json.Unmarshal(owner, &id); err != nil || id == "" {
				return desktopPermission{}, errors.New("无法识别 Codex 桌面账户状态")
			}
			atomsKey = "electron-owner-atoms:" + id
		}
		if atoms := state[atomsKey]; len(atoms) != 0 {
			var entries struct {
				Selected json.RawMessage        `json:"permission-selection-by-host-id:local"`
				Modes    struct{ Local string } `json:"agent-mode-by-host-id"`
				Derived  struct{ Local string } `json:"config-derived-agent-mode-by-host-id"`
			}
			if err = json.Unmarshal(atoms, &entries); err != nil {
				return desktopPermission{}, err
			}
			raw, mode, derived = entries.Selected, entries.Modes.Local, entries.Derived.Local
		}
	}
	var selection desktopPermission
	// The desktop's mode picker stores explicit overrides and config mirrors
	// separately. Only a difference identifies a user-selected mode.
	if len(raw) == 0 && mode != "" && mode != derived {
		if mode == "custom" {
			return desktopPermission{}, nil
		}
		return desktopPermission{Kind: "agent-mode", AgentMode: mode}, nil
	}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &selection); err != nil {
			return desktopPermission{}, err
		}
	}
	return selection, nil
}

func (d desktopPermission) apply(params map[string]any) error {
	switch d.Kind {
	case "", "custom", "server-default":
		// The server resolves config.toml, managed settings and the project layer.
		return nil
	case "profile":
		if d.ProfileID == "" {
			return errors.New("Codex 桌面权限配置缺少名称")
		}
		params["permissions"] = d.ProfileID
	case "agent-mode":
		if d.AgentMode == "granular" {
			params["permissions"], params["approvalsReviewer"] = ":workspace", "user"
			params["approvalPolicy"] = map[string]any{"granular": map[string]bool{"sandbox_approval": false, "rules": false, "skill_approval": false, "request_permissions": true, "mcp_elicitations": false}}
			return nil
		}
		for _, preset := range permissionPresets {
			if d.AgentMode == preset.desktopMode {
				params["permissions"], params["approvalPolicy"], params["approvalsReviewer"] = preset.profile, preset.policy, preset.reviewer
				return nil
			}
		}
		return errors.New("无法识别 Codex 桌面权限级别")
	default:
		return errors.New("无法识别 Codex 桌面权限选择")
	}
	return nil
}

// Unloaded threads have no live settings snapshot. The thread index records
// sandbox and approval mode, but does not record an approval reviewer.
func storedThreadPermission(ctx context.Context, home, id string) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	db, err := openReadOnlyCodexDB(ctx, filepath.Join(home, "state_5.sqlite"))
	if err != nil || db == nil {
		return "", ""
	}
	defer db.Close()
	var sandbox, policy string
	err = db.QueryRowContext(ctx, `SELECT json_extract(sandbox_policy,'$.type'), approval_mode FROM threads WHERE id=?`, id).Scan(&sandbox, &policy)
	if err == nil {
		switch {
		case sandbox == "disabled" && policy == "never":
			return "full", "完全访问"
		case sandbox == "managed" && policy == "on-request":
			return "", "受限访问（按需审批）"
		}
	}
	return "", ""
}
