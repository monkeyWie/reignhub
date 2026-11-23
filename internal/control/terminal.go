package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/terminal"
)

type remoteIdentityKey struct{}

// WithRemoteIdentity is set by the authenticated transport, never by RPC JSON.
func WithRemoteIdentity(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, remoteIdentityKey{}, identity)
}

func (d *Dispatcher) terminal(ctx context.Context, r Request) (any, error) {
	if d.Terminals == nil {
		return nil, &ResponseError{Code: "TERMINAL_UNSUPPORTED", Message: "此设备版本不支持终端，请更新被控端 ReignHub"}
	}
	owner, remote := ctx.Value(remoteIdentityKey{}).(string)
	if remote && (owner == "" || d.RemoteTerminalAllowed == nil || !d.RemoteTerminalAllowed()) {
		return nil, &ResponseError{Code: "TERMINAL_DENIED", Message: "请在被控设备的「设置 → 连接」中开启远程终端"}
	}
	if !remote {
		owner = "local"
	}
	if r.Provider == "" || r.Session == "" || d.Providers[r.Provider] == nil {
		return nil, errors.New("请选择有效会话")
	}
	if d.ProviderEnabled != nil && !d.ProviderEnabled(r.Provider) {
		return nil, errors.New("此 Agent 已在本机关闭")
	}
	if r.Method == "terminal.profiles" {
		return terminal.Profiles(ctx)
	}
	var p terminal.Params
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return nil, errors.New("无效终端参数")
	}
	scope := terminal.Scope{Owner: owner, Provider: r.Provider, Session: r.Session}
	switch r.Method {
	case "terminal.open", "terminal.openProfile":
		if (r.Method == "terminal.openProfile") != (p.Profile != "") {
			return nil, errors.New("无效终端配置")
		}
		return d.Terminals.Open(ctx, scope, p, func(ctx context.Context) (string, error) {
			t, err := d.Providers[r.Provider].Transcript(ctx, r.Session, "")
			if err != nil {
				return "", err
			}
			if t.Session.ID != r.Session {
				return "", errors.New("会话不存在")
			}
			dir := strings.TrimSpace(t.Session.CWD)
			if dir == "" {
				dir = strings.TrimSpace(t.Session.ProjectDir)
			}
			if !filepath.IsAbs(dir) {
				return "", errors.New("此会话未提供可用的工作目录")
			}
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				return "", errors.New("此会话的工作目录已不存在或无法访问")
			}
			return dir, nil
		})
	case "terminal.attach":
		return d.Terminals.Attach(ctx, scope, p)
	case "terminal.read":
		return d.Terminals.Read(ctx, scope, p)
	case "terminal.input":
		return nil, d.Terminals.Input(ctx, scope, p)
	case "terminal.resize":
		return nil, d.Terminals.Resize(ctx, scope, p)
	case "terminal.detach":
		return nil, d.Terminals.Detach(ctx, scope, p)
	case "terminal.close":
		wait, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		err := d.Terminals.Terminate(wait, scope, p)
		if errors.Is(err, context.DeadlineExceeded) {
			err = terminal.ErrCloseUnconfirmed
		}
		return nil, err
	case "terminal.closeStatus":
		return nil, d.Terminals.CloseStatus(ctx, scope, p)
	default:
		return nil, errors.New("不支持的终端操作")
	}
}
