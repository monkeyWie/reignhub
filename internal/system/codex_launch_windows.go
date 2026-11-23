package system

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
	"golang.org/x/sys/windows"
)

const CodexSharedDesktop = true
const CodexRestartForceStops = true

// LaunchCodexDesktop uses the registered package's full-trust executable.
// Keep the override in the child environment. A shell activation could reuse
// an existing process and silently discard the override.
func LaunchCodexDesktop(ctx context.Context, app, endpoint string, expected []provider.Instance, restart bool) (int, error) {
	exe := AppExecutable(app)
	if exe == "" {
		return 0, errors.New("找不到 Codex App 的启动程序")
	}
	current, err := Discover(ctx, app)
	if err != nil {
		return 0, err
	}
	if !approvedDesktopInstances(expected, current) {
		return 0, errors.New("Codex App 的运行状态已变化，请刷新后重试")
	}
	if len(current) != 0 {
		if !restart {
			return 0, errors.New("请确认重启 Codex App")
		}
		if err := stopCodexDesktop(ctx, exe, current); err != nil {
			return 0, err
		}
		remaining, err := Discover(ctx, app)
		if err != nil {
			return 0, err
		}
		if len(remaining) != 0 {
			return 0, errors.New("检测到新的 Codex App 实例，请刷新后重试")
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = append(cmd.Environ(), "CODEX_APP_SERVER_WS_URL="+endpoint, "CODEX_APP_SERVER_FORCE_CLI=0")
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("启动 Codex App 失败: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// Closing windows leaves Codex in the tray. The Windows restart confirmation
// authorizes ending the main process. Open and validate every target before
// changing any of them, then use those same handles for termination and waiting.
func stopCodexDesktop(ctx context.Context, executable string, approved []provider.Instance) error {
	handles := make([]windows.Handle, 0, len(approved))
	defer func() {
		for _, handle := range handles {
			windows.CloseHandle(handle)
		}
	}()
	for _, instance := range approved {
		const access = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_TERMINATE | windows.SYNCHRONIZE
		handle, err := windows.OpenProcess(access, false, uint32(instance.PID))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			return fmt.Errorf("无法打开 Codex App 进程: %w", err)
		}
		handles = append(handles, handle)
		stamp, path, err := processStamp(handle)
		if errors.Is(err, errProcessExited) {
			continue
		}
		if err != nil {
			return fmt.Errorf("无法验证 Codex App 进程: %w", err)
		}
		if stamp != instance.StartedAt || !strings.EqualFold(filepath.Clean(path), executable) {
			return errors.New("Codex App 的运行状态已变化，请刷新后重试")
		}
	}

	for _, handle := range handles {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := windows.TerminateProcess(handle, 1); err != nil && !errors.Is(processProbeError(handle, err), errProcessExited) {
			return fmt.Errorf("无法结束 Codex App 进程: %w", err)
		}
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for _, handle := range handles {
		for {
			status, err := windows.WaitForSingleObject(handle, 0)
			if err != nil {
				return fmt.Errorf("无法确认 Codex App 已退出: %w", err)
			}
			if status == windows.WAIT_OBJECT_0 {
				break
			}
			select {
			case <-wait.Done():
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("等待 Codex App 退出超时，请稍后重试")
			case <-tick.C:
			}
		}
	}
	return nil
}
