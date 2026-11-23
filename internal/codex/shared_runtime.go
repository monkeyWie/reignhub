package codex

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
	"github.com/monkeyWie/reignhub/internal/system"
)

func (p *Provider) ConnectDesktop(ctx context.Context, expected []provider.Instance, restart bool) error {
	if !system.CodexSharedDesktop {
		return p.Start(ctx)
	}
	// Healthy IPC keeps priority. Only a confirmed launch request may replace
	// the desktop process after the IPC setup attempt fails.
	if err := p.Start(ctx); err == nil {
		p.mu.Lock()
		shared, bridge := p.shared, p.ipc
		p.mu.Unlock()
		if (shared == nil && (bridge == nil || !bridge.needsSharedDesktop())) || (shared != nil && shared.connected() && !restart) {
			return nil
		}
	}
	if !p.connectMu.TryLock() {
		return errors.New("正在连接 Codex，请稍后重试")
	}
	defer p.connectMu.Unlock()
	p.mu.Lock()
	bridge, shared := p.ipc, p.shared
	p.mu.Unlock()
	if shared == nil && bridge != nil && !bridge.needsSharedDesktop() {
		if ipc := bridge.client(); ipc != nil && ipc.Alive() {
			return errors.New("Codex IPC 已恢复，请稍后重试")
		}
		if bridge.connectionError() == nil {
			return provider.ErrUnavailable
		}
	}
	setup, cancel := context.WithTimeout(ctx, 26*time.Second)
	defer cancel()
	app, err := system.FindAppContext(setup, p.cfg.CodexApp)
	if err != nil {
		return err
	}
	if app == "" {
		return errors.New("请先安装 Codex App")
	}
	var publish func(*rpc.Client)
	fresh := shared == nil || shared.ctx.Err() != nil
	if fresh {
		cli := system.FindCLI(p.cfg.CodexCLI, app)
		if cli == "" {
			return errors.New("未找到 Codex 可执行文件")
		}
		observe, setClient := p.runtimeObserver()
		publish = setClient
		backend, stop := context.WithTimeout(setup, 7*time.Second)
		shared, err = startSharedServer(backend, cli, observe)
		stop()
		if err != nil {
			return err
		}
	}
	attached := false
	desktop := &sharedDesktop{path: "/" + rand.Text()}
	previous := shared.desktop.Swap(desktop)
	defer func() {
		if !attached {
			shared.desktop.Store(previous)
			if fresh {
				shared.close()
			}
		}
	}()
	if _, err := system.LaunchCodexDesktop(setup, app, shared.address+desktop.path, expected, restart); err != nil {
		return err
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-setup.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("Codex App 未完成共享后端连接，请确认此版本支持 WebSocket 启动")
		case <-shared.ctx.Done():
			return errors.New("共享 Codex 后端在 App 连接前已停止")
		case <-tick.C:
			if desktop.ready.Load() > 0 {
				if fresh {
					p.useShared(shared, shared.client, publish)
				}
				attached = true
				return nil
			}
		}
	}
}

// An old executor can finish work it already accepted. It cannot publish
// events into, or receive another write from, the new provider connection.
func (p *Provider) useShared(shared *sharedServer, client *rpc.Client, publish func(*rpc.Client)) {
	p.mu.Lock()
	bridge := p.ipc
	p.mu.Unlock()
	if bridge != nil {
		bridge.close()
	}
	p.observeMu.Lock()
	p.mu.Lock()
	old := p.client
	if bridge != nil {
		p.retired = old
	}
	p.ipc, p.client, p.shared = nil, client, shared
	p.created = map[string]*rpc.Client{}
	p.endpoint, p.lastError = "websocket", ""
	p.mu.Unlock()
	p.clearRoute("service")
	publish(client)
	p.observeMu.Unlock()
	go func() {
		<-client.Done()
		p.clearRuntime(client)
	}()
}

func (p *Provider) runtimeObserver() (rpc.Observer, func(*rpc.Client)) {
	var bound atomic.Pointer[rpc.Client]
	return func(m rpc.Message, reply func(context.Context, any) error) {
		p.observeMu.Lock()
		defer p.observeMu.Unlock()
		p.mu.Lock()
		client := bound.Load()
		current := client != nil && client == p.client
		p.mu.Unlock()
		if current {
			p.observe(m, reply)
		}
	}, bound.Store
}

func (p *Provider) clearRuntime(client *rpc.Client) {
	p.observeMu.Lock()
	defer p.observeMu.Unlock()
	p.mu.Lock()
	current := p.client == client
	p.mu.Unlock()
	if current {
		p.clearRoute("service")
	}
}

type sharedWriteOutcome struct{ error }

func (*sharedWriteOutcome) OutcomeUnknown() bool { return true }

func sharedWriteError(method string, err error) error {
	var rejection *rpc.Error
	if err == nil || errors.As(err, &rejection) {
		return err
	}
	return &sharedWriteOutcome{fmt.Errorf("%s 的结果待确认，请检查 Codex 中的执行状态: %w", method, err)}
}

func (p *Provider) threadWrite(ctx context.Context, c *rpc.Client, method string, params, result any) error {
	p.mu.Lock()
	shared := p.shared != nil && p.client == c
	p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	err := c.Call(ctx, method, params, result)
	if shared {
		return sharedWriteError(method, err)
	}
	return err
}

func (p *Provider) activeShared() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shared != nil
}

// The shared server has its own per-connection subscription. Follow loaded
// threads on that server without opening a writer for archived history.
func (p *Provider) followShared(ctx context.Context, id string) error {
	if p.isCreatedThread(id) {
		return nil
	}
	var result struct{ Thread thread }
	if err := p.call(ctx, "thread/read", map[string]any{"threadId": id}, &result); err != nil {
		return err
	}
	switch result.Thread.Status.Type {
	case "active", "idle", "systemError":
		_, err := p.threadClient(ctx, id)
		return err
	default:
		return nil
	}
}
