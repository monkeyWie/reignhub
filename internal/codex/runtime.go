package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/monkeyWie/reignhub/internal/rpc"
	"github.com/monkeyWie/reignhub/internal/system"
)

func (p *Provider) startAppServer(ctx context.Context) error {
	p.connectMu.Lock()
	defer p.connectMu.Unlock()
	p.mu.Lock()
	b, c, shared := p.ipc, p.client, p.shared
	p.mu.Unlock()
	if c != nil && c.Alive() {
		if shared != nil {
			return nil
		}
		if b != nil {
			if ipc := b.client(); ipc != nil && ipc.Alive() {
				return nil
			}
		}
	}
	if shared != nil {
		return p.attachShared(ctx, shared)
	}
	var raw []byte
	if system.CodexSharedDesktop && p.cfg.DataDir != "" {
		var err error
		raw, err = readSharedEndpoint(ctx, p.cfg.DataDir)
		if err != nil {
			return err
		}
	}
	candidate := savedConnection(raw)
	restore := func() bool {
		if candidate == nil {
			return false
		}
		if candidate.waitDesktop(ctx) && p.attachShared(ctx, candidate) == nil {
			return true
		}
		candidate.http.CloseIdleConnections()
		return false
	}
	ipcFirst := candidate == nil || candidate.PreferIPC
	if !ipcFirst && restore() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := p.startIPC(ctx, b, c)
	usingIPC := err == nil
	if err != nil && (!ipcFirst || !restore()) {
		return err
	}
	if candidate != nil && usingIPC != candidate.PreferIPC {
		record := candidate.sharedEndpoint
		record.PreferIPC = usingIPC
		return saveSharedEndpoint(ctx, p.cfg.DataDir, raw, record)
	}
	return nil
}

func (p *Provider) attachShared(ctx context.Context, shared *sharedConnection) error {
	setup, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	observe, publish := p.runtimeObserver()
	client, err := shared.dial(setup, observe)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("共享 Codex 后端暂时无法连接")
	}
	p.useShared(shared, client, publish)
	return nil
}

func (p *Provider) startIPC(ctx context.Context, b *appIPC, c *rpc.Client) error {
	if b == nil {
		b = newAppIPC(p)
		p.mu.Lock()
		p.ipc = b
		p.mu.Unlock()
		attempt, stop := context.WithTimeout(ctx, 3*time.Second)
		err := b.connect(attempt)
		stop()
		go b.run()
		if err != nil {
			return appIPCConnectionError(err)
		}
	}
	ipc := b.client()
	if ipc == nil || !ipc.Alive() {
		if err := b.connectionError(); err != nil {
			return appIPCConnectionError(err)
		}
		return fmt.Errorf("Codex 连接尚未就绪，请稍后重试")
	}
	if c != nil && c.Alive() {
		return nil
	}
	app := system.FindApp(p.cfg.CodexApp)
	cli := system.FindCLI(p.cfg.CodexCLI, app)
	if cli == "" {
		return fmt.Errorf("未找到 Codex 可执行文件；请安装 Codex 或设置 codexCli")
	}
	if err := os.MkdirAll(p.cfg.DataDir, 0700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(p.cfg.DataDir, "codex.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.Command(cli, "-c", "features.code_mode_host=true", "app-server", "--listen", "stdio://")
	system.HideConsole(cmd)
	cmd.Stderr = logFile
	setup, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	observe, publish := p.runtimeObserver()
	c, err = rpc.StartProcess(setup, cmd, observe)
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("启动 Codex 私有后端失败: %w", err)
	}
	b.mu.Lock()
	b.owned = map[string]*ownedThread{}
	b.mu.Unlock()
	p.mu.Lock()
	p.client = c
	p.endpoint = "stdio"
	p.created = map[string]*rpc.Client{}
	p.composerSettings = nil
	p.lastError = ""
	p.mu.Unlock()
	publish(c)
	go func() {
		<-c.Done()
		_ = logFile.Close()
		p.clearRuntime(c)
	}()
	return nil
}

func appIPCConnectionError(err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		var diagnostic interface{ ConnectionGuidance() string }
		if errors.As(err, &diagnostic) {
			return fmt.Errorf("%s：%w", diagnostic.ConnectionGuidance(), err)
		}
		return fmt.Errorf("Codex 连接访问被拒绝，请确认 ReignHub 与 Codex App 由同一用户以相同权限级别运行：%w", err)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("Codex 连接服务尚未启动，请先打开 Codex：%w", err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("连接 Codex 超时，请稍后重试：%w", err)
	default:
		return fmt.Errorf("连接 Codex 失败：%w", err)
	}
}
