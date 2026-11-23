//go:build darwin && !ios

package system

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
)

const DesktopSupported = true
const DesktopUnsupportedReason = ""

func FindAppContext(_ context.Context, configured string) (string, error) {
	return FindApp(configured), nil
}

func FindApp(configured string) string {
	home, _ := os.UserHomeDir()
	paths := []string{configured, "/Applications/Codex.app", "/Applications/ChatGPT.app", filepath.Join(home, "Applications/Codex.app"), filepath.Join(home, "Applications/ChatGPT.app")}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

func bundledCLIPaths(app string) []string {
	if app == "" {
		return nil
	}
	return []string{filepath.Join(app, "Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex"), filepath.Join(app, "Contents/Resources/codex")}
}

func AppExecutable(app string) string {
	// Current official distributions use one of these executable names.
	for _, name := range []string{"Codex", "ChatGPT"} {
		p := filepath.Join(app, "Contents/MacOS", name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func ConnectDesktop(ctx context.Context, app, endpoint string, expected []provider.Instance, confirm bool) error {
	if AppExecutable(app) == "" {
		return fmt.Errorf("未找到官方 Codex App，请在设置中指定 .app 路径")
	}
	current, err := Discover(ctx, app)
	if err != nil {
		return err
	}
	if len(current) > 1 {
		return fmt.Errorf("检测到多个官方 App 实例，请先保留一个实例再接入")
	}
	if !SameInstances(expected, current) {
		return fmt.Errorf("App 实例已变化，请刷新后重新确认")
	}
	if len(current) > 0 {
		if !confirm {
			return fmt.Errorf("接入需要重新打开当前 App；请确认任务已妥善处理")
		}
		if err := quitDesktop(ctx, app); err != nil {
			return err
		}
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
	wait:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return fmt.Errorf("App 仍在运行，已停止接入；不会强制结束进程")
			case <-ticker.C:
				now, err := Discover(ctx, app)
				if err != nil {
					return err
				}
				if len(now) == 0 {
					break wait
				}
				if !SameInstances(now, current) {
					return fmt.Errorf("用户启动了新的 App 实例，已停止接入")
				}
			}
		}
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/open", "-a", app, "--env", "CODEX_APP_SERVER_WS_URL="+endpoint, "--env", "CODEX_APP_SERVER_FORCE_CLI=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("启动 App：%s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Send a normal quit AppleEvent and honor refusal. Application specifiers take
// a path string; coercing it to a Finder alias produces error -1728 on macOS.
func quitDesktop(ctx context.Context, app string) error {
	script := `on run argv
 set appPath to item 1 of argv
 tell application appPath to quit
end run`
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script, app)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("App 未能正常退出，请手动退出后重新启用：%s (%w)", strings.TrimSpace(string(out)), err)
	}
	return nil
}
