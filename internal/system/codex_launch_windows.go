package system

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/monkeyWie/reignhub/internal/provider"
	"golang.org/x/sys/windows"
)

var (
	user32      = windows.NewLazySystemDLL("user32.dll")
	enumWindows = user32.NewProc("EnumWindows")
	windowPID   = user32.NewProc("GetWindowThreadProcessId")
	postMessage = user32.NewProc("PostMessageW")
)

const CodexSharedDesktop = true

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
		if err := closeCodexWindows(ctx, app, current); err != nil {
			return 0, err
		}
	}
	remaining, err := Discover(ctx, app)
	if err != nil {
		return 0, err
	}
	if len(remaining) > 0 {
		return 0, errors.New("检测到新的 Codex App 实例，请刷新后重试")
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

func closeCodexWindows(ctx context.Context, app string, instances []provider.Instance) error {
	handles := make(map[uint32]windows.Handle, len(instances))
	defer func() {
		for _, handle := range handles {
			windows.CloseHandle(handle)
		}
	}()
	for _, instance := range instances {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(instance.PID))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			return fmt.Errorf("无法确认 Codex App 进程: %w", err)
		}
		handles[uint32(instance.PID)] = handle
		stamp, executable, err := processStamp(handle)
		if errors.Is(err, errProcessExited) {
			continue
		}
		if err != nil || stamp != instance.StartedAt || !strings.EqualFold(filepath.Clean(executable), AppExecutable(app)) {
			return errors.New("Codex App 的运行状态已变化，请刷新后重试")
		}
	}
	state := &codexCloseState{handles: handles}
	codexCloseMu.Lock()
	activeCodexClose = state
	ok, _, err := enumWindows.Call(codexCloseCallback, 0)
	activeCodexClose = nil
	codexCloseMu.Unlock()
	if ok == 0 {
		return fmt.Errorf("无法枚举 Codex App 窗口: %w", err)
	}
	if state.err != nil {
		return fmt.Errorf("无法请求 Codex App 正常退出: %w", state.err)
	}

	wait, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-wait.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for pid, handle := range handles {
				if status, _ := windows.WaitForSingleObject(handle, 0); status == uint32(windows.WAIT_TIMEOUT) {
					return fmt.Errorf("Codex 主进程（PID %d）仍在后台运行，请从应用菜单或托盘退出 Codex 后重试", pid)
				}
			}
			return nil
		case <-ticker.C:
			exited := true
			for _, handle := range handles {
				status, err := windows.WaitForSingleObject(handle, 0)
				if err != nil {
					return fmt.Errorf("无法确认 Codex App 是否已退出: %w", err)
				}
				exited = exited && status == windows.WAIT_OBJECT_0
			}
			if exited {
				return nil
			}
		}
	}
}

type codexCloseState struct {
	handles map[uint32]windows.Handle
	err     error
}

// EnumWindows invokes the callback synchronously. Reuse one callback for
// every restart because Go retains Windows callback registrations for life.
var (
	codexCloseMu     sync.Mutex
	activeCodexClose *codexCloseState
)

var codexCloseCallback = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
	const wmClose = 0x0010
	s := activeCodexClose
	var pid uint32
	thread, _, _ := windowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if handle, ok := s.handles[pid]; thread != 0 && ok {
		if alive, _ := windows.WaitForSingleObject(handle, 0); alive == uint32(windows.WAIT_TIMEOUT) {
			if ok, _, err := postMessage.Call(hwnd, wmClose, 0, 0); ok == 0 {
				s.err = err
			}
		}
	}
	return 1
})
